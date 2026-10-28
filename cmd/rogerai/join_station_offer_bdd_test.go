package main

// join_station_offer_bdd_test.go runs features/join/invite.feature section 6 (founder ruling
// 2026-10-11: a station offers only roger-served models by default) against the same real TLS
// Tower the invite runner uses. Detection is faked as two local servers: one roger serves
// (rogerServedBase) and one it does not (otherBase). What counts as roger-served is the
// rogerServedBases seam, read from where roger's own runtimes listen.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/bddrun"
	"rogerai.fm/roger/v7/internal/detect"
	"rogerai.fm/roger/v7/internal/lanmode"
	"rogerai.fm/roger/v7/internal/towerpin"
)

const (
	rogerServedBase = "http://127.0.0.1:2/v1"
	otherBase       = "http://127.0.0.1:1/v1"
)

type stationOfferBDD struct {
	*joinInviteBDD
	served, other []string // models on air at rogerServedBase and otherBase
	proxy         []string // models of a local proxy to hosted accounts (at otherBase)
	answer        *string  // the pick typed at the terminal; nil: no terminal
	explicit      string   // --models
	liveBefore    int
}

func (s *stationOfferBDD) found() []detect.Found {
	var f []detect.Found
	if len(s.served) > 0 {
		f = append(f, detect.Found{Name: "roger", BaseURL: rogerServedBase, Chat: rogerServedBase + "/chat/completions", Models: s.served})
	}
	if len(s.other) > 0 {
		f = append(f, detect.Found{Name: "port:8081", BaseURL: otherBase, Chat: otherBase + "/chat/completions", Models: s.other})
	}
	if len(s.proxy) > 0 {
		f = append(f, detect.Found{Name: "port:8788", BaseURL: "http://127.0.0.1:3/v1", Chat: "http://127.0.0.1:3/v1/chat/completions", Models: s.proxy})
	}
	return f
}

func (s *stationOfferBDD) joinStation() error {
	if err := s.mint("--station"); err != nil {
		return err
	}
	s.liveBefore = len(s.st.LiveInvitations())
	prevD, prevI, prevIn, prevCtx := detectFull, stationInteractive, stationStdin, lanShareContext
	detectFull = func(...string) ([]detect.Found, []string) { return s.found(), nil }
	stationInteractive = func() bool { return s.answer != nil }
	if s.answer != nil {
		stationStdin = strings.NewReader(*s.answer + "\n")
	}
	lanShareContext = func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), 1500*time.Millisecond)
	}
	defer func() { detectFull, stationInteractive, stationStdin, lanShareContext = prevD, prevI, prevIn, prevCtx }()
	args := []string{s.inv.String(), "kitchen-pi"}
	if s.explicit != "" {
		args = append(args, "--models", s.explicit)
	}
	s.out = captureStdout(s.t, func() { s.err = cmdJoin(loadConfig(), args) })
	return nil
}

func (s *stationOfferBDD) attached() []string {
	sts, _ := s.st.Stations()
	if len(sts) != 1 {
		return nil
	}
	return sts[0].Models
}

