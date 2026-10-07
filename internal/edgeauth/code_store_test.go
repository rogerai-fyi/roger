package edgeauth_test

// The authority half of features/edge/enrollment_root_trust.feature (sections 2, 6, 7 and 8):
// codes are minted for one join, kept 0600, attached as a root proof to the ONE answer that issues
// for that join, spent in the same locked step, and never outlive 15 minutes or a restart's prune.
// A real local authority on disk; real signed requests.

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edgeauth"
)

func rootFP(t *testing.T, resp edgeauth.Response) string {
	t.Helper()
	root, err := edgeauth.DecodeCert(resp.Root)
	require.NoError(t, err)
	return edgeauth.RootFingerprint(root)
}

func requireProof(t *testing.T, code string, resp edgeauth.Response, nonce string) {
	t.Helper()
	require.NotEmpty(t, resp.RootProof, "the answer carries no root proof")
	require.NoError(t, edgeauth.VerifyRootProof(code, resp.RootProof, rootFP(t, resp), resp.Account, resp.NodeID, nonce))
}

// allowedWithCode is a user key the owner allowed, with the code that allow minted.
func allowedWithCode(t *testing.T, local *edgeauth.Local) (ed25519.PrivateKey, string) {
	t.Helper()
	userPub, userPriv := keypair(t)
	require.NoError(t, local.Allow(hexEncode(userPub)))
	code, err := local.MintKeyCode(hexEncode(userPub))
	require.NoError(t, err)
	return userPriv, code
}

func TestAllowMintsACodeAndTheIssuingAnswerProvesTheRootWithIt(t *testing.T) {
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	user, code := allowedWithCode(t, local)
	_, err = edgeauth.CanonicalCode(code)
	require.NoError(t, err, "a minted code is a canonical code")

	nodePub, _ := keypair(t)
	req := enrollReq(user, nodePub, time.Now())
	resp, err := local.Issue(req)
	require.NoError(t, err)
	requireProof(t, code, resp, req.Nonce)
	require.Equal(t, rootFP(t, resp), local.Descriptor().Fingerprint)
}

func TestACodeIsSpentWhenItsCertificateIsIssued(t *testing.T) {
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	user, code := allowedWithCode(t, local)
	nodePub, _ := keypair(t)
	first := enrollReq(user, nodePub, time.Now())
	resp, err := local.Issue(first)
	require.NoError(t, err)
	requireProof(t, code, resp, first.Nonce)

	again := enrollReq(user, nodePub, time.Now())
	resp2, err := local.Issue(again)
	require.NoError(t, err, "the allowed key may still enroll (renew)")
	require.Empty(t, resp2.RootProof, "a spent code vouches for nothing again")
	require.Equal(t, edgeauth.ProofNone, resp2.ProofStatus, "and the answer says it held no live code")
	require.Empty(t, resp.ProofStatus)
}

func TestMintingAgainVoidsThePreviousCode(t *testing.T) {
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	userPub, user := keypair(t)
	require.NoError(t, local.Allow(hexEncode(userPub)))
	a, err := local.MintKeyCode(hexEncode(userPub))
	require.NoError(t, err)
	b, err := local.MintKeyCode(hexEncode(userPub))
	require.NoError(t, err)
	require.NotEqual(t, a, b)

	nodePub, _ := keypair(t)
	req := enrollReq(user, nodePub, time.Now())
	resp, err := local.Issue(req)
	require.NoError(t, err)
	requireProof(t, b, resp, req.Nonce)
	require.ErrorIs(t, edgeauth.VerifyRootProof(a, resp.RootProof, rootFP(t, resp), resp.Account, resp.NodeID, req.Nonce),
		edgeauth.ErrRootProofMismatch)
}

func TestACodeIsBoundToTheJoinItWasMintedFor(t *testing.T) {
	local, nodePub, nodePriv, nodeID := claimFixture(t)
	userB, codeB := allowedWithCode(t, local)
	require.NoError(t, local.GrantClaim(nodeID, "pocket"))
	codeP, err := local.MintNodeCode(nodeID)
	require.NoError(t, err)

	c := signedClaim(t, nodePub, nodePriv, time.Now())
	resp, err := local.Claim(c, time.Now())
	require.NoError(t, err)
	requireProof(t, codeP, resp, c.Nonce)
	require.Error(t, edgeauth.VerifyRootProof(codeB, resp.RootProof, rootFP(t, resp), resp.Account, resp.NodeID, c.Nonce))

	// bench's code is still live and unspent.
	benchNode, _ := keypair(t)
	req := enrollReq(userB, benchNode, time.Now())
	resp2, err := local.Issue(req)
	require.NoError(t, err)
	requireProof(t, codeB, resp2, req.Nonce)
}

