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

// A snapshot whose reads fail must not move the log position: the next tick retries it
// instead of skipping every change until the next reconcile.
func TestChangeSnapshotFailureKeepsPosition(t *testing.T) {
	mr := miniredis.RunT(t)
	_, priv, _ := ed25519.GenerateKey(nil)
	b := newMIBroker(t, priv, store.NewMem(), mr)
	mr.Set(keyPrefix+"regset", "not-a-set") // SMEMBERS now fails with WRONGTYPE
	b.syncChanges()
	if b.changes.last != "" || b.changes.snapshots.Load() != 0 {
		t.Fatalf("a failed snapshot advanced the position to %q (%d snapshots)", b.changes.last, b.changes.snapshots.Load())
	}
	mr.Del(keyPrefix + "regset")
	b.syncChanges()
	if b.changes.last == "" || b.changes.snapshots.Load() != 1 {
		t.Fatalf("the retry did not snapshot (last %q, %d snapshots)", b.changes.last, b.changes.snapshots.Load())
	}
}

// A registration a snapshot skips only for the local grace window is kept and applied once
// the grace has passed, so a peer's newer token is not lost until the next reconcile.
func TestChangeSnapshotDefersGracedRegistration(t *testing.T) {
	mr := miniredis.RunT(t)
	_, priv, _ := ed25519.GenerateKey(nil)
	b := newMIBroker(t, priv, store.NewMem(), mr)
	b.localRegAt = map[string]time.Time{}
	local := protocol.NodeRegistration{NodeID: "n1", BridgeToken: "local", TS: time.Now().Unix()}
	b.nodes["n1"] = local
	b.localRegAt["n1"] = time.Now()
	peer := local
	peer.BridgeToken, peer.TS = "peer", local.TS+5
	raw, _ := json.Marshal(peer)
	if err := b.shared.putNode("n1", raw, livenessTTL); err != nil {
		t.Fatal(err)
	}
	b.syncChanges() // the boot snapshot, inside the grace window
	if got := b.nodes["n1"].BridgeToken; got != "local" {
		t.Fatalf("the snapshot overwrote a registration inside its grace window (token %q)", got)
	}
	b.localRegAt["n1"] = time.Now().Add(-2 * syncLocalRegisterGrace)
	b.syncChanges() // an ordinary tick after the grace
	if got := b.nodes["n1"].BridgeToken; got != "peer" {
		t.Fatalf("the peer's newer registration was lost after the grace (token %q)", got)
	}
}
