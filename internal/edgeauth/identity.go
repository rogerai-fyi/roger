package edgeauth

// identity.go is the NODE's side: what it checks before it believes an answer, and what
// it keeps on disk afterwards.
//
// NOTHING IS WRITTEN UNTIL EVERYTHING CHECKS OUT. CheckResponse is total - it either
// returns a whole identity or an error - and Store.SaveIdentity is called only after
// it. That is what makes "a failed enrollment leaves the machine exactly as it was" a
// property of the shape of the code rather than of remembering to clean up.

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rogerai.fm/roger/v6/internal/towercore/cert"
)

// The files a machine's Edge directory holds. They are named rather than assembled at
// each call site so a test, an installer and a support answer all say the same thing.
const (
	// NodeKeyFile is the node's PRIVATE key. 0600, generated here, never transmitted.
	NodeKeyFile = "node.key"
	// NodeCertFile is the certificate the authority issued for it.
	NodeCertFile = "node.crt"
	// RootCertFile is the Edge's PUBLIC root - what every peer is checked against.
	RootCertFile = "root.crt"
	// DescriptorFile records which authority roots this Edge.
	DescriptorFile = "authority.json"
	// TrustFile holds the revocation list and when it was last refreshed.
	TrustFile = "trust.json"
	// AuthorityDir is where a DESIGNATED machine keeps the root it generated. It
	// exists on exactly one machine per Edge, and only if the owner made one.
	AuthorityDir = "authority"
	// RootKeyFile is the root's private half. 0600, and it never leaves this
	// directory: a root that travels is a root that leaks.
	RootKeyFile = "root.key"
	// AllowFile is the user keys this local authority will issue to.
	AllowFile = "allow.json"
	// IssuedFile maps a node to the serial of the certificate it was issued, which is
	// what a revocation names.
	IssuedFile = "issued.json"
)

// ErrDifferentAuthority refuses an answer from an authority that is not the one this
// Edge already has. An Edge has exactly one root at a time; mixing them would mean a
// peer that verifies for half the fleet.
var ErrDifferentAuthority = errors.New("that certificate comes from a different authority")

// Identity is what a node ends up holding.
type Identity struct {
	NodeID  string
	Account string
	Cert    *x509.Certificate
	Root    *x509.Certificate
	CertPEM string
	RootPEM string
}

// NotAfter is when this identity stops being one.
func (id *Identity) NotAfter() time.Time {
	if id == nil || id.Cert == nil {
		return time.Time{}
	}
	return id.Cert.NotAfter
}

// NeedsRenewal reports whether the certificate is far enough through its life to be
// renewed. Two thirds, the same point the Tower renews at: early enough that the old
// one still overlaps the new, late enough that renewal is rare.
func (id *Identity) NeedsRenewal(now time.Time) bool {
	if id == nil || id.Cert == nil {
		return true
	}
	life := id.Cert.NotAfter.Sub(id.Cert.NotBefore)
	if life <= 0 {
		return true
	}
	return now.After(id.Cert.NotBefore.Add(life * 2 / 3))
}

// Expect is what the asking node already knows, and therefore what the answer has to
// agree with.
type Expect struct {
	NodeID  string
	Account string
	Key     ed25519.PublicKey
	Now     time.Time
	// Root, when set, is the root this Edge ALREADY has. An answer rooted anywhere
	// else is refused rather than quietly adopted.
	Root *x509.Certificate
}

