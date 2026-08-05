package toweradmit

// pgstore.go is the durable admission registry.
//
// This is Roger Core state, not a cache, so it belongs in the authoritative database
// rather than in the shared Valkey layer the broker uses for accelerators. Two of the
// things recorded here are decisions we must never silently forget:
//
//   - a REVOCATION, which is the only thing standing between an abusive Tower and the
//     public network, and which also burns that Tower's identity key;
//   - FALSE-CLAIM EVIDENCE, which is only evidence if it accumulates across deploys.
//
// A lease and a lifecycle state matter for the same reason: they bound what a Tower may do
// while nobody is watching it closely.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"rogerai.fm/roger/v5/internal/pgmigrate"
)

// schema is applied on first use. Additive and idempotent, so opening against an existing
// database never destroys what is there.
//
// It creates TABLES and never the schema. The `rogerai` schema is provisioned by an admin
// and owned by the app's database user - least privilege, exactly as the money store
// documents: the user has no DB-level CREATE, only rights inside its own schema.
//
// CREATE SCHEMA IF NOT EXISTS is not safe here even though it reads as harmless. PostgreSQL
// checks CREATE-on-database BEFORE the IF-NOT-EXISTS short-circuit, so it fails with
// "permission denied for database" even when the schema is already there. Verified against
// a least-privilege role rather than reasoned about: it would have taken joined-Tower
// admission offline in production while every other subsystem started normally, and the
// only symptom would have been a log line saying admission was OFF.
const schema = `
CREATE TABLE IF NOT EXISTS rogerai.tower_enrollment_tokens (
    id      TEXT PRIMARY KEY,
    owner   TEXT        NOT NULL,
    expires TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS rogerai.tower_admissions (
    id            TEXT PRIMARY KEY,
    owner         TEXT        NOT NULL,
    -- UNIQUE is the one-key-one-Tower rule enforced by the database rather than by a
    -- read-then-check in application code: two concurrent enrollments presenting the same
    -- identity key cannot both win, whatever the callers happen to observe.
    key_hash      TEXT        NOT NULL UNIQUE,
    state         TEXT        NOT NULL,
    enrolled_at   TIMESTAMPTZ NOT NULL,
    lease_expires TIMESTAMPTZ NOT NULL,
    false_claims  INT         NOT NULL DEFAULT 0,
    rev           BIGINT      NOT NULL DEFAULT 1
);

-- The rest of the admission bundle, on the same row so it commits in the same write.
-- Additive and NULLable: a registry written before this keeps loading.
ALTER TABLE rogerai.tower_admissions ADD COLUMN IF NOT EXISTS tls_key_hash TEXT;
ALTER TABLE rogerai.tower_admissions ADD COLUMN IF NOT EXISTS lifecycle_revision BIGINT;
ALTER TABLE rogerai.tower_admissions ADD COLUMN IF NOT EXISTS lifecycle_hash TEXT;
ALTER TABLE rogerai.tower_admissions ADD COLUMN IF NOT EXISTS cert_serial TEXT;
ALTER TABLE rogerai.tower_admissions ADD COLUMN IF NOT EXISTS lease_sequence BIGINT;
ALTER TABLE rogerai.tower_admissions ADD COLUMN IF NOT EXISTS protocol_version INT;
-- Capabilities as JSON rather than a native array: the repo's driver is pgx and nothing
-- here queries INSIDE the list, so a text[] would mean taking on a second Postgres driver
-- purely to marshal one column.
ALTER TABLE rogerai.tower_admissions ADD COLUMN IF NOT EXISTS capabilities JSONB;
-- When this Tower last had its certificate reissued. It floors how often that may happen,
-- so a column that does not exist means the rate limit silently does not apply.
ALTER TABLE rogerai.tower_admissions ADD COLUMN IF NOT EXISTS renewed_at TIMESTAMPTZ;
-- One TLS key admits one Tower, for the same reason the identity key does. Partial, so the
-- NULLs of rows written before this column existed do not collide with each other.
CREATE UNIQUE INDEX IF NOT EXISTS tower_admissions_tls_key_uniq
    ON rogerai.tower_admissions (tls_key_hash) WHERE tls_key_hash IS NOT NULL;

CREATE INDEX IF NOT EXISTS tower_admissions_owner_idx ON rogerai.tower_admissions (owner);
-- The mint path checks how many live tokens an account already holds on every call, so that
-- lookup must not be a table scan.
CREATE INDEX IF NOT EXISTS tower_enrollment_tokens_owner_idx
    ON rogerai.tower_enrollment_tokens (owner);
`

