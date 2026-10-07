package client

// idempotency_proxy_bdd_test.go makes the @proxy scenarios of features/routing/idempotency.feature
// EXECUTABLE (slice 6, contract §14.B2): the local proxy mints ONE Idempotency-Key per client
// request and reuses it on its own retries, forwards a client's own key unchanged, does not
// re-pick an alternative when the broker already failed over (X-RogerAI-Attempts > 1), and relays
// a 409 request_in_flight with its Retry-After.
//
// Same seam as routing_regression_pins_bdd_test.go: the REAL ProxyHandlerLive /
// relayWithFailover over the wire, against a scripted stand-in broker (httptest) that records
// every relay attempt and every /discover call. The Background's broker-side givens (registry,
// fee, idempotency window, the station, the funded consumer) configure the stand-in's feed and
// are otherwise the broker runner's to enforce (cmd/rogerai-broker/hardening_idempotency_bdd_test.go).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/cucumber/godog"
)

type id6pAnswer struct {
	transportErr bool
	status       int
	attempts     string // X-RogerAI-Attempts
	retryAfter   string
	code         string
}

type id6pState struct {
	srv      *httptest.Server
	offers   []map[string]any
	answers  map[int]id6pAnswer
	mu       sync.Mutex
	attempts []rpAttempt
	byReq    [][]rpAttempt // attempts grouped per client request
	discover int
	rec      *httptest.ResponseRecorder
}

func (s *id6pState) reset() {
	if s.srv != nil {
		s.srv.Close()
	}
	*s = id6pState{answers: map[int]id6pAnswer{}}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
}

func (s *id6pState) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/discover" {
		s.mu.Lock()
		s.discover++
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"offers": s.offers})
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	s.mu.Lock()
	s.attempts = append(s.attempts, rpAttempt{headers: r.Header.Clone(), body: body, raw: raw})
	ans := s.answers[len(s.attempts)]
	s.mu.Unlock()
	if ans.transportErr {
		if hj, ok := w.(http.Hijacker); ok {
			if c, _, err := hj.Hijack(); err == nil {
				_ = c.Close()
				return
			}
		}
	}
	if ans.status == 0 {
		ans.status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-RogerAI-Provider", "s1")
	if ans.attempts != "" {
		w.Header().Set("X-RogerAI-Attempts", ans.attempts)
	}
	if ans.retryAfter != "" {
		w.Header().Set("Retry-After", ans.retryAfter)
	}
	w.WriteHeader(ans.status)
	switch {
	case ans.code != "":
		fmt.Fprintf(w, `{"error":{"code":%q,"message":"a request with this key is in flight"}}`, ans.code)
	case ans.status == http.StatusOK:
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}]}`))
	default:
		fmt.Fprintf(w, `{"error":{"message":"upstream failed (%d)"}}`, ans.status)
	}
}

func (s *id6pState) offer(node string) {
	s.offers = append(s.offers, map[string]any{"node_id": node, "model": "m", "online": true,
		"price_in": 0.1, "price_out": 0.3, "tps": 30.0, "signal": 80})
}

// ---- Background (broker-side givens at this seam) ----

func (s *id6pState) emptyRegistry() error { s.reset(); return nil }
func (s *id6pState) feeRate(int) error    { return nil }
func (s *id6pState) window(int) error     { return nil }
func (s *id6pState) funded(string) error  { return nil }
func (s *id6pState) onAir(node, _, _, _ string) error {
	s.offer(node)
	s.offer("s2") // an alternative /discover can name, so "re-pick or not" is a real choice
	return nil
}

// ---- Givens ----

func (s *id6pState) firstTransportErr() error {
	s.answers[1] = id6pAnswer{transportErr: true}
	return nil
}

func (s *id6pState) first502Attempts(n string) error {
	s.answers[1] = id6pAnswer{status: http.StatusBadGateway, attempts: n}
	return nil
}

func (s *id6pState) answers409(ra string) error {
	for i := 1; i <= 8; i++ {
		s.answers[i] = id6pAnswer{status: http.StatusConflict, code: "request_in_flight", retryAfter: ra}
	}
	return nil
}

// ---- When ----

func (s *id6pState) send(key string) error {
	opts := ProxyOptions{Broker: s.srv.URL, User: "u", Model: "m", SessionKey: NewSessionKey()}
	h := ProxyHandlerLive(NewProxyOptionsHolder(opts))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`)).WithContext(context.Background())
	req.Header.Set("Authorization", "Bearer "+opts.SessionKey)
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	s.mu.Lock()
	mark := len(s.attempts)
	s.discover = 0
	s.mu.Unlock()
	s.rec = httptest.NewRecorder()
	h.ServeHTTP(s.rec, req)
	s.mu.Lock()
	s.byReq = append(s.byReq, append([]rpAttempt(nil), s.attempts[mark:]...))
	s.mu.Unlock()
	return nil
}

func (s *id6pState) sendsOne() error             { return s.send("") }
func (s *id6pState) sendsWithKey(k string) error { return s.send(k) }

// ---- Thens ----

func (s *id6pState) req(i int) ([]rpAttempt, error) {
	if len(s.byReq) <= i || len(s.byReq[i]) == 0 {
		return nil, fmt.Errorf("client request %d reached the broker on no attempt", i+1)
	}
	return s.byReq[i], nil
}

