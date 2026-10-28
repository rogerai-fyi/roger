package edgeauth_test

// A bearer invite on a key that is ALREADY allowed (features/join/invite.feature): the retry after an
// answer lost on the way back, and a device whose key stayed allowed rejoining with a fresh invite.
// Both must come back proven with the invite's code, or the device holds a certificate it cannot
// verify and throws it away.

import (
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v7/internal/edgeauth"
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

// Audit 2026-10-13: an invite bound to a device is bound to that device's KEY, through the id it
// derives. An allowed key that names the victim's (public) node id with its own node key is refused
// before any invite is tried, and the victim's invite is untouched.
func TestABoundInviteCannotBeReachedWithACopiedNodeID(t *testing.T) {
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	userPub, user := keypair(t)
	require.NoError(t, local.Allow(hexEncode(userPub)))
	victim, _ := keypair(t)
	code, err := local.MintBearerCodeFor(edgeauth.NodeID(victim))
	require.NoError(t, err)

	mine, _ := keypair(t)
	req := enrollReq(user, mine, time.Now())
	req.NodeID = edgeauth.NodeID(victim) // the victim's id, this machine's key
	req.Sign(user)
	req.JoinProof = edgeauth.JoinProof(code, local.Descriptor().Fingerprint, req)
	_, err = local.Issue(req)
	require.ErrorIs(t, err, edgeauth.ErrKeyMismatch)

	_, other := keypair(t)
	_, err = local.Issue(proven(t, local, other, victim, code))
	require.NoError(t, err, "the device it is for still joins with it")
}

// A wrong proof from an allowed key costs a guess against an invite bound to its own node too.
func TestAnAllowedKeysMissCountsAgainstAnInviteBoundToItsNode(t *testing.T) {
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	userPub, user := keypair(t)
	require.NoError(t, local.Allow(hexEncode(userPub)))
	node, _ := keypair(t)
	code, err := local.MintBearerCodeFor(edgeauth.NodeID(node))
	require.NoError(t, err)
	for i := 0; i < edgeauth.BearerGuessBudget; i++ {
		_, err := local.Issue(proven(t, local, user, node, edgeauth.NewCode()))
		require.NoError(t, err)
	}
	resp, err := local.Issue(proven(t, local, user, node, code))
	require.NoError(t, err)
	require.Empty(t, resp.RootProof, "the invite was guessed at to its budget, and is void")
}

// The work a retry costs is bounded: only the invite this key spent most recently is checked for
// free (the one whose answer can have been lost). An older invite it spent is not stretched again,
// so a key that spent many cannot make each request cost one stretch per invite it ever spent.
func TestOnlyTheNewestInviteAKeySpentIsCheckedForARetry(t *testing.T) {
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	_, user := keypair(t)
	node, _ := keypair(t)
	older, err := local.MintBearerCode()
	require.NoError(t, err)
	_, err = local.Issue(proven(t, local, user, node, older))
	require.NoError(t, err)
	time.Sleep(1100 * time.Millisecond) // invites are dated to the second
	newer, err := local.MintBearerCode()
	require.NoError(t, err)
	_, err = local.Issue(proven(t, local, user, node, newer))
	require.NoError(t, err)

	resp, err := local.Issue(proven(t, local, user, node, older))
	require.NoError(t, err, "the key is allowed, so it is issued")
	require.Empty(t, resp.RootProof, "an older invite this key spent is not checked again")

	retry := proven(t, local, user, node, newer)
	resp, err = local.Issue(retry)
	require.NoError(t, err)
	requireProof(t, newer, resp, retry.Nonce)
}

// Review 2026-10-27: "newest" is by SPEND, not by mint. A device that joined with invite B and then
// rejoined with the earlier-minted, still-live invite A spent A last; when that answer is lost, its
// retry with A is the one checked for free, and is proven.
func TestTheRetryCheckedIsTheInviteSpentLastNotMintedLast(t *testing.T) {
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	_, user := keypair(t)
	node, _ := keypair(t)
	a, err := local.MintBearerCode()
	require.NoError(t, err)
	time.Sleep(1100 * time.Millisecond)
	b, err := local.MintBearerCode()
	require.NoError(t, err)
	_, err = local.Issue(proven(t, local, user, node, b))
	require.NoError(t, err)
	time.Sleep(1100 * time.Millisecond) // spends are dated to the second
	first := proven(t, local, user, node, a)
	resp, err := local.Issue(first)
	require.NoError(t, err)
	requireProof(t, a, resp, first.Nonce)

	retry := proven(t, local, user, node, a) // the answer above was lost
	resp, err = local.Issue(retry)
	require.NoError(t, err)
	requireProof(t, a, resp, retry.Nonce)
}

// Re-review 2026-10-27: two spends inside one second are still ordered by spend, not by mint.
func TestTwoSpendsInOneSecondAreStillOrderedBySpend(t *testing.T) {
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	at := time.Now().Truncate(time.Second) // requests are dated by the real clock; the authority's is held
	local.SetNow(func() time.Time { return at })
	_, user := keypair(t)
	node, _ := keypair(t)
	a, err := local.MintBearerCode()
	require.NoError(t, err)
	at = at.Add(2 * time.Second)
	b, err := local.MintBearerCode()
	require.NoError(t, err)
	_, err = local.Issue(proven(t, local, user, node, b))
	require.NoError(t, err)
	first := proven(t, local, user, node, a) // the same second as the spend of b
	_, err = local.Issue(first)
	require.NoError(t, err)

	retry := proven(t, local, user, node, a)
	resp, err := local.Issue(retry)
	require.NoError(t, err)
	requireProof(t, a, resp, retry.Nonce)
}

// Adversarial review 2026-10-27: a codes file from before spent_at was recorded (every SpentAt 0)
// has no spend order, so every invite the key spent is checked, and the retry is still proven.
func TestALegacyCodesFileWithNoSpendTimesStillProvesTheRetry(t *testing.T) {
	dir := t.TempDir()
	local, err := edgeauth.Designate(dir, "shed")
	require.NoError(t, err)
	_, user := keypair(t)
	node, _ := keypair(t)
	a, err := local.MintBearerCode()
	require.NoError(t, err)
	b, err := local.MintBearerCode()
	require.NoError(t, err)
	_, err = local.Issue(proven(t, local, user, node, b))
	require.NoError(t, err)
	_, err = local.Issue(proven(t, local, user, node, a)) // a is spent last
	require.NoError(t, err)
	var path string
	_ = filepath.Walk(dir, func(p string, _ os.FileInfo, _ error) error {
		if filepath.Base(p) == edgeauth.CodesFile {
			path = p
		}
		return nil
	})
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var got []map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	for _, c := range got {
		delete(c, "spent_at")
	}
	raw, err = json.Marshal(got)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0o600))

	retry := proven(t, local, user, node, a)
	resp, err := local.Issue(retry)
	require.NoError(t, err)
	requireProof(t, a, resp, retry.Nonce)
}

// Adversarial review 2026-10-27: an invite dated in the future (minted while this machine's clock ran
// ahead) is not live once the clock is corrected; it never outlives its fifteen minutes.
func TestAnInviteDatedInTheFutureIsNotLive(t *testing.T) {
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	real := time.Now()
	local.SetNow(func() time.Time { return real.Add(24 * time.Hour) })
	code, err := local.MintBearerCode()
	require.NoError(t, err)
	local.SetNow(func() time.Time { return real }) // the clock is corrected
	_, err = local.Issue(bearerReq(t, local, code))
	require.ErrorIs(t, err, edgeauth.ErrUnknownMachine, "an invite from the future is not one this Edge holds now")
}
