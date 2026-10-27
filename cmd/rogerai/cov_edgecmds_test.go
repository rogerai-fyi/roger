package main

// Coverage for `roger edge` routing every leaf to its own command, adopting by name (member, offline
// member, claim device, stranger), this machine's household line, and the agent commands' failures
// said in words rather than swallowed.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/store"
)

func TestEdgeRoutesEveryLeafToItsOwnCommand(t *testing.T) {
	newFacedAuthority(t, "desk")
	for _, tc := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"edge", "agent"}, 0, "this roger is not an agent"},
		{[]string{"edge", "maintenance"}, 0, "maintenance: OFF for this machine"},
		{[]string{"edge", "serve-control"}, 0, "serve-control: OFF for this machine"},
		{[]string{"edge", "maintain"}, 2, "usage: roger edge maintain <node> start <agent>"},
		{[]string{"edge", "logs"}, 2, "usage: roger edge logs <node>"},
		{[]string{"edge", "model"}, 2, "roger edge model <agent> <model>"},
		{[]string{"edge", "job"}, 2, "roger edge job <agent> <serve|watch|relay|none>"},
		{[]string{"edge", "authority"}, 0, `this Edge is rooted at the designated machine "desk"`},
		{[]string{"edge", "grant"}, 0, "no edge grants issued yet"},
		{[]string{"edge", "invoke"}, 2, "usage: roger edge invoke <callee> <prompt...>"},
	} {
		out, code := edgeRun(t, tc.args...)
		require.Equal(t, tc.code, code, "%v: %s", tc.args, out)
		require.Contains(t, out, tc.want, "%v", tc.args)
	}
}

func TestEdgeAdoptWantByName(t *testing.T) {
	newFacedAuthority(t, "desk")
	edgeSeed(t,
		store.EdgeNode{ID: "n_live", Name: "live", Kind: "board", Presence: string(edge.PresenceVerified)},
		store.EdgeNode{ID: "n_gone", Name: "gone", Kind: "board", Presence: string(edge.PresenceDark)},
		store.EdgeNode{ID: "n_back", Name: "back", Kind: "board", Presence: string(edge.PresenceDark)},
	)
	st, err := loadEdgeState()
	require.NoError(t, err)
	st.candidates = []store.EdgeNode{
		{ID: "n_phone", Name: "pocket", Kind: "mobile"},
		{ID: "n_back", Name: "back", Kind: "mobile"},
	}
	require.NoError(t, st.save())

	_, err = edgeAdoptWant("ghost")
	require.EqualError(t, err, `no candidate "ghost" was seen on this network (run `+"`roger edge scan`"+`)`)

	said, err := edgeAdoptWant("live")
	require.NoError(t, err)
	require.Equal(t, "live is already a member of this Edge - nothing to do (forget it first to re-adopt).", said)

	said, err = edgeAdoptWant("gone")
	require.NoError(t, err)
	require.Contains(t, said, "gone is on this Edge but offline and not on the network right now")
	require.Contains(t, said, "roger edge readmit gone")

	local, _, err := edgeauth.OpenLocal(edgeAuthDir())
	require.NoError(t, err)
	said, err = edgeAdoptWant("pocket")
	require.NoError(t, err)
	first, rest, _ := strings.Cut(said, "\n")
	require.Equal(t, "adopted pocket - it can now claim its certificate; it appears as a member when it checks in.", first)
	require.True(t, local.ClaimGranted("n_phone"))
	// The adopt now also says the one-time code the phone enters (enrollment_root_trust.feature).
	code, _, ok := local.NodeCode("n_phone")
	require.True(t, ok)
	require.Contains(t, rest, "on pocket, enter the enrollment code "+edgeauth.FormatCode(code))

	said, err = edgeAdoptWant("back")
	require.NoError(t, err)
	require.Contains(t, said, "re-admitted back - its previous certificate is revoked when it claims a fresh one")

	st, err = loadEdgeState()
	require.NoError(t, err)
	require.Empty(t, st.candidates, "an adopted candidate leaves DISCOVERED at once")
}

