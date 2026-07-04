package main

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// normalizedMarketQuery builds the STABLE cache key suffix for the PUBLIC market
// views from the request's filter params. It reads only the KNOWN filter keys
// (model / confidential / freq) - never the whole raw query - so unrelated or
// cache-busting params can't fragment (or poison) the shared cache, and two
// equivalent requests map to one entry. Values are lowercased + the parts joined in
// a fixed order so "?model=x&confidential=1" and "?confidential=1&model=x" key alike.
// /discover + /market do not filter today, so this is normally "" (one shared entry);
// it is here so any future filter is correctly keyed from day one.
func normalizedMarketQuery(r *http.Request) string {
	q := r.URL.Query()
	model := strings.ToLower(strings.TrimSpace(q.Get("model")))
	conf := strings.ToLower(strings.TrimSpace(q.Get("confidential")))
	freq := strings.ToLower(strings.TrimSpace(q.Get("freq")))
	return "m=" + model + "|c=" + conf + "|f=" + freq
}

type offerView struct {
	NodeID string `json:"node_id"`
	Region string `json:"region"`
	HW     string `json:"hw"`
	Model  string `json:"model"`
	// Modality is what the offer DOES: "chat" (the back-compat default), "tts" (speak), or
	// "stt" (listen). Carried on the public feed so the consumer's client + TUI can tell a
	// VOICE station apart from a chat station and never (wrongly) offer a voice band as a chat
	// channel (the "504 no station is serving <voice>" bug). Always canonical (offerModality):
	// a pre-voice offer's empty modality is normalized to "chat", never a bare "".
	Modality string `json:"modality,omitempty"`
	// Capabilities are the offer's chat sub-capabilities (["vision"] = accepts images); absent =
	// text-only or undetermined. omitempty: the app treats "vision"->show photo button, absent->
	// name-heuristic (it handles all three of vision/[]/absent identically for non-vision models).
	Capabilities []string `json:"capabilities,omitempty"`
	In           float64  `json:"price_in"`  // active (time-of-use) price right now
	Out          float64  `json:"price_out"` // active price right now
	// PriceTier is the neutral buyer-facing $-tier: 0 = FREE/unknown, 1..4 = $..$$$$,
	// graded vs the same-model external reference (preferred) or the live per-model
	// median. Computed server-side (assignPriceTiers) so every surface renders alike.
	PriceTier    int     `json:"price_tier"`
	Ctx          int     `json:"ctx"`
	CtxEstimated bool    `json:"ctx_estimated"` // Ctx is the estimated default, not a detected window
	Online       bool    `json:"online"`
	Confidential bool    `json:"confidential"`
	FreeNow      bool    `json:"free_now"`
	Scheduled    bool    `json:"scheduled"`
	TPS          float64 `json:"tps"`     // measured output tokens/sec (0 = not yet measured)
	TTFTMs       float64 `json:"ttft_ms"` // probe-measured time-to-first-token (ms; 0 = unmeasured)
	Quality      float64 `json:"quality"` // 0..1 broker-measured trust/verification signal
	SuccessRate  float64 `json:"success"` // 0..1 time-decayed success evidence (organic or probe)
	// SuccessSeen distinguishes a REAL measured/probe-positive success rate from the
	// neutral no-evidence fallback: false means "no data yet" (the UI shows "no data",
	// never a fabricated %); true means SuccessRate is real (organic EWMA or probe-OK).
	SuccessSeen bool `json:"success_seen"`
	Verified    bool `json:"verified"` // node has a recent PASSED canary (probe-verified serving)
	// Signal is the SAME 0..100 health score /market exposes per model, computed
	// here per OFFER (providers=1) so the band list has a meter to show even when
	// the node has zero traffic yet: an online node still scores its baseline from
	// supply + verified-serving + trust (no tps required). Offline offers score 0.
	Signal int `json:"signal"`
	// Terms is the per-factor breakdown (supply/speed/latency/verified/success/trust
	// + the congestion discount) so the UI can explain the number.
	Terms signalTerms `json:"terms"`
	// Smart-router v2 selection fields, surfaced so the client's failover ranking can
	// mirror the broker's capacity-aware load factor + UCB exploration lift (the
	// failover<->broker alignment contract). InFlight is current load; Capacity is the
	// node's concurrency capacity (under-load TPS, else hw-class prior); Radius is the
	// UCB exploration lift (0..1).
	InFlight int     `json:"in_flight"`
	Capacity int     `json:"capacity"`
	Radius   float64 `json:"radius"`
}

