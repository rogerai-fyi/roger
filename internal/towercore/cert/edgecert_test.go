package cert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"testing"

	"github.com/stretchr/testify/require"
)

func testAuthority(t *testing.T) *Authority {
	t.Helper()
	a, err := NewAuthority(Config{TTL: 24 * 3600 * 1e9})
	require.NoError(t, err)
	return a
}

// An edge certificate is a SERVER cert for the relay name, chaining to the root and usable to
// verify a TLS connection - which is what an unmodified consumer needs.
func TestAnEdgeCertificateIsAServerCertForTheName(t *testing.T) {
	a := testAuthority(t)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	leaf, err := a.IssueEdgeCert("st-abc.relay.example", &key.PublicKey)
	require.NoError(t, err)
	require.Equal(t, []string{"st-abc.relay.example"}, leaf.DNSNames)
	require.Contains(t, leaf.ExtKeyUsage, x509.ExtKeyUsageServerAuth)
	require.NotContains(t, leaf.ExtKeyUsage, x509.ExtKeyUsageClientAuth,
		"an edge cert must not double as a Tower channel identity")

	// It verifies against the root for the name.
	roots := x509.NewCertPool()
	roots.AddCert(a.Root())
	_, err = leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "st-abc.relay.example",
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	require.NoError(t, err)
}

func TestEdgeCertNeedsANameAndKey(t *testing.T) {
	a := testAuthority(t)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	_, err = a.IssueEdgeCert("", &key.PublicKey)
	require.ErrorContains(t, err, "relay name")
	_, err = a.IssueEdgeCert("st.relay.example", nil)
	require.ErrorContains(t, err, "public key")
}

// THE WILDCARD-CERTIFICATE REVIEW, as a test: the CA must refuse a name that would let one
// Station's certificate cover another's - a wildcard, a bare label, whitespace, a control
// character. Each of these was a way to break the edge path's confidentiality.
func TestTheCADoesNotMintADangerousName(t *testing.T) {
	a := testAuthority(t)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	for _, bad := range []string{
		"*.relay.example",      // the wildcard: one cert for every relay name
		"*",                    // bare wildcard
		"relay",                // single label, no dot
		"st-1.relay.example ",  // trailing space
		"st-1.relay.example\n", // control char
		"UPPER.relay.example",  // uppercase (case-folding surprises)
		"st-1..relay.example",  // empty label
		"-lead.relay.example",  // label starting with a dash
	} {
		_, err := a.IssueEdgeCert(bad, &key.PublicKey)
		require.Error(t, err, "the CA must refuse %q", bad)
	}
}
