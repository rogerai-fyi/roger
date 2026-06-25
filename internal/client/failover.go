package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"time"
)

// Criteria are the user's routing constraints - the same knobs honored by the
// broker's matcher. Failover re-selection MUST respect every one of these so an
// alternative provider is never a downgrade the user didn't ask for.
type Criteria struct {
	Model        string
	Confidential bool
	MinTPS       float64 // require measured tok/s >= this (0 = no floor)
	MaxPriceIn   float64 // skip offers whose input price exceeds this (0 = no cap)
	MaxPriceOut  float64 // skip offers whose output price exceeds this (0 = no cap)
	// Pref is the user-preference knob ("cheap"/"balanced"/"fast"/"reliable"; empty =
	// balanced). It reshapes the composite SCORE (the bounded price modifier strength),
	// never the hard filters - mirroring the broker so failover and normal routing agree.
	Pref string
}

// Offer is one discoverable provider offer (a subset of the broker's /discover
// view, just the fields selection needs).
type Offer struct {
	NodeID       string  `json:"node_id"`
	Model        string  `json:"model"`
	PriceIn      float64 `json:"price_in"`
	PriceOut     float64 `json:"price_out"`
	Online       bool    `json:"online"`
	Confidential bool    `json:"confidential"`
	TPS          float64 `json:"tps"`
	TTFTMs       float64 `json:"ttft_ms"`  // probe-measured TTFT (ms; 0 = unmeasured)
	Verified     bool    `json:"verified"` // a recent PASSED canary (probe-verified serving)
	// Signal is the broker's 0..100 channel-health composite for this offer
	// (speed + latency + verified-serving + reliability + trust). It carries even
	// when TPS==0, so it is the alignment key failover ranks on - the SAME composite
	// the broker's pick uses, so normal + failover routing agree on "best".
	Signal int `json:"signal"`
	// Smart-router v2 selection fields surfaced from /discover so failover mirrors the
	// broker's capacity-aware load factor (0 = unset, treated as neutral). InFlight is
	// the node's current load; Capacity is its concurrency capacity (under-load TPS or
	// hw-class prior); Radius is the broker's UCB exploration lift (0..1, scaled).
	InFlight int     `json:"in_flight"`
	Capacity int     `json:"capacity"`
	Radius   float64 `json:"radius"`
}

// failoverPolicy bounds the auto-recovery loop. Defaults are conservative so a
// flapping broker can't turn one client request into a retry storm.
type failoverPolicy struct {
	maxAttempts int           // total tries (initial + retries)
	baseBackoff time.Duration // first backoff; doubles each retry (capped)
	maxBackoff  time.Duration
}

func defaultPolicy() failoverPolicy {
	return failoverPolicy{maxAttempts: 4, baseBackoff: 200 * time.Millisecond, maxBackoff: 2 * time.Second}
}

// retryable reports whether a relay outcome warrants failing over to another
// provider. Transport errors (timeout / connection drop) and broker/node 5xx
// (incl. 502/503/504) are retryable; a 4xx is the caller's fault (bad request,
// no credits) and is surfaced immediately. statusCode<=0 means a transport error.
func retryable(statusCode int, err error) bool {
	if err != nil {
		return true
	}
	return statusCode >= 500
}

// backoff returns the delay before attempt n (0-based: attempt 0 has no prior
// delay; this is called for attempt n>=1), exponential with a cap.
func (p failoverPolicy) backoff(attempt int) time.Duration {
	d := p.baseBackoff
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= p.maxBackoff {
			return p.maxBackoff
		}
	}
	return d
}

// selectAlternative re-queries /discover and returns the best online offer that
// still satisfies the criteria, skipping any node in `exclude` (the providers
// that just failed). "Best" = highest measured tok/s among eligible, tie-broken
// by lowest input price - we want the failover target to be both fast and cheap.
// Returns ("", false) when nothing eligible remains.
func selectAlternative(broker string, c Criteria, exclude map[string]bool) (string, bool) {
	offers, err := discover(broker)
	if err != nil {
		return "", false
	}
	return pickAlternative(offers, c, exclude)
}

// PickBest returns the best online offer of `model` by the SAME composite ranking
// the failover path uses (value-per-credit, then load/price tie-break). Exported so
// other packages (and cross-package tests) can confirm the client and broker
// selectors converge on the same "best" offer.
func PickBest(offers []Offer, model string) (string, bool) {
	return pickAlternative(offers, Criteria{Model: model}, nil)
}