// enrichOffersForNode builds the fully-enriched offerView list for ONE node, with
// the SAME multi-factor signal/terms/success/verified/ctx/in-flight machinery the
// public /discover path uses, so a private band carries identical real metrics. It
// is the single source of the per-offer enrichment math (no duplication): both
// computeDiscover and the private-band bandOffers path call it.
//
// CONTRACT: the caller MUST already hold b.mu (node map is read here). This function
// acquires b.metricsMu itself for the per-node metric reads. The optional model
// filter `deny` (band's allow-list; nil for the public path) drops offers the band
// does not permit. When probeOnBrowse is true and probing is enabled, a stale online
// node is scheduled for a near-term demand probe (async; this read uses current data)
// - the public path opts in; the band resolve/relay liveness probe opts out so it
// stays a cheap read. The returned slice is appended to `out`.
func (b *broker) enrichOffersForNode(out []offerView, n protocol.NodeRegistration, now time.Time, deny func(string) bool, probeOnBrowse bool) []offerView {
	age := time.Since(b.lastSeen[n.NodeID])
	live := age < nodeTTL // heartbeat-fresh (drives recovery probing below)
	// The probe-dead veto below is only AUTHORITATIVE on the instance that hosts this node's
	// live poll (a recent local /agent/poll, stamped in agentPoll) - the one that can actually
	// probe it. In single-instance mode (no shared store) the poll is always local, so the veto
	// always applies (behavior unchanged). A MULTI-INSTANCE PEER that merely mirrors the node
	// via the shared registry/liveness must NOT probe-kill it with its own non-authoritative
	// streak: a cross-instance probe can time out or never land, so probeFails climbs on the
	// non-host and a node heartbeating on the host flickers OFFLINE here - the residual
	// /discover flicker the registry union (task #52) did NOT fix. b.mu is held by the caller,
	// so b.localPollAt is safe to read. Pinned by features/multinode/discover_liveness.feature.
	authoritative := b.shared == nil ||
		(!b.localPollAt[n.NodeID].IsZero() && now.Sub(b.localPollAt[n.NodeID]) < nodeTTL)
	b.metricsMu.Lock()
	tq := b.trust[n.NodeID]
	// A node that heartbeats but has failed a SUSTAINED streak of liveness probes is not
	// actually serving its model (dead/unloaded upstream) - surface it as OFFLINE so a
	// consumer never tunes into a dead channel and eats repeated 504s. It still heartbeats,
	// so the proberLoop keeps probing it (gated on `live` below); one OK probe resets the
	// streak and it flips back online. See probeDeadStreak. The veto is applied only by the
	// node's authoritative poll host (see above); a peer keeps it online on heartbeat liveness.
	online := live && (!authoritative || tq.probeFails < probeDeadStreak)
	tps := b.tps[n.NodeID]
	inflight := b.inflight[n.NodeID]
	sr, srSeen := b.success[n.NodeID]
	quality := tq.score()
	ttft := tq.ttftMs
	verified := tq.verifiedServing()
	staleness := b.measurementStalenessLocked(n.NodeID, now)
	capacity := capacityOf(b.concurrentTPS[n.NodeID], n.HW)
	radius := 0.0
	if tq.probed && tq.probeOK {
		radius = ucbRadius(prefBalanced.weights().c, b.totalReqs.Load(), tq.recounts, tq.probes, b.successCount[n.NodeID])
	}
	if probeOnBrowse && live && b.probe.enabled() && staleness < 1.0 {
		b.demandProbeSoonLocked(n.NodeID, now) // probe even a probe-dead node so it can recover
	}
	b.metricsMu.Unlock()

	recency := recencyOf(age)
	successRate := successFor(sr, srSeen, verified)
	terms := signalTerms{}
	if online {
		terms = computeSignal(signalInput{
			providers: 1, inflight: inflight, bestTPS: tps, ttftMs: ttft,
			successRate: successRate, trust: quality, recency: recency, verified: verified,
			staleness: staleness,
		})
	}
	successSeen := srSeen || verified
	for _, o := range n.Offers {
		if deny != nil && deny(o.Model) {
			continue
		}
		pin, pout, free, _ := o.ActivePrice(now)
		out = append(out, offerView{
			NodeID: n.NodeID, Region: n.Region, HW: n.HW, Model: o.Model, Modality: offerModality(o.Modality),
			Capabilities: protocol.CanonicalCapabilities(o.Capabilities), // canonicalized at read, never raw wire
			In:           pin, Out: pout, Ctx: o.Ctx, CtxEstimated: o.CtxEstimated, Online: online,
			Confidential: b.confidential[n.NodeID], FreeNow: free, Scheduled: len(o.Schedule) > 0,
			TPS:    tps,
			TTFTMs: ttft, Quality: quality,
			SuccessRate: round6(successRate), SuccessSeen: successSeen, Verified: verified,
			Signal:   terms.Total,
			Terms:    terms,
			InFlight: inflight, Capacity: capacity, Radius: round6(radius),
		})
	}
	return out
}

