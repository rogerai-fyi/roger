package edgeauth_test

// The local authority under attack and under damage. Two families of case:
//
//   - ADVERSARIAL requests reaching Local.Issue / Local.Claim directly (forged, stale, replayed,
//     key-swapped, revoked-then-returning), each asserting the exact refusal.
//   - DAMAGED state on disk (a corrupt or unreadable claims / issued / history / revoked /
//     allow / protected / enrolled-by file, an unwritable directory). Every one must FAIL CLOSED:
//     an error and no certificate, never a silent "nothing here" that issues or forgets wrongly.

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edgeauth"
)

func authDir(dir string) string { return filepath.Join(dir, edgeauth.AuthorityDir) }

// corrupt replaces one authority state file with bytes that are not JSON.
func corrupt(t *testing.T, dir, name string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(authDir(dir), name), []byte("{bad"), 0o600))
}

// unreadable replaces one authority state file with a DIRECTORY, so reading it fails with an I/O
// error rather than a parse error (the other way a file can be unusable).
func unreadable(t *testing.T, dir, name string) {
	t.Helper()
	p := filepath.Join(authDir(dir), name)
	_ = os.Remove(p)
	require.NoError(t, os.Mkdir(p, 0o700))
}

// readOnly makes the authority directory unwritable for the rest of the test (and writable again
// at cleanup, so t.TempDir can remove it).
func readOnly(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.Chmod(authDir(dir), 0o500))
	t.Cleanup(func() { _ = os.Chmod(authDir(dir), 0o700) })
}

// enrollee is an allowed user key on the authority plus a fresh node key.
func enrollee(t *testing.T, local *edgeauth.Local) (ed25519.PrivateKey, ed25519.PublicKey) {
	t.Helper()
	userPub, userPriv := keypair(t)
	require.NoError(t, local.Allow(hexEncode(userPub)))
	nodePub, _ := keypair(t)
	return userPriv, nodePub
}

func enrollReq(user ed25519.PrivateKey, node ed25519.PublicKey, now time.Time) edgeauth.Request {
	r := edgeauth.NewRequest(edgeauth.LocalAccount, "workshop", "host", node, now)
	r.Sign(user)
	return r
}

// --- adversarial requests at the local authority ---------------------------

func TestLocalIssueRefusesForgedAndStaleRequestsBeforeAnyStateWork(t *testing.T) {
	dir, local, _, _, _ := secFixture(t)
	user, node := enrollee(t, local)
	// Damage the revocation list: a refusal that happens BEFORE disk work still answers with its own
	// reason, proving the signature and freshness gates run first.
	corrupt(t, dir, "revoked.json")

	cases := []struct {
		name string
		req  func() edgeauth.Request
		want error
	}{
		{"altered after signing", func() edgeauth.Request {
			r := enrollReq(user, node, time.Now())
			r.Name = "renamed"
			return r
		}, edgeauth.ErrBadSignature},
		{"no signature", func() edgeauth.Request {
			r := enrollReq(user, node, time.Now())
			r.Sig = ""
			return r
		}, edgeauth.ErrBadSignature},
		{"a signing key that is not hex", func() edgeauth.Request {
			r := enrollReq(user, node, time.Now())
			r.UserKey = "zz"
			return r
		}, edgeauth.ErrBadSignature},
		{"signed an hour ago", func() edgeauth.Request { return enrollReq(user, node, time.Now().Add(-time.Hour)) },
			edgeauth.ErrStaleRequest},
		{"signed an hour ahead", func() edgeauth.Request { return enrollReq(user, node, time.Now().Add(time.Hour)) },
			edgeauth.ErrStaleRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := local.Issue(tc.req())
			require.ErrorIs(t, err, tc.want)
			require.Equal(t, edgeauth.Response{}, resp)
		})
	}
}

