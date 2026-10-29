package edge

// Executable spec: features/lan/voice.feature (@edge) - the speech path on an Edge face. A REAL
// face (TLS over a certificate a REAL Edge authority issued), REAL members dialling it with their
// own issued certificates over mutual TLS, and a REAL TTS-shaped upstream stub behind it.

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/bddrun"
	"rogerai.fm/roger/v7/internal/protocol"
)

// speechBDD stands a face whose node serves a chat band and/or a voice band.
type speechBDD struct {
	inferBDD
	voiceUp  *httptest.Server
	voiceHit atomic.Int32
	voices   map[string]bool // the voice bands this node serves
	chats    map[string]bool // the chat bands this node serves
}

func (s *speechBDD) resetSpeech() {
	s.inferBDD.reset()
	if s.voiceUp != nil {
		s.voiceUp.Close()
		s.voiceUp = nil
	}
	s.voiceHit.Store(0)
	s.voices, s.chats = map[string]bool{}, map[string]bool{}
}

// node stands the serving node's face up with the bands recorded so far.
func (s *speechBDD) node(name string) {
	s.startUpstream() // the chat upstream
	s.voiceUp = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.voiceHit.Add(1)
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = io.WriteString(w, "RIFF....WAVEfmt fake audio")
	}))
	s.serving = s.issue(s.auth, name)
	s.admitted[name] = true
	var bands []string
	for b := range s.chats {
		bands = append(bands, b)
	}
	for b := range s.voices {
		bands = append(bands, b)
	}
	s.srv = NewServer(Describe{NodeID: name, Account: "acct-1", Kind: "host",
		Instances: []Instance{{Name: "serve", Bands: bands}}}, s.serving.cert)
	s.srv.SetServing(Serving{
		Trust:  s.verify,
		Member: func(id string) bool { return s.admitted[id] },
		Upstream: func(b string) (string, string, string, bool) {
			if s.chats[b] || s.voices[b] {
				return s.upstream.URL, "", "serve", true
			}
			return "", "", "", false
		},
		Speech: func(b string) (string, string, string, bool) {
			if s.voices[b] {
				return s.voiceUp.URL + "/v1/audio/speech", "", "serve", true
			}
			return "", "", "", false
		},
		NodeID: name, Key: s.serving.key,
	})
	ts := httptest.NewUnstartedServer(s.srv.Handler())
	ts.TLS = s.srv.ListenerTLS()
	ts.StartTLS()
	s.face = ts
}

// speak is one member (or nobody) asking the face for speech.
func (s *speechBDD) speak(as *member, body string) {
	s.dialPath(as, SpeechPath, body)
}

func speechBody(model string) string {
	return `{"model":"` + model + `","input":"hello","response_format":"wav"}`
}

func (s *speechBDD) refusedWith(code int, parts ...string) error {
	if s.status != code {
		return fmt.Errorf("status %d, want %d: %s", s.status, code, s.body)
	}
	for _, p := range parts {
		if !strings.Contains(s.body, p) {
			return fmt.Errorf("refusal lacks %q: %s", p, s.body)
		}
	}
	if n := s.voiceHit.Load(); n != 0 {
		return fmt.Errorf("the voice upstream was reached %d time(s) by a refused request", n)
	}
	return nil
}

func TestLANVoiceEdgeFace(t *testing.T) {
	s := &speechBDD{inferBDD: inferBDD{t: t}}
	paths := []string{}
	for _, l := range []int{97, 107, 113, 119} {
		paths = append(paths, fmt.Sprintf("../../features/lan/voice.feature:%d", l))
	}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) { s.resetSpeech(); return c, nil })
			sc.After(func(c context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				s.closeAll()
				if s.voiceUp != nil {
					s.voiceUp.Close()
				}
				return c, nil
			})
			sc.Step(`^the Edge member "([^"]*)" serves a TTS band$`, func(n string) error {
				s.voices["studio-voice"] = true
				s.node(n)
				return nil
			})
			sc.Step(`^a request for speech reaches "([^"]*)"'s face without a member certificate$`, func(string) error {
				s.speak(nil, speechBody("studio-voice"))
				return nil
			})
			sc.Step(`^it is refused, as a chat turn would be$`, func() error {
				speech := s.status
				s.dialPath(nil, InferPath, turn) // the same caller on the chat path
				if s.status != speech || speech != http.StatusUnauthorized {
					return fmt.Errorf("speech answered %d, a chat turn %d; want both 401", speech, s.status)
				}
				return s.refusedWith(http.StatusUnauthorized)
			})
			sc.Step(`^the Edge member "([^"]*)" serves only the chat band "([^"]*)"$`, func(n, b string) error {
				s.chats[b] = true
				s.node(n)
				return nil
			})
			sc.Step(`^a member asks "([^"]*)" for speech with "([^"]*)"$`, func(_ string, b string) error {
				m := s.issue(s.auth, "laptop")
				s.admitted["laptop"] = true
				s.speak(m, speechBody(b))
				return nil
			})
			sc.Step(`^it is refused with 404, naming the bands "([^"]*)" serves$`, func(string) error {
				return s.refusedWith(http.StatusNotFound, "qwen3-8b")
			})
			sc.Step(`^a member sends "([^"]*)" a speech request larger than 64 KiB$`, func(n string) error {
				s.voices["studio-voice"] = true
				s.node(n)
				m := s.issue(s.auth, "laptop")
				s.admitted["laptop"] = true
				s.speak(m, `{"model":"studio-voice","input":"`+strings.Repeat("a", 64<<10)+`"}`)
				return nil
			})
			sc.Step(`^it is refused with 413$`, func() error { return s.refusedWith(http.StatusRequestEntityTooLarge) })
			sc.Step(`^"([^"]*)" serves the chat band "([^"]*)" and no TTS band$`, func(n, b string) error {
				s.chats[b] = true
				s.node(n)
				return nil
			})
			sc.Step(`^it is refused, saying "([^"]*)" is not a voice$`, func(b string) error {
				return s.refusedWith(http.StatusBadRequest, b+" is not a voice")
			})
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true, Paths: paths},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("voice.feature (@edge) failed")
	}
}

