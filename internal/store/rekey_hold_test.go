package store

// rekey_hold_test.go covers RekeyHold on BOTH backends via parityStores (Mem always, the real
// Postgres when ROGERAI_TEST_DATABASE_URL is set). RekeyHold is the upstream-failover hold
// move (features/routing/upstream_failover.feature): the ONE pre-auth hold follows the attempt
// that settles, so Finalize under the new id captures it and a ReleaseHoldFor under the old id
// finds nothing - never a double refund.

import (
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/protocol"
)

func TestRekeyHoldParity(t *testing.T) {
	for name, db := range parityStores(t) {
		db := db
		t.Run(name, func(t *testing.T) {
			_, err := db.AddCredits("w", 10)
			require.NoError(t, err)
			ok, err := db.HoldFor("w", "req", 1.5)
			require.NoError(t, err)
			require.True(t, ok)

			// Moved: the old id releases nothing, the new id finalizes the reservation.
			require.NoError(t, db.RekeyHold("w", "req", "req-2"))
			bal, err := db.ReleaseHoldFor("w", "req")
			require.NoError(t, err)
			require.InDelta(t, 8.5, bal, 1e-9, "the old id must no longer own the hold")
			bal, err = db.Finalize("w", "n", 1.5, 0.5, 0.35, protocol.UsageReceipt{RequestID: "req-2", NodeID: "n", User: "w", Model: "m"})
			require.NoError(t, err)
			require.InDelta(t, 9.5, bal, 1e-9, "finalize under the new id captures the moved hold")
			// The row is gone: a later release under either id is a no-op.
			bal, err = db.ReleaseHoldFor("w", "req-2")
			require.NoError(t, err)
			require.InDelta(t, 9.5, bal, 1e-9)

			// A missing source row (swept/captured already) or a wrong payer REFUSES the move
			// (ErrNoPendingHold: the relay must not fail over onto a reservation that is gone);
			// from==to is a no-op.
			require.ErrorIs(t, db.RekeyHold("w", "absent", "x"), ErrNoPendingHold)
			ok, err = db.HoldFor("w", "req3", 1)
			require.NoError(t, err)
			require.True(t, ok)
			require.ErrorIs(t, db.RekeyHold("someone-else", "req3", "stolen"), ErrNoPendingHold)
			require.NoError(t, db.RekeyHold("w", "req3", "req3"))
			bal, err = db.ReleaseHoldFor("w", "req3")
			require.NoError(t, err)
			require.InDelta(t, 9.5, bal, 1e-9, "the hold stayed under req3 for its payer")
		})
	}
}