func TestLocalIssueRefusesAKeySwapAndAnUnusableNodeKey(t *testing.T) {
	_, local, _, _, _ := secFixture(t)
	user, node := enrollee(t, local)
	other, _ := keypair(t)

	// Asks for another node's id while presenting its own key: signed, allowed, still refused.
	swap := edgeauth.NewRequest(edgeauth.LocalAccount, "workshop", "host", node, time.Now())
	swap.NodeID = edgeauth.NodeID(other)
	swap.Sign(user)
	_, err := local.Issue(swap)
	require.ErrorIs(t, err, edgeauth.ErrKeyMismatch)
	require.Len(t, local.Issuer().Requests(), 1, "the refused swap is in the owner's request log")

	// A node key that is not a key at all.
	bad := edgeauth.NewRequest(edgeauth.LocalAccount, "workshop", "host", node, time.Now())
	bad.NodeKey = "abcd"
	bad.Sign(user)
	_, err = local.Issue(bad)
	require.ErrorIs(t, err, edgeauth.ErrMalformed)
	// Every refused attempt is in the owner's request log, recorded as received, like the swap.
	log := local.Issuer().Requests()
	require.Len(t, log, 2, "the refused bad-node-key attempt is logged too")
	require.Equal(t, swap, log[0])
	require.Equal(t, bad, log[1])

	ok, err := local.HasIssued(edgeauth.NodeID(other))
	require.NoError(t, err)
	require.False(t, ok)
}

func TestLocalIssueRefusesAReplayedRequest(t *testing.T) {
	_, local, _, _, _ := secFixture(t)
	user, node := enrollee(t, local)
	r := enrollReq(user, node, time.Now())
	_, err := local.Issue(r)
	require.NoError(t, err)
	resp, err := local.Issue(r)
	require.ErrorIs(t, err, edgeauth.ErrReplayed)
	require.Equal(t, edgeauth.Response{}, resp)
}

// A plain Revoke (not a Forget) leaves the enrolling key ALLOWED. The node must still be unable to
// enroll a fresh certificate under its old node id: the revocation TOMBSTONE has to hold on its own,
// and its refusal must look like any other "no" so it is not a revocation oracle.
func TestARevokedNodeCannotReEnrollEvenWithItsKeyStillAllowed(t *testing.T) {
	_, local, _, _, _ := secFixture(t)
	user, node := enrollee(t, local)
	_, err := local.Issue(enrollReq(user, node, time.Now()))
	require.NoError(t, err)

	revoked, err := local.Revoke(edgeauth.NodeID(node))
	require.NoError(t, err)
	require.Len(t, revoked, 1)

	resp, err := local.Issue(enrollReq(user, node, time.Now()))
	require.ErrorIs(t, err, edgeauth.ErrUnknownMachine)
	require.Equal(t, edgeauth.Response{}, resp)

	// The same allowed key, a NEW node: a different machine, not the revoked one, so it may join.
	fresh, _ := keypair(t)
	_, err = local.Issue(enrollReq(user, fresh, time.Now()))
	require.NoError(t, err)
}

// Renewal: the same node enrolling again under the same key mints a new certificate, and the
// enrolled-by record is unchanged (it is not rewritten for nothing).
func TestTheSameNodeRenewingKeepsOneEnrolledByRecord(t *testing.T) {
	dir, local, _, _, _ := secFixture(t)
	user, node := enrollee(t, local)
	first, err := local.Issue(enrollReq(user, node, time.Now()))
	require.NoError(t, err)
	second, err := local.Issue(enrollReq(user, node, time.Now()))
	require.NoError(t, err)
	require.NotEqual(t, first.Cert, second.Cert)

	b, err := os.ReadFile(filepath.Join(authDir(dir), edgeauth.EnrolledByFile))
	require.NoError(t, err)
	var m map[string]string
	require.NoError(t, json.Unmarshal(b, &m))
	require.Equal(t, map[string]string{edgeauth.NodeID(node): hexEncode(user.Public().(ed25519.PublicKey))}, m)

	// A forget then revokes BOTH certificates the node holds.
	revoked, _, err := local.Forget(edgeauth.NodeID(node))
	require.NoError(t, err)
	require.Len(t, revoked, 2)
}

// --- damaged state: enrollment fails closed --------------------------------

func TestLocalIssueFailsClosedOnEveryDamagedStateFile(t *testing.T) {
	for _, tc := range []struct {
		file string
		want string
		dirt func(*testing.T, string, string)
	}{
		{"revoked.json", "could not read the revocation list", corrupt},
		{edgeauth.IssuedHistoryFile, "could not read the issued-certificate state", corrupt},
		{edgeauth.IssuedFile, "could not read the issued-certificate state", corrupt},
		{edgeauth.EnrolledByFile, "has NOT been issued", corrupt},
		{edgeauth.EnrolledByFile, "has NOT been issued", unreadable},
	} {
		t.Run(tc.file, func(t *testing.T) {
			dir, local, _, _, _ := secFixture(t)
			user, node := enrollee(t, local)
			tc.dirt(t, dir, tc.file)
			resp, err := local.Issue(enrollReq(user, node, time.Now()))
			require.ErrorContains(t, err, tc.want)
			require.Equal(t, edgeauth.Response{}, resp)
		})
	}
}

