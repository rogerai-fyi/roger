package store

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// MoveBand is the first write path Band.NodeID has ever had. Until now NodeID was set once
// at CreateBand and no store method could change it, which is why a private band was hard
// bound to one model for life (node id is "<station>-<model>"). Moving it is what lets an
// owner point their band at a different model WITHOUT rotating the secret code and cutting
// off everyone tuned in.
//
// EVERY case runs against BOTH backends. It was Mem-only when first written, and that is
// exactly the gap this repo has been bitten by before: a Postgres CAS silently dropped
// columns while the memory store kept them, and the tests stayed green throughout. Mem and
// Postgres implement MoveBand in completely different ways - a map delete plus a reindex
// versus a locking transaction - so agreement between them is a real result, not a
// formality. The Postgres half skips when ROGERAI_TEST_DATABASE_URL is unset; cover-gate
// always provisions one.
//
// For the same reason every assertion RE-READS from the store instead of trusting a
// returned value: an UPDATE that repointed node_id but dropped the display would still
// report moved=true.
//
// The four return shapes both backends must agree on:
//
//	unknown id / another owner / revoked -> (false, nil)   refused, indistinguishable
//	already on that node                 -> (true,  nil)   idempotent retry
//	destination occupied                 -> (false, ErrBandNodeOccupied)
//	moved                                -> (true,  nil)
//
// Spec: features/sharing/band_management.feature.
func eachBandStore(t *testing.T, fn func(t *testing.T, s Store)) {
	t.Helper()
	for name, s := range parityStores(t) {
		t.Run(name, func(t *testing.T) { fn(t, s) })
	}
}

func bandFixture(t *testing.T, s Store) Band {
	t.Helper()
	b := Band{
		ID: "band_1", CodeHash: "hash_1", CodeDisplay: "147.520 MHz · ••••-••••",
		Owner: "owner_pub", NodeID: "amber-fox-model-a", CreatedAt: 1000,
	}
	require.NoError(t, s.CreateBand(b), "CreateBand")
	return b
}

func TestMoveBandKeepsTheCodeAndFindsTheNewNode(t *testing.T) {
	eachBandStore(t, func(t *testing.T, s Store) {
		b := bandFixture(t, s)

		moved, err := s.MoveBand(b.ID, b.Owner, "amber-fox-model-b")
		require.NoError(t, err, "MoveBand")
		require.True(t, moved, "MoveBand should report that it moved the band")

		// EVERY column must survive, not just the one the move touches. An UPDATE that
		// repointed node_id but dropped the owner or created_at would still return
		// moved=true - the exact shape of the CAS bug this repo has already shipped.
		got, ok, err := s.BandByCodeHash("hash_1")
		require.NoError(t, err)
		require.True(t, ok, "the moved band must still resolve by its ORIGINAL code hash")
		require.Equal(t, b.ID, got.ID, "move must not disturb identity")
		require.Equal(t, b.CodeDisplay, got.CodeDisplay, "move must not disturb the masked display")
		require.Equal(t, b.Owner, got.Owner, "move must not drop the owner")
		require.Equal(t, b.CreatedAt, got.CreatedAt, "move must not drop created_at")
		require.False(t, got.Revoked, "a move must not revoke the band")
		require.Equal(t, "amber-fox-model-b", got.NodeID, "NodeID must be the destination")

		// The register seam (tunnel.go BandByNode) must now find it at the DESTINATION...
		nb, ok, err := s.BandByNode("amber-fox-model-b")
		require.NoError(t, err)
		require.True(t, ok, "BandByNode(destination) must return the moved band")
		require.Equal(t, b.ID, nb.ID)

		// ...and must NOT find it at the source, or the old node would silently keep the
		// band and go on answering for it (privacy fails closed).
		_, ok, err = s.BandByNode("amber-fox-model-a")
		require.NoError(t, err)
		require.False(t, ok, "the source node must no longer resolve a band")

		// A move is not a mint: the quota is unchanged.
		n, err := s.CountActiveBands(b.Owner, time.Now())
		require.NoError(t, err)
		require.Equal(t, 1, n, "a move must never mint")
	})
}

func TestMoveBandRefusesAnOccupiedDestination(t *testing.T) {
	eachBandStore(t, func(t *testing.T, s Store) {
		b := bandFixture(t, s)
		// A second band already lives on the destination node.
		other := Band{ID: "band_2", CodeHash: "hash_2", Owner: b.Owner, NodeID: "amber-fox-model-b"}
		require.NoError(t, s.CreateBand(other))

		moved, err := s.MoveBand(b.ID, b.Owner, "amber-fox-model-b")
		require.False(t, moved, "moving onto an occupied node must be refused")
		require.ErrorIs(t, err, ErrBandNodeOccupied,
			"the refusal must name the invariant it protects - one node carries at most one band")

		// Both bands are left exactly as they were: silently displacing the occupant would
		// take a station off air its owner never touched.
		got, _, err := s.BandByNode("amber-fox-model-a")
		require.NoError(t, err)
		require.Equal(t, "band_1", got.ID, "a refused move must leave the source band in place")
		got, _, err = s.BandByNode("amber-fox-model-b")
		require.NoError(t, err)
		require.Equal(t, "band_2", got.ID, "a refused move must leave the destination band in place")
	})
}

