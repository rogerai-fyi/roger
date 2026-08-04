package emailauth

// store.go is where an issued sign-in code lives between the mail and the person typing it
// back. It follows the same rules as the device-login store (internal/deviceauth/store.go)
// and for the same reasons:
//
//   - a code is spent by a COMPARE-AND-SWAP, never by a read followed by a delete, so N
//     concurrent submissions of one correct code accept exactly one;
//   - what is written down is not itself a credential - the store holds only hashes, so a
//     backup, a replica, or an operational scan does not sign anybody in;
//   - an unreachable store is an ERROR, never a clean miss, because a miss is
//     indistinguishable from "your code is wrong" and that is the wrong thing to tell a
//     person whose code is fine.

import (
	"sync"
	"time"
)

// Record is one outstanding sign-in code.
//
// There is nowhere in it to put a plaintext address or a plaintext code, which is
// deliberate: the fields that exist are the fields that can leak.
type Record struct {
	AddrHash string    `json:"addr_hash"`
	CodeHash string    `json:"code_hash"`
	Issued   time.Time `json:"issued"`
	Expires  time.Time `json:"expires"`
	// Attempts is the per-ADDRESS guessing budget. It lives on the record so that
	// retiring a code (issuing a new one) also clears it, and so that a budget cannot be
	// refilled by a restart.
	Attempts int `json:"attempts"`
	// Rev is the revision this record was read at; Consume applies only if it still matches.
	Rev int64 `json:"rev"`
}

// Store is where outstanding codes and the abuse counters live.
type Store interface {
	// Put records a code for an address, REPLACING any code already outstanding for it.
	// The replacement is what retires the previous code.
	Put(r Record) error

	// ByAddress resolves the outstanding code for an address hash. An absent record is
	// (false, nil): a miss is not an error.
	ByAddress(addrHash string) (Record, bool, error)

	// Consume spends the record if its revision is still current, and reports whether THIS
	// call spent it. A false return is a legitimate outcome - somebody else got there
	// first - not an error.
	Consume(r Record) (bool, error)

	// Penalize spends one unit of an address's guessing budget and returns the new total.
	Penalize(addrHash string, ttl time.Duration) (int, error)

	// AllowRequest reports whether a code may be issued: it charges BOTH the per-address
	// and the per-source budgets. Both are charged in one call so a caller cannot check
	// one and forget the other.
	AllowRequest(addrHash, source string, perAddress, perSource int, window time.Duration, now time.Time) (bool, error)

	// AllowSubmit reports whether a submission may be attempted, charging the per-source
	// budget.
	AllowSubmit(source string, perSource int, window time.Duration, now time.Time) (bool, error)

	// Reap removes records that can no longer be used, so the store does not grow without
	// bound under a caller who only ever requests.
	Reap(now time.Time) error
}

// --- the in-process implementation ----------------------------------------

type memStore struct {
	mu      sync.Mutex
	recs    map[string]Record
	counts  map[string]*window
	nextRev int64
}

// window is a fixed-window counter: a count and the moment the window opened. A fixed
// window is enough here because these budgets bound ABUSE VOLUME rather than enforcing a
// smooth rate, and the burst a fixed window permits at a boundary is at most one extra
// window's worth of mail.
type window struct {
	count int
	since time.Time
}

// NewMemStore builds the in-process store: the single-instance default, with no new
// dependency and no configuration.
func NewMemStore() Store {
	return &memStore{recs: map[string]Record{}, counts: map[string]*window{}}
}

func (m *memStore) Put(r Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextRev++
	r.Rev = m.nextRev
	r.Attempts = 0 // a fresh code carries a fresh budget
	m.recs[r.AddrHash] = r
	return nil
}

func (m *memStore) ByAddress(addrHash string) (Record, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.recs[addrHash]
	return r, ok, nil
}

func (m *memStore) Consume(r Record) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.recs[r.AddrHash]
	if !ok || cur.Rev != r.Rev {
		return false, nil
	}
	delete(m.recs, r.AddrHash)
	return true, nil
}

func (m *memStore) Penalize(addrHash string, _ time.Duration) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.recs[addrHash]
	if !ok {
		return 0, nil
	}
	r.Attempts++
	m.nextRev++
	r.Rev = m.nextRev
	m.recs[addrHash] = r
	return r.Attempts, nil
}

// allowLocked charges one fixed window. Caller holds m.mu.
func (m *memStore) allowLocked(key string, limit int, dur time.Duration, now time.Time) bool {
	w, ok := m.counts[key]
	if !ok || now.Sub(w.since) >= dur {
		m.counts[key] = &window{count: 1, since: now}
		return true
	}
	if w.count >= limit {
		return false
	}
	w.count++
	return true
}

func (m *memStore) AllowRequest(addrHash, source string, perAddress, perSource int, dur time.Duration, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// The address budget is charged first and the source budget only if it passed, so a
	// blocked address does not also burn the sender's wider allowance.
	if !m.allowLocked("req:addr:"+addrHash, perAddress, dur, now) {
		return false, nil
	}
	return m.allowLocked("req:src:"+source, perSource, dur, now), nil
}

func (m *memStore) AllowSubmit(source string, perSource int, dur time.Duration, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.allowLocked("sub:src:"+source, perSource, dur, now), nil
}

func (m *memStore) Reap(now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, r := range m.recs {
		if now.After(r.Expires) {
			delete(m.recs, k)
		}
	}
	for k, w := range m.counts {
		// A window nobody has touched for an hour is dead weight; the budget it held has
		// long since reset.
		if now.Sub(w.since) > time.Hour {
			delete(m.counts, k)
		}
	}
	return nil
}
