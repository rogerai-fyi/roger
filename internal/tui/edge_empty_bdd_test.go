package tui

// Executable spec: features/edge/empty_edge.feature (@tui) - what [3] EDGE says when the
// owner has set nothing up. The real model, the real renderer at fixed widths, a real
// (empty) fleet, and a SelfStatus record filled the way the host fills it.

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

type emptyEdgeBDD struct {
	t       *testing.T
	now     time.Time
	fleet   *edge.Fleet
	status  edge.SelfStatus
	noHost  bool
	m       model
	built   bool
	w       int
	out     string
	frameA  string
	frameB  string
	noColor bool
}

func (s *emptyEdgeBDD) reset() {
	s.now = time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	s.fleet = edge.NewFleet(store.NewMem(), "acct-1")
	s.fleet.SetClock(func() time.Time { return s.now })
	s.status = edge.SelfStatus{Authority: "Core", Discovery: edge.DiscoveryScanning, IntervalS: 30}
	s.noHost, s.built, s.w, s.out, s.frameA, s.frameB, s.noColor = false, false, 0, "", "", "", false
}

func (s *emptyEdgeBDD) build() {
	if s.built {
		return
	}
	hooks := Hooks{Station: "gentle-mongoose-93"}
	if !s.noHost {
		hooks.EdgeFleet = s.fleet
		hooks.EdgeCandidates = func() []store.EdgeNode { return nil }
		hooks.EdgeStatus = func() edge.SelfStatus { return s.status }
		if s.status.Enrolled {
			hooks.EdgeSelf = s.status.Name
		}
	}
	var tm tea.Model = NewWithHooks("http://broker.local", "tester", nil, hooks)
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m := tm.(model)
	m.edgeClock = func() time.Time { return s.now }
	s.m = m
	s.built = true
}

func (s *emptyEdgeBDD) render(w int) string {
	s.build()
	if s.m.mode != modeEdge {
		s.m.enterEdge()
	} else {
		s.m.refreshEdge()
	}
	s.w = w
	raw := s.m.edgeView(w)
	if s.noColor && strings.Contains(raw, "\x1b[") {
		s.t.Errorf("the frame carries ANSI escapes under NO_COLOR:\n%q", raw)
	}
	s.out = stripANSI(raw)
	return s.out
}

// factLine is the text after a label, joined with the continuation lines under it.
func (s *emptyEdgeBDD) factLine(label string) (string, error) {
	lines := strings.Split(s.out, "\n")
	for i, ln := range lines {
		if !strings.Contains(ln, label) {
			continue
		}
		out := strings.TrimSpace(strings.SplitN(ln, label, 2)[1])
		for j := i + 1; j < len(lines); j++ {
			t := lines[j]
			if strings.TrimSpace(t) == "" || regexp.MustCompile(`^\s{2}\S`).MatchString(t) {
				break // the next label, or a blank
			}
			out += " " + strings.TrimSpace(t)
		}
		return out, nil
	}
	return "", fmt.Errorf("no %s fact on screen:\n%s", label, s.out)
}

func contains(hay string, needles ...string) error {
	for _, n := range needles {
		if !strings.Contains(hay, n) {
			return fmt.Errorf("%q is missing from %q", n, hay)
		}
	}
	return nil
}

// ---- givens ----------------------------------------------------------------

func (s *emptyEdgeBDD) freshMachine() error {
	s.status = edge.SelfStatus{Authority: "Core", Discovery: edge.DiscoveryScanning, IntervalS: 30}
	return nil
}

func (s *emptyEdgeBDD) enrolledAs(name string) error {
	s.status.Enrolled, s.status.Name, s.status.LoggedIn = true, name, true
	return nil
}

func (s *emptyEdgeBDD) localAuthority(where string, allowed int) error {
	s.status.Authority, s.status.AuthorityLocal, s.status.AuthorityHere, s.status.Allowed = where, true, true, allowed
	s.status.Enrolled, s.status.Name = true, where
	return nil
}

func (s *emptyEdgeBDD) localAuthorityAt(where, addr string) error {
	if err := s.localAuthority(where, 0); err != nil {
		return err
	}
	s.status.AuthorityAddr = addr
	return nil
}

func (s *emptyEdgeBDD) notLoggedInCore() error {
	s.status.Authority, s.status.LoggedIn, s.status.Enrolled = "Core", false, false
	return nil
}

func (s *emptyEdgeBDD) lastPassNothing(secs int) error {
	s.status.LastPass = s.now.Add(-time.Duration(secs) * time.Second).Unix()
	s.status.Found = edge.PassSummary(0, 0)
	return nil
}

