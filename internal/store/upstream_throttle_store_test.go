package store

// upstream_throttle_store_test.go covers the store side of the upstream-throttle void: the
// receipt the broker keeps for a request the PROVIDER refused (429), what settlement records
// for it, and the per-node count the recourse path reads back.
//
// The rule under test is that a 429 upstream is capacity, not misconduct: nothing was consumed
// upstream, so the receipt bills ZERO tokens whatever the station claimed, and the void survives
// on the receipt itself (there is no strike row to carry it) so ThrottledCount can find it.

import (
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/protocol"
)

// throttledRec is a $0 voided receipt: the station still claims tokens, and the broker still
// must record none.
func throttledRec(id, node string, ts int64) protocol.UsageReceipt {
	return protocol.UsageReceipt{
		RequestID: id, NodeID: node, User: "w", Model: "m",
		PromptTokens: 900, CompletionTokens: 700,
		TS: ts, VoidReason: protocol.VoidUpstreamThrottled, UpstreamStatus: 429,
	}
}

func TestThrottledCountParity(t *testing.T) {
	const now = int64(1_800_000_000)
	for name, db := range parityStores(t) {
		db := db
		t.Run(name, func(t *testing.T) {
			_, err := db.AddCredits("w", 10)
			require.NoError(t, err)

			// A voided request settles at $0 against the serving node.
			_, err = db.Settle("w", "n1", 0, 0, throttledRec("req-void-1", "n1", now))
			require.NoError(t, err)
			// An older void, outside the window the caller asks about.
			_, err = db.Settle("w", "n1", 0, 0, throttledRec("req-void-old", "n1", now-7200))
			require.NoError(t, err)
			// Another node's void must not be attributed to n1.
			_, err = db.Settle("w", "n2", 0, 0, throttledRec("req-void-other", "n2", now))
			require.NoError(t, err)
			// A NORMAL settled request from n1 is not a throttle, however it is billed.
			_, err = db.Settle("w", "n1", 1, 0.5, protocol.UsageReceipt{
				RequestID: "req-ok", NodeID: "n1", User: "w", Model: "m",
				PromptTokens: 10, CompletionTokens: 4, TS: now,
			})
			require.NoError(t, err)

			n, err := db.ThrottledCount("n1", now-3600)
			require.NoError(t, err)
			require.Equal(t, 1, n, "only n1's void inside the window counts")

			n, err = db.ThrottledCount("n1", now-86400)
			require.NoError(t, err)
			require.Equal(t, 2, n, "a wider window picks up the older void too")

			n, err = db.ThrottledCount("n2", now-3600)
			require.NoError(t, err)
			require.Equal(t, 1, n, "the count is per node")

			n, err = db.ThrottledCount("n-never-served", now-3600)
			require.NoError(t, err)
			require.Zero(t, n, "a node with no receipts counts zero, not an error")

			n, err = db.ThrottledCount("n1", now+1)
			require.NoError(t, err)
			require.Zero(t, n, "since is inclusive of ts, so a window starting after it is empty")

			// The claim is NOT recorded: the provider refused, so zero tokens were consumed.
			entries, err := db.EntriesByUser("w", now-1, now+1)
			require.NoError(t, err)
			var void, ok bool
			for _, e := range entries {
				switch e.RequestID {
				case "req-void-1":
					void = true
					require.Zero(t, e.PromptTokens, "an upstream-throttled void bills no prompt tokens")
					require.Zero(t, e.CompletionTokens, "an upstream-throttled void bills no completion tokens")
					require.Zero(t, e.Cost)
				case "req-ok":
					ok = true
					require.Equal(t, 10, e.PromptTokens, "a normal settle still records its counts")
					require.Equal(t, 4, e.CompletionTokens)
				}
			}
			require.True(t, void, "the voided request is still metered as a receipt")
			require.True(t, ok)
		})
	}
}

// ReceiptOf is Mem-only (the in-memory twin of the Postgres receipts.receipt column), so the
// retention it reads back is pinned directly: every settlement path must keep the receipt, or
// the void's audit fields are lost on this backend.
func TestMemRetainsReceiptOnEverySettlementPath(t *testing.T) {
	const now = int64(1_800_000_000)
	m := NewMem()
	_, err := m.AddCredits("w", 50)
	require.NoError(t, err)

	_, err = m.Settle("w", "n1", 0, 0, throttledRec("req-settle", "n1", now))
	require.NoError(t, err)

	ok, err := m.HoldFor("w", "req-final", 2)
	require.NoError(t, err)
	require.True(t, ok)
	_, err = m.Finalize("w", "n1", 2, 0, 0, throttledRec("req-final", "n1", now))
	require.NoError(t, err)

	ok, err = m.HoldFor("w", "req-edge", 2)
	require.NoError(t, err)
	require.True(t, ok)
	_, err = m.SettleEdge("w", "n1", "acct-s", "t1", "acct-t", 0, 0, 0, false, throttledRec("req-edge", "n1", now))
	require.NoError(t, err)

	for _, id := range []string{"req-settle", "req-final", "req-edge"} {
		rec, found := m.ReceiptOf(id)
		require.True(t, found, "%s: the settled receipt must be retained", id)
		require.Equal(t, protocol.VoidUpstreamThrottled, rec.VoidReason, "%s: the void reason must survive", id)
		require.Equal(t, 429, rec.UpstreamStatus, "%s: the upstream status must survive", id)
		require.Equal(t, 900, rec.PromptTokens, "the retained receipt is the STATION's claim, unedited")
	}
	_, found := m.ReceiptOf("req-never")
	require.False(t, found, "an unknown request id retains nothing")
	_, found = m.ReceiptOf("")
	require.False(t, found, "an empty request id carries no idempotency key, so nothing is retained under it")

	// All three paths served n1 and were voided, so all three count.
	n, err := m.ThrottledCount("n1", now)
	require.NoError(t, err)
	require.Equal(t, 3, n)
}
