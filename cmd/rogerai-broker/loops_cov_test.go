package main

import (
	"crypto/ed25519"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// loopBroker builds a fully-wired broker (single-instance: no shared layer, no
// background goroutines) for driving the daemon ticker loops with a stop seam.
func loopBroker(t *testing.T) *broker {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ROGERAI_REDIS_URL", "")
	t.Setenv("ROGERAI_MULTI_INSTANCE", "")
	_, priv, _ := ed25519.GenerateKey(nil)
	return buildBroker(store.NewMem(), priv, 0.30, 100, time.Hour)
}

// awaitReturn fails if the loop goroutine does not return within a short grace.
func awaitReturn(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s did not return after stop was closed", what)
	}
}

// TestProberLoopTicksThenStops covers the prober daemon: a tiny interval fires probeOnce
// (the worker branch via the ticker case), and closing stop returns the loop.
func TestProberLoopTicksThenStops(t *testing.T) {
	b := loopBroker(t)
	b.probe.interval = time.Millisecond
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { b.proberLoop(stop); close(done) }()

	// Wait until at least one probe round actually fired (the t.C worker branch).
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadUint64(&b.probe.round) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("probeOnce never fired on the tiny interval")
		}
		time.Sleep(time.Millisecond)
	}
	close(stop)
	awaitReturn(t, done, "proberLoop")
}

// TestReattestSweepStops covers the re-attest daemon: ttl<=0 returns immediately
// (disabled), and with a positive ttl a closed stop returns the loop (ticker setup +
// select + stop case). The per-tick worker (expireStaleAttestations) is covered
// separately in TestExpireStaleAttestations.
func TestReattestSweepStops(t *testing.T) {
	// Disabled (ttl<=0) returns at once.
	b0 := loopBroker(t)
	b0.attest.reattestTTL = 0
	doneDisabled := make(chan struct{})
	go func() { b0.reattestSweep(nil); close(doneDisabled) }()
	awaitReturn(t, doneDisabled, "reattestSweep(disabled)")

	// Enabled: a closed stop halts the loop.
	b := loopBroker(t)
	b.attest.reattestTTL = time.Hour
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { b.reattestSweep(stop); close(done) }()
	close(stop)
	awaitReturn(t, done, "reattestSweep")
}

// TestPruneStaleNodesSweepRuns covers the node-prune daemon end to end: a sub-ms grace +
// interval let the first pass + a steady tick fire, pruning a node offline past the TTL,
// then a closed stop returns the loop. The disabled (TTL<=0) early return is covered too.
func TestPruneStaleNodesSweepRuns(t *testing.T) {
	// Disabled path.
	defer func(ttl time.Duration, g, iv time.Duration) {
		staleNodeTTL, pruneStaleGrace, pruneStaleInterval = ttl, g, iv
	}(staleNodeTTL, pruneStaleGrace, pruneStaleInterval)

	staleNodeTTL = 0
	bDis := loopBroker(t)
	doneDis := make(chan struct{})
	go func() { bDis.pruneStaleNodesSweep(nil); close(doneDis) }()
	awaitReturn(t, doneDis, "pruneStaleNodesSweep(disabled)")

	// Enabled: fast grace + interval so the prune pass actually runs.
	staleNodeTTL = time.Hour
	pruneStaleGrace = time.Millisecond
	pruneStaleInterval = time.Millisecond
	b := loopBroker(t)
	b.nodes["dead"] = protocol.NodeRegistration{NodeID: "dead"}
	b.lastSeen["dead"] = time.Now().Add(-2 * time.Hour) // offline past the TTL
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { b.pruneStaleNodesSweep(stop); close(done) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		b.mu.Lock()
		_, present := b.nodes["dead"]
		b.mu.Unlock()
		if !present {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the stale node was never pruned by the sweep")
		}
		time.Sleep(time.Millisecond)
	}
	close(stop)
	awaitReturn(t, done, "pruneStaleNodesSweep")
}

