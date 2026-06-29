package main

import (
	"hash/fnv"
	"math"
	"math/rand"
	"strings"
)

// router.go is the smart-router v2 scoring + selection core (the winning
// design-competition synthesis). It replaces value-per-credit ranking with:
//
//	score(c) = ucb( reliability * speedFit * priceMod ) * loadFactor
//
// and selects with capacity-aware power-of-two-choices over a reliability-bounded
// top band, so no rig becomes a magnet and no honest laptop starves. The pure
// pieces live here (no broker locks, no I/O) so they are directly unit-testable;
// pick (tunnel.go) gathers the per-node metrics under metricsMu and calls these.
//
// The default profile (prefBalanced, band clamp toward 1, deterministic seed) is
// the conservative path the existing callers/tests exercise; the new spread +
// exploration behaviour widens from there as the user knob and live load demand.

// Router tuning constants (the spec's exact values). They are package-level so a
// future env override is a one-line change; v1 ships the spec defaults.
const (
	tpsTarget    = 120.0  // "fast enough" decode tok/s: speedFit's throughput half saturates here
	ttftCapMs    = 2000.0 // effective-TTFT ceiling (ms): at/above this the latency half bottoms out
	prefillRatio = 8.0    // prefill tok/s ~= decode tps * this (prefill >> decode)
	tpsPerSlot   = 40.0   // concurrent tok/s that constitutes one capacity "slot"
	maxSlots     = 16     // capacity clamp ceiling (a single node never models infinite concurrency)
	ucbCap       = 200.0  // N_eff clamp: non-stationarity floor on the evidence count
	bandRelDiff  = 0.15   // top-band membership: scores within this rel-gap of the best
	bandMin      = 2      // adaptive band lower clamp (always allow a P2C pair when >1 candidate)
	bandMax      = 8      // adaptive band upper clamp (cap the spread set)
)

// pref is the user-preference profile (cheap <-> balanced <-> fast <-> reliable).
// It reshapes the SCORE only - never the hard filters. prefBalanced reproduces
// today's intent (reliability-weighted, mild price pull) and is the zero-value
// default, so existing traffic does not change behaviour class.
type pref int

const (
	prefBalanced pref = iota
	prefCheap
	prefFast
	prefReliable
)

// parsePref maps the X-Roger-Pref header to a profile. Unknown/empty => balanced.
func parsePref(s string) pref {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "cheap":
		return prefCheap
	case "fast":
		return prefFast
	case "reliable":
		return prefReliable
	default:
		return prefBalanced
	}
}

// prefWeights are the knob anchors (spec table 1.2): the price-modifier strength
// kPrice + exponent priceExp, the UCB exploration radius C, the speedFit emphasis
// speedMul, and the P2C concentration beta.
type prefWeights struct {
	kPrice   float64
	priceExp float64
	c        float64 // UCB exploration radius coefficient
	speedMul float64 // speedFit multiplier (>1 favours speed)
	beta     float64 // P2C sampling concentration (score^beta)
}

func (p pref) weights() prefWeights {
	switch p {
	case prefCheap:
		return prefWeights{kPrice: 0.45, priceExp: 0.5, c: 0.25, speedMul: 1.0, beta: 1.5}
	case prefFast:
		return prefWeights{kPrice: 0.10, priceExp: 1.5, c: 0.20, speedMul: 1.3, beta: 3.0}
	case prefReliable:
		return prefWeights{kPrice: 0.20, priceExp: 0.8, c: 0.20, speedMul: 1.0, beta: 3.0}
	default: // balanced
		return prefWeights{kPrice: 0.25, priceExp: 1.0, c: 0.35, speedMul: 1.0, beta: 2.0}
	}
}

// reliabilityFactor is the multiplicative reliability spine (spec 1.1a): any one
// collapsing factor tanks the node, so speed/price can never buy back reliability.
// It is a PICK-LOCAL graded mapping of probe + organic evidence; it does not change
// verifiedServing()'s global meaning. A single transient probe miss costs ~40%
// (verifiedFactor 0.6), not 100% - the smoothness fix.
func reliabilityFactor(probed, probeOK bool, probeFails int, success float64, sseen bool, trust float64) float64 {
	ver := verifiedFactorOf(probed, probeOK, probeFails)
	// successFactor: floored at 0.5 so a node is never zeroed on success evidence;
	// reuses the channel's organic-or-verified success reading.
	verifiedOK := probed && probeOK && probeFails == 0
	sf := 0.5 + 0.5*successFor(success, sseen, verifiedOK)
	// trustFactor: map 0.5..1.0 (L1 + canary trust never zeroes the spine).
	tf := 0.5 + 0.5*clamp01(trust)
	return clamp01(ver) * clamp01(sf) * clamp01(tf)
}

