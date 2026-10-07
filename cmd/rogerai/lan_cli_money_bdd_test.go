package main

// Executable spec: features/lan/money_and_accounts.feature (@cli, the Edge receipt) and
// features/lan/ui_visibility.feature (@cli: LAN mode named once, and machine output kept clean).
// The REAL CLI runs in-process; a recording broker proves what the network was asked, and this
// machine's own serve is a real share record the production self rung reads.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"rogerai.fm/roger/v6/internal/bddrun"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/onair"
	"rogerai.fm/roger/v6/internal/protocol"
)

type lanCLIMoneyBDD struct {
	lanUseBDD
	note     bytes.Buffer
	shareRan bool
}

// serveHere makes this machine serve band through the production self rung: a share record for
// this (live) process, read by lanSelfServe and the LAN dial alike.
func (s *lanCLIMoneyBDD) serveHere(band string) error {
	lanSelfServe = prodLANSelfServe
	up := s.counting("self", &s.selfHits)
	return writeShareRecord(onair.Share{PID: os.Getpid(), Band: band, ChatURL: up.URL + "/v1/chat/completions"})
}

func (s *lanCLIMoneyBDD) loggedIn() error {
	c := loadConfig()
	c.User = "u_gh_7"
	return saveConfig(c)
}

// runLine runs a roger command line, the note captured apart from stdout.
func (s *lanCLIMoneyBDD) runLine(line string) error {
	s.note.Reset()
	lanNoteOut = &s.note
	if strings.Contains(line, " share") {
		s.shareRan = true
		prevCtx := lanShareContext
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		lanShareContext = func() (context.Context, context.CancelFunc) { return ctx, func() {} }
		defer func() { lanShareContext = prevCtx }()
	}
	return s.runUse(line)
}

// announcing counts the lines, stdout and the note together, that name the mode.
func (s *lanCLIMoneyBDD) announcing() int {
	n := 0
	for _, ln := range strings.Split(s.out+"\n"+s.note.String(), "\n") {
		ln = strings.TrimSpace(ln)
		if strings.HasPrefix(ln, "LAN mode") || strings.HasPrefix(ln, "mode: LAN") {
			n++
		}
	}
	return n
}

func (s *lanCLIMoneyBDD) plateLacks(words ...string) error {
	low := strings.ToLower(s.out)
	for _, w := range words {
		if strings.Contains(low, strings.ToLower(w)) {
			return fmt.Errorf("the plate contains %q:\n%s", w, s.out)
		}
	}
	return nil
}

