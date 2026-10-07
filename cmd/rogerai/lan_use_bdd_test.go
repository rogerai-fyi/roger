package main

// Executable spec: features/lan/routing_and_inference.feature (@cli `roger use`). The REAL CLI
// runs in-process (`run`), opens its REAL proxy handler through client.SetUseServe (no port is
// held), and a turn is then driven through that handler to a REAL standalone Tower or a pinned
// Edge member face. A recording broker proves what the market was asked.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/client"
)

type lanUseBDD struct {
	lanRouteBDD
	handler http.Handler
	opened  bool
}

func (s *lanUseBDD) resetUse() {
	s.resetRoute()
	// an http broker, so any path the CLI asks of it is recorded
	s.t.Setenv("ROGER_BROKER", s.broker.srv.URL)
	s.handler, s.opened = nil, false
}

func (s *lanUseBDD) runUse(line string) error {
	restore := client.SetUseServe(func(_ string, h http.Handler) error {
		s.handler, s.opened = h, true
		return nil
	})
	defer restore()
	args := strings.Fields(line)[1:]
	out, err := captureEdgeStdout(func() error { return run(args, loadConfig()) })
	s.out, s.err = out, err
	return nil
}

// turn sends one completion through the handler the CLI opened.
func (s *lanUseBDD) turn(model string) error {
	if s.handler == nil {
		return fmt.Errorf("no proxy was opened: %v / %s", s.err, s.out)
	}
	key := ""
	for _, ln := range strings.Split(s.out, "\n") {
		if f := strings.Fields(ln); len(f) == 3 && f[0] == "API" && f[1] == "KEY" {
			key = f[2]
		}
	}
	srv := httptest.NewServer(s.handler)
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions",
		bytes.NewReader([]byte(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`)))
	req.Header.Set("Authorization", "Bearer "+key)
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

func (s *lanUseBDD) brokerNotAsked(paths ...string) error {
	for _, p := range paths {
		if s.broker.saw(p) {
			return fmt.Errorf("the broker was asked %s: %v", p, s.broker.paths)
		}
	}
	return nil
}

func TestLANUseBDD(t *testing.T) {
	s := &lanUseBDD{lanRouteBDD: lanRouteBDD{lanDialBDD: lanDialBDD{t: t}}}
	var paths []string
	for _, l := range []int{144, 152, 160, 170} {
		paths = append(paths, fmt.Sprintf("../../features/lan/routing_and_inference.feature:%d", l))
	}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { s.resetUse(); return ctx, nil })
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				s.restoreRoute()
				return ctx, nil
			})
			sc.Step(`^LAN mode is on$`, func() error { return nil }) // each command line carries --lan
			sc.Step(`^the public broker is reachable from this machine$`, func() error { return nil })
			sc.Step(`^the LAN Tower at ([0-9.:]+) serves "([^"]*)"$`, s.towerServing)
			sc.Step(`^the LAN Tower at ([0-9.:]+) serves "([^"]*)" and "([^"]*)"$`, func(addr, a, b string) error {
				tw := s.tower(addr)
				tw.attachServing(s.t, "st-two", s.counting("tower", &s.towerHits).URL+"/v1/chat/completions", a, b)
				s.addTower(addr)
				return nil
			})
			sc.Step(`^nothing local serves "([^"]*)"$`, func(string) error { return nil })
			sc.Step(`^the Edge member "([^"]*)" serves "([^"]*)"$`, func(node, band string) error { return s.face(node, band, 0) })
			sc.Step(`^the market has no station for "([^"]*)"$`, func(string) error { return nil })
			sc.Step(`^they run "([^"]*)"$`, s.runUse)
			sc.Step(`^the loopback proxy opens$`, func() error {
				if !s.opened || s.err != nil {
					return fmt.Errorf("no proxy opened: %v\n%s", s.err, s.out)
				}
				if err := s.turn("llama-3.3-70b"); err != nil {
					return err
				}
				return s.servedBy("tower")
			})
			sc.Step(`^no request was made to the public broker for /discover, /balance or /bands/resolve$`, func() error {
				return s.brokerNotAsked("/discover", "/balance", "/bands/resolve")
			})
			sc.Step(`^the plate names the source "([^"]*)"$`, func(src string) error {
				if !strings.Contains(s.out, src) {
					return fmt.Errorf("the plate does not name %q:\n%s", src, s.out)
				}
				return nil
			})
			sc.Step(`^it exits non-zero$`, func() error {
				if s.err == nil {
					return fmt.Errorf("it succeeded:\n%s", s.out)
				}
				return nil
			})
			sc.Step(`^the message names the band and the three local sources it checked$`, func() error {
				msg := s.err.Error()
				for _, want := range []string{"gpt-oss-120b", "this machine", "your Edge", "your LAN Towers"} {
					if !strings.Contains(msg, want) {
						return fmt.Errorf("the message lacks %q: %s", want, msg)
					}
				}
				return nil
			})
			sc.Step(`^no proxy port was opened$`, func() error {
				if s.opened {
					return fmt.Errorf("a proxy was opened")
				}
				return nil
			})
			sc.Step(`^the proxy opens pinned to "([^"]*)"$`, func(node string) error {
				if !s.opened || !strings.Contains(s.out, "edge: "+node) {
					return fmt.Errorf("not pinned to %s: %v\n%s", node, s.err, s.out)
				}
				if err := s.turn("gpt-oss-20b"); err != nil {
					return err
				}
				return s.servedBy(node)
			})
			sc.Step(`^no request was made to the public broker$`, func() error {
				return s.brokerNotAsked("/discover", "/balance", "/bands/resolve", "/v1/chat/completions")
			})
			sc.Step(`^the proxy opens pinned to that Tower for "([^"]*)"$`, func(model string) error {
				if !s.opened || !strings.Contains(s.out, "tower: 192.168.1.20:8787") || !strings.Contains(s.out, model) {
					return fmt.Errorf("not pinned to the Tower for %s: %v\n%s", model, s.err, s.out)
				}
				if err := s.turn(model); err != nil {
					return err
				}
				return s.servedBy("tower")
			})
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true, Paths: paths},
	}
	if suite.Run() != 0 {
		t.Fatal("routing_and_inference.feature (@cli use) failed")
	}
}
