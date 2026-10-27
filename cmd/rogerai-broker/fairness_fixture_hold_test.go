package main

// fairness_fixture_hold_test.go - the fairness runner states a Tower's capacity, tok/s and
// TTFT in its Givens. The broker blends its own measurement of every served request into
// those figures, and the Tower path (a real agent behind a real hub) measures slow on a
// loaded machine, so a figure that is not re-applied before each relay drifts with load and
// the share a scenario asserts drifts with it. The Tower's figures must be held like a
// direct station's.

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFairnessFixtureHoldsTowerFigures(t *testing.T) {
	st := &fa6State{rpState: &rpState{foState: &foState{t: t, logs: &utLog{}}}}
	require.NoError(t, st.reset())
	t.Cleanup(st.teardown)
	require.NoError(t, st.directsAndTower("2", "1", "1", "m"))
	require.NoError(t, st.everyTierA())
	id := st.towers["t1"].nodeID

	// What a slow served relay through the Tower leaves behind.
	st.b.metricsMu.Lock()
	st.b.concurrentTPS[id], st.b.tps[id] = 1, 1
	st.b.metricsMu.Unlock()
	st.b.mu.Lock()
	tq := st.b.trust[id]
	tq.ttftMs = 9000
	st.b.trust[id] = tq
	st.b.mu.Unlock()

	for _, f := range st.held { // what fa6State.do runs before every relay
		f()
	}

	st.b.metricsMu.Lock()
	gotCap, gotTPS := st.b.concurrentTPS[id], st.b.tps[id]
	st.b.metricsMu.Unlock()
	st.b.mu.Lock()
	gotTTFT := st.b.trust[id].ttftMs
	st.b.mu.Unlock()
	require.Equal(t, 1*tpsPerSlot, gotCap, "the Tower's stated capacity is re-applied before each relay")
	require.Equal(t, 50.0, gotTPS, "the Tower's stated tok/s is re-applied before each relay")
	require.Equal(t, 200.0, gotTTFT, "Tier A's TTFT is re-applied before each relay")
}
