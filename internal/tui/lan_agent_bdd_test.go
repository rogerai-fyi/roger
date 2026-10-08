package tui

// Executable spec: features/lan/routing_and_inference.feature:203 (@agent). In LAN mode an agent
// turn on this machine's own model goes STRAIGHT to the local server (harness.LocalCompleter), the
// operator's own hardware, not through the LAN relay the other agent turns use.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"rogerai.fm/roger/v6/internal/bddrun"
	"sync/atomic"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/detect"
	"rogerai.fm/roger/v6/internal/harness"
)

type lanAgentBDD struct {
	t                    *testing.T
	m                    model
	localHits, relayHits atomic.Int32
}

func (s *lanAgentBDD) answering(name string, hits *atomic.Int32) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"from ` + name + `"}}]}`))
	}))
	s.t.Cleanup(srv.Close)
	return srv
}

func (s *lanAgentBDD) running(band string) error {
	local := s.answering("local", &s.localHits)
	relayed := s.answering("relay", &s.relayHits)
	// LAN mode's one relay for this process: a turn that went through it would land here.
	client.SetLANRelay(&client.ProxyOptions{LANMode: true, EdgePrefer: "local-only",
		LANSelf: func(string) (string, string) { return relayed.URL + "/v1/chat/completions", "" }})
	s.t.Cleanup(func() { client.SetLANRelay(nil) })
	m := agentWithLocals(s.t)
	m.localFound = []detect.Found{{Name: "llama.cpp", BaseURL: local.URL + "/v1",
		Chat: local.URL + "/v1/chat/completions", Models: []string{band}, Ctx: map[string]int{band: 32768}}}
	pm, _ := m.openAgentModelPicker()
	s.m = asModel(pm)
	s.m.pickAgentModel(band)
	if s.m.agent.localChat == "" {
		return fmt.Errorf("the agent did not bind this machine's model %q", band)
	}
	return nil
}

func (s *lanAgentBDD) turn() error {
	_, err := s.m.agent.loop.Send(context.Background(), "hi", func(harness.Event) {})
	return err
}

func (s *lanAgentBDD) direct() error {
	if s.localHits.Load() != 1 || s.relayHits.Load() != 0 {
		return fmt.Errorf("local server took %d turn(s), the LAN relay %d; want 1 and 0", s.localHits.Load(), s.relayHits.Load())
	}
	return nil
}

func TestLANAgentLocalBDD(t *testing.T) {
	s := &lanAgentBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Step(`^LAN mode is on$`, func() error { return nil })
			sc.Step(`^the public broker is reachable from this machine$`, func() error { return nil })
			sc.Step(`^the TUI agent is running in LAN mode on "([^"]*)" served by this machine$`, s.running)
			sc.Step(`^the agent takes a turn$`, s.turn)
			sc.Step(`^it is served by the local completer directly$`, s.direct)
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{"../../features/lan/routing_and_inference.feature:203"}},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("routing_and_inference.feature:203 failed")
	}
}
