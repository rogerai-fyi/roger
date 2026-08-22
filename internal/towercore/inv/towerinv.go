// Package towerinv is what a joined Tower is allowed to tell Roger Core about the Stations
// behind it, and - far more importantly - what Core refuses to believe.
//
// A Tower is a relay we do not control. It is already admitted, so the threat here is not
// an impostor: it is an operator who wants more traffic, better prices, or capacity they do
// not have. Everything in this package is arranged around one sentence from the approved
// spec: the Tower TRANSPORTS offers, it does not MAKE them. A leaf is only ever evidence
// signed by the Station itself, relayed by a Tower that signed the collection - and even a
// perfectly signed leaf is worth nothing until Core's own registry agrees.
//
// THE FOUR PROPERTIES, and what each one stops:
//
//   - ATOMICITY. A revision is accepted whole or not at all. Half-applying a rejected
//     inventory is how a Tower gets to smuggle one leaf past a check by attaching it to a
//     revision that fails somewhere else. On rejection the previously accepted revision
//     stays authoritative until its own expiry - we do not fall back to nothing, because a
//     malformed push would then be a way to blank a competitor's fleet.
//
//   - TWO INDEPENDENT SIGNATURES, neither of which substitutes for the other. The Station
//     signs its offer; the Tower signs the collection. The Tower's signature says "these
//     leaves are the ones I am relaying" - it does NOT make any claim inside a leaf true.
//     An invalid leaf is dropped and the rest of the revision stands, because punishing a
//     whole fleet for one bad Station is a denial-of-service an attacker would use.
//
//   - A HASH CHAIN. Each revision names the exact prior head it follows. This is what makes
//     deltas safe: without it, a Tower could replay an old delta, or apply one to a base we
//     never accepted, and our view would silently diverge from theirs. Anything ambiguous
//     costs a full resync rather than a guess - see delta.go.
//
//   - EXPIRY CARRIED BY THE OBJECT. Nothing here polls. An inventory says how long it is
//     good for, and when that passes its leaves leave routing on their own. That is the
//     whole answer to "a Tower disconnects and its leaves take work forever": we do not
//     need to notice the disconnect, and no other Tower can refresh it, because refreshing
//     means producing a newer revision signed by THAT Tower's key.
//
// THE SCHEMA IS CLOSED. Unknown members are refused, in the inventory and in every leaf.
// That is a security decision, not tidiness: the spec requires that operator-declared
// geography or hardware is never labeled measured, and the cheapest way to keep an
// unverifiable claim from being presented as fact is to leave it nowhere to ride. If a
// field is not in the list below, a Tower cannot send it at all.
//
// WHAT THIS PACKAGE DOES NOT DECIDE. Bans, owners, revoked keys, allowed models, and price
// bands are central state, and central state is the caller's - see Policy. towerinv does
// the cryptography, the structure, the sequencing and the arithmetic; it ASKS about
// everything it cannot prove for itself. Keeping that seam sharp is what lets the whole
// rejection table be tested without a database.
package inv

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"rogerai.fm/roger/v6/internal/towerobj"
)

// The object types and version this package speaks. They are part of the signing domain, so
// a signature over an inventory can never be replayed as a delta or as an offer.
const (
	TypeInventory = "tower.inventory"
	TypeDelta     = "tower.inventory.delta"
	TypeOffer     = "station.offer"

	// Version is the object version, distinct from the link protocol version.
	Version = 1

	// PublicNetwork is the only network a joined Tower may speak on.
	PublicNetwork = "roger-public"

	sigMember   = "sig"
	offerSigMbr = "station_sig"
)

// ErrRejected means the revision is refused in full. The caller keeps serving whatever it
// had; the Tower may push a corrected revision.
var ErrRejected = errors.New("the inventory revision was rejected")

// ErrResync means we cannot tell where this Tower's sequence is, so the delta is discarded
// and a full snapshot must be requested. It is deliberately separate from ErrRejected: one
// is "you sent something wrong", the other is "we are out of step" - and the second is
// recoverable without anybody being at fault.
var ErrResync = errors.New("a full inventory snapshot is required")

// errIdentity marks the failures that are an authorization problem rather than a
// sequencing one - wrong network, wrong Tower. A delta forgives almost everything with a
// resync, but not these: asking a Tower to resend an inventory it was never entitled to
// relay would be answering an attack with a retry.
var errIdentity = errors.New("channel identity")

