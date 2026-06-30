package main

import (
	"crypto/ed25519"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/store"
)

// TestBuildBroker covers the broker construction + wiring extracted from main(): every
// map is initialized, the flag-derived fields land, the env-driven config loaders run,
// the store is rehydrated, and (no ROGERAI_REDIS_URL) the shared layer stays nil so no
// background goroutine is launched.
func TestBuildBroker(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ROGERAI_REDIS_URL", "")      // single-instance: b.shared stays nil
	t.Setenv("ROGERAI_MULTI_INSTANCE", "") // and no bus goroutine
	_, priv, _ := ed25519.GenerateKey(nil)

	b := buildBroker(store.NewMem(), priv, 0.30, 100, 24*time.Hour)

	if b.db == nil || b.feeRate != 0.30 || b.seedFunds != 100 || b.lockWin != 24*time.Hour {
		t.Fatalf("flag-derived fields not wired: fee=%v seed=%v lock=%v", b.feeRate, b.seedFunds, b.lockWin)
	}
	if b.shared != nil {
		t.Error("with no ROGERAI_REDIS_URL the shared layer should be nil")
	}
	// Core maps initialized (a nil map write would panic on the hot path).
	for name, ok := range map[string]bool{
		"nodes": b.nodes != nil, "tunnels": b.tunnels != nil, "lastSeen": b.lastSeen != nil,
		"trust": b.trust != nil, "banned": b.banned != nil, "bannedOwners": b.bannedOwners != nil,
		"quotes": b.quotes != nil, "pubOfUser": b.pubOfUser != nil, "inflight": b.inflight != nil,
	} {
		if !ok {
			t.Errorf("map %q not initialized", name)
		}
	}
	// Config loaders ran.
	if b.recount.client == nil || b.rl == nil || b.grantRL == nil || b.anonRL == nil || b.concierge == nil {
		t.Error("config loaders did not fully wire (recount/rate-limiters/concierge)")
	}
	if b.startTime.IsZero() {
		t.Error("startTime should be stamped")
	}
}

// TestRoutes covers the route table built by routes(): the cheap public endpoints
// respond as registered through the real mux.
func TestRoutes(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ROGERAI_REDIS_URL", "")
	_, priv, _ := ed25519.GenerateKey(nil)
	b := buildBroker(store.NewMem(), priv, 0.30, 100, time.Hour)

	srv := httptest.NewServer(b.routes())
	defer srv.Close()

	// /health -> "ok"
	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "ok" {
		t.Fatalf("/health = %d %q, want 200 ok", resp.StatusCode, body)
	}

	// / -> the service descriptor JSON
	r2, _ := http.Get(srv.URL + "/")
	b2, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	if r2.StatusCode != http.StatusOK || !strings.Contains(string(b2), "rogerai-broker") {
		t.Errorf("/ = %d %q, want the service descriptor", r2.StatusCode, b2)
	}

	// /openapi.yaml -> the spec, served as yaml
	r3, _ := http.Get(srv.URL + "/openapi.yaml")
	r3.Body.Close()
	if r3.StatusCode != http.StatusOK || !strings.Contains(r3.Header.Get("Content-Type"), "yaml") {
		t.Errorf("/openapi.yaml = %d ct=%q, want 200 yaml", r3.StatusCode, r3.Header.Get("Content-Type"))
	}

	// A registered API route is reachable (anon /discover returns JSON, not 404).
	r4, _ := http.Get(srv.URL + "/discover")
	r4.Body.Close()
	if r4.StatusCode == http.StatusNotFound {
		t.Error("/discover should be a registered route, got 404")
	}
}
