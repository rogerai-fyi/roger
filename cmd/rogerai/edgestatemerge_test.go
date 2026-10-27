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
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/store"
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

// ---- the review of 2026-09-29 -------------------------------------------------------------
// Four defects were PROVEN against the first version of this merge, which compared whole records.
// Each one is now a scenario here, named for what it did.

// A HEARTBEAT IS NOT AN EDIT. The host sees every node on every pass, so comparing whole records
// made it "the process that changed this node" every time, and it then overwrote a rename typed in
// another terminal with its own stale copy.
func TestAHeartbeatDoesNotEatAForeignRename(t *testing.T) {
	useTempConfig(t)
	edgeSeed(t, edgeNodeFor("n_pi", "quiet-pi", "board"))

	host, err := loadEdgeState()
	require.NoError(t, err)
	require.NoError(t, host.save()) // the host is long-lived: it has a savedAt

	// The owner renames the node in a second terminal.
	other, err := loadEdgeState()
	require.NoError(t, err)
	require.NoError(t, other.fleet.Rename("n_pi", "bench-pi"))
	require.NoError(t, other.save())

	// The host merely SEES the node again: a new heartbeat, nothing an owner decided.
	n, ok, err := host.fleet.Get("n_pi")
	require.NoError(t, err)
	require.True(t, ok)
	n.LastSeen = time.Now().Add(time.Minute).Unix()
	n.Presence = string(edge.PresenceVerified)
	_, err = host.db.EnrollEdgeNode(n)
	require.NoError(t, err)
	require.NoError(t, host.save())

	on := edgeIDsOnDisk(t)
	require.Equal(t, "bench-pi", on["n_pi"], "a heartbeat overwrote a rename made in another terminal")
}

// AND IT DOES NOT RESURRECT WHAT WAS FORGOTTEN. Forgetting revokes the node's certificate, so a
// record that comes back is a member that can never talk: it sits on the fleet screen, the ladder
// dials it, and the owner has to forget it again.
func TestAHeartbeatDoesNotResurrectAForgottenNode(t *testing.T) {
	useTempConfig(t)
	edgeSeed(t, edgeNodeFor("n_pi", "quiet-pi", "board"), edgeNodeFor("n_nas", "nas", "host"))

	host, err := loadEdgeState()
	require.NoError(t, err)
	require.NoError(t, host.save())

	other, err := loadEdgeState()
	require.NoError(t, err)
	require.NoError(t, other.fleet.Forget("n_pi"))
	require.NoError(t, other.save())

	// A sighting was in flight when the forget landed.
	n, ok, _ := host.fleet.Get("n_pi")
	require.True(t, ok)
	n.LastSeen = time.Now().Add(time.Minute).Unix()
	_, err = host.db.EnrollEdgeNode(n)
	require.NoError(t, err)
	require.NoError(t, host.save())

	require.NotContains(t, edgeIDsOnDisk(t), "n_pi", "a sighting brought back a node whose certificate was revoked")
}

// AN ADOPTED EDIT IS NOT UNDONE BY THE NEXT SAVE. The base advanced to the merged record while
// memory kept the stale one, so the following save saw "mine differs from base" and put the stale
// record back.
func TestAnAdoptedEditSurvivesTheNextSave(t *testing.T) {
	useTempConfig(t)
	edgeSeed(t, edgeNodeFor("n_pi", "quiet-pi", "board"))

	host, err := loadEdgeState()
	require.NoError(t, err)
	require.NoError(t, host.save())

	other, err := loadEdgeState()
	require.NoError(t, err)
	require.NoError(t, other.fleet.Rename("n_pi", "bench-pi"))
	require.NoError(t, other.save())

	require.NoError(t, host.save())
	require.Equal(t, "bench-pi", edgeIDsOnDisk(t)["n_pi"], "the first save adopted the rename")
	require.NoError(t, host.save())
	require.Equal(t, "bench-pi", edgeIDsOnDisk(t)["n_pi"], "the second save undid the rename it had just adopted")

	// And memory agrees with the file, so the screen beside it is not telling a different story.
	n, ok, err := host.fleet.Get("n_pi")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "bench-pi", n.Name)
}