// Registration is what Core already knows about a Station, from its own records. Not one
// field here comes from the Tower; that is the point of the type.
type Registration struct {
	// Known is false for a Station ID Core has no attachment record for. A Tower may not
	// introduce a Station by asserting one exists.
	Known bool
	// Key is the signing key Core recorded at attachment. A leaf must verify against THIS
	// key, never against a key the leaf carries - otherwise "signed by the Station" means
	// only "signed by whoever wrote the leaf".
	Key ed25519.PublicKey
	// Banned, KeyRevoked, OwnerPresent and OwnerSuspended are the central states that
	// override a cryptographically perfect offer.
	Banned         bool
	KeyRevoked     bool
	OwnerPresent   bool
	OwnerSuspended bool
	// Quarantined means the Station is attached and verifiable but has NOT yet earned
	// eligibility. Admission proves who a Station is; it never proves it is any good, and
	// the spec requires a freshly attached Station to be quarantine inventory until central
	// probes and policy say otherwise. Without this, admission and eligibility collapse into
	// one step and anyone who can attach is immediately carrying customer traffic.
	Quarantined bool
	// Unavailable means Core could not READ its own state for this Station - a ban list it
	// could not load, an account it could not resolve. The leaf is refused either way, but
	// the reason must not be one of the others: reporting "not registered" would send an
	// operator off to re-attach a Station that is fine, and reporting "banned" would accuse
	// them of something that did not happen. Found by the first end-to-end test of the
	// attachment -> policy -> inventory chain, where an unreadable ban set surfaced as
	// "Station ID is not consistent with any registered key".
	Unavailable bool
}

// Policy is the central authority towerinv consults for everything it cannot verify with
// mathematics. Implementations read Core's own registry and price tables; none of them may
// consult the Tower.
type Policy interface {
	// Station returns what Core knows about the Station ID, from Core's records.
	Station(stationID string) Registration
	// ModelAllowed reports whether the model may be offered on the public network at all.
	ModelAllowed(model string) bool
	// ModalityAllowed reports the same for a modality.
	ModalityAllowed(modality string) bool
	// PriceBand is the public floor and ceiling for a model, in the same units the offer
	// quotes, applied to the input and output consumer rates alike. ok=false means the
	// model has no public band, which is not routable at any price.
	PriceBand(model string) (floor, ceiling int64, ok bool)
}

// Config bounds what a Tower may push. Every ceiling here is a signed policy value in the
// design so one operator can be raised without a release.
type Config struct {
	Network string
	// Skew is how far ahead of us an issued time may be before we call it a forgery rather
	// than a slow clock.
	Skew time.Duration
	// MaxLifetime caps how far out an expiry may sit. An inventory that never expires is an
	// inventory that survives the Tower going dark, which is the exact failure the expiry
	// exists to prevent.
	MaxLifetime time.Duration
	// MaxLeaves is the per-Tower leaf ceiling (10,000 in the approved design).
	MaxLeaves int
	// MaxCapabilities caps the number of DISTINCT capability strings a Tower may advertise
	// across one inventory. Capabilities widen what a leaf may be selected for, so an
	// unbounded set is an unbounded claim surface.
	MaxCapabilities int
	// MaxBytes caps the encoded revision. Measured at ~538 bytes a leaf, the ceiling
	// snapshot is ~5.4 MB; the default leaves real headroom without letting one Tower push
	// an arbitrary amount of memory into every instance.
	MaxBytes int

	// Now is the clock, injectable so expiry and skew are testable without sleeping.
	Now func() time.Time
	// RecordHead is called with every accepted revision. towerlink hands the head back to
	// the Tower on reconnect, which is what turns a returning fleet into a hundred bytes
	// each instead of a full snapshot each. Optional.
	RecordHead func(towerID string, revision int64, hash string)
}

func (c *Config) applyDefaults() {
	if c.Network == "" {
		c.Network = PublicNetwork
	}
	if c.Skew <= 0 {
		c.Skew = 60 * time.Second
	}
	if c.MaxLifetime <= 0 {
		c.MaxLifetime = time.Hour
	}
	if c.MaxLeaves <= 0 {
		c.MaxLeaves = 10000
	}
	if c.MaxCapabilities <= 0 {
		c.MaxCapabilities = 256
	}
	if c.MaxBytes <= 0 {
		c.MaxBytes = 8 << 20
	}
	if c.Now == nil {
		c.Now = time.Now
	}
}

