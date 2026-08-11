package dispatch

// memstore.go is the in-process attempt store.
//
// It is the REFERENCE IMPLEMENTATION the durable one is held against, and it is written
// deliberately differently: a held mutex and a map here, a conditional UPDATE and a row count
// there. The parity suite runs the same scenarios through both and requires the same answers,
// so agreement between them is a result rather than a restatement.
//
// It is also the correct store for a SINGLE broker, which is what a self-hoster runs.

import (
	"sync"
	"time"
)

type memStore struct {
	mu sync.Mutex
	by map[string]Record
}

// NewMemStore returns an in-process attempt store.
func NewMemStore() Store { return &memStore{by: map[string]Record{}} }

// Put records a new attempt and leaves an existing one alone.
//
// FIRST WRITE WINS, matching the durable store's ON CONFLICT DO NOTHING. Overwriting looks
// harmless - an attempt id comes from crypto/rand, so a second Put is a retry of the same
// thing - but it would reset a CLAIMED attempt back to issued and hand the same work out
// twice. The parity suite caught exactly that, which is the whole reason these two are held
// against each other.
func (m *memStore) Put(r Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.by[r.AttemptID]; exists {
		return nil
	}
	m.by[r.AttemptID] = r
	return nil
}

func (m *memStore) Get(attemptID string) (Record, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.by[attemptID]
	return r, ok, nil
}

// ClaimByID is the compare-and-swap, under one held lock from read to write. Releasing it
// between the two would be the check-then-act this exists to avoid.
func (m *memStore) ClaimByID(attemptID, towerID string, now time.Time) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	r, ok := m.by[attemptID]
	// A grant issued for another Tower is answered as NOT FOUND rather than as forbidden. An
	// attempt id is not a secret, and distinguishing the two would turn this into an oracle
	// for which attempts exist.
	if !ok || r.TowerID != towerID {
		return Record{}, ErrNotFound
	}
	switch {
	case r.State == StateSettled:
		return Record{}, ErrAlreadySettled
	case r.State == StateClaimed:
		return Record{}, ErrAlreadyClaimed
	case !now.Before(r.Deadline):
		return Record{}, ErrExpired
	}
	r.State = StateClaimed
	m.by[attemptID] = r
	return r, nil
}

func (m *memStore) ClaimNext(towerID string, now time.Time) (Record, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, r := range m.by {
		if r.TowerID != towerID || r.State != StateIssued || !now.Before(r.Deadline) {
			continue
		}
		r.State = StateClaimed
		m.by[id] = r
		return r, true, nil
	}
	return Record{}, false, nil
}

func (m *memStore) Settle(attemptID string, now time.Time) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	r, ok := m.by[attemptID]
	if !ok {
		return Record{}, ErrNotFound
	}
	switch {
	case r.State == StateSettled:
		return Record{}, ErrAlreadySettled
	case r.State != StateClaimed:
		return Record{}, ErrNotClaimed
	case !now.Before(r.Deadline):
		return Record{}, ErrExpired
	}
	r.State = StateSettled
	m.by[attemptID] = r
	return r, nil
}

func (m *memStore) Reap(before time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for id, r := range m.by {
		if !before.Before(r.Deadline) {
			delete(m.by, id)
			n++
		}
	}
	return n, nil
}

// Len is how Registry.Pending reports depth. Not on the Store interface: a durable store's
// count is a query with a cost, and nothing needs it badly enough to make every store answer.
func (m *memStore) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.by)
}
