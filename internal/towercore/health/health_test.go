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
		_, err = db.Exec(`DELETE FROM rogerai.tower_health; DELETE FROM rogerai.tower_health_daily; DELETE FROM rogerai.tower_health_runs`)
		require.NoError(t, err)
		body(t, s)
	})
}

var day0 = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func TestHealthChecksAreCountedPerTowerPerDay(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		for i := 0; i < 10; i++ {
			require.NoError(t, record(s, "tw-1", i != 3, day0.Add(time.Duration(i)*time.Hour*12)))
		}
		require.NoError(t, record(s, "tw-2", false, day0))

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
		require.NoError(t, record(s, "tw-1", true, day0))
		require.NoError(t, record(s, "tw-1", false, day0.Add(time.Minute)))
		sum, _ := s.Summary("tw-1", day0)
		require.Equal(t, 1, sum.FailStreak)
		require.True(t, sum.StreakSince.Equal(day0.Add(time.Minute)), "the run began at its first failure")
		require.False(t, sum.BothLastFailed())

		require.NoError(t, record(s, "tw-1", false, day0.Add(2*time.Minute)))
		sum, _ = s.Summary("tw-1", day0)
		require.Equal(t, 2, sum.FailStreak)
		require.True(t, sum.BothLastFailed())
		require.True(t, sum.StreakSince.Equal(day0.Add(time.Minute)), "and keeps its start")

		require.NoError(t, record(s, "tw-1", true, day0.Add(3*time.Minute)))
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
		require.NoError(t, record(s, "tw-1", true, day0))
		require.NoError(t, record(s, "tw-1", true, day0.Add(100*24*time.Hour)))
		require.NoError(t, s.Reap(day0.Add(90*24*time.Hour)))
		sum, _ := s.Summary("tw-1", day0)
		require.Equal(t, 1, sum.Passed, "the 100-day-old row is gone")
		require.False(t, sum.LastAt.IsZero(), "the latest result is kept")
	})
}

func TestAnOutOfOrderRecordDoesNotRewriteTheLatest(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		require.NoError(t, record(s, "tw-1", true, day0.Add(time.Hour)))
		require.NoError(t, record(s, "tw-1", false, day0)) // older, arriving late
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

		require.NoError(t, s.RecordCheck("tw-1", Check{OK: false, At: day0.Add(time.Minute), Source: SourceCanary}))
		sum, _ = s.Summary("tw-1", day0)
		require.Equal(t, SourceCanary, sum.LastSource)
		require.Zero(t, sum.LastRTT)
		require.Equal(t, 1, sum.Failed)
	})
}

// The fail streak counts every health check - hub checks and Station canaries alike, as the
// approved specs define it - but at most one failure per Tower per canary interval (a slot), so
// a hub check and a canary failing in the same sweep are one outage, not two. A pass resets it.
// Results from an earlier slot than the latest one counted never move it.
func TestTheStreakCountsOneFailurePerSlotFromAnySource(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		require.NoError(t, s.RecordCheck("tw-1", Check{OK: false, At: day0, Source: SourceHubCheck, Slot: 100}))
		require.NoError(t, s.RecordCheck("tw-1", Check{OK: false, At: day0.Add(time.Second), Source: SourceCanary, Slot: 100}))
		sum, _ := s.Summary("tw-1", day0)
		require.Equal(t, 1, sum.FailStreak, "one outage in one slot")
		require.Equal(t, 2, sum.Failed, "both count toward the rate")

		// The next slot's canary alone advances it: a failing relay with a live hub is caught.
		require.NoError(t, s.RecordCheck("tw-1", Check{OK: false, At: day0.Add(5 * time.Minute), Source: SourceCanary, Slot: 101}))
		sum, _ = s.Summary("tw-1", day0)
		require.Equal(t, 2, sum.FailStreak)
		require.True(t, sum.StreakSince.Equal(day0.Add(time.Second)), "the canary run began at its first failure")

		// A late result from an earlier slot never moves it, whatever its time stamp says.
		require.NoError(t, s.RecordCheck("tw-1", Check{OK: true, At: day0.Add(10 * time.Minute), Source: SourceHubCheck, Slot: 99}))
		sum, _ = s.Summary("tw-1", day0)
		require.Equal(t, 2, sum.FailStreak)

		// A canary pass clears the canary run; the hub run (one failure) is what is left.
		require.NoError(t, s.RecordCheck("tw-1", Check{OK: true, At: day0.Add(6 * time.Minute), Source: SourceCanary, Slot: 101}))
		sum, _ = s.Summary("tw-1", day0)
		require.Equal(t, 1, sum.FailStreak)
		require.NoError(t, s.RecordCheck("tw-1", Check{OK: true, At: day0.Add(7 * time.Minute), Source: SourceHubCheck, Slot: 101}))
		sum, _ = s.Summary("tw-1", day0)
		require.Zero(t, sum.FailStreak)
		require.True(t, sum.StreakSince.IsZero())
	})
}

// record files a check in a slot of its own (its nanosecond), so each call counts on its own -
// what these tests mean by "a check at this time".
func record(s Store, towerID string, ok bool, at time.Time) error {
	return s.RecordCheck(towerID, Check{OK: ok, At: at, Source: SourceHubCheck, Slot: at.UnixNano()})
}