// Leaf is one admitted Station offer. It carries the exact bytes the Station signed, not a
// re-rendering of them: routing quotes and settlement must be able to point at the object
// that was actually signed, and anything we re-encode is a second encoding that can drift.
type Leaf struct {
	TowerID   string
	StationID string
	OfferID   string
	Model     string
	Modality  string

	// PriceIn and PriceOut are what the consumer pays; EarnIn and EarnOut are what the
	// Station is paid. The second may never exceed the first - a Station earning more than
	// the consumer is charged is money out of Core's pocket on every token.
	PriceIn  int64
	PriceOut int64
	EarnIn   int64
	EarnOut  int64

	Capacity     int64
	Capabilities []string
	Expires      time.Time

	// Offer is the canonical Station-signed object, and OfferHash binds that exact object.
	Offer     []byte
	OfferHash string
}

// Exclusion records a leaf that was dropped and why, so an operator can see which of their
// Stations is not earning without us having to accept it to tell them.
type Exclusion struct {
	StationID string
	OfferID   string
	Reason    string
}

// Result describes what an accepted revision did.
type Result struct {
	Revision int64
	Hash     string
	Routable int
	Excluded []Exclusion
	// Full is true when this revision replaced the whole set rather than amending it.
	Full bool
}

// state is one Tower's accepted view.
type state struct {
	revision int64
	hash     string
	expires  time.Time
	// byOffer is keyed on offer ID, which the rejection table guarantees is unique within a
	// revision. stations is the parallel uniqueness set for Station IDs.
	byOffer map[string]Leaf
}

func (s *state) clone() *state {
	c := &state{revision: s.revision, hash: s.hash, expires: s.expires, byOffer: make(map[string]Leaf, len(s.byOffer))}
	for k, v := range s.byOffer {
		c.byOffer[k] = v
	}
	return c
}

// Set holds the accepted inventory for every Tower attached to this instance.
//
// It is in-process on purpose, exactly like link.Sessions: a Tower holds one
// connection to one instance, so this is not shared state. The durable part is only the
// head revision and hash, which is what RecordHead is for - the body is large, changes
// often, and is fully reconstructible by asking the Tower to resync.
type Set struct {
	cfg    Config
	policy Policy

	mu     sync.RWMutex
	towers map[string]*state
	// origins is the "one Station, one active origin" index: Station ID -> the Tower
	// currently relaying it. Without it, the same Station advertised behind two Towers
	// would be counted twice and dispatched to concurrently, which multiplies capacity an
	// operator does not have and is the cheapest way to oversell a fleet. First origin
	// holds it; moving a Station between Towers is the separate fenced rehome flow in
	// station_attachment, not something an inventory push may do on its own.
	origins map[string]string
}

// New builds a Set. A zero Config is safe; every bound has a floor.
func New(cfg Config, p Policy) *Set {
	cfg.applyDefaults()
	return &Set{cfg: cfg, policy: p, towers: map[string]*state{}, origins: map[string]string{}}
}

// Head reports the accepted revision and hash for a Tower, which is what a reconnect
// compares against.
func (s *Set) Head(towerID string) (int64, string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.towers[towerID]
	if !ok {
		return 0, "", false
	}
	return st.revision, st.hash, true
}

// Forget drops a Tower's inventory outright. Used on revocation, where waiting for expiry
// is too slow.
func (s *Set) Forget(towerID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.towers, towerID)
	s.releaseOrigins(towerID)
}

// releaseOrigins drops every Station claim held by a Tower. Called before reindexing an
// accepted revision, so a Station the operator removed stops blocking its own rehoming.
func (s *Set) releaseOrigins(towerID string) {
	for station, holder := range s.origins {
		if holder == towerID {
			delete(s.origins, station)
		}
	}
}