// verifiedFactorOf is the GRADED verified-serving factor (spec 1.1a): a clean recent
// canary => 1.0; ONE transient probe miss => 0.6 (40% cost, NOT a hard zero - the
// smoothness fix); two or more misses => 0.15 (heavy but nonzero, last-resort
// availability); stale/never-probed => 0.7 (no positive proof, no failure either).
func verifiedFactorOf(probed, probeOK bool, probeFails int) float64 {
	switch {
	case probed && probeOK && probeFails == 0:
		return 1.0
	case probeFails == 1:
		return 0.6
	case probeFails >= 2:
		return 0.15
	default:
		return 0.7
	}
}

// speedFit is the saturating "fast enough" fit, request-size aware (spec 1.1b). A
// long prompt drives effective TTFT past the cap on weak hardware so it evicts
// itself - this is the heterogeneity router. tps==0 / ttft==0 read as neutral so a
// brand-new node still competes. speedMul (from pref) lifts the throughput half for
// the "fast" profile.
func speedFit(tps, ttftMs float64, promptTokens int, speedMul float64) float64 {
	// throughput half: 0.5..1.0, saturating at tpsTarget.
	tp := 0.75 // tps unmeasured: neutral-positive
	if tps > 0 {
		tp = 0.5 + 0.5*clamp01(tps*speedMul/tpsTarget)
	}
	// effective TTFT: probe/organic first-byte + the prefill cost of THIS prompt, so a
	// long prompt penalises weak hardware (prefillRate scales with the node's tps).
	ttftEff := ttftMs
	if promptTokens > 0 {
		prefillRate := math.Max(tps, 1) * prefillRatio
		ttftEff += float64(promptTokens) / prefillRate
	}
	lat := 0.8 // ttft unmeasured AND no prompt cost: neutral-positive
	if ttftEff > 0 {
		lat = 0.6 + 0.4*(1-clamp01(ttftEff/ttftCapMs))
	}
	return clamp01(tp) * clamp01(lat)
}

// priceMod is a BOUNDED soft modifier within the user's range (spec 1.1c) - NOT a
// divisor. A free node is neutral 1.0 (NOT score+1), killing the flaky-free-wins
// distortion. rangeMin is the cheapest ELIGIBLE out-price (computed in pick's own
// pass); rangeMax is the user's cap, else the eligible max. The modifier swings the
// score at most kPrice inside the user's own window.
func priceMod(out, rangeMin, rangeMax, kPrice, priceExp float64) float64 {
	if out <= 0 {
		return 1.0 // free: neutral, not a magnet
	}
	span := rangeMax - rangeMin
	if span <= 0 {
		return 1.0 // single price point (or degenerate range): no spread to reward
	}
	norm := clamp01((out - rangeMin) / span)
	return clamp01(1 - kPrice*math.Pow(norm, priceExp))
}

// extendOutRange folds an eligible offer's OUTPUT price into the running [min,max] range
// pick feeds to priceMod, IGNORING free (out<=0) offers so a giveaway never moves the
// eligible price window (rangeMin stays the cheapest PAID price, not 0). Returns the
// updated range and whether any paid price has been seen yet. This is the exact derivation
// pickFor uses; extracted so the "free never moves the range" invariant is directly
// testable (a free out=0 leaves the window and haveRange untouched).
func extendOutRange(out, rangeMin, rangeMax float64, haveRange bool) (float64, float64, bool) {
	if !(out > 0) { // exact negation of the original `if out > 0` guard (NaN-safe: NaN is ignored)
		return rangeMin, rangeMax, haveRange
	}
	if !haveRange || out < rangeMin {
		rangeMin = out
	}
	if !haveRange || out > rangeMax {
		rangeMax = out
	}
	return rangeMin, rangeMax, true
}

// priceCeiling is the upper bound of the priceMod reward range (spec 1.1c): the dearest
// ELIGIBLE out-price, WIDENED to the caller's max-out cap when they set one ("I'll pay up
// to X but reward me below it"). A zero/absent cap (maxPriceOut<=0) leaves the eligible max
// as the ceiling. This is the exact rmax pickFor computes; extracted so the cap-widening is
// directly testable.
func priceCeiling(rangeMax, maxPriceOut float64) float64 {
	if maxPriceOut > 0 && maxPriceOut > rangeMax {
		return maxPriceOut
	}
	return rangeMax
}

