package edgeauth_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edgeauth"
)

// A REVOKED node must not be able to re-claim a fresh, unrevoked certificate. The audit found that a
// leftover grant plus no revocation check let a forgotten node POST /edge/claim and rejoin. Once a
// node's certificate is revoked, a claim for that node id is refused even if a grant is present.
func TestClaimRefusesANodeWhoseCertIsRevoked(t *testing.T) {
	local, pub, priv, id := claimFixture(t)

	require.NoError(t, local.GrantClaim(id, "pixel-8"))
	resp, err := local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
	require.NoError(t, err)
	require.Contains(t, resp.Cert, "BEGIN CERTIFICATE")

	// The owner revokes the node (it left the Edge, or was compromised).
	_, err = local.Revoke(id)
	require.NoError(t, err)

	// Even if a grant reappears (a stale serving-path grant, a re-adopt by mistake), the node cannot
	// claim a new certificate while its serial stands revoked.
	require.NoError(t, local.GrantClaim(id, "pixel-8"))
	_, err = local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
	require.Error(t, err, "a revoked node must not re-claim a fresh certificate")
}

// Concurrent claims for the SAME adopted node must issue AT MOST ONE certificate: one adopt, one
// cert. The audit found the check-then-issue-then-consume was unlocked, so two racing claims both
// issued and issued.json kept only the last serial (making the other un-revokable).
func TestClaimConcurrentIssuesAtMostOneCertificate(t *testing.T) {
	local, pub, priv, id := claimFixture(t)
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

// A grant that is never claimed must not stand open forever: a bounded window limits how long an
// accidental or coerced adopt remains a live invitation (audit minor: ClaimGrant.At but no expiry).
func TestClaimRefusesAnExpiredGrant(t *testing.T) {
	local, pub, priv, id := claimFixture(t)
	require.NoError(t, local.GrantClaim(id, "pixel-8"))

	// The grant was made now; a claim a long time later (and signed then, to pass freshness) is too
	// old to honour.
	late := time.Now().Add(edgeauth.GrantValidity + time.Hour)
	_, err := local.Claim(signedClaim(t, pub, priv, late), late)
	require.ErrorIs(t, err, edgeauth.ErrNotAdopted, "an expired grant is as good as none")
}

func init() { _ = ed25519.GenerateKey; _ = rand.Reader }
