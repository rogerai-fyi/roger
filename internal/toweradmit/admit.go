// Package toweradmit is Roger Core's admission registry for joined Towers.
//
// Contract: features/tower/public_enrollment.feature.
//
// One idea runs through all of it: Roger Core alone decides a Tower's state. A Tower's
// claim about itself is an input to be checked, never a fact - so no function here takes
// a state from the Tower, and a statement claiming one is recorded as evidence instead of
// applied.
//
// This is Phase 2's foundation. Certificates, dispatch leases and the receipt contract
// all hang off "which Towers exist, who owns them, and what may they do right now", and
// that question is answered here.
package toweradmit

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"time"
)

// State is a joined Tower's lifecycle position. These are exactly the seven the spec
// approves; there is no eighth, and nothing outside this list is representable.
type State string

const (
	// StatePending: enrollment exists but proof or approval is incomplete.
	StatePending State = "pending"
	// StateQuarantine: authenticated, but restricted to probes or bounded beta traffic.
	// Every newly admitted Tower starts here - having an account confers no trust.
	StateQuarantine State = "quarantine"
	// StateActive: eligible for ordinary work within its assigned limits.
	StateActive State = "active"
	// StateDraining: no new jobs; existing leases may finish within their deadlines.
	StateDraining State = "draining"
	// StateSuspended: reversible policy or health exclusion.
	StateSuspended State = "suspended"
	// StateRevoked: credential denied. Terminal.
	StateRevoked State = "revoked"
	// StateExpired: the lease lapsed. Terminal.
	StateExpired State = "expired"
)

// Eligibility is what a state permits.
type Eligibility string

const (
	EligibilityNone       Eligibility = "ineligible"
	EligibilityProbesOnly Eligibility = "probes or bounded beta only"
	EligibilityEligible   Eligibility = "eligible within its limits"
)

// Valid reports whether a value is one of the seven states. A value outside the enum is
// refused outright - never scored, stored, or applied - so an unrecognised state can never
// be mistaken for a permissive one.
func Valid(s State) bool { return len(legalTransitions[s]) > 0 || s == StateRevoked }

// EligibleFor reports what a state permits. Only ACTIVE takes ordinary public work; the
// table is the approved one and deliberately has no default-allow branch.
func EligibleFor(s State) Eligibility {
	switch s {
	case StateActive:
		return EligibilityEligible
	case StateQuarantine:
		return EligibilityProbesOnly
	default:
		return EligibilityNone
	}
}

// legalTransitions is the spec's table, verbatim (public_enrollment.feature). Two edges
// are worth reading twice, because both are easy to get wrong in the obvious direction:
//
//   - suspended does NOT go straight back to active. Clearing a suspension returns a Tower
//     to quarantine, where it must pass fresh probes - otherwise a Tower suspended for a
//     security decision could resume full public traffic on someone's say-so alone.
//   - expired is NOT terminal. A lapsed Tower is re-admitted through quarantine on fresh
//     key proof and fresh probes; what it can never do is activate directly.
//
// Revocation is the single terminal state, and appears here only as a destination.
var legalTransitions = map[State][]State{
	StatePending:    {StateQuarantine, StateExpired, StateRevoked},
	StateQuarantine: {StateActive, StateSuspended, StateExpired, StateRevoked},
	StateActive:     {StateDraining, StateSuspended, StateExpired, StateRevoked},
	StateDraining:   {StateActive, StateSuspended, StateExpired, StateRevoked},
	StateSuspended:  {StateQuarantine, StateExpired, StateRevoked},
	StateExpired:    {StateQuarantine, StateRevoked},
	StateRevoked:    nil,
}

// CanTransition reports whether Roger Core may move a Tower from one state to another.
func CanTransition(from, to State) bool {
	return slices.Contains(legalTransitions[from], to)
}

