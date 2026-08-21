package main

import (
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"rogerai.fm/roger/v5/internal/store"
)

// The shared store these tests run against is the REAL valkeyStore over miniredis, not a map
// that returns what was put in it. That is deliberate and it is the point: every property under
// test here is a property of the SEMANTICS - self-exclusion, per-instance hash fields, key
// namespacing and TTL expiry - and a hand-written fake would have to reproduce all four
// correctly to be worth anything. A fake that got self-exclusion wrong would pass
// TestEdgeLoadIsFleetWide while the real system double-counted, and a fake with no TTL cannot
// fail TestSharedEdgeLoadSurvivesTTL at all.

// miEdgePair builds two broker instances sharing one miniredis, in the multi-instance posture:
// distinct instance ids, the bus flag on, the edge counters initialized as newBroker does.
func miEdgePair(t *testing.T) (*broker, *broker) {
	t.Helper()
	mr := miniredis.RunT(t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	a := newMIBroker(t, brokerPriv, store.NewMem(), mr)
	b := newMIBroker(t, brokerPriv, store.NewMem(), mr)
	for _, x := range []*broker{a, b} {
		x.edgeLoad = map[string]int{}
		x.edgeInflight = map[string]edgeAttemptLoad{}
		x.edgeOpenByAccount = map[string]int{}
		x.peerEdgeLoad = map[string]int{}
	}
	return a, b
}

// TestEdgeLoadIsFleetWide: an edge attempt opened on instance A must be visible to instance B's
// placement math. Before the write-through existed, b.edgeLoad was one process's private map and
// B scored the station as completely free - so both instances independently ranked the busiest
// station highest, which is the magnet the load divisor is supposed to prevent.
func TestEdgeLoadIsFleetWide(t *testing.T) {
	a, b := miEdgePair(t)
	deadline := time.Now().Add(time.Hour)

	a.edgeEnterInflight("att-1", "n1", "u_a", deadline)
	a.edgeEnterInflight("att-2", "n1", "u_a", deadline)

	b.mergeSharedInflight()
	b.metricsMu.Lock()
	peer, total := b.peerEdgeLoad["n1"], b.edgeLoadLocked("n1")
	b.metricsMu.Unlock()
	if peer != 2 {
		t.Errorf("B peerEdgeLoad(n1) = %d after merge, want 2 (A's edge attempts are invisible cross-instance)", peer)
	}
	if total != 2 {
		t.Errorf("B edgeLoadLocked(n1) = %d, want 2 - placement is scoring a busy station as free", total)
	}

	// SELF-EXCLUSION. A must not read its own published count back on top of its exact local
	// one; a merge that double-counted would make every instance look twice as loaded as it is.
	a.mergeSharedInflight()
	a.metricsMu.Lock()
	own := a.edgeLoadLocked("n1")
	a.metricsMu.Unlock()
	if own != 2 {
		t.Errorf("A edgeLoadLocked(n1) = %d after merging its own write-through, want 2 (self must be excluded)", own)
	}

	// Draining on A releases the station everywhere, not just on A.
	a.edgeExitInflight("att-1")
	a.edgeExitInflight("att-2")
	b.mergeSharedInflight()
	b.metricsMu.Lock()
	after := b.edgeLoadLocked("n1")
	b.metricsMu.Unlock()
	if after != 0 {
		t.Errorf("B edgeLoadLocked(n1) = %d after A drained, want 0", after)
	}
}

// TestEdgeLoadStaysOutOfTheClassicHash is the guard on the decision, not on the feature.
//
// b.edgeLoad was split out of b.inflight because b.inflight is EVIDENCE as well as load: pickFor
// divides the paid router's score by inflight+peerInflight, and probeOnce refuses to canary a
// node whose inflight is non-zero. Publishing edge load into the classic shared hash would hand
// that lever back on every instance except the one that opened the attempt - the split would
// hold in one process and dissolve across the fleet, which is the failure mode nobody would
// notice locally. This test asserts on the exact expression pickFor uses.
func TestEdgeLoadStaysOutOfTheClassicHash(t *testing.T) {
	a, b := miEdgePair(t)
	a.edgeEnterInflight("att-1", "n1", "u_a", time.Now().Add(time.Hour))
	b.mergeSharedInflight()

	b.metricsMu.Lock()
	classic := b.inflight["n1"] + b.peerInflight["n1"] // pickFor's capacity-aware load, verbatim
	edge := b.peerEdgeLoad["n1"]
	b.metricsMu.Unlock()

	if classic != 0 {
		t.Errorf("classic router load for n1 = %d, want 0: an EDGE attempt has reached the paid "+
			"fabric's divisor across the instance boundary - the one-way split is broken", classic)
	}
	if edge != 1 {
		t.Errorf("peerEdgeLoad(n1) = %d, want 1 (the attempt must land somewhere)", edge)
	}
}

// TestSharedEdgeLoadSurvivesTTL: a long-lived attempt must not vanish from the peer view.
//
// markEdgeInflight (like markInflight) sets a TTL on the node's hash and is only called when the
// count CHANGES, so before refreshSharedLoad existed an attempt that outlived inflightTTL had
// its published count expire underneath it while the work was still open. That is backwards from
// what a quiescence gate needs: the longest-running work in the fleet is exactly the work that
// disappeared first, so the gate would have concluded "idle" about the busiest Stations.
func TestSharedEdgeLoadSurvivesTTL(t *testing.T) {
	mr := miniredis.RunT(t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	a := newMIBroker(t, brokerPriv, store.NewMem(), mr)
	b := newMIBroker(t, brokerPriv, store.NewMem(), mr)
	a.edgeLoad, a.edgeInflight, a.edgeOpenByAccount = map[string]int{}, map[string]edgeAttemptLoad{}, map[string]int{}

	// An edge grant's execution deadline can be minutes out; inflightTTL is one.
	a.edgeEnterInflight("att-long", "n1", "u_a", time.Now().Add(time.Hour))
	mr.FastForward(inflightTTL + time.Second)

	// A's sync tick republishes what it is still carrying, THEN B reads.
	a.mergeSharedInflight()
	b.mergeSharedInflight()

	b.metricsMu.Lock()
	peer := b.peerEdgeLoad["n1"]
	b.metricsMu.Unlock()
	if peer != 1 {
		t.Errorf("B peerEdgeLoad(n1) = %d past inflightTTL with the attempt still open, want 1 - "+
			"the busiest station went invisible the moment it stayed busy", peer)
	}
}

// edgeBlindStore is a real shared store with ONE read broken, so the degraded-mode split can be
// exercised without breaking anything else about the backend's semantics.
type edgeBlindStore struct {
	sharedStore
	fail bool
}

func (e *edgeBlindStore) edgeInflightByNode(self string) (map[string]int, error) {
	if e.fail {
		return nil, errors.New("valkey unreachable")
	}
	return e.sharedStore.edgeInflightByNode(self)
}

// TestQuiescenceAndRankingDisagreeOnDegradedMode is the heart of the change.
//
// The same failed merge has to mean two opposite things. RANKING keeps the last snapshot,
// because a stale divisor is a better placement input than a fleet that suddenly looks empty.
// The QUIESCENCE gate must refuse, because the action behind it - moving a live Station's relay
// binding - voids a request and refunds a consumer if it is wrong, and "I cannot read the peer
// view" is not "the Station is idle". A single bool that only counts zeros gives both readers the
// same answer, which is how a gate like this gets built wrong.
func TestQuiescenceAndRankingDisagreeOnDegradedMode(t *testing.T) {
	a, b := miEdgePair(t)
	blind := &edgeBlindStore{sharedStore: b.shared}
	b.shared = blind

	a.edgeEnterInflight("att-1", "n1", "u_a", time.Now().Add(time.Hour))
	b.mergeSharedInflight() // clean round: B learns n1 is busy and stamps freshness

	if q, why := b.stationQuiescent("n1"); q {
		t.Errorf("stationQuiescent(n1) = true (%s) while a peer holds a live attempt", why)
	}
	if q, _ := b.stationQuiescent("n-idle"); !q {
		t.Error("stationQuiescent(n-idle) = false on a clean, fresh snapshot with no load anywhere")
	}

	// The peer drains, B merges cleanly once more: n1 is now genuinely idle fleet-wide.
	a.edgeExitInflight("att-1")
	b.mergeSharedInflight()
	if q, why := b.stationQuiescent("n1"); !q {
		t.Fatalf("stationQuiescent(n1) = false (%s) after the peer drained on a clean round", why)
	}

	// Now the shared store goes dark on the edge read. Ranking must keep working off the last
	// snapshot; the gate must stop trusting it once the snapshot ages past peerLoadFreshness.
	blind.fail = true
	a.edgeEnterInflight("att-2", "n1", "u_a", time.Now().Add(time.Hour))
	b.metricsMu.Lock()
	before := b.peerLoadAt
	b.metricsMu.Unlock()
	b.mergeSharedInflight() // dirty round: no swap of the edge map, no freshness stamp

	b.metricsMu.Lock()
	stamped, rankable := b.peerLoadAt, b.edgeLoadLocked("n1")
	b.metricsMu.Unlock()
	// A HALF-SUCCESSFUL ROUND IS NOT A REFRESH. The classic snapshot came back fine, so the
	// ranking maps were updated and ranking keeps working; but the edge read failed, so the
	// freshness stamp must NOT advance. If it advanced, the gate would go on believing a peer
	// view it can no longer read, for as long as the outage lasts.
	if !stamped.Equal(before) {
		t.Errorf("peerLoadAt advanced on a round where the edge snapshot failed (%v -> %v): the "+
			"quiescence gate is now trusting a view it did not actually refresh", before, stamped)
	}
	if rankable < 0 {
		t.Fatalf("edgeLoadLocked returned %d", rankable)
	}

	// Age the last clean stamp past the tolerance and the gate stops answering yes.
	b.metricsMu.Lock()
	b.peerLoadAt = time.Now().Add(-peerLoadFreshness() - time.Second)
	b.metricsMu.Unlock()
	if q, why := b.stationQuiescent("n1"); q {
		t.Errorf("stationQuiescent(n1) = true (%s) on a peer view that is unreadable and stale - "+
			"the gate treated 'I cannot prove it is idle' as 'it is idle'", why)
	}
}

// TestQuiescenceSingleInstanceIsExact: with no peers, this instance's own counters ARE the
// fleet's, so the freshness machinery must be skipped entirely rather than refusing forever on a
// snapshot that will never exist. A single-instance broker never merges anything, so a gate that
// demanded a fresh peer snapshot would answer "not idle" for the life of the process.
func TestQuiescenceSingleInstanceIsExact(t *testing.T) {
	b := &broker{inflight: map[string]int{}, edgeLoad: map[string]int{},
		edgeInflight: map[string]edgeAttemptLoad{}, edgeOpenByAccount: map[string]int{}}

	if q, why := b.stationQuiescent("n1"); !q {
		t.Errorf("stationQuiescent(n1) = false (%s) on an idle single-instance broker", why)
	}
	b.edgeEnterInflight("att-1", "n1", "u_a", time.Now().Add(time.Hour))
	if q, _ := b.stationQuiescent("n1"); q {
		t.Error("stationQuiescent(n1) = true with a local edge attempt open")
	}
	b.edgeExitInflight("att-1")
	b.enterInflight("n1")
	if q, _ := b.stationQuiescent("n1"); q {
		t.Error("stationQuiescent(n1) = true with local CLASSIC work in flight - both fabrics are one machine")
	}
	if q, _ := b.stationQuiescent(""); q {
		t.Error("stationQuiescent(\"\") = true; an unnamed station cannot be proven anything")
	}
}

// TestWriteThroughEdgeLoadIsFreeSingleInstance: the single-instance path must not acquire the
// shared store at all. The guard is asserted by handing a broker a store that panics if touched.
func TestWriteThroughEdgeLoadIsFreeSingleInstance(t *testing.T) {
	b := &broker{shared: panicStore{}, edgeLoad: map[string]int{},
		edgeInflight: map[string]edgeAttemptLoad{}, edgeOpenByAccount: map[string]int{}}
	b.edgeEnterInflight("att-1", "n1", "u_a", time.Now().Add(time.Hour))
	b.edgeExitInflight("att-1")
	b.refreshSharedLoad()
}

// panicStore fails the test loudly if the single-instance edge path reaches the shared store.
type panicStore struct{ sharedStore }

func (panicStore) markEdgeInflight(string, string, int, time.Time) error {
	panic("single-instance edge path must not touch the shared store")
}
func (panicStore) markInflight(string, string, int, time.Time) error {
	panic("single-instance edge path must not touch the shared store")
}
