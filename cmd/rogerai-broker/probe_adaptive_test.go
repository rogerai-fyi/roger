package main

import (
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// adaptiveBroker is a minimal broker with the per-node metric maps + the adaptive
// probe schedule initialised, and the probe enabled at the given floor/ceiling. It is
// enough to exercise the scheduler/demand/staleness logic WITHOUT the proberLoop
// goroutine or a real tunnel.
func adaptiveBroker(floor, ceiling time.Duration) *broker {
	return &broker{
		nodes:        map[string]protocol.NodeRegistration{},
		lastSeen:     map[string]time.Time{},
		confidential: map[string]bool{},
		private:      map[string]bool{},
		banned:       map[string]bool{},
		tps:          map[string]float64{},
		inflight:     map[string]int{},
		success:      map[string]float64{},
		trust:        map[string]trustState{},
		probeSched:   map[string]*probeState{},
		probe:        probeConfig{interval: floor, ceiling: ceiling, perOwner: 0},
	}
}

// TestBackoffSchedule: the per-node interval walks floor -> doubling -> ceiling.
func TestBackoffSchedule(t *testing.T) {
	c := probeConfig{interval: 30 * time.Second, ceiling: 15 * time.Minute}
	want := []time.Duration{
		30 * time.Second, // lvl 0 = floor
		60 * time.Second, // lvl 1
		2 * time.Minute,  // lvl 2
		4 * time.Minute,  // lvl 3
		8 * time.Minute,  // lvl 4
		15 * time.Minute, // lvl 5 would be 16m -> clamped to ceiling
		15 * time.Minute, // lvl 6 stays at ceiling
		15 * time.Minute, // far-out level stays at ceiling
	}
	for lvl, w := range want {
		if got := c.backoffInterval(lvl); got != w {
			t.Errorf("backoffInterval(%d) = %s, want %s", lvl, got, w)
		}
	}
	// A ceiling below the floor (loadProbe clamps it, but defend the math too) never
	// drops below the floor.
	c2 := probeConfig{interval: time.Minute, ceiling: time.Minute}
	if got := c2.backoffInterval(5); got != time.Minute {
		t.Errorf("no-room backoff = %s, want floor 1m", got)
	}
}

// TestLoadProbeCeiling: ROGERAI_PROBE_CEILING is honored, defaults to 15m, and is
// clamped to be >= the floor.
func TestLoadProbeCeiling(t *testing.T) {
	t.Setenv("ROGERAI_PROBE_INTERVAL", "30")
	t.Setenv("ROGERAI_PROBE_CEILING", "")
	if c := loadProbe(); c.ceiling != defaultProbeCeiling {
		t.Errorf("default ceiling = %s, want %s", c.ceiling, defaultProbeCeiling)
	}
	t.Setenv("ROGERAI_PROBE_CEILING", "600") // 10m
	if c := loadProbe(); c.ceiling != 10*time.Minute {
		t.Errorf("ceiling = %s, want 10m", c.ceiling)
	}
	// Ceiling below the floor is clamped UP to the floor (no negative backoff room).
	t.Setenv("ROGERAI_PROBE_INTERVAL", "120")
	t.Setenv("ROGERAI_PROBE_CEILING", "30")
	if c := loadProbe(); c.ceiling != 120*time.Second {
		t.Errorf("sub-floor ceiling = %s, want clamped to floor 120s", c.ceiling)
	}
}

// TestIdleNodeBacksOffFloorToCeiling: an idle node's effective probe interval (the gap
// between successive due-times) doubles each round it is probed without traffic, up to
// the ceiling. We drive probeOnce-style scheduling by calling the same advance the
// loop does, asserting the due-time gaps grow floor -> 2x -> ... -> ceiling.
func TestIdleNodeBacksOffFloorToCeiling(t *testing.T) {
	floor, ceiling := 30*time.Second, 4*time.Minute
	b := adaptiveBroker(floor, ceiling)
	id := "n1"

	now := time.Now()
	// Round 0: first sight => due immediately (floor). Simulate the loop advancing the
	// backoff for a probed node.
	advance := func(now time.Time) time.Time {
		b.metricsMu.Lock()
		st := b.probeSched[id]
		if st == nil {
			st = &probeState{}
			b.probeSched[id] = st
		}
		due := now.Add(b.probe.backoffInterval(st.backoff))
		gap := due.Sub(now)
		st.nextDue = due
		st.backoff++
		b.metricsMu.Unlock()
		return now.Add(gap) // jump to when it is next due
	}

	wantGaps := []time.Duration{floor, 2 * floor, 4 * floor, ceiling, ceiling}
	cur := now
	for i, want := range wantGaps {
		b.metricsMu.Lock()
		st := b.probeSched[id]
		var lvl int
		if st != nil {
			lvl = st.backoff
		}
		gap := b.probe.backoffInterval(lvl)
		b.metricsMu.Unlock()
		if gap != want {
			t.Errorf("round %d effective interval = %s, want %s", i, gap, want)
		}
		cur = advance(cur)
	}
}

// TestProbeOnceSkipsBackedOffNode: probeOnce only probes nodes whose next-due has
// arrived; a backed-off node (next-due in the future) is skipped, a due node is
// probed and re-scheduled with a longer interval.
func TestProbeOnceSkipsBackedOffNode(t *testing.T) {
	b := adaptiveBroker(30*time.Second, 15*time.Minute)
	b.nodes["due"] = protocol.NodeRegistration{NodeID: "due", Offers: []protocol.ModelOffer{{Model: "m"}}}
	b.nodes["backed"] = protocol.NodeRegistration{NodeID: "backed", Offers: []protocol.ModelOffer{{Model: "m"}}}
	now := time.Now()
	b.lastSeen["due"] = now
	b.lastSeen["backed"] = now
	// "backed" is not due for another 10 minutes; "due" has never been scheduled.
	b.probeSched["backed"] = &probeState{nextDue: now.Add(10 * time.Minute), backoff: 5}

	// No tunnels exist, so probeNode is a no-op for the actual inference; we only assert
	// the SCHEDULING decisions probeOnce makes.
	b.tunnels = map[string]*nodeTunnel{}
	b.probeOnce()

	// "due" got scheduled forward (backoff advanced from 0 to 1, next-due ~ floor out).
	b.metricsMu.Lock()
	dueSt := b.probeSched["due"]
	backedSt := b.probeSched["backed"]
	b.metricsMu.Unlock()
	if dueSt == nil || dueSt.backoff != 1 {
		t.Errorf("due node backoff = %v, want advanced to 1", dueSt)
	}
	if dueSt.nextDue.Before(now.Add(20 * time.Second)) {
		t.Errorf("due node next-due not pushed out: %s", dueSt.nextDue.Sub(now))
	}
	// "backed" was skipped: its schedule is untouched (still 10m out, backoff 5).
	if backedSt.backoff != 5 || !backedSt.nextDue.Equal(now.Add(10*time.Minute)) {
		t.Errorf("backed node schedule mutated: backoff=%d due=+%s", backedSt.backoff, backedSt.nextDue.Sub(now))
	}
}

// TestMarkMeasuredResetsBackoff: a real served request (markMeasured) resets the probe
// backoff to the floor, pushes the next probe out, and stamps lastMeasured so the node
// reads as freshly verified.
func TestMarkMeasuredResetsBackoff(t *testing.T) {
	b := adaptiveBroker(30*time.Second, 15*time.Minute)
	id := "served"
	now := time.Now()
	b.probeSched[id] = &probeState{nextDue: now, backoff: 6} // deeply backed off

	b.markMeasured(id)

	b.metricsMu.Lock()
	st := b.probeSched[id]
	b.metricsMu.Unlock()
	if st.backoff != 0 {
		t.Errorf("backoff after real traffic = %d, want 0 (reset)", st.backoff)
	}
	if st.nextDue.Before(now.Add(20 * time.Second)) {
		t.Errorf("next-due not extended past ~floor after real traffic: +%s", st.nextDue.Sub(now))
	}
	if st.lastMeasured.IsZero() {
		t.Error("lastMeasured not stamped on a real measurement")
	}
	// And the freshly-measured node now reads at full staleness confidence.
	b.metricsMu.Lock()
	conf := b.measurementStalenessLocked(id, time.Now())
	b.metricsMu.Unlock()
	if conf != 1.0 {
		t.Errorf("freshly-measured confidence = %.2f, want 1.0", conf)
	}
}

// TestDemandSchedulesNearTermProbe: a demand touch on a stale/backed-off node pulls its
// next-due back toward the floor and resets the backoff level.
func TestDemandSchedulesNearTermProbe(t *testing.T) {
	b := adaptiveBroker(30*time.Second, 15*time.Minute)
	id := "browsed"
	now := time.Now()
	b.probeSched[id] = &probeState{nextDue: now.Add(14 * time.Minute), backoff: 6}

	b.demandProbeSoon(id)

	b.metricsMu.Lock()
	st := b.probeSched[id]
	b.metricsMu.Unlock()
	if st.backoff != 0 {
		t.Errorf("backoff after demand = %d, want 0", st.backoff)
	}
	if st.nextDue.After(now.Add(time.Second)) {
		t.Errorf("next-due not pulled to ~now after demand: +%s", st.nextDue.Sub(now))
	}
}

// TestPickStaleCandidateSchedulesProbe: pick routing to a candidate whose reading is
// STALE marks it for a near-term probe (demand-driven), without blocking the route.
func TestPickStaleCandidateSchedulesProbe(t *testing.T) {
	floor, ceiling := 30*time.Second, 2*time.Minute
	b := adaptiveBroker(floor, ceiling)
	now := time.Now()
	b.nodes["n"] = protocol.NodeRegistration{NodeID: "n", Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 1, PriceOut: 1}}}
	b.lastSeen["n"] = now
	// Last measured well past the ceiling => stale, and currently backed off far out.
	b.probeSched["n"] = &probeState{nextDue: now.Add(time.Hour), backoff: 6, lastMeasured: now.Add(-10 * ceiling)}

	b.mu.Lock()
	got, _, ok := b.pick("m", false, 0, 0, 0, "", nil, nil, nil)
	b.mu.Unlock()
	if !ok || got.NodeID != "n" {
		t.Fatalf("pick = %q ok=%v, want n", got.NodeID, ok)
	}
	// The stale candidate is now scheduled near-term.
	b.metricsMu.Lock()
	st := b.probeSched["n"]
	b.metricsMu.Unlock()
	if st.backoff != 0 || st.nextDue.After(now.Add(time.Second)) {
		t.Errorf("stale pick did not schedule a near-term probe: backoff=%d due=+%s", st.backoff, st.nextDue.Sub(now))
	}
}

