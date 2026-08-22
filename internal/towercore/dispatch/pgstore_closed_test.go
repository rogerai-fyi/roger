package dispatch

// A DATABASE THAT CANNOT ANSWER MUST SAY SO - the same discipline, and the same trick, as
// internal/towercore/attach/pgstore_closed_test.go: closing the pool underneath a live
// store reaches every Exec/Query error branch at once. These branches decide whether a
// settle failure reads as "this attempt cannot settle" (permanent, the courier abandons a
// receipt) or "the database is down" (transient, the courier retries) - and on this path
// that difference is somebody's pay.

import (
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestDispatchPGStoreNeedsADatabase(t *testing.T) {
	_, err := NewPGStore(nil)
	require.Error(t, err)
}

func TestDispatchPGStoreRefusesAPoolThatCannotMigrate(t *testing.T) {
	// The schema migration is the constructor's first act; a pool that cannot run it must
	// fail construction rather than hand back a store whose every call will fail worse.
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("no ROGERAI_TEST_DATABASE_URL")
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	_, err = NewPGStore(db)
	require.Error(t, err)
}

func TestAClosedPoolFailsEveryDispatchPathAsAnOutage(t *testing.T) {
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("no ROGERAI_TEST_DATABASE_URL")
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	p, err := NewPGStore(db)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	now := time.Unix(1_700_000_000, 0)
	require.Error(t, p.Put(Record{AttemptID: "att-x", TowerID: "tw-1"}))
	_, _, err = p.Get("att-x")
	require.Error(t, err)
	_, err = p.ClaimByID("att-x", "tw-1", now)
	require.Error(t, err)
	_, _, err = p.ClaimNext("tw-1", now)
	require.Error(t, err)
	_, err = p.Settle("att-x", now)
	require.Error(t, err)
	_, err = p.Reap(now)
	require.Error(t, err)
}

// The durable store's WHY answers - the diagnoses that ride refusals back to a caller. A
// second claim must say "already claimed", a second settle "already settled", and a row
// whose recorded Station key has rotted must refuse to answer at all rather than hand back
// a key nobody can verify a receipt against.
func TestTheDurableStoreDiagnosesItsRefusals(t *testing.T) {
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("no ROGERAI_TEST_DATABASE_URL")
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	p, err := NewPGStore(db)
	require.NoError(t, err)

	now := time.Unix(1_700_000_000, 0).UTC()
	rec := Record{AttemptID: "att-diag", TowerID: "tw-diag", StationID: "st-diag",
		Model: "m", Modality: "text", Nonce: "n-diag", Deadline: now.Add(time.Hour),
		AssertionKey: []byte{1, 2, 3, 4}, State: "issued"}
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM rogerai.tower_attempts WHERE attempt_id='att-diag'`) })
	require.NoError(t, p.Put(rec))

	_, err = p.ClaimByID("att-diag", "tw-diag", now)
	require.NoError(t, err)
	_, err = p.ClaimByID("att-diag", "tw-diag", now)
	require.ErrorIs(t, err, ErrAlreadyClaimed, "a second claim must be diagnosed, not re-granted")

	_, err = p.Settle("att-diag", now)
	require.NoError(t, err)
	_, err = p.Settle("att-diag", now)
	require.ErrorIs(t, err, ErrAlreadySettled, "settlement is one-use; the duplicate must say so")

	// Bit-rot in the recorded key: refusing to answer is the only safe read, because a key
	// treated as absent would mean accepting whatever the relay sent.
	_, err = db.Exec(`UPDATE rogerai.tower_attempts SET assertion_key='zz' WHERE attempt_id='att-diag'`)
	require.NoError(t, err)
	_, _, err = p.Get("att-diag")
	require.ErrorContains(t, err, "Station key is unreadable")
}
