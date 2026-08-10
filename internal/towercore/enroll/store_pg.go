package enroll

// store_pg.go adapts the durable enrollment tables (which live with the admission registry,
// sharing its handle and its migration path) to this package's Store.
//
// The adapter exists so the dependency runs one way: towerenroll imports toweradmit, never
// the reverse. toweradmit therefore stores plain values and this file gives them meaning.

import (
	"errors"
	"time"

	"rogerai.fm/roger/v5/internal/towercore/admit"
)

// pgStore is the durable in-flight enrollment state.
type pgStore struct{ inner *admit.PGEnrollStore }

// NewPGStore returns a durable Store over the given enrollment tables.
func NewPGStore(inner *admit.PGEnrollStore) (Store, error) {
	if inner == nil {
		return nil, errors.New("durable enrollment state needs its store")
	}
	return &pgStore{inner: inner}, nil
}

func (p *pgStore) PutChallenge(c Challenge) error {
	if err := p.inner.PutChallengeRow(c.Nonce, c.Subject, c.Purpose, c.Expires); err != nil {
		return unavailableStore(err)
	}
	return nil
}

func (p *pgStore) TakeChallenge(nonce string) (Challenge, bool, error) {
	row, ok, err := p.inner.TakeChallengeRow(nonce)
	if err != nil {
		return Challenge{}, false, unavailableStore(err)
	}
	if !ok {
		return Challenge{}, false, nil
	}
	return Challenge{
		Nonce: row.Nonce, Subject: row.Subject, Purpose: row.Purpose, Expires: row.Expires,
	}, true, nil
}

func (p *pgStore) Committed(txnID string) (Committed, bool, error) {
	row, ok, err := p.inner.CommittedRow(txnID)
	if err != nil {
		return Committed{}, false, unavailableStore(err)
	}
	if !ok {
		return Committed{}, false, nil
	}
	return Committed{TowerID: row.TowerID, KeyHash: row.KeyHash, CertDER: row.CertDER}, true, nil
}

func (p *pgStore) PutCommitted(txnID string, c Committed) error {
	if err := p.inner.PutCommittedRow(txnID, c.TowerID, c.KeyHash, c.CertDER); err != nil {
		return unavailableStore(err)
	}
	return nil
}

func (p *pgStore) Reap(now time.Time) error {
	if err := p.inner.ReapChallenges(now); err != nil {
		return unavailableStore(err)
	}
	return nil
}

// unavailableStore keeps the storage layer's failures distinguishable from a rejection, so
// an operator is never told their enrollment is invalid because our database blinked.
func unavailableStore(err error) error {
	if errors.Is(err, ErrUnavailable) {
		return err
	}
	return errors.Join(ErrUnavailable, err)
}