func (s *emptyEdgeBDD) lastPassCandidates(secs, n int) error {
	s.status.LastPass = s.now.Add(-time.Duration(secs) * time.Second).Unix()
	s.status.Found = edge.PassSummary(0, n)
	return nil
}

func (s *emptyEdgeBDD) discoveryOff() error { s.status.Discovery = edge.DiscoveryOff; return nil }

func (s *emptyEdgeBDD) discoveryNoPassYet() error {
	s.status.Discovery, s.status.LastPass, s.status.Found = edge.DiscoveryScanning, 0, ""
	return nil
}

func (s *emptyEdgeBDD) hostDidNotStart() error { s.noHost = true; return nil }

func (s *emptyEdgeBDD) recordCannotBeRead() error {
	s.status = edge.SelfStatus{Err: "edge record: permission denied"}
	return nil
}

func (s *emptyEdgeBDD) terminalColumns(cols int) error { s.w = cols; return nil }

func (s *emptyEdgeBDD) noColorSet() error {
	s.t.Setenv("NO_COLOR", "1") // the model reads it when built, as the topology suite does
	s.built, s.noColor = false, true
	return nil
}

func (s *emptyEdgeBDD) statusThatChanges() error { return s.freshMachine() }

// ---- whens -----------------------------------------------------------------

func (s *emptyEdgeBDD) rendersAt(cols int) error { s.render(cols); return nil }
func (s *emptyEdgeBDD) renders() error {
	if s.w == 0 {
		s.w = 100
	}
	s.render(s.w)
	return nil
}

func (s *emptyEdgeBDD) twoFrames() error {
	s.frameA = s.render(100)
	s.status.Enrolled, s.status.Name = true, "workshop"
	s.frameB = s.render(100)
	return nil
}

// ---- thens -----------------------------------------------------------------

func (s *emptyEdgeBDD) saysOnlyNode() error { return contains(s.out, "is the only node on the Edge") }

func (s *emptyEdgeBDD) machineSays(a, b string) error {
	f, err := s.factLine("THIS MACHINE")
	if err != nil {
		return err
	}
	return contains(f, a, b)
}

func (s *emptyEdgeBDD) authorityCore() error {
	f, err := s.factLine("AUTHORITY")
	if err != nil {
		return err
	}
	return contains(f, "Core", "needs the network, to reach Core")
}

// names matches across the wrap: a long hint may continue on the next line under its
// label, and the sentence is still one sentence.
func (s *emptyEdgeBDD) names(what string) error {
	return contains(regexp.MustCompile(`\s+`).ReplaceAllString(s.out, " "), what)
}

// doesNotName is scoped to the THIS MACHINE fact: the enroll-against hint under ADD A
// NODE legitimately says "roger edge enroll", but an enrolled machine is not told to
// enroll itself.
func (s *emptyEdgeBDD) doesNotName(what string) error {
	f, err := s.factLine("THIS MACHINE")
	if err != nil {
		return err
	}
	if strings.Contains(f, what) {
		return fmt.Errorf("%q is still a next step for this machine: %q", what, f)
	}
	return nil
}

func (s *emptyEdgeBDD) discoveryScanning() error {
	f, err := s.factLine("DISCOVERY")
	if err != nil {
		return err
	}
	return contains(f, "scanning this network", "every 30s")
}

func (s *emptyEdgeBDD) authorityLocal(where string) error {
	f, err := s.factLine("AUTHORITY")
	if err != nil {
		return err
	}
	return contains(f, where, "no network beyond this LAN")
}

func (s *emptyEdgeBDD) isTheAuthority(n int) error {
	f, err := s.factLine("AUTHORITY")
	if err != nil {
		return err
	}
	return contains(f, "this machine IS the authority", fmt.Sprintf("%d machines may enroll", n))
}

func (s *emptyEdgeBDD) loginNeeded() error {
	f, err := s.factLine("AUTHORITY")
	if err != nil {
		return err
	}
	return contains(f, "not logged in", "roger login")
}

func (s *emptyEdgeBDD) lastPassSays(secs int) error {
	f, err := s.factLine("DISCOVERY")
	if err != nil {
		return err
	}
	return contains(f, fmt.Sprintf("last pass %ds ago", secs), "nothing answered")
}

func (s *emptyEdgeBDD) candidatesSeen(n int) error {
	f, err := s.factLine("DISCOVERY")
	if err != nil {
		return err
	}
	return contains(f, fmt.Sprintf("%d candidates seen", n))
}