func TestEdgeAdoptByClaimNeedsTheRoot(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	st, err := loadEdgeState()
	require.NoError(t, err)
	st.candidates = []store.EdgeNode{{ID: "n_phone", Name: "pocket", Kind: "mobile"}}
	require.NoError(t, st.save())
	_, err = edgeAdoptWant("pocket")
	require.EqualError(t, err, "only the machine that roots this Edge can adopt a device that joins by claim")
}

func TestEdgeWriteThisMachineNamesItsRogers(t *testing.T) {
	useTempConfig(t)
	st, err := loadEdgeState()
	require.NoError(t, err)
	var b bytes.Buffer
	edgeWriteThisMachine(&b, st, time.Now())
	require.Empty(t, b.String(), "a machine that has not enrolled has no household line")

	self := newFacedAuthority(t, "desk")
	st, err = loadEdgeState()
	require.NoError(t, err)
	edgeWriteThisMachine(&b, st, time.Now())
	require.Equal(t, "this machine: desk · no roger running on it right now\n", b.String())

	for _, n := range []string{"roger", "night"} {
		reg, err := edge.Register(edgeInstancesDir(), self, edge.Instance{Name: n, Bands: []string{"tiny"}}, time.Now())
		require.NoError(t, err)
		t.Cleanup(func() { _ = reg.Deregister() })
	}
	b.Reset()
	edgeWriteThisMachine(&b, st, time.Now())
	require.Contains(t, b.String(), "this machine: desk · 2 instances")
	require.Contains(t, b.String(), "night")
	require.Equal(t, "1 instance", plural(1, "instance"))
}

func TestEdgeAgentCommandsSayWhatFailed(t *testing.T) {
	newFacedAuthority(t, "desk")
	prev := edgeSpawnRoger
	t.Cleanup(func() { edgeSpawnRoger = prev })
	edgeSpawnRoger = func(map[string]string) error { return errors.New("no executable") }

	require.NoError(t, edge.MarkPersistent(edgePersistDir(), edgeThisNodeID(), "night", time.Now()))
	_, err := edgeAgentResumed(loadConfig(), "night")
	require.EqualError(t, err, `could not launch a roger for "night": no executable`)
	require.ErrorContains(t, edgeAgentResume(loadConfig(), "ghost"), `no persistent agent named "ghost"`)

	// A durable record that cannot be written is an error, not a silent "you are an agent now".
	require.NoError(t, os.RemoveAll(edgePersistDir()))
	require.NoError(t, os.WriteFile(edgePersistDir(), []byte("in the way"), 0o600))
	require.Error(t, edgeAgentSetRole(loadConfig(), true))
	_, err = edgeAgentRoleApplied(loadConfig(), false)
	require.Error(t, err)
	require.Error(t, edgeAgentRemove(loadConfig(), "night"))
}

func TestEdgeAgentRuntimeRefusesAnUnreadableEdge(t *testing.T) {
	useTempConfig(t)
	require.NoError(t, os.MkdirAll(edgeStatePath(), 0o700)) // edge.json is a directory: unreadable
	err := runEdgeAgentCtx(context.Background(), loadConfig())
	require.ErrorContains(t, err, "the Edge agent could not start")
}

// claim.feature (2026-10-26): "an advert cannot move a member that serves a certificate onto the claim
// path", on the approve and fleet-screen surface too (edgeAdoptWant), not only the screen's adopt.
func TestApproveCannotMoveAServingMemberOntoTheClaimPath(t *testing.T) {
	newFacedAuthority(t, "desk")
	id := enrollServingNode(t, "pi")
	edgeSeed(t, store.EdgeNode{ID: id, Name: "pi", Kind: "board", Pin: strings.Repeat("ab", 32), Presence: string(edge.PresenceDark)})
	st, err := loadEdgeState()
	require.NoError(t, err)
	st.candidates = []store.EdgeNode{{ID: id, Name: "pi", Kind: "board"}} // advertises no certificate
	require.NoError(t, st.save())

	said, err := edgeAdoptWant("pi")
	require.NoError(t, err)
	require.Equal(t, "pi is already a member of this Edge - nothing to do (forget it first to re-adopt).", said)
	local, _, err := edgeauth.OpenLocal(edgeAuthDir())
	require.NoError(t, err)
	require.False(t, local.ClaimGranted(id), "no claim was granted for the pi")
}
