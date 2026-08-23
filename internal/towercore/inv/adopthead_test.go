package inv

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// AdoptHead: the durable head is the chain authority for SEQUENCING; it never fabricates
// leaves and never rewinds what this instance has verified. These are the guards the
// two-instance BDD suite cannot isolate, because there the handler always adopts.

func TestAFullSnapshotChainsAgainstAnAdoptedHead(t *testing.T) {
	h := newHarness(t)
	base, leaf := h.baseline() // locally verified revision 40

	// A peer instance accepted 41 and 42; the durable store says so.
	h.set.AdoptHead(towerA, base.Revision+2, "durable-head-42")
	rev, hash, held := h.set.Head(towerA)
	require.True(t, held)
	require.Equal(t, base.Revision+2, rev)
	require.Equal(t, "durable-head-42", hash)

	// The tower's next full snapshot chains against the adopted head first try - the
	// exact push that used to be refused as "revision N skips M".
	res, err := h.set.AcceptFull(towerA, h.towerPub(), h.inventory(invSpec{
		revision: base.Revision + 3, prevHash: "durable-head-42",
		leaves: []map[string]any{leaf}}))
	require.NoError(t, err)
	require.Equal(t, base.Revision+3, res.Revision)
}

// A delta amends leaves, and an adopted head has none behind it. The answer is the
// resync the tower already satisfies by resending the full snapshot - decided by the
// check inside the write lock, where prior was fetched, because a pre-check outside it
// raced a concurrent AdoptHead.
func TestADeltaAgainstAnAdoptedHeadResyncs(t *testing.T) {
	h := newHarness(t)
	base, _ := h.baseline()
	h.set.AdoptHead(towerA, base.Revision+2, "durable-head-42")

	// Correctly chained, correctly signed - the ONLY problem is the missing leaves.
	_, err := h.set.AcceptDelta(towerA, h.towerPub(), h.delta(deltaSpec{
		base: base.Revision + 2, revision: base.Revision + 3, prevHash: "durable-head-42"}))
	require.ErrorIs(t, err, ErrResync,
		"a chained delta against a head-only state must resync, not apply onto an empty leaf map")
}

// Forward only: a lagging durable read must never rewind a head this instance verified,
// and an equal revision is a no-op - same position, and the local one holds the leaves.
func TestAdoptHeadNeverRewindsAVerifiedHead(t *testing.T) {
	h := newHarness(t)
	base, _ := h.baseline()

	h.set.AdoptHead(towerA, base.Revision-3, "an-older-hash")
	rev, hash, held := h.set.Head(towerA)
	require.True(t, held)
	require.Equal(t, base.Revision, rev, "an older durable read must not rewind")
	require.Equal(t, base.Hash, hash)

	h.set.AdoptHead(towerA, base.Revision, "a-competing-hash")
	_, hash, _ = h.set.Head(towerA)
	require.Equal(t, base.Hash, hash)

	// And garbage adopts nothing at all - including for a tower with NO prior, where the
	// forward-only check cannot be the thing that refuses it.
	h.set.AdoptHead(towerA, 0, "")
	rev, _, _ = h.set.Head(towerA)
	require.Equal(t, base.Revision, rev)

	h.set.AdoptHead(towerB, 0, "")
	_, _, held = h.set.Head(towerB)
	require.False(t, held, "a zero revision or empty hash must not mint a head from nothing")
	h.set.AdoptHead(towerB, 3, "")
	_, _, held = h.set.Head(towerB)
	require.False(t, held)
}

// Adopting a HIGHER head over held leaves discards them - and must release their Station
// origin claims, or those Stations stay pinned to this Tower until the next full accept
// and can never re-home.
func TestAdoptHeadReleasesTheDiscardedLeavesOrigins(t *testing.T) {
	h := newHarness(t)
	base, _ := h.baseline() // towerA holds stationA's origin via its leaf

	h.set.AdoptHead(towerA, base.Revision+2, "durable-head-42")

	// The claim itself is gone from the origin index - checked directly, because the
	// conflict path ALSO waives a holder whose state has expired, and a head-only state
	// carries no expiry: behavioural checks alone let a stale claim linger unobserved.
	h.set.mu.RLock()
	_, stillClaimed := h.set.origins[stationA]
	h.set.mu.RUnlock()
	require.False(t, stillClaimed, "the discarded leaves' origin claims must be released with them")

	// And behaviourally: the same Station advertised by ANOTHER tower is accepted.
	viaB := h.offer(stationA, "offer-b", offerSpec{pre: func(m map[string]any) { m["tower_id"] = towerB }})
	rawB := h.finish(map[string]any{
		"network": PublicNetwork, "tower_id": towerB, "revision": "1", "prev_hash": "genesis",
		"lease_head": "lease-1", "lifecycle_head": "life-1",
		"issued": h.unix(0), "expires": h.unix(30 * time.Minute),
		"leaves": []any{viaB},
	}, TypeInventory, nil, nil, nil, nil, false)
	res, err := h.set.AcceptFull(towerB, h.towerPub(), rawB)
	require.NoError(t, err)
	require.Equal(t, 1, res.Routable,
		"the discarded leaves' origin claims must be released with them")
}

// A cold instance - no local chain - adopts nothing. Minting a head from the durable
// record here would refuse a relinking Tower's cold-start rev-1/genesis snapshot as
// "does not advance", bricking its inventory on this instance until revocation.
func TestAColdInstanceAdoptsNothingAndAcceptsGenesis(t *testing.T) {
	h := newHarness(t)

	h.set.AdoptHead(towerA, 8, "durable-head-8")
	_, _, held := h.set.Head(towerA)
	require.False(t, held, "no local chain, no adoption")

	// The relinking Tower's cold start: revision 1 from genesis, accepted as ever.
	res, err := h.set.AcceptFull(towerA, h.towerPub(), h.inventory(invSpec{
		revision: 1, prevHash: "genesis", leaves: []map[string]any{h.offer(stationA, "offer-1", offerSpec{})}}))
	require.NoError(t, err, "a cold-start genesis snapshot must never be refused by an adopted head")
	require.Equal(t, int64(1), res.Revision)
}
