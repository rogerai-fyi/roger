package attempt

// memstore.go is the in-process chain: correct for one broker, and the reference the durable
// store is held against.

import (
	"bytes"
	"sync"
)

type memStore struct {
	mu sync.Mutex
	// by attempt, then revision. Kept whole rather than as a head pointer, because the
	// idempotent-replay check has to compare the BYTES already committed at a revision.
	by map[string]map[int64]Record
}

// NewMemStore returns an in-process attempt chain.
func NewMemStore() Store { return &memStore{by: map[string]map[int64]Record{}} }

// Append is the compare-and-swap, under one held lock from read to write.
//
// The three outcomes are the spec's: an exact replay is idempotent, a different event at a
// taken revision is a conflict, and anything that is not the immediate next revision fails
// before state or hold moves.
func (m *memStore) Append(rec Record, expectPrev int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	chain, exists := m.by[rec.AttemptID]
	if !exists {
		if expectPrev != 0 || rec.Revision != 1 {
			return ErrNotFound
		}
		m.by[rec.AttemptID] = map[int64]Record{1: rec}
		return nil
	}
	// EXACT REPLAY IS IDEMPOTENT, and it is checked FIRST. A retried commit of identical
	// bytes is the same fact stated twice, and refusing it would turn a lost response into a
	// stuck attempt.
	existing, taken := chain[rec.Revision]
	if taken && existing.EventID == rec.EventID && bytes.Equal(existing.Signed, rec.Signed) {
		return nil
	}
	// ISSUING AGAINST AN EXISTING ATTEMPT is a duplicate issue, and is answered as one. It is
	// also technically a byte conflict at revision 1, but "already issued" is what the caller
	// actually did and is the thing they can act on; "conflicting bytes" describes our
	// storage rather than their mistake.
	if expectPrev == 0 {
		return ErrAlreadyIssued
	}
	if taken {
		return ErrConflict
	}
	head := chain[expectPrev]
	if head.Revision != expectPrev || rec.Revision != expectPrev+1 {
		return ErrRevision
	}
	chain[rec.Revision] = rec
	return nil
}

func (m *memStore) Head(attemptID string) (Record, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	chain, ok := m.by[attemptID]
	if !ok || len(chain) == 0 {
		return Record{}, false, nil
	}
	var best Record
	for _, r := range chain {
		if r.Revision > best.Revision {
			best = r
		}
	}
	return best, true, nil
}

func (m *memStore) At(attemptID string, revision int64) (Record, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.by[attemptID][revision]
	return r, ok, nil
}
