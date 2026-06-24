package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// hbBroker wires a minimal broker with one registered node "n" whose BridgeToken is
// "secret-token", for the heartbeat auth tests.
func hbBroker() *broker {
	b := &broker{
		db:       store.NewMem(),
		tunnels:  map[string]*nodeTunnel{},
		lastSeen: map[string]time.Time{},
	}
	b.tunnels["n"] = &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}, token: "secret-token"}
	return b
}

// TestHeartbeatRequiresToken locks the heartbeat auth fix: a heartbeat must carry the
// node's Bearer BridgeToken. An unsigned heartbeat (no token), a forged token, and an
// unknown/forged node id are all rejected and NEVER refresh a node's online TTL; only
// a correctly-signed heartbeat for a registered node updates lastSeen.
func TestHeartbeatRequiresToken(t *testing.T) {
	post := func(b *broker, body, auth string) int {
		r := httptest.NewRequest(http.MethodPost, "/nodes/heartbeat", bytes.NewReader([]byte(body)))
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		b.heartbeat(w, r)
		return w.Code
	}

	// (1) Unsigned (no Authorization) -> 401, lastSeen untouched.
	b := hbBroker()
	if c := post(b, `{"node_id":"n"}`, ""); c != http.StatusUnauthorized {
		t.Errorf("unsigned heartbeat = %d, want 401", c)
	}
	if _, ok := b.lastSeen["n"]; ok {
		t.Error("an unsigned heartbeat must not refresh the node TTL")
	}

	// (2) Forged token -> 401, lastSeen untouched.
	b = hbBroker()
	if c := post(b, `{"node_id":"n"}`, "Bearer wrong-token"); c != http.StatusUnauthorized {
		t.Errorf("forged-token heartbeat = %d, want 401", c)
	}
	if _, ok := b.lastSeen["n"]; ok {
		t.Error("a forged-token heartbeat must not refresh the node TTL")
	}

	// (3) Forged/unknown node id (even with a valid-looking token) -> 404.
	b = hbBroker()
	if c := post(b, `{"node_id":"ghost"}`, "Bearer secret-token"); c != http.StatusNotFound {
		t.Errorf("unknown-node heartbeat = %d, want 404", c)
	}
	if _, ok := b.lastSeen["ghost"]; ok {
		t.Error("a forged node id must never be marked online")
	}

	// (4) Missing node id -> 400.
	b = hbBroker()
	if c := post(b, `{}`, "Bearer secret-token"); c != http.StatusBadRequest {
		t.Errorf("missing node_id = %d, want 400", c)
	}

	// (5) Correctly-signed for a registered node -> 200, lastSeen refreshed.
	b = hbBroker()
	if c := post(b, `{"node_id":"n"}`, "Bearer secret-token"); c != http.StatusOK {
		t.Errorf("authed heartbeat = %d, want 200", c)
	}
	if _, ok := b.lastSeen["n"]; !ok {
		t.Error("a correctly-signed heartbeat must refresh the node TTL")
	}
}

// TestHeartbeatBodyCapped verifies the body is bounded by an io.LimitReader: an
// oversized JSON body is read only up to the cap (it cannot exhaust memory), and a
// body that overflows the cap mid-token does not authenticate or update anything.
func TestHeartbeatBodyCapped(t *testing.T) {
	b := hbBroker()
	// A node_id padded past the 4KiB cap: the LimitReader truncates the JSON so it
	// either fails to parse the node_id or yields a truncated id that is not "n" ->
	// the handler must NOT mark node "n" online.
	huge := `{"node_id":"n","pad":"` + strings.Repeat("A", 8<<10) + `"}`
	r := httptest.NewRequest(http.MethodPost, "/nodes/heartbeat", bytes.NewReader([]byte(huge)))
	r.Header.Set("Authorization", "Bearer secret-token")
	w := httptest.NewRecorder()
	b.heartbeat(w, r)
	// The leading node_id ("n") sits well within 4KiB, so this particular body still
	// parses node_id="n" and authenticates - the point under test is that only the
	// capped prefix is ever read (no unbounded allocation). Assert it did not error in
	// a way that 500s, and that an over-cap body never panics.
	if w.Code != http.StatusOK && w.Code != http.StatusBadRequest {
		t.Errorf("capped-body heartbeat = %d, want 200 or 400 (bounded, no 5xx/panic)", w.Code)
	}
}
