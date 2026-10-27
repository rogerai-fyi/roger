package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/edgeauth"
	"rogerai.fm/roger/v7/internal/store"
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

func TestEdgeTrustedRevocationIgnoresTheWireValue(t *testing.T) {
	st := &edge.GrantStore{Path: filepath.Join(t.TempDir(), "edge", "grants.json")}
	// The authority issued g1 and later REVOKED it in its own store.
	require.NoError(t, st.Put(store.Grant{ID: "g1"}))
	_, err := st.Revoke("g1")
	require.NoError(t, err)

	// A caller sends the SAME grant with Revoked:false (forged - Revoked is unsigned). The callee must
	// apply the store's standing, not the wire's.
	wire := store.Grant{ID: "g1", Revoked: false}
	got, terr := edgeTrustedRevocation(st, wire)
	require.NoError(t, terr)
	require.True(t, got.Revoked, "the callee trusts its own store, never the wire Revoked")
}

func TestEdgeTrustedRevocationClearsAForgedRevokedTrueToo(t *testing.T) {
	st := &edge.GrantStore{Path: filepath.Join(t.TempDir(), "edge", "grants.json")}
	require.NoError(t, st.Put(store.Grant{ID: "g_live"})) // issued, not revoked
	// A wire grant claiming Revoked:true for a live grant is also not trusted - the store says live.
	got, terr := edgeTrustedRevocation(st, store.Grant{ID: "g_live", Revoked: true})
	require.NoError(t, terr)
	require.False(t, got.Revoked, "the wire value is ignored in both directions; the store is the truth")
}

func TestEdgeTrustedRevocationUnknownGrantHasNoLocalStanding(t *testing.T) {
	st := &edge.GrantStore{Path: filepath.Join(t.TempDir(), "edge", "grants.json")}
	// A grant this node did not issue: no local standing, so the wire Revoked is cleared to false and
	// the grant stands on its signed fields (expiry, node scope) plus node-level cert revocation.
	got, terr := edgeTrustedRevocation(st, store.Grant{ID: "g_foreign", Revoked: true})
	require.NoError(t, terr)
	require.False(t, got.Revoked)
}

func TestEdgeIssueGrantRefusesAnAlreadyExpiredGrant(t *testing.T) {
	st, local := grantIssueFixture(t)
	now := time.Now()
	_, err := edgeIssueGrant(st, local.Account(), local.Authority().RootKey(),
		edgeGrantSpec{caller: "n_a", callee: "n_b", capability: "operate", expiresAt: now.Add(-time.Hour).Unix()}, now)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already be expired")

	got, _ := st.List()
	require.Empty(t, got, "an already-expired grant is never stored")
}
