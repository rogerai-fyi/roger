package reputation

import (
	"database/sql"
	"errors"
	"time"

	"rogerai.fm/roger/v6/internal/pgmigrate"
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
-- WHICH STATION this outcome concerns, added after the table shipped, so it arrives as an
-- ALTER rather than in the CREATE above - a deployment that already has the table would
-- otherwise never see the column, because CREATE TABLE IF NOT EXISTS is a no-op on it.
--
-- NOT in the primary key, deliberately. The key is the idempotency rule ("an attempt has one
-- terminal outcome, and a retry must not count twice") and widening it with a fourth column
-- would let two writes that disagreed about the Station both land - which is double-counting
-- wearing an attribution's clothes. The station is a fact ABOUT the row; the first writer's
-- value stands, and every writer passes the same one.
--
-- DEFAULT '' rather than NULL because the empty string is a real answer here - see Event -
-- and because a nullable column would make every reader choose between two spellings of
-- "no Station" and eventually one of them would be handled and the other would not.
ALTER TABLE rogerai.tower_outcomes ADD COLUMN IF NOT EXISTS station_id TEXT NOT NULL DEFAULT '';
-- No index on station_id: it is only ever read grouped inside one Tower's window, which the
-- (tower_id, at) index above already narrows to a few rows. An index whose selectivity is
-- "the stations behind one tower" would be paid for on every write and read by nothing.
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
		INSERT INTO rogerai.tower_outcomes (tower_id, attempt_id, outcome, at, station_id)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (tower_id, attempt_id, outcome) DO NOTHING`,
		e.TowerID, e.AttemptID, string(e.Outcome), e.At.UTC(), e.StationID)
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

// TallyByStation groups one Tower's window by station AND outcome in a single scan - the same
// rows Tally reads, cut a second way, so the two cannot disagree about the window.
func (p *PGStore) TallyByStation(towerID string, since time.Time) (map[string]Tally, error) {
	rows, err := p.db.Query(`
		SELECT station_id, outcome, count(*) FROM rogerai.tower_outcomes
		 WHERE tower_id = $1 AND at >= $2 GROUP BY station_id, outcome`, towerID, since.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Tally{}
	for rows.Next() {
		var stationID, outcome string
		var n int
		if serr := rows.Scan(&stationID, &outcome, &n); serr != nil {
			return nil, serr
		}
		t := out[stationID]
		t.TowerID = towerID
		// Through addOutcome, like scanTally, so an outcome cannot land in one column here and
		// a different one on the memory path.
		for i := 0; i < n; i++ {
			addOutcome(&t, Outcome(outcome))
		}
		out[stationID] = t
	}
	return out, rows.Err()
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
