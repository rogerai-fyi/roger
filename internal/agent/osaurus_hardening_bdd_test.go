package agent

// osaurus_hardening_bdd_test.go makes features/relay/osaurus_hardening.feature EXECUTABLE under
// godog, driving the REAL agent.serve() and agent.serveStream() against a REAL httptest backend
// that RECORDS the path, headers, and body it is hit on. It is the Osaurus-only safety contract:
// on an Osaurus upstream the node adds "X-Persist: false" and pins the body model to the offer;
// on any other backend nothing changes. No mocks of the node internals - the only stubs are the
// local backend (the surface Osaurus presents) and the broker (the stream sink).

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/protocol"
)

type osaHardState struct {
	backend    *httptest.Server
	broker     *httptest.Server
	lastHeader atomic.Value // http.Header seen by the backend on the last hit
	lastBody   atomic.Value // []byte the backend received on the last hit
	sentBody   []byte       // the tuner body handed to the relay (for the "unchanged" assertion)
	cfg        Config
	priv       ed25519.PrivateKey
}

const osaBackendBody = `{"usage":{"prompt_tokens":1,"completion_tokens":1}}`
const osaUpstreamKey = "sk-node-upstream-SECRET"

func (s *osaHardState) reset() {
	if s.backend != nil {
		s.backend.Close()
	}
	if s.broker != nil {
		s.broker.Close()
	}
	*s = osaHardState{}
	_, s.priv, _ = ed25519.GenerateKey(nil)
}

// --- Given ---

func (s *osaHardState) recordingBackend() error {
	s.backend = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.lastHeader.Store(r.Header.Clone())
		s.lastBody.Store(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(osaBackendBody))
	}))
	// The broker only needs to swallow the stream + result POSTs the streaming path makes.
	s.broker = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	return nil
}

func (s *osaHardState) offersModel(model string) error {
	s.cfg = Config{
		NodeID:      "n1",
		Model:       model,
		Upstream:    s.backend.URL + "/v1/chat/completions",
		UpstreamKey: osaUpstreamKey,
		Broker:      s.broker.URL,
	}
	return nil
}

func (s *osaHardState) upstreamIsOsaurus() error    { s.cfg.Osaurus = true; return nil }
func (s *osaHardState) upstreamIsNotOsaurus() error { s.cfg.Osaurus = false; return nil }

// --- When ---

// bodyNaming builds a chat body: a named model, an ABSENT model (name == ""), or a plain body.
func bodyNaming(model string) []byte {
	if model == "" {
		return []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	}
	return []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}]}`, model))
}

func (s *osaHardState) doServe(body []byte) {
	s.sentBody = body
	job := protocol.Job{ID: "j", User: "u", Body: body}
	_ = serve(s.cfg, protocol.ModelOffer{Model: s.cfg.Model}, s.priv, &http.Client{Timeout: 3 * time.Second}, job)
}

func (s *osaHardState) doServeStream(body []byte) {
	s.sentBody = body
	job := protocol.Job{ID: "j", User: "u", Body: body}
	_ = serveStream(s.cfg, protocol.ModelOffer{Model: s.cfg.Model}, s.priv, "tok", job)
}

func (s *osaHardState) relayChatJob() error { s.doServe(bodyNaming(s.cfg.Model)); return nil }
func (s *osaHardState) relayStreamingChatJob() error {
	s.doServeStream([]byte(`{"stream":true,"messages":[]}`))
	return nil
}
func (s *osaHardState) relayChatNaming(model string) error { s.doServe(bodyNaming(model)); return nil }
func (s *osaHardState) relayStreamNaming(model string) error {
	if model == "" {
		s.doServeStream([]byte(`{"stream":true,"messages":[]}`))
	} else {
		s.doServeStream([]byte(fmt.Sprintf(`{"model":%q,"stream":true,"messages":[]}`, model)))
	}
	return nil
}
func (s *osaHardState) relayUnparseable() error { s.doServe([]byte("this is not json")); return nil }

// --- Then ---

func (s *osaHardState) header() (http.Header, error) {
	h, _ := s.lastHeader.Load().(http.Header)
	if h == nil {
		return nil, bddErr("no forwarded request was recorded")
	}
	return h, nil
}

func (s *osaHardState) carriesHeader(name, value string) error {
	h, err := s.header()
	if err != nil {
		return err
	}
	if got := h.Get(name); got != value {
		return bddErr(fmt.Sprintf("forwarded %s = %q, want %q", name, got, value))
	}
	return nil
}

func (s *osaHardState) carriesNoXPersist() error {
	h, err := s.header()
	if err != nil {
		return err
	}
	if got := h.Get("X-Persist"); got != "" {
		return bddErr("a non-Osaurus upstream must get NO X-Persist header, got " + got)
	}
	return nil
}

func (s *osaHardState) bodyHasModel(want string) error {
	b, _ := s.lastBody.Load().([]byte)
	var m struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return bddErr("forwarded body is not JSON: " + string(b))
	}
	if m.Model != want {
		return bddErr(fmt.Sprintf("forwarded model = %q, want %q", m.Model, want))
	}
	return nil
}

func (s *osaHardState) onlyNodeHeaders() error {
	h, err := s.header()
	if err != nil {
		return err
	}
	if ct := h.Get("Content-Type"); ct != "application/json" {
		return bddErr("forwarded Content-Type " + ct + ", want application/json")
	}
	if auth := h.Get("Authorization"); auth != "Bearer "+osaUpstreamKey {
		return bddErr("forwarded Authorization " + auth + ", want the node's Bearer upstream key")
	}
	allowed := map[string]bool{
		"Content-Type": true, "Authorization": true, "X-Persist": true, // the node's own headers
		"Host": true, "User-Agent": true, "Accept-Encoding": true, "Content-Length": true, // Go transport
	}
	for name := range h {
		if !allowed[name] {
			return bddErr("forwarded an unexpected header " + name + "; no tuner header may leak")
		}
	}
	return nil
}

func (s *osaHardState) forwardedBodyUnchanged() error {
	b, _ := s.lastBody.Load().([]byte)
	if string(b) != string(s.sentBody) {
		return bddErr("forwarded body " + string(b) + ", want byte-for-byte " + string(s.sentBody))
	}
	return nil
}

func TestOsaurusHardeningBDD(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st := &osaHardState{}
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				if st.backend != nil {
					st.backend.Close()
					st.backend = nil
				}
				if st.broker != nil {
					st.broker.Close()
					st.broker = nil
				}
				return ctx, nil
			})
			sc.Step(`^a local backend that records the path, headers, and body it is hit on$`, st.recordingBackend)
			sc.Step(`^the node offers model "([^"]*)"$`, st.offersModel)
			sc.Step(`^the upstream is Osaurus$`, st.upstreamIsOsaurus)
			sc.Step(`^the upstream is not Osaurus$`, st.upstreamIsNotOsaurus)
			sc.Step(`^the broker relays a chat job$`, st.relayChatJob)
			sc.Step(`^the broker relays a streaming chat job$`, st.relayStreamingChatJob)
			sc.Step(`^the broker relays a chat job naming model "([^"]*)"$`, st.relayChatNaming)
			sc.Step(`^the broker relays a streaming chat job naming model "([^"]*)"$`, st.relayStreamNaming)
			sc.Step(`^the broker relays a chat job with an unparseable body$`, st.relayUnparseable)
			sc.Step(`^the forwarded request carries the header "([^"]+): ([^"]+)"$`, st.carriesHeader)
			sc.Step(`^the forwarded request carries no X-Persist header$`, st.carriesNoXPersist)
			sc.Step(`^the forwarded body has model "([^"]*)"$`, st.bodyHasModel)
			sc.Step(`^the backend sees only the node's Content-Type, Authorization, and X-Persist headers$`, st.onlyNodeHeaders)
			sc.Step(`^the forwarded body is unchanged$`, st.forwardedBodyUnchanged)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/relay/osaurus_hardening.feature"},
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("relay/osaurus_hardening behavior scenarios failed (see godog output above)")
	}
}

