package tui

// Executable spec: features/edge/onboard.feature (@tui) - the interactive, game-like Edge
// onboarding wizard. The real model, the real renderer at fixed widths, the real key routing,
// and HOST CLOSURES over a fake enrollment backend that records what the wizard asked it to do
// (so we prove the wizard drives the host's enrollment, and never its own copy).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/cucumber/godog"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

// fakeSetupBackend is the host's enrollment, faked: it records every call and returns the
// error the scenario configured. It is exactly the shape cmd/rogerai/edgesetup.go fills for
// real, so a green test here is a green wiring there.
type fakeSetupBackend struct {
	defName string
	userKey string

	newErr, joinErr, selfErr error
	acceptAfterAllow         bool // a not-allowed join that succeeds once retried

	newCalls, joinCalls, selfCalls int
	lastName, lastAddr             string
	state                          EdgeSetupState
}

func (f *fakeSetupBackend) hooks() EdgeSetup {
	return EdgeSetup{
		DefaultName: func() string { return f.defName },
		UserKeyHex:  func() string { return f.userKey },
		NewEdge: func(name string) error {
			f.newCalls++
			f.lastName = name
			return f.newErr
		},
		Join: func(name, addr string) error {
			f.joinCalls++
			f.lastName, f.lastAddr = name, addr
			if f.acceptAfterAllow && f.joinCalls >= 2 {
				return nil
			}
			return f.joinErr
		},
		EnrollSelf: func() error {
			f.selfCalls++
			return f.selfErr
		},
	}
}

type onboardBDD struct {
	t       *testing.T
	now     time.Time
	fleet   *edge.Fleet
	status  edge.SelfStatus
	fake    *fakeSetupBackend
	noHooks bool
	m       model
	built   bool
	w       int
	out     string
	raw     string
	prev    string // a baseline frame, for "unchanged" assertions
	bandA   string
	bandB   string
}

func (s *onboardBDD) reset() {
	s.now = time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	s.fleet = edge.NewFleet(store.NewMem(), "acct-1")
	s.fleet.SetClock(func() time.Time { return s.now })
	s.status = edge.SelfStatus{Authority: "Core", Discovery: edge.DiscoveryScanning, IntervalS: 30}
	s.fake = &fakeSetupBackend{
		defName: "workstation",
		userKey: strings.Repeat("ab", 32), // 64 hex, the shape a real user key has
		state:   EdgeSetupState{},
	}
	s.noHooks, s.built, s.w, s.out, s.raw, s.prev = false, false, 0, "", "", ""
	s.bandA, s.bandB = "", ""
}

func (s *onboardBDD) build() {
	if s.built {
		return
	}
	hooks := Hooks{Station: "gentle-mongoose-93"}
	hooks.EdgeFleet = s.fleet
	hooks.EdgeCandidates = func() []store.EdgeNode { return nil }
	hooks.EdgeStatus = func() edge.SelfStatus { return s.status }
	if !s.noHooks {
		setup := s.fake.hooks()
		hooks.EdgeSetup = &setup
		hooks.EdgeSetupOf = func() EdgeSetupState { return s.fake.state }
	}
	var tm tea.Model = NewWithHooks("http://broker.local", "tester", nil, hooks)
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m := tm.(model)
	m.edgeClock = func() time.Time { return s.now }
	if m.mode != modeEdge {
		m.enterEdge()
	}
	s.m = m
	s.built = true
}

func (s *onboardBDD) render(w int) string {
	s.build()
	s.w = w
	s.raw = s.m.edgeView(w)
	s.out = stripANSI(s.raw)
	return s.out
}

func (s *onboardBDD) update(msg tea.Msg) tea.Cmd {
	s.build()
	tm, cmd := s.m.Update(msg)
	switch v := tm.(type) {
	case model:
		s.m = v
	case *model:
		s.m = *v
	}
	return cmd
}

func (s *onboardBDD) key(k tea.KeyMsg) tea.Cmd { return s.update(k) }
func (s *onboardBDD) runes(str string) tea.Cmd {
	return s.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(str)})
}
func (s *onboardBDD) enter() tea.Cmd { return s.update(tea.KeyMsg{Type: tea.KeyEnter}) }
func (s *onboardBDD) esc() tea.Cmd   { return s.update(tea.KeyMsg{Type: tea.KeyEsc}) }
func (s *onboardBDD) pressE() tea.Cmd {
	return s.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
}

func (s *onboardBDD) wiz() *edgeSetup { return s.m.edge.setup }