func TestAClaimsCodeIsSpentWithTheGrant(t *testing.T) {
	local, nodePub, nodePriv, nodeID := claimFixture(t)
	require.NoError(t, local.GrantClaim(nodeID, "pocket"))
	code, err := local.MintNodeCode(nodeID)
	require.NoError(t, err)
	got, _, ok := local.NodeCode(nodeID)
	require.True(t, ok)
	require.Equal(t, code, got, "the adopting panel can show the live code")

	c := signedClaim(t, nodePub, nodePriv, time.Now())
	resp, err := local.Claim(c, time.Now())
	require.NoError(t, err)
	requireProof(t, code, resp, c.Nonce)
	_, _, ok = local.NodeCode(nodeID)
	require.False(t, ok, "the code is spent with the grant")
	_, err = local.Claim(signedClaim(t, nodePub, nodePriv, time.Now()), time.Now())
	require.ErrorIs(t, err, edgeauth.ErrNotAdopted)
}

func TestACodeExpiresAfterFifteenMinutesAndIsHonouredJustInside(t *testing.T) {
	for _, tc := range []struct {
		age   time.Duration
		proof bool
	}{{14 * time.Minute, true}, {16 * time.Minute, false}} {
		t.Run(tc.age.String(), func(t *testing.T) {
			dir := t.TempDir()
			local, err := edgeauth.Designate(dir, "shed")
			require.NoError(t, err)
			minted := time.Now()
			local.SetNow(func() time.Time { return minted })
			user, code := allowedWithCode(t, local)
			later := minted.Add(tc.age)
			local.SetNow(func() time.Time { return later })
			nodePub, _ := keypair(t)
			req := enrollReq(user, nodePub, later)
			resp, err := local.Issue(req)
			require.NoError(t, err)
			if tc.proof {
				requireProof(t, code, resp, req.Nonce)
				return
			}
			require.Empty(t, resp.RootProof, "an expired code vouches for nothing")
			raw, _ := os.ReadFile(filepath.Join(authDir(dir), edgeauth.CodesFile))
			require.NotContains(t, string(raw), code, "an expired code is pruned")
		})
	}
}

func TestALiveCodeSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	local, err := edgeauth.Designate(dir, "shed")
	require.NoError(t, err)
	user, code := allowedWithCode(t, local)
	again, ok, err := edgeauth.OpenLocal(dir)
	require.NoError(t, err)
	require.True(t, ok)
	nodePub, _ := keypair(t)
	req := enrollReq(user, nodePub, time.Now())
	resp, err := again.Issue(req)
	require.NoError(t, err)
	requireProof(t, code, resp, req.Nonce)
}

func TestCodesAreKeptOwnerOnlyAndNowhereElse(t *testing.T) {
	dir := t.TempDir()
	local, err := edgeauth.Designate(dir, "shed")
	require.NoError(t, err)
	_, code := allowedWithCode(t, local)
	fi, err := os.Stat(filepath.Join(authDir(dir), edgeauth.CodesFile))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	entries, err := os.ReadDir(authDir(dir))
	require.NoError(t, err)
	for _, e := range entries {
		if e.IsDir() || e.Name() == edgeauth.CodesFile {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(authDir(dir), e.Name()))
		require.NotContains(t, string(b), code, "the code leaked into %s", e.Name())
	}
}

func TestARefusalNeverCarriesAProofAndIsTheSameWithOrWithoutALiveCode(t *testing.T) {
	refusal := func(withCode bool) string {
		local, err := edgeauth.Designate(t.TempDir(), "shed")
		require.NoError(t, err)
		if withCode {
			allowedWithCode(t, local)
		}
		_, stranger := keypair(t)
		nodePub, _ := keypair(t)
		resp, err := local.Issue(enrollReq(stranger, nodePub, time.Now()))
		require.Error(t, err)
		require.Empty(t, resp.RootProof)
		return err.Error()
	}
	require.Equal(t, refusal(false), refusal(true))
}

