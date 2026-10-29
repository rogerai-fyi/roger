package tui

// Executable spec: features/edge/scan_on_request.feature (@tui) - the TUI asks for a LAN scan
// exactly while the [3] EDGE screen shows. The real model's Update drives it, through real key
// presses; the host side (what an open request allows, and the sockets) is pinned in
// cmd/rogerai/edge_scan_on_request_bdd_test.go.

import (
	"context"
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/bddrun"
)

type scanTUIBDD struct {
	m             model
	opened, ended map[string]int
}

func (s *scanTUIBDD) reset() {
	s.opened, s.ended = map[string]int{}, map[string]int{}
	s.m = model{}
}

func (s *scanTUIBDD) running() error {
	m := NewWithHooks("http://127.0.0.1:1", "tester", nil, Hooks{
		EdgeScan: func(kind string) func() {
			s.opened[kind]++
			return func() { s.ended[kind]++ }
		},
	})
	m.width, m.height = 120, 40
	s.m = m
	return nil
}

func (s *scanTUIBDD) presses(k string) error {
	tm, _ := s.m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
	mm, ok := tm.(model)
	if !ok {
		return fmt.Errorf("Update returned %T", tm)
	}
	s.m = mm
	return nil
}

func (s *scanTUIBDD) askedOnce() error {
	if s.m.mode != modeEdge {
		return fmt.Errorf("the [3] EDGE screen is not showing (mode %v)", s.m.mode)
	}
	if s.opened["edge"] != 1 || s.ended["edge"] != 0 {
		return fmt.Errorf("edge scan opened %d time(s), ended %d time(s); want 1 and 0", s.opened["edge"], s.ended["edge"])
	}
	return nil
}

func (s *scanTUIBDD) requestEnded() error {
	if s.m.mode == modeEdge {
		return fmt.Errorf("still on the [3] EDGE screen")
	}
	if s.ended["edge"] != 1 {
		return fmt.Errorf("edge scan ended %d time(s), want 1", s.ended["edge"])
	}
	return nil
}

func TestScanOnRequestFeatureTUI(t *testing.T) {
	st := &scanTUIBDD{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(); return c, nil })
			sc.Given(`^a fresh install that has not joined an Edge and holds no Edge root$`, func() error { return nil })
			sc.Given(`^the TUI is running with a scan hook$`, st.running)
			sc.When(`^the owner presses (\d)$`, st.presses)
			sc.Then(`^the scan was asked for once$`, st.askedOnce)
			sc.Then(`^the scan request was ended$`, st.requestEnded)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@tui",
			Paths: []string{"../../features/edge/scan_on_request.feature"},
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("scan_on_request.feature (@tui) failed")
	}
}
