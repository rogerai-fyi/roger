package station

// tlsid.go is the Station's TLS identity: the key that terminates the CONSUMER's session.
//
// # WHY THE STATION HOLDS THIS AND THE TOWER MUST NOT
//
// In the edge design a consumer's TLS session runs end to end to the Station, and the Tower
// in between splices bytes it cannot read. That property rests on exactly one thing: the
// private key for the relayed name lives here and nowhere else. A Tower holding it could
// terminate the session, read every prompt and completion, and re-encrypt onwards with
// nothing downstream able to tell.
//
// So the key is GENERATED HERE and never leaves. What travels is a certificate signing
// request and, back, a certificate - both public documents. Core provisions the certificate
// for a name under a domain Core controls, because a name a Station chose for itself would
// let one Station answer for another.
//
// # NOT PEM BY ACCIDENT
//
// The key file is PEM here, unlike the hex key files next to it, because it has to be
// readable by ordinary TLS tooling: an operator diagnosing a handshake reaches for openssl,
// and a bespoke encoding turns a five-minute problem into an afternoon.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	tlsKeyFile  = "tls.key"
	tlsCertFile = "tls.crt"
)

// TLSKeyPath and TLSCertPath say where the two halves live, so an operator installing a
// certificate is told the exact path rather than guessing at a layout.
func (s *Station) TLSKeyPath() string  { return filepath.Join(s.dir, tlsKeyFile) }
func (s *Station) TLSCertPath() string { return filepath.Join(s.dir, tlsCertFile) }

// EnsureTLSKey returns this Station's TLS private key, generating it on first use.
//
// IDEMPOTENT ON PURPOSE. Regenerating would silently invalidate a certificate already issued
// for the old key, and the failure would surface as handshake errors on live traffic rather
// than here.
func (s *Station) EnsureTLSKey() (*ecdsa.PrivateKey, error) {
	path := s.TLSKeyPath()
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		return parseECKey(raw, path)
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	blob := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	// 0600. This is the file that makes the Tower blind.
	if err := os.WriteFile(path, blob, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

func parseECKey(raw []byte, path string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("%s is not a PEM private key", path)
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s is not a usable EC private key: %w", path, err)
	}
	return key, nil
}

// SignCSR produces a certificate signing request for the name Core will issue against.
//
// The name is the CALLER's, not this Station's invention, and it is checked only for being
// present: which names are acceptable is Core's decision, and a Station that enforced its own
// idea of the naming scheme would need updating every time Core's changed.
func (s *Station) SignCSR(name string) ([]byte, error) {
	if name == "" {
		return nil, errors.New("a certificate request needs the name Core will issue it for")
	}
	key, err := s.EnsureTLSKey()
	if err != nil {
		return nil, err
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: name},
		DNSNames: []string{name},
	}, key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), nil
}

// InstallCert writes a certificate chain issued for this Station's key.
//
// IT REFUSES A CHAIN THAT DOES NOT MATCH THE KEY. Installing a mismatched certificate would
// leave the Station starting cleanly and failing every handshake, which reads as a network
// problem for as long as it takes somebody to check.
func (s *Station) InstallCert(chainPEM []byte) error {
	key, err := s.EnsureTLSKey()
	if err != nil {
		return err
	}
	if err := matchesKey(chainPEM, key); err != nil {
		return err
	}
	return os.WriteFile(s.TLSCertPath(), chainPEM, 0o644)
}

func matchesKey(chainPEM []byte, key *ecdsa.PrivateKey) error {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	// tls.X509KeyPair does the public-half comparison for us, and does it the same way the
	// server will when it loads them for real.
	if _, err := tls.X509KeyPair(chainPEM, keyPEM); err != nil {
		return fmt.Errorf("this certificate was not issued for this Station's key: %w", err)
	}
	return nil
}

// TLSCertificate loads the installed identity, ready to serve.
func (s *Station) TLSCertificate() (tls.Certificate, error) {
	chain, err := os.ReadFile(s.TLSCertPath())
	if errors.Is(err, os.ErrNotExist) {
		return tls.Certificate{}, fmt.Errorf("this Station has no certificate installed.\n"+
			"Run `roger-station csr --name <name>`, have Roger Core issue against it, and install "+
			"the result at %s", s.TLSCertPath())
	}
	if err != nil {
		return tls.Certificate{}, err
	}
	keyPEM, err := os.ReadFile(s.TLSKeyPath())
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(chain, keyPEM)
}
