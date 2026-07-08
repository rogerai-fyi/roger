package tui

// band_badges_bdd_test.go - the godog harness for features/tui/band_badges.feature
// (the AGENT [0] desk-entry redesign, R4/R5: agent-ready ⌁ / vision ◪ badges). The steps
// drive the REAL badge builders (bandBadge / plainBandBadge / bandBadgeLegend) over a
// REAL band grouped from real offers by groupBands - no mocks, no seams.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/cucumber/godog"
	"github.com/rogerai-fyi/roger/internal/glyphs"
)

type badgeBDD struct {
	t       *testing.T
	free    bool
	ctx     int
	caps    []string
	name    string
	haveCtx bool
}

func (b *badgeBDD) reset() { *b = badgeBDD{t: b.t} }

// build groups the declared offer(s) into the one band under test.
func (b *badgeBDD) band() band {
	o := offer{NodeID: "n1", Model: b.name, Online: true, PriceOut: 0.3, PriceIn: 0.2}
	if b.haveCtx {
		o.Ctx = b.ctx
	}
	if b.free {
		o.FreeNow, o.PriceIn, o.PriceOut = true, 0, 0
	}
	o.Capabilities = b.caps
	bands := groupBands([]offer{o}, &LimitStore{})
	if len(bands) != 1 {
		b.t.Fatalf("expected one band, got %d", len(bands))
	}
	return bands[0]
}

func (b *badgeBDD) badge() string      { return stripANSI(bandBadge(b.band(), &LimitStore{}, false)) }
func (b *badgeBDD) plainBadge() string { return plainBandBadge(b.band(), &LimitStore{}, false) }

// --- Given -------------------------------------------------------------------------------

func (b *badgeBDD) bandWithWindow(name string, ctx int) error {
	b.name, b.ctx, b.haveCtx = name, ctx, true
	return nil
}
func (b *badgeBDD) freeBandWithWindow(name string, ctx int) error {
	b.name, b.ctx, b.haveCtx, b.free = name, ctx, true, true
	return nil
}
func (b *badgeBDD) bandDeclaringCaps(name, caps string) error {
	b.name = name
	if strings.TrimSpace(caps) != "" {
		b.caps = strings.Split(caps, ",")
	}
	return nil
}
func (b *badgeBDD) bandDeclaringNoCaps(name string) error { b.name = name; return nil }
func (b *badgeBDD) thatBandDeclaresCaps(caps string) error {
	b.caps = strings.Split(caps, ",")
	return nil
}
func (b *badgeBDD) asciiSet() error {
	b.t.Setenv("ROGERAI_ASCII", "1")
	return nil
}

// --- Then --------------------------------------------------------------------------------

