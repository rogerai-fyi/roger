package main

// THE STATE FILE IS SHARED, so a write must never be a whole-file overwrite from one process's
// memory. Two rogers run on this machine all the time - the host's own pass and whatever the owner
// types in a second terminal, plus the fleet screen, which is a child process of its own - and they
// all read and write edge.json.
//
// This is the founder's vanished phone (2026-09-27): a device adopted and saved by one process,
// then silently dropped by another process that had loaded the file before that save, and then
// FORGOTTEN for good when the host folded the disk back in and found it missing.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

func edgeNodeFor(id, name, kind string) store.EdgeNode {
	return store.EdgeNode{ID: id, Name: name, Kind: kind,
		Presence: string(edge.PresenceVerified), LastSeen: time.Now().Unix()}
}

func edgeIDsOnDisk(t *testing.T) map[string]string {
	t.Helper()
	st, err := loadEdgeState()
	require.NoError(t, err)
	nodes, err := st.list()
	require.NoError(t, err)
	out := map[string]string{}
	for _, n := range nodes {
		out[n.ID] = n.Name
	}
	return out
}

// A process that loaded the file BEFORE another process adopted a device must not drop that device
// when it writes. This is the lost update, and it is what made the phone disappear.
func TestASecondProcessDoesNotDropADeviceItNeverSaw(t *testing.T) {
	useTempConfig(t)
	edgeSeed(t, edgeNodeFor("n_nas", "nas", "host"))

	// The second terminal loads the file as it is now: one node.
	other, err := loadEdgeState()
	require.NoError(t, err)

	// Meanwhile the host adopts the phone and saves it.
	host, err := loadEdgeState()
	require.NoError(t, err)
	_, err = host.fleet.Enroll(edgeNodeFor("n_phone", "gentle-ibex-14", "mobile"))
	require.NoError(t, err)
	require.NoError(t, host.save())
	require.Contains(t, edgeIDsOnDisk(t), "n_phone", "the phone is on disk after the host saves it")

	// Now the second terminal does its own edit and saves. It never knew about the phone.
	require.NoError(t, other.fleet.Rename("n_nas", "nas-2"))
	require.NoError(t, other.save())

	on := edgeIDsOnDisk(t)
	require.Equal(t, "nas-2", on["n_nas"], "the second terminal's rename must land")
	require.Contains(t, on, "n_phone", "the phone was dropped by a process that never saw it")
}

// And the other direction: a node this process really did forget must STAY forgotten, even though
// the disk still has it. A merge that keeps everything would make forget impossible.
func TestAForgetStillRemovesTheNode(t *testing.T) {
	useTempConfig(t)
	edgeSeed(t, edgeNodeFor("n_nas", "nas", "host"), edgeNodeFor("n_pi", "quiet-pi", "board"))

	st, err := loadEdgeState()
	require.NoError(t, err)
	require.NoError(t, st.fleet.Forget("n_pi"))
	require.NoError(t, st.save())

	on := edgeIDsOnDisk(t)
	require.NotContains(t, on, "n_pi", "a forget must remove the node")
	require.Contains(t, on, "n_nas")
}

// An edit made elsewhere is not undone by a process that did not touch that node.
func TestAnEditElsewhereSurvivesAnUnrelatedSave(t *testing.T) {
	useTempConfig(t)
	edgeSeed(t, edgeNodeFor("n_nas", "nas", "host"), edgeNodeFor("n_pi", "quiet-pi", "board"))

	early, err := loadEdgeState()
	require.NoError(t, err)

	elsewhere, err := loadEdgeState()
	require.NoError(t, err)
	require.NoError(t, elsewhere.fleet.Rename("n_pi", "bench-pi"))
	require.NoError(t, elsewhere.save())

	// The early process edits a DIFFERENT node and saves. It still holds "quiet-pi" in memory.
	require.NoError(t, early.fleet.Rename("n_nas", "nas-2"))
	require.NoError(t, early.save())

	on := edgeIDsOnDisk(t)
	require.Equal(t, "nas-2", on["n_nas"])
	require.Equal(t, "bench-pi", on["n_pi"], "the rename made elsewhere was undone by an unrelated save")
}

