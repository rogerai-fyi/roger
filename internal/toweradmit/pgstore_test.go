package toweradmit

// The durable registry, run against REAL PostgreSQL.
//
// It runs the same behaviour as the in-process store, deliberately: two implementations of
// one interface are only interchangeable if the same assertions hold against both. The
// coverage gate stands up a throwaway Postgres and sets ROGERAI_TEST_DATABASE_URL; without
// it these skip rather than silently passing.

import (
	"database/sql"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	// The registry itself never opens a connection - the broker hands it a pool - so the
	// driver is registered here, where the tests do the opening.
	_ "github.com/jackc/pgx/v5/stdlib"
)

func pgStore(t *testing.T) Store {
	t.Helper()
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ROGERAI_TEST_DATABASE_URL not set; skipping the durable registry tests")
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	require.NoError(t, db.Ping())
	t.Cleanup(func() { db.Close() })

	s, err := NewPGStore(db)
	require.NoError(t, err)
	// Each test starts from a clean registry; the tables are shared with other suites on
	// the same throwaway server.
	_, err = db.Exec(`TRUNCATE rogerai.tower_admissions, rogerai.tower_enrollment_tokens`)
	require.NoError(t, err)
	return s
}

func TestDurableRegistrySurvivesARestart(t *testing.T) {
	s := pgStore(t)
	cfg := Config{}
	r := NewWithStore(cfg, s)

	tok, err := r.IssueToken("acct-1")
	require.NoError(t, err)
	tw, err := r.Enroll(tok, "keyhash-A")
	require.NoError(t, err)
	require.NoError(t, r.Transition(tw.ID, StateRevoked))

	// A brand-new Registry over the same database is exactly what a redeploy is.
	after := NewWithStore(cfg, s)
	got, ok := after.Get(tw.ID)
	require.True(t, ok)
	require.Equal(t, StateRevoked, got.State, "a revocation is not undone by a deploy")
	require.False(t, after.MayTakeWork(tw.ID))

	tok2, _ := after.IssueToken("acct-1")
	_, err = after.Enroll(tok2, "keyhash-A")
	require.Error(t, err, "and the revoked key stays burned")
}

func TestDurableRegistryKeepsFalseClaimEvidence(t *testing.T) {
	s := pgStore(t)
	r := NewWithStore(Config{}, s)
	tok, _ := r.IssueToken("acct-1")
	tw, err := r.Enroll(tok, "keyhash-A")
	require.NoError(t, err)

	r.RecordClaim(tw.ID, StateActive)
	r.RecordClaim(tw.ID, StateActive)

	after := NewWithStore(Config{}, s)
	got, ok := after.Get(tw.ID)
	require.True(t, ok)
	require.Equal(t, 2, got.FalseClaims, "evidence that resets on deploy is not evidence")
}

func TestDurableTokenIsOneTimeAndSurvivesUnspent(t *testing.T) {
	s := pgStore(t)
	r := NewWithStore(Config{}, s)

	unspent, _ := r.IssueToken("acct-1")
	spent, _ := r.IssueToken("acct-1")
	_, err := r.Enroll(spent, "keyhash-A")
	require.NoError(t, err)

	after := NewWithStore(Config{}, s)
	_, err = after.Enroll(spent, "keyhash-B")
	require.Error(t, err, "a spent token stays spent")

	tw, err := after.Enroll(unspent, "keyhash-C")
	require.NoError(t, err, "an unspent one is still good")
	require.NotEmpty(t, tw.ID)
}

func TestDurableFailedEnrollmentDoesNotBurnTheToken(t *testing.T) {
	s := pgStore(t)
	r := NewWithStore(Config{}, s)
	tok, _ := r.IssueToken("acct-1")

	_, err := r.Enroll(tok, "")
	require.Error(t, err)

	tw, err := r.Enroll(tok, "keyhash-A")
	require.NoError(t, err, "the legitimate holder still needs that token")
	require.NotEmpty(t, tw.ID)
}

