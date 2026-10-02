package main

// features/edge/node_identity.feature: no id is ever a name. The confirmations a person reads -
// rename, forget, adopt, enroll - and the grant lines name a unit the way the rack does
// (edge.UnitName, edge.SelfLabel for this machine), never by its id or a short cut of one. A grant's
// own id (grant_...) and a rule's id stay: those are identifiers, not names.

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/store"
)

// unnamedPhone is a phone nobody named: its record name IS its id, so the rack calls it "phone".
const unnamedPhone = "n_" + "7c7c7c7c7c7c7c7c7c7c7c7c7c7c7c7c7c7c7c7c7c7c7c7c"

func requireNoID(t *testing.T, s string) {
	t.Helper()
	require.NotContains(t, s, "n_", "a sentence names a unit, never by its id: %q", s)
}

func TestRenameConfirmationsNameTheUnitNotItsID(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	edgeSeed(t, store.EdgeNode{ID: unnamedPhone, Name: unnamedPhone, Kind: "mobile"},
		store.EdgeNode{ID: "n_shed", Name: "shed", Kind: "host"})

	out, code := edgeRun(t, "edge", "name", unnamedPhone, "pocket")
	require.Equal(t, 0, code, out)
	requireNoID(t, out)
	require.Contains(t, out, "phone is now pocket")

	out, code = edgeRun(t, "edge", "name", "pocket", "pocket")
	require.Equal(t, 0, code, out)
	requireNoID(t, out)
	require.Contains(t, out, "pocket is already named pocket")

	out, code = edgeRun(t, "edge", "name", "pocket", "shed")
	require.NotEqual(t, 0, code)
	requireNoID(t, out)
	require.Contains(t, out, "shed already holds it")
}

func TestAdoptConfirmationNamesTheDeviceNotItsID(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	_, err := edgeauth.Designate(edgeAuthDir(), "hub")
	require.NoError(t, err)
	st, err := loadEdgeState()
	require.NoError(t, err)
	st.candidates = []store.EdgeNode{{ID: unnamedPhone, Name: unnamedPhone, Kind: "mobile"}}
	require.NoError(t, st.save())

	msg, err := edgeAdoptWant(unnamedPhone)
	require.NoError(t, err)
	requireNoID(t, msg)
	require.True(t, strings.HasPrefix(msg, "adopted phone "), msg)
}

func TestEnrollConfirmationNamesThisMachineNotItsID(t *testing.T) {
	for _, c := range []struct {
		name, want string
		already    bool
	}{
		{"workshop", `enrolled "workshop" on the Edge rooted at hub.`, false},
		{unnamedPhone, `enrolled "gm-93" on the Edge rooted at hub.`, false},
		{unnamedPhone, `already enrolled as "gm-93", on the Edge rooted at hub.`, true},
	} {
		res := edgeEnrollResult{NodeID: unnamedPhone, Name: c.name, Authority: "hub", AlreadyEnrolled: c.already, NotAfter: time.Now()}
		got := edgeEnrolledLine(res, "gm-93")
		requireNoID(t, got)
		require.Equal(t, c.want, got)
	}
}

func TestGrantLinesNameBothEndsNotTheirIDs(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	_, err := edgeauth.Designate(edgeAuthDir(), "hub")
	require.NoError(t, err)
	edgeSeed(t, store.EdgeNode{ID: unnamedPhone, Name: unnamedPhone, Kind: "mobile"},
		store.EdgeNode{ID: "n_shed", Name: "shed", Kind: "host"})

	_, line, err := edgeGrantIssueCore(unnamedPhone, "operate", "n_shed", "7d", 0)
	require.NoError(t, err)
	requireNoID(t, line)
	require.True(t, strings.HasPrefix(line, "phone may operate shed until "), line)

	out, code := edgeRun(t, "edge", "grant")
	require.Equal(t, 0, code, out)
	for _, ln := range strings.Split(strings.TrimSpace(out), "\n") {
		_, sentence, _ := strings.Cut(ln, "  ") // the grant's own id leads the line: an identifier
		requireNoID(t, sentence)
	}
	require.Contains(t, out, "phone may operate shed")

	// A unit no longer on the Edge is said so, not printed as an id.
	require.Equal(t, edgeGoneUnit, edgeGrantWho("n_"+strings.Repeat("ab", 24)))
	lines, err := edgeGrantLines(map[string]string{}, time.Now())
	require.NoError(t, err)
	for _, g := range lines {
		requireNoID(t, g.Caller)
		requireNoID(t, g.Callee)
	}
}
