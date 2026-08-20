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
	"sort"
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
-- AND THE SAME UNIQUENESS OVER THE STATES THAT HOLD A KEY, which is one state wider: dormant.
-- A dormant Station is asleep, not gone, and its assertion key is PUBLIC material that rides in
-- the clear on every hub poll - so if going quiet freed the key, anyone could bind it to a
-- Station of their own and the rightful owner's return would be refused for a key they never
-- gave up. Terminal rows still release theirs.
--
-- ADDED BESIDE THE LIVE INDEXES RATHER THAN REPLACING THEM. These predicates are a strict
-- superset, so the pair is redundant rather than contradictory, and adding an index is a safe
-- migration where dropping a live uniqueness constraint and rebuilding it is a window in which
-- there is none. The old pair can go in a later, deliberate migration.
CREATE UNIQUE INDEX IF NOT EXISTS station_attachments_held_assertion_key
    ON rogerai.station_attachments (assertion_key)
    WHERE state IN ('quarantine','active','dormant');
CREATE UNIQUE INDEX IF NOT EXISTS station_attachments_held_session_key
    ON rogerai.station_attachments (session_key)
    WHERE state IN ('quarantine','active','dormant');
CREATE INDEX IF NOT EXISTS station_attachments_owner ON rogerai.station_attachments (owner);
-- Option C self-attach: the node's bearer token for its Tower's data-plane hub. Plaintext,
-- like the broker's node BridgeToken - the Tower must compare the exact presented value.
ALTER TABLE rogerai.station_authorizations ADD COLUMN IF NOT EXISTS hub_token TEXT NOT NULL DEFAULT '';
ALTER TABLE rogerai.station_attachments  ADD COLUMN IF NOT EXISTS hub_token TEXT NOT NULL DEFAULT '';
ALTER TABLE rogerai.station_attachments  ADD COLUMN IF NOT EXISTS audit_proven_at TIMESTAMPTZ;
-- The self-attached node's offer: model/modality + micro-USD-per-1M-token prices.
ALTER TABLE rogerai.station_authorizations ADD COLUMN IF NOT EXISTS model TEXT NOT NULL DEFAULT '';
ALTER TABLE rogerai.station_authorizations ADD COLUMN IF NOT EXISTS modality TEXT NOT NULL DEFAULT '';
ALTER TABLE rogerai.station_authorizations ADD COLUMN IF NOT EXISTS price_in  BIGINT NOT NULL DEFAULT 0;
ALTER TABLE rogerai.station_authorizations ADD COLUMN IF NOT EXISTS price_out BIGINT NOT NULL DEFAULT 0;
ALTER TABLE rogerai.station_attachments ADD COLUMN IF NOT EXISTS model TEXT NOT NULL DEFAULT '';
ALTER TABLE rogerai.station_attachments ADD COLUMN IF NOT EXISTS modality TEXT NOT NULL DEFAULT '';
ALTER TABLE rogerai.station_attachments ADD COLUMN IF NOT EXISTS price_in  BIGINT NOT NULL DEFAULT 0;
ALTER TABLE rogerai.station_attachments ADD COLUMN IF NOT EXISTS price_out BIGINT NOT NULL DEFAULT 0;
-- node_id joins a Station to the BROKER registration for the same machine, so edge
-- placement can read the reliability, TTFT and TPS that probes record against the node id.
-- Additive and defaulted: every row written before this migration has no roger-share half
-- to point at, and an empty join key reads as "unmeasured" rather than as a broken row.
ALTER TABLE rogerai.station_authorizations ADD COLUMN IF NOT EXISTS node_id TEXT NOT NULL DEFAULT '';
ALTER TABLE rogerai.station_attachments    ADD COLUMN IF NOT EXISTS node_id TEXT NOT NULL DEFAULT '';
-- The lookup edge placement will make is "which attachments belong to this node id", and
-- the one a re-attach makes is "does this node already have a station".
CREATE INDEX IF NOT EXISTS station_attachments_node_id_idx
  ON rogerai.station_attachments (node_id) WHERE node_id <> '';
