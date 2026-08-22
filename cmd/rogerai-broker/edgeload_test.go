package main

import (
	"crypto/ed25519"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"rogerai.fm/roger/v6/internal/store"
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
// the FIRST of those levers back on every instance except the one that opened the attempt - the
// split would hold in one process and dissolve across the fleet, which is the failure mode
// nobody would notice locally. This test asserts on the exact expression pickFor uses.
//
// NOT THE SECOND LEVER, and the original wording here claimed it was. probeOnce reads
// `b.inflight[n.NodeID]` and never touches peerInflight, so probe suppression could not have
// crossed the instance boundary through the classic hash however it was published - which an
// audit confirmed by pointing markEdgeInflight at inflightKey and watching
// TestEdgeLoadDoesNotSuppressProbingOrPaidRouting stay green while THIS test went red. That is
// the honest division of labour between the two tests: the local split is that one's, the
// cross-instance paid-router score is this one's, and neither covers the other.
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

// The BATCH forms are what the publisher actually calls, so the guard has to be asserted on
// them too - overriding only the singular pair would leave the test passing on a nil embedded
// interface panic, which is a crash rather than the sentence above.
func (panicStore) markEdgeInflightBatch(string, map[string]int, time.Time) error {
	panic("single-instance edge path must not touch the shared store")
}
func (panicStore) markInflightBatch(string, map[string]int, time.Time) error {
	panic("single-instance edge path must not touch the shared store")
}

// gatedLoadStore holds the FIRST shared-load write it is given until the test releases it, and
// lets every later one straight through. That is enough to make the publish ordering
// deterministic from a test: arrange for a write to be in flight, change the count underneath
// it, and then see which value the fleet is left believing.
//
// It wraps the real store rather than replacing it, so the semantics under test - per-instance
// hash fields, self-exclusion, the TTL - are still the backend's own and not a fake's idea of
// them.
type gatedLoadStore struct {
	sharedStore
	once    sync.Once
	entered chan struct{} // closed when the first write is inside the store
	release chan struct{} // closed by the test to let that write finish
}

func newGatedLoadStore(inner sharedStore) *gatedLoadStore {
	return &gatedLoadStore{sharedStore: inner,
		entered: make(chan struct{}), release: make(chan struct{})}
}

func (g *gatedLoadStore) hold() {
	first := false
	g.once.Do(func() { first = true })
	if !first {
		return
	}
	close(g.entered)
	<-g.release
}

func (g *gatedLoadStore) markInflightBatch(inst string, counts map[string]int, now time.Time) error {
	g.hold()
	return g.sharedStore.markInflightBatch(inst, counts, now)
}

func (g *gatedLoadStore) markEdgeInflightBatch(inst string, counts map[string]int, now time.Time) error {
	g.hold()
	return g.sharedStore.markEdgeInflightBatch(inst, counts, now)
}

// TestAWriteInFlightCannotBePassedByANewerOne is the ordering property, and it is stated in the
// two ways it was actually wrong.
//
// Every load write used to be "read the count under metricsMu, unlock, publish what you read".
// The unlock is mandatory - metricsMu is on the hot placement path and must never span a round
// trip - but it left two concurrent changes to one node racing to the shared store on two pool
// connections with two different snapshots, and the last one to LAND won regardless of which
// was newer. Both directions of that race are here because they harm different things:
//
//   - A STALE ZERO OVER LIVE WORK is the quiescence failure. The fleet is told a serving
//     Station is idle, and nothing corrects it: the refresh tick republished non-zero counts
//     only, so a node that is locally zero was skipped and the lie stood for inflightTTL. The
//     founder's no-drain ruling rests on the gate not doing this.
//
//   - A STALE UNDER-COUNT is the money failure, and it is the one the old comment said could
//     not happen ("can over-state, never under-state"). loadFactor is 1/(1+inflight/capacity),
//     so a node the fleet thinks is carrying less than it is scores HIGHER on the paid router
//     and attracts work it should not get.
//
// The assertion is on what the FLEET believes once the publisher has finished, not on the order
// of the round trips: a superseded value may be in the store briefly, but it must never be what
// is left standing.
func TestAWriteInFlightCannotBePassedByANewerOne(t *testing.T) {
	t.Run("a close racing an open must not leave the fleet believing idle", func(t *testing.T) {
		a, peer := miEdgePair(t)
		deadline := time.Now().Add(time.Hour)
		a.edgeEnterInflight("att-1", "n1", "u_a", deadline)

		gate := newGatedLoadStore(a.shared)
		a.shared = gate
		done := make(chan struct{})
		go func() { defer close(done); a.edgeExitInflight("att-1") }() // publishes 0, then waits
		<-gate.entered
		a.edgeEnterInflight("att-2", "n1", "u_a", deadline) // the Station is serving again
		close(gate.release)
		<-done

		peer.mergeSharedInflight()
		peer.metricsMu.Lock()
		seen := peer.peerEdgeLoad["n1"]
		peer.metricsMu.Unlock()
		if seen != 1 {
			t.Errorf("peer sees edge load %d for a station holding 1 live attempt, want 1", seen)
		}
		if q, why := peer.stationQuiescent("n1"); q {
			t.Errorf("stationQuiescent(n1) = true (%s) while n1 is serving: a stale zero from a "+
				"closing attempt landed over a live one, and the mobility gate would move it", why)
		}
	})

	t.Run("an older count must not land over a newer one", func(t *testing.T) {
		a, peer := miEdgePair(t)
		gate := newGatedLoadStore(a.shared)
		a.shared = gate
		done := make(chan struct{})
		go func() { defer close(done); a.enterInflight("n1") }() // publishes 1, then waits
		<-gate.entered
		a.enterInflight("n1") // a second real request: the node is carrying two
		close(gate.release)
		<-done

		peer.mergeSharedInflight()
		peer.metricsMu.Lock()
		seen := peer.peerInflight["n1"]
		peer.metricsMu.Unlock()
		if seen != 2 {
			t.Errorf("peer sees classic load %d for a node carrying 2 requests, want 2: an "+
				"under-stated count RAISES loadFactor, so the node attracts more paid work", seen)
		}
	})
}

// failingWriteStore drops load writes on demand, so a test can lose exactly the write it means
// to lose. Reads and everything else stay real.
type failingWriteStore struct {
	sharedStore
	fail bool
}

func (f *failingWriteStore) markInflightBatch(inst string, counts map[string]int, now time.Time) error {
	if f.fail {
		return errors.New("valkey unreachable")
	}
	return f.sharedStore.markInflightBatch(inst, counts, now)
}

func (f *failingWriteStore) markEdgeInflightBatch(inst string, counts map[string]int, now time.Time) error {
	if f.fail {
		return errors.New("valkey unreachable")
	}
	return f.sharedStore.markEdgeInflightBatch(inst, counts, now)
}

// TestALostDecrementIsRepairedByTheNextTick pins the repair the non-zero filter used to make
// impossible.
//
// A load write is best-effort, which is the right posture - it must never fail a request - but
// it means the write that says "this node is now free" can be lost like any other. The refresh
// tick republished NON-ZERO counts only, so the node was then skipped by every subsequent tick
// (it is locally zero, or gone from the map entirely) and the only thing that ever cleared the
// residue was the hash ageing out at inflightTTL. The comment on that filter said the error
// "heals on the next tick"; the truth was a full minute, twelve ticks later.
//
// A minute of a spurious +1 is not cosmetic on either fabric: loadFactor is
// 1/(1+inflight/capacity), so on a capacity-1 node it HALVES the paid router's score, and the
// quiescence gate refuses a move it could have allowed for the same minute.
func TestALostDecrementIsRepairedByTheNextTick(t *testing.T) {
	for _, tc := range []struct {
		name  string
		open  func(a *broker)
		close func(a *broker)
		seen  func(peer *broker) int
	}{
		{
			name:  "edge",
			open:  func(a *broker) { a.edgeEnterInflight("att-1", "n1", "u_a", time.Now().Add(time.Hour)) },
			close: func(a *broker) { a.edgeExitInflight("att-1") },
			seen:  func(peer *broker) int { return peer.peerEdgeLoad["n1"] },
		},
		{
			name:  "classic",
			open:  func(a *broker) { a.enterInflight("n1") },
			close: func(a *broker) { a.exitInflight("n1", true) },
			seen:  func(peer *broker) int { return peer.peerInflight["n1"] },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, peer := miEdgePair(t)
			flaky := &failingWriteStore{sharedStore: a.shared}
			a.shared = flaky

			tc.open(a)
			flaky.fail = true
			tc.close(a) // the decrement never reaches the store
			flaky.fail = false

			peer.mergeSharedInflight()
			peer.metricsMu.Lock()
			residue := tc.seen(peer)
			peer.metricsMu.Unlock()
			if residue != 1 {
				t.Fatalf("peer sees %d before the repair, want the lost decrement to have left 1 "+
					"- the test is not exercising what it claims", residue)
			}

			a.refreshSharedLoad() // ONE tick, not sixty seconds of them
			peer.mergeSharedInflight()
			peer.metricsMu.Lock()
			after := tc.seen(peer)
			peer.metricsMu.Unlock()
			if after != 0 {
				t.Errorf("peer still sees %d after a full sync tick, want 0: a lost decrement is "+
					"unreachable by the tick and stands until inflightTTL", after)
			}
		})
	}
}