func TestTwoRacingEnrollmentsGetAtMostOneProof(t *testing.T) {
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	user, code := allowedWithCode(t, local)
	var wg sync.WaitGroup
	var mu sync.Mutex
	proofs := 0
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			nodePub, _ := keypair(t)
			req := enrollReq(user, nodePub, time.Now())
			resp, err := local.Issue(req)
			if err == nil && resp.RootProof != "" {
				require.NoError(t, edgeauth.VerifyRootProof(code, resp.RootProof, rootFP(t, resp), resp.Account, resp.NodeID, req.Nonce))
				mu.Lock()
				proofs++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	require.Equal(t, 1, proofs)
}

func TestForgettingANodeVoidsItsCode(t *testing.T) {
	local, _, _, nodeID := claimFixture(t)
	require.NoError(t, local.GrantClaim(nodeID, "pocket"))
	_, err := local.MintNodeCode(nodeID)
	require.NoError(t, err)
	_, _, err = local.Forget(nodeID)
	require.NoError(t, err)
	_, _, ok := local.NodeCode(nodeID)
	require.False(t, ok)
}

func TestMintingRefusesABlankJoin(t *testing.T) {
	local, err := edgeauth.Designate(t.TempDir(), "shed")
	require.NoError(t, err)
	_, err = local.MintKeyCode("  ")
	require.Error(t, err)
	_, err = local.MintNodeCode("")
	require.Error(t, err)
}

func TestADamagedCodeStoreFailsClosed(t *testing.T) {
	dir := t.TempDir()
	local, err := edgeauth.Designate(dir, "shed")
	require.NoError(t, err)
	user, _ := allowedWithCode(t, local)
	corrupt(t, dir, edgeauth.CodesFile)
	nodePub, _ := keypair(t)
	_, err = local.Issue(enrollReq(user, nodePub, time.Now()))
	require.Error(t, err, "an unreadable code store issues nothing")
	require.True(t, strings.Contains(err.Error(), "enrollment code"), err.Error())
	_, err = local.MintKeyCode(hexEncode(user.Public().(ed25519.PublicKey)))
	require.Error(t, err)
	_, _, ok := local.NodeCode("n_x")
	require.False(t, ok)
}

// VerifyAnswer is the one rule every joiner applies to an authority's answer
// (enrollment_root_trust.feature): a held root decides on its own; otherwise a pin or the code's
// proof must vouch for the root, and with neither nothing may be kept.
func TestVerifyAnswerIsTheJoinersWholeRule(t *testing.T) {
	local, nodePub, nodePriv, nodeID := claimFixture(t)
	require.NoError(t, local.GrantClaim(nodeID, "pocket"))
	code, err := local.MintNodeCode(nodeID)
	require.NoError(t, err)
	c := signedClaim(t, nodePub, nodePriv, time.Now())
	resp, err := local.Claim(c, time.Now())
	require.NoError(t, err)
	want := edgeauth.Expect{NodeID: nodeID, Key: nodePub, Now: time.Now()}
	fp := rootFP(t, resp)
	other, err := edgeauth.Designate(t.TempDir(), "other")
	require.NoError(t, err)

	_, err = edgeauth.VerifyAnswer(resp, want, c.Nonce, "", "")
	require.ErrorIs(t, err, edgeauth.ErrRootUnproven, "no code and no pin keeps nothing")

	id, err := edgeauth.VerifyAnswer(resp, want, c.Nonce, code, "")
	require.NoError(t, err)
	require.Equal(t, fp, edgeauth.RootFingerprint(id.Root))

	_, err = edgeauth.VerifyAnswer(resp, want, c.Nonce, "ZZZZZZZZZZ", "")
	require.ErrorIs(t, err, edgeauth.ErrRootProofMismatch)

	_, err = edgeauth.VerifyAnswer(resp, want, c.Nonce, "", fp)
	require.NoError(t, err, "the full pin alone vouches")
	_, err = edgeauth.VerifyAnswer(resp, want, c.Nonce, "", other.Descriptor().Fingerprint)
	require.ErrorIs(t, err, edgeauth.ErrPinMismatch)
	_, err = edgeauth.VerifyAnswer(resp, want, c.Nonce, code, other.Descriptor().Fingerprint)
	require.ErrorIs(t, err, edgeauth.ErrPinMismatch, "a pin and a code must both agree")

	held := want
	held.Root = other.Authority().Root()
	_, err = edgeauth.VerifyAnswer(resp, held, c.Nonce, "", "")
	require.ErrorIs(t, err, edgeauth.ErrDifferentAuthority, "a held root is never replaced by an answer")
	held.Root, _ = edgeauth.DecodeCert(resp.Root)
	_, err = edgeauth.VerifyAnswer(resp, held, c.Nonce, "", "")
	require.NoError(t, err, "the root already held vouches on its own")
}

// "an expired code is pruned and a restart does not revive it": reopening the authority rewrites
// the store without the codes that have run out.
func TestARestartPrunesExpiredCodesFromTheStore(t *testing.T) {
	dir := t.TempDir()
	local, err := edgeauth.Designate(dir, "shed")
	require.NoError(t, err)
	minted := time.Now().Add(-16 * time.Minute)
	local.SetNow(func() time.Time { return minted })
	_, old := allowedWithCode(t, local)
	raw, err := os.ReadFile(filepath.Join(authDir(dir), edgeauth.CodesFile))
	require.NoError(t, err)
	require.Contains(t, string(raw), old, "before the restart the expired code is still on disk")
	_, _, err = edgeauth.OpenLocal(dir)
	require.NoError(t, err)
	raw, err = os.ReadFile(filepath.Join(authDir(dir), edgeauth.CodesFile))
	require.NoError(t, err)
	require.NotContains(t, string(raw), old, "the restart pruned it")
}
