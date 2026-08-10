// Package towercert issues and authenticates the credential a joined Tower speaks with.
//
// This certificate is the only thing that lets a community-run machine act on the public
// network as a named Tower. Everything downstream - inventory, routing, dispatch,
// settlement - trusts the Tower ID it asserts, so every check here is really one question:
// can a machine end up speaking as a Tower it is not?
//
// THREE PROPERTIES SHAPE THE DESIGN.
//
// It names exactly one Tower. A certificate carrying two identities is an ambiguity, and an
// attacker picks which answer we use.
//
// It carries no authority beyond the channel. The spec puts it as "no wallet, settlement,
// admin, or platform-signing authority"; the enforceable form is that the certificate may
// not issue another identity and may not be used for anything but the joined channel. A
// Tower that could mint a Tower would be a second admission authority.
//
// It is SHORT-LIVED. The lease is the long-lived thing, and it lives in the admission
// registry where it can be changed. A certificate cannot be recalled once handed out, so
// the way we take one back is to have already planned its expiry - revocation is the
// urgent path, not the ordinary one.
package cert

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"sync"
	"time"
)

// trustDomain is the authority half of every workload identity we issue. Keeping Towers
// and Stations in ONE domain but on DIFFERENT paths is what lets a single check reject a
// Station credential presented on a Tower channel, rather than that depending on somebody
// remembering to compare types.
const trustDomain = "spiffe://rogerai.fm"

const towerPath = "/tower/"

// defaultTTL is how long an issued certificate lives. Short by intent: see the package
// comment on why expiry, not revocation, is the ordinary way a credential ends.
const defaultTTL = time.Hour

// serialBits is the entropy in a certificate serial. A serial is what revocation names, so
// a guessable one lets somebody talk about a certificate that does not exist yet.
const serialBits = 128

// URI is a workload identity.
type URI string

func (u URI) String() string { return string(u) }

// TowerURI is the identity a joined Tower certificate carries.
func TowerURI(towerID string) URI {
	return URI(trustDomain + towerPath + towerID)
}

// validTowerID reports whether an ID can be an identity at all. It must survive a round
// trip through a URI unchanged: anything that re-parses differently is a chance for the
// name we checked and the name we act on to diverge.
func validTowerID(id string) bool {
	if id == "" || strings.TrimSpace(id) != id {
		return false
	}
	if strings.ContainsAny(id, "/\\ \t\r\n?#%:@") {
		return false
	}
	u, err := url.Parse(TowerURI(id).String())
	if err != nil {
		return false
	}
	return u.Path == towerPath+id
}

// towerIDFromURI extracts the Tower an identity names, and reports whether it is a Tower
// identity at all.
func towerIDFromURI(u *url.URL) (string, bool) {
	if u == nil || u.Scheme+"://"+u.Host != trustDomain {
		return "", false
	}
	if !strings.HasPrefix(u.Path, towerPath) {
		return "", false // a Station, or something else entirely
	}
	id := strings.TrimPrefix(u.Path, towerPath)
	if !validTowerID(id) {
		return "", false
	}
	return id, true
}

// Config tunes the authority.
type Config struct {
	TTL time.Duration
	// RootKeyPEM and RootCertPEM inject an existing root, so the deployment's secret store
	// owns it rather than the application database. Supply BOTH or neither: see custody.go
	// for why a half-configured root is refused instead of generated.
	RootKeyPEM  []byte
	RootCertPEM []byte
}

// Authority is Roger Core's issuer for joined-Tower credentials.
type Authority struct {
	cfg  Config
	key  crypto.Signer
	root *x509.Certificate

	mu      sync.RWMutex
	revoked map[string]bool // serial (decimal string) -> revoked
	// custody persists revocations as they are made. Nil means this authority keeps them
	// only in memory, which is correct for a test and never for a deployment.
	custody Custody
}

// NewAuthority mints a fresh root and returns an authority over it. Production loads a
// persisted root through NewAuthorityFrom; this is for tests and first-run bootstrap.
func NewAuthority(cfg Config) (*Authority, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "RogerAI Tower Admission CA"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		// The chain is exactly root -> Tower. No intermediate may appear, because an
		// intermediate is a second thing that can name Towers.
		MaxPathLen:     0,
		MaxPathLenZero: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, err
	}
	root, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return NewAuthorityFrom(key, root, cfg, nil)
}

// NewAuthorityFrom builds an authority over an existing root and a known revocation set.
//
// The revocation set is a PARAMETER rather than in-process state on purpose: a revocation
// that lives only in the process that made it is undone by the next deploy, which is
// exactly the defect the admission registry had. The caller loads it from durable storage
// and hands it in.
func NewAuthorityFrom(key crypto.Signer, root *x509.Certificate, cfg Config, revoked []string) (*Authority, error) {
	if key == nil || root == nil {
		return nil, errors.New("an authority needs a root certificate and its key")
	}
	if !root.IsCA {
		return nil, errors.New("that root is not a certificate authority")
	}
	if cfg.TTL <= 0 {
		cfg.TTL = defaultTTL
	}
	a := &Authority{cfg: cfg, key: key, root: root, revoked: map[string]bool{}}
	for _, s := range revoked {
		a.revoked[s] = true
	}
	return a, nil
}

// Root returns the issuing certificate.
func (a *Authority) Root() *x509.Certificate { return a.root }

// RootKey returns the issuing key, so a caller can persist and reload the authority.
func (a *Authority) RootKey() crypto.Signer { return a.key }

func randomSerial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), serialBits))
}

