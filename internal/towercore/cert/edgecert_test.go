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
