package main

// netfilters.go - the open-network filters of ROUTING-EXPRESSION-CONTRACT §5 (slice 2):
// parameter count, context window, time to first token, trust and region. One check serves
// the direct pick (pickFor) and the Tower rows (edgeEligibleM), so both fabrics apply the
// same rule. Declared attributes (params_b, ctx, region) that are not declared leave an
// offer ineligible under their filter; measured ones (ttft) that were not measured pass.

import (
	"os"
	"strconv"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/detect"
	"rogerai.fm/roger/v6/internal/protocol"
)

// netFilters are the slice-2 hard filters a request states. The zero value filters nothing.
type netFilters struct {
	paramsLo, paramsHi float64         // roger.params_b [min, max], billions; 0,0 = none
	minCtx             int             // roger.min_ctx: a DECLARED window at least this large
	maxTTFT            float64         // roger.max_ttft_ms: measured TTFT at most this; unmeasured passes
	trustVerified      bool            // roger.trust_min "verified"
	regions            map[string]bool // roger.region; nil = none
}

func (f netFilters) any() bool {
	return f.paramsHi > 0 || f.minCtx > 0 || f.maxTTFT > 0 || f.trustVerified || f.regions != nil
}

// offerParams is the parameter count an offer routes and displays under: the station's
// declaration, else the estimate read from the model id. known=false when neither exists.
func offerParams(o protocol.ModelOffer) (v float64, estimated, known bool) {
	if o.ParamsB > 0 {
		return o.ParamsB, false, true
	}
	if g, ok := detect.ParamsFromID(o.Model); ok {
		return g, true, true
	}
	return 0, false, false
}

// paramsContradicted reports a station declaration the model id flatly contradicts (more
// than a factor of two either way). Such an offer is ineligible under a params_b filter: a
// declaration cannot be used to pass a filter the weights would fail (edge_bridge_parity).
func paramsContradicted(o protocol.ModelOffer) bool {
	if o.ParamsB <= 0 {
		return false
	}
	g, ok := detect.ParamsFromID(o.Model)
	return ok && (o.ParamsB > 2*g || o.ParamsB < g/2)
}

// verifiedFresh is trust_min "verified": a passed, completed, model-consistent canary
// (verifiedServing) whose measurement is inside the probe freshness window the score already
// uses (probeConfig.measurementStale, the probe ceiling). With no ceiling there is no window.
// Callers hold b.metricsMu.
func (b *broker) verifiedFreshLocked(nodeID string, tq trustState, now time.Time) bool {
	if !tq.verifiedServing() || b.organicContradictsLocked(nodeID, tq) {
		return false
	}
	if b.probe.ceiling <= 0 {
		return true // no freshness window configured: nothing to age out of
	}
	st := b.probeSchedLocked()[nodeID]
	return st != nil && !b.probe.measurementStale(st.lastMeasured, now)
}

// verifiedStrikes / verifiedWindow are ROGERAI_VERIFIED_STRIKES (default 3) and
// ROGERAI_VERIFIED_WINDOW (default 1h): K organic recount strikes inside the window withdraw
// `verified` whatever the probe says (§14.B7).
func verifiedStrikes() int {
	if n, err := strconv.Atoi(os.Getenv("ROGERAI_VERIFIED_STRIKES")); err == nil && n > 0 {
		return n
	}
	return 3
}

func verifiedWindow() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("ROGERAI_VERIFIED_WINDOW")); err == nil && d > 0 {
		return d
	}
	return time.Hour
}

// organicContradictsLocked reports organic evidence against a station the probe calls
// verified: K recount strikes from customers' relays inside the window, or an organic success
// rate below the Tier-A bar. Once the strikes age out and the success recovers, the next
// passing canary restores `verified`. Caller holds metricsMu.
func (b *broker) organicContradictsLocked(nodeID string, tq trustState) bool {
	since := b.now().Add(-verifiedWindow()).UnixMilli()
	n := 0
	for _, at := range tq.organicStrikes {
		if at >= since {
			n++
		}
	}
	if n >= verifiedStrikes() {
		return true
	}
	sr, seen := b.success[nodeID]
	return seen && sr < tierASuccessBar
}