// A CANDIDATE THAT HAS BEEN ADOPTED IS NOT PUT BACK ON THE BENCH. The map lands the cursor on a
// waiting device, so a resurrected candidate means the screen opens offering to adopt a phone that
// is already a member.
func TestAnAdoptedCandidateIsNotPutBackOnTheBench(t *testing.T) {
	useTempConfig(t)
	edgeSeed(t, edgeNodeFor("n_nas", "nas", "host"))

	// A process whose candidate list still holds the phone.
	stale, err := loadEdgeState()
	require.NoError(t, err)
	phone := edgeNodeFor("n_phone", "gentle-ibex-14", "mobile")
	phone.Presence = string(edge.PresenceCandidate)
	stale.candidates = []store.EdgeNode{phone}

	// Elsewhere it is adopted.
	host, err := loadEdgeState()
	require.NoError(t, err)
	_, err = host.fleet.Enroll(edgeNodeFor("n_phone", "gentle-ibex-14", "mobile"))
	require.NoError(t, err)
	require.NoError(t, host.save())

	require.NoError(t, stale.save())

	st, err := loadEdgeState()
	require.NoError(t, err)
	require.Contains(t, edgeIDsOnDisk(t), "n_phone", "the adopted phone left the fleet")
	for _, c := range st.candidates {
		require.NotEqual(t, "n_phone", c.ID, "an adopted device is still on the bench waiting to be adopted")
	}
}

// TWO EDITS TO DIFFERENT FIELDS OF ONE NODE BOTH LAND. A record-grained merge lost one of them,
// which the doc comment claimed it would not.
func TestTwoEditsToOneNodeBothLand(t *testing.T) {
	useTempConfig(t)
	edgeSeed(t, edgeNodeFor("n_pi", "quiet-pi", "board"))

	tagger, err := loadEdgeState()
	require.NoError(t, err)
	namer, err := loadEdgeState()
	require.NoError(t, err)

	require.NoError(t, tagger.fleet.SetTags("n_pi", []string{"bench"}))
	require.NoError(t, namer.fleet.Rename("n_pi", "bench-pi"))
	require.NoError(t, tagger.save())
	require.NoError(t, namer.save())

	st, err := loadEdgeState()
	require.NoError(t, err)
	n, ok, err := st.fleet.Get("n_pi")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "bench-pi", n.Name, "the rename was lost")
	require.Equal(t, []string{"bench"}, n.Tags, "the tag was lost")
}

// An event is a thing that happened: a process that did not see it has not decided it did not.
func TestHistoryIsTheUnionOfWhatEachSideSaw(t *testing.T) {
	a := []store.EdgeEvent{{At: 10, What: "adopted"}, {At: 30, What: "renamed"}}
	b := []store.EdgeEvent{{At: 20, What: "serving"}, {At: 30, What: "renamed"}}
	got := edgeMergeHistory(a, b)
	require.Len(t, got, 3, "an event was dropped for being on only one side: %+v", got)
	require.Equal(t, int64(10), got[0].At, "history is oldest first")
	require.Equal(t, int64(30), got[2].At)

	// It stays bounded the way the record is.
	var many []store.EdgeEvent
	for i := 0; i < edge.MaxNodeHistory+20; i++ {
		many = append(many, store.EdgeEvent{At: int64(i), What: "x"})
	}
	require.Len(t, edgeMergeHistory(many, nil), edge.MaxNodeHistory+20, "one side alone is passed through whole")
	require.Len(t, edgeMergeHistory(many, []store.EdgeEvent{{At: 9999, What: "y"}}), edge.MaxNodeHistory)
}

// The field-grained rule itself, stated once.
func TestIntentAndObservationMergeDifferently(t *testing.T) {
	base := store.EdgeNode{ID: "n", Name: "a", Tags: []string{"x"}, LastSeen: 100, Presence: "VERIFIED"}
	// Intent changed here, observation fresher there: each side wins its own half.
	mine := base
	mine.Name = "mine"
	theirs := base
	theirs.LastSeen, theirs.Presence = 200, "DARK"
	got := edgeMergeOne(base, mine, theirs, true)
	require.Equal(t, "mine", got.Name, "an owner's edit here was lost")
	require.Equal(t, int64(200), got.LastSeen, "a fresher sighting there was lost")
	require.Equal(t, "DARK", got.Presence)

	// Intent changed there, and here nothing but a sighting: their edit stands, my sighting lands.
	mine2 := base
	mine2.LastSeen = 300
	theirs2 := base
	theirs2.Name = "theirs"
	got = edgeMergeOne(base, mine2, theirs2, true)
	require.Equal(t, "theirs", got.Name, "an edit made elsewhere was overwritten by a heartbeat")
	require.Equal(t, int64(300), got.LastSeen)
}