// An unreadable allow-list is NOBODY allowed, never everybody.
func TestAnUnreadableAllowListAdmitsNobody(t *testing.T) {
	dir, local, _, _, _ := secFixture(t)
	user, node := enrollee(t, local)
	corrupt(t, dir, edgeauth.AllowFile)
	_, err := local.Issue(enrollReq(user, node, time.Now()))
	require.ErrorIs(t, err, edgeauth.ErrUnknownMachine)
	other, _ := keypair(t)
	require.Error(t, local.Allow(hexEncode(other)), "an allow over an unreadable list is refused, not a fresh list")
}

// A certificate that cannot be RECORDED is not issued: if it were handed out unrecorded, a later
// forget could never revoke it.
func TestAnUnrecordableEnrollmentIsNotIssued(t *testing.T) {
	dir, local, _, _, _ := secFixture(t)
	user, node := enrollee(t, local)
	readOnly(t, dir)
	resp, err := local.Issue(enrollReq(user, node, time.Now()))
	require.ErrorContains(t, err, "has NOT been issued")
	require.Equal(t, edgeauth.Response{}, resp)
}

func TestTheAuthorityLockFileBeingUnusableStopsEveryMutation(t *testing.T) {
	dir, local, pub, priv, id := secFixture(t)
	unreadable(t, dir, ".authlock")
	user, _ := keypair(t)
	require.Error(t, local.Allow(hexEncode(user)))
	require.Error(t, local.GrantClaim(id, "pixel"))
	_, err := local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
	require.Error(t, err)
	_, err = local.Revoke(id)
	require.Error(t, err)
	_, _, err = local.Forget(id)
	require.Error(t, err)
}

// --- damaged state: claims fail closed and never spend the adopt ----------

func TestClaimFailsClosedOnDamagedStateAndKeepsTheGrant(t *testing.T) {
	for _, tc := range []struct {
		file string
		want string
	}{
		{"revoked.json", "could not read the revocation list"},
		{edgeauth.IssuedHistoryFile, "could not read the issued-certificate state"},
	} {
		t.Run(tc.file, func(t *testing.T) {
			dir, local, pub, priv, id := secFixture(t)
			require.NoError(t, local.GrantClaim(id, "pixel"))
			corrupt(t, dir, tc.file)
			resp, err := local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
			require.ErrorContains(t, err, tc.want)
			require.Equal(t, edgeauth.Response{}, resp)
			require.True(t, local.ClaimGranted(id), "a refused claim must not spend the owner's adopt")
		})
	}
}

func TestAnUnrecordableClaimIsNotIssuedAndKeepsTheGrant(t *testing.T) {
	dir, local, pub, priv, id := secFixture(t)
	require.NoError(t, local.GrantClaim(id, "pixel"))
	readOnly(t, dir)
	resp, err := local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
	require.ErrorContains(t, err, "has NOT been issued")
	require.Equal(t, edgeauth.Response{}, resp)
	require.True(t, local.ClaimGranted(id))
}

// A grant with no timestamp (a legacy or hand-edited record) cannot be aged, so it is no grant.
func TestATimestamplessGrantIsRefusedAndCleared(t *testing.T) {
	dir, local, pub, priv, id := secFixture(t)
	b, err := json.Marshal([]edgeauth.ClaimGrant{{NodeID: id, Name: "pixel"}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(authDir(dir), edgeauth.ClaimsFile), b, 0o600))
	_, err = local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
	require.ErrorIs(t, err, edgeauth.ErrNotAdopted)
	require.False(t, local.ClaimGranted(id), "the un-ageable grant is cleared, not left to lean on")
}