func TestMoveBandIsOwnerScoped(t *testing.T) {
	eachBandStore(t, func(t *testing.T, s Store) {
		b := bandFixture(t, s)

		moved, err := s.MoveBand(b.ID, "someone_else", "attacker-node")
		require.NoError(t, err,
			"a foreign move is a plain refusal, not an error - an error would confirm the band exists")
		require.False(t, moved, "a band must never be moved by anyone but its issuing owner")

		got, _, err := s.BandByNode("amber-fox-model-a")
		require.NoError(t, err)
		require.Equal(t, b.ID, got.ID, "a foreign move attempt must not disturb the band")
		_, ok, err := s.BandByNode("attacker-node")
		require.NoError(t, err)
		require.False(t, ok, "a foreign move must not bind the band to the attacker's node")
	})
}

func TestMoveBandUnknownIDReportsNoMove(t *testing.T) {
	eachBandStore(t, func(t *testing.T, s Store) {
		b := bandFixture(t, s)
		moved, err := s.MoveBand("band_nope", b.Owner, "amber-fox-model-b")
		require.NoError(t, err, "an unknown id is indistinguishable from another owner's band, by design")
		require.False(t, moved, "an unknown band id must report no move")
	})
}

// A revoked band is dead: moving it would resurrect a burnt code at a new node.
func TestMoveBandRefusesARevokedBand(t *testing.T) {
	eachBandStore(t, func(t *testing.T, s Store) {
		b := bandFixture(t, s)
		revoked, err := s.SetBandRevoked(b.ID, b.Owner, true)
		require.NoError(t, err, "SetBandRevoked")
		require.True(t, revoked)

		moved, err := s.MoveBand(b.ID, b.Owner, "amber-fox-model-b")
		require.NoError(t, err)
		require.False(t, moved, "a revoked band must not be movable - its code is permanently burnt")

		// And the destination must not have been bound on the way to refusing.
		_, ok, err := s.BandByNode("amber-fox-model-b")
		require.NoError(t, err)
		require.False(t, ok, "a refused move must leave the destination empty")
	})
}

// Moving to the node it already sits on is a harmless no-op, not an "occupied" error
// (otherwise a retried request would fail confusingly). Note the destination IS occupied -
// by the band itself - so this case has to be decided BEFORE the occupancy check, in both
// implementations.
func TestMoveBandToItsOwnNodeIsIdempotent(t *testing.T) {
	eachBandStore(t, func(t *testing.T, s Store) {
		b := bandFixture(t, s)
		moved, err := s.MoveBand(b.ID, b.Owner, b.NodeID)
		require.NoError(t, err, "a self-move must not error")
		require.True(t, moved, "a self-move should report success (idempotent retry)")

		got, ok, err := s.BandByNode(b.NodeID)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, b.ID, got.ID, "a self-move must leave the band bound where it was")
	})
}

// A REVOKED band on the destination does not block a move: its code is dead, so the node is
// free. This separates "occupied" from "carries an old tombstone", and the backends decide
// it differently - Mem checks the occupant's Revoked flag through byNode, Postgres filters
// revoked=false in the EXISTS - so it is worth pinning on both.
func TestMoveBandRevokedOccupantDoesNotBlock(t *testing.T) {
	eachBandStore(t, func(t *testing.T, s Store) {
		b := bandFixture(t, s)
		dead := Band{ID: "band_dead", CodeHash: "hash_dead", Owner: b.Owner, NodeID: "amber-fox-model-b"}
		require.NoError(t, s.CreateBand(dead))
		revoked, err := s.SetBandRevoked("band_dead", b.Owner, true)
		require.NoError(t, err)
		require.True(t, revoked)

		moved, err := s.MoveBand(b.ID, b.Owner, "amber-fox-model-b")
		require.NoError(t, err, "a revoked occupant must not poison a node forever")
		require.True(t, moved)

		got, _, err := s.BandByCodeHash("hash_1")
		require.NoError(t, err)
		require.Equal(t, "amber-fox-model-b", got.NodeID)

		// The LIVE band wins the node lookup over the revoked tombstone sharing it.
		nb, ok, err := s.BandByNode("amber-fox-model-b")
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, b.ID, nb.ID, "the live band must win the node lookup, not the tombstone")
	})
}

// The broker 400s an empty node_id long before the store sees it, but if one ever arrived
// the band must not be stranded: a band that resolves to nothing is unreachable forever,
// with no way to re-bind it.
func TestMoveBandEmptyDestinationLeavesTheBandResolvable(t *testing.T) {
	eachBandStore(t, func(t *testing.T, s Store) {
		b := bandFixture(t, s)
		_, err := s.MoveBand(b.ID, b.Owner, "")
		require.NoError(t, err, "an empty destination must not error out of the store")

		got, ok, err := s.BandByCodeHash("hash_1")
		require.NoError(t, err)
		require.True(t, ok, "the band must still resolve by its code")
		require.Equal(t, b.ID, got.ID)
	})
}
