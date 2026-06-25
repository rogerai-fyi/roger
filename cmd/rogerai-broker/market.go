package main

import (
	"net/http"
	"sort"
	"time"
)

type offerView struct {
	NodeID       string  `json:"node_id"`
	Region       string  `json:"region"`
	HW           string  `json:"hw"`
	Model        string  `json:"model"`
	In           float64 `json:"price_in"`  // active (time-of-use) price right now
	Out          float64 `json:"price_out"` // active price right now
	Ctx          int     `json:"ctx"`
	Online       bool    `json:"online"`
	Confidential bool    `json:"confidential"`
	FreeNow      bool    `json:"free_now"`
	Scheduled    bool    `json:"scheduled"`
	TPS          float64 `json:"tps"`      // measured output tokens/sec (0 = not yet measured)
	TTFTMs       float64 `json:"ttft_ms"`  // probe-measured time-to-first-token (ms; 0 = unmeasured)
	Quality      float64 `json:"quality"`  // 0..1 broker-measured trust/verification signal
	SuccessRate  float64 `json:"success"`  // 0..1 time-decayed success evidence (organic or probe)
	Verified     bool    `json:"verified"` // node has a recent PASSED canary (probe-verified serving)
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

// discover handles GET /discover: all model offers with live status, measured
// throughput, and active (time-of-use) price, cheapest-now first.
func (b *broker) discover(w http.ResponseWriter, r *http.Request) {
	if corsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodGet) {
		return
	}
	cors(w) // public market data - let the website (rogerai.fyi) fetch it
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
		age := time.Since(b.lastSeen[n.NodeID])
		online := age < nodeTTL
		// Per-node market metrics, read once under metricsMu, that feed BOTH the
		// surfaced quality/tps fields AND the 0..100 signal. An offline node scores 0
		// (offerSignal forces it); an online node with no traffic still earns its
		// baseline from supply + verified-serving + trust, differentiated by probe
		// evidence (ttft/verified) rather than an optimistic constant.
		b.metricsMu.Lock()
		tq := b.trust[n.NodeID]
		tps := b.tps[n.NodeID]
		inflight := b.inflight[n.NodeID]
		sr, srSeen := b.success[n.NodeID]
		quality := tq.score()
		ttft := tq.ttftMs
		verified := tq.verifiedServing()
		staleness := b.measurementStalenessLocked(n.NodeID, now)
		// Smart-router v2 selection fields (mirror pick): capacity from under-load TPS
		// else hw-class prior; the UCB exploration lift (gated to canary-passed nodes).
		capacity := capacityOf(b.concurrentTPS[n.NodeID], n.HW)
		radius := 0.0
		if tq.probed && tq.probeOK {
			radius = ucbRadius(prefBalanced.weights().c, b.totalReqs.Load(), tq.recounts, tq.probes, b.successCount[n.NodeID])
		}
		// Demand-driven: a consumer is browsing this offer. If its measurement is stale,
		// schedule a near-term probe so the NEXT browse/route reads fresh data (async;
		// this browse uses the current reading). Only the online nodes are worth probing.
		if online && b.probe.enabled() && staleness < 1.0 {
			b.demandProbeSoonLocked(n.NodeID, now)
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
		for _, o := range n.Offers {
			pin, pout, free, _ := o.ActivePrice(now)
			out = append(out, offerView{
				NodeID: n.NodeID, Region: n.Region, HW: n.HW, Model: o.Model,
				In: pin, Out: pout, Ctx: o.Ctx, Online: online,
				Confidential: b.confidential[n.NodeID], FreeNow: free, Scheduled: len(o.Schedule) > 0,
				TPS:    tps,
				TTFTMs: ttft, Quality: quality,
				SuccessRate: round6(successRate), Verified: verified,
				Signal:   terms.Total,
				Terms:    terms,
				InFlight: inflight, Capacity: capacity, Radius: round6(radius),
			})
		}
	}
	b.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].In < out[j].In })
	writeJSON(w, http.StatusOK, map[string]any{"offers": out})
}

// marketView is the per-model market summary surfaced by GET /market.
type marketView struct {
	Model       string  `json:"model"`
	Providers   int     `json:"providers"`    // online nodes offering this model
	InFlight    int     `json:"in_flight"`    // active requests across those nodes
	MinPrice    float64 `json:"min_price"`    // cheapest active input price (credits/1M)
	BestTPS     float64 `json:"best_tps"`     // fastest measured output tok/s
	BestTTFTMs  float64 `json:"ttft_ms"`      // best (lowest) probe-measured TTFT across providers (ms; 0 = unmeasured)
	Quality     float64 `json:"quality"`      // mean broker-measured trust/quality across providers (0..1)
	SuccessRate float64 `json:"success_rate"` // mean time-decayed success across providers (0..1)
	Verified    bool    `json:"verified"`     // at least one provider has a recent PASSED canary
	Signal      int     `json:"signal"`       // 0..100 demand/quality signal
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
	type acc struct {
		providers     int
		inflight      int
		minPrice      float64
		havePrice     bool
		bestTPS       float64
		bestTTFT      float64 // lowest non-zero probe TTFT (ms)
		haveTTFT      bool
		qualitySum    float64
		successSum    float64 // sum of per-node time-decayed success evidence
		successSeen   int
		bestRecency   float64 // freshest heartbeat recency across providers
		anyVerified   bool    // at least one provider has a recent PASSED canary
		bestStaleness float64 // freshest measurement-confidence across providers (0.7..1.0)
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
				a = &acc{}
				agg[o.Model] = a
			}
			a.providers++
			a.inflight += inflight
			in, _, _, _ := o.ActivePrice(now)
			if !a.havePrice || in < a.minPrice {
				a.minPrice, a.havePrice = in, true
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
		out = append(out, marketView{
			Model: model, Providers: a.providers, InFlight: a.inflight,
			MinPrice: a.minPrice, BestTPS: a.bestTPS, BestTTFTMs: round6(a.bestTTFT),
			Quality:     round6(quality),
			SuccessRate: round6(successRate),
			Verified:    a.anyVerified,
			Signal:      terms.Total,
			Terms:       terms,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Signal > out[j].Signal })
	writeJSON(w, http.StatusOK, map[string]any{"market": out})
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

// offerSignal scores a SINGLE offer 0..100 using the exact same blend /market uses,
// so the band list's meter and the /market view agree for the same node.
// providers=1 for one offer. An offline offer is 0 (no supply); an online offer with
// no traffic still earns its baseline from supply + verified-serving + trust (no tps
// required) - the freshly-on-air-band fix, now differentiated by probe evidence.
func offerSignal(online bool, inflight int, tps, ttftMs, successRate, trust, recency float64, verified bool) int {
	if !online {
		return 0
	}
	return computeSignal(signalInput{
		providers: 1, inflight: inflight, bestTPS: tps, ttftMs: ttftMs,
		successRate: successRate, trust: trust, recency: recency, verified: verified,
	}).Total
}

// marketSignal is the original 5-arg blend, KEPT for the monotonicity tests and as
// a thin shim: supply + speed + success + trust, idle-fresh (recency 1), no probe
// evidence (latency neutral, unverified). New call sites use computeSignal directly
// with the full evidence; this remains so the existing contract + tests hold.
func marketSignal(providers, inflight int, bestTPS, successRate, trust float64) int {
	return computeSignal(signalInput{
		providers: providers, inflight: inflight, bestTPS: bestTPS,
		successRate: successRate, trust: trust, recency: 1,
	}).Total
}
