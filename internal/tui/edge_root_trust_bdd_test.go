package tui

// Executable spec: features/edge/enrollment_root_trust.feature (@tui) - the [3] EDGE wizard holds
// the authority's answer and asks for the enrollment code, and the adopting panel shows the code a
// device needs. The real model, the real renderer, the real key routing, over host closures (the
// same shape cmd/rogerai fills for real and tests against real authorities there).

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cucumber/godog"

	"rogerai.fm/roger/v7/internal/bddrun"
	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/store"
)

type rootTrustTUI struct {
	*onboardBDD
	// the adopting panel
	am       model
	code     string
	until    time.Time
	now      time.Time
	adopted  bool
	frames   map[int]string
	plainOut string
}

func (s *rootTrustTUI) resetAll() {
	s.onboardBDD.reset()
	s.am, s.code, s.adopted, s.frames, s.plainOut = model{}, "", false, map[int]string{}, ""
	s.now = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	s.until = s.now.Add(15 * time.Minute)
}

func (s *rootTrustTUI) authorityAllows() error { return s.authorityWillAccept() }

func (s *rootTrustTUI) stopsAtCodeStep() error {
	if w := s.wiz(); w == nil || w.step != setupCode {
		return fmt.Errorf("the wizard did not stop at the code step: %+v", s.wiz())
	}
	s.render(100)
	return contains(strings.Join(strings.Fields(s.out), " "), "enter the code shown on the authority")
}

func (s *rootTrustTUI) showsOfferedRoot() error {
	s.render(100)
	return contains(s.out, "3F7A-91C2-0B44-E8D1")
}

func (s *rootTrustTUI) locksAndDone() error {
	if w := s.wiz(); w == nil || w.step != setupDone {
		return fmt.Errorf("the wizard did not reach the done step: %+v", s.wiz())
	}
	if !s.fake.joinConfirmed {
		return fmt.Errorf("the host never confirmed the held join with the code")
	}
	s.render(100)
	return contains(s.out, "CARRIER LOCKED")
}

func (s *rootTrustTUI) askingForCode() error {
	if err := s.wizardJoinNamedAt("bench", "http://192.168.1.10:8791"); err != nil {
		return err
	}
	if err := s.authorityWillAccept(); err != nil {
		return err
	}
	return s.ownerConfirms()
}

func (s *rootTrustTUI) entersWrongCode() error { return s.entersCode("ZZZZZ-ZZZZZ") }

func (s *rootTrustTUI) saysNoMatchWithAttempts() error {
	s.render(100)
	return contains(strings.Join(strings.Fields(s.out), " "), "does not match the Edge that answered", "attempts left")
}

func (s *rootTrustTUI) staysAtCodeStep() error {
	if w := s.wiz(); w == nil || w.step != setupCode {
		return fmt.Errorf("the wizard left the code step: %+v", s.wiz())
	}
	return nil
}

func (s *rootTrustTUI) leavesWithEsc() error {
	s.esc()
	return nil
}

func (s *rootTrustTUI) nothingKept() error {
	if s.fake.joinConfirmed || s.fake.codeCalls != 0 {
		return fmt.Errorf("the held join was confirmed after the owner left")
	}
	if s.wiz() != nil {
		return fmt.Errorf("the wizard is still open")
	}
	return nil
}

func (s *rootTrustTUI) edgeScreenUnchanged() error {
	s.render(100)
	return contains(s.out, "not set up yet") // the simple empty state, as before the wizard opened
}

// ---- the adopting panel ----------------------------------------------------

func (s *rootTrustTUI) adoptFixture() {
	self := edge.SelfStatus{Root: edge.RootLocal, Authority: "hub", AuthorityLocal: true, AuthorityHere: true,
		AuthorityAddr: "http://192.168.1.9:8791", Enrolled: true, Name: "hub", Discovery: edge.DiscoveryScanning, IntervalS: 30}
	m := networkModel(s.t, self)
	cand := store.EdgeNode{ID: "n_pocket0000000000000000000000000000000000000", Name: "pocket", Kind: "mobile"}
	cands := []store.EdgeNode{cand}
	m.hooks.EdgeHousehold = func() []edge.Instance { return []edge.Instance{{Name: "hub", Agent: true}} }
	m.hooks.EdgeSelfInstance = func() string { return "hub" }
	m.hooks.EdgeCandidates = func() []store.EdgeNode { return cands }
	m.hooks.EdgeAdopt = func(id, name string) error {
		s.adopted, cands = true, nil
		s.code = "7KQ2M-9XH4P"
		return nil
	}
	m.hooks.EdgeAdopting = func() []store.EdgeNode {
		if !s.adopted {
			return nil
		}
		return []store.EdgeNode{{ID: cand.ID, Name: "pocket"}}
	}
	m.hooks.EdgeAdoptCode = func(id string) (string, time.Time, bool) {
		if s.code == "" || !s.now.Before(s.until) {
			return "", time.Time{}, false
		}
		return s.code, s.until, true
	}
	m.edgeClock = func() time.Time { return s.now }
	m.enterEdge()
	s.am = m
}

func (s *rootTrustTUI) phoneCandidate(name string) error {
	s.adoptFixture()
	return nil
}

func (s *rootTrustTUI) adoptsIt() error {
	s.am.edgeNearbyApprove()
	return nil
}

func (s *rootTrustTUI) confirmationShowsCode(name string) error {
	st := stripANSI(s.am.status)
	return contains(st, name, "7KQ2M-9XH4P", "15 min")
}

