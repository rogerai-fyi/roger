// Package stationattach is how a Station becomes something Roger Core will believe.
//
// It is the foundation the rest of the Tower network stands on, and it was missing.
// towerinv verifies a leaf against "the key Core recorded at attachment" and inv.Policy
// is asked for that key - but nothing recorded one. Without this package no leaf can ever
// verify, so no inventory can admit anything, so dispatch has nothing to dispatch to.
//
// WHAT AN ATTACHMENT IS. An owner-authorized binding of a Station ID to TWO independent
// keys, under exactly one origin:
//
//   - the ASSERTION key (A) signs the Station's offers. This is the key towerinv checks.
//   - the SECURE-SESSION key (K) terminates the end-to-end channel to the Station. A Tower
//     relays that channel and cannot mint it.
//
// They are separate keys on purpose: a Tower that could speak on the session channel must
// still not be able to sign an offer, and a leaked offer key must not hand over live
// traffic. Presenting one key for both purposes is refused rather than tolerated.
//
// THE FOUR PROPERTIES, and what each one stops:
//
//   - ONE AUTHORIZATION, CONSUMED ONCE, IN THE SAME TRANSACTION AS THE ATTACHMENT. Two
//     processes racing one invitation must produce exactly one origin. A read-then-write
//     would let both win and leave two origins for one Station, which is capacity the
//     operator does not have and a second identity nobody authorized.
//
//   - A LOST RESPONSE IS NOT A SECOND ATTACHMENT. A retry presenting the same authorization
//     and the same keys gets the SAME outcome back, because the caller could not tell a lost
//     reply from a refusal and would otherwise be stuck. A retry presenting the same
//     authorization with DIFFERENT keys is refused: that is not a retry, it is reuse.
//
//   - ORIGIN PRESENCE IS CLOSED. Joined requires exactly one admitted Tower; direct requires
//     the Tower field to be absent. Neither "joined with no Tower" nor "direct with a Tower"
//     is a shape this network has a meaning for, so both are refused before anything is
//     consumed rather than normalised into whichever the reader assumed.
//
//   - ORIGIN KIND IS IMMUTABLE IN V1. A Station admitted direct never becomes joined, or the
//     reverse. Its earnings lineage, capacity and held compensation are bound to that
//     identity; migrating the kind under a stable Station ID would silently move all of it.
//     The old identity must reach terminal revoked state and a NEW Station ID be allocated.
//
// NOTHING IS PARTIALLY COMMITTED. Every refusal below happens before the authorization is
// consumed, so a failed attachment leaves no origin, no binding, and nothing for a caller to
// retry around.
//
// Spec: features/tower/station_attachment.feature.
package attach

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Origin kinds. Standalone Towers are deliberately absent: a standalone Station creates no
// RogerAI authority at all, so it never reaches this package.
const (
	OriginDirect = "direct"
	OriginJoined = "joined"
)

// Attachment lifecycle states. A fresh attachment is ALWAYS quarantine: admission proves
// who a Station is, never that it is any good, and eligibility is decided later by
// Core-observed evidence.
const (
	StateQuarantine = "quarantine"
	StateActive     = "active"
	StateRevoked    = "revoked"
	StateDetached   = "detached"
)

// ErrRejected is every refusal. The reason is wrapped for operators; callers branch on this
// sentinel alone, because a Station learning WHICH check refused it is a probing oracle.
var ErrRejected = errors.New("the attachment was refused")

// ErrUnavailable is a store that could not answer. Distinct from a refusal on purpose: a
// backend blink must never be reported to an operator as "your keys are wrong".
var ErrUnavailable = errors.New("the attachment service is temporarily unavailable")

func reject(cause error) error { return fmt.Errorf("%w: %w", ErrRejected, cause) }

// Origin is where a Station serves from.
type Origin struct {
	Kind    string `json:"kind"`
	TowerID string `json:"tower_id,omitempty"`
}

// check enforces the closed presence rule. Both halves matter: a joined origin with no
// Tower would be routable through nobody, and a direct origin carrying a Tower ID would
// invite a later reader to treat it as joined.
func (o Origin) check() error {
	switch o.Kind {
	case OriginJoined:
		if strings.TrimSpace(o.TowerID) == "" {
			return errors.New("a joined origin needs exactly one admitted Tower")
		}
		return nil
	case OriginDirect:
		if o.TowerID != "" {
			return errors.New("a direct origin must carry no Tower ID")
		}
		return nil
	default:
		return fmt.Errorf("unknown origin kind %q", o.Kind)
	}
}

