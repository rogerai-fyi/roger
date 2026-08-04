package toweradmit

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
	// GetToken reads a token WITHOUT consuming it, so a rejected enrollment does not burn
	// the token its legitimate holder still needs.
	GetToken(id string) (Token, bool, error)
	// ConsumeToken atomically removes a token and reports whether THIS call removed it.
	// It is called only once every other check has passed, which is what makes redemption
	// one-time across the deployment while leaving a failed attempt harmless: of two
	// concurrent enrollments that both validate, exactly one can consume.
	ConsumeToken(id string) (bool, error)

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

	// ReapTokens removes tokens that can no longer be redeemed, so the token space cannot
	// be grown without bound by anybody holding an account.
	ReapTokens(now time.Time) error
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
