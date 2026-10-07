package client

// routing_passthrough_bdd_test.go makes the @slice0 scenarios of
// features/proxy/routing_passthrough.feature EXECUTABLE: the old-broker negotiation
// (contract §9). "The operator tunes a band" drives the REAL `Use` (its stdin/serve/handler
// seams capture the assembled ProxyOptions instead of binding a port), so the probe runs
// exactly where production runs it; requests then go through the REAL ProxyHandlerLive.
// The broker is a recording httptest stand-in (the approved seam for proxy specs): it answers
// GET /v1/models as each scenario scripts (200 / 404 / 503), serves /discover, and records
// every chat attempt's headers and body. Nothing about the proxy is mocked.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/cucumber/godog"
)

type rpNegState struct {
	t *testing.T

	srv          *httptest.Server
	modelsStatus int // what GET /v1/models answers
	model        string
	sessionKey   string
	ownerMaxOut  float64
	ownerMaxIn   float64
	ownerMinTPS  float64

	mu       sync.Mutex
	attempts []rpAttempt

	opts    ProxyOptions // the options Use assembled at tune time
	tuned   bool
	holder  *ProxyOptionsHolder
	handler http.Handler
	rec     *httptest.ResponseRecorder
	logBuf  bytes.Buffer
	prevLog io.Writer

	// seams restored after each scenario
	prevServe   func(net.Listener, http.Handler) error
	prevListen  func(int) (net.Listener, error)
	prevHandler func(ProxyOptions) http.Handler
	prevStdin   *os.File
}

func (s *rpNegState) reset() {
	s.cleanup()
	*s = rpNegState{t: s.t, modelsStatus: http.StatusOK, model: "qwen3-32b-fp8", sessionKey: "sk-test-0123", prevLog: log.Writer(),
		prevServe: useServe, prevListen: useListen, prevHandler: newProxyHandler, prevStdin: useStdin}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	log.SetOutput(&s.logBuf)
	// Use's seams: capture the assembled options + handler, never bind a port or read a tty.
	newProxyHandler = func(o ProxyOptions) http.Handler {
		o.SessionKey = s.sessionKey
		s.opts, s.tuned = o, true
		s.holder = NewProxyOptionsHolder(o)
		s.handler = ProxyHandlerLive(s.holder)
		return s.handler
	}
	useListen = func(port int) (net.Listener, error) {
		return addrListener{&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}}, nil
	}
	useServe = func(net.Listener, http.Handler) error { return nil }
}

func (s *rpNegState) cleanup() {
	if s.srv != nil {
		s.srv.Close()
		s.srv = nil
	}
	if s.prevLog != nil {
		log.SetOutput(s.prevLog)
	}
	if s.prevServe != nil {
		useServe, useListen, newProxyHandler, useStdin = s.prevServe, s.prevListen, s.prevHandler, s.prevStdin
	}
}