// TestQuiescenceRefusesBeforeAnyPeerSnapshot closes the third degraded row in
// stationQuiescent's own documented table - "no snapshot ever taken" - which had no test at all.
//
// It is the row a multi-instance broker is in for the first few seconds of its life and for as
// long as the shared store is unreachable from the moment it starts, and it is the most
// dangerous of the three: every counter reads zero because nothing has ever been merged, so a
// gate that treated "I have never looked" as "nobody is working" would be at its most confident
// exactly when it knows least.
func TestQuiescenceRefusesBeforeAnyPeerSnapshot(t *testing.T) {
	a, _ := miEdgePair(t)
	a.metricsMu.Lock()
	never := a.peerLoadAt.IsZero()
	a.metricsMu.Unlock()
	if !never {
		t.Fatal("a broker that has never merged must have no peer-load stamp")
	}
	q, why := a.stationQuiescent("n-idle")
	if q {
		t.Errorf("stationQuiescent(n-idle) = true (%s) on an instance that has never seen the "+
			"peer view: an unmerged zero is not evidence of anything", why)
	}
	if why != "no peer load snapshot yet" {
		t.Errorf("stationQuiescent reason = %q, want the never-merged reason: the caller logs "+
			"this beside the placement decision and the three refusals mean different things", why)
	}
}