func (s *id6pState) sameMintedKey() error {
	at, err := s.req(0)
	if err != nil {
		return err
	}
	if len(at) < 2 {
		return fmt.Errorf("the proxy made %d attempt(s), want a retry after the transport error", len(at))
	}
	k := at[0].headers.Get("Idempotency-Key")
	if k == "" {
		return fmt.Errorf("the proxy's first attempt carries no Idempotency-Key")
	}
	for i, a := range at {
		if got := a.headers.Get("Idempotency-Key"); got != k {
			return fmt.Errorf("attempt %d carries Idempotency-Key %q, attempt 1 carried %q", i+1, got, k)
		}
	}
	return nil
}

func (s *id6pState) differentKey() error {
	if err := s.sendsOne(); err != nil {
		return err
	}
	a, err := s.req(0)
	if err != nil {
		return err
	}
	b, err := s.req(1)
	if err != nil {
		return err
	}
	ka, kb := a[0].headers.Get("Idempotency-Key"), b[0].headers.Get("Idempotency-Key")
	if kb == "" || ka == kb {
		return fmt.Errorf("the second client request carries Idempotency-Key %q (first %q), want a different non-empty key", kb, ka)
	}
	return nil
}

func (s *id6pState) everyCarries(k string) error {
	at, err := s.req(0)
	if err != nil {
		return err
	}
	for i, a := range at {
		if got := a.headers.Get("Idempotency-Key"); got != k {
			return fmt.Errorf("attempt %d carries Idempotency-Key %q, want %q", i+1, got, k)
		}
	}
	return nil
}

func (s *id6pState) retry() (rpAttempt, error) {
	at, err := s.req(0)
	if err != nil {
		return rpAttempt{}, err
	}
	if len(at) < 2 {
		return rpAttempt{}, fmt.Errorf("the proxy made %d attempt(s), want a retry after the 502", len(at))
	}
	return at[1], nil
}

func (s *id6pState) retryNoOrder() error {
	a, err := s.retry()
	if err != nil {
		return err
	}
	if o := stringList(providerObj(a)["order"]); len(o) > 0 {
		return fmt.Errorf("the proxy's retry carries provider.order %v though the broker already made 3 attempts", o)
	}
	return nil
}

func (s *id6pState) noDiscover() error {
	s.mu.Lock()
	n := s.discover
	s.mu.Unlock()
	if n != 0 {
		return fmt.Errorf("the proxy consulted /discover %d time(s) after a broker that already failed over", n)
	}
	return nil
}

func (s *id6pState) retryHasOrder() error {
	a, err := s.retry()
	if err != nil {
		return err
	}
	o := stringList(providerObj(a)["order"])
	if len(o) != 1 || o[0] == "s1" {
		return fmt.Errorf("the proxy's retry provider.order = %v, want one alternative station (not s1)", o)
	}
	return nil
}

func (s *id6pState) guestSees(code int, ra string) error {
	if s.rec.Code != code {
		return fmt.Errorf("the guest saw %d, want %d: %s", s.rec.Code, code, s.rec.Body.String())
	}
	if got := s.rec.Header().Get("Retry-After"); got != ra {
		return fmt.Errorf("the guest saw Retry-After %q, want %q", got, ra)
	}
	if !strings.Contains(s.rec.Body.String(), "request_in_flight") {
		return fmt.Errorf("the guest's body lost the request_in_flight code: %s", s.rec.Body.String())
	}
	return nil
}

func TestIdempotencyProxyBDD(t *testing.T) {
	s := &id6pState{}
	suite := godog.TestSuite{
		Name: "idempotency-proxy",
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.reset()
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, err error) (context.Context, error) {
				if s.srv != nil {
					s.srv.Close()
				}
				return ctx, err
			})
			sc.Step(`^a broker with an empty in-memory node registry$`, s.emptyRegistry)
			sc.Step(`^the fee rate is (\d+)%$`, s.feeRate)
			sc.Step(`^the idempotency window is (\d+) minutes$`, s.window)
			sc.Step(`^station "([^"]+)" is on air for "([^"]+)" at in \$([0-9.]+) out \$([0-9.]+) per 1M$`, s.onAir)
			sc.Step(`^"([^"]+)" is a funded consumer$`, s.funded)
			sc.Step(`^the broker answers the first proxy attempt with a transport error$`, s.firstTransportErr)
			sc.Step(`^the broker answers the first proxy attempt 502 with X-RogerAI-Attempts "(\d+)"$`, s.first502Attempts)
			sc.Step(`^the broker answers 409 request_in_flight with Retry-After "(\d+)"$`, s.answers409)
			sc.Step(`^a guest sends one chat request through the local proxy(?: with no Idempotency-Key)?$`, s.sendsOne)
			sc.Step(`^a guest sends a chat request through the local proxy with Idempotency-Key "([^"]+)"$`, s.sendsWithKey)
			sc.Step(`^every proxy attempt for it carries the same Idempotency-Key$`, s.sameMintedKey)
			sc.Step(`^a different client request carries a different key$`, s.differentKey)
			sc.Step(`^every proxy attempt carries Idempotency-Key "([^"]+)"$`, s.everyCarries)
			sc.Step(`^the proxy's retry carries no provider\.order$`, s.retryNoOrder)
			sc.Step(`^the proxy does not consult /discover for an alternative$`, s.noDiscover)
			sc.Step(`^the proxy's retry carries provider\.order naming an alternative station$`, s.retryHasOrder)
			sc.Step(`^the guest sees (\d+) with Retry-After "(\d+)"$`, s.guestSees)
		},
		Options: &godog.Options{Format: "pretty", Paths: []string{"../../features/routing/idempotency.feature"},
			Tags: "@proxy", TestingT: t, Strict: true},
	}
	if suite.Run() != 0 {
		t.Fatal("idempotency proxy scenarios failed")
	}
}
