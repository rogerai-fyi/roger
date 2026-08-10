package attach

// pgstore.go is the durable Station registry.
//
// This is Roger Core authority, not a cache: an attachment is who a Station IS, and every
// offer that will ever be verified is verified against a key recorded here. It belongs in
// the authoritative database rather than the shared Valkey layer the broker uses for
// accelerators.
//
// THE DATABASE ENFORCES THE INVARIANTS, not just the code above it. Application ordering
// decides the ordinary case; constraints decide the racing one, and the racing one is where
// the money is. Three of them:
//
//   - Admit runs in a transaction with the authorization row locked FOR UPDATE, so
//     consuming the invitation and writing the attachment cannot interleave with another
//     attempt. The Mem store gets the same property from a held mutex.
//   - A PARTIAL UNIQUE INDEX on each key, over LIVE rows only, so two Stations cannot share
//     an assertion or secure-session key even if two transactions check simultaneously.
//     Partial, because a revoked Station must not poison its keys forever.
//   - The consume is a CAS: `UPDATE ... WHERE NOT consumed`, and zero rows affected means
//     somebody else won. Reading `consumed` and then writing would be the same read-then-
//     write race the whole design exists to remove.

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"rogerai.fm/roger/v5/internal/pgmigrate"
)

// schema is applied on first use. Additive and idempotent.
//
// It creates TABLES and never the schema. `rogerai` is provisioned by an admin and owned by
// the app's database user - least privilege. CREATE SCHEMA IF NOT EXISTS is NOT safe here
// even though it reads as harmless: PostgreSQL checks CREATE-on-database before the
// IF-NOT-EXISTS short-circuit, so it fails with "permission denied for database" even when
// the schema already exists. That was verified against a least-privilege role rather than
// reasoned about, and getting it wrong takes the subsystem offline while everything else
// starts normally.
const schema = `
CREATE TABLE IF NOT EXISTS rogerai.station_authorizations (
    id            TEXT PRIMARY KEY,
    network       TEXT        NOT NULL,
    station_id    TEXT        NOT NULL,
    owner         TEXT        NOT NULL,
    origin_kind   TEXT        NOT NULL,
    origin_tower  TEXT        NOT NULL DEFAULT '',
    assertion_key TEXT        NOT NULL,
    session_key   TEXT        NOT NULL,
    ceiling_hash  TEXT        NOT NULL DEFAULT '',
    -- sha256 of the one-use invitation secret. The plaintext is shown once at invite and
    -- never stored, so reading this table cannot hand anybody an attachment.
    secret_hash   TEXT        NOT NULL DEFAULT '',
    role          TEXT        NOT NULL DEFAULT '',
    issued_at     TIMESTAMPTZ NOT NULL,
    expires_at    TIMESTAMPTZ NOT NULL,
    -- consumed/consumed_by are the one-use spend. consumed_by is the Station that resulted,
    -- which is what makes a lost-response retry answerable rather than a dead end.
    consumed      BOOLEAN     NOT NULL DEFAULT false,
    consumed_by   TEXT        NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS rogerai.station_attachments (
    station_id    TEXT PRIMARY KEY,
    owner         TEXT        NOT NULL,
    assertion_key TEXT        NOT NULL,
    session_key   TEXT        NOT NULL,
    origin_kind   TEXT        NOT NULL,
    origin_tower  TEXT        NOT NULL DEFAULT '',
    epoch         BIGINT      NOT NULL DEFAULT 1,
    ceiling_hash  TEXT        NOT NULL DEFAULT '',
    state         TEXT        NOT NULL,
    attached_at   TIMESTAMPTZ NOT NULL,
    auth_id       TEXT        NOT NULL DEFAULT ''
);

-- One live Station per key, enforced by the database. PARTIAL on the live states: a revoked
-- or detached Station must not hold its keys hostage forever, but a live one must be the
-- only holder even when two transactions check at the same instant.
CREATE UNIQUE INDEX IF NOT EXISTS station_attachments_live_assertion_key
    ON rogerai.station_attachments (assertion_key)
    WHERE state IN ('quarantine','active');
CREATE UNIQUE INDEX IF NOT EXISTS station_attachments_live_session_key
    ON rogerai.station_attachments (session_key)
    WHERE state IN ('quarantine','active');
CREATE INDEX IF NOT EXISTS station_attachments_owner ON rogerai.station_attachments (owner);
`

// PGStore is the durable Store.
type PGStore struct{ db *sql.DB }

// NewPGStore applies the schema and returns the store.
func NewPGStore(db *sql.DB) (*PGStore, error) {
	if db == nil {
		return nil, errors.New("a durable Station registry needs a database handle")
	}
	if err := pgmigrate.Apply(db, schema); err != nil {
		return nil, err
	}
	return &PGStore{db: db}, nil
}

// isConstraintViolation reports whether Postgres refused the write because it would break
// an invariant, rather than because it could not do the write. SQLSTATE class 23 is
// integrity-constraint violation; 23505 is unique_violation, which covers both the primary
// key and the partial unique indexes.
func isConstraintViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return strings.HasPrefix(pgErr.Code, "23")
	}
	return false
}

