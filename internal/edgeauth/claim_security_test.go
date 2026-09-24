package edgeauth_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edgeauth"
)

// secFixture returns an authority plus the directory it lives in, so a test can open a SECOND handle
// on the same authority - the shape that matters for cross-instance behaviour (a running daemon and
// a `roger edge forget` in another process share the directory, not the object).
func secFixture(t *testing.T) (dir string, local *edgeauth.Local, pub ed25519.PublicKey, priv ed25519.PrivateKey, id string) {
	t.Helper()
	dir = t.TempDir()
	var err error
	local, err = edgeauth.Designate(dir, "hub")
	require.NoError(t, err)
	pub, priv, err = ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return dir, local, pub, priv, edgeauth.NodeID(pub)
}

// A REVOKED node must not re-claim a fresh certificate - AND the check must hold when the revocation
// was made by a SEPARATE handle on the authority (a CLI forget beside the running daemon), which
// persists to disk without touching the daemon's in-memory set. The earlier fix read the in-memory
// set and so was inert in exactly this, the real, setup (audit 2026-09-23).
func TestClaimRefusesANodeRevokedByAnotherProcess(t *testing.T) {
	dir, local, pub, priv, id := secFixture(t)

	require.NoError(t, local.GrantClaim(id, "pixel-8"))
	resp, err := local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
	require.NoError(t, err)
	require.Contains(t, resp.Cert, "BEGIN CERTIFICATE")

	// A DIFFERENT handle on the same authority revokes and forgets - as `roger edge forget` does in
	// its own process. It writes revoked.json to disk; the first handle's memory is untouched.
	other, ok, err := edgeauth.OpenLocal(dir)
	require.NoError(t, err)
	require.True(t, ok)
	_, err = other.Revoke(id)
	require.NoError(t, err)

	// A grant reappears (a mistaken re-adopt) and the node claims against the ORIGINAL handle. It must
	// still be refused, because the revocation is read from disk, not from a stale snapshot.
	require.NoError(t, local.GrantClaim(id, "pixel-8"))
	_, err = local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
	require.Error(t, err, "a node revoked by another process must not re-claim a fresh certificate")
}

// Concurrent claims for the SAME adopted node issue AT MOST ONE certificate.
func TestClaimConcurrentIssuesAtMostOneCertificate(t *testing.T) {
	_, local, pub, priv, id := secFixture(t)
	require.NoError(t, local.GrantClaim(id, "pixel-8"))

	const n = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	var ok int
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, err := local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
			if err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	require.Equal(t, 1, ok, "exactly one concurrent claim may succeed")
}

// A grant that is never claimed expires; a claim past the window is refused.
func TestClaimRefusesAnExpiredGrant(t *testing.T) {
	_, local, pub, priv, id := secFixture(t)
	require.NoError(t, local.GrantClaim(id, "pixel-8"))

	late := time.Now().Add(edgeauth.GrantValidity + time.Hour)
	_, err := local.Claim(signedClaim(t, pub, priv, late), late)
	require.ErrorIs(t, err, edgeauth.ErrNotAdopted, "an expired grant is as good as none")
}

// Re-adopting after a grant expired RENEWS it (refreshes the clock), so the owner adopting again is
// not a silent no-op: the phone's next claim succeeds.
func TestReAdoptRenewsAnExpiredGrant(t *testing.T) {
	_, local, pub, priv, id := secFixture(t)
	require.NoError(t, local.GrantClaim(id, "pixel-8"))

	// The grant has expired; a claim now is refused, AND an expired grant is not shown as adopting.
	late := time.Now().Add(edgeauth.GrantValidity + time.Hour)
	_, err := local.Claim(signedClaim(t, pub, priv, late), late)
	require.Error(t, err)
	live, err := local.Claims()
	require.NoError(t, err)
	require.Empty(t, live, "an expired grant is not listed as a live adoption")

	// The owner adopts again: the grant is renewed and a claim now succeeds.
	require.NoError(t, local.GrantClaim(id, "pixel-8"))
	resp, err := local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
	require.NoError(t, err)
	require.Contains(t, resp.Cert, "BEGIN CERTIFICATE")
}

// Forgetting a node must revoke EVERY certificate it ever held, not only the most recent. A node
// re-issued while an earlier cert is still in date would otherwise keep that earlier cert working
// after forget, and rejoin by presenting it (audit 2026-09-24).
func TestRevokeEndsEveryCertificateANodeHeld(t *testing.T) {
	_, local, pub, priv, id := secFixture(t)

	// First cert (serial A).
	require.NoError(t, local.GrantClaim(id, "pixel-8"))
	respA, err := local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
	require.NoError(t, err)
	leafA, err := edgeauth.DecodeCert(respA.Cert)
	require.NoError(t, err)
	serialA := leafA.SerialNumber.String()

	// A second cert for the SAME node (serial B) - a re-adopt/re-claim while A is still valid.
	require.NoError(t, local.GrantClaim(id, "pixel-8"))
	respB, err := local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
	require.NoError(t, err)
	leafB, err := edgeauth.DecodeCert(respB.Cert)
	require.NoError(t, err)
	serialB := leafB.SerialNumber.String()
	require.NotEqual(t, serialA, serialB)

	// Forgetting the node revokes BOTH.
	revoked, err := local.Revoke(id)
	require.NoError(t, err)
	require.Contains(t, revoked, serialA, "the earlier certificate must be revoked too")
	require.Contains(t, revoked, serialB)
	require.True(t, local.Authority().SerialRevoked(serialA))
	require.True(t, local.Authority().SerialRevoked(serialB))
}

