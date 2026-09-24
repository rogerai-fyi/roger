package edgeauth

// local.go is the airgap answer: a machine the owner designates generates the Edge root
// here, keeps the private half here, and issues certificates to the rest of the network
// without Core existing at all.
//
// THE ROOT DOES NOT TRAVEL. Only root.crt is ever handed out. root.key is written 0600
// in this directory and nothing in this package ever reads it out to a caller, puts it
// in a Response, or sends it anywhere - this package cannot send anything anywhere (see
// structural_test.go).
//
// AN AUTHORITY STILL DECIDES WHO MAY JOIN. A network with no Core has no account
// registry, so the owner keeps one here: the allow-list of user keys this authority
// will issue to, starting with the designating machine's own. "No internet" is not a
// reason to let any machine on the LAN mint itself an identity.

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"rogerai.fm/roger/v6/internal/towercore/cert"
)

// EdgeCertTTL is how long an Edge certificate lives. Long enough that a plant network
// is not renewing hourly, short enough that a credential nobody remembers issuing
// eventually stops working on its own.
const EdgeCertTTL = 30 * 24 * time.Hour

// GrantValidity bounds how long an unclaimed adopt stands open. A phone claims within seconds of
// being adopted; a grant that is never claimed should not remain a live invitation forever, so an
// accidental or coerced adopt expires on its own (defence in depth beside consume-on-forget).
const GrantValidity = 24 * time.Hour

// ErrRootExists refuses to generate a second root. An Edge has exactly one.
var ErrRootExists = errors.New("this machine already holds an Edge root")

// Local is a designated machine's Edge authority.
type Local struct {
	dir   string // <edge dir>/authority
	where string
	auth  *cert.Authority
	iss   *Issuer
	// mu serialises claim, grant and issue: check-take-issue-record-consume must be atomic so two
	// racing claims cannot both mint a certificate for one adopt (audit 2026-09-23).
	mu sync.Mutex
}

// Designate generates this Edge's root, here, once.
func Designate(edgeDir, where string) (*Local, error) {
	dir := filepath.Join(edgeDir, AuthorityDir)
	if _, err := os.Stat(filepath.Join(dir, RootKeyFile)); err == nil {
		return nil, ErrRootExists
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, whereFile), []byte(where), 0o600); err != nil {
		return nil, err
	}
	// cert.LoadOrCreate's third rung: nothing configured and nothing stored, so it
	// generates once and hands the root straight to custody, which writes it 0600. The
	// generation, the storage and the refusal to overwrite are all already written and
	// already tested there; duplicating them here would be a second root ceremony to
	// keep in step with the first.
	return open(dir)
}

// whereFile records the name the owner gave the designated machine.
const whereFile = "where"

// OpenLocal loads a designated machine's authority. Not being one is not an error.
func OpenLocal(edgeDir string) (*Local, bool, error) {
	dir := filepath.Join(edgeDir, AuthorityDir)
	if _, err := os.Stat(filepath.Join(dir, RootKeyFile)); os.IsNotExist(err) {
		return nil, false, nil
	} else if err != nil {
		return nil, false, err
	}
	l, err := open(dir)
	if err != nil {
		return nil, false, err
	}
	return l, true, nil
}

// open builds the authority over whatever custody holds in this directory.
func open(dir string) (*Local, error) {
	auth, err := cert.LoadOrCreate(cert.Config{TTL: EdgeCertTTL}, &fileCustody{dir: dir})
	if err != nil {
		return nil, err
	}
	l := &Local{dir: dir, auth: auth}
	if b, err := os.ReadFile(filepath.Join(dir, whereFile)); err == nil {
		l.where = string(b)
	}
	l.iss = NewIssuer(IssuerConfig{Authority: auth, Accounts: l.registry()})
	issued, err := l.loadIssued()
	if err != nil {
		return nil, err
	}
	l.iss.mu.Lock()
	for id, serial := range issued {
		l.iss.issued[id] = serial
	}
	l.iss.mu.Unlock()
	return l, nil
}

// Authority is the issuing authority. It holds the root's private half, which is why it
// exists only on the designated machine.
func (l *Local) Authority() *cert.Authority { return l.auth }

// lockFile is the advisory lock every mutation of this authority's JSON state takes, so a claim in
// the daemon and a forget in the CLI cannot interleave their read-modify-write.
const lockFile = ".authlock"

