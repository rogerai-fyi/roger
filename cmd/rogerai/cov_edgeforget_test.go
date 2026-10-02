package main

// Coverage for `roger edge forget` beyond the fleet: a pending adoption by name or id, an ambiguous
// name refused with the ids to choose from, the prompt that gates every destructive path, and a
// member forgotten only on a deliberate yes.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/store"
)

func TestEdgeForgetAPendingAdoption(t *testing.T) {
	newFacedAuthority(t, "desk")
	prevIn := edgeStdin
	t.Cleanup(func() { edgeStdin = prevIn })
	local, _, err := edgeauth.OpenLocal(edgeAuthDir())
	require.NoError(t, err)
	require.NoError(t, local.GrantClaim("n_p1", "pocket"))
	require.NoError(t, local.GrantClaim("n_p2", "pocket"))
	require.NoError(t, local.GrantClaim("n_bare", ""))

	out, code := edgeRun(t, "edge", "forget", "pocket", "--yes")
	require.Equal(t, 1, code, out)
	require.Contains(t, out, `"pocket" names 2 adopted devices - forget one by its node id`)
	require.True(t, local.ClaimGranted("n_p1"), "an ambiguous name forgets nothing")
	require.True(t, local.ClaimGranted("n_p2"))

	edgeStdin = strings.NewReader("\n")
	out, code = edgeRun(t, "edge", "forget", "n_p1")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "forget pocket? its claim grant is cleared and any certificate it holds is revoked [y/N]")
	require.Contains(t, out, "left alone.")
	require.True(t, local.ClaimGranted("n_p1"))

	edgeStdin = strings.NewReader("y\n")
	out, code = edgeRun(t, "edge", "forget", "n_p1")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "forgot pocket: its claim grant is cleared.")
	require.False(t, local.ClaimGranted("n_p1"))

	out, code = edgeRun(t, "edge", "forget", "n_bare", "--yes")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "forgot a device adopted but not yet claimed: its claim grant is cleared.", "a claim with no name is described, never named by its id")
	require.NotContains(t, out, "n_bare")
	require.False(t, local.ClaimGranted("n_bare"))

	out, code = edgeRun(t, "edge", "forget", "n_nobody", "--yes")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, `nothing to forget: "n_nobody" is not on this Edge.`)
}

func TestEdgeForgetAMemberOnlyOnAYes(t *testing.T) {
	newFacedAuthority(t, "desk")
	edgeSeed(t, store.EdgeNode{ID: "n_attic", Name: "attic", Kind: "board"})
	prevIn := edgeStdin
	t.Cleanup(func() { edgeStdin = prevIn })

	edgeStdin = strings.NewReader("no\n")
	out, code := edgeRun(t, "edge", "forget", "attic")
	require.Equal(t, 0, code, out)
	require.Contains(t, out, "forget attic? it leaves this Edge and its pinned certificate is cleared [y/N]")
	require.Contains(t, out, "left alone.")
	_, _, ok, err := edgeLookup("attic")
	require.NoError(t, err)
	require.True(t, ok, "no is no")

	edgeStdin = strings.NewReader("y\n")
	out, code = edgeRun(t, "edge", "forget", "attic")
	require.Equal(t, 0, code, out)
	_, _, ok, err = edgeLookup("attic")
	require.NoError(t, err)
	require.False(t, ok)
}
