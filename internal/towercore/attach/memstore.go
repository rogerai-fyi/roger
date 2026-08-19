package attach

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// The refusals a store raises when its own invariants would be broken. They are REFUSALS,
// not outages: a Station ID that is already attached, or a key another live Station holds,
// is a permanent answer. Reporting either as a transient failure invites a caller to retry
// forever against something that will never change.
var (
	errAlreadyAttached  = fmt.Errorf("%w: that Station is already attached", ErrRejected)
	errKeyHeldByAnother = fmt.Errorf("%w: that key is already held by another live Station", ErrRejected)
)

// memStore is the in-process Store. It exists so the contract can be exercised without a
// database, and so the Postgres implementation has something to be held against in a parity
// suite - the band work in internal/store is a standing reminder of what happens when a
// memory store is covered and its durable twin is not.
//
// Admit takes the lock for the whole consume-and-write, which is what makes it ONE
// transaction here. The Postgres implementation gets the same property from a transaction
// with the authorization row locked; a read-then-write in either would let two racing
// attachments both win.
type memStore struct {
	mu    sync.Mutex
	auths map[string]Authorization
	byID  map[string]Attachment
	// lastRoutable is the TouchRoutable stamp, kept BESIDE the record rather than on it - the
	// Postgres store keeps it as a column scanAttachment does not read, and the two stores are
	// only interchangeable if the reference one hides it the same way. It is housekeeping
	// about a Station rather than part of what Core recorded about it, and putting it on
	// Attachment would put it in front of every reader of an attachment for one sweep's sake.
	lastRoutable map[string]time.Time
}

// NewMemStore builds an empty in-process store.
func NewMemStore() Store {
	return &memStore{
		auths: map[string]Authorization{}, byID: map[string]Attachment{},
		lastRoutable: map[string]time.Time{},
	}
}

func (m *memStore) PutAuthorization(a Authorization) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.auths[a.ID] = a
	return nil
}

// PutAuthorizationCapped counts and writes under the SAME held lock, which is what makes it
// a cap rather than a suggestion.
func (m *memStore) PutAuthorizationCapped(a Authorization, max int) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	live := 0
	for _, existing := range m.auths {
		if existing.Owner == a.Owner && !existing.Consumed && !existing.ExpiresAt.Before(a.IssuedAt) {
			live++
		}
	}
	if live >= max {
		return false, nil
	}
	// Postgres has a primary key here; without the same check the stores disagree on a
	// duplicate id - one silently overwrites, the other refuses.
	if _, exists := m.auths[a.ID]; exists {
		return false, fmt.Errorf("%w: that invitation id already exists", ErrRejected)
	}
	m.auths[a.ID] = a
	return true, nil
}

// Reap drops expired UNCONSUMED invitations, keeping the consumed ones that answer retries.
func (m *memStore) Reap(before time.Time, retryHorizon time.Duration) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for id, a := range m.auths {
		if a.ExpiresAt.After(before) {
			continue // still live
		}
		// Consumed rows answer a lost-response retry, so they linger past expiry - but only
		// until no plausible retry could still arrive.
		if a.Consumed && a.ExpiresAt.After(before.Add(-retryHorizon)) {
			continue
		}
		delete(m.auths, id)
		n++
	}
	return n, nil
}

func (m *memStore) CountLiveAttachments(owner string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, at := range m.byID {
		if at.Owner == owner && at.Live() {
			n++
		}
	}
	return n, nil
}

func (m *memStore) Authorization(id string) (Authorization, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.auths[id]
	return a, ok, nil
}

// Admit is the whole point of the type: the authorization is re-checked and spent, and the
// attachment written, without releasing the lock in between.
func (m *memStore) Admit(authID string, at Attachment) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	a, ok := m.auths[authID]
	if !ok || a.Consumed {
		return false, nil // lost the race, or never existed
	}
	// THE SAME INVARIANTS THE DATABASE ENFORCES, enforced here too.
	//
	// Postgres has a station_id primary key and partial unique indexes on each live key.
	// Without the equivalent under this mutex the two stores disagree exactly where it
	// matters: two concurrent Admits under DISTINCT authorizations sharing a session key
	// both win in memory while Postgres rejects one. checkBindings hides that sequentially,
	// which is why a sequential parity test cannot see it.
	if existing, taken := m.byID[at.StationID]; taken && existing.Live() && existing.AuthID != authID {
		return false, errAlreadyAttached
	}
	for _, other := range m.byID {
		if other.StationID == at.StationID || !other.Live() {
			continue
		}
		if other.AssertionKey == at.AssertionKey || other.SessionKey == at.SessionKey {
			return false, errKeyHeldByAnother
		}
	}
	a.Consumed, a.ConsumedBy = true, at.StationID
	m.auths[authID] = a
	m.byID[at.StationID] = at
	return true, nil
}