// noteOrganicStrike records a recount strike from an organic relay against nodeID (pruned to
// the window).
func (b *broker) noteOrganicStrike(nodeID string) {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	if b.trust == nil {
		return
	}
	tq := b.trust[nodeID]
	since := b.now().Add(-verifiedWindow()).UnixMilli()
	kept := tq.organicStrikes[:0]
	for _, at := range tq.organicStrikes {
		if at >= since {
			kept = append(kept, at)
		}
	}
	tq.organicStrikes = append(kept, b.now().UnixMilli())
	b.trust[nodeID] = tq
}

// netReject names the first slice-2 filter an offer fails ("" = passes), in the fixed order
// the no_match message lists them. Callers hold b.mu and b.metricsMu.
func (b *broker) netRejectLocked(f netFilters, reg protocol.NodeRegistration, o protocol.ModelOffer, declared bool, now time.Time) string {
	if f.paramsHi > 0 {
		v, _, known := offerParams(o)
		if paramsContradicted(o) {
			b.stats.noteParamsMismatch(reg.NodeID)
			return "params_b"
		}
		if !declared || !known || v < f.paramsLo || v > f.paramsHi {
			return "params_b"
		}
	}
	if f.minCtx > 0 && (!declared || o.CtxEstimated || o.Ctx < f.minCtx) {
		return "min_ctx"
	}
	tq := b.trust[reg.NodeID]
	if f.maxTTFT > 0 && tq.ttftMs > 0 && tq.ttftMs > f.maxTTFT {
		return "max_ttft_ms"
	}
	if f.trustVerified && !b.verifiedFreshLocked(reg.NodeID, tq, now) {
		return "trust_min"
	}
	if f.regions != nil && (reg.Region == "" || !f.regions[strings.ToLower(strings.TrimSpace(reg.Region))] || regionContradicted(reg)) {
		return "region"
	}
	return ""
}

// noMatchFilters names, in a fixed order, each STATED filter that turned away at least one
// on-air public offer of the requested models - the filters a refused consumer could relax.
// Liveness, bans and private bands are the fleet's state, never named here.
func (b *broker) noMatchFilters(models []routedModel, req pickReq, minTPS float64) []string {
	want := map[string]bool{}
	for _, m := range models {
		want[m.bare] = true
	}
	now := time.Now()
	hit := map[string]bool{}
	b.mu.Lock()
	b.metricsMu.Lock()
	for _, n := range b.nodes {
		if now.Sub(b.lastSeen[n.NodeID]) >= nodeTTL || b.banned[n.NodeID] || b.private[n.NodeID] {
			continue
		}
		for _, o := range n.Offers {
			if !want[o.Model] {
				continue
			}
			if req.quants != nil && !req.quants[strings.ToLower(o.Quant)] && !(o.Quant == "" && req.quants[protocol.QuantUnknown]) {
				hit["quantizations"] = true
			}
			if tps := b.tps[n.NodeID]; minTPS > 0 && tps > 0 && tps < minTPS {
				hit["min_tps"] = true
			}
			if req.selfHostedOnly && n.Curated {
				hit["self_hosted_only"] = true
			}
			for _, f := range []netFilters{
				{paramsLo: req.paramsLo, paramsHi: req.paramsHi}, {minCtx: req.minCtx}, {maxTTFT: req.maxTTFT},
				{trustVerified: req.trustVerified}, {regions: req.regions},
			} {
				if f.any() {
					if why := b.netRejectLocked(f, n, o, true, now); why != "" {
						hit[why] = true
					}
				}
			}
		}
	}
	b.metricsMu.Unlock()
	b.mu.Unlock()
	var out []string
	for _, f := range noMatchFilterOrder {
		if hit[f] {
			out = append(out, f)
		}
	}
	return out
}

var noMatchFilterOrder = []string{"quantizations", "params_b", "min_ctx", "min_tps", "max_ttft_ms", "trust_min", "self_hosted_only", "region"}
