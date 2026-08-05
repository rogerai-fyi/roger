package towercert

// custody.go decides where the issuing root lives.
//
// NewAuthority mints a fresh root, which is right for a test and catastrophic for a
// deployment: a restart would issue a NEW root and every certificate already in an
// operator's hands would stop authenticating at once, with no recovery except re-enrolling
// every Tower on the network. LoadOrCreate is what production calls.
//
// THREE WAYS TO GET A ROOT, in priority order, and the order is the point.
//
//  1. INJECTED. Both halves supplied as PEM - from a secret manager, a sealed secret, a
//     mounted file. The process neither generates nor stores the root; it is handed one.
//     This is what a production deployment should do, because it keeps the root's custody
//     outside the application database and lets it be rotated without a code change.
//
//  2. PERSISTED. A root this deployment generated earlier and kept.
//
//  3. GENERATED ONCE, then persisted, with a loud log line. A self-hoster should not have
//     to run a PKI ceremony before their first Tower, but they should be told plainly that
//     their root is sitting in their database.
//
// A HALF-CONFIGURED root is refused rather than quietly falling through to (2) or (3):
// generating a root because one environment variable was missing is how a deployment ends
// up issuing under a root nobody meant to use.

import (
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"sync"
)

// Custody persists the root and the revocation set.
//
// The root KEY is the most sensitive material this system holds: whoever has it can mint a
// certificate for any Tower ID and speak as any Tower on the network. An implementation
// that stores it should be storing it encrypted at rest, and a deployment that can avoid
// storing it at all should use the injected path instead.
type Custody interface {
	// LoadRoot returns the stored root, or ok=false if none has been stored.
	LoadRoot() (keyPEM, certPEM []byte, ok bool, err error)
	// SaveRoot stores a newly generated root. It must not overwrite an existing one:
	// replacing a root silently would invalidate every certificate issued under it.
	SaveRoot(keyPEM, certPEM []byte) error

	// LoadRevoked returns every revoked serial.
	LoadRevoked() ([]string, error)
	// SaveRevoked records one, at the moment it is made. Waiting until shutdown means a
	// crash loses it - and a crash is exactly when somebody has just revoked urgently.
	SaveRevoked(serial string) error
}

// LoadOrCreate resolves the root by the ladder above and returns an authority over it.
func LoadOrCreate(cfg Config, store Custody) (*Authority, error) {
	if store == nil {
		return nil, errors.New("the certificate authority needs somewhere to keep its root")
	}

	haveKey, haveCert := len(cfg.RootKeyPEM) > 0, len(cfg.RootCertPEM) > 0
	switch {
	case haveKey != haveCert:
		return nil, errors.New(
			"a Tower CA root needs BOTH its key and its certificate: supplying one without the other " +
				"is a misconfiguration, and generating a root instead would issue under one nobody chose")
	case haveKey && haveCert:
		// Injected. Nothing is written: the operator's secret store owns this root.
		return authorityFromPEM(cfg, cfg.RootKeyPEM, cfg.RootCertPEM, store)
	}

	if keyPEM, certPEM, ok, err := store.LoadRoot(); err != nil {
		return nil, err
	} else if ok {
		return authorityFromPEM(cfg, keyPEM, certPEM, store)
	}

	// Nothing configured and nothing stored: first run.
	fresh, err := NewAuthority(cfg)
	if err != nil {
		return nil, err
	}
	keyPEM, certPEM, err := ExportRoot(fresh)
	if err != nil {
		return nil, err
	}
	if err := store.SaveRoot(keyPEM, certPEM); err != nil {
		return nil, err
	}
	log.Printf("tower CA: generated a new issuing root and stored it. " +
		"This root can mint a certificate for ANY Tower - move it to your secret store and " +
		"supply it as configuration before running in production.")
	return authorityFromPEM(cfg, keyPEM, certPEM, store)
}

