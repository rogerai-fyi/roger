package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// instmetrics_test.go validates the MULTI-INSTANCE OBSERVABILITY counters: that local vs.
// cross-instance (bus) dispatch, no-poller, and bus-error outcomes are each counted on the
// relay path, that the Valkey op-error counter funnels through noteErr, and that the
// counters surface on the admin overview only in multi-instance mode (single-instance is
// byte-for-byte unchanged - no new keys).

// TestDispatchCounterBus: a successful cross-instance dispatch (poller on instance B)
// increments busDispatch on the ORIGINATING instance A and leaves localDispatch at 0.
func TestDispatchCounterBus(t *testing.T) {
	mr := miniredis.RunT(t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()
	a := newMIBroker(t, brokerPriv, db, mr)
	bInst := newMIBroker(t, brokerPriv, db, mr)

	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(nodePub)
	const token = "tok-n1"
	offers := []protocol.ModelOffer{{Model: "free-m"}}
	miRegisterNode(a, "n1", pubHex, token, offers)
	miRegisterNode(bInst, "n1", pubHex, token, offers)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		job, ok := pollOnce(t, bInst, "n1", token)
		if !ok {
			t.Errorf("instance B poll got no job")
			return
		}
		res := miSignedResult(job.ID, "n1", "free-m", "hi", nodePriv, 200)
		rb, _ := json.Marshal(res)
		rr := httptest.NewRequest(http.MethodPost, "/agent/result?node=n1", bytes.NewReader(rb))
		rr.Header.Set("Authorization", miNodeBearer(token))
		bInst.agentResult(httptest.NewRecorder(), rr)
	}()
	time.Sleep(150 * time.Millisecond)

	_, userPriv, _ := ed25519.GenerateKey(nil)
	r := miSignedRelayReq(t, userPriv, []byte(`{"model":"free-m","max_tokens":8}`), nil)
	w := httptest.NewRecorder()
	a.relay(w, r)
	wg.Wait()

	if w.Code != http.StatusOK {
		t.Fatalf("relay = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if got := a.stats.busDispatch.Load(); got != 1 {
		t.Errorf("busDispatch = %d, want 1", got)
	}
	if got := a.stats.localDispatch.Load(); got != 0 {
		t.Errorf("localDispatch = %d, want 0 (multi-instance dispatches only over the bus)", got)
	}
	if got := a.stats.busNoPoller.Load() + a.stats.busDispatchErr.Load(); got != 0 {
		t.Errorf("no-poller/err counters = %d, want 0 on a successful dispatch", got)
	}
}

// TestDispatchCounterNoPoller: a bus dispatch with NO poller on any instance increments
// busNoPoller (the cross-instance equivalent of a full local queue) and returns 503.
func TestDispatchCounterNoPoller(t *testing.T) {
	mr := miniredis.RunT(t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	a := newMIBroker(t, brokerPriv, store.NewMem(), mr)
	nodePub, _, _ := ed25519.GenerateKey(nil)
	miRegisterNode(a, "n1", hex.EncodeToString(nodePub), "tok", []protocol.ModelOffer{{Model: "free-m"}})

	_, userPriv, _ := ed25519.GenerateKey(nil)
	r := miSignedRelayReq(t, userPriv, []byte(`{"model":"free-m","max_tokens":8}`), nil)
	w := httptest.NewRecorder()
	a.relay(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("relay = %d, want 503", w.Code)
	}
	if got := a.stats.busNoPoller.Load(); got != 1 {
		t.Errorf("busNoPoller = %d, want 1", got)
	}
	if got := a.stats.busDispatch.Load(); got != 0 {
		t.Errorf("busDispatch = %d, want 0 (no poller delivered)", got)
	}
}

// TestDispatchCounterBusErr: a DEAD bus increments busDispatchErr and fails cleanly.
func TestDispatchCounterBusErr(t *testing.T) {
	mr := miniredis.RunT(t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()
	a := newMIBroker(t, brokerPriv, db, mr)

	_, userPriv, _ := ed25519.GenerateKey(nil)
	userPubHex := hex.EncodeToString(userPriv.Public().(ed25519.PublicKey))
	_ = db.BindOwner(store.Owner{GitHubID: 7, Login: "octocat", Pubkey: userPubHex})
	_, _ = db.BalanceOf("u_gh_7", 50)
	nodePub, _, _ := ed25519.GenerateKey(nil)
	miRegisterNode(a, "n1", hex.EncodeToString(nodePub), "tok",
		[]protocol.ModelOffer{{Model: "paid-m", PriceOut: 0.5}})

	mr.Close() // kill the bus

	r := miSignedRelayReq(t, userPriv, []byte(`{"model":"paid-m","max_tokens":8}`), nil)
	w := httptest.NewRecorder()
	a.relay(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("relay = %d, want 503", w.Code)
	}
	if got := a.stats.busDispatchErr.Load(); got < 1 {
		t.Errorf("busDispatchErr = %d, want >= 1 on a dead bus", got)
	}
}

// TestDispatchCounterLocal: the SINGLE-INSTANCE path increments localDispatch and never
// touches the bus counters (byte-for-byte: only an atomic int moves).
func TestDispatchCounterLocal(t *testing.T) {
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()
	b := newMIBroker(t, brokerPriv, db, nil) // mr==nil -> shared==nil, multiInstance==false

	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(nodePub)
	const token = "tok-n1"
	miRegisterNode(b, "n1", pubHex, token, []protocol.ModelOffer{{Model: "free-m"}})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		job, ok := pollOnce(t, b, "n1", token)
		if !ok {
			t.Errorf("local poll got no job")
			return
		}
		res := miSignedResult(job.ID, "n1", "free-m", "local hi", nodePriv, 200)
		rb, _ := json.Marshal(res)
		rr := httptest.NewRequest(http.MethodPost, "/agent/result?node=n1", bytes.NewReader(rb))
		rr.Header.Set("Authorization", miNodeBearer(token))
		b.agentResult(httptest.NewRecorder(), rr)
	}()
	time.Sleep(100 * time.Millisecond)

	_, userPriv, _ := ed25519.GenerateKey(nil)
	r := miSignedRelayReq(t, userPriv, []byte(`{"model":"free-m","max_tokens":8}`), nil)
	w := httptest.NewRecorder()
	b.relay(w, r)
	wg.Wait()

	if w.Code != http.StatusOK {
		t.Fatalf("relay = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	if got := b.stats.localDispatch.Load(); got != 1 {
		t.Errorf("localDispatch = %d, want 1", got)
	}
	if got := b.stats.busDispatch.Load() + b.stats.busNoPoller.Load() + b.stats.busDispatchErr.Load(); got != 0 {
		t.Errorf("bus counters = %d, want 0 on the single-instance path", got)
	}
}

// TestValkeyOpErrorsCounter: every failed Valkey op funnels through noteErr and bumps the
// opErrors counter; a clean miss (redis.Nil) does NOT count.
func TestValkeyOpErrorsCounter(t *testing.T) {
	mr := miniredis.RunT(t)
	vs, err := newValkeyStore("redis://" + mr.Addr())
	if err != nil {
		t.Fatalf("newValkeyStore: %v", err)
	}
	defer vs.Close()

	// A clean miss must NOT count as an error.
	if _, found, err := vs.cacheGet("absent"); found || err != nil {
		t.Fatalf("cacheGet(absent) = found %v err %v, want clean miss", found, err)
	}
	if got := vs.opErrors.Load(); got != 0 {
		t.Fatalf("opErrors after a clean miss = %d, want 0", got)
	}

	// Kill the backend: the next op errors and must bump the counter.
	mr.Close()
	if _, _, err := vs.cacheGet("x"); err == nil {
		t.Fatal("cacheGet on a dead backend should error")
	}
	if got := vs.opErrors.Load(); got < 1 {
		t.Errorf("opErrors after a backend failure = %d, want >= 1", got)
	}
}

// TestAdminLiveMultiInstanceFields: the admin live feed exposes instance_id + the dispatch
// counters ONLY in multi-instance mode; the single-instance feed has neither key (so the
// single-instance response shape is unchanged).
func TestAdminLiveMultiInstanceFields(t *testing.T) {
	liveHealth := func(b *broker) map[string]any {
		b.adminKey = "admin-secret-key"
		r := httptest.NewRequest(http.MethodGet, "/admin/live", nil)
		r.Header.Set("X-Roger-Admin", "admin-secret-key")
		w := httptest.NewRecorder()
		b.adminLive(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("adminLive = %d, want 200 (body=%s)", w.Code, w.Body.String())
		}
		var resp struct {
			Health map[string]any `json:"health"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode overview: %v", err)
		}
		return resp.Health
	}

	// Multi-instance: both keys present.
	mr := miniredis.RunT(t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	mi := newMIBroker(t, brokerPriv, store.NewMem(), mr)
	mi.stats.busDispatch.Add(3)
	h := liveHealth(mi)
	if h["instance_id"] != mi.instanceID || mi.instanceID == "" {
		t.Errorf("instance_id = %v, want %q", h["instance_id"], mi.instanceID)
	}
	disp, ok := h["dispatch"].(map[string]any)
	if !ok {
		t.Fatalf("dispatch missing/!map: %v", h["dispatch"])
	}
	if disp["bus_dispatch"] != float64(3) { // JSON numbers decode to float64
		t.Errorf("dispatch.bus_dispatch = %v, want 3", disp["bus_dispatch"])
	}

	// Single-instance: neither key present (response shape unchanged).
	_, brokerPriv2, _ := ed25519.GenerateKey(nil)
	si := newMIBroker(t, brokerPriv2, store.NewMem(), nil)
	h2 := liveHealth(si)
	if _, present := h2["instance_id"]; present {
		t.Errorf("single-instance overview must NOT include instance_id, got %v", h2["instance_id"])
	}
	if _, present := h2["dispatch"]; present {
		t.Errorf("single-instance overview must NOT include dispatch, got %v", h2["dispatch"])
	}
}
