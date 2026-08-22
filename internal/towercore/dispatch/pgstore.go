package dispatch

// pgstore.go is the durable attempt store: the one place a fleet of brokers can agree that
// an attempt has been claimed, or settled, exactly once.
//
// # WHY IT HAS TO BE DURABLE
//
// Production runs more than one broker. With the attempt table in each process, the two
// guarantees this package exists for stop being guarantees: "at most one attempt reaches
// executing state" holds per instance while a Tower polling both is handed the same work
// twice, and "at most one result can settle" holds per instance while a result posted to
// each is accepted by each. Neither failure is visible from either side - both brokers are
// behaving perfectly correctly, over half the truth.
//
// It is also what makes the poll work at all across a fleet. A Tower reaches whichever
// instance the load balancer chose, which is very often not the one that created its work.
//
// # EVERY TRANSITION IS A CONDITIONAL UPDATE
//
// Not a SELECT then an UPDATE. The state to move FROM is in the WHERE clause and the row
// count is the answer: exactly one caller can move an attempt out of `issued`, and exactly
// one out of `claimed`, no matter how many are trying. Reading first and writing after is
// the race the whole design removes - both read `issued`, both proceed, and the work happens
// twice on somebody's hardware while a caller is charged for one of them.
//
// When the swap wins we know why. When it loses we read the row back to say which of "gone",
// "already taken", "already settled" or "too late" it was, because a caller acts differently
// on each - and that read is only ever used to EXPLAIN a decision the database already made.

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"rogerai.fm/roger/v6/internal/pgmigrate"
)

// schema is applied on first use. Additive and idempotent, and it creates TABLES only -
// `rogerai` is provisioned by an admin and owned by the app's least-privilege user. CREATE
// SCHEMA IF NOT EXISTS is deliberately absent: PostgreSQL checks CREATE-on-database before
// the IF-NOT-EXISTS short-circuit, so it fails with "permission denied for database" even
// when the schema is already there.
const schema = `
CREATE TABLE IF NOT EXISTS rogerai.tower_attempts (
    attempt_id     TEXT PRIMARY KEY,
    job_id         TEXT        NOT NULL,
    tower_id       TEXT        NOT NULL,
    station_id     TEXT        NOT NULL,
    station_epoch  BIGINT      NOT NULL DEFAULT 0,
    model          TEXT        NOT NULL,
    modality       TEXT        NOT NULL,
    request_digest TEXT        NOT NULL,
    nonce          TEXT        NOT NULL,
    deadline       TIMESTAMPTZ NOT NULL,
    -- The signed grant, relayed verbatim to the Tower. Stored so ANY instance can hand out
    -- work another instance created.
    grant_signed   BYTEA       NOT NULL,
    -- The exact request the grant commits to. Stored so ANY instance can hand out work
    -- another instance created; the digest alone would leave only the issuer able to serve.
    request        BYTEA       NOT NULL DEFAULT '',
    -- The Station's assertion key as recorded at attachment, hex. The receipt is verified
    -- against it, and the verifying instance is very often not the issuing one.
    assertion_key  TEXT        NOT NULL,
    state          TEXT        NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- The poll's index: find this Tower's next unclaimed attempt without scanning the settled
-- history of every other Tower.
CREATE INDEX IF NOT EXISTS tower_attempts_pending
    ON rogerai.tower_attempts (tower_id, state, deadline);
-- The reaper's index.
CREATE INDEX IF NOT EXISTS tower_attempts_deadline
    ON rogerai.tower_attempts (deadline);
-- ADDITIVE, and not merely present in the CREATE above. A column added to a CREATE TABLE IF
-- NOT EXISTS body never reaches a database that already has the table - the statement is a
-- no-op there - so every column added after the first release needs its own ALTER. Caught by
-- a test database that had the earlier shape, which is exactly the situation a deployed
-- broker would have been in.
ALTER TABLE rogerai.tower_attempts ADD COLUMN IF NOT EXISTS request BYTEA NOT NULL DEFAULT '';
ALTER TABLE rogerai.tower_attempts ADD COLUMN IF NOT EXISTS consumer_key BYTEA NOT NULL DEFAULT '';
`

// PGStore is the durable attempt store.
type PGStore struct{ db *sql.DB }

// NewPGStore prepares the durable store, applying the schema.
func NewPGStore(db *sql.DB) (*PGStore, error) {
	if db == nil {
		return nil, errors.New("a durable attempt store needs a database handle")
	}
	if err := pgmigrate.Apply(db, schema); err != nil {
		return nil, err
	}
	return &PGStore{db: db}, nil
}

const attemptColumns = `attempt_id, job_id, tower_id, station_id, station_epoch, model,
	modality, request_digest, nonce, deadline, grant_signed, request, assertion_key,
	consumer_key, state`

