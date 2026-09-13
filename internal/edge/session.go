package edge

// SESSIONS - "a node is a thing; a session is a thing happening".
//
// The fleet so far has only nouns. A session is the verb: one exchange that really
// happened between a participant and a station that served it. `roger use`, the TUI's own
// agent, a guest operator, the web console and a board escalating a reading are NOT five
// mechanisms - each is a participant opening a session against a band. They differ in WHO
// INITIATED and WHAT AUTHORITY THEY CARRIED, never in shape, which is why one view holds
// all of them and one receipt already covers them all.
//
// THE RULE THIS FILE EXISTS TO ENFORCE: the Edge session layer is a WINDOW onto traffic
// that was already authorized and already receipted. It has no dispatch, no wallet, no
// grant check and no way to reach a station. Every constructor here takes evidence the
// relay path already produced - the receipts from cmd/rogerai-broker/tunnel.go, including
// the SECOND one a failover writes and the $0 VoidReason one settleVoid records - and
// derives a session from it. Nothing else can make one. That is why looking at the Edge
// cannot cost anything: there is no code path from the view to a relay.
//
// A session is therefore never "pending": it is drawn once its receipt exists. That is the
// literal reading of the approved spec ("everything drawn was already receipted"), and it
// is also the only reading in which the view can never show a turn that turned out not to
// have happened.
//
// Spec: features/edge/sessions.feature. Ground truth: cmd/rogerai-broker/tunnel.go.

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"rogerai.fm/roger/v6/internal/protocol"
)

// Initiator is WHO opened a session. The whole difference between the five.
type Initiator string

const (
	// FromUse is the local OpenAI-shaped endpoint `roger use` serves.
	FromUse Initiator = "use"
	// FromAgent is the TUI's own agent, one session per turn.
	FromAgent Initiator = "agent"
	// FromGuest is a guest operator holding the mic (opencode, hermes, aider) through
	// the local proxy. It carries the guest's own name.
	FromGuest Initiator = "guest"
	// FromConsole is the web console / Playbox, a browser-session relay caller.
	FromConsole Initiator = "console"
	// FromDevice is a node on the Edge - a board that could not name what it saw and
	// escalated. It carries the device's name.
	FromDevice Initiator = "device"
)

// Initiators lists the vocabulary in the design's order.
func Initiators() []Initiator {
	return []Initiator{FromUse, FromAgent, FromGuest, FromConsole, FromDevice}
}

// Valid reports whether i is an initiator at all. An unknown one is never recorded: a
// session the view cannot attribute is a session it must not draw.
func (i Initiator) Valid() bool {
	for _, k := range Initiators() {
		if k == i {
			return true
		}
	}
	return false
}

// NamesItself reports the initiators whose attribution is their OWN name rather than the
// mechanism's: a guest is called what the guest is called, and a device is called what the
// owner named it.
func (i Initiator) NamesItself() bool { return i == FromGuest || i == FromDevice }

// Label is the mechanism's name, for the initiators that have one.
func (i Initiator) Label() string {
	switch i {
	case FromUse:
		return "roger use"
	case FromAgent:
		return "agent"
	case FromConsole:
		return "console"
	}
	return string(i)
}

// Outcome is how a session ended. There are two, because a session is only recorded once
// its receipt exists: it either got served or it got refused.
type Outcome string

const (
	// OutcomeServed: a station answered, and its receipt says which.
	OutcomeServed Outcome = "SERVED"
	// OutcomeRefused: nothing served it, and the $0 receipt says why.
	OutcomeRefused Outcome = "REFUSED"
)

// Refusal reasons the Edge draws. They are VoidReasons in the receipt's OWN vocabulary
// (protocol.Void*): nothing was billed, and the row says why. Kept short because they are
// drawn in a column, and hyphenated to match the reasons the broker already stamps.
const (
	// RefusedNoStation: every station for the band was cooling or absent.
	RefusedNoStation = "no-station"
	// RefusedNoGrant: a device action arrived with no valid grant for it.
	RefusedNoGrant = "no-grant"
	// RefusedOverLimit: the consumer's existing spend limit refused it, exactly as it
	// refuses any relay.
	RefusedOverLimit = "over-limit"
)

// ErrNoReceipt refuses a session the relay path did not receipt. It is the Edge's whole
// admission rule: the view is a window onto receipted traffic, so a session with no
// receipt is not drawn - not dimmed, not pending, not drawn.
var ErrNoReceipt = errors.New("a session with no receipt is not traffic, so nothing is drawn")

// ErrOtherAccount refuses traffic belonging to somebody else. Account scoping is a field
// on the ledger, not an argument a caller can forget - the same construction the Fleet
// uses - so there is no path where one owner's Edge shows another's session.
var ErrOtherAccount = errors.New("that traffic belongs to another account")

