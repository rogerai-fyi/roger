package toweradmit

// The durable CA custody and in-flight enrollment tables, against real PostgreSQL.
//
// These hold the two things that must never be forgotten: the root every Tower certificate
// chains to, and the record of an enrollment that already happened. Losing the first makes
// the whole network unverifiable; losing the second strands an operator whose token is
// spent.

import (
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func pgHandle(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ROGERAI_TEST_DATABASE_URL not set; skipping the durable enrollment tests")
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	require.NoError(t, db.Ping())
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`CREATE SCHEMA IF NOT EXISTS rogerai`)
	require.NoError(t, err)
	return db
}

func TestTheCARootIsStoredOnceAndReadBack(t *testing.T) {
	db := pgHandle(t)
	c, err := NewPGCustody(db)
	require.NoError(t, err)
	_, err = db.Exec(`TRUNCATE rogerai.tower_ca_root, rogerai.tower_ca_revoked`)
	require.NoError(t, err)

	_, _, ok, err := c.LoadRoot()
	require.NoError(t, err)
	require.False(t, ok, "a fresh deployment has no root yet")

	require.NoError(t, c.SaveRoot([]byte("key-pem"), []byte("cert-pem")))
	key, cert, ok, err := c.LoadRoot()
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []byte("key-pem"), key)
	require.Equal(t, []byte("cert-pem"), cert)

	// Two instances racing on first start must settle on ONE root: a second write that
	// replaced the first would make every certificate issued in between unverifiable.
	require.NoError(t, c.SaveRoot([]byte("other-key"), []byte("other-cert")))
	key, _, _, err = c.LoadRoot()
	require.NoError(t, err)
	require.Equal(t, []byte("key-pem"), key, "the first root written wins, and is never replaced")
}

func TestRevocationsArePersistedAndIdempotent(t *testing.T) {
	db := pgHandle(t)
	c, err := NewPGCustody(db)
	require.NoError(t, err)
	_, err = db.Exec(`TRUNCATE rogerai.tower_ca_revoked`)
	require.NoError(t, err)

	require.NoError(t, c.SaveRevoked("12345"))
	require.NoError(t, c.SaveRevoked("12345"), "revoking twice is not an error")
	require.NoError(t, c.SaveRevoked("67890"))

	got, err := c.LoadRevoked()
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"12345", "67890"}, got)
}

func TestAChallengeIsTakenExactlyOnce(t *testing.T) {
	db := pgHandle(t)
	s, err := NewPGEnrollStore(db)
	require.NoError(t, err)
	_, err = db.Exec(`TRUNCATE rogerai.tower_enroll_challenges`)
	require.NoError(t, err)

	require.NoError(t, s.PutChallengeRow("n1", "tok-1", "enroll", time.Now().Add(time.Minute)))

	row, ok, err := s.TakeChallengeRow("n1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "tok-1", row.Subject)
	require.Equal(t, "enroll", row.Purpose)

	_, ok, err = s.TakeChallengeRow("n1")
	require.NoError(t, err)
	require.False(t, ok, "the nonce is spent, whoever takes it")

	_, ok, err = s.TakeChallengeRow("never-issued")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestExpiredChallengesAreReaped(t *testing.T) {
	db := pgHandle(t)
	s, err := NewPGEnrollStore(db)
	require.NoError(t, err)
	_, err = db.Exec(`TRUNCATE rogerai.tower_enroll_challenges`)
	require.NoError(t, err)

	now := time.Now()
	require.NoError(t, s.PutChallengeRow("dead", "tok-1", "enroll", now.Add(-time.Minute)))
	require.NoError(t, s.PutChallengeRow("live", "tok-1", "enroll", now.Add(time.Minute)))

	require.NoError(t, s.ReapChallenges(now))

	_, ok, err := s.TakeChallengeRow("dead")
	require.NoError(t, err)
	require.False(t, ok)
	_, ok, err = s.TakeChallengeRow("live")
	require.NoError(t, err)
	require.True(t, ok, "the nonce space is bounded without dropping live challenges")
}

func TestACommittedEnrollmentSurvivesAndIsNeverReplaced(t *testing.T) {
	// The record that makes a retry-after-lost-response work. A second write replacing it
	// would mean a racing retry could overwrite the outcome it was retrying.
	db := pgHandle(t)
	s, err := NewPGEnrollStore(db)
	require.NoError(t, err)
	_, err = db.Exec(`TRUNCATE rogerai.tower_enroll_committed`)
	require.NoError(t, err)

	_, ok, err := s.CommittedRow("txn-unknown")
	require.NoError(t, err)
	require.False(t, ok)

	require.NoError(t, s.PutCommittedRow("txn-1", "tw-1", "keyhash-1", []byte{1, 2, 3}))
	require.NoError(t, s.PutCommittedRow("txn-1", "tw-OTHER", "keyhash-2", []byte{9}))

	row, ok, err := s.CommittedRow("txn-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "tw-1", row.TowerID, "the first outcome stands")
	require.Equal(t, "keyhash-1", row.KeyHash)
	require.Equal(t, []byte{1, 2, 3}, row.CertDER)
}

func TestDurableEnrollmentStoresRefuseANilHandle(t *testing.T) {
	_, err := NewPGCustody(nil)
	require.Error(t, err)
	_, err = NewPGEnrollStore(nil)
	require.Error(t, err)
}

func TestTheDurableCAAndEnrollmentTablesReportOutages(t *testing.T) {
	// Every one of these paths decides whether the caller believes something is stored. A
	// swallowed failure here means a root, a revocation, or a committed enrollment that we
	// think exists and does not.
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ROGERAI_TEST_DATABASE_URL not set")
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	custody, err := NewPGCustody(db)
	require.NoError(t, err)
	enroll, err := NewPGEnrollStore(db)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	_, _, _, err = custody.LoadRoot()
	require.ErrorIs(t, err, ErrUnavailable)
	require.ErrorIs(t, custody.SaveRoot([]byte("k"), []byte("c")), ErrUnavailable)
	_, err = custody.LoadRevoked()
	require.ErrorIs(t, err, ErrUnavailable)
	require.ErrorIs(t, custody.SaveRevoked("1"), ErrUnavailable)

	require.ErrorIs(t, enroll.PutChallengeRow("n", "t", "enroll", time.Now()), ErrUnavailable)
	_, _, err = enroll.TakeChallengeRow("n")
	require.ErrorIs(t, err, ErrUnavailable)
	require.ErrorIs(t, enroll.ReapChallenges(time.Now()), ErrUnavailable)
	_, _, err = enroll.CommittedRow("txn")
	require.ErrorIs(t, err, ErrUnavailable)
	require.ErrorIs(t, enroll.PutCommittedRow("txn", "tw", "kh", []byte{1}), ErrUnavailable)
}

func TestBuildingTheDurableStoresRefusesAClosedHandle(t *testing.T) {
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ROGERAI_TEST_DATABASE_URL not set")
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	// A store that cannot apply its schema must not pretend it is ready: the first write
	// would fail on a missing table, long after the operator concluded startup succeeded.
	_, err = NewPGCustody(db)
	require.Error(t, err)
	_, err = NewPGEnrollStore(db)
	require.Error(t, err)
}
