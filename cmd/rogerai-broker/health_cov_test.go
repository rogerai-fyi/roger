package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rogerai-fyi/roger/internal/store"
)

// TestReadyMethodAndSharedBranches covers ready()'s remaining branches: a non-GET is
// rejected, a wired+healthy shared layer reports "ok", and a wired+unreachable shared layer
// reports "degraded" WITHOUT failing readiness (the in-memory path is authoritative).
func TestReadyMethodAndSharedBranches(t *testing.T) {
	readyJSON := func(t *testing.T, b *broker) (int, map[string]any) {
		t.Helper()
		w := httptest.NewRecorder()
		b.ready(w, httptest.NewRequest(http.MethodGet, "/ready", nil))
		var got map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		return w.Code, got
	}

	t.Run("methodNotAllowed", func(t *testing.T) {
		b := testBrokerWithDB(store.NewMem())
		w := httptest.NewRecorder()
		b.ready(w, httptest.NewRequest(http.MethodPost, "/ready", nil))
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("POST /ready = %d, want 405", w.Code)
		}
	})

	t.Run("sharedHealthyOK", func(t *testing.T) {
		b := testBrokerWithDB(store.NewMem())
		vs, _ := newTestValkey(t) // a live miniredis-backed shared layer: healthy
		b.shared = vs
		code, got := readyJSON(t, b)
		if code != http.StatusOK || got["ready"] != true {
			t.Fatalf("ready with healthy deps = %d %v, want 200 ready:true", code, got)
		}
		if got["shared"] != "ok" {
			t.Errorf("shared status = %v, want \"ok\"", got["shared"])
		}
	})

	t.Run("sharedDegradedStillReady", func(t *testing.T) {
		b := testBrokerWithDB(store.NewMem())
		b.shared = newMemStore() // inert memStore: never healthy
		code, got := readyJSON(t, b)
		if code != http.StatusOK || got["ready"] != true {
			t.Fatalf("a degraded shared layer must NOT fail readiness; got %d %v", code, got)
		}
		if got["shared"] != "degraded" {
			t.Errorf("shared status = %v, want \"degraded\"", got["shared"])
		}
	})
}
