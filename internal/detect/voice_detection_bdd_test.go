package detect

// voice_detection_bdd_test.go makes features/voice/detection.feature EXECUTABLE under godog:
// stub OpenAI-compatible servers (TTS / STT / chat / mixed / id-only / key-protected) are run
// through the REAL DetectFull and classified by modality. CPU vs GPU never enters in — detection
// probes the endpoint, not the silicon. Layer 2 of the voice/audio modality (VOICE-AUDIO-DESIGN.md).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

type vdErr string

func (e vdErr) Error() string { return string(e) }
func vdExpect(s string) error { return vdErr("expected " + s) }

type voiceDetectState struct {
	models        []string
	endpoints     map[string]bool
	protectedAuth bool
	srv           *httptest.Server
	found         []Found
	needKey       []string
	lastModel     string
}

func (s *voiceDetectState) reset() {
	*s = voiceDetectState{endpoints: map[string]bool{}}
}

func (s *voiceDetectState) list1(a string) error    { s.models = []string{a}; return nil }
func (s *voiceDetectState) list2(a, b string) error { s.models = []string{a, b}; return nil }
func (s *voiceDetectState) servesSpeech() error     { s.endpoints["/v1/audio/speech"] = true; return nil }
func (s *voiceDetectState) servesTranscribe() error {
	s.endpoints["/v1/audio/transcriptions"] = true
	return nil
}
func (s *voiceDetectState) servesChat() error { s.endpoints["/v1/chat/completions"] = true; return nil }
func (s *voiceDetectState) servesChatAndSpeech() error {
	s.endpoints["/v1/chat/completions"] = true
	s.endpoints["/v1/audio/speech"] = true
	return nil
}
func (s *voiceDetectState) neitherAudio() error { return nil }
func (s *voiceDetectState) roggentoo(a string) error {
	s.models = []string{a}
	s.endpoints["/v1/audio/speech"] = true
	return nil
}
func (s *voiceDetectState) protectedTTS() error {
	s.protectedAuth = true
	s.endpoints["/v1/audio/speech"] = true
	return nil
}
func (s *voiceDetectState) noKey() error { return nil } // envKeysFn is stubbed empty in Before

func (s *voiceDetectState) detect() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		if s.protectedAuth && r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var data []map[string]string
		for _, m := range s.models {
			data = append(data, map[string]string{"id": m})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	for path := range s.endpoints {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadRequest) })
	}
	s.srv = httptest.NewServer(mux)
	old := probes
	probes = []struct{ name, base string }{{"test", s.srv.URL + "/v1"}}
	s.found, s.needKey = DetectFull()
	probes = old
	return nil
}

func (s *voiceDetectState) modalityOf(model string) (string, bool) {
	for _, f := range s.found {
		if m, ok := f.Modality[model]; ok {
			return m, true
		}
	}
	return "", false
}

func (s *voiceDetectState) detectedModality(model, modality string) error {
	got, ok := s.modalityOf(model)
	if !ok {
		return vdExpect(model + " to be detected")
	}
	if got != modality {
		return vdExpect("modality " + modality + " for " + model + ", got " + got)
	}
	s.lastModel = model
	return nil
}

func (s *voiceDetectState) unitIs(unit string) error {
	mod, _ := s.modalityOf(s.lastModel)
	o := protocol.ModelOffer{Modality: mod}
	o.Normalize()
	if o.Unit != unit {
		return vdExpect("unit " + unit + " for " + s.lastModel + ", got " + o.Unit)
	}
	return nil
}

func (s *voiceDetectState) needsKey() error {
	if len(s.needKey) == 0 {
		return vdExpect("the server reported as needing a key")
	}
	return nil
}

func TestEndpointExistsDead(t *testing.T) {
	// a dead port => transport error => the endpoint is not "present" (endpointExists err branch)
	if endpointExists("http://127.0.0.1:1/v1/audio/speech", "") {
		t.Error("dead endpoint reported as existing")
	}
}

func TestVoiceDetectionBDD(t *testing.T) {
	var oldEnum func() []int
	var oldEnv func() []candidate
	var oldKeys func() []string
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &voiceDetectState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				oldEnum, oldEnv, oldKeys = enumPorts, envCands, envKeysFn
				enumPorts = func() []int { return nil }
				envCands = func() []candidate { return nil }
				envKeysFn = func() []string { return nil }
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				enumPorts, envCands, envKeysFn = oldEnum, oldEnv, oldKeys
				if st.srv != nil {
					st.srv.Close()
				}
				return ctx, nil
			})
			sc.Step(`^a local server that serves GET /v1/models listing "([^"]*)"$`, st.list1)
			sc.Step(`^that server serves POST /v1/audio/speech$`, st.servesSpeech)
			sc.Step(`^that server serves POST /v1/audio/transcriptions$`, st.servesTranscribe)
			sc.Step(`^that server serves POST /v1/chat/completions$`, st.servesChat)
			sc.Step(`^a local server that serves both POST /v1/chat/completions and POST /v1/audio/speech$`, st.servesChatAndSpeech)
			sc.Step(`^it lists "([^"]*)" and "([^"]*)" on /v1/models$`, st.list2)
			sc.Step(`^a local server that lists "([^"]*)" and "([^"]*)" on /v1/models$`, st.list2)
			sc.Step(`^that server answers neither audio endpoint on a capability probe$`, st.neitherAudio)
			sc.Step(`^a local server matching roggentoo-tts \(GET /v1/models -> "([^"]*)", POST /v1/audio/speech\)$`, st.roggentoo)
			sc.Step(`^a local TTS server that answers 401 to an unauthenticated GET /v1/models$`, st.protectedTTS)
			sc.Step(`^no usable key is present in the environment$`, st.noKey)
			sc.Step(`^roger detects local models$`, st.detect)
			sc.Step(`^"([^"]*)" is detected with modality "([^"]*)"$`, st.detectedModality)
			sc.Step(`^"([^"]*)" is detected as "([^"]*)"$`, st.detectedModality)
			sc.Step(`^"([^"]*)" is classified "([^"]*)"$`, st.detectedModality)
			sc.Step(`^its unit is "([^"]*)"$`, st.unitIs)
			sc.Step(`^the server is reported as needing a key$`, st.needsKey)
			sc.Step(`^it is not silently dropped$`, st.needsKey)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/voice/detection.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("voice/detection behavior scenarios failed (see godog output above)")
	}
}