// ErrNoSuchSession is returned when the ledger holds no session for that request.
var ErrNoSuchSession = errors.New("no such session on this Edge")

// Session is one exchange, as the receipts describe it.
type Session struct {
	// Request is the relay's request id - the id its receipts bind to. It is the
	// session's identity, because a session IS a request.
	Request string
	Account string
	Kind    Initiator
	// Who is the guest's or the device's own name; empty for the mechanisms that are
	// named after themselves.
	Who string
	// From is the participant the session originated at ("" = this machine).
	From string
	// Via is the relay the path really went through ("" = direct). Drawn as its own hop
	// so the path is visible rather than implied.
	Via string
	// Band is the model that was asked, from the receipt.
	Band string
	// Station is the station that SERVED, from the receipt that settled ("" when
	// nothing served it).
	Station string
	// Left is the station a failover walked away from, from the voided first attempt.
	Left string
	// Escalate marks a device that could not name what it saw. It is a GOOD outcome:
	// the models' strongest measured skill, and the right call.
	Escalate bool
	// Contract is the device's fixed framing, travelling with the escalation because
	// model and prompt ship as one unit.
	Contract Contract
	// Answer is the model's finding. Routing NEVER rewrites it.
	Answer string
	// Route is where the finding goes (log, human review, a policy queue). It is the
	// only thing Route() changes.
	Route   string
	Outcome Outcome
	// Reason is why nothing served it. Empty on a served session.
	Reason string
	// At is when the session was recorded; it is what the fade is measured from.
	At int64
	// Receipts are the receipts it was derived from, in attempt order. A failover
	// leaves two, exactly as the relay writes them.
	Receipts []protocol.UsageReceipt
}

// Attribution is what the session is called on the Edge: the mechanism's name, or the
// guest's / device's own.
func (s Session) Attribution() string {
	if s.Kind.NamesItself() {
		return s.Who
	}
	return s.Kind.Label()
}

// Traffic is one exchange as the relay path already knows it. It is the ONLY way a session
// comes into being, and every field of it is something the relay had anyway.
type Traffic struct {
	Account  string
	Request  string
	Kind     Initiator
	Who      string
	From     string
	Via      string
	Band     string // a fallback only: the band is read from the receipt when it names one
	Escalate bool
	Contract Contract
	Answer   string
	Route    string
	Receipts []protocol.UsageReceipt
}

// SessionLife is how long a session stays visible after it happened. Sessions FADE rather
// than accumulate: a still graph means a quiet fleet, so a session that has stopped being
// news has to stop being drawn.
const SessionLife = 90 * time.Second

// SessionsMax bounds the ledger. A busy relay must not be able to grow the view without
// limit, so the oldest session is dropped rather than the newest refused: the relay never
// waits on, and is never refused by, the thing looking at it.
const SessionsMax = 256

// Sessions is one account's live session ledger: a bounded, self-pruning ring of the
// traffic that account's Edge carried. It holds no connection to anything - it is written
// by the relay path and read by the view.
type Sessions struct {
	mu      sync.Mutex
	account string
	now     func() time.Time
	life    time.Duration
	max     int
	order   []string // request ids, oldest first
	byReq   map[string]Session
}

// NewSessions returns the session ledger for one account.
func NewSessions(account string) *Sessions {
	return &Sessions{
		account: account, now: time.Now, life: SessionLife, max: SessionsMax,
		byReq: map[string]Session{},
	}
}

// SetClock replaces the ledger's clock. Test seam, for the same reason the Fleet has one:
// a session that fades "after a while" must fade because the clock moved.
func (s *Sessions) SetClock(now func() time.Time) { s.mu.Lock(); s.now = now; s.mu.Unlock() }

// SetLife replaces how long a session stays visible.
func (s *Sessions) SetLife(d time.Duration) { s.mu.Lock(); s.life = d; s.mu.Unlock() }

// Account is the owner this ledger belongs to.
func (s *Sessions) Account() string { return s.account }

// ReceiptBelongs reports whether a receipt is one of THIS request's.
//
// The broker's failover names each attempt with attemptID (cmd/rogerai-broker/cooling.go):
// the request itself for the first attempt, then "<request>-2", "<request>-3". So a
// request's receipts are its own id and that id's attempt suffixes, and nothing else -
// which is what stops one session being drawn from another's evidence.
func ReceiptBelongs(rec protocol.UsageReceipt, request string) bool {
	if request == "" {
		return false
	}
	return rec.RequestID == request || strings.HasPrefix(rec.RequestID, request+"-")
}

