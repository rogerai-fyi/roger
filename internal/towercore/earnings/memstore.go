package earnings

import (
	"sync"
	"time"
)

type memStore struct {
	mu       sync.Mutex
	accruals map[string]Accrual   // attempt id -> accrual (idempotent)
	payouts  map[string]payoutRow // payout id -> row (idempotent)
}

type payoutRow struct {
	owner  string
	micros int64
	at     time.Time
}

// NewMemStore builds the in-process funding ledger.
func NewMemStore() Store {
	return &memStore{accruals: map[string]Accrual{}, payouts: map[string]payoutRow{}}
}

func (m *memStore) Accrue(a Accrual) error {
	if err := checkAccrual(a); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.accruals[a.AttemptID]; exists {
		return nil // an attempt earns once
	}
	m.accruals[a.AttemptID] = a
	return nil
}

func (m *memStore) RecordPayout(owner, payoutID string, micros int64, at time.Time) error {
	if owner == "" || payoutID == "" {
		return errPayout
	}
	if micros < 0 {
		return errNegativePayout
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.payouts[payoutID]; exists {
		return nil // idempotent: a retried disbursement is not a second debt reduction
	}
	m.payouts[payoutID] = payoutRow{owner: owner, micros: micros, at: at}
	return nil
}

func (m *memStore) OwedTo(owner string, since time.Time) (OwedByOwner, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := OwedByOwner{Owner: owner}
	for _, a := range m.accruals {
		if a.Owner == owner && !a.At.Before(since) {
			out.Accrued += a.Micros
			out.Attempts++
		}
	}
	for _, p := range m.payouts {
		if p.owner == owner && !p.at.Before(since) {
			out.Paid += p.micros
		}
	}
	return out, nil
}

func (m *memStore) Reap(before time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for id, a := range m.accruals {
		if a.At.Before(before) {
			delete(m.accruals, id)
			n++
		}
	}
	for id, p := range m.payouts {
		if p.at.Before(before) {
			delete(m.payouts, id)
		}
	}
	return n, nil
}
