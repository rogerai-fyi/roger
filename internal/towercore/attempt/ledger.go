package attempt

// ledger.go is the writer: the only thing that may move an attempt, and the only thing that
// may append to its chain.

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"time"

	"rogerai.fm/roger/v6/internal/towerobj"
)

// Record is one committed event as a store holds it.
type Record struct {
	AttemptID string
	Revision  int64
	EventID   string
	State     string
	Kind      string
	Hold      string
	Hash      string
	Signed    []byte
	// Spec is the attempt's immutable authority, carried so a successor restates it exactly
	// rather than being handed it again by a caller who might differ.
	Spec IssueSpec
}

// Store is the durable chain. Every write is a compare-and-swap on the revision.
type Store interface {
	// Append commits one event IF the attempt is currently at expectPrev.
	//
	// expectPrev is 0 for the issuing event, which must be the FIRST: an attempt that already
	// exists cannot be issued again, and the store is where that is decided rather than in a
	// read the caller did a moment earlier.
	Append(rec Record, expectPrev int64) error
	// Head returns the latest committed event for an attempt.
	Head(attemptID string) (Record, bool, error)
	// At returns the event committed at one revision, for the idempotent-replay check.
	At(attemptID string, revision int64) (Record, bool, error)
}

// Config is how a ledger is built.
type Config struct {
	Network string
	// Signer is the PURPOSE-SEPARATED attempt-state key. The spec calls for its own service;
	// at minimum it is its own key, so a compromise elsewhere cannot forge attempt state.
	Signer ed25519.PrivateKey
	Now    func() time.Time
	// Sequence assigns the independently-assigned Core ordering. Independent of the caller
	// on purpose: "key validity, compromise cutoff, deadlines, and event ordering derive from
	// the independently assigned commit tuple, never signer issue time."
	//
	// IT MUST BE SAFE FOR CONCURRENT USE, and must never return the same value twice. The
	// ledger calls it from whichever goroutine is committing, and two attempts handed the
	// same ordering position are two attempts nothing downstream can put in order - which is
	// the one thing a global sequence exists to prevent. A plain `n++` in a closure looks
	// harmless and is not; the race detector found exactly that in this package's own tests.
	Sequence func() int64
}

// Ledger commits attempt state.
type Ledger struct {
	cfg   Config
	store Store
}

func New(cfg Config, store Store) *Ledger {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if store == nil {
		store = NewMemStore()
	}
	return &Ledger{cfg: cfg, store: store}
}

func (l *Ledger) now() time.Time { return l.cfg.Now() }

func (l *Ledger) seq() int64 {
	if l.cfg.Sequence == nil {
		return l.now().UnixNano()
	}
	return l.cfg.Sequence()
}

// Issue creates an attempt: revision 1, state issued, with its disclosure-safe commitment.
//
// ONE COMMIT, or neither object. "failure before the transaction commits creates no attempt
// or hold" - so the commitment is built first and both are written by a single Append. A
// commitment that existed without its event would be a promise of an attempt that was never
// recorded, which is exactly what a Tower would present as proof it was authorized.
func (l *Ledger) Issue(s IssueSpec) (Commitment, Event, error) {
	if s.Network == "" {
		s.Network = l.cfg.Network
	}
	if s.Network != l.cfg.Network {
		return Commitment{}, Event{}, errors.New("that attempt is for another network")
	}
	if s.AttemptID == "" || s.JobID == "" {
		return Commitment{}, Event{}, errors.New("an attempt needs a job and an attempt id")
	}
	if s.GrantHash == "" {
		return Commitment{}, Event{}, errors.New("an attempt is issued under a grant")
	}
	if s.CommitTime.IsZero() {
		s.CommitTime = l.now()
	}
	if s.Sequence == 0 {
		s.Sequence = l.seq()
	}

	commitmentID, err := CommitmentID(s.Network, s.AttemptID)
	if err != nil {
		return Commitment{}, Event{}, err
	}
	cobj, err := buildCommitment(s, commitmentID)
	if err != nil {
		return Commitment{}, Event{}, err
	}
	commitmentSigned, err := l.sign(cobj, TypeCommitment)
	if err != nil {
		return Commitment{}, Event{}, err
	}

	eventID, err := EventID(s.Network, s.AttemptID, 1)
	if err != nil {
		return Commitment{}, Event{}, err
	}
	eobj, err := buildEvent(s, eventFields{
		id: eventID, revision: 1, kind: KindIssued, state: StateIssued, hold: HoldReserved,
		commitmentID: commitmentID, commitTime: s.CommitTime, sequence: s.Sequence,
	})
	if err != nil {
		return Commitment{}, Event{}, err
	}
	eventSigned, err := l.sign(eobj, TypeEvent)
	if err != nil {
		return Commitment{}, Event{}, err
	}
	hash, err := towerobj.Hash(eventSigned)
	if err != nil {
		return Commitment{}, Event{}, err
	}

	rec := Record{
		AttemptID: s.AttemptID, Revision: 1, EventID: eventID, State: StateIssued,
		Kind: string(KindIssued), Hold: string(HoldReserved), Hash: hash,
		Signed: eventSigned, Spec: s,
	}
	if err := l.store.Append(rec, 0); err != nil {
		return Commitment{}, Event{}, err
	}
	return Commitment{ID: commitmentID, Signed: commitmentSigned},
		Event{
			ID: eventID, Revision: 1, State: StateIssued, Kind: KindIssued,
			Hold: HoldReserved, Hash: hash, Signed: eventSigned,
		}, nil
}

