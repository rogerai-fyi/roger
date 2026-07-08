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

// TestMultiInstanceToolCallCanaryCrossesBus mirrors TestMultiInstanceProbeCrossesBus for the
// TOOL-CALL canary: the provider polls instance B, the canary ORIGINATES on instance A (where
// the node is a mirrored stub with no local poller), so it must dispatch over the Valkey bus.
// The provider returns a well-formed tool_calls response and A records the verified tools bit.
func TestMultiInstanceToolCallCanaryCrossesBus(t *testing.T) {
	mr := miniredis.RunT(t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()
	a := newMIBroker(t, brokerPriv, db, mr)     // canary originates here
	bInst := newMIBroker(t, brokerPriv, db, mr) // provider polls here
	a.toolsOK = map[string]bool{}
	bInst.toolsOK = map[string]bool{}

	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	pubHex := hex.EncodeToString(nodePub)
	const token = "tok-toolprobe"
	offers := []protocol.ModelOffer{{Model: "free-m", Ctx: 131072}}

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
			t.Errorf("provider on B got no tool-call canary - it did not cross the bus")
			return
		}
		res := miSignedResult(job.ID, "p1", "free-m", "", nodePriv, 200)
		res.Body = []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"roger_probe_ack","arguments":"{\"ok\":true}"}}]}}]}`)
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
	a.probeToolCall(reg, "free-m", true)
	wg.Wait()

	a.metricsMu.Lock()
	ok := a.toolsOK[toolKey("p1", "free-m")]
	a.metricsMu.Unlock()
	if !ok {
		t.Fatal("cross-instance tool-call canary did not earn the tools bit over the bus")
	}
}

// TestToolsVerifiedHostClearPropagatesToPeer is the REGRESSION GUARD for the audit's major
// finding: a per-instance monotonic map let a peer keep surfacing "tools" after the
// authoritative host cleared a regressed verdict, and a re-register could resurrect it. With the
// verdict as first-class shared state, the host's clear propagates to the peer on its next sync.
func TestToolsVerifiedHostClearPropagatesToPeer(t *testing.T) {
	mr := miniredis.RunT(t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	host := newMIBroker(t, brokerPriv, store.NewMem(), mr) // authoritative poll host
	peer := newMIBroker(t, brokerPriv, store.NewMem(), mr) // mirroring peer
	for _, b := range []*broker{host, peer} {
		b.toolsOK = map[string]bool{}
		b.toolsMerged = map[string]bool{}
		b.nodes["n1"] = protocol.NodeRegistration{NodeID: "n1",
			Offers: []protocol.ModelOffer{{Model: "m", Ctx: 131072}}}
		b.lastSeen["n1"] = time.Now()
	}

	// Host proves tools; both instances see it via the shared union after a sync.
	host.recordToolProbe("n1", "m", true, false, true)
	peer.syncToolsVerified()
	if !contains(capsFor(peer, "n1", "m", time.Now()), protocol.CapTools) {
		t.Fatal("peer did not surface tools after the host proved it (shared union broken)")
	}

	// The model regresses; the AUTHORITATIVE host clears. The peer's own non-authoritative
	// definitive fail is a no-op AND its stale would-be-monotonic bit must not survive.
	peer.recordToolProbe("n1", "m", false, false, false) // peer: not authoritative -> no clear
	host.recordToolProbe("n1", "m", false, false, true)  // host: authoritative -> clears shared field
	peer.syncToolsVerified()

	if contains(capsFor(peer, "n1", "m", time.Now()), protocol.CapTools) {
		t.Fatal("peer STILL surfaces tools after the authoritative host cleared the regression (the audit bug)")
	}
	if contains(capsFor(host, "n1", "m", time.Now()), protocol.CapTools) {
		t.Fatal("host still surfaces tools after clearing its own verdict")
	}
}
