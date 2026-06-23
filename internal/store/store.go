// Package store is the broker's persistence boundary - deliberately tiny so the
// backend is swappable (in-memory now; Postgres for DO; anything later). Only the
// money/audit state persists; live node/tunnel state stays in the broker's memory
// (nodes re-register on reconnect).
package store

import (
	"sync"

	"github.com/bownux/rogerai/internal/protocol"
)

type Store interface {
	// BalanceOf returns the user's credit balance, seeding a new user with `seed`.
	BalanceOf(user string, seed float64) (float64, error)
	// Settle atomically debits the user by cost, credits the node's owner share,
	// and appends the lineage receipt. Returns the user's new balance.
	Settle(user, node string, cost, ownerShare float64, rec protocol.UsageReceipt) (newBalance float64, err error)
	// EarningsOf returns a node's accrued (unpaid) owner credits.
	EarningsOf(node string) (float64, error)
	// AddCredits tops a user up (Stripe webhook in P1).
	AddCredits(user string, amount float64) (float64, error)
	// MarkProcessed records an idempotency key (e.g. a Stripe session id) and
	// reports whether it was newly added (true) vs already seen (false) - makes
	// the Stripe webhook safe against at-least-once redelivery (no double-credit).
	MarkProcessed(key string) (firstTime bool, err error)
	// CreditOnce atomically records the idempotency key AND credits the user in a
	// single transaction: returns credited=true only the first time. Prevents both
	// double-credit (redelivery) and lost-credit (mark succeeds, credit fails).
	CreditOnce(key, user string, amount float64) (credited bool, newBalance float64, err error)
	Close() error
}

// Mem is the in-memory implementation (single-process, non-durable).
type Mem struct {
	mu        sync.Mutex
	wallet    map[string]float64
	earnings  map[string]float64
	audit     []protocol.UsageReceipt
	processed map[string]bool
}

func NewMem() *Mem {
	return &Mem{wallet: map[string]float64{}, earnings: map[string]float64{}, processed: map[string]bool{}}
}

func (m *Mem) BalanceOf(user string, seed float64) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.wallet[user]; !ok {
		m.wallet[user] = seed
	}
	return m.wallet[user], nil
}

func (m *Mem) Settle(user, node string, cost, ownerShare float64, rec protocol.UsageReceipt) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.wallet[user] -= cost
	m.earnings[node] += ownerShare
	m.audit = append(m.audit, rec)
	return m.wallet[user], nil
}

func (m *Mem) EarningsOf(node string) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.earnings[node], nil
}

func (m *Mem) AddCredits(user string, amount float64) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.wallet[user] += amount
	return m.wallet[user], nil
}

func (m *Mem) MarkProcessed(key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.processed[key] {
		return false, nil
	}
	m.processed[key] = true
	return true, nil
}

func (m *Mem) CreditOnce(key, user string, amount float64) (bool, float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.processed[key] {
		return false, m.wallet[user], nil
	}
	m.processed[key] = true
	m.wallet[user] += amount
	return true, m.wallet[user], nil
}

func (m *Mem) Close() error { return nil }
