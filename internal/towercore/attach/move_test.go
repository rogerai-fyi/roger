package attach

// The two narrow writes a Station move needs (features/tower/station_mobility.feature): set a
// live Station dormant only if it is still the row the mover read, and put it back only if it
// is still that dormant row. Run against both stores.

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDormantForMoveIsACompareAndSet(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, r *Registry, _ time.Time) {
		at, err := r.Admit(goodProof())
		require.NoError(t, err)

		ok, err := s.DormantForMove(station, "other-tower", at.Epoch)
		require.NoError(t, err)
		require.False(t, ok, "a different Tower than the row's: nothing written")
		ok, err = s.DormantForMove(station, tower, at.Epoch+1)
		require.NoError(t, err)
		require.False(t, ok, "a different epoch: nothing written")
		got, _, _ := s.ByStation(station)
		require.Equal(t, StateQuarantine, got.State)

		ok, err = s.DormantForMove(station, tower, at.Epoch)
		require.NoError(t, err)
		require.True(t, ok)
		got, _, _ = s.ByStation(station)
		require.Equal(t, StateDormant, got.State)
		require.Equal(t, tower, got.Origin.TowerID, "the origin is not written: Admit's revival does that")
		require.Equal(t, at.Epoch, got.Epoch, "nor the epoch")

		ok, err = s.DormantForMove(station, tower, at.Epoch)
		require.NoError(t, err)
		require.False(t, ok, "already dormant: a second mover writes nothing")

		ok, err = s.DormantForMove("no-such-station", tower, 1)
		require.NoError(t, err)
		require.False(t, ok)
	})
}

func TestOnlyOneOfTwoMoversWins(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, r *Registry, _ time.Time) {
		at, err := r.Admit(goodProof())
		require.NoError(t, err)
		var wg sync.WaitGroup
		wins := make([]bool, 8)
		for i := range wins {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				wins[i], _ = s.DormantForMove(station, tower, at.Epoch)
			}(i)
		}
		wg.Wait()
		n := 0
		for _, w := range wins {
			if w {
				n++
			}
		}
		require.Equal(t, 1, n)
	})
}

func TestUndoMoveRestoresOnlyThatDormantRow(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store, r *Registry, _ time.Time) {
		at, err := r.Admit(goodProof())
		require.NoError(t, err)
		ok, err := s.UndoMove(station, tower, at.Epoch, StateQuarantine)
		require.NoError(t, err)
		require.False(t, ok, "not dormant: nothing to undo")

		ok, _ = s.DormantForMove(station, tower, at.Epoch)
		require.True(t, ok)
		ok, err = s.UndoMove(station, tower, at.Epoch+1, StateQuarantine)
		require.NoError(t, err)
		require.False(t, ok, "a row that moved on (another epoch) is not undone")
		ok, err = s.UndoMove(station, tower, at.Epoch, StateRevoked)
		require.Error(t, err, "an undo restores a serving state only")
		require.False(t, ok)

		ok, err = s.UndoMove(station, tower, at.Epoch, StateQuarantine)
		require.NoError(t, err)
		require.True(t, ok)
		got, _, _ := s.ByStation(station)
		require.Equal(t, StateQuarantine, got.State)
		require.Equal(t, at.Epoch, got.Epoch, "the old epoch, not a new one")
	})
}
