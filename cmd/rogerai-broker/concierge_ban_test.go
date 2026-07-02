package main

// concierge_ban_test.go pins that the homepage-Ping dogfood pick paths never select a
// report-banned node (audit finding #9). pickFreeStation/pickGrantStation checked only
// liveness, unlike the paid pickFor which drops b.banned + banned-owner nodes - so a banned
// node was still served on the free Ping surface. Real broker state, no mocks.

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

func conciergeNode(b *broker, id, model string) {
	pub, _, _ := ed25519.GenerateKey(nil)
	b.nodes[id] = protocol.NodeRegistration{NodeID: id, PubKey: hex.EncodeToString(pub), Offers: []protocol.ModelOffer{{Model: model}}}
	b.lastSeen[id] = time.Now()
}

func TestPickFreeStationSkipsBanned(t *testing.T) {
	b := newConciergeBroker()
	b.banned = map[string]bool{}
	conciergeNode(b, "free", "free-m")

	if _, _, ok := b.pickFreeStation(); !ok {
		t.Fatal("precondition: a fresh free node should be pickable")
	}
	b.banned["free"] = true
	if _, _, ok := b.pickFreeStation(); ok {
		t.Error("a report-banned node must NOT be dogfooded via the free Ping pick")
	}
}

func TestPickGrantStationSkipsBanned(t *testing.T) {
	b := newConciergeBroker()
	b.banned = map[string]bool{}
	conciergeNode(b, "gnode", "grant-m")
	allow := map[string]bool{"gnode": true}

	if _, ok := b.pickGrantStation(allow, "grant-m"); !ok {
		t.Fatal("precondition: a fresh in-allow node should be pickable")
	}
	b.banned["gnode"] = true
	if _, ok := b.pickGrantStation(allow, "grant-m"); ok {
		t.Error("a report-banned node must NOT be dogfooded via the grant Ping pick")
	}
}
