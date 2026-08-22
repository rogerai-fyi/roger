package dispatch

// ackstore.go holds consumer acknowledgements until the attempt they belong to settles.
//
// Contract: features/tower/edge_dispatch.feature.
//
// # WHY IT HAS TO BE SHARED
//
// The acknowledgement and the settlement do not arrive at the same broker. The consumer acks
// whichever instance the load balancer picked, and the Station's receipt arrives at whichever
// one its Tower reached. With this in one process's memory, settlement would find no
// acknowledgement almost every time on a multi-instance deployment and mark honest attempts
// uncorroborated - which is not a crash, not an error, and would quietly show up as an
// operator's uncorroborated rate looking suspicious for reasons that had nothing to do with
// them.
//
// # FIRST WRITE WINS
//
// An acknowledgement is a one-time statement about what a consumer received. A second one
// for the same attempt is either a retry (identical, so nothing to do) or an attempt to
// revise evidence after the fact (which is exactly what must not be possible). Both are
// handled by refusing to overwrite - and the caller is not told which, because "your first
// acknowledgement stands" is the whole answer either way.

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"sync"
	"time"

	"rogerai.fm/roger/v6/internal/pgmigrate"
)

// AckStore is where acknowledgements wait for their settlement.
type AckStore interface {
	// Put records an acknowledgement. First write wins; a later one is silently kept out.
	Put(attemptID string, a Ack) error
	Get(attemptID string) (Ack, bool, error)
	// Reap drops acknowledgements older than a cutoff. An attempt that never settled cannot
	// settle later, and a table that only grows is a leak with a deadline attached.
	Reap(before time.Time) (int64, error)
}

// NewAckMemStore is the in-process store. Correct for a single broker, and the reference the
// durable one is held against.
func NewAckMemStore() AckStore {
	return &ackMem{by: map[string]ackRow{}}
}

type ackRow struct {
	ack Ack
	at  time.Time
}

type ackMem struct {
	mu sync.Mutex
	by map[string]ackRow
}

func (m *ackMem) Put(attemptID string, a Ack) error {
	if attemptID == "" {
		return errors.New("an acknowledgement is stored against an attempt")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.by[attemptID]; exists {
		return nil // first write wins
	}
	m.by[attemptID] = ackRow{ack: a, at: time.Now()}
	return nil
}

func (m *ackMem) Get(attemptID string) (Ack, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.by[attemptID]
	return row.ack, ok, nil
}

func (m *ackMem) Reap(before time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for id, row := range m.by {
		if row.at.Before(before) {
			delete(m.by, id)
			n++
		}
	}
	return n, nil
}

// ackSchema is applied on first use. TABLES only - `rogerai` is provisioned by an admin and
// owned by the app's least-privilege user, and CREATE SCHEMA IF NOT EXISTS fails with
// "permission denied for database" even when the schema is already there.
const ackSchema = `
CREATE TABLE IF NOT EXISTS rogerai.tower_acks (
    attempt_id      TEXT PRIMARY KEY,
    response_digest TEXT        NOT NULL,
    usage_in        BIGINT      NOT NULL,
    usage_out       BIGINT      NOT NULL,
    first_byte      TIMESTAMPTZ NOT NULL,
    completed       TIMESTAMPTZ NOT NULL,
    -- The signed object itself, kept so a settlement can be re-checked later by somebody who
    -- was not here. A digest we merely copied out would be our word for what the consumer
    -- said, which is exactly what this evidence exists to avoid being.
    signed          TEXT        NOT NULL,
    recorded_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS tower_acks_recorded ON rogerai.tower_acks (recorded_at);
`

// AckPGStore is the durable store.
type AckPGStore struct{ db *sql.DB }

// NewAckPGStore prepares the durable store, applying the schema.
func NewAckPGStore(db *sql.DB) (*AckPGStore, error) {
	if db == nil {
		return nil, errors.New("a durable acknowledgement store needs a database handle")
	}
	if err := pgmigrate.Apply(db, ackSchema); err != nil {
		return nil, err
	}
	return &AckPGStore{db: db}, nil
}

func (p *AckPGStore) Put(attemptID string, a Ack) error {
	if attemptID == "" {
		return errors.New("an acknowledgement is stored against an attempt")
	}
	// DO NOTHING is the first-write-wins rule: a retry is idempotent and a revision is
	// refused, by the same clause, without the caller having to tell us which it was.
	_, err := p.db.Exec(`
		INSERT INTO rogerai.tower_acks
		    (attempt_id, response_digest, usage_in, usage_out, first_byte, completed, signed)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (attempt_id) DO NOTHING`,
		attemptID, a.ResponseDigest, a.Usage.In, a.Usage.Out, a.FirstByte.UTC(),
		a.Completed.UTC(), base64.StdEncoding.EncodeToString(a.Signed))
	return err
}

func (p *AckPGStore) Get(attemptID string) (Ack, bool, error) {
	var a Ack
	var signed string
	err := p.db.QueryRow(`
		SELECT attempt_id, response_digest, usage_in, usage_out, first_byte, completed, signed
		  FROM rogerai.tower_acks WHERE attempt_id = $1`, attemptID).
		Scan(&a.AttemptID, &a.ResponseDigest, &a.Usage.In, &a.Usage.Out,
			&a.FirstByte, &a.Completed, &signed)
	if errors.Is(err, sql.ErrNoRows) {
		return Ack{}, false, nil
	}
	if err != nil {
		return Ack{}, false, err
	}
	raw, derr := base64.StdEncoding.DecodeString(signed)
	if derr != nil {
		// Evidence we cannot decode is evidence we cannot re-verify. Treating it as absent
		// would settle the attempt uncorroborated, which is safe; reporting it is better,
		// because a store returning unreadable rows is a fault somebody should see.
		return Ack{}, false, errors.New("the recorded acknowledgement is unreadable")
	}
	a.Signed = raw
	// Postgres hands back its own location; callers compare against wall-clock time.
	a.FirstByte, a.Completed = a.FirstByte.UTC(), a.Completed.UTC()
	return a, true, nil
}

func (p *AckPGStore) Reap(before time.Time) (int64, error) {
	res, err := p.db.Exec(`DELETE FROM rogerai.tower_acks WHERE recorded_at <= $1`, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