// Tower is one admitted relay as Roger Core records it.
type Tower struct {
	ID           string
	Owner        string
	KeyHash      string
	State        State
	EnrolledAt   time.Time
	LeaseExpires time.Time
	// FalseClaims counts statements in which the Tower asserted a state it does not
	// hold. Evidence, not a penalty - enforcement is a separate, approved decision.
	FalseClaims int
	// Rev is the revision this record was read at. A write applies only if the stored
	// revision still matches, so two callers acting on one read cannot both win.
	Rev int64

	// --- the rest of the admission bundle ---------------------------------
	//
	// The spec requires the token, lifecycle event, certificate, and lease to "commit
	// atomically or none do". They live on the Tower row rather than beside it precisely
	// so that is one write: separate tables would need a transaction to stay consistent,
	// and a window where they disagree is a window where a Tower holds a certificate the
	// registry has no lease for.

	// TLSKeyHash is the channel key, kept apart from KeyHash (the persistent identity)
	// so rotating a certificate never touches who the Tower IS, and a stolen TLS key
	// proves nothing about its identity.
	TLSKeyHash string
	// LifecycleRevision and LifecycleHash identify the TowerLifecycleEventV1 that admitted
	// this Tower. The lease binds the hash, which is what makes the bundle acyclic:
	// lifecycle first, then the certificate and lease that reference it.
	LifecycleRevision int64
	LifecycleHash     string
	// CertSerial is what revocation names.
	CertSerial string
	// LeaseSequence is the TowerAdmissionLeaseV1 sequence; enrollment issues sequence 1.
	LeaseSequence   int64
	ProtocolVersion int
	Capabilities    []string
	// RenewedAt is when this Tower last had its certificate reissued. It exists to floor
	// how often that may happen: a Tower renewing in a loop would mint unbounded live
	// certificates, each valid to its own expiry and each one a credential to track.
	RenewedAt time.Time
}

// Renewal is what a certificate reissue changes about a Tower. Deliberately narrow: a
// renewal may move the channel key, the serial, and the lease, and nothing else. It may
// never touch the identity, the owner, or the lifecycle state.
//
// It carries no lease deadline. The LEASE and the CERTIFICATE are different grants with
// different lifetimes - the certificate is short because it cannot be recalled, the lease
// is long because it is the thing we can change at any time - so the registry extends the
// lease by its OWN configured term rather than inheriting the certificate's. Deriving one
// from the other also silently loses sub-second renewals, because x509 serialises validity
// to whole seconds.
type Renewal struct {
	CertSerial string
	TLSKeyHash string
	At         time.Time
}

// Config bounds the registry.
type Config struct {
	TokenTTL          time.Duration
	LeaseTTL          time.Duration
	MaxTowersPerOwner int
}

// Registry is Roger Core's record of every joined Tower. It holds no admission state of
// its own: everything lives in the Store, so the record survives the process that wrote it.
// See store.go for why that is not optional.
type Registry struct {
	cfg   Config
	store Store
}

// New builds a registry over the in-process store, with sensible floors so a zero Config
// is still safe.
func New(cfg Config) *Registry { return NewWithStore(cfg, nil) }

// NewWithStore builds a registry over an explicit store.
func NewWithStore(cfg Config, store Store) *Registry {
	if cfg.TokenTTL <= 0 {
		cfg.TokenTTL = time.Hour
	}
	if cfg.LeaseTTL <= 0 {
		cfg.LeaseTTL = 24 * time.Hour
	}
	if cfg.MaxTowersPerOwner <= 0 {
		cfg.MaxTowersPerOwner = 10
	}
	if store == nil {
		store = NewMemStore()
	}
	return &Registry{cfg: cfg, store: store}
}

// unavailable wraps a store failure. Deliberately distinct from every "not admitted"
// answer: "this Tower may not work" and "we cannot currently tell" are different facts,
// and reporting the second as the first turns an outage into a network-wide ban.
func unavailable(err error) error {
	if errors.Is(err, ErrUnavailable) {
		return ErrUnavailable
	}
	return fmt.Errorf("%w: %v", ErrUnavailable, err)
}

