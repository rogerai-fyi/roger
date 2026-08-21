package main

import (
	"sync"
	"time"
)

// Cross-instance EDGE load: the second half of a counter that only ever had one.
//
// b.edgeLoad counts the open edge attempts THIS instance authorized. Until this file it had no
// peer equivalent at all, while the classic counter next to it in edgeLoadLocked has had one
// since Stage 2 (writeThroughInflight / mergeSharedInflight). That asymmetry cost two different
// things, and only the first of them was visible:
//
//   - PLACEMENT QUALITY, today, on the deployment we already run. Two instances each see their
//     own edge attempts and none of each other's, so both under-count every station's real edge
//     load, both divide by too small a number, and both conclude the same busiest station is the
//     best one. That is the magnet: the score claims to be spreading work while every instance
//     independently piles onto the same rig. An edge attempt is opened by whichever broker the
//     consumer's authorize landed on and settled by whichever one the Tower reaches, so a busy
//     station is routinely busy somewhere other than where it is being scored - peer load matters
//     MORE on this path than on the classic one, not less.
//
//   - QUIESCENCE, which is what §6.3b of docs/relay-selection-design.md is about to depend on.
//     The founder's sticky-placement ruling lets Core move a Station's relay binding only while
//     the Station is idle, and edgeLoadLocked's zero is the idle signal that gate reads. A zero
//     that is one broker's view is not proof: instance B can be holding a live attempt that
//     instance A cannot see, and A moves the binding out from under it. The epoch fence (§6.6b)
//     makes that fail loudly rather than silently - the moved-under grant settles 410 and the
//     consumer's hold refunds on age - but "we voided a live request" is an outcome the design
//     claims not to have, and the consumer would attribute it to the relay rather than to us.
//
// The mechanism is the sibling of the classic one, deliberately not the same key: sharing the
// key would put edge attempts into the peer sum the CLASSIC paid router divides by, so a
// reservation anybody can open for a fraction of a cent would depress a node's score on the
// fabric that pays it, on every instance except the one that opened it. It would NOT reach the
// canary - an earlier version of this comment said it would, and an audit disproved it:
// probeOnce reads `b.inflight[n.NodeID]` and never peerInflight, so probe suppression is a
// same-process concern that the split in broker.edgeLoad already handles. See markEdgeInflight
// in sharedstore.go.

// writeThroughEdgeLoad mirrors THIS instance's open-edge-attempt count for a node into the
// shared edge hash, so a peer's placement sees it. Called on every change (open and close),
// exactly as writeThroughInflight is, and with the same posture: best-effort, non-fatal, never
// on a path that can fail a request. A write that does not land only means a peer's count is
// stale until the next refresh tick republishes it.
//
// IT TAKES NO COUNT, and that is the fix rather than a tidy-up. It used to be handed the value
// the caller read while it held metricsMu, and publish it after unlocking - so two concurrent
// changes to one node raced to the shared store carrying two different snapshots, and whichever
// round trip finished last won. See publishSharedLoad: the value is now read inside the
// publisher's own critical section, so the caller has nothing to hand over and cannot hand over
// something stale.
//
// EXACTLY FREE WHEN MULTI-INSTANCE IS OFF. The guard is the first thing in the function and it
// is the same guard writeThroughInflight uses, so a single-instance broker does no allocation,
// takes no lock and makes no call - the edge path is byte-for-byte what it was.
func (b *broker) writeThroughEdgeLoad(node string) { b.markLoadDirty(node, true) }

// markLoadDirty records that a node's published count may no longer match its local one and
// then tries to publish. Both counters go through here; the bool picks which.
func (b *broker) markLoadDirty(node string, edge bool) {
	if !b.multiInstance || b.shared == nil || b.instanceID == "" || node == "" {
		return
	}
	b.loadPub.dirty(node, edge)
	b.publishSharedLoad()
}

