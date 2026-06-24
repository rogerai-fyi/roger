package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// newBroker builds a broker backed by the given store with the in-memory registry
// maps initialized - the shape main() uses, for the persistence + re-hydrate tests.
func newBroker(db store.Store) *broker {
	return &broker{
		nodes:        map[string]protocol.NodeRegistration{},
		tunnels:      map[string]*nodeTunnel{},
		lastSeen:     map[string]time.Time{},
		confidential: map[string]bool{},
		tps:          map[string]float64{},
		lastPersist:  map[string]time.Time{},
		db:           db,
	}
}

// registerNode posts a valid signed registration for nodeID/model at the given
// bridge token and returns the HTTP status. A nonzero price is avoided so the
// earning-login gate never fires (these tests are about the registry, not auth).
func registerNode(t *testing.T, b *broker, nodeID, token string, priv ed25519.PrivateKey) int {
	t.Helper()
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	reg := protocol.NodeRegistration{
		NodeID: nodeID, PubKey: pubHex, BridgeToken: token, HW: "test-hw",
		Offers: []protocol.ModelOffer{{Model: "m", Ctx: 4096}}, TS: time.Now().Unix(),
	}
	reg.SignRegistration(priv)
	body, _ := json.Marshal(reg)
	w := httptest.NewRecorder()
	b.register(w, httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body)))
	return w.Code
}

