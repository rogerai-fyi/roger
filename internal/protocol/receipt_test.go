package protocol

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"
)

func sampleReceipt() UsageReceipt {
	return UsageReceipt{
		RequestID: "req1", NodeID: "n1", User: "u_abc", Model: "m",
		PromptTokens: 100, CompletionTokens: 250,
		PriceIn: 0.20, PriceOut: 0.30, TS: 1700000000,
	}
}

func TestCost(t *testing.T) {
	r := sampleReceipt()
	// (100*0.20 + 250*0.30) / 1e6 = (20 + 75) / 1e6 = 95e-6
	if got, want := r.Cost(), 95e-6; got != want {
		t.Errorf("Cost() = %v, want %v", got, want)
	}
}

func TestSignAndVerifyNode(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(pub)

	r := sampleReceipt()
	if r.VerifyNode(pubHex) {
		t.Error("unsigned receipt must not verify")
	}
	r.SignNode(priv)
	if !r.VerifyNode(pubHex) {
		t.Error("signed receipt should verify with the matching pubkey")
	}

	// Tampering with a metered field must invalidate the node signature.
	tampered := r
	tampered.CompletionTokens = 9999
	if tampered.VerifyNode(pubHex) {
		t.Error("tampered receipt must not verify")
	}

	// A different key must not verify.
	otherPub, _, _ := ed25519.GenerateKey(nil)
	if r.VerifyNode(hex.EncodeToString(otherPub)) {
		t.Error("wrong pubkey must not verify")
	}

	// Garbage pubkey/sig inputs are rejected, not panicked.
	if r.VerifyNode("not-hex") || r.VerifyNode("ab") {
		t.Error("malformed pubkey must not verify")
	}
}

// TestSignBrokerIndependent confirms the broker counter-signature is over the same
// canonical bytes (sigs excluded), so adding it doesn't break the node signature.
func TestSignBrokerIndependent(t *testing.T) {
	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)

	r := sampleReceipt()
	r.SignNode(nodePriv)
	hBefore := r.Hash()
	r.SignBroker(brokerPriv)

	if !r.VerifyNode(hex.EncodeToString(nodePub)) {
		t.Error("node sig must still verify after broker counter-signs")
	}
	// Hash excludes both sigs, so it must be stable across signing.
	if r.Hash() != hBefore {
		t.Error("Hash must be independent of NodeSig/BrokerSig")
	}
}

// TestHashChain mirrors the node's per-receipt chaining: each receipt's PrevHash
// is the prior receipt's Hash, and any edit to an earlier link changes the chain.
func TestHashChain(t *testing.T) {
	r1 := sampleReceipt()
	r1.RequestID = "r1"
	h1 := r1.Hash()

	r2 := sampleReceipt()
	r2.RequestID = "r2"
	r2.PrevHash = h1
	h2 := r2.Hash()

	if h1 == "" || h2 == "" {
		t.Fatal("hash should be non-empty")
	}
	if h1 == h2 {
		t.Error("distinct receipts must hash differently")
	}
	// Re-deriving with the same content is deterministic.
	if r2.Hash() != h2 {
		t.Error("Hash must be deterministic")
	}
	// Break the chain root: r2 then points at the wrong prev.
	r2.PrevHash = "deadbeef"
	if r2.Hash() == h2 {
		t.Error("changing PrevHash must change the hash (chain integrity)")
	}
}

func TestEncodeDecodeReceipt(t *testing.T) {
	r := sampleReceipt()
	_, priv, _ := ed25519.GenerateKey(nil)
	r.SignNode(priv)
	enc := EncodeReceipt(r)
	got, err := DecodeReceipt(enc)
	if err != nil {
		t.Fatalf("DecodeReceipt: %v", err)
	}
	if got.RequestID != r.RequestID || got.CompletionTokens != r.CompletionTokens || got.NodeSig != r.NodeSig {
		t.Errorf("round-trip mismatch: %+v vs %+v", got, r)
	}
	if _, err := DecodeReceipt("{bad json"); err == nil {
		t.Error("DecodeReceipt should error on malformed JSON")
	}
}

func TestNewRequestID(t *testing.T) {
	a, b := NewRequestID(), NewRequestID()
	if a == "" || len(a) != 16 { // 8 random bytes -> 16 hex chars
		t.Errorf("request id = %q, want 16 hex chars", a)
	}
	if a == b {
		t.Error("request ids should be unique")
	}
}