// Observation is the evidence Core accepted, and what it concluded from it.
type Observation struct {
	Kind Kind
	// EvidenceHash binds the exact thing observed - a receipt, a lease acceptance, a sweep's
	// own signed decision. Required after issue: a state change on the strength of nothing
	// would be Core asserting rather than observing.
	EvidenceHash string
	// Reason is required on a terminal state and refused otherwise.
	Reason string
	// ReleaseID and ReleaseIndex name the funding release, for a terminal that released its
	// hold. Never for settled.
	ReleaseID    string
	ReleaseIndex int64
}

// Commit appends the next event for an attempt.
//
// The order is: read the head, ask the TABLE what this event does from there, build the
// bytes, then CAS. Everything before the swap is pure, so two callers racing produce two
// identical proposals and exactly one of them lands.
func (l *Ledger) Commit(attemptID string, obs Observation) (Event, error) {
	head, ok, err := l.store.Head(attemptID)
	if err != nil {
		return Event{}, err
	}
	if !ok {
		return Event{}, ErrNotFound
	}
	// A TERMINAL ATTEMPT IS NOT REVIVABLE, by anyone. Checked before the table so the answer
	// is about the attempt being over rather than about which event was proposed.
	if Terminal(head.State) {
		return Event{}, ErrTerminal
	}
	to, hold, allowed := Next(head.State, obs.Kind)
	if !allowed {
		return Event{}, ErrNotAllowed
	}
	if obs.EvidenceHash == "" {
		return Event{}, errors.New("an event after issue records the evidence Core observed")
	}

	revision := head.Revision + 1
	eventID, err := EventID(head.Spec.Network, attemptID, revision)
	if err != nil {
		return Event{}, err
	}
	commitmentID, err := CommitmentID(head.Spec.Network, attemptID)
	if err != nil {
		return Event{}, err
	}
	obj, err := buildEvent(head.Spec, eventFields{
		id: eventID, revision: revision, kind: obs.Kind, state: to, hold: hold,
		commitmentID: commitmentID, prevHash: head.Hash, evidenceHash: obs.EvidenceHash,
		reason: obs.Reason, releaseID: obs.ReleaseID, releaseIndex: obs.ReleaseIndex,
		commitTime: l.now(), sequence: l.seq(),
	})
	if err != nil {
		return Event{}, err
	}
	signed, err := l.sign(obj, TypeEvent)
	if err != nil {
		return Event{}, err
	}
	hash, err := towerobj.Hash(signed)
	if err != nil {
		return Event{}, err
	}

	rec := Record{
		AttemptID: attemptID, Revision: revision, EventID: eventID, State: to,
		Kind: string(obs.Kind), Hold: string(hold), Hash: hash, Signed: signed,
		Spec: head.Spec,
	}
	if err := l.store.Append(rec, head.Revision); err != nil {
		return Event{}, err
	}
	return Event{
		ID: eventID, Revision: revision, State: to, Kind: obs.Kind, Hold: hold,
		Hash: hash, Signed: signed,
	}, nil
}

// State reports where an attempt is now.
func (l *Ledger) State(attemptID string) (string, int64, bool, error) {
	head, ok, err := l.store.Head(attemptID)
	if err != nil || !ok {
		return "", 0, false, err
	}
	return head.State, head.Revision, true, nil
}

func (l *Ledger) sign(obj map[string]any, objType string) ([]byte, error) {
	raw, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	return towerobj.Sign(l.cfg.Signer, l.cfg.Network, objType, Version, raw, "sig")
}
