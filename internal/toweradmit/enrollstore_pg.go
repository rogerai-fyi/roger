package toweradmit

// enrollstore_pg.go holds the durable halves of Tower enrollment that are NOT the admission
// registry itself: the CA's root and revocations, and in-flight enrollment state.
//
// They live in this package because they share its database handle and its schema, and
// keeping one migration path for everything a joined Tower needs is simpler to reason about
// at deploy time than three packages each owning a table.
//
// WHY THE COMMITTED OUTCOMES ARE IN POSTGRES AND NOT THE CACHE. A lost committed outcome is
// not a slow path, it is an operator whose enrollment token has been spent and whose Tower
// identity nothing remembers. That is unrecoverable without an administrator, so it belongs
// with the authoritative data rather than with anything that may be evicted.

import (
	"database/sql"
	"errors"
	"time"
)

const enrollSchema = `
-- The issuing root, when the deployment did not inject one. At most one row.
CREATE TABLE IF NOT EXISTS rogerai.tower_ca_root (
    id       INT PRIMARY KEY DEFAULT 1,
    key_pem  BYTEA       NOT NULL,
    cert_pem BYTEA       NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT tower_ca_root_singleton CHECK (id = 1)
);

CREATE TABLE IF NOT EXISTS rogerai.tower_ca_revoked (
    serial     TEXT PRIMARY KEY,
    revoked_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- In-flight enrollment. Challenges are short-lived; committed outcomes are not, because a
-- retry may arrive long after the response was lost.
CREATE TABLE IF NOT EXISTS rogerai.tower_enroll_challenges (
    nonce    TEXT PRIMARY KEY,
    token_id TEXT        NOT NULL,
    expires  TIMESTAMPTZ NOT NULL
);
-- What the challenge may be answered for. Domain separation between enrolling and
-- renewing: a signature collected for one must not satisfy the other. Additive and
-- defaulted, so rows written before this column keep their original meaning.
ALTER TABLE rogerai.tower_enroll_challenges
    ADD COLUMN IF NOT EXISTS purpose TEXT NOT NULL DEFAULT 'enroll';

CREATE TABLE IF NOT EXISTS rogerai.tower_enroll_committed (
    txn_id   TEXT PRIMARY KEY,
    tower_id TEXT  NOT NULL,
    key_hash TEXT  NOT NULL,
    cert_der BYTEA NOT NULL,
    committed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
`

// PGCustody is the database-backed CA custody.
type PGCustody struct{ db *sql.DB }

// NewPGCustody applies the schema and returns custody over the given handle.
func NewPGCustody(db *sql.DB) (*PGCustody, error) {
	if db == nil {
		return nil, errors.New("CA custody needs a database handle")
	}
	if err := applySchema(db, schema+enrollSchema); err != nil {
		return nil, err
	}
	return &PGCustody{db: db}, nil
}

func (p *PGCustody) LoadRoot() (keyPEM, certPEM []byte, ok bool, err error) {
	err = p.db.QueryRow(`SELECT key_pem, cert_pem FROM rogerai.tower_ca_root WHERE id=1`).
		Scan(&keyPEM, &certPEM)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, wrap("load CA root", err)
	}
	return keyPEM, certPEM, true, nil
}