// publishSharedLoad is the ONE writer of both shared load hashes, and the serialization it
// provides is a correctness property that three separate defects came out of.
//
// WHAT WAS WRONG. Every write used to be "read the count under metricsMu, unlock, publish what
// you read". Unlocking before the round trip is mandatory - metricsMu is held on the hot
// placement path and must never span a network call - but it also means two concurrent changes
// to one node reach the shared store on two different pool connections carrying two different
// snapshots, and the LAST ONE TO LAND WINS whatever it says. That produced:
//
//   - A STALE ZERO OVER LIVE WORK. Open and close race; the close's 0 lands after the open's 1,
//     and the shared hash then says a serving Station is idle until something else writes it.
//     Nothing corrected it either, because the refresh tick republished non-zero counts only,
//     so a node the tick reads as locally zero was skipped: the wrong value stood for a full
//     inflightTTL, sixty seconds, not the "next tick" the old comment promised. That is the one
//     reading edgeload.go's own header says must never be produced by our own bookkeeping, and
//     the quiescence gate the founder's no-drain ruling rests on would have read it as proof.
//
//   - A STALE UNDER-COUNT. The same race with two opens: 1 lands after 2, and the fleet sees
//     one classic request where a node is carrying two. "Can over-state, never under-state" was
//     the claim, and it was false in the direction that costs money: loadFactor is
//     1/(1+inflight/capacity), so an under-stated count RAISES the node's paid-router score and
//     it attracts more work than it should.
//
// WHAT IS TRUE NOW. A publisher holds one token across the round trip and reads the counts it
// is about to publish INSIDE that critical section. Writes for a node are therefore totally
// ordered, and each carries a value that was current when its own write began. A change that
// lands after a publisher has read is not lost: it marked the node dirty first, so the
// publisher picks it up on its next round. The strongest honest statement is that a superseded
// value can be in the store for at most one further round trip and is then corrected - never
// that it stands until a TTL, and never that a correction depends on the counter's direction.
//
// A CALLER NEVER WAITS ON THE ROUND TRIP. If another goroutine already holds the token this
// returns immediately; the node is already marked, and the holder re-reads the dirty set after
// every write specifically so it publishes what the callers it skipped were carrying. That is
// strictly better than what it replaces, where every writer blocked on its own Valkey call and
// a sick backend charged all of them sharedOpTimeout.
//
// AND IT BATCHES. The dirty set is written in one pipeline per counter, so a burst of opens
// across many nodes - and the refresh tick, which marks every node this instance is carrying -
// costs one round trip rather than one per node.
func (b *broker) publishSharedLoad() {
	if !b.multiInstance || b.shared == nil || b.instanceID == "" {
		return
	}
	// BOUNDED, because the goroutine doing this work is usually a request's. Each round is one
	// pipeline that clears everything outstanding, so hitting the cap means writes are arriving
	// faster than the backend answers - and in that case the right thing is to hand the rest to
	// the next writer or to the sync tick (which marks and drains everything this instance
	// believes it has published) rather than to conscript one request into an unbounded loop.
	const maxRounds = 4
	for round := 0; round < maxRounds; round++ {
		if !b.loadPub.publishMu.TryLock() {
			return // somebody else holds the token; what we marked is theirs to publish
		}
		classic, edge := b.loadPub.take()
		if len(classic) == 0 && len(edge) == 0 {
			b.loadPub.publishMu.Unlock()
			// A writer can mark a node between our take and our unlock, find the token held,
			// and return - so an empty take is not proof that there is nothing to do. Look
			// again before leaving, or that node waits for the tick.
			if !b.loadPub.pending() {
				return
			}
			continue
		}
		now := time.Now()
		b.metricsMu.Lock()
		classicCounts := countsFor(b.inflight, classic)
		edgeCounts := countsFor(b.edgeLoad, edge)
		b.metricsMu.Unlock()
		if len(classicCounts) > 0 {
			if err := b.shared.markInflightBatch(b.instanceID, classicCounts, now); err == nil {
				b.loadPub.published(classicCounts, false)
			}
		}
		if len(edgeCounts) > 0 {
			if err := b.shared.markEdgeInflightBatch(b.instanceID, edgeCounts, now); err == nil {
				b.loadPub.published(edgeCounts, true)
			}
		}
		b.loadPub.publishMu.Unlock()
	}
}

