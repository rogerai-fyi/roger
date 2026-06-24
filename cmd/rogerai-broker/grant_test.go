package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

func grantBroker() *broker {
	return &broker{
		db:      store.NewMem(),
		grantRL: loadRateLimiter(),
		rl:      loadRateLimiter(),
		feeRate: 0.30,
	}
}

// reqWithGrant builds a relay request carrying a grant bearer token.
func reqWithGrant(secret string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	r.Header.Set("Authorization", "Bearer "+secret)
	return r
}

// TestResolveGrant covers grant auth: a live grant resolves to its owner's nodes,
// a revoked/expired/unknown one 401s, and a non-grant request falls through.
func TestResolveGrant(t *testing.T) {
	b := grantBroker()
	owner := "ownerpubkey"
	_ = b.db.BindNode("node-a", owner)
	_ = b.db.BindNode("node-b", owner)
	_ = b.db.BindNode("node-other", "someoneelse")

	secret := "rog-grant_secret123"
	sum := sha256.Sum256([]byte(secret))
	g := store.Grant{ID: "grant_1", SecretHash: hex.EncodeToString(sum[:]), Owner: owner, Label: "petlings", Free: true}
	_ = b.db.CreateGrant(g)

	gc, ok, err := b.resolveGrant(reqWithGrant(secret))
	if !ok || err != "" {
		t.Fatalf("live grant: ok=%v err=%q", ok, err)
	}
	if gc.wallet != "g_grant_1" {
		t.Fatalf("grant wallet = %q, want g_grant_1", gc.wallet)
	}
	// Owner's nodes only (node-other excluded).
	if !gc.nodeAllow["node-a"] || !gc.nodeAllow["node-b"] || gc.nodeAllow["node-other"] {
		t.Fatalf("node allow-set wrong: %v", gc.nodeAllow)
	}

	// No grant token -> fall through (ok=false, no error).
	if _, ok, err := b.resolveGrant(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)); ok || err != "" {
		t.Fatalf("non-grant request should fall through: ok=%v err=%q", ok, err)
	}

	// Unknown secret -> 401.
	if _, ok, err := b.resolveGrant(reqWithGrant("rog-grant_nope")); ok || err == "" {
		t.Fatalf("unknown grant should 401")
	}

	// Revoked -> 401.
	_, _ = b.db.SetGrantRevoked("grant_1", owner, true)
	if _, ok, err := b.resolveGrant(reqWithGrant(secret)); ok || err == "" {
		t.Fatalf("revoked grant should 401")
	}
}

// TestGrantNodeScoping: a grant restricted to a subset of the owner's nodes only
// reaches that subset.
func TestGrantNodeScoping(t *testing.T) {
	b := grantBroker()
	owner := "op"
	_ = b.db.BindNode("a", owner)
	_ = b.db.BindNode("b", owner)
	secret := "rog-grant_x"
	sum := sha256.Sum256([]byte(secret))
	_ = b.db.CreateGrant(store.Grant{ID: "g1", SecretHash: hex.EncodeToString(sum[:]), Owner: owner, Nodes: []string{"a"}, Free: true})
	gc, ok, _ := b.resolveGrant(reqWithGrant(secret))
	if !ok || !gc.nodeAllow["a"] || gc.nodeAllow["b"] {
		t.Fatalf("node-restricted grant allow = %v", gc.nodeAllow)
	}
	if gc.modelDenied("anything") {
		t.Fatalf("empty models means any model is allowed")
	}
	gc.grant.Models = []string{"only-this"}
	if !gc.modelDenied("other") || gc.modelDenied("only-this") {
		t.Fatalf("model gating wrong")
	}
}

// TestGrantCapCheck enforces the daily token cap.
func TestGrantCapCheck(t *testing.T) {
	b := grantBroker()
	g := store.Grant{ID: "gc", DailyCap: 100}
	if st, _ := b.grantCapCheck(g); st != 0 {
		t.Fatalf("under cap should pass, got %d", st)
	}
	_ = b.db.AddGrantUsage("gc", 100, time.Now())
	if st, _ := b.grantCapCheck(g); st != http.StatusTooManyRequests {
		t.Fatalf("at cap should 429, got %d", st)
	}
	if st, _ := b.grantCapCheck(store.Grant{ID: "nocap"}); st != 0 {
		t.Fatalf("no cap should always pass")
	}
}

// TestResolvePricing covers the three billing branches.
func TestResolvePricing(t *testing.T) {
	b := grantBroker()
	owner := "opk"
	_ = b.db.BindNode("mynode", owner)
	node := protocol.NodeRegistration{NodeID: "mynode"}
	offer := protocol.ModelOffer{Model: "m", PriceIn: 0.2, PriceOut: 0.5}

	// Free grant -> $0 metering only.
	gcFree := grantContext{grant: store.Grant{ID: "g", Owner: owner, Free: true}, wallet: "g_g"}
	if p := b.resolvePricing(gcFree, true, "g_g", "g_g", node, offer); !p.free || !p.fixed || p.payer != "g_g" {
		t.Fatalf("free grant pricing = %+v", p)
	}
	// Priced grant -> owner sponsors (owner consumer wallet), fixed price.
	gcPriced := grantContext{grant: store.Grant{ID: "g2", Owner: owner, PriceIn: 0.1, PriceOut: 0.3}, wallet: "g_g2"}
	p := b.resolvePricing(gcPriced, true, "g_g2", "g_g2", node, offer)
	if p.free || !p.fixed || p.out != 0.3 || p.payer != protocol.UserIDFromPubkey(owner) {
		t.Fatalf("priced grant pricing = %+v", p)
	}
	// Signed self-use: the caller-owner consuming their own node -> $0.
	selfUser := protocol.UserIDFromPubkey(owner)
	if sp := b.resolvePricing(grantContext{}, false, selfUser, selfUser, node, offer); !sp.free || !sp.fixed {
		t.Fatalf("self-use should be free: %+v", sp)
	}
	// Public market: a stranger -> not fixed, not free (relay applies market price),
	// billed to the resolved wallet (here == the signed id, a logged-out keypair).
	if pp := b.resolvePricing(grantContext{}, false, "u_stranger", "u_stranger", node, offer); pp.free || pp.fixed || pp.payer != "u_stranger" {
		t.Fatalf("public traffic should bill market price: %+v", pp)
	}
}

// TestReservedGrantID: the g_ namespace is reserved (an unsigned header can't claim it).
func TestReservedGrantID(t *testing.T) {
	if !reservedID("g_grant_abc") {
		t.Fatalf("g_ ids must be reserved against unsigned impersonation")
	}
}
