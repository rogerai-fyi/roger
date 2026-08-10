package attach

import (
	"errors"
	"sync"
)

// The refusals a store raises when its own invariants would be broken. They are REFUSALS,
// not outages: a Station ID that is already attached, or a key another live Station holds,
// is a permanent answer. Reporting either as a transient failure invites a caller to retry
// forever against something that will never change.
var (
	errAlreadyAttached  = errors.New("that Station is already attached")
	errKeyHeldByAnother = errors.New("that key is already held by another live Station")
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
}

// NewMemStore builds an empty in-process store.
func NewMemStore() Store {
	return &memStore{auths: map[string]Authorization{}, byID: map[string]Attachment{}}
}

func (m *memStore) PutAuthorization(a Authorization) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.auths[a.ID] = a
	return nil
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
