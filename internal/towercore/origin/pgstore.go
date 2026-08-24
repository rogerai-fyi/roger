package origin

import (
	"database/sql"
	"errors"
	"time"

	"rogerai.fm/roger/v6/internal/pgmigrate"
)

// The privacy promise is STRUCTURAL: no stored row ever carries an attempt id BESIDE a
// country, so there is nothing in the database to join a consumer (reachable via the
// attempt id the billing ledger keys) to where their request came from. Two tables enforce
// it:
//
//   - tower_origin_seen holds ONLY the attempt id, for idempotency - a retried open counts
//     once. It knows nothing about country or Tower.
//   - tower_origin_events holds the country and Tower under a surrogate id, with NO attempt
//     id. It is what ByTower counts.
//
// A country therefore cannot be traced back to an attempt, and thus not to a wallet, inside
// the store - the view can answer "how much from where" and the schema itself cannot answer
// "who".
const schema = `
CREATE TABLE IF NOT EXISTS rogerai.tower_origin_seen (
    attempt_id TEXT PRIMARY KEY,
    at         TIMESTAMPTZ NOT NULL
);
CREATE TABLE IF NOT EXISTS rogerai.tower_origin_events (
    id       BIGSERIAL PRIMARY KEY,
    tower_id TEXT        NOT NULL,
    country  TEXT        NOT NULL,
    at       TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS tower_origin_events_tower ON rogerai.tower_origin_events (tower_id, at);
`

// PGStore is the durable, fleet-wide origin tally: every instance inserts, and ByTower reads
// the union - so the detail view sees demand recorded by any instance.
type PGStore struct{ db *sql.DB }

// NewPGStore applies the schema and returns the durable origin tally.
func NewPGStore(db *sql.DB) (*PGStore, error) {
	if db == nil {
		return nil, errors.New("a durable origin tally needs a database handle")
	}
	if err := pgmigrate.Apply(db, schema); err != nil {
		return nil, err
	}
	return &PGStore{db: db}, nil
}

func (p *PGStore) Record(towerID, attemptID, country string, at time.Time) error {
	if towerID == "" || attemptID == "" {
		return nil
	}
	// Claim the attempt id first. If it was already seen, this inserts nothing and we do NOT
	// write a country event - a retried open counts once. The two writes are ordered so a
	// crash between them can only UNDER-count (a claimed attempt with no event), never
	// double-count, and never leave a country row joinable to the claimed id.
	res, err := p.db.Exec(`
		INSERT INTO rogerai.tower_origin_seen (attempt_id, at)
		VALUES ($1, $2) ON CONFLICT (attempt_id) DO NOTHING`,
		attemptID, at.UTC())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil // already counted
	}
	_, err = p.db.Exec(`
		INSERT INTO rogerai.tower_origin_events (tower_id, country, at)
		VALUES ($1, $2, $3)`,
		towerID, normCountry(country), at.UTC())
	return err
}

func (p *PGStore) ByTower(towerID string, since time.Time) ([]Tally, error) {
	rows, err := p.db.Query(`
		SELECT country, COUNT(*)
		  FROM rogerai.tower_origin_events
		  WHERE tower_id = $1 AND at >= $2
		  GROUP BY country`,
		towerID, since.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Tally
	for rows.Next() {
		var t Tally
		if serr := rows.Scan(&t.Country, &t.Attempts); serr != nil {
			return nil, serr
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Sort in Go, not SQL: a database's default collation orders mixed-case ("US" vs
	// "unknown") differently from Go's byte-wise sort, which would make the two stores
	// disagree on order. Sorting here keeps mem and Postgres identical.
	sortTallies(out)
	return out, nil
}
