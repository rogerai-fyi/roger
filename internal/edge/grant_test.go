package edge

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/store"
	"rogerai.fm/roger/v6/internal/towercore/cert"
)

func aGrant() store.Grant {
	return store.Grant{
		ID: "grant_abc", Owner: "acct-1", Caller: "n_laptop",
		Nodes: []string{"n_workstation"}, Caps: []string{"operate"},
		ExpiresAt: time.Now().Add(24 * time.Hour).Unix(), MaxHops: 0,
	}
}

func TestSignAndVerifyGrantUnderTheRoot(t *testing.T) {
	a, err := cert.NewAuthority(cert.Config{TTL: time.Hour})
	require.NoError(t, err)
	signed, err := SignGrant(aGrant(), a.RootKey())
	require.NoError(t, err)
	require.NotEmpty(t, signed.Sig)
	require.NoError(t, VerifyGrant(signed, a.Root()))
}

func TestVerifyGrantRefusesTamperingAndUnsigned(t *testing.T) {
	a, err := cert.NewAuthority(cert.Config{TTL: time.Hour})
	require.NoError(t, err)
	signed, err := SignGrant(aGrant(), a.RootKey())
	require.NoError(t, err)

	require.ErrorIs(t, VerifyGrant(aGrant(), a.Root()), ErrGrantUnsigned)

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
		require.ErrorIs(t, VerifyGrant(g, a.Root()), ErrGrantBadSignature)
	}
}

func TestGrantSignedByOneEdgeDoesNotVerifyUnderAnother(t *testing.T) {
	a, err := cert.NewAuthority(cert.Config{TTL: time.Hour})
	require.NoError(t, err)
	b, err := cert.NewAuthority(cert.Config{TTL: time.Hour})
	require.NoError(t, err)
	signed, err := SignGrant(aGrant(), a.RootKey())
	require.NoError(t, err)
	require.ErrorIs(t, VerifyGrant(signed, b.Root()), ErrGrantBadSignature)
}