func pgwrap(op string, err error) error {
	return fmt.Errorf("%w: %s: %v", ErrUnavailable, op, err)
}

func (p *PGStore) PutAuthorization(a Authorization) error {
	_, err := p.db.Exec(`
		INSERT INTO rogerai.station_authorizations
		  (id,network,station_id,owner,origin_kind,origin_tower,assertion_key,session_key,
		   ceiling_hash,secret_hash,role,issued_at,expires_at,consumed,consumed_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		ON CONFLICT (id) DO UPDATE SET
		  network=EXCLUDED.network, station_id=EXCLUDED.station_id, owner=EXCLUDED.owner,
		  origin_kind=EXCLUDED.origin_kind, origin_tower=EXCLUDED.origin_tower,
		  assertion_key=EXCLUDED.assertion_key, session_key=EXCLUDED.session_key,
		  ceiling_hash=EXCLUDED.ceiling_hash, secret_hash=EXCLUDED.secret_hash,
		  role=EXCLUDED.role, issued_at=EXCLUDED.issued_at,
		  expires_at=EXCLUDED.expires_at`,
		a.ID, a.Network, a.StationID, a.Owner, a.Origin.Kind, a.Origin.TowerID,
		a.AssertionKey, a.SessionKey, a.CeilingHash, a.SecretHash, a.Role,
		a.IssuedAt.UTC(), a.ExpiresAt.UTC(), a.Consumed, a.ConsumedBy)
	if err != nil {
		return pgwrap("put authorization", err)
	}
	return nil
}

// PutAuthorizationCapped serialises minting PER OWNER with a transaction-scoped advisory
// lock, then counts and inserts inside it.
//
// A conditional INSERT alone is not enough under READ COMMITTED: two transactions can both
// evaluate the count before either commits, and both insert. The lock is keyed on the owner,
// so it costs nothing across accounts and only ever serialises one account against itself -
// which is exactly the abuse being bounded.
func (p *PGStore) PutAuthorizationCapped(a Authorization, max int) (bool, error) {
	tx, err := p.db.Begin()
	if err != nil {
		return false, pgwrap("put invitation", err)
	}
	defer tx.Rollback() //nolint:errcheck // a no-op once committed

	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext($1))`,
		"station-invite:"+a.Owner); err != nil {
		return false, pgwrap("put invitation", err)
	}
	var live int
	if err := tx.QueryRow(`SELECT count(*) FROM rogerai.station_authorizations
	                        WHERE owner=$1 AND NOT consumed AND expires_at >= $2`,
		a.Owner, a.IssuedAt.UTC()).Scan(&live); err != nil {
		return false, pgwrap("put invitation", err)
	}
	if live >= max {
		return false, nil
	}
	if _, err := tx.Exec(`
		INSERT INTO rogerai.station_authorizations
		  (id,network,station_id,owner,origin_kind,origin_tower,assertion_key,session_key,
		   ceiling_hash,secret_hash,role,issued_at,expires_at,consumed,consumed_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,false,'')`,
		a.ID, a.Network, a.StationID, a.Owner, a.Origin.Kind, a.Origin.TowerID,
		a.AssertionKey, a.SessionKey, a.CeilingHash, a.SecretHash, a.Role,
		a.IssuedAt.UTC(), a.ExpiresAt.UTC()); err != nil {
		if isConstraintViolation(err) {
			// A duplicate id is a permanent answer, and the memory store says the same.
			return false, reject(errors.New("that invitation id already exists"))
		}
		return false, pgwrap("put invitation", err)
	}
	if err := tx.Commit(); err != nil {
		return false, pgwrap("put invitation", err)
	}
	return true, nil
}

func (p *PGStore) Authorization(id string) (Authorization, bool, error) {
	var a Authorization
	err := p.db.QueryRow(`
		SELECT id,network,station_id,owner,origin_kind,origin_tower,assertion_key,session_key,
		       ceiling_hash,secret_hash,role,issued_at,expires_at,consumed,consumed_by
		  FROM rogerai.station_authorizations WHERE id=$1`, id).
		Scan(&a.ID, &a.Network, &a.StationID, &a.Owner, &a.Origin.Kind, &a.Origin.TowerID,
			&a.AssertionKey, &a.SessionKey, &a.CeilingHash, &a.SecretHash, &a.Role,
			&a.IssuedAt, &a.ExpiresAt, &a.Consumed, &a.ConsumedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return Authorization{}, false, nil
	}
	if err != nil {
		return Authorization{}, false, pgwrap("read authorization", err)
	}
	return a, true, nil
}

// Admit consumes the invitation and writes the attachment in ONE transaction.
//
// The row is locked FOR UPDATE before anything is decided, and the consume is a CAS on
// `NOT consumed`. A racing attempt therefore either blocks until this commits and then sees
// consumed=true, or loses the CAS - never both winning.
func (p *PGStore) Admit(authID string, at Attachment) (bool, error) {
	tx, err := p.db.Begin()
	if err != nil {
		return false, pgwrap("begin", err)
	}
	defer tx.Rollback() //nolint:errcheck // a no-op once committed

	var consumed bool
	err = tx.QueryRow(`SELECT consumed FROM rogerai.station_authorizations
	                    WHERE id=$1 FOR UPDATE`, authID).Scan(&consumed)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil // no such invitation: the caller reports a refusal, not an outage
	}
	if err != nil {
		return false, pgwrap("lock authorization", err)
	}
	if consumed {
		return false, nil
	}

	res, err := tx.Exec(`UPDATE rogerai.station_authorizations
	                        SET consumed=true, consumed_by=$2
	                      WHERE id=$1 AND NOT consumed`, authID, at.StationID)
	if err != nil {
		return false, pgwrap("consume authorization", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}

	if _, err := tx.Exec(`
		INSERT INTO rogerai.station_attachments
		  (station_id,owner,assertion_key,session_key,origin_kind,origin_tower,epoch,
		   ceiling_hash,state,attached_at,auth_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		at.StationID, at.Owner, at.AssertionKey, at.SessionKey, at.Origin.Kind,
		at.Origin.TowerID, at.Epoch, at.CeilingHash, at.State, at.AttachedAt.UTC(),
		at.AuthID); err != nil {
		// A constraint violation here is a PERMANENT answer, not a blip: the station_id
		// primary key means that Station is already attached, and a partial unique index
		// means another live Station holds one of these keys. Reporting either as an outage
		// invites a caller to retry forever against something that will never change.
		// Rolling back leaves the invitation UNSPENT, which is what the spec asks for: a
		// refused attachment must not cost the owner their invitation.
		if isConstraintViolation(err) {
			return false, reject(errors.New("that Station ID or key is already attached"))
		}
		return false, pgwrap("record attachment", err)
	}
	if err := tx.Commit(); err != nil {
		return false, pgwrap("commit", err)
	}
	return true, nil
}

