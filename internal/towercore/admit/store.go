package admit

// store.go is where the admission registry LIVES.
//
// This registry is Roger Core's record of which Towers are admitted to the public network,
// what lease each holds, what lifecycle state it is in, and what false-claim evidence has
// accumulated against it. It used to live in three process-local maps, which meant a
// restart forgot the whole thing. Two of the consequences are not merely inconvenient:
//
//   - REVOCATION WAS UNDONE. A Tower revoked for abuse simply stopped being revoked after a
//     deploy, and the identity key that revocation burns became re-enrollable.
//   - EVIDENCE WAS ERASED. FalseClaims counts a Tower asserting a state it does not hold;
//     a count that resets every time we ship is not evidence of anything.
//
// The seam matches internal/deviceauth's, and for the same reasons: a durable default that
// changes no single-instance behaviour, state changes applied by COMPARE-AND-SWAP so two
// writers acting on one read cannot both win, and a store failure that REFUSES rather than
// inventing an answer. An admission we cannot record must never be reported as an
// admission, and a registry we cannot read grants nothing.

import (
	"errors"
	"slices"
	"sort"
	"sync"
	"time"
)

// ErrUnavailable means the registry could not be reached. It is never conflated with "not
// admitted": the difference between "this Tower is not allowed to work" and "we cannot
// currently tell" is the difference between a correct refusal and an outage that silently
// looks like a network-wide ban.
var ErrUnavailable = errors.New("the admission registry is temporarily unavailable")

// Token is an unspent enrollment token.
type Token struct {
	ID      string    `json:"id"`
	Owner   string    `json:"owner"`
	Expires time.Time `json:"expires"`
}

// Store is where admission state lives. Every method reports a transport failure as an
// error; no implementation may substitute a local fallback, because a fallback is a second
// opinion about who is admitted to the public network.
type Store interface {
	// PutToken records an unspent enrollment token.
	PutToken(t Token) error

	// PutTokenCapped records a token ONLY if the owner is under max live tokens, and
	// reports whether it was written.
	//
	// The cap has to be enforced where the write happens. Counting first and inserting
	// after is a check-then-act: concurrent mints all read the same count, all pass, and
	// all insert - overshooting by the caller's concurrency, once per TTL window. A cap
	// that only holds when nobody is trying is not a cap.
	PutTokenCapped(t Token, max int) (bool, error)
	// GetToken reads a token WITHOUT consuming it, so a rejected enrollment does not burn
	// the token its legitimate holder still needs.
	GetToken(id string) (Token, bool, error)
	// ConsumeToken atomically removes a token and reports whether THIS call removed it.
	// It is called only once every other check has passed, which is what makes redemption
	// one-time across the deployment while leaving a failed attempt harmless: of two
	// concurrent enrollments that both validate, exactly one can consume.
	ConsumeToken(id string) (bool, error)

	// Admit consumes the enrollment token AND writes the Tower in ONE transaction, and
	// reports whether THIS call did it.
	//
	// The pair has to be atomic. Consuming the token and then inserting the Tower is fine
	// until the process dies between them - and then the token is spent while no Tower
	// exists, so the operator holds a receipt for an admission that never happened and
	// has no way to retry. A write failure rolls the token consumption back with it, so a
	// rejected attempt leaves the token usable, as an approved scenario requires.
	Admit(tokenID string, tw Tower) (bool, error)

	// PutTower writes a Tower record. Rev carries the revision the caller read; a write
	// against a superseded revision is refused by CAS below.
	PutTower(tw Tower) error
	// CASTower writes tw only if the stored revision still matches tw.Rev, reporting
	// whether THIS call wrote it. A false return is a legitimate outcome, not an error.
	CASTower(tw Tower) (bool, error)

	TowerByID(id string) (Tower, bool, error)
	// TowerByKey resolves the identity-key index. It is what keeps one key to one Tower -
	// and what keeps a revoked key burned, since the record survives revocation.
	TowerByKey(keyHash string) (Tower, bool, error)
	// TowersByOwner lists an account's Towers, for the per-owner quota and the operator's
	// own view.
	TowersByOwner(owner string) ([]Tower, error)

	// AllTowers lists every Tower, for the admin's approval queue. Ordering is the
	// REGISTRY's job, not the store's: two stores that sorted differently would reshuffle
	// the queue depending on the deployment.
	AllTowers() ([]Tower, error)

	// ReapTokens removes tokens that can no longer be redeemed.
	ReapTokens(now time.Time) error

	// LiveTokens lists an owner's unspent, unexpired tokens.
	//
	// Reaping alone does NOT bound the token space: it clears expired tokens, and says
	// nothing about a burst of live ones. Without this an authenticated account could mint
	// in a loop and grow the table without limit for a whole TTL window - a
	// database-filling vector behind nothing but a free registration.
	LiveTokens(owner string, now time.Time) ([]string, error)

	// AppendEvent adds one entry to a Tower's governance record. There is deliberately no
	// method that edits or removes one.
	AppendEvent(ev Event) error
	// Events lists a Tower's governance record, newest first, at most limit entries.
	Events(towerID string, limit int) ([]Event, error)
	// CASTowerEvent is CASTower and AppendEvent in ONE write: both happen or neither does.
	CASTowerEvent(tw Tower, ev Event) (bool, error)
	// FindEvent returns the newest (or, with oldest, the first) event of any of the kinds.
	FindEvent(towerID string, kinds []string, oldest bool) (Event, bool, error)
	// EventsOfKinds returns a Tower's events of any of the kinds strictly after since, oldest
	// first, at most limit of them.
	EventsOfKinds(towerID string, kinds []string, since time.Time, limit int) ([]Event, error)
	// AppendEventIfLast appends ev only if the newest event of those kinds still has the id
	// the caller saw (0 for none), so two instances acting on one read cannot both write.
	AppendEventIfLast(ev Event, kinds []string, lastID int64) (bool, error)
	// CountEvents counts a Tower's events of one kind at or after since; a non-empty reason
	// or actor narrows it to that reason or that exact actor. It is what paces how fast one
	// Tower, or one operator, can add to the record.
	CountEvents(towerID, kind, reason, actor string, since time.Time) (int, error)
	// FindTransition returns a Tower's newest event that moved it from one state to another,
	// whatever its kind, in one read however large the record has grown.
	FindTransition(towerID string, from, to State) (Event, bool, error)
	// LinkSummaries reads, for every Tower with any event of the kinds, the first and
	// newest of each kind and the newest overall, in one pass.
	LinkSummaries(kinds []string) (map[string]LinkSummary, error)
}