// explorationRadius is the canary-GATED UCB exploration lift (spec 1.1e): a node earns a
// non-zero radius ONLY once it has been probed AND passed the canary (probed && probeOK) -
// we explore honest-capable capacity, never unproven-flaky nodes (which get a flat 0). This
// is the exact gate pickFor applies; extracted so the gating is directly testable.
func explorationRadius(tq trustState, c float64, totalReqs int64, successCount int) float64 {
	if tq.probed && tq.probeOK {
		return ucbRadius(c, totalReqs, tq.recounts, tq.probes, successCount)
	}
	return 0
}

// hwConcurrencyClass is the conservative cold-start capacity prior from the node's
// self-asserted hw string (spec 1.1d): multi-GPU => 4, single discrete GPU => 2,
// else 1. DISPLAY/prior only - never score-trusted; washed out by the first real
// observedConcurrentTPS measurement. Coarse substring match; an unparseable string
// is the safe default of 1.
func hwConcurrencyClass(hw string) int {
	h := strings.ToLower(hw)
	// Nodes now advertise a PRIVACY-BUCKETED class (multi-gpu / single-gpu / apple /
	// cpu) instead of a raw rig string; map those directly. Legacy raw strings still
	// fall through to the marker heuristic below.
	switch h {
	case "multi-gpu":
		return 4
	case "single-gpu":
		return 2
	case "apple":
		return 2 // Apple Silicon unified memory: between a CPU and a discrete GPU
	case "cpu", "unknown", "":
		return 1
	}
	// A discrete GPU is present if ANY accelerator marker matches (synonyms for the
	// SAME card count once - we test presence, not how many synonyms hit).
	gpuMarkers := []string{"rtx", "geforce", "radeon", "instinct", "tesla", "a100", "h100", "mi300", "mi250", "gpu", "cuda", "rocm", "quadro", "nvidia", "amd radeon"}
	hasGPU := false
	for _, m := range gpuMarkers {
		if strings.Contains(h, m) {
			hasGPU = true
			break
		}
	}
	// Multi-accelerator is signalled ONLY by an explicit count/multiplier (dual / quad
	// / 2x / 4x / "4 x"), never by multiple synonyms matching one physical card.
	multi := strings.Contains(h, "dual") || strings.Contains(h, "quad") ||
		strings.Contains(h, "x4") || strings.Contains(h, "4x") ||
		strings.Contains(h, "x2") || strings.Contains(h, "2x") ||
		strings.Contains(h, "4 x") || strings.Contains(h, "2 x")
	switch {
	case multi:
		return 4
	case hasGPU:
		return 2
	default:
		return 1
	}
}

// capacityOf derives a node's concurrency capacity (spec 1.1d): from
// observedConcurrentTPS UNDER LOAD when we have it (incentive-compatible - a node
// can't win a bigger allotment from an idle canary), else a conservative hw-class
// prior. Clamped to [1, maxSlots].
func capacityOf(concurrentTPS float64, hw string) int {
	if concurrentTPS > 0 {
		c := int(math.Round(concurrentTPS / tpsPerSlot))
		return clampInt(c, 1, maxSlots)
	}
	return clampInt(hwConcurrencyClass(hw), 1, maxSlots)
}

// loadFactor is the capacity-normalized congestion discount (spec 1.1d):
// 1/(1+inflight/capacity). A node absorbs ~capacity concurrent requests before its
// score sags, so a rig is not a magnet and a laptop is not starved.
func loadFactor(inflight, capacity int) float64 {
	if capacity < 1 {
		capacity = 1
	}
	return 1.0 / (1.0 + float64(inflight)/float64(capacity))
}

// ucbRadius is the exploration lift (spec 1.1e): C*sqrt(ln(1+totalReqs)/(1+N)) with
// N = recounts + probes + 3*successCount (successCount weighted 3x - it is the
// evidence for the reward dimension real traffic exercises), clamped by ucbCap for
// non-stationarity. Wide for a fresh node, self-extinguishing as N grows.
func ucbRadius(c float64, totalReqs int64, recounts, probes, successCount int) float64 {
	if c <= 0 {
		return 0 // C=0 short-circuits to deterministic merit ranking (legacy/tests)
	}
	n := float64(recounts + probes + 3*successCount)
	if n > ucbCap {
		n = ucbCap
	}
	tr := float64(totalReqs)
	if tr < 0 {
		tr = 0
	}
	return c * math.Sqrt(math.Log(1+tr)/(1+n))
}

// ucb applies the (gated) exploration lift, clamped to a valid score.
func ucb(v, radius float64) float64 {
	return clamp01(v + radius)
}