// Record derives a session from traffic the relay already carried, and returns it.
//
// It is an UPSERT on the request: the relay may record the same request again as its
// evidence grows (a failover's second receipt), and the session is the one it always was
// with more known about it. It creates nothing: with no receipt there is no session.
func (s *Sessions) Record(t Traffic) (Session, error) {
	if t.Account != s.account {
		return Session{}, fmt.Errorf("%w", ErrOtherAccount)
	}
	if t.Request == "" {
		return Session{}, errors.New("a session is a request; this one names none")
	}
	if !t.Kind.Valid() {
		return Session{}, fmt.Errorf("%q is not an initiator", t.Kind)
	}
	if t.Kind.NamesItself() && t.Who == "" {
		return Session{}, fmt.Errorf("a %s session is attributed to its own name, and this one has none", t.Kind)
	}
	if len(t.Receipts) == 0 {
		return Session{}, ErrNoReceipt
	}
	for _, rec := range t.Receipts {
		if !ReceiptBelongs(rec, t.Request) {
			return Session{}, fmt.Errorf("receipt %q is not request %q's: %w", rec.RequestID, t.Request, ErrNoReceipt)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.pruneLocked(now)

	ses := Session{
		Request: t.Request, Account: s.account, Kind: t.Kind, Who: t.Who,
		From: t.From, Via: t.Via, Escalate: t.Escalate, Contract: t.Contract,
		Answer: t.Answer, Route: t.Route, At: now.Unix(),
		Receipts: append([]protocol.UsageReceipt(nil), t.Receipts...),
	}
	ses.Band, ses.Station, ses.Left, ses.Outcome, ses.Reason = deriveFromReceipts(t.Receipts, t.Band)
	if ses.Outcome == OutcomeRefused {
		// A refusal is drawn as what it was. It never carries an answer, because no
		// model produced one.
		ses.Answer = ""
	}
	if prev, ok := s.byReq[t.Request]; ok {
		ses.At = prev.At // the session is the one it always was; only its evidence grew
		if ses.Route == "" {
			ses.Route = prev.Route
		}
	} else {
		s.order = append(s.order, t.Request)
	}
	s.byReq[t.Request] = ses
	s.evictLocked()
	return ses, nil
}

// deriveFromReceipts reads the session out of the evidence, and nowhere else.
//
// The station that served is the one on the last receipt that was NOT voided; the station
// it left is the first voided one, which is exactly what a failover writes. When every
// receipt is a $0 void, nothing served it, and the last void's reason is why.
func deriveFromReceipts(recs []protocol.UsageReceipt, fallbackBand string) (band, station, left string, out Outcome, reason string) {
	band = fallbackBand
	for _, rec := range recs {
		if rec.Model != "" {
			band = rec.Model
		}
	}
	served := -1
	for i, rec := range recs {
		if rec.VoidReason == "" {
			served = i
		}
	}
	if served < 0 {
		return band, "", "", OutcomeRefused, recs[len(recs)-1].VoidReason
	}
	station = recs[served].NodeID
	for _, rec := range recs[:served] {
		if rec.VoidReason != "" && rec.NodeID != "" && rec.NodeID != station {
			left = rec.NodeID
			break
		}
	}
	return band, station, left, OutcomeServed, ""
}

// Route changes WHERE a finding goes, and nothing else.
//
// This is the Wave Mesh rule made structural (features/web/playbox_mesh_workbench.feature):
// response routing happens AFTER the finding and never instead of it, so this function can
// reach exactly one field. There is no path here that can touch Answer.
func (s *Sessions) Route(request, dest string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ses, ok := s.byReq[request]
	if !ok {
		return Session{}, ErrNoSuchSession
	}
	ses.Route = dest
	s.byReq[request] = ses
	return ses, nil
}

// Get returns one session by its request id.
func (s *Sessions) Get(request string) (Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(s.now())
	ses, ok := s.byReq[request]
	return ses, ok
}

// Live is the traffic still within its visible life, newest first. It is a COPY: the view
// draws a snapshot, so a session recorded mid-frame can never produce a frame that is half
// one thing and half another.
func (s *Sessions) Live() []Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.pruneLocked(now)
	out := make([]Session, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.byReq[id])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].At != out[j].At {
			return out[i].At > out[j].At
		}
		return out[i].Request < out[j].Request
	})
	return out
}

// Len is how many sessions are still visible.
func (s *Sessions) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(s.now())
	return len(s.order)
}

// pruneLocked drops everything past its visible life. Sessions fade; they never
// accumulate, and nothing about the fleet changes when one goes.
func (s *Sessions) pruneLocked(now time.Time) {
	cut := now.Add(-s.life).Unix()
	keep := s.order[:0]
	for _, id := range s.order {
		if s.byReq[id].At < cut {
			delete(s.byReq, id)
			continue
		}
		keep = append(keep, id)
	}
	s.order = keep
}

// evictLocked holds the ring to its bound by dropping the OLDEST. The relay is never
// refused and never waits: the view is what gives way.
func (s *Sessions) evictLocked() {
	for len(s.order) > s.max {
		delete(s.byReq, s.order[0])
		s.order = s.order[1:]
	}
}
