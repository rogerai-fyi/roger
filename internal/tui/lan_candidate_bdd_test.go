package tui

// Executable spec: the @tui scenarios of features/lan/discovery_and_dial.feature (adding a
// candidate Tower), adversarial.feature (a flood of Tower advertisements) and
// trust_and_honesty.feature (the add confirm's trust note; verified and unverified sources).
// The real model renders and handles keys; the LAN dial is the one shared seam the TUI reads.

import (
	"context"
	"fmt"
	"rogerai.fm/roger/v7/internal/bddrun"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/client"
)

type lanCandBDD struct {
	t      *testing.T
	m      model
	out    string
	bands  []client.LANBand
	status []client.LANTowerStatus
	added  []string
}

func (s *lanCandBDD) reset() {
	s.bands, s.status, s.added, s.out = nil, nil, nil, ""
	SetLANDial(func() client.LANDial { return client.LANDial{Bands: s.bands, Towers: s.status} })
}

func (s *lanCandBDD) seed() {
	var tm tea.Model = NewWithHooks("http://broker.invalid", "tester", nil, Hooks{
		LANAddTower: func(addr string) (string, error) {
			s.added = append(s.added, addr)
			s.status = nil
			s.bands = append(s.bands, client.LANBand{Model: "garage-band",
				Sources: []client.LANSource{{Kind: client.LANTower, ID: addr, Addr: addr}}})
			return "added " + addr, nil
		},
	})
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	tm, _ = tm.Update(fetchOffers("http://broker.invalid")())
	s.m = tm.(model)
	s.out = stripANSI(s.m.View())
}

func (s *lanCandBDD) key(k string) {
	var tm tea.Model = s.m
	var cmd tea.Cmd
	tm, cmd = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
	for i := 0; cmd != nil && i < 4; i++ { // run what the key started (the add, the refresh)
		msg := cmd()
		if msg == nil {
			break
		}
		tm, cmd = tm.Update(msg)
	}
	s.m = tm.(model)
	s.out = stripANSI(s.m.View())
}

func (s *lanCandBDD) candidate(addr string) {
	s.status = append(s.status, client.LANTowerStatus{Addr: addr, State: client.LANTowerCandidate,
		Detail: `advertised as "garage"; ask to join with roger join ` + addr})
}

