package main

// share_market_consent_bdd_test.go runs features/sharing/market_offer_consent.feature (founder
// ruling 2026-10-11: confirm before offering on the market). Detection is faked as one local
// server; "went on air" is the agentStart seam being called, which is what registers a share
// with the broker.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/agent"
	"rogerai.fm/roger/v7/internal/bddrun"
	"rogerai.fm/roger/v7/internal/detect"
)

const marketConsentFeature = "../../features/sharing/market_offer_consent.feature"

type marketConsentBDD struct {
	t       *testing.T
	found   []detect.Found
	served  []string // base URLs roger serves (rogerServedBases)
	saved   string   // the setup wizard's saved model
	started []string // models agentStart was called with
	out     string
	err     error
}

func (s *marketConsentBDD) run(args []string, tty bool, answer string) error {
	s.t.Setenv("XDG_CONFIG_HOME", s.t.TempDir())
	prevD, prevServed, prevTTY, prevIn := detectFull, rogerServedBases, shareInteractive, shareStdin
	prevStart, prevBlock := agentStart, shareBlock
	detectFull = func(...string) ([]detect.Found, []string) { return s.found, nil }
	rogerServedBases = func() []string { return s.served }
	shareInteractive = func() bool { return tty }
	shareStdin = strings.NewReader(answer + "\n")
	agentStart = func(c agent.Config) (*agent.Session, error) {
		s.started = append(s.started, c.Model)
		return &agent.Session{}, nil
	}
	shareBlock = func() {}
	prevFabric := startRelayFabric
	startRelayFabric = func(agent.Config) {} // the relay join is not what this spec checks
	defer func() {
		detectFull, rogerServedBases, shareInteractive, shareStdin = prevD, prevServed, prevTTY, prevIn
		agentStart, shareBlock, startRelayFabric = prevStart, prevBlock, prevFabric
	}()
	cfg := config{Broker: "http://127.0.0.1:1", User: "u"}
	if s.saved != "" {
		cfg.Share = &Share{Model: s.saved}
	}
	s.out = captureStdout(s.t, func() {
		done := make(chan error, 1)
		go func() { done <- cmdShare(cfg, args) }()
		select {
		case s.err = <-done:
		case <-time.After(10 * time.Second):
			s.err = fmt.Errorf("cmdShare did not return")
		}
	})
	return nil
}

func TestShareMarketConsentBDD(t *testing.T) {
	s := &marketConsentBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				*s = marketConsentBDD{t: t}
				return ctx, nil
			})
			sc.Step(`^a local server on this machine has "([^"]*)" on air$`, func(m string) error {
				s.found = append(s.found, detect.Found{Name: "port:8081", BaseURL: "http://127.0.0.1:8081/v1",
					Chat: "http://127.0.0.1:8081/v1/chat/completions", Models: []string{m}})
				return nil
			})
			sc.Step(`^a local proxy to hosted accounts on this machine has "([^"]*)" on air$`, func(m string) error {
				s.found = append(s.found, detect.Found{Name: "port:8788", BaseURL: "http://127.0.0.1:8788/v1",
					Chat: "http://127.0.0.1:8788/v1/chat/completions", Models: []string{m, "codex"}})
				return nil
			})
			sc.Step(`^roger serves "([^"]*)" on this machine$`, func(m string) error {
				base := "http://127.0.0.1:18080/v1"
				s.served = append(s.served, base)
				s.found = append(s.found, detect.Found{Name: "roger", BaseURL: base, Chat: base + "/chat/completions", Models: []string{m}})
				return nil
			})
			sc.Step(`^the setup wizard saved "([^"]*)" as the model to share$`, func(m string) error {
				s.saved = m
				return nil
			})
			sc.Step(`^the operator runs "roger share" at a terminal and answers "([^"]*)"$`, func(a string) error {
				return s.run(nil, true, a)
			})
			sc.Step(`^the operator runs "roger share" with no terminal$`, func() error {
				return s.run(nil, false, "")
			})
			sc.Step(`^the operator runs "roger share ([^"]*)" with no terminal$`, func(rest string) error {
				return s.run(strings.Fields(rest), false, "")
			})
			sc.Step(`^roger showed "([^"]*)" and the server it comes from before asking$`, func(m string) error {
				i, j := strings.Index(s.out, m), strings.Index(s.out, "[y/N]")
				if i < 0 || j < 0 || i > j || !strings.Contains(s.out, "127.0.0.1:8081") {
					return fmt.Errorf("the model and its server are not shown before the question:\n%s", s.out)
				}
				return nil
			})
			sc.Step(`^it asked "([^"]*)"$`, func(q string) error {
				if !strings.Contains(s.out, q) {
					return fmt.Errorf("output lacks %q:\n%s", q, s.out)
				}
				return nil
			})
			sc.Step(`^"([^"]*)" went on air$`, func(m string) error {
				if len(s.started) != 1 || s.started[0] != m {
					return fmt.Errorf("went on air: %v, want %q (err %v)\n%s", s.started, m, s.err, s.out)
				}
				return nil
			})
			sc.Step(`^nothing went on air$`, func() error {
				if len(s.started) != 0 {
					return fmt.Errorf("went on air: %v\n%s", s.started, s.out)
				}
				return nil
			})
			sc.Step(`^roger said "([^"]*)"$`, func(line string) error {
				all := s.out
				if s.err != nil {
					all += s.err.Error()
				}
				if !strings.Contains(all, line) {
					return fmt.Errorf("roger did not say %q:\n%s", line, all)
				}
				return nil
			})
			sc.Step(`^the server it found was not saved as the one to share$`, func() error {
				if c := loadConfig(); c.Share != nil && c.Share.Upstream != "" {
					return fmt.Errorf("the declined server was saved as %q", c.Share.Upstream)
				}
				return nil
			})
			sc.Step(`^roger did not ask anything$`, func() error {
				if strings.Contains(s.out, "[y/N]") {
					return fmt.Errorf("roger asked:\n%s", s.out)
				}
				return nil
			})
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true, Paths: []string{marketConsentFeature}},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("features/sharing/market_offer_consent.feature failed")
	}
}
