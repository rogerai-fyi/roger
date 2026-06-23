// Package store is the broker's persistence boundary - deliberately tiny so the
// backend is swappable (in-memory now; Postgres for DO; anything later). Only the
// money/audit state persists; live node/tunnel state stays in the broker's memory
// (nodes re-register on reconnect).
package store

import (
	"sort"
	"sync"

	"github.com/bownux/rogerai/internal/protocol"
)

// Entry is one settled request, as surfaced to dashboards. It carries the real
// (un-pseudonymized) user + node, the billed cost, and the owner's share, so a
// consumer can see spend and an owner can see earnings from the same record.
type Entry struct {
	RequestID        string  `json:"request_id"`
	User             string  `json:"user"`
	Node             string  `json:"node"`
	Model            string  `json:"model"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	Cost             float64 `json:"cost"`        // credits the consumer paid
	OwnerShare       float64 `json:"owner_share"` // credits credited to the node owner
	TS               int64   `json:"ts"`
}

type Store interface {
	// BalanceOf returns the user's credit balance, seeding a new user with `seed`.
	BalanceOf(user string, seed float64) (float64, error)
	// Settle atomically debits the user by cost, credits the node's owner share,
	// and appends the lineage receipt. Returns the user's new balance.
	Settle(user, node string, cost, ownerShare float64, rec protocol.UsageReceipt) (newBalance float64, err error)
	// EarningsOf returns a node's accrued (unpaid) owner credits.
	EarningsOf(node string) (float64, error)
	// SpendOf returns a user's lifetime total spend (sum of settled costs).
	SpendOf(user string) (float64, error)
	// RecentByUser returns a user's most-recent settled requests (newest first).
	RecentByUser(user string, limit int) ([]Entry, error)
	// RecentByNode returns a node's most-recent settled requests (newest first).
	RecentByNode(node string, limit int) ([]Entry, error)
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
	spend     map[string]float64
	entries   []Entry
	processed map[string]bool
}

func NewMem() *Mem {
	return &Mem{wallet: map[string]float64{}, earnings: map[string]float64{}, spend: map[string]float64{}, processed: map[string]bool{}}
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
	m.spend[user] += cost
	m.entries = append(m.entries, Entry{
		RequestID: rec.RequestID, User: user, Node: node, Model: rec.Model,
		PromptTokens: rec.PromptTokens, CompletionTokens: rec.CompletionTokens,
		Cost: cost, OwnerShare: ownerShare, TS: rec.TS,
	})
	return m.wallet[user], nil
}

func (m *Mem) EarningsOf(node string) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.earnings[node], nil
}

func (m *Mem) SpendOf(user string) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.spend[user], nil
}

func (m *Mem) RecentByUser(user string, limit int) ([]Entry, error) {
	return m.recent(func(e Entry) bool { return e.User == user }, limit), nil
}

func (m *Mem) RecentByNode(node string, limit int) ([]Entry, error) {
	return m.recent(func(e Entry) bool { return e.Node == node }, limit), nil
}

// recent returns the most-recent entries matching pred, newest first, capped.
func (m *Mem) recent(pred func(Entry) bool, limit int) []Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Entry
	for _, e := range m.entries {
		if pred(e) {
			out = append(out, e)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TS > out[j].TS })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
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