func TestDurableConcurrentEnrollmentsWithOneTokenAdmitExactlyOne(t *testing.T) {
	// The headline race, decided by the database rather than by a read-then-check.
	s := pgStore(t)
	r := NewWithStore(Config{}, s)
	tok, _ := r.IssueToken("acct-1")

	var wg sync.WaitGroup
	results := make([]error, 2)
	keys := []string{"keyhash-A", "keyhash-B"}
	wg.Add(2)
	for i := range keys {
		go func(i int) {
			defer wg.Done()
			_, results[i] = r.Enroll(tok, keys[i])
		}(i)
	}
	wg.Wait()

	admitted := 0
	for _, err := range results {
		if err == nil {
			admitted++
		}
	}
	require.Equal(t, 1, admitted, "one token admits exactly one Tower")
}

func TestDurableOneKeyAdmitsOneTower(t *testing.T) {
	s := pgStore(t)
	r := NewWithStore(Config{}, s)

	tok1, _ := r.IssueToken("acct-1")
	_, err := r.Enroll(tok1, "keyhash-A")
	require.NoError(t, err)

	tok2, _ := r.IssueToken("acct-2")
	_, err = r.Enroll(tok2, "keyhash-A")
	require.Error(t, err,
		"a single machine holding two admissions means a suspension stops only one of them")
}

func TestDurableCASRefusesAStaleRevision(t *testing.T) {
	s := pgStore(t)
	r := NewWithStore(Config{}, s)
	tok, _ := r.IssueToken("acct-1")
	tw, err := r.Enroll(tok, "keyhash-A")
	require.NoError(t, err)

	read, ok, err := s.TowerByID(tw.ID)
	require.NoError(t, err)
	require.True(t, ok)
	stale := read

	read.State = StateActive
	won, err := s.CASTower(read)
	require.NoError(t, err)
	require.True(t, won)

	stale.State = StateRevoked
	won, err = s.CASTower(stale)
	require.NoError(t, err)
	require.False(t, won, "a decision made from a state we never saw must not overwrite one we did")

	got, _, err := s.TowerByID(tw.ID)
	require.NoError(t, err)
	require.Equal(t, StateActive, got.State)
}

func TestDurableQuotaAndOwnerListing(t *testing.T) {
	s := pgStore(t)
	cfg := Config{MaxTowersPerOwner: 2}
	r := NewWithStore(cfg, s)

	for _, key := range []string{"keyhash-A", "keyhash-B"} {
		tok, _ := r.IssueToken("acct-1")
		_, err := r.Enroll(tok, key)
		require.NoError(t, err)
	}
	tok, _ := r.IssueToken("acct-1")
	_, err := r.Enroll(tok, "keyhash-C")
	require.Error(t, err, "the quota holds across a durable store")

	after := NewWithStore(cfg, s)
	require.Len(t, after.ByOwner("acct-1"), 2)

	// Revoking frees the slot without forgetting the Tower or unburning its key.
	towers := after.ByOwner("acct-1")
	require.NoError(t, after.Transition(towers[0].ID, StateRevoked))
	tok2, _ := after.IssueToken("acct-1")
	_, err = after.Enroll(tok2, "keyhash-D")
	require.NoError(t, err, "a revoked Tower must not consume quota forever")
	require.Len(t, after.ByOwner("acct-1"), 3, "but it stays on the record")
}

func TestDurableExpiredTokensAreReaped(t *testing.T) {
	s := pgStore(t)
	r := NewWithStore(Config{TokenTTL: time.Millisecond}, s)
	tok, err := r.IssueToken("acct-1")
	require.NoError(t, err)
	time.Sleep(5 * time.Millisecond)

	require.NoError(t, s.ReapTokens(time.Now()))
	_, ok, err := s.GetToken(tok)
	require.NoError(t, err)
	require.False(t, ok, "the token space cannot be grown without bound")
}

