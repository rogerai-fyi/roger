package main

// audio_timeout_bdd_test.go makes features/voice/relay_timeout.feature EXECUTABLE:
// the 2026-07-02 dead-voice-station incident, where audio.go's intended 90s
// "station timed out" 504 was unreachable behind the blanket 30s non-stream
// response deadline (http.TimeoutHandler), so consumers saw the edge-mangled
// generic timeout instead of the broker's own retryable JSON.
//
// REAL DEPS, NO MOCKS: the request travels the FULL production handler stack -
// streamSafeHandler(b.routes()) over a real HTTP server - signed with a real
// Ed25519 identity, held against the real store, routed to a registered voice
// station whose bridge simply never returns (jobs pile in its channel unanswered,
// exactly what a hung Kokoro box looks like). The provider-wait is shortened for
// the dead-station scenarios ONLY (the documented nonStreamRelayWait test seam);
// the reachability scenario checks the UNTOUCHED production values.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

type audioTimeoutState struct {
	t *testing.T

	mem      store.Store
	b        *broker
	srv      *httptest.Server
	modality string // the dead station's modality (tts | stt)

	userPriv ed25519.PrivateKey
	before   float64 // balance before the relay

	code int
	body []byte

	prevWait time.Duration
}

func (s *audioTimeoutState) reset(t *testing.T) {
	s.t = t
	s.mem, s.b, s.srv = nil, nil, nil
	s.modality = ""
	s.userPriv, s.before = nil, 0
	s.code, s.body = 0, nil
	s.prevWait = nonStreamRelayWait
}

func (s *audioTimeoutState) cleanup() {
	if s.srv != nil {
		s.srv.Close()
	}
	nonStreamRelayWait = s.prevWait
}

// --- Given -------------------------------------------------------------------------

func (s *audioTimeoutState) fundedConsumer() error {
	s.mem = store.NewMem()
	s.b = relayBroker(s.mem)
	_, priv, _ := ed25519.GenerateKey(nil)
	s.userPriv = priv
	pubHex := hex.EncodeToString(priv.Public().(ed25519.PublicKey))
	if err := s.mem.BindOwner(store.Owner{GitHubID: 7, Login: "alice", Pubkey: pubHex}); err != nil {
		return err
	}
	if _, err := s.mem.AddCredits("u_gh_7", 100); err != nil {
		return err
	}
	s.before, _ = s.mem.PeekBalance("u_gh_7")
	return nil
}

// deadStation registers a PRICED voice station of the given modality whose bridge
// never returns: its tunnel exists (jobs are accepted into the channel) but nothing
// ever posts a result - a hung provider, the incident shape.
func (s *audioTimeoutState) deadStation(modality string) error {
	if s.b == nil {
		return fmt.Errorf("build the consumer first")
	}
	s.modality = modality
	nodePub, _, _ := ed25519.GenerateKey(nil)
	s.b.nodes["dead-voice"] = protocol.NodeRegistration{
		NodeID: "dead-voice", PubKey: hex.EncodeToString(nodePub),
		Offers: []protocol.ModelOffer{{Model: "kokoro", Modality: modality, PriceIn: 15}},
	}
	s.b.lastSeen["dead-voice"] = time.Now()
	s.b.tunnels["dead-voice"] = &nodeTunnel{jobs: make(chan protocol.Job, 64), waiters: map[string]chan protocol.JobResult{}, token: "tok"}
	if err := s.mem.BindNode("dead-voice", "op1"); err != nil {
		return err
	}
	// Shorten the provider-wait for THIS scenario only (the documented var seam) so
	// the dead-station leg runs in milliseconds; restored in the After hook.
	nonStreamRelayWait = 250 * time.Millisecond
	// The full production edge: the streamSafeHandler-wrapped route table.
	s.srv = httptest.NewServer(streamSafeHandler(s.b.routes()))
	return nil
}

// --- When --------------------------------------------------------------------------