-- last_routable is when some instance last saw the MACHINE behind this Station alive while
-- publishing it as routable. It is the evidence DetachIdle acts on, and it is NULLABLE
-- rather than defaulted: a row written before this column existed has never been stamped,
-- and "never stamped" has to read as "measure it from attached_at" rather than as "last seen
-- at the zero time" - the second reading would retire the entire existing fleet on the first
-- sweep. Nothing SELECTs it into an Attachment; it is housekeeping, not identity.
ALTER TABLE rogerai.station_attachments ADD COLUMN IF NOT EXISTS last_routable TIMESTAMPTZ;
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
		   ceiling_hash,secret_hash,role,hub_token,node_id,model,modality,price_in,price_out,
		   issued_at,expires_at,consumed,consumed_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)
		ON CONFLICT (id) DO UPDATE SET
		  network=EXCLUDED.network, station_id=EXCLUDED.station_id, owner=EXCLUDED.owner,
		  origin_kind=EXCLUDED.origin_kind, origin_tower=EXCLUDED.origin_tower,
		  assertion_key=EXCLUDED.assertion_key, session_key=EXCLUDED.session_key,
		  ceiling_hash=EXCLUDED.ceiling_hash, secret_hash=EXCLUDED.secret_hash,
		  role=EXCLUDED.role, hub_token=EXCLUDED.hub_token, node_id=EXCLUDED.node_id,
		  model=EXCLUDED.model,
		  modality=EXCLUDED.modality, price_in=EXCLUDED.price_in, price_out=EXCLUDED.price_out,
		  issued_at=EXCLUDED.issued_at, expires_at=EXCLUDED.expires_at`,
		a.ID, a.Network, a.StationID, a.Owner, a.Origin.Kind, a.Origin.TowerID,
		a.AssertionKey, a.SessionKey, a.CeilingHash, a.SecretHash, a.Role, a.HubToken, a.NodeID,
		a.Model, a.Modality, a.PriceIn, a.PriceOut,
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
		   ceiling_hash,secret_hash,role,hub_token,node_id,model,modality,price_in,price_out,
		   issued_at,expires_at,consumed,consumed_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,false,'')`,
		a.ID, a.Network, a.StationID, a.Owner, a.Origin.Kind, a.Origin.TowerID,
		a.AssertionKey, a.SessionKey, a.CeilingHash, a.SecretHash, a.Role, a.HubToken, a.NodeID,
		a.Model, a.Modality, a.PriceIn, a.PriceOut,
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
		       ceiling_hash,secret_hash,role,hub_token,node_id,model,modality,price_in,price_out,
		       issued_at,expires_at,consumed,consumed_by
		  FROM rogerai.station_authorizations WHERE id=$1`, id).
		Scan(&a.ID, &a.Network, &a.StationID, &a.Owner, &a.Origin.Kind, &a.Origin.TowerID,
			&a.AssertionKey, &a.SessionKey, &a.CeilingHash, &a.SecretHash, &a.Role, &a.HubToken,
			&a.NodeID, &a.Model, &a.Modality, &a.PriceIn, &a.PriceOut,
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

	// THE ONE ROW THIS MAY WRITE OVER IS A DORMANT ONE BELONGING TO THE SAME MACHINE.
	//
	// The plain INSERT this replaces made a returning Station structurally impossible: the
	// station_id primary key refused it, so a machine coming back after a long silence was told
	// its own identity was taken. The conflict clause is scoped as narrowly as the recovery it
	// exists for - dormant state, same owner, same origin kind, same assertion key, same session
	// key - and every other conflict still lands in the constraint-violation branch below, which
	// is the answer a live, revoked or detached row deserves.
	//
	// last_routable is NULLED on the way through. It is the stamp from the machine's previous
	// life, and leaving it would put the fresh attachment straight back over the idle horizon:
	// retired again on the next sweep, seconds after coming home. audit_proven_at is left
	// untouched deliberately - it is a fact about the machine, and Registry.Admit carries the
	// same value forward on its own copy so the two stores agree.
	res, err = tx.Exec(`
		INSERT INTO rogerai.station_attachments
		  (station_id,owner,assertion_key,session_key,origin_kind,origin_tower,epoch,
		   ceiling_hash,state,attached_at,auth_id,hub_token,node_id,model,modality,price_in,price_out)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		ON CONFLICT (station_id) DO UPDATE SET
		    origin_tower=EXCLUDED.origin_tower, epoch=EXCLUDED.epoch,
		    ceiling_hash=EXCLUDED.ceiling_hash, state=EXCLUDED.state,
		    attached_at=EXCLUDED.attached_at, auth_id=EXCLUDED.auth_id,
		    hub_token=EXCLUDED.hub_token, node_id=EXCLUDED.node_id, model=EXCLUDED.model,
		    modality=EXCLUDED.modality, price_in=EXCLUDED.price_in, price_out=EXCLUDED.price_out,
		    last_routable=NULL
		  WHERE rogerai.station_attachments.state = 'dormant'
		    AND rogerai.station_attachments.owner = EXCLUDED.owner
		    AND rogerai.station_attachments.origin_kind = EXCLUDED.origin_kind
		    AND rogerai.station_attachments.assertion_key = EXCLUDED.assertion_key
		    AND rogerai.station_attachments.session_key = EXCLUDED.session_key`,
		at.StationID, at.Owner, at.AssertionKey, at.SessionKey, at.Origin.Kind,
		at.Origin.TowerID, at.Epoch, at.CeilingHash, at.State, at.AttachedAt.UTC(),
		at.AuthID, at.HubToken, at.NodeID, at.Model, at.Modality, at.PriceIn, at.PriceOut)
	if err != nil {
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
	// A CONFLICT WHOSE `WHERE` DID NOT HOLD AFFECTS NO ROWS AND RAISES NO ERROR, so silence here
	// is a refusal rather than a success: the Station ID exists and is not a dormant row this
	// machine may wake. Rolling back leaves the invitation unspent, which is what a refused
	// attachment is owed.
	if n, _ := res.RowsAffected(); n == 0 {
		return false, reject(errors.New("that Station ID or key is already attached"))
	}
	if err := tx.Commit(); err != nil {
		return false, pgwrap("commit", err)
	}
	return true, nil
}

const attachCols = `station_id,owner,assertion_key,session_key,origin_kind,origin_tower,
                    epoch,ceiling_hash,state,attached_at,auth_id,audit_proven_at,hub_token,node_id,model,modality,
                    price_in,price_out`

func scanAttachment(row interface{ Scan(...any) error }) (Attachment, error) {
	var at Attachment
	// NULL audit_proven_at means "has never answered an audit", which is the state every
	// attachment starts in and the one older rows are already in.
	var proven sql.NullTime
	err := row.Scan(&at.StationID, &at.Owner, &at.AssertionKey, &at.SessionKey,
		&at.Origin.Kind, &at.Origin.TowerID, &at.Epoch, &at.CeilingHash, &at.State,
		&at.AttachedAt, &at.AuthID, &proven, &at.HubToken, &at.NodeID, &at.Model, &at.Modality,
		&at.PriceIn, &at.PriceOut)
	if proven.Valid {
		at.AuditProvenAt = proven.Time.UTC()
	}
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

// ByStations answers for a whole placement in one round trip. `= ANY($1)` rather than a
// generated IN-list: one prepared statement whatever N is, no string building on a path that
// takes caller-supplied ids, and the driver already carries []string as text[] (the ledger's
// kind filter in internal/store does the same).
//
// State-agnostic, exactly like ByStation - see the Store interface for why the batch form must
// not quietly become stricter than the singular one.
func (p *PGStore) ByStations(stationIDs []string) (map[string]Attachment, error) {
	out := make(map[string]Attachment, len(stationIDs))
	if len(stationIDs) == 0 {
		// Not merely an optimization: `= ANY('{}')` is a round trip that can only return
		// nothing, and this is called on the authorize path.
		return out, nil
	}
	rows, err := p.db.Query(`SELECT `+attachCols+`
		FROM rogerai.station_attachments WHERE station_id = ANY($1)`, stationIDs)
	if err != nil {
		return nil, pgwrap("read attachments", err)
	}
	defer rows.Close()
	for rows.Next() {
		at, serr := scanAttachment(rows)
		if serr != nil {
			return nil, pgwrap("read attachments", serr)
		}
		out[at.StationID] = at
	}
	if err := rows.Err(); err != nil {
		return nil, pgwrap("read attachments", err)
	}
	return out, nil
}

// TouchRoutable stamps the liveness evidence for a whole Tower's Stations in one statement.
// Rows that do not exist are simply not updated, which is what the Mem store also does.
func (p *PGStore) TouchRoutable(stationIDs []string, at time.Time) error {
	if len(stationIDs) == 0 {
		return nil
	}
	if _, err := p.db.Exec(`UPDATE rogerai.station_attachments
		SET last_routable = $2 WHERE station_id = ANY($1)`, stationIDs, at.UTC()); err != nil {
		return pgwrap("stamp routable", err)
	}
	return nil
}

// DetachIdle retires the quiet attachments and RETURNS which, so the caller can say out loud
// what it retired. One statement, so the read and the write cannot disagree: selecting the
// candidates first and updating them after would retire a Station that got stamped in
// between, which is the machine coming back at exactly the wrong moment.
//
// THE NODE-ID FILTER IS THE WHOLE CORRECTNESS ARGUMENT, not a clause added for tidiness - see
// the Store interface for why. It is also the predicate the partial index
// station_attachments_node_id_idx is built on, so the scoping costs nothing.
func (p *PGStore) DetachIdle(towerID string, before time.Time) ([]string, error) {
	rows, err := p.db.Query(`UPDATE rogerai.station_attachments
		   SET state = $3
		 WHERE origin_tower = $1
		   AND state IN ('quarantine','active')
		   AND node_id <> ''
		   AND COALESCE(last_routable, attached_at) < $2
		RETURNING station_id`, towerID, before.UTC(), StateDormant)
	if err != nil {
		return nil, pgwrap("detach idle attachments", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if serr := rows.Scan(&id); serr != nil {
			return nil, pgwrap("detach idle attachments", serr)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, pgwrap("detach idle attachments", err)
	}
	// UPDATE ... RETURNING has no defined order; the Mem store sorts, so this does too.
	sort.Strings(out)
	return out, nil
}

// ByAssertionKey and BySessionKey look at the rows that HOLD a key - live plus dormant -
// matching the partial indexes and the Mem store. A dormant Station's keys are still its own
// (see StateDormant); a terminal Station's are free again.
func (p *PGStore) ByAssertionKey(key string) (Attachment, bool, error) {
	return p.byLiveKey("assertion_key", key)
}

func (p *PGStore) BySessionKey(key string) (Attachment, bool, error) {
	return p.byLiveKey("session_key", key)
}

func (p *PGStore) byLiveKey(col, key string) (Attachment, bool, error) {
	at, err := scanAttachment(p.db.QueryRow(
		`SELECT `+attachCols+` FROM rogerai.station_attachments
		  WHERE `+col+`=$1 AND state IN ('quarantine','active','dormant')`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return Attachment{}, false, nil
	}
	if err != nil {
		return Attachment{}, false, pgwrap("read attachment by key", err)
	}
	return at, true, nil
}

// MarkAuditProven stamps the first answered audit; the IS NULL guard makes it idempotent, so
// the recorded moment stays the moment the Station actually proved itself.
func (p *PGStore) MarkAuditProven(stationID string, at time.Time) (bool, error) {
	res, err := p.db.Exec(`UPDATE rogerai.station_attachments SET audit_proven_at=$2
	                        WHERE station_id=$1 AND audit_proven_at IS NULL`, stationID, at.UTC())
	if err != nil {
		return false, pgwrap("mark audit proven", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
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

// ByTower lists the LIVE attachments served through one Tower - what that Tower's hub must
// serve. Live states only, matching the partial indexes and the Mem store.
func (p *PGStore) ByTower(towerID string) ([]Attachment, error) {
	rows, err := p.db.Query(`SELECT `+attachCols+` FROM rogerai.station_attachments
		WHERE origin_tower=$1 AND state IN ('quarantine','active')`, towerID)
	if err != nil {
		return nil, pgwrap("list attachments by tower", err)
	}
	defer rows.Close()
	var out []Attachment
	for rows.Next() {
		at, serr := scanAttachment(rows)
		if serr != nil {
			return nil, pgwrap("list attachments by tower", serr)
		}
		out = append(out, at)
	}
	return out, rows.Err()
}

// RetireDormant is the second, much later horizon: a dormant Station nobody has seen since
// `before` becomes terminal. One statement, like DetachIdle, and measured on the same
// COALESCE(last_routable, attached_at) so the two horizons are two points on one timeline.
//
// Fleet-wide rather than per Tower: this is the pass that ends an identity, and it belongs
// beside the other irreversible housekeeping rather than inside the per-Tower publish loop.
func (p *PGStore) RetireDormant(before time.Time) (int64, error) {
	res, err := p.db.Exec(`UPDATE rogerai.station_attachments
		   SET state = $2
		 WHERE state = 'dormant'
		   AND COALESCE(last_routable, attached_at) < $1`, before.UTC(), StateDetached)
	if err != nil {
		return 0, pgwrap("retire dormant attachments", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ReapTerminal deletes revoked/detached attachments attached before the horizon (see the
// Store interface for why terminal rows cannot be kept forever).
func (p *PGStore) ReapTerminal(before time.Time) (int64, error) {
	res, err := p.db.Exec(`DELETE FROM rogerai.station_attachments
		WHERE state IN ('revoked','detached') AND attached_at <= $1`, before.UTC())
	if err != nil {
		return 0, pgwrap("reap terminal attachments", err)
	}
	return res.RowsAffected()
}