// pickAlternative is the pure selection step (no I/O) so it is unit-testable.
func pickAlternative(offers []Offer, c Criteria, exclude map[string]bool) (string, bool) {
	var eligible []Offer
	for _, o := range offers {
		if !o.Online || o.Model != c.Model {
			continue
		}
		if exclude[o.NodeID] {
			continue
		}
		if c.Confidential && !o.Confidential {
			continue
		}
		if c.MaxPriceIn > 0 && o.PriceIn > c.MaxPriceIn {
			continue
		}
		if c.MaxPriceOut > 0 && o.PriceOut > c.MaxPriceOut {
			continue
		}
		// Only exclude nodes MEASURED as too slow; unmeasured (tps==0) get a
		// chance so new providers aren't permanently passed over (mirrors broker).
		if c.MinTPS > 0 && o.TPS > 0 && o.TPS < c.MinTPS {
			continue
		}
		eligible = append(eligible, o)
	}
	if len(eligible) == 0 {
		return "", false
	}
	// Smart-router v2 composite (mirrors the broker's pick): score each eligible offer
	// on the SAME shape - ucb( base * priceMod ) * loadFactor - where base is the
	// broker's per-offer Signal (which already folds reliability + speedFit + trust),
	// priceMod is a BOUNDED modifier within the eligible offer set's own out-price range
	// (NOT a divisor; free = neutral), and loadFactor is capacity-normalized congestion.
	// This is the failover<->broker alignment contract (PickBest == broker pick).
	rangeMin, rangeMax := offerOutRange(eligible, c.MaxPriceOut)
	w := prefWeights(c.Pref)
	scored := make([]scoredOffer, len(eligible))
	for i, o := range eligible {
		base := float64(o.Signal) / 100.0 // 0..1 reliability+speed composite
		pm := boundedPriceMod(offerEffPrice(o), rangeMin, rangeMax, w.kPrice, w.priceExp)
		s := clamp01(base*pm+o.Radius) * offerLoadFactor(o.InFlight, o.Capacity)
		scored[i] = scoredOffer{o: o, score: s}
	}
	sort.SliceStable(scored, func(i, j int) bool {
		return offerLess(scored[i], scored[j])
	})
	return scored[0].o.NodeID, true
}

// scoredOffer pairs an eligible offer with its v2 composite score for ranking.
type scoredOffer struct {
	o     Offer
	score float64
}

// prefW holds the client-side knob anchors (mirrors the broker's prefWeights for the
// terms the client can compute from /discover: the bounded-price-mod strength).
type prefW struct {
	kPrice   float64
	priceExp float64
}

// prefWeights maps the X-Roger-Pref string to the client knob anchors (default
// balanced). It mirrors the broker's table for the price-modifier terms.
func prefWeights(p string) prefW {
	switch p {
	case "cheap":
		return prefW{kPrice: 0.45, priceExp: 0.5}
	case "fast":
		return prefW{kPrice: 0.10, priceExp: 1.5}
	case "reliable":
		return prefW{kPrice: 0.20, priceExp: 0.8}
	default:
		return prefW{kPrice: 0.25, priceExp: 1.0}
	}
}

// offerOutRange is the cheapest/dearest eligible OUTPUT price (the user's effective
// range for the bounded price modifier). maxOut, when set, widens the ceiling so a
// user who gave only a cap still gets a sane window. Free offers don't move the bounds.
func offerOutRange(offers []Offer, maxOut float64) (min, max float64) {
	have := false
	for _, o := range offers {
		p := offerEffPrice(o)
		if p <= 0 {
			continue
		}
		if !have || p < min {
			min = p
		}
		if !have || p > max {
			max = p
		}
		have = true
	}
	if maxOut > 0 && maxOut > max {
		max = maxOut
	}
	return min, max
}

// boundedPriceMod is the BOUNDED soft price modifier within the user's range (mirrors
// the broker's priceMod): 1 - kPrice*norm^priceExp. NOT a divisor; free = neutral 1.0.
func boundedPriceMod(out, rangeMin, rangeMax, kPrice, priceExp float64) float64 {
	if out <= 0 {
		return 1.0
	}
	span := rangeMax - rangeMin
	if span <= 0 {
		return 1.0
	}
	norm := clamp01((out - rangeMin) / span)
	return clamp01(1 - kPrice*math.Pow(norm, priceExp))
}

// offerLoadFactor is the capacity-normalized congestion discount (mirrors the
// broker's loadFactor). An unset capacity (0) defaults to 1 slot (conservative).
func offerLoadFactor(inflight, capacity int) float64 {
	if capacity < 1 {
		capacity = 1
	}
	return 1.0 / (1.0 + float64(inflight)/float64(capacity))
}

// offerEffPrice is the OUTPUT price (what the broker bills + quotes most on), falling
// back to the input price when out is unset.
func offerEffPrice(o Offer) float64 {
	if o.PriceOut > 0 {
		return o.PriceOut
	}
	return o.PriceIn
}

// offerLess orders scored offers best-first: higher v2 composite wins; ties (within
// ~2%) break to the faster node, then the cheaper price - mirroring the broker's
// load/price tie-break so the two selectors converge on the same pick.
func offerLess(a, b scoredOffer) bool {
	hi := a.score
	if b.score > hi {
		hi = b.score
	}
	if hi > 0 {
		d := a.score - b.score
		if d < 0 {
			d = -d
		}
		if d/hi > 0.02 {
			return a.score > b.score
		}
	}
	if a.o.TPS != b.o.TPS {
		return a.o.TPS > b.o.TPS // faster first
	}
	return offerEffPrice(a.o) < offerEffPrice(b.o) // then cheaper
}

