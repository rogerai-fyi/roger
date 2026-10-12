package main

// Executable spec: features/edge/empty_edge_simple.feature (§7, the untagged CLI scenario) -
// `roger edge` with nothing set up says the same three things the TUI and the console show.
// Reuses the CLI suite's fixture (an isolated config dir, the real dispatch, captured stdout).

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/bddrun"
	"rogerai.fm/roger/v6/internal/look"
)

func (s *edgeCLIBDD) printsSimpleEmpty() error {
	for _, w := range []string{look.EmptyEdgeCLIHead, look.EmptyEdgeSay, look.EmptyEdgeCLIStart, look.EmptyEdgeCLIJoin, look.EmptyEdgeStart} {
		if !strings.Contains(s.out, w) {
			return fmt.Errorf("%q missing:\n%s", w, s.out)
		}
	}
	for _, w := range []string{"not enrolled", "roger edge enroll", "roger login", "Core ·", "the only node"} {
		if strings.Contains(s.out, w) {
			return fmt.Errorf("the empty Edge still says %q:\n%s", w, s.out)
		}
	}
	return nil
}

func TestEmptyEdgeSimpleFeatureCLI(t *testing.T) {
	st := &edgeCLIBDD{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(t); return c, nil })
			sc.Given(`^this machine is in no Edge \(not enrolled and not an authority\)$`, func() error { return nil })
			sc.When("^the owner runs `roger edge` on a terminal$", func() error { return st.run("roger edge") })
			sc.Then("^it prints \"your Edge: not set up yet\", the one-sentence explanation, and two next steps: `roger edge setup` \\(start an Edge here\\) and `roger join <invite or address>`$", st.printsSimpleEmpty)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "~@tui && ~@console",
			Paths: []string{"../../features/edge/empty_edge_simple.feature"},
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("the CLI's simple empty Edge scenario failed")
	}
}
