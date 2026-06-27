package main

import (
	"bytes"
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/store"
)

// TestRelayAuthAndPick covers the relay entry path: an unsigned request is refused at the
// spend gate (401), and a signed request for a model NO node serves runs through identity
// + rate-limit + moderation-allow + pick and fails cleanly (no station), not a 200.
func TestRelayAuthAndPick(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ROGERAI_REDIS_URL", "")
	_, bpriv, _ := ed25519.GenerateKey(nil)
	b := buildBroker(store.NewMem(), bpriv, 0.30, 100, time.Hour)

	// Unsigned spend -> 401 (spending requires a signed request).
	w := httptest.NewRecorder()
	b.relay(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(`{"model":"m"}`))))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("relay(unsigned) = %d, want 401", w.Code)
	}

	// Signed request for a model no node serves -> reaches pick, fails cleanly (not 200).
	_, userPriv, _ := ed25519.GenerateKey(nil)
	body := []byte(`{"model":"no-such-model","messages":[]}`)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	signReq(r, userPriv, body)
	w2 := httptest.NewRecorder()
	b.relay(w2, r)
	if w2.Code == http.StatusOK {
		t.Errorf("relay(no station) = 200, want an error (no station serving the model)")
	}
	if w2.Code == http.StatusUnauthorized {
		t.Errorf("a SIGNED relay should pass the auth gate, got 401")
	}
}
