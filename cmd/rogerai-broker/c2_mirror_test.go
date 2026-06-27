package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// TestMultiInstanceRegistryMirrorEnablesCrossInstance is the C2 regression guard. Unlike
// the older rendezvous test (which seeded the node on BOTH instances and so masked the
// prod break), here the node registers on instance A ONLY and is mirrored to B via the
// shared registry. This reproduces production (a node dials ONE instance, DO load-
// balances each request) and proves the fix: B can PICK the node and ACCEPT its poll/
// result (before the fix, B 503'd on pick and 404'd on poll - "unknown node").
func TestMultiInstanceRegistryMirrorEnablesCrossInstance(t *testing.T) {
	mr := miniredis.RunT(t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()
	a := newMIBroker(t, brokerPriv, db, mr)
	bInst := newMIBroker(t, brokerPriv, db, mr)

	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(nodePub)
	const token = "tok-mirror"
	offers := []protocol.ModelOffer{{Model: "free-m"}} // free => no hold

	// Register on A ONLY + mirror to the shared registry exactly as register() now does.
	miRegisterNode(a, "n1", pubHex, token, offers)
	raw, _ := json.Marshal(a.nodes["n1"])
	if err := a.shared.putNode("n1", raw, livenessTTL); err != nil {
		t.Fatal(err)
	}
	if _, ok := bInst.tunnels["n1"]; ok {
		t.Fatal("precondition: B must NOT know n1 before the mirror sync")
	}

	// The registry mirror brings n1 + its bridge token to B.
	bInst.syncRegistry()
	bInst.mu.Lock()
	tun := bInst.tunnels["n1"]
	_, known := bInst.nodes["n1"]
	bInst.mu.Unlock()
	if !known {
		t.Fatal("after syncRegistry B must know n1 (pickFor would 503 otherwise)")
	}
	if tun == nil || tun.token != token {
		t.Fatalf("after syncRegistry B must hold n1's tunnel + bridge token (agentPoll/agentResult 404'd otherwise); tun=%+v", tun)
	}

	// End to end: the provider long-polls instance B (which learned n1 ONLY via the
	// mirror); A dispatches the relay over the bus; B serves; the result returns to A.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		job, ok := pollOnce(t, bInst, "n1", token)
		if !ok {
			t.Errorf("B poll got no job - cross-instance dispatch did not arrive via the mirror")
			return
		}
		res := miSignedResult(job.ID, "n1", "free-m", "served via mirror", nodePriv, 200)
		rb, _ := json.Marshal(res)
		rr := httptest.NewRequest(http.MethodPost, "/agent/result?node=n1", bytes.NewReader(rb))
		rr.Header.Set("Authorization", miNodeBearer(token))
		rw := httptest.NewRecorder()
		bInst.agentResult(rw, rr)
		if rw.Code != http.StatusOK {
			t.Errorf("B agentResult = %d, want 200", rw.Code)
		}
	}()
	time.Sleep(150 * time.Millisecond)

	_, userPriv, _ := ed25519.GenerateKey(nil)
	body := []byte(`{"model":"free-m","max_tokens":8}`)
	r := miSignedRelayReq(t, userPriv, body, nil)
	w := httptest.NewRecorder()
	a.relay(w, r)
	wg.Wait()

	if w.Code != http.StatusOK {
		t.Fatalf("relay on A = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "served via mirror") {
		t.Errorf("relay body = %q, want the completion served cross-instance on B", w.Body.String())
	}
}

// TestMarkSeenKeepsRegistryIndexAlive is the regression guard for the DEFERRED C2 break
// caught in audit: a node registers once (putNode sets reg:<node> AND the regset index,
// both with livenessTTL=10m) and then only HEARTBEATS - it never re-registers. If markSeen
// refreshes reg:<node> but not the regset index that allNodes() enumerates through, the
// index expires after 10m, allNodes() returns empty, and a peer that restarts/scales out
// later can no longer learn the node (503/404) even though it is alive and heartbeating.
// This drives ~1h of heartbeats well past the original TTL and asserts the node survives
// in the index. Without the markSeen regset PExpire this fails at the 10m mark.
func TestMarkSeenKeepsRegistryIndexAlive(t *testing.T) {
	mr := miniredis.RunT(t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()
	a := newMIBroker(t, brokerPriv, db, mr)
	bInst := newMIBroker(t, brokerPriv, db, mr)

	nodePub, _, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(nodePub)
	offers := []protocol.ModelOffer{{Model: "free-m"}}
	miRegisterNode(a, "n1", pubHex, "tok", offers)
	raw, _ := json.Marshal(a.nodes["n1"])
	if err := a.shared.putNode("n1", raw, livenessTTL); err != nil {
		t.Fatal(err)
	}

	hb, ok := a.shared.(interface {
		markSeen(string, time.Time) error
	})
	if !ok {
		t.Fatal("shared store does not expose markSeen")
	}

	// 12 heartbeats, 5m apart (< livenessTTL each step, but cumulatively ~1h - far past
	// the original 10m putNode TTL). A live node re-registers rarely; only the heartbeat
	// keeps it on-air. Each step must refresh BOTH reg:<node> and the regset index.
	for i := 0; i < 12; i++ {
		mr.FastForward(5 * time.Minute)
		if err := hb.markSeen("n1", time.Now()); err != nil {
			t.Fatalf("markSeen #%d: %v", i, err)
		}
	}

	regs, err := a.shared.allNodes()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := regs["n1"]; !ok {
		t.Fatalf("allNodes() lost n1 after ~1h of heartbeats (regset index expired) - the mirror would silently drop a LIVE node; got %d entries", len(regs))
	}
	// A peer that only now syncs (e.g. just scaled out) must still re-learn the live node.
	bInst.syncRegistry()
	bInst.mu.Lock()
	_, known := bInst.nodes["n1"]
	bInst.mu.Unlock()
	if !known {
		t.Fatal("peer could not re-learn n1 from the registry index after heartbeats (scale-out > TTL would 503)")
	}
}

// TestSyncRegistryConfidentialSeedsAttestation guards the minor audit finding: a mirrored
// confidential node must get its re-attestation clock seeded (b.attestedAt) just like a
// locally-registered one, else cross-instance confidential routing sees a zero clock and
// treats the node as never-attested.
func TestSyncRegistryConfidentialSeedsAttestation(t *testing.T) {
	mr := miniredis.RunT(t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()
	a := newMIBroker(t, brokerPriv, db, mr)
	bInst := newMIBroker(t, brokerPriv, db, mr)

	nodePub, _, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(nodePub)
	reg := protocol.NodeRegistration{
		NodeID:       "c1",
		PubKey:       pubHex,
		BridgeToken:  "tok-conf",
		Confidential: true,
		Offers:       []protocol.ModelOffer{{Model: "free-m"}},
	}
	raw, _ := json.Marshal(reg)
	if err := a.shared.putNode("c1", raw, livenessTTL); err != nil {
		t.Fatal(err)
	}

	bInst.syncRegistry()
	bInst.mu.Lock()
	defer bInst.mu.Unlock()
	if !bInst.confidential["c1"] {
		t.Fatal("mirrored node must be marked confidential on the peer")
	}
	if bInst.attestedAt["c1"].IsZero() {
		t.Fatal("mirrored confidential node must have its attestation clock seeded (was zero) - cross-instance confidential routing would treat it as never-attested")
	}
}

// TestRehydrateRepublishesToSharedRegistry guards the restart/redeploy gap: after an
// instance restarts it rehydrates registrations from Postgres, and (multi-instance) must
// re-publish PUBLIC nodes to the shared registry so a peer can re-learn a heartbeat-only
// node whose shared reg key lapsed while this instance was down. Private nodes must NOT be
// published. Every DO deploy rolls instances, so this is the common path.
func TestRehydrateRepublishesToSharedRegistry(t *testing.T) {
	mr := miniredis.RunT(t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()

	pubReg := protocol.NodeRegistration{NodeID: "pub1", BridgeToken: "tok-pub", Offers: []protocol.ModelOffer{{Model: "free-m"}}}
	privReg := protocol.NodeRegistration{NodeID: "priv1", BridgeToken: "tok-priv", Private: true, Offers: []protocol.ModelOffer{{Model: "free-m"}}}
	if err := db.UpsertNode(store.NodeRecord{NodeID: "pub1", Reg: pubReg, LastSeen: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertNode(store.NodeRecord{NodeID: "priv1", Reg: privReg, LastSeen: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}

	a := newMIBroker(t, brokerPriv, db, mr)
	a.rehydrateNodes()

	regs, err := a.shared.allNodes()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := regs["pub1"]; !ok {
		t.Fatal("rehydrate did not re-publish the public node to the shared registry - a peer could not re-learn it after a redeploy")
	}
	if _, ok := regs["priv1"]; ok {
		t.Fatal("rehydrate must NOT publish a PRIVATE node to the shared registry")
	}

	// A peer learns the re-published node end to end (registry mirror -> known + tunnel).
	bInst := newMIBroker(t, brokerPriv, db, mr)
	bInst.syncRegistry()
	bInst.mu.Lock()
	tun := bInst.tunnels["pub1"]
	_, known := bInst.nodes["pub1"]
	bInst.mu.Unlock()
	if !known || tun == nil || tun.token != "tok-pub" {
		t.Fatalf("peer did not re-learn the rehydrated node (known=%v tun=%+v)", known, tun)
	}
}

// TestMultiInstanceProbeCrossesBus guards a multi-instance scalability bug: liveness
// probes must dispatch over the Valkey bus like relays, not via the local tunnel only.
// The provider polls instance B; the probe ORIGINATES on instance A where the node is
// only a mirrored stub with no local poller. Before the fix, probeNode sent to A's local
// stub channel (nobody draining), timed out after 30s, and recorded probeDead - falsely
// failing a perfectly healthy node and deprioritizing it. With the fix the probe reaches
// the provider on B over the bus and records ALIVE.
func TestMultiInstanceProbeCrossesBus(t *testing.T) {
	mr := miniredis.RunT(t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()
	a := newMIBroker(t, brokerPriv, db, mr)     // probe originates here
	bInst := newMIBroker(t, brokerPriv, db, mr) // provider polls here

	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(nodePub)
	const token = "tok-probe"
	offers := []protocol.ModelOffer{{Model: "free-m"}}

	miRegisterNode(bInst, "p1", pubHex, token, offers)
	raw, _ := json.Marshal(bInst.nodes["p1"])
	if err := bInst.shared.putNode("p1", raw, livenessTTL); err != nil {
		t.Fatal(err)
	}
	a.syncRegistry()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		job, ok := pollOnce(t, bInst, "p1", token)
		if !ok {
			t.Errorf("provider on B got no probe job - the probe did not cross the bus")
			return
		}
		res := miSignedResult(job.ID, "p1", "free-m", "pong", nodePriv, 200)
		rb, _ := json.Marshal(res)
		rr := httptest.NewRequest(http.MethodPost, "/agent/result?node=p1", bytes.NewReader(rb))
		rr.Header.Set("Authorization", miNodeBearer(token))
		rw := httptest.NewRecorder()
		bInst.agentResult(rw, rr)
		if rw.Code != http.StatusOK {
			t.Errorf("B agentResult = %d, want 200", rw.Code)
		}
	}()
	time.Sleep(150 * time.Millisecond)

	a.mu.Lock()
	reg := a.nodes["p1"]
	a.mu.Unlock()
	a.probeNode(reg, "free-m", canaryFingerprint{prompt: "ping", expect: "pong"})
	wg.Wait()

	a.metricsMu.Lock()
	tq := a.trust["p1"]
	a.metricsMu.Unlock()
	if !tq.probed {
		t.Fatal("probe outcome was never recorded")
	}
	if !tq.probeOK {
		t.Fatal("cross-instance probe recorded as FAILED (probeDead) - it did not reach the provider over the bus; a healthy node would be falsely deprioritized")
	}
	if tq.probeFails != 0 {
		t.Fatalf("probeFails=%d, want 0 for a live cross-instance probe", tq.probeFails)
	}
}