// LinkSummary is one Tower's link history at a glance: the first event of the kinds, the
// newest of each, and the newest overall.
type LinkSummary struct {
	First  Event
	Latest Event
	LastOf map[string]Event
}

// --- the in-process implementation ----------------------------------------

// memStore is the default: the behaviour the registry has always had, with no new
// dependency and no configuration. It is what the contract is proven against, and it is
// what a single-instance deployment with no database configured keeps using.
type memStore struct {
	mu      sync.Mutex
	tokens  map[string]Token
	towers  map[string]Tower
	byKey   map[string]string // identity key hash -> tower id
	nextRev int64
	events  []Event
	nextEv  int64
}

// NewMemStore builds the in-process store.
func NewMemStore() Store {
	return &memStore{
		tokens: map[string]Token{},
		towers: map[string]Tower{},
		byKey:  map[string]string{},
	}
}

func (m *memStore) PutToken(t Token) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokens[t.ID] = t
	return nil
}

// PutTokenCapped counts and writes under one lock, so the check and the act cannot be
// separated by another goroutine.
func (m *memStore) PutTokenCapped(t Token, max int) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	live := 0
	for _, existing := range m.tokens {
		if existing.Owner == t.Owner && !now.After(existing.Expires) {
			live++
		}
	}
	if live >= max {
		return false, nil
	}
	m.tokens[t.ID] = t
	return true, nil
}

func (m *memStore) GetToken(id string) (Token, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tokens[id]
	return t, ok, nil
}

func (m *memStore) ConsumeToken(id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tokens[id]; !ok {
		return false, nil
	}
	delete(m.tokens, id)
	return true, nil
}

func (m *memStore) Admit(tokenID string, tw Tower) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tokens[tokenID]; !ok {
		return false, nil
	}
	// Every rejection is checked BEFORE anything is written, which is this store's
	// equivalent of a rollback: with the lock held there is no partial state to undo.
	if _, exists := m.byKey[tw.KeyHash]; exists {
		return false, errors.New("that identity key is already admitted")
	}
	if _, exists := m.towers[tw.ID]; exists {
		return false, errors.New("that Tower ID already exists")
	}
	delete(m.tokens, tokenID)
	m.nextRev++
	tw.Rev = m.nextRev
	m.towers[tw.ID] = tw
	m.byKey[tw.KeyHash] = tw.ID
	return true, nil
}

func (m *memStore) PutTower(tw Tower) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextRev++
	tw.Rev = m.nextRev
	m.towers[tw.ID] = tw
	m.byKey[tw.KeyHash] = tw.ID
	return nil
}

func (m *memStore) CASTower(tw Tower) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.towers[tw.ID]
	if !ok || cur.Rev != tw.Rev {
		return false, nil
	}
	m.nextRev++
	tw.Rev = m.nextRev
	m.towers[tw.ID] = tw
	m.byKey[tw.KeyHash] = tw.ID
	return true, nil
}

func (m *memStore) TowerByID(id string) (Tower, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tw, ok := m.towers[id]
	return tw, ok, nil
}

func (m *memStore) TowerByKey(keyHash string) (Tower, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.byKey[keyHash]
	if !ok {
		return Tower{}, false, nil
	}
	tw, ok := m.towers[id]
	return tw, ok, nil
}

