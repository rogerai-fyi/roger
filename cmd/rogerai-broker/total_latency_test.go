package main

// total_latency_test.go: the total-latency measurement sort:latency ranks on (contract §14.B7
// #19, founder ruling 2026-10-05). Every served attempt, direct or bridged, folds its time to the
// whole answer into a per-node EWMA that lives in the shared store, so every instance ranks on
// the same figure; an instance without the shared store keeps its own.

import (
	"crypto/ed25519"
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
