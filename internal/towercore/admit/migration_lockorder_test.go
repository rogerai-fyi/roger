package admit

// A deploy-time migration must not deadlock a concurrent admission.
//
// During a rolling deploy a new broker instance runs the startup migration while an old one
// is serving. Admit locks the token table (DELETE) and then the admissions table (INSERT).
// The migration used to run as ONE implicit transaction that locked admissions (ALTER TABLE)
// and then wanted the token table (CREATE INDEX): the opposite order, so the two could wait
// on each other until Postgres aborted one with SQLSTATE 40P01.
//
// This pins Admit in the gap between its two statements, starts the migration, waits until
// the migration is blocked on a lock, and only then lets Admit continue. That is the exact
// interleave that deadlocked, every time, without needing thousands of loops.

import (
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/pgmigrate"
)

// errRecorder passes statements through and remembers every error, so a failure the
// migration's one retry would otherwise paper over is still visible to the test.
type errRecorder struct {
	db   *sql.DB
	mu   sync.Mutex
	errs []error
}

func (r *errRecorder) Exec(q string, args ...any) (sql.Result, error) {
	res, err := r.db.Exec(q, args...)
	if err != nil {
		r.mu.Lock()
		r.errs = append(r.errs, err)
		r.mu.Unlock()
	}
	return res, err
}

func (r *errRecorder) seen() []error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]error(nil), r.errs...)
}

func TestAStartupMigrationDoesNotDeadlockAConcurrentAdmission(t *testing.T) {
	s := pgStore(t).(*PGStore)
	db := s.db
	require.NoError(t, s.PutToken(Token{ID: "tok-lock", Owner: "acct-1", Expires: time.Now().Add(time.Hour)}))

	paused := make(chan struct{})
	resume := make(chan struct{})
	admitBetweenStatements = func() {
		close(paused)
		<-resume
	}
	t.Cleanup(func() { admitBetweenStatements = nil })

	type result struct {
		ok  bool
		err error
	}
	admitted := make(chan result, 1)
	go func() {
		ok, err := s.Admit("tok-lock", sampleAdmission("tw-lock", "key-lock"))
		admitted <- result{ok, err}
	}()
	<-paused // Admit now holds the token row and has not touched admissions yet.

	// The migration every store runs at startup: the registry's and the enrollment state's.
	rec := &errRecorder{db: db}
	migrated := make(chan error, 1)
	go func() { migrated <- pgmigrate.Apply(rec, schema+enrollSchema) }()

	// Wait until the migration is blocked behind Admit, so the interleave is the real one.
	// The match is a lock on the token table that has not been granted, which only the
	// migration's token index can be waiting for here (Admit holds that lock), so another
	// lock waiter on a shared test server cannot release Admit early. pg_locks rather than
	// pg_stat_activity's query text, which is cut off at 1 KB and so would not show the
	// statement inside the long single-transaction script this used to be.
	require.Eventually(t, func() bool {
		var n int
		if err := db.QueryRow(
			`SELECT count(*) FROM pg_locks
			  WHERE NOT granted AND relation = 'rogerai.tower_enrollment_tokens'::regclass`).Scan(&n); err != nil {
			t.Logf("probing for the blocked migration: %v", err)
			return false
		}
		return n > 0
	}, 10*time.Second, 10*time.Millisecond, "the migration never waited on Admit's lock")

	close(resume)

	var got result
	select {
	case got = <-admitted:
	case <-time.After(30 * time.Second):
		t.Fatal("Admit never finished")
	}
	select {
	case err := <-migrated:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("the migration never finished")
	}

	for _, err := range rec.seen() {
		require.False(t, strings.Contains(err.Error(), "40P01") || strings.Contains(err.Error(), "deadlock"),
			"the migration deadlocked against Admit: %v", err)
	}
	require.Empty(t, rec.seen(), "the migration hit an error, even if its retry hid it")
	require.NoError(t, got.err, "the station's admission failed")
	require.True(t, got.ok)

	tw, ok, err := s.TowerByID("tw-lock")
	require.NoError(t, err)
	require.True(t, ok, "the admission committed")
	require.Equal(t, "key-lock", tw.KeyHash)
}
