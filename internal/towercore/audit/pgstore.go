package audit

import (
	"database/sql"
	"errors"
	"time"

	"rogerai.fm/roger/v5/internal/pgmigrate"
)

const schema = `
CREATE TABLE IF NOT EXISTS rogerai.tower_audit_wanted (
    attempt_id      TEXT PRIMARY KEY,
    tower_id        TEXT        NOT NULL,
    station_id      TEXT        NOT NULL,
    request_digest  TEXT        NOT NULL,
    response_digest TEXT        NOT NULL,
    usage_in        BIGINT      NOT NULL DEFAULT 0,
    usage_out       BIGINT      NOT NULL DEFAULT 0,
    deadline        TIMESTAMPTZ NOT NULL
);
ALTER TABLE rogerai.tower_audit_wanted ADD COLUMN IF NOT EXISTS usage_in  BIGINT NOT NULL DEFAULT 0;
ALTER TABLE rogerai.tower_audit_wanted ADD COLUMN IF NOT EXISTS usage_out BIGINT NOT NULL DEFAULT 0;
ALTER TABLE rogerai.tower_audit_wanted ADD COLUMN IF NOT EXISTS wire_in   BIGINT NOT NULL DEFAULT 0;
ALTER TABLE rogerai.tower_audit_wanted ADD COLUMN IF NOT EXISTS wire_out  BIGINT NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS tower_audit_wanted_tower ON rogerai.tower_audit_wanted (tower_id, deadline);
CREATE INDEX IF NOT EXISTS tower_audit_wanted_deadline ON rogerai.tower_audit_wanted (deadline);
`

// PGStore is the durable wanted list.
type PGStore struct{ db *sql.DB }

// NewPGStore prepares the durable wanted list.
func NewPGStore(db *sql.DB) (*PGStore, error) {
	if db == nil {
		return nil, errors.New("a durable audit list needs a database handle")
	}
	if err := pgmigrate.Apply(db, schema); err != nil {
		return nil, err
	}
	return &PGStore{db: db}, nil
}

func (p *PGStore) Want(w Wanted) error {
	if err := check(w); err != nil {
		return err
	}
	// DO NOTHING: wanted once, even if two instances select the same attempt at once.
	_, err := p.db.Exec(`
		INSERT INTO rogerai.tower_audit_wanted
		    (attempt_id, tower_id, station_id, request_digest, response_digest, usage_in, usage_out, wire_in, wire_out, deadline)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (attempt_id) DO NOTHING`,
		w.AttemptID, w.TowerID, w.StationID, w.RequestDigest, w.ResponseDigest,
		w.UsageIn, w.UsageOut, w.WireIn, w.WireOut, w.Deadline.UTC())
	return err
}

func (p *PGStore) Pending(towerID string, now time.Time) ([]Wanted, error) {
	rows, err := p.db.Query(`
		SELECT attempt_id, tower_id, station_id, request_digest, response_digest, usage_in, usage_out, wire_in, wire_out, deadline
		  FROM rogerai.tower_audit_wanted WHERE tower_id = $1 AND deadline > $2`, towerID, now.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanWanted(rows)
}

func (p *PGStore) Resolve(attemptID string) error {
	_, err := p.db.Exec(`DELETE FROM rogerai.tower_audit_wanted WHERE attempt_id = $1`, attemptID)
	return err
}

// Overdue reads and deletes past-deadline rows in one statement, so a row is reported once
// even if two instances sweep at the same moment - the DELETE ... RETURNING is the claim.
func (p *PGStore) Overdue(now time.Time) ([]Wanted, error) {
	rows, err := p.db.Query(`
		DELETE FROM rogerai.tower_audit_wanted WHERE deadline <= $1
		RETURNING attempt_id, tower_id, station_id, request_digest, response_digest, usage_in, usage_out, wire_in, wire_out, deadline`, now.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanWanted(rows)
}

func scanWanted(rows *sql.Rows) ([]Wanted, error) {
	var out []Wanted
	for rows.Next() {
		var w Wanted
		if err := rows.Scan(&w.AttemptID, &w.TowerID, &w.StationID,
			&w.RequestDigest, &w.ResponseDigest, &w.UsageIn, &w.UsageOut,
			&w.WireIn, &w.WireOut, &w.Deadline); err != nil {
			return nil, err
		}
		w.Deadline = w.Deadline.UTC()
		out = append(out, w)
	}
	return out, rows.Err()
}