// Authorization is the one-use invitation an owner obtained for a specific Station and a
// specific pair of keys. It is spent by Admit, in the same transaction that records the
// attachment.
type Authorization struct {
	ID        string
	Network   string
	StationID string
	Owner     string // owner pubkey
	Origin    Origin
	// AssertionKey and SessionKey are the EXACT keys this invitation is for. Attaching with
	// any other key is not the attachment that was authorized.
	AssertionKey string
	SessionKey   string
	CeilingHash  string
	// SecretHash is sha256 of the one-use invitation secret, hex encoded. The plaintext is
	// shown to the operator ONCE at invite and never stored, so a database read cannot hand
	// somebody an attachment they were not given. An authorization with no verifier is
	// unusable rather than open - see validate.
	SecretHash string
	Role       string
	IssuedAt   time.Time
	ExpiresAt  time.Time
	// Consumed and ConsumedBy record the spend. ConsumedBy is the Station ID that resulted,
	// which is what makes a lost-response retry answerable.
	Consumed   bool
	ConsumedBy string
}

// Attachment is what Core records, and what inv.Policy later reads.
type Attachment struct {
	StationID    string
	Owner        string
	AssertionKey string
	SessionKey   string
	Origin       Origin
	// Epoch increments only on a fenced rehome. It is what lets an old origin's in-flight
	// work be refused after the move.
	Epoch       int64
	CeilingHash string
	State       string
	AttachedAt  time.Time
	AuthID      string
}

// Live reports whether this attachment may carry public work at all. Quarantine is live-
// but-not-yet-eligible; revoked and detached are terminal for this Station ID.
func (a Attachment) Live() bool {
	return a.State == StateQuarantine || a.State == StateActive
}

// Proof is what a Station presents. Every field must match the authorization exactly; this
// type exists so the comparison is explicit rather than a pile of arguments.
type Proof struct {
	AuthID string
	// Secret is the one-use invitation material the operator handed over. It proves the
	// presenter was GIVEN this invitation, which possession of the two keys does not: the
	// operator chose those keys at invite time, so anyone who learned them and the
	// authorization id could otherwise attach in the Station's place.
	Secret       string
	Network      string
	StationID    string
	Owner        string
	Origin       Origin
	AssertionKey string
	SessionKey   string
}

// Store is the durable half. Admit MUST consume the authorization and write the attachment
// atomically - see the package doc for why a read-then-write loses the race.
type Store interface {
	// PutAuthorization records a fresh invitation.
	PutAuthorization(a Authorization) error
	// Authorization reads one back.
	Authorization(id string) (Authorization, bool, error)
	// Admit consumes authID and records at, in ONE transaction. It returns false with no
	// error when the authorization was already consumed - the caller then decides whether
	// this is an idempotent retry or divergent reuse.
	Admit(authID string, at Attachment) (bool, error)
	// ByStation, ByAssertionKey and BySessionKey are the uniqueness and lookup reads.
	ByStation(stationID string) (Attachment, bool, error)
	ByAssertionKey(key string) (Attachment, bool, error)
	BySessionKey(key string) (Attachment, bool, error)
	// SetState moves an attachment through its lifecycle.
	SetState(stationID, state string) (bool, error)
}

// NewInvite mints a one-use invitation and returns it alongside the PLAINTEXT secret, which
// is the only time that value exists outside the caller. Store the Authorization; show the
// secret once; never write it down.
func NewInvite(a Authorization, ttl time.Duration, now time.Time) (Authorization, string, error) {
	if err := a.Origin.check(); err != nil {
		return Authorization{}, "", err
	}
	switch {
	case a.ID == "", a.Network == "", a.StationID == "", a.Owner == "":
		return Authorization{}, "", errors.New("an invitation needs an id, a network, a Station and an owner")
	case a.AssertionKey == "" || a.SessionKey == "":
		return Authorization{}, "", errors.New("an invitation names both keys or it names neither")
	case a.AssertionKey == a.SessionKey:
		return Authorization{}, "", errors.New("the assertion and secure-session keys must be different keys")
	case ttl <= 0:
		return Authorization{}, "", errors.New("an invitation needs a positive lifetime")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return Authorization{}, "", err
	}
	secret := hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(secret))
	a.SecretHash = hex.EncodeToString(sum[:])
	a.IssuedAt, a.ExpiresAt = now, now.Add(ttl)
	a.Consumed, a.ConsumedBy = false, ""
	return a, secret, nil
}

