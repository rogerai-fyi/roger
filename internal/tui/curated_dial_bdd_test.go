package tui

// curated_dial_bdd_test.go - the godog harness for features/curated/curated_dial.feature:
// the badge, the filter, and the band card's price split, on the real dial.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cucumber/godog"
)

type curDialBDD struct {
	t *testing.T
	m model
}

func curDialSeed(w int) model {
	var m tea.Model = New("http://broker.local", "tester")
	m, _ = m.Update(tea.WindowSizeMsg{Width: w, Height: 40})
	m, _ = m.Update(offersMsg{
		// a human station and a curated station on ONE band, plus a curated-only band
		{NodeID: "human-node", Region: "home", Model: "gpt-oss-20b", Online: true, TPS: 40},
		{NodeID: "proxy-node", Region: "openrouter", Model: "gpt-oss-20b", Online: true, TPS: 90,
			Curated: true, CuratedProvider: "openrouter", PriceIn: 1.30, PriceOut: 2.60, UpstreamIn: 1.0, UpstreamOut: 2.0},
		{NodeID: "proxy-only", Region: "conifer", Model: "deepseek-v4", Online: true, TPS: 80,
			Curated: true, CuratedProvider: "conifer", PriceIn: 0.13, PriceOut: 0.26, UpstreamIn: 0.1, UpstreamOut: 0.2},
	})
	m, _ = m.Update(balanceMsg{balance: 42.17, loggedIn: true})
	m, _ = m.Update(tickMsg{})
	return m.(model)
}

func (s *curDialBDD) view() string { return stripANSI(s.m.View()) }

func (s *curDialBDD) curatedServing(model, provider string) error {
	s.m = curDialSeed(120)
	return nil
}

func (s *curDialBDD) browserLists() error {
	s.m.mode = modeBrowse
	return nil
}

func (s *curDialBDD) rowCarriesMark() error {
	v := s.view()
	if !strings.Contains(v, glyphCurated) {
		return fmt.Errorf("no curated mark on the dial:\n%s", v)
	}
	if !strings.Contains(v, "openrouter") && !strings.Contains(v, "conifer") {
		return fmt.Errorf("the provider name is not shown beside the mark:\n%s", v)
	}
	return nil
}

func (s *curDialBDD) visuallyDistinct() error {
	// The mark must be its OWN glyph, not a reuse of any existing badge.
	for _, taken := range []string{"◆", "⌁", "◪", "✓"} {
		if glyphCurated == taken {
			return fmt.Errorf("the curated mark reuses %q - the founder asked for a NEW badge", taken)
		}
	}
	return nil
}

func (s *curDialBDD) curatedPresent() error {
	s.m = curDialSeed(120)
	s.m.mode = modeBrowse
	return nil
}

func (s *curDialBDD) rowsAppearMarked() error { return s.rowCarriesMark() }

func (s *curDialBDD) filterLineCounts() error {
	v := s.view()
	if !strings.Contains(v, "curated") {
		return fmt.Errorf("the filter/count line does not say how many are curated:\n%s", v)
	}
	return nil
}

func (s *curDialBDD) toggleOff() error {
	if len(s.m.bands) == 0 { // a scenario with no Given starts from the standard dial
		if err := s.curatedPresent(); err != nil {
			return err
		}
	}
	out, _ := s.m.onKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'U'}})
	s.m = asModel(out)
	return nil
}

func (s *curDialBDD) curatedOnlyGone() error {
	v := s.view()
	if strings.Contains(v, "deepseek-v4") {
		return fmt.Errorf("a curated-only band survived the hide toggle:\n%s", v)
	}
	return nil
}

func (s *curDialBDD) mixedStaysHumanCount() error {
	v := s.view()
	if !strings.Contains(v, "gpt-oss-20b") {
		return fmt.Errorf("a band with a human station must stay on the dial:\n%s", v)
	}
	if strings.Contains(v, glyphCurated) {
		return fmt.Errorf("with curated hidden, the mixed band must count only its human "+
			"stations and drop the mark:\n%s", v)
	}
	return nil
}

func (s *curDialBDD) toggledOff() error {
	if err := s.curatedPresent(); err != nil {
		return err
	}
	return s.toggleOff()
}

func (s *curDialBDD) leaveAndReturn() error {
	s.m.mode = modeHelp
	s.m.mode = modeBrowse
	return nil
}

func (s *curDialBDD) stillHidden() error { return s.curatedOnlyGone() }

func (s *curDialBDD) filterStripSaysSo() error {
	if !strings.Contains(s.view(), "curated hidden") {
		return fmt.Errorf("an active hide must be visible on the filter strip:\n%s", s.view())
	}
	return nil
}

func (s *curDialBDD) hidCurated() error { return s.toggledOff() }

func (s *curDialBDD) agentAutoTunes() error { return nil } // asserted below via the pick itself

func (s *curDialBDD) neverBindsCuratedOnly() error {
	// The auto-tune picker must skip a curated-only band while the filter hides it.
	if pick := pickAutoBand(s.m.visibleBands(), true); pick != nil && pick.model == "deepseek-v4" {
		return fmt.Errorf("auto-tune bound a curated-only band the operator had hidden")
	}
	return nil
}

func (s *curDialBDD) bandCardShowsSplit() error {
	s.m = curDialSeed(120)
	nm, _ := s.m.openBandConfig("gpt-oss-20b", modeBrowse)
	s.m = asModel(nm)
	v := s.view()
	if !strings.Contains(v, "upstream") || !strings.Contains(v, "routing") {
		return fmt.Errorf("the band card must show the upstream list price and the routing "+
			"fee separately - the consumer sees what the 30%% buys:\n%s", v)
	}
	return nil
}

func TestCuratedDialFeature(t *testing.T) {
	st := &curDialBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) {
				*st = curDialBDD{t: t}
				return c, nil
			})
			sc.Step(`^a curated station serving "([^"]*)" via "([^"]*)"$`, st.curatedServing)
			sc.Step(`^the band browser lists it$`, st.browserLists)
			sc.Step(`^the row carries a curated mark and the provider name$`, st.rowCarriesMark)
			sc.Step(`^it is visually distinct from every human-station badge$`, st.visuallyDistinct)
			sc.Step(`^the band browser renders with curated stations present$`, st.curatedPresent)
			sc.Step(`^the curated rows appear with their mark$`, st.rowsAppearMarked)
			sc.Step(`^the filter line says how many are curated$`, st.filterLineCounts)
			sc.Step(`^the operator toggles curated off in the band filter$`, st.toggleOff)
			sc.Step(`^every curated-only band disappears from the dial$`, st.curatedOnlyGone)
			sc.Step(`^a band with mixed supply stays, counting only its human stations$`, st.mixedStaysHumanCount)
			sc.Step(`^the operator toggled curated off$`, st.toggledOff)
			sc.Step(`^they leave and return to the browser$`, st.leaveAndReturn)
			sc.Step(`^curated is still hidden$`, st.stillHidden)
			sc.Step(`^the filter strip says so$`, st.filterStripSaysSo)
			sc.Step(`^the operator hid curated supply$`, st.hidCurated)
			sc.Step(`^the agent auto-tunes a band$`, st.agentAutoTunes)
			sc.Step(`^it never binds a curated-only band$`, st.neverBindsCuratedOnly)
			sc.Step(`^the band card shows the upstream list price and the routing fee separately$`, st.bandCardShowsSplit)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true,
			Paths: []string{"../../features/curated/curated_dial.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("curated dial scenarios failed")
	}
}