// LoadOrCreateFrom adopts an already-built authority and attaches durable custody to it,
// so its revocations are persisted. Used where the root came from somewhere else entirely.
func LoadOrCreateFrom(a *Authority, store Custody) (*Authority, error) {
	if a == nil || store == nil {
		return nil, errors.New("both an authority and its custody are required")
	}
	revoked, err := store.LoadRevoked()
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	for _, s := range revoked {
		a.revoked[s] = true
	}
	a.custody = store
	a.mu.Unlock()
	return a, nil
}

func authorityFromPEM(cfg Config, keyPEM, certPEM []byte, store Custody) (*Authority, error) {
	keyBlock, _ := decodePEMBlock(keyPEM, "PRIVATE KEY")
	if keyBlock == nil {
		return nil, errors.New("the Tower CA key is not a usable PEM private key")
	}
	key, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("the Tower CA key could not be read: %w", err)
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, errors.New("the Tower CA key cannot sign")
	}

	certBlock, _ := decodePEMBlock(certPEM, "CERTIFICATE")
	if certBlock == nil {
		return nil, errors.New("the Tower CA certificate is not a usable PEM certificate")
	}
	root, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("the Tower CA certificate could not be read: %w", err)
	}

	// The halves must belong together. Two halves of DIFFERENT roots would otherwise
	// produce certificates that nothing on the network can verify, and the failure would
	// show up as every Tower being rejected rather than as a configuration error.
	type equaler interface{ Equal(crypto.PublicKey) bool }
	rootPub, ok := root.PublicKey.(equaler)
	if !ok || !rootPub.Equal(signer.Public()) {
		return nil, errors.New("the Tower CA key does not match the certificate it was supplied with")
	}

	revoked, err := store.LoadRevoked()
	if err != nil {
		return nil, err
	}
	a, err := NewAuthorityFrom(signer, root, cfg, revoked)
	if err != nil {
		return nil, err
	}
	a.custody = store
	return a, nil
}

// ExportRoot renders an authority's root as PEM, so it can be moved into a secret store.
func ExportRoot(a *Authority) (keyPEM, certPEM []byte, err error) {
	if a == nil {
		return nil, nil, errors.New("no authority")
	}
	der, err := x509.MarshalPKCS8PrivateKey(a.key)
	if err != nil {
		return nil, nil, err
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: a.root.Raw})
	return keyPEM, certPEM, nil
}

// decodePEMBlock finds the first block of the wanted type, so a bundle carrying more than
// one block does not depend on ordering.
func decodePEMBlock(raw []byte, want string) (*pem.Block, []byte) {
	rest := raw
	for {
		block, remainder := pem.Decode(rest)
		if block == nil {
			return nil, nil
		}
		if block.Type == want || (want == "PRIVATE KEY" && len(block.Type) > 11 &&
			block.Type[len(block.Type)-11:] == "PRIVATE KEY") {
			return block, remainder
		}
		rest = remainder
	}
}

// --- an in-memory custody, for tests and for a broker with no database --------

type memCustody struct {
	mu        sync.Mutex
	keyPEM    []byte
	certPEM   []byte
	revoked   []string
	writes    int
	failWrite bool
}

// NewMemCustody keeps a root for the lifetime of the process. It is what a broker with no
// durable store falls back to, and it is honest about what that means: the root does not
// survive a restart, so it is only ever appropriate for a test or a scratch deployment.
func NewMemCustody() Custody { return &memCustody{} }

func (m *memCustody) LoadRoot() ([]byte, []byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.keyPEM) == 0 {
		return nil, nil, false, nil
	}
	return m.keyPEM, m.certPEM, true, nil
}

func (m *memCustody) SaveRoot(keyPEM, certPEM []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failWrite {
		return errors.New("custody unavailable")
	}
	if len(m.keyPEM) != 0 {
		return errors.New("a root is already stored")
	}
	m.keyPEM, m.certPEM = keyPEM, certPEM
	m.writes++
	return nil
}

func (m *memCustody) LoadRevoked() ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.revoked))
	copy(out, m.revoked)
	return out, nil
}

func (m *memCustody) SaveRevoked(serial string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failWrite {
		return errors.New("custody unavailable")
	}
	m.revoked = append(m.revoked, serial)
	return nil
}
