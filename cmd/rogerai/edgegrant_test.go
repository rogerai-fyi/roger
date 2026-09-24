package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/edgeauth"
)

func grantIssueFixture(t *testing.T) (*edge.GrantStore, *edgeauth.Local) {
	t.Helper()
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	st := &edge.GrantStore{Path: filepath.Join(t.TempDir(), "edge", "grants.json")}
	return st, local
}

func TestEdgeIssueGrantSignsUnderTheRootAndStoresIt(t *testing.T) {
	st, local := grantIssueFixture(t)
	exp := time.Now().Add(7 * 24 * time.Hour).Unix()

	g, err := edgeIssueGrant(st, local.Account(), local.Authority().RootKey(),
		edgeGrantSpec{caller: "n_laptop", callee: "n_workstation", capability: "operate", expiresAt: exp}, time.Now())
	require.NoError(t, err)
	require.NotEmpty(t, g.ID)
	require.Equal(t, "n_laptop", g.Caller)
	require.Equal(t, []string{"n_workstation"}, g.Nodes)
	require.Equal(t, []string{"operate"}, g.Caps)
	require.False(t, g.Self, "a caller-scoped grant is not a Self grant")

	// It verifies with the public root alone - the airgap-safe promise (control.feature).
	require.NoError(t, edge.VerifyGrant(g, local.Authority().Root()))

	// It was stored, so `roger edge grant` can list it.
	got, err := st.List()
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, g.ID, got[0].ID)
}

func TestEdgeIssueGrantRefusesAnUnknownCapability(t *testing.T) {
	st, local := grantIssueFixture(t)
	_, err := edgeIssueGrant(st, local.Account(), local.Authority().RootKey(),
		edgeGrantSpec{caller: "n_a", callee: "n_b", capability: "teleport", expiresAt: 0}, time.Now())
	require.Error(t, err)
	require.Contains(t, err.Error(), "not an edge capability")

	got, _ := st.List()
	require.Empty(t, got, "an invalid grant is never stored")
}

func TestEdgeIssueGrantRefusesAnEmptyCallee(t *testing.T) {
	st, local := grantIssueFixture(t)
	_, err := edgeIssueGrant(st, local.Account(), local.Authority().RootKey(),
		edgeGrantSpec{caller: "n_a", callee: "", capability: "operate"}, time.Now())
	require.Error(t, err)
	require.Contains(t, err.Error(), "callee")
}

func TestEdgeIssueGrantEmptyCallerIsASelfGrant(t *testing.T) {
	st, local := grantIssueFixture(t)
	g, err := edgeIssueGrant(st, local.Account(), local.Authority().RootKey(),
		edgeGrantSpec{caller: "", callee: "n_b", capability: "operate"}, time.Now())
	require.NoError(t, err)
	require.True(t, g.Self, "an owner-to-own-instances grant is a Self grant (always $0)")
	require.NoError(t, edge.VerifyGrant(g, local.Authority().Root()))
}

func TestEdgeIssueGrantThenRevokeInStore(t *testing.T) {
	st, local := grantIssueFixture(t)
	g, err := edgeIssueGrant(st, local.Account(), local.Authority().RootKey(),
		edgeGrantSpec{caller: "n_a", callee: "n_b", capability: "sense", expiresAt: time.Now().Add(time.Hour).Unix()}, time.Now())
	require.NoError(t, err)

	found, err := st.Revoke(g.ID)
	require.NoError(t, err)
	require.True(t, found)

	back, ok, err := st.Get(g.ID)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, back.Revoked)
}

func TestEdgeNewGrantIDIsUniqueAndPrefixed(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		id := edgeNewGrantID()
		require.True(t, len(id) > len("grant_"))
		require.Equal(t, "grant_", id[:6])
		require.False(t, seen[id], "grant ids collide")
		seen[id] = true
	}
}