func (s *emptyEdgeBDD) discoveryOffSays() error {
	f, err := s.factLine("DISCOVERY")
	if err != nil {
		return err
	}
	return contains(f, "off", "ROGERAI_EDGE_DISCOVERY")
}

func (s *emptyEdgeBDD) notScanning() error {
	f, _ := s.factLine("DISCOVERY")
	if strings.Contains(f, "scanning") {
		return fmt.Errorf("discovery is off but the screen says scanning: %q", f)
	}
	return nil
}

func (s *emptyEdgeBDD) noPassYet() error {
	f, err := s.factLine("DISCOVERY")
	if err != nil {
		return err
	}
	return contains(f, "scanning", "no pass has completed yet")
}

func (s *emptyEdgeBDD) noAgeShown() error {
	f, _ := s.factLine("DISCOVERY")
	if regexp.MustCompile(`\d+[smhd] ago`).MatchString(f) {
		return fmt.Errorf("an age is shown for a pass that never happened: %q", f)
	}
	return nil
}

func (s *emptyEdgeBDD) addNodeLAN() error {
	f, err := s.factLine("ADD A NODE")
	if err != nil {
		return err
	}
	return contains(f, "run RogerAI on another machine on this network", "CANDIDATE", "a adopts it")
}

func (s *emptyEdgeBDD) addNodeEnroll() error {
	f, err := s.factLine("ADD A NODE")
	if err != nil {
		return err
	}
	return contains(f, "enroll", "--authority")
}

func (s *emptyEdgeBDD) notOnlyNode() error {
	if strings.Contains(s.out, "only node") {
		return fmt.Errorf("an unstarted host is drawn as an empty Edge:\n%s", s.out)
	}
	return nil
}

func (s *emptyEdgeBDD) hostNotStartedSaid() error {
	return contains(s.out, "did not start this run", "log")
}

func (s *emptyEdgeBDD) statusUnread() error {
	return contains(s.out, "could not be read", "permission denied")
}

func (s *emptyEdgeBDD) noClaims() error {
	for _, w := range []string{"Core", "enrolled", "scanning"} {
		if strings.Contains(s.out, w) {
			return fmt.Errorf("an unread status still claims %q:\n%s", w, s.out)
		}
	}
	return nil
}

func (s *emptyEdgeBDD) noLineExceeds(cols int) error {
	for i, ln := range strings.Split(s.out, "\n") {
		if got := lipgloss.Width(ln); got > cols {
			return fmt.Errorf("line %d is %d columns wide at %d: %q", i, got, cols, ln)
		}
	}
	return nil
}

func (s *emptyEdgeBDD) threeFactsPresent() error {
	return contains(s.out, "THIS MACHINE", "AUTHORITY", "DISCOVERY")
}

func (s *emptyEdgeBDD) labelsAsText() error {
	// Under NO_COLOR nothing but the words can carry the structure: each label starts
	// its own line and is the first thing on it.
	for _, l := range []string{"THIS MACHINE", "AUTHORITY", "DISCOVERY"} {
		if !regexp.MustCompile(`(?m)^\s*` + l).MatchString(s.out) {
			return fmt.Errorf("%s is not a line-leading label:\n%s", l, s.out)
		}
	}
	return nil
}

func (s *emptyEdgeBDD) framesConsistent() error {
	if !strings.Contains(s.frameA, "not enrolled") || strings.Contains(s.frameA, "workshop") {
		return fmt.Errorf("frame A mixes states:\n%s", s.frameA)
	}
	if !strings.Contains(s.frameB, "workshop") || strings.Contains(s.frameB, "not enrolled") {
		return fmt.Errorf("frame B mixes states:\n%s", s.frameB)
	}
	return nil
}

func (s *emptyEdgeBDD) rReReads() error {
	s.status.Enrolled, s.status.Name = false, ""
	tm, _ := s.m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	switch v := tm.(type) {
	case model:
		s.m = v
	case *model:
		s.m = *v
	}
	s.out = stripANSI(s.m.edgeView(100))
	return contains(s.out, "not enrolled")
}