// countingLoadStore counts the shared-store WRITE calls, so a test can assert the cost of a
// tick in round trips rather than in wall-clock time.
type countingLoadStore struct {
	sharedStore
	classicCalls, edgeCalls int
	classicNodes, edgeNodes int
}

func (c *countingLoadStore) markInflightBatch(inst string, counts map[string]int, now time.Time) error {
	c.classicCalls++
	c.classicNodes += len(counts)
	return c.sharedStore.markInflightBatch(inst, counts, now)
}

func (c *countingLoadStore) markEdgeInflightBatch(inst string, counts map[string]int, now time.Time) error {
	c.edgeCalls++
	c.edgeNodes += len(counts)
	return c.sharedStore.markEdgeInflightBatch(inst, counts, now)
}

// TestTheRefreshTickCostsOneRoundTripPerCounter is about what the tick costs on a fleet, not
// about what it publishes.
//
// The republish used to issue its own pipelined round trip PER NODE PER COUNTER, sequentially,
// on a five-second tick. At a hundred busy nodes that is about a tenth of a tick and nobody
// noticed; at two thousand it is seconds of it, and a backend that has gone sick charges
// sharedOpTimeout for every one of them - so the loop that exists to keep the peer view fresh
// becomes the reason it is not. The batched READ has been one round trip since it was written;
// this asserts the write side matches, and that the batch really does carry every node rather
// than quietly dropping the tail.
func TestTheRefreshTickCostsOneRoundTripPerCounter(t *testing.T) {
	a, peer := miEdgePair(t)
	counting := &countingLoadStore{sharedStore: a.shared}
	a.shared = counting
	deadline := time.Now().Add(time.Hour)
	for _, node := range []string{"n1", "n2", "n3"} {
		a.edgeEnterInflight("att-"+node, node, "u_a", deadline)
		a.enterInflight(node)
	}
	counting.classicCalls, counting.edgeCalls = 0, 0
	counting.classicNodes, counting.edgeNodes = 0, 0

	a.refreshSharedLoad()

	if counting.edgeCalls != 1 || counting.classicCalls != 1 {
		t.Errorf("one tick made %d classic and %d edge write calls for 3 nodes, want 1 each: the "+
			"republish is per-node again and its cost scales with the fleet",
			counting.classicCalls, counting.edgeCalls)
	}
	if counting.edgeNodes != 3 || counting.classicNodes != 3 {
		t.Errorf("the batches carried %d classic and %d edge nodes, want 3 each",
			counting.classicNodes, counting.edgeNodes)
	}
	// AND THE BATCH IS NOT JUST CHEAP, IT IS COMPLETE: every node in it reaches a peer.
	peer.mergeSharedInflight()
	peer.metricsMu.Lock()
	defer peer.metricsMu.Unlock()
	for _, node := range []string{"n1", "n2", "n3"} {
		if peer.peerEdgeLoad[node] != 1 || peer.peerInflight[node] != 1 {
			t.Errorf("peer sees %s at edge=%d classic=%d after a batched republish, want 1 and 1",
				node, peer.peerEdgeLoad[node], peer.peerInflight[node])
		}
	}
}