func TestClaimsListsOnlyLiveGrants(t *testing.T) {
	dir, local, _, _, _ := secFixture(t)
	now := time.Now()
	grants := []edgeauth.ClaimGrant{
		{NodeID: "n_live", Name: "live", At: now.Unix()},
		{NodeID: "n_old", Name: "old", At: now.Add(-edgeauth.GrantValidity - time.Hour).Unix()},
		{NodeID: "n_untimed", Name: "untimed"},
	}
	b, err := json.Marshal(grants)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(authDir(dir), edgeauth.ClaimsFile), b, 0o600))

	got, err := local.Claims()
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "n_live", got[0].NodeID)

	// ReadClaims (no custody opened) agrees.
	got, err = edgeauth.ReadClaims(dir)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "n_live", got[0].NodeID)

	corrupt(t, dir, edgeauth.ClaimsFile)
	_, err = local.Claims()
	require.Error(t, err)
	_, err = edgeauth.ReadClaims(dir)
	require.Error(t, err)
}

func TestReadClaimsWithNoAuthorityIsNothing(t *testing.T) {
	got, err := edgeauth.ReadClaims(t.TempDir())
	require.NoError(t, err)
	require.Empty(t, got)

	// A designated authority that has granted nothing yet is also nothing.
	dir, _, _, _, _ := secFixture(t)
	got, err = edgeauth.ReadClaims(dir)
	require.NoError(t, err)
	require.Empty(t, got)

	// An edge dir that cannot even be looked into is an error, not an empty Edge.
	_, err = edgeauth.ReadClaims(fileAsDir(t))
	require.Error(t, err)
}

func TestGrantAndConsumeFailClosedOnACorruptClaimsFile(t *testing.T) {
	dir, local, _, _, id := secFixture(t)
	corrupt(t, dir, edgeauth.ClaimsFile)
	require.Error(t, local.GrantClaim(id, "pixel"))
	require.Error(t, local.ConsumeClaim(id))
	_, _, err := local.Forget(id)
	require.Error(t, err, "a forget that cannot clear the grant must not report success")
}

func TestABlankNodeIDIsNeverGranted(t *testing.T) {
	dir, local, _, _, _ := secFixture(t)
	for _, blank := range []string{"", "   ", "\t\n"} {
		require.NoError(t, local.GrantClaim(blank, "x"))
		got, err := local.ReAdmit(blank, "x")
		require.NoError(t, err)
		require.Empty(t, got)
		got, err = local.ReAdmitRoutine(blank, "x")
		require.NoError(t, err)
		require.Empty(t, got)
	}
	_, err := os.Stat(filepath.Join(authDir(dir), edgeauth.ClaimsFile))
	require.True(t, os.IsNotExist(err), "nothing was written for a blank id")
}

// --- revoke / forget / re-admit over damaged state -------------------------

func TestRevokeForgetAndReAdmitFailClosedOnUnreadableHistory(t *testing.T) {
	for _, dirt := range []func(*testing.T, string, string){corrupt, unreadable} {
		dir, local, _, _, id := secFixture(t)
		dirt(t, dir, edgeauth.IssuedHistoryFile)
		_, err := local.Revoke(id)
		require.Error(t, err)
		_, _, err = local.Forget(id)
		require.Error(t, err)
		_, err = local.ReAdmit(id, "x")
		require.Error(t, err)
		_, err = local.ReAdmitRoutine(id, "x")
		require.Error(t, err)
		require.False(t, local.ClaimGranted(id), "a failed re-admit grants nothing")
	}
}

func TestReAdmitRoutineFailsClosedOnAnUnreadableRevocationList(t *testing.T) {
	dir, local, _, _, id := secFixture(t)
	corrupt(t, dir, "revoked.json")
	_, err := local.ReAdmitRoutine(id, "x")
	require.Error(t, err)
	require.False(t, local.ClaimGranted(id))
}

// issued.json naming something that is not a serial is damage, not "nothing to revoke".
func TestAGarbledSerialStopsRevokeForgetAndReAdmit(t *testing.T) {
	dir, local, _, _, id := secFixture(t)
	b, err := json.Marshal(map[string]string{id: "not-a-serial"})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(authDir(dir), edgeauth.IssuedFile), b, 0o600))

	_, err = local.Revoke(id)
	require.ErrorContains(t, err, "is not a certificate serial")
	_, _, err = local.Forget(id)
	require.ErrorContains(t, err, "is not a certificate serial")
	_, err = local.ReAdmit(id, "x")
	require.ErrorContains(t, err, "is not a certificate serial")
	require.False(t, local.ClaimGranted(id))
}

