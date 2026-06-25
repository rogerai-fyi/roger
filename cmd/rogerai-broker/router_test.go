package main

import (
	"math/rand"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// twoNodeBroker builds a route-able broker with two same-model offers and the v2
// metric maps initialised.
func twoNodeBroker(now time.Time, aPrice, bPrice float64) *broker {
	b := routeBroker(now, map[string]protocol.NodeRegistration{
		"a": {NodeID: "a", Offers: []protocol.ModelOffer{{Model: "m", PriceIn: aPrice, PriceOut: aPrice}}},
		"b": {NodeID: "b", Offers: []protocol.ModelOffer{{Model: "m", PriceIn: bPrice, PriceOut: bPrice}}},
	})
	b.successCount = map[string]int{}
	b.concurrentTPS = map[string]float64{}
	return b
}

// TestReliabilitySpineGraded: a single probe miss costs ~40% (verifiedFactor 0.6),
// NOT 100% - the smoothness fix. Two misses tank it hard but never to zero.
func TestReliabilitySpineGraded(t *testing.T) {
	full := reliabilityFactor(true, true, 0, 0, false, 1.0)
	oneMiss := reliabilityFactor(true, false, 1, 0, false, 1.0)
	twoMiss := reliabilityFactor(true, false, 2, 0, false, 1.0)
	if oneMiss <= 0 {
		t.Fatal("one probe miss must not zero the node")
	}
	// The graded verifiedFactor itself is 0.6 (40% cost, NOT a hard zero) - the
	// smoothness fix, checked in isolation. (The composite spine drops a touch further
	// because the same miss also lowers the success-evidence factor, which is the
	// intended compounding - a missed probe is weaker on two axes.)
	verFull := verifiedFactorOf(true, true, 0)
	verMiss := verifiedFactorOf(true, false, 1)
	if verFull != 1.0 || verMiss != 0.6 {
		t.Errorf("verifiedFactor full=%.2f miss=%.2f, want 1.00 / 0.60", verFull, verMiss)
	}
	// The composite one-miss node still retains roughly half its reliability (not 0).
	ratio := oneMiss / full
	if ratio < 0.40 || ratio > 0.65 {
		t.Errorf("one-miss reliability ratio = %.2f, want ~0.5 (graded, not zeroed)", ratio)
	}
	if twoMiss <= 0 {
		t.Error("two misses must still be nonzero (last-resort availability)")
	}
	if twoMiss >= oneMiss {
		t.Error("two misses must be worse than one")
	}
}

// TestReliabilitySpineCannotBeBoughtBack: a high-reliability paid node beats a
// flaky free node - price/free can NEVER buy back the reliability spine (the spec's
// core inversion + the flaky-free-wins fix).
func TestReliabilitySpineCannotBeBoughtBack(t *testing.T) {
	now := time.Now()
	b := twoNodeBroker(now, 0.50, 0.0) // a paid, b free
	b.tps["a"], b.tps["b"] = 200, 300  // free node is even faster
	// a: solid, verified, healthy. b: free + fast but probe-FAILING (flaky).
	b.trust["a"] = trustState{probed: true, probeOK: true, probeFails: 0, ttftMs: 200}
	b.trust["b"] = trustState{probed: true, probeOK: false, probeFails: 3, ttftMs: 100}

	b.mu.Lock()
	n, _, ok := b.pick("m", false, 0, 0, 0, "", nil, nil, nil)
	b.mu.Unlock()
	if !ok || n.NodeID != "a" {
		t.Errorf("pick = %q ok=%v, want a (reliable paid beats flaky free)", n.NodeID, ok)
	}
}

// TestFreeNodeNeutralNotMagnet: a free node with EQUAL reliability/speed to a paid
// node does not dominate purely because it is free (priceMod neutral 1.0); the paid
// node, being cheapest in its own range, ties or wins on merit, not on a +1 lift.
func TestFreeNodeIsNeutral(t *testing.T) {
	// priceMod: free => exactly 1.0 (neutral), never a divisor-driven blowup.
	if pm := priceMod(0, 0.1, 0.5, 0.25, 1.0); pm != 1.0 {
		t.Errorf("free priceMod = %.3f, want 1.0 (neutral)", pm)
	}
	// cheapest eligible (out==rangeMin) => modifier 1.0 (no penalty at the floor).
	if pm := priceMod(0.1, 0.1, 0.5, 0.25, 1.0); pm != 1.0 {
		t.Errorf("rangeMin priceMod = %.3f, want 1.0", pm)
	}
	// dearest (out==rangeMax) => 1 - kPrice (the bounded max swing), not a blowup.
	if pm := priceMod(0.5, 0.1, 0.5, 0.25, 1.0); pm < 0.74 || pm > 0.76 {
		t.Errorf("rangeMax priceMod = %.3f, want ~0.75 (1 - kPrice)", pm)
	}
}

// TestRequestSizeRouting: a long prompt drives effective TTFT past the cap on weak
// hardware so the laptop falls out of the top band; a short prompt keeps it
// competitive - the heterogeneity router.
func TestRequestSizeRouting(t *testing.T) {
	now := time.Now()
	b := twoNodeBroker(now, 0.30, 0.30) // equal price
	// rig: fast decode + low ttft. laptop: slow decode + higher ttft.
	b.tps["a"], b.tps["b"] = 300, 20 // a=rig, b=laptop
	b.trust["a"] = trustState{probed: true, probeOK: true, ttftMs: 150}
	b.trust["b"] = trustState{probed: true, probeOK: true, ttftMs: 400}

	// Short prompt: both clear "fast enough"; the laptop is still a viable candidate
	// (its speedFit stays well above the rig's, so it isn't evicted).
	shortFit := speedFit(20, 400, 50, 1.0)
	if shortFit <= 0.4 {
		t.Errorf("laptop short-prompt speedFit = %.2f, want viable (>0.4)", shortFit)
	}
	// Long prompt (30k tokens): the laptop's ttftEff blows past the cap, collapsing its
	// latency multiplier toward the 0.6 floor; the rig's prefill is ~15x faster.
	longLaptop := speedFit(20, 400, 30000, 1.0)
	longRig := speedFit(300, 150, 30000, 1.0)
	if longRig <= longLaptop {
		t.Errorf("long-prompt: rig speedFit %.2f should exceed laptop %.2f", longRig, longLaptop)
	}
	if longLaptop >= shortFit {
		t.Errorf("long prompt should DROP the laptop's fit (%.2f) below its short-prompt fit (%.2f)", longLaptop, shortFit)
	}
	_ = b
}

// TestCapacityNotGamedByIdleCanary: capacity does NOT rise from an idle single-stream
// probe (probeTPS); it only rises from observedConcurrentTPS recorded under load. An
// unmeasured node falls back to the conservative hw-class prior.
func TestCapacityNotGamedByIdleCanary(t *testing.T) {
	// No under-load measurement: capacity is the hw-class prior, NOT inferred from a
	// fast idle probe.
	if c := capacityOf(0, "Intel Core i7"); c != 1 {
		t.Errorf("idle CPU node capacity = %d, want 1 (hw-class prior, not probe-inflated)", c)
	}
	if c := capacityOf(0, "NVIDIA RTX PRO 4500"); c != 2 {
		t.Errorf("single-GPU prior = %d, want 2", c)
	}
	if c := capacityOf(0, "Dual NVIDIA RTX 4090"); c != 4 {
		t.Errorf("multi-GPU prior = %d, want 4", c)
	}
	// Only a real under-load TPS lifts capacity: 320 concurrent t/s / 40 = 8 slots.
	if c := capacityOf(320, "Intel Core i7"); c != 8 {
		t.Errorf("under-load capacity = %d, want 8 (measured, not prior)", c)
	}
}

// TestRecordServedQualityGate: a quality-validated completion increments
// successCount; an empty/garbage one does NOT (it cannot shrink the UCB radius).
// concurrentTPS is folded only when served under load (>=2 in-flight at dispatch).
func TestRecordServedQualityGate(t *testing.T) {
	b := newTrustBroker()
	b.successCount = map[string]int{}
	b.concurrentTPS = map[string]float64{}

	b.recordServed("n", true, 100, 1) // quality OK, but solo (inflight 1)
	if b.successCount["n"] != 1 {
		t.Errorf("quality success not counted: %d", b.successCount["n"])
	}
	if b.concurrentTPS["n"] != 0 {
		t.Errorf("solo request must not record concurrentTPS, got %v", b.concurrentTPS["n"])
	}
	b.recordServed("n", false, 200, 3) // NOT quality (empty body), under load
	if b.successCount["n"] != 1 {
		t.Errorf("non-quality completion must not increment successCount, got %d", b.successCount["n"])
	}
	if b.concurrentTPS["n"] != 200 {
		t.Errorf("under-load TPS should be recorded regardless of quality, got %v", b.concurrentTPS["n"])
	}
}

// TestColdStartExploration: a fresh canary-passed node gets a non-zero, DECAYING UCB
// radius; the radius shrinks as evidence (N) accumulates and is self-extinguishing.
func TestColdStartExploration(t *testing.T) {
	const total = 1000
	cold := ucbRadius(0.35, total, 0, 1, 0)  // just one probe of evidence
	warm := ucbRadius(0.35, total, 5, 5, 20) // lots of served evidence
	if cold <= 0 {
		t.Fatal("a fresh canary-passed node must get a non-zero exploration radius")
	}
	if warm >= cold {
		t.Errorf("radius must DECAY with evidence: cold=%.3f warm=%.3f", cold, warm)
	}
	// successCount is weighted 3x in N, so the SAME raw count of served requests shrinks
	// the radius more than that many probes (served traffic is stronger evidence).
	rProbe := ucbRadius(0.35, total, 0, 3, 0)  // 3 probes -> N=3
	rServed := ucbRadius(0.35, total, 0, 0, 3) // 3 served -> N=9 (3x weight)
	if rServed >= rProbe {
		t.Errorf("served evidence (3x weight) should shrink radius more than equal probes: served=%.3f probe=%.3f", rServed, rProbe)
	}
	// C=0 disables exploration entirely (the deterministic/legacy path).
	if r := ucbRadius(0, total, 0, 0, 0); r != 0 {
		t.Errorf("C=0 radius = %.3f, want 0", r)
	}
}

// TestLoadSpreadNoAllToOne: under many concurrent picks, power-of-two-choices spreads
// load across equally-good nodes instead of piling every request onto one. No node
// should take more than ~60% of the traffic (a deterministic top-1 would take 100%).
func TestLoadSpreadNoAllToOne(t *testing.T) {
	now := time.Now()
	b := routeBroker(now, map[string]protocol.NodeRegistration{
		"x": {NodeID: "x", Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 0.3, PriceOut: 0.3}}},
		"y": {NodeID: "y", Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 0.3, PriceOut: 0.3}}},
		"z": {NodeID: "z", Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 0.3, PriceOut: 0.3}}},
	})
	b.successCount = map[string]int{}
	b.concurrentTPS = map[string]float64{}
	// Three identical, healthy, verified nodes.
	for _, id := range []string{"x", "y", "z"} {
		b.tps[id] = 200
		b.trust[id] = trustState{probed: true, probeOK: true, ttftMs: 200}
	}
	counts := map[string]int{}
	for i := 0; i < 600; i++ {
		// A distinct seed per request (mimics distinct request ids) so P2C actually
		// spreads; without a seed pick() is deterministic top-1.
		req := pickReq{rng: rand.New(rand.NewSource(int64(i)))}
		b.mu.Lock()
		n, _, ok := b.pickFor("m", false, 0, 0, 0, "", nil, nil, nil, req)
		b.mu.Unlock()
		if !ok {
			t.Fatal("pick found nothing")
		}
		counts[n.NodeID]++
	}
	for id, c := range counts {
		frac := float64(c) / 600
		if frac > 0.6 {
			t.Errorf("node %q took %.0f%% of traffic, want spread (<60%%) - all-to-one not fixed", id, frac*100)
		}
	}
	if len(counts) < 3 {
		t.Errorf("only %d of 3 nodes received traffic - load did not spread", len(counts))
	}
}