// execCmd runs a command to its message(s), flattening a Batch. A tea.Tick inside blocks for
// its (short, 90ms) duration; that is fine in a test and we simply drop the tick message.
func execCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if b, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range b {
			out = append(out, execCmd(c)...)
		}
		return out
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

// settle runs the dispatched op to its result and lands it, past the min-show hold (which we
// clear by advancing the clock), so the wizard reaches done/allow/failed at once.
func (s *onboardBDD) settle(cmd tea.Cmd) {
	s.now = s.now.Add(2 * time.Second) // past edgeSetupMinShow
	for _, msg := range execCmd(cmd) {
		if _, ok := msg.(edgeSetupDoneMsg); ok {
			s.update(msg)
		}
	}
}

// ---- givens: reaching a wizard state --------------------------------------

func (s *onboardBDD) notEnrolled() error {
	s.status = edge.SelfStatus{Authority: "Core", Discovery: edge.DiscoveryScanning, IntervalS: 30}
	s.fake.state = EdgeSetupState{}
	return nil
}

func (s *onboardBDD) enrolledAs(name string) error {
	s.status.Enrolled, s.status.Name = true, name
	s.fake.state = EdgeSetupState{Enrolled: true}
	return nil
}

func (s *onboardBDD) authorityHereNotEnrolled(where string) error {
	s.status.Authority, s.status.AuthorityLocal, s.status.AuthorityHere = where, true, true
	s.status.Enrolled = false
	s.fake.state = EdgeSetupState{AuthorityHere: true}
	return nil
}

func (s *onboardBDD) wizardAtChoice() error {
	if err := s.notEnrolled(); err != nil {
		return err
	}
	s.pressE()
	if w := s.wiz(); w == nil || w.step != setupChoose {
		return fmt.Errorf("wizard did not open at the choice: %+v", s.wiz())
	}
	return nil
}

func (s *onboardBDD) wizardAtNameNew() error {
	if err := s.wizardAtChoice(); err != nil {
		return err
	}
	s.enter() // default choice is "new"
	if w := s.wiz(); w == nil || w.step != setupName || w.join {
		return fmt.Errorf("not at the new-Edge name step: %+v", s.wiz())
	}
	return nil
}

func (s *onboardBDD) wizardAtName() error { return s.wizardAtNameNew() }

func (s *onboardBDD) wizardOnJoin() error {
	if err := s.wizardAtChoice(); err != nil {
		return err
	}
	s.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}) // down -> join
	s.enter()
	if w := s.wiz(); w == nil || !w.join || w.step != setupName {
		return fmt.Errorf("not on the join name step: %+v", s.wiz())
	}
	return nil
}

func (s *onboardBDD) wizardJoinNamedAt(name, addr string) error {
	if err := s.wizardOnJoin(); err != nil {
		return err
	}
	s.wiz().name = name
	s.enter() // name -> addr
	if w := s.wiz(); w == nil || w.step != setupAddr {
		return fmt.Errorf("did not advance to the address step: %+v", s.wiz())
	}
	s.wiz().addr = addr
	return nil
}

func (s *onboardBDD) wizardJoinAt(addr string) error { return s.wizardJoinNamedAt("bench", addr) }

func (s *onboardBDD) wizardOpen() error { return s.wizardAtChoice() }

// ---- givens: backend outcomes ---------------------------------------------

func (s *onboardBDD) designateWillFail() error {
	s.fake.newErr = errors.New("designate: home is read-only")
	return nil
}
func (s *onboardBDD) authorityWillAccept() error { s.fake.joinErr = nil; return nil }
func (s *onboardBDD) authorityNotAllowYet() error {
	s.fake.joinErr = ErrNotAllowedYet
	return nil
}
func (s *onboardBDD) authorityRefusesOther() error {
	s.fake.joinErr = errors.New("enroll refused: 403 signature does not verify")
	return nil
}
func (s *onboardBDD) authorityUnreachable() error {
	s.fake.joinErr = errors.New("dial tcp 192.168.1.10:8791: connect: connection refused")
	return nil
}

// ---- whens ----------------------------------------------------------------

func (s *onboardBDD) rendersAt(cols int) error { s.render(cols); return nil }
func (s *onboardBDD) rendersNow() error {
	if s.w == 0 {
		s.w = 100
	}
	s.render(s.w)
	return nil
}
func (s *onboardBDD) pressesE() error   { s.pressE(); return nil }
func (s *onboardBDD) pressesEsc() error { s.esc(); return nil }

