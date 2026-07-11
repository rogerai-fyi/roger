package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/store"
)

// capsuleBroker builds a broker with an initialized capsule store (buildBroker wires b.capsules).
func capsuleBroker(t *testing.T) *broker {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(nil)
	return buildBroker(store.NewMem(), priv, 0.30, 100, time.Hour)
}

func capsuleMintReq(t *testing.T, b *broker, priv ed25519.PrivateKey, lookup string, blob []byte, sign bool) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"lookup": lookup, "blob": base64.StdEncoding.EncodeToString(blob)})
	r := httptest.NewRequest(http.MethodPost, "/capsule", bytes.NewReader(body))
	if sign {
		signReq(r, priv, body)
	}
	w := httptest.NewRecorder()
	b.capsuleMint(w, r)
	return w
}

func capsuleResolveReq(t *testing.T, b *broker, lookup string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"lookup": lookup})
	r := httptest.NewRequest(http.MethodPost, "/capsule/resolve", bytes.NewReader(body))
	w := httptest.NewRecorder()
	b.capsuleResolve(w, r)
	return w
}

func TestCapsuleMintResolveRoundTrip(t *testing.T) {
	b := capsuleBroker(t)
	_, priv, _ := ed25519.GenerateKey(nil)
	lookup := "abc123" // stands in for hex(sha256(code)); the broker treats it as opaque
	blob := []byte("this-is-ciphertext-the-broker-cannot-read")

	if w := capsuleMintReq(t, b, priv, lookup, blob, true); w.Code != http.StatusOK {
		t.Fatalf("mint status %d: %s", w.Code, w.Body.String())
	}

	// resolve returns the exact blob once
	w := capsuleResolveReq(t, b, lookup)
	if w.Code != http.StatusOK {
		t.Fatalf("resolve status %d: %s", w.Code, w.Body.String())
	}
	var out struct {
		Blob string `json:"blob"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	got, _ := base64.StdEncoding.DecodeString(out.Blob)
	if !bytes.Equal(got, blob) {
		t.Fatalf("resolved blob = %q, want %q", got, blob)
	}

	// ONE-TIME: a second resolve of the same lookup is the uniform 404
	w2 := capsuleResolveReq(t, b, lookup)
	if w2.Code != http.StatusNotFound {
		t.Errorf("second resolve status %d, want 404 (one-time, delete-on-read)", w2.Code)
	}
}

func TestCapsuleResolveUniform404(t *testing.T) {
	b := capsuleBroker(t)
	miss := capsuleResolveReq(t, b, "does-not-exist")
	empty := capsuleResolveReq(t, b, "")
	// an unknown lookup and an empty lookup must produce the IDENTICAL response (no existence oracle)
	if miss.Code != http.StatusNotFound || empty.Code != http.StatusNotFound {
		t.Fatalf("miss=%d empty=%d, both want 404", miss.Code, empty.Code)
	}
	if miss.Body.String() != empty.Body.String() {
		t.Errorf("unknown vs empty lookup gave different bodies (%q vs %q) - that is an oracle", miss.Body.String(), empty.Body.String())
	}
}

func TestCapsuleExpired(t *testing.T) {
	b := capsuleBroker(t)
	// store a blob that expired in the past (put with a `now` two TTLs ago -> expires one TTL ago)
	b.capsules.put("stale", []byte("old"), time.Now().Add(-2*capsuleTTL))
	if w := capsuleResolveReq(t, b, "stale"); w.Code != http.StatusNotFound {
		t.Errorf("expired blob resolve status %d, want 404", w.Code)
	}
}

func TestCapsuleOversize(t *testing.T) {
	b := capsuleBroker(t)
	_, priv, _ := ed25519.GenerateKey(nil)
	big := bytes.Repeat([]byte{0x41}, capsuleMaxBlob+1)
	if w := capsuleMintReq(t, b, priv, "toobig", big, true); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversize mint status %d, want 413", w.Code)
	}
}

func TestCapsuleUnsignedMintRejected(t *testing.T) {
	b := capsuleBroker(t)
	_, priv, _ := ed25519.GenerateKey(nil)
	if w := capsuleMintReq(t, b, priv, "nosig", []byte("x"), false); w.Code != http.StatusUnauthorized {
		t.Errorf("unsigned mint status %d, want 401", w.Code)
	}
	// and it must NOT have stored anything (content-blind + no unsigned writes)
	if w := capsuleResolveReq(t, b, "nosig"); w.Code != http.StatusNotFound {
		t.Errorf("an unsigned mint must store nothing, got resolve status %d", w.Code)
	}
}

// TestCapsuleStoreBounded: the in-memory store sheds load at capacity rather than growing unbounded.
func TestCapsuleStoreBounded(t *testing.T) {
	c := newCapsuleStore()
	now := time.Now()
	exp := now.Add(capsuleTTL).Unix()
	for i := 0; i < capsuleMaxEntries; i++ {
		c.m[string(rune(i))+"-k"] = capsuleBlob{blob: []byte("v"), expires: exp} // fill to capacity (all live)
	}
	if c.put("one-too-many", []byte("v"), now) {
		t.Error("put past capacity should be refused, not grow unbounded")
	}
}
