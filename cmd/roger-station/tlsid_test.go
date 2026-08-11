package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/station"
)

func initDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	_, err := station.Init(dir)
	require.NoError(t, err)
	return dir
}

func TestCSREmitsARequestAndSaysWhereTheKeyStays(t *testing.T) {
	dir := initDir(t)
	var b bytes.Buffer
	require.NoError(t, run([]string{"csr", "--dir", dir, "--name", "st-a.relay.example"}, &b))

	out := b.String()
	require.Contains(t, out, "BEGIN CERTIFICATE REQUEST")
	require.Contains(t, out, filepath.Join(dir, "tls.key"))
	require.Contains(t, out, "must never be copied to a Tower")
	// The KEY ITSELF is never printed. An operator pasting this into a ticket must not be
	// pasting the thing that makes the Tower blind.
	require.NotContains(t, out, "PRIVATE KEY")
}

func TestCSRNeedsADirectoryAndAName(t *testing.T) {
	var b bytes.Buffer
	require.ErrorContains(t, run([]string{"csr"}, &b), "--dir is required")
	require.ErrorContains(t, run([]string{"csr", "--dir", initDir(t)}, &b), "--name is required")
	require.Error(t, run([]string{"csr", "--dir", t.TempDir(), "--name", "x"}, &b))
	require.Error(t, run([]string{"csr", "--nope"}, &b))
}

func TestInstallCertAcceptsWhatCoreIssued(t *testing.T) {
	dir := initDir(t)
	var b bytes.Buffer
	require.NoError(t, run([]string{"csr", "--dir", dir, "--name", "st-a.relay.example"}, &b))

	path := filepath.Join(t.TempDir(), "chain.pem")
	require.NoError(t, os.WriteFile(path, certForStationKey(t, dir), 0o644))

	b.Reset()
	require.NoError(t, run([]string{"install-cert", "--dir", dir, "--cert", path}, &b))
	require.Contains(t, b.String(), "certificate installed")
	require.FileExists(t, filepath.Join(dir, "tls.crt"))
}

func TestInstallCertRefusesTheWrongKeyAndSaysSo(t *testing.T) {
	dir := initDir(t)
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "chain.pem")
	require.NoError(t, os.WriteFile(path, issueOver(t, &other.PublicKey), 0o644))

	var b bytes.Buffer
	require.ErrorContains(t, run([]string{"install-cert", "--dir", dir, "--cert", path}, &b),
		"not issued for this Station's key")
}

func TestInstallCertNeedsItsArguments(t *testing.T) {
	var b bytes.Buffer
	require.ErrorContains(t, run([]string{"install-cert"}, &b), "--dir is required")
	require.ErrorContains(t, run([]string{"install-cert", "--dir", initDir(t)}, &b), "--cert is required")
	require.Error(t, run([]string{"install-cert", "--dir", t.TempDir(), "--cert", "x"}, &b))
	require.Error(t, run([]string{"install-cert", "--dir", initDir(t), "--cert", "/no/such/file"}, &b))
	require.Error(t, run([]string{"install-cert", "--nope"}, &b))
}

func TestBothCommandsAreListedInTheUsage(t *testing.T) {
	var b bytes.Buffer
	require.NoError(t, run(nil, &b))
	require.Contains(t, b.String(), "roger-station csr")
	require.Contains(t, b.String(), "install-cert")
}

// certForStationKey issues over whatever key the Station generated, the way Core would.
func certForStationKey(t *testing.T, dir string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "tls.key"))
	require.NoError(t, err)
	block, _ := pem.Decode(raw)
	require.NotNil(t, block)
	key, err := x509.ParseECPrivateKey(block.Bytes)
	require.NoError(t, err)
	return issueOver(t, &key.PublicKey)
}

func issueOver(t *testing.T, pub *ecdsa.PublicKey) []byte {
	t.Helper()
	ca, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "st-a.relay.example"},
		DNSNames:     []string{"st-a.relay.example"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, ca)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// A Station told to terminate TLS with nothing installed must refuse AT STARTUP. Starting
// anyway would mean a Station that looks healthy and fails every handshake.
func TestServingWithTLSAndNoCertificateRefusesAtStartup(t *testing.T) {
	dir := initDir(t)
	var b bytes.Buffer
	err := run([]string{"serve", "--dir", dir, "--tls", "--listen", "127.0.0.1:0",
		"--upstream", "http://127.0.0.1:1/v1",
		"--core-key", strings.Repeat("ab", 32),
		"--core-envelope-key", strings.Repeat("cd", 32)}, &b)
	require.ErrorContains(t, err, "roger-station csr")
}
