package main

// A test binary runs every test in one process, and an Edge host's serve loop reads its paths
// from the environment on every pass. A host one test left running therefore writes its own copy
// of the Edge into whatever temp config the NEXT test set up - with its own account on it, which
// the next test's reader then discards as another account's cache (CI, 2026-10-28: the maintenance
// test found no record for its own machine). useTempConfig stops every host started under it.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/edge"
)

func TestAnEdgeHostATestLeftRunningDoesNotWriteIntoTheNextTest(t *testing.T) {
	var leaked *edgeHost
	t.Run("a test that leaves its host running", func(t *testing.T) {
		useTempConfig(t)
		edgeWriteAuth(t, "owner") // signed in to Core, and not on any Edge yet
		t.Setenv(edge.EnvDiscovery, "1")
		edgeUseOptions(t, func(o edge.Options) edge.Options {
			o.Config.Interval = 20 * time.Millisecond
			o.Plane = func() (edge.Transport, error) { return nil, edge.ErrNoLAN } // no real network
			return o
		})
		h, err := newEdgeHost("roger-leaky")
		require.NoError(t, err)
		h.start(context.Background())
		require.True(t, h.running())
		leaked = h // No stop: this is the leak.
	})

	t.Run("the next test", func(t *testing.T) {
		select {
		case <-leaked.done:
		default:
			t.Fatal("the host the last test left running is still serving")
		}
		maintainRig(t)
		id := edgeThisNodeID()
		require.NotEmpty(t, id)
		st, err := loadEdgeState()
		require.NoError(t, err)
		_, ok, err := st.fleet.Get(id)
		require.NoError(t, err)
		require.True(t, ok, "enrollment put this machine on its own Edge")

		time.Sleep(300 * time.Millisecond) // many passes of any host still running
		st, err = loadEdgeState()
		require.NoError(t, err)
		_, ok, err = st.fleet.Get(id)
		require.NoError(t, err)
		require.True(t, ok, "this machine's record is still on this test's Edge")
	})
}

// The same write, in one process with no test leaking anything: a roger that is running while
// this machine joins an Edge from another terminal (the case adoptEnrollment exists for) holds
// a state loaded under the OLD account. Between the enrollment and that roger noticing it, one
// of its passes saved that state over the new Edge's file, account and all, and every reader
// then discarded the file as another account's cache - this machine's own record included.
func TestAStateLoadedUnderAnotherAccountDoesNotOverwriteTheEdgeThisMachineJoined(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	stale, err := loadEdgeState()
	require.NoError(t, err)
	require.Equal(t, "owner", stale.account)

	_, err = edgeDesignateCore(edgeAuthDir(), "shed")
	require.NoError(t, err)
	_, err = edgeEnrollCore(loadConfig(), "shed", "", "")
	require.NoError(t, err)
	id := edgeThisNodeID()
	require.NotEmpty(t, id)
	require.NotEqual(t, "owner", edgeAccount(), "the machine now belongs to its own Edge")

	require.ErrorIs(t, stale.save(), errEdgeAccountMoved, "the stale state refuses to write")

	st, err := loadEdgeState()
	require.NoError(t, err)
	_, ok, err := st.fleet.Get(id)
	require.NoError(t, err)
	require.True(t, ok, "this machine's record is still on the Edge it joined")
}
