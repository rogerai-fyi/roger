package agent

// offer_voice_bdd_test.go makes features/voicebooth/offer_voice.feature EXECUTABLE under godog,
// driving the REAL protocol.ModelOffer (round-trip) + the REAL agent.serve() (node-side injection)
// so the wire-honesty contract fails red if it regresses:
//   - offer.Voice/Speed round-trip through JSON; a chat offer omits them (no wire bloat);
//   - on the /v1/audio/speech path the node INJECTS the offer's Voice (single id OR blend string)
//     and Speed when the request OMITS them, but NEVER clobbers a caller's explicit value;
//   - the injection touches ONLY the speech path (a chat job is untouched) and never crashes on a
//     malformed body (forwarded byte-for-byte).
// Steps use a REAL httptest LOCAL server that CAPTURES the exact body the node forwarded (no mocks).

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

// bddErr is a tiny error type for readable godog assertion failures (the agent package's first
// godog test, so it defines its own — mirrors internal/protocol's errExpect).
type bddErr string

func (e bddErr) Error() string { return string(e) }
func errExpect(s string) error { return bddErr("expected " + s) }

type offerVoiceState struct {
	o          protocol.ModelOffer
	jsonBytes  []byte
	roundTrip  protocol.ModelOffer
	srv        *httptest.Server
	gotBody    []byte // the exact body the node forwarded to the local server
	forwardKey string // "speech" | "chat" — which local path the node hit
	cfg        Config
	offer      protocol.ModelOffer
	priv       ed25519.PrivateKey
}

func (s *offerVoiceState) reset() {
	if s.srv != nil {
		s.srv.Close()
	}
	*s = offerVoiceState{}
	_, s.priv, _ = ed25519.GenerateKey(nil)
}

// echoServer stands in for the operator's LOCAL model server: it records the request path + body
// and echoes the body back, so a step can assert what the node forwarded.
func (s *offerVoiceState) startEcho() {
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.gotBody = b
		if strings.Contains(r.URL.Path, "/audio/speech") {
			s.forwardKey = "speech"
		} else {
			s.forwardKey = "chat"
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
	}))
}

// --- Given: offers ---

func (s *offerVoiceState) ttsOfferVoiceSpeed(model, voice string, speed float64) error {
	s.o = protocol.ModelOffer{Model: model, Modality: protocol.ModalityTTS, Voice: voice, Speed: speed}
	return nil
}
func (s *offerVoiceState) chatOfferNoVoice(model string) error {
	s.o = protocol.ModelOffer{Model: model, Modality: protocol.ModalityChat}
	return nil
}
func (s *offerVoiceState) echoSpeechServer() error {
	s.startEcho()
	s.cfg = Config{NodeID: "n1", Model: "roger-operator-voice", Upstream: s.srv.URL + "/v1/chat/completions"}
	s.offer = protocol.ModelOffer{Model: "roger-operator-voice", Modality: protocol.ModalityTTS}
	return nil
}
func (s *offerVoiceState) echoChatServer() error { return s.echoSpeechServer() }
func (s *offerVoiceState) offerDefaultVoice(voice string) error {
	s.offer.Voice = voice
	return nil
}
func (s *offerVoiceState) offerDefaultVoiceSpeed(voice string, speed float64) error {
	s.offer.Voice = voice
	s.offer.Speed = speed
	return nil
}
func (s *offerVoiceState) offerDefaultVoiceEmpty() error {
	s.offer.Voice = ""
	return nil
}

// --- When: marshal / relay ---

func (s *offerVoiceState) marshal() error {
	b, err := json.Marshal(s.o)
	s.jsonBytes = b
	return err
}
func (s *offerVoiceState) marshalRoundTrip() error {
	if err := s.marshal(); err != nil {
		return err
	}
	return json.Unmarshal(s.jsonBytes, &s.roundTrip)
}

// relay serves a job with the given path + body through the REAL serve(), so the echo server
// captures the exact forwarded body (post-injection).
func (s *offerVoiceState) relay(path, body string) {
	job := protocol.Job{ID: "j", Body: json.RawMessage(body), Path: path}
	_ = serve(s.cfg, s.offer, s.priv, s.srv.Client(), job)
}
func (s *offerVoiceState) relaySpeechOmitVoice() error {
	s.relay("/v1/audio/speech", `{"model":"m","input":"hello"}`)
	return nil
}
func (s *offerVoiceState) relaySpeechWithVoice(voice string) error {
	s.relay("/v1/audio/speech", `{"model":"m","input":"hi","voice":"`+voice+`"}`)
	return nil
}
func (s *offerVoiceState) relaySpeechWithSpeed(speed string) error {
	s.relay("/v1/audio/speech", `{"model":"m","input":"hi","speed":`+speed+`}`)
	return nil
}
func (s *offerVoiceState) relayChatOmitVoice() error {
	s.relay("/v1/chat/completions", `{"model":"m","messages":[]}`)
	return nil
}
func (s *offerVoiceState) relaySpeechBadJSON() error {
	s.relay("/v1/audio/speech", `not-json{`)
	return nil
}

