package inv

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/towerobj"
)

type deltaSpec struct {
	base     int64
	revision int64
	prevHash string
	issued   time.Duration
	expires  time.Duration
	ops      []any

	pre      func(map[string]any)
	post     func(map[string]any)
	rawPost  func([]byte) []byte
	signer   ed25519.PrivateKey
	unsigned bool
}

func (h *harness) delta(spec deltaSpec) []byte {
	h.t.Helper()
	if spec.expires == 0 {
		spec.expires = 30 * time.Minute
	}
	if spec.ops == nil {
		spec.ops = []any{}
	}
	m := map[string]any{
		"network":       PublicNetwork,
		"tower_id":      towerA,
		"base_revision": towerobj.FormatInt(spec.base),
		"revision":      towerobj.FormatInt(spec.revision),
		"prev_hash":     spec.prevHash,
		"issued":        h.unix(spec.issued),
		"expires":       h.unix(spec.expires),
		"ops":           spec.ops,
	}
	return h.finish(m, TypeDelta, spec.pre, spec.post, spec.rawPost, spec.signer, spec.unsigned)
}

func addOp(leaf map[string]any) any     { return map[string]any{"op": opAdd, "leaf": leaf} }
func replaceOp(leaf map[string]any) any { return map[string]any{"op": opReplace, "leaf": leaf} }
func removeOp(station, offer string) any {
	return map[string]any{"op": opRemove, "station_id": station, "offer_id": offer}
}

// fleet installs revision 40 with three leaves, which is enough to show an add, a replace,
// a removal and an untouched leaf all in one delta.
func (h *harness) fleet() (Result, map[string]any) {
	h.t.Helper()
	h.register("station-c")
	h.register("station-d")
	untouched := h.offer("station-d", "offer-untouched", offerSpec{})
	res, err := h.set.AcceptFull(towerA, h.towerPub(), h.inventory(invSpec{revision: 40, leaves: []map[string]any{
		h.offer(stationA, "offer-1", offerSpec{}),
		h.offer(stationB, "offer-2", offerSpec{}),
		untouched,
	}}))
	require.NoError(h.t, err)
	require.Equal(h.t, 3, res.Routable)
	return res, untouched
}

func (h *harness) routableByOffer() map[string]Leaf {
	out := map[string]Leaf{}
	for _, l := range h.set.Routable(towerA) {
		out[l.OfferID] = l
	}
	return out
}

func TestAValidDeltaAppliesOnlyToItsDeclaredBaseRevision(t *testing.T) {
	h := newHarness(t)
	base, untouched := h.fleet()

	repriced := h.offer(stationA, "offer-1", offerSpec{pre: func(m map[string]any) { m["price_in"] = "1500" }})
	added := h.offer("station-c", "offer-3", offerSpec{})

	res, err := h.set.AcceptDelta(towerA, h.towerPub(), h.delta(deltaSpec{
		base: 40, revision: 41, prevHash: base.Hash,
		ops: []any{replaceOp(repriced), removeOp(stationB, "offer-2"), addOp(added)},
	}))
	require.NoError(t, err)
	require.Equal(t, int64(41), res.Revision, "the result is exactly revision 41")
	require.False(t, res.Full)
	require.Equal(t, 3, res.Routable)

	got := h.routableByOffer()
	require.Len(t, got, 3)
	require.Equal(t, int64(1500), got["offer-1"].PriceIn)
	require.NotContains(t, got, "offer-2")
	require.Contains(t, got, "offer-3")

	// An unchanged leaf keeps the EXACT bytes its Station signed and the origin it came in
	// under. Re-deriving it would quietly substitute our encoding for the signed one.
	require.Equal(t, canon(t, untouched), got["offer-untouched"].Offer)
	require.Equal(t, towerA, got["offer-untouched"].TowerID)
}