// IssueToken mints a one-time enrollment token for an account.
func (r *Registry) IssueToken(owner string) (string, error) {
	if owner == "" {
		return "", errors.New("an enrollment token must belong to an account")
	}
	// Reaping here is what bounds the token space: anyone with an account can mint these,
	// so something has to remove the ones that can no longer be redeemed.
	if err := r.store.ReapTokens(time.Now()); err != nil {
		return "", unavailable(err)
	}
	id, err := randomHex(24)
	if err != nil {
		return "", err
	}
	if err := r.store.PutToken(Token{ID: id, Owner: owner, Expires: time.Now().Add(r.cfg.TokenTTL)}); err != nil {
		// A token we could not record would be refused at redemption, so handing it to an
		// operator is handing them a guaranteed failure later instead of an error now.
		return "", unavailable(err)
	}
	return id, nil
}

// Enroll admits a Tower, consuming the token.
//
// Every rejection happens BEFORE anything is recorded, so a refused enrollment leaves no
// partial identity for a later attempt to adopt as real.
func (r *Registry) Enroll(tokenID, keyHash string) (Tower, error) {
	// Read WITHOUT consuming: a rejected attempt must not burn the token its legitimate
	// holder still needs. The token is spent below, once every check has passed.
	tk, ok, err := r.store.GetToken(tokenID)
	if err != nil {
		return Tower{}, unavailable(err)
	}
	if !ok || time.Now().After(tk.Expires) {
		return Tower{}, errors.New("that enrollment token is not valid")
	}
	if keyHash == "" {
		return Tower{}, errors.New("enrollment requires the Tower's identity key")
	}
	// One key, one Tower: otherwise a single machine could hold several admissions and
	// a suspension would stop only one of them. The record survives revocation, which is
	// what keeps a revoked key BURNED rather than merely currently-refused.
	existing, found, err := r.store.TowerByKey(keyHash)
	if err != nil {
		return Tower{}, unavailable(err)
	}
	if found {
		if existing.State == StateRevoked {
			return Tower{}, errors.New("that identity key has been revoked and cannot be re-enrolled")
		}
		return Tower{}, errors.New("that identity key is already admitted")
	}
	live, err := r.countOwner(tk.Owner)
	if err != nil {
		return Tower{}, unavailable(err)
	}
	if live >= r.cfg.MaxTowersPerOwner {
		return Tower{}, fmt.Errorf("this account already runs %d Towers", r.cfg.MaxTowersPerOwner)
	}

	id, err := randomHex(12)
	if err != nil {
		return Tower{}, err
	}
	now := time.Now()
	tw := Tower{
		ID: id, Owner: tk.Owner, KeyHash: keyHash,
		// Quarantine, always. An account proves who is accountable, not that the Tower
		// behaves - promotion is earned from centrally observed evidence.
		State:        StateQuarantine,
		EnrolledAt:   now,
		LeaseExpires: now.Add(r.cfg.LeaseTTL),
	}
	return r.AdmitBundle(tokenID, tw)
}

// AdmitBundle commits an assembled admission: the token is spent and the Tower recorded in
// ONE transaction, so the bundle the spec describes either happens or does not.
//
// It is exported because enrollment assembles the rest of the bundle - the lifecycle event,
// the certificate, and the lease - before there is anything to commit, and those are not
// this package's job. This is the commit point they all arrive at.
func (r *Registry) AdmitBundle(tokenID string, tw Tower) (Tower, error) {
	admitted, err := r.store.Admit(tokenID, tw)
	if err != nil {
		// A rejected admission rolls back the token with it, so the operator's next
		// attempt is still possible.
		if errors.Is(err, ErrUnavailable) {
			return Tower{}, unavailable(err)
		}
		return Tower{}, err
	}
	if !admitted {
		return Tower{}, errors.New("that enrollment token is not valid")
	}
	return tw, nil
}

