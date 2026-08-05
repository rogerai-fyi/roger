package towerenroll

// store.go is where an in-flight enrollment lives.
//
// Two pieces of state, and neither may be process-local.
//
// A CHALLENGE issued by one instance has to be answerable on another, or enrollment behind
// a load balancer works only when both calls happen to land on the same process. It must
// still be spendable exactly ONCE across the whole deployment, because the one-time nonce
// is the only thing stopping a replay - a signature stays valid forever.
//
// A COMMITTED OUTCOME is the more serious of the two. It is what makes the spec's "the
// response was lost" retry work. If it lives only in the process that made it, then after
// a restart the token has been consumed while nothing remembers what it bought: the
// operator's retry is refused as a spent token and their Tower identity is unreachable
// without an administrator. That is precisely the situation idempotency exists to prevent.

import (
	"errors"
	"sync"
	"time"
)

// ErrUnavailable means the enrollment store could not be reached. Distinct from a rejection
// on purpose: "we cannot record this" and "your enrollment is invalid" are different facts,
// and an operator told the second when the first is true goes looking for a problem that is
// not theirs.
var ErrUnavailable = errors.New("enrollment is temporarily unavailable")

// Committed is the outcome of an enrollment that already happened.
//
// The certificate is kept as DER rather than a parsed value: a retry has to hand back the
// SAME certificate, and re-issuing one would mint a second credential for a Tower that
// already has one.
type Committed struct {
	TowerID string `json:"tower_id"`
	// KeyHash is re-proved on every retry, so a transaction id observed on the wire cannot
	// be used to have somebody else's Tower re-issued to a different key.
	KeyHash string `json:"key_hash"`
	CertDER []byte `json:"cert_der"`
}

// Store holds in-flight enrollment state.
type Store interface {
	// PutChallenge records an unanswered challenge.
	PutChallenge(c Challenge) error
	// TakeChallenge atomically returns AND removes a challenge, so a nonce is spendable
	// exactly once across the deployment rather than once per process.
	TakeChallenge(nonce string) (Challenge, bool, error)

	// Committed returns a completed enrollment by transaction id.
	Committed(txnID string) (Committed, bool, error)
	// PutCommitted records one.
	PutCommitted(txnID string, c Committed) error

	// Reap drops challenges that can no longer be answered, so the nonce space cannot be
	// grown without bound by anybody holding a token.
	Reap(now time.Time) error
}

// --- the in-process implementation ----------------------------------------

// memStore is the default: the single-instance deployment, with no new dependency and no
// configuration. It is what the contract is proven against.
type memStore struct {
	mu         sync.Mutex
	challenges map[string]Challenge
	committed  map[string]Committed
}

// NewMemStore builds the in-process store.
func NewMemStore() Store {
	return &memStore{
		challenges: map[string]Challenge{},
		committed:  map[string]Committed{},
	}
}

func (m *memStore) PutChallenge(c Challenge) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.challenges[c.Nonce] = c
	return nil
}

func (m *memStore) TakeChallenge(nonce string) (Challenge, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.challenges[nonce]
	if !ok {
		return Challenge{}, false, nil
	}
	// Removed as it is read: the caller never gets a window in which to read, decide, and
	// delete separately, which is where a replay would fit.
	delete(m.challenges, nonce)
	return c, true, nil
}

func (m *memStore) Committed(txnID string) (Committed, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.committed[txnID]
	return c, ok, nil
}

func (m *memStore) PutCommitted(txnID string, c Committed) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.committed[txnID] = c
	return nil
}

func (m *memStore) Reap(now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for nonce, c := range m.challenges {
		if now.After(c.Expires) {
			delete(m.challenges, nonce)
		}
	}
	return nil
}
