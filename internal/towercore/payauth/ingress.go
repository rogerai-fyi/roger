package payauth

// ingress.go is the record of what arrived: one row per provider event id, holding the
// canonical hash of the exact bytes that carried it.
//
// Contract: features/tower/payment_authority.feature ("Webhook replay and mutation are
// distinguished").
//
// # WHY THE BODY HASH IS STORED BESIDE THE ID
//
// Providers retry. A retry is the SAME event id carrying the SAME bytes, and the right answer
// is to acknowledge it and do no more work. But an event id arriving with DIFFERENT bytes is
// not a retry - it is either a provider changing its mind about a past event or somebody
// replaying an id under new content, and the two are indistinguishable from here. Storing the
// hash is what lets Core tell them apart at all; without it, the second delivery would
// silently overwrite the first and the difference would never be visible.
//
// The response to that case is deliberately to STOP: quarantine the conflict and guess
// nothing. Payment state is not something to resolve by picking the newer bytes.

import (
	"errors"
	"sync"
	"time"
)

// Outcome is what an ingress record did.
type Outcome string

const (
	// OutcomeFresh: first sighting. Exactly this outcome schedules a reconciliation fetch.
	OutcomeFresh Outcome = "fresh"
	// OutcomeDuplicate: same id, same bytes. Acknowledge; the fetch already scheduled (or
	// already ran) is the one that counts, so no second trigger is created.
	OutcomeDuplicate Outcome = "duplicate"
	// OutcomeConflict: same id, DIFFERENT bytes. Nothing is inferred and nothing is
	// overwritten; the pair is kept for a human.
	OutcomeConflict Outcome = "conflict"
)

// Record is one stored ingress event.
type Record struct {
	EventID     string
	RawBodyHash string
	Merchant    string
	SourceID    string
	SourceKind  string
	ReceivedAt  time.Time
	// Conflicting holds the hash that disagreed, when this record has been quarantined.
	Conflicting string
}

// Quarantined reports whether this record is in conflict and must not drive reconciliation.
func (r Record) Quarantined() bool { return r.Conflicting != "" }

// IngressStore holds ingress records. Implementations must make Admit ATOMIC per event id:
// two concurrent deliveries of the same id must produce one record and exactly one fresh
// outcome, or the "coalesced trigger" the spec requires becomes two fetches racing.
type IngressStore interface {
	// Admit records a hint and reports what it was. Idempotent on (event id, body hash).
	Admit(h Hint) (Outcome, Record, error)
	// Get reads a record back.
	Get(eventID string) (Record, bool, error)
}

// ErrEmptyEventID refuses a record with no key. An empty id would collapse every anonymous
// delivery onto one row.
var ErrEmptyEventID = errors.New("payauth: an ingress record needs its event id")

// MemIngress is the in-process store: the reference implementation the durable one is held
// against, and what a single-instance deployment runs on.
type MemIngress struct {
	mu sync.Mutex
	by map[string]Record
}

// NewMemIngress builds an empty store.
func NewMemIngress() *MemIngress { return &MemIngress{by: map[string]Record{}} }

// Admit is the whole replay/mutation table in one function.
func (m *MemIngress) Admit(h Hint) (Outcome, Record, error) {
	if h.EventID == "" {
		return "", Record{}, ErrEmptyEventID
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	prior, seen := m.by[h.EventID]
	if !seen {
		rec := Record{
			EventID: h.EventID, RawBodyHash: h.RawBodyHash, Merchant: h.Merchant,
			SourceID: h.SourceID, SourceKind: h.SourceKind, ReceivedAt: h.ReceivedAt,
		}
		m.by[h.EventID] = rec
		return OutcomeFresh, rec, nil
	}
	if prior.Quarantined() {
		return OutcomeConflict, prior, nil
	}
	if prior.RawBodyHash == h.RawBodyHash {
		// A retry. The FIRST record stands - including its received time, which is when this
		// event actually reached us and is what any window is measured from.
		return OutcomeDuplicate, prior, nil
	}
	prior.Conflicting = h.RawBodyHash
	m.by[h.EventID] = prior
	return OutcomeConflict, prior, nil
}

// Get reads one record.
func (m *MemIngress) Get(eventID string) (Record, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.by[eventID]
	return r, ok, nil
}
