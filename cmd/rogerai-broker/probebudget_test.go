package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/protocol"
	"rogerai.fm/roger/v7/internal/store"
)

// Review finding: the probe lease outlived a round, so the next round found it taken on both
// instances. The prober releases it the moment its reading is published (or its check is
// refused); the TTL only covers a prober that died.
func TestTheProbeLeaseIsReleasedWithTheReading(t *testing.T) {
	vs, mr := testValkeyShared(t)
	a := adaptiveBroker(30*time.Second, 15*time.Minute)
	a.shared, a.multiInstance, a.instanceID = vs, true, "i-a"
	b := adaptiveBroker(30*time.Second, 15*time.Minute)
	b.shared, b.multiInstance, b.instanceID = vs, true, "i-b"
	now := time.Now()
	require.True(t, a.claimProbe("n", now, time.Nanosecond))
	require.False(t, b.claimProbe("n", now, time.Nanosecond), "a peer in the same round probed too")
	a.publishProbe("n", trustState{probed: true, probeOK: true}, now)
	require.True(t, b.claimProbe("n", now.Add(time.Second), time.Nanosecond), "the published round still held the lease")
	// A prober that died never publishes: its lease lapses on its own.
	mr.FastForward(probeLeaseTTL + time.Second)
	require.True(t, a.claimProbe("n", now.Add(probeLeaseTTL+time.Second), time.Nanosecond))
}

// Audit finding: freshness equal to the idle interval went stale for a moment every cycle, and a
// browse then restarted the probe ramp (about 160 canaries a day against 60). Freshness outlasts
// the ceiling, a browse never restarts a healthy node's ramp, and tool-call checks keep to a share
// of the day so liveness canaries are never crowded out.
func TestABrowsedIdleNodeStaysWithinItsBudgetAndFresh(t *testing.T) {
	c := probeConfig{interval: 30 * time.Second, ceiling: defaultProbeCeiling}
	require.Greater(t, c.freshFor(), c.ceiling, "a reading goes stale before the next idle canary lands")
	require.False(t, c.measurementStale(time.Now().Add(-c.ceiling-time.Minute), time.Now()))

	b := probeMinBroker()
	addNode(b, "n", 0) // a verified, healthy node
	b.metricsMu.Lock()
	b.probeSched["n"] = &probeState{backoff: 6, nextDue: time.Now().Add(20 * time.Minute)}
	b.demandProbeSoonLocked("n", time.Now())
	level := b.probeSched["n"].backoff
	b.metricsMu.Unlock()
	require.Equal(t, 6, level, "a browse restarted a healthy node's probe ramp")
	b.trust["n"] = trustState{probed: true, probeOK: false} // a failing node does start over
	b.metricsMu.Lock()
	b.demandProbeSoonLocked("n", time.Now())
	level = b.probeSched["n"].backoff
	b.metricsMu.Unlock()
	require.Zero(t, level, "a failing node was not let recover quickly")

	// A day at the ceiling: 48 canaries, with tool checks asking as often as they can.
	vs, _ := testValkeyShared(t)
	d := adaptiveBroker(30*time.Second, defaultProbeCeiling)
	d.shared = vs
	t0 := time.Now()
	refused := 0
	for i := 0; i < int(24*time.Hour/defaultProbeCeiling); i++ {
		at := t0.Add(time.Duration(i) * defaultProbeCeiling)
		checkClock = func() time.Time { return at }
		for j := 0; j < 3; j++ {
			d.allowCheck("day-node", "tools")
		}
		if !d.allowCheck("day-node", "canary") {
			refused++
		}
	}
	checkClock = time.Now
	require.Zero(t, refused, "tool-call checks crowded %d liveness canaries out of the day", refused)
}

// Audit finding: a reading this instance already took ended its wait for the round it lost, so a
// slow winner's reading never reached it. Only a newer reading ends the wait.
func TestALostRoundWaitsForANewReading(t *testing.T) {
	vs, _ := testValkeyShared(t)
	a := adaptiveBroker(30*time.Second, 15*time.Minute)
	a.shared, a.multiInstance, a.instanceID = vs, true, "i-a"
	b := adaptiveBroker(30*time.Second, 15*time.Minute)
	b.shared, b.multiInstance, b.instanceID = vs, true, "i-b"
	a.publishProbe("n", trustState{probed: true, probeOK: true, ttftMs: 100}, time.Now())
	b.takePeerReadings(time.Now()) // nothing lost yet: nothing taken
	b.probeLost = map[string]time.Time{"n": time.Now()}
	b.takePeerReadings(time.Now())
	first := b.probeApplied["n"]
	require.NotZero(t, first, "the peer's reading was not taken")
	b.probeLost = map[string]time.Time{"n": time.Now()} // a new round lost to the same peer
	b.takePeerReadings(time.Now())
	require.Contains(t, b.probeLost, "n", "an already-taken reading ended the wait for the new one")
	time.Sleep(2 * time.Millisecond)
	a.publishProbe("n", trustState{probed: true, probeOK: true, ttftMs: 200}, time.Now())
	b.takePeerReadings(time.Now())
	require.NotContains(t, b.probeLost, "n")
	require.Equal(t, 200.0, b.trust["n"].ttftMs)
}