const attachCols = `station_id,owner,assertion_key,session_key,origin_kind,origin_tower,
                    epoch,ceiling_hash,state,attached_at,auth_id`

func scanAttachment(row interface{ Scan(...any) error }) (Attachment, error) {
	var at Attachment
	err := row.Scan(&at.StationID, &at.Owner, &at.AssertionKey, &at.SessionKey,
		&at.Origin.Kind, &at.Origin.TowerID, &at.Epoch, &at.CeilingHash, &at.State,
		&at.AttachedAt, &at.AuthID)
	return at, err
}

func (p *PGStore) ByStation(stationID string) (Attachment, bool, error) {
	at, err := scanAttachment(p.db.QueryRow(
		`SELECT `+attachCols+` FROM rogerai.station_attachments WHERE station_id=$1`, stationID))
	if errors.Is(err, sql.ErrNoRows) {
		return Attachment{}, false, nil
	}
	if err != nil {
		return Attachment{}, false, pgwrap("read attachment", err)
	}
	return at, true, nil
}

// ByAssertionKey and BySessionKey look only at LIVE rows, matching the partial indexes and
// the Mem store. A retired Station's keys are free again.
func (p *PGStore) ByAssertionKey(key string) (Attachment, bool, error) {
	return p.byLiveKey("assertion_key", key)
}

func (p *PGStore) BySessionKey(key string) (Attachment, bool, error) {
	return p.byLiveKey("session_key", key)
}

func (p *PGStore) byLiveKey(col, key string) (Attachment, bool, error) {
	at, err := scanAttachment(p.db.QueryRow(
		`SELECT `+attachCols+` FROM rogerai.station_attachments
		  WHERE `+col+`=$1 AND state IN ('quarantine','active')`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return Attachment{}, false, nil
	}
	if err != nil {
		return Attachment{}, false, pgwrap("read attachment by key", err)
	}
	return at, true, nil
}

func (p *PGStore) SetState(stationID, state string) (bool, error) {
	res, err := p.db.Exec(`UPDATE rogerai.station_attachments SET state=$2 WHERE station_id=$1`,
		stationID, state)
	if err != nil {
		return false, pgwrap("set state", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// CountLiveAttachments counts an owner's attached Stations in the states that carry work.
func (p *PGStore) CountLiveAttachments(owner string) (int, error) {
	var n int
	if err := p.db.QueryRow(`SELECT count(*) FROM rogerai.station_attachments
	                          WHERE owner=$1 AND state IN ('quarantine','active')`, owner).
		Scan(&n); err != nil {
		return 0, pgwrap("count attachments", err)
	}
	return n, nil
}

// Reap deletes authorizations that expired long enough ago to be beyond any retry. Consumed
// ones are KEPT: they are the record that answers a lost-response retry, and forgetting one
// turns a harmless duplicate into a refusal.
func (p *PGStore) Reap(before time.Time, retryHorizon time.Duration) (int64, error) {
	res, err := p.db.Exec(`DELETE FROM rogerai.station_authorizations
	                        WHERE expires_at < $1
	                          AND (NOT consumed OR expires_at < $2)`,
		before.UTC(), before.Add(-retryHorizon).UTC())
	if err != nil {
		return 0, pgwrap("reap", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