// discover handles GET /discover: all model offers with live status, measured
// throughput, and active (time-of-use) price, cheapest-now first.
func (b *broker) discover(w http.ResponseWriter, r *http.Request) {
	if corsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodGet) {
		return
	}
	// Per-IP rate limit on this UNAUTHENTICATED public surface (keyed on the validated
	// CF-Connecting-IP, see clientIP). /discover is no-auth and enumerates the whole
	// market, so it gets the same per-IP discipline as the concierge and the anon relay
	// to keep a single source from hammering it.
	if ok, retry := b.anonRL.allow(clientIP(r)); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		jsonErr(w, http.StatusTooManyRequests, "rate limit exceeded - slow down")
		return
	}
	cors(w) // public market data - let the website (rogerai.fyi) fetch it

	// Hot-path cache (flag-gated, behind ROGERAI_REDIS_URL). /discover recomputes every
	// offer + its multi-factor signal per request; this collapses repeated full-market
	// recomputes into one within a tiny window, shared across instances. PUBLIC, no-auth
	// data, so a single cache entry is safely shared across all callers - keyed by the
	// normalized query so a future filtered view never reuses another's bytes. Flag OFF
	// => serveCachedJSON computes directly (zero behavior change). Note: on a cache HIT
	// the demand-probe scheduling below is skipped, but a miss recomputes every ~few
	// seconds (the TTL), so demand probing still fires steadily under browsing load.
	b.serveCachedJSON(w, "discover:"+normalizedMarketQuery(r), publicMarketTTL, b.computeDiscover)
}

