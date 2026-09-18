package main

// Executable spec: features/edge/empty_edge.feature (@cli) - `roger edge` with nothing set
// up prints the same three facts the TUI and the console show. Reuses the CLI suite's
// fixture (an isolated config dir, the real dispatch, captured stdout).

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cucumber/godog"
)

func (s *edgeCLIBDD) printsOnlyNode() error {
	if !strings.Contains(s.out, "this machine is the only node") {
		return fmt.Errorf("no 'only node' line:\n%s", s.out)
	}
	return nil
}

func (s *edgeCLIBDD) printsFact(label, want string) error {
	for _, ln := range strings.Split(s.out, "\n") {
		if strings.Contains(ln, label) {
			if strings.Contains(ln, want) {
				return nil
			}
			return fmt.Errorf("%s line does not say %q: %q", label, want, ln)
		}
	}
	return fmt.Errorf("no %s line:\n%s", label, s.out)
}

func (s *edgeCLIBDD) printsScanAdoptHints() error {
	for _, w := range []string{"roger edge scan", "roger edge adopt"} {
		if !strings.Contains(s.out, w) {
			return fmt.Errorf("%q missing:\n%s", w, s.out)
		}
	}
	return nil
}

func (s *edgeCLIBDD) namesBoth(a, b string) error {
	for _, w := range []string{a, b} {
		if !strings.Contains(s.out, w) {
			return fmt.Errorf("%q missing:\n%s", w, s.out)
		}
	}
	return nil
}

func TestEmptyEdgeFeatureCLI(t *testing.T) {
	st := &edgeCLIBDD{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(t); return c, nil })
			// A machine with NOTHING set up: no login, no Edge cache, no enrollment. (The
			// cli.feature step of the same name also seeds a LAN peer; this one must not.)
			sc.Given(`^an owner who is not logged in$`, func() error {
				err := os.Remove(filepath.Join(filepath.Dir(configPath()), "auth.json"))
				if err != nil && !os.IsNotExist(err) {
					return err
				}
				return nil
			})
			sc.When(`^they run "([^"]*)"$`, st.run)
			sc.Then(`^it prints that this machine is the only node$`, st.printsOnlyNode)
			sc.Then(`^it prints THIS MACHINE as not enrolled$`, func() error { return st.printsFact("THIS MACHINE", "not enrolled") })
			sc.Then(`^it prints AUTHORITY as Core$`, func() error { return st.printsFact("AUTHORITY", "Core") })
			sc.Then(`^it prints the scan and adopt hints$`, st.printsScanAdoptHints)
			sc.Then(`^it names "([^"]*)" and "([^"]*)"$`, st.namesBoth)
			sc.Then(`^it prints DISCOVERY as not scanning in this process$`, func() error {
				if err := st.printsFact("DISCOVERY", "not scanning in this process"); err != nil {
					return err
				}
				for _, ln := range strings.Split(st.out, "\n") {
					if strings.Contains(ln, "DISCOVERY") && strings.Contains(ln, "scanning this network") {
						return fmt.Errorf("the one-shot CLI claims to be scanning: %q", ln)
					}
				}
				return nil
			})
			sc.Then(`^it names "([^"]*)" as the way to look now$`, func(w string) error { return st.namesBoth(w, w) })
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@cli",
			Paths: []string{"../../features/edge/empty_edge.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the empty-Edge CLI scenarios failed")
	}
}
