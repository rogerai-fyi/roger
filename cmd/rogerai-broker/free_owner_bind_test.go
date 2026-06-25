package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// registerFreeOwned posts a FREE (all-zero-price, public) node registration that
// CARRIES a valid owner signature (X-Roger-Pubkey/-TS/-Sig). It is the new case the
// bind fix targets: a logged-in owner's free supply. Returns status + error message.
func registerFreeOwned(t *testing.T, b *broker, nodeID string, nodePriv ed25519.PrivateKey, nodePubHex string, userPriv ed25519.PrivateKey, ip string) (int, string) {
	t.Helper()
	reg := protocol.NodeRegistration{
		NodeID: nodeID, PubKey: nodePubHex, BridgeToken: "tok-" + nodeID, TS: time.Now().Unix(),
		Offers: []protocol.ModelOffer{{Model: "m", Ctx: 4096}}, // FREE: no price, not private
	}
	reg.SignRegistration(nodePriv) // proof-of-possession of the NODE key
	body, _ := json.Marshal(reg)
	r := httptest.NewRequest(http.MethodPost, "/nodes/register", bytes.NewReader(body))
	signReq(r, userPriv, body) // OWNER signature on the request
	if ip != "" {
		r.Header.Set("CF-Connecting-IP", ip)
	}
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

// TestFreeOwnerSignedRegistrationBinds: a FREE, non-private node that arrives
// owner-signed is BOUND to the owner account (AccountOfNode resolves the owner;
// NodesOfAccount includes the node) - this is what lets account grant keys find a
// logged-in owner's free supply.
func TestFreeOwnerSignedRegistrationBinds(t *testing.T) {
	b, userPriv, nodePriv, nodePubHex := newBandBroker(t)
	ownerPub := ownerPubFor(t, userPriv)

	if code, msg := registerFreeOwned(t, b, "free1", nodePriv, nodePubHex, userPriv, "203.0.113.1"); code != http.StatusOK {
		t.Fatalf("free owner-signed register = %d (%q), want 200", code, msg)
	}

	// AccountOfNode resolves the owner.
	acct, ok, err := b.db.AccountOfNode("free1")
	if err != nil || !ok {
		t.Fatalf("AccountOfNode(free1) ok=%v err=%v, want bound", ok, err)
	}
	if acct != ownerPub {
		t.Errorf("AccountOfNode(free1) = %q, want owner pubkey %q", acct, ownerPub)
	}
	// NodesOfAccount includes the node.
	nodes, err := b.db.NodesOfAccount(ownerPub)
	if err != nil {
		t.Fatalf("NodesOfAccount err=%v", err)
	}
	if !contains(nodes, "free1") {
		t.Errorf("NodesOfAccount(owner) = %v, want it to include free1", nodes)
	}
}

// TestAnonFreeRegistrationStaysUnbound: an ANONYMOUS free registration (no owner
// signature) is UNBOUND (AccountOfNode finds nothing) and is subject to the per-IP
// free-reg ceiling, NOT the per-owner cap. The anonymous path is unchanged by the fix.
func TestAnonFreeRegistrationStaysUnbound(t *testing.T) {
	b := newCeilingBroker(t)
	b.freeRegPerIP = 2
	b.freeRegWindow = time.Hour

	// Two anon free nodes from one IP pass; the third NEW one trips the per-IP ceiling.
	if code, _ := registerFree(t, b, "anon1", "198.51.100.5"); code != http.StatusOK {
		t.Fatalf("anon free anon1 = %d, want 200", code)
	}
	if code, _ := registerFree(t, b, "anon2", "198.51.100.5"); code != http.StatusOK {
		t.Fatalf("anon free anon2 = %d, want 200", code)
	}
	if code, msg := registerFree(t, b, "anon3", "198.51.100.5"); code != http.StatusTooManyRequests {
		t.Errorf("anon free anon3 = %d (%q), want 429 (per-IP ceiling, NOT per-owner cap)", code, msg)
	}
	// And it never got bound to any account.
	if _, ok, _ := b.db.AccountOfNode("anon1"); ok {
		t.Errorf("anon free anon1 is BOUND, want UNBOUND (anonymous free has no owner)")
	}
}

// TestBannedOwnerFreeNodeBlocked: a banned owner's FREE owner-signed node is BLOCKED
// at register - binding makes the durable owner ban apply to free supply too.
func TestBannedOwnerFreeNodeBlocked(t *testing.T) {
	b, userPriv, nodePriv, nodePubHex := newBandBroker(t)
	ownerPub := ownerPubFor(t, userPriv)
	b.banOwner(ownerPub, "test ban", "")

	code, msg := registerFreeOwned(t, b, "banned1", nodePriv, nodePubHex, userPriv, "203.0.113.2")
	if code != http.StatusForbidden {
		t.Fatalf("banned owner free node = %d (%q), want 403", code, msg)
	}
	// And it was NOT bound (ban is enforced BEFORE BindNode).
	if _, ok, _ := b.db.AccountOfNode("banned1"); ok {
		t.Errorf("banned owner free node got BOUND, want rejected before bind")
	}
}

// TestPerOwnerCapAppliesToFreeOwnerBoundNodes: free owner-bound nodes count toward the
// per-owner on-air cap (NOT the per-IP free ceiling). The (cap+1)th free owner-signed
// node is rejected with the station-limit copy.
func TestPerOwnerCapAppliesToFreeOwnerBoundNodes(t *testing.T) {
	b, userPriv, nodePriv, nodePubHex := newBandBroker(t)
	b.maxNodesPerOwner = 2

	if code, msg := registerFreeOwned(t, b, "f1", nodePriv, nodePubHex, userPriv, "203.0.113.3"); code != http.StatusOK {
		t.Fatalf("free owner node f1 = %d (%q), want 200", code, msg)
	}
	if code, msg := registerFreeOwned(t, b, "f2", nodePriv, nodePubHex, userPriv, "203.0.113.3"); code != http.StatusOK {
		t.Fatalf("free owner node f2 = %d (%q), want 200", code, msg)
	}
	// The 3rd free owner-bound node trips the PER-OWNER cap (not the per-IP ceiling).
	code, msg := registerFreeOwned(t, b, "f3", nodePriv, nodePubHex, userPriv, "203.0.113.3")
	if code != http.StatusTooManyRequests {
		t.Fatalf("3rd free owner node f3 = %d (%q), want 429", code, msg)
	}
	if !containsSub(msg, "station limit reached") {
		t.Errorf("reject message = %q, want the station-limit copy (per-owner cap, not per-IP)", msg)
	}
}

// ownerPubFor returns the hex owner pubkey the test owner is bound under (matches
// newBandBroker's BindOwner). The owner account id is the signing pubkey hex.
func ownerPubFor(t *testing.T, userPriv ed25519.PrivateKey) string {
	t.Helper()
	return hex.EncodeToString(userPriv.Public().(ed25519.PublicKey))
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func containsSub(s, sub string) bool { return bytes.Contains([]byte(s), []byte(sub)) }