func TestEmptyEdgeFeatureTUI(t *testing.T) {
	st := &emptyEdgeBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(); return c, nil })
			sc.Step(`^a machine that is not enrolled, not logged in, rooted at Core, scanning its network$`, st.freshMachine)
			sc.Step(`^a machine enrolled as "([^"]*)" with no other node$`, st.enrolledAs)
			sc.Step(`^a machine that is the local authority "([^"]*)" with (\d+) machines allowed$`, st.localAuthority)
			sc.Step(`^a machine that is the local authority "([^"]*)" reachable at "([^"]*)"$`, st.localAuthorityAt)
			sc.Step(`^a machine that is not logged in and rooted at Core$`, st.notLoggedInCore)
			sc.Step(`^discovery last ran (\d+) seconds ago and nothing answered$`, st.lastPassNothing)
			sc.Step(`^discovery last ran (\d+) seconds ago and saw (\d+) candidates$`, st.lastPassCandidates)
			sc.Step(`^discovery is off$`, st.discoveryOff)
			sc.Step(`^discovery is on and has not completed a pass$`, st.discoveryNoPassYet)
			sc.Step(`^the Edge host did not start this run$`, st.hostDidNotStart)
			sc.Step(`^the machine's Edge record cannot be read$`, st.recordCannotBeRead)
			sc.Step(`^a terminal (\d+) columns wide$`, st.terminalColumns)
			sc.Step(`^NO_COLOR is set$`, st.noColorSet)
			sc.Step(`^a status that changes while the screen is open$`, st.statusThatChanges)
			sc.Step(`^the Edge screen renders at (\d+) columns$`, st.rendersAt)
			sc.Step(`^the Edge screen renders$`, st.renders)
			sc.Step(`^two frames are rendered$`, st.twoFrames)
			sc.Step(`^it still says this machine is the only node on the Edge$`, st.saysOnlyNode)
			sc.Step(`^under THIS MACHINE it says "([^"]*)" and names "([^"]*)"$`, st.machineSays)
			sc.Step(`^under THIS MACHINE it says "([^"]*)" and "([^"]*)"$`, st.machineSays)
			sc.Step(`^under AUTHORITY it says Core and that enrolling a new node needs the network$`, st.authorityCore)
			sc.Step(`^it names "([^"]*)" as the way to form an Edge with no internet$`, st.names)
			sc.Step(`^it names "([^"]*)" as the way to admit another$`, st.names)
			sc.Step(`^it names "([^"]*)" as the way to read the last-known Edge$`, st.names)
			sc.Step(`^it names "([^"]*)"$`, st.names)
			sc.Step(`^it does not name "([^"]*)" as a next step$`, st.doesNotName)
			sc.Step(`^under DISCOVERY it says it is scanning this network and how often$`, st.discoveryScanning)
			sc.Step(`^under AUTHORITY it names "([^"]*)" and says enrolling needs no network beyond this LAN$`, st.authorityLocal)
			sc.Step(`^it says this machine IS the authority and that (\d+) machines may enroll against it$`, st.isTheAuthority)
			sc.Step(`^it says a login is needed to enroll under Core, and names "roger login"$`, st.loginNeeded)
			sc.Step(`^under DISCOVERY it says the last pass was (\d+)s ago and nothing answered$`, st.lastPassSays)
			sc.Step(`^under DISCOVERY it says (\d+) candidates were seen$`, st.candidatesSeen)
			sc.Step(`^under DISCOVERY it says "off" and names ROGERAI_EDGE_DISCOVERY$`, st.discoveryOffSays)
			sc.Step(`^it does not claim to be scanning$`, st.notScanning)
			sc.Step(`^under DISCOVERY it says it is scanning and that no pass has completed yet$`, st.noPassYet)
			sc.Step(`^no age is shown for a pass that never happened$`, st.noAgeShown)
			sc.Step(`^under ADD A NODE it names running RogerAI on another machine on this network, adopted with a$`, st.addNodeLAN)
			sc.Step(`^it names enrolling another machine against this Edge's authority$`, st.addNodeEnroll)
			sc.Step(`^it does not say this machine is the only node$`, st.notOnlyNode)
			sc.Step(`^it says the Edge host did not start this run and that the log says why$`, st.hostNotStartedSaid)
			sc.Step(`^it says the status could not be read, with the reason$`, st.statusUnread)
			sc.Step(`^it does not claim any authority or enrollment$`, st.noClaims)
			sc.Step(`^no line exceeds (\d+) columns$`, st.noLineExceeds)
			sc.Step(`^the three facts are still present$`, st.threeFactsPresent)
			sc.Step(`^the three fact labels are distinguishable as text alone$`, st.labelsAsText)
			sc.Step(`^each frame is internally consistent$`, st.framesConsistent)
			sc.Step(`^r re-reads the status$`, st.rReReads)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@tui",
			Paths: []string{"../../features/edge/empty_edge.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the empty-Edge TUI scenarios failed")
	}
}
