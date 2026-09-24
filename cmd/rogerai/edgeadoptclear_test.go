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
	require.NoError(t, local.GrantClaim("n_adopting"))

	got := edgeDropGranted([]store.EdgeNode{{ID: "n_adopting"}, {ID: "n_new"}})
	require.Len(t, got, 1)
	require.Equal(t, "n_new", got[0].ID, "a granted (adopting) candidate is filtered out")
}