func (s *rpNegState) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/v1/models" && r.Method == http.MethodGet:
		w.WriteHeader(s.modelsStatus)
		if s.modelsStatus == http.StatusOK {
			_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
		}
		return
	case r.URL.Path == "/discover":
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"offers": []map[string]any{
			{"node_id": "n1", "model": "qwen3-32b-fp8", "online": true, "price_in": 0.1, "price_out": 0.5, "tps": 30.0, "signal": 80},
			{"node_id": "n2", "model": "llama-3.3-70b", "online": true, "price_in": 0.1, "price_out": 0.5, "tps": 30.0, "signal": 80},
		}})
		return
	case r.URL.Path == "/v1/chat/completions":
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		s.mu.Lock()
		s.attempts = append(s.attempts, rpAttempt{headers: r.Header.Clone(), body: body, raw: raw})
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-RogerAI-Provider", "n1")
		w.Header().Set("X-RogerAI-Cost", "0.0001")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}]}`))
		return
	default:
		http.NotFound(w, r)
	}
}

// ---- Background ----

func (s *rpNegState) tunedBand(model string) error { s.model = model; return nil }

func (s *rpNegState) boundWithKey(key string) error { s.sessionKey = key; return nil }

func (s *rpNegState) ownerTuned(maxOut, minTPS float64) error {
	s.ownerMaxOut, s.ownerMinTPS = maxOut, minTPS
	return nil
}

func (s *rpNegState) ownerAlsoMaxIn(maxIn float64) error { s.ownerMaxIn = maxIn; return nil }

func (s *rpNegState) ownerNoCaps() error {
	s.ownerMaxOut, s.ownerMaxIn, s.ownerMinTPS = 0, 0, 0
	return nil
}

func (s *rpNegState) brokerAcceptsBody() error { s.modelsStatus = http.StatusOK; return nil }

// ---- Given ----

func (s *rpNegState) modelsAnswers(code int) error { s.modelsStatus = code; return nil }

func (s *rpNegState) sessionInHeaderMode() error {
	s.modelsStatus = http.StatusNotFound
	return s.tune()
}

// ---- When ----

// tune runs the REAL Use for the current band with --yes; the seams capture the handler.
func (s *rpNegState) tune() error {
	s.tuned = false
	err := Use(s.srv.URL, "u", s.model, UseOptions{Port: 1, MaxOut: s.ownerMaxOut, MaxIn: s.ownerMaxIn, MinTPS: s.ownerMinTPS, Yes: true})
	if err != nil {
		return fmt.Errorf("Use: %v", err)
	}
	if !s.tuned {
		return fmt.Errorf("Use did not open the channel (no handler built)")
	}
	return nil
}

func (s *rpNegState) retuneOther() error {
	s.model = "llama-3.3-70b"
	return s.tune()
}

func (s *rpNegState) chat(body string) error {
	if s.handler == nil {
		// The Background describes the owner's tune; the first chat opens the channel with it.
		if err := s.tune(); err != nil {
			return err
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)).WithContext(context.Background())
	req.Header.Set("Authorization", "Bearer "+s.sessionKey)
	req.Header.Set("Content-Type", "application/json")
	s.rec = httptest.NewRecorder()
	s.handler.ServeHTTP(s.rec, req)
	return nil
}

// chatWithCarrier sends a guest body. By default the guest names the tuned band's model (a
// guest naming any other model with a carrier is refused: guest may only tighten); the form
// `model "X" and <carrier>` names X explicitly.
func (s *rpNegState) chatWithCarrier(carrier string) error {
	if carrier == "no routing carrier" {
		return s.plainChat()
	}
	model := s.model
	if m := rpNegModelAnd.FindStringSubmatch(carrier); m != nil {
		model, carrier = m[1], m[2]
	}
	enc, _ := json.Marshal(model)
	return s.chat(`{"model":` + string(enc) + `,"messages":[{"role":"user","content":"hi"}],` + carrier + `}`)
}

var rpNegModelAnd = regexp.MustCompile(`^model "([^"]+)" and (.+)$`)

// plainChat sends a body with NO carrier and a foreign model id, which the proxy rewrites to
// the band model (model_rewrite.feature).
func (s *rpNegState) plainChat() error {
	return s.chat(`{"model":"anything","messages":[{"role":"user","content":"hi"}]}`)
}

func (s *rpNegState) brokerReceivesModel(want string) error {
	a, err := s.last()
	if err != nil {
		return err
	}
	var got struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(a.raw, &got); err != nil {
		return fmt.Errorf("broker body is not JSON: %v", err)
	}
	if got.Model != want {
		return fmt.Errorf("the broker received model %q, want %q", got.Model, want)
	}
	return nil
}

func (s *rpNegState) last() (rpAttempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.attempts) == 0 {
		return rpAttempt{}, fmt.Errorf("no chat request reached the broker (status %d body %s)", s.rec.Code, s.rec.Body.String())
	}
	return s.attempts[len(s.attempts)-1], nil
}

// ---- Then ----

// sessionMode is both the Given ("the session is in header mode": stand up such a session)
// and the Then (assert the negotiated mode) - godog matches text, not keywords.
func (s *rpNegState) sessionMode(mode string) error {
	if !s.tuned {
		if mode == "header" {
			return s.sessionInHeaderMode()
		}
		return s.tune()
	}
	want := mode == "header"
	if s.opts.HeaderRouting != want {
		return fmt.Errorf("session HeaderRouting = %v, want %s mode", s.opts.HeaderRouting, mode)
	}
	return nil
}

var routingHeaders = []string{"X-Roger-Pref", "X-Roger-Min-TPS", "X-Roger-Confidential", "X-Roger-Node", "X-Roger-Exclude-Nodes"}

func (s *rpNegState) firstChatBodyNoHeaders() error {
	if err := s.plainChat(); err != nil {
		return err
	}
	a, err := s.last()
	if err != nil {
		return err
	}
	if rogerObj(a) == nil {
		return fmt.Errorf("the first chat request carries no roger body object: %s", a.raw)
	}
	for _, h := range routingHeaders {
		if v := a.headers.Get(h); v != "" {
			return fmt.Errorf("the first chat request carries %s=%q", h, v)
		}
	}
	if a.headers.Get("X-Roger-Max-Price-Out") == "" {
		return fmt.Errorf("X-Roger-Max-Price-Out missing (kept for one release)")
	}
	return nil
}

