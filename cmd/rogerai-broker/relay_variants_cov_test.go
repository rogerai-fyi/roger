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

// TestRelayGrantFreeRoundTrip covers the GRANT authentication path of the relay: a
// `Bearer rog-grant_...` resolves to the issuing owner's node allow-set (its own
// authentication, no signed identity needed), passes the grant rate-limit + cap checks,
// dispatches to the owner's node, and settles a FREE grant ($0, metering-only): the
// completion is returned, cost is 0, and no money lot mints.
func TestRelayGrantFreeRoundTrip(t *testing.T) {
	db := store.NewMem()
	b := relayBroker(db)

	owner := "ownerpubkeyhex"
	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	b.nodes["g-node"] = protocol.NodeRegistration{
		NodeID: "g-node", PubKey: hex.EncodeToString(nodePub),
		Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 7, PriceOut: 7, Ctx: 4096}},
	}
	b.lastSeen["g-node"] = time.Now()
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	b.tunnels["g-node"] = tun
	_ = db.BindNode("g-node", owner)

	secret := "rog-grant_relaykey"
	sum := sha256.Sum256([]byte(secret))
	_ = db.CreateGrant(store.Grant{ID: "grant_relay", SecretHash: hex.EncodeToString(sum[:]), Owner: owner, Label: "fleet", Free: true, RPM: 60, Burst: 10})

	go func() {
		job := <-tun.jobs
		rec := protocol.UsageReceipt{
			RequestID: job.ID, NodeID: "g-node", Model: "m",
			PromptTokens: 11, CompletionTokens: 6, TS: time.Now().Unix(),
		}
		rec.SignNode(nodePriv)
		res := protocol.JobResult{ID: job.ID, Status: 200,
			Body: []byte(`{"choices":[{"message":{"content":"grant served this"}}]}`), Receipt: rec}
		tun.mu.Lock()
		ch := tun.waiters[job.ID]
		tun.mu.Unlock()
		ch <- res
	}()

	body := []byte(`{"model":"m","max_tokens":8}`)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	r.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()
	b.relay(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("grant relay = %d, want 200: %s", w.Code, readBody(w))
	}
	if got := w.Header().Get("X-RogerAI-Cost"); got != "0" {
		t.Errorf("free grant cost header = %q, want 0", got)
	}
	if !strings.Contains(readBody(w), "grant served this") {
		t.Errorf("grant relay should return the completion, got %q", readBody(w))
	}
	if earn, _ := db.EarningsOf("g-node"); earn != 0 {
		t.Errorf("a free grant must mint no earning, got %v", earn)
	}
}

// TestRelayMethodNotAllowed covers the relay's method guard: a GET (not POST) is rejected
// at the allow() gate before any auth/pick work.
func TestRelayMethodNotAllowed(t *testing.T) {
	b := relayBroker(store.NewMem())
	w := httptest.NewRecorder()
	b.relay(w, httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("relay(GET) = %d, want 405", w.Code)
	}
}

// TestRelayInvalidSignature covers the signed-but-invalid path: a request that OFFERS
// signature headers which do NOT verify is rejected at identityOf with 401, distinct from
// the unsigned-spend 401 (this exercises the !iok branch, not the !authed branch).
func TestRelayInvalidSignature(t *testing.T) {
	b := relayBroker(store.NewMem())
	body := []byte(`{"model":"m"}`)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	_, userPriv, _ := ed25519.GenerateKey(nil)
	signReq(r, userPriv, body)
	// Corrupt the signature so verification fails (headers present but invalid).
	r.Header.Set(protocol.HeaderSig, "deadbeef")
	w := httptest.NewRecorder()
	b.relay(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("relay(bad sig) = %d, want 401", w.Code)
	}
	if !strings.Contains(readBody(w), "invalid request signature") {
		t.Errorf("expected the invalid-signature message, got %q", readBody(w))
	}
}

// TestRelayInsufficientBalance covers the pre-dispatch hold gate: a logged-in consumer
// whose funded balance is below the worst-case cost is rejected with 402 BEFORE any job is
// dispatched (the node goroutine must never be reached).
func TestRelayInsufficientBalance(t *testing.T) {
	db := store.NewMem()
	b := relayBroker(db)

	nodePub, _, _ := ed25519.GenerateKey(nil)
	// Price within the $10/1M consumer out-cap so the node is actually PICKED (a higher
	// price would be filtered in pick and 503, never reaching the hold gate).
	b.nodes["paid"] = protocol.NodeRegistration{
		NodeID: "paid", PubKey: hex.EncodeToString(nodePub),
		Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 10, PriceOut: 10, Ctx: 4096}},
	}
	b.lastSeen["paid"] = time.Now()
	dispatched := make(chan struct{}, 1)
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	b.tunnels["paid"] = tun
	_ = db.BindNode("paid", "owner1")

	_, userPriv, _ := ed25519.GenerateKey(nil)
	userPubHex := hex.EncodeToString(userPriv.Public().(ed25519.PublicKey))
	_ = db.BindOwner(store.Owner{GitHubID: 7, Login: "consumer", Pubkey: userPubHex})
	_, _ = db.AddCredits("u_gh_7", 0.0001) // funded far below the worst-case cost

	go func() { // must NOT receive a job
		select {
		case <-tun.jobs:
			dispatched <- struct{}{}
		case <-time.After(500 * time.Millisecond):
		}
	}()

	body := []byte(`{"model":"m","max_tokens":64}`)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	signReq(r, userPriv, body)
	w := httptest.NewRecorder()
	b.relay(w, r)

	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("relay(insufficient) = %d, want 402: %s", w.Code, readBody(w))
	}
	select {
	case <-dispatched:
		t.Error("a 402 must reject BEFORE dispatching the job to the node")
	default:
	}
}