func TestLANCandidateTUIBDD(t *testing.T) {
	s := &lanCandBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) { s.reset(); return ctx, nil })
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				SetLANDial(nil)
				return ctx, nil
			})
			sc.Step(`^LAN mode is on$`, func() error { return nil })
			sc.Step(`^the dial shows the candidate Tower at ([0-9.:]+)$`, func(addr string) error {
				s.candidate(addr)
				s.seed()
				if !strings.Contains(s.out, addr) {
					return fmt.Errorf("the dial does not show the candidate %s:\n%s", addr, s.out)
				}
				return nil
			})
			sc.Step(`^the owner selects it and presses the add key$`, func() error { s.key("+"); return nil })
			sc.Step(`^the TUI asks to confirm, showing the address and the trust note$`, func() error {
				for _, w := range []string{"192.168.1.50:8787", "plain http", "any device on this network"} {
					if !strings.Contains(s.out, w) {
						return fmt.Errorf("the confirm lacks %q:\n%s", w, s.out)
					}
				}
				if len(s.added) != 0 {
					return fmt.Errorf("the Tower was added before the owner confirmed")
				}
				return nil
			})
			sc.Step(`^the owner confirms$`, func() error { s.key("y"); return nil })
			sc.Step(`^lan_towers contains "([^"]*)"$`, func(addr string) error {
				if len(s.added) != 1 || s.added[0] != addr {
					return fmt.Errorf("added %v, want [%s]", s.added, addr)
				}
				return nil
			})
			sc.Step(`^its bands appear on the dial$`, func() error {
				if !strings.Contains(s.out, "garage-band") {
					return fmt.Errorf("the added Tower's band is not on the dial:\n%s", s.out)
				}
				return nil
			})
			sc.Step(`^10000 distinct mDNS Tower advertisements arrive within a minute$`, func() error {
				for i := 0; i < 8; i++ {
					s.candidate(fmt.Sprintf("192.168.1.%d:8787", 100+i))
				}
				s.status = append(s.status, client.LANTowerStatus{State: client.LANTowerCandidate,
					Detail: "9992 more Tower advertisement(s) heard and not shown"})
				return nil
			})
			sc.Step(`^the dial renders$`, func() error { s.seed(); return nil })
			sc.Step(`^at most the candidate cap is shown$`, func() error {
				if n := strings.Count(s.out, "candidate Tower"); n == 0 || n > 8 {
					return fmt.Errorf("%d candidate lines shown:\n%s", n, s.out)
				}
				return nil
			})
			sc.Step(`^the line notes how many more were heard$`, func() error {
				if !strings.Contains(s.out, "9992 more") {
					return fmt.Errorf("no line notes how many more were heard:\n%s", s.out)
				}
				return nil
			})
			sc.Step(`^memory held for candidates is bounded$`, func() error {
				n := 0
				for _, c := range s.m.lanCandidates() {
					if c.Addr != "" { // the flood summary is one counted line, not a candidate
						n++
					}
				}
				if n > 8 {
					return fmt.Errorf("the TUI holds %d candidates", n)
				}
				return nil
			})
			sc.Step(`^the owner adds a candidate Tower from the dial$`, func() error {
				s.candidate("192.168.1.50:8787")
				s.seed()
				s.key("+")
				return nil
			})
			sc.Step(`^the confirm states the plain-http exposure in one sentence$`, func() error {
				if !strings.Contains(s.out, "plain http") || !strings.Contains(s.out, "can read what you send") {
					return fmt.Errorf("the confirm does not state the exposure:\n%s", s.out)
				}
				return nil
			})
			sc.Step(`^the dial lists a band from the LAN Tower at ([0-9.:]+) and one from the Edge member "([^"]*)"$`, func(addr, node string) error {
				s.bands = []client.LANBand{
					{Model: "tower-band", Sources: []client.LANSource{{Kind: client.LANTower, ID: addr, Addr: addr}}},
					{Model: "edge-band", Sources: []client.LANSource{{Kind: client.LANEdge, ID: node, Verified: true}}},
				}
				return nil
			})
			sc.Step(`^the Tower row's source carries "([^"]*)"$`, func(w string) error { return s.rowHas("tower-band", w) })
			sc.Step(`^the Edge row's source carries "([^"]*)"$`, func(w string) error {
				if err := s.rowHas("edge-band", w); err != nil {
					return err
				}
				return s.rowLacks("edge-band", "unverified")
			})
			sc.Step(`^the difference is readable with NO_COLOR$`, func() error {
				t.Setenv("NO_COLOR", "1")
				s.seed()
				if err := s.rowHas("tower-band", "unverified"); err != nil {
					return err
				}
				return s.rowLacks("edge-band", "unverified")
			})
		},
		Options: &godog.Options{Format: "pretty", Strict: true, TestingT: t, Paths: []string{
			"../../features/lan/discovery_and_dial.feature:191",
			"../../features/lan/adversarial.feature:62",
			"../../features/lan/trust_and_honesty.feature:44",
			"../../features/lan/trust_and_honesty.feature:49",
		}},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("LAN candidate TUI scenarios failed")
	}
}

func (s *lanCandBDD) row(model string) string {
	for _, ln := range strings.Split(s.out, "\n") {
		if strings.Contains(ln, model) {
			return ln
		}
	}
	return ""
}

func (s *lanCandBDD) rowHas(model, w string) error {
	if r := s.row(model); !strings.Contains(r, w) {
		return fmt.Errorf("the %s row %q does not carry %q:\n%s", model, r, w, s.out)
	}
	return nil
}

func (s *lanCandBDD) rowLacks(model, w string) error {
	if r := s.row(model); strings.Contains(r, w) {
		return fmt.Errorf("the %s row %q carries %q", model, r, w)
	}
	return nil
}
