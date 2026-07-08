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

// TestMirrorToolsToSharedPrivateAndMissing covers mirrorToolsToShared's PRIVATE-namespace
// branch (a verified private band mirrors via putPrivateNode, never the public registry) and
// the missing-node early return (nothing to publish).
func TestMirrorToolsToSharedPrivateAndMissing(t *testing.T) {
	mr := miniredis.RunT(t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	b := newMIBroker(t, brokerPriv, store.NewMem(), mr)
	b.toolsOK = map[string]bool{}

	// Missing node: a clean no-op (no panic, nothing published).
	b.mirrorToolsToShared("ghost")
	if _, ok, _ := b.shared.getPrivateNode("ghost"); ok {
		t.Fatal("mirrorToolsToShared published a node that does not exist")
	}

	// A verified PRIVATE band mirrors into the private namespace with tools stamped.
	b.nodes["band1"] = protocol.NodeRegistration{NodeID: "band1", Private: true,
		Offers: []protocol.ModelOffer{{Model: "m", Ctx: 131072}}}
	b.metricsMu.Lock()
	b.toolsOK[toolKey("band1", "m")] = true
	b.metricsMu.Unlock()
	b.mirrorToolsToShared("band1")

	raw, ok, _ := b.shared.getPrivateNode("band1")
	if !ok {
		t.Fatal("verified private band was not mirrored into the private namespace")
	}
	var reg protocol.NodeRegistration
	_ = json.Unmarshal(raw, &reg)
	if len(reg.Offers) != 1 || !contains(protocol.CanonicalCapabilities(reg.Offers[0].Capabilities), protocol.CapTools) {
		t.Fatalf("private mirror did not stamp the verified tools bit: %+v", reg.Offers)
	}
	// It must NOT leak into the public registry.
	if _, pub, _ := b.shared.getNode("band1"); pub {
		t.Fatal("a private band leaked into the public shared registry")
	}
}