// CheckResponse decides whether an enrollment answer may be applied.
//
// Every row of the spec's defect table lands on exactly one of these, in an order
// chosen so the owner is told the most specific true thing: an expired certificate is
// expired, not "unknown authority", even though a chain check would refuse both.
func CheckResponse(resp Response, want Expect) (*Identity, error) {
	root, err := DecodeCert(resp.Root)
	if err != nil {
		return nil, fmt.Errorf("the Edge root that came back is unusable: %w", err)
	}
	leaf, err := DecodeCert(resp.Cert)
	if err != nil {
		return nil, fmt.Errorf("the certificate that came back is unusable: %w", err)
	}
	if resp.NodeID == "" || resp.Account == "" {
		return nil, fmt.Errorf("%w: it names no node or no account", ErrMalformed)
	}
	if want.Account != "" && resp.Account != want.Account {
		return nil, fmt.Errorf("%w: it enrolls this machine somewhere else", ErrWrongAccount)
	}
	now := want.Now
	if now.IsZero() {
		now = time.Now()
	}
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return nil, fmt.Errorf("%w: it is not valid now", ErrCertExpired)
	}
	verifier, err := cert.NewVerifier(root, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnknownAuthority, err)
	}
	named, err := verifier.Authenticate(leaf)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnknownAuthority, err)
	}
	if want.NodeID != "" && named != want.NodeID {
		return nil, fmt.Errorf("%w: it names another node", ErrIdentityMismatch)
	}
	if resp.NodeID != named {
		return nil, fmt.Errorf("%w: the answer and the certificate name different nodes", ErrIdentityMismatch)
	}
	if want.Key != nil {
		if err := verifier.ProveMatches(leaf, want.Key); err != nil {
			return nil, fmt.Errorf("%w: it is bound to another key", ErrIdentityMismatch)
		}
	}
	if want.Root != nil && !want.Root.Equal(root) {
		return nil, ErrDifferentAuthority
	}
	return &Identity{
		NodeID: named, Account: resp.Account, Cert: leaf, Root: root,
		CertPEM: resp.Cert, RootPEM: resp.Root,
	}, nil
}

// --- what a machine keeps ------------------------------------------------

// Store is one machine's Edge directory.
type Store struct{ Dir string }

func (s Store) path(name string) string { return filepath.Join(s.Dir, name) }

// SaveIdentity writes the whole identity or nothing. The node key is 0600 - a private
// key another user on the box can read is not a private key.
func (s Store) SaveIdentity(id *Identity, key ed25519.PrivateKey, d Descriptor) error {
	if id == nil || len(key) != ed25519.PrivateKeySize {
		return errors.New("an Edge identity is a certificate AND the key it binds")
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	d.Fingerprint = RootFingerprint(id.Root)
	desc, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	writes := []struct {
		name string
		body []byte
		perm os.FileMode
	}{
		{NodeKeyFile, []byte(hex.EncodeToString(key)), 0o600},
		{NodeCertFile, []byte(id.CertPEM), 0o600},
		{RootCertFile, []byte(id.RootPEM), 0o600},
		{DescriptorFile, desc, 0o600},
	}
	for _, w := range writes {
		if err := os.WriteFile(s.path(w.name), w.body, w.perm); err != nil {
			// Half a credential is worse than none: take the attempt back out.
			s.forgetIdentityFiles()
			return err
		}
	}
	return nil
}

// LoadIdentity reads what this machine holds. Missing is not an error: a machine that
// has never enrolled is an ordinary state, not a broken one.
func (s Store) LoadIdentity() (*Identity, ed25519.PrivateKey, bool, error) {
	keyHex, err := os.ReadFile(s.path(NodeKeyFile))
	if os.IsNotExist(err) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	raw, err := hex.DecodeString(strings.TrimSpace(string(keyHex)))
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return nil, nil, false, errors.New("this machine's Edge key is unreadable")
	}
	certPEM, err := os.ReadFile(s.path(NodeCertFile))
	if err != nil {
		return nil, nil, false, err
	}
	rootPEM, err := os.ReadFile(s.path(RootCertFile))
	if err != nil {
		return nil, nil, false, err
	}
	leaf, err := DecodeCert(string(certPEM))
	if err != nil {
		return nil, nil, false, err
	}
	root, err := DecodeCert(string(rootPEM))
	if err != nil {
		return nil, nil, false, err
	}
	id := &Identity{
		NodeID: leaf.Subject.CommonName, Cert: leaf, Root: root,
		CertPEM: string(certPEM), RootPEM: string(rootPEM),
	}
	if acct, err := s.AccountName(); err == nil {
		id.Account = acct
	}
	return id, ed25519.PrivateKey(raw), true, nil
}

// accountFile records which account this machine's certificate enrolled it into. It is
// kept beside the certificate rather than read out of it, because the certificate binds
// a NODE, not an account - the account is Edge state.
const accountFile = "account"

// AccountName is the account this machine's certificate enrolled it into. Absent is not
// an error: a machine that has never enrolled belongs to no Edge.
func (s Store) AccountName() (string, error) {
	b, err := os.ReadFile(s.path(accountFile))
	if os.IsNotExist(err) {
		return "", nil
	}
	return strings.TrimSpace(string(b)), err
}

// SaveAccount records the account an identity belongs to.
func (s Store) SaveAccount(account string) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(s.path(accountFile), []byte(account), 0o600)
}

