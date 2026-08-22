// Package towerlink is the gate on the joined relay link: what a session IS, and what
// nothing gets past.
//
// The approved spec puts the requirement bluntly - when negotiation fails, "no inventory,
// lease, payload, result, or settlement message is accepted". So this package is arranged
// so that failing to negotiate leaves NOTHING to send a message on. There is no half-open
// state, no session-pending, no partially agreed connection: Open either returns a bound
// session or it returns an error.
//
// WHY THE SESSION IS THE UNIT. A session binds four things at once - network, protocol
// version, Tower identity, and a session id we minted - and every later frame must carry
// all four. That single rule is what makes a frame non-transferable: it cannot be lifted
// from one Tower's session into another's, replayed into a later session, or reinterpreted
// under a different protocol version.
//
// WHY LIVENESS LIVES HERE AND NOT IN A DATABASE. Routing asks "is this Tower live?" on
// every request. That has to be a map read. The link being open IS the liveness signal;
// the heartbeat only distinguishes "open" from "open but wedged", and the freshness window
// means a Tower that dies leaves routing on its own without anybody polling it.
package link

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"rogerai.fm/roger/v6/internal/towerhub"
)

// PublicNetwork is the only network a joined Tower may speak on. A standalone Tower mints
// its own network id and has no business here; refusing by name means a standalone
// credential cannot be pointed at us even by accident.
const PublicNetwork = "roger-public"

// The capabilities a joined session MUST carry. Both are integrity properties rather than
// features: without the first we cannot tell a modified frame from an honest one, and
// without the second a Station's traffic would be readable by the Tower carrying it.
const (
	CapIntegrity    = "frame-integrity-v1"
	CapInnerSession = "inner-station-session-v1"
)

// minVersion is the signed protocol floor. A Tower below it is refused rather than
// admitted-and-ignored: admitting software we will not talk to leaves an operator
// convinced they are on the network.
const minVersion = 1

var (
	// ErrNegotiation is every negotiation failure. Uniform because the caller's next move
	// is the same for all of them - there is no session - and because a Tower that is
	// probing learns nothing from the distinction.
	ErrNegotiation = errors.New("the joined session could not be negotiated")

	// ErrNoSession means a frame arrived for a session that is not open, is not this
	// Tower's, or has aged out.
	ErrNoSession = errors.New("that frame does not belong to an open session")
)

// Config bounds the link.
type Config struct {
	Network   string
	Versions  []int
	Heartbeat time.Duration
	// Freshness is how long a session survives without a heartbeat. It is the ONLY thing
	// that removes a silently-dead Tower from routing, so it must comfortably exceed the
	// heartbeat - a single lost frame must not cost an operator their traffic.
	Freshness   time.Duration
	MaxPerTower int
}

// Hello is the Tower's opening frame.
type Hello struct {
	Network      string   `json:"network"`
	Versions     []int    `json:"versions"`
	TowerID      string   `json:"tower_id"`
	Capabilities []string `json:"capabilities"`
	// HeadRevision and HeadHash are what this Tower believes its accepted inventory head
	// is. Carrying them here is what turns a reconnect into ~100 bytes instead of a full
	// snapshot: see Accepted.NeedFullInventory.
	HeadRevision int64  `json:"head_revision,omitempty"`
	HeadHash     string `json:"head_hash,omitempty"`
	// RelayEndpoint is where CONSUMERS reach this Tower's data plane, as host:port. It is
	// how Core learns where to send an edge consumer: the Tower is the only party that
	// knows its own public address, and a Tower that does not relay simply leaves it empty.
	// It is advertised on the link rather than configured on Core because the address is
	// the operator's to change, and a value Core had to be told out of band would go stale
	// the first time an operator moved a box.
	RelayEndpoint string `json:"relay_endpoint,omitempty"`
	// RelayTLSSPKI is the hex sha256 of the SubjectPublicKeyInfo of the certificate this
	// Tower's hub presents, or empty for a hub that serves plaintext. It is what lets a node
	// and a consumer VERIFY the hub they were sent to without a publicly-trusted certificate
	// and without a domain name - see internal/towerhub/pin.go for the whole argument.
	//
	// ADDITIVE, AND THE ENDPOINT FORMAT IS UNTOUCHED. The obvious alternative was to let
	// RelayEndpoint carry a URL, which would have been a breaking change to a field two
	// ingress points parse with net.SplitHostPort and three clients concatenate onto - every
	// one of which would have had to land in the same release as every tower binary in the
	// fleet. A field that is absent on an older Tower, means "plaintext", and therefore means
	// exactly what the system does today is backward compatible by construction.
	//
	// THERE IS NO SEPARATE "does this hub speak TLS" BOOLEAN, deliberately. The pin IS the
	// advertisement, so the state it exists to prevent - a TLS listener whose clients cannot
	// check it - has no representation on the wire.
	RelayTLSSPKI string `json:"relay_tls_spki,omitempty"`
}