// A revocation made by ONE handle on the authority must be visible to ANOTHER handle's Revocations()
// - the distributed path other members refresh from reads the persisted list, not a per-process
// snapshot (audit 2026-09-24).
func TestRevocationsSeesAnotherProcessRevoke(t *testing.T) {
	dir, local, pub, priv, id := secFixture(t)
	require.NoError(t, local.GrantClaim(id, "pixel-8"))
	resp, err := local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
	require.NoError(t, err)
	leaf, err := edgeauth.DecodeCert(resp.Cert)
	require.NoError(t, err)
	serial := leaf.SerialNumber.String()

	// Revoke through a SECOND handle (a CLI forget in its own process).
	other, ok, err := edgeauth.OpenLocal(dir)
	require.NoError(t, err)
	require.True(t, ok)
	_, err = other.Revoke(id)
	require.NoError(t, err)

	// The ORIGINAL handle's Revocations() - what a member refreshes from - includes it.
	revs, err := local.Revocations()
	require.NoError(t, err)
	require.Contains(t, revs, serial, "a revoke from another process is in the served list")
}

// Revocations() fails CLOSED: an unreadable revocation list returns an error, so the endpoint answers
// 503 rather than serving an empty list that would let a revoked node look fine.
func TestRevocationsFailsClosedOnACorruptList(t *testing.T) {
	dir, local, _, _, _ := secFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, edgeauth.AuthorityDir, "revoked.json"), []byte("{not json"), 0o600))
	_, err := local.Revocations()
	require.Error(t, err, "an unreadable revocation list is an error, never an empty list")
}

// Forget clears the grant AND revokes every issued certificate in one step, so a node that was
// forgotten cannot re-claim (grant gone) nor present an old cert (all serials revoked) (audit 2026-09-24).
func TestForgetClearsGrantAndRevokesEveryCert(t *testing.T) {
	_, local, pub, priv, id := secFixture(t)
	require.NoError(t, local.GrantClaim(id, "pixel-8"))
	respA, err := local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
	require.NoError(t, err)
	leafA, err := edgeauth.DecodeCert(respA.Cert)
	require.NoError(t, err)
	require.NoError(t, local.GrantClaim(id, "pixel-8"))
	respB, err := local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
	require.NoError(t, err)
	leafB, err := edgeauth.DecodeCert(respB.Cert)
	require.NoError(t, err)

	// A grant is standing again (a mistaken re-adopt) when forget runs.
	require.NoError(t, local.GrantClaim(id, "pixel-8"))
	revoked, err := local.Forget(id)
	require.NoError(t, err)
	require.Contains(t, revoked, leafA.SerialNumber.String())
	require.Contains(t, revoked, leafB.SerialNumber.String())
	require.False(t, local.ClaimGranted(id), "forget cleared the standing grant")

	// A claim after forget is refused: the grant is gone.
	_, err = local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
	require.ErrorIs(t, err, edgeauth.ErrNotAdopted)
}

// HasIssued fails CLOSED: an unreadable issued-certificate state is an error, so a caller (forget)
// never mistakes it for "this node has no certificate" and leaves a live cert unrevoked (audit 2026-09-24).
func TestHasIssuedFailsClosedOnCorruptState(t *testing.T) {
	dir, local, _, _, id := secFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, edgeauth.AuthorityDir, "issued_history.json"), []byte("{bad"), 0o600))
	_, err := local.HasIssued(id)
	require.Error(t, err, "an unreadable issued state is an error, not a bare false")
}

// A forgotten machine must not RE-ENROLL under the same node id and get a fresh certificate: the
// enroll path (Issue) checks the revoked history just as the claim path does, or a revoke means
// nothing once the account key is still valid (audit 2026-09-24).
func TestEnrollRefusesARevokedNode(t *testing.T) {
	_, local, _, _, _ := secFixture(t)
	userPub, userPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	require.NoError(t, local.Allow(hexEncode(userPub)))

	nodePub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	nodeID := edgeauth.NodeID(nodePub)

	// First enroll succeeds and mints a certificate.
	req := edgeauth.NewRequest(local.Account(), "workshop", "host", nodePub, time.Now())
	req.Sign(userPriv)
	resp, err := local.Issue(req)
	require.NoError(t, err)
	require.Contains(t, resp.Cert, "BEGIN CERTIFICATE")

	// The machine is forgotten (its certificate revoked).
	revoked, err := local.Forget(nodeID)
	require.NoError(t, err)
	require.NotEmpty(t, revoked)

	// Re-enrolling the SAME node id is refused - the revocation stands even though the account key is
	// still allowed.
	again := edgeauth.NewRequest(local.Account(), "workshop", "host", nodePub, time.Now())
	again.Sign(userPriv)
	_, err = local.Issue(again)
	require.Error(t, err, "a revoked node must not re-enroll a fresh certificate")
}

func hexEncode(b []byte) string { return hex.EncodeToString(b) }
