package main

// private_flip_test.go guards a regression introduced by the private-band cross-instance
// mirror (P1-3): a node can re-register to FLIP between private and public (register re-
// asserts the signed Private flag every time). Because markSeen now keeps BOTH namespaces'
// TTLs alive, a stale entry in the OPPOSITE namespace would otherwise linger and a peer would
// mis-classify the node after a flip. The fix: register clears the opposite shared namespace
// (dropSharedNode) and syncRegistry's public pass clears any stale in-memory private flag.

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// registerNodeAs registers nodeID with a fixed node key + owner key and the given Private
// flag, through the REAL register handler (owner-signed — required for private, harmless for
// public). Same node key across calls = a TOFU-valid re-register (a flip).
func registerNodeAs(t *testing.T, b *broker, nodeID, token string, nodePub, nodePriv ed25519.PrivateKey, ownerPriv ed25519.PrivateKey, private bool) int {
	t.Helper()
	reg := protocol.NodeRegistration{
		NodeID: nodeID, PubKey: hex.EncodeToString(nodePub.Public().(ed25519.PublicKey)),
		BridgeToken: token, HW: "hw", Offers: []protocol.ModelOffer{{Model: "m", Ctx: 4096}},
		TS: time.Now().Unix(), Private: private,
	}
	reg.SignRegistration(nodePriv)
	body, _ := json.Marshal(reg)
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
	signReq(r, ownerPriv, body)
	w := httptest.NewRecorder()
	b.register(w, r)
	b.markSeen(nodeID)
	return w.Code
}

func flipPair(t *testing.T) (a, bInst *broker, nodePriv, ownerPriv ed25519.PrivateKey) {
	t.Helper()
	mr := miniredis.RunT(t)
	_, brokerPriv, _ := ed25519.GenerateKey(nil)
	db := store.NewMem()
	a = newMIBroker(t, brokerPriv, db, mr)
	bInst = newMIBroker(t, brokerPriv, db, mr)
	_, nodePriv, _ = ed25519.GenerateKey(nil)
	_, ownerPriv, _ = ed25519.GenerateKey(nil)
	if err := db.BindOwner(store.Owner{GitHubID: 1, Login: "o", Pubkey: hex.EncodeToString(ownerPriv.Public().(ed25519.PublicKey))}); err != nil {
		t.Fatal(err)
	}
	return a, bInst, nodePriv, ownerPriv
}

func bDiscoverHas(b *broker, node string) bool {
	res, _ := b.computeDiscover().(map[string]any)
	offers, _ := res["offers"].([]offerView)
	for _, o := range offers {
		if o.NodeID == node {
			return true
		}
	}
	return false
}

// TestPrivateToPublicFlipUnhidesOnPeer: a private band node that re-registers PUBLIC must
// become visible on a peer (not stuck hidden by a stale private mirror), and must leave the
// private shared namespace.
func TestPrivateToPublicFlipUnhidesOnPeer(t *testing.T) {
	a, bInst, nodePriv, ownerPriv := flipPair(t)

	if c := registerNodeAs(t, a, "p1", "tok-p1", nodePriv, nodePriv, ownerPriv, true); c != http.StatusOK {
		t.Fatalf("private register = %d", c)
	}
	bInst.syncLivenessOnce()
	bInst.mu.Lock()
	priv0 := bInst.private["p1"]
	bInst.mu.Unlock()
	if !priv0 {
		t.Fatal("precondition: B should first see p1 as private")
	}

	// FLIP to public (same node key).
	if c := registerNodeAs(t, a, "p1", "tok-p1", nodePriv, nodePriv, ownerPriv, false); c != http.StatusOK {
		t.Fatalf("public re-register = %d", c)
	}
	bInst.syncLivenessOnce()

	bInst.mu.Lock()
	priv1 := bInst.private["p1"]
	bInst.mu.Unlock()
	if priv1 {
		t.Error("p1 flipped to PUBLIC but instance B still flags it private (stale private mirror not cleared)")
	}
	if !bDiscoverHas(bInst, "p1") {
		t.Error("p1 flipped to PUBLIC but is absent from instance B's /discover")
	}
	if pregs, _ := a.shared.allPrivateNodes(); pregs["p1"] != nil {
		t.Error("p1 still in the PRIVATE shared namespace after going public (stale entry kept alive by markSeen)")
	}
}

// TestPublicToPrivateFlipLeavesPublicRegistry: a public node that re-registers PRIVATE must
// disappear from the PUBLIC shared registry (allNodes) and from a peer's /discover - no leak.
func TestPublicToPrivateFlipLeavesPublicRegistry(t *testing.T) {
	a, bInst, nodePriv, ownerPriv := flipPair(t)

	if c := registerNodeAs(t, a, "q1", "tok-q1", nodePriv, nodePriv, ownerPriv, false); c != http.StatusOK {
		t.Fatalf("public register = %d", c)
	}
	bInst.syncLivenessOnce()
	if !bDiscoverHas(bInst, "q1") {
		t.Fatal("precondition: B should first see q1 in /discover")
	}

	// FLIP to private.
	if c := registerNodeAs(t, a, "q1", "tok-q1", nodePriv, nodePriv, ownerPriv, true); c != http.StatusOK {
		t.Fatalf("private re-register = %d", c)
	}
	bInst.syncLivenessOnce()

	if pub, _ := a.shared.allNodes(); pub["q1"] != nil {
		t.Error("q1 still in the PUBLIC shared registry after going private (would surface to a public allNodes consumer)")
	}
	if bDiscoverHas(bInst, "q1") {
		t.Error("q1 still in instance B's /discover after going private (leak)")
	}
}
