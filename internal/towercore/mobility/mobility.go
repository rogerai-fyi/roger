// Package mobility keeps what Core knows about each Station for placement and moves: where
// it was observed and whether its node understands the re-attach nudge (Facts), and the move
// in progress, if any, with the Tower a failed move is skipped for. Contracts:
// features/tower/station_mobility.feature, placement_locality.feature.
package mobility

import (
	"database/sql"
	"sync"
	"time"

	"rogerai.fm/roger/v7/internal/towercore/placement"
)

// Facts are written whole on every attach.
type Facts struct {
	Locality placement.Locality
	Nudge    bool // the node declared it understands the re-attach nudge
}

// Move is one Station's move in progress.
type Move struct {
	StationID  string
	From, To   string
	Trigger    string
	Epoch      int64  // the epoch the Station had when it was set dormant
	PriorState string // the serving state an undo restores
	Started    time.Time
	Nudges     int
	LastNudge  time.Time
}

// Store is shared by every instance (PG) or in-process (Mem).
type Store interface {
	PutFacts(stationID string, f Facts) error
	Facts(stationID string) (Facts, bool, error)
	StartMove(m Move) (bool, error)
	Move(stationID string) (Move, bool, error)
	NoteNudge(stationID string, at time.Time) (int, error)
	// FinishMove ends the move in progress. A non-zero skipUntil pauses further moves of the
	// Station until then and, when skipTower is set, keeps it off that Tower; a zero one clears
	// any pause.
	FinishMove(stationID, skipTower string, skipUntil time.Time) error
	Skip(stationID string) (string, time.Time, error)
	Pending() ([]Move, error)
}

type skip struct {
	tower string
	until time.Time
}

// MemStore is the in-process Store.
type MemStore struct {
	mu    sync.Mutex
	facts map[string]Facts
	moves map[string]Move
	skips map[string]skip
}

func NewMemStore() *MemStore {
	return &MemStore{facts: map[string]Facts{}, moves: map[string]Move{}, skips: map[string]skip{}}
}

func (m *MemStore) PutFacts(id string, f Facts) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.facts[id] = f
	return nil
}

func (m *MemStore) Facts(id string) (Facts, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.facts[id]
	return f, ok, nil
}

func (m *MemStore) StartMove(mv Move) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, busy := m.moves[mv.StationID]; busy {
		return false, nil
	}
	mv.Started = mv.Started.UTC()
	m.moves[mv.StationID] = mv
	return true, nil
}

func (m *MemStore) Move(id string) (Move, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mv, ok := m.moves[id]
	return mv, ok, nil
}

func (m *MemStore) NoteNudge(id string, at time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mv, ok := m.moves[id]
	if !ok {
		return 0, nil
	}
	mv.Nudges++
	mv.LastNudge = at.UTC()
	m.moves[id] = mv
	return mv.Nudges, nil
}

func (m *MemStore) FinishMove(id, tower string, until time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.moves, id)
	if until.IsZero() {
		delete(m.skips, id)
	} else {
		m.skips[id] = skip{tower, until.UTC()}
	}
	return nil
}

func (m *MemStore) Skip(id string) (string, time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.skips[id]
	return s.tower, s.until, nil
}

func (m *MemStore) Pending() ([]Move, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Move, 0, len(m.moves))
	for _, mv := range m.moves {
		out = append(out, mv)
	}
	return out, nil
}

