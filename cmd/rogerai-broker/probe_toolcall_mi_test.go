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

// TestToolsVerifiedClearsAfterRestart is the REGRESSION GUARD for the pre-push audit's second
// major: after a broker RESTART the local b.toolsOK is empty while the shared field is still set
// (a prior instance proved it). An authoritative definitive regression must STILL clear the
// shared field - the clear must NOT be gated on a local-map transition - or a regressed model
// stays falsely VERIFIED for up to toolsVerifiedTTL across the fleet.
func TestToolsVerifiedClearsAfterRestart(t *testing.T) {
	mr := miniredis.RunT(t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)

	// A prior instance proved the bit into the shared store.
	seed := newMIBroker(t, brokerPriv, store.NewMem(), mr)
	if err := seed.shared.markToolsVerified("n1", "m", toolsVerifiedTTL); err != nil {
		t.Fatal(err)
	}

	// A FRESH instance (post-restart): empty local toolsOK, shared field still set. It syncs the
	// bit into its merged union and surfaces it.
	fresh := newMIBroker(t, brokerPriv, store.NewMem(), mr)
	fresh.toolsOK, fresh.toolsMerged = map[string]bool{}, map[string]bool{}
	fresh.nodes["n1"] = protocol.NodeRegistration{NodeID: "n1", Offers: []protocol.ModelOffer{{Model: "m", Ctx: 131072}}}
	fresh.lastSeen["n1"] = time.Now()
	fresh.syncToolsVerified()
	if !contains(capsFor(fresh, "n1", "m", time.Now()), protocol.CapTools) {
		t.Fatal("fresh instance did not pick up the shared verified bit after restart")
	}

	// An authoritative definitive regression on the fresh instance (its local map never had the
	// bit) must clear the SHARED field, not silently skip because `changed` was false.
	fresh.recordToolProbe("n1", "m", false, false, true)
	got, err := fresh.shared.toolsVerified(toolsVerifiedTTL)
	if err != nil {
		t.Fatal(err)
	}
	if got[toolKey("n1", "m")] {
		t.Fatal("authoritative regression after restart did NOT clear the shared field (stale VERIFIED persists fleet-wide)")
	}
	fresh.syncToolsVerified()
	if contains(capsFor(fresh, "n1", "m", time.Now()), protocol.CapTools) {
		t.Fatal("fresh instance still surfaces tools after the post-restart authoritative regression")
	}
}

// TestMarkMeasuredRefreshesToolsShared covers the served-traffic refresh: a continuously-busy
// node (skipped by probeOnce) keeps its verified-tools shared field fresh from real traffic,
// throttled so the hot settle path does not write Valkey per request.
func TestMarkMeasuredRefreshesToolsShared(t *testing.T) {
	mr := miniredis.RunT(t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	b := newMIBroker(t, brokerPriv, store.NewMem(), mr)
	b.toolsOK, b.toolsMerged, b.lastToolMark = map[string]bool{}, map[string]bool{}, map[string]time.Time{}
	b.probe = probeConfig{interval: 30 * time.Second, ceiling: 15 * time.Minute}
	if b.localPollAt == nil {
		b.localPollAt = map[string]time.Time{}
	}
	b.localPollAt["n1"] = time.Now() // THIS instance hosts the poll => authoritative refresher
	b.metricsMu.Lock()
	b.toolsOK[toolKey("n1", "m")] = true
	b.metricsMu.Unlock()

	// First served request re-marks the shared field (nothing marked yet).
	b.markMeasured("n1")
	got, err := b.shared.toolsVerified(toolsVerifiedTTL)
	if err != nil {
		t.Fatal(err)
	}
	if !got[toolKey("n1", "m")] {
		t.Fatal("served traffic did not refresh the verified-tools shared field for a busy node")
	}

	// A second immediate served request is THROTTLED (no second write within toolsRefreshEvery).
	b.metricsMu.Lock()
	last := b.lastToolMark["n1"]
	b.metricsMu.Unlock()
	b.markMeasured("n1")
	b.metricsMu.Lock()
	again := b.lastToolMark["n1"]
	b.metricsMu.Unlock()
	if !again.Equal(last) {
		t.Fatal("served-traffic tool refresh was not throttled (wrote Valkey again within toolsRefreshEvery)")
	}
}

// TestMarkMeasuredNonAuthoritativePeerDoesNotRePoison is the REGRESSION GUARD for the pre-push
// audit's round-3 finding: a NON-authoritative peer must NOT refresh the shared verified-tools
// field from its own stale toolsOK. Scenario: the peer's probe once passed (peer.toolsOK=true),
// the authoritative host later cleared the regression from the shared store, then relayed traffic
// settles on the peer (markMeasured). The peer must NOT re-mark the cleared field.
func TestMarkMeasuredNonAuthoritativePeerDoesNotRePoison(t *testing.T) {
	mr := miniredis.RunT(t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	peer := newMIBroker(t, brokerPriv, store.NewMem(), mr)
	peer.toolsOK, peer.toolsMerged, peer.lastToolMark = map[string]bool{}, map[string]bool{}, map[string]time.Time{}
	peer.probe = probeConfig{interval: 30 * time.Second, ceiling: 15 * time.Minute}
	if peer.localPollAt == nil {
		peer.localPollAt = map[string]time.Time{}
	}
	// The peer's own earlier pass left a STALE local bit; it does NOT host the node's poll.
	peer.metricsMu.Lock()
	peer.toolsOK[toolKey("n1", "m")] = true
	peer.metricsMu.Unlock()
	// The shared field is CLEARED (the authoritative host retracted the regressed verdict).
	if err := peer.shared.clearToolsVerified("n1", "m"); err != nil {
		t.Fatal(err)
	}

	peer.markMeasured("n1") // relayed traffic settles on the non-authoritative peer

	got, err := peer.shared.toolsVerified(toolsVerifiedTTL)
	if err != nil {
		t.Fatal(err)
	}
	if got[toolKey("n1", "m")] {
		t.Fatal("a non-authoritative peer's served traffic RE-POISONED the host-cleared verdict (audit round-3 bug)")
	}
}