// withLock runs fn while holding BOTH the in-process mutex and a cross-process file lock on the
// authority directory, so claims.json / issued.json / revoked.json stay consistent under concurrent
// writers whether they share this process or not (audit 2026-09-23).
func (l *Local) withLock(fn func() error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, err := os.OpenFile(filepath.Join(l.dir, lockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := flockExclusive(f); err != nil {
		return err
	}
	defer func() { _ = flockUnlock(f) }()
	return fn()
}

// revokedNow reads the PERSISTED revocation list from disk, so a serial revoked by a separate
// process (a CLI forget beside the running authority) is seen at once rather than only after a
// restart. It fails CLOSED: if the list cannot be read, the caller must treat the node as revoked.
func (l *Local) revokedNow() (map[string]bool, error) {
	got, err := (&fileCustody{dir: l.dir}).LoadRevoked()
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(got))
	for _, sName := range got {
		set[sName] = true
	}
	return set, nil
}

// Descriptor is what this authority is, for the owner and for the machines it roots.
func (l *Local) Descriptor() Descriptor {
	return Descriptor{Kind: KindLocal, Where: l.where, Fingerprint: RootFingerprint(l.auth.Root())}
}

// Issuer is the signing side, ready to serve.
func (l *Local) Issuer() *Issuer { return l.iss }

// Issue mints a certificate and remembers its serial, so forgetting the node later can
// revoke exactly that certificate.
func (l *Local) Issue(r Request) (Response, error) {
	resp, err := l.iss.Issue(r)
	if err != nil {
		return resp, err
	}
	if serial, ok := l.iss.SerialOf(resp.NodeID); ok {
		if err := l.withLock(func() error { return l.recordIssued(resp.NodeID, serial) }); err != nil {
			return Response{}, fmt.Errorf("that certificate could not be recorded, so it has NOT been issued: %w", err)
		}
	}
	return resp, nil
}

// registry is who this authority will issue to: the user keys on its allow-list.
func (l *Local) registry() Registry {
	return func(userKey string) (string, bool) {
		allowed, err := l.Allowed()
		if err != nil {
			return "", false
		}
		for _, k := range allowed {
			if k == userKey {
				return l.Account(), true
			}
		}
		return "", false
	}
}

// LocalAccount is the account label a locally rooted Edge enrolls into.
//
// It is a CONSTANT rather than something derived from the root, and that is deliberate:
// the account is only a scoping label for one machine's fleet, and deriving it from the
// root would mean that changing authority silently emptied the fleet - the exact
// failure "members are not silently dropped" forbids. What actually distinguishes one
// Edge from another is the ROOT, and a peer from another Edge is refused as an unknown
// authority however its advertisement is labelled.
const LocalAccount = "edge-local"

// Account is the account a locally rooted Edge enrolls into. There is no Core here to
// have a login with, so the Edge names itself.
func (l *Local) Account() string { return LocalAccount }

// Allow adds a machine's user key to the list this authority will issue to.
func (l *Local) Allow(userKeyHex string) error {
	got, err := l.Allowed()
	if err != nil {
		return err
	}
	for _, k := range got {
		if k == userKeyHex {
			return nil
		}
	}
	return l.writeJSON(AllowFile, append(got, userKeyHex))
}

// Allowed lists the user keys this authority will issue to.
func (l *Local) Allowed() ([]string, error) {
	var out []string
	err := l.readJSON(AllowFile, &out)
	return out, err
}

// ClaimGrant is one adopted node awaiting its certificate: the owner said yes (a grant), and the
// node's friendly name is kept so it can be shown as ADOPTING until it claims and checks in.
type ClaimGrant struct {
	NodeID string `json:"node_id"`
	Name   string `json:"name,omitempty"`
	At     int64  `json:"at,omitempty"`
}

// GrantClaim records that the owner has ADOPTED a node, so it may claim a certificate
// (features/edge/claim.feature). Idempotent - adopting twice grants once; a later grant fills in a
// name it did not have.
func (l *Local) GrantClaim(nodeID, name string) error {
	if strings.TrimSpace(nodeID) == "" {
		return nil
	}
	return l.withLock(func() error {
		got, err := l.readClaims()
		if err != nil {
			return err
		}
		for i := range got {
			if got[i].NodeID == nodeID {
				// A re-adopt renews the grant: it refreshes the clock (so an expired grant becomes
				// live again rather than a silent no-op) and fills in a name it did not have.
				got[i].At = time.Now().Unix()
				if name != "" {
					got[i].Name = name
				}
				return l.writeJSON(ClaimsFile, got)
			}
		}
		return l.writeJSON(ClaimsFile, append(got, ClaimGrant{NodeID: nodeID, Name: name, At: time.Now().Unix()}))
	})
}

// Claims lists the nodes the owner has adopted and that may claim a certificate.
func (l *Local) Claims() ([]ClaimGrant, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	got, err := l.readClaims()
	if err != nil {
		return nil, err
	}
	// An expired grant is no longer a live adoption: it is not shown as ADOPTING and does not keep a
	// candidate out of the DISCOVERED band. It is pruned from the file lazily, when it is next
	// claimed (refused) or re-granted.
	now := time.Now()
	live := got[:0]
	for _, g := range got {
		if g.At > 0 && now.Sub(time.Unix(g.At, 0)) > GrantValidity {
			continue
		}
		live = append(live, g)
	}
	return live, nil
}

// readClaims is the unlocked read the claim/grant/consume path shares while holding l.mu.
func (l *Local) readClaims() ([]ClaimGrant, error) {
	var out []ClaimGrant
	err := l.readJSON(ClaimsFile, &out)
	return out, err
}

// ClaimGranted reports whether a node has been adopted.
func (l *Local) ClaimGranted(nodeID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.grantFor(nodeID)
	return ok
}

// grantFor returns the standing grant for a node id, unlocked. Callers hold l.mu.
func (l *Local) grantFor(nodeID string) (ClaimGrant, bool) {
	got, _ := l.readClaims()
	for _, g := range got {
		if g.NodeID == nodeID {
			return g, true
		}
	}
	return ClaimGrant{}, false
}

// ConsumeClaim removes one grant, so a claim is one-time: one adopt, one certificate. Removing a
// grant that is not there is not an error (the desired end state already holds).
func (l *Local) ConsumeClaim(nodeID string) error {
	return l.withLock(func() error { return l.consumeClaim(nodeID) })
}

// consumeClaim removes one grant, unlocked. Callers hold l.mu.
func (l *Local) consumeClaim(nodeID string) error {
	got, err := l.readClaims()
	if err != nil {
		return err
	}
	kept := got[:0]
	for _, g := range got {
		if g.NodeID != nodeID {
			kept = append(kept, g)
		}
	}
	return l.writeJSON(ClaimsFile, kept)
}

// Claim issues a certificate to an ADOPTED candidate that proves possession of its node key. It is
// the node-key-authorised counterpart of Issue (which is account-key-authorised): the owner's adopt
// is the authorisation, the node-key signature is the proof, and the grant is consumed on success.
func (l *Local) Claim(c ClaimRequest, now time.Time) (Response, error) {
	if err := c.VerifySignature(); err != nil {
		return Response{}, err
	}
	if d := now.Sub(time.Unix(c.TS, 0)); d > RequestFreshness || d < -RequestFreshness {
		return Response{}, ErrStaleRequest
	}
	// The whole take-grant -> issue -> record -> consume runs under the authority lock (in-process
	// AND cross-process) so two racing claims for one adopt cannot both mint a certificate.
	var resp Response
	err := l.withLock(func() error {
		grant, ok := l.grantFor(c.NodeID)
		if !ok {
			return ErrNotAdopted
		}
		// A grant that has stood too long is as good as none: it expires on its own, and the stale
		// record is cleared so it cannot be leaned on again.
		if grant.At > 0 && now.Sub(time.Unix(grant.At, 0)) > GrantValidity {
			_ = l.consumeClaim(c.NodeID)
			return ErrNotAdopted
		}
		// A node whose certificate stands REVOKED must not mint a fresh one, even if a grant
		// reappears (a stale serving-path grant, a mistaken re-adopt): revocation is final until a
		// new root relationship is established. The list is read from DISK so a revocation made by a
		// separate process (a CLI forget beside the running authority) is honoured at once. It fails
		// CLOSED: if the list cannot be read, the claim is refused rather than issued blind.
		revoked, err := l.revokedNow()
		if err != nil {
			return fmt.Errorf("could not read the revocation list, so no certificate was issued: %w", err)
		}
		if serial, ok := l.issuedSerial(c.NodeID); ok && revoked[serial] {
			return ErrNotAdopted
		}
		pub, err := c.NodePublicKey()
		if err != nil {
			return err
		}
		leaf, err := l.auth.Issue(c.NodeID, pub)
		if err != nil {
			return fmt.Errorf("that certificate could not be issued: %w", err)
		}
		if err := l.recordIssued(c.NodeID, leaf.SerialNumber.String()); err != nil {
			return fmt.Errorf("that certificate could not be recorded, so it has NOT been issued: %w", err)
		}
		// Consume the grant only AFTER the certificate is safely recorded, so a write failure does
		// not spend the owner's adopt for nothing.
		if err := l.consumeClaim(c.NodeID); err != nil {
			return err
		}
		resp = Response{
			NodeID:  c.NodeID,
			Account: l.Account(),
			Cert:    EncodeCert(leaf),
			Root:    EncodeCert(l.auth.Root()),
		}
		return nil
	})
	if err != nil {
		return Response{}, err
	}
	return resp, nil
}

// Revoke ends the certificate this authority issued to a node. It returns the serial it
// revoked so the caller can put it in its own trust store too.
func (l *Local) Revoke(nodeID string) (string, error) {
	var serial string
	err := l.withLock(func() error {
		issued, err := l.loadIssued()
		if err != nil {
			return err
		}
		s, ok := issued[nodeID]
		if !ok {
			return nil // nothing was ever issued to that node here
		}
		n, ok := parseSerial(s)
		if !ok {
			return fmt.Errorf("%q is not a certificate serial", s)
		}
		if err := l.auth.Revoke(n); err != nil {
			return err
		}
		serial = s
		return nil
	})
	return serial, err
}

// RootPEM is the PUBLIC root this authority signs under - the only half that travels.
func (l *Local) RootPEM() string { return EncodeCert(l.auth.Root()) }

// Revocations is every serial this authority has revoked - what a node refreshes.
func (l *Local) Revocations() []string { return l.auth.RevokedSerials() }

func (l *Local) loadIssued() (map[string]string, error) {
	out := map[string]string{}
	err := l.readJSON(IssuedFile, &out)
	return out, err
}

// issuedSerial returns the serial of the certificate this authority last issued to a node, unlocked.
// Callers hold the authority lock.
func (l *Local) issuedSerial(nodeID string) (string, bool) {
	issued, err := l.loadIssued()
	if err != nil {
		return "", false
	}
	serial, ok := issued[nodeID]
	return serial, ok
}

func (l *Local) recordIssued(nodeID, serial string) error {
	issued, err := l.loadIssued()
	if err != nil {
		return err
	}
	issued[nodeID] = serial
	return l.writeJSON(IssuedFile, issued)
}

func (l *Local) readJSON(name string, into any) error {
	b, err := os.ReadFile(filepath.Join(l.dir, name))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, into)
}

