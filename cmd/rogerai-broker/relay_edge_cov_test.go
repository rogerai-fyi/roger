package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// nonFlusherWriter is an http.ResponseWriter that does NOT implement http.Flusher, so the
// relayStream "streaming unsupported" guard can be exercised.
type nonFlusherWriter struct {
	header http.Header
	code   int
	body   []byte
}

func (n *nonFlusherWriter) Header() http.Header {
	if n.header == nil {
		n.header = http.Header{}
	}
	return n.header
}
func (n *nonFlusherWriter) Write(p []byte) (int, error) {
	n.body = append(n.body, p...)
	return len(p), nil
}
func (n *nonFlusherWriter) WriteHeader(code int) { n.code = code }

// TestRelayStreamUnsupported locks the flusher guard: a ResponseWriter that cannot flush
// gets a clean 500 "streaming unsupported" before any dispatch.
func TestRelayStreamUnsupported(t *testing.T) {
	b := relayBroker(store.NewMem())
	node := protocol.NodeRegistration{NodeID: "x", PubKey: "ab"}
	offer := protocol.ModelOffer{Model: "m"}
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	job := protocol.Job{ID: "j1"}
	resCh := make(chan protocol.JobResult, 1)
	w := &nonFlusherWriter{}
	b.relayStream(w, tun, node, offer, streamBill{user: "u", consumer: "u", model: "m", pricing: pricingPlan{free: true, fixed: true}}, job, resCh, 0)
	if w.code != http.StatusInternalServerError {
		t.Fatalf("non-flusher stream = %d, want 500", w.code)
	}
	if !strings.Contains(string(w.body), "streaming unsupported") {
		t.Errorf("body = %q, want 'streaming unsupported'", string(w.body))
	}
}

// TestRelayFreqModelDenied locks the band model-deny path: a valid live band that EXCLUDES
// the requested model returns the uniform 503 (no oracle revealing the band exists).
func TestRelayFreqModelDenied(t *testing.T) {
	db := store.NewMem()
	b := relayBroker(db)
	nodePub, _, _ := ed25519.GenerateKey(nil)
	// The node offers BOTH the band-allowed model and "m"; the band allows only "allowed".
	b.nodes["bn"] = protocol.NodeRegistration{
		NodeID: "bn", PubKey: hex.EncodeToString(nodePub),
		Offers: []protocol.ModelOffer{
			{Model: "allowed", PriceIn: 1, PriceOut: 1, Ctx: 4096},
			{Model: "m", PriceIn: 1, PriceOut: 1, Ctx: 4096},
		},
	}
	b.lastSeen["bn"] = time.Now()
	b.tunnels["bn"] = &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	b.private["bn"] = true
	freq := "147.500 MHz ABCD-1234"
	_ = db.CreateBand(store.Band{ID: "bd1", CodeHash: protocol.BandCodeHash(freq), Owner: "o", NodeID: "bn", Models: []string{"allowed"}})

	_, userPriv, _ := ed25519.GenerateKey(nil)
	body := []byte(`{"model":"m","max_tokens":8}`)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	signReq(r, userPriv, body)
	r.Header.Set("X-Roger-Freq", freq)
	w := httptest.NewRecorder()
	b.relay(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("freq model-denied relay = %d, want 503: %s", w.Code, readBody(w))
	}
	if !strings.Contains(readBody(w), "no station on that frequency") {
		t.Errorf("body = %q, want uniform no-station message", readBody(w))
	}
}

// TestRelayGrantNoNodeForModel locks the grant pick-miss message: the grant's owner has a
// serving node, but none serves the requested model -> the grant-specific 503.
func TestRelayGrantNoNodeForModel(t *testing.T) {
	db := store.NewMem()
	b := relayBroker(db)
	owner := "ownerX"
	nodePub, _, _ := ed25519.GenerateKey(nil)
	b.nodes["gx"] = protocol.NodeRegistration{
		NodeID: "gx", PubKey: hex.EncodeToString(nodePub),
		Offers: []protocol.ModelOffer{{Model: "other", PriceIn: 1, PriceOut: 1, Ctx: 4096}},
	}
	b.lastSeen["gx"] = time.Now()
	b.tunnels["gx"] = &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	_ = db.BindNode("gx", owner)
	secret := makeGrant(t, db, store.Grant{ID: "g_x", Owner: owner, Free: true, RPM: 60, Burst: 10}, "rog-grant_x")

	w, r := grantPost(secret) // requests model "m", which gx does not serve
	b.relay(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("grant no-model-node = %d, want 503: %s", w.Code, readBody(w))
	}
	if !strings.Contains(readBody(w), "is serving m right now") {
		t.Errorf("body = %q, want grant-no-model message", readBody(w))
	}
}

// TestRelayGrantSponsorInsufficient locks the sponsored-grant insufficient-balance gate: a
// PAID grant whose owner sponsor wallet is broke is rejected 402 with the top-up-to-sponsor
// message, before dispatch.
func TestRelayGrantSponsorInsufficient(t *testing.T) {
	db := store.NewMem()
	b := relayBroker(db)
	owner := "ownerBroke"
	nodePub, _, _ := ed25519.GenerateKey(nil)
	b.nodes["gp"] = protocol.NodeRegistration{
		NodeID: "gp", PubKey: hex.EncodeToString(nodePub),
		Offers: []protocol.ModelOffer{{Model: "m", PriceIn: 5, PriceOut: 5, Ctx: 4096}},
	}
	b.lastSeen["gp"] = time.Now()
	b.tunnels["gp"] = &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	_ = db.BindNode("gp", owner)
	// A PRICED grant (not free) -> the owner sponsors from their wallet, which has $0.
	secret := makeGrant(t, db, store.Grant{ID: "g_paid", Owner: owner, Free: false, PriceIn: 5, PriceOut: 5, RPM: 60, Burst: 10}, "rog-grant_paid")

	w, r := grantPost(secret)
	b.relay(w, r)
	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("broke sponsor grant = %d, want 402: %s", w.Code, readBody(w))
	}
	if !strings.Contains(readBody(w), "top up to keep sponsoring") {
		t.Errorf("body = %q, want sponsor-topup message", readBody(w))
	}
}