// ReleaseStation drops one Station's origin claim, without disturbing any Tower's accepted
// chain.
//
// Retiring a Station must not cost its siblings a full resync, which is what forgetting the
// whole Tower would do. The leaf itself stops being routable through policy - the attachment
// is revoked, so the next revision refuses it - and this releases only the one-origin claim
// so the Station can attach somewhere else.
//
// HOW LONG THE SAVING LASTS, stated honestly: the revision and hash are deliberately left
// alone, so this instance's leaf set now differs from what the Tower believes we hold. The
// chain hash is over the delta bytes, not over the leaf set, so nothing detects that
// divergence - until the Tower next sends a remove or replace naming this offer, which finds
// it absent and forces a resync. The siblings are spared everything up to that point, which
// is the common case, but this is not a permanent economy.
func (s *Set) ReleaseStation(stationID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.origins, stationID)
	for _, st := range s.towers {
		for offerID, leaf := range st.byOffer {
			if leaf.StationID == stationID {
				delete(st.byOffer, offerID)
			}
		}
	}
}

// Routable is the eligibility snapshot routing takes. Past the accepted revision's expiry
// it is empty - the revision stays recorded, because its head is still what a delta must
// chain from, but nothing behind it receives new work.
func (s *Set) Routable(towerID string) []Leaf {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.towers[towerID]
	if !ok || !s.cfg.Now().Before(st.expires) {
		return nil
	}
	out := make([]Leaf, 0, len(st.byOffer))
	for _, l := range st.byOffer {
		if s.cfg.Now().Before(l.Expires) {
			out = append(out, l)
		}
	}
	return out
}

// AcceptFull validates and installs a complete signed inventory revision.
//
// towerKey is the key the CHANNEL authenticated, from the certificate - not anything the
// object carries. channelTowerID likewise. A signature that verifies against a key the
// message supplied proves only that the message is self-consistent.
func (s *Set) AcceptFull(channelTowerID string, towerKey ed25519.PublicKey, raw []byte) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	inv, err := s.openSigned(channelTowerID, towerKey, TypeInventory, raw,
		"network", "tower_id", "revision", "prev_hash", "lease_head", "lifecycle_head",
		"issued", "expires", "leaves", sigMember)
	if err != nil {
		return Result{}, reject(err)
	}

	revision, err := s.fullSequence(inv, channelTowerID)
	if err != nil {
		return Result{}, err
	}
	expires, err := s.window(inv)
	if err != nil {
		return Result{}, err
	}

	// Both heads are required: they are how a later lease or lifecycle decision proves it
	// was made against the same view of the Tower the inventory was built under.
	if _, err := inv.str("lease_head"); err != nil {
		return Result{}, reject(err)
	}
	if _, err := inv.str("lifecycle_head"); err != nil {
		return Result{}, reject(err)
	}

	rawLeaves, err := inv.list("leaves")
	if err != nil {
		return Result{}, reject(err)
	}
	if len(rawLeaves) > s.cfg.MaxLeaves {
		return Result{}, reject(fmt.Errorf("%d leaves is above the ceiling of %d", len(rawLeaves), s.cfg.MaxLeaves))
	}

	next := &state{byOffer: make(map[string]Leaf, len(rawLeaves))}
	// stations and offers record what was SUBMITTED, not what was admitted. Checking
	// uniqueness against the admitted set instead would let a duplicate ride in behind an
	// excluded leaf: the first occurrence never lands, so the second looks unique, and a
	// revision with two claims on one offer ID is accepted.
	stations := map[string]bool{}
	offers := map[string]bool{}
	caps := map[string]bool{}
	var excluded []Exclusion

	for i, rl := range rawLeaves {
		lo, ok := rl.(map[string]any)
		if !ok {
			return Result{}, reject(fmt.Errorf("leaf %d is not an object", i))
		}
		// Identity and uniqueness are inventory-level: a duplicate Station or offer makes
		// the whole revision ambiguous, so it cannot be handled by dropping one of them.
		ident, err := leafIdentity(lo)
		if err != nil {
			return Result{}, reject(fmt.Errorf("leaf %d: %w", i, err))
		}
		if stations[ident.stationID] {
			return Result{}, reject(fmt.Errorf("Station %s appears twice", ident.stationID))
		}
		if offers[ident.offerID] {
			return Result{}, reject(fmt.Errorf("offer %s appears twice", ident.offerID))
		}
		stations[ident.stationID], offers[ident.offerID] = true, true

		leaf, why := s.admitLeaf(channelTowerID, lo)
		if why != "" {
			excluded = append(excluded, Exclusion{StationID: ident.stationID, OfferID: ident.offerID, Reason: why})
			continue
		}
		for _, c := range leaf.Capabilities {
			caps[c] = true
		}
		next.byOffer[leaf.OfferID] = leaf
	}

	if len(caps) > s.cfg.MaxCapabilities {
		return Result{}, reject(fmt.Errorf("%d distinct capabilities is above the limit of %d", len(caps), s.cfg.MaxCapabilities))
	}

	hash, err := towerobj.Hash(raw)
	if err != nil {
		return Result{}, reject(err)
	}
	next.revision, next.hash, next.expires = revision, hash, expires
	s.install(channelTowerID, next)

	return Result{Revision: revision, Hash: hash, Routable: len(next.byOffer), Excluded: excluded, Full: true}, nil
}

