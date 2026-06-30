package main

import (
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// TestBannedOwnerNodeSet covers the precomputed owner-ban filter that replaced the
// per-candidate AccountOfNode call under metricsMu (the routing cliff): nil (zero work) when
// no owner is banned, and exactly the banned owner's nodes otherwise. The resolution happens
// OUTSIDE the lock so a single banned owner no longer serializes every pick on store I/O.
func TestBannedOwnerNodeSet(t *testing.T) {
	now := time.Now()
	mem := store.NewMem()
	if err := mem.BindNode("n1", "ownerA"); err != nil {
		t.Fatal(err)
	}
	if err := mem.BindNode("n2", "ownerB"); err != nil {
		t.Fatal(err)
	}
	b := routeBroker(now, map[string]protocol.NodeRegistration{
		"n1": {NodeID: "n1"}, "n2": {NodeID: "n2"},
	})
	b.db = mem
	b.bannedOwners = map[string]bool{}

	if got := b.bannedOwnerNodeSet(); got != nil {
		t.Errorf("bannedOwnerNodeSet with no bans = %v, want nil (zero work)", got)
	}

	b.bannedOwners["ownerA"] = true
	got := b.bannedOwnerNodeSet()
	if !got["n1"] || got["n2"] {
		t.Errorf("bannedOwnerNodeSet = %v, want only n1 (ownerA banned)", got)
	}
}

// TestCachedOwnerOfGuards covers the nil-db / empty-node guards (tests run with no store).
func TestCachedOwnerOfGuards(t *testing.T) {
	b := &broker{}
	if _, ok := b.cachedOwnerOf("n1"); ok {
		t.Error("cachedOwnerOf with nil db must return ok=false")
	}
	if _, ok := (&broker{db: store.NewMem()}).cachedOwnerOf(""); ok {
		t.Error("cachedOwnerOf with empty node must return ok=false")
	}
}

// TestPickForExcludesBannedOwnerNode is the end-to-end regression: with ownerA banned, the
// pick drops ownerA's node and routes to ownerB's, via the precomputed set (no per-candidate
// store round-trip under metricsMu).
func TestPickForExcludesBannedOwnerNode(t *testing.T) {
	now := time.Now()
	mem := store.NewMem()
	_ = mem.BindNode("n1", "ownerA")
	_ = mem.BindNode("n2", "ownerB")
	nodes := map[string]protocol.NodeRegistration{
		"n1": {NodeID: "n1", Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 0.5, PriceOut: 0.5}}},
		"n2": {NodeID: "n2", Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 0.5, PriceOut: 0.5}}},
	}
	b := routeBroker(now, nodes)
	b.db = mem
	b.bannedOwners = map[string]bool{"ownerA": true}

	b.mu.Lock()
	picked, _, ok := b.pickFor("m", false, 0, 0, 0, "", nil, nil, nil, pickReq{})
	b.mu.Unlock()
	if !ok || picked.NodeID != "n2" {
		t.Fatalf("pickFor = %q ok=%v, want n2 (ownerA banned, n1 must be excluded)", picked.NodeID, ok)
	}
}
