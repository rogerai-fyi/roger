package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// probe_voice_skip_test.go locks the RELEASE-BLOCKING fix (a live E2E caught it): the
// active CHAT canary must only target CHAT offers/nodes. A voice-only (tts/stt) node has
// no /v1/chat/completions, so a chat canary relays to its upstream and fails liveness;
// after a sustained streak the broker marks it OFFLINE and drops it from routing, killing
// EVERY voice station ("no station on air"). The BDD suite missed it because its httptest
// brokers never run the probe goroutine - these tests drive probeOnce's SELECTION (the
// skipped-goroutine logic) and the probeNode->trust->offline path directly, with real deps.

// voiceProbeBroker is a minimal broker with the metric/schedule maps and the probe enabled
// at a fixed floor/ceiling - enough to drive probeOnce's selection without the proberLoop.
func voiceProbeBroker() *broker {
	return &broker{
		nodes:         map[string]protocol.NodeRegistration{},
		tunnels:       map[string]*nodeTunnel{},
		lastSeen:      map[string]time.Time{},
		confidential:  map[string]bool{},
		private:       map[string]bool{},
		banned:        map[string]bool{},
		bannedOwners:  map[string]bool{},
		tps:           map[string]float64{},
		inflight:      map[string]int{},
		success:       map[string]float64{},
		trust:         map[string]trustState{},
		successCount:  map[string]int{},
		concurrentTPS: map[string]float64{},
		probeSched:    map[string]*probeState{},
		probe:         probeConfig{interval: 30 * time.Second, ceiling: 15 * time.Minute, perOwner: 0},
	}
}

// probeSelectedBackoff reports whether probeOnce selected nodeID this round: a selected
// target has its adaptive backoff advanced (0 -> 1) after the round; a node skipped by the
// modality/model gate keeps backoff 0 (see probeOnce, which advances only chosen targets).
// A node the loop never saw as due has no schedule row at all (backoff -1 sentinel here).
func probeSelectedBackoff(b *broker, nodeID string) int {
	b.metricsMu.Lock()
	defer b.metricsMu.Unlock()
	st := b.probeSched[nodeID]
	if st == nil {
		return -1
	}
	return st.backoff
}

// probeJobModel pulls the "model" field out of a canary job body (the probe builds an
// OpenAI-style chat request) so a test can assert WHICH model was probed.
func probeJobModel(job protocol.Job) string {
	var m struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(job.Body, &m)
	return m.Model
}

// TestProbeOnceSkipsVoiceOnlyNode: a node whose ONLY offers are voice (tts/stt) is NOT
// selected for the chat canary by probeOnce - so it is never sent a /v1/chat/completions
// probe and can never be failed/quarantined by it. A chat node in the same round IS
// selected (its backoff advances), proving the round ran and the skip is modality-specific.
func TestProbeOnceSkipsVoiceOnlyNode(t *testing.T) {
	b := voiceProbeBroker()
	now := time.Now()

	// A tts-only node and an stt-only node: both voice, no chat capability.
	b.nodes["tts"] = protocol.NodeRegistration{NodeID: "tts", Offers: []protocol.ModelOffer{
		{Model: "kokoro", Modality: protocol.ModalityTTS},
	}}
	b.nodes["stt"] = protocol.NodeRegistration{NodeID: "stt", Offers: []protocol.ModelOffer{
		{Model: "whisper", Modality: protocol.ModalitySTT},
	}}
	// A chat node so the round is non-empty and we prove selection still happens.
	b.nodes["chat"] = protocol.NodeRegistration{NodeID: "chat", Offers: []protocol.ModelOffer{
		{Model: "m", Modality: protocol.ModalityChat},
	}}
	for _, id := range []string{"tts", "stt", "chat"} {
		b.lastSeen[id] = now
	}
	// No tunnels: probeNode is a no-op for the async inference; we assert only SELECTION.
	b.probeOnce()

	if got := probeSelectedBackoff(b, "tts"); got == 1 {
		t.Fatalf("tts-only node was SELECTED for the chat canary (backoff advanced to %d) - it will relay a chat probe to a voice upstream and get quarantined", got)
	}
	if got := probeSelectedBackoff(b, "stt"); got == 1 {
		t.Fatalf("stt-only node was SELECTED for the chat canary (backoff advanced to %d) - voice nodes must never be chat-probed", got)
	}
	if got := probeSelectedBackoff(b, "chat"); got != 1 {
		t.Fatalf("chat node backoff = %d, want 1 (a chat node must still be probed - no regression)", got)
	}
}