// PGStore is the shared Store.
type PGStore struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS rogerai.station_facts (
    station_id    TEXT PRIMARY KEY,
    loc_bucket    TEXT NOT NULL DEFAULT '',
    loc_continent TEXT NOT NULL DEFAULT '',
    nudge         BOOLEAN NOT NULL DEFAULT false
);
CREATE TABLE IF NOT EXISTS rogerai.station_moves (
    station_id  TEXT PRIMARY KEY,
    from_tower  TEXT NOT NULL DEFAULT '',
    to_tower    TEXT NOT NULL DEFAULT '',
    trigger     TEXT NOT NULL DEFAULT '',
    epoch       BIGINT NOT NULL DEFAULT 0,
    prior_state TEXT NOT NULL DEFAULT '',
    started_at  TIMESTAMPTZ,
    nudges      INT NOT NULL DEFAULT 0,
    last_nudge  TIMESTAMPTZ,
    skip_tower  TEXT NOT NULL DEFAULT '',
    skip_until  TIMESTAMPTZ
)`

func NewPGStore(db *sql.DB) (*PGStore, error) {
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	return &PGStore{db: db}, nil
}

func (p *PGStore) PutFacts(id string, f Facts) error {
	_, err := p.db.Exec(`INSERT INTO rogerai.station_facts (station_id, loc_bucket, loc_continent, nudge)
        VALUES ($1,$2,$3,$4) ON CONFLICT (station_id) DO UPDATE
        SET loc_bucket=EXCLUDED.loc_bucket, loc_continent=EXCLUDED.loc_continent, nudge=EXCLUDED.nudge`,
		id, f.Locality.Bucket, f.Locality.Continent, f.Nudge)
	return err
}

func (p *PGStore) Facts(id string) (Facts, bool, error) {
	var f Facts
	err := p.db.QueryRow(`SELECT loc_bucket, loc_continent, nudge FROM rogerai.station_facts WHERE station_id=$1`, id).
		Scan(&f.Locality.Bucket, &f.Locality.Continent, &f.Nudge)
	if err == sql.ErrNoRows {
		return Facts{}, false, nil
	}
	return f, err == nil, err
}

// StartMove claims the Station's move slot: an existing row with no move in progress is taken
// over, a row with one is left alone.
func (p *PGStore) StartMove(m Move) (bool, error) {
	res, err := p.db.Exec(`INSERT INTO rogerai.station_moves
          (station_id, from_tower, to_tower, trigger, epoch, prior_state, started_at, nudges, last_nudge)
        VALUES ($1,$2,$3,$4,$5,$6,$7,0,NULL)
        ON CONFLICT (station_id) DO UPDATE SET from_tower=EXCLUDED.from_tower, to_tower=EXCLUDED.to_tower,
          trigger=EXCLUDED.trigger, epoch=EXCLUDED.epoch, prior_state=EXCLUDED.prior_state,
          started_at=EXCLUDED.started_at, nudges=0, last_nudge=NULL
        WHERE rogerai.station_moves.started_at IS NULL`,
		m.StationID, m.From, m.To, m.Trigger, m.Epoch, m.PriorState, m.Started.UTC())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

const moveCols = `station_id, from_tower, to_tower, trigger, epoch, prior_state, started_at, nudges, last_nudge`

func scanMove(row interface{ Scan(...any) error }) (Move, error) {
	var m Move
	var last sql.NullTime
	err := row.Scan(&m.StationID, &m.From, &m.To, &m.Trigger, &m.Epoch, &m.PriorState, &m.Started, &m.Nudges, &last)
	if last.Valid {
		m.LastNudge = last.Time
	}
	return m, err
}

func (p *PGStore) Move(id string) (Move, bool, error) {
	m, err := scanMove(p.db.QueryRow(`SELECT `+moveCols+` FROM rogerai.station_moves
        WHERE station_id=$1 AND started_at IS NOT NULL`, id))
	if err == sql.ErrNoRows {
		return Move{}, false, nil
	}
	return m, err == nil, err
}

func (p *PGStore) NoteNudge(id string, at time.Time) (int, error) {
	var n int
	err := p.db.QueryRow(`UPDATE rogerai.station_moves SET nudges=nudges+1, last_nudge=$2
        WHERE station_id=$1 AND started_at IS NOT NULL RETURNING nudges`, id, at.UTC()).Scan(&n)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return n, err
}

func (p *PGStore) FinishMove(id, tower string, until time.Time) error {
	var u any
	if !until.IsZero() {
		u = until.UTC()
	} else {
		tower = ""
	}
	_, err := p.db.Exec(`UPDATE rogerai.station_moves SET started_at=NULL, nudges=0, last_nudge=NULL,
        skip_tower=$2, skip_until=$3 WHERE station_id=$1`, id, tower, u)
	return err
}

func (p *PGStore) Skip(id string) (string, time.Time, error) {
	var tower string
	var until sql.NullTime
	err := p.db.QueryRow(`SELECT skip_tower, skip_until FROM rogerai.station_moves WHERE station_id=$1`, id).
		Scan(&tower, &until)
	if err == sql.ErrNoRows {
		return "", time.Time{}, nil
	}
	return tower, until.Time, err
}

func (p *PGStore) Pending() ([]Move, error) {
	rows, err := p.db.Query(`SELECT ` + moveCols + ` FROM rogerai.station_moves WHERE started_at IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Move
	for rows.Next() {
		m, err := scanMove(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