func (l *Local) writeJSON(name string, from any) error {
	b, err := json.MarshalIndent(from, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(l.dir, name), b, 0o600)
}

// --- custody: where this machine keeps its root and its revocations ------

// fileCustody is cert.Custody over the designated machine's authority directory. The
// root arrives injected (OpenLocal supplies both PEM halves), so LoadRoot/SaveRoot are
// never the path that decides anything here; the revocation half is, and it is written
// at the moment a revocation is made rather than at shutdown.
type fileCustody struct{ dir string }

func (f *fileCustody) LoadRoot() ([]byte, []byte, bool, error) {
	keyPEM, err := os.ReadFile(filepath.Join(f.dir, RootKeyFile))
	if os.IsNotExist(err) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	certPEM, err := os.ReadFile(filepath.Join(f.dir, RootCertFile))
	if err != nil {
		return nil, nil, false, err
	}
	return keyPEM, certPEM, true, nil
}

// SaveRoot writes a freshly generated root, private half first and 0600, and REFUSES to
// replace one that is already here: overwriting a root silently would invalidate every
// certificate on the Edge at once.
func (f *fileCustody) SaveRoot(keyPEM, certPEM []byte) error {
	if _, err := os.Stat(filepath.Join(f.dir, RootKeyFile)); err == nil {
		return ErrRootExists
	}
	if err := os.WriteFile(filepath.Join(f.dir, RootKeyFile), keyPEM, 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(f.dir, RootCertFile), certPEM, 0o600)
}

const revokedFile = "revoked.json"

func (f *fileCustody) LoadRevoked() ([]string, error) {
	b, err := os.ReadFile(filepath.Join(f.dir, revokedFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (f *fileCustody) SaveRevoked(serial string) error {
	got, err := f.LoadRevoked()
	if err != nil {
		return err
	}
	for _, s := range got {
		if s == serial {
			return nil
		}
	}
	b, err := json.MarshalIndent(append(got, serial), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(f.dir, revokedFile), b, 0o600)
}

// parseSerial reads a certificate serial back out of its decimal string form, which is
// how a revocation names one.
func parseSerial(s string) (*big.Int, bool) {
	n, ok := new(big.Int).SetString(s, 10)
	return n, ok
}
