package store

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The per-node receipt chain only proves something if the broker holds the head and
// checks it. These pin the DETECT-AND-RECORD contract: AdvanceChain always advances so
// one break is not reported forever, and it reports continuity rather than refusing.

func TestChainOpensOnFirstReceipt(t *testing.T) {
	m := NewMem()
	res, err := m.AdvanceChain("n1", "", "h1")
	require.NoError(t, err)
	require.True(t, res.Continuous, "a node's first receipt opens its chain")
	require.Equal(t, "h1", res.Head)

	head, err := m.ChainHead("n1")
	require.NoError(t, err)
	require.Equal(t, "h1", head)
}

func TestChainAdvancesWhenContinuous(t *testing.T) {
	m := NewMem()
	_, err := m.AdvanceChain("n1", "", "h1")
	require.NoError(t, err)

	res, err := m.AdvanceChain("n1", "h1", "h2")
	require.NoError(t, err)
	require.True(t, res.Continuous)
	require.Equal(t, "h2", res.Head)
}

func TestChainsArePerNode(t *testing.T) {
	m := NewMem()
	_, err := m.AdvanceChain("nA", "", "a1")
	require.NoError(t, err)
	_, err = m.AdvanceChain("nB", "", "b1")
	require.NoError(t, err)

	resA, err := m.AdvanceChain("nA", "a1", "a2")
	require.NoError(t, err)
	require.True(t, resA.Continuous, "node A must continue from node A's head")

	headB, err := m.ChainHead("nB")
	require.NoError(t, err)
	require.Equal(t, "b1", headB, "node A's receipt must not advance node B")
}

func TestChainBreakIsDetectedAndReported(t *testing.T) {
	for _, tc := range []struct{ name, supplied string }{
		{"restarted node", ""},
		{"older receipt", "h0"},
		{"never seen", "unknown-hash"},
		{"wrong hash", "0000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewMem()
			_, err := m.AdvanceChain("n1", "", "h1")
			require.NoError(t, err)

			res, err := m.AdvanceChain("n1", tc.supplied, "h2")
			require.NoError(t, err)
			require.False(t, res.Continuous, "a chain that does not continue must be reported broken")
			require.Equal(t, "h1", res.Expected, "the break must name the head the broker held")
			require.Equal(t, "h2", res.Head, "a break still advances, so it is not reported forever")
		})
	}
}

func TestBreakAdvancesSoNextReceiptIsContinuous(t *testing.T) {
	m := NewMem()
	_, err := m.AdvanceChain("n1", "", "h1")
	require.NoError(t, err)

	broke, err := m.AdvanceChain("n1", "wrong", "h2")
	require.NoError(t, err)
	require.False(t, broke.Continuous)

	next, err := m.AdvanceChain("n1", "h2", "h3")
	require.NoError(t, err)
	require.True(t, next.Continuous, "exactly one break, not a permanent broken state")
}

// An omission: the node skips presenting R2, so R3 arrives claiming R2's hash while
// the broker's head is still R1's. That must read as a break, not be accepted.
func TestOmissionIsVisibleAsABreak(t *testing.T) {
	m := NewMem()
	_, err := m.AdvanceChain("n1", "", "r1")
	require.NoError(t, err)

	res, err := m.AdvanceChain("n1", "r2", "r3") // r2 never settled
	require.NoError(t, err)
	require.False(t, res.Continuous, "a skipped receipt must surface as a break")
	require.Equal(t, "r1", res.Expected)
}

// Settlement retries. Re-applying the identical receipt must not look like a break.
func TestReapplyingSameReceiptIsIdempotent(t *testing.T) {
	m := NewMem()
	_, err := m.AdvanceChain("n1", "", "h1")
	require.NoError(t, err)

	again, err := m.AdvanceChain("n1", "", "h1")
	require.NoError(t, err)
	require.True(t, again.Continuous, "re-applying the same receipt must not manufacture a break")
	require.Equal(t, "h1", again.Head)

	head, err := m.ChainHead("n1")
	require.NoError(t, err)
	require.Equal(t, "h1", head, "the head is unchanged by a replay")
}

func TestChainHeadOfUnknownNodeIsEmpty(t *testing.T) {
	m := NewMem()
	head, err := m.ChainHead("nobody")
	require.NoError(t, err)
	require.Empty(t, head)
}

// A node's FIRST receipt establishes the baseline. Without this, every node that already
// had an in-process chain before head tracking shipped would be counted as broken on its
// very first settled receipt - which is precisely what the detect-and-record design says
// must not happen.
func TestFirstSightingIsABaselineNotABreak(t *testing.T) {
	m := NewMem()
	res, err := m.AdvanceChain("n1", "some-prior-hash-from-before-we-tracked", "h1")
	require.NoError(t, err)
	require.True(t, res.Continuous, "the first receipt from a node cannot break a chain nobody was tracking")
	require.Empty(t, res.Expected)

	st, err := m.ChainStatus("n1")
	require.NoError(t, err)
	require.Zero(t, st.Breaks, "no break may be counted for a first sighting")

	// From here on the chain IS tracked, so a mismatch is a real break.
	res, err = m.AdvanceChain("n1", "wrong", "h2")
	require.NoError(t, err)
	require.False(t, res.Continuous)
}

// The two backends must agree on ChainStatus after a replay.
func TestReplayStampsTheCheckTime(t *testing.T) {
	m := NewMem()
	_, err := m.AdvanceChain("n1", "", "h1")
	require.NoError(t, err)
	before, err := m.ChainStatus("n1")
	require.NoError(t, err)
	require.NotZero(t, before.CheckedAt)

	_, err = m.AdvanceChain("n1", "", "h1") // replay
	require.NoError(t, err)
	after, err := m.ChainStatus("n1")
	require.NoError(t, err)
	require.NotZero(t, after.CheckedAt, "a replay is still a check and must stamp the time")
}
