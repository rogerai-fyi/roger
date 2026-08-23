package link

import (
	"errors"
	"sync"
	"time"
)

// Mirror is the shared view of every instance's live sessions.
//
// Sessions memory is per-process; production runs more than one process behind a
// per-request load balancer. The first real Tower on the network opened its session on
// one instance and had the other refuse its very next inventory push with "open a
// session before pushing inventory" - the exact class of failure the /discover registry
// split taught: mirror per-instance state to the shared store, read the union, write
// idempotently. Contract: features/tower/link_multi_instance.feature.
//
// The mirror is written ONLY by Core's own handlers after the Tower's signed request is
// authenticated, so a Tower can no more forge a peer's record here than it could in the
// per-process map. Nil Mirror means in-process only, which is correct for one instance.
type Mirror interface {
	// Put records (or refreshes) a Tower's live session. Idempotent.
	Put(towerID string, r Record) error
	// Get answers with the record and whether one exists.
	Get(towerID string) (Record, bool, error)
	// Del removes a Tower's record ONLY while it still names this session - a
	// compare-and-delete, so a stale close of a superseded session cannot wipe the newer
	// row a peer just wrote and leave the Tower transiently dark there. Deleting an
	// absent or superseded record is not an error.
	Del(towerID, sessionID string) error
	// All lists every record, for the fleet-wide live set.
	All() (map[string]Record, error)
}

// Record is one Tower's live session as any instance may need it: enough to keep the
// link alive, gate inventory, and resolve the relay plane - and nothing more.
type Record struct {
	SessionID string
	Version   int
	LastSeen  time.Time
	Relay     RelayPlane
}

// ErrMirrorDown is a mirror that cannot answer. Callers fall back to what they can
// actually see: an instance never invents liveness it cannot verify.
var ErrMirrorDown = errors.New("the link mirror is unavailable")

// MemMirror is the in-memory Mirror used by tests and by a deliberate single-process
// deployment that still wants the code path exercised.
type MemMirror struct {
	mu   sync.RWMutex
	by   map[string]Record
	fail bool
}

func NewMemMirror() *MemMirror { return &MemMirror{by: map[string]Record{}} }

// FailForTest makes every operation answer ErrMirrorDown, standing in for the shared
// store being unreachable.
func (m *MemMirror) FailForTest(fail bool) { m.mu.Lock(); m.fail = fail; m.mu.Unlock() }

func (m *MemMirror) Put(towerID string, r Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return ErrMirrorDown
	}
	m.by[towerID] = r
	return nil
}

func (m *MemMirror) Get(towerID string) (Record, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.fail {
		return Record{}, false, ErrMirrorDown
	}
	r, ok := m.by[towerID]
	return r, ok, nil
}

func (m *MemMirror) Del(towerID, sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return ErrMirrorDown
	}
	if r, ok := m.by[towerID]; ok && r.SessionID == sessionID {
		delete(m.by, towerID)
	}
	return nil
}

func (m *MemMirror) All() (map[string]Record, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.fail {
		return nil, ErrMirrorDown
	}
	out := make(map[string]Record, len(m.by))
	for k, v := range m.by {
		out[k] = v
	}
	return out, nil
}