// SaveRoot writes the root only if none exists. ON CONFLICT DO NOTHING makes two instances
// racing on first start settle on ONE root rather than each overwriting the other's - which
// would leave certificates issued in the gap unverifiable.
func (p *PGCustody) SaveRoot(keyPEM, certPEM []byte) error {
	res, err := p.db.Exec(
		`INSERT INTO rogerai.tower_ca_root(id,key_pem,cert_pem) VALUES(1,$1,$2)
		 ON CONFLICT (id) DO NOTHING`, keyPEM, certPEM)
	if err != nil {
		return wrap("save CA root", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Somebody else won the race. Not an error: the caller re-reads and uses theirs.
		return nil
	}
	return nil
}

func (p *PGCustody) LoadRevoked() ([]string, error) {
	rows, err := p.db.Query(`SELECT serial FROM rogerai.tower_ca_revoked`)
	if err != nil {
		return nil, wrap("load revocations", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, wrap("load revocations", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (p *PGCustody) SaveRevoked(serial string) error {
	_, err := p.db.Exec(
		`INSERT INTO rogerai.tower_ca_revoked(serial) VALUES($1) ON CONFLICT DO NOTHING`, serial)
	if err != nil {
		return wrap("save revocation", err)
	}
	return nil
}

// PGEnrollStore is the database-backed in-flight enrollment state.
type PGEnrollStore struct{ db *sql.DB }

// NewPGEnrollStore applies the schema and returns the store.
func NewPGEnrollStore(db *sql.DB) (*PGEnrollStore, error) {
	if db == nil {
		return nil, errors.New("enrollment state needs a database handle")
	}
	if err := applySchema(db, schema+enrollSchema); err != nil {
		return nil, err
	}
	return &PGEnrollStore{db: db}, nil
}

// ChallengeRow is the storage shape of a challenge. towerenroll owns the type it uses; this
// package must not import it, because towerenroll already imports this one.
type ChallengeRow struct {
	Nonce   string
	Subject string
	Purpose string
	Expires time.Time
}

func (p *PGEnrollStore) PutChallengeRow(nonce, subject, purpose string, expires time.Time) error {
	_, err := p.db.Exec(
		`INSERT INTO rogerai.tower_enroll_challenges(nonce,token_id,purpose,expires) VALUES($1,$2,$3,$4)
		 ON CONFLICT (nonce) DO NOTHING`, nonce, subject, purpose, expires.UTC())
	if err != nil {
		return wrap("put challenge", err)
	}
	return nil
}

// TakeChallengeRow deletes and returns a challenge in one statement, so a nonce is spendable
// exactly once across the deployment.
func (p *PGEnrollStore) TakeChallengeRow(nonce string) (ChallengeRow, bool, error) {
	var row ChallengeRow
	err := p.db.QueryRow(
		`DELETE FROM rogerai.tower_enroll_challenges WHERE nonce=$1
		 RETURNING nonce, token_id, purpose, expires`, nonce).
		Scan(&row.Nonce, &row.Subject, &row.Purpose, &row.Expires)
	if errors.Is(err, sql.ErrNoRows) {
		return ChallengeRow{}, false, nil
	}
	if err != nil {
		return ChallengeRow{}, false, wrap("take challenge", err)
	}
	return row, true, nil
}

func (p *PGEnrollStore) ReapChallenges(now time.Time) error {
	_, err := p.db.Exec(`DELETE FROM rogerai.tower_enroll_challenges WHERE expires < $1`, now.UTC())
	if err != nil {
		return wrap("reap challenges", err)
	}
	return nil
}

// CommittedRow is a completed enrollment.
type CommittedRow struct {
	TowerID string
	KeyHash string
	CertDER []byte
}

func (p *PGEnrollStore) CommittedRow(txnID string) (CommittedRow, bool, error) {
	var row CommittedRow
	err := p.db.QueryRow(
		`SELECT tower_id, key_hash, cert_der FROM rogerai.tower_enroll_committed WHERE txn_id=$1`, txnID).
		Scan(&row.TowerID, &row.KeyHash, &row.CertDER)
	if errors.Is(err, sql.ErrNoRows) {
		return CommittedRow{}, false, nil
	}
	if err != nil {
		return CommittedRow{}, false, wrap("read committed enrollment", err)
	}
	return row, true, nil
}

// PutCommittedRow records an outcome. DO NOTHING on conflict because the first write is the
// authoritative one: a retry that raced the original must not replace what it is retrying.
func (p *PGEnrollStore) PutCommittedRow(txnID, towerID, keyHash string, certDER []byte) error {
	_, err := p.db.Exec(
		`INSERT INTO rogerai.tower_enroll_committed(txn_id,tower_id,key_hash,cert_der)
		 VALUES($1,$2,$3,$4) ON CONFLICT (txn_id) DO NOTHING`,
		txnID, towerID, keyHash, certDER)
	if err != nil {
		return wrap("record committed enrollment", err)
	}
	return nil
}