// RecordRenewal applies a certificate reissue to a Tower.
//
// It is a CAS like every other state change, so a renewal racing a revocation cannot
// overwrite it - the losing writer is told to re-read rather than silently winning. The
// fields it may change are fixed by the Renewal type: an identity, an owner, or a lifecycle
// state can never be moved by renewing.
func (r *Registry) RecordRenewal(id string, rn Renewal) (Tower, error) {
	tw, ok, err := r.store.TowerByID(id)
	if err != nil {
		return Tower{}, unavailable(err)
	}
	if !ok {
		return Tower{}, errors.New("no such Tower")
	}
	tw.CertSerial = rn.CertSerial
	tw.TLSKeyHash = rn.TLSKeyHash
	tw.RenewedAt = rn.At
	// A connected, healthy Tower should not lose its lease for the crime of staying up, and
	// renewal is the natural moment to carry it forward. Forward only: a renewal must never
	// shorten a lease somebody is already relying on.
	if next := rn.At.Add(r.cfg.LeaseTTL); next.After(tw.LeaseExpires) {
		tw.LeaseExpires = next
	}
	won, err := r.store.CASTower(tw)
	if err != nil {
		return Tower{}, unavailable(err)
	}
	if !won {
		return Tower{}, errors.New("this Tower changed state concurrently; re-read it and retry")
	}
	return tw, nil
}

// ForceLeaseExpiryForTest lapses a lease without waiting for the clock, so the expiry path
// can be exercised.
func (r *Registry) ForceLeaseExpiryForTest(id string) error {
	tw, ok, err := r.store.TowerByID(id)
	if err != nil || !ok {
		return errors.New("no such Tower")
	}
	tw.LeaseExpires = time.Now().Add(-time.Second)
	if _, err := r.store.CASTower(tw); err != nil {
		return err
	}
	return nil
}

// Token reads an unspent enrollment token without consuming it, so a caller can check who
// it belongs to and whether it is live before doing the work an admission needs.
func (r *Registry) Token(id string) (Token, bool, error) {
	tok, ok, err := r.store.GetToken(id)
	if err != nil {
		return Token{}, false, unavailable(err)
	}
	return tok, ok, nil
}

// Get returns a Tower by id.
func (r *Registry) Get(id string) (Tower, bool) {
	tw, ok, err := r.store.TowerByID(id)
	if err != nil {
		return Tower{}, false
	}
	return tw, ok
}

// ByOwner lists an account's Towers.
func (r *Registry) ByOwner(owner string) []Tower {
	out, err := r.store.TowersByOwner(owner)
	if err != nil {
		return nil
	}
	return out
}

// Transition moves a Tower's state. Roger Core is the only caller: nothing here accepts a
// state asserted by the Tower itself.
func (r *Registry) Transition(id string, to State) error {
	tw, ok, err := r.store.TowerByID(id)
	if err != nil {
		return unavailable(err)
	}
	if !ok {
		return errors.New("no such Tower")
	}
	if !Valid(to) {
		return fmt.Errorf("%q is not a Tower state", to)
	}
	if !CanTransition(tw.State, to) {
		return fmt.Errorf("a %s Tower cannot become %s", tw.State, to)
	}
	// Re-admission restarts the lease. Without this, a Tower cleared back into quarantine
	// would be lapsed the instant it returned and could never be promoted.
	if to == StateQuarantine {
		tw.LeaseExpires = time.Now().Add(r.cfg.LeaseTTL)
	}
	tw.State = to
	won, err := r.store.CASTower(tw)
	if err != nil {
		return unavailable(err)
	}
	if !won {
		// Somebody else moved this Tower between our read and our write. Theirs stands;
		// applying ours on top would silently overwrite a decision made from a state we
		// never saw - and one of those decisions is revocation.
		return errors.New("this Tower changed state concurrently; re-read it and retry")
	}
	return nil
}

// RecordClaim notes that a Tower asserted a state. It never applies it - the claim is
// evidence about the Tower, not information about the network.
func (r *Registry) RecordClaim(id string, claimed State) {
	tw, ok, err := r.store.TowerByID(id)
	if err != nil || !ok {
		return
	}
	// A value outside the enum is refused, not scored: it is unparseable input, and
	// counting it as evidence would let noise accumulate into a penalty.
	if !Valid(claimed) {
		return
	}
	if tw.State == claimed {
		return
	}
	tw.FalseClaims++
	// Best-effort by design: a lost increment under contention under-counts evidence,
	// which is the safe direction. Over-counting would be a penalty somebody did not earn.
	_, _ = r.store.CASTower(tw)
}

