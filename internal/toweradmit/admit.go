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
	"sync"
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
}

// Config bounds the registry.
type Config struct {
	TokenTTL          time.Duration
	LeaseTTL          time.Duration
	MaxTowersPerOwner int
}

type token struct {
	owner   string
	expires time.Time
}

// Registry is Roger Core's record of every joined Tower.
type Registry struct {
	mu     sync.Mutex
	cfg    Config
	tokens map[string]*token
	towers map[string]*Tower
	byKey  map[string]string // identity key hash -> tower id
}

// New builds a registry with sensible floors, so a zero Config is still safe.
func New(cfg Config) *Registry {
	if cfg.TokenTTL <= 0 {
		cfg.TokenTTL = time.Hour
	}
	if cfg.LeaseTTL <= 0 {
		cfg.LeaseTTL = 24 * time.Hour
	}
	if cfg.MaxTowersPerOwner <= 0 {
		cfg.MaxTowersPerOwner = 10
	}
	return &Registry{
		cfg:    cfg,
		tokens: map[string]*token{},
		towers: map[string]*Tower{},
		byKey:  map[string]string{},
	}
}

// IssueToken mints a one-time enrollment token for an account.
func (r *Registry) IssueToken(owner string) (string, error) {
	if owner == "" {
		return "", errors.New("an enrollment token must belong to an account")
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	id, err := randomHex(24)
	if err != nil {
		return "", err
	}
	r.tokens[id] = &token{owner: owner, expires: time.Now().Add(r.cfg.TokenTTL)}
	return id, nil
}

// Enroll admits a Tower, consuming the token.
//
// Every rejection happens BEFORE anything is recorded, so a refused enrollment leaves no
// partial identity for a later attempt to adopt as real.
func (r *Registry) Enroll(tokenID, keyHash string) (Tower, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	tk, ok := r.tokens[tokenID]
	if !ok || time.Now().After(tk.expires) {
		return Tower{}, errors.New("that enrollment token is not valid")
	}
	if keyHash == "" {
		return Tower{}, errors.New("enrollment requires the Tower's identity key")
	}
	// One key, one Tower: otherwise a single machine could hold several admissions and
	// a suspension would stop only one of them.
	if _, exists := r.byKey[keyHash]; exists {
		return Tower{}, errors.New("that identity key is already admitted")
	}
	if r.countOwnerLocked(tk.owner) >= r.cfg.MaxTowersPerOwner {
		return Tower{}, fmt.Errorf("this account already runs %d Towers", r.cfg.MaxTowersPerOwner)
	}

	id, err := randomHex(12)
	if err != nil {
		return Tower{}, err
	}
	now := time.Now()
	tw := &Tower{
		ID: id, Owner: tk.owner, KeyHash: keyHash,
		// Quarantine, always. An account proves who is accountable, not that the Tower
		// behaves - promotion is earned from centrally observed evidence.
		State:        StateQuarantine,
		EnrolledAt:   now,
		LeaseExpires: now.Add(r.cfg.LeaseTTL),
	}
	// Deleting is what makes the token one-time, and it also bounds a map that anyone
	// with an account can otherwise grow without limit.
	delete(r.tokens, tokenID)
	r.towers[id] = tw
	r.byKey[keyHash] = id
	return *tw, nil
}

// Get returns a Tower by id.
func (r *Registry) Get(id string) (Tower, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tw, ok := r.towers[id]
	if !ok {
		return Tower{}, false
	}
	return *tw, true
}

// ByOwner lists an account's Towers.
func (r *Registry) ByOwner(owner string) []Tower {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Tower
	for _, tw := range r.towers {
		if tw.Owner == owner {
			out = append(out, *tw)
		}
	}
	return out
}

// Transition moves a Tower's state. Roger Core is the only caller: nothing here accepts a
// state asserted by the Tower itself.
func (r *Registry) Transition(id string, to State) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	tw, ok := r.towers[id]
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
	return nil
}

// RecordClaim notes that a Tower asserted a state. It never applies it - the claim is
// evidence about the Tower, not information about the network.
func (r *Registry) RecordClaim(id string, claimed State) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tw, ok := r.towers[id]
	if !ok {
		return
	}
	// A value outside the enum is refused, not scored: it is unparseable input, and
	// counting it as evidence would let noise accumulate into a penalty.
	if !Valid(claimed) {
		return
	}
	if tw.State != claimed {
		tw.FalseClaims++
	}
}

// MayTakeWork reports whether this Tower may be given an ordinary public job right now.
// It checks the lease as well as the state: an expired lease takes no new work even while
// the state still reads active, because the lease is what bounds offline drift.
func (r *Registry) MayTakeWork(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	tw, ok := r.towers[id]
	if !ok {
		return false
	}
	if time.Now().After(tw.LeaseExpires) {
		return false
	}
	return EligibleFor(tw.State) == EligibilityEligible
}

// Renew extends a live Tower's lease. A terminal Tower cannot renew its way back.
func (r *Registry) Renew(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	tw, ok := r.towers[id]
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
	return nil
}

// Expire records that a Tower's lease has lapsed, so the registry says what the Tower
// already behaves as instead of reading active forever. It refuses a Tower still inside
// its lease: expiry is an observation, not a lever.
func (r *Registry) Expire(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	tw, ok := r.towers[id]
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
	return nil
}

// countOwnerLocked counts an owner's LIVE Towers. A revoked or expired one stays on
// record - freeing the slot is not forgetting it, and its key stays burned - but it must
// not consume quota forever, or an operator who revokes their Towers is locked out of
// running any.
func (r *Registry) countOwnerLocked(owner string) int {
	n := 0
	for _, tw := range r.towers {
		if tw.Owner == owner && tw.State != StateRevoked && tw.State != StateExpired {
			n++
		}
	}
	return n
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// --- test seams ------------------------------------------------------------

// forceStateForTest sets a state without walking the transition table, so eligibility can
// be checked for every state including ones no legal path reaches from quarantine.
func (r *Registry) forceStateForTest(id string, s State) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	tw, ok := r.towers[id]
	if !ok {
		return errors.New("no such Tower")
	}
	tw.State = s
	return nil
}

// openTokensForTest reports how many enrollment tokens are still held.
func (r *Registry) openTokensForTest() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.tokens)
}