// install commits a validated revision and publishes the head. Every caller reaches this
// only after all validation, which is what makes acceptance atomic.
func (s *Set) install(towerID string, next *state) {
	s.towers[towerID] = next
	s.releaseOrigins(towerID)
	for _, l := range next.byOffer {
		s.origins[l.StationID] = towerID
	}
	if s.cfg.RecordHead != nil {
		s.cfg.RecordHead(towerID, next.revision, next.hash)
	}
}

// openSigned performs the checks every signed Tower object shares: canonical bytes, the
// closed schema, the network, the channel identity, and the Tower's signature.
//
// It returns BARE causes, never ErrRejected/ErrResync. Which of those a failure means is
// the caller's decision - a snapshot rejects, a delta usually resyncs - and an error that
// arrived pre-wrapped would satisfy errors.Is for both sentinels at once, which makes the
// distinction the package rests on unaskable.
func (s *Set) openSigned(channelTowerID string, towerKey ed25519.PublicKey, objType string, raw []byte, allowed ...string) (obj, error) {
	if len(raw) > s.cfg.MaxBytes {
		return nil, fmt.Errorf("%d encoded bytes is above the limit of %d", len(raw), s.cfg.MaxBytes)
	}
	o, err := canonicalObject(raw)
	if err != nil {
		return nil, err
	}
	if err := o.closed(allowed...); err != nil {
		return nil, err
	}
	network, err := o.str("network")
	if err != nil {
		return nil, err
	}
	if network != s.cfg.Network {
		return nil, fmt.Errorf("%w: network %q is not %q", errIdentity, network, s.cfg.Network)
	}
	towerID, err := o.str("tower_id")
	if err != nil {
		return nil, err
	}
	// The object must name the Tower whose certificate opened this channel. Without this a
	// Tower could relay another Tower's inventory and inherit its fleet.
	if towerID != channelTowerID {
		return nil, fmt.Errorf("%w: tower_id %q is not the channel identity %q", errIdentity, towerID, channelTowerID)
	}
	if err := towerobj.Verify(towerKey, s.cfg.Network, objType, Version, raw, sigMember); err != nil {
		return nil, fmt.Errorf("Tower signature: %w", err)
	}
	return o, nil
}

// revisionNumber reads and bounds a revision. Called by both paths so the sequence limits
// cannot drift apart between them.
func (o obj) revisionNumber(name string) (int64, error) {
	revision, err := o.integer(name)
	if err != nil {
		return 0, err
	}
	if revision <= 0 {
		return 0, fmt.Errorf("%s %d is not positive", name, revision)
	}
	// A revision with no possible successor is the end of the sequence. Accepting it would
	// leave the Tower unable to ever push again except by resetting, and a reset is exactly
	// the ambiguity the chain exists to remove.
	if revision == math.MaxInt64 {
		return 0, fmt.Errorf("%s overflows the sequence", name)
	}
	return revision, nil
}