// heartbeat posts a heartbeat for nodeID with the given Bearer token, returns status.
func heartbeat(b *broker, nodeID, token string) int {
	body, _ := json.Marshal(map[string]string{"node_id": nodeID})
	r := httptest.NewRequest(http.MethodPost, "/nodes/heartbeat", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	b.heartbeat(w, r)
	return w.Code
}

// onAir reports whether pick considers the node serving model "m" right now (the
// same liveness gate /discover + /market use).
func (b *broker) onAir(model string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, _, ok := b.pick(model, false, 0, 0, 0, "", nil, nil)
	return ok
}

// TestNodeRegistrationPersistsAndRehydrates is the core fix: a registration lands in
// the store, and a SIMULATED broker restart (a fresh broker re-hydrating from the
// SAME store) still knows the node - the registry is no longer wiped by a restart.
func TestNodeRegistrationPersistsAndRehydrates(t *testing.T) {
	db := store.NewMem()
	_, priv, _ := ed25519.GenerateKey(nil)

	b := newBroker(db)
	if c := registerNode(t, b, "n1", "tok-1", priv); c != http.StatusOK {
		t.Fatalf("register = %d, want 200", c)
	}
	// It persisted to the store.
	recs, _ := db.AllNodes()
	if len(recs) != 1 || recs[0].NodeID != "n1" {
		t.Fatalf("store should hold 1 node n1, got %+v", recs)
	}
	if recs[0].Reg.BridgeToken != "tok-1" {
		t.Errorf("persisted bridge token = %q, want tok-1", recs[0].Reg.BridgeToken)
	}

	// Simulate a broker restart: a brand-new broker over the SAME store. Without
	// persistence this registry would be empty (the old bug).
	b2 := newBroker(db)
	b2.rehydrateNodes()
	if _, ok := b2.nodes["n1"]; !ok {
		t.Fatal("after restart, the re-hydrated broker must still know node n1")
	}
	if b2.tunnels["n1"] == nil || b2.tunnels["n1"].token != "tok-1" {
		t.Fatal("re-hydrated tunnel must carry the persisted bridge token (so the node's ongoing heartbeat still authenticates)")
	}
}

// TestRehydratedNodeGoesOnAirOnNextHeartbeat: a re-hydrated node is NOT trusted as
// on-air purely from the persisted record once that record is stale; its NEXT
// heartbeat (no re-register) re-confirms liveness and it reappears on-air.
func TestRehydratedNodeGoesOnAirOnNextHeartbeat(t *testing.T) {
	db := store.NewMem()
	_, priv, _ := ed25519.GenerateKey(nil)

	// Persist a node whose last_seen is OLD (older than the TTL): it represents a
	// provider that was on air before a restart but whose persisted liveness has aged.
	reg := protocol.NodeRegistration{
		NodeID: "n1", PubKey: hex.EncodeToString(priv.Public().(ed25519.PublicKey)),
		BridgeToken: "tok-1", Offers: []protocol.ModelOffer{{Model: "m"}},
	}
	stale := time.Now().Add(-2 * nodeTTL).Unix()
	if err := db.UpsertNode(store.NodeRecord{NodeID: "n1", Reg: reg, LastSeen: stale}); err != nil {
		t.Fatal(err)
	}

	b := newBroker(db)
	b.rehydrateNodes()
	// Known, but NOT on-air yet (truthful: a stale persisted last_seen does not make
	// a node falsely on-air).
	if _, ok := b.nodes["n1"]; !ok {
		t.Fatal("re-hydrate should know n1")
	}
	if b.onAir("m") {
		t.Fatal("a re-hydrated node with a STALE last_seen must NOT be shown on-air")
	}
	// Its next heartbeat (authenticated by the persisted token, NO re-register) flips
	// it on-air within seconds.
	if c := heartbeat(b, "n1", "tok-1"); c != http.StatusOK {
		t.Fatalf("heartbeat with the persisted token = %d, want 200 (no re-register needed)", c)
	}
	if !b.onAir("m") {
		t.Fatal("after one heartbeat the re-hydrated node must be on-air again")
	}
}

// TestNodeNotOnAirWithoutRecentHeartbeat: liveness stays truthful - a node whose last
// heartbeat is older than the TTL is not served, even though it is still registered.
func TestNodeNotOnAirWithoutRecentHeartbeat(t *testing.T) {
	db := store.NewMem()
	_, priv, _ := ed25519.GenerateKey(nil)
	b := newBroker(db)
	if c := registerNode(t, b, "n1", "tok-1", priv); c != http.StatusOK {
		t.Fatalf("register = %d, want 200", c)
	}
	if !b.onAir("m") {
		t.Fatal("freshly registered node should be on-air")
	}
	// Age its liveness past the TTL: no recent heartbeat -> not on-air (truthful).
	b.mu.Lock()
	b.lastSeen["n1"] = time.Now().Add(-2 * nodeTTL)
	b.mu.Unlock()
	if b.onAir("m") {
		t.Fatal("a node with no recent heartbeat must NOT be shown on-air")
	}
}

// TestRehydratedRecentNodeStaysOnAir: a node that was seen RECENTLY (within the TTL
// grace) right before a restart re-hydrates as on-air immediately, so the restart
// window does not flicker-drop a still-running provider.
func TestRehydratedRecentNodeStaysOnAir(t *testing.T) {
	db := store.NewMem()
	_, priv, _ := ed25519.GenerateKey(nil)
	reg := protocol.NodeRegistration{
		NodeID: "n1", PubKey: hex.EncodeToString(priv.Public().(ed25519.PublicKey)),
		BridgeToken: "tok-1", Offers: []protocol.ModelOffer{{Model: "m"}},
	}
	if err := db.UpsertNode(store.NodeRecord{NodeID: "n1", Reg: reg, LastSeen: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	b := newBroker(db)
	b.rehydrateNodes()
	if !b.onAir("m") {
		t.Fatal("a recently-seen re-hydrated node should be on-air across the restart window")
	}
}

// TestReregisterRefreshesPersistedToken: auto-re-register (belt-and-suspenders) must
// still work AND update the persisted token, so a node that re-registers with a fresh
// token survives a subsequent restart with that token.
func TestReregisterRefreshesPersistedToken(t *testing.T) {
	db := store.NewMem()
	_, priv, _ := ed25519.GenerateKey(nil)
	b := newBroker(db)
	if c := registerNode(t, b, "n1", "tok-1", priv); c != http.StatusOK {
		t.Fatalf("first register = %d, want 200", c)
	}
	// Re-register the same node id with the same key but a NEW token (what the agent's
	// reregistrar does after a broker restart).
	if c := registerNode(t, b, "n1", "tok-2", priv); c != http.StatusOK {
		t.Fatalf("re-register = %d, want 200", c)
	}
	recs, _ := db.AllNodes()
	if len(recs) != 1 || recs[0].Reg.BridgeToken != "tok-2" {
		t.Fatalf("persisted token after re-register = %+v, want tok-2", recs)
	}
	// The old token no longer authenticates; the new one does.
	if c := heartbeat(b, "n1", "tok-1"); c != http.StatusUnauthorized {
		t.Errorf("heartbeat with the old token = %d, want 401", c)
	}
	if c := heartbeat(b, "n1", "tok-2"); c != http.StatusOK {
		t.Errorf("heartbeat with the refreshed token = %d, want 200", c)
	}
}