// RelayPlane is where a Tower's data plane is, and what will answer there: the two facts a
// party needs before it can dial one, kept together because they are only true together.
//
// They travel as one value rather than as two lookups because of what a MIX of them is. The
// endpoint from one session and the pin from another - a reconnect landing between the two
// calls - is an address paired with the fingerprint of a certificate it will not present,
// which at the client is indistinguishable from the attack the pin exists to detect. It is
// also the shape of the obvious half-done change: read the endpoint, forget the pin, and dial
// plaintext into a TLS listener.
type RelayPlane struct {
	// Endpoint is host:port. Never a URL - see Hello.RelayTLSSPKI.
	Endpoint string
	// TLSSPKI is the hub certificate pin, or empty for plaintext.
	TLSSPKI string
}

// Accepted is what Core replies with.
type Accepted struct {
	Version          int    `json:"version"`
	SessionID        string `json:"session_id"`
	HeartbeatSeconds int    `json:"heartbeat_seconds"`
	FreshnessSeconds int    `json:"freshness_seconds"`
	// NeedFullInventory is true only when Core cannot reconcile the Tower's head with what
	// it accepted. The common reconnect - nothing changed while we redeployed - is false,
	// which is the difference between a fleet returning with a hundred bytes each and a
	// fleet returning with megabytes each at the same instant.
	NeedFullInventory bool `json:"need_full_inventory"`
}

// Frame is the identity every message on the link must carry.
type Frame struct {
	Network   string `json:"network"`
	Version   int    `json:"version"`
	TowerID   string `json:"tower_id"`
	SessionID string `json:"session_id"`
}

type session struct {
	towerID  string
	version  int
	opened   time.Time
	lastSeen time.Time
	// relay is the data plane the Tower advertised in its Hello - address and hub certificate
	// pin - kept so the fleet projection can stamp both onto routable rows.
	relay RelayPlane
}

type head struct {
	revision int64
	hash     string
}

// Sessions is the live set. It is per-process by nature: a Tower holds ONE connection, to
// one instance, so this is not shared state and deliberately not in a store. Which
// instance holds a given Tower is a separate, cheap fact that dispatch needs and liveness
// does not.
type Sessions struct {
	cfg Config

	mu      sync.RWMutex
	byID    map[string]*session // session id -> session
	byTower map[string]string   // tower id -> its ONE live session id
	heads   map[string]head     // tower id -> last accepted inventory head
	offset  time.Duration       // test clock
}

// New builds the session set with sensible floors, so a zero Config is still safe.
func New(cfg Config) *Sessions {
	if cfg.Network == "" {
		cfg.Network = PublicNetwork
	}
	if len(cfg.Versions) == 0 {
		cfg.Versions = []int{1}
	}
	if cfg.Heartbeat <= 0 {
		cfg.Heartbeat = 60 * time.Second
	}
	if cfg.Freshness <= cfg.Heartbeat {
		// A freshness window at or below the heartbeat means one lost frame drops a
		// healthy Tower out of routing.
		cfg.Freshness = 3 * cfg.Heartbeat
	}
	if cfg.MaxPerTower <= 0 {
		cfg.MaxPerTower = 1
	}
	return &Sessions{
		cfg:     cfg,
		byID:    map[string]*session{},
		byTower: map[string]string{},
		heads:   map[string]head{},
	}
}

func (s *Sessions) now() time.Time {
	return time.Now().Add(s.offset)
}

// advance moves the clock. Test-only seam: the alternative is sleeping through real
// freshness windows, which makes the suite slow and flaky.
func (s *Sessions) advance(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.offset += d
}

