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

// TestAdminLive covers the broker's slim live feed: 403 without the key, and a keyed GET that
// returns the in-memory snapshot (health + live marketplace + seed/fee/stripe) the private
// roger-admin portal merges with its own Postgres rollups.
func TestAdminLive(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ROGERAI_REDIS_URL", "")
	_, priv, _ := ed25519.GenerateKey(nil)
	b := buildBroker(store.NewMem(), priv, 0.30, 100, time.Hour)
	b.adminKey = "super-secret"

	// No admin key -> 403.
	w := httptest.NewRecorder()
	b.adminLive(w, httptest.NewRequest(http.MethodGet, "/admin/live", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("adminLive without key = %d, want 403", w.Code)
	}

	// Keyed -> 200 with the live snapshot.
	r := httptest.NewRequest(http.MethodGet, "/admin/live", nil)
	r.Header.Set("X-Roger-Admin", "super-secret")
	w2 := httptest.NewRecorder()
	b.adminLive(w2, r)
	if w2.Code != http.StatusOK {
		t.Fatalf("adminLive keyed = %d, want 200: %s", w2.Code, w2.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w2.Body.Bytes(), &resp); err != nil {
		t.Fatalf("live not JSON: %v", err)
	}
	// Must carry the live sections so a regression that drops one is caught.
	for _, k := range []string{"health", "marketplace_live", "seed_funded", "fee_rate", "stripe_mode"} {
		if _, ok := resp[k]; !ok {
			t.Errorf("adminLive missing %q; got keys %v", k, keysOf(resp))
		}
	}
}

func keysOf(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

// TestAdminLiveMethodAndDBDown covers the method guard (405) and the DB-down health branch:
// an admin-keyed live read against an unhealthy store reports db:"down", ready:false (and
// still returns 200 - the dashboard must render even when the store is unreachable).
func TestAdminLiveMethodAndDBDown(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ROGERAI_REDIS_URL", "")
	_, priv, _ := ed25519.GenerateKey(nil)
	b := buildBroker(store.NewMem(), priv, 0.30, 100, time.Hour)
	b.adminKey = "k"

	// Method guard: a POST is 405.
	wm := httptest.NewRecorder()
	rm := httptest.NewRequest(http.MethodPost, "/admin/live", nil)
	rm.Header.Set("X-Roger-Admin", "k")
	b.adminLive(wm, rm)
	if wm.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST live = %d, want 405", wm.Code)
	}

	// DB down -> health.db:"down", ready:false, still 200.
	b.db = unhealthyStore{b.db}
	r := httptest.NewRequest(http.MethodGet, "/admin/live", nil)
	r.Header.Set("X-Roger-Admin", "k")
	w := httptest.NewRecorder()
	b.adminLive(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("live (db down) = %d, want 200", w.Code)
	}
	var resp struct {
		Health map[string]any `json:"health"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Health["db"] != "down" || resp.Health["ready"] != false {
		t.Errorf("health = %+v, want db:down ready:false", resp.Health)
	}
}
