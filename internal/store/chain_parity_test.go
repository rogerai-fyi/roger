package store

// chain_parity_test.go runs the receipt-chain DETECT-AND-RECORD contract against BOTH
// backends. chain_test.go pins the contract on Mem in fine-grained cases; this drives the
// SAME rules through parityStores so the Postgres implementation (a row-locked read-then-
// upsert, a different mechanism entirely) is held to them too rather than trusted.
//
// node_chain is deliberately NOT in freshPostgres' TRUNCATE list (it is audit state, not
// money), so every node id here carries a per-run unique suffix: a leftover row from an
// earlier run must never make a "first sighting" assertion pass or fail by accident.

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestChainContractParity(t *testing.T) {
	run := fmt.Sprintf("%d", time.Now().UnixNano())
	for name, db := range parityStores(t) {
		db := db
		t.Run(name, func(t *testing.T) {
			nodeA := "chain-A-" + run
			nodeB := "chain-B-" + run

			// An unseen node has no head and a zero status - no row, not an error.
			head, err := db.ChainHead("chain-never-" + run)
			require.NoError(t, err)
			require.Empty(t, head)
			st, err := db.ChainStatus("chain-never-" + run)
			require.NoError(t, err)
			require.Equal(t, ChainStatus{}, st, "a node the broker has never seen reports zero, not an error")

			// FIRST SIGHTING: a prevHash the broker never tracked is a baseline, not a break.
			res, err := db.AdvanceChain(nodeA, "some-prior-hash-from-before-we-tracked", "h1")
			require.NoError(t, err)
			require.True(t, res.Continuous, "the first receipt from a node cannot break a chain nobody was tracking")
			require.Empty(t, res.Expected)
			require.Equal(t, "h1", res.Head)
			head, err = db.ChainHead(nodeA)
			require.NoError(t, err)
			require.Equal(t, "h1", head)
			st, err = db.ChainStatus(nodeA)
			require.NoError(t, err)
			require.Equal(t, "h1", st.Head)
			require.Zero(t, st.Breaks, "no break may be counted for a first sighting")
			require.NotZero(t, st.CheckedAt, "a sighting stamps the check time")

			// REPLAY: re-applying the receipt that already set this head is idempotent.
			res, err = db.AdvanceChain(nodeA, "", "h1")
			require.NoError(t, err)
			require.True(t, res.Continuous, "re-applying the same receipt must not manufacture a break")
			require.Equal(t, "h1", res.Head)
			st, err = db.ChainStatus(nodeA)
			require.NoError(t, err)
			require.Zero(t, st.Breaks)
			require.NotZero(t, st.CheckedAt, "a replay is still a check and must stamp the time")

			// CONTINUOUS: prevHash matches the recorded head, so the chain advances clean.
			res, err = db.AdvanceChain(nodeA, "h1", "h2")
			require.NoError(t, err)
			require.True(t, res.Continuous)
			require.Equal(t, "h2", res.Head)
			st, err = db.ChainStatus(nodeA)
			require.NoError(t, err)
			require.Zero(t, st.Breaks)

			// PER-NODE: node B's chain is its own; A's traffic never advances it.
			res, err = db.AdvanceChain(nodeB, "", "b1")
			require.NoError(t, err)
			require.True(t, res.Continuous)
			_, err = db.AdvanceChain(nodeA, "h2", "h3")
			require.NoError(t, err)
			head, err = db.ChainHead(nodeB)
			require.NoError(t, err)
			require.Equal(t, "b1", head, "another node's receipt must not advance this one")

			// BREAK: the supplied prevHash is not the head. Reported, counted, and STILL
			// advanced so one break is not re-reported on every later receipt.
			res, err = db.AdvanceChain(nodeA, "wrong", "h4")
			require.NoError(t, err)
			require.False(t, res.Continuous, "a chain that does not continue must be reported broken")
			require.Equal(t, "h3", res.Expected, "the break must name the head the broker held")
			require.Equal(t, "h4", res.Head, "a break still advances, so it is not reported forever")
			st, err = db.ChainStatus(nodeA)
			require.NoError(t, err)
			require.EqualValues(t, 1, st.Breaks, "exactly one break is recorded")

			// The next in-sequence receipt is continuous again: one break, not a stuck state.
			res, err = db.AdvanceChain(nodeA, "h4", "h5")
			require.NoError(t, err)
			require.True(t, res.Continuous, "exactly one break, not a permanent broken state")
			st, err = db.ChainStatus(nodeA)
			require.NoError(t, err)
			require.EqualValues(t, 1, st.Breaks, "a continuous receipt adds no break")
			require.Equal(t, "h5", st.Head)

			// A second, independent break increments rather than replacing the count.
			_, err = db.AdvanceChain(nodeA, "also-wrong", "h6")
			require.NoError(t, err)
			st, err = db.ChainStatus(nodeA)
			require.NoError(t, err)
			require.EqualValues(t, 2, st.Breaks, "breaks accumulate")
		})
	}
}
