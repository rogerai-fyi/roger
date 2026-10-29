package tui

// Executable spec: features/lan/edge_only.feature (@tui). The REAL model renders with the shared
// LAN dial set and edge-only marked, exactly as `roger --lan --edge-only` sets them.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/cucumber/godog"
	"github.com/muesli/termenv"

	"rogerai.fm/roger/v7/internal/bddrun"
)

func TestLANEdgeOnlyTUIBDD(t *testing.T) {
	s := &lanUIBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				s.reset()
				SetLANEdgeOnly(true)
				return ctx, nil
			})
			sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
				SetLANDial(nil)
				SetLANEdgeOnly(false)
				return ctx, nil
			})
			sc.Step(`^edge-only is on$`, func() error { return nil })
			sc.Step(`^the TUI opens$`, func() error {
				s.seed(120, "http://broker.invalid")
				s.out = s.m.View()
				return nil
			})
			sc.Step(`^its header carries "([^"]*)" in place of the balance tag$`, func(want string) error {
				if err := s.headerHas(strings.ReplaceAll(want, "-", `\-`)); err != nil {
					return err
				}
				if head := strings.SplitN(stripANSI(s.out), "\n", 4); strings.Contains(strings.Join(head[:3], "\n"), "42.17") {
					return fmt.Errorf("the header still carries the balance tag:\n%s", strings.Join(head[:3], "\n"))
				}
				// the windowshade carries it too
				s.m.compact = true
				if c := stripANSI(s.m.View()); !strings.Contains(c, want) {
					return fmt.Errorf("the windowshade lacks %q:\n%s", want, c)
				}
				return nil
			})
			sc.Step(`^the badge is drawn in the same ink as the LAN badge, never the brand red$`, func() error {
				prev := lipgloss.ColorProfile()
				lipgloss.SetColorProfile(termenv.ANSI256)
				defer lipgloss.SetColorProfile(prev)
				badge := lanBadge(true)
				red := strings.TrimSuffix(stRed.Render("x"), "x\x1b[0m")
				if red == "" || strings.Contains(badge, red) {
					return fmt.Errorf("the badge carries the red style: %q", badge)
				}
				if want := stKey.Render("LAN EDGE-ONLY"); badge != want {
					return fmt.Errorf("the badge %q is not in the LAN badge's ink %q", badge, want)
				}
				return nil
			})
			sc.Step(`^it reads correctly with NO_COLOR$`, func() error {
				s.t.Setenv("NO_COLOR", "1")
				s.seed(100, "http://broker.invalid")
				s.out = s.m.View()
				return s.headerHas(`LAN EDGE\-ONLY`)
			})
			sc.Step(`^the dial is shown$`, func() error {
				s.seed(120, "http://broker.invalid")
				s.out = stripANSI(s.m.View())
				return nil
			})
			sc.Step(`^its title reads "([^"]*)"$`, func(want string) error {
				if !strings.Contains(s.out, want) {
					return fmt.Errorf("the dial title lacks %q:\n%s", want, s.out)
				}
				return nil
			})
		},
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true, Paths: []string{
			"../../features/lan/edge_only.feature:203",
			"../../features/lan/edge_only.feature:211",
		}},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("edge_only.feature (TUI) failed")
	}
}
