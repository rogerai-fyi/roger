package enrollhttp

// Core enrollment is authenticated by TLS (features/edge/enrollment_root_trust.feature, Core): the
// enrollment client verifies the server's certificate against the trusted roots and never skips
// it, so a device on the LAN answering for Core cannot complete the handshake.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTheEnrollmentClientVerifiesTLSAndASeamOnlyChangesWhichRootsAreTrusted(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("-----BEGIN CERTIFICATE-----\n"))
	}))
	defer srv.Close()

	_, err := Root(context.Background(), srv.URL)
	require.Error(t, err, "a certificate no trusted root vouches for is refused")
	require.Contains(t, err.Error(), "certificate")

	orig := Client
	t.Cleanup(func() { Client = orig })
	Client = srv.Client() // trusts exactly this server's root, still verifying
	_, err = Root(context.Background(), srv.URL)
	require.NoError(t, err)

	impostor := impostorTLSServer(t)
	defer impostor.Close()
	_, err = Root(context.Background(), impostor.URL)
	require.Error(t, err, "another server's certificate does not verify under the trusted root")
}

// impostorTLSServer answers TLS with a certificate of its own for 127.0.0.1 - every httptest TLS
// server shares one built-in certificate, so an impostor needs a different one.
func impostorTLSServer(t *testing.T) *httptest.Server {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "broker"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	srv.StartTLS()
	return srv
}