// countsFor reads the current value of each named node out of a counter map. The caller holds
// metricsMu; a node that is absent reads zero, which is the right answer and the one the
// publisher must be able to send - an absent entry is how edgeLoad says "nothing open here".
func countsFor(src map[string]int, nodes []string) map[string]int {
	if len(nodes) == 0 {
		return nil
	}
	out := make(map[string]int, len(nodes))
	for _, n := range nodes {
		out[n] = src[n]
	}
	return out
}

// refreshSharedLoad is the sync tick's half of the write side: it marks everything this
// instance might owe the shared store and lets the publisher above send it in one round trip
// per counter.
//
// IT CLOSES TWO HOLES, and the second one used to be a deliberate omission.
//
// THE EXPIRY. markInflight PExpires the node's hash at inflightTTL (60s) on every write, and a
// write only happens when the count CHANGES. So a single request that runs longer than the TTL
// - a long completion on the classic path, an edge attempt whose grant deadline is minutes out
// - has its hash expire underneath it, and every peer instance stops seeing the load entirely
// while the work is still in flight. For RANKING that is a wrong divisor for a while. For a
// QUIESCENCE GATE it is the exact failure the gate exists to prevent: the longest-running work
// in the fleet is precisely the work that disappears from the peer view, so the gate would
// conclude "idle" about the busiest Stations first.
//
// THE LOST DECREMENT. A write is best-effort; the one that says "this node is now at zero" can
// fail like any other. This tick used to republish NON-ZERO counts only, on the argument that a
// stale republish could otherwise restore a zero over live work - which was true of a publisher
// that shipped a value read before the write, and is no longer true of the one above. The cost
// of that filter was that the residue it protected was also unreachable: a node the tick reads
// as locally zero was skipped, so the only thing that ever cleared a lost zero was the hash
// ageing out sixty seconds later. On a capacity-1 node a spurious +1 HALVES the paid router's
// score (loadFactor is 1/(1+inflight/capacity)) for that whole minute, and the quiescence gate
// refuses a move it could have allowed. Now the tick marks every node it believes it has
// published a non-zero for, so a dropped zero is corrected on the next tick, five seconds -
// which is what the old comment claimed and the old code could not do.
//
// WHAT IT DOES NOT MARK is every node that ever existed. The candidates are the nodes carrying
// local work plus the nodes this instance believes it has a live non-zero for in the store;
// everything else is already absent or already zero there, and republishing zeros for the whole
// registry every five seconds would be a fleet-sized write for no information.
func (b *broker) refreshSharedLoad() {
	if !b.multiInstance || b.shared == nil || b.instanceID == "" {
		return
	}
	b.metricsMu.Lock()
	classic := nonZeroCounts(b.inflight)
	edge := nonZeroCounts(b.edgeLoad)
	b.metricsMu.Unlock()
	b.loadPub.dirtyAll(classic, edge)
	b.publishSharedLoad()
}

// nonZeroCounts copies the entries of a counter map that are actually carrying something, so the
// caller can iterate them after releasing the lock that guards the map. Copying is the point: a
// shared-store write must never happen under metricsMu, which is held on the hot placement path.
func nonZeroCounts(src map[string]int) map[string]int {
	var out map[string]int
	for k, v := range src {
		if v <= 0 {
			continue
		}
		if out == nil {
			out = make(map[string]int, 4)
		}
		out[k] = v
	}
	return out
}

// sharedLoadMirror is the bookkeeping behind publishSharedLoad: what this instance still owes
// the shared store, and what it believes the shared store currently holds for it.
//
// TWO LOCKS, AND THEY GUARD DIFFERENT THINGS. publishMu is the publisher's token - held across
// a network round trip, and the reason writes for one node cannot overtake each other. mu
// guards the maps, is never held across anything slower than a map write, and is what lets a
// caller mark a node dirty without waiting for whatever is currently in flight. Neither is
// metricsMu, which must not be held across a shared-store call at all.
//
// A ZERO VALUE IS READY, because a broker built as a struct literal in a test must behave like
// one built by newBroker. The maps are made on first use.
type sharedLoadMirror struct {
	publishMu sync.Mutex
	mu        sync.Mutex
	classic   loadCounterMirror
	edge      loadCounterMirror
}