func TestADeltaChainsFromItsOwnHash(t *testing.T) {
	h := newHarness(t)
	base, _ := h.fleet()

	first, err := h.set.AcceptDelta(towerA, h.towerPub(), h.delta(deltaSpec{base: 40, revision: 41, prevHash: base.Hash}))
	require.NoError(t, err)

	rev, hash, ok := h.set.Head(towerA)
	require.True(t, ok)
	require.Equal(t, int64(41), rev)
	require.Equal(t, first.Hash, hash)
	require.Equal(t, recordedHead{towerA, 41, first.Hash}, h.heads[len(h.heads)-1],
		"an accepted delta publishes its head like any other revision")

	_, err = h.set.AcceptDelta(towerA, h.towerPub(), h.delta(deltaSpec{base: 41, revision: 42, prevHash: first.Hash}))
	require.NoError(t, err)

	// And the previous head no longer chains: the sequence moved on.
	_, err = h.set.AcceptDelta(towerA, h.towerPub(), h.delta(deltaSpec{base: 41, revision: 42, prevHash: first.Hash}))
	require.ErrorIs(t, err, ErrResync)
}

func TestAnAmbiguousDeltaCausesResynchronization(t *testing.T) {
	// Every row here ends the same way: nothing is partially applied, and we ask for a full
	// snapshot rather than guessing where in the sequence we are. A wrong guess is a silent
	// divergence nobody notices until a grant names a Station that is not there.
	rows := []struct {
		condition string
		want      string
		spec      func(h *harness, base Result) deltaSpec
	}{
		{"base revision 39", "based on revision 39, not the accepted 40", func(h *harness, b Result) deltaSpec {
			return deltaSpec{base: 39, revision: 40, prevHash: b.Hash}
		}},
		{"base revision 41", "based on revision 41, not the accepted 40", func(h *harness, b Result) deltaSpec {
			return deltaSpec{base: 41, revision: 42, prevHash: b.Hash}
		}},
		{"target revision 40", "targets revision 40, not 41", func(h *harness, b Result) deltaSpec {
			return deltaSpec{base: 40, revision: 40, prevHash: b.Hash}
		}},
		{"target revision 42", "targets revision 42, not 41", func(h *harness, b Result) deltaSpec {
			return deltaSpec{base: 40, revision: 42, prevHash: b.Hash}
		}},
		{"removal of an unknown leaf", "not in the accepted revision", func(h *harness, b Result) deltaSpec {
			return deltaSpec{base: 40, revision: 41, prevHash: b.Hash, ops: []any{removeOp(stationA, "offer-nowhere")}}
		}},
		{"duplicate operation on one leaf", "more than one operation touches", func(h *harness, b Result) deltaSpec {
			return deltaSpec{base: 40, revision: 41, prevHash: b.Hash, ops: []any{
				removeOp(stationA, "offer-1"),
				replaceOp(h.offer(stationA, "offer-1", offerSpec{})),
			}}
		}},
		{"two removals of one leaf", "more than one operation touches", func(h *harness, b Result) deltaSpec {
			return deltaSpec{base: 40, revision: 41, prevHash: b.Hash, ops: []any{
				removeOp(stationA, "offer-1"), removeOp(stationA, "offer-1"),
			}}
		}},
		{"signature failure", "signature does not verify", func(h *harness, b Result) deltaSpec {
			return deltaSpec{base: 40, revision: 41, prevHash: b.Hash, signer: h.otherKey}
		}},
		{"truncated body", "", func(h *harness, b Result) deltaSpec {
			return deltaSpec{base: 40, revision: 41, prevHash: b.Hash,
				rawPost: func(raw []byte) []byte { return raw[:len(raw)/2] }}
		}},
		// Not in the spec table, but the same class: a delta with nothing to amend.
		{"a prev_hash that is not the head", "prev_hash is not the accepted head", func(h *harness, b Result) deltaSpec {
			return deltaSpec{base: 40, revision: 41, prevHash: "not-the-head"}
		}},
	}

	for _, row := range rows {
		t.Run(row.condition, func(t *testing.T) {
			h := newHarness(t)
			base, _ := h.fleet()
			before := h.routableByOffer()

			_, err := h.set.AcceptDelta(towerA, h.towerPub(), h.delta(row.spec(h, base)))
			require.ErrorIs(t, err, ErrResync, "condition %q must force a resync", row.condition)
			// The two sentinels have to be mutually exclusive or asking which one this is
			// gets yes for both. Found by the pre-push audit: openSigned used to hand back
			// an already-rejected error that the delta path then wrapped as a resync.
			require.NotErrorIs(t, err, ErrRejected,
				"condition %q must be a resync and NOT also a rejection", row.condition)
			if row.want != "" {
				require.Contains(t, err.Error(), row.want,
					"condition %q was caught by a different check than the one it names", row.condition)
			}

			rev, hash, _ := h.set.Head(towerA)
			require.Equal(t, int64(40), rev, "the delta must not be partially applied")
			require.Equal(t, base.Hash, hash)
			require.Equal(t, before, h.routableByOffer())
			require.Len(t, h.heads, 1)
		})
	}
}