// PGStore is the database-backed Store.
type PGStore struct{ db *sql.DB }

// NewPGStore wraps an already-open handle.
//
// It deliberately does NOT open its own connection: the broker already holds a pool to the
// authoritative database, and a second pool to the same server would double the connection
// footprint and give the registry a lifecycle of its own to get wrong. Whoever owns the
// pool closes it.
func NewPGStore(db *sql.DB) (*PGStore, error) {
	if db == nil {
		return nil, errors.New("a durable admission registry needs a database handle")
	}
	if err := pgmigrate.Apply(db, schema); err != nil {
		return nil, err
	}
	return &PGStore{db: db}, nil
}

func wrap(op string, err error) error {
	return fmt.Errorf("%w: %s: %v", ErrUnavailable, op, err)
}

func (p *PGStore) PutToken(t Token) error {
	_, err := p.db.Exec(
		`INSERT INTO rogerai.tower_enrollment_tokens(id,owner,expires) VALUES($1,$2,$3)
		 ON CONFLICT (id) DO NOTHING`,
		t.ID, t.Owner, t.Expires.UTC())
	if err != nil {
		return wrap("put token", err)
	}
	return nil
}

// PutTokenCapped serialises minting PER OWNER with a transaction-scoped advisory lock, then
// counts and inserts inside it.
//
// A conditional INSERT alone is not enough under READ COMMITTED: two transactions can both
// evaluate the count subquery before either commits, and both insert. The advisory lock is
// keyed on the owner, so it costs nothing across accounts and only ever serialises one
// account minting against itself - which is exactly the abuse being bounded.
func (p *PGStore) PutTokenCapped(t Token, max int) (bool, error) {
	tx, err := p.db.Begin()
	if err != nil {
		return false, wrap("put token", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtext($1))`, "tower-token:"+t.Owner); err != nil {
		return false, wrap("put token", err)
	}
	var live int
	if err := tx.QueryRow(
		`SELECT count(*) FROM rogerai.tower_enrollment_tokens WHERE owner=$1 AND expires >= $2`,
		t.Owner, time.Now().UTC()).Scan(&live); err != nil {
		return false, wrap("put token", err)
	}
	if live >= max {
		return false, nil
	}
	if _, err := tx.Exec(
		`INSERT INTO rogerai.tower_enrollment_tokens(id,owner,expires) VALUES($1,$2,$3)
		 ON CONFLICT (id) DO NOTHING`, t.ID, t.Owner, t.Expires.UTC()); err != nil {
		return false, wrap("put token", err)
	}
	if err := tx.Commit(); err != nil {
		return false, wrap("put token", err)
	}
	return true, nil
}

func (p *PGStore) GetToken(id string) (Token, bool, error) {
	var t Token
	err := p.db.QueryRow(
		`SELECT id,owner,expires FROM rogerai.tower_enrollment_tokens WHERE id=$1`, id).
		Scan(&t.ID, &t.Owner, &t.Expires)
	if errors.Is(err, sql.ErrNoRows) {
		return Token{}, false, nil
	}
	if err != nil {
		return Token{}, false, wrap("get token", err)
	}
	return t, true, nil
}

// ConsumeToken is a single DELETE whose row count IS the decision. Of two concurrent
// enrollments that both validated, exactly one deletes the row and the other sees zero -
// which is the one-time property, decided by the database rather than by a race.
func (p *PGStore) ConsumeToken(id string) (bool, error) {
	res, err := p.db.Exec(`DELETE FROM rogerai.tower_enrollment_tokens WHERE id=$1`, id)
	if err != nil {
		return false, wrap("consume token", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, wrap("consume token", err)
	}
	return n == 1, nil
}

func (p *PGStore) PutTower(tw Tower) error {
	if err := insertTowerIn(p.db, tw); err != nil {
		return wrap("put tower", err)
	}
	return nil
}

// CASTower applies a write only against the revision the caller read. Losing is not an
// error: it means somebody else moved this Tower first, and one of the things they may
// have moved it to is revoked.
// CASTower writes EVERY mutable column, not just the lifecycle ones.
//
// It previously listed seven, so a renewal against Postgres left the registry naming the
// OLD certificate serial - and a Tower cannot be revoked by a serial the registry does not
// hold. The memory store keeps the whole struct, so the two implementations disagreed and
// the renewal tests passed against both by asserting on the returned value rather than
// re-reading. Any column added to Tower has to be added here too; the contract test now
// re-reads, so forgetting one fails.
func (p *PGStore) CASTower(tw Tower) (bool, error) {
	res, err := p.db.Exec(
		`UPDATE rogerai.tower_admissions
		    SET owner=$2, key_hash=$3, state=$4, enrolled_at=$5,
		        lease_expires=$6, false_claims=$7, rev=rev+1,
		        tls_key_hash=$9, lifecycle_revision=$10, lifecycle_hash=$11,
		        cert_serial=$12, lease_sequence=$13, protocol_version=$14,
		        capabilities=$15, renewed_at=$16
		  WHERE id=$1 AND rev=$8`,
		tw.ID, tw.Owner, tw.KeyHash, string(tw.State),
		tw.EnrolledAt.UTC(), tw.LeaseExpires.UTC(), tw.FalseClaims, tw.Rev,
		nullString(tw.TLSKeyHash), nullInt64(tw.LifecycleRevision), nullString(tw.LifecycleHash),
		nullString(tw.CertSerial), nullInt64(tw.LeaseSequence), tw.ProtocolVersion,
		capabilitiesJSON(tw.Capabilities), nullTime(tw.RenewedAt))
	if err != nil {
		return false, wrap("cas tower", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, wrap("cas tower", err)
	}
	return n == 1, nil
}

const towerCols = `id,owner,key_hash,state,enrolled_at,lease_expires,false_claims,rev,` +
	`tls_key_hash,lifecycle_revision,lifecycle_hash,cert_serial,lease_sequence,protocol_version,capabilities,renewed_at`

func scanTower(row interface{ Scan(...any) error }) (Tower, error) {
	var tw Tower
	var state string
	var tlsKey, lifecycleHash, certSerial sql.NullString
	var lifecycleRev, leaseSeq sql.NullInt64
	var protocolVersion sql.NullInt32
	var capabilities []byte
	var renewedAt sql.NullTime
	err := row.Scan(&tw.ID, &tw.Owner, &tw.KeyHash, &state,
		&tw.EnrolledAt, &tw.LeaseExpires, &tw.FalseClaims, &tw.Rev,
		&tlsKey, &lifecycleRev, &lifecycleHash, &certSerial, &leaseSeq, &protocolVersion, &capabilities, &renewedAt)
	tw.State = State(state)
	tw.TLSKeyHash = tlsKey.String
	tw.LifecycleRevision = lifecycleRev.Int64
	tw.LifecycleHash = lifecycleHash.String
	tw.CertSerial = certSerial.String
	tw.LeaseSequence = leaseSeq.Int64
	tw.ProtocolVersion = int(protocolVersion.Int32)
	if renewedAt.Valid {
		tw.RenewedAt = renewedAt.Time
	}
	if len(capabilities) > 0 {
		// A capability list we cannot read is not a reason to hand back a Tower with no
		// capabilities, which would read as "this Tower may do nothing" - the caller sees
		// the decode failure instead.
		if jsonErr := json.Unmarshal(capabilities, &tw.Capabilities); jsonErr != nil && err == nil {
			return tw, jsonErr
		}
	}
	return tw, err
}

// insertTowerIn writes a Tower through whatever executor it is handed, so the same
// statement serves both the plain insert and the admission transaction.
func insertTowerIn(x interface {
	Exec(string, ...any) (sql.Result, error)
}, tw Tower) error {
	_, err := x.Exec(
		`INSERT INTO rogerai.tower_admissions
		   (id,owner,key_hash,state,enrolled_at,lease_expires,false_claims,rev,
		    tls_key_hash,lifecycle_revision,lifecycle_hash,cert_serial,lease_sequence,
		    protocol_version,capabilities,renewed_at)
		 VALUES($1,$2,$3,$4,$5,$6,$7,1,$8,$9,$10,$11,$12,$13,$14,$15)`,
		tw.ID, tw.Owner, tw.KeyHash, string(tw.State),
		tw.EnrolledAt.UTC(), tw.LeaseExpires.UTC(), tw.FalseClaims,
		nullString(tw.TLSKeyHash), nullInt64(tw.LifecycleRevision), nullString(tw.LifecycleHash),
		nullString(tw.CertSerial), nullInt64(tw.LeaseSequence), tw.ProtocolVersion,
		capabilitiesJSON(tw.Capabilities), nullTime(tw.RenewedAt))
	return err
}

// capabilitiesJSON renders the list for storage. An empty list is NULL rather than "[]" so
// a row written before this column existed and a Tower that requested nothing read alike.
func capabilitiesJSON(caps []string) any {
	if len(caps) == 0 {
		return nil
	}
	b, err := json.Marshal(caps)
	if err != nil {
		return nil
	}
	return b
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}

func nullInt64(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}

// Admit consumes the token and inserts the Tower in ONE transaction: the whole bundle
// commits or none of it does. A failed insert rolls the token consumption back with it,
// so a rejected attempt leaves the token usable for the operator's next try.
func (p *PGStore) Admit(tokenID string, tw Tower) (bool, error) {
	tx, err := p.db.Begin()
	if err != nil {
		return false, wrap("admit", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	// DELETE ... RETURNING is the consume and the check in one statement, and the row lock
	// it takes is what makes concurrent admissions on one token serialise: the second
	// transaction blocks here and then finds nothing.
	var owner string
	err = tx.QueryRow(
		`DELETE FROM rogerai.tower_enrollment_tokens WHERE id=$1 RETURNING owner`, tokenID).
		Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil // already spent, or never existed
	}
	if err != nil {
		return false, wrap("admit", err)
	}
	if err := insertTowerIn(tx, tw); err != nil {
		// Rolls back the token consumption too.
		return false, fmt.Errorf("that Tower could not be admitted: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, wrap("admit", err)
	}
	return true, nil
}

func (p *PGStore) TowerByID(id string) (Tower, bool, error) {
	tw, err := scanTower(p.db.QueryRow(
		`SELECT `+towerCols+` FROM rogerai.tower_admissions WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Tower{}, false, nil
	}
	if err != nil {
		return Tower{}, false, wrap("tower by id", err)
	}
	return tw, true, nil
}

