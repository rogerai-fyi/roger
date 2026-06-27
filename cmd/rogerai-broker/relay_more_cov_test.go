package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// grantRelayNode wires one serving node bound to `owner` into b (registration + live
// tunnel + lastSeen), the shared shape every grant round-trip needs.
func grantRelayNode(t *testing.T, b *broker, db store.Store, nodeID, owner string) {
	t.Helper()
	nodePub, _, _ := ed25519.GenerateKey(nil)
	b.nodes[nodeID] = protocol.NodeRegistration{
		NodeID: nodeID, PubKey: hex.EncodeToString(nodePub),
		Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 7, PriceOut: 7, Ctx: 4096}},
	}
	b.lastSeen[nodeID] = time.Now()
	b.tunnels[nodeID] = &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	_ = db.BindNode(nodeID, owner)
}

// makeGrant stores a grant and returns the raw bearer secret for it.
func makeGrant(t *testing.T, db store.Store, g store.Grant, secret string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(secret))
	g.SecretHash = hex.EncodeToString(sum[:])
	if err := db.CreateGrant(g); err != nil {
		t.Fatal(err)
	}
	return secret
}

func grantPost(secret string) (*httptest.ResponseRecorder, *http.Request) {
	body := `{"model":"m","max_tokens":8}`
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+secret)
	return httptest.NewRecorder(), r
}

// TestRelayGrantInvalidToken locks the grant-auth failure path: a `rog-grant_...` bearer
// that resolves to no stored grant is rejected 401 before any pick/dispatch.
func TestRelayGrantInvalidToken(t *testing.T) {
	b := relayBroker(store.NewMem())
	w, r := grantPost("rog-grant_does_not_exist")
	b.relay(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unknown grant = %d, want 401: %s", w.Code, readBody(w))
	}
	if !strings.Contains(readBody(w), "invalid or revoked") {
		t.Errorf("body = %q, want invalid/revoked message", readBody(w))
	}
}

// TestRelayGrantRateLimited locks the per-grant rate-limit trip: once the grant's bucket
// is drained, the next relay is 429 with a Retry-After header (before dispatch).
func TestRelayGrantRateLimited(t *testing.T) {
	db := store.NewMem()
	b := relayBroker(db)
	b.grantRL = loadRateLimiter()
	owner := "ownerRL"
	grantRelayNode(t, b, db, "rl-node", owner)
	secret := makeGrant(t, db, store.Grant{ID: "g_rl", Owner: owner, Free: true, RPM: 1, Burst: 1}, "rog-grant_rl")

	// Drain the grant's single token so the relay call below trips.
	if ok, _ := b.grantRL.allowAt("g_rl", 1, 1); !ok {
		t.Fatal("precondition: first token must be granted")
	}
	w, r := grantPost(secret)
	b.relay(w, r)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("rate-limited grant = %d, want 429: %s", w.Code, readBody(w))
	}
	if w.Header().Get("Retry-After") == "" {
		t.Error("a 429 must carry a Retry-After header")
	}
}

// TestRelayGrantDailyCap locks the grant daily token cap: a grant whose day usage is at/
// over its DailyCap is rejected 429 before dispatch.
func TestRelayGrantDailyCap(t *testing.T) {
	db := store.NewMem()
	b := relayBroker(db)
	owner := "ownerCap"
	grantRelayNode(t, b, db, "cap-node", owner)
	secret := makeGrant(t, db, store.Grant{ID: "g_cap", Owner: owner, Free: true, RPM: 60, Burst: 10, DailyCap: 1}, "rog-grant_cap")
	_ = db.AddGrantUsage("g_cap", 5, time.Now()) // already over the daily cap

	w, r := grantPost(secret)
	b.relay(w, r)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("over-cap grant = %d, want 429: %s", w.Code, readBody(w))
	}
	if !strings.Contains(readBody(w), "daily token cap") {
		t.Errorf("body = %q, want daily-cap message", readBody(w))
	}
}

// TestRelayGrantModelDenied locks the grant model allow-list: a grant restricted to other
// models rejects model "m" with 403.
func TestRelayGrantModelDenied(t *testing.T) {
	db := store.NewMem()
	b := relayBroker(db)
	owner := "ownerMD"
	grantRelayNode(t, b, db, "md-node", owner)
	secret := makeGrant(t, db, store.Grant{ID: "g_md", Owner: owner, Free: true, RPM: 60, Burst: 10, Models: []string{"other-model"}}, "rog-grant_md")

	w, r := grantPost(secret)
	b.relay(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("model-denied grant = %d, want 403: %s", w.Code, readBody(w))
	}
	if !strings.Contains(readBody(w), "does not allow model m") {
		t.Errorf("body = %q, want model-denied message", readBody(w))
	}
}