func TestADeltaBeforeAnySnapshotAsksForOne(t *testing.T) {
	h := newHarness(t)
	_, err := h.set.AcceptDelta(towerA, h.towerPub(), h.delta(deltaSpec{base: 1, revision: 2, prevHash: "x"}))
	require.ErrorIs(t, err, ErrResync)
	require.Contains(t, err.Error(), "no accepted revision to amend")
}

func TestAddingALeafWeAlreadyHaveIsAmbiguous(t *testing.T) {
	h := newHarness(t)
	base, _ := h.fleet()
	_, err := h.set.AcceptDelta(towerA, h.towerPub(), h.delta(deltaSpec{
		base: 40, revision: 41, prevHash: base.Hash,
		ops: []any{addOp(h.offer(stationA, "offer-1", offerSpec{}))},
	}))
	require.ErrorIs(t, err, ErrResync)
	require.Contains(t, err.Error(), "already accepted")
}

func TestReplacingALeafWeDoNotHaveIsAmbiguous(t *testing.T) {
	h := newHarness(t)
	base, _ := h.fleet()
	_, err := h.set.AcceptDelta(towerA, h.towerPub(), h.delta(deltaSpec{
		base: 40, revision: 41, prevHash: base.Hash,
		ops: []any{replaceOp(h.offer("station-c", "offer-new", offerSpec{}))},
	}))
	require.ErrorIs(t, err, ErrResync)
	require.Contains(t, err.Error(), "is not accepted")
}

func TestARemovalMustNameTheStationThatOwnsTheOffer(t *testing.T) {
	// Naming the wrong Station means our two views of who owns that offer already differ,
	// and applying it would delete a leaf the operator did not mean to withdraw.
	h := newHarness(t)
	base, _ := h.fleet()
	_, err := h.set.AcceptDelta(towerA, h.towerPub(), h.delta(deltaSpec{
		base: 40, revision: 41, prevHash: base.Hash, ops: []any{removeOp(stationB, "offer-1")},
	}))
	require.ErrorIs(t, err, ErrResync)
	require.Contains(t, err.Error(), "belongs to")
}

func TestAnInadmissibleReplacementRetiresTheOfferItReplaced(t *testing.T) {
	// The operator has said the old offer is gone. Keeping it alive because its replacement
	// failed a check would route work at a price they have withdrawn.
	h := newHarness(t)
	base, _ := h.fleet()

	bad := h.offer(stationA, "offer-1", offerSpec{pre: func(m map[string]any) { m["capacity"] = "0" }})
	res, err := h.set.AcceptDelta(towerA, h.towerPub(), h.delta(deltaSpec{
		base: 40, revision: 41, prevHash: base.Hash, ops: []any{replaceOp(bad)},
	}))
	require.NoError(t, err)
	require.Len(t, res.Excluded, 1)
	require.Contains(t, res.Excluded[0].Reason, "capacity is not positive")
	require.NotContains(t, h.routableByOffer(), "offer-1")
	require.Equal(t, 2, res.Routable)
}