// computeDiscover builds the /discover payload (all model offers with live status,
// measured throughput, and active price, cheapest-now first). It is a READ of broker
// state (no money/ledger mutation), so its serialized result is safe to cache for a
// short window. The only side effect is demand-probe scheduling, which is a best-effort
// hint and still fires on every cache miss.
func (b *broker) computeDiscover() any {
	b.mu.Lock()
	now := time.Now()
	var out []offerView
	for _, n := range b.nodes {
		// Ejected/banned nodes are removed from the public market view too (not just
		// pick), so a reported node disappears from /discover.
		if b.isBanned(n.NodeID) {
			continue
		}
		// Private bands are HIDDEN from the public market: a freq-code node is only
		// reachable via /bands/resolve, never enumerable here.
		if b.private[n.NodeID] {
			continue
		}
		// Per-offer enrichment (signal/terms/success/verified/ctx/in-flight + the
		// smart-router selection fields) is the SAME machinery a private band uses; it
		// lives in the shared enrichOffersForNode (b.mu held here, deny=nil for the public
		// path, demand-probe scheduling on while browsing).
		out = b.enrichOffersForNode(out, n, now, nil, true)
	}
	b.mu.Unlock()
	// Classify each offer's neutral $-tier (external reference preferred, else the live
	// per-model median over this set) before returning, so /discover carries it.
	b.assignPriceTiers(out)
	sort.Slice(out, func(i, j int) bool { return out[i].In < out[j].In })
	return map[string]any{"offers": out}
}

// marketView is the per-model market summary surfaced by GET /market.
type marketView struct {
	Model string `json:"model"`
	// Modality mirrors offerView's canonical modality ("chat"/"tts"/"stt", always
	// offerModality-normalized - a pre-voice empty modality reads "chat", never a bare "") so
	// the aggregated market row can never present a VOICE station (tts/stt) as a usable CHAT
	// model in a client picker. A model's offers share one modality; the first seen sets it.
	Modality string `json:"modality,omitempty"`
	// Capabilities is the UNION across this model's on-air providers: a model is vision-capable
	// if it can be ROUTED to any provider that reports vision. ["vision"] if any provider does,
	// omitted otherwise (the app name-guesses for a model with no declared vision provider).
	Capabilities []string `json:"capabilities,omitempty"`
	Providers    int      `json:"providers"`    // online nodes offering this model
	InFlight     int      `json:"in_flight"`    // active requests across those nodes
	MinPrice     float64  `json:"min_price"`    // cheapest active input price (credits/1M)
	PriceTier    int      `json:"price_tier"`   // 0..4 neutral $-tier for the model's BEST (cheapest) active out-price (0 = FREE/unknown); mirrors the cheapest offer's /discover tier
	BestTPS      float64  `json:"best_tps"`     // fastest measured output tok/s
	BestTTFTMs   float64  `json:"ttft_ms"`      // best (lowest) probe-measured TTFT across providers (ms; 0 = unmeasured)
	Quality      float64  `json:"quality"`      // mean broker-measured trust/quality across providers (0..1)
	SuccessRate  float64  `json:"success_rate"` // mean time-decayed success across providers (0..1)
	Verified     bool     `json:"verified"`     // at least one provider has a recent PASSED canary
	Signal       int      `json:"signal"`       // 0..100 demand/quality signal
	// Terms is the per-factor breakdown (supply/speed/latency/verified/success/trust
	// + congestion discount) so the website can explain the meter.
	Terms signalTerms `json:"terms"`
}

// market handles GET /market: a per-model marketplace view aggregated from live
// node state - how many providers are online, current in-flight load, the cheapest
// active price, the best measured throughput, mean success rate, and a 0..100
// "signal" combining supply, quality, and reliability. Concurrency-safe.
func (b *broker) market(w http.ResponseWriter, r *http.Request) {
	if corsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodGet) {
		return
	}
	cors(w) // public market data - let the website (rogerai.fyi) fetch it

	// Hot-path cache (flag-gated). /market aggregates per-model signal across every live
	// node per request; the cache collapses repeated aggregations into one within a tiny
	// window, shared across instances. PUBLIC, no-auth data - a single entry is safe to
	// share across all callers (keyed by the normalized query). Flag OFF => direct
	// compute (zero behavior change).
	b.serveCachedJSON(w, "market:"+normalizedMarketQuery(r), publicMarketTTL, b.computeMarket)
}

