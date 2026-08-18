package fleet

// memstore.go is the in-process projection: correct for a single broker, and the reference
// the durable one is held against.

import (
	"sort"
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
	// DEDUPE BY OFFER ID, LAST WINS - the exact semantics the Postgres store's
	// (tower_id, offer_id) primary key + ON CONFLICT upsert give. Without this the two
	// stores disagree about a duplicate offer id within one Replace, and parity is the
	// whole point of having a reference store.
	seen := map[string]int{}
	out := make([]Station, 0, len(rows))
	for _, r := range rows {
		if i, dup := seen[r.OfferID]; dup {
			out[i] = r
			continue
		}
		seen[r.OfferID] = len(out)
		out = append(out, r)
	}
	m.by[towerID] = out
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
	// The SAME total order Postgres returns. This map is ranged, so without a sort the
	// reference implementation answers identical calls differently - which both diverges
	// from the durable store the parity suites hold it against, and hides ordering bugs
	// behind Go's map randomisation rather than surfacing them.
	sort.Slice(out, func(i, j int) bool {
		if out[i].StationID != out[j].StationID {
			return out[i].StationID < out[j].StationID
		}
		return out[i].OfferID < out[j].OfferID
	})
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

// RoutableTowers lists distinct Towers with an unexpired endpoint row.
func (m *memStore) RoutableTowers(now time.Time) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for tower, rows := range m.by {
		for _, r := range rows {
			if r.Endpoint != "" && now.Before(r.Expires) {
				if !seen[tower] {
					seen[tower] = true
					out = append(out, tower)
				}
				break
			}
		}
	}
	return out, nil
}

// ByTower is a Tower's unexpired rows.
func (m *memStore) ByTower(towerID string, now time.Time) ([]Station, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Station
	for _, r := range m.by[towerID] {
		if now.Before(r.Expires) {
			out = append(out, r)
		}
	}
	// Same total order Postgres returns, for the same reason Candidates sorts: the parity
	// suites hold this implementation against the durable one, and an unordered result makes
	// them disagree for reasons that have nothing to do with the code under test.
	sort.Slice(out, func(i, j int) bool {
		if out[i].StationID != out[j].StationID {
			return out[i].StationID < out[j].StationID
		}
		return out[i].OfferID < out[j].OfferID
	})
	return out, nil
}