// TestDeterministicWithoutSeed: pick() with no rng (the default/legacy path) is
// deterministic - the same candidate set always routes to the same node, so existing
// callers/tests are stable.
func TestDeterministicWithoutSeed(t *testing.T) {
	now := time.Now()
	mk := func() *broker {
		b := routeBroker(now, map[string]protocol.NodeRegistration{
			"p": {NodeID: "p", Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 0.3, PriceOut: 0.3}}},
			"q": {NodeID: "q", Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 0.2, PriceOut: 0.2}}},
		})
		b.successCount = map[string]int{}
		b.concurrentTPS = map[string]float64{}
		b.tps["p"], b.tps["q"] = 200, 200
		b.trust["p"] = trustState{probed: true, probeOK: true, ttftMs: 200}
		b.trust["q"] = trustState{probed: true, probeOK: true, ttftMs: 200}
		return b
	}
	var first string
	for i := 0; i < 20; i++ {
		b := mk()
		b.mu.Lock()
		n, _, ok := b.pick("m", false, 0, 0, 0, "", nil, nil, nil)
		b.mu.Unlock()
		if !ok {
			t.Fatal("pick found nothing")
		}
		if i == 0 {
			first = n.NodeID
		} else if n.NodeID != first {
			t.Fatalf("deterministic pick diverged: %q vs %q", n.NodeID, first)
		}
	}
}