// computeMarket builds the /market payload: a per-model marketplace view aggregated
// from live node state (online providers, in-flight load, cheapest active price, best
// measured throughput, mean success, and a 0..100 signal). Pure read of broker state,
// so its serialized result is safe to cache briefly.
// marketCapabilities collapses a model's per-provider capability union into the aggregated value:
// the sorted union when any provider declared a capability, [] when providers declared only
// text-only, nil (omit) when none declared. IN PRACTICE today only "vision" reaches the app: a
// node's text-only [] collapses to absent on the offer wire (ModelOffer omitempty, required for
// the registration possession-proof), so `seen` is effectively true only when some provider
// reported vision. The app shows the photo button on "vision" and name-heuristics otherwise -
// correct for the non-vision models on air. (Restoring the text-only signal = TODO, off the
// signed offer.) The [] path is kept so it lights up the moment that channel exists.
func marketCapabilities(union map[string]bool, seen bool) []string {
	if !seen {
		return nil
	}
	out := make([]string, 0, len(union))
	for c := range union {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

func (b *broker) computeMarket() any {
	type acc struct {
		modality      string // canonical modality of this model's offers (offerModality; first seen sets it)
		providers     int
		inflight      int
		minPrice      float64
		havePrice     bool
		minOut        float64   // cheapest active OUT-price (incl. 0 = a free provider) - the model's BEST price
		haveOut       bool      // whether any provider's active out-price was seen
		outPrices     []float64 // online active OUT-prices > 0, for the per-model median baseline (mirrors assignPriceTiers' peers)
		bestTPS       float64
		bestTTFT      float64 // lowest non-zero probe TTFT (ms)
		haveTTFT      bool
		qualitySum    float64
		successSum    float64 // sum of per-node time-decayed success evidence
		successSeen   int
		bestRecency   float64         // freshest heartbeat recency across providers
		anyVerified   bool            // at least one provider has a recent PASSED canary
		bestStaleness float64         // freshest measurement-confidence across providers (0.7..1.0)
		capsUnion     map[string]bool // union of chat sub-capabilities across providers
		capsSeen      bool            // any provider DECLARED capabilities (so [] means text-only, not unknown)
	}
	now := time.Now()
	agg := map[string]*acc{}

	b.mu.Lock()
	b.metricsMu.Lock()
	for _, n := range b.nodes {
		if time.Since(b.lastSeen[n.NodeID]) >= nodeTTL {
			continue
		}
		// Banned/ejected nodes drop out of the aggregated market signal too. metricsMu
		// is already held here, so read b.banned directly (no re-lock via isBanned).
		if b.banned[n.NodeID] {
			continue
		}
		// Private bands are hidden from the aggregated market signal too (b.mu is held
		// here, so b.private is safe to read directly).
		if b.private[n.NodeID] {
			continue
		}
		recency := recencyOf(time.Since(b.lastSeen[n.NodeID]))
		tps := b.tps[n.NodeID]
		inflight := b.inflight[n.NodeID]
		sr, srSeen := b.success[n.NodeID]
		tq := b.trust[n.NodeID]
		ttft := tq.ttftMs
		quality := tq.score()
		verified := tq.verifiedServing()
		// Per-node time-decayed success evidence (organic EWMA, else probe-verified or
		// neutral) - NOT the old constant 1.0, so an unproven idle node doesn't inflate
		// the channel's reliability.
		nodeSuccess := successFor(sr, srSeen, verified)
		// Measurement-staleness confidence for this node + demand-driven refresh: a
		// consumer is browsing the market for this model, so if this provider's reading
		// is stale, schedule a near-term probe (async; this view uses the current data).
		staleness := b.measurementStalenessLocked(n.NodeID, now)
		if b.probe.enabled() && staleness < 1.0 {
			b.demandProbeSoonLocked(n.NodeID, now)
		}
		for _, o := range n.Offers {
			a := agg[o.Model]
			if a == nil {
				// The SAME canonical modality the per-offer feed carries (offerView):
				// offerModality normalizes a pre-voice empty modality to "chat".
				a = &acc{modality: offerModality(o.Modality), capsUnion: map[string]bool{}}
				agg[o.Model] = a
			}
			if caps := protocol.CanonicalCapabilities(o.Capabilities); caps != nil { // declared vs undetermined (nil)
				a.capsSeen = true
				for _, c := range caps { // canonicalized: unknown wire values already dropped
					a.capsUnion[c] = true
				}
			}
			a.providers++
			a.inflight += inflight
			in, out, _, _ := o.ActivePrice(now)
			if !a.havePrice || in < a.minPrice {
				a.minPrice, a.havePrice = in, true
			}
			// Track the cheapest active OUT-price (the model's BEST price - the tier numerator)
			// and the spread of priced OUT-offers (the internal-median baseline, online only,
			// > 0 - exactly the peers assignPriceTiers uses for the per-offer tier).
			if !a.haveOut || out < a.minOut {
				a.minOut, a.haveOut = out, true
			}
			if out > 0 {
				a.outPrices = append(a.outPrices, out)
			}
			if tps > a.bestTPS {
				a.bestTPS = tps
			}
			if ttft > 0 && (!a.haveTTFT || ttft < a.bestTTFT) {
				a.bestTTFT, a.haveTTFT = ttft, true
			}
			if recency > a.bestRecency {
				a.bestRecency = recency
			}
			if staleness > a.bestStaleness {
				a.bestStaleness = staleness // freshest measurement across providers
			}
			if verified {
				a.anyVerified = true
			}
			a.qualitySum += quality
			a.successSum += nodeSuccess
			a.successSeen++
		}
	}
	b.metricsMu.Unlock()
	b.mu.Unlock()

	out := make([]marketView, 0, len(agg))
	for model, a := range agg {
		successRate := 0.6 // neutral when somehow no provider contributed
		if a.successSeen > 0 {
			successRate = a.successSum / float64(a.successSeen)
		}
		quality := 1.0 // optimistic until measured
		if a.providers > 0 {
			quality = a.qualitySum / float64(a.providers)
		}
		terms := computeSignal(signalInput{
			providers: a.providers, inflight: a.inflight, bestTPS: a.bestTPS, ttftMs: a.bestTTFT,
			successRate: successRate, trust: quality, recency: a.bestRecency, verified: a.anyVerified,
			staleness: a.bestStaleness,
		})
		// Per-model neutral $-tier: priceTier over the model's BEST (cheapest) active out-price
		// vs the external reference (preferred) else the live per-model median of online out-
		// prices - the SAME priceTier the cheapest provider's offer carries on /discover, so the
		// aggregate row agrees with the per-offer feed. b.refOut locks b.refMu (independent of
		// b.mu/b.metricsMu, both released above). A FREE/thin model yields tier 0.
		ref, _ := b.refOut(model)
		tier := priceTier(a.minOut, ref, a.outPrices)
		out = append(out, marketView{
			Model: model, Modality: a.modality, Capabilities: marketCapabilities(a.capsUnion, a.capsSeen),
			Providers: a.providers, InFlight: a.inflight,
			MinPrice: a.minPrice, PriceTier: tier, BestTPS: a.bestTPS, BestTTFTMs: round6(a.bestTTFT),
			Quality:     round6(quality),
			SuccessRate: round6(successRate),
			Verified:    a.anyVerified,
			Signal:      terms.Total,
			Terms:       terms,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Signal > out[j].Signal })
	return map[string]any{"market": out}
}

// signalInput is the full per-channel evidence the multi-factor signal scores. It
// folds heartbeat RECENCY, probe TTFT, a probe-VERIFIED-SERVING bit, and
// time-decayed success on top of the original supply/speed/trust/congestion terms,
// so an IDLE band differentiates: a probed-fast, recently-verified node scores well
// ABOVE a probed-slow or never-verified one even with zero organic traffic.
type signalInput struct {
	providers int
	inflight  int
	bestTPS   float64 // fastest measured output tok/s across the channel's nodes
	ttftMs    float64 // best (lowest) probe TTFT across nodes (ms; 0 = unmeasured)
	// successRate is the time-decayed success EWMA (0..1). A node with NO organic
	// traffic but a recent PASSED canary should pass successDecayed ~ verified-OK
	// (positive evidence), NOT the old constant 1.0.
	successRate float64
	trust       float64 // 0..1 broker trust/quality (L1 + canary)
	recency     float64 // 1 - clamp(age/nodeTTL); 1 = just heartbeat'd, 0 = at TTL edge
	verified    bool    // a recent PASSED canary (probe-verified serving)
	// staleness is a gentle 0.7..1.0 recency-of-MEASUREMENT confidence factor: 1.0 when
	// the node was probed/served within the probe ceiling, modestly lower the longer it
	// has gone UNMEASURED (a long-idle node we deliberately stopped probing reads as
	// "not recently verified" rather than us burning a probe to keep it at 1.0). It
	// discounts only the MEASURED terms (speed/latency/verified) - heartbeat liveness +
	// supply are untouched. 0 (the zero value) is treated as 1.0 so callers that don't
	// set it (and the legacy shims/tests) keep full confidence.
	staleness float64
}

// signalTerms is the per-term breakdown surfaced to /market + /discover so the UI
// can explain the number ("why is this band a 71?"). Each field is the term's
// post-weight contribution to the 0..100 score (congestion is the multiplicative
// discount that was applied). Total is the final clamped 0..100 signal.
type signalTerms struct {
	Supply     float64 `json:"supply"`     // supply contribution (points)
	Speed      float64 `json:"speed"`      // measured tok/s contribution (points)
	Latency    float64 `json:"latency"`    // probe TTFT contribution (points)
	Verified   float64 `json:"verified"`   // probe-verified-serving contribution (points)
	Success    float64 `json:"success"`    // time-decayed success contribution (points)
	Trust      float64 `json:"trust"`      // L1 + canary trust contribution (points)
	Congestion float64 `json:"congestion"` // congestion discount applied (0..1; 0 = none)
	Total      int     `json:"total"`      // final 0..100 signal
}

// Signal weights. They sum to 1.0 before the congestion discount. Re-weighted from
// the old supply-heavy blend to reward MEASURED serving (speed+latency 0.30,
// verified-serving 0.20) so the signal reflects "is this node actually fast and
// proven right now", not just "are there a lot of them".
const (
	wSupply   = 0.20
	wSpeed    = 0.18 // throughput half of the speed+ttft 0.30 block
	wLatency  = 0.12 // TTFT half of the speed+ttft 0.30 block
	wVerified = 0.20
	wSuccess  = 0.15
	wTrust    = 0.15
	// ttftFloor is the TTFT (ms) at/below which the latency term is full; ttftCap is
	// where it bottoms out. Mirrors the audit's 1 - clamp(ttftMs/2000).
	ttftCap = 2000.0
)

// recencyOf maps a node's heartbeat age to a continuous 1..0 recency factor:
// 1 - clamp(age/nodeTTL). 1 = just heartbeat'd, 0 = at the TTL edge (about to age
// out). Continuous so the meter sags smoothly instead of staying pinned until it
// snaps to 0 at TTL.
func recencyOf(age time.Duration) float64 {
	return clamp01(1 - float64(age)/float64(nodeTTL))
}

// successFor returns the channel's success evidence (0..1) for the signal:
//   - measured organic success EWMA when we have traffic (srSeen);
//   - otherwise, NOT the old constant 1.0: a node with a recent PASSED canary counts
//     as positive (probed-OK-no-traffic = good evidence, verifiedOK), while a node
//     with NO evidence at all sits at a NEUTRAL 0.6 (unproven, not assumed perfect).
func successFor(sr float64, srSeen, verifiedOK bool) float64 {
	if srSeen {
		return clamp01(sr)
	}
	if verifiedOK {
		return 0.9 // probed OK, no organic traffic yet: strong positive, just shy of proven
	}
	return 0.6 // no evidence either way: neutral, not optimistic-1.0
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

// computeSignal scores a channel 0..100 from full multi-factor evidence and returns
// the per-term breakdown. No providers = dead channel (0). Deliberately monotonic
// in supply, speed, success, trust, verified, recency, and latency (lower TTFT is
// better), and discounted by congestion - so it stays a glanceable health bar, not
// a price.
func computeSignal(in signalInput) signalTerms {
	if in.providers == 0 {
		return signalTerms{}
	}
	// Supply: saturates around ~5 providers.
	supply := clamp01(float64(in.providers) / 5.0)
	// Speed: measured tok/s, saturating around 300 t/s.
	speed := clamp01(in.bestTPS / 300.0)
	// Latency: 1 - clamp(ttftMs/2000). Unmeasured TTFT (0) is treated as NEUTRAL
	// (0.5), not as instant - we have no evidence either way until a probe lands.
	latency := 0.5
	if in.ttftMs > 0 {
		latency = 1 - clamp01(in.ttftMs/ttftCap)
	}
	// Verified-serving: a recent PASSED canary is hard positive evidence the node is
	// actually answering correctly right now. Heartbeat-only nodes get 0 here, so a
	// probed-OK node scores materially above an unverified one at equal everything.
	verified := 0.0
	if in.verified {
		verified = 1.0
	}
	// Time-decayed success: caller already decays toward neutral with age (see
	// successFor); clamp here.
	success := clamp01(in.successRate)
	trust := clamp01(in.trust)
	// Recency multiplies the whole blend: a channel whose newest heartbeat is aging
	// toward the TTL is a weaker signal than one that just checked in. Continuous, so
	// the meter sags smoothly instead of staying pinned until it snaps to 0 at TTL.
	recency := clamp01(in.recency)
	// Staleness-of-MEASUREMENT confidence (0.7..1.0): a node we deliberately stopped
	// probing (long idle, no traffic) reads as "not recently verified" with a MODEST
	// haircut on the measured terms, instead of us burning a probe to keep it at 1.0.
	// 0 (unset) => 1.0, so callers/tests that don't supply it keep full confidence. It
	// touches ONLY speed/latency/verified (the probe-measured terms); supply, success,
	// trust, recency are unaffected. A fresh measurement restores it to 1.0 at once.
	staleness := 1.0
	if in.staleness > 0 {
		staleness = clamp01(in.staleness)
	}

	// Per-term point contributions (post-weight, pre-congestion, scaled to 100). The
	// measured terms (speed/latency/verified) carry the staleness confidence factor.
	t := signalTerms{
		Supply:   100 * wSupply * supply,
		Speed:    100 * wSpeed * speed * staleness,
		Latency:  100 * wLatency * latency * staleness,
		Verified: 100 * wVerified * verified * staleness,
		Success:  100 * wSuccess * success,
		Trust:    100 * wTrust * trust,
	}
	base := t.Supply + t.Speed + t.Latency + t.Verified + t.Success + t.Trust
	base *= recency

	// Congestion penalty: load per provider; ~2+ in-flight each = fully congested.
	congestion := clamp01(float64(in.inflight) / float64(in.providers) / 2.0)
	t.Congestion = congestion
	final := base * (1 - 0.4*congestion)

	// Re-scale the surfaced per-term contributions by the same recency + congestion
	// factors so they sum to Total (the breakdown stays honest/additive).
	scale := recency * (1 - 0.4*congestion)
	t.Supply *= scale
	t.Speed *= scale
	t.Latency *= scale
	t.Verified *= scale
	t.Success *= scale
	t.Trust *= scale

	s := int(final + 0.5)
	if s < 0 {
		s = 0
	}
	if s > 100 {
		s = 100
	}
	t.Total = s
	return t
}
