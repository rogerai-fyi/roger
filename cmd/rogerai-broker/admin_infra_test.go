package main

import (
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/rogerai-fyi/roger/internal/store"
)

// liveInfra drives an admin-keyed GET /admin/live and returns the parsed infra + health blocks.
func liveInfra(t *testing.T, b *broker) (infra, health map[string]any) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/admin/live", nil)
	r.Header.Set("X-Roger-Admin", b.adminKey)
	w := httptest.NewRecorder()
	b.adminLive(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("adminLive = %d, want 200: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("live not JSON: %v", err)
	}
	in, ok := resp["infra"].(map[string]any)
	if !ok {
		t.Fatalf("response missing infra block; keys=%v", keysOf(resp))
	}
	h, ok := resp["health"].(map[string]any)
	if !ok {
		t.Fatalf("response missing health block; keys=%v", keysOf(resp))
	}
	return in, h
}

// TestAdminLiveInfraSingleInstance: with the bus OFF (no shared store), infra reports
// multi_instance=false, exactly 1 live instance, and shared_store reachable=false role=none,
// while health still carries version/uptime_seconds/db (reused, not duplicated into infra).
func TestAdminLiveInfraSingleInstance(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ROGERAI_REDIS_URL", "")
	t.Setenv("ROGERAI_MULTI_INSTANCE", "")
	_, priv, _ := ed25519.GenerateKey(nil)
	b := buildBroker(store.NewMem(), priv, 0.30, 100, time.Hour)
	b.adminKey = "k"

	infra, health := liveInfra(t, b)
	if infra["multi_instance"] != false {
		t.Errorf("multi_instance = %v, want false", infra["multi_instance"])
	}
	if infra["instances_live"] != float64(1) {
		t.Errorf("instances_live = %v, want 1", infra["instances_live"])
	}
	ss, ok := infra["shared_store"].(map[string]any)
	if !ok {
		t.Fatalf("infra missing shared_store")
	}
	if ss["reachable"] != false {
		t.Errorf("shared_store.reachable = %v, want false", ss["reachable"])
	}
	if ss["role"] != "none" {
		t.Errorf("shared_store.role = %v, want none", ss["role"])
	}
	// db/version/uptime are reused from health, not re-emitted under infra.
	for _, k := range []string{"version", "uptime_seconds", "db"} {
		if _, ok := health[k]; !ok {
			t.Errorf("health missing %q; got %v", k, keysOf(health))
		}
	}
}

// TestAdminLiveInfraMultiInstancePeers: with the bus ON over a reachable Valkey, instances_live
// reflects the DISTINCT live presence heartbeats in the shared store (this instance + 2 seeded
// peers = 3), shared_store is reachable with the valkey accelerator+bus role.
func TestAdminLiveInfraMultiInstancePeers(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	mr := miniredis.RunT(t)
	t.Setenv("ROGERAI_REDIS_URL", "redis://"+mr.Addr())
	t.Setenv("ROGERAI_MULTI_INSTANCE", "1")
	_, priv, _ := ed25519.GenerateKey(nil)
	b := buildBroker(store.NewMem(), priv, 0.30, 100, time.Hour)
	t.Cleanup(func() { _ = b.shared.Close() })
	b.adminKey = "k"

	if !b.multiInstance {
		t.Fatal("multi-instance must be ON")
	}
	// buildBroker marks THIS instance present at startup; seed two peers directly.
	now := time.Now()
	if err := b.shared.markInstance("peer-a", now); err != nil {
		t.Fatalf("markInstance peer-a: %v", err)
	}
	if err := b.shared.markInstance("peer-b", now); err != nil {
		t.Fatalf("markInstance peer-b: %v", err)
	}

	infra, _ := liveInfra(t, b)
	if infra["multi_instance"] != true {
		t.Errorf("multi_instance = %v, want true", infra["multi_instance"])
	}
	if infra["instances_live"] != float64(3) {
		t.Errorf("instances_live = %v, want 3 (self + 2 peers)", infra["instances_live"])
	}
	ss := infra["shared_store"].(map[string]any)
	if ss["reachable"] != true {
		t.Errorf("shared_store.reachable = %v, want true", ss["reachable"])
	}
	if ss["role"] != "valkey accelerator+bus" {
		t.Errorf("shared_store.role = %v, want 'valkey accelerator+bus'", ss["role"])
	}
}

// TestAdminLiveInfraSharedUnreachable: the bus is ON but the Valkey backend is gone. The read
// must NOT panic or block, must still 200, report shared_store reachable=false, and fall back to
// a self-only count of 1 (never 0 live instances).
func TestAdminLiveInfraSharedUnreachable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	mr := miniredis.RunT(t)
	t.Setenv("ROGERAI_REDIS_URL", "redis://"+mr.Addr())
	t.Setenv("ROGERAI_MULTI_INSTANCE", "1")
	_, priv, _ := ed25519.GenerateKey(nil)
	b := buildBroker(store.NewMem(), priv, 0.30, 100, time.Hour)
	t.Cleanup(func() { _ = b.shared.Close() })
	b.adminKey = "k"
	if !b.multiInstance {
		t.Fatal("multi-instance must be ON")
	}

	mr.Close() // backend gone

	infra, _ := liveInfra(t, b)
	if infra["multi_instance"] != true {
		t.Errorf("multi_instance = %v, want true", infra["multi_instance"])
	}
	if infra["instances_live"] != float64(1) {
		t.Errorf("instances_live = %v, want 1 (self-only fallback)", infra["instances_live"])
	}
	ss := infra["shared_store"].(map[string]any)
	if ss["reachable"] != false {
		t.Errorf("shared_store.reachable = %v, want false", ss["reachable"])
	}
}