func TestJoinStationOfferBDD(t *testing.T) {
	s := &stationOfferBDD{joinInviteBDD: &joinInviteBDD{t: t}}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.reset()
				s.served, s.other, s.proxy, s.answer, s.explicit = nil, nil, nil, nil, ""
				prev := rogerServedBases
				rogerServedBases = func() []string { return []string{rogerServedBase} }
				t.Cleanup(func() { rogerServedBases = prev })
				return ctx, nil
			})
			// Background
			sc.Step(`^a standalone Tower "lab" is serving at 192\.168\.1\.20:8787 over TLS with its own key$`, func() error {
				resp, err := towerpin.Client(mustFP(s.st), 0).Get("https://" + s.addr + "/discover")
				if err != nil {
					return err
				}
				resp.Body.Close()
				if resp.StatusCode != http.StatusUnauthorized {
					return fmt.Errorf("/discover answered %d", resp.StatusCode)
				}
				return nil
			})
			sc.Step(`^the Tower's identity fingerprint is "sha256:9f3c\.\.\.e1"$`, func() error { return nil })
			sc.Step(`^the owner is signed in to nothing \(no Roger Core account is needed\)$`, func() error { return nil })

			sc.Step(`^this machine serves "([^"]*)" with "roger model serve"$`, func(m string) error {
				s.served = append(s.served, m)
				return nil
			})
			sc.Step(`^roger serves nothing on this machine$`, func() error { s.served = nil; return nil })
			sc.Step(`^another local server on this machine has "([^"]*)" on air$`, func(m string) error {
				s.other = append(s.other, m)
				return nil
			})
			sc.Step(`^a local proxy to hosted accounts on this machine has "([^"]*)" and "([^"]*)" on air$`, func(a, b string) error {
				s.proxy = []string{a, b}
				return nil
			})
			sc.Step(`^the station joins with a station invite, not at a terminal, without --models$`, func() error {
				return s.joinStation()
			})
			sc.Step(`^the station joins with a station invite at a terminal and answers "([^"]*)"$`, func(a string) error {
				s.answer = &a
				return s.joinStation()
			})
			sc.Step(`^the station joins with a station invite and --models "([^"]*)"$`, func(m string) error {
				s.explicit = m
				return s.joinStation()
			})
			sc.Step(`^the Tower attaches it serving only "([^"]*)"$`, func(m string) error {
				if s.err != nil {
					return fmt.Errorf("join: %v\n%s", s.err, s.out)
				}
				if got := strings.Join(s.attached(), ","); got != m {
					return fmt.Errorf("the Tower attached %q, want only %q\n%s", got, m, s.out)
				}
				return nil
			})
			sc.Step(`^the Tower attaches it serving "([^"]*)" and "([^"]*)"$`, func(a, b string) error {
				if s.err != nil {
					return fmt.Errorf("join: %v\n%s", s.err, s.out)
				}
				if got := strings.Join(s.attached(), ","); got != a+","+b {
					return fmt.Errorf("the Tower attached %q, want %q and %q\n%s", got, a, b, s.out)
				}
				return nil
			})
			sc.Step(`^the station prints "([^"]*)"$`, func(line string) error {
				if !strings.Contains(s.out, line) {
					return fmt.Errorf("output lacks %q:\n%s", line, s.out)
				}
				return nil
			})
			sc.Step(`^the station does not print "([^"]*)"$`, func(word string) error {
				if strings.Contains(s.out, word) {
					return fmt.Errorf("output names %q, want only a count:\n%s", word, s.out)
				}
				return nil
			})
			sc.Step(`^the station lists "([^"]*)" before asking, with nothing selected$`, func(line string) error {
				i, j := strings.Index(s.out, line), strings.Index(s.out, "add which?")
				if i < 0 || j < 0 || i > j {
					return fmt.Errorf("the list %q does not come before the question:\n%s", line, s.out)
				}
				if !strings.Contains(s.out, "none is offered unless you pick it") {
					return fmt.Errorf("the list does not say nothing is selected:\n%s", s.out)
				}
				return nil
			})
			sc.Step(`^the station lists "([^"]*)"$`, func(line string) error {
				if !strings.Contains(s.out, line) {
					return fmt.Errorf("output lacks %q:\n%s", line, s.out)
				}
				return nil
			})
			sc.Step(`^the join stops with "([^"]*)" before contacting it, and the invite is not spent$`, func(msg string) error {
				if s.err == nil || !strings.Contains(s.err.Error(), msg) {
					return fmt.Errorf("join error %v, want %q\n%s", s.err, msg, s.out)
				}
				if len(s.attached()) != 0 {
					return fmt.Errorf("the Tower attached a station: %v", s.attached())
				}
				if n := len(s.st.LiveInvitations()); n != s.liveBefore {
					return fmt.Errorf("the invite was spent (%d live, %d before)", n, s.liveBefore)
				}
				return nil
			})
			sc.Step(`^a station joined offering only "([^"]*)"$`, func(m string) error {
				s.served = []string{m}
				s.other = []string{"qwen3-8b"}
				if err := s.joinStation(); err != nil {
					return err
				}
				if s.err != nil {
					return fmt.Errorf("join: %v\n%s", s.err, s.out)
				}
				return nil
			})
			sc.Step(`^the server it shares from also lists "([^"]*)" and "([^"]*)"$`, func(a, b string) error {
				s.served = append(s.served, a, b) // the roger-served server now lists more
				s.other = nil
				return nil
			})
			sc.Step(`^the station's next poll reaches the Tower$`, func() error {
				// A real LAN share of this machine, auto-detecting the server it shares from, polling
				// the Tower: what the poll reports is what the Tower then routes to the station.
				prevD, prevCtx := detectFull, lanShareContext
				detectFull = func(...string) ([]detect.Found, []string) { return s.found(), nil }
				lanShareContext = func() (context.Context, context.CancelFunc) {
					return context.WithTimeout(context.Background(), 1500*time.Millisecond)
				}
				defer func() { detectFull, lanShareContext = prevD, prevCtx }()
				c := loadConfig()
				c.lan.LAN, c.lan.Source = true, lanmode.SrcFlag
				var err error
				s.lastShare = captureStdout(s.t, func() { err = cmdShare(c, nil) })
				if err != nil {
					return fmt.Errorf("share: %v\n%s", err, s.lastShare)
				}
				return nil
			})
			sc.Step(`^the poll reports only "([^"]*)"$`, func(m string) error {
				if got := strings.Join(s.attached(), ","); got != m {
					return fmt.Errorf("after the poll the Tower routes %q, want only %q\n%s", got, m, s.lastShare)
				}
				return nil
			})
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{joinInviteFeature + ":251", joinInviteFeature + ":259", joinInviteFeature + ":266", joinInviteFeature + ":272",
				joinInviteFeature + ":279", joinInviteFeature + ":286", joinInviteFeature + ":293"}},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("features/join/invite.feature section 6 failed")
	}
}
