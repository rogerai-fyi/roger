package main

// Executable spec: features/edge/instances.feature (@cli) - the CLI shows a node's running
// rogers under it, describes one, and renames this one. Isolated config dir; the real
// dispatch; a real household directory with real registrations standing in for the rogers
// running on this machine.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/edgeauth"
	"rogerai.fm/roger/v6/internal/store"
)

type instCLIBDD struct {
	t    *testing.T
	out  string
	err  error
	regs map[string]*edge.Registry
	node string
}

func (s *instCLIBDD) reset() {
	useTempConfig(s.t)
	s.t.Setenv("ROGER_BROKER", "http://127.0.0.1:1")
	s.out, s.err, s.regs, s.node = "", nil, map[string]*edge.Registry{}, ""
}

func (s *instCLIBDD) run(line string) error {
	args := strings.Fields(line)
	edgeStdin = strings.NewReader("")
	out, err := captureEdgeStdout(func() error { return dispatch(loadConfig(), args[1:]) })
	s.out, s.err = out, err
	if err != nil {
		s.out += "\nerror: " + err.Error() + "\n"
	}
	return nil
}

// thisMachineEnrolled makes this machine the node named `node`, rooted locally so no
// network is needed, exactly as the enrolment spec does.
func (s *instCLIBDD) thisMachineEnrolled(node string) error {
	s.node = node
	if err := s.run("roger edge authority local " + node); err != nil {
		return err
	}
	if s.err != nil {
		return s.err
	}
	if err := s.run("roger edge enroll " + node); err != nil {
		return err
	}
	return s.err
}

func (s *instCLIBDD) nodeID() string {
	id, _, ok, err := edgeauth.Store{Dir: edgeAuthDir()}.LoadIdentity()
	if err != nil || !ok {
		s.t.Fatalf("this machine is not enrolled: ok=%v err=%v", ok, err)
	}
	return id.NodeID
}

// running is a roger on this machine: a real registration in this machine's household.
func (s *instCLIBDD) running(name string, bands ...string) error {
	inst := edge.Instance{Name: name, Caps: edgeInstanceCaps(bands), Bands: bands}
	r, err := edge.Register(edgeInstancesDir(), s.nodeID(), inst, time.Now())
	if err != nil {
		return err
	}
	s.regs[name] = r
	return nil
}

func (s *instCLIBDD) fleetWithNodeRunning(node, a, b string) error {
	if err := s.thisMachineEnrolled(node); err != nil {
		return err
	}
	if err := s.running(a, "gpt-oss-20b"); err != nil {
		return err
	}
	return s.running(b)
}

func (s *instCLIBDD) fleetWithNodeRunningOne(node, a string) error {
	if err := s.thisMachineEnrolled(node); err != nil {
		return err
	}
	return s.running(a, "gpt-oss-20b")
}

func (s *instCLIBDD) enrolledRunningOne() error {
	if err := s.thisMachineEnrolled("workshop"); err != nil {
		return err
	}
	return s.running("workshop")
}

func (s *instCLIBDD) listedOnceAsNode(node string) error {
	n := 0
	for _, ln := range strings.Split(s.out, "\n") {
		// the mode header names the authority machine, which may be this node; only
		// fleet rows (not the "EDGE ·" header, not instance rows) count as node lines
		if strings.HasPrefix(ln, "EDGE ·") || strings.Contains(ln, "/") {
			continue
		}
		if strings.Contains(ln, node) {
			n++
		}
	}
	if n != 1 {
		return fmt.Errorf("%q appears on %d node lines:\n%s", node, n, s.out)
	}
	return nil
}

func (s *instCLIBDD) listedUnder(a, b string) error {
	for _, name := range []string{a, b} {
		found := false
		for _, ln := range strings.Split(s.out, "\n") {
			if strings.HasPrefix(strings.TrimLeft(ln, " "), "/"+name) && strings.Contains(ln, "operate") {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("instance %q is not listed under its node with its capabilities:\n%s", name, s.out)
		}
	}
	return nil
}

func (s *instCLIBDD) printsInstanceFacts() error {
	for _, w := range []string{"node", "capabilities", "serving", "started"} {
		if !strings.Contains(s.out, w) {
			return fmt.Errorf("%q missing:\n%s", w, s.out)
		}
	}
	return nil
}

func (s *instCLIBDD) noCertPretence() error {
	if !strings.Contains(s.out, "none of its own") {
		return fmt.Errorf("the instance is described as if it had a certificate:\n%s", s.out)
	}
	return nil
}

func (s *instCLIBDD) renamedTo(name string) error {
	if s.err != nil {
		return s.err
	}
	// the running roger picks the choice up on its next pass: the same sync the host does
	for _, r := range s.regs {
		if err := r.Rename(s.nodeID(), loadConfig().EdgeInstance, time.Now()); err != nil {
			return err
		}
	}
	for _, in := range edge.Household(edgeInstancesDir(), time.Now()) {
		if in.Name == name {
			return nil
		}
	}
	return fmt.Errorf("no instance named %q in the household", name)
}

func (s *instCLIBDD) everySurfaceShowsNewName() error {
	if err := s.run("roger edge list"); err != nil {
		return err
	}
	if !strings.Contains(s.out, "/bench-desk") {
		return fmt.Errorf("list does not show the new name:\n%s", s.out)
	}
	return nil
}

func (s *instCLIBDD) nodeNameUntouched() error {
	st, err := loadEdgeState()
	if err != nil {
		return err
	}
	n, ok, err := st.fleet.Get(s.nodeID())
	if err != nil || !ok || n.Name != s.node {
		return fmt.Errorf("node = %+v ok=%v err=%v", n, ok, err)
	}
	return nil
}

var _ = store.EdgeNode{}

func TestInstancesFeatureCLI(t *testing.T) {
	st := &instCLIBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(); return c, nil })
			sc.Step(`^a fleet with a node "([^"]*)" running "([^"]*)" and "([^"]*)"$`, st.fleetWithNodeRunning)
			sc.Step(`^a fleet with a node "([^"]*)" running "([^"]*)"$`, st.fleetWithNodeRunningOne)
			sc.Step(`^this machine is enrolled and running one instance$`, st.enrolledRunningOne)
			sc.Step(`^they run "([^"]*)"$`, st.run)
			sc.Step(`^"([^"]*)" is listed once as a node$`, st.listedOnceAsNode)
			sc.Step(`^"([^"]*)" and "([^"]*)" are listed under it with their capabilities$`, st.listedUnder)
			sc.Step(`^it prints the instance's name, node, capabilities, start time and what it is serving$`, st.printsInstanceFacts)
			sc.Step(`^it does not pretend the instance has a certificate of its own$`, st.noCertPretence)
			sc.Step(`^this instance is renamed to "([^"]*)"$`, st.renamedTo)
			sc.Step(`^every surface shows the new name$`, st.everySurfaceShowsNewName)
			sc.Step(`^the node's name is untouched$`, st.nodeNameUntouched)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@cli",
			Paths: []string{"../../features/edge/instances.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the instances CLI scenarios failed")
	}
}
