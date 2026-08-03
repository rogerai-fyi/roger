package protocol

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
)

func hexKey(pub ed25519.PublicKey) string { return hex.EncodeToString(pub) }

// These tests pin the E4 repair: the broker's counter-signature must cover the
// broker-SET fields that decide the bill (BrokerPromptTokens, BrokerCompletionTokens)
// and the grant attribution (GrantID), while the node's signature must keep covering
// only the fields the node authored.
//
// Before the repair, SignBroker signed the same canonical bytes as SignNode - with the
// broker fields zeroed - so the numbers billedTokens() uses to charge a consumer and
// credit a provider were covered by NO signature at all.

func signedPair(t *testing.T) (UsageReceipt, ed25519.PublicKey, ed25519.PublicKey) {
	t.Helper()
	nodePub, nodePriv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	brokerPub, brokerPriv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	rec := UsageReceipt{
		RequestID:        "req-1",
		NodeID:           "node-1",
		User:             "alice",
		Model:            "m",
		PromptTokens:     1000,
		CompletionTokens: 2000,
		PriceIn:          1,
		PriceOut:         2,
		TS:               1,
		PrevHash:         "prev",
	}
	// The node signs what it authored.
	rec.SignNode(nodePriv)
	// The broker then sets its own re-counts and the grant tag, and counter-signs.
	rec.BrokerPromptTokens = 900
	rec.BrokerCompletionTokens = 1800
	rec.GrantID = "grant-1"
	rec.SignBroker(brokerPriv)
	return rec, nodePub, brokerPub
}

// TestNodeSigSurvivesBrokerFields is the invariant that forced the original design:
// the node signs before the broker fields exist, so setting them must not break it.
func TestNodeSigSurvivesBrokerFields(t *testing.T) {
	rec, nodePub, _ := signedPair(t)
	require.True(t, rec.VerifyNode(hexKey(nodePub)), "node sig must survive broker-set fields")
}

// TestReceiptHashIgnoresBrokerFields keeps the per-node chain stable: PrevHash is
// computed by the node before the broker fields exist.
func TestReceiptHashIgnoresBrokerFields(t *testing.T) {
	rec, _, _ := signedPair(t)
	bare := rec
	bare.BrokerPromptTokens, bare.BrokerCompletionTokens, bare.GrantID = 0, 0, ""
	require.Equal(t, bare.Hash(), rec.Hash(), "chain hash must not depend on broker-set fields")
}

// TestBrokerSigVerifies is the missing capability: production had no VerifyBroker at all.
func TestBrokerSigVerifies(t *testing.T) {
	rec, _, brokerPub := signedPair(t)
	require.True(t, rec.VerifyBroker(hexKey(brokerPub)), "broker sig must verify")
}

// TestBrokerSigCoversBilledCounts is the core E4 regression. Altering the broker's own
// re-count changes what billedTokens() charges, so it MUST break the broker signature.
func TestBrokerSigCoversBilledCounts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		muton func(*UsageReceipt)
	}{
		{"BrokerPromptTokens", func(r *UsageReceipt) { r.BrokerPromptTokens = 10 }},
		{"BrokerCompletionTokens", func(r *UsageReceipt) { r.BrokerCompletionTokens = 10 }},
		{"GrantID", func(r *UsageReceipt) { r.GrantID = "other-grant" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, _, brokerPub := signedPair(t)
			require.True(t, rec.VerifyBroker(hexKey(brokerPub)))
			tc.muton(&rec)
			require.False(t, rec.VerifyBroker(hexKey(brokerPub)),
				"tampering with %s must invalidate the broker signature", tc.name)
		})
	}
}

// TestBrokerSigCoversNodeFields: the broker form is a superset, so node-authored
// tampering must break the broker signature too.
func TestBrokerSigCoversNodeFields(t *testing.T) {
	for _, tc := range []struct {
		name  string
		muton func(*UsageReceipt)
	}{
		{"PromptTokens", func(r *UsageReceipt) { r.PromptTokens = 1 }},
		{"CompletionTokens", func(r *UsageReceipt) { r.CompletionTokens = 1 }},
		{"PriceIn", func(r *UsageReceipt) { r.PriceIn = 99 }},
		{"PriceOut", func(r *UsageReceipt) { r.PriceOut = 99 }},
		{"RequestID", func(r *UsageReceipt) { r.RequestID = "other" }},
		{"NodeID", func(r *UsageReceipt) { r.NodeID = "other" }},
		{"PrevHash", func(r *UsageReceipt) { r.PrevHash = "other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, nodePub, brokerPub := signedPair(t)
			tc.muton(&rec)
			require.False(t, rec.VerifyNode(hexKey(nodePub)), "node sig must break on %s", tc.name)
			require.False(t, rec.VerifyBroker(hexKey(brokerPub)), "broker sig must break on %s", tc.name)
		})
	}
}

// TestVerifyBrokerRejectsMissingOrWrongKey covers the boundary cases.
func TestVerifyBrokerRejectsMissingOrWrongKey(t *testing.T) {
	rec, nodePub, brokerPub := signedPair(t)
	require.True(t, rec.VerifyBroker(hexKey(brokerPub)))
	require.False(t, rec.VerifyBroker(hexKey(nodePub)), "wrong key must not verify")
	require.False(t, rec.VerifyBroker("not-hex"), "malformed key must not verify")
	require.False(t, rec.VerifyBroker(""), "empty key must not verify")

	unsigned := rec
	unsigned.BrokerSig = ""
	require.False(t, unsigned.VerifyBroker(hexKey(brokerPub)), "absent broker sig must not verify")
}

// TestNodeAndBrokerFormsDiffer proves the two canonical forms are genuinely distinct
// once broker fields are set - otherwise the coverage gap silently returns.
func TestNodeAndBrokerFormsDiffer(t *testing.T) {
	rec, _, _ := signedPair(t)
	require.NotEqual(t, string(rec.nodeSigningBytes()), string(rec.brokerSigningBytes()),
		"broker canonical form must include the broker-set fields the node form excludes")
}
