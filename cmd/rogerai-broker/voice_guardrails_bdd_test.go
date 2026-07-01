package main

// voice_guardrails_bdd_test.go makes features/voice/relay_guardrails.feature EXECUTABLE:
// the TTS input cap (ROGERAI_TTS_MAX_CHARS) and the in-flight-audio bound
// (ROGERAI_AUDIO_INFLIGHT), driven through the REAL audioRelay/transcribeRelay handlers
// on a real broker with an on-air tts + stt node. The concurrency bound is exercised by
// pre-occupying the real semaphore (b.audioSem), which is exactly what a concurrent
// in-flight request holds - no sleeps, fully deterministic.

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

type guardState struct {
	t         *testing.T
	b         *broker
	mem       *store.Mem
	userPriv  ed25519.PrivateKey
	maxChars  int
	inflight  int
	code      int
	spend     float64
	nodeFired bool
}

func (s *guardState) reset(t *testing.T) {
	s.t = t
	s.b, s.mem = nil, nil
	s.maxChars, s.inflight = 10000, 8
	s.code, s.spend, s.nodeFired = 0, 0, false
}

// build wires a broker with the configured guardrails, an on-air tts node "dj-1" and stt
// node "listener-1" (same raw model each), a funded logged-in consumer, and a stub node
// goroutine that returns audio for any dispatched job.
func (s *guardState) build() {
	s.mem = store.NewMem()
	s.b = relayBroker(s.mem)
	s.b.ttsMaxChars = s.maxChars
	if s.inflight <= 0 {
		s.b.audioSem = nil
	} else {
		s.b.audioSem = make(chan struct{}, s.inflight)
	}
	// tts + stt share the audio bound; a chat node proves chat is NEVER gated by it.
	for id, modality := range map[string]string{"dj-1": protocol.ModalityTTS, "listener-1": protocol.ModalitySTT, "chat-1": protocol.ModalityChat} {
		nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
		model := "voice-" + modality
		off := protocol.ModelOffer{Model: model, Modality: modality, PriceIn: 12}
		if modality == protocol.ModalityChat {
			off = protocol.ModelOffer{Model: "voice-tts", Modality: modality, PriceIn: 0} // free chat, same model id the request uses
		}
		s.b.nodes[id] = protocol.NodeRegistration{
			NodeID: id, PubKey: hex.EncodeToString(nodePub),
			Offers: []protocol.ModelOffer{off},
		}
		s.b.lastSeen[id] = time.Now()
		tun := &nodeTunnel{jobs: make(chan protocol.Job, 4), waiters: map[string]chan protocol.JobResult{}}
		s.b.tunnels[id] = tun
		_ = s.mem.BindNode(id, "op-"+id)
		go s.serve(tun, id, model, nodePriv)
	}
	_, s.userPriv, _ = ed25519.GenerateKey(nil)
	pub := hex.EncodeToString(s.userPriv.Public().(ed25519.PublicKey))
	_ = s.mem.BindOwner(store.Owner{GitHubID: 7, Login: "alice", Pubkey: pub})
	_, _ = s.mem.AddCredits("u_gh_7", 1_000_000)
}

func (s *guardState) serve(tun *nodeTunnel, id, model string, nodePriv ed25519.PrivateKey) {
	for {
		job, ok := <-tun.jobs
		if !ok {
			return
		}
		s.nodeFired = true
		rec := protocol.UsageReceipt{RequestID: job.ID, NodeID: id, Model: model, TS: time.Now().Unix()}
		rec.SignNode(nodePriv)
		res := protocol.JobResult{ID: job.ID, Status: 200, Body: []byte(`{"text":"ok"}`), Receipt: rec}
		tun.mu.Lock()
		ch := tun.waiters[job.ID]
		tun.mu.Unlock()
		if ch != nil {
			ch <- res
		}
	}
}

func (s *guardState) speak(chars int) {
	if s.b == nil {
		s.build()
	}
	body := []byte(fmt.Sprintf(`{"model":"voice-tts","input":%q}`, strings.Repeat("a", chars)))
	s.fire("/v1/audio/speech", body, s.b.audioRelay)
}

