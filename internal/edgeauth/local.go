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
	if err := atomicWriteFile(filepath.Join(dir, whereFile), []byte(where), 0o600); err != nil {
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
	l.backfillProtection()
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
	// Verify the SIGNATURE (and freshness) BEFORE any disk read or lock, so an unsigned or stale LAN
	// request is turned away cheaply and cannot use the tombstone as a revocation oracle or make the
	// authority do disk work (audit 2026-09-24).
	if err := r.VerifySignature(); err != nil {
		return Response{}, err
	}
	if d := l.iss.cfg.Now().Sub(time.Unix(r.TS, 0)); d > RequestFreshness || d < -RequestFreshness {
		return Response{}, ErrStaleRequest
	}
	var resp Response
	err := l.withLock(func() error {
		// Check the allow-list FIRST, before any revocation disk work, so the tombstone can never be a
		// revocation oracle for a stranger and every refusal below is the SAME ErrUnknownMachine. Log
		// each REFUSED attempt so the owner's request log answers "did that machine even ask" (a
		// successful enrol is logged by iss.Issue itself) (audit 2026-09-24).
		if _, ok := l.registry()(r.UserKey); !ok {
			l.iss.record(r)
			return ErrUnknownMachine
		}
		// Bind the node id to the key it presents BEFORE reading the revocation list, so an
		// allowed-key holder cannot probe an arbitrary node id's revocation status (a mismatch is
		// refused here, the same as iss.Issue would, before the tombstone is consulted) (audit 2026-09-24).
		pub, err := r.NodePublicKey()
		if err != nil {
			return err
		}
		if NodeID(pub) != r.NodeID {
			l.iss.record(r)
			return ErrKeyMismatch
		}
		// A node whose earlier certificate stands REVOKED must not enroll a fresh one - the enrol
		// path is the account-key-authorised sibling of Claim and needs the same tombstone, or a
		// FORGOTTEN machine (its cert revoked, its account still valid) simply re-enrolls under the
		// same node id and rejoins. The list is read from disk, fails CLOSED. The refusal is
		// ErrUnknownMachine, the SAME as any other "no" - it does not reveal revocation.
		revoked, err := l.revokedNow()
		if err != nil {
			return fmt.Errorf("could not read the revocation list, so no certificate was issued: %w", err)
		}
		serials, err := l.serialsFor(r.NodeID)
		if err != nil {
			return fmt.Errorf("could not read the issued-certificate state, so no certificate was issued: %w", err)
		}
		for _, srl := range serials {
			if revoked[srl] {
				l.iss.record(r)
				return ErrUnknownMachine
			}
		}
		// Mint AND record inside one lock (in-process and cross-process), so a concurrent forget or
		// claim cannot interleave, and record the serial of THIS certificate parsed from its own
		// bytes - never SerialOf, which returns the latest and under a race could be another call's.
		r2, err := l.iss.Issue(r)
		if err != nil {
			return err
		}
		leaf, err := DecodeCert(r2.Cert)
		if err != nil {
			return fmt.Errorf("that certificate could not be read back, so it has NOT been issued: %w", err)
		}
		if err := l.recordIssued(r2.NodeID, leaf.SerialNumber.String()); err != nil {
			return fmt.Errorf("that certificate could not be recorded, so it has NOT been issued: %w", err)
		}
		// Remember WHICH user key enrolled this node, so a later forget can drop that (unprotected)
		// key from the allow-list, de-authorising exactly the forgotten machine.
		if r.UserKey != "" {
			if err := l.recordEnrolledBy(r2.NodeID, r.UserKey); err != nil {
				return fmt.Errorf("that certificate could not be recorded, so it has NOT been issued: %w", err)
			}
		}
		resp = r2
		return nil
	})
	if err != nil {
		return Response{}, err
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
	// Read-modify-write under the authority lock, so a concurrent forget->disallow cannot interleave
	// and resurrect a just-evicted key (or drop this one) - a lost update on the allow-list is a
	// fail-open (audit 2026-09-24).
	return l.withLock(func() error {
		got, err := l.readJSON2Allowed()
		if err != nil {
			return err
		}
		for _, k := range got {
			if k == userKeyHex {
				return nil
			}
		}
		return l.writeJSON(AllowFile, append(got, userKeyHex))
	})
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
		if g.At == 0 || now.Sub(time.Unix(g.At, 0)) > GrantValidity {
			continue // expired, or an un-ageable timestamp-less grant: not a live adoption
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
		// A grant that has stood too long - or carries no timestamp at all (a legacy or hand-edited
		// record we cannot age) - is as good as none: it is refused and the stale record cleared, so
		// it cannot be leaned on again. Treating At==0 as expired is the fail-closed choice.
		if grant.At == 0 || now.Sub(time.Unix(grant.At, 0)) > GrantValidity {
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
		serials, err := l.serialsFor(c.NodeID)
		if err != nil {
			return fmt.Errorf("could not read the issued-certificate state, so no certificate was issued: %w", err)
		}
		for _, s := range serials {
			if revoked[s] {
				return ErrNotAdopted
			}
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

// Forget removes a node from this authority in one locked step: it clears any standing claim grant
// FIRST, then revokes every certificate the node was issued, returning the revoked serials. Doing
// both under a single lock closes the window the audit found, where a claim could mint a fresh
// certificate between a separate Revoke and ConsumeClaim (audit 2026-09-24).
func (l *Local) Forget(nodeID string) ([]string, string, error) {
	var revoked []string
	var evictedKey string
	err := l.withLock(func() error {
		if err := l.consumeClaim(nodeID); err != nil {
			return err
		}
		serials, err := l.serialsFor(nodeID)
		if err != nil {
			return err
		}
		for _, srl := range serials {
			n, ok := parseSerial(srl)
			if !ok {
				return fmt.Errorf("%q is not a certificate serial", srl)
			}
			if err := l.auth.Revoke(n); err != nil {
				return err
			}
			revoked = append(revoked, srl)
		}
		// Drop the node's enrolling user key from the allow-list (unless protected), so a forgotten
		// machine cannot re-enroll under a FRESH node key on the strength of a still-allowed key. The
		// key it evicted (or "") is returned so the caller can tell the owner accurately (audit 2026-09-24).
		evictedKey, err = l.dropEnrolledBy(nodeID)
		return err
	})
	return revoked, evictedKey, err
}

// Revoke ends the certificate this authority issued to a node. It returns the serial it
// revoked so the caller can put it in its own trust store too.
func (l *Local) Revoke(nodeID string) ([]string, error) {
	var revoked []string
	err := l.withLock(func() error {
		serials, err := l.serialsFor(nodeID)
		if err != nil {
			return err
		}
		for _, s := range serials {
			n, ok := parseSerial(s)
			if !ok {
				return fmt.Errorf("%q is not a certificate serial", s)
			}
			if err := l.auth.Revoke(n); err != nil {
				return err
			}
			revoked = append(revoked, s)
		}
		return nil
	})
	return revoked, err
}

// RootPEM is the PUBLIC root this authority signs under - the only half that travels.
func (l *Local) RootPEM() string { return EncodeCert(l.auth.Root()) }

// Revocations is every serial this authority has revoked - what a node refreshes. It is read from
// DISK so a revocation made by a separate process (a CLI forget beside the running authority) is
// distributed to members at once, not only after a restart.
func (l *Local) Revocations() ([]string, error) {
	set, err := l.revokedNow()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	return out, nil
}

func (l *Local) loadIssued() (map[string]string, error) {
	out := map[string]string{}
	err := l.readJSON(IssuedFile, &out)
	return out, err
}

func (l *Local) recordIssued(nodeID, serial string) error {
	issued, err := l.loadIssued()
	if err != nil {
		return err
	}
	issued[nodeID] = serial
	if err := l.writeJSON(IssuedFile, issued); err != nil {
		return err
	}
	// Remember EVERY serial, not just the latest, so a forget revokes them all. A node re-issued
	// while an earlier cert is still in date would otherwise keep that earlier cert working.
	hist, err := l.loadHistory()
	if err != nil {
		return err
	}
	for _, s := range hist[nodeID] {
		if s == serial {
			return nil
		}
	}
	hist[nodeID] = append(hist[nodeID], serial)
	return l.writeJSON(IssuedHistoryFile, hist)
}

// loadHistory reads the per-node record of every serial issued. Unlocked; callers hold the lock.
func (l *Local) loadHistory() (map[string][]string, error) {
	out := map[string][]string{}
	err := l.readJSON(IssuedHistoryFile, &out)
	return out, err
}

// HasIssued reports whether this authority has issued at least one certificate to a node id. It
// returns an error rather than a bare false when the issued state cannot be read, so a caller
// (forget) can fail closed instead of silently reporting "nothing to forget" while a cert is live.
func (l *Local) HasIssued(nodeID string) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	serials, err := l.serialsFor(nodeID)
	if err != nil {
		return false, err
	}
	return len(serials) > 0, nil
}

// loadEnrolledBy reads the node-id -> enrolling-user-key record. Unlocked; callers hold the lock.
func (l *Local) loadEnrolledBy() (map[string]string, error) {
	out := map[string]string{}
	err := l.readJSON(EnrolledByFile, &out)
	return out, err
}

// recordEnrolledBy remembers which user key enrolled a node. Unlocked; callers hold the lock.
func (l *Local) recordEnrolledBy(nodeID, userKey string) error {
	m, err := l.loadEnrolledBy()
	if err != nil {
		return err
	}
	if m[nodeID] == userKey {
		return nil
	}
	m[nodeID] = userKey
	return l.writeJSON(EnrolledByFile, m)
}

// dropEnrolledBy removes a node from the enrolled-by record and drops its (unprotected) enrolling
// user key from the allow-list, so the forgotten machine cannot re-enrol under a fresh node key.
// Keys are per-device, so this de-authorises exactly that machine; the authority's own protected key
// is never evicted. Unlocked; callers hold the lock.
func (l *Local) dropEnrolledBy(nodeID string) (string, error) {
	m, err := l.loadEnrolledBy()
	if err != nil {
		return "", err
	}
	key, had := m[nodeID]
	if !had {
		// No record of which key enrolled this node (it enrolled before this record existed, or it
		// joined by CLAIM which uses no user key). We cannot know which key to evict, so we leave the
		// allow-list untouched; the node's certificate is still revoked. The owner can `roger edge
		// authority` to review allowed keys. A residual, documented rather than guessed (audit 2026-09-24).
		return "", nil
	}
	delete(m, nodeID)
	// A PROTECTED key (the authority's own designation key) is never evicted, and its records are
	// left intact: forgetting an orphan self node id must not lock the authority out of renewing its
	// own certificate, nor erase its current enrolled-by record.
	if key == "" || l.isProtected(key) {
		return "", l.writeJSON(EnrolledByFile, m)
	}
	// Drop stale enrolled-by records that named this (unprotected) key so the record does not grow
	// without bound.
	for other, k := range m {
		if k == key {
			delete(m, other)
		}
	}
	if err := l.writeJSON(EnrolledByFile, m); err != nil {
		return "", err
	}
	// Drop the enrolling key from the allow-list. Keys are per-device (each machine's own login), so
	// this de-authorises exactly the forgotten machine; an orphan record for its prior node id under
	// the same key cannot keep it alive. Existing members keep their certificates until renewal; to
	// admit a machine under this key again the owner re-runs `roger edge authority allow <key>`
	// (audit 2026-09-24).
	if err := l.disallow(key); err != nil {
		return "", err
	}
	return key, nil
}

// backfillProtection protects the designating key for an authority created before protection
// existed: if this machine holds a root and no protected-keys file is present yet, the FIRST allowed
// key (the designating machine's own, added at Designate) is protected. A one-time migration so an
// older local authority cannot evict its own key on a forget (audit 2026-09-24).
func (l *Local) backfillProtection() {
	if l.auth == nil || l.auth.Root() == nil {
		return
	}
	if _, err := os.Stat(filepath.Join(l.dir, ProtectedKeysFile)); err == nil {
		return // already have a protected set
	}
	allowed, err := l.Allowed()
	if err != nil || len(allowed) == 0 {
		return
	}
	_ = l.Protect(allowed[0])
}

// Protect marks a user key as one forget must never evict (the designating machine's own key).
func (l *Local) Protect(userKeyHex string) error {
	return l.withLock(func() error {
		var got []string
		if err := l.readJSON(ProtectedKeysFile, &got); err != nil {
			return err
		}
		for _, k := range got {
			if k == userKeyHex {
				return nil
			}
		}
		return l.writeJSON(ProtectedKeysFile, append(got, userKeyHex))
	})
}

// isProtected reports whether a key must never be evicted. Unlocked; callers hold the lock.
func (l *Local) isProtected(userKeyHex string) bool {
	var got []string
	if err := l.readJSON(ProtectedKeysFile, &got); err != nil {
		return true // fail SAFE: if we cannot tell, do not evict
	}
	for _, k := range got {
		if k == userKeyHex {
			return true
		}
	}
	return false
}

// disallow removes a user key from the allow-list. Unlocked; callers hold the lock.
func (l *Local) disallow(userKeyHex string) error {
	got, err := l.readJSON2Allowed()
	if err != nil {
		return err
	}
	kept := got[:0]
	for _, k := range got {
		if k != userKeyHex {
			kept = append(kept, k)
		}
	}
	return l.writeJSON(AllowFile, kept)
}

// readJSON2Allowed reads the allow-list unlocked (Allowed() takes no lock today, but reading through
// one helper keeps the RMW inside dropEnrolledBy consistent).
func (l *Local) readJSON2Allowed() ([]string, error) {
	var out []string
	err := l.readJSON(AllowFile, &out)
	return out, err
}

// serialsFor is every serial this authority has issued to a node - the history, plus the latest
// from issued.json for a node issued before history was kept. It returns an error rather than an
// empty list when the state cannot be read, so callers can fail CLOSED.
func (l *Local) serialsFor(nodeID string) ([]string, error) {
	hist, err := l.loadHistory()
	if err != nil {
		return nil, err
	}
	issued, err := l.loadIssued()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range hist[nodeID] {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if s, ok := issued[nodeID]; ok && !seen[s] {
		out = append(out, s)
	}
	return out, nil
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
	return atomicWriteFile(filepath.Join(l.dir, name), b, 0o600)
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
	if err := atomicWriteFile(filepath.Join(f.dir, RootKeyFile), keyPEM, 0o600); err != nil {
		return err
	}
	return atomicWriteFile(filepath.Join(f.dir, RootCertFile), certPEM, 0o600)
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
	return atomicWriteFile(filepath.Join(f.dir, revokedFile), b, 0o600)
}

// parseSerial reads a certificate serial back out of its decimal string form, which is
// how a revocation names one.
func parseSerial(s string) (*big.Int, bool) {
	n, ok := new(big.Int).SetString(s, 10)
	return n, ok
}
