package main

// Executable spec: features/lan/trust_and_honesty.feature (@cli, @client). The real `roger use` (its
// proxy opened in-process), a REAL standalone Tower, and the request the Tower actually received.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/protocol"
)

func TestLANTrustBDD(t *testing.T) {
	s := &lanUseBDD{lanRouteBDD: lanRouteBDD{lanDialBDD: lanDialBDD{t: t}}}
	var paths []string
	for _, l := range []int{29, 38, 67, 94} {
		paths = append(paths, fmt.Sprintf("../../features/lan/trust_and_honesty.feature:%d", l))
	}
	paths = append(paths, "../../features/lan/regressions.feature:99")
	var nonceAddr string // the Tower address the nonce regression sends its turn to
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.resetUse()
				t.Setenv("ROGER_LAN", "1")
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				s.restoreRoute()
				return ctx, nil
			})
			sc.Step(`^LAN mode is on$`, func() error { return nil })
			sc.Step(`^lan_towers contains "([^"]*)"$`, func(addr string) error { return s.towerServing(addr, "llama-3.3-70b") })
			sc.Step(`^this machine has never been told the LAN trust note$`, func() error {
				if loadConfig().LanTrustNoted {
					return fmt.Errorf("the config already records the note")
				}
				return nil
			})
			sc.Step(`^they run "roger --lan use ([^"]*)" and the Tower serves it$`, func(band string) error {
				if err := s.runUse("roger --lan use " + band + " --yes"); err != nil {
					return err
				}
				if err := s.turn(band); err != nil {
					return err
				}
				return s.servedBy("tower")
			})
			sc.Step(`^the output states that a Tower on plain http can be impersonated by any device on this network and can read what is sent to it$`, func() error {
				for _, w := range []string{"plain http", "any device on this network", "can read"} {
					if !strings.Contains(s.out, w) {
						return fmt.Errorf("the output lacks %q:\n%s", w, s.out)
					}
				}
				return nil
			})
			sc.Step(`^it says Edge members are verified by certificate and are not affected$`, func() error {
				if !strings.Contains(s.out, "Edge members are verified by certificate") {
					return fmt.Errorf("the output does not say Edge members are verified:\n%s", s.out)
				}
				return nil
			})
			sc.Step(`^the config records that the note was shown$`, func() error {
				if !loadConfig().LanTrustNoted {
					return fmt.Errorf("the config does not record the note")
				}
				return nil
			})
			sc.Step(`^the config records that the LAN trust note was shown$`, func() error {
				c := loadConfig()
				c.LanTrustNoted = true
				if err := saveConfig(c); err != nil {
					return err
				}
				return s.towerServing("192.168.1.20:8787", "llama-3.3-70b")
			})
			sc.Step(`^they run "roger --lan use ([^"]*)"$`, func(band string) error { return s.runUse("roger --lan use " + band + " --yes") })
			sc.Step(`^the trust note is not printed$`, func() error {
				if strings.Contains(s.out, "plain http") {
					return fmt.Errorf("the note was printed again:\n%s", s.out)
				}
				return nil
			})
			sc.Step(`^a turn is sent to the LAN Tower at ([0-9.:]+)$`, func(addr string) error {
				if err := s.towerServing(addr, "llama-3.3-70b"); err != nil {
					return err
				}
				return s.dispatch("llama-3.3-70b")
			})
			sc.Step(`^the request carries this machine's signature$`, func() error {
				tw := s.tower("192.168.1.20:8787")
				tw.mu.Lock()
				defer tw.mu.Unlock()
				if tw.lastSig == "" || tw.lastHeader.Get(protocol.HeaderPubkey) == "" {
					return fmt.Errorf("the Tower saw no signature: %v", tw.lastHeader)
				}
				return nil
			})
			sc.Step(`^it carries a fresh X-Roger-Nonce$`, func() error {
				tw := s.tower("192.168.1.20:8787")
				tw.mu.Lock()
				defer tw.mu.Unlock()
				if tw.lastHeader.Get(protocol.HeaderNonce) == "" {
					return fmt.Errorf("the Tower saw no nonce: %v", tw.lastHeader)
				}
				return nil
			})
			// regressions.feature:99 (2026-10-07): every LAN Tower address gets the replay nonce.
			sc.Step(`^a LAN Tower at (\S+) serving "([^"]*)", added with roger lan add$`, func(addr, model string) error {
				nonceAddr = addr
				if err := s.towerRuns(addr, model); err != nil {
					return err
				}
				if _, err := captureEdgeStdout(func() error { return cmdLAN(loadConfig(), []string{"add", addr}) }); err != nil {
					return fmt.Errorf("roger lan add %s refused it: %v", addr, err)
				}
				return nil
			})
			sc.Step(`^a turn is sent to that Tower$`, func() error { return s.dispatch("llama-3.3-70b") })
			sc.Step(`^the request the Tower received carries a fresh X-Roger-Nonce$`, func() error {
				tw := s.tower(nonceAddr)
				tw.mu.Lock()
				defer tw.mu.Unlock()
				if tw.chatRequests == 0 {
					return fmt.Errorf("no turn reached the Tower at %s", nonceAddr)
				}
				if tw.lastHeader.Get(protocol.HeaderNonce) == "" {
					return fmt.Errorf("the Tower at %s saw no nonce: %v", nonceAddr, tw.lastHeader)
				}
				return nil
			})
			sc.Step(`^a captured copy replayed to that Tower is refused by the Tower$`, func() error {
				return s.replayRefused(s.tower(nonceAddr))
			})
			sc.Step(`^a captured copy replayed to the Tower is refused by the Tower$`, func() error {
				return s.replayRefused(s.tower("192.168.1.20:8787"))
			})
			sc.Step(`^nothing local serves "([^"]*)"$`, func(string) error { return nil })
			sc.Step(`^a turn is dispatched for "([^"]*)"$`, s.dispatch)
			sc.Step(`^the refusal says LAN mode is on and the market was not tried$`, func() error {
				return s.refusedNaming("LAN mode is on", "the market was not tried")
			})
			sc.Step(`^nothing in any surface shows the turn as served$`, func() error {
				if len(s.receipts) != 0 {
					return fmt.Errorf("a receipt records the refused turn: %+v", s.receipts)
				}
				if s.hdr.Get("X-RogerAI-Cost") != "" && s.hdr.Get("X-RogerAI-Cost") != "0" {
					return fmt.Errorf("the refusal carries a cost: %s", s.hdr.Get("X-RogerAI-Cost"))
				}
				return s.noMarketInference()
			})
		},
		Options: &godog.Options{Format: "pretty", Strict: true, TestingT: t, Paths: paths},
	}
	if suite.Run() != 0 {
		t.Fatal("trust scenarios failed")
	}
}

var _ = client.LANTower

// replayRefused resends the Tower's last completion request, headers and body exactly, and
// requires the Tower to refuse it.
func (s *lanUseBDD) replayRefused(tw *realTower) error {
	tw.mu.Lock()
	hdr, body := tw.lastHeader.Clone(), append([]byte(nil), tw.lastBody...)
	tw.mu.Unlock()
	req, _ := http.NewRequest(http.MethodPost, tw.srv.URL+"/v1/chat/completions", bytes.NewReader(body))
	req.Header = hdr
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK {
		return fmt.Errorf("the replay was served: %s", b)
	}
	return nil
}