// A node issued before per-node history was kept has its serial ONLY in issued.json. It must still
// be revoked, and once revoked must not claim again.
func TestALegacyIssuedOnlySerialIsStillRevoked(t *testing.T) {
	dir, local, pub, priv, id := secFixture(t)
	require.NoError(t, local.GrantClaim(id, "pixel"))
	resp, err := local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
	require.NoError(t, err)
	leaf, err := edgeauth.DecodeCert(resp.Cert)
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(authDir(dir), edgeauth.IssuedHistoryFile)))

	ok, err := local.HasIssued(id)
	require.NoError(t, err)
	require.True(t, ok)
	revoked, err := local.Revoke(id)
	require.NoError(t, err)
	require.Equal(t, []string{leaf.SerialNumber.String()}, revoked)

	require.NoError(t, local.GrantClaim(id, "pixel"))
	_, err = local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
	require.ErrorIs(t, err, edgeauth.ErrNotAdopted)
}

func TestIssuedStateThatCannotBeReadIsAnError(t *testing.T) {
	dir, local, _, _, id := secFixture(t)
	corrupt(t, dir, edgeauth.IssuedFile)
	_, err := local.HasIssued(id)
	require.Error(t, err)
	_, err = local.Revoke(id)
	require.Error(t, err)

	// And a reopen over it refuses rather than starting an authority that forgot what it issued.
	_, _, err = edgeauth.OpenLocal(dir)
	require.Error(t, err)
}

func TestForgetFailsClosedOnAnUnreadableEnrolledByRecord(t *testing.T) {
	dir, local, _, _, _ := secFixture(t)
	user, node := enrollee(t, local)
	_, err := local.Issue(enrollReq(user, node, time.Now()))
	require.NoError(t, err)
	corrupt(t, dir, edgeauth.EnrolledByFile)
	_, key, err := local.Forget(edgeauth.NodeID(node))
	require.Error(t, err, "the key that must be evicted cannot be known, so the forget is not reported done")
	require.Empty(t, key)
}

func TestForgetFailsClosedWhenTheAllowListCannotBeRewritten(t *testing.T) {
	dir, local, _, _, _ := secFixture(t)
	user, node := enrollee(t, local)
	_, err := local.Issue(enrollReq(user, node, time.Now()))
	require.NoError(t, err)
	corrupt(t, dir, edgeauth.AllowFile)
	_, key, err := local.Forget(edgeauth.NodeID(node))
	require.Error(t, err, "an enrolling key that could not be evicted is not reported evicted")
	require.Empty(t, key)
}

// --- protection --------------------------------------------------------------

func TestProtectionIsIdempotentAndFailsClosedOnDamage(t *testing.T) {
	dir, local, _, _, _ := secFixture(t)
	k, _ := keypair(t)
	key := hexEncode(k)

	require.NoError(t, local.AllowProtected(key))
	require.NoError(t, local.AllowProtected(key), "protecting twice is protecting once")
	require.NoError(t, local.Protect(key))
	allowed, err := local.Allowed()
	require.NoError(t, err)
	require.Equal(t, []string{key}, allowed)
	b, err := os.ReadFile(filepath.Join(authDir(dir), edgeauth.ProtectedKeysFile))
	require.NoError(t, err)
	var prot []string
	require.NoError(t, json.Unmarshal(b, &prot))
	require.Equal(t, []string{key}, prot)

	// A backfill when a protected set already exists changes nothing.
	other, _ := keypair(t)
	require.NoError(t, local.Allow(hexEncode(other)))
	require.NoError(t, local.BackfillProtection(hexEncode(other)))
	b, err = os.ReadFile(filepath.Join(authDir(dir), edgeauth.ProtectedKeysFile))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(b, &prot))
	require.Equal(t, []string{key}, prot, "backfill never adds to an existing protected set")

	corrupt(t, dir, edgeauth.ProtectedKeysFile)
	require.Error(t, local.Protect(key))
	require.Error(t, local.AllowProtected(key))

	corrupt(t, dir, edgeauth.AllowFile)
	require.Error(t, local.AllowProtected(key))
}

func TestBackfillProtectionRefusesToGuess(t *testing.T) {
	dir, local, _, _, _ := secFixture(t)
	require.NoError(t, local.BackfillProtection(""), "no own key, nothing to protect")
	_, err := os.Stat(filepath.Join(authDir(dir), edgeauth.ProtectedKeysFile))
	require.True(t, os.IsNotExist(err))

	corrupt(t, dir, edgeauth.AllowFile)
	k, _ := keypair(t)
	require.Error(t, local.BackfillProtection(hexEncode(k)))
}