func TestLANCLIMoneyBDD(t *testing.T) {
	s := &lanCLIMoneyBDD{lanUseBDD: lanUseBDD{lanRouteBDD: lanRouteBDD{lanDialBDD: lanDialBDD{t: t}}}}
	var paths []string
	for _, l := range []int{27, 66, 79, 86, 93} {
		paths = append(paths, fmt.Sprintf("../../features/lan/money_and_accounts.feature:%d", l))
	}
	for _, l := range []int{79, 91} {
		paths = append(paths, fmt.Sprintf("../../features/lan/ui_visibility.feature:%d", l))
	}
	var turns atomic.Int32
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.resetUse()
				s.shareRan = false
				turns.Store(0)
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				lanNoteOut = os.Stderr
				s.restoreRoute()
				return ctx, nil
			})
			sc.Step(`^LAN mode is on$`, func() error { return nil })
			sc.Step(`^the public broker is reachable from this machine$`, func() error { return nil })
			sc.Step(`^the LAN Tower at ([0-9.:]+) serves "([^"]*)"$`, s.towerServing)
			sc.Step(`^they run "([^"]*)"$`, s.runLine)
			sc.Step(`^no request is made to /balance$`, func() error { return s.brokerNotAsked("/balance") })
			sc.Step(`^the plate does not contain "est\.", "balance" or "hold"$`, func() error {
				if s.err != nil || !s.opened {
					return fmt.Errorf("no proxy opened: %v\n%s", s.err, s.out)
				}
				return s.plateLacks("est.", "balance", "hold")
			})
			sc.Step(`^the plate reads "free" and "local"$`, func() error {
				low := strings.ToLower(s.out)
				if !strings.Contains(low, "free") || !strings.Contains(low, "local") {
					return fmt.Errorf("the plate does not read free and local:\n%s", s.out)
				}
				return nil
			})
			// the Edge receipt
			sc.Step(`^the Edge member "([^"]*)" serves "([^"]*)"$`, func(node, band string) error { return s.face(node, band, 0) })
			sc.Step(`^a turn is served by "([^"]*)"$`, func(node string) error {
				if err := s.dispatch("gpt-oss-20b"); err != nil {
					return err
				}
				return s.servedBy(node)
			})
			sc.Step(`^no hold was placed and no ledger row was written$`, func() error {
				if n := s.broker.count(); n != 0 {
					return fmt.Errorf("the broker was asked %d thing(s): %v", n, s.broker.paths)
				}
				return nil
			})
			sc.Step(`^the session carries a local receipt signed by "([^"]*)"$`, func(node string) error {
				f := s.faces[node]
				for _, r := range s.receipts {
					if r.Local && r.NodeID == node && r.VerifyNode(fmt.Sprintf("%x", []byte(f.pub))) {
						return nil
					}
				}
				return fmt.Errorf("no local receipt signed by %s: %+v", node, s.receipts)
			})
			// no login
			sc.Step(`^this machine has never logged in$`, func() error {
				// an account wallet comes only from a sign-in; a fresh machine holds a device id
				for _, acct := range []string{"u_gh_", "u_apple_", "u_email_"} {
					if strings.HasPrefix(loadConfig().User, acct) {
						return fmt.Errorf("the scenario's machine is logged in as %s", loadConfig().User)
					}
				}
				return nil
			})
			sc.Step(`^they run "([^"]*)" and this machine serves "([^"]*)"$`, func(line, band string) error {
				if err := s.serveHere(band); err != nil {
					return err
				}
				return s.runLine(line)
			})
			sc.Step(`^the proxy opens and the turn is served$`, func() error {
				if err := s.turn("qwen3-8b"); err != nil {
					return err
				}
				if s.code != 200 || s.selfHits.Load() != 1 {
					return fmt.Errorf("the turn was not served here: %d %s", s.code, s.body)
				}
				return nil
			})
			sc.Step(`^no login prompt is shown$`, func() error {
				if strings.Contains(strings.ToLower(s.out), "login") {
					return fmt.Errorf("a login prompt was shown:\n%s", s.out)
				}
				return nil
			})
			// a logged-in user's account
			sc.Step(`^this machine is logged in with a funded account$`, s.loggedIn)
			sc.Step(`^this machine is logged in$`, s.loggedIn)
			sc.Step(`^they run "([^"]*)" and send (\d+) turns$`, func(line string, n int) error {
				if err := s.serveHere("qwen3-8b"); err != nil {
					return err
				}
				if err := s.runLine(line); err != nil {
					return err
				}
				for i := 0; i < n; i++ {
					if err := s.turn("qwen3-8b"); err != nil {
						return err
					}
					if s.code != 200 {
						return fmt.Errorf("turn %d: %d %s", i+1, s.code, s.body)
					}
				}
				return nil
			})
			sc.Step(`^the account's balance is unchanged$`, func() error {
				return s.brokerNotAsked("/balance", "/v1/chat/completions", "/discover")
			})
			sc.Step(`^no hold was placed$`, func() error {
				if n := s.broker.count(); n != 0 {
					return fmt.Errorf("the broker was asked %d thing(s): %v", n, s.broker.paths)
				}
				return nil
			})
			sc.Step(`^it shows the network balance as usual$`, func() error {
				if s.err != nil || !strings.Contains(s.out, "$12.5000") || !s.broker.saw("/balance") {
					return fmt.Errorf("the balance was not shown from the network: %v\n%s", s.err, s.out)
				}
				return nil
			})
			sc.Step(`^it adds one line saying turns in LAN mode are free and do not touch this balance$`, func() error {
				if c := strings.Count(s.out, "turns in LAN mode are free and do not touch this balance"); c != 1 {
					return fmt.Errorf("the LAN line appears %d time(s):\n%s", c, s.out)
				}
				return nil
			})
			// the mode named once, and machine output kept clean
			sc.Step(`^they run "([^"]*)" in a terminal$`, func(line string) error {
				if err := s.serveHere("qwen3-8b"); err != nil {
					return err
				}
				if strings.Contains(line, " share") {
					c := loadConfig()
					c.Share = &Share{Upstream: s.counting("self", &s.selfHits).URL + "/v1", Model: "qwen3-8b"}
					if err := saveConfig(c); err != nil {
						return err
					}
				}
				return s.runLine(line)
			})
			sc.Step(`^the output names LAN mode once$`, func() error {
				if n := s.announcing(); n != 1 {
					return fmt.Errorf("LAN mode is named %d times:\nstdout: %s\nnote: %s\nerr: %v", n, s.out, s.note.String(), s.err)
				}
				return nil
			})
			sc.Step(`^stdout is valid JSON$`, func() error {
				var v any
				if err := json.Unmarshal([]byte(s.out), &v); err != nil {
					return fmt.Errorf("stdout is not JSON (%v):\n%s", err, s.out)
				}
				return nil
			})
			sc.Step(`^the LAN note, if any, is on stderr$`, func() error {
				if strings.Contains(s.out, "LAN mode") {
					return fmt.Errorf("the LAN note is on stdout:\n%s", s.out)
				}
				return nil
			})
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true, Paths: paths},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("money_and_accounts / ui_visibility CLI scenarios failed")
	}
}

// keep the imports honest if a step above is trimmed
var _ = edge.Serve
var _ protocol.UsageReceipt
