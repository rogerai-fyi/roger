package protocol

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"
)

// TestNodeSigCoversEverySignedField is the exhaustive form of the spec's tamper table
// (features/trust/lineage_receipts.feature): the node signature must cover EVERY lineage
// field, so altering any one of them after signing breaks VerifyNode. receipt_test.go
// already spot-checks CompletionTokens; this locks the whole canonical surface so a future
// field added to signingBytes (or wrongly excluded from it) can't silently become forgeable.
func TestNodeSigCoversEverySignedField(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(pub)

	mutations := []struct {
		field  string
		mutate func(*UsageReceipt)
	}{
		{"Model", func(r *UsageReceipt) { r.Model = "evil-model" }},
		{"User", func(r *UsageReceipt) { r.User = "u_evil" }},
		{"PromptTokens", func(r *UsageReceipt) { r.PromptTokens = 1 }},
		{"CompletionTokens", func(r *UsageReceipt) { r.CompletionTokens = 1 }},
		{"PriceIn", func(r *UsageReceipt) { r.PriceIn = 9.99 }},
		{"PriceOut", func(r *UsageReceipt) { r.PriceOut = 9.99 }},
		{"PrevHash", func(r *UsageReceipt) { r.PrevHash = "deadbeef" }},
		{"TS", func(r *UsageReceipt) { r.TS = 1 }},
		{"RequestID", func(r *UsageReceipt) { r.RequestID = "req-evil" }},
		{"NodeID", func(r *UsageReceipt) { r.NodeID = "n-evil" }},
	}

	for _, m := range mutations {
		r := sampleReceipt()
		r.SignNode(priv)
		if !r.VerifyNode(pubHex) {
			t.Fatalf("%s: a freshly node-signed receipt must verify", m.field)
		}
		m.mutate(&r)
		if r.VerifyNode(pubHex) {
			t.Errorf("tampering %s after signing must break the node signature", m.field)
		}
	}
}

// TestBrokerSetFieldsDoNotBreakNodeSig is the inverse: the three broker-set fields are
// EXCLUDED from signingBytes, so the node sig must survive each being set independently
// (the settle path sets them after the node signs). Locks the exclusion list field-by-field.
func TestBrokerSetFieldsDoNotBreakNodeSig(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(pub)

	setters := []struct {
		field string
		set   func(*UsageReceipt)
	}{
		{"GrantID", func(r *UsageReceipt) { r.GrantID = "grant_x" }},
		{"BrokerPromptTokens", func(r *UsageReceipt) { r.BrokerPromptTokens = 7 }},
		{"BrokerCompletionTokens", func(r *UsageReceipt) { r.BrokerCompletionTokens = 9 }},
	}

	for _, s := range setters {
		r := sampleReceipt()
		r.SignNode(priv)
		s.set(&r)
		if !r.VerifyNode(pubHex) {
			t.Errorf("setting the broker-only field %s must NOT break the node signature", s.field)
		}
	}
}
