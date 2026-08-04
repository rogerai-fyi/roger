package deviceauth

// store.go is where a pending login LIVES between the request that issues it and the poll
// that redeems it.
//
// It used to live in two process-local maps, which cost us two things. A restart dropped
// every login in flight and reported the loss as "that code is not valid" - the rejection
// meant for a guesser, aimed at a person whose code was fine. And behind more than one
// broker instance the flow could not complete at all: the CLI polls whichever instance the
// load balancer picks while the human approves on whichever serves their browser, so the
// approval was written to one process's map and the poll read another's.
//
// TWO PROPERTIES SHAPE THIS INTERFACE.
//
// Consumption is ONE ATOMIC DECISION, never a read followed by a write. Two processes each
// reading "not consumed yet" and each proceeding is the classic double-spend, so every
// state change goes through CAS against the revision the caller read.
//
// What is written down is NOT ITSELF A CREDENTIAL. In process memory the codes were
// reachable only by the process; in a shared store they are reachable by anything holding
// the store's credential - a backup, a replica, whatever operational tooling can run a
// scan. So the Record carries only hashes, and there is deliberately nowhere in it to put
// a plaintext code even by accident.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// ErrUnavailable means the store could not be reached. It is DISTINCT from errRejected on
// purpose: a rejection is a statement about the caller's code, and telling a legitimate
// CLI that its perfectly good code is invalid because our backend blinked is both wrong
// and alarming. Callers surface this as "retry", never as "invalid".
var ErrUnavailable = errors.New("the login service is temporarily unavailable")

// ErrCorruptRecord means a record was found but could not be understood. It resolves to a
// refusal, never to an approval or a denial: an unreadable record is not evidence that
// somebody approved anything.
var ErrCorruptRecord = errors.New("the stored login record could not be read")

// Record is one pending login as it is persisted.
//
// DevHash and UserHash are sha256 hex digests of the device and user codes. The plaintext
// codes exist only in the response to the CLI and in the human's hands.
type Record struct {
	DevHash   string        `json:"dev_hash"`
	UserHash  string        `json:"user_hash"`
	BoundKey  string        `json:"bound_key"`
	Status    Status        `json:"status"`
	Account   string        `json:"account,omitempty"`
	Requested time.Time     `json:"requested"`
	Expires   time.Time     `json:"expires"`
	LastPoll  time.Time     `json:"last_poll,omitempty"`
	Interval  time.Duration `json:"interval"`
	Consumed  bool          `json:"consumed"`
	// Rev is the revision the record was read at. CAS applies a write only if the stored
	// revision still matches, so a writer working from a superseded read loses outright
	// rather than clobbering the winner.
	Rev int64 `json:"rev"`
}

// Store is where pending logins live. Every method reports a transport failure as an
// error; NO implementation may substitute a local fallback, because a fallback is exactly
// the split-brain the shared store exists to remove.
type Store interface {
	// Create records a new pending login under both of its indexes.
	Create(r Record) error

	// ByDevice and ByUser resolve a record. An absent record is (false, nil) - a miss is
	// not an error.
	ByDevice(devHash string) (Record, bool, error)
	ByUser(userHash string) (Record, bool, error)

	// CAS writes r if the stored revision still equals r.Rev, and reports whether THIS
	// call wrote it. A false return is not an error: it means somebody else got there
	// first, which is a legitimate outcome the caller must handle.
	CAS(r Record) (bool, error)

	// Delete removes a record and both of its indexes.
	Delete(devHash string) error

	// Budget reports how much of a submitter's guessing allowance is spent, and Penalize
	// spends one more and returns the new total. The allowance is PER SUBMITTER: a single
	// global counter would let one attacker lock every other person out of signing in,
	// turning an anti-guessing control into a denial of service.
	Budget(submitter string) (int, error)
	Penalize(submitter string, ttl time.Duration) (int, error)

	// Reap removes records that can no longer be used. Without it the store only grows,
	// and any signed caller could raise its size without bound.
	Reap(now time.Time) error
}

// hashCode is how a plaintext code becomes what we are willing to write down.
func hashCode(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// --- the in-process implementation ----------------------------------------

// memStore is the default: the single-instance behaviour the broker has always had, with
// no new dependency and no configuration. It is also what every Store contract test runs
// against, so the contract itself is proven independently of any server.
//
// A restart still loses its contents - a map cannot survive the process holding it. What
// changes is that the loss is now REPORTABLE rather than indistinguishable from a bad
// code, because the Flow can tell "no record" from "store said no".
type memStore struct {
	mu      sync.Mutex
	byDev   map[string]Record
	byUser  map[string]string // user hash -> device hash
	wrong   map[string]int
	nextRev int64
}

// NewMemStore builds the in-process store.
func NewMemStore() Store {
	return &memStore{
		byDev:  map[string]Record{},
		byUser: map[string]string{},
		wrong:  map[string]int{},
	}
}

func (m *memStore) Create(r Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextRev++
	r.Rev = m.nextRev
	m.byDev[r.DevHash] = r
	m.byUser[r.UserHash] = r.DevHash
	return nil
}

func (m *memStore) ByDevice(devHash string) (Record, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.byDev[devHash]
	return r, ok, nil
}

func (m *memStore) ByUser(userHash string) (Record, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	dev, ok := m.byUser[userHash]
	if !ok {
		return Record{}, false, nil
	}
	r, ok := m.byDev[dev]
	return r, ok, nil
}

func (m *memStore) CAS(r Record) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.byDev[r.DevHash]
	if !ok || cur.Rev != r.Rev {
		return false, nil
	}
	m.nextRev++
	r.Rev = m.nextRev
	m.byDev[r.DevHash] = r
	m.byUser[r.UserHash] = r.DevHash
	return true, nil
}

func (m *memStore) Delete(devHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.byDev[devHash]; ok {
		delete(m.byUser, r.UserHash)
	}
	delete(m.byDev, devHash)
	return nil
}

func (m *memStore) Budget(submitter string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.wrong[submitter], nil
}

func (m *memStore) Penalize(submitter string, _ time.Duration) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.wrong[submitter]++
	return m.wrong[submitter], nil
}

func (m *memStore) Reap(now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for dev, r := range m.byDev {
		if r.Consumed || now.After(r.Expires) {
			delete(m.byUser, r.UserHash)
			delete(m.byDev, dev)
		}
	}
	return nil
}