func (m *memStore) ByStation(stationID string) (Attachment, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	at, ok := m.byID[stationID]
	return at, ok, nil
}

// ByStations is the batch read, and it answers exactly what len(ids) calls to ByStation
// would: every state, absent ids absent from the map. A duplicate id in the request is
// collapsed by the map, which is also what the Postgres `= ANY($1)` does.
func (m *memStore) ByStations(stationIDs []string) (map[string]Attachment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]Attachment, len(stationIDs))
	for _, id := range stationIDs {
		if at, ok := m.byID[id]; ok {
			out[id] = at
		}
	}
	return out, nil
}

// TouchRoutable stamps only Stations that EXIST. Postgres cannot stamp a row that is not
// there, so neither may this: a stamp for an unknown Station would otherwise linger and
// pre-date a later attachment under the same id, which is exactly the kind of divergence the
// parity suites exist to catch.
func (m *memStore) TouchRoutable(stationIDs []string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range stationIDs {
		if _, ok := m.byID[id]; ok {
			m.lastRoutable[id] = at
		}
	}
	return nil
}

// DetachIdle retires this Tower's live attachments that have gone quiet, measuring each from
// its stamp or, absent one, from when it attached - the COALESCE the durable store does.
//
// SCOPED TO THE ROWS THE STAMP CAN REACH - the ones carrying a node id, which is the same
// filter the durable store's WHERE clause applies. See the Store interface for the argument.
func (m *memStore) DetachIdle(towerID string, before time.Time) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for id, rec := range m.byID {
		if rec.Origin.TowerID != towerID || !rec.Live() || rec.NodeID == "" {
			continue
		}
		seen := m.lastRoutable[id]
		if seen.IsZero() {
			seen = rec.AttachedAt
		}
		if !seen.Before(before) {
			continue
		}
		rec.State = StateDetached
		m.byID[id] = rec
		out = append(out, id)
	}
	// A total order, because a Go map has none and the durable store's answer is sorted. A
	// caller logs these ids; two stores that disagree about their order would make the same
	// sweep unreproducible between a test and production.
	sort.Strings(out)
	return out, nil
}

func (m *memStore) ByTower(towerID string) ([]Attachment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Attachment
	for _, at := range m.byID {
		if at.Origin.TowerID == towerID && at.Live() {
			out = append(out, at)
		}
	}
	return out, nil
}

// ByAssertionKey and BySessionKey scan rather than index. The set is small, and a scan
// cannot fall out of step with the records the way a side index can - which is precisely
// the bug the band occupancy check shipped with.
func (m *memStore) ByAssertionKey(key string) (Attachment, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, at := range m.byID {
		if at.AssertionKey == key && at.Live() {
			return at, true, nil
		}
	}
	return Attachment{}, false, nil
}

func (m *memStore) BySessionKey(key string) (Attachment, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, at := range m.byID {
		if at.SessionKey == key && at.Live() {
			return at, true, nil
		}
	}
	return Attachment{}, false, nil
}

func (m *memStore) ReapTerminal(before time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for id, at := range m.byID {
		if (at.State == StateRevoked || at.State == StateDetached) && !at.AttachedAt.After(before) {
			delete(m.byID, id)
			n++
		}
	}
	return n, nil
}

// MarkAuditProven stamps the first answered audit. Later answers are no-ops: the proof is
// that it EVER produced one, and re-stamping would let a node that has since gone silent
// keep looking freshly capable.
func (m *memStore) MarkAuditProven(stationID string, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.byID[stationID]
	if !ok || !rec.AuditProvenAt.IsZero() {
		return false, nil
	}
	rec.AuditProvenAt = at
	m.byID[stationID] = rec
	return true, nil
}

func (m *memStore) SetState(stationID, state string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	at, ok := m.byID[stationID]
	if !ok {
		return false, nil
	}
	at.State = state
	m.byID[stationID] = at
	return true, nil
}
