package main

// Executable spec: features/lan/regressions.feature (@tui chat, @agent, @console, @client use).
// Without LAN mode, under edge.prefer local-only, the process installs the one relay
// (installLANRelay), so TUI chat and console chat (client.ChatTurns) and the agent (the broker
// completer) reach a pinned Edge member face over mTLS and never the broker. `roger use <node>`
// runs the REAL CLI in-process and opens its REAL proxy handler.

import (
	"context"
	"fmt"
	"net"
	"strconv"
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
	for _, l := range []int{32, 41, 49, 57, 67, 74, 80} {
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
			var ln net.Listener
			var loop, private, public string
			listen := func(bind string) error {
				var err error
				ln, err = edgeListen(bind)
				return err
			}
			sc.Step(`^a machine with a public and a private interface$`, func() error {
				loop, private, public = fakeInterfaces(t)
				return nil
			})
			sc.Step(`^a machine with a public and a private interface that is a local Edge authority$`, func() error {
				loop, private, public = fakeInterfaces(t)
				useTempConfig(t)
				return nil
			})
			sc.Step(`^the Edge host starts its face listener$`, func() error { return listen(edgeFaceBind()) })
			sc.Step(`^the owner set the Edge face bind to an explicit address$`, func() error {
				useTempConfig(t)
				t.Setenv(envFaceBind, "127.0.0.9:0")
				return nil
			})
			sc.Step(`^the Edge host starts$`, func() error { return listen(edgeFaceBind()) })
			sc.Step(`^it listens on that address$`, func() error {
				if got := ln.Addr().(*net.TCPAddr).IP.String(); got != "127.0.0.9" {
					return fmt.Errorf("it listens on %s, want the explicit 127.0.0.9", got)
				}
				return nil
			})
			sc.Step("^`roger edge` output names the explicit bind$", func() error {
				out, _ := edgeRun(t, "edge")
				if !strings.Contains(out, "face bind: 127.0.0.9:0 (explicit") {
					return fmt.Errorf("`roger edge` does not name the explicit bind:\n%s", out)
				}
				return nil
			})
			sc.Step(`^the authority listener starts$`, func() error { return listen(edgeAuthorityBind()) })
			sc.Step(`^it listens on the private interface and loopback only$`, func() error {
				port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
				for _, a := range []string{private, loop} {
					if !dialOK(net.JoinHostPort(a, port)) {
						return fmt.Errorf("%s does not answer on %s", a, port)
					}
				}
				if dialOK(net.JoinHostPort(public, port)) {
					return fmt.Errorf("the public interface %s answers on %s", public, port)
				}
				return nil
			})
			sc.Step(`^a connection to the public interface's address is refused$`, func() error {
				if dialOK(net.JoinHostPort(public, strconv.Itoa(ln.Addr().(*net.TCPAddr).Port))) {
					return fmt.Errorf("the public interface accepted a connection")
				}
				return nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				if ln != nil {
					_ = ln.Close()
					ln = nil
				}
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
