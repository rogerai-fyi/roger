package agent

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v6/internal/protocol"
)

// The receipt hash chain is documented as PER NODE. It was implemented with a single
// process-global lastHash, so one agent process serving two nodes interleaved their
// receipts into one chain - every link then points at a receipt from the other node,
// which destroys exactly the omission/reorder evidence the chain exists to provide.

func hexPub(p ed25519.PublicKey) string { return hex.EncodeToString(p) }

func TestChainsAreIndependentPerNode(t *testing.T) {
	resetChains()
	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	mk := func(nodeID, reqID string) protocol.UsageReceipt {
		rec := protocol.UsageReceipt{RequestID: reqID, NodeID: nodeID, PromptTokens: 1}
		chainSign(nodeID, &rec, priv)
		return rec
	}

	// Interleaved across two nodes in one process.
	a1 := mk("node-A", "a1")
	b1 := mk("node-B", "b1")
	a2 := mk("node-A", "a2")
	b2 := mk("node-B", "b2")

	require.Empty(t, a1.PrevHash, "node-A's first receipt opens its own chain")
	require.Empty(t, b1.PrevHash, "node-B's first receipt opens its own chain")

	require.Equal(t, a1.Hash(), a2.PrevHash, "node-A must chain to node-A")
	require.Equal(t, b1.Hash(), b2.PrevHash, "node-B must chain to node-B")

	require.NotEqual(t, b1.Hash(), a2.PrevHash, "node-A must not inherit node-B's hash")
	require.NotEqual(t, a1.Hash(), b2.PrevHash, "node-B must not inherit node-A's hash")
}

func TestChainSignProducesVerifiableReceipt(t *testing.T) {
	resetChains()
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	rec := protocol.UsageReceipt{RequestID: "r1", NodeID: "n1", PromptTokens: 5}
	chainSign("n1", &rec, priv)
	require.True(t, rec.VerifyNode(hexPub(pub)), "chainSign must leave a valid node signature")
}

func TestChainAdvancesMonotonically(t *testing.T) {
	resetChains()
	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	var prev string
	for i := 0; i < 5; i++ {
		rec := protocol.UsageReceipt{RequestID: string(rune('a' + i)), NodeID: "n1", PromptTokens: i + 1}
		chainSign("n1", &rec, priv)
		require.Equal(t, prev, rec.PrevHash, "each receipt links to its predecessor")
		prev = rec.Hash()
	}
}