// TestSpeechAnswerIsReceipted pins the happy path at the face: a member's speech returns the
// audio and a local receipt the serving node signed.
func TestSpeechAnswerIsReceipted(t *testing.T) {
	s := &speechBDD{inferBDD: inferBDD{t: t}}
	s.resetSpeech()
	defer s.closeAll()
	s.voices["studio-voice"] = true
	s.node("jetson")
	m := s.issue(s.auth, "laptop")
	s.admitted["laptop"] = true
	s.speak(m, speechBody("studio-voice"))
	if s.status != http.StatusOK || !strings.HasPrefix(s.body, "RIFF") {
		t.Fatalf("status %d body %q", s.status, s.body)
	}
	if !s.hasRec || !s.rec.Local || s.rec.User != "laptop" || s.rec.Model != "studio-voice" {
		t.Fatalf("no local receipt for the member: %+v", s.rec)
	}
	if !VerifyLocalReceipt(s.rec, s.leaf) {
		t.Fatal("the receipt does not verify against the face's certificate")
	}
	var _ protocol.UsageReceipt = s.rec
}

// The speech path's other refusals: a GET, a body naming no voice, a voice server that fails or is
// gone (its body never echoed: it can carry the key this node sent), and a node that serves no
// speech at all.
func TestSpeechRefusals(t *testing.T) {
	s := &speechBDD{inferBDD: inferBDD{t: t}}
	s.resetSpeech()
	defer s.closeAll()
	s.voices["studio-voice"] = true
	s.node("jetson")
	m := s.issue(s.auth, "laptop")
	s.admitted["laptop"] = true

	cfg := &tls.Config{InsecureSkipVerify: true, Certificates: []tls.Certificate{m.cert}}
	c := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg, DisableKeepAlives: true}}
	resp, err := c.Get(s.face.URL + SpeechPath)
	if err != nil || resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %v %v", err, resp)
	}
	resp.Body.Close()

	s.speak(m, `{"input":"hello"}`)
	if s.status != http.StatusBadRequest {
		t.Fatalf("no voice named: %d %s", s.status, s.body)
	}

	// the voice server fails, echoing a key
	s.voiceUp.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "bad key sk-from-this-node")
	})
	s.speak(m, speechBody("studio-voice"))
	if s.status != http.StatusBadGateway || strings.Contains(s.body, "sk-from-this-node") {
		t.Fatalf("failing voice: %d %s", s.status, s.body)
	}
	s.voiceUp.Close()
	s.speak(m, speechBody("studio-voice"))
	if s.status != http.StatusBadGateway || !strings.Contains(s.body, "did not answer") {
		t.Fatalf("gone voice: %d %s", s.status, s.body)
	}

	// a node with no Speech serves none: a band it does not have is a 404
	s.srv.serving.Speech = nil
	s.speak(m, speechBody("studio-voice"))
	if s.status != http.StatusBadRequest || !strings.Contains(s.body, "is not a voice") {
		t.Fatalf("no speech on this node: %d %s", s.status, s.body)
	}
}

func TestVoicesNeverWiderThanBands(t *testing.T) {
	got := voicesWithin([]string{"a", "x", "b"}, []string{"a", "b", "c"})
	if strings.Join(got, ",") != "a,b" {
		t.Fatalf("got %v", got)
	}
}
