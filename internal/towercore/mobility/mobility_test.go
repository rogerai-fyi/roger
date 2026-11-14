package mobility

// What Core keeps about each Station for placement and moves (station_mobility.feature,
// placement_locality.feature "A Station's location ..."), on both stores.

import (
	"database/sql"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v7/internal/pgtest"
	"rogerai.fm/roger/v7/internal/towercore/placement"
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
		_, err = db.Exec(`DELETE FROM rogerai.station_facts; DELETE FROM rogerai.station_moves`)
		require.NoError(t, err)
		body(t, s)
	})
}

var t0 = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

func TestFactsAreReplacedWholeOnEveryAttach(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		_, ok, err := s.Facts("st-1")
		require.NoError(t, err)
		require.False(t, ok)
		require.NoError(t, s.PutFacts("st-1", Facts{Locality: placement.Locality{Bucket: "b1", Continent: "NA"}, Nudge: true}))
		f, ok, err := s.Facts("st-1")
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, Facts{Locality: placement.Locality{Bucket: "b1", Continent: "NA"}, Nudge: true}, f)
		require.NoError(t, s.PutFacts("st-1", Facts{}))
		f, _, _ = s.Facts("st-1")
		require.Equal(t, Facts{}, f, "no value from the old network survives")
	})
}

func TestAMoveStartsOnceAndIsReadBack(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		m := Move{StationID: "st-1", From: "tw-a", To: "tw-b", Trigger: "better_tower", Epoch: 4,
			PriorState: "active", Started: t0}
		ok, err := s.StartMove(m)
		require.NoError(t, err)
		require.True(t, ok)
		ok, err = s.StartMove(m)
		require.NoError(t, err)
		require.False(t, ok, "one move per Station at a time")

		got, ok, err := s.Move("st-1")
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, "tw-b", got.To)
		require.Equal(t, int64(4), got.Epoch)
		require.True(t, got.Started.Equal(t0))
		require.Equal(t, 0, got.Nudges)

		n, err := s.NoteNudge("st-1", t0.Add(time.Second))
		require.NoError(t, err)
		require.Equal(t, 1, n)
		n, _ = s.NoteNudge("st-1", t0.Add(2*time.Second))
		require.Equal(t, 2, n)
		got, _, _ = s.Move("st-1")
		require.True(t, got.LastNudge.Equal(t0.Add(2*time.Second)))

		pending, err := s.Pending()
		require.NoError(t, err)
		require.Len(t, pending, 1)
		require.Equal(t, "st-1", pending[0].StationID)
	})
}

func TestFinishingAMoveKeepsOnlyItsSkip(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		_, _ = s.StartMove(Move{StationID: "st-1", From: "tw-a", To: "tw-b", Trigger: "t", Epoch: 1, PriorState: "active", Started: t0})
		require.NoError(t, s.FinishMove("st-1", "tw-b", t0.Add(time.Hour)))
		_, ok, _ := s.Move("st-1")
		require.False(t, ok, "no move in progress")
		pending, _ := s.Pending()
		require.Empty(t, pending)
		tower, until, err := s.Skip("st-1")
		require.NoError(t, err)
		require.Equal(t, "tw-b", tower)
		require.True(t, until.Equal(t0.Add(time.Hour)))

		// A finish without a skip clears any.
		_, _ = s.StartMove(Move{StationID: "st-1", From: "tw-a", To: "tw-c", Trigger: "t", Epoch: 2, PriorState: "active", Started: t0})
		require.NoError(t, s.FinishMove("st-1", "", time.Time{}))
		tower, until, _ = s.Skip("st-1")
		require.Empty(t, tower)
		require.True(t, until.IsZero(), "a zero time clears the pause")

		// A pause with no Tower to keep it off.
		_, _ = s.StartMove(Move{StationID: "st-1", From: "tw-a", Trigger: "tower_failing", Epoch: 3, PriorState: "active", Started: t0})
		require.NoError(t, s.FinishMove("st-1", "", t0.Add(time.Hour)))
		tower, until, _ = s.Skip("st-1")
		require.Empty(t, tower)
		require.True(t, until.Equal(t0.Add(time.Hour)))

		// Finishing what is not there is fine.
		require.NoError(t, s.FinishMove("st-none", "", time.Time{}))
		n, err := s.NoteNudge("st-none", t0)
		require.NoError(t, err)
		require.Zero(t, n)
	})
}

func TestForgetDropsEverythingAboutReapedStations(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		require.NoError(t, s.PutFacts("st-1", Facts{Nudge: true}))
		_, _ = s.StartMove(Move{StationID: "st-1", From: "a", Trigger: "t", Epoch: 1, PriorState: "active", Started: t0})
		require.NoError(t, s.PutFacts("st-2", Facts{Nudge: true}))
		require.NoError(t, s.Forget([]string{"st-1", "st-none"}))
		_, ok, _ := s.Facts("st-1")
		require.False(t, ok)
		_, ok, _ = s.Move("st-1")
		require.False(t, ok)
		_, ok, _ = s.Facts("st-2")
		require.True(t, ok, "others are kept")
		require.NoError(t, s.Forget(nil))
	})
}

// Releasing a claimed move leaves a Station's skip alone (audit finding: cleanup paths erased
// the hour a returned or undone move had just set).
func TestReleaseClearsTheClaimButNotTheSkip(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		_, _ = s.StartMove(Move{StationID: "st-1", From: "a", Trigger: "t", Epoch: 1, PriorState: "active", Started: t0})
		require.NoError(t, s.FinishMove("st-1", "tw-b", t0.Add(time.Hour)))
		ok, _ := s.StartMove(Move{StationID: "st-1", From: "a", Trigger: "t", Epoch: 2, PriorState: "active", Started: t0})
		require.True(t, ok)
		require.NoError(t, s.ReleaseMove("st-1"))
		_, moving, _ := s.Move("st-1")
		require.False(t, moving)
		tower, until, _ := s.Skip("st-1")
		require.Equal(t, "tw-b", tower)
		require.True(t, until.Equal(t0.Add(time.Hour)))
		require.NoError(t, s.ReleaseMove("st-none"))
	})
}
