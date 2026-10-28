package main

// The authority listener checks members against a verify-only trust built from the root certificate
// and the revocation list as they are now (edgeMemberTrust), the same for presence and for a phone's
// turns; a revocation made beside it is honoured at the next request.

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/edgeauth"
)

func TestTheAuthorityListenerTrustsTheRootCertAndTheRevocationsAsTheyAreNow(t *testing.T) {
	dir := t.TempDir()
	local, err := edgeauth.Designate(dir, "shed")
	require.NoError(t, err)
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	id := edgeauth.NodeID(pub)
	leaf, err := local.Authority().Issue(id, pub)
	require.NoError(t, err)

	v := edgeMemberTrust(dir, local)
	require.NotNil(t, v)
	require.Nil(t, v.RootKey(), "verify-only: the root key is not loaded")
	got, err := v.Authenticate(leaf)
	require.NoError(t, err)
	require.Equal(t, id, got)

	// A forget in another process writes revoked.json; the next request sees it.
	other, ok, err := edgeauth.OpenLocal(dir)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, other.Authority().Revoke(leaf.SerialNumber))
	_, err = edgeMemberTrust(dir, local).Authenticate(leaf)
	require.Error(t, err)

	require.Nil(t, edgeMemberTrust(t.TempDir(), local), "no root certificate: fail closed")
}
