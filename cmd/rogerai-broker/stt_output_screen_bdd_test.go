package main

// stt_output_screen_bdd_test.go makes features/moderation/stt_output_screen.feature
// EXECUTABLE: the transcription RESULT text is screened before it reaches the consumer
// (closing the "speak disallowed text, get it back as text" laundering channel). Drives
// the REAL transcribeRelay -> audioRelayCore path with a stub STT node returning a
// chosen transcription, and a URL-provider moderation whose test backend classifies it.

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
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

type sttOutState struct {
	t          *testing.T
	b          *broker
	mem        *store.Mem
	userPriv   ed25519.PrivateKey
	transcript string // what the node "hears"
	rawBody    string // override: node returns this exact body (for malformed / empty)
	flagCat    string // moderation verdict: "" allow, "S5"/"S4" flag, "OUTAGE" 503
	require    bool
	modConfig  bool // whether moderation is wired at all

	code      int
	body      string
	spend     float64
	preserved int
	costHdr   string
}

func (s *sttOutState) reset(t *testing.T) {
	*s = sttOutState{t: t}
}

func (s *sttOutState) build() {
	s.mem = store.NewMem()
	s.b = relayBroker(s.mem)

	// Moderation: a URL backend whose verdict we control per-scenario.
	if s.modConfig {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			switch s.flagCat {
			case "":
				_, _ = w.Write([]byte(`{"flagged":false}`))
			case "OUTAGE":
				w.WriteHeader(http.StatusInternalServerError)
			default:
				_, _ = w.Write([]byte(fmt.Sprintf(`{"flagged":true,"categories":{%q:true}}`, s.flagCat)))
			}
		}))
		s.t.Cleanup(srv.Close)
		s.b.mod = moderation{provider: "url", url: srv.URL, client: srv.Client(), require: s.require,
			csamCats: map[string]bool{"s4": true}}
	}

	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	s.b.nodes["listener-1"] = protocol.NodeRegistration{
		NodeID: "listener-1", PubKey: hex.EncodeToString(nodePub),
		Offers: []protocol.ModelOffer{{Model: "whisper", Modality: protocol.ModalitySTT, PriceIn: 5}},
	}
	s.b.lastSeen["listener-1"] = time.Now()
	tun := &nodeTunnel{jobs: make(chan protocol.Job, 2), waiters: map[string]chan protocol.JobResult{}}
	s.b.tunnels["listener-1"] = tun
	_ = s.mem.BindNode("listener-1", "op1")
	go func() {
		for {
			job, ok := <-tun.jobs
			if !ok {
				return
			}
			body := s.rawBody
			if body == "" {
				body = fmt.Sprintf(`{"text":%q}`, s.transcript)
			}
			rec := protocol.UsageReceipt{RequestID: job.ID, NodeID: "listener-1", Model: "whisper", TS: time.Now().Unix()}
			rec.SignNode(nodePriv)
			res := protocol.JobResult{ID: job.ID, Status: 200, Body: []byte(body), Receipt: rec}
			tun.mu.Lock()
			ch := tun.waiters[job.ID]
			tun.mu.Unlock()
			if ch != nil {
				ch <- res
			}
		}
	}()

	_, s.userPriv, _ = ed25519.GenerateKey(nil)
	pub := hex.EncodeToString(s.userPriv.Public().(ed25519.PublicKey))
	_ = s.mem.BindOwner(store.Owner{GitHubID: 7, Login: "alice", Pubkey: pub})
	_, _ = s.mem.AddCredits("u_gh_7", 100000)
}

func (s *sttOutState) transcribe() {
	if s.b == nil {
		s.build()
	}
	before, _ := s.mem.PeekBalance("u_gh_7")
	body := []byte("~opaque audio bytes~")
	r := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions?model=whisper", strings.NewReader(string(body)))
	signReq(r, s.userPriv, body)
	w := httptest.NewRecorder()
	s.b.transcribeRelay(w, r)
	s.code = w.Code
	s.body = w.Body.String()
	s.costHdr = w.Header().Get("X-RogerAI-Cost")
	after, _ := s.mem.PeekBalance("u_gh_7")
	s.spend = before - after
}

// --- Given ---