func (p *PGStore) TowerByKey(keyHash string) (Tower, bool, error) {
	tw, err := scanTower(p.db.QueryRow(
		`SELECT `+towerCols+` FROM rogerai.tower_admissions WHERE key_hash=$1`, keyHash))
	if errors.Is(err, sql.ErrNoRows) {
		return Tower{}, false, nil
	}
	if err != nil {
		return Tower{}, false, wrap("tower by key", err)
	}
	return tw, true, nil
}

func (p *PGStore) TowersByOwner(owner string) ([]Tower, error) {
	rows, err := p.db.Query(
		`SELECT `+towerCols+` FROM rogerai.tower_admissions WHERE owner=$1 ORDER BY enrolled_at`, owner)
	if err != nil {
		return nil, wrap("towers by owner", err)
	}
	defer rows.Close()
	var out []Tower
	for rows.Next() {
		tw, err := scanTower(rows)
		if err != nil {
			return nil, wrap("towers by owner", err)
		}
		out = append(out, tw)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap("towers by owner", err)
	}
	return out, nil
}

// LiveTokens lists an owner's unspent, unexpired tokens. Indexed by owner so the cap check
// on the mint path stays a cheap lookup rather than a scan.
func (p *PGStore) LiveTokens(owner string, now time.Time) ([]string, error) {
	rows, err := p.db.Query(
		`SELECT id FROM rogerai.tower_enrollment_tokens WHERE owner=$1 AND expires >= $2`,
		owner, now.UTC())
	if err != nil {
		return nil, wrap("live tokens", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, wrap("live tokens", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap("live tokens", err)
	}
	return out, nil
}

func (p *PGStore) ReapTokens(now time.Time) error {
	_, err := p.db.Exec(`DELETE FROM rogerai.tower_enrollment_tokens WHERE expires < $1`, now.UTC())
	if err != nil {
		return wrap("reap tokens", err)
	}
	return nil
}