// NO SURFACE PRINTS A 48-CHARACTER ID AS A NAME. The TUI, the Limoni screens and the console were
// swept one at a time; this pins the CLI's own text, which is the surface a script reads and the one
// an owner sees first.
func TestNoCLISurfacePrintsAnIdAsAName(t *testing.T) {
	useTempConfig(t)
	id := "n_" + strings.Repeat("ef", 24)
	n := edgeNodeFor(id, id, "mobile") // enrolment fell back to the id as the name
	n.Tags = []string{"bench"}
	n.Instances = []store.EdgeInstance{{Name: "share", Software: &store.EdgeSoftware{Roger: "6.13.0"}}}
	edgeSeed(t, n, edgeNodeFor("n_nas", "nas", "host"))

	for _, c := range []struct {
		name string
		args []string
	}{
		{"the fleet table", []string{"edge", "list"}},
		{"the inventory", []string{"edge", "inventory"}},
		{"the tags", []string{"edge", "tag", id}},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, _ := edgeRun(t, c.args...)
			if strings.Contains(out, id) && !strings.Contains(out, "roger edge tag "+id) {
				t.Errorf("%s prints the whole id as a name:\n%s", c.name, out)
			}
			// Updated 2026-10-02 for the approved fleet-window ruling: an unnamed unit is called by its
			// hardware word (edge.UnitName), not a short id.
			if strings.Contains(out, edge.ShortID(id)) && !strings.Contains(out, "roger edge tag "+id) {
				t.Errorf("%s names the node by a short id:\n%s", c.name, out)
			}
			if !strings.Contains(out, "phone") {
				t.Errorf("%s does not name the node at all:\n%s", c.name, out)
			}
		})
	}
	// And the shared name lookup every report reads is named at the source.
	if got := edgeNodeNames()[id]; got != "phone" {
		t.Errorf("the shared name lookup gives %q, want the rack's word", got)
	}
}

// THE BASE MUST NOT SHARE MEMORY WITH WHAT IT IS THE BASE OF. A node record carries slices, and a
// shallow copy of the record shares their backing array: mutating a capability IN PLACE then
// changed the base too, the merge compared the edit against itself, concluded nothing had changed,
// and took the disk's older version. The edit vanished with no error anywhere.
//
// Found on 2026-09-29 by building the actuate ceremony on top of this, which is the one edit that
// mutates a capability in place. Rename and SetTags replace the field, so none of the tests above
// could ever have caught it.
func TestAnInPlaceEditIsNotLostToTheBase(t *testing.T) {
	useTempConfig(t)
	edgeSeed(t, store.EdgeNode{ID: "n_door", Name: "shed-door", Kind: "board",
		Presence: string(edge.PresenceVerified), LastSeen: time.Now().Unix(),
		Caps: []store.EdgeCap{{Name: "actuate", State: string(edge.Claimed)}}})

	st, err := loadEdgeState()
	require.NoError(t, err)
	require.NoError(t, st.fleet.ConfirmActuate("n_door", "owner")) // mutates Caps[i] IN PLACE
	require.NoError(t, st.save())

	fresh, err := loadEdgeState()
	require.NoError(t, err)
	n, ok, err := fresh.fleet.Get("n_door")
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, n.Caps, 1)
	require.Equal(t, string(edge.Verified), n.Caps[0].State, "an in-place capability edit was lost")
	require.Equal(t, "owner", n.Caps[0].ConfirmedBy)

	// The same for tags and history, the other slices on the record.
	require.NoError(t, fresh.fleet.SetTags("n_door", []string{"outside"}))
	require.NoError(t, fresh.save())
	again, err := loadEdgeState()
	require.NoError(t, err)
	n, _, _ = again.fleet.Get("n_door")
	require.Equal(t, []string{"outside"}, n.Tags)
	require.Equal(t, string(edge.Verified), n.Caps[0].State, "confirming was undone by a later tag edit")
}

// A phone's vitals are an observation: the fresher sighting's vitals win, like its presence does.
func TestAPhonesVitalsMergeWithTheFresherSighting(t *testing.T) {
	low, high := 20, 80
	base := store.EdgeNode{ID: "n_p", Name: "nina", Kind: "mobile", LastSeen: 100}
	mine := base
	mine.LastSeen, mine.Phone = 200, &store.EdgePhone{BatteryPct: &high}
	theirs := base
	theirs.LastSeen, theirs.Phone = 150, &store.EdgePhone{BatteryPct: &low}
	require.Equal(t, 80, *edgeMergeOne(base, mine, theirs, true).Phone.BatteryPct)
	mine.LastSeen = 120
	require.Equal(t, 20, *edgeMergeOne(base, mine, theirs, true).Phone.BatteryPct)
}
