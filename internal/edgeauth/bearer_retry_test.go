package edgeauth_test

// A bearer invite on a key that is ALREADY allowed (features/join/invite.feature): the retry after an
// answer lost on the way back, and a device whose key stayed allowed rejoining with a fresh invite.
// Both must come back proven with the invite's code, or the device holds a certificate it cannot
// verify and throws it away.

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edgeauth"
)

func proven(t *testing.T, local *edgeauth.Local, user ed25519.PrivateKey, node ed25519.PublicKey, code string) edgeauth.Request {
	t.Helper()
	req := enrollReq(user, node, time.Now())
	req.JoinProof = edgeauth.JoinProof(code, local.Descriptor().Fingerprint, req)
	return req
}

// The first answer is lost: the code is spent and the key allowed. The retry, a new request with the
// same invite, is proven with that same code, so the device can keep what it is given.
func TestABearerRetryAfterALostAnswerIsProven(t *testing.T) {
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	code, err := local.MintBearerCode()
	require.NoError(t, err)
	_, user := keypair(t)
	node, _ := keypair(t)

	first := proven(t, local, user, node, code)
	_, err = local.Issue(first) // answered, and the answer never arrives
	require.NoError(t, err)

	retry := proven(t, local, user, node, code)
	resp, err := local.Issue(retry)
	require.NoError(t, err)
	require.Empty(t, resp.ProofStatus, "not \"none\": the spent invite still proves the retry of the key that spent it")
	requireProof(t, code, resp, retry.Nonce)

	// It still admits nobody else.
	_, err = local.Issue(bearerReq(t, local, code))
	require.ErrorIs(t, err, edgeauth.ErrUnknownMachine)
}

// A device that left and rejoins with a FRESH invite: its key is still allowed, and the answer is
// proven with the fresh code, which is spent.
func TestAFreshInviteOnAnAllowedKeyProvesAndSpends(t *testing.T) {
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	userPub, user := keypair(t)
	require.NoError(t, local.Allow(hexEncode(userPub)))
	code, err := local.MintBearerCode()
	require.NoError(t, err)
	node, _ := keypair(t)

	req := proven(t, local, user, node, code)
	resp, err := local.Issue(req)
	require.NoError(t, err)
	requireProof(t, code, resp, req.Nonce)

	_, err = local.Issue(bearerReq(t, local, code))
	require.ErrorIs(t, err, edgeauth.ErrUnknownMachine, "the fresh invite was spent by this join")
}

// Audit 2026-10-13: an allowed key gets no free guesses. A proof from it that matches nothing counts
// one guess against every live unbound invite, as a stranger's does, and the invite is void at the
// budget. The key itself is still issued as before.
func TestAnAllowedKeysWrongProofIsAGuess(t *testing.T) {
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	userPub, user := keypair(t)
	require.NoError(t, local.Allow(hexEncode(userPub)))
	code, err := local.MintBearerCode()
	require.NoError(t, err)
	for i := 0; i < edgeauth.BearerGuessBudget; i++ {
		node, _ := keypair(t)
		_, err := local.Issue(proven(t, local, user, node, edgeauth.NewCode()))
		require.NoError(t, err, "an allowed key is issued whatever its proof says")
	}
	_, err = local.Issue(bearerReq(t, local, code))
	require.ErrorIs(t, err, edgeauth.ErrUnknownMachine, "the invite was guessed at to its budget, and is void")
}

// An invite another key spent is not this key's to retry with: the answer is unproven, and the
// invite is untouched for its own retry.
func TestAnInviteSpentByAnotherKeyIsNotTried(t *testing.T) {
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	code, err := local.MintBearerCode()
	require.NoError(t, err)
	_, first := keypair(t)
	node, _ := keypair(t)
	_, err = local.Issue(proven(t, local, first, node, code))
	require.NoError(t, err)

	otherPub, other := keypair(t)
	require.NoError(t, local.Allow(hexEncode(otherPub)))
	resp, err := local.Issue(proven(t, local, other, node, code))
	require.NoError(t, err)
	require.Empty(t, resp.RootProof, "not proven with an invite another key spent")

	retry := proven(t, local, first, node, code)
	resp, err = local.Issue(retry)
	require.NoError(t, err)
	requireProof(t, code, resp, retry.Nonce)
}

// A bound invite on an allowed key from another node is passed over, not spent.
func TestABoundInviteIsNotSpentByAnotherAllowedNode(t *testing.T) {
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	userPub, user := keypair(t)
	require.NoError(t, local.Allow(hexEncode(userPub)))
	forPub, _ := keypair(t)
	code, err := local.MintBearerCodeFor(edgeauth.NodeID(forPub))
	require.NoError(t, err)
	other, _ := keypair(t)
	resp, err := local.Issue(proven(t, local, user, other, code))
	require.NoError(t, err)
	require.Empty(t, resp.RootProof, "not proven with an invite meant for another device")

	_, user2 := keypair(t)
	req := proven(t, local, user2, forPub, code)
	_, err = local.Issue(req)
	require.NoError(t, err, "the device it is for still joins with it")
}
