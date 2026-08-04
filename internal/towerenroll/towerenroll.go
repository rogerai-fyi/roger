// Package towerenroll admits a joined Tower to the public network.
//
// It is the point where a machine nobody has vouched for becomes a named Tower holding a
// credential, so the whole package is organised around one requirement from the spec: an
// invalid enrollment "fails without creating partial authority". No certificate, no lease,
// no directory entry, and nothing a later attempt could adopt as real.
//
// THE ORDER OF CHECKS IS THE DESIGN. Everything that can reject runs BEFORE anything is
// written, and the single write at the end is the atomic admission bundle
// (toweradmit.AdmitBundle). There is deliberately no path that half-succeeds.
//
// WHAT PROVES WHAT. The token proves an operator was approved to run a Tower. The challenge
// signature proves the machine holds the identity key it claims - not merely that it knows
// a public key, which is public. The CSR proves it also holds a SEPARATE channel key. The
// three are independent: holding any one of them is not enough.
package towerenroll

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"rogerai.fm/roger/v5/internal/keypurpose"
	"rogerai.fm/roger/v5/internal/toweradmit"
	"rogerai.fm/roger/v5/internal/towercert"
)

// defaultChallengeTTL bounds how long a challenge may go unanswered. Short, because its
// only job is to prove the machine is answering NOW - a long-lived challenge is a
// long-lived opportunity to answer one somebody else collected.
const defaultChallengeTTL = 5 * time.Minute

var (
	errTermsNotAccepted  = errors.New("this account has not accepted the current Tower terms")
	errOperatorSuspended = errors.New("this account is suspended")

	// errRejected is what every invalid enrollment returns to the caller. Uniform on
	// purpose: the reason is recorded for us, not handed to whoever is probing.
	errRejected = errors.New("that enrollment is not valid")
)

// OperatorPolicy answers whether an account may enroll a Tower at all. Terms acceptance,
// suspension, and standing are the account system's knowledge, not this package's.
type OperatorPolicy interface {
	MayEnroll(owner string) error
}

// Config wires the enroller.
type Config struct {
	Registry  *toweradmit.Registry
	Authority *towercert.Authority
	Policy    OperatorPolicy
	// MinVersion and MaxVersion bound the protocol an admitted Tower may speak. A Tower
	// below the floor is refused rather than admitted-and-ignored: admitting software we
	// will not talk to leaves an operator convinced they are on the network.
	MinVersion, MaxVersion int
	// MaxSkew is how far a Tower's clock may differ from ours. It bounds how stale a
	// replayed request can be.
	MaxSkew time.Duration
	// ChallengeTTL bounds how long a challenge may go unanswered.
	ChallengeTTL time.Duration
}

// Challenge is the nonce a Tower must sign to prove it holds its identity key.
type Challenge struct {
	Nonce   string
	TokenID string
	Expires time.Time
}

// SigningInput is exactly what the Tower signs.
//
// It binds the nonce to the TOKEN, so a challenge collected for one enrollment cannot be
// answered for another - without that, an eavesdropper could pair a captured signature
// with their own token.
func (c Challenge) SigningInput() []byte {
	return []byte("rogerai-tower-enroll-v1\x00" + c.TokenID + "\x00" + c.Nonce)
}

// Request is one enrollment attempt.
type Request struct {
	// Operator is the ACCOUNT the broker authenticated for this call. The token alone is a
	// bearer credential: if it leaks - from a log, a shoulder, a shared terminal - anybody
	// holding it could otherwise enroll a Tower onto somebody else's account and be paid
	// for it. Requiring the session too means a leaked token is not, by itself, enough.
	Operator string
	TokenID  string
	// TransactionID makes a retry recognisable as a retry. A lost response must not cost
	// the operator their token.
	TransactionID   string
	Nonce           string
	IdentityKey     ed25519.PublicKey
	Signature       []byte
	CSR             []byte // DER, carrying the SEPARATE channel key
	ProtocolVersion int
	Realm           keypurpose.Realm
	Capabilities    []string
	// Now is the Tower's clock, checked against ours.
	Now time.Time
}

// Result is a completed admission.
type Result struct {
	TowerID     string
	Tower       toweradmit.Tower
	Certificate *x509.Certificate
}

// Enroller admits Towers.
type Enroller struct {
	cfg Config

	mu sync.Mutex
	// challenges are one-time. A signature stays valid forever, so what stops a replay is
	// the nonce being spendable once.
	challenges map[string]Challenge
	// committed remembers completed enrollments by transaction, so a retry after a lost
	// response returns the SAME identity instead of failing as a consumed token.
	committed map[string]Result
}