// TestRelayGrantNoOwnerNode locks the empty-node-allow path: a grant whose owner has NO
// serving node yields an empty nodeAllow and a 503 (the grant can never reach another
// owner's hardware, so with none of its own there is nowhere to route).
func TestRelayGrantNoOwnerNode(t *testing.T) {
	db := store.NewMem()
	b := relayBroker(db)
	secret := makeGrant(t, db, store.Grant{ID: "g_none", Owner: "lonely-owner", Free: true, RPM: 60, Burst: 10}, "rog-grant_none")

	w, r := grantPost(secret)
	b.relay(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("ownerless grant = %d, want 503: %s", w.Code, readBody(w))
	}
	if !strings.Contains(readBody(w), "no node of this grant's owner") {
		t.Errorf("body = %q, want owner-no-node message", readBody(w))
	}
}

// TestRelayConfidentialNoNode locks the confidential-only pick miss: a signed request that
// demands X-Roger-Confidential but no node is attested confidential gets the 503 with the
// "on a confidential node" suffix.
func TestRelayConfidentialNoNode(t *testing.T) {
	db := store.NewMem()
	b := relayBroker(db)
	// A normal (non-confidential) node serving "m" - the confidential filter excludes it.
	nodePub, _, _ := ed25519.GenerateKey(nil)
	b.nodes["plain"] = protocol.NodeRegistration{
		NodeID: "plain", PubKey: hex.EncodeToString(nodePub),
		Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 5, PriceOut: 5, Ctx: 4096}},
	}
	b.lastSeen["plain"] = time.Now()
	b.tunnels["plain"] = &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	_ = db.BindNode("plain", "owner1")

	_, userPriv, _ := ed25519.GenerateKey(nil)
	body := []byte(`{"model":"m","max_tokens":8}`)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	signReq(r, userPriv, body)
	r.Header.Set("X-Roger-Confidential", "1")
	w := httptest.NewRecorder()
	b.relay(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("confidential-only relay = %d, want 503: %s", w.Code, readBody(w))
	}
	if !strings.Contains(readBody(w), "on a confidential node") {
		t.Errorf("body = %q, want confidential-node suffix", readBody(w))
	}
}

// TestRelayAnonPaidModelLoginPrompt locks the anon-spend gate: a signed but UNBOUND
// keypair (no GitHub login -> anon wallet) hitting a PAID public model is rejected 401
// with the login prompt, never silently seeding an anon wallet to spend.
func TestRelayAnonPaidModelLoginPrompt(t *testing.T) {
	db := store.NewMem()
	b := relayBroker(db)
	nodePub, _, _ := ed25519.GenerateKey(nil)
	b.nodes["paid"] = protocol.NodeRegistration{
		NodeID: "paid", PubKey: hex.EncodeToString(nodePub),
		Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 5, PriceOut: 5, Ctx: 4096}},
	}
	b.lastSeen["paid"] = time.Now()
	b.tunnels["paid"] = &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	_ = db.BindNode("paid", "someone-else")

	_, userPriv, _ := ed25519.GenerateKey(nil) // signed but NOT bound to any GitHub owner
	body := []byte(`{"model":"m","max_tokens":8}`)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	signReq(r, userPriv, body)
	w := httptest.NewRecorder()
	b.relay(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anon paid relay = %d, want 401: %s", w.Code, readBody(w))
	}
	if !strings.Contains(readBody(w), "log in to spend on paid models") {
		t.Errorf("body = %q, want login-to-spend prompt", readBody(w))
	}
}

// respondOnce launches a goroutine that answers the next job dispatched to tun with a
// node-signed JobResult carrying `body` (and the given token counts), the streaming and
// non-streaming round-trip both settle through.
func respondOnce(tun *nodeTunnel, nodeID string, nodePriv ed25519.PrivateKey, body string, in, out int, priceIn, priceOut float64) {
	go func() {
		job := <-tun.jobs
		rec := protocol.UsageReceipt{
			RequestID: job.ID, NodeID: nodeID, Model: "m",
			PromptTokens: in, CompletionTokens: out, PriceIn: priceIn, PriceOut: priceOut, TS: time.Now().Unix(),
		}
		rec.SignNode(nodePriv)
		res := protocol.JobResult{ID: job.ID, Status: 200, Body: []byte(body), Receipt: rec}
		tun.mu.Lock()
		ch := tun.waiters[job.ID]
		tun.mu.Unlock()
		ch <- res
	}()
}

