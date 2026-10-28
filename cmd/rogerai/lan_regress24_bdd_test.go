package main

// Executable spec: features/lan/regressions.feature:24 (@client). Outside LAN mode, with the
// broker setting pointed at a REAL standalone Tower, a turn the Tower answers as local and free
// ("X-Roger-Local: 1", "X-Roger-Cost: 0") is recorded as local and free by the client.

import (
	"context"
	"fmt"
	"rogerai.fm/roger/v7/internal/bddrun"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/client"
)

func TestLANRegression24BDD(t *testing.T) {
	s := &lanRouteBDD{lanDialBDD: lanDialBDD{t: t}}
	var towerBase string
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { s.resetRoute(); return ctx, nil })
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				s.restoreRoute()
				return ctx, nil
			})
			sc.Step(`^the broker setting is a standalone Tower$`, func() error {
				tw := s.tower("192.168.1.20:8787")
				tw.attachServing(s.t, "st-desk", s.counting("tower", &s.towerHits).URL+"/v1/chat/completions", "llama-3.3-70b")
				towerBase = tw.srv.URL
				return nil
			})
			sc.Step(`^LAN mode is off$`, func() error { s.off = true; return nil })
			sc.Step(`^a turn is served by it with "X-Roger-Cost: 0" and "X-Roger-Local: 1"$`, func() error {
				s.tweak = func(o *client.ProxyOptions) { o.Broker = towerBase }
				if err := s.dispatch("llama-3.3-70b"); err != nil {
					return err
				}
				if s.code != 200 || s.towerHits.Load() != 1 {
					return fmt.Errorf("the Tower did not serve the turn: %d %s", s.code, s.body)
				}
				return nil
			})
			sc.Step(`^the turn is recorded as local and free$`, func() error {
				if got := s.hdr.Get("X-RogerAI-Cost"); got != "0" {
					return fmt.Errorf("the proxy response carries X-RogerAI-Cost %q, want 0", got)
				}
				for _, r := range s.receipts {
					if r.Local {
						return nil
					}
				}
				return fmt.Errorf("no local receipt was recorded: %+v", s.receipts)
			})
			sc.Step(`^no hold text was shown for it$`, func() error {
				for _, a := range s.alerts {
					if strings.Contains(strings.ToLower(a), "hold") {
						return fmt.Errorf("hold text was shown: %q", a)
					}
				}
				if s.hdr.Get("X-RogerAI-Balance") != "" {
					return fmt.Errorf("a balance was relayed for a local turn")
				}
				return nil
			})
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{"../../features/lan/regressions.feature:24"}},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("regressions.feature:24 failed")
	}
}