// namesItAndConfirms replaces the name buffer, then confirms - dispatching the op and landing it.
func (s *onboardBDD) namesItAndConfirms(name string) error {
	if s.wiz() == nil {
		return errors.New("no wizard open")
	}
	s.wiz().name = name
	s.settle(s.enter())
	return nil
}

func (s *onboardBDD) ownerConfirms() error {
	s.settle(s.enter())
	return nil
}

func (s *onboardBDD) ownerRetries() error {
	s.fake.acceptAfterAllow = true
	s.settle(s.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}}))
	return nil
}

// startJoinInFlight dispatches a join but does NOT land it: the wizard is left at the running
// step with the handshake animating, so the animation scenarios can watch it.
func (s *onboardBDD) startJoinInFlight() error {
	if err := s.wizardJoinAt("http://192.168.1.10:8791"); err != nil {
		return err
	}
	s.enter() // dispatch; the returned batch is intentionally not executed
	if w := s.wiz(); w == nil || w.step != setupRunning || !w.anim {
		return fmt.Errorf("join is not in flight: %+v", s.wiz())
	}
	return nil
}

func (s *onboardBDD) handshakeInFlight() error { return s.startJoinInFlight() }

func (s *onboardBDD) newEdgeJustCreated(name string) error {
	if err := s.wizardAtNameNew(); err != nil {
		return err
	}
	s.fake.state.AuthorityAddr = "http://192.168.1.20:8791"
	return s.namesItAndConfirms(name)
}

func (s *onboardBDD) allowInstructionsShown(name string) error {
	if err := s.wizardJoinNamedAt(name, "http://192.168.1.10:8791"); err != nil {
		return err
	}
	if err := s.authorityNotAllowYet(); err != nil {
		return err
	}
	return s.ownerConfirms()
}

func (s *onboardBDD) sinceAllowed() error { s.fake.acceptAfterAllow = true; return nil }

func (s *onboardBDD) framesWhileInFlight() error {
	s.bandA = s.render(100)
	s.update(edgeSetupTickMsg{})
	s.update(edgeSetupTickMsg{})
	s.bandB = s.render(100)
	return nil
}

func (s *onboardBDD) enrollmentReturnsSuccess() error {
	s.settle(func() tea.Msg { return edgeSetupDoneMsg{kind: setupDone} })
	return nil
}

func (s *onboardBDD) enrollmentReturns() error { return s.enrollmentReturnsSuccess() }

func (s *onboardBDD) noColorSet() error { s.t.Setenv("NO_COLOR", "1"); return nil }
func (s *onboardBDD) compactOn() error  { s.build(); s.m.compact = true; return nil }

func (s *onboardBDD) joinInFlightThenSucceeds() error {
	if err := s.startJoinInFlight(); err != nil {
		return err
	}
	s.bandA = s.render(100) // the in-flight phase frame
	return s.enrollmentReturnsSuccess()
}

func (s *onboardBDD) rendersMany() error { return nil } // widths are checked in the Then

func (s *onboardBDD) completesNewEdge() error {
	if err := s.wizardAtNameNew(); err != nil {
		return err
	}
	return s.namesItAndConfirms("shed")
}

// ---- thens ----------------------------------------------------------------