// --- custody -----------------------------------------------------------------

func TestDesignateRefusesWhereItCannotWrite(t *testing.T) {
	_, err := edgeauth.Designate(fileAsDir(t), "shed")
	require.Error(t, err)
}

func TestARootKeyWithoutItsCertificateIsAnError(t *testing.T) {
	dir, _, _, _, _ := secFixture(t)
	require.NoError(t, os.Remove(filepath.Join(authDir(dir), edgeauth.RootCertFile)))
	_, _, err := edgeauth.OpenLocal(dir)
	require.Error(t, err)
}

func TestAnUnreadableRevocationListStopsRevoking(t *testing.T) {
	dir, local, pub, priv, id := secFixture(t)
	require.NoError(t, local.GrantClaim(id, "pixel"))
	_, err := local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
	require.NoError(t, err)
	unreadable(t, dir, "revoked.json")
	_, err = local.Revoke(id)
	require.Error(t, err, "a revocation that could not be persisted is not reported done")
	_, err = local.Revocations()
	require.Error(t, err)
}

// --- the claim's own signature checks ---------------------------------------

func TestAClaimSignatureCoversEveryFieldAndTheKeyItNames(t *testing.T) {
	pub, priv := keypair(t)
	good := edgeauth.NewClaimRequest("pixel", "mobile", pub, time.Now())
	good.Sign(priv)
	require.NoError(t, good.VerifySignature())
	got, err := good.NodePublicKey()
	require.NoError(t, err)
	require.Equal(t, pub, got)

	for name, mutate := range map[string]func(*edgeauth.ClaimRequest){
		"name":                 func(c *edgeauth.ClaimRequest) { c.Name = "other" },
		"kind":                 func(c *edgeauth.ClaimRequest) { c.Kind = "server" },
		"nonce":                func(c *edgeauth.ClaimRequest) { c.Nonce = "00" },
		"timestamp":            func(c *edgeauth.ClaimRequest) { c.TS++ },
		"node key not hex":     func(c *edgeauth.ClaimRequest) { c.NodeKey = "zz" },
		"node key too short":   func(c *edgeauth.ClaimRequest) { c.NodeKey = c.NodeKey[:10] },
		"signature not hex":    func(c *edgeauth.ClaimRequest) { c.Sig = "zz" },
		"signature too short":  func(c *edgeauth.ClaimRequest) { c.Sig = c.Sig[:10] },
		"node id not from key": func(c *edgeauth.ClaimRequest) { other, _ := keypair(t); c.NodeID = edgeauth.NodeID(other) },
	} {
		t.Run(name, func(t *testing.T) {
			c := good
			mutate(&c)
			require.ErrorIs(t, c.VerifySignature(), edgeauth.ErrBadSignature)
		})
	}
	bad := good
	bad.NodeKey = "zz"
	_, err = bad.NodePublicKey()
	require.ErrorIs(t, err, edgeauth.ErrBadSignature)
}

// --- the machine's own store -------------------------------------------------

func heldIdentity(t *testing.T) (*edgeauth.Identity, ed25519.PrivateKey) {
	t.Helper()
	a := authority(t)
	pub, priv := keypair(t)
	leaf, err := a.Issue(edgeauth.NodeID(pub), pub)
	require.NoError(t, err)
	return &edgeauth.Identity{
		NodeID: edgeauth.NodeID(pub), Cert: leaf, Root: a.Root(),
		CertPEM: edgeauth.EncodeCert(leaf), RootPEM: edgeauth.EncodeCert(a.Root()),
	}, priv
}

// Half a credential is worse than none: a save that fails partway takes the key back out.
func TestAHalfWrittenIdentityIsTakenBackOut(t *testing.T) {
	dir := t.TempDir()
	st := edgeauth.Store{Dir: dir}
	held, priv := heldIdentity(t)
	require.NoError(t, os.Mkdir(filepath.Join(dir, edgeauth.RootCertFile), 0o700))

	require.Error(t, st.SaveIdentity(held, priv, edgeauth.Descriptor{Kind: edgeauth.KindCore}))
	_, err := os.Stat(filepath.Join(dir, edgeauth.NodeKeyFile))
	require.True(t, os.IsNotExist(err), "the private key must not be left behind")
	_, _, ok, err := st.LoadIdentity()
	require.NoError(t, err)
	require.False(t, ok)
}

