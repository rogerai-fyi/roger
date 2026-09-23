package main

// The phone joins by CLAIM, so adopting a candidate that advertises NO certificate (a phone) must
// GRANT a claim, not dial-verify it (the phone does not serve). Regression for the 2026-09-23 fix.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edgeauth"
)

func TestAdoptByClaimGrantsWithoutDial(t *testing.T) {
	useTempConfig(t)

	// With no authority here, a claim-adopt is refused with a helpful reason (not a dial error).
	err := edgeAdoptByClaim("n_phone000000000000000000000000000000000000000")
	require.Error(t, err)
	require.Contains(t, err.Error(), "roots this Edge")

	// Designate this machine as a local authority, then claim-adopt grants without any dial.
	_, err = edgeauth.Designate(edgeAuthDir(), "hub")
	require.NoError(t, err)
	require.NoError(t, edgeAdoptByClaim("n_phone000000000000000000000000000000000000000"))

	local, ok, err := edgeauth.OpenLocal(edgeAuthDir())
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, local.ClaimGranted("n_phone000000000000000000000000000000000000000"),
		"adopting a no-certificate candidate must grant it a claim")
}
