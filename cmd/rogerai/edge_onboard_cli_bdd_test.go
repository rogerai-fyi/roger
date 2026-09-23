package main

// Executable spec: features/edge/onboard.feature (@cli) - `roger edge setup`, the interactive
// CLI mirror of the TUI wizard. An isolated config dir, the REAL dispatch and the REAL
// edgeDesignateCore / edgeEnrollCore, prompts answered over edgeStdin, and (for the not-allowed
// join) a REAL HTTP authority that refuses with the real 403 the enrollment path classifies.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/client"
	"rogerai.fm/roger/v6/internal/edgeauth"
)

type onboardCLIBDD struct {
	t         *testing.T
	dir       string
	out       string
	err       error
	stdin     string
	interact  bool
	authority *httptest.Server
	prevInter func() bool
}

func (s *onboardCLIBDD) reset(t *testing.T) {
	s.t = t
	s.dir = useTempConfig(t)
	s.out, s.err, s.stdin, s.interact = "", nil, "", true
	if s.authority != nil {
		s.authority.Close()
		s.authority = nil
	}
	s.prevInter = edgeIsInteractive
	edgeIsInteractive = func() bool { return s.interact }
	edgeStdin = strings.NewReader("")
	t.Cleanup(func() { edgeIsInteractive = s.prevInter })
}

func (s *onboardCLIBDD) run(line string) error {
	args := strings.Fields(line)
	if len(args) == 0 || args[0] != "roger" {
		return fmt.Errorf("a command line starts with `roger`, got %q", line)
	}
	edgeStdin = strings.NewReader(s.stdin)
	out, err := captureEdgeStdout(func() error { return dispatch(loadConfig(), args[1:]) })
	s.out, s.err = out, err
	if err != nil {
		s.out += "\nerror: " + err.Error() + "\n"
	}
	return nil
}

// --- givens ----------------------------------------------------------------

func (s *onboardCLIBDD) nothingSetUp() error { return nil } // the temp dir is already bare

func (s *onboardCLIBDD) notYetAllowed() error {
	// A real authority that refuses this machine the way an un-allowed enroll is refused:
	// 403 with the exact reason the enrollment path maps back to ErrUnknownMachine.
	s.authority = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, edgeauth.ErrUnknownMachine.Error(), http.StatusForbidden)
	}))
	return nil
}

func (s *onboardCLIBDD) noInteractiveInput() error { s.interact = false; return nil }

// --- whens -----------------------------------------------------------------

func (s *onboardCLIBDD) runsPlain(line string) error { return s.run(line) }

func (s *onboardCLIBDD) runsNewNamed(name string) error {
	s.stdin = "n\n" + name + "\n"
	return s.run("roger edge setup")
}

func (s *onboardCLIBDD) runsJoin(name string) error {
	if s.authority == nil {
		return fmt.Errorf("no authority stub stood up")
	}
	s.stdin = "j\n" + name + "\n" + s.authority.URL + "\n"
	return s.run("roger edge setup")
}

// --- thens -----------------------------------------------------------------

func (s *onboardCLIBDD) has(subs ...string) error {
	for _, sub := range subs {
		if !strings.Contains(strings.ToLower(s.out), strings.ToLower(sub)) {
			return fmt.Errorf("output does not mention %q:\n%s", sub, s.out)
		}
	}
	return nil
}

