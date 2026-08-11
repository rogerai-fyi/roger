package fleet

// memstore.go is the in-process projection: correct for a single broker, and the reference
// the durable one is held against.

import (
	"sync"
	"time"
)

type memStore struct {
	mu sync.Mutex
	by map[string][]Station
}

// NewMemStore returns an in-process fleet view.
func NewMemStore() Store { return &memStore{by: map[string][]Station{}} }

func (m *memStore) Replace(towerID string, rows []Station) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(rows) == 0 {
		delete(m.by, towerID)
		return nil
	}
	m.by[towerID] = append([]Station(nil), rows...)
	return nil
}

func (m *memStore) Candidates(model string, now time.Time) ([]Station, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Station
	for _, rows := range m.by {
		for _, r := range rows {
			if r.Model == model && now.Before(r.Expires) {
				out = append(out, r)
			}
		}
	}
	return out, nil
}

func (m *memStore) Forget(towerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.by, towerID)
	return nil
}

func (m *memStore) Reap(now time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for tower, rows := range m.by {
		kept := rows[:0]
		for _, r := range rows {
			if now.Before(r.Expires) {
				kept = append(kept, r)
				continue
			}
			n++
		}
		if len(kept) == 0 {
			delete(m.by, tower)
			continue
		}
		m.by[tower] = kept
	}
	return n, nil
}
