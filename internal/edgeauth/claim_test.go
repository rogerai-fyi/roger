package edgeauth_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edgeauth"
)

func claimFixture(t *testing.T) (*edgeauth.Local, ed25519.PublicKey, ed25519.PrivateKey, string) {
	t.Helper()
	local, err := edgeauth.Designate(t.TempDir(), "hub")
	require.NoError(t, err)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return local, pub, priv, edgeauth.NodeID(pub)
}

func signedClaim(t *testing.T, pub ed25519.PublicKey, priv ed25519.PrivateKey, now time.Time) edgeauth.ClaimRequest {
	t.Helper()
	c := edgeauth.NewClaimRequest("pixel-8", "mobile", pub, now)
	c.Sign(priv)
	return c
}

func TestClaimNeedsAdoptThenIssuesAndConsumes(t *testing.T) {
	local, pub, priv, id := claimFixture(t)

	// Not adopted -> refused.
	_, err := local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
	require.ErrorIs(t, err, edgeauth.ErrNotAdopted)

	// The owner adopts (grants), then the phone claims and gets a certificate.
	require.NoError(t, local.GrantClaim(id))
	resp, err := local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
	require.NoError(t, err)
	require.Equal(t, id, resp.NodeID)
	require.Equal(t, edgeauth.LocalAccount, resp.Account)
	require.Contains(t, resp.Cert, "BEGIN CERTIFICATE")

	// The issued certificate authenticates under the authority's own root.
	leaf, err := edgeauth.DecodeCert(resp.Cert)
	require.NoError(t, err)
	got, err := local.Authority().Authenticate(leaf)
	require.NoError(t, err)
	require.Equal(t, id, got)

	// The grant is consumed: a second claim with the same key is refused (one adopt, one cert).
	_, err = local.Claim(signedClaim(t, pub, priv, time.Now()), time.Now())
	require.ErrorIs(t, err, edgeauth.ErrNotAdopted)
}

func TestClaimRefusesIdentityMismatch(t *testing.T) {
	local, pub, priv, id := claimFixture(t)
	require.NoError(t, local.GrantClaim(id))
	c := signedClaim(t, pub, priv, time.Now())
	c.NodeID = "n_someothernode00000000000000000000000000000000" // claim one id, prove another
	_, err := local.Claim(c, time.Now())
	require.ErrorIs(t, err, edgeauth.ErrBadSignature)
}

func TestClaimRefusesBadSignature(t *testing.T) {
	local, pub, priv, id := claimFixture(t)
	require.NoError(t, local.GrantClaim(id))
	c := signedClaim(t, pub, priv, time.Now())
	c.Sig = "00"
	_, err := local.Claim(c, time.Now())
	require.ErrorIs(t, err, edgeauth.ErrBadSignature)
}

func TestClaimRefusesWrongKeyForNode(t *testing.T) {
	local, _, _, id := claimFixture(t)
	require.NoError(t, local.GrantClaim(id))
	// A different keypair signs a claim naming id N - but N is not the id THIS key derives, so it
	// cannot even name N consistently; sign with the impostor and set N.
	_, impostor, _ := ed25519.GenerateKey(rand.Reader)
	impPub := impostor.Public().(ed25519.PublicKey)
	c := edgeauth.NewClaimRequest("pixel-8", "mobile", impPub, time.Now())
	c.Sign(impostor)
	c.NodeID = id // claim the adopted id with the wrong key
	_, err := local.Claim(c, time.Now())
	require.ErrorIs(t, err, edgeauth.ErrBadSignature)
}

func TestClaimRefusesStale(t *testing.T) {
	local, pub, priv, id := claimFixture(t)
	require.NoError(t, local.GrantClaim(id))
	c := signedClaim(t, pub, priv, time.Now().Add(-30*time.Minute))
	_, err := local.Claim(c, time.Now())
	require.ErrorIs(t, err, edgeauth.ErrStaleRequest)
}

func TestGrantClaimIdempotentAndConsume(t *testing.T) {
	local, _, _, id := claimFixture(t)
	require.NoError(t, local.GrantClaim(id))
	require.NoError(t, local.GrantClaim(id)) // idempotent
	got, err := local.Claims()
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.True(t, local.ClaimGranted(id))
	require.NoError(t, local.ConsumeClaim(id))
	require.False(t, local.ClaimGranted(id))
	require.NoError(t, local.ConsumeClaim(id)) // consuming a missing grant is not an error
}
