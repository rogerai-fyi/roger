package main

import "time"

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
// The mechanism is the sibling of the classic one, deliberately not the same key. See
// markEdgeInflight in sharedstore.go for why sharing the key would undo the split that keeps an
// edge attempt from suppressing a node's canary probes and depressing its paid-fabric score.

// writeThroughEdgeLoad mirrors THIS instance's open-edge-attempt count for a node into the
// shared edge hash, so a peer's placement sees it. Called on every change (open and close),
// exactly as writeThroughInflight is, and with the same posture: best-effort, non-fatal, never
// on a path that can fail a request. A write that does not land only means a peer's count is
// stale until the next refresh tick republishes it.
//
// EXACTLY FREE WHEN MULTI-INSTANCE IS OFF. The guard is the first thing in the function and it
// is the same guard writeThroughInflight uses, so a single-instance broker does no allocation,
// takes no lock and makes no call - the edge path is byte-for-byte what it was.
func (b *broker) writeThroughEdgeLoad(node string, count int) {
	if !b.multiInstance || b.shared == nil || b.instanceID == "" {
		return
	}
	_ = b.shared.markEdgeInflight(b.instanceID, node, count, time.Now())
}

// refreshSharedLoad republishes this instance's NON-ZERO counts for both load counters on the
// sync tick, and it closes a hole that has been in the classic counter since it was written.
//
// THE HOLE. markInflight PExpires the node's hash at inflightTTL (60s) on every write, and a
// write only happens when the count CHANGES. So a single request that runs longer than the TTL -
// a long completion on the classic path, an edge attempt whose grant deadline is minutes out -
// has its hash expire underneath it, and every peer instance stops seeing the load entirely
// while the work is still in flight. For RANKING that is a wrong divisor for a while. For a
// QUIESCENCE GATE it is the exact failure the gate exists to prevent: the longest-running work
// in the fleet is precisely the work that disappears from the peer view, so the gate would
// conclude "idle" about the busiest Stations first.
//
// ONLY NON-ZERO COUNTS ARE REPUBLISHED, and that asymmetry is deliberate. Republishing is a
// snapshot read under metricsMu followed by a write outside it, so it can in principle land
// after a newer write from the enter/exit path and briefly restore a superseded value. Bounded
// to non-zero values, the only error that can produce is an OVER-statement of load that heals on
// the next tick - which under-ranks a node slightly and makes the quiescence gate refuse a move
// it could have allowed. Both are the safe direction. Publishing zeros here would make the
// opposite error reachable: a stale zero landing over a live count is a station that looks idle
// while it is serving, which is the one reading that must never be produced by our own
// bookkeeping. A count that drops to zero is published by the exit path itself, and if THAT
// write is lost the residue ages out at inflightTTL - over-stating load for a minute, again in
// the safe direction.
//
// Cost is one pipelined round trip per node this instance is currently serving, once per tick,
// on the background loop. Nodes at rest cost nothing because they are not in the maps (edgeLoad
// deletes its entry at zero) or are skipped (inflight keeps a zero entry and it is filtered).
func (b *broker) refreshSharedLoad() {
	if !b.multiInstance || b.shared == nil || b.instanceID == "" {
		return
	}
	b.metricsMu.Lock()
	classic := nonZeroCounts(b.inflight)
	edge := nonZeroCounts(b.edgeLoad)
	b.metricsMu.Unlock()
	for node, n := range classic {
		_ = b.shared.markInflight(b.instanceID, node, n, time.Now())
	}
	for node, n := range edge {
		_ = b.shared.markEdgeInflight(b.instanceID, node, n, time.Now())
	}
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