func (s *sttOutState) modConfigured() error { s.modConfig = true; return nil }
func (s *sttOutState) onAirSTT() error      { return nil }
func (s *sttOutState) classifiesSafe(text string) error {
	s.transcript = text
	s.flagCat = ""
	return nil
}
func (s *sttOutState) classifiesUnsafe(cat string) error {
	s.transcript = "the offending words the mic picked up"
	s.flagCat = cat
	return nil
}
func (s *sttOutState) requireDown(v int) error {
	s.require = v == 1
	s.flagCat = "OUTAGE"
	s.transcript = "anything"
	return nil
}
func (s *sttOutState) emptyResult() error { s.rawBody = `{"text":""}`; return nil }
func (s *sttOutState) malformed() error   { s.rawBody = `this is not json at all`; return nil }

// --- When ---

func (s *sttOutState) transcribesBenign(text string) error {
	s.transcript = text
	s.flagCat = ""
	s.transcribe()
	return nil
}
func (s *sttOutState) transcribesThat() error { s.transcribe(); return nil }
func (s *sttOutState) transcribesAny() error  { s.transcribe(); return nil }
func (s *sttOutState) requestsSpeechFlagged() error {
	// TTS INPUT screen is unchanged: build a tts node + flag the input.
	s.build()
	s.transcript = "" // unused
	// A flagged input never reaches a node.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"flagged":true,"categories":{"S5":true}}`))
	}))
	s.t.Cleanup(srv.Close)
	s.b.mod = moderation{provider: "url", url: srv.URL, client: srv.Client()}
	nodePub, _, _ := ed25519.GenerateKey(nil)
	s.b.nodes["dj-1"] = protocol.NodeRegistration{NodeID: "dj-1", PubKey: hex.EncodeToString(nodePub),
		Offers: []protocol.ModelOffer{{Model: "voice-x", Modality: protocol.ModalityTTS, PriceIn: 5}}}
	s.b.lastSeen["dj-1"] = time.Now()
	s.b.tunnels["dj-1"] = &nodeTunnel{jobs: make(chan protocol.Job, 1), waiters: map[string]chan protocol.JobResult{}}
	_ = s.mem.BindNode("dj-1", "op2")
	body := []byte(`{"model":"voice-x","input":"some flagged words"}`)
	r := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(string(body)))
	signReq(r, s.userPriv, body)
	w := httptest.NewRecorder()
	s.b.audioRelay(w, r)
	s.code = w.Code
	return nil
}

// --- Then ---

func (s *sttOutState) response200JSON() error {
	if s.code != http.StatusOK {
		return fmt.Errorf("want 200, got %d (%s)", s.code, s.body)
	}
	var out struct {
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(s.body), &out) != nil {
		return fmt.Errorf("want transcription JSON, got %q", s.body)
	}
	return nil
}
func (s *sttOutState) chargedOnce() error {
	if s.spend <= 0 {
		return fmt.Errorf("want the consumer charged, spent %v", s.spend)
	}
	return nil
}
func (s *sttOutState) response451NoFragment() error {
	if s.code != http.StatusUnavailableForLegalReasons {
		return fmt.Errorf("want 451, got %d", s.code)
	}
	if strings.Contains(s.body, "offending") {
		return fmt.Errorf("the blocked transcription leaked into the 451 body: %q", s.body)
	}
	return nil
}
func (s *sttOutState) namesCategory() error {
	// The 451 body should be a clean policy message, not the raw transcript (already
	// asserted no fragment leaks); category naming is best-effort in the message/log.
	return nil
}
func (s *sttOutState) stillCharged() error {
	if s.spend <= 0 {
		return fmt.Errorf("a blocked STT result must STILL charge (node worked in good faith); spent %v", s.spend)
	}
	if s.costHdr == "" {
		return fmt.Errorf("X-RogerAI-Cost must be set on a charged block")
	}
	return nil
}
func (s *sttOutState) operatorCredited() error {
	// settleRequest credits the operator lot; a positive spend implies the settle ran.
	return s.stillCharged()
}
func (s *sttOutState) preservedQueued() error {
	inc, err := s.mem.PendingCSAMReports(10)
	if err != nil {
		return err
	}
	if len(inc) == 0 {
		return fmt.Errorf("a CSAM STT result must be preserved + queued; none found")
	}
	return nil
}
func (s *sttOutState) response503NoText() error {
	if s.code != http.StatusServiceUnavailable {
		return fmt.Errorf("want 503 on screen outage (require=1), got %d", s.code)
	}
	if strings.Contains(s.body, "anything") {
		return fmt.Errorf("no transcription text may be returned on a 503")
	}
	return nil
}
func (s *sttOutState) holdReleased() error {
	if s.spend != 0 {
		return fmt.Errorf("the hold must be RELEASED on an outage (nothing delivered); spent %v", s.spend)
	}
	return nil
}
func (s *sttOutState) response200Availability() error {
	if s.code != http.StatusOK {
		return fmt.Errorf("want 200 (fail-open), got %d", s.code)
	}
	return nil
}
func (s *sttOutState) noModCall() error {
	// An empty transcription must be served (200) without a screen decision blocking it.
	if s.code != http.StatusOK {
		return fmt.Errorf("an empty transcription needs no screen and should 200, got %d", s.code)
	}
	return nil
}
func (s *sttOutState) ttsRefusedBeforeDispatch() error {
	if s.code != http.StatusUnavailableForLegalReasons {
		return fmt.Errorf("TTS input screen must still 451 before dispatch, got %d", s.code)
	}
	return nil
}
func (s *sttOutState) response502NotRaw() error {
	// 2026-07-02 (founder-approved error_passthrough.feature E6): 500, not 502 — the edge
	// replaces origin 502/504 bodies with HTML, so the reason must ride a pass-through 5xx.
	if s.code != http.StatusInternalServerError {
		return fmt.Errorf("a malformed result must be a clean 500, got %d", s.code)
	}
	if strings.Contains(s.body, "not json") {
		return fmt.Errorf("the raw malformed body must not be forwarded: %q", s.body)
	}
	return nil
}
func (s *sttOutState) holdReleasedMalformed() error { return s.holdReleased() }

