package harness

// routing_pins_bdd_test.go makes the @harness scenario of features/routing/regression_pins.feature
// EXECUTABLE: an agent turn with MaxPriceOut 0 still carries the effective consumer out-cap
// ($10/1M, client.EffectiveMaxOut) as X-Roger-Max-Price-Out - the harness is just another
// consume path and is bounded exactly like `roger use`. It drives the REAL BrokerCompleterRoute
// against a recording httptest broker (the established seam for harness specs: the broker
// is a separate binary, and the completer's contract is the HTTP it emits). The Background
// lines are broker-side givens; here they only fix what the recorder stands in for.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/cucumber/godog"
)

type rpState struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	headers []http.Header
	cap     string
}

func (s *rpState) reset(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // the completer signs with a fresh user key
	*s = rpState{t: t, cap: "10"}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.headers = append(s.headers, r.Header.Clone())
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"roger"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
}

func (s *rpState) noop() error { return nil }
func (s *rpState) defaultCap(v string) error {
	if v != "10" {
		return fmt.Errorf("this runner's recorder stands in for a broker whose default cap is $10/1M; got %s", v)
	}
	s.cap = v
	return nil
}
func (s *rpState) ceiling(string) error { return nil }

func (s *rpState) turnWithMaxOut(maxOut float64) error {
	complete := BrokerCompleterRoute(BrokerRoute{Broker: s.srv.URL, User: "u-h", Model: "qwen3-32b", MaxOut: maxOut})
	_, err := complete(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	return err
}

func (s *rpState) brokerReceives(name, want string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.headers) == 0 {
		return fmt.Errorf("no request reached the broker")
	}
	if got := s.headers[len(s.headers)-1].Get(name); got != want {
		return fmt.Errorf("header %s = %q, want %q", name, got, want)
	}
	return nil
}

func TestRoutingPinsHarness(t *testing.T) {
	s := &rpState{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.reset(t)
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				s.srv.Close()
				return ctx, nil
			})
			sc.Step(`^a broker with an empty in-memory node registry$`, s.noop)
			sc.Step(`^the fee rate is (\d+)%$`, func(int) error { return nil })
			sc.Step(`^the consumer default out-cap is \$([0-9.]+)/1M$`, s.defaultCap)
			sc.Step(`^the register out-price ceiling is \$([0-9.]+)/1M$`, s.ceiling)
			sc.Step(`^the agent harness runs a turn with MaxPriceOut ([0-9.]+)$`, s.turnWithMaxOut)
			sc.Step(`^the broker receives the header (X-Roger-[A-Za-z-]+) "([^"]+)"$`, s.brokerReceives)
		},
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"../../features/routing/regression_pins.feature"},
			Tags:     "@harness",
			TestingT: t,
			Strict:   true,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("features/routing/regression_pins.feature (@harness): failing scenarios")
	}
}