// MayTakeWork reports whether this Tower may be given an ordinary public job right now.
// It checks the lease as well as the state: an expired lease takes no new work even while
// the state still reads active, because the lease is what bounds offline drift.
//
// An unreadable registry grants NOTHING. Failing closed is the only safe direction: the
// alternative is that a registry outage hands work to Towers nobody can currently vouch
// for, including ones that are revoked.
func (r *Registry) MayTakeWork(id string) bool {
	tw, ok, err := r.store.TowerByID(id)
	if err != nil || !ok {
		return false
	}
	if time.Now().After(tw.LeaseExpires) {
		return false
	}
	return EligibleFor(tw.State) == EligibilityEligible
}

// Renew extends a live Tower's lease. A terminal Tower cannot renew its way back.
func (r *Registry) Renew(id string) error {
	tw, ok, err := r.store.TowerByID(id)
	if err != nil {
		return unavailable(err)
	}
	if !ok {
		return errors.New("no such Tower")
	}
	if tw.State == StateRevoked || tw.State == StateExpired {
		return fmt.Errorf("a %s Tower cannot renew", tw.State)
	}
	// A lapsed lease is re-admitted through quarantine, on fresh key proof and fresh
	// probes. Renewing one would route around that control entirely.
	if time.Now().After(tw.LeaseExpires) {
		return errors.New("this Tower's lease has lapsed; it must be re-admitted, not renewed")
	}
	tw.LeaseExpires = time.Now().Add(r.cfg.LeaseTTL)
	won, err := r.store.CASTower(tw)
	if err != nil {
		return unavailable(err)
	}
	if !won {
		return errors.New("this Tower changed state concurrently; re-read it and retry")
	}
	return nil
}

// Expire records that a Tower's lease has lapsed, so the registry says what the Tower
// already behaves as instead of reading active forever. It refuses a Tower still inside
// its lease: expiry is an observation, not a lever.
func (r *Registry) Expire(id string) error {
	tw, ok, err := r.store.TowerByID(id)
	if err != nil {
		return unavailable(err)
	}
	if !ok {
		return errors.New("no such Tower")
	}
	if !time.Now().After(tw.LeaseExpires) {
		return errors.New("this Tower's lease has not lapsed")
	}
	if !CanTransition(tw.State, StateExpired) {
		return fmt.Errorf("a %s Tower cannot expire", tw.State)
	}
	tw.State = StateExpired
	won, err := r.store.CASTower(tw)
	if err != nil {
		return unavailable(err)
	}
	if !won {
		return errors.New("this Tower changed state concurrently; re-read it and retry")
	}
	return nil
}

// countOwner counts an owner's LIVE Towers. A revoked or expired one stays on record -
// freeing the slot is not forgetting it, and its key stays burned - but it must not consume
// quota forever, or an operator who revokes their Towers is locked out of running any.
func (r *Registry) countOwner(owner string) (int, error) {
	towers, err := r.store.TowersByOwner(owner)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, tw := range towers {
		if tw.State != StateRevoked && tw.State != StateExpired {
			n++
		}
	}
	return n, nil
}

// --- test seams ------------------------------------------------------------

// forceStateForTest sets a state without walking the transition table, so eligibility can
// be checked for every state including ones no legal path reaches from quarantine.
func (r *Registry) forceStateForTest(id string, s State) error {
	tw, ok, err := r.store.TowerByID(id)
	if err != nil || !ok {
		return errors.New("no such Tower")
	}
	tw.State = s
	if _, err := r.store.CASTower(tw); err != nil {
		return err
	}
	return nil
}

// openTokensForTest reports how many enrollment tokens are still held.
func (r *Registry) openTokensForTest() int {
	m, ok := r.store.(*memStore)
	if !ok {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.tokens)
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
