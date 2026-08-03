package protocol

import (
	"crypto/ed25519"
	"testing"

	"github.com/stretchr/testify/require"
)

// These tests pin the E3 repair. A valid node signature proves only that the node
// signed those bytes; it does not prove the receipt describes the request the broker
// dispatched. Settlement claims the hold keyed on the receipt's own RequestID, so a
// receipt naming a foreign, empty, or already-used request id makes the broker clear
// the wrong hold row - the real hold is never captured and is later swept back to the
// payer. That is served inference nobody paid for.

func TestBindsToAcceptsTheDispatchedJob(t *testing.T) {
	rec := UsageReceipt{RequestID: "req-A", NodeID: "node-1"}
	require.True(t, rec.BindsTo("req-A", "node-1"))
}

func TestBindsToRejectsEveryMismatch(t *testing.T) {
	for _, tc := range []struct {
		name string
		rec  UsageReceipt
	}{
		{"foreign request", UsageReceipt{RequestID: "req-B", NodeID: "node-1"}},
		{"empty request", UsageReceipt{RequestID: "", NodeID: "node-1"}},
		{"foreign node", UsageReceipt{RequestID: "req-A", NodeID: "node-2"}},
		{"empty node", UsageReceipt{RequestID: "req-A", NodeID: ""}},
		{"both empty", UsageReceipt{}},
		{"both foreign", UsageReceipt{RequestID: "req-B", NodeID: "node-2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.False(t, tc.rec.BindsTo("req-A", "node-1"),
				"a receipt with %s must not bind to the dispatched job", tc.name)
		})
	}
}

// The broker must never accept an empty authoritative id either - that would make
// every empty-RequestID receipt bind and reintroduce the hole from the other side.
func TestBindsToRejectsEmptyAuthoritativeIdentity(t *testing.T) {
	rec := UsageReceipt{RequestID: "", NodeID: ""}
	require.False(t, rec.BindsTo("", ""), "an empty dispatched identity must never bind")
	require.False(t, rec.BindsTo("req-A", ""), "an empty dispatched node must never bind")
	require.False(t, rec.BindsTo("", "node-1"), "an empty dispatched request must never bind")
}

// A replayed receipt is signature-valid but names an earlier request, so binding is
// what stops it - there is no nonce in the receipt to rely on.
func TestReplayedReceiptDoesNotBindToNewRequest(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	old := UsageReceipt{RequestID: "req-OLD", NodeID: "node-1", PromptTokens: 10}
	old.SignNode(priv)

	// Same bytes, still a perfectly valid signature - replayed against a new job.
	require.False(t, old.BindsTo("req-NEW", "node-1"),
		"a replayed receipt must not bind to a newly dispatched request")
}

// Binding is independent of signature validity: both gates must be applied, and
// neither substitutes for the other.
func TestBindingIsIndependentOfSignature(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	rec := UsageReceipt{RequestID: "req-A", NodeID: "node-1"}
	rec.SignNode(priv)

	require.True(t, rec.VerifyNode(hexKey(pub)))
	require.True(t, rec.BindsTo("req-A", "node-1"))

	// A signed receipt for the wrong job: signature good, binding bad.
	wrong := UsageReceipt{RequestID: "req-B", NodeID: "node-1"}
	wrong.SignNode(priv)
	require.True(t, wrong.VerifyNode(hexKey(pub)), "signature is still valid")
	require.False(t, wrong.BindsTo("req-A", "node-1"), "but it does not bind to this job")
}