// Audit finding: the tool-call back-off of nodes that never came back was kept forever.
func TestOldToolBackoffIsPruned(t *testing.T) {
	b := probeMinBroker()
	b.db = store.NewMem()
	now := time.Now()
	b.toolFails = map[string]toolBackoffState{"gone\x00m": {streak: 6, last: now.Add(-toolBackoffKeep - time.Hour)},
		"recent\x00m": {streak: 2, last: now.Add(-time.Hour)}}
	b.pruneStaleNodes(now)
	require.NotContains(t, b.toolFails, "gone\x00m")
	require.Contains(t, b.toolFails, "recent\x00m", "a back-off a reconnecting node continues was dropped")
}

// Audit finding: readings were shared only when two instances' rounds collided inside the lease;
// rounds a few seconds further apart both probed. A peer's reading still fresh within this
// instance's interval is taken instead of probing.
func TestAFreshPeerReadingIsTakenInsteadOfProbing(t *testing.T) {
	vs, _ := testValkeyShared(t)
	a := adaptiveBroker(30*time.Second, 15*time.Minute)
	a.shared, a.multiInstance, a.instanceID = vs, true, "i-a"
	b := adaptiveBroker(30*time.Second, 15*time.Minute)
	b.shared, b.multiInstance, b.instanceID = vs, true, "i-b"
	a.publishProbe("n", trustState{probed: true, probeOK: true, ttftMs: 120}, time.Now())
	require.False(t, b.claimProbe("n", time.Now().Add(40*time.Second), 2*time.Minute),
		"a fresh peer reading was probed again")
	require.Equal(t, 120.0, b.trust["n"].ttftMs, "the peer's reading was not taken")
	require.True(t, b.claimProbe("n2", time.Now(), 2*time.Minute), "a node with no reading was not probed")
	vs2, _ := testValkeyShared(t)
	c := adaptiveBroker(30*time.Second, 15*time.Minute)
	c.shared, c.multiInstance, c.instanceID = vs2, true, "i-c"
	a2 := adaptiveBroker(30*time.Second, 15*time.Minute)
	a2.shared, a2.multiInstance, a2.instanceID = vs2, true, "i-a"
	a2.publishProbe("m", trustState{probed: true, probeOK: true}, time.Now().Add(-time.Hour))
	require.True(t, c.claimProbe("m", time.Now().Add(10*time.Minute), time.Minute), "a stale reading stopped a probe")
}

// Audit finding: an instance took a peer's reading only after losing a round to it, so one whose
// round fell long before the peer's let the node's reading lapse. A node whose own reading is ageing
// takes the peer's newer one on any tick.
func TestAnAgeingReadingTakesThePeersNewerOne(t *testing.T) {
	vs, _ := testValkeyShared(t)
	a := adaptiveBroker(30*time.Second, 30*time.Minute)
	a.shared, a.multiInstance, a.instanceID = vs, true, "i-a"
	b := adaptiveBroker(30*time.Second, 30*time.Minute)
	b.shared, b.multiInstance, b.instanceID = vs, true, "i-b"
	b.nodes["n"] = protocol.NodeRegistration{NodeID: "n"}
	b.probeSched["n"] = &probeState{lastMeasured: time.Now().Add(-40 * time.Minute)}
	a.publishProbe("n", trustState{probed: true, probeOK: true, ttftMs: 90}, time.Now())
	b.takePeerReadings(time.Now())
	require.Equal(t, 90.0, b.trust["n"].ttftMs, "an ageing node did not take the peer's newer reading")
}

// Audit finding: a tool-call slot reserved before the main budget refused the check was kept.
func TestARefusedToolCheckGivesItsSlotBack(t *testing.T) {
	vs, _ := testValkeyShared(t)
	b := adaptiveBroker(30*time.Second, 30*time.Minute)
	b.shared = vs
	for i := 0; i < checkBudgetHour; i++ {
		require.True(t, b.allowCheck("slot-node", "canary"))
	}
	for i := 0; i < toolBudgetDay+5; i++ {
		require.False(t, b.allowCheck("slot-node", "tools"), "the hour's budget is spent")
	}
	// An hour later the main budget has room again, and the tool share was never spent.
	at := time.Now().Add(61 * time.Minute)
	checkClock = func() time.Time { return at }
	defer func() { checkClock = time.Now }()
	require.True(t, b.allowCheck("slot-node", "tools"), "refused tool checks spent the tool share")
}

// An old peer reading (a lost publish, a pruned node) is not taken for an ageing node, and a node is
// looked up at most once a minute.
func TestAnOldPeerReadingIsNotTakenAndLookupsAreRateLimited(t *testing.T) {
	vs, _ := testValkeyShared(t)
	a := adaptiveBroker(30*time.Second, 30*time.Minute)
	a.shared, a.multiInstance, a.instanceID = vs, true, "i-a"
	b := adaptiveBroker(30*time.Second, 30*time.Minute)
	b.shared, b.multiInstance, b.instanceID = vs, true, "i-b"
	b.nodes["n"] = protocol.NodeRegistration{NodeID: "n"}
	a.publishProbe("n", trustState{probed: true, probeOK: true, ttftMs: 90}, time.Now())
	later := time.Now().Add(2 * b.probe.freshFor())
	b.takeAgeingReadings(later)
	require.False(t, b.trust["n"].probed, "an old peer reading marked a node measured")
	require.True(t, b.ageCheckDue("m", time.Now()))
	require.False(t, b.ageCheckDue("m", time.Now().Add(30*time.Second)), "a node was looked up twice inside a minute")
	require.True(t, b.ageCheckDue("m", time.Now().Add(ageCheckEvery+time.Second)))
}
