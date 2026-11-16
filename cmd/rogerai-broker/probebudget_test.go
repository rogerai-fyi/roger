package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
	require.True(t, a.claimProbe("n", now))
	require.False(t, b.claimProbe("n", now), "a peer in the same round probed too")
	mr.FastForward(a.probe.interval)
	require.True(t, a.claimProbe("n", now.Add(a.probe.interval)), "the holder could not probe its next round")
	mr.FastForward(a.probe.interval)
	require.True(t, b.claimProbe("n", now.Add(2*a.probe.interval)), "the peer could not probe a later round")
	tiny := adaptiveBroker(time.Second, time.Minute)
	require.Equal(t, 5*time.Second, tiny.leaseTTL(), "a lease is never under a few seconds")
}
