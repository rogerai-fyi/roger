package main

// total_latency_test.go: the total-latency measurement sort:latency ranks on (contract §14.B7
// #19, founder ruling 2026-10-05). Every served attempt, direct or bridged, folds its time to the
// whole answer into a per-node EWMA that lives in the shared store, so every instance ranks on
// the same figure; an instance without the shared store keeps its own.

import (
	"crypto/ed25519"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/store"
)

func TestTotalLatencyEWMA(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	b := buildBroker(store.NewMem(), priv, 0.30, 100, time.Hour)
	require.Zero(t, b.totalLatencyOf("n1"), "unmeasured reads 0")
	b.observeTotalLatency("n1", 2000)
	require.InDelta(t, 2000, b.totalLatencyOf("n1"), 1e-9, "the first sample sets the figure")
	b.observeTotalLatency("n1", 1000)
	require.InDelta(t, 0.3*1000+0.7*2000, b.totalLatencyOf("n1"), 1e-9, "later samples blend (EWMA 0.3)")
	b.observeTotalLatency("n1", 0)
	b.observeTotalLatency("n1", -5)
	require.InDelta(t, 1700, b.totalLatencyOf("n1"), 1e-9, "a non-positive sample is ignored")
}

func TestTotalLatencySharedAcrossInstances(t *testing.T) {
	mr := miniredis.RunT(t)
	t.Setenv("ROGERAI_REDIS_URL", "redis://"+mr.Addr())
	t.Setenv("ROGERAI_MULTI_INSTANCE", "1")
	_, priv, _ := ed25519.GenerateKey(nil)
	a := buildBroker(store.NewMem(), priv, 0.30, 100, time.Hour)
	b := buildBroker(store.NewMem(), priv, 0.30, 100, time.Hour)
	t.Cleanup(func() { _ = a.shared.Close(); _ = b.shared.Close() })

	a.observeTotalLatency("n1", 9000)
	require.Zero(t, b.totalLatencyOf("n1"), "instance B has not synced yet")
	b.syncTotalLatency()
	require.InDelta(t, 9000, b.totalLatencyOf("n1"), 1e-9, "instance B ranks on instance A's measurement")

	// B's next sample blends into the shared figure, and A picks it up on its own sync.
	b.observeTotalLatency("n1", 1000)
	a.syncTotalLatency()
	require.InDelta(t, 0.3*1000+0.7*9000, a.totalLatencyOf("n1"), 1e-9)

	// A store outage keeps each instance's last figure (degrade, never flap to unmeasured).
	mr.Close()
	b.syncTotalLatency()
	require.InDelta(t, 0.3*1000+0.7*9000, b.totalLatencyOf("n1"), 1e-9)
}

func TestMsSinceKeepsSubMillisecondServes(t *testing.T) {
	require.Greater(t, msSince(time.Now().Add(-300*time.Microsecond)), 0.0, "a sub-millisecond serve is still a sample")
	require.InDelta(t, 1500, msSince(time.Now().Add(-1500*time.Millisecond)), 50)
}

// slice-6 review 2026-10-06: the shared hash is pruned per node, so a node that stopped
// serving drops out after the TTL instead of living as long as any other node keeps writing.
func TestTotalLatencyPrunesStaleNodes(t *testing.T) {
	mr := miniredis.RunT(t)
	t.Setenv("ROGERAI_REDIS_URL", "redis://"+mr.Addr())
	t.Setenv("ROGERAI_MULTI_INSTANCE", "1")
	_, priv, _ := ed25519.GenerateKey(nil)
	b := buildBroker(store.NewMem(), priv, 0.30, 100, time.Hour)
	t.Cleanup(func() { _ = b.shared.Close() })

	b.observeTotalLatency("fresh", 1200)
	stale := time.Now().Add(-totalLatencyTTL - time.Minute).UnixMilli()
	mr.HSet(totalLatencyKey(), "stale", fmt.Sprintf("900@%d", stale))

	got, err := b.shared.totalLatencies()
	require.NoError(t, err)
	require.InDelta(t, 1200, got["fresh"], 1e-9)
	_, kept := got["stale"]
	require.False(t, kept, "a figure older than the TTL is not served")
	require.False(t, mr.Exists(totalLatencyKey()) && mr.HGet(totalLatencyKey(), "stale") != "", "and it is deleted")
}
