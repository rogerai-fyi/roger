package main

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

func pruneTestBroker() *broker {
	_, priv, _ := ed25519.GenerateKey(nil)
	return &broker{
		db:            store.NewMem(),
		priv:          priv,
		nodes:         map[string]protocol.NodeRegistration{},
		tunnels:       map[string]*nodeTunnel{},
		lastSeen:      map[string]time.Time{},
		confidential:  map[string]bool{},
		private:       map[string]bool{},
		bandOf:        map[string]string{},
		tps:           map[string]float64{},
		inflight:      map[string]int{},
		success:       map[string]float64{},
		trust:         map[string]trustState{},
		successCount:  map[string]int{},
		concurrentTPS: map[string]float64{},
		pubOfUser:     map[string]string{},
	}
}

// TestPruneStaleNodes: a node offline longer than staleNodeTTL is removed from the
// registry AND the store; a recently-offline node (still shown as ○) is kept. Earnings
// and the owner binding survive the prune (only the registration row is dropped).
func TestPruneStaleNodes(t *testing.T) {
	b := pruneTestBroker()
	now := time.Now()

	// Dead: old hostname-style id, last seen well past the prune TTL.
	dead := "demo-mac-studio"
	b.nodes[dead] = protocol.NodeRegistration{NodeID: dead, Offers: []protocol.ModelOffer{{Model: "m"}}}
	b.lastSeen[dead] = now.Add(-staleNodeTTL - time.Hour)
	b.tps[dead] = 12
	_ = b.db.UpsertNode(store.NodeRecord{NodeID: dead, Reg: b.nodes[dead], LastSeen: b.lastSeen[dead].Unix()})
	_ = b.db.BindNode(dead, "owner-pubkey")                               // owner binding (separate table)
	_, _ = b.db.Settle("u_consumer", dead, 0, 0, protocol.UsageReceipt{}) // some earnings history

	// Recently offline: a callsign node off for 2 minutes - must NOT be pruned.
	recent := "eager-puma-54"
	b.nodes[recent] = protocol.NodeRegistration{NodeID: recent, Offers: []protocol.ModelOffer{{Model: "m"}}}
	b.lastSeen[recent] = now.Add(-2 * time.Minute)
	_ = b.db.UpsertNode(store.NodeRecord{NodeID: recent, Reg: b.nodes[recent], LastSeen: b.lastSeen[recent].Unix()})

	if n := b.pruneStaleNodes(now); n != 1 {
		t.Fatalf("pruned %d, want 1 (only the dead node)", n)
	}
	if _, ok := b.nodes[dead]; ok {
		t.Fatal("dead node still in the in-memory registry")
	}
	if _, ok := b.tps[dead]; ok {
		t.Fatal("dead node metric (tps) not cleaned up")
	}
	if _, ok := b.nodes[recent]; !ok {
		t.Fatal("recently-offline node was wrongly pruned")
	}
	// Persistent registration gone for the dead node, kept for the recent one.
	recs, _ := b.db.AllNodes()
	for _, r := range recs {
		if r.NodeID == dead {
			t.Fatal("dead node still persisted in the store")
		}
	}
	// Owner binding survives (historical attribution intact).
	if acct, ok, _ := b.db.AccountOfNode(dead); !ok || acct != "owner-pubkey" {
		t.Fatalf("owner binding lost on prune: acct=%q ok=%v (want owner-pubkey/true)", acct, ok)
	}
}

// TestPruneStaleNodesDisabled: a zero/negative TTL is a no-op (the env opt-out).
func TestPruneStaleNodesDisabled(t *testing.T) {
	old := staleNodeTTL
	staleNodeTTL = 0
	defer func() { staleNodeTTL = old }()

	b := pruneTestBroker()
	b.nodes["x"] = protocol.NodeRegistration{NodeID: "x"}
	b.lastSeen["x"] = time.Now().Add(-100 * 24 * time.Hour)
	if n := b.pruneStaleNodes(time.Now()); n != 0 {
		t.Fatalf("disabled prune removed %d, want 0", n)
	}
	if _, ok := b.nodes["x"]; !ok {
		t.Fatal("disabled prune still deleted a node")
	}
}