// TestPinModel pins the purely-lexical corners the Gherkin table cannot carry: a body with no
// model key, a valid-but-unusual model value, and an unparseable body (returned byte-for-byte).
func TestPinModel(t *testing.T) {
	const offer = "gpt-oss-20b"
	cases := []struct {
		name string
		in   string
		want string // expected model field after pin (or "" to assert byte-for-byte passthrough)
	}{
		{"different model rewritten", `{"model":"llama-3.3-70b","x":1}`, offer},
		{"absent model filled", `{"messages":[]}`, offer},
		{"empty model filled", `{"model":"","messages":[]}`, offer},
		{"correct model kept", `{"model":"gpt-oss-20b"}`, offer},
		{"numeric-ish model coerced", `{"model":"123"}`, offer},
	}
	for _, c := range cases {
		got := pinModel([]byte(c.in), offer)
		var m struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(got, &m); err != nil {
			t.Errorf("%s: pinned body not JSON: %s", c.name, got)
			continue
		}
		if m.Model != c.want {
			t.Errorf("%s: model = %q, want %q", c.name, m.Model, c.want)
		}
	}

	// An unparseable body is returned byte-for-byte (never corrupted).
	raw := []byte("not json at all")
	if got := pinModel(raw, offer); string(got) != string(raw) {
		t.Errorf("unparseable body should pass through unchanged, got %s", got)
	}
	// REGRESSION: the literal `null` unmarshals to a NIL map (no error) - pinModel must NOT panic on
	// the nil-map assignment (a bare `null` body from a tuner would otherwise crash the node).
	if got := pinModel([]byte("null"), offer); string(got) != "null" {
		t.Errorf("null body should pass through unchanged, got %s", got)
	}
	// withUsageOption has the same latent nil-map hazard on the stream path - guard confirmed.
	if got := withUsageOption([]byte("null")); string(got) != "null" {
		t.Errorf("withUsageOption(null) should pass through unchanged, got %s", got)
	}
	// A body that is valid JSON but not an object (an array) is also passed through unchanged.
	arr := []byte(`["a","b"]`)
	if got := pinModel(arr, offer); string(got) != string(arr) {
		t.Errorf("non-object JSON should pass through unchanged, got %s", got)
	}
}

// TestPinModelStripsSmuggledOtherFields confirms the pin only touches "model" and preserves the
// rest of the tuner's request (messages, temperature, etc.) so a legitimate request still works.
func TestPinModelPreservesOtherFields(t *testing.T) {
	in := `{"model":"evil","messages":[{"role":"user","content":"hi"}],"temperature":0.7}`
	got := pinModel([]byte(in), "safe-model")
	var m map[string]json.RawMessage
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("pinned body not JSON: %s", got)
	}
	if !strings.Contains(string(m["model"]), "safe-model") {
		t.Errorf("model not pinned: %s", got)
	}
	if _, ok := m["messages"]; !ok {
		t.Errorf("messages dropped by pin: %s", got)
	}
	if _, ok := m["temperature"]; !ok {
		t.Errorf("temperature dropped by pin: %s", got)
	}
}
