package towerenroll

// The durable enrollment store, through the adapter, against real PostgreSQL.
//
// The adapter's whole job is to keep the dependency running one way while giving the stored
// values meaning. These prove the round trip and - the part that matters - that a storage
// failure stays distinguishable from a rejection.

import (
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/toweradmit"
)

func durableStore(t *testing.T) Store {
	t.Helper()
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ROGERAI_TEST_DATABASE_URL not set; skipping the durable enrollment tests")
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	require.NoError(t, db.Ping())
	t.Cleanup(func() { db.Close() })

	inner, err := toweradmit.NewPGEnrollStore(db)
	require.NoError(t, err)
	_, err = db.Exec(`TRUNCATE rogerai.tower_enroll_challenges, rogerai.tower_enroll_committed`)
	require.NoError(t, err)

	s, err := NewPGStore(inner)
	require.NoError(t, err)
	return s
}

func TestDurableChallengeRoundTripAndOneTimeUse(t *testing.T) {
	s := durableStore(t)
	exp := time.Now().Add(time.Minute)
	require.NoError(t, s.PutChallenge(Challenge{Nonce: "n1", Subject: "tok-1", Purpose: PurposeEnroll, Expires: exp}))

	ch, ok, err := s.TakeChallenge("n1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "tok-1", ch.Subject)
	require.WithinDuration(t, exp, ch.Expires, time.Second)

	_, ok, err = s.TakeChallenge("n1")
	require.NoError(t, err)
	require.False(t, ok, "a nonce is answered once")
}

func TestDurableCommittedRoundTrip(t *testing.T) {
	s := durableStore(t)
	_, ok, err := s.Committed("txn-none")
	require.NoError(t, err)
	require.False(t, ok)

	require.NoError(t, s.PutCommitted("txn-1", Committed{
		TowerID: "tw-1", KeyHash: "kh-1", CertDER: []byte{7, 8, 9},
	}))
	got, ok, err := s.Committed("txn-1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "tw-1", got.TowerID)
	require.Equal(t, []byte{7, 8, 9}, got.CertDER)
}

func TestDurableReapDropsOnlyDeadChallenges(t *testing.T) {
	s := durableStore(t)
	now := time.Now()
	require.NoError(t, s.PutChallenge(Challenge{Nonce: "dead", Subject: "t", Purpose: PurposeEnroll, Expires: now.Add(-time.Minute)}))
	require.NoError(t, s.PutChallenge(Challenge{Nonce: "live", Subject: "t", Purpose: PurposeEnroll, Expires: now.Add(time.Minute)}))

	require.NoError(t, s.Reap(now))

	_, ok, _ := s.TakeChallenge("dead")
	require.False(t, ok)
	_, ok, _ = s.TakeChallenge("live")
	require.True(t, ok)
}

func TestTheDurableStoreRefusesToBeBuiltWithoutItsTables(t *testing.T) {
	_, err := NewPGStore(nil)
	require.Error(t, err)
}

func TestAStorageFailureIsUnavailableNotARejection(t *testing.T) {
	// The distinction an operator feels: "your enrollment is invalid" sends them looking for
	// a problem with their machine; "temporarily unavailable" tells them to retry.
	dsn := os.Getenv("ROGERAI_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("ROGERAI_TEST_DATABASE_URL not set")
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	inner, err := toweradmit.NewPGEnrollStore(db)
	require.NoError(t, err)
	s, err := NewPGStore(inner)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	require.ErrorIs(t, s.PutChallenge(Challenge{Nonce: "n", Subject: "t", Purpose: PurposeEnroll, Expires: time.Now()}), ErrUnavailable)

	_, _, err = s.TakeChallenge("n")
	require.ErrorIs(t, err, ErrUnavailable)

	_, _, err = s.Committed("txn")
	require.ErrorIs(t, err, ErrUnavailable)

	require.ErrorIs(t, s.PutCommitted("txn", Committed{TowerID: "t"}), ErrUnavailable)
	require.ErrorIs(t, s.Reap(time.Now()), ErrUnavailable)
}
