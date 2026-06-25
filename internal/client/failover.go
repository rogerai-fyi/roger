package client

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	// Signal is the broker's 0..100 channel-health score for this offer (online +
	// quality + tps + reliability). It carries even when TPS==0, so the meter shows
	// a freshly-on-air node's baseline strength instead of a blank tps-driven bar.
	Signal int `json:"signal"`
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
	sort.SliceStable(eligible, func(i, j int) bool {
		if eligible[i].TPS != eligible[j].TPS {
			return eligible[i].TPS > eligible[j].TPS // faster first
		}
		return eligible[i].PriceIn < eligible[j].PriceIn // then cheaper
	})
	return eligible[0].NodeID, true
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