// --- Then: assertions ---

func (s *offerVoiceState) decodedVoiceIs(v string) error {
	if s.roundTrip.Voice != v {
		return errExpect("decoded voice " + v + ", got " + s.roundTrip.Voice)
	}
	return nil
}
func (s *offerVoiceState) decodedSpeedIs(sp float64) error {
	if s.roundTrip.Speed != sp {
		return errExpect("decoded speed as set")
	}
	return nil
}
func (s *offerVoiceState) jsonHasNoField(field string) error {
	if strings.Contains(string(s.jsonBytes), `"`+field+`"`) {
		return errExpect("no " + field + " field in JSON, got " + string(s.jsonBytes))
	}
	return nil
}
func (s *offerVoiceState) forwardedVoiceIs(v string) error {
	var m map[string]any
	if err := json.Unmarshal(s.gotBody, &m); err != nil {
		return errExpect("valid forwarded JSON, got " + string(s.gotBody))
	}
	if got, _ := m["voice"].(string); got != v {
		return errExpect("forwarded voice " + v + ", got " + got)
	}
	return nil
}
func (s *offerVoiceState) forwardedSpeedIs(sp float64) error {
	var m map[string]any
	if err := json.Unmarshal(s.gotBody, &m); err != nil {
		return errExpect("valid forwarded JSON")
	}
	got, ok := m["speed"].(float64)
	if !ok || got != sp {
		return errExpect("forwarded speed as expected")
	}
	return nil
}
func (s *offerVoiceState) forwardedOmitsVoice() error {
	var m map[string]any
	if err := json.Unmarshal(s.gotBody, &m); err != nil {
		return errExpect("valid forwarded JSON")
	}
	if _, ok := m["voice"]; ok {
		return errExpect("forwarded body to OMIT voice, got " + string(s.gotBody))
	}
	return nil
}
func (s *offerVoiceState) forwardedUnchanged() error {
	if string(s.gotBody) != "not-json{" {
		return errExpect("forwarded body byte-for-byte unchanged, got " + string(s.gotBody))
	}
	return nil
}

func TestOfferVoiceBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &offerVoiceState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				if st.srv != nil {
					st.srv.Close()
					st.srv = nil
				}
				return ctx, nil
			})
			sc.Step(`^a tts offer for "([^"]*)" with voice "([^"]*)" and speed ([0-9.]+)$`, st.ttsOfferVoiceSpeed)
			sc.Step(`^a chat offer for "([^"]*)" with no voice set$`, st.chatOfferNoVoice)
			sc.Step(`^a local speech server that echoes back the request body it received$`, st.echoSpeechServer)
			sc.Step(`^a local chat server that echoes back the request body it received$`, st.echoChatServer)
			sc.Step(`^a voice offer whose default voice is "([^"]*)" and speed is ([0-9.]+)$`, st.offerDefaultVoiceSpeed)
			sc.Step(`^a voice offer whose default voice is "([^"]*)"$`, st.offerDefaultVoice)
			sc.Step(`^a voice offer whose default voice is empty$`, st.offerDefaultVoiceEmpty)
			sc.Step(`^it is marshalled to JSON and decoded back$`, st.marshalRoundTrip)
			sc.Step(`^it is marshalled to JSON$`, st.marshal)
			sc.Step(`^the broker relays a /v1/audio/speech job whose body omits `+"`voice`"+`$`, st.relaySpeechOmitVoice)
			sc.Step(`^the broker relays a /v1/audio/speech job whose body already sets voice "([^"]*)"$`, st.relaySpeechWithVoice)
			sc.Step(`^the broker relays a /v1/audio/speech job whose body already sets speed ([0-9.]+)$`, st.relaySpeechWithSpeed)
			sc.Step(`^the broker relays a /v1/chat/completions job whose body omits `+"`voice`"+`$`, st.relayChatOmitVoice)
			sc.Step(`^the broker relays a /v1/audio/speech job whose body is not valid JSON$`, st.relaySpeechBadJSON)
			sc.Step(`^the decoded offer voice is "([^"]*)"$`, st.decodedVoiceIs)
			sc.Step(`^the decoded offer speed is ([0-9.]+)$`, st.decodedSpeedIs)
			sc.Step(`^the JSON does not contain a "([^"]*)" field$`, st.jsonHasNoField)
			sc.Step(`^the request the node forwards to the local server carries voice "([^"]*)"$`, st.forwardedVoiceIs)
			sc.Step(`^the request the node forwards to the local server carries speed ([0-9.]+)$`, st.forwardedSpeedIs)
			sc.Step(`^the request the node forwards to the local server omits `+"`voice`"+`$`, st.forwardedOmitsVoice)
			sc.Step(`^the node forwards the body byte-for-byte unchanged$`, st.forwardedUnchanged)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/voicebooth/offer_voice.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("voicebooth/offer_voice behavior scenarios failed (see godog output above)")
	}
}
