package tui

// Executable pin for the @tui scenario of features/edge/node_identity.feature: the node page offers
// the actuate ceremony while it is pending, and asks before it acts - exactly as forget does.

import (
	"rogerai.fm/roger/v7/internal/bddrun"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/edge"
)

type actuateTUI struct {
	t         *testing.T
	m         model
	confirmed *[]string
}

func (s *actuateTUI) pendingMember() error {
	s.m, s.confirmed = actuateModel(s.t, edge.Claimed)
	return nil
}

func (s *actuateTUI) pageOpened() error {
	s.m.edge.netDetail = true
	return nil
}

func (s *actuateTUI) saysItIsWaiting() error {
	out := stripANSI(s.m.edgeView(116))
	if !strings.Contains(out, "awaiting your confirmation") {
		return errfTUI("the page does not say actuate is waiting on the owner:\n%s", out)
	}
	return nil
}

func (s *actuateTUI) panelOffersIt() error {
	// ON THE PAGE the offer is the page's own line, because the button bar belongs to the map. The
	// key is the same one either way, which is what makes "press c" on the page honest.
	out := stripANSI(s.m.edgeView(116))
	for _, want := range []string{"press c", "physical world"} {
		if !strings.Contains(out, want) {
			return errfTUI("the page does not offer the ceremony (%q):\n%s", want, out)
		}
	}
	// And on the map, it is a button.
	back := s.m
	back.edge.netDetail = false
	panel := stripANSI(back.edgeView(116))
	for _, want := range []string{"Confirm actuate", "physical world"} {
		if !strings.Contains(panel, want) {
			return errfTUI("the panel does not offer the ceremony (%q):\n%s", want, panel)
		}
	}
	return nil
}

func (s *actuateTUI) asksFirstLikeForget() error {
	tm, _ := s.m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	s.m = asModel(tm)
	if s.m.edge.actuateID == "" {
		return errfTUI("pressing c did not arm the question")
	}
	if len(*s.confirmed) != 0 {
		return errfTUI("it confirmed without asking")
	}
	asked := stripANSI(s.m.status)
	for _, want := range []string{"ACTUATE?", "[y/N]"} {
		if !strings.Contains(asked, want) {
			return errfTUI("the question does not say %q: %q", want, asked)
		}
	}
	return nil
}

func errfTUI(f string, a ...any) error { return fmtErr(f, a...) }

func TestEdgeActuateCeremonyTUI(t *testing.T) {
	st := &actuateTUI{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Step(`^a member declaring "actuate", pending confirmation$`, st.pendingMember)
			sc.Step(`^its node page is opened$`, st.pageOpened)
			sc.Step(`^it says actuate is awaiting the owner's confirmation$`, st.saysItIsWaiting)
			sc.Step(`^the panel offers to confirm it, naming what it licenses$`, st.panelOffersIt)
			sc.Step(`^confirming asks first, exactly as forget does$`, st.asksFirstLikeForget)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@tui",
			Paths: []string{"../../features/edge/node_identity.feature"},
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("the actuate ceremony TUI scenario failed")
	}
}
