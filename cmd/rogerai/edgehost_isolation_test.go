package main

// A test binary runs every test in one process, and an Edge host's serve loop reads its paths
// from the environment on every pass. A host one test left running therefore writes its own copy
// of the Edge into whatever temp config the NEXT test set up - with its own account on it, which
// the next test's reader then discards as another account's cache (CI, 2026-10-28: the maintenance
// test found no record for its own machine). useTempConfig stops every host started under it.

import (
	"bytes"
	"context"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/store"
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

// A forget from a roger whose view is under an account this machine has since left refuses BEFORE
// it does anything that cannot be undone: no departure is recorded and no certificate is revoked
// ahead of a save that would be refused anyway (pre-push audit, 2026-10-28).
func TestAForgetFromAViewUnderAnotherAccountChangesNothing(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	edgeSeed(t, atticPi4("n_shed", "shed"))
	h, err := newEdgeHost("roger-desk")
	require.NoError(t, err)

	_, err = edgeDesignateCore(edgeAuthDir(), "home")
	require.NoError(t, err)
	_, err = edgeEnrollCore(loadConfig(), "home", "", "")
	require.NoError(t, err)
	before := snapshotDir(t, filepath.Dir(configPath()))

	require.ErrorIs(t, h.forgetNode("n_shed"), errEdgeAccountMoved)
	_, err = os.Stat(edgeDepartedPath())
	require.True(t, os.IsNotExist(err), "no departure was recorded")
	require.Equal(t, before, snapshotDir(t, filepath.Dir(configPath())), "nothing on disk changed, revocations included")
}

// snapshotDir is every regular file under dir and its contents.
func snapshotDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[p] = string(b)
		return nil
	}))
	return out
}

// The same for an adopt: granting a claim is a write into the Edge's authority, so a view under an
// account this machine has left refuses before it, not at a save nobody checks.
func TestAnAdoptFromAViewUnderAnotherAccountChangesNothing(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	h, err := newEdgeHost("roger-desk")
	require.NoError(t, err)
	h.st.candidates = []store.EdgeNode{{ID: "n_phone", Name: "phone", Kind: "phone"}} // no certificate: joins by claim

	_, err = edgeDesignateCore(edgeAuthDir(), "home")
	require.NoError(t, err)
	_, err = edgeEnrollCore(loadConfig(), "home", "", "")
	require.NoError(t, err)
	before := snapshotDir(t, filepath.Dir(configPath()))

	require.ErrorIs(t, h.adopt("n_phone", "phone"), errEdgeAccountMoved)
	require.Equal(t, before, snapshotDir(t, filepath.Dir(configPath())), "no claim was granted and nothing was written")
}

// A roger whose Core login changed while it ran (no Edge identity, so nothing reloads its view) stops
// saving that view. It says why - once, not on every pass (pre-push audit, 2026-10-28).
func TestAHostWhoseLoginChangedSaysOnceWhyItStoppedSaving(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	t.Setenv(edge.EnvDiscovery, "1")
	edgeUseOptions(t, func(o edge.Options) edge.Options {
		o.Plane = func() (edge.Transport, error) { return nil, edge.ErrNoLAN } // no real network
		return o
	})
	openScan(t, scanEdge) // the owner is on [3], so the host browses and saves each pass
	h, err := newEdgeHost("roger-desk")
	require.NoError(t, err)
	require.True(t, h.arm())
	t.Cleanup(h.stopBrowse)

	edgeWriteAuth(t, "someone-else") // signed in again, as another account
	var logged bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(prev) })
	for i := 0; i < 3; i++ {
		h.runPass(context.Background())
	}
	log.SetOutput(prev)

	require.Equal(t, 1, strings.Count(logged.String(), "no longer saved"), logged.String())
	require.Contains(t, logged.String(), "someone-else")
	require.Contains(t, logged.String(), "next launch")
}