// Open negotiates a session, or refuses.
//
// certTowerID is the identity the TLS layer already proved - not something the Tower
// asserts. The Hello's claimed Tower ID is checked against it, so a Tower holding a valid
// certificate cannot present itself as a different one.
func (s *Sessions) Open(h Hello, certTowerID string) (Accepted, error) {
	if certTowerID == "" || h.TowerID == "" || h.TowerID != certTowerID {
		return Accepted{}, fmt.Errorf("%w: the session identity does not match the certificate", ErrNegotiation)
	}
	if h.Network != s.cfg.Network {
		// A standalone Tower's own network, or a typo. Either way it is not this network.
		return Accepted{}, fmt.Errorf("%w: that is not the public network", ErrNegotiation)
	}
	if err := checkCapabilities(h.Capabilities); err != nil {
		return Accepted{}, err
	}
	if h.RelayEndpoint != "" {
		// Validated at the door rather than at dispatch: an unparseable endpoint accepted
		// here would surface hours later as consumers failing to connect, attributed to the
		// wrong component.
		if _, _, err := net.SplitHostPort(h.RelayEndpoint); err != nil {
			return Accepted{}, fmt.Errorf("%w: the relay endpoint must be host:port, got %q",
				ErrNegotiation, h.RelayEndpoint)
		}
	}
	// THE PIN IS CHECKED FOR SHAPE AT THE SAME DOOR, AND FOR THE SAME REASON. A malformed
	// fingerprint accepted here would be published into the fleet projection, handed to every
	// node and consumer routed to this Tower, and would surface as each of them refusing to
	// dial - attributed to the tower being down rather than to one bad field. It is also the
	// one error whose fallback would be a silent downgrade to plaintext, which is not a
	// degraded mode but the exact outcome this field exists to prevent.
	if h.RelayTLSSPKI != "" {
		if h.RelayEndpoint == "" {
			// A pin without an address is a Tower saying how to verify a hub it does not
			// advertise. Nothing can ever act on it, so it is a configuration mistake, and a
			// mistake in this particular field is worth naming rather than dropping.
			return Accepted{}, fmt.Errorf("%w: a hub certificate pin was advertised without a "+
				"relay endpoint to reach it at", ErrNegotiation)
		}
		if !towerhub.ValidPin(h.RelayTLSSPKI) {
			return Accepted{}, fmt.Errorf("%w: the hub certificate pin must be %d hex characters "+
				"of sha256 over the certificate's public key, got %q",
				ErrNegotiation, towerhub.PinLen, h.RelayTLSSPKI)
		}
	}
	version, ok := s.bestVersion(h.Versions)
	if !ok {
		return Accepted{}, fmt.Errorf("%w: no mutually supported protocol version", ErrNegotiation)
	}

	id, err := newSessionID()
	if err != nil {
		return Accepted{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, taken := s.byID[id]; taken {
		// We mint these, so a collision is not something a Tower can cause. Refusing is
		// still correct: reusing an id would let an older session's frames be accepted.
		return Accepted{}, fmt.Errorf("%w: session identity collision", ErrNegotiation)
	}
	// One Tower, one session. A reconnect after a blip must not leave the old session
	// alive - its capacity would be counted twice and half its frames would go to a
	// session nobody is reading.
	if prev, exists := s.byTower[h.TowerID]; exists {
		delete(s.byID, prev)
	}
	now := s.now()
	s.byID[id] = &session{towerID: h.TowerID, version: version, opened: now, lastSeen: now,
		relay: RelayPlane{Endpoint: h.RelayEndpoint, TLSSPKI: h.RelayTLSSPKI}}
	s.byTower[h.TowerID] = id

	need := true
	if known, ok := s.heads[h.TowerID]; ok {
		// Reconcilable only on an exact match. A hash that differs, a revision we never
		// accepted, or no head at all all mean resync - we never guess at what a Tower has.
		need = !(known.revision == h.HeadRevision && known.hash == h.HeadHash && h.HeadHash != "")
	}

	return Accepted{
		Version:           version,
		SessionID:         id,
		HeartbeatSeconds:  int(s.cfg.Heartbeat.Seconds()),
		FreshnessSeconds:  int(s.cfg.Freshness.Seconds()),
		NeedFullInventory: need,
	}, nil
}

// RelayPlane reports where a Tower's data plane is reachable and what certificate will answer
// there, from its live session.
//
// From the SESSION rather than a durable record, deliberately: an endpoint is only worth
// routing a consumer to while the Tower behind it is connected and heartbeating, and a
// stored address for a Tower that went away is a timeout handed to a customer.
//
// IT RETURNS BOTH OR NEITHER. This used to be RelayEndpoint, returning the address alone, and
// the pin was added as a value that has to travel with it - see RelayPlane for why a mixture
// of the two is worse than either. Callers that only want the address say `.Endpoint`, which
// is a visible act rather than an omission.
func (s *Sessions) RelayPlane(towerID string) (RelayPlane, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byTower[towerID]
	if !ok {
		return RelayPlane{}, false
	}
	sess, ok := s.byID[id]
	if !ok || sess.relay.Endpoint == "" {
		return RelayPlane{}, false
	}
	return sess.relay, true
}

// Adopt reports whether a session id may be claimed. It exists so a replayed session id is
// refused explicitly rather than by accident.
func (s *Sessions) Adopt(sessionID, towerID string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, exists := s.byID[sessionID]; exists {
		return fmt.Errorf("%w: that session identity is already in use", ErrNegotiation)
	}
	return nil
}

// bestVersion picks the highest version both peers support and that is at or above the
// signed floor. Highest rather than first: a Tower offering an old version alongside a new
// one should get the new one, or an upgrade never takes effect.
func (s *Sessions) bestVersion(offered []int) (int, bool) {
	best, found := 0, false
	for _, o := range offered {
		if o < minVersion {
			continue
		}
		for _, ours := range s.cfg.Versions {
			if o == ours && o > best {
				best, found = o, true
			}
		}
	}
	return best, found
}

// checkCapabilities requires every mandatory capability and refuses anything the Tower
// marks mandatory that we do not know.
func checkCapabilities(offered []string) error {
	have := map[string]bool{}
	for _, c := range offered {
		// A leading "!" marks a capability the Tower says is REQUIRED. One we do not
		// recognise must fail the handshake rather than be ignored: proceeding would mean
		// the peers disagree about what the session guarantees.
		if len(c) > 0 && c[0] == '!' {
			return fmt.Errorf("%w: unknown mandatory capability %q", ErrNegotiation, c)
		}
		have[c] = true
	}
	for _, need := range []string{CapIntegrity, CapInnerSession} {
		if !have[need] {
			return fmt.Errorf("%w: missing mandatory capability %q", ErrNegotiation, need)
		}
	}
	return nil
}

// Check is the gate every later frame passes. It verifies all four bound values, so a
// frame is usable only in the exact session it was made for.
func (s *Sessions) Check(f Frame) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.byID[f.SessionID]
	if !ok {
		return ErrNoSession
	}
	if f.Network != s.cfg.Network || f.Version != sess.version || f.TowerID != sess.towerID {
		return ErrNoSession
	}
	if s.now().Sub(sess.lastSeen) > s.cfg.Freshness {
		return ErrNoSession
	}
	return nil
}

// Heartbeat refreshes a session. It takes the Tower id as well as the session id so a
// heartbeat can only ever refresh its OWN session - the spec's "no heartbeat fabricated by
// another Tower refreshes it", enforced structurally rather than by a separate check.
func (s *Sessions) Heartbeat(sessionID, towerID string) error {
	if sessionID == "" {
		return ErrNoSession
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.byID[sessionID]
	if !ok || sess.towerID != towerID {
		return ErrNoSession
	}
	sess.lastSeen = s.now()
	return nil
}

// Close ends a session deliberately. An operator who drains on purpose leaves routing at
// once rather than waiting out the freshness window.
func (s *Sessions) Close(sessionID, towerID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.byID[sessionID]
	if !ok || sess.towerID != towerID {
		return // a Tower cannot hang up somebody else's link
	}
	delete(s.byID, sessionID)
	if s.byTower[sess.towerID] == sessionID {
		delete(s.byTower, sess.towerID)
	}
}

// Live answers the question routing asks on every request. A map read and nothing else:
// no error, no context, no I/O, by design.
func (s *Sessions) Live(towerID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.byTower[towerID]
	if !ok {
		return false
	}
	sess, ok := s.byID[id]
	return ok && s.now().Sub(sess.lastSeen) <= s.cfg.Freshness
}

// LiveTowers lists every Tower currently eligible to receive work.
func (s *Sessions) LiveTowers() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []string{}
	for tower, id := range s.byTower {
		if sess, ok := s.byID[id]; ok && s.now().Sub(sess.lastSeen) <= s.cfg.Freshness {
			out = append(out, tower)
		}
	}
	return out
}

// Count is how many sessions are held, live or not. Used by the reaper and by tests.
func (s *Sessions) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byID)
}

// RecordHead notes the inventory head Core has accepted for a Tower, so the next reconnect
// can be answered without a snapshot.
func (s *Sessions) RecordHead(towerID string, revision int64, hash string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.heads[towerID] = head{revision: revision, hash: hash}
}

// Reap drops sessions past their freshness window. Without it the map only grows across a
// long uptime with reconnect churn.
func (s *Sessions) Reap() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for id, sess := range s.byID {
		if now.Sub(sess.lastSeen) > s.cfg.Freshness {
			delete(s.byID, id)
			if s.byTower[sess.towerID] == id {
				delete(s.byTower, sess.towerID)
			}
		}
	}
}

func newSessionID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "sess-" + hex.EncodeToString(raw), nil
}
