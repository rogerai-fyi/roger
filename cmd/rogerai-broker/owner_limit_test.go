package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// registerPriced posts a signed, OWNER-BOUND (priced) registration for nodeID and
// returns the HTTP status + the broker's error message (when rejected). A priced
// offer makes the node owner-bound, which is what the per-owner on-air cap counts.
func registerPriced(t *testing.T, b *broker, nodeID string, nodePriv ed25519.PrivateKey, nodePubHex string, userPriv ed25519.PrivateKey) (int, string) {
	t.Helper()
	reg := protocol.NodeRegistration{
		NodeID: nodeID, PubKey: nodePubHex, BridgeToken: "tok-" + nodeID, TS: time.Now().Unix(),
		Offers: []protocol.ModelOffer{{Model: "m", Ctx: 4096, PriceOut: 1.0}},
	}
	reg.SignRegistration(nodePriv)
	body, _ := json.Marshal(reg)
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
	signReq(r, userPriv, body)
	w := httptest.NewRecorder()
	b.register(w, r)
	var resp struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return w.Code, resp.Error.Message
}

// TestOwnerOnAirCap: the broker rejects the (max+1)th SIMULTANEOUSLY on-air node per
// owner, allows it again after one ages off air, and an idempotent re-register of an
// existing node does NOT double-count.
func TestOwnerOnAirCap(t *testing.T) {
	b, userPriv, nodePriv, nodePubHex := newBandBroker(t)
	b.maxNodesPerOwner = 3 // small cap for the test

	// Fill the cap: 3 distinct on-air nodes for the one owner.
	for _, id := range []string{"n1", "n2", "n3"} {
		if code, msg := registerPriced(t, b, id, nodePriv, nodePubHex, userPriv); code != http.StatusOK {
			t.Fatalf("register %s = %d (%q), want 200", id, code, msg)
		}
	}

	// The 4th distinct node is rejected with the station-limit message.
	code, msg := registerPriced(t, b, "n4", nodePriv, nodePubHex, userPriv)
	if code != http.StatusTooManyRequests {
		t.Fatalf("4th node = %d, want 429", code)
	}
	if !strings.Contains(msg, "station limit reached") || !strings.Contains(msg, "take one off air") {
		t.Errorf("reject message = %q, want the station-limit copy", msg)
	}

	// Idempotent re-register of an EXISTING node (n1) must NOT count as a new one even
	// though the owner is already at the cap (a node refreshing itself is never rejected).
	if code, msg := registerPriced(t, b, "n1", nodePriv, nodePubHex, userPriv); code != http.StatusOK {
		t.Fatalf("idempotent re-register of n1 at the cap = %d (%q), want 200", code, msg)
	}

	// Free a slot: age n2 off air (push its last_seen past the TTL). Now the 4th node
	// fits, since only 2 of the owner's nodes are still live.
	b.mu.Lock()
	b.lastSeen["n2"] = time.Now().Add(-2 * nodeTTL)
	b.mu.Unlock()
	if code, msg := registerPriced(t, b, "n4", nodePriv, nodePubHex, userPriv); code != http.StatusOK {
		t.Fatalf("4th node after one aged off air = %d (%q), want 200", code, msg)
	}
}

// TestOwnerOnAirCapDisabled: a 0 cap disables the per-owner backstop entirely.
func TestOwnerOnAirCapDisabled(t *testing.T) {
	b, userPriv, nodePriv, nodePubHex := newBandBroker(t)
	b.maxNodesPerOwner = 0 // disabled
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		if code, _ := registerPriced(t, b, id, nodePriv, nodePubHex, userPriv); code != http.StatusOK {
			t.Fatalf("register %s with cap disabled = %d, want 200", id, code)
		}
	}
}

// TestOwnerOnAirCapPerOwner: the cap is PER OWNER - a different account's on-air nodes
// do not count against this owner's slots.
func TestOwnerOnAirCapPerOwner(t *testing.T) {
	b, userPriv, nodePriv, nodePubHex := newBandBroker(t)
	b.maxNodesPerOwner = 2

	// A second owner with their own node, pre-bound + live, must not consume owner-1's slots.
	_, other2Priv, _ := ed25519.GenerateKey(nil)
	other2Pub := hex.EncodeToString(other2Priv.Public().(ed25519.PublicKey))
	_ = b.db.BindOwner(store.Owner{GitHubID: 2, Login: "owner2", Pubkey: other2Pub})
	if code, _ := registerPriced(t, b, "o2n1", nodePriv, nodePubHex, other2Priv); code != http.StatusOK {
		t.Fatalf("owner2 node = %d, want 200", code)
	}

	// Owner-1 can still fill its own 2 slots despite owner-2's live node.
	if code, _ := registerPriced(t, b, "n1", nodePriv, nodePubHex, userPriv); code != http.StatusOK {
		t.Fatalf("owner1 n1 = %d, want 200", code)
	}
	if code, _ := registerPriced(t, b, "n2", nodePriv, nodePubHex, userPriv); code != http.StatusOK {
		t.Fatalf("owner1 n2 = %d, want 200", code)
	}
	if code, _ := registerPriced(t, b, "n3", nodePriv, nodePubHex, userPriv); code != http.StatusTooManyRequests {
		t.Fatalf("owner1 n3 (over own cap) = %d, want 429", code)
	}
}
