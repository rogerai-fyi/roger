package main

// Executable spec: features/lan/routing_and_inference.feature (@tui, @agent, @operator, @console).
// Each path is driven through the code the process actually uses: the dispatch hook every
// front-end installs (frontEndLadder) on the REAL proxy handler a tune binds and a guest operator
// is handed; client.ChatTurns (TUI chat and console chat) and the agent's broker completer with
// the relay a LAN-mode process installs (installLANRelay). Sources are a REAL standalone Tower, a
// pinned Edge member face over mTLS, or this machine's serve; a recording broker proves the
// market was never asked.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"rogerai.fm/roger/v7/internal/bddrun"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/client"
	"rogerai.fm/roger/v7/internal/harness"
)

type lanFrontBDD struct {
	lanRouteBDD
	tuned   string
	handler http.Handler
	reply   string
}

func (s *lanFrontBDD) resetFront() {
	s.resetRoute()
	s.tuned, s.handler, s.reply = "", nil, ""
	client.SetLANRelay(nil)
}

// tune binds the proxy the way a front-end does: its options, then the hook it installs.
func (s *lanFrontBDD) tune(band string) {
	s.tuned = band
	opts := client.ProxyOptions{Broker: s.broker.srv.URL, User: "u", Model: band, SessionKey: "k"}
	frontEndLadder(s.cfg())(&opts)
	s.handler = client.ProxyHandler(opts)
	installLANRelay(s.cfg())
}

func (s *lanFrontBDD) send(path, method, body string) error {
	srv := httptest.NewServer(s.handler)
	defer srv.Close()
	req, _ := http.NewRequest(method, srv.URL+path, bytes.NewReader([]byte(body)))
	req.Header.Set("Authorization", "Bearer k")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	s.code, s.body, s.hdr = resp.StatusCode, string(b), resp.Header
	return nil
}

