package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/store"
)

// TestJitterWindowAndProbeOnce covers the probe jitter math + a probeOnce round over an
// empty registry (the snapshot/grouping path, no nodes to probe).
func TestJitterWindowAndProbeOnce(t *testing.T) {
	if w := (probeConfig{interval: 10 * time.Second}).jitterWindow(); w != 5*time.Second {
		t.Errorf("jitterWindow(10s) = %s, want 5s (capped)", w)
	}
	if w := (probeConfig{interval: 2 * time.Second}).jitterWindow(); w != time.Second {
		t.Errorf("jitterWindow(2s) = %s, want 1s (interval/2)", w)
	}
	if w := (probeConfig{interval: -time.Second}).jitterWindow(); w != 0 {
		t.Errorf("jitterWindow(neg) = %s, want 0", w)
	}
	// probeOnce over an empty registry: snapshots nothing, probes nothing, no panic.
	(&broker{}).probeOnce()
}

// TestModerationScreen covers the content-screen gate's no-network branches: unconfigured
// (allow when not required, 503 when required) and the empty-text short-circuit.
func TestModerationScreenBranches(t *testing.T) {
	// Unconfigured + not required -> ALLOW.
	if r := (moderation{}).screen("hello"); !r.allow() {
		t.Errorf("unconfigured+optional screen should ALLOW, got %d", r.status)
	}
	// Unconfigured + required -> fail closed (503).
	if r := (moderation{require: true}).screen("hello"); r.status != http.StatusServiceUnavailable {
		t.Errorf("unconfigured+required screen = %d, want 503", r.status)
	}
	// Configured but empty text -> short-circuit ALLOW (no network).
	if r := (moderation{provider: "url", url: "http://x", client: http.DefaultClient}).screen("   "); !r.allow() {
		t.Errorf("empty-text screen should ALLOW, got %d", r.status)
	}
	// Groq provider with no key -> unconfigured branch (ALLOW when optional).
	if r := (moderation{provider: "groq"}).screen("hello"); !r.allow() {
		t.Errorf("groq-without-key+optional should ALLOW, got %d", r.status)
	}
}

// TestGrantByID covers the single-grant handler: unauthenticated 401, and an owner GET
// that returns its own grant.
func TestGrantByID(t *testing.T) {
	b, o := brokerWithOwner(t)
	_ = b.db.CreateGrant(store.Grant{ID: "grant_x", SecretHash: "h", Owner: o.Pubkey, Label: "petlings"})

	// No auth -> 401.
	w := httptest.NewRecorder()
	b.grantByID(w, httptest.NewRequest(http.MethodGet, "/grants/grant_x", nil), "grant_x")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("grantByID(anon) = %d, want 401", w.Code)
	}

	// Owner session GET -> 200 with the grant.
	r := sessionReq(b, http.MethodGet, "/grants/grant_x", "octocat", 7)
	w2 := httptest.NewRecorder()
	b.grantByID(w2, r, "grant_x")
	if w2.Code != http.StatusOK {
		t.Fatalf("grantByID(owner GET) = %d, want 200: %s", w2.Code, w2.Body.String())
	}
}
