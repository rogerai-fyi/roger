package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/store"
)

// Adopting a candidate clears it from DISCOVERED at once (not on the next pass), and a candidate
// that has already been granted a claim is not re-offered (it is adopting, not to-adopt).
func TestAdoptInstantlyClearsCandidate(t *testing.T) {
	useTempConfig(t)
	_, err := edgeauth.Designate(edgeAuthDir(), "hub")
	require.NoError(t, err)
	st, err := loadEdgeState()
	require.NoError(t, err)
	// A phone candidate (empty Pin -> claim path).
	st.candidates = []store.EdgeNode{{ID: "n_phone", Name: "gentle-ibex-14", Kind: "mobile",
		Presence: string(edge.PresenceCandidate)}}
	require.NoError(t, st.save())

	_, hooks := edgeHostOff(t)
	require.NoError(t, hooks.EdgeAdopt("n_phone", "gentle-ibex-14"))

	require.Empty(t, hooks.EdgeCandidates(), "an adopted candidate leaves DISCOVERED immediately")
	local, ok, err := edgeauth.OpenLocal(edgeAuthDir())
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, local.ClaimGranted("n_phone"), "adopting a phone grants its claim")
}

func TestDropGrantedFiltersAdoptingCandidates(t *testing.T) {
	useTempConfig(t)
	local, err := edgeauth.Designate(edgeAuthDir(), "hub")
	require.NoError(t, err)
	require.NoError(t, local.GrantClaim("n_adopting", "gentle-ibex-14"))

	got := edgeDropGranted([]store.EdgeNode{{ID: "n_adopting"}, {ID: "n_new"}})
	require.Len(t, got, 1)
	require.Equal(t, "n_new", got[0].ID, "a granted (adopting) candidate is filtered out")
}

// Forgetting a node must clear any standing claim grant, so a revoked/forgotten node cannot lean on
// a leftover grant to POST /edge/claim for a fresh certificate and rejoin (audit 2026-09-23).
func TestForgettingANodeConsumesItsGrant(t *testing.T) {
	useTempConfig(t)
	local, err := edgeauth.Designate(edgeAuthDir(), "hub")
	require.NoError(t, err)
	require.NoError(t, local.GrantClaim("n_gone", "gentle-ibex-14"))
	require.True(t, local.ClaimGranted("n_gone"))

	require.NoError(t, edgeRevokeOnForget("n_gone"))

	reopened, ok, err := edgeauth.OpenLocal(edgeAuthDir())
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, reopened.ClaimGranted("n_gone"), "forgetting a node clears its claim grant")
}

// A node already in the fleet must not be re-adopted from the TUI. Re-adopting a member mints a
// second certificate while the first is still live; a later forget would revoke only the newest and
// leave the old one working. The CLI adopt already refused a member; the TUI adopt must too, and a
// member re-advertising as a candidate is dropped from DISCOVERED (audit 2026-09-24).
func TestTuiAdoptRefusesANodeAlreadyAMember(t *testing.T) {
	useTempConfig(t)
	edgeWriteAuth(t, "owner")
	// A member of the fleet that is ALSO showing up as a candidate (its advert still says account="").
	edgeSeed(t, store.EdgeNode{ID: "n_member", Name: "gentle-ibex-14", Kind: "mobile",
		Presence: string(edge.PresenceVerified)})

	h, hooks := edgeHostOff(t)
	h.mu.Lock()
	h.st.candidates = []store.EdgeNode{{ID: "n_member", Name: "gentle-ibex-14", Kind: "mobile",
		Presence: string(edge.PresenceCandidate)}}
	_ = h.st.save()
	h.mu.Unlock()

	err := hooks.EdgeAdopt("n_member", "gentle-ibex-14")
	require.Error(t, err, "a node already a member must not be adopted again")
	require.Contains(t, err.Error(), "already a member")

	// And a member re-advertising as a candidate is filtered from what is on offer.
	require.Empty(t, edgeDropMembers(h.st.candidates, h.st.fleet),
		"a member is not offered for adoption")
}