// TestRelayPaidVoidNoOutputRoundTrip covers the void-on-no-output path on a node that is
// actually PICKED (price within the consumer cap): the consumer's hold is refunded in
// full, $0 is charged, NO earning lot mints, and the (empty) completion is still returned.
// This is the non-vacuous twin of the higher-priced void case (those nodes never pick).
func TestRelayPaidVoidNoOutputRoundTrip(t *testing.T) {
	db := store.NewMem()
	b := relayBroker(db)

	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	b.nodes["paid"] = protocol.NodeRegistration{
		NodeID: "paid", PubKey: hex.EncodeToString(nodePub),
		Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 8, PriceOut: 8, Ctx: 4096}},
	}
	b.lastSeen["paid"] = time.Now()
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	b.tunnels["paid"] = tun
	_ = db.BindNode("paid", "owner1")

	_, userPriv, _ := ed25519.GenerateKey(nil)
	userPubHex := hex.EncodeToString(userPriv.Public().(ed25519.PublicKey))
	_ = db.BindOwner(store.Owner{GitHubID: 7, Login: "consumer", Pubkey: userPubHex})
	startBal, _ := db.AddCredits("u_gh_7", 500)

	go func() {
		job := <-tun.jobs
		rec := protocol.UsageReceipt{
			RequestID: job.ID, NodeID: "paid", Model: "m",
			// TRUE-negative: empty body AND zero reported completion tokens -> voided. (An empty
			// body WITH reported tokens is the usage backstop - billed off the reported tokens,
			// capped/struck by the re-count layer - not voided; see recount_billing.feature.)
			PromptTokens: 10, CompletionTokens: 0,
			PriceIn: 8, PriceOut: 8, TS: time.Now().Unix(),
		}
		rec.SignNode(nodePriv)
		res := protocol.JobResult{ID: job.ID, Status: 200,
			Body: []byte(`{"choices":[{"message":{"content":""}}]}`), Receipt: rec}
		tun.mu.Lock()
		ch := tun.waiters[job.ID]
		tun.mu.Unlock()
		ch <- res
	}()

	body := []byte(`{"model":"m","max_tokens":8}`)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	signReq(r, userPriv, body)
	w := httptest.NewRecorder()
	b.relay(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("void round-trip = %d, want 200 (the empty body is still returned)", w.Code)
	}
	if got := w.Header().Get("X-RogerAI-Cost"); got != "0" {
		t.Errorf("void cost header = %q, want 0", got)
	}
	if endBal, _ := db.PeekBalance("u_gh_7"); endBal != startBal {
		t.Errorf("void must refund the hold in full: %v -> %v", startBal, endBal)
	}
	if earn, _ := db.EarningsOf("paid"); earn != 0 {
		t.Errorf("void must mint NO earning, got %v", earn)
	}
}

