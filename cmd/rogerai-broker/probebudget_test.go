package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/store"
)

// Review finding: the probe lease outlived a round (45 s against a 30 s floor), so the next
// round found it taken on both instances and nobody probed. It now lasts under the floor: a peer
// in the same round is refused, the next round is free for either instance.
func TestTheProbeLeaseNeverBlocksTheNextRound(t *testing.T) {
	vs, mr := testValkeyShared(t)
	a := adaptiveBroker(30*time.Second, 15*time.Minute)
	a.shared, a.multiInstance, a.instanceID = vs, true, "i-a"
	b := adaptiveBroker(30*time.Second, 15*time.Minute)
	b.shared, b.multiInstance, b.instanceID = vs, true, "i-b"
	require.Less(t, a.leaseTTL(), a.probe.interval)
	now := time.Now()
	require.True(t, a.claimProbe("n", now, time.Second))
	require.False(t, b.claimProbe("n", now, time.Second), "a peer in the same round probed too")
	mr.FastForward(a.probe.interval)
	require.True(t, a.claimProbe("n", now.Add(a.probe.interval), time.Second), "the holder could not probe its next round")
	mr.FastForward(a.probe.interval)
	require.True(t, b.claimProbe("n", now.Add(2*a.probe.interval), time.Second), "the peer could not probe a later round")
	tiny := adaptiveBroker(time.Second, time.Minute)
	require.Equal(t, 5*time.Second, tiny.leaseTTL(), "a lease is never under a few seconds")
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
