package edgeauth_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/store"
)

func aGrant() store.Grant {
	return store.Grant{
		ID: "grant_abc", Owner: edgeauth.LocalAccount, Caller: "n_laptop",
		Nodes: []string{"n_workstation"}, Caps: []string{"operate"},
		ExpiresAt: time.Now().Add(24 * time.Hour).Unix(), MaxHops: 0,
	}
}

func TestSignAndVerifyGrantUnderTheRoot(t *testing.T) {
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)

	signed, err := edgeauth.SignGrant(aGrant(), local.Authority().RootKey())
	require.NoError(t, err)
	require.NotEmpty(t, signed.Sig, "a signed grant carries a signature")

	// It verifies under THIS Edge's root.
	require.NoError(t, edgeauth.VerifyGrant(signed, local.Authority().Root()))
}

func TestVerifyGrantRefusesTamperingAndUnsigned(t *testing.T) {
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	signed, err := edgeauth.SignGrant(aGrant(), local.Authority().RootKey())
	require.NoError(t, err)

	// Unsigned.
	require.ErrorIs(t, edgeauth.VerifyGrant(aGrant(), local.Authority().Root()), edgeauth.ErrGrantUnsigned)

	// Tamper each authorizing field -> bad signature.
	for _, tamper := range []func(*store.Grant){
		func(g *store.Grant) { g.Caller = "n_someone_else" },
		func(g *store.Grant) { g.Nodes = []string{"n_othernode"} },
		func(g *store.Grant) { g.Caps = []string{"actuate"} },
		func(g *store.Grant) { g.ExpiresAt += 3600 },
		func(g *store.Grant) { g.MaxHops = 5 },
		func(g *store.Grant) { g.Free = !g.Free },
		func(g *store.Grant) { g.Owner = "someone-else" },
	} {
		g := signed
		tamper(&g)
		require.ErrorIs(t, edgeauth.VerifyGrant(g, local.Authority().Root()), edgeauth.ErrGrantBadSignature)
	}
}

func TestGrantSignedByOneEdgeDoesNotVerifyUnderAnother(t *testing.T) {
	a, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	b, err := edgeauth.Designate(t.TempDir(), "annex")
	require.NoError(t, err)
	signed, err := edgeauth.SignGrant(aGrant(), a.Authority().RootKey())
	require.NoError(t, err)
	require.ErrorIs(t, edgeauth.VerifyGrant(signed, b.Authority().Root()), edgeauth.ErrGrantBadSignature)
}