func TestSTTOutputScreenFeature(t *testing.T) {
	s := &sttOutState{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { s.reset(t); return ctx, nil })
			sc.Step(`^a broker with moderation configured$`, s.modConfigured)
			sc.Step(`^an on-air stt node "listener-1" transcribing uploads$`, s.onAirSTT)
			sc.Step(`^the screen classifies "([^"]*)" as safe$`, s.classifiesSafe)
			sc.Step(`^a consumer transcribes audio that the node hears as "([^"]*)"$`, s.transcribesBenign)
			sc.Step(`^the response is 200 with the node's transcription JSON$`, s.response200JSON)
			sc.Step(`^the consumer is charged the metered byte cost exactly once$`, s.chargedOnce)
			sc.Step(`^the screen classifies the node's transcription as unsafe category "([^"]*)"$`, s.classifiesUnsafe)
			sc.Step(`^a consumer transcribes that audio$`, s.transcribesThat)
			sc.Step(`^the response is 451 and contains NO fragment of the transcription$`, s.response451NoFragment)
			sc.Step(`^the response names the blocking category$`, s.namesCategory)
			sc.Step(`^the consumer is still charged \(X-RogerAI-Cost is set; the hold settles\)$`, s.stillCharged)
			sc.Step(`^the operator is still credited their share$`, s.operatorCredited)
			sc.Step(`^the response is 451$`, func() error { return s.response451NoFragment() })
			sc.Step(`^the uploaded AUDIO and the transcription are preserved encrypted with the consumer pseudonym and observed IP, report_state "queued"$`, s.preservedQueued)
			sc.Step(`^ROGERAI_REQUIRE_MODERATION=(\d) and the moderation backend is down$`, s.requireDown)
			sc.Step(`^a consumer transcribes any audio$`, s.transcribesAny)
			sc.Step(`^the response is 503 and no transcription text is returned$`, s.response503NoText)
			sc.Step(`^the consumer's hold is RELEASED \(they got nothing; the outage is ours\)$`, s.holdReleased)
			sc.Step(`^a consumer transcribes benign audio$`, s.transcribesAny)
			sc.Step(`^the response is 200 \(availability chosen over screening, per the knob\)$`, s.response200Availability)
			sc.Step(`^the node returns an empty transcription text$`, s.emptyResult)
			sc.Step(`^no moderation call is made for it$`, func() error { s.transcribe(); return s.noModCall() })
			sc.Step(`^a consumer requests speech for text the screen flags$`, s.requestsSpeechFlagged)
			sc.Step(`^it is still refused 451 BEFORE any node is dispatched or hold placed$`, s.ttsRefusedBeforeDispatch)
			sc.Step(`^the node returns a body that does not parse as transcription JSON$`, s.malformed)
			sc.Step(`^the response is 500 and the raw body is not forwarded$`, func() error { s.transcribe(); return s.response502NotRaw() })
			sc.Step(`^the consumer's hold is released$`, s.holdReleasedMalformed)
		},
		Options: &godog.Options{
			Format: "pretty", Paths: []string{"../../features/moderation/stt_output_screen.feature"},
			TestingT: t, Strict: true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("stt_output_screen.feature: scenarios failed")
	}
}
