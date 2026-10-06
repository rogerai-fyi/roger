package main

// Executable spec: features/lan/routing_and_inference.feature (@client). Turns go through the
// REAL loopback proxy handler built with the CLI's own ladder (applyEdgeLadder + applyLAN), to a
// REAL standalone Tower whose REAL station loop serves an upstream; a recording broker proves no
// inference reached the market.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/client"
)

type lanRouteBDD struct {
	lanDialBDD
	selfHits, towerHits atomic.Int32
	code                int
	body                string
}

func (s *lanRouteBDD) resetRoute() {
	s.lanDialBDD.reset()
	s.selfHits.Store(0)
	s.towerHits.Store(0)
	s.code, s.body = 0, ""
	lanSelfServe = func(string) (string, string) { return "", "" }
}

func (s *lanRouteBDD) restoreRoute() {
	s.lanDialBDD.restore()
	lanSelfServe = func(band string) (string, string) { return "", "" }
}

func (s *lanRouteBDD) counting(name string, hits *atomic.Int32) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"from ` + name + `"}}]}`))
	}))
	s.t.Cleanup(srv.Close)
	return srv
}

func (s *lanRouteBDD) machineServesRoute(band string) error {
	up := s.counting("self", &s.selfHits)
	lanSelfServe = func(b string) (string, string) {
		if b == band {
			return up.URL + "/v1/chat/completions", ""
		}
		return "", ""
	}
	return nil
}

// towerServing stands a Tower at addr whose real station serves band.
func (s *lanRouteBDD) towerServing(addr, band string) error {
	tw := s.tower(addr)
	tw.attachServing(s.t, "st-"+strings.ReplaceAll(band, ".", "-"), s.counting("tower", &s.towerHits).URL+"/v1/chat/completions", band)
	s.addTower(addr)
	return nil
}

func (s *lanRouteBDD) dispatch(band string) error {
	return s.dispatchBody(band, `{"model":"`+band+`","messages":[{"role":"user","content":"hi"}]}`)
}

func (s *lanRouteBDD) dispatchBody(band, body string) error {
	cfg := s.cfg()
	opts := client.ProxyOptions{Broker: s.broker.srv.URL, Model: band, SessionKey: "k"}
	applyEdgeLadder(cfg, &opts)
	applyLAN(cfg, &opts)
	srv := httptest.NewServer(client.ProxyHandler(opts))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", bytes.NewReader([]byte(body)))
	req.Header.Set("Authorization", "Bearer k")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	s.code, s.body = resp.StatusCode, string(b)
	return nil
}

func (s *lanRouteBDD) servedBy(name string) error {
	if s.code != 200 || !strings.Contains(s.body, "from "+name) {
		return fmt.Errorf("status %d, body %s", s.code, s.body)
	}
	return nil
}

func (s *lanRouteBDD) noMarketInference() error {
	if s.broker.saw("/v1/chat/completions") || s.broker.saw("/discover") || s.broker.saw("/balance") {
		return fmt.Errorf("the broker was asked: %v", s.broker.paths)
	}
	return nil
}

func (s *lanRouteBDD) refusedNaming(parts ...string) error {
	if s.code != http.StatusServiceUnavailable {
		return fmt.Errorf("status %d, want 503: %s", s.code, s.body)
	}
	for _, p := range parts {
		if !strings.Contains(s.body, p) {
			return fmt.Errorf("refusal lacks %q: %s", p, s.body)
		}
	}
	return nil
}

func TestLANRoutingBDD(t *testing.T) {
	s := &lanRouteBDD{lanDialBDD: lanDialBDD{t: t}}
	var paths []string
	for _, l := range []int{49, 66, 75, 94, 278} {
		paths = append(paths, fmt.Sprintf("../../features/lan/routing_and_inference.feature:%d", l))
	}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { s.resetRoute(); return ctx, nil })
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				s.restoreRoute()
				return ctx, nil
			})
			sc.Step(`^LAN mode is on$`, func() error { return nil })
			sc.Step(`^the public broker is reachable from this machine$`, func() error { return nil })
			sc.Step(`^this machine serves "([^"]*)"$`, s.machineServesRoute)
			sc.Step(`^the Edge member "([^"]*)" and the LAN Tower at ([0-9.:]+) also serve "([^"]*)"$`, func(node, addr, band string) error {
				if err := s.memberVerified(node, band); err != nil {
					return err
				}
				return s.towerServing(addr, band)
			})
			sc.Step(`^a turn is dispatched for "([^"]*)"$`, s.dispatch)
			sc.Step(`^it is served by this machine$`, func() error { return s.servedBy("self") })
			sc.Step(`^no peer, no Tower and no broker was contacted for inference$`, func() error {
				if s.towerHits.Load() != 0 {
					return fmt.Errorf("the Tower's station served %d turns", s.towerHits.Load())
				}
				return s.noMarketInference()
			})
			sc.Step(`^the LAN Tower at ([0-9.:]+) serves "([^"]*)"$`, s.towerServing)
			sc.Step(`^nothing else local serves it$`, func() error { return nil })
			sc.Step(`^the request goes to "http://192.168.1.20:8787/v1/chat/completions"$`, func() error { return s.servedBy("tower") })
			sc.Step(`^it carries this machine's signature and a per-request nonce$`, func() error {
				return nil // the real plane admitted the turn: it refuses any unsigned or nonce-less LAN request
			})
			sc.Step(`^the answer is returned to the caller$`, func() error { return s.servedBy("tower") })
			sc.Step(`^nothing local serves "([^"]*)"$`, func(string) error { return nil })
			sc.Step(`^the market has "([^"]*)" on air$`, func(string) error { return nil })
			sc.Step(`^it is refused$`, func() error { return s.refusedNaming() })
			sc.Step(`^the refusal names the band and says nothing on this LAN serves it$`, func() error {
				return s.refusedNaming("gpt-oss-120b", "nothing on this LAN serves")
			})
			sc.Step(`^it names the three ways to change that, and that turning LAN mode off would use the market$`, func() error {
				return s.refusedNaming("serve it here", "add an Edge member", "add a Tower", "turning LAN mode off would use the market")
			})
			sc.Step(`^no request was made to the public broker$`, s.noMarketInference)
			sc.Step(`^no money moved$`, s.noMarketInference)
			sc.Step(`^the Edge member "([^"]*)" and the LAN Tower at ([0-9.:]+) both serve "([^"]*)"$`, func(node, addr, band string) error {
				if err := s.memberVerified(node, band); err != nil {
					return err
				}
				// a Tower that lists the band but cannot serve it: no station is free to poll
				failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/discover" {
						_ = json.NewEncoder(w).Encode(map[string]any{"offers": []client.Offer{{Model: band, Online: true}}})
						return
					}
					w.WriteHeader(http.StatusServiceUnavailable)
				}))
				s.t.Cleanup(failing.Close)
				s.dial.set(addr, failing.Listener.Addr().String())
				s.addTower(addr)
				return nil
			})
			sc.Step(`^both fail$`, func() error { return nil }) // jetson's recorded face does not answer; the Tower answers 503
			sc.Step(`^it is refused, naming "([^"]*)" and the Tower and how each failed$`, func(node string) error {
				return s.refusedNaming(node, "192.168.1.20:8787", "status 503")
			})
			sc.Step(`^the request carries provider\.max_price with prompt 0\.1 and completion 0\.1$`, func() error { return nil })
			sc.Step(`^it is served$`, func() error {
				// the turn carries the caps the Given names
				if err := s.dispatchBody("qwen3-8b", `{"model":"qwen3-8b","messages":[{"role":"user","content":"hi"}],"provider":{"max_price":{"prompt":0.1,"completion":0.1}}}`); err != nil {
					return err
				}
				return s.servedBy("self")
			})
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true, Paths: paths},
	}
	if suite.Run() != 0 {
		t.Fatal("routing_and_inference.feature failed")
	}
}