func (b *badgeBDD) showsAgentReady() error {
	if !strings.Contains(b.badge(), glyphs.Current().AgentReady) {
		return fmt.Errorf("badge %q lacks the agent-ready mark", b.badge())
	}
	return nil
}
func (b *badgeBDD) noAgentReady() error {
	if strings.Contains(b.badge(), glyphs.Current().AgentReady) {
		return fmt.Errorf("badge %q unexpectedly shows the agent-ready mark", b.badge())
	}
	return nil
}
func (b *badgeBDD) agentReadyInferred() error {
	want := glyphs.Current().AgentReady + "~"
	if !strings.Contains(b.badge(), want) {
		return fmt.Errorf("badge %q lacks the inferred %q", b.badge(), want)
	}
	return nil
}
func (b *badgeBDD) showsVision() error {
	if !strings.Contains(b.badge(), glyphs.Current().Vision) {
		return fmt.Errorf("badge %q lacks the vision mark", b.badge())
	}
	return nil
}
func (b *badgeBDD) noVision() error {
	if strings.Contains(b.badge(), glyphs.Current().Vision) {
		return fmt.Errorf("badge %q unexpectedly shows the vision mark", b.badge())
	}
	return nil
}
func (b *badgeBDD) badgeSays(text string) error {
	if !strings.Contains(b.badge(), text) {
		return fmt.Errorf("badge %q lacks %q", b.badge(), text)
	}
	return nil
}
func (b *badgeBDD) badgeNotSays(text string) error {
	if strings.Contains(b.badge(), text) {
		return fmt.Errorf("badge %q unexpectedly contains %q", b.badge(), text)
	}
	return nil
}
func (b *badgeBDD) badgeContains(text string) error {
	if !strings.Contains(b.badge(), text) {
		return fmt.Errorf("badge %q lacks %q", b.badge(), text)
	}
	return nil
}
func (b *badgeBDD) badgeNotContains(text string) error {
	if strings.Contains(b.badge(), text) {
		return fmt.Errorf("badge %q unexpectedly contains %q", b.badge(), text)
	}
	return nil
}
func (b *badgeBDD) plainShowsAgentReady() error {
	if !strings.Contains(b.plainBadge(), glyphs.Current().AgentReady) {
		return fmt.Errorf("plain badge %q lacks the agent-ready mark", b.plainBadge())
	}
	return nil
}
func (b *badgeBDD) plainShowsVision() error {
	if !strings.Contains(b.plainBadge(), glyphs.Current().Vision) {
		return fmt.Errorf("plain badge %q lacks the vision mark", b.plainBadge())
	}
	return nil
}
func (b *badgeBDD) plainNoANSI() error {
	if strings.Contains(b.plainBadge(), "\x1b[") {
		return fmt.Errorf("plain badge carries ANSI: %q", b.plainBadge())
	}
	return nil
}
func (b *badgeBDD) legendNames(text string) error {
	leg := stripANSI(bandBadgeLegend())
	if !strings.Contains(leg, text) {
		return fmt.Errorf("legend %q lacks %q", leg, text)
	}
	return nil
}

func TestBandBadgesBDD(t *testing.T) {
	st := &badgeBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.reset()
				return ctx, nil
			})
			sc.Step(`^a band "([^"]*)" whose representative window is (\d+)$`, st.bandWithWindow)
			sc.Step(`^a free band "([^"]*)" whose representative window is (\d+)$`, st.freeBandWithWindow)
			sc.Step(`^a band "([^"]*)" with a station declaring capabilities "([^"]*)"$`, st.bandDeclaringCaps)
			sc.Step(`^a band "([^"]*)" with a station declaring no capabilities$`, st.bandDeclaringNoCaps)
			sc.Step(`^that band has a station declaring capabilities "([^"]*)"$`, st.thatBandDeclaresCaps)
			sc.Step(`^ROGERAI_ASCII is set$`, st.asciiSet)
			sc.Step(`^the band badge shows the agent-ready mark$`, st.showsAgentReady)
			sc.Step(`^the band badge does not show the agent-ready mark$`, st.noAgentReady)
			sc.Step(`^the agent-ready mark carries the inferred "~" suffix$`, st.agentReadyInferred)
			sc.Step(`^the band badge shows the vision mark$`, st.showsVision)
			sc.Step(`^the band badge does not show the vision mark$`, st.noVision)
			sc.Step(`^the band badge shows "([^"]*)"$`, st.badgeSays)
			sc.Step(`^the band badge does not say "([^"]*)"$`, st.badgeNotSays)
			sc.Step(`^the band badge contains "([^"]*)"$`, st.badgeContains)
			sc.Step(`^the band badge does not contain "([^"]*)"$`, st.badgeNotContains)
			sc.Step(`^the plain band badge shows the agent-ready mark$`, st.plainShowsAgentReady)
			sc.Step(`^the plain band badge shows the vision mark$`, st.plainShowsVision)
			sc.Step(`^the plain band badge carries no ANSI color$`, st.plainNoANSI)
			sc.Step(`^the badge legend names the agent-ready mark as "([^"]*)"$`, st.legendNames)
			sc.Step(`^the badge legend names the inferred suffix as "([^"]*)"$`, st.legendNames)
			sc.Step(`^the badge legend names the vision mark as "([^"]*)"$`, st.legendNames)
		},
		Options: &godog.Options{Format: "pretty", Paths: []string{"../../features/tui"}, TestingT: t, Strict: true},
	}
	if suite.Run() != 0 {
		t.Fatal("band-badge scenarios failed (see godog output above)")
	}
}