func TestDurableLeaseRenewalAndExpiry(t *testing.T) {
	s := pgStore(t)
	r := NewWithStore(Config{LeaseTTL: 50 * time.Millisecond}, s)
	tok, _ := r.IssueToken("acct-1")
	tw, err := r.Enroll(tok, "keyhash-A")
	require.NoError(t, err)

	require.NoError(t, r.Renew(tw.ID))
	time.Sleep(80 * time.Millisecond)

	require.False(t, r.MayTakeWork(tw.ID), "a lapsed lease takes no new work")
	require.Error(t, r.Renew(tw.ID), "a lapsed lease is re-admitted, not renewed")
	require.NoError(t, r.Expire(tw.ID))

	after := NewWithStore(Config{}, s)
	got, _ := after.Get(tw.ID)
	require.Equal(t, StateExpired, got.State)
}

func TestDurableRegistryReportsAnOutageRatherThanAnAnswer(t *testing.T) {
	// Every path must distinguish "this Tower is not admitted" from "we cannot currently
	// tell". Conflating them turns a database blip into a network-wide ban - and, in the
	// other direction, would let an unreadable registry grant work.
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ROGERAI_TEST_DATABASE_URL not set; skipping the durable registry tests")
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	s, err := NewPGStore(db)
	require.NoError(t, err)
	require.NoError(t, db.Close()) // the database goes away

	require.ErrorIs(t, s.PutToken(Token{ID: "t", Owner: "acct-1", Expires: time.Now()}), ErrUnavailable)

	_, _, err = s.GetToken("t")
	require.ErrorIs(t, err, ErrUnavailable)

	_, err = s.ConsumeToken("t")
	require.ErrorIs(t, err, ErrUnavailable)

	require.ErrorIs(t, s.PutTower(Tower{ID: "x", KeyHash: "k"}), ErrUnavailable)

	_, err = s.CASTower(Tower{ID: "x", Rev: 1})
	require.ErrorIs(t, err, ErrUnavailable)

	_, _, err = s.TowerByID("x")
	require.ErrorIs(t, err, ErrUnavailable)

	_, _, err = s.TowerByKey("k")
	require.ErrorIs(t, err, ErrUnavailable)

	_, err = s.TowersByOwner("acct-1")
	require.ErrorIs(t, err, ErrUnavailable)

	require.ErrorIs(t, s.ReapTokens(time.Now()), ErrUnavailable)

	// And through the Registry: an unreadable registry grants nothing.
	r := NewWithStore(Config{}, s)
	require.False(t, r.MayTakeWork("x"), "failing closed is the only safe direction")
	_, ok := r.Get("x")
	require.False(t, ok)
	require.Nil(t, r.ByOwner("acct-1"))
	require.Error(t, r.Transition("x", StateActive))
	require.Error(t, r.Renew("x"))
	require.Error(t, r.Expire("x"))
	r.RecordClaim("x", StateActive) // must not panic
}

func TestNewPGStoreRefusesANilHandle(t *testing.T) {
	_, err := NewPGStore(nil)
	require.Error(t, err)
}

func TestTheMigrationNeverCreatesTheSchemaItself(t *testing.T) {
	// The `rogerai` schema is admin-provisioned and the app's database user has no
	// DB-level CREATE. CREATE SCHEMA IF NOT EXISTS reads as harmless but is not: PostgreSQL
	// checks CREATE-on-database BEFORE the IF-NOT-EXISTS short-circuit, so it fails with
	// "permission denied for database" even when the schema is already there.
	//
	// Verified against a real least-privilege role. The symptom in production would have
	// been joined-Tower admission logging that it was OFF while every other subsystem
	// started normally - which is a bad thing to discover from an operator's bug report,
	// so this guards it permanently.
	for name, ddl := range map[string]string{
		"registry":   schema,
		"enrollment": enrollSchema,
	} {
		t.Run(name, func(t *testing.T) {
			require.NotContains(t, strings.ToUpper(ddl), "CREATE SCHEMA",
				"migrations create tables inside the schema, never the schema")
		})
	}
}