func TestADamagedIdentityIsAnErrorNeverAPartialIdentity(t *testing.T) {
	held, priv := heldIdentity(t)
	for name, damage := range map[string]func(t *testing.T, dir string){
		"key unreadable": func(t *testing.T, dir string) {
			require.NoError(t, os.Remove(filepath.Join(dir, edgeauth.NodeKeyFile)))
			require.NoError(t, os.Mkdir(filepath.Join(dir, edgeauth.NodeKeyFile), 0o700))
		},
		"root missing": func(t *testing.T, dir string) {
			require.NoError(t, os.Remove(filepath.Join(dir, edgeauth.RootCertFile)))
		},
		"root not a certificate": func(t *testing.T, dir string) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, edgeauth.RootCertFile), []byte("rubbish"), 0o600))
		},
		"descriptor unreadable": func(t *testing.T, dir string) {
			require.NoError(t, os.Remove(filepath.Join(dir, edgeauth.DescriptorFile)))
			require.NoError(t, os.Mkdir(filepath.Join(dir, edgeauth.DescriptorFile), 0o700))
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			st := edgeauth.Store{Dir: dir}
			require.NoError(t, st.SaveIdentity(held, priv, edgeauth.Descriptor{Kind: edgeauth.KindCore}))
			damage(t, dir)
			if name == "descriptor unreadable" {
				_, _, err := st.Descriptor()
				require.Error(t, err)
				return
			}
			id, key, ok, err := st.LoadIdentity()
			require.Error(t, err)
			require.False(t, ok)
			require.Nil(t, id)
			require.Nil(t, key)
		})
	}
}

// enrollment.feature ("the owner enrolls this machine as workshop"; one node per name): the name in
// an enrollment the authority accepted - signed by a user key it allowed - is remembered, so the
// fleet can call the member by it when it first sees it. A refused enrollment remembers nothing.
func TestTheAuthorityRemembersTheNameAMachineEnrolledUnder(t *testing.T) {
	dir := t.TempDir()
	local, err := edgeauth.Designate(dir, "hub")
	require.NoError(t, err)
	user, node := enrollee(t, local)
	r := enrollReq(user, node, time.Now())
	resp, err := local.Issue(r)
	require.NoError(t, err)
	require.Equal(t, "workshop", local.EnrolledName(resp.NodeID))

	otherNode, _ := keypair(t)
	refused := edgeauth.NewRequest(edgeauth.LocalAccount, "imposter", "host", otherNode, time.Now())
	_, strangerPriv := keypair(t)
	refused.Sign(strangerPriv)
	_, err = local.Issue(refused)
	require.Error(t, err)
	require.Empty(t, local.EnrolledName(refused.NodeID), "a refused enrollment names nothing")
	require.Empty(t, local.EnrolledName("n_nobody"))
}

// Pre-push audit (2026-10-26): SaveTrust reads the file first to keep the last signing time; an
// unreadable file must still be overwritten, as it always was, not fail every refresh after it.
func TestSaveTrustOverwritesAnUnreadableTrustFile(t *testing.T) {
	dir := t.TempDir()
	st := edgeauth.Store{Dir: dir}
	require.NoError(t, os.WriteFile(filepath.Join(dir, edgeauth.TrustFile), []byte("{not json"), 0o600))
	now := time.Now()
	require.NoError(t, st.SaveTrust([]string{"0a0a"}, now))
	tr, err := st.LoadTrust()
	require.NoError(t, err)
	require.Equal(t, []string{"0a0a"}, tr.Revoked)
	require.Equal(t, now.Unix(), tr.RefreshedAt)
}

// Pre-push audit (2026-10-26, minor): the authority's claim handler revokes into this machine's trust
// list while its paced refresh rewrites the same file; both read, change and write it. Concurrent
// changes must all land, none overwritten by another.
func TestConcurrentRevocationsAllLand(t *testing.T) {
	st := edgeauth.Store{Dir: t.TempDir()}
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			require.NoError(t, st.Revoke(fmt.Sprintf("%04x", i), time.Now()))
		}(i)
	}
	wg.Wait()
	tr, err := st.LoadTrust()
	require.NoError(t, err)
	require.Len(t, tr.Revoked, 40, "every revocation landed")
}
