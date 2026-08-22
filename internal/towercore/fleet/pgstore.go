package fleet

// pgstore.go is the durable fleet view - the one every broker can read.

import (
	"database/sql"
	"errors"
	"time"

	"rogerai.fm/roger/v6/internal/pgmigrate"
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
    -- capacity is DEAD and stays only because dropping a column while an older instance is
    -- still inserting it breaks that instance mid-deploy. Nothing writes it and nothing reads
    -- it: it never held anything but the constant 1, and real per-node capacity is derived at
    -- score time from what the probes measured (see fleet.Station's own comment).
    capacity   BIGINT      NOT NULL DEFAULT 0,
    expires    TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tower_id, offer_id)
);
-- The routing lookup: candidates for a model, without scanning every Tower's history.
CREATE INDEX IF NOT EXISTS tower_routable_model ON rogerai.tower_routable (model, expires);
-- ADDITIVE: a column in the CREATE body never reaches a table that already exists.
ALTER TABLE rogerai.tower_routable ADD COLUMN IF NOT EXISTS endpoint TEXT NOT NULL DEFAULT '';
-- Per-token pricing (Option C): micro-USD per 1,000,000 tokens, from the signed leaf.
ALTER TABLE rogerai.tower_routable ADD COLUMN IF NOT EXISTS price_in  BIGINT NOT NULL DEFAULT 0;
ALTER TABLE rogerai.tower_routable ADD COLUMN IF NOT EXISTS price_out BIGINT NOT NULL DEFAULT 0;
-- The broker node id of the same machine, so placement can rank a candidate by what the
-- probes measured instead of taking whichever row came back first. Empty on rows published
-- before the join existed, which reads as "unmeasured", not as "bad".
ALTER TABLE rogerai.tower_routable ADD COLUMN IF NOT EXISTS node_id TEXT NOT NULL DEFAULT '';
-- The hub certificate pin that belongs with the endpoint column: hex sha256 over the
-- SubjectPublicKeyInfo of the certificate that Tower's hub presents, or empty for a hub that
-- serves plaintext. Empty on every row published before this column existed, which reads as
-- "plaintext" - which is what those towers were.
ALTER TABLE rogerai.tower_routable ADD COLUMN IF NOT EXISTS tls_spki TEXT NOT NULL DEFAULT '';
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
				(tower_id, station_id, offer_id, model, modality, expires, endpoint, price_in, price_out, node_id, tls_spki)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			ON CONFLICT (tower_id, offer_id) DO UPDATE SET
				station_id = EXCLUDED.station_id, model = EXCLUDED.model,
				modality = EXCLUDED.modality,
				expires = EXCLUDED.expires, endpoint = EXCLUDED.endpoint,
				price_in = EXCLUDED.price_in, price_out = EXCLUDED.price_out,
				node_id = EXCLUDED.node_id, tls_spki = EXCLUDED.tls_spki`,
			towerID, r.StationID, r.OfferID, r.Model, r.Modality, r.Expires, r.Endpoint, r.PriceIn, r.PriceOut, r.NodeID, r.TLSSPKI); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (p *PGStore) Candidates(model string, now time.Time) ([]Station, error) {
	rows, err := p.db.Query(`
		SELECT tower_id, station_id, offer_id, model, modality, expires, endpoint, price_in, price_out, node_id, tls_spki
		  FROM rogerai.tower_routable
		 WHERE model = $1 AND expires > $2
		 -- A TOTAL, STABLE ORDER. Without it this returned rows in whatever order the heap
		 -- gave them, which made edge placement not merely unranked but non-reproducible:
		 -- the same fleet could answer two identical requests differently. Callers rank
		 -- properly on top of this; the point here is that the INPUT is deterministic, so a
		 -- placement decision can be explained after the fact.
		 ORDER BY station_id ASC, offer_id ASC`, model, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Station
	for rows.Next() {
		var s Station
		if err := rows.Scan(&s.TowerID, &s.StationID, &s.OfferID, &s.Model, &s.Modality,
			&s.Expires, &s.Endpoint, &s.PriceIn, &s.PriceOut, &s.NodeID, &s.TLSSPKI); err != nil {
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

// RoutableTowers lists distinct Towers with an unexpired endpoint row.
func (p *PGStore) RoutableTowers(now time.Time) ([]string, error) {
	rows, err := p.db.Query(`
		SELECT DISTINCT tower_id FROM rogerai.tower_routable
		 WHERE endpoint <> '' AND expires > $1
		 -- ORDERED, so a canary sweep walks the fleet the same way twice and so the in-memory
		 -- reference has something deterministic to be held against. DISTINCT does not promise
		 -- an order, and the parity suite could not see that while it only ever asserted a
		 -- one-element result.
		 ORDER BY tower_id ASC`, now.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ByTower is a Tower's unexpired rows.
func (p *PGStore) ByTower(towerID string, now time.Time) ([]Station, error) {
	rows, err := p.db.Query(`
		SELECT tower_id, station_id, offer_id, model, modality, expires, endpoint, price_in, price_out, node_id, tls_spki
		  FROM rogerai.tower_routable WHERE tower_id = $1 AND expires > $2
		 ORDER BY station_id ASC, offer_id ASC`, towerID, now.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Station
	for rows.Next() {
		var st Station
		if err := rows.Scan(&st.TowerID, &st.StationID, &st.OfferID, &st.Model, &st.Modality,
			&st.Expires, &st.Endpoint, &st.PriceIn, &st.PriceOut, &st.NodeID, &st.TLSSPKI); err != nil {
			return nil, err
		}
		st.Expires = st.Expires.UTC()
		out = append(out, st)
	}
	return out, rows.Err()
}

func (p *PGStore) Reap(now time.Time) (int64, error) {
	res, err := p.db.Exec(`DELETE FROM rogerai.tower_routable WHERE expires <= $1`, now)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
