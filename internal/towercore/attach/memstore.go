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

// PutAuthorization writes an invitation. SPENT IS ONE-WAY: a re-write may mark an invitation
// consumed and may never un-consume one.
//
// THE TWO STORES DISAGREED HERE and nobody had noticed, because nothing in production re-writes
// an invitation id with a stale flag. Postgres' ON CONFLICT DO UPDATE lists every column EXCEPT
// consumed and consumed_by; this store replaced the whole row, so the same call handed the
// caller back an UNSPENT invitation. That direction of the divergence is the dangerous one, and
// it is the one closed here: Admit's first question is `auth.Consumed`, and answering it wrongly
// skips the replay branch entirely - the caller runs on into checkBindings, hits the racer
// short-circuit (`existing.AuthID == authID`), gets an EMPTY revived attachment back, and writes
// Epoch 1 over a Station sitting at 2. An epoch that goes DOWN is the one thing the §6.6b fence
// cannot survive, since its permanent 410 is licensed by monotonicity.
//
// WHY THIS IS AN "OR" AND NOT A COPY OF POSTGRES' RULE, which is the part worth reading twice.
// Copying Postgres exactly - ignore both flags on an overwrite - looks like the parity fix and
// is not: it would import a live defect INTO this store. `toweredgeattach.go` marks the internal
// invitation consumed by re-putting it when a self-attach is refused, precisely so a refusal
// loop cannot fill an owner's open-invite cap and lock them out; on Postgres that write is
// silently dropped today (an audit found it: twenty-five refusals can bar an account from
// attaching for up to the invite TTL, an hour), and it works here only because this store
// overwrites. So the two stores WERE wrong in OPPOSITE directions on one method, and the rule
// that satisfies both intents is monotonic: an invitation may be spent once and never unspent.
//
// BOTH STORES NOW FOLLOW IT. Postgres carries the same rule as `consumed = <row>.consumed OR
// EXCLUDED.consumed`, with consumed_by moved only on the transition - which is what this store
// does by carrying the prior row's pair forward wholesale. The pair is held to it from both
// directions against a real database by TestParityARefusedSelfAttachSpendsItsOwnInvitation and
// TestParityRewritingAnInvitationDoesNotUnconsumeIt.
func (m *memStore) PutAuthorization(a Authorization) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if prior, exists := m.auths[a.ID]; exists && prior.Consumed {
		a.Consumed, a.ConsumedBy = prior.Consumed, prior.ConsumedBy
	}
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
	// Postgres has a station_id primary key and a partial unique index on the ASSERTION key.
	// Without the equivalent under this mutex the two stores disagree exactly where it
	// matters: two concurrent Admits under DISTINCT authorizations sharing an assertion key
	// both win in memory while Postgres rejects one. checkBindings hides that sequentially,
	// which is why a sequential parity test cannot see it.
	//
	// The worked example here used to be the SESSION key, which is now legal in both stores -
	// see the essay at the top of pgstore.go. A justification that cites a rule the tree no
	// longer has is how the rule grows back, so it names the one that is still enforced.
	if existing, taken := m.byID[at.StationID]; taken && existing.AuthID != authID {
		// A DORMANT ROW IS THE ONE THING THIS MAY OVERWRITE, and only by the machine that holds
		// its keys. Everything else - live, revoked, detached - is a Station ID that is somebody
		// else's, and the durable store's PRIMARY KEY says so whatever this thinks. This used to
		// test only Live(), so a terminal row here was silently REPLACED in memory while
		// Postgres refused the insert: a parity divergence that checkBindings happened to hide
		// sequentially, which is exactly how the last one in this function went unnoticed.
		if !(existing.Recoverable() && existing.Owner == at.Owner &&
			existing.Origin.Kind == at.Origin.Kind &&
			existing.AssertionKey == at.AssertionKey && existing.SessionKey == at.SessionKey) {
			return false, errAlreadyAttached
		}
	}
	// AN EPOCH MAY ONLY GO UP, AND THAT IS NOW THIS STORE'S RULE RATHER THAN ITS CALLER'S.
	//
	// Registry.Admit is the only writer of an epoch and it only ever raises one, which is what
	// lets the settlement fence answer a superseded grant with a permanent 410 instead of a
	// retryable 503 - "no retry can un-supersede this placement" is a statement about
	// monotonicity, and it was resting entirely on one function's control flow. A caller that
	// reaches here with a LOWER epoch has computed the wrong attachment (Admit does so today if
	// it is handed a revived invitation whose consumed flag was cleared underneath it), and the
	// cheapest place to make that impossible is the write itself. The durable store carries the
	// same clause in the same position; see pgstore.Admit.
	//
	// It refuses rather than clamps: a write whose epoch has not advanced is a write whose
	// whole attachment is suspect, and silently keeping the old number would leave the rest of
	// the row - origin tower, hub token, keys - written from the same bad computation.
	if existing, taken := m.byID[at.StationID]; taken && at.Epoch <= existing.Epoch {
		return false, errAlreadyAttached
	}
	for _, other := range m.byID {
		// HELD, NOT LIVE. A dormant Station keeps its keys reserved - see StateDormant - and
		// the durable store's partial unique index is built on the same three states, so the
		// two agree about who may take a key that is asleep.
		if other.StationID == at.StationID || !other.Held() {
			continue
		}
		// THE ASSERTION KEY ONLY. The secure-session key used to be tested here too, beside a
		// partial unique index in Postgres that matched it, and both are gone: that key does not
		// sign, nothing routes by it, and the only thing its uniqueness ever achieved was
		// letting one account lock another out of an identity by naming a key it had asked Core
		// for. checkBindings holds the whole argument. The two stores still agree - the durable
		// half dropped its session-key indexes in the same change.
		if other.AssertionKey == at.AssertionKey {
			return false, errKeyHeldByAnother
		}
	}
	a.Consumed, a.ConsumedBy = true, at.StationID
	m.auths[authID] = a
	m.byID[at.StationID] = at
	// THE STAMP GOES WITH THE OLD LIFE. A revived Station's last_routable belongs to the machine
	// as it was before it went quiet, and leaving it in place would put the fresh attachment
	// straight back over the idle horizon - retired again on the next sweep, seconds after
	// coming back. The durable store NULLs the column on the same write for the same reason.
	delete(m.lastRoutable, at.StationID)
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
		// DORMANT, NOT DETACHED: out of service, not out of existence. See StateDormant.
		rec.State = StateDormant
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

// ByAssertionKey scans rather than indexes. The set is small, and a scan cannot fall out of
// step with the records the way a side index can - which is precisely the bug the band
// occupancy check shipped with.
//
// There is no BySessionKey beside it any more. It had one caller, the session-key uniqueness
// rule, and that rule is gone: see checkBindings.
func (m *memStore) ByAssertionKey(key string) (Attachment, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, at := range m.byID {
		// Held rather than Live: a dormant Station's keys are still its own, so a lookup for
		// them must find it - both to refuse another Station taking them, and so the durable
		// store's partial unique index and this scan answer the same question.
		if at.AssertionKey == key && at.Held() {
			return at, true, nil
		}
	}
	return Attachment{}, false, nil
}

// RetireDormant is the second, much later horizon: a Station nobody has seen since `before`
// stops being recoverable and becomes terminal. Measured on the same stamp-or-attach clock as
// DetachIdle, so the two horizons are two points on one timeline rather than two timers.
func (m *memStore) RetireDormant(before time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for id, rec := range m.byID {
		if rec.State != StateDormant {
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
		n++
	}
	return n, nil
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
