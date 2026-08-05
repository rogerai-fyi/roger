// Package towerstore is the database-backed store for a durable standalone Tower.
//
// It lives outside internal/tower deliberately. That package is covered by a gate test
// that fails if any file in it gains the ability to reach the network, and a database
// driver dials - so keeping the driver here is what lets the standalone core stay
// provably egress-free while still having durable storage.
//
// The spec permits a local PostgreSQL, with one condition that this package enforces:
// every resolved address must stay inside the operator's declared private allowlist.
// Otherwise "a standalone Tower talks to nothing" quietly stops being true the moment
// somebody points it at a hosted database.
package towerstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver
	"rogerai.fm/roger/v5/internal/pgmigrate"
	"rogerai.fm/roger/v5/internal/tower"
)

// execCtx carries the caller's deadline into the shared migration helper, which speaks the
// plain Exec shape. Without it a Tower with an unreachable database would hang on startup
// instead of failing inside its connect timeout.
type execCtx struct {
	ctx context.Context
	db  *sql.DB
}

func (e execCtx) Exec(query string, args ...any) (sql.Result, error) {
	return e.db.ExecContext(e.ctx, query, args...)
}

// schema is applied on first use. It is additive and idempotent, so starting a Tower
// against an existing database never destroys what is there.
//
// The state is one row holding a JSON snapshot plus a revision. That is deliberate for
// v1: the Tower's admission state is small, is always read and written whole, and its
// correctness rests on the compare-and-swap rather than on relational structure.
// Normalising it buys nothing until something needs to query inside it.
const schema = `
CREATE TABLE IF NOT EXISTS tower_admission (
    id         INT PRIMARY KEY DEFAULT 1,
    revision   BIGINT NOT NULL,
    state      JSONB  NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT tower_admission_single_row CHECK (id = 1));
`

// PGStore is a tower.Store backed by PostgreSQL.
type PGStore struct {
	dsn string
	db  *sql.DB
}

// Open validates the destination and prepares a store. It does NOT dial: a Tower should
// be able to check its configuration without a database being up, and readiness reports
// the connection separately with its own repair instruction.
func Open(dsn string, allowed []*net.IPNet) (*PGStore, error) {
	if err := checkDestination(dsn, allowed); err != nil {
		return nil, err
	}
	return &PGStore{dsn: dsn}, nil
}

// checkDestination enforces the private allowlist on the DSN's host.
func checkDestination(dsn string, allowed []*net.IPNet) error {
	u, err := url.Parse(dsn)
	if err != nil || u.Host == "" {
		return fmt.Errorf("the database URL is not a valid postgres:// address")
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "5432"
	}
	// "localhost" is accepted as the CONSTANT it is, not by resolving it. Every
	// PostgreSQL DSN a person writes says localhost, and refusing it would make the
	// documented local-database path fail for everyone - while substituting the loopback
	// literal performs no lookup at all, so the no-DNS property is untouched.
	//
	// This is the only name accepted. EgressGuard still refuses every other hostname
	// rather than resolving it: resolving is already the lookup the standalone contract
	// forbids, and a name that resolves somewhere private today can resolve elsewhere
	// tomorrow.
	if host == "localhost" {
		host = "127.0.0.1"
	}
	return tower.NewEgressGuard(allowed).Allow(net.JoinHostPort(host, port))
}

// connect dials on first use and applies the schema.
func (p *PGStore) connect() (*sql.DB, error) {
	if p.db != nil {
		return p.db, nil
	}
	db, err := sql.Open("pgx", p.dsn)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("cannot reach the Tower database: %w", err)
	}
	// Retried once: an operator running two Tower processes against one local database can
	// have one lose a catalog race on an IF NOT EXISTS create. See internal/pgmigrate.
	if err := pgmigrate.Apply(execCtx{ctx: ctx, db: db}, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("cannot apply the Tower schema: %w", err)
	}
	p.db = db
	return db, nil
}

// Load reads the snapshot, minting a fresh one when the table is empty.
func (p *PGStore) Load() (*tower.Snapshot, error) {
	db, err := p.connect()
	if err != nil {
		return nil, err
	}
	var revision int64
	var raw []byte
	err = db.QueryRow(`SELECT revision, state FROM tower_admission WHERE id = 1`).Scan(&revision, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return tower.NewSnapshot()
	}
	if err != nil {
		return nil, err
	}
	var s tower.Snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		// Corrupt state must not read as empty state: that would re-mint the verifier
		// secret and orphan every credential already issued.
		return nil, fmt.Errorf("the stored Tower state is unreadable: %w", err)
	}
	s.Revision = revision
	return &s, nil
}

// Save writes the snapshot if the stored revision still matches, in one statement so a
// concurrent writer cannot interleave between the check and the write.
func (p *PGStore) Save(s *tower.Snapshot) (int64, error) {
	db, err := p.connect()
	if err != nil {
		return 0, err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return 0, err
	}
	next := s.Revision + 1

	// INSERT covers the first write; the DO UPDATE applies only when the caller's
	// revision is still current, so a stale writer affects no rows and is refused.
	res, err := db.Exec(`
        INSERT INTO tower_admission (id, revision, state, updated_at)
        VALUES (1, $1, $2, now())
        ON CONFLICT (id) DO UPDATE
          SET revision = EXCLUDED.revision, state = EXCLUDED.state, updated_at = now()
          WHERE tower_admission.revision = $3`, next, raw, s.Revision)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, tower.ErrStaleWrite
	}
	s.Revision = next
	return next, nil
}

// Close releases the connection pool.
func (p *PGStore) Close() error {
	if p.db == nil {
		return nil
	}
	return p.db.Close()
}