// loadCounterMirror is one counter's half: the nodes whose published value may be wrong, and
// the last value successfully written for each. `sent` holds only NON-ZERO values - a node
// published at zero is a node the store need not be told about again, which is what keeps the
// tick's candidate set proportional to work in flight rather than to the size of the fleet.
type loadCounterMirror struct {
	dirty map[string]struct{}
	sent  map[string]int
}

func (m *sharedLoadMirror) side(edge bool) *loadCounterMirror {
	if edge {
		return &m.edge
	}
	return &m.classic
}

// dirty marks one node as owing the shared store a value.
func (m *sharedLoadMirror) dirty(node string, edge bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	side := m.side(edge)
	if side.dirty == nil {
		side.dirty = map[string]struct{}{}
	}
	side.dirty[node] = struct{}{}
}

// dirtyAll marks the sync tick's candidate set: the nodes carrying local work, plus every node
// this instance believes it has a live non-zero published for. The second half is what repairs
// a decrement whose write was lost, and it is why the tick can publish zeros safely.
func (m *sharedLoadMirror) dirtyAll(classic, edge map[string]int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range []struct {
		side  *loadCounterMirror
		local map[string]int
	}{{&m.classic, classic}, {&m.edge, edge}} {
		if s.side.dirty == nil {
			s.side.dirty = map[string]struct{}{}
		}
		for node := range s.local {
			s.side.dirty[node] = struct{}{}
		}
		for node := range s.side.sent {
			s.side.dirty[node] = struct{}{}
		}
	}
}

// take empties both dirty sets and returns what was in them. Emptying here rather than after
// the write is what makes a change arriving DURING a write re-dirty the node instead of being
// swallowed by it.
func (m *sharedLoadMirror) take() (classic, edge []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.classic.take(), m.edge.take()
}

func (c *loadCounterMirror) take() []string {
	if len(c.dirty) == 0 {
		return nil
	}
	out := make([]string, 0, len(c.dirty))
	for node := range c.dirty {
		out = append(out, node)
		delete(c.dirty, node)
	}
	return out
}

// pending reports whether anything is waiting to be published.
func (m *sharedLoadMirror) pending() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.classic.dirty) > 0 || len(m.edge.dirty) > 0
}

// published records what actually landed. A zero drops the node from the set, because the
// store now agrees with us and there is nothing left to repair or to keep alive against the
// TTL. Only called on a write that returned no error: a failed write leaves the previous
// belief in place, which is exactly what makes the next tick retry it.
func (m *sharedLoadMirror) published(counts map[string]int, edge bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	side := m.side(edge)
	for node, n := range counts {
		if n <= 0 {
			delete(side.sent, node)
			continue
		}
		if side.sent == nil {
			side.sent = map[string]int{}
		}
		side.sent[node] = n
	}
}

// mergeSharedEdgeLoad pulls the peer edge-attempt snapshot and swaps it into b.peerEdgeLoad,
// reporting whether the round succeeded. It is the edge sibling of the merge in
// mergeSharedInflight and degrades the same way for the RANKING reader: on an error the previous
// snapshot stays, because a stale divisor is a better placement input than a fleet that
// suddenly looks empty. The boolean is what lets the QUIESCENCE reader take the opposite view of
// the same failure - see stationQuiescent.
func (b *broker) mergeSharedEdgeLoad() bool {
	if b.shared == nil {
		return false
	}
	snap, err := b.shared.edgeInflightByNode(b.instanceID)
	if err != nil {
		return false
	}
	b.metricsMu.Lock()
	b.peerEdgeLoad = snap
	b.metricsMu.Unlock()
	return true
}