func (s *lanFrontBDD) completion(model string) error {
	return s.send("/v1/chat/completions", http.MethodPost, `{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)
}

func (s *lanFrontBDD) chat() error {
	res, err := client.ChatTurns(s.broker.srv.URL, "u", s.tuned, []client.ChatTurn{{Role: "user", Content: "hi"}}, false, 0, "", nil)
	if err != nil {
		return err
	}
	s.reply = res.Reply
	return nil
}

func (s *lanFrontBDD) replyFrom(name string) error {
	if !strings.Contains(s.reply, "from "+name) {
		return fmt.Errorf("the reply %q was not from %s", s.reply, name)
	}
	return nil
}

func (s *lanFrontBDD) noBroker() error {
	return brokerNotAsked(s.broker, "/discover", "/balance", "/bands/resolve", "/v1/chat/completions")
}

func TestLANFrontEndsBDD(t *testing.T) {
	s := &lanFrontBDD{lanRouteBDD: lanRouteBDD{lanDialBDD: lanDialBDD{t: t}}}
	var paths []string
	for _, l := range []int{179, 187, 195, 209, 217, 224, 231} {
		paths = append(paths, fmt.Sprintf("../../features/lan/routing_and_inference.feature:%d", l))
	}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { s.resetFront(); return ctx, nil })
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				client.SetLANRelay(nil)
				s.restoreRoute()
				return ctx, nil
			})
			sc.Step(`^LAN mode is on$`, func() error { return nil })
			sc.Step(`^the public broker is reachable from this machine$`, func() error { return nil })
			sc.Step(`^the TUI is open in LAN mode$`, func() error { installLANRelay(s.cfg()); return nil })
			sc.Step(`^the dial lists "([^"]*)" from the LAN Tower at ([0-9.:]+)$`, func(band, addr string) error {
				return s.towerServing(addr, band)
			})
			sc.Step(`^the owner tunes "([^"]*)" and confirms$`, func(band string) error { s.tune(band); return nil })
			sc.Step(`^the live proxy routes to that Tower$`, func() error {
				if err := s.completion(s.tuned); err != nil {
					return err
				}
				return s.servedBy("tower")
			})
			sc.Step(`^no request was made to the public broker$`, s.noBroker)
			sc.Step(`^the TUI is tuned to "([^"]*)" from the LAN Tower at ([0-9.:]+)$`, func(band, addr string) error {
				if err := s.towerServing(addr, band); err != nil {
					return err
				}
				s.tune(band)
				return nil
			})
			sc.Step(`^the owner sends a chat message$`, s.chat)
			sc.Step(`^the turn is served by that Tower$`, func() error { return s.replyFrom("tower") })
			sc.Step(`^no request was made to the public broker's /v1/chat/completions$`, func() error {
				return brokerNotAsked(s.broker, "/v1/chat/completions")
			})
			sc.Step(`^the TUI agent is running in LAN mode on "([^"]*)" served by the Edge member "([^"]*)"$`, func(band, node string) error {
				if err := s.face(node, band, 0); err != nil {
					return err
				}
				s.tune(band)
				return nil
			})
			sc.Step(`^the agent takes a turn$`, func() error {
				msg, err := harness.BrokerCompleterRoute(harness.BrokerRoute{Broker: s.broker.srv.URL, User: "u", Model: s.tuned})(
					context.Background(), []harness.Message{{Role: "user", Content: "hi"}}, nil)
				if err != nil {
					return err
				}
				s.reply = msg.Content
				return nil
			})
			sc.Step(`^the completion is served by "([^"]*)"$`, s.replyFrom)
			sc.Step(`^the TUI is tuned in LAN mode to "([^"]*)" served by the Edge member "([^"]*)"$`, func(band, node string) error {
				if err := s.face(node, band, 0); err != nil {
					return err
				}
				s.tune(band)
				return nil
			})
			sc.Step(`^the guest operator "([^"]*)" is handed the mic$`, func(string) error { return nil }) // it is given the proxy's base URL
			sc.Step(`^the operator sends a completion through the loopback proxy$`, func() error { return s.completion(s.tuned) })
			sc.Step(`^it is served by "([^"]*)"$`, s.servedBy)
			sc.Step(`^the TUI is tuned in LAN mode$`, func() error {
				if err := s.machineServesRoute("qwen3-8b"); err != nil {
					return err
				}
				s.tune("qwen3-8b")
				return nil
			})
			sc.Step(`^the guest operator requests GET /v1/models from the loopback proxy$`, func() error {
				return s.send("/v1/models", http.MethodGet, "")
			})
			sc.Step(`^the list contains only bands from the local sources$`, func() error {
				var list struct {
					Data []struct {
						ID string `json:"id"`
					} `json:"data"`
				}
				if err := json.Unmarshal([]byte(s.body), &list); err != nil || len(list.Data) == 0 {
					return fmt.Errorf("no model list: %s", s.body)
				}
				for _, m := range list.Data {
					if m.ID != "qwen3-8b" {
						return fmt.Errorf("the list carries %q, which nothing local serves", m.ID)
					}
				}
				return nil
			})
			sc.Step(`^the guest operator sends a completion for "([^"]*)" which nothing local serves$`, s.completion)
			sc.Step(`^the proxy answers 503 with a message that nothing on this LAN serves it$`, func() error {
				return s.refusedNaming("gpt-oss-120b", "nothing on this LAN serves")
			})
			sc.Step(`^"roger --lan webui" is running and tuned to "([^"]*)" served by this machine$`, func(band string) error {
				if err := s.machineServesRoute(band); err != nil {
					return err
				}
				s.tune(band)
				return nil
			})
			sc.Step(`^a chat message is sent from the console$`, s.chat) // the console's chat sends through client.ChatTurns
			sc.Step(`^it is served by this machine$`, func() error { return s.replyFrom("self") })
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true, Paths: paths},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("routing_and_inference.feature (front ends) failed")
	}
}
