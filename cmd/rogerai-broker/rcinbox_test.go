package main

import (
	"crypto/ed25519"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"rogerai.fm/roger/v6/internal/protocol"
	"rogerai.fm/roger/v6/internal/store"
)

// A session's delivery worker on an instance that no longer has its viewers, host polls or
// frames exits and is forgotten, so peers do not keep a goroutine per session forever.
func TestRCWorkerExitsWhenIdle(t *testing.T) {
	defer func(d time.Duration) { rcRouteRefresh = d }(rcRouteRefresh)
	rcRouteRefresh = 20 * time.Millisecond
	mr := miniredis.RunT(t)
	_, priv, _ := ed25519.GenerateKey(nil)
	b := newQBroker(t, priv, store.NewMem(), mr, dispatchViaQueueOnly)
	q := b.dqueue()
	raw, _ := json.Marshal(protocol.RCFrame{Kind: protocol.RCKindAssistant, Seq: 1, Text: "x"})
	b.rcFrameArrived(q, "rcs_idle", raw)
	h := b.rcHubFor("rcs_idle")
	h.mu.Lock()
	started := h.rem != nil
	h.mu.Unlock()
	if !started {
		t.Fatal("a frame did not start a delivery worker")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		h.mu.Lock()
		gone := h.rem == nil
		h.mu.Unlock()
		if gone {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("an idle session's worker never exited")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A wake-up for a session with no hub on this instance creates nothing.
func TestRCHostWakeCreatesNoHub(t *testing.T) {
	b := &broker{}
	b.rcHostWake("rcs_none")
	if len(b.rcHubs) != 0 {
		t.Fatalf("a wake-up created %d hubs", len(b.rcHubs))
	}
}