func (s *rpNegState) chatCarriesHeaders(tps, maxOut string) error {
	if err := s.plainChat(); err != nil {
		return err
	}
	a, err := s.last()
	if err != nil {
		return err
	}
	if got := a.headers.Get("X-Roger-Min-TPS"); got != tps {
		return fmt.Errorf("X-Roger-Min-TPS = %q, want %q", got, tps)
	}
	if got := a.headers.Get("X-Roger-Max-Price-Out"); got != maxOut {
		return fmt.Errorf("X-Roger-Max-Price-Out = %q, want %q", got, maxOut)
	}
	return nil
}

func (s *rpNegState) noCarrier() error {
	a, err := s.last()
	if err != nil {
		return err
	}
	if _, ok := a.body["provider"]; ok {
		return fmt.Errorf("body carries a provider object in header mode: %s", a.raw)
	}
	if _, ok := a.body["roger"]; ok {
		return fmt.Errorf("body carries a roger object in header mode: %s", a.raw)
	}
	return nil
}

// bodyNumber reads a dotted key ("provider.max_price.completion") from the body the broker
// received on the last attempt.
func (s *rpNegState) bodyNumber(path string) (float64, error) {
	a, err := s.last()
	if err != nil {
		return 0, err
	}
	var cur any = a.body
	for _, k := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return 0, fmt.Errorf("the broker received no %s (body=%s)", path, a.raw)
		}
		if cur, ok = m[k]; !ok {
			return 0, fmt.Errorf("the broker received no %s (body=%s)", path, a.raw)
		}
	}
	f, ok := cur.(float64)
	if !ok {
		return 0, fmt.Errorf("%s is not a number: %v (body=%s)", path, cur, a.raw)
	}
	return f, nil
}

func (s *rpNegState) brokerReceives(path string, want float64) error {
	got, err := s.bodyNumber(path)
	if err != nil {
		return err
	}
	if got != want {
		a, _ := s.last()
		return fmt.Errorf("the broker received %s = %v, want %v (X-Roger-Max-Price-Out=%q X-Roger-Max-Price=%q)", path, got, want,
			a.headers.Get("X-Roger-Max-Price-Out"), a.headers.Get("X-Roger-Max-Price"))
	}
	return nil
}

func (s *rpNegState) brokerReceivesTwo(p1 string, v1 float64, p2 string, v2 float64) error {
	if err := s.brokerReceives(p1, v1); err != nil {
		return err
	}
	return s.brokerReceives(p2, v2)
}

func (s *rpNegState) guestNoError() error {
	if s.rec.Code != http.StatusOK {
		return fmt.Errorf("guest response status = %d, want 200: %s", s.rec.Code, s.rec.Body.String())
	}
	if strings.Contains(s.rec.Body.String(), `"error"`) {
		return fmt.Errorf("guest response carries an error: %s", s.rec.Body.String())
	}
	return nil
}

func (s *rpNegState) noNegotiationRetry() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := len(s.attempts); n != 1 {
		return fmt.Errorf("%d chat requests reached the broker, want exactly 1 (no retry to negotiate)", n)
	}
	return nil
}

func (s *rpNegState) logLineSays(want string) error {
	n := strings.Count(s.logBuf.String(), want)
	if n != 1 {
		return fmt.Errorf("log contains %q %d time(s), want exactly one line:\n%s", want, n, s.logBuf.String())
	}
	return nil
}

func (s *rpNegState) requestCarriesMinTPS(v string) error {
	a, err := s.last()
	if err != nil {
		return err
	}
	if got := a.headers.Get("X-Roger-Min-TPS"); got != v {
		return fmt.Errorf("X-Roger-Min-TPS = %q, want %q (body=%s)", got, v, a.raw)
	}
	return nil
}

func (s *rpNegState) responseDropped(v string) error {
	if got := s.rec.Header().Get("X-Roger-Routing-Dropped"); got != v {
		return fmt.Errorf("X-Roger-Routing-Dropped = %q, want %q", got, v)
	}
	return nil
}

// nothingReachedBroker: the proxy answered locally - no chat attempt was recorded.
func (s *rpNegState) nothingReachedBroker() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.attempts) > 0 {
		return fmt.Errorf("%d chat request(s) reached the broker, want none", len(s.attempts))
	}
	return nil
}

// brokerReceivesModels: the body the broker received carries exactly this models list.
func (s *rpNegState) brokerReceivesModels(list string) error {
	a, err := s.last()
	if err != nil {
		return err
	}
	var want []string
	if err := json.Unmarshal([]byte(list), &want); err != nil {
		return fmt.Errorf("bad list in the step: %v", err)
	}
	var got struct {
		Models []string `json:"models"`
	}
	if err := json.Unmarshal(a.raw, &got); err != nil {
		return fmt.Errorf("broker body is not JSON: %v", err)
	}
	if strings.Join(got.Models, "\x00") != strings.Join(want, "\x00") {
		return fmt.Errorf("the broker received models %v, want %v", got.Models, want)
	}
	return nil
}