// fullSequence places a full snapshot in the Tower's sequence.
//
// With no accepted revision there is no history to protect, so the chain is taken on faith
// for exactly one object: a cold start, or the first snapshot after a resync, cannot be
// checked against something we never saw. Everything after it is chained.
func (s *Set) fullSequence(o obj, towerID string) (int64, error) {
	revision, err := o.revisionNumber("revision")
	if err != nil {
		return 0, reject(err)
	}
	// prev_hash is required on EVERY snapshot, including the first. The schema is closed
	// and complete: a member that is only sometimes required is a member a Tower can drop,
	// and "we could not check it this time" must not become "it need not be there".
	prev, err := o.str("prev_hash")
	if err != nil {
		return 0, reject(err)
	}
	prior, havePrior := s.towers[towerID]
	if !havePrior {
		return revision, nil
	}
	switch {
	case revision <= prior.revision:
		return 0, reject(fmt.Errorf("revision %d does not advance on the accepted %d", revision, prior.revision))
	case revision != prior.revision+1:
		return 0, reject(fmt.Errorf("revision %d skips %d", revision, prior.revision+1))
	}
	if prev != prior.hash {
		return 0, reject(errors.New("prev_hash is not the accepted head"))
	}
	return revision, nil
}

// window checks the object's own time bounds against our clock. These are the same for a
// snapshot and a delta: an expired object is refused outright either way, because asking a
// Tower to resend something it dated wrong would just repeat the mistake.
func (s *Set) window(o obj) (time.Time, error) {
	issued, err := o.integer("issued")
	if err != nil {
		return time.Time{}, reject(err)
	}
	expiresUnix, err := o.integer("expires")
	if err != nil {
		return time.Time{}, reject(err)
	}
	now := s.cfg.Now()
	issuedAt, expires := time.Unix(issued, 0), time.Unix(expiresUnix, 0)
	if issuedAt.After(now.Add(s.cfg.Skew)) {
		return time.Time{}, reject(errors.New("issued in the future beyond the allowed skew"))
	}
	if !now.Before(expires) {
		return time.Time{}, reject(errors.New("the inventory is already expired"))
	}
	if expires.After(now.Add(s.cfg.MaxLifetime)) {
		return time.Time{}, reject(fmt.Errorf("expiry is beyond the allowed lease of %s", s.cfg.MaxLifetime))
	}
	return expires, nil
}

// reject and resync keep the cause IN THE CHAIN rather than flattening it to text. The
// delta path has to be able to ask whether a failure was an identity fault, and a cause
// formatted with %v cannot be asked anything.
func reject(cause error) error {
	return fmt.Errorf("%w: %w", ErrRejected, cause)
}

func resync(cause error) error {
	return fmt.Errorf("%w: %w", ErrResync, cause)
}

// --- strict field access ----------------------------------------------------
//
// Everything below reads a value that towerobj has already proven canonical, so the only
// remaining question is whether the field is present and the right shape. Integers arrive
// as bounded base-10 strings; there are no JSON numbers anywhere in this format.

type obj map[string]any

// canonicalObject requires the bytes to be EXACTLY canonical, not merely parseable. Two
// encodings of the same object hash differently, and the hash is what the chain binds, so
// "close enough" here would break the chain for a peer that re-encoded correctly.
func canonicalObject(raw []byte) (obj, error) {
	c, err := towerobj.Canonical(raw)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(c, raw) {
		return nil, errors.New("the object is not in canonical form")
	}
	var m map[string]any
	if err := json.Unmarshal(c, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// closed refuses any member not in the allowed list.
func (o obj) closed(allowed ...string) error {
	ok := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		ok[a] = true
	}
	for k := range o {
		if !ok[k] {
			return fmt.Errorf("unknown member %q", k)
		}
	}
	return nil
}

func (o obj) str(name string) (string, error) {
	v, ok := o[name]
	if !ok {
		return "", fmt.Errorf("missing required member %q", name)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("member %q is not a string", name)
	}
	if s == "" {
		return "", fmt.Errorf("member %q is empty", name)
	}
	return s, nil
}

func (o obj) integer(name string) (int64, error) {
	s, err := o.str(name)
	if err != nil {
		return 0, err
	}
	n, err := towerobj.ParseInt(s)
	if err != nil {
		return 0, fmt.Errorf("member %q: %w", name, err)
	}
	return n, nil
}

func (o obj) list(name string) ([]any, error) {
	v, ok := o[name]
	if !ok {
		return nil, fmt.Errorf("missing required member %q", name)
	}
	l, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("member %q is not an array", name)
	}
	return l, nil
}

func (o obj) strings(name string) ([]string, error) {
	l, err := o.list(name)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(l))
	for i, v := range l {
		s, ok := v.(string)
		if !ok || s == "" {
			return nil, fmt.Errorf("member %q element %d is not a non-empty string", name, i)
		}
		out = append(out, s)
	}
	return out, nil
}