// Config bounds the admission.
type Config struct {
	Network string
	// Skew is how far ahead of us an issue time may sit before we call it a forgery.
	Skew time.Duration
	Now  func() time.Time
}

func (c *Config) defaults() {
	if c.Network == "" {
		c.Network = "roger-public"
	}
	if c.Skew <= 0 {
		c.Skew = 60 * time.Second
	}
	if c.Now == nil {
		c.Now = time.Now
	}
}

// Registry admits Stations and answers what Core knows about them.
type Registry struct {
	cfg   Config
	store Store
}

func New(cfg Config, s Store) *Registry {
	cfg.defaults()
	return &Registry{cfg: cfg, store: s}
}

// Admit runs the whole admission. On success the Station is recorded in QUARANTINE.
//
// The ordering below is deliberate: everything that can refuse runs BEFORE the authorization
// is spent, so no refusal leaves a consumed invitation the owner cannot use again.
func (r *Registry) Admit(p Proof) (Attachment, error) {
	now := r.cfg.Now()

	auth, ok, err := r.store.Authorization(p.AuthID)
	if err != nil {
		return Attachment{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if !ok {
		return Attachment{}, reject(errors.New("no such invitation"))
	}

	// A consumed authorization is either the caller retrying after a lost reply, or somebody
	// trying to mint a second identity from one invitation. The difference is whether the
	// proof is IDENTICAL to the one that won.
	if auth.Consumed {
		return r.replay(auth, p)
	}

	if err := r.validate(auth, p, now); err != nil {
		return Attachment{}, err
	}

	// Uniqueness, read before the commit. The commit itself is what settles a race; these
	// give a clear refusal in the ordinary case.
	if err := r.checkBindings(p); err != nil {
		return Attachment{}, err
	}

	at := Attachment{
		StationID:    p.StationID,
		Owner:        p.Owner,
		AssertionKey: p.AssertionKey,
		SessionKey:   p.SessionKey,
		Origin:       p.Origin,
		Epoch:        1,
		CeilingHash:  auth.CeilingHash,
		State:        StateQuarantine,
		AttachedAt:   now,
		AuthID:       auth.ID,
	}

	won, err := r.store.Admit(auth.ID, at)
	if err != nil {
		return Attachment{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if !won {
		// Somebody else consumed it between our read and our write. That is exactly the race
		// this design expects, and the loser answers from the winner's record rather than
		// refusing a caller who did nothing wrong.
		fresh, ok, ferr := r.store.Authorization(auth.ID)
		if ferr != nil {
			return Attachment{}, fmt.Errorf("%w: %v", ErrUnavailable, ferr)
		}
		if !ok {
			return Attachment{}, reject(errors.New("no such invitation"))
		}
		return r.replay(fresh, p)
	}
	return at, nil
}

// replay answers a caller presenting an already-consumed authorization. Identical proof gets
// the committed outcome; anything else is reuse and is refused.
func (r *Registry) replay(auth Authorization, p Proof) (Attachment, error) {
	at, ok, err := r.store.ByStation(auth.ConsumedBy)
	if err != nil {
		return Attachment{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if !ok {
		return Attachment{}, reject(errors.New("this invitation has already been used"))
	}
	same := at.StationID == p.StationID &&
		at.Owner == p.Owner &&
		at.AssertionKey == p.AssertionKey &&
		at.SessionKey == p.SessionKey &&
		at.Origin == p.Origin
	if !same {
		return Attachment{}, reject(errors.New("this invitation has already been used"))
	}
	return at, nil
}

// validate is the refusal table. Every row leaves the invitation unspent.
func (r *Registry) validate(auth Authorization, p Proof, now time.Time) error {
	switch {
	case auth.ExpiresAt.IsZero() || !now.Before(auth.ExpiresAt):
		return reject(errors.New("this invitation has expired"))
	case auth.IssuedAt.After(now.Add(r.cfg.Skew)):
		return reject(errors.New("this invitation is not valid yet"))
	}

	// The network is checked against OUR configuration, not against the two sides agreeing
	// with each other - two peers can agree on the wrong network all day.
	if p.Network != r.cfg.Network || auth.Network != r.cfg.Network {
		return reject(errors.New("this invitation is for another network"))
	}

	if err := p.Origin.check(); err != nil {
		return reject(err)
	}
	if err := auth.Origin.check(); err != nil {
		return reject(err)
	}

	switch {
	case auth.StationID != p.StationID:
		return reject(errors.New("this invitation is for another Station"))
	case auth.Owner != p.Owner:
		return reject(errors.New("this invitation belongs to another owner"))
	case auth.Origin != p.Origin:
		return reject(errors.New("this invitation is for another origin"))
	case auth.AssertionKey != p.AssertionKey:
		return reject(errors.New("the assertion key is not the one this invitation names"))
	case auth.SessionKey != p.SessionKey:
		return reject(errors.New("the secure-session key is not the one this invitation names"))
	}

	// Two purposes, two keys. One key doing both jobs means compromising the offer signer
	// hands over live traffic as well, and there would be no separation left to rotate.
	if p.AssertionKey == "" || p.SessionKey == "" {
		return reject(errors.New("attachment needs both an assertion key and a secure-session key"))
	}
	if p.AssertionKey == p.SessionKey {
		return reject(errors.New("the assertion and secure-session keys must be different keys"))
	}

	// The one-use secret, checked last because it is the most expensive to get wrong: a
	// timing signal here would let somebody walk the verifier a byte at a time. An
	// authorization stored WITHOUT a verifier is unusable rather than open - a row that lost
	// its hash must not become an invitation anyone can redeem.
	if auth.SecretHash == "" {
		return reject(errors.New("this invitation has no verifier and cannot be redeemed"))
	}
	sum := sha256.Sum256([]byte(p.Secret))
	if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(auth.SecretHash)) != 1 {
		return reject(errors.New("the invitation secret does not match"))
	}
	return nil
}

// checkBindings enforces the uniqueness rules and the immutability of origin kind.
func (r *Registry) checkBindings(p Proof) error {
	existing, ok, err := r.store.ByStation(p.StationID)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if ok {
		switch {
		case existing.Origin.Kind != p.Origin.Kind:
			// The whole point of v1's immutability rule. Earnings lineage, capacity and held
			// compensation hang off this identity; letting the kind change would move all of
			// it silently.
			return reject(errors.New(
				"this Station was admitted under a different origin kind, and origin kind cannot change: " +
					"revoke it and attach a new Station ID"))
		case existing.AssertionKey != p.AssertionKey:
			return reject(errors.New("this Station ID is already bound to another assertion key"))
		case !existing.Live():
			return reject(errors.New("this Station ID has been retired and cannot be reattached"))
		}
	}

	// A secure-session key belonging to another Station would let one machine terminate
	// another's end-to-end channel.
	if bound, ok, err := r.store.BySessionKey(p.SessionKey); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	} else if ok && bound.StationID != p.StationID {
		return reject(errors.New("that secure-session key is already bound to another Station"))
	}

	// Likewise an assertion key: two Stations signing offers with one key are one signer
	// wearing two identities.
	if bound, ok, err := r.store.ByAssertionKey(p.AssertionKey); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	} else if ok && bound.StationID != p.StationID {
		return reject(errors.New("that assertion key is already bound to another Station"))
	}
	return nil
}

// Station is the read inv.Policy needs: what Core knows about a Station ID.
func (r *Registry) Station(stationID string) (Attachment, bool, error) {
	at, ok, err := r.store.ByStation(stationID)
	if err != nil {
		return Attachment{}, false, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return at, ok, nil
}

// Revoke retires a Station identity terminally. Terminal is the point: the spec requires a
// cross-kind migration to go through revocation and a NEW Station ID, so a revoked identity
// must never come back.
func (r *Registry) Revoke(stationID string) (bool, error) {
	ok, err := r.store.SetState(stationID, StateRevoked)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return ok, nil
}

// Promote moves a quarantined Station to active. It is deliberately separate from Admit:
// admission proves identity, and only Core-observed evidence earns eligibility.
func (r *Registry) Promote(stationID string) (bool, error) {
	at, ok, err := r.store.ByStation(stationID)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if !ok || at.State != StateQuarantine {
		return false, nil
	}
	moved, err := r.store.SetState(stationID, StateActive)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return moved, nil
}
