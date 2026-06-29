package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// TestRelayEmitsBilledTokenHeaders pins the DISPLAY of the BILLED token counts on the
// non-streaming relay: alongside X-RogerAI-Cost, the settled response carries
// X-RogerAI-Tokens-In / X-RogerAI-Tokens-Out = the SAME prompt/completion counts the
// cost was computed from (rec.CostWith2(billedPrompt, billedCompletion)). With no
// re-count sidecar the billed counts equal the node's claim, and the headers must be
// arithmetically consistent with the cost header: cost == (in*priceIn + out*priceOut)/1e6.
// This is exposure of an already-settled value (the TUI meter's honest ↑↓), NOT a change
// to any billing math.
func TestRelayEmitsBilledTokenHeaders(t *testing.T) {
	db := store.NewMem()
	b := relayBroker(db)

	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	b.nodes["tok"] = protocol.NodeRegistration{
		NodeID: "tok", PubKey: hex.EncodeToString(nodePub),
		Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 9, PriceOut: 9, Ctx: 4096}},
	}
	b.lastSeen["tok"] = time.Now()
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	b.tunnels["tok"] = tun
	_ = db.BindNode("tok", "owner1")

	_, userPriv, _ := ed25519.GenerateKey(nil)
	userPubHex := hex.EncodeToString(userPriv.Public().(ed25519.PublicKey))
	_ = db.BindOwner(store.Owner{GitHubID: 7, Login: "consumer", Pubkey: userPubHex})
	_, _ = db.AddCredits("u_gh_7", 500)

	const claimIn, claimOut = 12, 20
	go func() {
		job := <-tun.jobs
		rec := protocol.UsageReceipt{
			RequestID: job.ID, NodeID: "tok", Model: "m",
			PromptTokens: claimIn, CompletionTokens: claimOut,
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
		t.Fatalf("relay = %d, want 200: %s", w.Code, readBody(w))
	}
	gotIn := w.Header().Get("X-RogerAI-Tokens-In")
	gotOut := w.Header().Get("X-RogerAI-Tokens-Out")
	if gotIn != strconv.Itoa(claimIn) {
		t.Errorf("X-RogerAI-Tokens-In = %q, want %d (the billed prompt count)", gotIn, claimIn)
	}
	if gotOut != strconv.Itoa(claimOut) {
		t.Errorf("X-RogerAI-Tokens-Out = %q, want %d (the billed completion count)", gotOut, claimOut)
	}
	// The displayed tokens must be the very counts the cost was computed from.
	cost, perr := strconv.ParseFloat(w.Header().Get("X-RogerAI-Cost"), 64)
	if perr != nil {
		t.Fatalf("X-RogerAI-Cost not parseable: %q", w.Header().Get("X-RogerAI-Cost"))
	}
	want := (float64(claimIn)*9 + float64(claimOut)*9) / 1e6
	if d := cost - want; d > 1e-12 || d < -1e-12 {
		t.Errorf("cost header %.12g != (in*pin+out*pout)/1e6 = %.12g — tokens and cost must agree", cost, want)
	}
}

// TestRelayTokenHeadersAreBilledNotClaimed proves the token headers carry the BILLED
// count, not the node's raw CLAIM: a node claiming MORE prompt tokens than the request
// body has UTF-8 bytes is arithmetically impossible, so settleRecountPrompt clamps the
// billed prompt to the body-byte floor (the fail-closed input defense). The header must
// show that CLAMPED value, never the inflated claim — i.e. it tracks what was billed.
func TestRelayTokenHeadersAreBilledNotClaimed(t *testing.T) {
	db := store.NewMem()
	b := relayBroker(db)

	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	b.nodes["liar"] = protocol.NodeRegistration{
		NodeID: "liar", PubKey: hex.EncodeToString(nodePub),
		Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 9, PriceOut: 9, Ctx: 4096}},
	}
	b.lastSeen["liar"] = time.Now()
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	b.tunnels["liar"] = tun
	_ = db.BindNode("liar", "owner1")

	_, userPriv, _ := ed25519.GenerateKey(nil)
	userPubHex := hex.EncodeToString(userPriv.Public().(ed25519.PublicKey))
	_ = db.BindOwner(store.Owner{GitHubID: 8, Login: "consumer8", Pubkey: userPubHex})
	_, _ = db.AddCredits("u_gh_8", 500)

	body := []byte(`{"model":"m","max_tokens":32}`)
	// Claim more prompt tokens than the body has bytes (but within the ban margin, so it
	// is clamped, not permabanned). The billed prompt must collapse to the byte floor.
	overClaim := len(body) + 100
	go func() {
		job := <-tun.jobs
		rec := protocol.UsageReceipt{
			RequestID: job.ID, NodeID: "liar", Model: "m",
			PromptTokens: overClaim, CompletionTokens: 6,
			PriceIn: 9, PriceOut: 9, TS: time.Now().Unix(),
		}
		rec.SignNode(nodePriv)
		res := protocol.JobResult{ID: job.ID, Status: 200,
			Body: []byte(`{"choices":[{"message":{"content":"served"}}]}`), Receipt: rec}
		tun.mu.Lock()
		ch := tun.waiters[job.ID]
		tun.mu.Unlock()
		ch <- res
	}()

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	signReq(r, userPriv, body)
	w := httptest.NewRecorder()
	b.relay(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("relay = %d, want 200: %s", w.Code, readBody(w))
	}
	gotIn := w.Header().Get("X-RogerAI-Tokens-In")
	if gotIn == strconv.Itoa(overClaim) {
		t.Fatalf("X-RogerAI-Tokens-In = %q is the inflated CLAIM, not the billed count", gotIn)
	}
	if gotIn != strconv.Itoa(len(body)) {
		t.Errorf("X-RogerAI-Tokens-In = %q, want the byte-floor billed count %d", gotIn, len(body))
	}
}
