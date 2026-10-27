package store

// relay_request_parity_test.go: a settled attempt keeps the request id it belongs to beside
// its own (attempt) id (contract §14.B7 #13, founder ruling 2026-10-05). The consumer's ledger
// rows carry it so a receipt ties back to X-RogerAI-Request-Id; the station owner's rows never
// do, because two owners comparing request ids would re-link the attempts the per-attempt id
// keeps apart. Both backends.

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/protocol"
)

func TestRelayRequestIDParity(t *testing.T) {
	for name, s := range parityStores(t) {
		t.Run(name, func(t *testing.T) {
			pfx := fmt.Sprintf("rrid-%s-", name)
			user, node := pfx+"u1", pfx+"n1"
			_, err := s.BalanceOf(user, 10)
			require.NoError(t, err)
			ts := time.Now().Unix()

			// A settled attempt (Finalize, the paid path) and a $0 metering attempt (Settle).
			paid := protocol.UsageReceipt{RequestID: pfx + "att-1", NodeID: node, Model: "m", PromptTokens: 10, CompletionTokens: 5, TS: ts, RelayRequestID: pfx + "R1"}
			ok, err := s.HoldFor(user, paid.RequestID, 1)
			require.NoError(t, err)
			require.True(t, ok)
			_, err = s.Finalize(user, node, 1, 0.5, 0.35, paid)
			require.NoError(t, err)
			free := protocol.UsageReceipt{RequestID: pfx + "att-2", NodeID: node, Model: "m", TS: ts + 1, RelayRequestID: pfx + "R2"}
			_, err = s.Settle(user, node, 0, 0, free)
			require.NoError(t, err)

			mine, err := s.RecentByUser(user, 10)
			require.NoError(t, err)
			got := map[string]string{}
			for _, e := range mine {
				got[e.RequestID] = e.RelayRequestID
			}
			require.Equal(t, map[string]string{pfx + "att-1": pfx + "R1", pfx + "att-2": pfx + "R2"}, got,
				"the consumer's rows carry the request id beside each attempt id")

			owner, err := s.RecentByNode(node, 10)
			require.NoError(t, err)
			require.Len(t, owner, 2)
			for _, e := range owner {
				require.Empty(t, e.RelayRequestID, "an owner's row never carries the request id (%s)", e.RequestID)
			}
		})
	}
}
