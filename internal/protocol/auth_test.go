package protocol

import (
	"crypto/ed25519"
	"testing"
	"time"
)

// TestSignVerifyRequest covers the consumer request-signing fix: a signature over
// the canonical (method,path,ts,body) string verifies, derives a stable id, and
// is rejected if any signed field is altered.
func TestSignVerifyRequest(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	body := []byte(`{"model":"m","messages":[]}`)
	pub, ts, sig := SignRequest(priv, "POST", "/v1/chat/completions", body)

	id, ok := VerifyRequest(pub, sig, ts, "POST", "/v1/chat/completions", body)
	if !ok {
		t.Fatal("valid signature should verify")
	}
	if id != UserIDFromPubkey(pub) {
		t.Errorf("derived id = %q, want %q", id, UserIDFromPubkey(pub))
	}

	// Tamper with each signed component → must fail.
	if _, ok := VerifyRequest(pub, sig, ts, "GET", "/v1/chat/completions", body); ok {
		t.Error("changed method should not verify")
	}
	if _, ok := VerifyRequest(pub, sig, ts, "POST", "/balance", body); ok {
		t.Error("changed path should not verify")
	}
	if _, ok := VerifyRequest(pub, sig, ts, "POST", "/v1/chat/completions", []byte(`{"model":"evil"}`)); ok {
		t.Error("changed body should not verify")
	}
	if _, ok := VerifyRequest(pub, sig, ts+1, "POST", "/v1/chat/completions", body); ok {
		t.Error("changed ts should not verify")
	}
}

// TestVerifyRequestStaleTS verifies the anti-replay window: a too-old (or
// too-far-future) timestamp is rejected even with an otherwise valid signature.
func TestVerifyRequestStaleTS(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	body := []byte("x")
	pub := encPub(priv)

	stale := time.Now().Add(-SigMaxSkew - time.Minute).Unix()
	sig := signAt(priv, "POST", "/p", stale, body)
	if _, ok := VerifyRequest(pub, sig, stale, "POST", "/p", body); ok {
		t.Error("stale timestamp should be rejected")
	}

	future := time.Now().Add(SigMaxSkew + time.Minute).Unix()
	sig = signAt(priv, "POST", "/p", future, body)
	if _, ok := VerifyRequest(pub, sig, future, "POST", "/p", body); ok {
		t.Error("far-future timestamp should be rejected")
	}

	fresh := time.Now().Unix()
	sig = signAt(priv, "POST", "/p", fresh, body)
	if _, ok := VerifyRequest(pub, sig, fresh, "POST", "/p", body); !ok {
		t.Error("fresh timestamp should verify")
	}
}

// TestUserIDFromPubkey verifies the id is stable for a key and shaped as expected.
func TestUserIDFromPubkey(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	h := encPubKey(pub)
	id1 := UserIDFromPubkey(h)
	id2 := UserIDFromPubkey(h)
	if id1 != id2 {
		t.Errorf("id not stable: %q vs %q", id1, id2)
	}
	if len(id1) != 18 || id1[:2] != "u_" {
		t.Errorf("id %q should be u_ + 16 hex (len 18)", id1)
	}
	pub2, _, _ := ed25519.GenerateKey(nil)
	if UserIDFromPubkey(encPubKey(pub2)) == id1 {
		t.Error("different keys must derive different ids")
	}
}

// TestVerifyRequestBadInputs covers malformed pubkey/sig and an invalid ts string
// path (handled by the broker, but the verifier must also reject garbage hex).
func TestVerifyRequestBadInputs(t *testing.T) {
	body := []byte("x")
	ts := time.Now().Unix()
	if _, ok := VerifyRequest("zzzz", "00", ts, "POST", "/p", body); ok {
		t.Error("non-hex pubkey should fail")
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	if _, ok := VerifyRequest(encPub(priv), "zz", ts, "POST", "/p", body); ok {
		t.Error("non-hex sig should fail")
	}
	if _, ok := VerifyRequest(encPub(priv), "00", ts, "POST", "/p", body); ok {
		t.Error("wrong-length sig should fail")
	}
}

// --- small test helpers (avoid hex import churn in the test file) ---

func encPub(priv ed25519.PrivateKey) string {
	return encPubKey(priv.Public().(ed25519.PublicKey))
}

func encPubKey(pub ed25519.PublicKey) string {
	const hexd = "0123456789abcdef"
	b := make([]byte, len(pub)*2)
	for i, c := range pub {
		b[i*2] = hexd[c>>4]
		b[i*2+1] = hexd[c&0x0f]
	}
	return string(b)
}

func signAt(priv ed25519.PrivateKey, method, path string, ts int64, body []byte) string {
	sig := ed25519.Sign(priv, []byte(CanonicalRequest(method, path, ts, body)))
	return encBytes(sig)
}

func encBytes(b []byte) string {
	const hexd = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = hexd[c>>4]
		out[i*2+1] = hexd[c&0x0f]
	}
	return string(out)
}