func TestADeltaCannotCarryAnotherTowersIdentity(t *testing.T) {
	// The one thing a resync does not forgive. Asking the sender to resend an inventory it
	// was never entitled to relay would be answering an attack with a retry.
	h := newHarness(t)
	base, _ := h.fleet()

	for _, tc := range []struct {
		name string
		pre  func(map[string]any)
	}{
		{"a foreign network", func(m map[string]any) { m["network"] = "roger-private" }},
		{"another Tower's ID", func(m map[string]any) { m["tower_id"] = towerB }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.set.AcceptDelta(towerA, h.towerPub(), h.delta(deltaSpec{
				base: 40, revision: 41, prevHash: base.Hash, pre: tc.pre,
			}))
			require.ErrorIs(t, err, ErrRejected)
			require.NotErrorIs(t, err, ErrResync)
		})
	}
}

func TestADeltaOutsideItsTimeWindowIsRejectedNotResynced(t *testing.T) {
	// A Tower that dated its own object wrong does not need our history; resending it would
	// just repeat the mistake.
	h := newHarness(t)
	base, _ := h.fleet()
	for _, tc := range []struct {
		name string
		spec deltaSpec
	}{
		{"already expired", deltaSpec{base: 40, revision: 41, prevHash: base.Hash, expires: -time.Minute}},
		{"issued in the future", deltaSpec{base: 40, revision: 41, prevHash: base.Hash, issued: 10 * time.Minute}},
		{"expiring beyond the lease", deltaSpec{base: 40, revision: 41, prevHash: base.Hash, expires: 2 * time.Hour}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.set.AcceptDelta(towerA, h.towerPub(), h.delta(tc.spec))
			require.ErrorIs(t, err, ErrRejected)
		})
	}
}

func TestAnUnknownOperationIsRejectedNotResynced(t *testing.T) {
	// A shape we understand naming an operation we do not implement is a version mismatch.
	// Resending the same thing would not help either side.
	h := newHarness(t)
	base, _ := h.fleet()
	_, err := h.set.AcceptDelta(towerA, h.towerPub(), h.delta(deltaSpec{
		base: 40, revision: 41, prevHash: base.Hash,
		ops: []any{map[string]any{"op": "upsert", "leaf": h.offer(stationA, "offer-9", offerSpec{})}},
	}))
	require.ErrorIs(t, err, ErrRejected)
	require.Contains(t, err.Error(), "unknown operation")
}

