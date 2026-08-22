package reputation

import (
	"sync"
	"time"
)

// memStore is the in-process reputation ledger: correct for one broker, and the reference the
// durable store is held against.
type memStore struct {
	mu sync.Mutex
	// events, in arrival order. A slice rather than a map because the questions are all
	// windowed scans, and the volume is bounded by the reap.
	events []Event
	// seen keys (tower|attempt|outcome) for idempotency, so a retried Record does not
	// double-count.
	seen map[string]bool
}

// NewMemStore builds an in-process reputation ledger.
func NewMemStore() Store {
	return &memStore{seen: map[string]bool{}}
}

func idemKey(e Event) string { return e.TowerID + "|" + e.AttemptID + "|" + string(e.Outcome) }

func (m *memStore) Record(e Event) error {
	if err := checkEvent(e); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := idemKey(e)
	if m.seen[k] {
		return nil
	}
	m.seen[k] = true
	m.events = append(m.events, e)
	return nil
}

func (m *memStore) Tally(towerID string, since time.Time) (Tally, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := Tally{TowerID: towerID}
	for _, e := range m.events {
		if e.TowerID != towerID || e.At.Before(since) {
			continue
		}
		addOutcome(&t, e.Outcome)
	}
	return t, nil
}

// TallyByStation groups this Tower's window by the Station each outcome named. One pass over
// the same slice Tally scans, so the two cannot disagree about which events are in the window.
func (m *memStore) TallyByStation(towerID string, since time.Time) (map[string]Tally, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]Tally{}
	for _, e := range m.events {
		if e.TowerID != towerID || e.At.Before(since) {
			continue
		}
		t := out[e.StationID]
		t.TowerID = towerID
		addOutcome(&t, e.Outcome)
		out[e.StationID] = t
	}
	return out, nil
}

func (m *memStore) FleetTally(since time.Time) (Tally, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := Tally{}
	for _, e := range m.events {
		if e.At.Before(since) {
			continue
		}
		addOutcome(&t, e.Outcome)
	}
	return t, nil
}

func (m *memStore) Reap(before time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	kept := m.events[:0]
	var dropped int64
	for _, e := range m.events {
		if e.At.Before(before) {
			delete(m.seen, idemKey(e))
			dropped++
			continue
		}
		kept = append(kept, e)
	}
	m.events = kept
	return dropped, nil
}

// addOutcome is the one place an outcome maps to a counter, so mem and PG cannot drift on
// which column an outcome lands in.
func addOutcome(t *Tally, o Outcome) {
	t.Total++
	switch o {
	case Corroborated:
		t.Corroborated++
	case Uncorroborated:
		t.Uncorroborated++
	case Disputed:
		t.Disputed++
	case CanaryPass:
		t.CanaryPass++
	case CanaryFail:
		t.CanaryFail++
	case AuditMismatch:
		t.AuditMismatch++
	case StationFault:
		t.StationFault++
	}
}
