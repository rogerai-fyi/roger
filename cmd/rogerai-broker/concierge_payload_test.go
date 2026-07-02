package main

// The dogfood relay payloads must pin LOW reasoning effort for the band relays:
// gpt-oss reasoning models at default effort burn the entire max_tokens budget on
// hidden analysis and return EMPTY content with finish_reason=length, so Ping's
// dogfood rungs "serve" nothing and every homepage chat degrades to Groq (the
// July 2026 21s-cold-hit bug; reproduced live: 220/220 completion tokens, zero
// content). With reasoning_effort=low the same node answers in ~1.5s / ~91 tokens.
// The Groq fallback payload must NOT carry the field - a foreign API surface we
// do not shape.

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
)

// drainDogfoodJob pulls the enqueued relay Job off the node tunnel and decodes
// its OpenAI body. The relay itself times out (nobody answers the waiter) - this
// test is about the WIRE SHAPE, not the reply path.
func drainDogfoodJob(t *testing.T, tun *nodeTunnel) map[string]any {
	t.Helper()
	select {
	case job := <-tun.jobs:
		var payload map[string]any
		if err := json.Unmarshal(job.Body, &payload); err != nil {
			t.Fatalf("relay job body is not JSON: %v", err)
		}
		return payload
	case <-time.After(3 * time.Second):
		t.Fatal("no relay job enqueued on the tunnel within 3s")
		return nil
	}
}

// TestGrantDogfoodPayloadPinsLowReasoningEffort covers the grant rung (the
// production Ping path: CONCIERGE_GRANT_KEY + CONCIERGE_MODEL=gpt-oss-120b).
func TestGrantDogfoodPayloadPinsLowReasoningEffort(t *testing.T) {
	b, _, node := grantConciergeBroker(t, []string{"gpt-oss-120b"})
	b.lastSeen[node] = time.Now()
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	b.tunnels[node] = tun
	b.concierge.relayTimeout = clampRelayTimeout(1)

	done := make(chan struct{})
	go func() { defer close(done); b.dogfoodGrantRelay([]chatMsg{{Role: "user", Content: "hi ping"}}) }()

	payload := drainDogfoodJob(t, tun)
	<-done

	if got, ok := payload["reasoning_effort"]; !ok || got != "low" {
		t.Fatalf(`grant dogfood payload reasoning_effort = %v (present=%v), want "low" (default effort burns the whole max_tokens budget on analysis and yields empty content)`, got, ok)
	}
	if _, ok := payload["max_tokens"]; !ok {
		t.Fatal("grant dogfood payload lost max_tokens - the hard cap must stay")
	}
}

// TestFreeDogfoodPayloadPinsLowReasoningEffort covers the free-station rung.
func TestFreeDogfoodPayloadPinsLowReasoningEffort(t *testing.T) {
	b, _, _ := grantConciergeBroker(t, []string{"gpt-oss-120b"})
	freePub, _, _ := ed25519.GenerateKey(nil)
	b.nodes["freenode"] = protocol.NodeRegistration{
		NodeID: "freenode", PubKey: hex.EncodeToString(freePub),
		Offers: []protocol.ModelOffer{{Model: "free-m"}}, // zero price => free
	}
	b.lastSeen["freenode"] = time.Now()
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	b.tunnels["freenode"] = tun
	b.concierge.relayTimeout = clampRelayTimeout(1)

	done := make(chan struct{})
	go func() { defer close(done); b.dogfoodRelay([]chatMsg{{Role: "user", Content: "hi ping"}}) }()

	payload := drainDogfoodJob(t, tun)
	<-done

	if got, ok := payload["reasoning_effort"]; !ok || got != "low" {
		t.Fatalf(`free dogfood payload reasoning_effort = %v (present=%v), want "low"`, got, ok)
	}
}

// TestGroqPayloadCarriesNoReasoningEffort pins the fallback: Groq's API is not
// ours to shape; the field must stay OFF that wire.
func TestGroqPayloadCarriesNoReasoningEffort(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi from groq"}}]}`))
	}))
	defer srv.Close()

	b, _, _ := grantConciergeBroker(t, []string{"gpt-oss-120b"})
	b.concierge.groqURL = srv.URL
	b.concierge.groqKey = "gk_test"
	b.concierge.client = srv.Client()

	reply, ok := b.groqCall([]chatMsg{{Role: "user", Content: "hi ping"}})
	if !ok || reply == "" {
		t.Fatalf("groqCall failed against the stub: ok=%v reply=%q", ok, reply)
	}
	if _, present := got["reasoning_effort"]; present {
		t.Fatal(`Groq payload carries reasoning_effort - the fallback wire must stay unshaped`)
	}
	if _, present := got["max_tokens"]; !present {
		t.Fatal("Groq payload lost max_tokens")
	}
}
