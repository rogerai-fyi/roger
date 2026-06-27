package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

func deadProbeBroker() *broker {
	return &broker{
		nodes: map[string]protocol.NodeRegistration{}, tunnels: map[string]*nodeTunnel{},
		lastSeen: map[string]time.Time{}, confidential: map[string]bool{}, private: map[string]bool{},
		tps: map[string]float64{}, success: map[string]float64{}, trust: map[string]trustState{},
		inflight: map[string]int{}, concurrentTPS: map[string]float64{}, successCount: map[string]int{},
		banned: map[string]bool{}, bannedOwners: map[string]bool{},
	}
}

// TestProbeDeadExcludedFromPickAndOffline locks fix #1: a node that has failed a sustained
// streak of liveness probes (its model upstream is dead/unloaded) is EXCLUDED from pick
// (so a relay returns a clean no-station instead of a 504) and shown OFFLINE on /discover,
// while a healthy node serving the same model is still picked + online. Recovery (one OK
// probe → streak 0) restores it.
func TestProbeDeadExcludedFromPickAndOffline(t *testing.T) {
	b := deadProbeBroker()
	pub, _, _ := ed25519.GenerateKey(nil)
	hexPub := hex.EncodeToString(pub)
	now := time.Now()

	// One node serving model "m", heartbeat-fresh but probe-DEAD (upstream not serving).
	b.nodes["dead"] = protocol.NodeRegistration{NodeID: "dead", PubKey: hexPub, Offers: []protocol.ModelOffer{{Model: "m"}}}
	b.lastSeen["dead"] = now
	b.trust["dead"] = trustState{probed: true, probeFails: probeDeadStreak}

	// Excluded from pick -> no station serving (a clean 503, not a dispatch into a 504).
	if _, _, ok := b.pick("m", false, 0, 0, 0, "", nil, nil, nil); ok {
		t.Fatal("a probe-dead node must be EXCLUDED from pick (else relays dispatch into a 504)")
	}
	// Shown OFFLINE on the discover/market offer view.
	offers := b.enrichOffersForNode(nil, b.nodes["dead"], now, nil, false)
	if len(offers) != 1 || offers[0].Online {
		t.Fatalf("probe-dead node must be Online=false in /discover, got %+v", offers)
	}

	// A healthy node serving the same model IS picked + online.
	b.nodes["good"] = protocol.NodeRegistration{NodeID: "good", PubKey: hexPub, Offers: []protocol.ModelOffer{{Model: "m"}}}
	b.lastSeen["good"] = now
	b.trust["good"] = trustState{probed: true, probeOK: true, probeFails: 0}
	if reg, _, ok := b.pick("m", false, 0, 0, 0, "", nil, nil, nil); !ok || reg.NodeID != "good" {
		t.Fatalf("pick must select the healthy node, got ok=%v node=%q", ok, reg.NodeID)
	}
	good := b.enrichOffersForNode(nil, b.nodes["good"], now, nil, false)
	if len(good) != 1 || !good[0].Online {
		t.Fatalf("healthy node must be Online=true, got %+v", good)
	}

	// Recovery: one OK probe resets the streak -> the previously-dead node is eligible again.
	b.trust["dead"] = trustState{probed: true, probeOK: true, probeFails: 0}
	if rec := b.enrichOffersForNode(nil, b.nodes["dead"], now, nil, false); len(rec) != 1 || !rec[0].Online {
		t.Fatalf("a recovered node must be Online=true again, got %+v", rec)
	}
}
