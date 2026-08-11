package fleet

// pgstore.go is the durable fleet view - the one every broker can read.

import (
	"database/sql"
	"errors"
	"time"

	"rogerai.fm/roger/v5/internal/pgmigrate"
)

// schema is additive and idempotent, and creates TABLES only: `rogerai` is provisioned by an
// admin and owned by the app's least-privilege user.
const schema = `
CREATE TABLE IF NOT EXISTS rogerai.tower_routable (
    tower_id   TEXT        NOT NULL,
    station_id TEXT        NOT NULL,
    offer_id   TEXT        NOT NULL,
    model      TEXT        NOT NULL,
    modality   TEXT        NOT NULL,
    capacity   BIGINT      NOT NULL DEFAULT 0,
    expires    TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tower_id, offer_id)
);
-- The routing lookup: candidates for a model, without scanning every Tower's history.
CREATE INDEX IF NOT EXISTS tower_routable_model ON rogerai.tower_routable (model, expires);
`

// PGStore is the durable fleet view.
type PGStore struct{ db *sql.DB }

// NewPGStore prepares the durable projection.
func NewPGStore(db *sql.DB) (*PGStore, error) {
	if db == nil {
		return nil, errors.New("a durable fleet view needs a database handle")
	}
	if err := pgmigrate.Apply(db, schema); err != nil {
		return nil, err
	}
	return &PGStore{db: db}, nil
}

// Replace swaps a Tower's whole routable set in ONE transaction.
//
// A delete followed by inserts outside a transaction would leave a window in which the Tower
// looks like it is offering nothing - and a request arriving in that window would be refused
// for a fleet that is perfectly healthy. The window is small and it is exactly the sort of
// thing that happens under load, which is when it matters.
func (p *PGStore) Replace(towerID string, rows []Station) error {
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM rogerai.tower_routable WHERE tower_id = $1`, towerID); err != nil {
		return err
	}
	for _, r := range rows {
		if _, err := tx.Exec(`
			INSERT INTO rogerai.tower_routable
				(tower_id, station_id, offer_id, model, modality, capacity, expires)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (tower_id, offer_id) DO UPDATE SET
				station_id = EXCLUDED.station_id, model = EXCLUDED.model,
				modality = EXCLUDED.modality, capacity = EXCLUDED.capacity,
				expires = EXCLUDED.expires`,
			towerID, r.StationID, r.OfferID, r.Model, r.Modality, r.Capacity, r.Expires); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (p *PGStore) Candidates(model string, now time.Time) ([]Station, error) {
	rows, err := p.db.Query(`
		SELECT tower_id, station_id, offer_id, model, modality, capacity, expires
		  FROM rogerai.tower_routable
		 WHERE model = $1 AND expires > $2`, model, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Station
	for rows.Next() {
		var s Station
		if err := rows.Scan(&s.TowerID, &s.StationID, &s.OfferID, &s.Model, &s.Modality,
			&s.Capacity, &s.Expires); err != nil {
			return nil, err
		}
		s.Expires = s.Expires.UTC()
		out = append(out, s)
	}
	return out, rows.Err()
}

func (p *PGStore) Forget(towerID string) error {
	_, err := p.db.Exec(`DELETE FROM rogerai.tower_routable WHERE tower_id = $1`, towerID)
	return err
}

func (p *PGStore) Reap(now time.Time) (int64, error) {
	res, err := p.db.Exec(`DELETE FROM rogerai.tower_routable WHERE expires <= $1`, now)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
