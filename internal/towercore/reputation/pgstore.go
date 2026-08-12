package reputation

import (
	"database/sql"
	"errors"
	"time"

	"rogerai.fm/roger/v5/internal/pgmigrate"
)

// schema is applied on first use. TABLES only - `rogerai` is provisioned by an admin and
// owned by the app's least-privilege user, and CREATE SCHEMA IF NOT EXISTS fails with
// "permission denied for database" even when the schema already exists.
const schema = `
CREATE TABLE IF NOT EXISTS rogerai.tower_outcomes (
    tower_id   TEXT        NOT NULL,
    attempt_id TEXT        NOT NULL,
    outcome    TEXT        NOT NULL,
    at         TIMESTAMPTZ NOT NULL,
    -- One row per (tower, attempt, outcome): an attempt has one terminal outcome, and the
    -- write that records it must be idempotent under a retry - double-counting is how a
    -- reliable Tower earns a reputation it did not.
    PRIMARY KEY (tower_id, attempt_id, outcome)
);
-- The windowed scans: a Tower's recent outcomes, and the fleet's, without reading history.
CREATE INDEX IF NOT EXISTS tower_outcomes_tower_at ON rogerai.tower_outcomes (tower_id, at);
CREATE INDEX IF NOT EXISTS tower_outcomes_at ON rogerai.tower_outcomes (at);
`

// PGStore is the durable reputation ledger, shared across brokers.
//
// Durable and shared for the same reason the ack store is: an attempt authorized on one
// instance settles on whichever the Tower reached, and a rate computed per-process would see
// each broker's fraction of the evidence and mistake it for the whole.
type PGStore struct{ db *sql.DB }

// NewPGStore prepares the durable ledger.
func NewPGStore(db *sql.DB) (*PGStore, error) {
	if db == nil {
		return nil, errors.New("a durable reputation ledger needs a database handle")
	}
	if err := pgmigrate.Apply(db, schema); err != nil {
		return nil, err
	}
	return &PGStore{db: db}, nil
}

func (p *PGStore) Record(e Event) error {
	if err := checkEvent(e); err != nil {
		return err
	}
	// DO NOTHING is the idempotency rule, the same clause the ack store uses: a retry is a
	// no-op rather than a second count.
	_, err := p.db.Exec(`
		INSERT INTO rogerai.tower_outcomes (tower_id, attempt_id, outcome, at)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (tower_id, attempt_id, outcome) DO NOTHING`,
		e.TowerID, e.AttemptID, string(e.Outcome), e.At.UTC())
	return err
}

func (p *PGStore) Tally(towerID string, since time.Time) (Tally, error) {
	rows, err := p.db.Query(`
		SELECT outcome, count(*) FROM rogerai.tower_outcomes
		 WHERE tower_id = $1 AND at >= $2 GROUP BY outcome`, towerID, since.UTC())
	if err != nil {
		return Tally{}, err
	}
	defer rows.Close()
	t := Tally{TowerID: towerID}
	return scanTally(rows, t)
}

func (p *PGStore) FleetTally(since time.Time) (Tally, error) {
	rows, err := p.db.Query(`
		SELECT outcome, count(*) FROM rogerai.tower_outcomes
		 WHERE at >= $1 GROUP BY outcome`, since.UTC())
	if err != nil {
		return Tally{}, err
	}
	defer rows.Close()
	return scanTally(rows, Tally{})
}

func scanTally(rows *sql.Rows, t Tally) (Tally, error) {
	for rows.Next() {
		var outcome string
		var n int
		if err := rows.Scan(&outcome, &n); err != nil {
			return Tally{}, err
		}
		// The counts arrive grouped, so add each group's total at once rather than one row at
		// a time - but through the SAME mapping the mem store uses, so an outcome cannot land
		// in different columns on the two paths.
		for i := 0; i < n; i++ {
			addOutcome(&t, Outcome(outcome))
		}
	}
	return t, rows.Err()
}

func (p *PGStore) Reap(before time.Time) (int64, error) {
	res, err := p.db.Exec(`DELETE FROM rogerai.tower_outcomes WHERE at < $1`, before.UTC())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