// peerLoadFreshness is how old the last fully-successful peer merge may be before the quiescence
// reader stops believing it. Derived from the sync cadence rather than written as a constant so
// the two cannot drift apart, and so a test that shrinks syncTickInterval to drive a tick gets a
// freshness bound that shrank with it.
//
// TWO TICKS: one dropped merge is a hiccup on a shared store that is allowed to hiccup
// (sharedOpTimeout is 750ms against a 5s tick), and refusing every placement decision on one
// missed round would make the gate useless during ordinary Valkey noise. Two consecutive misses
// is not noise, and by then we have no idea what the fleet is doing.
func peerLoadFreshness() time.Duration { return 2 * syncTickInterval }

// stationQuiescent answers the question the §6.3b mobility gate has to ask before it moves a
// Station's relay binding: is this Station carrying no work ANYWHERE in the fleet, and do I have
// good enough evidence to act on that?
//
// IT IS NOT edgeLoadLocked() == 0, and the difference is the whole reason it is a separate
// function. edgeLoadLocked is the RANKING reader. It wants a number, always, and it is right to
// take the last snapshot it has when the shared store is unreachable: placement with stale load
// is a slightly worse choice, and placement with no load at all is a magnet. This is the
// QUIESCENCE reader, and the action behind it is not "rank this node lower", it is "move a live
// Station's relay", whose failure mode is a voided request and a refunded consumer. So the
// degraded answers have to be opposite:
//
//	shared store unreadable   ranking: use the last snapshot   here: NOT quiescent
//	no snapshot ever taken    ranking: peers contribute 0      here: NOT quiescent
//	snapshot older than 2 ticks   ranking: use it anyway       here: NOT quiescent
//
// "I cannot prove it is idle" and "it is idle" are the same value in a bool that only counts
// zeros, which is exactly how a gate like this gets built wrong. The reason is returned rather
// than logged here so the caller can log it with the placement decision it belongs to; every
// false has a reason, and a gate that never fires should be readable from those lines.
//
// SINGLE-INSTANCE IS THE EXACT ANSWER, NOT A DEGRADED ONE. With no peers, this instance's own
// counters ARE the fleet's, so the freshness machinery is skipped entirely and the local zero is
// proof. That is also why the multi-instance branch is gated on b.multiInstance rather than on
// b.shared: the shared registry mirror runs with a wired Valkey and the bus off, and in that
// configuration there is still only one broker.
//
// WHAT IT STILL CANNOT PROVE, and the caller must be built knowing it. A peer's write-through
// lands within a round trip, but this instance only learns of it on its next merge tick, so an
// attempt opened on another instance is invisible here for up to syncTickInterval - and up to
// peerLoadFreshness if a merge was missed. Nothing on this side can close that: the window is
// the price of not putting a Valkey read on the placement path. It is closed at the other two
// ends instead - the node re-checks its own liveness before acting on a placement instruction
// (§6.10 item 5), and the Station-epoch fence (§6.6b) refuses to settle a grant minted under a
// superseded placement. This function narrows the window; it is not the safety property.
//
// NO PRODUCTION CALLER YET, deliberately. The move itself is §6.3c and is recorded, not built.
func (b *broker) stationQuiescent(nodeID string) (bool, string) {
	if nodeID == "" {
		return false, "no station id"
	}
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	if n := b.inflight[nodeID] + b.edgeLoad[nodeID]; n > 0 {
		return false, "local work in flight"
	}
	if !b.multiInstance {
		return true, "quiescent (single instance)"
	}
	if b.peerLoadAt.IsZero() {
		return false, "no peer load snapshot yet"
	}
	if age := time.Since(b.peerLoadAt); age > peerLoadFreshness() {
		return false, "peer load snapshot stale (" + age.Round(time.Second).String() + ")"
	}
	if n := b.peerInflight[nodeID] + b.peerEdgeLoad[nodeID]; n > 0 {
		return false, "peer work in flight"
	}
	return true, "quiescent"
}