func (m *memStore) AllTowers() ([]Tower, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Tower, 0, len(m.towers))
	for _, tw := range m.towers {
		out = append(out, tw)
	}
	return out, nil
}

func (m *memStore) TowersByOwner(owner string) ([]Tower, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Tower
	for _, tw := range m.towers {
		if tw.Owner == owner {
			out = append(out, tw)
		}
	}
	return out, nil
}

func (m *memStore) LiveTokens(owner string, now time.Time) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for id, t := range m.tokens {
		if t.Owner == owner && !now.After(t.Expires) {
			out = append(out, id)
		}
	}
	return out, nil
}

func (m *memStore) ReapTokens(now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, t := range m.tokens {
		if now.After(t.Expires) {
			delete(m.tokens, id)
		}
	}
	return nil
}

func (m *memStore) AppendEvent(ev Event) error {
	if err := ev.valid(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.appendEventLocked(ev)
	return nil
}

func (m *memStore) appendEventLocked(ev Event) {
	m.nextEv++
	ev.ID = m.nextEv
	m.events = append(m.events, ev)
}

func (m *memStore) Events(towerID string, limit int) ([]Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Event
	for _, ev := range m.events {
		if ev.TowerID == towerID {
			out = append(out, ev)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return laterEvent(out[i], out[j]) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memStore) CASTowerEvent(tw Tower, ev Event) (bool, error) {
	if err := ev.valid(); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.towers[tw.ID]
	if !ok || cur.Rev != tw.Rev {
		return false, nil
	}
	m.nextRev++
	tw.Rev = m.nextRev
	m.towers[tw.ID] = tw
	m.byKey[tw.KeyHash] = tw.ID
	m.appendEventLocked(ev)
	return true, nil
}

// laterEvent orders events the way every reader does: by time, then by id.
func laterEvent(a, b Event) bool {
	if !a.At.Equal(b.At) {
		return a.At.After(b.At)
	}
	return a.ID > b.ID
}

func (m *memStore) findLocked(towerID string, kinds []string, oldest bool) (Event, bool) {
	var best Event
	found := false
	for _, ev := range m.events {
		if ev.TowerID != towerID || !slices.Contains(kinds, ev.Kind) {
			continue
		}
		if !found || laterEvent(ev, best) != oldest {
			best, found = ev, true
		}
	}
	return best, found
}

func (m *memStore) EventsOfKinds(towerID string, kinds []string, since time.Time, limit int) ([]Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	want := map[string]bool{}
	for _, k := range kinds {
		want[k] = true
	}
	var out []Event
	for _, ev := range m.events {
		if ev.TowerID == towerID && want[ev.Kind] && ev.At.After(since) {
			out = append(out, ev)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return laterEvent(out[j], out[i]) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memStore) FindEvent(towerID string, kinds []string, oldest bool) (Event, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ev, ok := m.findLocked(towerID, kinds, oldest)
	return ev, ok, nil
}

func (m *memStore) AppendEventIfLast(ev Event, kinds []string, lastID int64) (bool, error) {
	if err := ev.valid(); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.findLocked(ev.TowerID, kinds, false)
	if (ok && cur.ID != lastID) || (!ok && lastID != 0) {
		return false, nil
	}
	m.appendEventLocked(ev)
	return true, nil
}

func (m *memStore) CountEvents(towerID, kind, reason, actor string, since time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, ev := range m.events {
		if ev.TowerID == towerID && ev.Kind == kind && !ev.At.Before(since) &&
			(reason == "" || ev.Reason == reason) && (actor == "" || ev.Actor == actor) {
			n++
		}
	}
	return n, nil
}

func (m *memStore) LinkSummaries(kinds []string) (map[string]LinkSummary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]LinkSummary{}
	for _, ev := range m.events {
		if slices.Contains(kinds, ev.Kind) {
			out[ev.TowerID] = foldSummary(out[ev.TowerID], ev)
		}
	}
	return out, nil
}

// foldSummary adds one event to a summary. Both stores build summaries through it, so they
// cannot disagree about what "first" and "newest" mean.
func foldSummary(s LinkSummary, ev Event) LinkSummary {
	if s.LastOf == nil {
		s.LastOf = map[string]Event{}
	}
	if s.First.Kind == "" || laterEvent(s.First, ev) {
		s.First = ev
	}
	if s.Latest.Kind == "" || laterEvent(ev, s.Latest) {
		s.Latest = ev
	}
	if cur, ok := s.LastOf[ev.Kind]; !ok || laterEvent(ev, cur) {
		s.LastOf[ev.Kind] = ev
	}
	return s
}

func (m *memStore) FindTransition(towerID string, from, to State) (Event, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var best Event
	found := false
	for _, ev := range m.events {
		if ev.TowerID == towerID && ev.From == from && ev.To == to && (!found || laterEvent(ev, best)) {
			best, found = ev, true
		}
	}
	return best, found, nil
}