// clampInt clamps n to [lo, hi].
func clampInt(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

// scoredCand is a fully scored Tier-A candidate ready for band selection. score is
// the final composite ucb(R*speedFit*priceMod)*loadFactor; load is inflight/capacity
// (the live P2C tie-break, lower=better).
type scoredCand struct {
	idx   int     // index back into pick's parallel offer slice
	score float64 // final composite score (0..1+radius, clamped to 0..1 in ucb)
	load  float64 // inflight/capacity for the P2C live-load tie-break
}

// selectP2C is the anti-all-to-one selection (spec 1.5): build the adaptive top band
// (all candidates within bandRelDiff of the best, clamped to [bandMin,bandMax]),
// sample two members weighted by score^beta, and route to the one with the lower
// live load (inflight/capacity). Deterministic when rng is nil or one candidate
// (the old top-1 special case, protecting every existing test). cands MUST be
// non-empty; it returns the chosen index into the caller's parallel slice.
func selectP2C(cands []scoredCand, beta float64, rng *rand.Rand) int {
	if len(cands) == 0 {
		return -1
	}
	// Best-first by score; stable on the original index so a nil-rng tie is
	// deterministic and reproduces the legacy "first best wins" ordering.
	best := 0
	for i := 1; i < len(cands); i++ {
		if cands[i].score > cands[best].score ||
			(cands[i].score == cands[best].score && cands[i].idx < cands[best].idx) {
			best = i
		}
	}
	// Deterministic short-circuit: no PRNG (tests / C=0 / single-candidate) routes to
	// the single best, exactly as the pre-v2 running-best pick did.
	if rng == nil || len(cands) == 1 {
		return cands[best].idx
	}
	topScore := cands[best].score
	// Adaptive band: members within bandRelDiff of the best, clamped to [bandMin,bandMax].
	band := make([]scoredCand, 0, len(cands))
	for _, c := range cands {
		if topScore <= 0 || (topScore-c.score)/topScore <= bandRelDiff {
			band = append(band, c)
		}
	}
	// Sort the band best-first (so the clamp keeps the strongest members).
	for i := 1; i < len(band); i++ {
		for j := i; j > 0 && band[j].score > band[j-1].score; j-- {
			band[j], band[j-1] = band[j-1], band[j]
		}
	}
	if len(band) > bandMax {
		band = band[:bandMax]
	}
	// Ensure at least bandMin members where available (value-gap-adaptive: if only one
	// node is clearly best, the band stays small - no forced spread to junk).
	if len(band) < bandMin && len(cands) >= bandMin {
		// Take the bandMin strongest overall.
		all := make([]scoredCand, len(cands))
		copy(all, cands)
		for i := 1; i < len(all); i++ {
			for j := i; j > 0 && all[j].score > all[j-1].score; j-- {
				all[j], all[j-1] = all[j-1], all[j]
			}
		}
		band = all[:bandMin]
	}
	if len(band) == 1 {
		return band[0].idx
	}
	// Power-of-two-choices: sample two DISTINCT band members weighted by score^beta,
	// then route to the lower live-load one (dodges same-burst stampedes the EWMA
	// hasn't caught yet).
	a := weightedPick(band, beta, rng)
	bIdx := weightedPick(band, beta, rng)
	if bIdx == a {
		bIdx = (a + 1) % len(band)
	}
	ca, cb := band[a], band[bIdx]
	if cb.load < ca.load {
		return cb.idx
	}
	if ca.load < cb.load {
		return ca.idx
	}
	// Equal live load: prefer the higher score (then lower idx for determinism).
	if cb.score > ca.score || (cb.score == ca.score && cb.idx < ca.idx) {
		return cb.idx
	}
	return ca.idx
}

// weightedPick draws one band index with probability proportional to score^beta.
func weightedPick(band []scoredCand, beta float64, rng *rand.Rand) int {
	total := 0.0
	weights := make([]float64, len(band))
	for i, c := range band {
		w := math.Pow(math.Max(c.score, 1e-9), beta)
		weights[i] = w
		total += w
	}
	if total <= 0 {
		return 0
	}
	x := rng.Float64() * total
	for i, w := range weights {
		x -= w
		if x <= 0 {
			return i
		}
	}
	return len(band) - 1
}

// seededRand derives a deterministic *rand.Rand from a request id, so a routing
// decision is reproducible in tests (seed the PRNG from hash(requestID)). An empty
// id yields a nil rng -> deterministic top-1 (the legacy path).
func seededRand(requestID string) *rand.Rand {
	if requestID == "" {
		return nil
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(requestID))
	return rand.New(rand.NewSource(int64(h.Sum64())))
}
