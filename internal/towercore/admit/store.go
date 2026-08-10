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

	// ReapTokens removes tokens that can no longer be redeemed.
	ReapTokens(now time.Time) error

	// LiveTokens lists an owner's unspent, unexpired tokens.
	//
	// Reaping alone does NOT bound the token space: it clears expired tokens, and says
	// nothing about a burst of live ones. Without this an authenticated account could mint
	// in a loop and grow the table without limit for a whole TTL window - a
	// database-filling vector behind nothing but a free registration.
	LiveTokens(owner string, now time.Time) ([]string, error)
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