// clamp01 clamps x to [0,1].
func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

// BandRange is the live cross-station OUTPUT-price spread for one model: min/max
// of the active out-price across the online stations serving that band, plus the
// cheapest station and how many are on air. It answers "if I tune this band this
// second, what could I pay?" - the headline range the pricing UX shows. Single
// station => Min==Max, Stations==1 (no spread; do not fake one).
type BandRange struct {
	Model     string
	Min, Max  float64 // $/1M out across online stations
	Stations  int     // online stations serving this band
	CheapNode string  // node id at Min (the broker's default route)
	CheapTPS  float64 // that node's measured tok/s (0 = unmeasured)
	CheapIn   float64 // that node's input price (shown in connect detail)
}

// bandRange computes the cross-station out-price range for `model` from a set of
// offers (pure, so it is unit-testable). Only online offers of the exact model
// count. ok=false when no station serves the band right now.
func bandRange(offers []Offer, model string) (BandRange, bool) {
	br := BandRange{Model: model}
	for _, o := range offers {
		if !o.Online || o.Model != model {
			continue
		}
		if br.Stations == 0 || o.PriceOut < br.Min {
			br.Min = o.PriceOut
			br.CheapNode = o.NodeID
			br.CheapTPS = o.TPS
			br.CheapIn = o.PriceIn
		}
		if br.Stations == 0 || o.PriceOut > br.Max {
			br.Max = o.PriceOut
		}
		br.Stations++
	}
	return br, br.Stations > 0
}

// BandRangeFor fetches /discover and returns the live cross-station out-price
// range for `model` (the headline range the connect screens show).
func BandRangeFor(broker, model string) (BandRange, bool) {
	offers, err := discover(broker)
	if err != nil {
		return BandRange{Model: model}, false
	}
	return bandRange(offers, model)
}

// estReplyCost is the credits one typical reply costs at out-price `priceOut`,
// given `outTokens` output tokens (default ~800). Input cost is negligible for
// the headline estimate; we bill primarily on output.
func estReplyCost(priceOut float64, outTokens int) float64 {
	if outTokens <= 0 {
		outTokens = 800
	}
	return priceOut * float64(outTokens) / 1e6
}

// MarketMedianOut returns the median active OUTPUT price across the online public
// stations serving `model`, for the operator soft price-warn (a price far above the
// median is likely a typo). It reads /discover (public). ok=false when there is no
// public station for the model (nothing to compare against). Best-effort: a fetch
// error returns ok=false (the warn is non-blocking, never fatal to sharing).
func MarketMedianOut(broker, model string) (float64, bool) {
	offers, err := discover(broker)
	if err != nil {
		return 0, false
	}
	var outs []float64
	for _, o := range offers {
		if o.Online && o.Model == model {
			outs = append(outs, o.PriceOut)
		}
	}
	if len(outs) == 0 {
		return 0, false
	}
	sort.Float64s(outs)
	n := len(outs)
	if n%2 == 1 {
		return outs[n/2], true
	}
	return (outs[n/2-1] + outs[n/2]) / 2, true
}

// ResolveBand resolves a private band frequency code against the broker's public
// POST /bands/resolve (no login). It returns the band's live offers for `model` (or
// all of them when model==""). ok=false on the broker's uniform "no station on that
// frequency" reply - which is IDENTICAL for a wrong code, a revoked/expired band, OR
// a valid band whose station is off air (no enumeration oracle). The display string
// (cosmetic "147.520 MHz · ...") is returned for the connect screen when present.
func ResolveBand(broker, freq, model string) (offers []Offer, display string, ok bool) {
	body, _ := json.Marshal(map[string]string{"freq": freq})
	resp, err := http.Post(broker+"/bands/resolve", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, "", false
	}
	defer resp.Body.Close()
	var d struct {
		Offers []Offer `json:"offers"`
		Band   struct {
			Display string `json:"display"`
		} `json:"band"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&d)
	// The broker returns 404 {"offers":[]} uniformly for every negative case. Treat an
	// empty offer list as "no station" regardless of status, so the client never leaks
	// a wrong-vs-offline distinction either.
	if resp.StatusCode != http.StatusOK || len(d.Offers) == 0 {
		return nil, "", false
	}
	if model != "" {
		var filtered []Offer
		for _, o := range d.Offers {
			if o.Model == model {
				filtered = append(filtered, o)
			}
		}
		if len(filtered) == 0 {
			return nil, "", false
		}
		d.Offers = filtered
	}
	return d.Offers, d.Band.Display, true
}

// discover fetches the current offer list from the broker.
func discover(broker string) ([]Offer, error) {
	resp, err := http.Get(broker + "/discover")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discover: status %d", resp.StatusCode)
	}
	var d struct {
		Offers []Offer `json:"offers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, err
	}
	return d.Offers, nil
}
