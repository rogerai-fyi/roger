package main

// Executable spec: features/lan/regressions.feature (@tui chat, @agent, @console, @client use).
// Without LAN mode, under edge.prefer local-only, the process installs the one relay
// (installLANRelay), so TUI chat and console chat (client.ChatTurns) and the agent (the broker
// completer) reach a pinned Edge member face over mTLS and never the broker. `roger use <node>`
// runs the REAL CLI in-process and opens its REAL proxy handler.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/harness"
	"rogerai.fm/roger/v6/internal/lanmode"
)

type lanRegressBDD struct {
	lanUseBDD
	reply string
}

// localOnly is this process with LAN mode off and edge.prefer local-only, as `roger` starts it.
func (s *lanRegressBDD) localOnly() error {
	c := loadConfig()
	c.EdgePrefer = "local-only"
	if err := saveConfig(c); err != nil {
		return err
	}
	return nil
}

func (s *lanRegressBDD) offCfg() config {
	c := loadConfig()
	c.lan = lanmode.Mode{}
	return c
}

func TestLANRegressionsBDD(t *testing.T) {
	s := &lanRegressBDD{lanUseBDD: lanUseBDD{lanRouteBDD: lanRouteBDD{lanDialBDD: lanDialBDD{t: t}}}}
	var paths []string
	for _, l := range []int{32, 41, 49, 57} {
		paths = append(paths, fmt.Sprintf("../../features/lan/regressions.feature:%d", l))
	}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.resetUse()
				s.reply = ""
				client.SetLANRelay(nil)
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				client.SetLANRelay(nil)
				s.restoreRoute()
				return ctx, nil
			})
			sc.Step(`^LAN mode is off and edge\.prefer is "local-only"$`, s.localOnly)
			sc.Step(`^LAN mode is off$`, func() error { return nil })
			sc.Step(`^the Edge member "([^"]*)" serves "([^"]*)"$`, func(node, band string) error {
				if err := s.face(node, band, 0); err != nil {
					return err
				}
				installLANRelay(s.offCfg()) // what `roger` does at start, after the fleet is read
				return nil
			})
			chat := func(band string) error {
				res, err := client.ChatTurns(s.broker.srv.URL, "u", band, []client.ChatTurn{{Role: "user", Content: "hi"}}, false, 0, "", nil)
				if err != nil {
					return err
				}
				s.reply = res.Reply
				return nil
			}
			sc.Step(`^the owner sends a chat message on "([^"]*)" in the TUI$`, chat)
			sc.Step(`^a chat message is sent from the console on "([^"]*)"$`, chat)
			sc.Step(`^the TUI agent takes a turn on "([^"]*)"$`, func(band string) error {
				msg, err := harness.BrokerCompleterRoute(harness.BrokerRoute{Broker: s.broker.srv.URL, User: "u", Model: band})(
					context.Background(), []harness.Message{{Role: "user", Content: "hi"}}, nil)
				if err != nil {
					return err
				}
				s.reply = msg.Content
				return nil
			})
			sc.Step(`^it is served by "([^"]*)"$`, func(node string) error {
				if !strings.Contains(s.reply, "from "+node) {
					return fmt.Errorf("the reply %q was not from %s", s.reply, node)
				}
				if s.faces[node].mutualTLS.Load() == 0 {
					return fmt.Errorf("%s answered without the caller's certificate", node)
				}
				return nil
			})
			sc.Step(`^no request was made to the broker$`, func() error {
				for _, p := range s.broker.paths {
					if p != "/local/poll" {
						return fmt.Errorf("the broker was asked %s", p)
					}
				}
				return nil
			})
			sc.Step(`^the market has no station for "([^"]*)"$`, func(string) error { return nil })
			sc.Step(`^they run "([^"]*)"$`, s.runUse)
			sc.Step(`^the proxy opens pinned to "([^"]*)"$`, func(node string) error {
				if !s.opened || !strings.Contains(s.out, "edge: "+node) {
					return fmt.Errorf("not pinned to %s: %v\n%s", node, s.err, s.out)
				}
				if err := s.turn("gpt-oss-20b"); err != nil {
					return err
				}
				return s.servedBy(node)
			})
			sc.Step(`^it does not print "([^"]*)"$`, func(text string) error {
				if strings.Contains(s.out, text) {
					return fmt.Errorf("it printed %q:\n%s", text, s.out)
				}
				return nil
			})
			sc.Step(`^no request was made to the broker's /discover before the peer was dialed$`, func() error {
				return brokerNotAsked(s.broker, "/discover", "/balance")
			})
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true, Paths: paths},
	}
	if suite.Run() != 0 {
		t.Fatal("regressions.feature failed")
	}
}