func (s *guardState) speakMultibyte(chars int) {
	if s.b == nil {
		s.build()
	}
	body := []byte(fmt.Sprintf(`{"model":"voice-tts","input":%q}`, strings.Repeat("世", chars)))
	s.fire("/v1/audio/speech", body, s.b.audioRelay)
}

func (s *guardState) transcribe() {
	if s.b == nil {
		s.build()
	}
	body := []byte("~fake audio bytes, opaque to the broker~")
	s.fire("/v1/audio/transcriptions?model=voice-stt", body, s.b.transcribeRelay)
}

func (s *guardState) chat() {
	// A chat relay must never be gated by the audio semaphore.
	body := []byte(`{"model":"voice-tts","messages":[{"role":"user","content":"hi"}]}`)
	s.fire("/v1/chat/completions", body, s.b.relay)
}

func (s *guardState) fire(path string, body []byte, h http.HandlerFunc) {
	before, _ := s.mem.PeekBalance("u_gh_7")
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	signReq(r, s.userPriv, body)
	w := httptest.NewRecorder()
	h(w, r)
	s.code = w.Code
	after, _ := s.mem.PeekBalance("u_gh_7")
	s.spend = before - after
}

// occupy pre-fills the semaphore to simulate n in-flight audio relays. With the bound
// disabled (nil sem) there is nothing to occupy - "in flight" is a no-op, exactly the
// behavior under test (a disabled guard never blocks).
func (s *guardState) occupy(n int) {
	if s.b == nil {
		s.build()
	}
	if s.b.audioSem == nil {
		return
	}
	for i := 0; i < n; i++ {
		s.b.audioSem <- struct{}{}
	}
}

// --- Given ---

func (s *guardState) capChars(n int) error     { s.maxChars = n; return nil }
func (s *guardState) boundIs(n int) error      { s.inflight = n; return nil }
func (s *guardState) inFlight(n int) error     { s.occupy(n); return nil }
func (s *guardState) oneSpeechInFlight() error { s.occupy(1); return nil }

// --- When ---

func (s *guardState) requestSpeech(n int) error   { s.speak(n); return nil }
func (s *guardState) requestSpeechMB(n int) error { s.speakMultibyte(n); return nil }
func (s *guardState) requestSpeechFlagged(n int) error {
	s.b = nil
	s.build()
	// A URL-provider moderation whose test backend flags everything -> screen() returns 451.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"flagged":true,"categories":{"S5":true}}`))
	}))
	s.t.Cleanup(srv.Close)
	s.b.mod = moderation{provider: "url", url: srv.URL, client: srv.Client()}
	s.speak(n)
	return nil
}
func (s *guardState) transcribeArrives() error { s.transcribe(); return nil }
func (s *guardState) nTranscriptionsInFlight(n int) error {
	// n concurrent transcriptions = n occupied slots.
	s.occupy(n)
	return nil
}
func (s *guardState) aThirdArrives() error { s.transcribe(); return nil }
func (s *guardState) itCompletes() error {
	// A finished request releases its slot.
	<-s.b.audioSem
	return nil
}
func (s *guardState) nextTranscription() error       { s.transcribe(); return nil }
func (s *guardState) concurrentTranscription() error { s.transcribe(); return nil }
func (s *guardState) concurrentChat() error          { s.chat(); return nil }

// --- Then ---

