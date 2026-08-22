package attempt

// pgstore.go is the durable attempt chain.
//
// This is the record money is decided from, so it is authority in the strongest sense here:
// which attempt executed, exactly once, and what its one terminal outcome was. Everything
// downstream - settlement, earnings, a dispute six months later - reads this and nothing else.
//
// THE PRIMARY KEY IS THE COMPARE-AND-SWAP. (attempt_id, revision) is unique, so two writers
// proposing revision N both try to insert the same key and exactly one succeeds. That is a
// stronger guarantee than a conditional UPDATE, because it does not depend on anybody having
// read the right row first: the database refuses the second insert whatever either writer
// believed about the state.

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	"rogerai.fm/roger/v6/internal/pgmigrate"
)

// schema is additive and idempotent, and creates TABLES only.
const schema = `
CREATE TABLE IF NOT EXISTS rogerai.attempt_events (
    attempt_id TEXT        NOT NULL,
    revision   BIGINT      NOT NULL,
    event_id   TEXT        NOT NULL,
    state      TEXT        NOT NULL,
    kind       TEXT        NOT NULL,
    hold       TEXT        NOT NULL,
    -- The event's complete hash: what the NEXT revision binds.
    hash       TEXT        NOT NULL,
    signed     BYTEA       NOT NULL,
    -- The attempt's immutable authority, so a successor restates it exactly rather than
    -- being handed it again by a caller who might differ.
    spec       JSONB       NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- THE CAS. Two writers proposing the same revision both insert this key and exactly one
    -- of them wins, whatever either believed about the current state.
    PRIMARY KEY (attempt_id, revision)
);
-- One event id is one event, network-wide. A duplicate would mean two chain positions
-- claiming the same derived identity.
CREATE UNIQUE INDEX IF NOT EXISTS attempt_events_event_id ON rogerai.attempt_events (event_id);
`

// PGStore is the durable chain.
type PGStore struct{ db *sql.DB }

// NewPGStore prepares the durable chain.
func NewPGStore(db *sql.DB) (*PGStore, error) {
	if db == nil {
		return nil, errors.New("a durable attempt ledger needs a database handle")
	}
	if err := pgmigrate.Apply(db, schema); err != nil {
		return nil, err
	}
	return &PGStore{db: db}, nil
}

// Append commits one event, or explains why it could not.
func (p *PGStore) Append(rec Record, expectPrev int64) error {
	spec, err := json.Marshal(rec.Spec)
	if err != nil {
		return err
	}
	// The successor case is guarded by the PRIOR revision existing, in the same statement:
	// an INSERT ... SELECT that produces no row when the parent is absent or is not the head
	// we were told to expect. So a skipped revision inserts nothing rather than creating a
	// chain with a hole in it.
	var res sql.Result
	if expectPrev == 0 {
		res, err = p.db.Exec(`
			INSERT INTO rogerai.attempt_events
				(attempt_id, revision, event_id, state, kind, hold, hash, signed, spec)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			ON CONFLICT (attempt_id, revision) DO NOTHING`,
			rec.AttemptID, rec.Revision, rec.EventID, rec.State, rec.Kind, rec.Hold,
			rec.Hash, rec.Signed, spec)
	} else {
		res, err = p.db.Exec(`
			INSERT INTO rogerai.attempt_events
				(attempt_id, revision, event_id, state, kind, hold, hash, signed, spec)
			SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9
			 WHERE EXISTS (
			       SELECT 1 FROM rogerai.attempt_events
			        WHERE attempt_id = $1 AND revision = $10)
			ON CONFLICT (attempt_id, revision) DO NOTHING`,
			rec.AttemptID, rec.Revision, rec.EventID, rec.State, rec.Kind, rec.Hold,
			rec.Hash, rec.Signed, spec, expectPrev)
	}
	if err != nil {
		// A duplicate EVENT ID at a different chain position: two positions claiming one
		// derived identity, which the unique index refuses.
		if isUniqueViolation(err) {
			return ErrConflict
		}
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	// Nothing was written. The read below only EXPLAINS that - it never changes the outcome.
	return p.whyNot(rec, expectPrev)
}

// whyNot turns a refused append into the reason for it.
func (p *PGStore) whyNot(rec Record, expectPrev int64) error {
	existing, taken, err := p.At(rec.AttemptID, rec.Revision)
	if err != nil {
		return err
	}
	if taken {
		// EXACT REPLAY IS IDEMPOTENT: the same fact stated twice.
		if existing.EventID == rec.EventID && existing.Hash == rec.Hash {
			return nil
		}
		if expectPrev == 0 {
			// Issuing against an attempt that already exists is a duplicate issue, which is
			// what the caller actually did - "conflicting bytes" would describe our storage
			// rather than their mistake.
			return ErrAlreadyIssued
		}
		return ErrConflict
	}
	if expectPrev == 0 {
		return ErrAlreadyIssued
	}
	// The revision is free, so the guard was what failed: the prior we were told to expect is
	// not there.
	if _, ok, herr := p.At(rec.AttemptID, expectPrev); herr != nil {
		return herr
	} else if !ok {
		if _, any, aerr := p.Head(rec.AttemptID); aerr != nil {
			return aerr
		} else if !any {
			return ErrNotFound
		}
	}
	return ErrRevision
}

func (p *PGStore) Head(attemptID string) (Record, bool, error) {
	row := p.db.QueryRow(`
		SELECT attempt_id, revision, event_id, state, kind, hold, hash, signed, spec
		  FROM rogerai.attempt_events
		 WHERE attempt_id = $1
		 ORDER BY revision DESC
		 LIMIT 1`, attemptID)
	return scanEvent(row)
}

func (p *PGStore) At(attemptID string, revision int64) (Record, bool, error) {
	row := p.db.QueryRow(`
		SELECT attempt_id, revision, event_id, state, kind, hold, hash, signed, spec
		  FROM rogerai.attempt_events
		 WHERE attempt_id = $1 AND revision = $2`, attemptID, revision)
	return scanEvent(row)
}

type scanner interface{ Scan(dest ...any) error }

func scanEvent(row scanner) (Record, bool, error) {
	var r Record
	var spec []byte
	err := row.Scan(&r.AttemptID, &r.Revision, &r.EventID, &r.State, &r.Kind, &r.Hold,
		&r.Hash, &r.Signed, &spec)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	if err := json.Unmarshal(spec, &r.Spec); err != nil {
		// An unreadable authority is not an absent one: a successor built from a default
		// spec would restate this attempt's money differently from its own first event.
		return Record{}, false, errors.New("the recorded attempt authority is unreadable")
	}
	r.Spec.Deadline = r.Spec.Deadline.UTC()
	r.Spec.FinalizationCeiling = r.Spec.FinalizationCeiling.UTC()
	r.Spec.CommitTime = r.Spec.CommitTime.UTC()
	return r, true, nil
}

// isUniqueViolation reports whether Postgres refused a write for breaking uniqueness.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