// A pass clears the slot's counted failure, so fail, pass, fail in one slot ends on a streak
// of one: the latest check failed (audit finding).
func TestAPassClearsTheSlotsCountedFailure(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		for i, ok := range []bool{false, true, false} {
			require.NoError(t, s.RecordCheck("tw-1", Check{OK: ok, At: day0.Add(time.Duration(i) * time.Second), Source: SourceHubCheck, Slot: 7}))
		}
		sum, _ := s.Summary("tw-1", day0)
		require.Equal(t, 1, sum.FailStreak)
	})
}

// Founder ruling 2026-11-14: a passing hub check does not clear canary failures, nor a passing
// canary hub-check failures. Each kind keeps its own run; the Tower's streak is the longer.
func TestEachKindOfCheckKeepsItsOwnRun(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		for i := int64(0); i < 2; i++ {
			at := day0.Add(time.Duration(i) * 5 * time.Minute)
			require.NoError(t, s.RecordCheck("tw-1", Check{OK: true, At: at, Source: SourceHubCheck, Slot: i}))
			require.NoError(t, s.RecordCheck("tw-1", Check{OK: false, At: at.Add(time.Second), Source: SourceCanary, Slot: i}))
		}
		sum, _ := s.Summary("tw-1", day0)
		require.Equal(t, 2, sum.FailStreak, "the hub answering does not hide a failing relay")
		require.True(t, sum.StreakSince.Equal(day0.Add(time.Second)), "the run began at the first canary failure")
	})
}

// Two broker instances recording one blip moments apart, across a slot boundary, count it once.
func TestFailuresCloserThanTheGapCountOnce(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		gap := 150 * time.Second
		require.NoError(t, s.RecordCheck("tw-1", Check{OK: false, At: day0, Source: SourceCanary, Slot: 10, MinGap: gap}))
		require.NoError(t, s.RecordCheck("tw-1", Check{OK: false, At: day0.Add(10 * time.Second), Source: SourceCanary, Slot: 11, MinGap: gap}))
		sum, _ := s.Summary("tw-1", day0)
		require.Equal(t, 1, sum.FailStreak)
		require.NoError(t, s.RecordCheck("tw-1", Check{OK: false, At: day0.Add(5 * time.Minute), Source: SourceCanary, Slot: 12, MinGap: gap}))
		sum, _ = s.Summary("tw-1", day0)
		require.Equal(t, 2, sum.FailStreak, "a real next interval counts")
	})
}

// AUDIT FINDING: when no canary can run any more (its Stations moved off), the canary run must
// not hold the Tower failing forever. A run its kind has not refreshed within RunStaleAfter of
// the Tower's latest result stops counting.
func TestARunNobodyRefreshesStopsCounting(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		for i := int64(0); i < 2; i++ {
			at := day0.Add(time.Duration(i) * 5 * time.Minute)
			require.NoError(t, s.RecordCheck("tw-1", Check{OK: false, At: at, Source: SourceCanary, Slot: i}))
		}
		sum, _ := s.Summary("tw-1", day0)
		require.Equal(t, 2, sum.FailStreak)
		// Only hub checks from now on, passing, for longer than RunStaleAfter.
		later := day0.Add(5*time.Minute + RunStaleAfter + time.Second)
		require.NoError(t, s.RecordCheck("tw-1", Check{OK: true, At: later, Source: SourceHubCheck, Slot: 9}))
		sum, _ = s.Summary("tw-1", day0)
		require.Zero(t, sum.FailStreak, "a canary run nobody refreshes no longer counts")
	})
}

// AUDIT FINDING: a pass did not reset the last counted failure, so fail, pass, fail inside the
// gap ended on a streak of zero with a failing latest result.
func TestAPassResetsTheGapClock(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		gap := 150 * time.Second
		for i, ok := range []bool{false, true, false} {
			require.NoError(t, s.RecordCheck("tw-1", Check{OK: ok, At: day0.Add(time.Duration(i) * time.Second),
				Source: SourceCanary, Slot: 5 + int64(i), MinGap: gap}))
		}
		sum, _ := s.Summary("tw-1", day0)
		require.Equal(t, 1, sum.FailStreak)
	})
}

// AUDIT FINDING: a run that went stale kept its old count, so the next failure of that kind
// resumed it (3 after one blip) with the old start. A result arriving after its kind has been
// quiet longer than RunStaleAfter starts the run over.
func TestAStaleRunStartsOver(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		for i := int64(0); i < 2; i++ {
			require.NoError(t, s.RecordCheck("tw-1", Check{OK: false, At: day0.Add(time.Duration(i) * 5 * time.Minute), Source: SourceCanary, Slot: i}))
		}
		back := day0.Add(5*time.Minute + RunStaleAfter + time.Minute)
		require.NoError(t, s.RecordCheck("tw-1", Check{OK: false, At: back, Source: SourceCanary, Slot: 50}))
		sum, _ := s.Summary("tw-1", day0)
		require.Equal(t, 1, sum.FailStreak, "one blip after a quiet spell is one")
		require.True(t, sum.StreakSince.Equal(back), "with a new start")
		require.NoError(t, s.RecordCheck("tw-1", Check{OK: false, At: back.Add(5 * time.Minute), Source: SourceCanary, Slot: 51}))
		sum, _ = s.Summary("tw-1", day0)
		require.Equal(t, 2, sum.FailStreak)
		require.True(t, sum.StreakSince.Equal(back))
	})
}
