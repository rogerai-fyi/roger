package main

import (
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// TestLiveMarket covers the marketplace rollup: a live unbanned node counts as on-air
// with its models; a private/banned/stale node is bucketed accordingly.
func TestLiveMarket(t *testing.T) {
	now := time.Now()
	b := &broker{
		nodes: map[string]protocol.NodeRegistration{
			"n-live":    {NodeID: "n-live", Offers: []protocol.ModelOffer{{Model: "m1"}}},
			"n-private": {NodeID: "n-private", Offers: []protocol.ModelOffer{{Model: "m2"}}},
			"n-stale":   {NodeID: "n-stale", Offers: []protocol.ModelOffer{{Model: "m3"}}},
		},
		lastSeen: map[string]time.Time{"n-live": now, "n-private": now, "n-stale": now.Add(-48 * time.Hour)},
		private:  map[string]bool{"n-private": true},
		banned:   map[string]bool{},
	}
	m := b.liveMarket(now)
	if m["nodes_total"].(int) != 3 {
		t.Errorf("nodes_total = %v, want 3", m["nodes_total"])
	}
	if m["private"].(int) != 1 {
		t.Errorf("private = %v, want 1", m["private"])
	}
	if m["on_air"].(int) < 1 {
		t.Errorf("on_air = %v, want >=1 (the live node)", m["on_air"])
	}
}

// TestWebhookBranches covers the Stripe webhook's guard branches: 503 when billing is
// unconfigured, and 400 on a bad signature (the full event path requires a valid HMAC).
func TestWebhookBranches(t *testing.T) {
	// Unconfigured -> 503.
	b := &broker{}
	w := httptest.NewRecorder()
	b.webhook(w, httptest.NewRequest(http.MethodPost, "/billing/webhook", strings.NewReader("{}")))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("webhook(unconfigured) = %d, want 503", w.Code)
	}
	// Configured but a bogus signature -> 400.
	b.bill.secretKey = "sk_test"
	b.bill.webhookSecret = "whsec_test"
	r := httptest.NewRequest(http.MethodPost, "/billing/webhook", strings.NewReader(`{"type":"x"}`))
	r.Header.Set("Stripe-Signature", "t=1,v1=deadbeef")
	w2 := httptest.NewRecorder()
	b.webhook(w2, r)
	if w2.Code != http.StatusBadRequest {
		t.Errorf("webhook(bad sig) = %d, want 400", w2.Code)
	}
}

// TestAdminOverview covers the admin dashboard rollup: 403 without the key, and a keyed
// GET that returns the HEALTH + MARKETPLACE + REVENUE payload (drives liveMarket +
// AdminFinancials + AdminMarketTotals through the real store).
func TestAdminOverview(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ROGERAI_REDIS_URL", "")
	_, priv, _ := ed25519.GenerateKey(nil)
	b := buildBroker(store.NewMem(), priv, 0.30, 100, time.Hour)
	b.adminKey = "super-secret"

	// No admin key -> 403.
	w := httptest.NewRecorder()
	b.adminOverview(w, httptest.NewRequest(http.MethodGet, "/admin/overview", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("adminOverview without key = %d, want 403", w.Code)
	}

	// Keyed -> 200 with a structured rollup.
	r := httptest.NewRequest(http.MethodGet, "/admin/overview?days=7", nil)
	r.Header.Set("X-Roger-Admin", "super-secret")
	w2 := httptest.NewRecorder()
	b.adminOverview(w2, r)
	if w2.Code != http.StatusOK {
		t.Fatalf("adminOverview keyed = %d, want 200: %s", w2.Code, w2.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w2.Body.Bytes(), &resp); err != nil {
		t.Fatalf("overview not JSON: %v", err)
	}
	if len(resp) == 0 {
		t.Error("adminOverview should return a non-empty rollup")
	}
}