func (s *onboardBDD) stillOnlyNode() error { return contains(s.out, "is the only node on the Edge") }
func (s *onboardBDD) stillNames(what string) error {
	return contains(strings.Join(strings.Fields(s.out), " "), what)
}
func (s *onboardBDD) invitesPressE() error {
	return contains(s.out, "press e", "set up this machine")
}
func (s *onboardBDD) wizardOpens() error {
	if s.wiz() == nil || !s.wiz().open {
		return fmt.Errorf("wizard did not open")
	}
	return nil
}
func (s *onboardBDD) asksNewOrJoin() error {
	s.render(100)
	return contains(s.out, "start a new Edge", "join an existing Edge")
}
func (s *onboardBDD) railShown() error {
	s.render(100)
	return contains(s.out, "mode", "name", "handshake", "on air")
}
func (s *onboardBDD) wizardDoesNotOpen() error {
	if s.wiz() != nil {
		return fmt.Errorf("wizard opened when it should not have: %+v", s.wiz())
	}
	return nil
}
func (s *onboardBDD) screenUnchanged() error {
	now := s.render(100)
	if s.prev != "" && now != s.prev {
		return fmt.Errorf("screen changed:\n--- was ---\n%s\n--- now ---\n%s", s.prev, now)
	}
	return nil
}
func (s *onboardBDD) offersFinish() error {
	return contains(s.out, "finish setting up", "enrolling this machine")
}
func (s *onboardBDD) opensAtEnrollThisMachine() error {
	if w := s.wiz(); w == nil || !w.finish || w.step != setupName {
		return fmt.Errorf("did not open at the finish/enroll step: %+v", s.wiz())
	}
	if s.wiz().step == setupChoose {
		return fmt.Errorf("opened at the choice, not the finish step")
	}
	return nil
}
func (s *onboardBDD) pathNew() error {
	s.render(100)
	return contains(s.out, "start a new Edge - this machine becomes the authority")
}
func (s *onboardBDD) pathJoin() error {
	s.render(100)
	return contains(s.out, "join an existing Edge")
}
func (s *onboardBDD) defaultSelected() error {
	if s.wiz().choice != 0 {
		return fmt.Errorf("no path selected by default")
	}
	return nil
}
func (s *onboardBDD) newPathExplains() error {
	s.render(100)
	return contains(s.out, "needs no internet", "roots the fleet here")
}
func (s *onboardBDD) joinPathExplains() error {
	s.render(100)
	return contains(s.out, "authority's address", "allowed on it")
}
func (s *onboardBDD) wizardCloses() error {
	if s.wiz() != nil {
		return fmt.Errorf("wizard did not close")
	}
	return nil
}
func (s *onboardBDD) edgeShownUnchanged() error {
	s.render(100)
	return contains(s.out, "is the only node on the Edge")
}
func (s *onboardBDD) defaultOffered() error {
	s.render(100)
	return contains(s.out, s.fake.defName)
}
func (s *onboardBDD) acceptOrType() error {
	if s.wiz().name != s.fake.defName {
		return fmt.Errorf("name field is not the default: %q", s.wiz().name)
	}
	return nil
}
func (s *onboardBDD) saysWhyStays() error {
	s.wiz().name = "not a valid name!!"
	s.enter()
	s.render(100)
	if s.wiz().step != setupName {
		return fmt.Errorf("advanced past an invalid name")
	}
	if s.wiz().err == "" {
		return fmt.Errorf("no reason shown for the invalid name")
	}
	if s.fake.newCalls != 0 {
		return fmt.Errorf("dispatched an enroll for an invalid name")
	}
	return nil
}
func (s *onboardBDD) canCorrect() error {
	if s.wiz().step != setupName {
		return fmt.Errorf("owner cannot correct in place")
	}
	return nil
}
func (s *onboardBDD) backToChoice() error {
	if w := s.wiz(); w == nil || w.step != setupChoose {
		return fmt.Errorf("esc did not return to the choice: %+v", s.wiz())
	}
	return nil
}
func (s *onboardBDD) secondEscLeaves() error {
	s.esc()
	return s.wizardCloses()
}
func (s *onboardBDD) handshakeDesignatesEnrolls() error {
	if s.fake.newCalls != 1 {
		return fmt.Errorf("the host's designate-and-enroll was not called once: %d", s.fake.newCalls)
	}
	return nil
}
func (s *onboardBDD) noNetworkNeeded() error {
	if s.fake.lastAddr != "" {
		return fmt.Errorf("a new Edge used a network address: %q", s.fake.lastAddr)
	}
	return nil
}
func (s *onboardBDD) reachesDoneNaming() error {
	s.render(100)
	if s.wiz().step != setupDone {
		return fmt.Errorf("not at the done step: %d", s.wiz().step)
	}
	return contains(s.out, "on air", "on your Edge")
}
func (s *onboardBDD) reachesDone() error {
	s.render(100)
	if s.wiz().step != setupDone {
		return fmt.Errorf("not at the done step: %d", s.wiz().step)
	}
	return nil
}
func (s *onboardBDD) doneNamesAddressAndCommand() error {
	s.render(100)
	return contains(s.out, "192.168.1.20:8791", "roger edge enroll")
}
func (s *onboardBDD) doneSaysLocal() error {
	return contains(s.out, "LOCAL", "no internet")
}
func (s *onboardBDD) showsFailureOwnWords() error {
	s.render(100)
	if s.wiz().step != setupFailed {
		return fmt.Errorf("not at the failed step: %d", s.wiz().step)
	}
	// The backend's OWN words, verbatim - never reworded, never flattened to a generic "no".
	return contains(s.out, s.wiz().err)
}
func (s *onboardBDD) offersTryAgain() error {
	if s.wiz().step != setupFailed && s.wiz().step != setupAllow {
		return fmt.Errorf("no retry offered from step %d", s.wiz().step)
	}
	return contains(s.out, "try again")
}
func (s *onboardBDD) stillNotEnrolledNoRoot() error {
	// The fake never returned success, so nothing on the machine changed: the wizard is the
	// only state, and it is at the failure. (The real backend's leave-as-was is proven in
	// enrollment.feature; here we prove the wizard never claimed otherwise.)
	if s.wiz().step == setupDone {
		return fmt.Errorf("the wizard reached done on a failed enroll")
	}
	return nil
}
func (s *onboardBDD) asksNameThisMachine() error {
	s.render(100)
	return contains(s.out, "name for THIS machine")
}
func (s *onboardBDD) asksAddress() error {
	s.wiz().name = "bench"
	s.enter()
	s.render(100)
	return contains(s.out, "authority", "http://")
}
func (s *onboardBDD) enrolledAgainstAuthority() error {
	if s.fake.joinCalls < 1 {
		return fmt.Errorf("join was not dispatched")
	}
	if s.fake.lastAddr != "http://192.168.1.10:8791" {
		return fmt.Errorf("join used the wrong address: %q", s.fake.lastAddr)
	}
	return nil
}
func (s *onboardBDD) saysNotAllowedYet() error {
	s.render(100)
	if s.wiz().step != setupAllow {
		return fmt.Errorf("not at the allow step: %d", s.wiz().step)
	}
	return contains(s.out, "not allowed on that Edge yet")
}
func (s *onboardBDD) showsUserKey() error {
	return contains(s.out, s.fake.userKey[:16]) // clipped on narrow, but its head shows
}
func (s *onboardBDD) showsAllowCommand() error {
	return contains(s.out, "roger edge authority allow")
}
func (s *onboardBDD) offersRetryWhenDone() error {
	return contains(s.out, "retry")
}
func (s *onboardBDD) doesNotShowAllow() error {
	if strings.Contains(s.out, "roger edge authority allow") {
		return fmt.Errorf("the allow instructions were shown for a non-allow refusal")
	}
	return nil
}
func (s *onboardBDD) saysUnreachableWithAddr() error {
	s.render(100)
	if s.wiz().step != setupFailed {
		return fmt.Errorf("not at the failed step: %d", s.wiz().step)
	}
	return contains(s.out, "192.168.1.10:8791")
}
func (s *onboardBDD) sweepMoves() error {
	if s.bandA == "" || s.bandB == "" {
		return fmt.Errorf("no frames captured")
	}
	a, b := s.handshakeLine(s.bandA), s.handshakeLine(s.bandB)
	if a == "" {
		return fmt.Errorf("no sweep band in frame A:\n%s", s.bandA)
	}
	if a == b {
		return fmt.Errorf("the sweep did not move between frames: %q", a)
	}
	return nil
}
func (s *onboardBDD) handshakeLine(frame string) string {
	// The animated channel rides on the tower row ("╱█╲ …carrier… ╱█╲"); the banner's static
	// broadcast rings must not be mistaken for it, so key off the tower glyph the scene alone
	// draws.
	for _, ln := range strings.Split(frame, "\n") {
		if strings.Contains(ln, "╱█╲") {
			return strings.TrimRight(ln, " ")
		}
	}
	return ""
}
func (s *onboardBDD) sweepLocksResolves() error {
	s.render(100)
	if s.wiz().step != setupDone {
		return fmt.Errorf("did not resolve to done on success: %d", s.wiz().step)
	}
	return contains(s.out, "on air")
}
func (s *onboardBDD) pulsesUntouched() error {
	if len(s.m.edge.pulses) != 0 {
		return fmt.Errorf("the handshake touched the fleet's pulse loop: %d pulses", len(s.m.edge.pulses))
	}
	return nil
}
func (s *onboardBDD) loopStopsWhenEnded() error {
	s.settle(func() tea.Msg { return edgeSetupDoneMsg{kind: setupDone} })
	_, cmd := s.m.onEdgeSetupTick()
	if cmd != nil {
		return fmt.Errorf("the handshake loop kept ticking after it ended")
	}
	return nil
}
func (s *onboardBDD) animStopsNextFrame() error {
	if s.wiz().anim {
		return fmt.Errorf("the animation is still marked running after the op returned")
	}
	_, cmd := s.m.onEdgeSetupTick()
	if cmd != nil {
		return fmt.Errorf("a tick was rescheduled with nothing in flight")
	}
	return nil
}
func (s *onboardBDD) nothingKeepsTicking() error { return s.animStopsNextFrame() }
func (s *onboardBDD) phasesAsPlainLines() error {
	if !strings.Contains(s.bandA, "contacting the authority") {
		return fmt.Errorf("the in-flight phase was not stated plainly:\n%s", s.bandA)
	}
	s.render(100)
	return contains(s.out, "on air")
}
func (s *onboardBDD) noAnsiInFrame() error {
	if strings.Contains(s.raw, "\x1b[") {
		return fmt.Errorf("the frame carries ANSI under NO_COLOR")
	}
	return nil
}
func (s *onboardBDD) phaseStatedNoSweep() error {
	out := s.render(100)
	if !strings.Contains(out, "HANDSHAKE") {
		return fmt.Errorf("the phase is not stated under reduced motion:\n%s", out)
	}
	// The animated tower scene (its ╱█╲ towers and its ◈ carrier packet) must be absent; the
	// banner's static broadcast rings are decoration, not the sweep, so they are allowed.
	if strings.Contains(out, "╱█╲") || strings.Contains(out, "◈)") {
		return fmt.Errorf("a travelling sweep was drawn under reduced motion")
	}
	return nil
}
func (s *onboardBDD) noLineExceedsAny() error {
	for _, cols := range []int{120, 100, 80, 60} {
		out := s.render(cols)
		for i, ln := range strings.Split(out, "\n") {
			if lipgloss.Width(ln) > cols {
				return fmt.Errorf("line %d is %d wide at %d cols: %q", i, lipgloss.Width(ln), cols, ln)
			}
		}
	}
	return nil
}
func (s *onboardBDD) readableAtEach() error {
	// On the join path's name step: the field and the hint line both survive the narrowest width.
	out := s.render(60)
	return contains(out, "name", "esc")
}
func (s *onboardBDD) calledHostDesignateEnroll() error {
	if s.fake.newCalls != 1 {
		return fmt.Errorf("the wizard did not call the host's designate-and-enroll once: %d", s.fake.newCalls)
	}
	return nil
}
func (s *onboardBDD) noHooksToldUnavailable() error {
	// A second machine whose host wired no setup hooks: pressing e half-does nothing and
	// says so, rather than pretending.
	s2 := &onboardBDD{t: s.t}
	s2.reset()
	s2.noHooks = true
	s2.notEnrolled()
	s2.pressE()
	if s2.wiz() != nil {
		return fmt.Errorf("a wizard opened with no hooks wired")
	}
	if !strings.Contains(stripANSI(s2.m.status), "cannot set up") &&
		!strings.Contains(stripANSI(s2.m.status), "unavailable") {
		return fmt.Errorf("no unavailable message was shown: %q", s2.m.status)
	}
	return nil
}
func (s *onboardBDD) nothingUntilConfirm() error {
	if s.fake.newCalls != 0 || s.fake.joinCalls != 0 || s.fake.selfCalls != 0 {
		return fmt.Errorf("something was enrolled before the owner confirmed")
	}
	return nil
}
func (s *onboardBDD) leavingChangesNothing() error {
	s.esc()
	s.esc()
	if s.fake.newCalls != 0 || s.fake.joinCalls != 0 || s.fake.selfCalls != 0 {
		return fmt.Errorf("leaving the wizard changed the machine")
	}
	return nil
}