// TestSyncLivenessLoopMerges covers the liveness sync daemon: with a shared backend and a
// tiny tick, the ticker case runs syncLivenessOnce (a peer's fresher last_seen is merged),
// then a closed stop returns the loop.
func TestSyncLivenessLoopMerges(t *testing.T) {
	defer func(d time.Duration) { syncTickInterval = d }(syncTickInterval)
	syncTickInterval = time.Millisecond

	now := time.Now()
	b := loopBroker(t)
	b.lastSeen["peer"] = now.Add(-time.Hour)
	b.shared = &fakeShared{memStore: newMemStore(), live: map[string]time.Time{"peer": now}}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { b.syncLiveness(stop); close(done) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		b.mu.Lock()
		merged := b.lastSeen["peer"].Equal(now)
		b.mu.Unlock()
		if merged {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("syncLivenessOnce never merged the peer last_seen")
		}
		time.Sleep(time.Millisecond)
	}
	close(stop)
	awaitReturn(t, done, "syncLiveness")
}

// TestSyncLivenessLoopExitsWhenSharedNil covers the in-loop guard: a tick with no shared
// backend returns the loop on its own (no stop needed).
func TestSyncLivenessLoopExitsWhenSharedNil(t *testing.T) {
	defer func(d time.Duration) { syncTickInterval = d }(syncTickInterval)
	syncTickInterval = time.Millisecond
	b := loopBroker(t) // b.shared is nil
	done := make(chan struct{})
	go func() { b.syncLiveness(nil); close(done) }()
	awaitReturn(t, done, "syncLiveness(no shared)")
}

// TestSyncInflightLoopMerges covers the inflight sync daemon: under multi-instance with a
// shared backend and a tiny tick, the ticker case runs mergeSharedInflight (peer inflight
// is copied into peerInflight), then a closed stop returns the loop.
func TestSyncInflightLoopMerges(t *testing.T) {
	defer func(d time.Duration) { syncTickInterval = d }(syncTickInterval)
	syncTickInterval = time.Millisecond

	b := loopBroker(t)
	b.multiInstance = true
	b.instanceID = "inst-1"
	b.peerInflight = map[string]int{}
	b.shared = &fakeShared{memStore: newMemStore(), inflight: map[string]int{"node-a": 5}}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { b.syncInflight(stop); close(done) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		b.metricsMu.Lock()
		got := b.peerInflight["node-a"]
		b.metricsMu.Unlock()
		if got == 5 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("mergeSharedInflight never copied the peer inflight snapshot")
		}
		time.Sleep(time.Millisecond)
	}
	close(stop)
	awaitReturn(t, done, "syncInflight")
}

// TestSyncInflightLoopExitsWhenNotMulti covers the in-loop guard: a tick that is not in
// multi-instance mode returns the loop on its own.
func TestSyncInflightLoopExitsWhenNotMulti(t *testing.T) {
	defer func(d time.Duration) { syncTickInterval = d }(syncTickInterval)
	syncTickInterval = time.Millisecond
	b := loopBroker(t) // multiInstance false, shared nil
	done := make(chan struct{})
	go func() { b.syncInflight(nil); close(done) }()
	awaitReturn(t, done, "syncInflight(not multi)")
}

// TestNodeBanSweepLoopStops covers the node-ban auto-lift daemon's enabled startup +
// shutdown: with a positive window it builds the ticker and a closed stop returns the loop
// (its sweep interval is floored at 1h, so the worker is covered via TestNodeBanSweepOnce).
func TestNodeBanSweepLoopStops(t *testing.T) {
	b := &broker{db: store.NewMem(), nodeBanDays: 1, banned: map[string]bool{}}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { b.nodeBanSweep(stop); close(done) }()
	close(stop)
	awaitReturn(t, done, "nodeBanSweep")
}

// TestRecountHoldSweepLoopStops covers the recount-hold auto-expiry daemon's enabled
// startup + shutdown: with a positive window it builds the ticker and a closed stop returns
// the loop (interval floored at 1h; the worker is covered via TestRecountHoldSweepOnce).
func TestRecountHoldSweepLoopStops(t *testing.T) {
	b := &broker{db: store.NewMem(), recountHoldDays: 1}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { b.recountHoldSweep(stop); close(done) }()
	close(stop)
	awaitReturn(t, done, "recountHoldSweep")
}

// TestReversalRetrySweepStops covers the reversal-retry daemon: a nil store returns at
// once, and with a store a closed stop returns the loop (the 5-minute tick is never hit;
// reversalRetryOnce is covered separately).
func TestReversalRetrySweepStops(t *testing.T) {
	b := &broker{db: store.NewMem()}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { b.reversalRetrySweep(stop); close(done) }()
	close(stop)
	awaitReturn(t, done, "reversalRetrySweep")
}