// The merge rule itself, case by case. This is the whole of what makes a shared file safe, so every
// branch is named rather than left to the two-process tests above.
func TestTheStateMergeRule(t *testing.T) {
	n := func(id, name string) store.EdgeNode { return edgeNodeFor(id, name, "host") }
	base := edgeBaseOf([]store.EdgeNode{n("a", "a"), n("b", "b")})

	byID := func(in []store.EdgeNode) map[string]string {
		out := map[string]string{}
		for _, x := range in {
			out[x.ID] = x.Name
		}
		return out
	}

	// Untouched here, edited there: their edit stands.
	got := byID(edgeMergeNodes(base, []store.EdgeNode{n("a", "a"), n("b", "b")}, []store.EdgeNode{n("a", "a2"), n("b", "b")}))
	require.Equal(t, map[string]string{"a": "a2", "b": "b"}, got)

	// Changed here, untouched there: this edit stands.
	got = byID(edgeMergeNodes(base, []store.EdgeNode{n("a", "a3"), n("b", "b")}, []store.EdgeNode{n("a", "a"), n("b", "b")}))
	require.Equal(t, map[string]string{"a": "a3", "b": "b"}, got)

	// Added here: kept, even though the file has never seen it.
	got = byID(edgeMergeNodes(base, []store.EdgeNode{n("a", "a"), n("b", "b"), n("c", "c")}, []store.EdgeNode{n("a", "a"), n("b", "b")}))
	require.Contains(t, got, "c")

	// Added there: kept, and not mistaken for something this process forgot.
	got = byID(edgeMergeNodes(base, []store.EdgeNode{n("a", "a"), n("b", "b")}, []store.EdgeNode{n("a", "a"), n("b", "b"), n("d", "d")}))
	require.Contains(t, got, "d")

	// Forgotten here: gone, whatever the file still says.
	got = byID(edgeMergeNodes(base, []store.EdgeNode{n("a", "a")}, []store.EdgeNode{n("a", "a"), n("b", "b")}))
	require.NotContains(t, got, "b")

	// Forgotten there, untouched here: stays gone.
	got = byID(edgeMergeNodes(base, []store.EdgeNode{n("a", "a"), n("b", "b")}, []store.EdgeNode{n("a", "a")}))
	require.NotContains(t, got, "b")

	// Forgotten there but CHANGED here: it comes back, because losing an edit is silent and a
	// returning row is not - forgetting revoked its certificate, so it cannot talk regardless.
	got = byID(edgeMergeNodes(base, []store.EdgeNode{n("a", "a"), n("b", "b9")}, []store.EdgeNode{n("a", "a")}))
	require.Equal(t, "b9", got["b"])

	// An empty base (a brand new file) keeps everything from both sides and drops nothing.
	got = byID(edgeMergeNodes(nil, []store.EdgeNode{n("a", "a")}, []store.EdgeNode{n("z", "z")}))
	require.Equal(t, map[string]string{"a": "a", "z": "z"}, got)
}

// Two processes writing at the same moment do not tear the file: the lock makes each write a whole
// read-modify-write, so both edits survive.
func TestConcurrentWritesBothLand(t *testing.T) {
	useTempConfig(t)
	edgeSeed(t, edgeNodeFor("n_a", "a", "host"), edgeNodeFor("n_b", "b", "host"))

	one, err := loadEdgeState()
	require.NoError(t, err)
	two, err := loadEdgeState()
	require.NoError(t, err)
	require.NoError(t, one.fleet.Rename("n_a", "a-one"))
	require.NoError(t, two.fleet.Rename("n_b", "b-two"))

	done := make(chan error, 2)
	go func() { done <- one.save() }()
	go func() { done <- two.save() }()
	require.NoError(t, <-done)
	require.NoError(t, <-done)

	on := edgeIDsOnDisk(t)
	require.Equal(t, "a-one", on["n_a"])
	require.Equal(t, "b-two", on["n_b"])
}