// New builds an enroller. Every dependency is required: enrollment without a registry
// admits nothing, without an authority issues nothing, and without a policy cannot know
// who is allowed to enroll at all.
func New(cfg Config) (*Enroller, error) {
	switch {
	case cfg.Registry == nil:
		return nil, errors.New("enrollment needs the admission registry")
	case cfg.Authority == nil:
		return nil, errors.New("enrollment needs the certificate authority")
	case cfg.Policy == nil:
		return nil, errors.New("enrollment needs an operator policy")
	}
	if cfg.MaxVersion < cfg.MinVersion {
		return nil, errors.New("the protocol version range is inverted")
	}
	if cfg.MaxSkew <= 0 {
		cfg.MaxSkew = 5 * time.Minute
	}
	if cfg.ChallengeTTL <= 0 {
		cfg.ChallengeTTL = defaultChallengeTTL
	}
	return &Enroller{
		cfg:        cfg,
		challenges: map[string]Challenge{},
		committed:  map[string]Result{},
	}, nil
}

// Challenge issues a fresh nonce for a live enrollment token.
//
// It requires the token to exist FIRST, so an unauthenticated caller cannot use this to
// mint challenges or to discover which tokens are live - the refusal is the same either way.
func (e *Enroller) Challenge(tokenID string) (Challenge, error) {
	if tokenID == "" {
		return Challenge{}, errRejected
	}
	if _, ok, err := e.cfg.Registry.Token(tokenID); err != nil || !ok {
		return Challenge{}, errRejected
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return Challenge{}, err
	}
	ch := Challenge{
		Nonce:   hex.EncodeToString(raw),
		TokenID: tokenID,
		Expires: time.Now().Add(e.cfg.ChallengeTTL),
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.reapLocked(time.Now())
	e.challenges[ch.Nonce] = ch
	return ch, nil
}

func (e *Enroller) reapLocked(now time.Time) {
	for nonce, ch := range e.challenges {
		if now.After(ch.Expires) {
			delete(e.challenges, nonce)
		}
	}
}

// Enroll admits a Tower, or refuses without leaving anything behind.
func (e *Enroller) Enroll(req Request) (Result, error) {
	if req.TransactionID == "" {
		return Result{}, fmt.Errorf("%w: an enrollment needs a transaction id", errRejected)
	}

	// A retry of something already committed returns the original outcome. It re-proves
	// the identity key first: a transaction id observed on the wire must not become a way
	// to have somebody else's Tower re-issued to your key.
	if done, ok := e.completed(req.TransactionID); ok {
		if subtle.ConstantTimeCompare(hashKey(req.IdentityKey), []byte(done.Tower.KeyHash)) != 1 {
			return Result{}, errRejected
		}
		return done, nil
	}

	tw, cert, err := e.validateAndAdmit(req)
	if err != nil {
		return Result{}, err
	}
	res := Result{TowerID: tw.ID, Tower: tw, Certificate: cert}

	e.mu.Lock()
	e.committed[req.TransactionID] = res
	e.mu.Unlock()
	return res, nil
}

func (e *Enroller) completed(txn string) (Result, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.committed[txn]
	return r, ok
}

// validateAndAdmit runs every rejection before the single write at the end.
func (e *Enroller) validateAndAdmit(req Request) (toweradmit.Tower, *x509.Certificate, error) {
	fail := func(reason string) (toweradmit.Tower, *x509.Certificate, error) {
		// The reason is for us. It deliberately never carries the token, the nonce, or any
		// key material - an error string travels into logs and support tickets.
		return toweradmit.Tower{}, nil, fmt.Errorf("%w: %s", errRejected, reason)
	}

	// --- the material the Tower presents ---------------------------------
	if len(req.IdentityKey) != ed25519.PublicKeySize {
		return fail("no usable identity key")
	}
	if req.Realm != keypurpose.RealmTower {
		// Material issued under one trust root carries no authority under another. A
		// standalone Tower, a Station, and Roger Core itself are all foreign here.
		return fail("that identity belongs to another network")
	}
	if req.ProtocolVersion < e.cfg.MinVersion || req.ProtocolVersion > e.cfg.MaxVersion {
		return fail("unsupported protocol version")
	}
	for _, c := range req.Capabilities {
		if c == "" {
			return fail("malformed capability request")
		}
	}
	now := time.Now()
	if req.Now.IsZero() || absDuration(now.Sub(req.Now)) > e.cfg.MaxSkew {
		return fail("clock outside the admitted skew")
	}

	// --- the challenge ----------------------------------------------------
	//
	// Spent here, before the signature is even checked. A nonce that is only spent on
	// SUCCESS lets an attacker probe the same challenge repeatedly.
	ch, live := e.spendChallenge(req.Nonce, now)
	if !live {
		return fail("unknown, spent, or expired challenge")
	}
	if ch.TokenID != req.TokenID {
		return fail("that challenge was issued for another enrollment")
	}
	if len(req.Signature) != ed25519.SignatureSize ||
		!ed25519.Verify(req.IdentityKey, ch.SigningInput(), req.Signature) {
		return fail("the challenge signature does not verify")
	}

	// --- the token and its operator ---------------------------------------
	tok, ok, err := e.cfg.Registry.Token(req.TokenID)
	if err != nil {
		return toweradmit.Tower{}, nil, err
	}
	if !ok {
		return fail("that enrollment token is not valid")
	}
	if now.After(tok.Expires) {
		return fail("that enrollment token has expired")
	}
	if req.Operator == "" || subtle.ConstantTimeCompare([]byte(req.Operator), []byte(tok.Owner)) != 1 {
		// The token belongs to somebody else. This is the check that makes a leaked token
		// useless on its own.
		return fail("that enrollment token was issued to another account")
	}
	if err := e.cfg.Policy.MayEnroll(tok.Owner); err != nil {
		return fail("this account may not enroll a Tower")
	}

	// --- the channel key ---------------------------------------------------
	csr, err := x509.ParseCertificateRequest(req.CSR)
	if err != nil {
		return fail("the certificate request could not be read")
	}
	if err := csr.CheckSignature(); err != nil {
		// Proves the requester holds the channel key, not merely a copy of its public half.
		return fail("the certificate request is not signed by its own key")
	}
	if sameKey(csr.PublicKey, req.IdentityKey) {
		// The spec initialises a joined Tower with DISTINCT identity and TLS keys. One key
		// doing both means rotating the certificate rotates the Tower's identity, and a
		// stolen channel key becomes proof of who the Tower is.
		return fail("the channel key must differ from the identity key")
	}

	// --- the bundle --------------------------------------------------------
	//
	// Assembled in the order the spec requires and commits acyclically: the lifecycle event
	// first, then the certificate and the lease that bind its hash.
	towerID, err := newTowerID()
	if err != nil {
		return toweradmit.Tower{}, nil, err
	}
	identityHash := string(hashKey(req.IdentityKey))
	tlsHash, err := hashCSRKey(csr.PublicKey)
	if err != nil {
		return fail("that channel key is unusable")
	}
	lifecycleHash := lifecycleEventHash(towerID, tok.Owner, identityHash, now)

	cert, err := e.cfg.Authority.Issue(towerID, csr.PublicKey)
	if err != nil {
		return toweradmit.Tower{}, nil, err
	}

	tw := toweradmit.Tower{
		ID: towerID, Owner: tok.Owner,
		KeyHash: identityHash, TLSKeyHash: tlsHash,
		// Quarantine, always: an account proves who is accountable, not that the Tower
		// behaves. Promotion is earned from centrally observed evidence.
		State:             toweradmit.StateQuarantine,
		EnrolledAt:        now,
		LeaseExpires:      cert.NotAfter,
		LifecycleRevision: 1,
		LifecycleHash:     lifecycleHash,
		CertSerial:        cert.SerialNumber.String(),
		LeaseSequence:     1,
		ProtocolVersion:   req.ProtocolVersion,
		Capabilities:      req.Capabilities,
	}

	// The one write. It consumes the token and records the Tower together, so a failure
	// here leaves the operator's token usable and no partial identity behind. The
	// certificate above was only ever in memory until this succeeds.
	admitted, err := e.cfg.Registry.AdmitBundle(req.TokenID, tw)
	if err != nil {
		return toweradmit.Tower{}, nil, err
	}
	return admitted, cert, nil
}

// spendChallenge takes a nonce out of circulation and reports whether it was live.
func (e *Enroller) spendChallenge(nonce string, now time.Time) (Challenge, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	ch, ok := e.challenges[nonce]
	if !ok {
		return Challenge{}, false
	}
	delete(e.challenges, nonce)
	if now.After(ch.Expires) {
		return Challenge{}, false
	}
	return ch, true
}

func hashKey(pub ed25519.PublicKey) []byte {
	sum := sha256.Sum256(pub)
	return []byte(hex.EncodeToString(sum[:]))
}

func hashCSRKey(pub crypto.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:]), nil
}

func sameKey(csrKey crypto.PublicKey, identity ed25519.PublicKey) bool {
	other, ok := csrKey.(ed25519.PublicKey)
	return ok && other.Equal(identity)
}

// lifecycleEventHash identifies the revision-1 pending-to-quarantine event this admission
// commits. The lease binds it, which is what makes the bundle acyclic rather than a set of
// records that merely happen to agree.
func lifecycleEventHash(towerID, owner, identityHash string, at time.Time) string {
	sum := sha256.Sum256([]byte("TowerLifecycleEventV1\x00rev=1\x00pending->quarantine\x00" +
		towerID + "\x00" + owner + "\x00" + identityHash + "\x00" +
		at.UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(sum[:])
}

func newTowerID() (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "tw-" + hex.EncodeToString(raw), nil
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
