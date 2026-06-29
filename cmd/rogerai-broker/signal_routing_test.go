package main

import (
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/client"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// routeBroker builds a broker with ALL the per-node metric maps initialised so the
// composite pick has somewhere to read tps / inflight / success / successCount /
// concurrentTPS / trust from (the score reads successCount + concurrentTPS too, so they
// are seeded here rather than re-added per suite).
func routeBroker(now time.Time, nodes map[string]protocol.NodeRegistration) *broker {
	b := &broker{
		nodes:         nodes,
		lastSeen:      map[string]time.Time{},
		confidential:  map[string]bool{},
		private:       map[string]bool{},
		banned:        map[string]bool{},
		tps:           map[string]float64{},
		inflight:      map[string]int{},
		success:       map[string]float64{},
		successCount:  map[string]int{},
		concurrentTPS: map[string]float64{},
		trust:         map[string]trustState{},
	}
	for id := range nodes {
		b.lastSeen[id] = now
	}
	return b
}

// TestComputeSignalProbedFastBeatsSlow: at EQUAL price + providers, a probed-fast,
// low-latency, verified node scores materially above a probed-slow / high-latency
// one. Idle bands now differentiate on measured evidence, not just supply.
func TestComputeSignalProbedFastBeatsSlow(t *testing.T) {
	fast := computeSignal(signalInput{
		providers: 1, bestTPS: 280, ttftMs: 150, successRate: 0.9,
		trust: 1, recency: 1, verified: true,
	}).Total
	slow := computeSignal(signalInput{
		providers: 1, bestTPS: 25, ttftMs: 1800, successRate: 0.9,
		trust: 1, recency: 1, verified: true,
	}).Total
	unprobed := computeSignal(signalInput{
		providers: 1, successRate: successFor(0, false, false), trust: 1, recency: 1,
	}).Total
	if fast <= slow {
		t.Errorf("probed-fast (%d) should outscore probed-slow (%d)", fast, slow)
	}
	if fast <= unprobed {
		t.Errorf("probed-fast verified (%d) should outscore unverified idle (%d)", fast, unprobed)
	}
}

// TestPickProbedFastBeatsSlowAtEqualPrice: with identical price + providers, pick
// routes to the node with the higher measured composite (faster + verified).
func TestPickProbedFastBeatsSlowAtEqualPrice(t *testing.T) {
	now := time.Now()
	b := routeBroker(now, map[string]protocol.NodeRegistration{
		"fast": {NodeID: "fast", Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 0.5, PriceOut: 0.5}}},
		"slow": {NodeID: "slow", Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 0.5, PriceOut: 0.5}}},
	})
	b.tps["fast"], b.tps["slow"] = 280, 25
	b.trust["fast"] = trustState{probed: true, probeOK: true, ttftMs: 150}
	b.trust["slow"] = trustState{probed: true, probeOK: true, ttftMs: 1800}

	b.mu.Lock()
	n, _, ok := b.pickFor("m", false, 0, 0, 0, "", nil, nil, nil, pickReq{})
	b.mu.Unlock()
	if !ok || n.NodeID != "fast" {
		t.Errorf("pick = %q ok=%v, want fast (higher composite at equal price)", n.NodeID, ok)
	}
}

// TestPickHigherCompositeWithinPriceTolerance: when prices are within tolerance, the
// higher-composite (faster/verified) node wins even if it is a touch pricier. (Here
// "fast" is slightly more expensive but much faster.)
func TestPickHigherCompositeWithinPriceTolerance(t *testing.T) {
	now := time.Now()
	b := routeBroker(now, map[string]protocol.NodeRegistration{
		"fast":  {NodeID: "fast", Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 0.50, PriceOut: 0.50}}},
		"cheap": {NodeID: "cheap", Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 0.49, PriceOut: 0.49}}},
	})
	b.tps["fast"], b.tps["cheap"] = 290, 20
	b.trust["fast"] = trustState{probed: true, probeOK: true, ttftMs: 120}
	b.trust["cheap"] = trustState{probed: true, probeOK: true, ttftMs: 1900}

	b.mu.Lock()
	n, _, ok := b.pickFor("m", false, 0, 0, 0, "", nil, nil, nil, pickReq{})
	b.mu.Unlock()
	if !ok || n.NodeID != "fast" {
		t.Errorf("pick = %q ok=%v, want fast (higher value-per-credit within price tolerance)", n.NodeID, ok)
	}
}