// TestPickFreshCandidateNotRescheduled: pick routing to a candidate measured RECENTLY
// leaves its (backed-off) schedule alone - no needless near-term probe.
func TestPickFreshCandidateNotRescheduled(t *testing.T) {
	floor, ceiling := 30*time.Second, 2*time.Minute
	b := adaptiveBroker(floor, ceiling)
	now := time.Now()
	b.nodes["n"] = protocol.NodeRegistration{NodeID: "n", Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 1, PriceOut: 1}}}
	b.lastSeen["n"] = now
	farOut := now.Add(time.Hour)
	b.probeSched["n"] = &probeState{nextDue: farOut, backoff: 6, lastMeasured: now} // fresh

	b.mu.Lock()
	_, _, ok := b.pick("m", false, 0, 0, 0, "", nil, nil, nil)
	b.mu.Unlock()
	if !ok {
		t.Fatal("pick failed")
	}
	b.metricsMu.Lock()
	st := b.probeSched["n"]
	b.metricsMu.Unlock()
	if st.backoff != 6 || !st.nextDue.Equal(farOut) {
		t.Errorf("fresh pick disturbed the schedule: backoff=%d due moved=%v", st.backoff, !st.nextDue.Equal(farOut))
	}
}

// TestStalenessFactorDiscountsAndRestores: a long-unmeasured node gets a MODEST signal
// haircut on its measured terms, a fresh measurement restores full confidence, and the
// discount is gentle (a good idle node is not cratered).
func TestStalenessFactorDiscountsAndRestores(t *testing.T) {
	c := probeConfig{interval: 30 * time.Second, ceiling: 15 * time.Minute}
	// Within the ceiling: full confidence.
	if f := c.stalenessFactor(10 * time.Minute); f != 1.0 {
		t.Errorf("fresh staleness = %.3f, want 1.0", f)
	}
	// Just past the ceiling: a small haircut, still strong.
	mild := c.stalenessFactor(16 * time.Minute)
	if mild >= 1.0 || mild < 0.95 {
		t.Errorf("mildly-stale factor = %.3f, want a small haircut in [0.95,1.0)", mild)
	}
	// Long unmeasured: bottoms out at the gentle 0.7 floor (NOT cratered to 0).
	deep := c.stalenessFactor(time.Hour)
	if deep != 0.7 {
		t.Errorf("deeply-stale factor = %.3f, want floor 0.7", deep)
	}

	// The discount applied to a signal is modest: a fast verified node stays high.
	fresh := computeSignal(signalInput{
		providers: 1, bestTPS: 280, ttftMs: 150, successRate: 0.9, trust: 1,
		recency: 1, verified: true, staleness: 1.0,
	}).Total
	stale := computeSignal(signalInput{
		providers: 1, bestTPS: 280, ttftMs: 150, successRate: 0.9, trust: 1,
		recency: 1, verified: true, staleness: 0.7,
	}).Total
	if stale >= fresh {
		t.Errorf("stale signal (%d) should be below fresh (%d)", stale, fresh)
	}
	if stale < fresh-25 {
		t.Errorf("staleness haircut too harsh: fresh=%d stale=%d (cratered a good idle node)", fresh, stale)
	}
}

