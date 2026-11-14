package health

// Health-check evidence per Tower (features/tower/tower_trust_ramp.feature, "the hub check",
// "computing the stage"; placement_ranking.feature "health tier"). Hub checks and Station
// canaries are both recorded here; the ramp counts them and placement reads the last two.

import (
	"database/sql"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v7/internal/pgtest"
)

func eachStore(t *testing.T, body func(t *testing.T, s Store)) {
	t.Run("mem", func(t *testing.T) { body(t, NewMemStore()) })
	t.Run("pg", func(t *testing.T) {
		dsn := pgtest.DSN(t)
		if dsn == "" {
			t.Skip("ROGERAI_TEST_DATABASE_URL not set")
		}
		db, err := sql.Open("pgx", dsn)
		require.NoError(t, err)
		t.Cleanup(func() { db.Close() })
		_, _ = db.Exec(`CREATE SCHEMA IF NOT EXISTS rogerai`)
		s, err := NewPGStore(db)
		require.NoError(t, err)
		_, err = db.Exec(`DELETE FROM rogerai.tower_health; DELETE FROM rogerai.tower_health_daily`)
		require.NoError(t, err)
		body(t, s)
	})
}

var day0 = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func TestHealthChecksAreCountedPerTowerPerDay(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		for i := 0; i < 10; i++ {
			require.NoError(t, s.Record("tw-1", i != 3, day0.Add(time.Duration(i)*time.Hour*12)))
		}
		require.NoError(t, s.Record("tw-2", false, day0))

		sum, err := s.Summary("tw-1", day0)
		require.NoError(t, err)
		require.Equal(t, 9, sum.Passed)
		require.Equal(t, 1, sum.Failed)
		require.Equal(t, 0, sum.FailStreak)
		require.True(t, sum.LastAt.Equal(day0.Add(9*12*time.Hour)))

		// Since a later day counts only from that day.
		sum, err = s.Summary("tw-1", day0.Add(72*time.Hour))
		require.NoError(t, err)
		require.Equal(t, 5, sum.Passed, "checks 5..9 fall on Nov 4 and 5 (check 5 is Nov 4 00:00), counted by whole UTC day")
		require.Equal(t, 0, sum.Failed)

		other, err := s.Summary("tw-2", day0)
		require.NoError(t, err)
		require.Equal(t, 0, other.Passed)
		require.Equal(t, 1, other.Failed)
		require.Equal(t, 1, other.FailStreak)
	})
}

func TestTheFailStreakAndTheLastTwo(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		require.NoError(t, s.Record("tw-1", true, day0))
		require.NoError(t, s.Record("tw-1", false, day0.Add(time.Minute)))
		sum, _ := s.Summary("tw-1", day0)
		require.Equal(t, 1, sum.FailStreak)
		require.True(t, sum.StreakSince.Equal(day0.Add(time.Minute)), "the run began at its first failure")
		require.False(t, sum.BothLastFailed())

		require.NoError(t, s.Record("tw-1", false, day0.Add(2*time.Minute)))
		sum, _ = s.Summary("tw-1", day0)
		require.Equal(t, 2, sum.FailStreak)
		require.True(t, sum.BothLastFailed())
		require.True(t, sum.StreakSince.Equal(day0.Add(time.Minute)), "and keeps its start")

		require.NoError(t, s.Record("tw-1", true, day0.Add(3*time.Minute)))
		sum, _ = s.Summary("tw-1", day0)
		require.Equal(t, 0, sum.FailStreak, "a pass resets the streak")
		require.True(t, sum.StreakSince.IsZero())
		require.False(t, sum.BothLastFailed())
	})
}

func TestATowerNeverCheckedHasAnEmptySummary(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		sum, err := s.Summary("tw-none", day0)
		require.NoError(t, err)
		require.Equal(t, Summary{}, sum)
		require.False(t, sum.BothLastFailed(), "no evidence is not failing evidence")
		require.Zero(t, sum.Rate())
	})
}

func TestThePassRate(t *testing.T) {
	require.InDelta(t, 0.95, Summary{Passed: 95, Failed: 5}.Rate(), 1e-9)
	require.Zero(t, Summary{}.Rate())
}

func TestOldDaysAreReaped(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		require.NoError(t, s.Record("tw-1", true, day0))
		require.NoError(t, s.Record("tw-1", true, day0.Add(100*24*time.Hour)))
		require.NoError(t, s.Reap(day0.Add(90*24*time.Hour)))
		sum, _ := s.Summary("tw-1", day0)
		require.Equal(t, 1, sum.Passed, "the 100-day-old row is gone")
		require.False(t, sum.LastAt.IsZero(), "the latest result is kept")
	})
}

func TestAnOutOfOrderRecordDoesNotRewriteTheLatest(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		require.NoError(t, s.Record("tw-1", true, day0.Add(time.Hour)))
		require.NoError(t, s.Record("tw-1", false, day0)) // older, arriving late
		sum, _ := s.Summary("tw-1", day0)
		require.True(t, sum.LastAt.Equal(day0.Add(time.Hour)), "the newer result stays the latest")
		require.Equal(t, 1, sum.Failed, "but it is still counted")
		require.Equal(t, 0, sum.FailStreak)
	})
}

// A hub check is recorded as one, with its round-trip time (tower_trust_ramp.feature, "Every
// live Tower with a hub is checked"); a Station canary is recorded as a canary.
func TestTheLatestCheckKeepsItsSourceAndRoundTrip(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		require.NoError(t, s.RecordCheck("tw-1", Check{OK: true, At: day0, Source: SourceHubCheck, RTT: 42 * time.Millisecond}))
		sum, err := s.Summary("tw-1", day0)
		require.NoError(t, err)
		require.Equal(t, SourceHubCheck, sum.LastSource)
		require.Equal(t, 42*time.Millisecond, sum.LastRTT)
		require.Equal(t, 1, sum.Passed)

		require.NoError(t, s.Record("tw-1", false, day0.Add(time.Minute)))
		sum, _ = s.Summary("tw-1", day0)
		require.Equal(t, SourceCanary, sum.LastSource)
		require.Zero(t, sum.LastRTT)
		require.Equal(t, 1, sum.Failed)
	})
}