// TestPickLoadTieBreakSpreads: two nodes equal on price + quality, the LEAST in-flight
// one wins so concurrent traffic spreads instead of piling onto one node.
func TestPickLoadTieBreakSpreads(t *testing.T) {
	now := time.Now()
	b := routeBroker(now, map[string]protocol.NodeRegistration{
		"busy": {NodeID: "busy", Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 0.3, PriceOut: 0.3}}},
		"idle": {NodeID: "idle", Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 0.3, PriceOut: 0.3}}},
	})
	b.tps["busy"], b.tps["idle"] = 200, 200
	b.trust["busy"] = trustState{probed: true, probeOK: true, ttftMs: 200}
	b.trust["idle"] = trustState{probed: true, probeOK: true, ttftMs: 200}
	b.inflight["busy"] = 5 // busy carries load; idle has none

	b.mu.Lock()
	n, _, ok := b.pickFor("m", false, 0, 0, 0, "", nil, nil, nil, pickReq{})
	b.mu.Unlock()
	if !ok || n.NodeID != "idle" {
		t.Errorf("pick = %q ok=%v, want idle (least-loaded tie-break)", n.NodeID, ok)
	}
}

// TestPickFailingDeprioritized: a probe-failing node loses to a healthy one even when
// the failing node is cheaper AND faster (health is an absolute gate). It is still
// chosen if it is the only node serving the model.
func TestPickFailingDeprioritized(t *testing.T) {
	now := time.Now()
	b := routeBroker(now, map[string]protocol.NodeRegistration{
		"healthy": {NodeID: "healthy", Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 1.0, PriceOut: 1.0}}},
		"failing": {NodeID: "failing", Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 0.1, PriceOut: 0.1}}},
	})
	b.tps["healthy"], b.tps["failing"] = 100, 300 // failing is even faster
	b.trust["healthy"] = trustState{probed: true, probeOK: true, ttftMs: 300}
	b.trust["failing"] = trustState{probed: true, probeOK: false, probeFails: 4, ttftMs: 100}

	b.mu.Lock()
	n, _, ok := b.pickFor("m", false, 0, 0, 0, "", nil, nil, nil, pickReq{})
	b.mu.Unlock()
	if !ok || n.NodeID != "healthy" {
		t.Errorf("pick = %q ok=%v, want healthy (failing deprioritized despite cheaper+faster)", n.NodeID, ok)
	}

	// Failing is still served when it is the only option (availability).
	b.mu.Lock()
	delete(b.nodes, "healthy")
	n2, _, ok2 := b.pickFor("m", false, 0, 0, 0, "", nil, nil, nil, pickReq{})
	b.mu.Unlock()
	if !ok2 || n2.NodeID != "failing" {
		t.Errorf("pick = %q ok=%v, want failing as last resort", n2.NodeID, ok2)
	}
}

// TestBrokerAndClientPickAgree: the broker's pick and the client's pickAlternative
// converge on the SAME best offer (price + measured composite), so normal routing and
// failover routing do not disagree on "best".
func TestBrokerAndClientPickAgree(t *testing.T) {
	now := time.Now()
	// Three offers at varied price + speed. Build the broker side.
	b := routeBroker(now, map[string]protocol.NodeRegistration{
		"a": {NodeID: "a", Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 0.40, PriceOut: 0.40}}},
		"b": {NodeID: "b", Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 0.20, PriceOut: 0.20}}},
		"c": {NodeID: "c", Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 0.50, PriceOut: 0.50}}},
	})
	b.tps = map[string]float64{"a": 120, "b": 280, "c": 60}
	b.trust["a"] = trustState{probed: true, probeOK: true, ttftMs: 300}
	b.trust["b"] = trustState{probed: true, probeOK: true, ttftMs: 150}
	b.trust["c"] = trustState{probed: true, probeOK: true, ttftMs: 600}

	b.mu.Lock()
	bn, _, ok := b.pickFor("m", false, 0, 0, 0, "", nil, nil, nil, pickReq{})
	b.mu.Unlock()
	if !ok {
		t.Fatal("broker pick found nothing")
	}

	// Build the client-side offer list from the SAME truth: per-offer signal is the
	// broker composite the client ranks on. Mirror discover's per-offer signal.
	mkOffer := func(id string, price float64) client.Offer {
		tq := b.trust[id]
		sig := offerSignal(true, b.inflight[id], b.tps[id], tq.ttftMs,
			successFor(b.success[id], false, tq.verifiedServing()), tq.score(), 1, tq.verifiedServing())
		return client.Offer{
			NodeID: id, Model: "m", PriceIn: price, PriceOut: price, Online: true,
			TPS: b.tps[id], TTFTMs: tq.ttftMs, Verified: tq.verifiedServing(), Signal: sig,
		}
	}
	offers := []client.Offer{
		mkOffer("a", 0.40), mkOffer("b", 0.20), mkOffer("c", 0.50),
	}
	cid, cok := client.PickBest(offers, "m")
	if !cok {
		t.Fatal("client pick found nothing")
	}
	if cid != bn.NodeID {
		t.Errorf("broker pick %q != client pick %q (selectors must agree on best)", bn.NodeID, cid)
	}
}

