package link

import (
	"database/sql"
	"time"
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
)`

// NewPGMirror applies the (additive, idempotent) schema and returns the mirror.
func NewPGMirror(db *sql.DB) (*PGMirror, error) {
	if _, err := db.Exec(mirrorSchema); err != nil {
		return nil, err
	}
	return &PGMirror{db: db}, nil
}

func (p *PGMirror) Put(towerID string, r Record) error {
	_, err := p.db.Exec(`
        INSERT INTO rogerai.tower_link_mirror (tower_id, session_id, version, last_seen, endpoint, tls_spki)
        VALUES ($1, $2, $3, $4, $5, $6)
        ON CONFLICT (tower_id) DO UPDATE
          SET session_id = EXCLUDED.session_id, version = EXCLUDED.version,
              last_seen = EXCLUDED.last_seen, endpoint = EXCLUDED.endpoint,
              tls_spki = EXCLUDED.tls_spki`,
		towerID, r.SessionID, r.Version, r.LastSeen.UTC(), r.Relay.Endpoint, r.Relay.TLSSPKI)
	return err
}

func (p *PGMirror) Get(towerID string) (Record, bool, error) {
	var r Record
	var seen time.Time
	err := p.db.QueryRow(`
        SELECT session_id, version, last_seen, endpoint, tls_spki
          FROM rogerai.tower_link_mirror WHERE tower_id = $1`, towerID).
		Scan(&r.SessionID, &r.Version, &seen, &r.Relay.Endpoint, &r.Relay.TLSSPKI)
	if err == sql.ErrNoRows {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	r.LastSeen = seen
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
	rows, err := p.db.Query(`SELECT tower_id, session_id, version, last_seen, endpoint, tls_spki
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
		if err := rows.Scan(&id, &r.SessionID, &r.Version, &seen, &r.Relay.Endpoint, &r.Relay.TLSSPKI); err != nil {
			return nil, err
		}
		r.LastSeen = seen
		out[id] = r
	}
	return out, rows.Err()
}