func (s *rpNegState) openAI400(msg string) error {
	if s.rec.Code != http.StatusBadRequest {
		return fmt.Errorf("status = %d, want 400; body=%s", s.rec.Code, s.rec.Body.String())
	}
	var e struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(s.rec.Body.Bytes(), &e); err != nil || e.Error.Type == "" {
		return fmt.Errorf("400 body is not OpenAI-shaped: %s", s.rec.Body.String())
	}
	if !strings.Contains(e.Error.Message, msg) {
		return fmt.Errorf("400 message %q does not contain %q", e.Error.Message, msg)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.attempts) != 0 {
		return fmt.Errorf("a request reached the broker despite the local 400")
	}
	return nil
}

func TestRoutingPassthroughNegotiation(t *testing.T) {
	st := &rpNegState{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				st.cleanup()
				return ctx, nil
			})
			sc.Step(`^a tuned band whose model is "([^"]*)"$`, st.tunedBand)
			sc.Step(`^the local proxy is bound to that band with session key "([^"]*)"$`, st.boundWithKey)
			sc.Step(`^the proxy owner tuned with --max-out ([0-9.]+) --min-tps ([0-9.]+)$`, st.ownerTuned)
			sc.Step(`^the broker accepts the routing body object$`, st.brokerAcceptsBody)
			sc.Step(`^the proxy owner also tuned with --max-in ([0-9.]+)$`, st.ownerAlsoMaxIn)
			sc.Step(`^the proxy owner tuned with no caps at all$`, st.ownerNoCaps)

			sc.Step(`^the broker answers (\d+) to GET /v1/models$`, st.modelsAnswers)
			sc.Step(`^an old broker that answers (\d+) to GET /v1/models$`, st.modelsAnswers)
			sc.Step(`^the broker answers (\d+) to GET /v1/models at tune time$`, st.modelsAnswers)
			sc.Step(`^the broker now answers (\d+) to GET /v1/models$`, st.modelsAnswers)
			sc.Step(`^the session negotiated header mode$`, st.sessionInHeaderMode)

			sc.Step(`^the operator tunes a band$`, st.tune)
			sc.Step(`^the operator re-tunes to another band$`, st.retuneOther)
			sc.Step(`^a chat request arrives with (.+)$`, st.chatWithCarrier)

			sc.Step(`^the session is in (body|header) mode$`, st.sessionMode)
			sc.Step(`^the first chat request carries the body object and no routing headers beyond X-Roger-Max-Price-Out$`, st.firstChatBodyNoHeaders)
			sc.Step(`^a chat request with the owner's defaults carries X-Roger-Min-TPS: ([0-9.]+) and X-Roger-Max-Price-Out: ([0-9.]+)$`, st.chatCarriesHeaders)
			sc.Step(`^it carries no "provider" or "roger" carrier$`, st.noCarrier)
			sc.Step(`^no request is ever retried to negotiate$`, st.noNegotiationRetry)
			sc.Step(`^the broker receives ([a-z_.]+) = ([0-9.]+)$`, st.brokerReceives)
			sc.Step(`^the broker receives ([a-z_.]+) = ([0-9.]+) and ([a-z_.]+) = ([0-9.]+)$`, st.brokerReceivesTwo)
			sc.Step(`^the guest's response carries no error$`, st.guestNoError)
			sc.Step(`^one proxy log line says "([^"]*)"$`, st.logLineSays)
			sc.Step(`^the request carries X-Roger-Min-TPS: ([0-9.]+)$`, st.requestCarriesMinTPS)
			sc.Step(`^the guest's response carries X-Roger-Routing-Dropped: "([^"]*)"$`, st.responseDropped)
			sc.Step(`^the guest receives an OpenAI-shaped 400 "([^"]*)"$`, st.openAI400)
			sc.Step(`^nothing reaches the broker$`, st.nothingReachedBroker)
			sc.Step(`^the broker receives models (\[.*\])$`, st.brokerReceivesModels)
			sc.Step(`^the broker receives model "([^"]+)"$`, st.brokerReceivesModel)
			sc.Step(`^the broker receives model "([^"]+)" and models (\[.*\])$`, func(model, models string) error {
				if err := st.brokerReceivesModel(model); err != nil {
					return err
				}
				return st.brokerReceivesModels(models)
			})
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/proxy/routing_passthrough.feature"},
			Tags:     "@slice0",
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("routing passthrough negotiation scenarios failed (see godog output above)")
	}
}
