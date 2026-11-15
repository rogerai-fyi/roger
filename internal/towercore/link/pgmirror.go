package link

import (
	"database/sql"
	"time"

	"rogerai.fm/roger/v7/internal/towercore/placement"
)

// PGMirror is the Mirror production uses: one row per linked Tower in the same shared
// PostgreSQL every instance already trusts for stations, heads and dispatch.
//
// One row, upserted whole, read whole. The correctness rests on "last write wins for the
// same tower", which is exactly the semantics a heartbeat wants: the newest LastSeen is
// the truth and an older concurrent write losing is not a conflict, it is the point.
type PGMirror struct{ db *sql.DB }

const mirrorSchema = `
CREATE TABLE IF NOT EXISTS rogerai.tower_link_mirror (
    tower_id   TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    version    INT  NOT NULL,
    last_seen  TIMESTAMPTZ NOT NULL,
    endpoint   TEXT NOT NULL DEFAULT '',
    tls_spki   TEXT NOT NULL DEFAULT ''
);
ALTER TABLE rogerai.tower_link_mirror ADD COLUMN IF NOT EXISTS loc_bucket TEXT NOT NULL DEFAULT '';
ALTER TABLE rogerai.tower_link_mirror ADD COLUMN IF NOT EXISTS loc_continent TEXT NOT NULL DEFAULT '';
ALTER TABLE rogerai.tower_link_mirror ADD COLUMN IF NOT EXISTS loc_known BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE rogerai.tower_link_mirror ADD COLUMN IF NOT EXISTS loc_link TEXT NOT NULL DEFAULT '';
ALTER TABLE rogerai.tower_link_mirror ADD COLUMN IF NOT EXISTS loc_home TEXT NOT NULL DEFAULT '';
ALTER TABLE rogerai.tower_link_mirror ADD COLUMN IF NOT EXISTS link_bucket TEXT NOT NULL DEFAULT '';
ALTER TABLE rogerai.tower_link_mirror ADD COLUMN IF NOT EXISTS link_home TEXT NOT NULL DEFAULT '';
ALTER TABLE rogerai.tower_link_mirror ADD COLUMN IF NOT EXISTS link_known BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE rogerai.tower_link_mirror ADD COLUMN IF NOT EXISTS pull BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE rogerai.tower_link_mirror ADD COLUMN IF NOT EXISTS hub_class INT NOT NULL DEFAULT 0`

// NewPGMirror applies the (additive, idempotent) schema and returns the mirror.
func NewPGMirror(db *sql.DB) (*PGMirror, error) {
	if _, err := db.Exec(mirrorSchema); err != nil {
		return nil, err
	}
	return &PGMirror{db: db}, nil
}

func (p *PGMirror) Put(towerID string, r Record) error {
	_, err := p.db.Exec(`
        INSERT INTO rogerai.tower_link_mirror (tower_id, session_id, version, last_seen, endpoint, tls_spki,
                                               loc_bucket, loc_continent, loc_known, loc_link,
                                               loc_home, link_bucket, link_home, link_known, pull, hub_class)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
        ON CONFLICT (tower_id) DO UPDATE
          SET session_id = EXCLUDED.session_id, version = EXCLUDED.version,
              last_seen = EXCLUDED.last_seen, endpoint = EXCLUDED.endpoint,
              tls_spki = EXCLUDED.tls_spki, loc_bucket = EXCLUDED.loc_bucket,
              loc_continent = EXCLUDED.loc_continent, loc_known = EXCLUDED.loc_known,
              loc_link = EXCLUDED.loc_link, loc_home = EXCLUDED.loc_home,
              link_bucket = EXCLUDED.link_bucket, link_home = EXCLUDED.link_home,
              link_known = EXCLUDED.link_known, pull = EXCLUDED.pull, hub_class = EXCLUDED.hub_class`,
		towerID, r.SessionID, r.Version, r.LastSeen.UTC(), r.Relay.Endpoint, r.Relay.TLSSPKI,
		r.Locality.Bucket, r.Locality.Continent, r.LocalityKnown, r.Link.Continent,
		r.Locality.Home, r.Link.Bucket, r.Link.Home, r.LinkKnown, r.Pull, int(r.HubClass))
	return err
}

func (p *PGMirror) Get(towerID string) (Record, bool, error) {
	var r Record
	var seen time.Time
	var hub int
	err := p.db.QueryRow(`
        SELECT session_id, version, last_seen, endpoint, tls_spki, loc_bucket, loc_continent, loc_known, loc_link,
                loc_home, link_bucket, link_home, link_known, pull, hub_class
          FROM rogerai.tower_link_mirror WHERE tower_id = $1`, towerID).
		Scan(&r.SessionID, &r.Version, &seen, &r.Relay.Endpoint, &r.Relay.TLSSPKI,
			&r.Locality.Bucket, &r.Locality.Continent, &r.LocalityKnown, &r.Link.Continent,
			&r.Locality.Home, &r.Link.Bucket, &r.Link.Home, &r.LinkKnown, &r.Pull, &hub)
	if err == sql.ErrNoRows {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	r.LastSeen, r.HubClass = seen, placement.HubClass(hub)
	return r, true, nil
}

func (p *PGMirror) Del(towerID, sessionID string) error {
	// Compare-and-delete: only the session being closed may remove the row, so a stale
	// close cannot wipe a newer session a peer instance just recorded.
	_, err := p.db.Exec(`DELETE FROM rogerai.tower_link_mirror
        WHERE tower_id = $1 AND session_id = $2`, towerID, sessionID)
	return err
}

func (p *PGMirror) All() (map[string]Record, error) {
	rows, err := p.db.Query(`SELECT tower_id, session_id, version, last_seen, endpoint, tls_spki,
                loc_bucket, loc_continent, loc_known, loc_link,
                loc_home, link_bucket, link_home, link_known, pull, hub_class
          FROM rogerai.tower_link_mirror`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Record{}
	for rows.Next() {
		var id string
		var r Record
		var seen time.Time
		var hub int
		if err := rows.Scan(&id, &r.SessionID, &r.Version, &seen, &r.Relay.Endpoint, &r.Relay.TLSSPKI,
			&r.Locality.Bucket, &r.Locality.Continent, &r.LocalityKnown, &r.Link.Continent,
			&r.Locality.Home, &r.Link.Bucket, &r.Link.Home, &r.LinkKnown, &r.Pull, &hub); err != nil {
			return nil, err
		}
		r.LastSeen, r.HubClass = seen, placement.HubClass(hub)
		out[id] = r
	}
	return out, rows.Err()
}