// TestRelayPaidSettlesRoundTrip covers the full PAID public-market settle: a picked node
// returns a valid completion, the consumer's wallet is DEBITED, an earning lot is minted
// for the owner, and the receipt/cost/balance headers are emitted.
func TestRelayPaidSettlesRoundTrip(t *testing.T) {
	db := store.NewMem()
	b := relayBroker(db)

	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	b.nodes["paid"] = protocol.NodeRegistration{
		NodeID: "paid", PubKey: hex.EncodeToString(nodePub),
		Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 9, PriceOut: 9, Ctx: 4096}},
	}
	b.lastSeen["paid"] = time.Now()
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	b.tunnels["paid"] = tun
	_ = db.BindNode("paid", "owner1")

	_, userPriv, _ := ed25519.GenerateKey(nil)
	userPubHex := hex.EncodeToString(userPriv.Public().(ed25519.PublicKey))
	_ = db.BindOwner(store.Owner{GitHubID: 7, Login: "consumer", Pubkey: userPubHex})
	startBal, _ := db.AddCredits("u_gh_7", 500)

	go func() {
		job := <-tun.jobs
		rec := protocol.UsageReceipt{
			RequestID: job.ID, NodeID: "paid", Model: "m",
			PromptTokens: 12, CompletionTokens: 20,
			PriceIn: 9, PriceOut: 9, TS: time.Now().Unix(),
		}
		rec.SignNode(nodePriv)
		res := protocol.JobResult{ID: job.ID, Status: 200,
			Body: []byte(`{"choices":[{"message":{"content":"a real completion with several words"}}]}`), Receipt: rec}
		tun.mu.Lock()
		ch := tun.waiters[job.ID]
		tun.mu.Unlock()
		ch <- res
	}()

	body := []byte(`{"model":"m","max_tokens":32}`)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	signReq(r, userPriv, body)
	w := httptest.NewRecorder()
	b.relay(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("paid settle round-trip = %d, want 200: %s", w.Code, readBody(w))
	}
	endBal, _ := db.PeekBalance("u_gh_7")
	if !(endBal < startBal) {
		t.Fatalf("paid relay must DEBIT the wallet: %v -> %v (want a debit)", startBal, endBal)
	}
	spend, _ := db.SpendOf("u_gh_7")
	if spend <= 0 {
		t.Errorf("paid relay must record spend, got %v", spend)
	}
	if earn, _ := db.EarningsOf("paid"); earn <= 0 {
		t.Errorf("paid relay must mint an earning lot for the owner, got %v", earn)
	}
	if w.Header().Get("X-RogerAI-Receipt") == "" {
		t.Error("a settled relay must emit the co-signed receipt header")
	}
	if got := w.Header().Get("X-RogerAI-Cost"); got == "" || got == "0" {
		t.Errorf("settled cost header = %q, want a positive cost", got)
	}
}

// TestRelaySelfUseFree covers the self-use $0 round-trip: a consumer relaying to a node
// THEY own pays nothing (pricing.free -> maxCost 0, no hold), still gets the completion,
// and the X-RogerAI-Cost header is "0". No earning lot is minted (you never pay yourself).
func TestRelaySelfUseFree(t *testing.T) {
	db := store.NewMem()
	b := relayBroker(db)

	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	b.nodes["mine"] = protocol.NodeRegistration{
		NodeID: "mine", PubKey: hex.EncodeToString(nodePub),
		Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 5, PriceOut: 5, Ctx: 4096}},
	}
	b.lastSeen["mine"] = time.Now()
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	b.tunnels["mine"] = tun

	// The consumer IS the node owner: bind the node to the consumer's own pubkey.
	_, userPriv, _ := ed25519.GenerateKey(nil)
	userPubHex := hex.EncodeToString(userPriv.Public().(ed25519.PublicKey))
	_ = db.BindNode("mine", userPubHex)

	go func() {
		job := <-tun.jobs
		rec := protocol.UsageReceipt{
			RequestID: job.ID, NodeID: "mine", Model: "m",
			PromptTokens: 10, CompletionTokens: 4, TS: time.Now().Unix(),
		}
		rec.SignNode(nodePriv)
		res := protocol.JobResult{ID: job.ID, Status: 200,
			Body: []byte(`{"choices":[{"message":{"content":"hello"}}]}`), Receipt: rec}
		tun.mu.Lock()
		ch := tun.waiters[job.ID]
		tun.mu.Unlock()
		ch <- res
	}()

	body := []byte(`{"model":"m","max_tokens":8}`)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	signReq(r, userPriv, body)
	w := httptest.NewRecorder()
	b.relay(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("self-use relay = %d, want 200: %s", w.Code, readBody(w))
	}
	if got := w.Header().Get("X-RogerAI-Cost"); got != "0" {
		t.Errorf("self-use cost header = %q, want 0", got)
	}
	if earn, _ := db.EarningsOf("mine"); earn != 0 {
		t.Errorf("self-use must mint NO earning, got %v", earn)
	}
	if !strings.Contains(readBody(w), "hello") {
		t.Errorf("self-use should still return the completion, got %q", readBody(w))
	}
}

// TestRelayUnknownFrequency covers the private-band tune-in path: a request carrying an
// X-Roger-Freq that resolves to no live band gets the uniform 503 "no station" reply
// (constant-work lookup; no enumeration oracle), before pick.
func TestRelayUnknownFrequency(t *testing.T) {
	db := store.NewMem()
	b := relayBroker(db)

	_, userPriv, _ := ed25519.GenerateKey(nil)
	body := []byte(`{"model":"m"}`)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	signReq(r, userPriv, body)
	r.Header.Set("X-Roger-Freq", "88.5-not-a-real-code")
	w := httptest.NewRecorder()
	b.relay(w, r)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("relay(bad freq) = %d, want 503: %s", w.Code, readBody(w))
	}
	if !strings.Contains(readBody(w), "no station on that frequency") {
		t.Errorf("expected the uniform no-station message, got %q", readBody(w))
	}
}
