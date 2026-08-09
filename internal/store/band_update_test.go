package store

import (
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// PATCH /bands/{id} is an update, not only a move. The approved feature makes label the
// first editable band metadata while preserving the shown-once code and current binding.
func TestUpdateBandWritesAndClearsLabelWithoutMoving(t *testing.T) {
	eachBandStore(t, func(t *testing.T, s Store) {
		b := bandFixture(t, s)
		label := "family"
		updated, ok, err := s.UpdateBand(b.ID, b.Owner, BandPatch{Label: &label})
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, "family", updated.Label)
		require.Equal(t, b.NodeID, updated.NodeID, "a label-only patch must not move the band")

		got, found, err := s.BandByCodeHash(b.CodeHash)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, "family", got.Label)
		require.Equal(t, b.CodeDisplay, got.CodeDisplay)

		clear := ""
		updated, ok, err = s.UpdateBand(b.ID, b.Owner, BandPatch{Label: &clear})
		require.NoError(t, err)
		require.True(t, ok)
		require.Empty(t, updated.Label, "an explicit empty label clears it")
	})
}

func TestUpdateBandMoveAndLabelAreAtomic(t *testing.T) {
	eachBandStore(t, func(t *testing.T, s Store) {
		b := bandFixture(t, s)
		require.NoError(t, s.CreateBand(Band{
			ID: "band_2", CodeHash: "hash_2", Owner: b.Owner, NodeID: "occupied",
		}))
		label, node := "must-not-stick", "occupied"
		updated, ok, err := s.UpdateBand(b.ID, b.Owner, BandPatch{Label: &label, NodeID: &node})
		require.ErrorIs(t, err, ErrBandNodeOccupied)
		require.False(t, ok)
		require.Equal(t, Band{}, updated)

		got, found, err := s.BandByCodeHash(b.CodeHash)
		require.NoError(t, err)
		require.True(t, found)
		require.Empty(t, got.Label, "a refused move must not partially apply its label")
		require.Equal(t, b.NodeID, got.NodeID)
	})
}

func TestCreateBandRefusesASecondLiveBandOnTheSameNode(t *testing.T) {
	eachBandStore(t, func(t *testing.T, s Store) {
		require.NoError(t, s.CreateBand(Band{
			ID: "band_1", CodeHash: "hash_1", Owner: "owner_1", NodeID: "shared-node",
		}))
		err := s.CreateBand(Band{
			ID: "band_2", CodeHash: "hash_2", Owner: "owner_2", NodeID: "shared-node",
		})
		require.ErrorIs(t, err, ErrBandNodeOccupied,
			"the one-live-band invariant must cover concurrent mints as well as moves")
	})
}

func TestUnrevokeBandRefusesAnOccupiedNode(t *testing.T) {
	eachBandStore(t, func(t *testing.T, s Store) {
		require.NoError(t, s.CreateBand(Band{
			ID: "band_live", CodeHash: "hash_live", Owner: "owner", NodeID: "shared-node",
		}))
		require.NoError(t, s.CreateBand(Band{
			ID: "band_dead", CodeHash: "hash_dead", Owner: "owner", NodeID: "shared-node", Revoked: true,
		}))
		ok, err := s.SetBandRevoked("band_dead", "owner", false)
		require.False(t, ok)
		require.ErrorIs(t, err, ErrBandNodeOccupied,
			"unrevoking must obey the same invariant as minting and moving")
	})
}

// The durable invariant must be enforced by PostgreSQL itself. An application-only
// EXISTS check is racy: two transactions moving different source rows can both observe an
// empty destination. Exactly one may win, regardless of scheduling.
func TestPostgresConcurrentMovesCannotShareADestination(t *testing.T) {
	pg := pgOnly(t)
	require.NoError(t, pg.CreateBand(Band{ID: "band_a", CodeHash: "hash_a", Owner: "owner", NodeID: "node-a"}))
	require.NoError(t, pg.CreateBand(Band{ID: "band_b", CodeHash: "hash_b", Owner: "owner", NodeID: "node-b"}))

	type result struct {
		moved bool
		err   error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for _, id := range []string{"band_a", "band_b"} {
		go func(id string) {
			ready.Done()
			<-start
			moved, err := pg.MoveBand(id, "owner", "shared-destination")
			results <- result{moved: moved, err: err}
		}(id)
	}
	ready.Wait()
	close(start)

	wins, occupied := 0, 0
	for range 2 {
		r := <-results
		switch {
		case r.moved && r.err == nil:
			wins++
		case !r.moved && isBandNodeOccupied(r.err):
			occupied++
		default:
			t.Fatalf("unexpected concurrent move result: moved=%v err=%v", r.moved, r.err)
		}
	}
	require.Equal(t, 1, wins)
	require.Equal(t, 1, occupied)

	var live int
	require.NoError(t, pg.db.QueryRow(`SELECT COUNT(*) FROM rogerai.private_bands
		WHERE node_id='shared-destination' AND revoked=false`).Scan(&live))
	require.Equal(t, 1, live, "the database must never commit two live destination rows")
}

func isBandNodeOccupied(err error) bool { return errors.Is(err, ErrBandNodeOccupied) }