func (p *PGStore) Put(r Record) error {
	// A nil request cannot happen through Issue, which refuses one - but nil and empty are
	// the same thing to every reader here, and a NOT NULL violation is an opaque SQLSTATE at
	// the wrong end of the call stack from whatever produced it.
	if r.Request == nil {
		r.Request = []byte{}
	}
	if r.Grant == nil {
		r.Grant = []byte{}
	}
	if r.ConsumerKey == nil {
		r.ConsumerKey = []byte{}
	}
	_, err := p.db.Exec(`
		INSERT INTO rogerai.tower_attempts (`+attemptColumns+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		-- An attempt id is minted from crypto/rand, so a collision is not a case to merge:
		-- it is a case that does not happen, and DO NOTHING keeps a retry idempotent rather
		-- than letting one attempt quietly overwrite another.
		ON CONFLICT (attempt_id) DO NOTHING`,
		r.AttemptID, r.JobID, r.TowerID, r.StationID, r.StationEpoch, r.Model, r.Modality,
		r.RequestDigest, r.Nonce, r.Deadline, r.Grant, r.Request,
		hex.EncodeToString(r.AssertionKey), r.ConsumerKey, r.State)
	return err
}

func (p *PGStore) Get(attemptID string) (Record, bool, error) {
	row := p.db.QueryRow(`SELECT `+attemptColumns+`
		FROM rogerai.tower_attempts WHERE attempt_id = $1`, attemptID)
	return scanAttempt(row)
}

// ClaimByID moves one named attempt from issued to claimed.
func (p *PGStore) ClaimByID(attemptID, towerID string, now time.Time) (Record, error) {
	row := p.db.QueryRow(`
		UPDATE rogerai.tower_attempts
		   SET state = $1
		 WHERE attempt_id = $2
		   AND tower_id   = $3
		   AND state      = $4
		   AND deadline   > $5
		RETURNING `+attemptColumns,
		StateClaimed, attemptID, towerID, StateIssued, now)

	rec, ok, err := scanAttempt(row)
	if err != nil {
		return Record{}, err
	}
	if ok {
		return rec, nil
	}
	// The swap lost. The row read below only EXPLAINS that - it never changes the outcome.
	return Record{}, p.whyNot(attemptID, towerID, now)
}

// ClaimNext takes any one attempt waiting for this Tower.
//
// FOR UPDATE SKIP LOCKED is the whole trick: two instances polling for the same Tower at the
// same moment take DIFFERENT rows instead of blocking on each other or handing out the same
// one. The inner select is ordered so the oldest work goes first - a caller who has been
// waiting longest should not be overtaken by one who just arrived.
func (p *PGStore) ClaimNext(towerID string, now time.Time) (Record, bool, error) {
	row := p.db.QueryRow(`
		UPDATE rogerai.tower_attempts
		   SET state = $1
		 WHERE attempt_id = (
		       SELECT attempt_id FROM rogerai.tower_attempts
		        WHERE tower_id = $2 AND state = $3 AND deadline > $4
		        ORDER BY created_at
		        FOR UPDATE SKIP LOCKED
		        LIMIT 1)
		RETURNING `+attemptColumns,
		StateClaimed, towerID, StateIssued, now)

	rec, ok, err := scanAttempt(row)
	if err != nil {
		return Record{}, false, err
	}
	return rec, ok, nil
}

// Settle moves claimed to settled, once.
func (p *PGStore) Settle(attemptID string, now time.Time) (Record, error) {
	row := p.db.QueryRow(`
		UPDATE rogerai.tower_attempts
		   SET state = $1
		 WHERE attempt_id = $2
		   AND state      = $3
		   AND deadline   > $4
		RETURNING `+attemptColumns,
		StateSettled, attemptID, StateClaimed, now)

	rec, ok, err := scanAttempt(row)
	if err != nil {
		return Record{}, err
	}
	if ok {
		return rec, nil
	}
	return Record{}, p.whySettleFailed(attemptID, now)
}

// Reap drops attempts past their deadline. Nothing may settle after one, so dropping them is
// safe - and an attempt table that only grows is a memory leak with a deadline attached.
func (p *PGStore) Reap(before time.Time) (int64, error) {
	res, err := p.db.Exec(`DELETE FROM rogerai.tower_attempts WHERE deadline <= $1`, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// whyNot turns a lost claim into the reason for it.
func (p *PGStore) whyNot(attemptID, towerID string, now time.Time) error {
	rec, ok, err := p.Get(attemptID)
	if err != nil {
		return err
	}
	// Unknown, or somebody else's: the same answer either way. An attempt id is not a secret,
	// and telling one Tower that another Tower's attempt exists is an oracle it has no
	// business having.
	if !ok || rec.TowerID != towerID {
		return ErrNotFound
	}
	switch {
	case rec.State == StateSettled:
		return ErrAlreadySettled
	case rec.State == StateClaimed:
		return ErrAlreadyClaimed
	case !now.Before(rec.Deadline):
		return ErrExpired
	}
	// The row is claimable and our swap still lost, which means somebody claimed and released
	// it between the two statements. Reported as already claimed: from here it is the same
	// situation, and inventing a fifth answer would be describing our own read rather than
	// the attempt.
	return ErrAlreadyClaimed
}

func (p *PGStore) whySettleFailed(attemptID string, now time.Time) error {
	rec, ok, err := p.Get(attemptID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	switch {
	case rec.State == StateSettled:
		return ErrAlreadySettled
	case rec.State == StateIssued:
		return ErrNotClaimed
	case !now.Before(rec.Deadline):
		return ErrExpired
	}
	return ErrAlreadySettled
}

type scanner interface{ Scan(dest ...any) error }

func scanAttempt(row scanner) (Record, bool, error) {
	var r Record
	var keyHex string
	err := row.Scan(&r.AttemptID, &r.JobID, &r.TowerID, &r.StationID, &r.StationEpoch,
		&r.Model, &r.Modality, &r.RequestDigest, &r.Nonce, &r.Deadline, &r.Grant,
		&r.Request, &keyHex, &r.ConsumerKey, &r.State)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	key, derr := hex.DecodeString(keyHex)
	if derr != nil {
		// A key we cannot decode is a key we cannot verify a receipt against, and treating it
		// as absent would mean accepting whatever the relay sent.
		return Record{}, false, errors.New("the recorded Station key is unreadable")
	}
	r.AssertionKey = key
	// Postgres hands back its own location; the caller compares against wall-clock time.
	r.Deadline = r.Deadline.UTC()
	return r, true, nil
}
