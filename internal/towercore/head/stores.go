package head

import (
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"rogerai.fm/roger/v5/internal/pgmigrate"
)

// --- in-process -------------------------------------------------------------

type memStore struct {
	mu    sync.Mutex
	heads map[string]Head
}

// NewMemStore is the in-process store, for tests and for a deployment with no database.
func NewMemStore() Store { return &memStore{heads: map[string]Head{}} }

// Record only ever advances. The comparison is the whole implementation: a head that could
// move backwards would let a slow instance finishing an older revision rewind the chain, and
// the next reconnect would look like a fork to everybody.
func (m *memStore) Record(h Head) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.heads[h.TowerID]
	if ok && h.Revision <= cur.Revision {
		return false, nil
	}
	m.heads[h.TowerID] = h
	return true, nil
}

func (m *memStore) Head(towerID string) (Head, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.heads[towerID]
	return h, ok, nil
}

func (m *memStore) Forget(towerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.heads, towerID)
	return nil
}

// --- durable ----------------------------------------------------------------

// schema stores the head HASH and REVISION only, never the inventory body. The body is
// large, changes often, and is fully reconstructible from the Tower on resync.
//
// TABLES only, never the schema: CREATE SCHEMA IF NOT EXISTS fails on a least-privilege role
// even when the schema exists, because PostgreSQL checks CREATE-on-database before the
// IF-NOT-EXISTS short-circuit.
const schema = `
CREATE TABLE IF NOT EXISTS rogerai.tower_inventory_head (
    tower_id   TEXT PRIMARY KEY,
    revision   BIGINT      NOT NULL,
    hash       TEXT        NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT tower_inventory_head_revision_positive CHECK (revision > 0)
);
`

// PGStore is the durable head store.
type PGStore struct{ db *sql.DB }

// NewPGStore applies the schema and returns the store.
func NewPGStore(db *sql.DB) (*PGStore, error) {
	if db == nil {
		return nil, errors.New("a durable head store needs a database handle")
	}
	if err := pgmigrate.Apply(db, schema); err != nil {
		return nil, err
	}
	return &PGStore{db: db}, nil
}

func wrap(op string, err error) error {
	return fmt.Errorf("%w: %s: %v", ErrUnavailable, op, err)
}

// Record advances the head in ONE statement.
//
// The monotonicity is enforced by the WHERE clause on the upsert, not by reading first and
// deciding in Go. Two instances can be recording for one Tower at the same moment; a
// read-then-write would let the slower one win and rewind the chain. Zero rows affected
// means somebody else is already at or ahead of this revision, which is not an error.
func (p *PGStore) Record(h Head) (bool, error) {
	res, err := p.db.Exec(`
		INSERT INTO rogerai.tower_inventory_head (tower_id,revision,hash,updated_at)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (tower_id) DO UPDATE
		   SET revision = EXCLUDED.revision,
		       hash     = EXCLUDED.hash,
		       updated_at = EXCLUDED.updated_at
		 WHERE rogerai.tower_inventory_head.revision < EXCLUDED.revision`,
		h.TowerID, h.Revision, h.Hash, h.UpdatedAt.UTC())
	if err != nil {
		return false, wrap("record head", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (p *PGStore) Head(towerID string) (Head, bool, error) {
	var h Head
	err := p.db.QueryRow(
		`SELECT tower_id,revision,hash,updated_at FROM rogerai.tower_inventory_head
		  WHERE tower_id=$1`, towerID).
		Scan(&h.TowerID, &h.Revision, &h.Hash, &h.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Head{}, false, nil
	}
	if err != nil {
		return Head{}, false, wrap("read head", err)
	}
	return h, true, nil
}

func (p *PGStore) Forget(towerID string) error {
	if _, err := p.db.Exec(
		`DELETE FROM rogerai.tower_inventory_head WHERE tower_id=$1`, towerID); err != nil {
		return wrap("forget head", err)
	}
	return nil
}