func (s *audioTimeoutState) post(path string, body []byte, contentType string) error {
	req, _ := http.NewRequest(http.MethodPost, s.srv.URL+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	pub, ts, sig := protocol.SignRequest(s.userPriv, http.MethodPost, strings.SplitN(path, "?", 2)[0], body)
	req.Header.Set(protocol.HeaderPubkey, pub)
	req.Header.Set(protocol.HeaderTS, strconv.FormatInt(ts, 10))
	req.Header.Set(protocol.HeaderSig, sig)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	s.code = resp.StatusCode
	s.body, _ = io.ReadAll(resp.Body)
	return nil
}

func (s *audioTimeoutState) postsSpeech() error {
	return s.post("/v1/audio/speech", []byte(`{"model":"kokoro","input":"hello from a dead station"}`), "application/json")
}

func (s *audioTimeoutState) postsTranscription() error {
	return s.post("/v1/audio/transcriptions?model=kokoro", []byte("RIFFxxxxWAVE~pretend audio~"), "application/octet-stream")
}

// --- Then --------------------------------------------------------------------------

// reachableInvariant is the incident's root cause, pinned: for each audio money
// path the INTENDED provider-timeout reply must be reachable - the route is exempt
// from the blanket non-stream response deadline (the chat-relay precedent) or its
// wait fires first - and the wait must stay below Cloudflare's ~100s proxy cap.
func (s *audioTimeoutState) reachableInvariant() error {
	for _, p := range []string{"/v1/audio/speech", "/v1/audio/transcriptions"} {
		if !streamRoutes[p] && nonStreamRelayWait >= nonStreamTimeout {
			return fmt.Errorf("%s: the %s provider-wait can NEVER fire behind the %s non-stream response deadline - a dead voice station surfaces as the edge-mangled generic timeout, never the intended 504 %q JSON (exempt the route like /v1/chat/completions, which does its own Cloudflare-aware bounding)",
				p, nonStreamRelayWait, nonStreamTimeout, "station timed out")
		}
	}
	return nil
}

func (s *audioTimeoutState) underCFCap() error {
	if cf := 100 * time.Second; nonStreamRelayWait >= cf {
		return fmt.Errorf("nonStreamRelayWait %s is not below Cloudflare's ~%s proxy cap - CF would emit its opaque 524 first", nonStreamRelayWait, cf)
	}
	return nil
}

func (s *audioTimeoutState) honest504() error {
	if s.code != http.StatusGatewayTimeout {
		return fmt.Errorf("status = %d, want the broker's own 504 (body: %s)", s.code, s.body)
	}
	var shape struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(s.body, &shape); err != nil {
		return fmt.Errorf("body is not the broker's JSON error shape: %q (%v)", s.body, err)
	}
	if shape.Error.Message != "station timed out" {
		return fmt.Errorf("error message %q, want %q", shape.Error.Message, "station timed out")
	}
	return nil
}

func (s *audioTimeoutState) holdRefunded() error {
	// The deferred ReleaseHoldFor runs as the handler returns; give it a beat.
	time.Sleep(100 * time.Millisecond)
	after, err := s.mem.PeekBalance("u_gh_7")
	if err != nil {
		return err
	}
	if after != s.before {
		return fmt.Errorf("balance %v -> %v: the dead-station hold was not refunded in full", s.before, after)
	}
	return nil
}

func TestAudioRelayTimeoutBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &audioTimeoutState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset(t)
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				st.cleanup()
				return ctx, nil
			})

			sc.Step(`^a funded signed voice consumer$`, st.fundedConsumer)
			sc.Step(`^a priced (tts|stt) station is on air whose bridge never returns a result$`, st.deadStation)
			sc.Step(`^the consumer posts speech for that station through the full production handler stack$`, st.postsSpeech)
			sc.Step(`^the consumer posts a transcription for that station through the full production handler stack$`, st.postsTranscription)
			sc.Step(`^each audio money path escapes the non-stream response deadline exactly like the chat relay$`, st.reachableInvariant)
			sc.Step(`^the non-stream provider-wait stays below Cloudflare's proxy cap$`, st.underCFCap)
			sc.Step(`^the response is the broker's own 504 JSON saying the station timed out$`, st.honest504)
			sc.Step(`^the consumer's hold is refunded in full$`, st.holdRefunded)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/voice/relay_timeout.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("voice relay timeout scenarios failed (see godog output above)")
	}
}