func (s *rootTrustTUI) panelKeepsCode(name string) error {
	out := stripANSI(s.am.edgeView(100))
	return contains(out, "ADOPTING", name, "7KQ2M-9XH4P", "min left")
}

func (s *rootTrustTUI) expiredPanelOffersFresh() error {
	s.now = s.now.Add(16 * time.Minute)
	out := strings.Join(strings.Fields(stripANSI(s.am.edgeView(100))), " ")
	if strings.Contains(out, "7KQ2M-9XH4P") {
		return fmt.Errorf("an expired code is still shown:\n%s", out)
	}
	return contains(out, "code expired", "roger edge invite --for pocket", "fresh code") // the fresh code is a bound invite (2026-10-12)
}

func (s *rootTrustTUI) adoptingPanelShowingCode() error {
	s.adoptFixture()
	s.am.edgeNearbyApprove()
	return nil
}

func (s *rootTrustTUI) rendersWidthsAndPlain() error {
	for _, w := range []int{120, 100, 80, 60} {
		s.frames[w] = stripANSI(s.am.edgeView(w))
	}
	s.t.Setenv("NO_COLOR", "1")
	s.plainOut = s.am.edgeView(80)
	return nil
}

func (s *rootTrustTUI) codeWholeAtEveryWidth() error {
	for w, out := range s.frames {
		if !strings.Contains(out, "7KQ2M-9XH4P") {
			return fmt.Errorf("at %d columns the code is not shown whole:\n%s", w, out)
		}
	}
	return nil
}

func (s *rootTrustTUI) noAnsiUnderNoColor() error {
	if strings.Contains(s.plainOut, "\x1b[") {
		return fmt.Errorf("NO_COLOR frame carries ANSI")
	}
	return contains(s.plainOut, "7KQ2M-9XH4P")
}

func (s *rootTrustTUI) doneShowsFullFingerprint() error {
	s.render(100)
	flat := strings.Join(strings.Fields(s.out), "")
	return contains(flat, "sha256:"+strings.Repeat("ab", 32))
}

func TestEnrollmentRootTrustFeatureTUI(t *testing.T) {
	st := &rootTrustTUI{onboardBDD: &onboardBDD{t: t}}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
				st.resetAll()
				return ctx, nil
			})
			// The Background's authority is the host's (cmd/rogerai runs it for real); on this screen
			// it is the host closures, so the Background establishes nothing more here.
			sc.Step(`^a local Edge authority "([^"]*)" whose root this machine has never held$`, func(string) error { return nil })
			sc.Step(`^the authority answers enrollment and claims on its LAN listener$`, func() error { return nil })
			sc.Step(`^the wizard is on the join path, named "([^"]*)", authority "([^"]*)"$`, st.wizardJoinNamedAt)
			sc.Step(`^that authority allows this machine$`, st.authorityAllows)
			sc.Step(`^the owner confirms$`, st.ownerConfirms)
			sc.Step(`^the handshake sweeps, and stops at a step that asks for the code shown on the authority$`, st.stopsAtCodeStep)
			sc.Step(`^it shows the root's short fingerprint the authority offered$`, st.showsOfferedRoot)
			sc.Step(`^the owner enters the code$`, st.entersTheCode)
			sc.Step(`^the sweep locks and the wizard reaches the done step$`, st.locksAndDone)
			sc.Step(`^the wizard is asking for the code$`, st.askingForCode)
			sc.Step(`^the owner enters a code that does not verify$`, st.entersWrongCode)
			sc.Step(`^it says the code does not match the Edge that answered, with attempts left$`, st.saysNoMatchWithAttempts)
			sc.Step(`^it stays at the code step$`, st.staysAtCodeStep)
			sc.Step(`^the owner leaves with esc$`, st.leavesWithEsc)
			sc.Step(`^nothing is kept and the machine is exactly as it was$`, st.nothingKept)
			sc.Step(`^the Edge screen is unchanged$`, st.edgeScreenUnchanged)
			sc.Step(`^a phone candidate "([^"]*)" on the \[3\] EDGE screen of the authority$`, st.phoneCandidate)
			sc.Step(`^the owner adopts it$`, st.adoptsIt)
			sc.Step(`^the confirmation names "([^"]*)" and shows its code and how long it is good for$`, st.confirmationShowsCode)
			sc.Step(`^the adopting panel for "([^"]*)" keeps showing the code until it is spent or expires$`, st.panelKeepsCode)
			sc.Step(`^once it expires the panel says so and offers a fresh code$`, st.expiredPanelOffersFresh)
			sc.Step(`^an adopting panel showing a code$`, st.adoptingPanelShowingCode)
			sc.Step(`^it renders at 120, 100, 80 and 60 columns and under NO_COLOR$`, st.rendersWidthsAndPlain)
			sc.Step(`^the code is shown whole, never wrapped or truncated, at every width$`, st.codeWholeAtEveryWidth)
			sc.Step(`^it carries no ANSI under NO_COLOR$`, st.noAnsiUnderNoColor)
			sc.Step(`^a new Edge was just created as "([^"]*)"$`, func(name string) error {
				if err := st.newEdgeJustCreated(name); err != nil {
					return err
				}
				st.fake.state.RootFingerprint = strings.Repeat("ab", 32) // the host reports its root
				return nil
			})
			sc.Step(`^the done step says another machine joins with the code that allow or adopt prints here$`, st.saysNextJoinsWithCode)
			sc.Step(`^it shows the root's full fingerprint for the pin path$`, st.doneShowsFullFingerprint)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@tui",
			Paths: []string{"../../features/edge/enrollment_root_trust.feature"},
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("the enrollment root trust TUI scenarios failed")
	}
}

var _ = tea.KeyMsg{}
