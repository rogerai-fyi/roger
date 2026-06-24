package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/agent"
)

// waitBadge polls the on-air panel render until it contains want (the link state is
// confirmed asynchronously by the heartbeat loop), or fails after a timeout.
func waitBadge(t *testing.T, m *model, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(stripANSI(m.onAirPanel(80)), want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("on-air panel never showed %q:\n%s", want, stripANSI(m.onAirPanel(80)))
}

// TestProviderStatusTruthfulOnAir: when the broker accepts heartbeats (200), the
// provider panel shows a genuine ON AIR (the broker is acknowledging the node).
func TestProviderStatusTruthfulOnAir(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true}) // 200 everywhere incl. heartbeat
	}))
	defer srv.Close()

	mm := New(srv.URL, "tester")
	sess, err := agent.Start(agent.Config{Broker: srv.URL, Upstream: "http://127.0.0.1:0", NodeID: "n", Model: "m", Ctx: 8192, Parallel: 1})
	if err != nil {
		t.Fatalf("agent.Start: %v", err)
	}
	defer sess.Stop()
	mm.shares = map[string]*agent.Session{"m": sess}
	mm.refreshShareHeadline()
	waitBadge(t, &mm, "ON AIR")
}

// TestProviderStatusTruthfulReconnecting: when the broker REJECTS heartbeats (401),
// the panel must show RECONNECTING / "broker not acknowledging", never a false ON
// AIR. It must also be NO_COLOR-safe (the plain words carry the meaning).
func TestProviderStatusTruthfulReconnecting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/nodes/register":
			_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true}) // register ok so Start succeeds
		case "/nodes/heartbeat":
			w.WriteHeader(http.StatusUnauthorized) // broker not acknowledging
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()

	mm := New(srv.URL, "tester")
	sess, err := agent.Start(agent.Config{Broker: srv.URL, Upstream: "http://127.0.0.1:0", NodeID: "n", Model: "m", Ctx: 8192, Parallel: 1})
	if err != nil {
		t.Fatalf("agent.Start: %v", err)
	}
	defer sess.Stop()
	mm.shares = map[string]*agent.Session{"m": sess}
	mm.refreshShareHeadline()

	waitBadge(t, &mm, "RECONNECTING")

	plain := stripANSI(mm.onAirPanel(80))
	if strings.Contains(plain, "ON AIR") {
		t.Errorf("panel must NOT claim ON AIR while heartbeats are rejected:\n%s", plain)
	}
	if !strings.Contains(plain, "broker not acknowledging") {
		t.Errorf("RECONNECTING panel should explain the broker is not acknowledging:\n%s", plain)
	}
}