func (s *onboardCLIBDD) describesInteractive() error {
	return s.has("interactively")
}
func (s *onboardCLIBDD) pointsAtSetup() error {
	return s.has("roger edge setup")
}
func (s *onboardCLIBDD) namesIndividualCommands() error {
	return s.has("roger edge enroll", "roger edge scan")
}
func (s *onboardCLIBDD) designatedAndEnrolled() error {
	// The real proof: this machine is now the local authority and enrolled. Read it back
	// through the same status the CLI reads.
	self := edgeSelfStatus(nil, edgeDiscoveryFactsFromEnv())
	if !self.AuthorityHere {
		return fmt.Errorf("this machine did not become the authority:\n%s", s.out)
	}
	if !self.Enrolled {
		return fmt.Errorf("this machine was not enrolled:\n%s", s.out)
	}
	return nil
}
func (s *onboardCLIBDD) printsHowToJoin() error {
	return s.has("roger edge enroll", "--authority")
}
func (s *onboardCLIBDD) printsKeyAndAllow() error {
	return s.has("roger edge authority allow")
}
func (s *onboardCLIBDD) printsThisKey() error {
	// The ACTUAL user key must appear - the same client.UserPubHex() the authority allows -
	// not merely the word "allow". A stub or a wrong key would fail this.
	key := client.UserPubHex()
	if key == "" {
		return fmt.Errorf("this machine has no user key to print")
	}
	return s.has(key)
}
func (s *onboardCLIBDD) didNotJoin() error {
	if strings.Contains(strings.ToLower(s.out), "joined:") {
		return fmt.Errorf("it claimed to join when it was not allowed:\n%s", s.out)
	}
	self := edgeSelfStatus(nil, edgeDiscoveryFactsFromEnv())
	if self.Enrolled {
		return fmt.Errorf("the machine was enrolled despite not being allowed")
	}
	return nil
}
func (s *onboardCLIBDD) explainsNeedsTerminal() error {
	return s.has("needs a terminal")
}
func (s *onboardCLIBDD) namesNonInteractive() error {
	return s.has("roger edge authority local", "roger edge enroll", "roger edge authority allow")
}
func (s *onboardCLIBDD) changesNothing() error {
	// No identity, no descriptor: the config dir's edge subtree is untouched.
	if _, err := os.Stat(filepath.Join(s.dir, "edge")); err == nil {
		return fmt.Errorf("setup created edge state when it should have changed nothing")
	}
	self := edgeSelfStatus(nil, edgeDiscoveryFactsFromEnv())
	if self.Enrolled || self.AuthorityHere {
		return fmt.Errorf("setup changed this machine's Edge state")
	}
	return nil
}

func TestOnboardFeatureCLI(t *testing.T) {
	st := &onboardCLIBDD{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(t); return c, nil })

			sc.Step(`^an owner at a machine that has set nothing up$`, st.nothingSetUp)
			sc.Step(`^an owner whose machine is not yet allowed on the authority$`, st.notYetAllowed)
			sc.Step(`^no interactive input is available$`, st.noInteractiveInput)

			sc.Step(`^they run "roger edge setup --help"$`, func() error { return st.run("roger edge setup --help") })
			sc.Step(`^they run "roger edge" with nothing set up$`, func() error { return st.run("roger edge") })
			sc.Step(`^they run "roger edge setup" and choose to start a new Edge named "([^"]*)"$`, st.runsNewNamed)
			sc.Step(`^they run "roger edge setup", choose to join, name it "([^"]*)" and give the authority address$`, st.runsJoin)
			sc.Step(`^they run "roger edge setup"$`, func() error { return st.run("roger edge setup") })

			sc.Step(`^it describes setting up this machine's Edge interactively$`, st.describesInteractive)
			sc.Step(`^the output points at "roger edge setup" as well as the individual commands$`, func() error {
				if err := st.pointsAtSetup(); err != nil {
					return err
				}
				return st.namesIndividualCommands()
			})
			sc.Step(`^this machine is designated the authority and enrolled$`, st.designatedAndEnrolled)
			sc.Step(`^it prints how another machine joins$`, st.printsHowToJoin)
			sc.Step(`^it prints this machine's user key and the allow command to run on the authority$`, func() error {
				if err := st.printsKeyAndAllow(); err != nil {
					return err
				}
				return st.printsThisKey()
			})
			sc.Step(`^it exits without pretending the machine joined$`, st.didNotJoin)
			sc.Step(`^it explains it needs a terminal, and names the non-interactive commands to use instead$`, func() error {
				if err := st.explainsNeedsTerminal(); err != nil {
					return err
				}
				return st.namesNonInteractive()
			})
			sc.Step(`^it changes nothing$`, st.changesNothing)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@cli",
			Paths: []string{"../../features/edge/onboard.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the onboarding CLI scenarios failed")
	}
}