// Descriptor is which authority roots this Edge. Absent means "none chosen yet", which
// is Core by default and is not an error.
func (s Store) Descriptor() (Descriptor, bool, error) {
	b, err := os.ReadFile(s.path(DescriptorFile))
	if os.IsNotExist(err) {
		return Descriptor{Kind: KindCore}, false, nil
	}
	if err != nil {
		return Descriptor{}, false, err
	}
	var d Descriptor
	if err := json.Unmarshal(b, &d); err != nil {
		return Descriptor{}, false, err
	}
	return d, true, nil
}

// SaveDescriptor records the choice.
func (s Store) SaveDescriptor(d Descriptor) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path(DescriptorFile), b, 0o600)
}

// ForgetIdentity removes the NODE identity and leaves the Edge. The root, the
// descriptor, the revocation list and the ACCOUNT LABEL stay: the owner still owns this
// Edge and its fleet, this machine is simply no longer a member of it - and a machine
// that re-enrolls after this is a NEW node, because it has no key to keep.
//
// The account label in particular must survive, because it is what the fleet on disk is
// filed under. Dropping it here would make every administrative change - a forget, an
// authority migration - look like a fleet that emptied itself.
func (s Store) ForgetIdentity() error {
	s.forgetIdentityFiles()
	return nil
}

func (s Store) forgetIdentityFiles() {
	for _, n := range []string{NodeKeyFile, NodeCertFile} {
		_ = os.Remove(s.path(n))
	}
}

// --- trust: the root, and what has been revoked under it -----------------

// Trust is what this machine knows about which certificates are still good.
type Trust struct {
	Revoked     []string `json:"revoked,omitempty"`
	RefreshedAt int64    `json:"refreshed_at,omitempty"`
}

// Age is how long since this machine last refreshed the list.
func (t Trust) Age(now time.Time) time.Duration {
	if t.RefreshedAt == 0 {
		return 0
	}
	return now.Sub(time.Unix(t.RefreshedAt, 0))
}

// Stale reports whether the list is older than the freshness window. A stale list is
// STATED, never silently trusted: the danger is not that it is old, it is that a
// revoked node looks fine because nobody said the list had stopped being refreshed.
func (t Trust) Stale(now time.Time, window time.Duration) bool {
	if t.RefreshedAt == 0 {
		return false // never refreshed because nothing is known yet, not because it lapsed
	}
	return t.Age(now) > window
}

// LoadTrust reads the revocation list.
func (s Store) LoadTrust() (Trust, error) {
	b, err := os.ReadFile(s.path(TrustFile))
	if os.IsNotExist(err) {
		return Trust{}, nil
	}
	if err != nil {
		return Trust{}, err
	}
	var t Trust
	if err := json.Unmarshal(b, &t); err != nil {
		return Trust{}, err
	}
	return t, nil
}

// SaveTrust records the revocation list and when it was refreshed.
func (s Store) SaveTrust(revoked []string, at time.Time) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(Trust{Revoked: revoked, RefreshedAt: at.Unix()}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path(TrustFile), b, 0o600)
}

// Revoke adds one serial to this machine's list and refreshes its timestamp.
func (s Store) Revoke(serial string, at time.Time) error {
	t, err := s.LoadTrust()
	if err != nil {
		return err
	}
	for _, got := range t.Revoked {
		if got == serial {
			return s.SaveTrust(t.Revoked, at)
		}
	}
	return s.SaveTrust(append(t.Revoked, serial), at)
}

// Trust returns the VERIFY-ONLY authority this machine checks its peers with, and what
// it knows about how current that check is.
//
// Verify-only is the point: the returned authority holds the PUBLIC root and no private
// half, so it can authenticate every peer of this Edge and it cannot issue anything. A
// node that could issue would be a second authority.
func (s Store) Trust(now time.Time) (*cert.Authority, Trust, error) {
	rootPEM, err := os.ReadFile(s.path(RootCertFile))
	if err != nil {
		return nil, Trust{}, fmt.Errorf("this machine holds no Edge root: %w", err)
	}
	root, err := DecodeCert(string(rootPEM))
	if err != nil {
		return nil, Trust{}, err
	}
	t, err := s.LoadTrust()
	if err != nil {
		return nil, Trust{}, err
	}
	auth, err := cert.NewVerifier(root, t.Revoked)
	if err != nil {
		return nil, Trust{}, err
	}
	return auth, t, nil
}