// Issue mints a certificate binding a Tower ID to the key it proved it holds.
func (a *Authority) Issue(towerID string, pub crypto.PublicKey) (*x509.Certificate, error) {
	return a.issueForTest(towerID, pub, nil)
}

// issueForTest is Issue with a hook that mutates the template before signing. It exists so
// the rejection table can be driven with certificates this authority really signed - the
// interesting failures are the ones that chain correctly and are still wrong, and a
// hand-rolled certificate would not test the same thing.
func (a *Authority) issueForTest(towerID string, pub crypto.PublicKey, mutate func(*x509.Certificate)) (*x509.Certificate, error) {
	if !validTowerID(towerID) {
		return nil, fmt.Errorf("%q is not a usable Tower ID", towerID)
	}
	if pub == nil {
		return nil, errors.New("a certificate must bind a public key")
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(TowerURI(towerID).String())
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: towerID},
		URIs:         []*url.URL{u},
		// A minute of backdating absorbs ordinary clock skew between us and the Tower.
		// Without it a freshly issued certificate is briefly "not yet valid" on a host
		// whose clock runs a little behind ours.
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(a.cfg.TTL),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
	}
	if mutate != nil {
		mutate(tmpl)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, a.root, pub, a.key)
	if err != nil {
		return nil, err
	}
	return x509.ParseCertificate(der)
}

// Revoke ends a certificate by serial. Revoking twice is not an error: a revocation is a
// decision about a state, not an event to be counted, and making the second call fail
// would turn a retried admin action into a spurious alarm.
func (a *Authority) Revoke(serial *big.Int) error {
	if serial == nil {
		return errors.New("revocation names a serial")
	}
	a.mu.Lock()
	custody := a.custody
	a.mu.Unlock()

	// Persisted FIRST, and the failure is reported. A revocation we could not record would
	// be undone by the next restart, and an operator who was told it succeeded would have
	// no reason to look again - so an in-memory-only revocation must never be reported as
	// done.
	if custody != nil {
		if err := custody.SaveRevoked(serial.String()); err != nil {
			return fmt.Errorf("that revocation could not be recorded, so it has NOT taken effect: %w", err)
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.revoked[serial.String()] = true
	return nil
}

// RevokedSerials returns the revocation set, for the caller to persist.
func (a *Authority) RevokedSerials() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]string, 0, len(a.revoked))
	for s := range a.revoked {
		out = append(out, s)
	}
	return out
}

func (a *Authority) isRevoked(serial *big.Int) bool {
	if serial == nil {
		return true // a certificate with no serial cannot be checked, so it is not trusted
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.revoked[serial.String()]
}

// Authenticate verifies a presented certificate and returns the Tower it names.
//
// It answers "which Tower is this, if any" - never "is this certificate broadly OK". The
// caller gets an identity or an error, so there is no path where a caller forgets to look
// at the identity and proceeds anyway.
func (a *Authority) Authenticate(leaf *x509.Certificate) (string, error) {
	if leaf == nil {
		return "", errors.New("no certificate was presented")
	}

	roots := x509.NewCertPool()
	roots.AddCert(a.root)
	// Verify covers the chain, the validity window, and the extended key usage. It also
	// rejects any critical extension it does not understand, which is the "unsupported
	// critical constraint" row: a constraint we cannot evaluate must never be ignored.
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		return "", fmt.Errorf("that certificate does not authenticate a Tower: %w", err)
	}

	// Verify accepts a CA as a leaf; we do not. A Tower that could issue would be a second
	// admission authority, which is the one power this whole design withholds.
	if leaf.IsCA || leaf.KeyUsage&x509.KeyUsageCertSign != 0 {
		return "", errors.New("a Tower certificate may not issue other certificates")
	}
	// Client auth and nothing else. Verify only checks that the usage we asked for is
	// PRESENT, so a certificate carrying extra powers still passes it.
	if len(leaf.ExtKeyUsage) != 1 || leaf.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		return "", errors.New("a Tower certificate is scoped to the joined channel only")
	}
	if len(leaf.URIs) != 1 {
		return "", errors.New("a Tower certificate names exactly one identity")
	}
	id, ok := towerIDFromURI(leaf.URIs[0])
	if !ok {
		return "", errors.New("that certificate does not name a Tower in this network")
	}
	if a.isRevoked(leaf.SerialNumber) {
		return "", errors.New("that certificate has been revoked")
	}
	return id, nil
}

// AuthenticateAs verifies a certificate AND that it speaks for the Tower expected here. A
// valid credential for one Tower must not answer for another.
func (a *Authority) AuthenticateAs(leaf *x509.Certificate, towerID string) error {
	got, err := a.Authenticate(leaf)
	if err != nil {
		return err
	}
	if got != towerID {
		return fmt.Errorf("this channel belongs to %s, not %s", got, towerID)
	}
	return nil
}

// ProveMatches reports whether a presented public key is the one the certificate binds.
//
// A certificate crosses the wire on every handshake, so it is public by nature; what makes
// it a credential is the key nobody else holds. In a live channel TLS proves possession
// itself - this is the same check for the paths that verify an already-completed handshake.
func (a *Authority) ProveMatches(leaf *x509.Certificate, pub crypto.PublicKey) error {
	if leaf == nil || pub == nil {
		return errors.New("proof of possession needs a certificate and a key")
	}
	type equaler interface{ Equal(crypto.PublicKey) bool }
	certPub, ok := leaf.PublicKey.(equaler)
	if !ok {
		return errors.New("that certificate carries an unusable key")
	}
	if !certPub.Equal(pub) {
		return errors.New("that key does not match the certificate")
	}
	return nil
}
