package tui

// The @tui scenarios of features/models/short_names_nearby.feature, against the real SHARE screen;
// cmd/rogerai runs every other scenario of the file and checks that the rows these hooks return
// are the ones the terminal suggests.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/bddrun"
)

type nearbyTUIBDD struct {
	t   *testing.T
	f   *nearbyFake
	m   model
	out string
}

func (s *nearbyTUIBDD) steps(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		s.f = &nearbyFake{modelsFake: &modelsFake{view: baseView()}}
		s.out = ""
		return ctx, nil
	})
	sc.Step(`^a machine whose usable memory roger can measure$`, func() error { return nil })
	sc.Step(`^nearby sources serve bands that fit this machine$`, func() error { s.f.nv = nearbyRows(); return nil })
	sc.Step(`^nothing is served nearby$`, func() error { return nil })
	open := func() error {
		s.m = openGet(s.t, s.f, 130)
		s.out = stripANSI(s.m.modelsView(130))
		return nil
	}
	sc.Step(`^the owner chooses "Get a model that fits this machine" in SHARE$`, open)
	sc.Step(`^the owner opens the model browser$`, open)
	sc.Step(`^the owner chooses a nearby band in the browser$`, func() error {
		s.f.nv = nearbyRows()
		if err := open(); err != nil {
			return err
		}
		s.m, _ = press(s.t, s.m, "enter")
		s.out = stripANSI(s.m.modelsView(130))
		return nil
	})
	sc.Step(`^the browser is drawn at 80 columns under NO_COLOR$`, func() error {
		s.t.Setenv("NO_COLOR", "1")
		s.f.nv = nearbyRows()
		s.m = openGet(s.t, s.f, 80)
		s.out = s.m.View()
		return nil
	})
	has := func(want ...string) error {
		for _, w := range want {
			if !strings.Contains(s.out, w) {
				return fmt.Errorf("missing %q in:\n%s", w, s.out)
			}
		}
		return nil
	}
	sc.Step(`^those bands are shown first, each with its source, size and verdict$`, func() error {
		if err := has("qwen3-8b", "your Edge member atlas", "5.0 GiB", "fits  at 8K"); err != nil {
			return err
		}
		if i, j := strings.Index(s.out, "qwen3-8b"), strings.Index(s.out, "gpt-oss-120b"); j < i {
			return fmt.Errorf("the ones that fit are not first")
		}
		return nil
	})
	sc.Step(`^this machine's usable memory is stated above them$`, func() error {
		if i, j := strings.Index(s.out, "this machine: 16.0 GiB of system memory"), strings.Index(s.out, "qwen3-8b"); i < 0 || j < i {
			return fmt.Errorf("the memory is not above the bands:\n%s", s.out)
		}
		return nil
	})
	sc.Step(`^searching the model hub is one keystroke away and is not sent until chosen$`, func() error {
		if err := has("/ search the model hub - nothing is sent until you press ⏎"); err != nil {
			return err
		}
		m, _ := press(s.t, s.m, "/")
		if !m.mdl.typing || len(s.f.searched) != 0 {
			return fmt.Errorf("/ did not open the search, or a search was sent: %v", s.f.searched)
		}
		return nil
	})
	sc.Step(`^it says nothing is served nearby$`, func() error { return has("nothing is served nearby") })
	sc.Step(`^the search is ready to type into, not yet sent$`, func() error {
		if !s.m.mdl.typing || len(s.f.searched) != 0 {
			return fmt.Errorf("typing=%v searched=%v", s.m.mdl.typing, s.f.searched)
		}
		return nil
	})
	sc.Step(`^the same mapping and consent line is shown as for "roger model serve <name>"$`, func() error {
		return has(s.f.nv.Fits[0].Ask, "checked against the digest your Edge member atlas reported")
	})
	sc.Step(`^nothing is fetched until the owner confirms$`, func() error {
		m, _ := press(s.t, s.m, "n")
		if len(s.f.picked) != 0 {
			return fmt.Errorf("a no fetched")
		}
		m, _ = press(s.t, m, "enter")
		_, cmd := press(s.t, m, "y")
		feed(s.t, m, cmd)
		if len(s.f.picked) != 1 {
			return fmt.Errorf("a yes did not fetch: %v", s.f.picked)
		}
		return nil
	})
	sc.Step(`^each suggestion's name, source, size and verdict read in text alone$`, func() error {
		if strings.Contains(s.out, "\x1b[") {
			return fmt.Errorf("colour escapes under NO_COLOR")
		}
		return has("qwen3-8b", "atlas", "5.0 GiB", "fits")
	})
	sc.Step(`^nothing overflows the width$`, func() error {
		for _, l := range strings.Split(s.out, "\n") {
			if w := utf8.RuneCountInString(stripANSI(l)); w > 80 {
				return fmt.Errorf("a line overflows 80 columns (%d): %q", w, l)
			}
		}
		return nil
	})
}

func TestShortNamesNearbyTUIBDD(t *testing.T) {
	s := &nearbyTUIBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: s.steps,
		Options: &godog.Options{Format: "pretty", TestingT: t, Strict: true, Tags: "@tui",
			Paths: []string{"../../features/models/short_names_nearby.feature"}},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("features/models/short_names_nearby.feature (@tui) failed")
	}
}
