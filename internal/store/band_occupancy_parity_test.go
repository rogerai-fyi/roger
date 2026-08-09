package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// ONE LIVE BAND PER NODE, decided the same way by both backends.
//
// Found by the pre-push audit. Before the fix, the two implementations answered "is this
// node occupied?" differently:
//
//	Mem        looks up bs.byNode[nodeID], a ONE-ENTRY index, and checks whether that
//	           single band is revoked.
//	Postgres   runs EXISTS(SELECT 1 ... WHERE node_id=$1 AND revoked=false), which sees
//	           every row.
//
// They agreed while a node had carried at most one band ever. They diverged the moment a node
// has carried two, because CreateBand overwrites byNode[node] - so the index can point at a
// REVOKED band while a LIVE one is still sitting on that node. Mem then read "occupied by
// something revoked, go ahead", and admits a second live band onto a node that already has
// one. Postgres refused. Mem broke its own stated invariant, and only on the durable path
// did the rule actually hold.
//
// This is the failure mode the parity suite exists for: neither backend is obviously wrong
// in isolation, and only running one scenario against both shows the disagreement.
func TestOneLiveBandPerNodeHoldsWhenANodeAlsoCarriesARevokedBand(t *testing.T) {
	for name, s := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			// Two bands have lived on shared-node over time: one still live, one revoked.
			// Order matters - the revoked one is created LAST, so a single-entry index ends
			// up pointing at it.
			require.NoError(t, s.CreateBand(Band{
				ID: "band_live", Owner: "owner_pub", CodeHash: "h_live",
				NodeID: "shared-node", CreatedAt: 1000,
			}))
			require.NoError(t, s.CreateBand(Band{
				ID: "band_dead", Owner: "owner_pub", CodeHash: "h_dead",
				NodeID: "shared-node", CreatedAt: 1001, Revoked: true,
			}))

			// A third band, elsewhere, tries to move onto that node.
			require.NoError(t, s.CreateBand(Band{
				ID: "band_mover", Owner: "owner_pub", CodeHash: "h_mover",
				NodeID: "other-node", CreatedAt: 1002,
			}))

			moved, err := s.MoveBand("band_mover", "owner_pub", "shared-node")
			require.False(t, moved,
				"shared-node still carries a LIVE band, so the move must be refused")
			require.ErrorIs(t, err, ErrBandNodeOccupied,
				"the refusal must name the invariant: one node carries at most one live band")

			// And the mover is untouched where it was.
			got, found, err := s.BandByNode("other-node")
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, "band_mover", got.ID, "a refused move must leave the band alone")

			got, found, err = s.BandByNode("shared-node")
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, "band_live", got.ID,
				"node lookup must prefer the live band over newer revoked history")
		})
	}
}
