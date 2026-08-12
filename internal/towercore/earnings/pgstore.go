package earnings

import (
	"database/sql"
	"errors"
	"time"

	"rogerai.fm/roger/v5/internal/pgmigrate"
)

const schema = `
CREATE TABLE IF NOT EXISTS rogerai.tower_earnings (
    attempt_id   TEXT PRIMARY KEY,
    tower_id     TEXT        NOT NULL,
    owner        TEXT        NOT NULL,
    model        TEXT        NOT NULL DEFAULT '',
    usage_in     BIGINT      NOT NULL DEFAULT 0,
    usage_out    BIGINT      NOT NULL DEFAULT 0,
    micros       BIGINT      NOT NULL,
    corroborated BOOLEAN     NOT NULL DEFAULT false,
    at           TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS tower_earnings_owner ON rogerai.tower_earnings (owner, at);
CREATE INDEX IF NOT EXISTS tower_earnings_at ON rogerai.tower_earnings (at);

CREATE TABLE IF NOT EXISTS rogerai.tower_payouts (
    payout_id TEXT PRIMARY KEY,
    owner     TEXT        NOT NULL,
    micros    BIGINT      NOT NULL,
    at        TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS tower_payouts_owner ON rogerai.tower_payouts (owner, at);
`

// PGStore is the durable funding ledger, shared across brokers.
//
// Durable and shared because an attempt settles on whichever instance the Tower reached, and a
// payout is decided by whichever one runs the disbursement - the debt and its repayment must
// agree across the fleet, or one instance pays what another already paid.
type PGStore struct{ db *sql.DB }

// NewPGStore prepares the durable funding ledger.
func NewPGStore(db *sql.DB) (*PGStore, error) {
	if db == nil {
		return nil, errors.New("a durable funding ledger needs a database handle")
	}
	if err := pgmigrate.Apply(db, schema); err != nil {
		return nil, err
	}
	return &PGStore{db: db}, nil
}

func (p *PGStore) Accrue(a Accrual) error {
	if err := checkAccrual(a); err != nil {
		return err
	}
	// DO NOTHING: an attempt earns once, whatever races or retries. This is the exactly-once
	// guarantee the whole design rests on - the money cannot be accrued twice for one attempt.
	_, err := p.db.Exec(`
		INSERT INTO rogerai.tower_earnings
		    (attempt_id, tower_id, owner, model, usage_in, usage_out, micros, corroborated, at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (attempt_id) DO NOTHING`,
		a.AttemptID, a.TowerID, a.Owner, a.Model, a.UsageIn, a.UsageOut, a.Micros,
		a.Corroborated, a.At.UTC())
	return err
}

func (p *PGStore) RecordPayout(owner, payoutID string, micros int64, at time.Time) error {
	if owner == "" || payoutID == "" {
		return errPayout
	}
	if micros < 0 {
		return errNegativePayout
	}
	// ON CONFLICT DO NOTHING keeps a retried disbursement idempotent, but a no-op could also
	// mean a DIFFERENT payout reused this id - which would silently lose a real debt reduction.
	// So we detect the no-op (RETURNING yields no row) and, only then, read the existing row: a
	// match is an idempotent retry (fine); a mismatch is a collision to surface, not swallow.
	var inserted string
	err := p.db.QueryRow(`
		INSERT INTO rogerai.tower_payouts (payout_id, owner, micros, at)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (payout_id) DO NOTHING
		RETURNING payout_id`,
		payoutID, owner, micros, at.UTC()).Scan(&inserted)
	if err == nil {
		return nil // inserted fresh
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var priorOwner string
	var priorMicros int64
	if serr := p.db.QueryRow(`SELECT owner, micros FROM rogerai.tower_payouts WHERE payout_id = $1`,
		payoutID).Scan(&priorOwner, &priorMicros); serr != nil {
		return serr
	}
	if priorOwner != owner || priorMicros != micros {
		return errPayoutConflict
	}
	return nil
}

func (p *PGStore) OwedTo(owner string, since time.Time) (OwedByOwner, error) {
	out := OwedByOwner{Owner: owner}
	err := p.db.QueryRow(`
		SELECT COALESCE(SUM(micros),0), COUNT(*) FROM rogerai.tower_earnings
		 WHERE owner = $1 AND at >= $2`, owner, since.UTC()).Scan(&out.Accrued, &out.Attempts)
	if err != nil {
		return OwedByOwner{}, err
	}
	err = p.db.QueryRow(`
		SELECT COALESCE(SUM(micros),0) FROM rogerai.tower_payouts
		 WHERE owner = $1 AND at >= $2`, owner, since.UTC()).Scan(&out.Paid)
	if err != nil {
		return OwedByOwner{}, err
	}
	return out, nil
}

func (p *PGStore) Reap(before time.Time) (int64, error) {
	res, err := p.db.Exec(`DELETE FROM rogerai.tower_earnings WHERE at < $1`, before.UTC())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if _, err := p.db.Exec(`DELETE FROM rogerai.tower_payouts WHERE at < $1`, before.UTC()); err != nil {
		return n, err
	}
	return n, nil
}