// TestStalenessUnmeasuredFullConfidence: a node with NO measurement (or a disabled
// probe) reads at full confidence - there is no probe evidence to discount.
func TestStalenessUnmeasuredFullConfidence(t *testing.T) {
	b := adaptiveBroker(30*time.Second, 15*time.Minute)
	now := time.Now()
	b.metricsMu.Lock()
	never := b.measurementStalenessLocked("never", now)
	b.metricsMu.Unlock()
	if never != 1.0 {
		t.Errorf("never-measured confidence = %.2f, want 1.0", never)
	}
	// Disabled probe: always full confidence.
	off := &broker{probe: probeConfig{interval: 0}, probeSched: map[string]*probeState{}}
	off.metricsMu.Lock()
	v := off.measurementStalenessLocked("x", now)
	off.metricsMu.Unlock()
	if v != 1.0 {
		t.Errorf("probe-off confidence = %.2f, want 1.0", v)
	}
}

// TestProbeOnceSkipsBusyNode: a node currently serving real traffic (in-flight > 0) is
// still skipped by the probe round (unchanged busy-skip), regardless of being due.
func TestProbeOnceSkipsBusyNode(t *testing.T) {
	b := adaptiveBroker(30*time.Second, 15*time.Minute)
	b.nodes["busy"] = protocol.NodeRegistration{NodeID: "busy", Offers: []protocol.ModelOffer{{Model: "m"}}}
	now := time.Now()
	b.lastSeen["busy"] = now
	b.inflight["busy"] = 1
	b.tunnels = map[string]*nodeTunnel{}
	b.probeOnce()
	// A skipped busy node's schedule is untouched (it was never selected as a target).
	b.metricsMu.Lock()
	st := b.probeSched["busy"]
	b.metricsMu.Unlock()
	if st != nil && st.backoff != 0 {
		t.Errorf("busy node should not be scheduled/advanced, got backoff=%d", st.backoff)
	}
}
