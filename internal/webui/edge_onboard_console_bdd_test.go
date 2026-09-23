package webui

// Executable spec: features/edge/onboard.feature (@console) - the console's empty Edge points
// at the terminal for setup (the wizard is a terminal thing for now). Reuses the console EDGE
// tab fixture: the REAL Server over httptest, a REAL empty Fleet, a SelfStatus that says
// "not enrolled", read exactly as the browser reads /api/edge.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/edge"
)

func (s *consoleEdgeBDD) consoleNotEnrolled() error {
	s.self = &edge.SelfStatus{Authority: "Core", Discovery: edge.DiscoveryScanning, IntervalS: 30}
	return nil
}

func (s *consoleEdgeBDD) panelSaysTerminalSetup() error {
	if s.snap.Facts == nil {
		return fmt.Errorf("no facts on the empty panel:\n%s", string(s.body))
	}
	if !strings.Contains(strings.ToLower(s.snap.Facts.Setup), "terminal") {
		return fmt.Errorf("the panel does not point at the terminal for setup: %q", s.snap.Facts.Setup)
	}
	return nil
}

func (s *consoleEdgeBDD) panelNamesSetupCommand() error {
	if !strings.Contains(s.snap.Facts.Setup, "roger edge setup") {
		return fmt.Errorf("the panel does not name roger edge setup: %q", s.snap.Facts.Setup)
	}
	return nil
}

func TestOnboardFeatureConsole(t *testing.T) {
	st := &consoleEdgeBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(); return c, nil })
			sc.Step(`^a console over a machine that is not enrolled$`, st.consoleNotEnrolled)
			sc.Step(`^the Edge data is read$`, func() error { st.readEdge(); return nil })
			sc.Step(`^the panel says setting up the Edge is done from the terminal for now$`, st.panelSaysTerminalSetup)
			sc.Step(`^it names roger edge setup$`, st.panelNamesSetupCommand)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@console",
			Paths: []string{"../../features/edge/onboard.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the onboarding console scenario failed")
	}
}