// baseline snaps the current empty frame, for the "unchanged" assertions.
func (s *onboardBDD) snapBaseline() { s.build(); s.prev = s.render(100) }

func TestOnboardFeatureTUI(t *testing.T) {
	st := &onboardBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(); return c, nil })

			// givens
			sc.Step(`^a machine that is not enrolled, rooted at Core, scanning its network$`, st.notEnrolled)
			sc.Step(`^a machine that is not enrolled$`, st.notEnrolled)
			sc.Step(`^this machine is enrolled as "([^"]*)"$`, func(n string) error {
				if err := st.enrolledAs(n); err != nil {
					return err
				}
				st.snapBaseline()
				return nil
			})
			sc.Step(`^this machine is the local authority "([^"]*)" but has not enrolled itself$`, st.authorityHereNotEnrolled)
			sc.Step(`^the wizard is open at the choice$`, st.wizardAtChoice)
			sc.Step(`^the wizard is open at the name step$`, st.wizardAtName)
			sc.Step(`^the wizard is at the name step$`, st.wizardAtName)
			sc.Step(`^the wizard is at the name step, having chosen to start a new Edge$`, st.wizardAtNameNew)
			sc.Step(`^the wizard is at the new-Edge name step$`, st.wizardAtNameNew)
			sc.Step(`^the wizard is on the join path$`, st.wizardOnJoin)
			sc.Step(`^the wizard is on the join path, named "([^"]*)", authority "([^"]*)"$`, st.wizardJoinNamedAt)
			sc.Step(`^the wizard is on the join path, authority "([^"]*)"$`, st.wizardJoinAt)
			sc.Step(`^the wizard is open$`, st.wizardOpen)
			sc.Step(`^a new Edge was just created as "([^"]*)"$`, st.newEdgeJustCreated)
			sc.Step(`^the wizard is showing the allow instructions for "([^"]*)"$`, st.allowInstructionsShown)
			sc.Step(`^a join is in flight$`, st.startJoinInFlight)
			sc.Step(`^a handshake is in flight$`, st.handshakeInFlight)
			sc.Step(`^a join is in flight, then succeeds$`, st.joinInFlightThenSucceeds)
			sc.Step(`^a machine whose host wires the enrollment hooks$`, st.notEnrolled)

			// backend outcomes
			sc.Step(`^designating the authority will fail$`, st.designateWillFail)
			sc.Step(`^that authority will accept this machine$`, st.authorityWillAccept)
			sc.Step(`^that authority does not yet allow this machine$`, st.authorityNotAllowYet)
			sc.Step(`^that authority refuses for another reason$`, st.authorityRefusesOther)
			sc.Step(`^that authority cannot be reached$`, st.authorityUnreachable)
			sc.Step(`^the owner has since allowed this machine on the authority$`, st.sinceAllowed)

			// whens
			sc.Step(`^the Edge screen renders at (\d+) columns$`, st.rendersAt)
			sc.Step(`^the owner presses e on the Edge screen$`, st.pressesE)
			sc.Step(`^the owner presses e$`, st.pressesE)
			sc.Step(`^the owner presses esc$`, st.pressesEsc)
			sc.Step(`^the owner enters "([^"]*)"$`, func(string) error { return nil }) // asserted in the Then
			sc.Step(`^the owner names it "([^"]*)" and confirms$`, st.namesItAndConfirms)
			sc.Step(`^the owner confirms$`, st.ownerConfirms)
			sc.Step(`^the owner retries$`, st.ownerRetries)
			sc.Step(`^frames render while it is in flight$`, st.framesWhileInFlight)
			sc.Step(`^the enrollment returns success$`, st.enrollmentReturnsSuccess)
			sc.Step(`^the enrollment returns$`, st.enrollmentReturns)
			sc.Step(`^the wizard renders$`, st.rendersNow)
			sc.Step(`^the wizard completes a new Edge$`, st.completesNewEdge)
			sc.Step(`^it renders at 120, 100, 80 and 60 columns$`, st.rendersMany)
			sc.Step(`^NO_COLOR is set$`, st.noColorSet)
			sc.Step(`^the compact mode is on$`, st.compactOn)

			// thens
			sc.Step(`^it still says this machine is the only node on the Edge$`, st.stillOnlyNode)
			sc.Step(`^it still names "([^"]*)"$`, st.stillNames)
			sc.Step(`^it also invites the owner to press e to set this up interactively$`, st.invitesPressE)
			sc.Step(`^the setup wizard opens$`, st.wizardOpens)
			sc.Step(`^it asks whether to start a new Edge or join an existing one$`, st.asksNewOrJoin)
			sc.Step(`^a step rail shows where the owner is in the flow$`, st.railShown)
			sc.Step(`^the wizard does not open$`, st.wizardDoesNotOpen)
			sc.Step(`^the screen is unchanged$`, st.screenUnchanged)
			sc.Step(`^it offers to finish setting up by enrolling this machine$`, st.offersFinish)
			sc.Step(`^the wizard opens straight at enrolling this machine, not at the choice$`, st.opensAtEnrollThisMachine)
			sc.Step(`^one path is "start a new Edge - this machine becomes the authority"$`, st.pathNew)
			sc.Step(`^the other is "join an existing Edge"$`, st.pathJoin)
			sc.Step(`^one is selected by default so enter alone makes progress$`, st.defaultSelected)
			sc.Step(`^the new-Edge path says it needs no internet and roots the fleet here$`, st.newPathExplains)
			sc.Step(`^the join path says it needs the authority's address and to be allowed on it$`, st.joinPathExplains)
			sc.Step(`^the wizard closes$`, st.wizardCloses)
			sc.Step(`^the Edge screen is shown, unchanged$`, st.edgeShownUnchanged)
			sc.Step(`^a default name is offered, not a blank field$`, st.defaultOffered)
			sc.Step(`^the owner can accept it with enter or type their own$`, st.acceptOrType)
			sc.Step(`^the wizard says why in a line, and does not advance$`, st.saysWhyStays)
			sc.Step(`^the owner can correct it without leaving the step$`, st.canCorrect)
			sc.Step(`^it returns to the choice, keeping what was chosen$`, st.backToChoice)
			sc.Step(`^a second esc leaves the wizard$`, st.secondEscLeaves)
			sc.Step(`^the handshake runs and this machine is designated the authority and enrolled$`, st.handshakeDesignatesEnrolls)
			sc.Step(`^no network was needed$`, st.noNetworkNeeded)
			sc.Step(`^the wizard reaches the done step naming this machine on its Edge$`, st.reachesDoneNaming)
			sc.Step(`^the done step names this machine's address and the command another machine runs to join$`, st.doneNamesAddressAndCommand)
			sc.Step(`^it says the mode is LOCAL, formed with no internet$`, st.doneSaysLocal)
			sc.Step(`^the wizard shows the failure in the backend's own words$`, st.showsFailureOwnWords)
			sc.Step(`^it offers to try again$`, st.offersTryAgain)
			sc.Step(`^this machine is still not enrolled and roots no Edge$`, st.stillNotEnrolledNoRoot)
			sc.Step(`^it asks for a name for this machine$`, st.asksNameThisMachine)
			sc.Step(`^it asks for the authority's address, shaped like http://host:port$`, st.asksAddress)
			sc.Step(`^the handshake runs and this machine is enrolled against that authority$`, st.enrolledAgainstAuthority)
			sc.Step(`^the wizard reaches the done step$`, st.reachesDone)
			sc.Step(`^the wizard says this machine is not allowed on that Edge yet$`, st.saysNotAllowedYet)
			sc.Step(`^it shows this machine's user key$`, st.showsUserKey)
			sc.Step(`^it shows the exact command to run ON THE AUTHORITY: roger edge authority allow <this key>$`, st.showsAllowCommand)
			sc.Step(`^it offers to retry once that is done$`, st.offersRetryWhenDone)
			sc.Step(`^the handshake runs and this machine is enrolled$`, st.reachesDone)
			sc.Step(`^the wizard shows the refusal in the authority's own words$`, st.showsFailureOwnWords)
			sc.Step(`^it does NOT show the allow-this-key instructions$`, st.doesNotShowAllow)
			sc.Step(`^the wizard says the authority could not be reached, with the address$`, st.saysUnreachableWithAddr)
			sc.Step(`^a carrier sweep moves across the band$`, st.sweepMoves)
			sc.Step(`^the sweep locks and the machine's place on the Edge resolves in$`, st.sweepLocksResolves)
			sc.Step(`^the fleet graph's pulses are untouched$`, st.pulsesUntouched)
			sc.Step(`^when the handshake ends its loop stops, so a still fleet stays still$`, st.loopStopsWhenEnded)
			sc.Step(`^the animation stops on the next frame$`, st.animStopsNextFrame)
			sc.Step(`^nothing keeps ticking once there is nothing in flight$`, st.nothingKeepsTicking)
			sc.Step(`^it shows the phases as plain lines: contacting the authority, then enrolled$`, st.phasesAsPlainLines)
			sc.Step(`^the frame carries no ANSI$`, st.noAnsiInFrame)
			sc.Step(`^the phase is stated without a travelling sweep$`, st.phaseStatedNoSweep)
			sc.Step(`^no line exceeds the width at any of them$`, st.noLineExceedsAny)
			sc.Step(`^the choice, the fields and the hint line are readable at each$`, st.readableAtEach)
			sc.Step(`^it called the host's designate-and-enroll, the same the CLI calls$`, st.calledHostDesignateEnroll)
			sc.Step(`^a machine whose host wired no such hooks is told setup is unavailable here, rather than half-doing it$`, st.noHooksToldUnavailable)
			sc.Step(`^nothing is designated or enrolled until the owner confirms a step$`, st.nothingUntilConfirm)
			sc.Step(`^leaving the wizard before confirming changes nothing on this machine$`, st.leavingChangesNothing)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@tui",
			Paths: []string{"../../features/edge/onboard.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the onboarding TUI scenarios failed")
	}
}
