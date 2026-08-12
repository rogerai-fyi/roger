package audit

import (
	"sync"
	"time"
)

type memStore struct {
	mu sync.Mutex
	by map[string]Wanted // attempt id -> wanted
}

// NewMemStore builds the in-process wanted list.
func NewMemStore() Store { return &memStore{by: map[string]Wanted{}} }

func (m *memStore) Want(w Wanted) error {
	if err := check(w); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.by[w.AttemptID]; exists {
		return nil // idempotent: wanted once
	}
	m.by[w.AttemptID] = w
	return nil
}

func (m *memStore) Pending(towerID string, now time.Time) ([]Wanted, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Wanted
	for _, w := range m.by {
		if w.TowerID == towerID && now.Before(w.Deadline) {
			out = append(out, w)
		}
	}
	return out, nil
}

func (m *memStore) Resolve(attemptID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.by, attemptID)
	return nil
}

func (m *memStore) Overdue(now time.Time) ([]Wanted, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Wanted
	for id, w := range m.by {
		if !now.Before(w.Deadline) {
			out = append(out, w)
			delete(m.by, id)
		}
	}
	return out, nil
}