// TestTwoTierHealthGate: a probeFails>=2 node is Tier B and never beats a Tier-A
// node; it is selected only when Tier A is empty (availability floor).
func TestTwoTierHealthGate(t *testing.T) {
	now := time.Now()
	b := twoNodeBroker(now, 0.5, 0.1) // a healthy/pricier, b cheap but failing
	b.tps["a"], b.tps["b"] = 100, 300
	b.trust["a"] = trustState{probed: true, probeOK: true, ttftMs: 300}
	b.trust["b"] = trustState{probed: true, probeOK: false, probeFails: 2, ttftMs: 100}

	b.mu.Lock()
	n, _, ok := b.pick("m", false, 0, 0, 0, "", nil, nil, nil)
	b.mu.Unlock()
	if !ok || n.NodeID != "a" {
		t.Errorf("pick = %q, want a (Tier-A beats Tier-B failing)", n.NodeID)
	}
	// Remove the healthy node: the failing node is the last resort.
	b.mu.Lock()
	delete(b.nodes, "a")
	n2, _, ok2 := b.pick("m", false, 0, 0, 0, "", nil, nil, nil)
	b.mu.Unlock()
	if !ok2 || n2.NodeID != "b" {
		t.Errorf("last-resort pick = %q ok=%v, want b", n2.NodeID, ok2)
	}
}