func TestMalformedOperationsForceAResync(t *testing.T) {
	h := newHarness(t)
	base, _ := h.fleet()
	for _, tc := range []struct {
		name string
		op   any
	}{
		{"not an object", "remove offer-1"},
		{"no op member", map[string]any{"offer_id": "offer-1"}},
		{"an unknown member on a removal", map[string]any{"op": opRemove, "station_id": stationA, "offer_id": "offer-1", "why": "x"}},
		{"an unknown member on an add", map[string]any{"op": opAdd, "leaf": map[string]any{}, "why": "x"}},
		{"a leaf that is not an object", map[string]any{"op": opAdd, "leaf": "nope"}},
		{"a removal with no offer ID", map[string]any{"op": opRemove, "station_id": stationA}},
		{"an add whose leaf has no IDs", map[string]any{"op": opAdd, "leaf": map[string]any{"model": "roger-1"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.set.AcceptDelta(towerA, h.towerPub(), h.delta(deltaSpec{
				base: 40, revision: 41, prevHash: base.Hash, ops: []any{tc.op},
			}))
			require.ErrorIs(t, err, ErrResync)
			require.NotErrorIs(t, err, ErrRejected)
		})
	}
}

func TestADeltaWithAMalformedSequenceForcesAResync(t *testing.T) {
	h := newHarness(t)
	base, _ := h.fleet()
	for _, tc := range []struct {
		name string
		pre  func(map[string]any)
	}{
		{"a base revision that is not an integer", func(m map[string]any) { m["base_revision"] = "forty" }},
		{"a zero revision", func(m map[string]any) { m["revision"] = "0" }},
		{"a missing prev_hash", func(m map[string]any) { delete(m, "prev_hash") }},
		{"a missing ops array", func(m map[string]any) { delete(m, "ops") }},
		{"an unknown member", func(m map[string]any) { m["note"] = "hello" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.set.AcceptDelta(towerA, h.towerPub(), h.delta(deltaSpec{
				base: 40, revision: 41, prevHash: base.Hash, pre: tc.pre,
			}))
			require.ErrorIs(t, err, ErrResync)
			require.NotErrorIs(t, err, ErrRejected)
		})
	}
}

func TestTheCeilingsApplyToTheResultNotTheAmendment(t *testing.T) {
	// Otherwise a Tower grows past any limit one small delta at a time.
	h := newHarness(t)
	base, _ := h.fleet()
	h.set.cfg.MaxLeaves = 3

	_, err := h.set.AcceptDelta(towerA, h.towerPub(), h.delta(deltaSpec{
		base: 40, revision: 41, prevHash: base.Hash,
		ops: []any{addOp(h.offer("station-c", "offer-3", offerSpec{}))},
	}))
	require.ErrorIs(t, err, ErrRejected)
	require.Contains(t, err.Error(), "above the ceiling")
	require.Equal(t, 3, len(h.routableByOffer()), "a rejected delta changes nothing")
}

func TestADeltaCannotSmuggleInADuplicateStation(t *testing.T) {
	h := newHarness(t)
	base, _ := h.fleet()
	dup := h.offer(stationA, "offer-dup", offerSpec{})
	_, err := h.set.AcceptDelta(towerA, h.towerPub(), h.delta(deltaSpec{
		base: 40, revision: 41, prevHash: base.Hash, ops: []any{addOp(dup)},
	}))
	require.ErrorIs(t, err, ErrRejected)
	require.Contains(t, err.Error(), "would appear twice")
}

func TestADeltaCannotSmuggleInTooManyCapabilities(t *testing.T) {
	h := newHarness(t)
	base, _ := h.fleet()
	wide := h.offer("station-c", "offer-3", offerSpec{pre: func(m map[string]any) {
		m["capabilities"] = []any{"vision", "tools", "audio"}
	}})
	_, err := h.set.AcceptDelta(towerA, h.towerPub(), h.delta(deltaSpec{
		base: 40, revision: 41, prevHash: base.Hash, ops: []any{addOp(wide)},
	}))
	require.ErrorIs(t, err, ErrRejected)
	require.Contains(t, err.Error(), "distinct capabilities")
}

func TestADeltaRefreshesTheExpiryWindow(t *testing.T) {
	// Push-on-change is the whole point: an operator whose fleet changes should not also
	// have to resend a full snapshot to stay in routing.
	h := newHarness(t)
	// Long-lived offers, so this test measures the INVENTORY's window and not the leaves'.
	// The two are independent deadlines and conflating them was how the first version of
	// this test passed for the wrong reason.
	longLived := offerSpec{pre: func(m map[string]any) { m["expires"] = h.unix(3 * time.Hour) }}
	base, err := h.set.AcceptFull(towerA, h.towerPub(), h.inventory(invSpec{revision: 40, leaves: []map[string]any{
		h.offer(stationA, "offer-1", longLived),
	}}))
	require.NoError(t, err)

	h.now = h.now.Add(20 * time.Minute)
	_, err = h.set.AcceptDelta(towerA, h.towerPub(), h.delta(deltaSpec{
		base: 40, revision: 41, prevHash: base.Hash, expires: 30 * time.Minute,
	}))
	require.NoError(t, err)

	h.now = h.now.Add(20 * time.Minute) // past the original expiry, inside the new one
	require.Len(t, h.set.Routable(towerA), 1)
}
