package edgeauth_test

// features/edge/revocation.feature (approved 2026-10-03): the revocation list a member fetches is
// signed by the Edge's root, and a member uses not one serial until the signature verifies.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/edgeauth"
)

func TestTheRevocationListIsSignedByTheRootAndVerifiedAgainstIt(t *testing.T) {
	local, err := edgeauth.Designate(t.TempDir(), "hub")
	require.NoError(t, err)
	at := time.Unix(1_790_000_000, 0)
	signed, err := local.SignedRevocations(at)
	require.NoError(t, err)
	require.Equal(t, at.Unix(), signed.At)
	require.NotEmpty(t, signed.Sig)

	root, err := edgeauth.DecodeCert(local.RootPEM())
	require.NoError(t, err)
	require.NoError(t, edgeauth.VerifyRevocations(root, signed))

	// A serial added after signing, a different time, or a different root: refused.
	forged := signed
	forged.Revoked = append([]string{"deadbeef"}, signed.Revoked...)
	require.ErrorIs(t, edgeauth.VerifyRevocations(root, forged), edgeauth.ErrBadSignature)
	moved := signed
	moved.At++
	require.ErrorIs(t, edgeauth.VerifyRevocations(root, moved), edgeauth.ErrBadSignature)
	other, err := edgeauth.Designate(t.TempDir(), "other")
	require.NoError(t, err)
	otherRoot, _ := edgeauth.DecodeCert(other.RootPEM())
	require.ErrorIs(t, edgeauth.VerifyRevocations(otherRoot, signed), edgeauth.ErrBadSignature, "signed by a different root")
	empty := signed
	empty.Sig = nil
	require.ErrorIs(t, edgeauth.VerifyRevocations(root, empty), edgeauth.ErrBadSignature)

	// The order serials arrive in is not part of what was signed.
	two := edgeauth.SignedRevocationsList{Revoked: []string{"b", "a"}, At: at.Unix()}
	sig, err := local.SignRevocationList(two.Revoked, at)
	require.NoError(t, err)
	two.Sig = sig.Sig
	two.Revoked = []string{"a", "b"}
	require.NoError(t, edgeauth.VerifyRevocations(root, two))
}
