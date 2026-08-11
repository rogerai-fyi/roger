package station

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func initStation(t *testing.T) *Station {
	t.Helper()
	s, err := Init(t.TempDir())
	require.NoError(t, err)
	return s
}

// The key is MINTED HERE. Nothing takes one as input, which is what keeps it off the Tower.
func TestTheTLSKeyIsGeneratedOnTheStationAndKeptPrivate(t *testing.T) {
	s := initStation(t)
	key, err := s.EnsureTLSKey()
	require.NoError(t, err)
	require.NotNil(t, key)

	info, err := os.Stat(s.TLSKeyPath())
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"the key that makes the Tower blind must not be world-readable")
}

// Regenerating would silently invalidate a certificate already issued for the old key, and
// the failure would show up as handshake errors on live traffic rather than here.
func TestAnExistingTLSKeyIsReusedRatherThanReplaced(t *testing.T) {
	s := initStation(t)
	first, err := s.EnsureTLSKey()
	require.NoError(t, err)
	second, err := s.EnsureTLSKey()
	require.NoError(t, err)
	require.True(t, first.Equal(second))
}

func TestAnUnreadableTLSKeyIsNamed(t *testing.T) {
	s := initStation(t)
	require.NoError(t, os.WriteFile(s.TLSKeyPath(), []byte("not pem"), 0o600))
	_, err := s.EnsureTLSKey()
	require.ErrorContains(t, err, "not a PEM private key")

	blob := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte("junk")})
	require.NoError(t, os.WriteFile(s.TLSKeyPath(), blob, 0o600))
	_, err = s.EnsureTLSKey()
	require.ErrorContains(t, err, "not a usable EC private key")
}

func TestATLSKeyThatCannotBeWrittenIsReported(t *testing.T) {
	s := initStation(t)
	require.NoError(t, os.MkdirAll(s.TLSKeyPath(), 0o755))
	_, err := s.EnsureTLSKey()
	require.Error(t, err)
}

func TestTheCSRCarriesTheNameCoreWillIssueFor(t *testing.T) {
	s := initStation(t)
	raw, err := s.SignCSR("st-abc123.relay.example")
	require.NoError(t, err)

	block, _ := pem.Decode(raw)
	require.NotNil(t, block)
	require.Equal(t, "CERTIFICATE REQUEST", block.Type)
	req, err := x509.ParseCertificateRequest(block.Bytes)
	require.NoError(t, err)
	require.NoError(t, req.CheckSignature())
	require.Equal(t, []string{"st-abc123.relay.example"}, req.DNSNames)

	key, err := s.EnsureTLSKey()
	require.NoError(t, err)
	require.True(t, req.PublicKey.(*ecdsa.PublicKey).Equal(&key.PublicKey),
		"the request must be for the key that stays on this Station")
}

func TestACSRWithoutANameIsRefused(t *testing.T) {
	s := initStation(t)
	_, err := s.SignCSR("")
	require.ErrorContains(t, err, "the name Core will issue it for")
}

func TestACSRCannotBeMadeWithoutAKey(t *testing.T) {
	s := initStation(t)
	require.NoError(t, os.WriteFile(s.TLSKeyPath(), []byte("not pem"), 0o600))
	_, err := s.SignCSR("st-a.relay.example")
	require.Error(t, err)
}

func TestAnIssuedCertificateInstallsAndLoads(t *testing.T) {
	s := initStation(t)
	key, err := s.EnsureTLSKey()
	require.NoError(t, err)

	require.NoError(t, s.InstallCert(issueFor(t, &key.PublicKey)))
	cert, err := s.TLSCertificate()
	require.NoError(t, err)
	require.NotEmpty(t, cert.Certificate)
}

// A mismatched certificate would leave the Station starting cleanly and failing every
// handshake - which reads as a network problem for as long as it takes somebody to check.
func TestACertificateForSomebodyElsesKeyIsRefused(t *testing.T) {
	s := initStation(t)
	_, err := s.EnsureTLSKey()
	require.NoError(t, err)

	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	err = s.InstallCert(issueFor(t, &other.PublicKey))
	require.ErrorContains(t, err, "not issued for this Station's key")
	require.NoFileExists(t, s.TLSCertPath())
}

func TestAStationWithNoCertificateSaysHowToGetOne(t *testing.T) {
	s := initStation(t)
	_, err := s.TLSCertificate()
	require.ErrorContains(t, err, "roger-station csr")
	require.ErrorContains(t, err, s.TLSCertPath())
}

func TestAnUnreadableCertificateIsReported(t *testing.T) {
	s := initStation(t)
	require.NoError(t, os.MkdirAll(s.TLSCertPath(), 0o755))
	_, err := s.TLSCertificate()
	require.Error(t, err)
}

func TestACertificateWithNoKeyBesideItIsReported(t *testing.T) {
	s := initStation(t)
	key, err := s.EnsureTLSKey()
	require.NoError(t, err)
	require.NoError(t, s.InstallCert(issueFor(t, &key.PublicKey)))
	require.NoError(t, os.Remove(s.TLSKeyPath()))
	_, err = s.TLSCertificate()
	require.Error(t, err)
}

func TestInstallingIntoAnUnwritablePathIsReported(t *testing.T) {
	s := initStation(t)
	key, err := s.EnsureTLSKey()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(s.TLSCertPath(), 0o755))
	require.Error(t, s.InstallCert(issueFor(t, &key.PublicKey)))
}

func TestTheIdentityPathsSitInTheStationDirectory(t *testing.T) {
	s := initStation(t)
	require.Equal(t, filepath.Join(s.Dir(), "tls.key"), s.TLSKeyPath())
	require.Equal(t, filepath.Join(s.Dir(), "tls.crt"), s.TLSCertPath())
}

// issueFor stands in for Core's issuance: a certificate over somebody's public key.
func issueFor(t *testing.T, pub *ecdsa.PublicKey) []byte {
	t.Helper()
	ca, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "st-abc123.relay.example"},
		DNSNames:     []string{"st-abc123.relay.example"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, ca)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