// TestNextCanaryRotates: the canary fingerprint changes round to round and cycles
// through the whole set (a node cannot hard-code a single answer).
func TestNextCanaryRotates(t *testing.T) {
	n := len(canaryFingerprints)
	if n < 2 {
		t.Fatal("need at least two fingerprints to rotate")
	}
	seen := map[string]bool{}
	for r := 0; r < n; r++ {
		fp := nextCanary(uint64(r))
		if fp.prompt == "" || fp.expect == "" {
			t.Fatalf("round %d: empty fingerprint", r)
		}
		seen[fp.prompt] = true
	}
	if len(seen) != n {
		t.Errorf("rotation covered %d/%d fingerprints over a full cycle", len(seen), n)
	}
	// Consecutive rounds differ.
	if nextCanary(0).prompt == nextCanary(1).prompt {
		t.Error("consecutive rounds should use different fingerprints")
	}
	// Wraps around deterministically.
	if nextCanary(0).prompt != nextCanary(uint64(n)).prompt {
		t.Error("rotation should wrap around the set")
	}
}

// TestProbeDefaultsEnabled: the probe is ON by default (30s) and explicitly OFF when
// ROGERAI_PROBE_INTERVAL=0.
func TestProbeDefaultsEnabled(t *testing.T) {
	t.Setenv("ROGERAI_PROBE_INTERVAL", "")
	t.Setenv("ROGERAI_PROBE_PER_OWNER", "")
	c := loadProbe()
	if !c.enabled() {
		t.Error("probe should be ENABLED by default")
	}
	if c.interval != defaultProbeInterval {
		t.Errorf("default interval = %s want %s", c.interval, defaultProbeInterval)
	}
	if c.perOwner != defaultProbePerOwner {
		t.Errorf("default per-owner = %d want %d", c.perOwner, defaultProbePerOwner)
	}
	t.Setenv("ROGERAI_PROBE_INTERVAL", "0")
	if loadProbe().enabled() {
		t.Error("ROGERAI_PROBE_INTERVAL=0 must DISABLE the probe")
	}
}

// TestProbeJitterWindow: the per-round jitter window never exceeds half the interval
// (so it cannot bleed into the next round) and is capped at probeJitter.
func TestProbeJitterWindow(t *testing.T) {
	if w := (probeConfig{interval: time.Second}).jitterWindow(); w != 500*time.Millisecond {
		t.Errorf("1s interval jitter = %s want 500ms (interval/2)", w)
	}
	if w := (probeConfig{interval: time.Minute}).jitterWindow(); w != probeJitter {
		t.Errorf("60s interval jitter = %s want %s (capped)", w, probeJitter)
	}
}

// TestProbeOncePerOwnerBudget: with a per-owner cap, only that many of a single
// owner's many nodes are probed in one round (the rest are covered on later rounds via
// rotation), so a big multi-node owner is sampled, not hammered.
func TestProbeOncePerOwnerBudget(t *testing.T) {
	now := time.Now()
	mem := store.NewMem()
	b := newTrustBroker()
	b.db = mem
	b.probe = probeConfig{interval: time.Second, perOwner: 2}
	b.nodes = map[string]protocol.NodeRegistration{}
	// 5 nodes, all owned by the same account "owner1".
	ids := []string{"n1", "n2", "n3", "n4", "n5"}
	for _, id := range ids {
		b.nodes[id] = protocol.NodeRegistration{NodeID: id, Offers: []protocol.ModelOffer{{Model: "m"}}}
		b.lastSeen[id] = now
		_ = mem.BindNode(id, "owner1")
		b.tunnels[id] = &nodeTunnel{jobs: make(chan protocol.Job, 4), waiters: map[string]chan protocol.JobResult{}}
	}

	b.probeOnce()

	// Give the jittered goroutines time to enqueue (jitter window = interval/2 = 500ms).
	deadline := time.Now().Add(2 * time.Second)
	count := func() int {
		n := 0
		for _, id := range ids {
			n += len(b.tunnels[id].jobs)
		}
		return n
	}
	for count() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	// Wait a touch longer to ensure no EXTRA probes sneak in beyond the cap.
	time.Sleep(700 * time.Millisecond)
	if got := count(); got != 2 {
		t.Errorf("probed %d of one owner's 5 nodes, want exactly the per-owner cap (2)", got)
	}
}