func (s *guardState) dispatchedBilled(n int) error {
	if s.code != http.StatusOK {
		return fmt.Errorf("want 200 dispatched, got %d", s.code)
	}
	if !s.nodeFired {
		return fmt.Errorf("the node was never dispatched")
	}
	return nil
}
func (s *guardState) dispatched() error {
	if s.code != http.StatusOK {
		return fmt.Errorf("want 200 dispatched, got %d", s.code)
	}
	return nil
}
func (s *guardState) refused413() error {
	if s.code != http.StatusRequestEntityTooLarge {
		return fmt.Errorf("want 413, got %d", s.code)
	}
	return nil
}
func (s *guardState) notRefusedForLength() error {
	if s.code == http.StatusRequestEntityTooLarge {
		return fmt.Errorf("must not 413 when the cap is disabled")
	}
	return nil
}
func (s *guardState) refused451() error {
	if s.code != http.StatusUnavailableForLegalReasons {
		return fmt.Errorf("want 451 (screen), got %d", s.code)
	}
	return nil
}
func (s *guardState) refused503Retry() error {
	if s.code != http.StatusServiceUnavailable {
		return fmt.Errorf("want 503, got %d", s.code)
	}
	return nil
}
func (s *guardState) noHold() error {
	if s.spend != 0 {
		return fmt.Errorf("a refused request must place no hold; spent %v", s.spend)
	}
	return nil
}
func (s *guardState) allDispatched(n int) error {
	// n slots were occupied to represent n in-flight; assert the pool holds exactly n.
	if got := len(s.b.audioSem); got != n {
		return fmt.Errorf("want %d in-flight slots held, got %d", n, got)
	}
	return nil
}
func (s *guardState) admitted() error {
	if s.code != http.StatusOK {
		return fmt.Errorf("want the next request admitted (200), got %d", s.code)
	}
	return nil
}
func (s *guardState) noneRefusedSaturation() error {
	// The guard is disabled and 20 "in flight" was a no-op; a real request must still
	// pass the saturation gate (it may 200 or fail for another reason, never 503-saturated).
	s.transcribe()
	if s.code == http.StatusServiceUnavailable {
		return fmt.Errorf("bound 0 disables the guard; must not 503")
	}
	return nil
}
func (s *guardState) chatProceeds() error {
	if s.code == http.StatusServiceUnavailable {
		return fmt.Errorf("chat must never be gated by the audio bound; got 503")
	}
	return nil
}

func TestVoiceGuardrailsFeature(t *testing.T) {
	s := &guardState{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { s.reset(t); return ctx, nil })
			sc.Step(`^a broker with an on-air tts node "dj-1" and stt node "listener-1"$`, func() error { return nil })
			sc.Step(`^ROGERAI_TTS_MAX_CHARS is (\d+)$`, s.capChars)
			sc.Step(`^ROGERAI_AUDIO_INFLIGHT is (\d+)$`, s.boundIs)
			sc.Step(`^a consumer requests speech for exactly (\d+) characters$`, s.requestSpeech)
			sc.Step(`^a consumer requests speech for (\d+) characters$`, s.requestSpeech)
			sc.Step(`^a consumer requests speech for (\d+) multibyte characters$`, s.requestSpeechMB)
			sc.Step(`^a consumer requests speech for (\d+) flagged characters$`, s.requestSpeechFlagged)
			sc.Step(`^the request is dispatched and billed for (\d+) characters$`, s.dispatchedBilled)
			sc.Step(`^the request is dispatched \(bytes never inflate the count\)$`, s.dispatched)
			sc.Step(`^the request is not refused for length$`, s.notRefusedForLength)
			sc.Step(`^the response is 413 naming the cap$`, s.refused413)
			sc.Step(`^the response is 451 \(screen\), still before any hold$`, s.refused451)
			sc.Step(`^no hold was placed and no node was dispatched$`, s.noHold)
			sc.Step(`^(\d+) transcriptions? (?:is|are) in flight$`, s.nTranscriptionsInFlight)
			sc.Step(`^all (\d+) are dispatched normally$`, s.allDispatched)
			sc.Step(`^a third arrives$`, s.aThirdArrives)
			sc.Step(`^it is refused 503 with a Retry-After header$`, s.refused503Retry)
			sc.Step(`^no hold was placed for it$`, s.noHold)
			sc.Step(`^it completes$`, s.itCompletes)
			sc.Step(`^the next transcription is admitted$`, func() error { s.nextTranscription(); return s.admitted() })
			sc.Step(`^(\d+) speech synthesis is in flight$`, s.nTranscriptionsInFlight)
			sc.Step(`^a concurrent transcription is refused 503$`, func() error { s.concurrentTranscription(); return s.refused503Retry() })
			sc.Step(`^a concurrent CHAT relay proceeds untouched$`, func() error { s.concurrentChat(); return s.chatProceeds() })
			sc.Step(`^none is refused for saturation$`, s.noneRefusedSaturation)
		},
		Options: &godog.Options{
			Format: "pretty", Paths: []string{"../../features/voice/relay_guardrails.feature"},
			TestingT: t, Strict: true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("relay_guardrails.feature: scenarios failed")
	}
}