// TestProbeOnceStillProbesChatNode: the back-compat empty-modality node (a pre-voice node
// that registered offers with no Modality field) is chat by default and MUST still be
// selected - the fix must not accidentally exclude legacy chat nodes.
func TestProbeOnceStillProbesChatNode(t *testing.T) {
	b := voiceProbeBroker()
	now := time.Now()
	// Empty modality == chat (back-compat); explicit "chat" too.
	b.nodes["legacy"] = protocol.NodeRegistration{NodeID: "legacy", Offers: []protocol.ModelOffer{{Model: "m"}}}
	b.nodes["explicit"] = protocol.NodeRegistration{NodeID: "explicit", Offers: []protocol.ModelOffer{{Model: "m2", Modality: protocol.ModalityChat}}}
	b.lastSeen["legacy"] = now
	b.lastSeen["explicit"] = now
	b.probeOnce()
	if got := probeSelectedBackoff(b, "legacy"); got != 1 {
		t.Fatalf("legacy empty-modality (chat) node backoff = %d, want 1 (must still be probed)", got)
	}
	if got := probeSelectedBackoff(b, "explicit"); got != 1 {
		t.Fatalf("explicit chat node backoff = %d, want 1 (must still be probed)", got)
	}
}

// TestProbeOnceMixedChatVoiceProbesChatModel: a node offering BOTH chat and tts is still
// chat-probed, and the probe MUST target its CHAT model (not the tts model) so the canary
// hits /v1/chat/completions, not the voice endpoint.
func TestProbeOnceMixedChatVoiceProbesChatModel(t *testing.T) {
	b := voiceProbeBroker()
	now := time.Now()
	// Offer order deliberately puts the voice offer FIRST, to prove selection picks the
	// chat model by modality, not merely the first offer.
	b.nodes["mixed"] = protocol.NodeRegistration{NodeID: "mixed", Offers: []protocol.ModelOffer{
		{Model: "kokoro", Modality: protocol.ModalityTTS},
		{Model: "llama", Modality: protocol.ModalityChat},
	}}
	b.lastSeen["mixed"] = now

	// Wire a local tunnel that records which model the probe job asked for.
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	b.tunnels["mixed"] = tun
	gotModel := make(chan string, 1)
	go func() {
		job := <-tun.jobs
		gotModel <- probeJobModel(job)
		res := protocol.JobResult{ID: job.ID, Status: http.StatusOK, Body: []byte(`{"choices":[{"message":{"content":"ok"}}]}`),
			Receipt: protocol.UsageReceipt{RequestID: job.ID, CompletionTokens: 1}}
		tun.mu.Lock()
		ch := tun.waiters[job.ID]
		tun.mu.Unlock()
		if ch != nil {
			ch <- res
		}
	}()

	b.probeOnce()
	select {
	case m := <-gotModel:
		if m != "llama" {
			t.Fatalf("mixed node was probed for model %q, want the CHAT model \"llama\" (never the tts model)", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("mixed chat+voice node was not chat-probed at all (expected a chat canary to its chat model)")
	}
}

// TestProbeVoiceNodeStaysOnlineAndRoutable is the QUARANTINE REGRESSION. It reproduces the
// production kill switch: a chat-probe failure streak against a voice node flips its voice
// offers OFFLINE and out of routing. Pre-fix this reproduces (the node is chat-probed and
// dies); post-fix the voice node is never chat-probed, so it stays ONLINE + routable off
// its passive heartbeat TTL.
func TestProbeVoiceNodeStaysOnlineAndRoutable(t *testing.T) {
	b := relayBroker(store.NewMem())
	nodePub, _, _ := ed25519.GenerateKey(nil)
	now := time.Now()

	// A tts-only voice station, heartbeat-fresh, with a live tunnel whose "upstream" (a
	// voice server) has NO chat endpoint: a chat canary relayed to it returns a 404, i.e.
	// exactly the E2E failure. Drive probeOnce enough rounds to cross the dead streak.
	b.nodes["voice"] = protocol.NodeRegistration{
		NodeID: "voice", PubKey: hex.EncodeToString(nodePub),
		Offers: []protocol.ModelOffer{{Model: "kokoro", Modality: protocol.ModalityTTS, PriceIn: 1, PriceOut: 1}},
	}
	b.lastSeen["voice"] = now
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 8), waiters: map[string]chan protocol.JobResult{}}
	b.tunnels["voice"] = tun
	// A responder that 404s any job it receives (a voice upstream has no chat route). If the
	// fix works, it receives NOTHING (the voice node is never chat-probed).
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case job := <-tun.jobs:
				res := protocol.JobResult{ID: job.ID, Status: http.StatusNotFound, Body: []byte(`{"error":"no chat route"}`),
					Receipt: protocol.UsageReceipt{RequestID: job.ID}}
				tun.mu.Lock()
				ch := tun.waiters[job.ID]
				tun.mu.Unlock()
				if ch != nil {
					ch <- res
				}
			}
		}
	}()

	// Run more than probeDeadStreak rounds. Each round: reset the schedule so the node is
	// due again (bypass the adaptive backoff so we test the modality gate, not the cadence),
	// then probe and let any dispatched probe settle.
	for i := 0; i < probeDeadStreak+2; i++ {
		b.metricsMu.Lock()
		delete(b.probeSched, "voice")
		b.metricsMu.Unlock()
		b.probeOnce()
		time.Sleep(20 * time.Millisecond) // let any (pre-fix) async probeNode settle
	}

	// POST-FIX: the voice offer stays ONLINE (no chat probe ever ran) and remains routable
	// as a tts offer. PRE-FIX: the streak crossed probeDeadStreak and it is quarantined.
	b.metricsMu.Lock()
	fails := b.trust["voice"].probeFails
	b.metricsMu.Unlock()
	if fails >= probeDeadStreak {
		t.Fatalf("voice node accumulated %d chat-probe failures (>=%d) - it was chat-probed and QUARANTINED; the whole voice feature dies", fails, probeDeadStreak)
	}
	offers := b.enrichOffersForNode(nil, b.nodes["voice"], now, nil, false)
	if len(offers) != 1 || !offers[0].Online {
		t.Fatalf("voice offer must stay Online after chat probing, got %+v (fails=%d)", offers, fails)
	}
	// And it is still routable for a voice (tts) request.
	if reg, _, ok := b.pickFor("kokoro", false, 0, 0, 0, "", nil, nil, nil, pickReq{modality: protocol.ModalityTTS}); !ok || reg.NodeID != "voice" {
		t.Fatalf("voice node must stay routable for tts, got ok=%v node=%q", ok, reg.NodeID)
	}
}

// NOTE on the mixed chat+voice case: in production a node registers ONE offer of ONE
// modality (internal/agent registers Offers:[]{offer}) and each MODEL gets its own NodeID
// (node.ShareNodeID(station, model, 0)), so chat and voice never share a trust row and a
// chat streak can never quarantine a voice node. A synthetic single-node-multiple-modality
// registration would still be excluded whole by the node-level not-serving gate (tunnel.go
// pickFor), but making that gate per-modality is a routing rework unwarranted by any real
// shape - a documented follow-up alongside a real voice canary, not part of this minimal
// fix. TestProbeOnceMixedChatVoiceProbesChatModel above locks the reachable invariant: when
// a node DOES advertise both, the chat canary targets the CHAT model, never the tts one.