// TestRelayGrantStreamRoundTrip drives the SINGLE-INSTANCE streaming relay (relayStream)
// end to end on a free grant: SSE headers are flushed, the node result settles $0, and the
// stream completes. This exercises the stream branch of relay + the local relayStream path.
func TestRelayGrantStreamRoundTrip(t *testing.T) {
	db := store.NewMem()
	b := relayBroker(db)
	owner := "ownerStream"
	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	b.nodes["s-node"] = protocol.NodeRegistration{
		NodeID: "s-node", PubKey: hex.EncodeToString(nodePub),
		Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 7, PriceOut: 7, Ctx: 4096}},
	}
	b.lastSeen["s-node"] = time.Now()
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	b.tunnels["s-node"] = tun
	_ = db.BindNode("s-node", owner)
	secret := makeGrant(t, db, store.Grant{ID: "g_stream", Owner: owner, Free: true, RPM: 60, Burst: 10}, "rog-grant_stream")

	respondOnce(tun, "s-node", nodePriv, `{"choices":[{"message":{"content":"streamed grant reply"}}]}`, 9, 5, 0, 0)

	body := `{"model":"m","max_tokens":8,"stream":true}`
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()
	b.relay(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("grant stream = %d, want 200: %s", w.Code, readBody(w))
	}
	if got := w.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("stream Content-Type = %q, want text/event-stream", got)
	}
	if earn, _ := db.EarningsOf("s-node"); earn != 0 {
		t.Errorf("free grant stream must mint no earning, got %v", earn)
	}
}

// TestRelayPaidStreamSettles drives the streaming relay on a PAID public request: a
// logged-in consumer with funds streams a completion, the wallet is DEBITED, and an
// earning lot mints for the owner (the fixed=false price-lock + settle branch of
// relayStream).
func TestRelayPaidStreamSettles(t *testing.T) {
	db := store.NewMem()
	b := relayBroker(db)
	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	b.nodes["sp"] = protocol.NodeRegistration{
		NodeID: "sp", PubKey: hex.EncodeToString(nodePub),
		Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 9, PriceOut: 9, Ctx: 4096}},
	}
	b.lastSeen["sp"] = time.Now()
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	b.tunnels["sp"] = tun
	_ = db.BindNode("sp", "owner-sp")

	_, userPriv, _ := ed25519.GenerateKey(nil)
	userPubHex := hex.EncodeToString(userPriv.Public().(ed25519.PublicKey))
	_ = db.BindOwner(store.Owner{GitHubID: 7, Login: "consumer", Pubkey: userPubHex})
	startBal, _ := db.AddCredits("u_gh_7", 500)

	respondOnce(tun, "sp", nodePriv, `{"choices":[{"message":{"content":"a streamed paid completion here"}}]}`, 12, 20, 9, 9)

	body := []byte(`{"model":"m","max_tokens":32,"stream":true}`)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	signReq(r, userPriv, body)
	w := httptest.NewRecorder()
	b.relay(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("paid stream = %d, want 200: %s", w.Code, readBody(w))
	}
	if endBal, _ := db.PeekBalance("u_gh_7"); !(endBal < startBal) {
		t.Fatalf("paid stream must DEBIT the wallet: %v -> %v", startBal, endBal)
	}
	if earn, _ := db.EarningsOf("sp"); earn <= 0 {
		t.Errorf("paid stream must mint an earning lot, got %v", earn)
	}
}

// TestRelayPaidStreamVoidRefunds drives the streaming VOID branch: a paid request whose
// node returns an EMPTY completion is charged $0 and the consumer's hold is refunded in
// full (no earning minted).
func TestRelayPaidStreamVoidRefunds(t *testing.T) {
	db := store.NewMem()
	b := relayBroker(db)
	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	b.nodes["sv"] = protocol.NodeRegistration{
		NodeID: "sv", PubKey: hex.EncodeToString(nodePub),
		Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 8, PriceOut: 8, Ctx: 4096}},
	}
	b.lastSeen["sv"] = time.Now()
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	b.tunnels["sv"] = tun
	_ = db.BindNode("sv", "owner-sv")

	_, userPriv, _ := ed25519.GenerateKey(nil)
	userPubHex := hex.EncodeToString(userPriv.Public().(ed25519.PublicKey))
	_ = db.BindOwner(store.Owner{GitHubID: 7, Login: "consumer", Pubkey: userPubHex})
	startBal, _ := db.AddCredits("u_gh_7", 500)

	// status 200 but ZERO completion tokens -> no usable output -> void.
	respondOnce(tun, "sv", nodePriv, `{"choices":[{"message":{"content":""}}]}`, 10, 0, 8, 8)

	body := []byte(`{"model":"m","max_tokens":8,"stream":true}`)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	signReq(r, userPriv, body)
	w := httptest.NewRecorder()
	b.relay(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("void stream = %d, want 200", w.Code)
	}
	if endBal, _ := db.PeekBalance("u_gh_7"); endBal != startBal {
		t.Errorf("void stream must refund the hold in full: %v -> %v", startBal, endBal)
	}
	if earn, _ := db.EarningsOf("sv"); earn != 0 {
		t.Errorf("void stream must mint NO earning, got %v", earn)
	}
}
