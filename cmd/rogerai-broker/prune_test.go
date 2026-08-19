package main

import (
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"rogerai.fm/roger/v5/internal/protocol"
	"rogerai.fm/roger/v5/internal/store"
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

// EVERY PER-NODE MAP GOES, INCLUDING THE ONES ADDED AFTER THIS SWEEP WAS WRITTEN.
//
// The prune deletes from a hand-maintained list of maps, which is a shape that rots: a map added
// later is correct everywhere except in the one place that cleans it up, and nothing fails. Two
// had rotted. b.netBucket - the observed network locality bucket, added with the locality work -
// held a prefix for every node the registry had already forgotten, forever. And b.edgeCanary is
// keyed by STATION rather than by node, so no node-id delete could ever reach it; it is aged out
// on its own evidence instead, and that sweep must not be conditional on some node happening to
// be stale, because the two go stale independently.
func TestPruneStaleNodesDropsTheLocalityAndCanaryMaps(t *testing.T) {
	b := pruneTestBroker()
	b.netBucket = map[string]string{}
	b.edgeCanary = map[string]edgeCanaryHealth{}
	now := time.Now()

	dead, alive := "dead-node", "live-node"
	for _, id := range []string{dead, alive} {
		b.nodes[id] = protocol.NodeRegistration{NodeID: id, Offers: []protocol.ModelOffer{{Model: "m"}}}
		b.netBucket[id] = "198.51.100.0/24"
	}
	b.lastSeen[dead] = now.Add(-staleNodeTTL - time.Hour)
	b.lastSeen[alive] = now.Add(-time.Minute)

	// Two canary readings: one for a Station probed recently, one nobody has looked at since
	// before the horizon.
	b.edgeCanary["st-recent"] = edgeCanaryHealth{at: now.Add(-time.Minute)}
	b.edgeCanary["st-ancient"] = edgeCanaryHealth{at: now.Add(-staleNodeTTL - time.Hour)}

	require.Equal(t, 1, b.pruneStaleNodes(now))

	_, keptDead := b.netBucket[dead]
	require.False(t, keptDead, "the pruned node's observed network prefix was retained forever")
	_, keptAlive := b.netBucket[alive]
	require.True(t, keptAlive, "a live node's locality was pruned with somebody else's")

	_, ancient := b.edgeCanary["st-ancient"]
	require.False(t, ancient, "a canary reading nobody has refreshed since the horizon is kept forever")
	_, recent := b.edgeCanary["st-recent"]
	require.True(t, recent, "a fresh canary reading was swept with the stale ones")
}

// AND THE STATION-KEYED SWEEP RUNS WHEN NO NODE IS STALE, which is the ordinary case and the one
// where the map grows fastest. Putting it after the early return would have made it dead code on
// a healthy fleet.
func TestTheCanaryMapIsAgedEvenWhenNoNodeIsPruned(t *testing.T) {
	b := pruneTestBroker()
	b.edgeCanary = map[string]edgeCanaryHealth{
		"st-ancient": {at: time.Now().Add(-staleNodeTTL - time.Hour)},
	}
	require.Zero(t, b.pruneStaleNodes(time.Now()), "no node should have been pruned")
	require.Empty(t, b.edgeCanary, "the canary sweep only runs when some node happens to be stale")
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
