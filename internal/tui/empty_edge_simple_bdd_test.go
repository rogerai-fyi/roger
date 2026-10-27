package tui

// Executable spec: features/edge/empty_edge_simple.feature (@tui) - what [3] EDGE shows on a
// machine that is in no Edge yet, and the two ways in (e, j). The real model and renderer at
// fixed widths, a real (empty) fleet, a SelfStatus filled the way the host fills it, the real
// setup wizard driven through its backend seam, and the real j prompt driven through the join
// seam (the parse it checks is the real invite parser).

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/cucumber/godog"
	"github.com/muesli/termenv"

	"rogerai.fm/roger/v7/internal/bddrun"
	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/glyphs"
	"rogerai.fm/roger/v7/internal/joincode"
	"rogerai.fm/roger/v7/internal/joininvite"
	"rogerai.fm/roger/v7/internal/look"
	"rogerai.fm/roger/v7/internal/store"
)

// fakeJoinPrompt is the j prompt's backend seam, recording what the TUI hands it. It parses
// what it is given with the REAL invite parser, as the host does.
type fakeJoinPrompt struct {
	texts    []string
	kinds    []joininvite.Kind
	startErr error
	ask      bool
	phrase   string
	approve  chan struct{}
	waitErr  error
	cancels  int
	onJoined func()
}

func (f *fakeJoinPrompt) prompt(now func() time.Time) *edge.JoinPrompt {
	return &edge.JoinPrompt{Start: func(text string) (*edge.JoinAsk, error) {
		f.texts = append(f.texts, text)
		if in, err := joininvite.Parse(text); err == nil {
			f.kinds = append(f.kinds, in.Kind)
		}
		if f.startErr != nil {
			return nil, f.startErr
		}
		if f.ask {
			return &edge.JoinAsk{
				Phrase: f.phrase,
				Until:  now().Add(2 * time.Minute),
				Wait: func() error {
					<-f.approve
					if f.waitErr == nil {
						f.onJoined()
					}
					return f.waitErr
				},
				Cancel: func() { f.cancels++ },
			}, nil
		}
		f.onJoined()
		return nil, nil
	}}
}

type emptySimpleBDD struct {
	t       *testing.T
	now     time.Time
	fleet   *edge.Fleet
	status  edge.SelfStatus
	fake    *fakeSetupBackend
	join    *fakeJoinPrompt
	noSetup bool
	opened  map[string]int
	ended   map[string]int
	m       model
	built   bool
	raw     string
	out     string
	pending tea.Cmd // the wait dispatched for an ask by address, run only when the test approves
	before  int     // the map highlight before j
	invite  string
	compact bool
	cols    int
	frames  map[int]string // whole TUI frames by width (the golden scenario)
	nb      nearbyFixture  // §7: nearby devices, a real local authority behind i, the owner's hooks
}

const testFP = "3f7a91c20b44e8d1aa55bb66cc77dd88ee99ff00112233445566778899aabbcc"

func (s *emptySimpleBDD) reset() {
	s.now = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	s.fleet = edge.NewFleet(store.NewMem(), "acct-1")
	s.fleet.SetClock(func() time.Time { return s.now })
	s.status = edge.SelfStatus{Authority: "Core", Discovery: edge.DiscoveryScanning, ScansOnRequest: true, IntervalS: 30}
	s.fake = &fakeSetupBackend{defName: "workstation", userKey: strings.Repeat("ab", 32)}
	s.join = &fakeJoinPrompt{phrase: "amber-kettle-harbor", approve: make(chan struct{})}
	s.join.onJoined = func() {
		s.status.Enrolled, s.status.Name = true, "bench"
		s.status.Authority, s.status.AuthorityLocal = "shed", true
	}
	s.noSetup, s.built, s.raw, s.out, s.pending, s.before, s.invite = false, false, "", "", nil, 0, ""
	s.m, s.compact, s.frames = model{}, false, map[int]string{}
	s.opened, s.ended = map[string]int{}, map[string]int{}
	s.nb.reset(s.t)
	s.invite = joininvite.Invite{Kind: joininvite.Edge, Addrs: []string{"192.168.1.10:8791"}, FP: testFP, Code: joincode.Edge.NewCode()}.String()
	lipgloss.SetColorProfile(termenv.Ascii)
}

func (s *emptySimpleBDD) build() {
	if s.built {
		return
	}
	hooks := Hooks{Station: "gentle-mongoose-93"}
	hooks.EdgeFleet = s.fleet
	hooks.EdgeCandidates = func() []store.EdgeNode { return s.nb.cands }
	s.nb.wire(&hooks, s.fleet, func() time.Time { return s.now })
	hooks.EdgeStatus = func() edge.SelfStatus { return s.status }
	hooks.EdgeScan = func(kind string) func() {
		s.opened[kind]++
		return func() { s.ended[kind]++ }
	}
	if !s.noSetup {
		setup := s.fake.hooks()
		setup.EnrollCore = func(name string) error { s.fake.lastName = name; return errors.New("no Core in this test") }
		hooks.EdgeSetup = &setup
		hooks.EdgeSetupOf = func() EdgeSetupState { return s.fake.state }
		hooks.EdgeJoin = s.join.prompt(func() time.Time { return s.now })
	}
	// The fake's NewEdge makes this machine the local authority, as the real one does.
	inner := s.fake.hooks().NewEdge
	if hooks.EdgeSetup != nil {
		hooks.EdgeSetup.NewEdge = func(name string) error {
			if err := inner(name); err != nil {
				return err
			}
			s.fake.state = EdgeSetupState{Enrolled: true, AuthorityHere: true, AuthorityAddr: "http://192.168.1.5:8791"}
			s.status.Enrolled, s.status.Name = true, name
			s.status.Authority, s.status.AuthorityLocal, s.status.AuthorityHere = name, true, true
			s.status.AuthorityAddr = "http://192.168.1.5:8791"
			return nil
		}
	}
	var tm tea.Model = NewWithHooks("http://broker.local", "tester", nil, hooks)
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m := tm.(model)
	m.edgeClock = func() time.Time { return s.now }
	m.compact = s.compact
	s.m = m
	s.built = true
}

func (s *emptySimpleBDD) update(msg tea.Msg) tea.Cmd {
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

func (s *emptySimpleBDD) key(k string) tea.Cmd {
	switch k {
	case "enter":
		return s.update(tea.KeyMsg{Type: tea.KeyEnter})
	case "esc":
		return s.update(tea.KeyMsg{Type: tea.KeyEsc})
	case "down":
		return s.update(tea.KeyMsg{Type: tea.KeyDown})
	}
	return s.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
}

// land runs a dispatched command and feeds back the messages the model acts on, past the
// wizard's min-show hold. A join wait is kept aside (s.pending) until the test approves it.
func (s *emptySimpleBDD) land(cmd tea.Cmd) {
	s.now = s.now.Add(2 * time.Second)
	for _, msg := range execCmd(cmd) {
		switch msg.(type) {
		case edgeSetupDoneMsg, edgeSetupCodeMsg, edgeJoinStartedMsg, edgeJoinDoneMsg:
			next := s.update(msg)
			if _, ok := msg.(edgeJoinStartedMsg); ok && s.m.edge.join.ask != nil {
				s.pending = next
			}
		}
	}
}

func (s *emptySimpleBDD) open(w int) error {
	s.build()
	if s.m.mode != modeEdge {
		s.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	}
	if s.m.mode != modeEdge {
		return fmt.Errorf("3 did not open [3] EDGE (mode %v)", s.m.mode)
	}
	s.cols = w
	s.render(w)
	return nil
}

func (s *emptySimpleBDD) render(w int) string {
	s.build()
	s.raw = s.m.edgeView(w)
	s.out = stripANSI(s.raw)
	return s.out
}

func (s *emptySimpleBDD) frame() string { return stripANSI(s.m.View()) }

// joined is the frame with its line breaks folded, for a sentence that wraps.
func (s *emptySimpleBDD) joined() string {
	return strings.Join(strings.Fields(s.out), " ")
}

func (s *emptySimpleBDD) cap(k string) string { return look.KeyCap(k, lookMono()) }

// ---- givens ----------------------------------------------------------------

func (s *emptySimpleBDD) inNoEdge() error { return nil } // the Background: reset() is that machine
func (s *emptySimpleBDD) passesEvery(n int) error {
	s.status.IntervalS = int64(n)
	return nil
}
func (s *emptySimpleBDD) noPassYet() error { s.status.LastPass = 0; return nil }
func (s *emptySimpleBDD) passSaw(seen, inEdge int) error {
	s.status.LastPass = s.now.Unix()
	s.status.Nearby, s.status.NearbyInEdge = seen, inEdge
	return nil
}
func (s *emptySimpleBDD) discoveryOff() error {
	s.t.Setenv("ROGERAI_EDGE_DISCOVERY", "0")
	s.status.Discovery = edge.DiscoveryOff
	return nil
}
func (s *emptySimpleBDD) openedAndScanning() error {
	if err := s.open(100); err != nil {
		return err
	}
	if s.opened["edge"] != 1 || s.ended["edge"] != 0 {
		return fmt.Errorf("edge scan opened %d, ended %d; want 1 and 0", s.opened["edge"], s.ended["edge"])
	}
	return nil
}
func (s *emptySimpleBDD) setupUnavailable() error { s.noSetup = true; return nil }
func (s *emptySimpleBDD) pressedE() error {
	if err := s.open(100); err != nil {
		return err
	}
	s.key("e")
	s.render(100)
	return nil
}
func (s *emptySimpleBDD) pressedENotLoggedIn() error {
	if err := s.pressedE(); err != nil {
		return err
	}
	if s.m.loggedInState() {
		return fmt.Errorf("the test model is logged in")
	}
	return nil
}
func (s *emptySimpleBDD) noInternet() error { return nil } // the fake backend reaches nothing
func (s *emptySimpleBDD) finishedStartAs(name string) error {
	if err := s.pressedE(); err != nil {
		return err
	}
	s.key("enter") // authority: local
	s.m.edge.setup.name = name
	s.land(s.key("enter"))
	if w := s.m.edge.setup; w == nil || w.step != setupDone {
		return fmt.Errorf("the start did not reach done: %+v", s.m.edge.setup)
	}
	s.key("enter") // dismiss done
	s.render(100)
	return nil
}
func (s *emptySimpleBDD) enrolledWithTwo(name string) error {
	if err := s.enrolledAs(name); err != nil {
		return err
	}
	for _, n := range []string{"shed", "garage"} {
		_, err := s.fleet.Enroll(store.EdgeNode{ID: "node-" + n, Name: n, Kind: "host", Presence: "VERIFIED", LastSeen: s.now.Unix()})
		if err != nil {
			return err
		}
	}
	return nil
}
func (s *emptySimpleBDD) enrolledAs(name string) error {
	s.status.Enrolled, s.status.Name = true, name
	s.status.Authority, s.status.AuthorityLocal, s.status.AuthorityHere = name, true, true
	s.fake.state = EdgeSetupState{Enrolled: true, AuthorityHere: true}
	return nil
}
func (s *emptySimpleBDD) pressedJ() error {
	if err := s.open(100); err != nil {
		return err
	}
	s.key("j")
	s.render(100)
	return nil
}
func (s *emptySimpleBDD) askWaiting() error {
	if err := s.pressedJ(); err != nil {
		return err
	}
	s.join.ask = true
	s.key("192.168.1.10:8791")
	s.land(s.key("enter"))
	if s.m.edge.join.ask == nil {
		return fmt.Errorf("no ask is waiting: %+v", s.m.edge.join)
	}
	return nil
}

// ---- whens -----------------------------------------------------------------

func (s *emptySimpleBDD) opensAt(cols int) error { return s.open(cols) }
func (s *emptySimpleBDD) opens() error           { return s.open(100) }
func (s *emptySimpleBDD) passCompletes() error {
	s.status.LastPass = s.now.Unix()
	s.update(edgeStatusTickMsg{})
	s.render(100)
	return nil
}
func (s *emptySimpleBDD) presses(k string) error {
	if s.m.mode != modeEdge {
		if err := s.open(100); err != nil {
			return err
		}
	}
	s.key(k)
	s.render(100)
	return nil
}
func (s *emptySimpleBDD) pressesJOnEdge() error {
	if err := s.open(100); err != nil {
		return err
	}
	s.before = s.m.edge.netSel
	s.key("j")
	s.render(100)
	return nil
}
func (s *emptySimpleBDD) choosesRogerAI() error {
	s.key("down")
	s.key("enter")
	s.render(100)
	return nil
}
func (s *emptySimpleBDD) pressesEAndDefaults() error {
	if err := s.open(100); err != nil {
		return err
	}
	s.key("e")
	s.key("enter") // authority: local
	s.land(s.key("enter"))
	s.render(100)
	return nil
}
func (s *emptySimpleBDD) pastesInviteEnter() error {
	s.key(s.invite)
	s.land(s.key("enter"))
	s.render(100)
	return nil
}
func (s *emptySimpleBDD) pastesInvite() error {
	if err := s.pressedJ(); err != nil {
		return err
	}
	s.key(s.invite)
	s.render(100)
	return nil
}
func (s *emptySimpleBDD) typesAddressEnter() error {
	s.join.ask = true
	s.key("192.168.1.10:8791")
	s.land(s.key("enter"))
	s.render(100)
	return nil
}
func (s *emptySimpleBDD) ownerApproves() error {
	close(s.join.approve)
	s.land(s.pending)
	s.render(100)
	return nil
}
func (s *emptySimpleBDD) whatHappens(what string) error {
	says := map[string]string{
		"the invite was already used":            look.JoinSaysUsed,
		"the invite expired":                     look.JoinSaysExpired,
		"the address does not answer":            look.JoinSaysNoAnswer,
		"the invite is for a Tower, not an Edge": look.JoinSaysTower,
		"the authority's key changed":            look.JoinSaysOtherEdge,
	}
	switch what {
	case "the authority refused the ask", "the ask timed out":
		s.join.ask = true
		s.join.waitErr = errors.New(look.JoinSaysRefused)
		if what == "the ask timed out" {
			s.join.waitErr = errors.New(look.JoinSaysTimeout)
		}
		s.key("192.168.1.10:8791")
		s.land(s.key("enter"))
		close(s.join.approve)
		s.land(s.pending)
	default:
		msg, ok := says[what]
		if !ok {
			return fmt.Errorf("no outcome for %q", what)
		}
		s.join.startErr = errors.New(msg)
		text := s.invite
		if what == "the address does not answer" || what == "the authority's key changed" {
			text = "192.168.1.10:8791"
		}
		s.key(text)
		s.land(s.key("enter"))
	}
	s.render(100)
	return nil
}

// ---- thens -----------------------------------------------------------------

func (s *emptySimpleBDD) shows(a, b string) error { return contains(s.out, a, b) }
func (s *emptySimpleBDD) saysSentence() error {
	return contains(s.joined(), look.EmptyEdgeSay)
}
func (s *emptySimpleBDD) offers(key, label, what string) error {
	for _, ln := range strings.Split(s.out, "\n") {
		if strings.Contains(ln, s.cap(key)) && strings.Contains(ln, label) {
			return contains(s.joined(), what)
		}
	}
	return fmt.Errorf("no line offers %s %s:\n%s", s.cap(key), label, s.out)
}
func (s *emptySimpleBDD) doesNotSay(w string) error {
	if strings.Contains(s.out, w) {
		return fmt.Errorf("the empty Edge says %q:\n%s", w, s.out)
	}
	return nil
}
func (s *emptySimpleBDD) namesNone(a, b, c string) error {
	for _, w := range []string{a, b, c} {
		if err := s.doesNotSay(w); err != nil {
			return err
		}
	}
	return nil
}
func (s *emptySimpleBDD) noLampOrBanner() error {
	for _, w := range []string{"ON AIR", "◉ YOUR EDGE ◉", "R O G E R"} {
		if err := s.doesNotSay(w); err != nil {
			return err
		}
	}
	// The eyebrow row carries no lamp: ◉ appears only in the scan eye after a pass.
	for _, ln := range strings.Split(s.out, "\n") {
		if strings.Contains(ln, "ROGER · EDGE") && strings.Contains(ln, "◉") {
			return fmt.Errorf("the brand row wears a lamp: %q", ln)
		}
	}
	return nil
}
func (s *emptySimpleBDD) noAdopt() error {
	if f := s.frame(); strings.Contains(f, "a adopt") {
		return fmt.Errorf("the footer offers a adopt:\n%s", f)
	}
	return s.doesNotSay("adopt")
}
func (s *emptySimpleBDD) actionBar(a, b, c string) error {
	lines := strings.Split(strings.TrimRight(s.out, "\n"), "\n")
	bar := strings.Join(strings.Fields(lines[len(lines)-1]), " ")
	for _, w := range []string{a, b, c} {
		if !strings.Contains(bar, w) {
			return fmt.Errorf("the action bar %q lacks %q", bar, w)
		}
	}
	return nil
}
func (s *emptySimpleBDD) says(w string) error { return contains(s.out, w) }
func (s *emptySimpleBDD) scanLeadsWith(eye string) error {
	want := look.Fold(eye, lookMono())
	for _, ln := range strings.Split(s.out, "\n") {
		if strings.Contains(ln, "scanning") {
			if strings.HasPrefix(strings.TrimSpace(ln), want) {
				return nil
			}
			return fmt.Errorf("the scan line leads with %q, want %q", strings.TrimSpace(ln), want)
		}
	}
	return fmt.Errorf("no scan line:\n%s", s.out)
}
func (s *emptySimpleBDD) secondScanSays(w string) error {
	lines := strings.Split(s.out, "\n")
	for i, ln := range lines {
		if strings.Contains(ln, "scanning") && i+1 < len(lines) {
			return contains(strings.TrimSpace(lines[i+1]), w)
		}
	}
	return fmt.Errorf("no scan line:\n%s", s.out)
}
func (s *emptySimpleBDD) secondScanNoPass() error {
	return s.secondScanSays("no pass has completed yet")
}
func (s *emptySimpleBDD) neverSays(w string) error { return s.doesNotSay(w) }
func (s *emptySimpleBDD) scanOff() error {
	return contains(s.out, "scanning is off", "ROGERAI_EDGE_DISCOVERY=0")
}
func (s *emptySimpleBDD) ejStillOffered() error {
	if err := s.offers("e", look.EmptyEdgeStart, look.EmptyEdgeStartIs); err != nil {
		return err
	}
	return s.offers("j", look.EmptyEdgeJoin, look.EmptyEdgeJoinIs)
}
func (s *emptySimpleBDD) scanStops() error {
	if s.ended["edge"] != 1 {
		return fmt.Errorf("the edge scan ended %d time(s), want 1", s.ended["edge"])
	}
	return nil
}
func (s *emptySimpleBDD) setupOpensAt(title string) error {
	w := s.m.edge.setup
	if w == nil || !w.open || w.join || w.step != setupChoose {
		return fmt.Errorf("the setup is not open at the start step: %+v", w)
	}
	return contains(s.out, title)
}
func (s *emptySimpleBDD) statusNames(a, b string) error {
	if s.m.edge.setup != nil {
		return fmt.Errorf("the setup opened on a build without it")
	}
	return contains(stripANSI(s.m.status), a, b)
}
func (s *emptySimpleBDD) neverAsksStartOrJoin() error {
	for _, w := range []string{"start a new Edge", "join an existing Edge"} {
		if err := s.doesNotSay(w); err != nil {
			return err
		}
	}
	return nil
}
func (s *emptySimpleBDD) authorityLocalDefault(w string) error {
	if s.m.edge.setup == nil || s.m.edge.setup.choice != 0 {
		return fmt.Errorf("local is not the default choice: %+v", s.m.edge.setup)
	}
	for _, ln := range strings.Split(s.out, "\n") {
		if strings.Contains(ln, w) {
			if strings.Contains(ln, "▶") || strings.Contains(ln, ">") || strings.Contains(ln, "●") {
				return nil
			}
			return fmt.Errorf("the local line is not marked selected: %q", ln)
		}
	}
	return fmt.Errorf("%q is not offered:\n%s", w, s.out)
}
func (s *emptySimpleBDD) offersLine(w string) error { return contains(s.out, w) }
func (s *emptySimpleBDD) enterContinuesLocal() error {
	s.key("enter")
	if w := s.m.edge.setup; w == nil || w.step != setupName || w.core || w.join {
		return fmt.Errorf("enter did not continue to the name step with the local choice: %+v", s.m.edge.setup)
	}
	return nil
}
func (s *emptySimpleBDD) loginNeededInPlace() error {
	if w := s.m.edge.setup; w == nil || w.step != setupLogin {
		return fmt.Errorf("not at the login step: %+v", s.m.edge.setup)
	}
	if err := contains(s.out, "login"); err != nil {
		return err
	}
	if !strings.Contains(s.out, "⏎") && !strings.Contains(s.out, "enter") {
		return fmt.Errorf("the login is not offered in place:\n%s", s.out)
	}
	return nil
}
func (s *emptySimpleBDD) escBackToAuthority() error {
	s.key("esc")
	if w := s.m.edge.setup; w == nil || w.step != setupChoose {
		return fmt.Errorf("esc did not return to the authority step: %+v", s.m.edge.setup)
	}
	if s.fake.newCalls != 0 || s.fake.lastName != "" || s.m.mode != modeEdge {
		return fmt.Errorf("something changed: new=%d name=%q mode=%v", s.fake.newCalls, s.fake.lastName, s.m.mode)
	}
	return nil
}
func (s *emptySimpleBDD) becomesLocalAuthority() error {
	if s.fake.newCalls != 1 || !s.fake.state.Enrolled || !s.fake.state.AuthorityHere {
		return fmt.Errorf("new Edge calls %d, state %+v", s.fake.newCalls, s.fake.state)
	}
	return nil
}
func (s *emptySimpleBDD) noNetworkBeyondLAN() error {
	if s.fake.joinCalls != 0 || len(s.join.texts) != 0 {
		return fmt.Errorf("the network was contacted: join calls %d, prompts %v", s.fake.joinCalls, s.join.texts)
	}
	return nil
}
func (s *emptySimpleBDD) fullViewWith(name string) error {
	if err := s.doesNotSay(look.EmptyEdgeState); err != nil {
		return err
	}
	return contains(s.out, name, "YOUR EDGE NETWORK")
}
func (s *emptySimpleBDD) saysHowToAddNext() error {
	return contains(s.joined(), "roger edge invite", "roger join")
}
func (s *emptySimpleBDD) selectionMovedDown() error {
	if s.m.edge.netSel == s.before && !s.m.edge.netSelSet {
		return fmt.Errorf("j did not move the selection (netSel %d)", s.m.edge.netSel)
	}
	return nil
}
func (s *emptySimpleBDD) noJoinPrompt() error {
	if s.m.edge.join.open {
		return fmt.Errorf("a join prompt opened")
	}
	return s.doesNotSay(look.EmptyEdgePromptAsk)
}
func (s *emptySimpleBDD) promptAsks() error {
	if !s.m.edge.join.open {
		return fmt.Errorf("the join prompt is not open")
	}
	return contains(s.out, look.EmptyEdgePromptAsk)
}
func (s *emptySimpleBDD) joinsWithSameCheck() error {
	if len(s.join.texts) != 1 || s.join.texts[0] != s.invite {
		return fmt.Errorf("the prompt handed the backend %v, want the pasted invite", s.join.texts)
	}
	if len(s.join.kinds) != 1 || s.join.kinds[0] != joininvite.Edge {
		return fmt.Errorf("the invite did not parse as an Edge invite: %v", s.join.kinds)
	}
	return nil
}
func (s *emptySimpleBDD) fullViewReplaces() error {
	if s.m.edge.join.open {
		return fmt.Errorf("the join prompt is still open")
	}
	if !s.m.edge.status.Enrolled {
		return fmt.Errorf("the frame's status is not enrolled")
	}
	return s.doesNotSay(look.EmptyEdgeState)
}
func (s *emptySimpleBDD) phraseShown() error {
	return contains(s.out, s.join.phrase, look.EmptyEdgePhraseNote)
}

var countdownRe = regexp.MustCompile(`\b[0-9]:[0-5][0-9]\b`)

func (s *emptySimpleBDD) waitsWithCountdown() error {
	if s.m.edge.join.ask == nil {
		return fmt.Errorf("no ask is waiting")
	}
	if !countdownRe.MatchString(s.out) {
		return fmt.Errorf("no countdown on screen:\n%s", s.out)
	}
	if !strings.Contains(s.out, "esc cancel") {
		return fmt.Errorf("esc to cancel is not offered:\n%s", s.out)
	}
	return nil
}
func (s *emptySimpleBDD) memberFullView() error {
	if !s.status.Enrolled {
		return fmt.Errorf("the approval did not enroll this machine")
	}
	return s.fullViewReplaces()
}
func (s *emptySimpleBDD) promptSaysUnchanged(says string) error {
	if !s.m.edge.join.open {
		return fmt.Errorf("the prompt closed")
	}
	if err := contains(s.out, says); err != nil {
		return err
	}
	if s.status.Enrolled {
		return fmt.Errorf("this machine was enrolled by a failed join")
	}
	return contains(s.out, look.EmptyEdgeState)
}
func (s *emptySimpleBDD) codeNowhere() error {
	in, _ := joininvite.Parse(s.invite)
	if strings.Contains(stripANSI(s.m.status), in.Code) {
		return fmt.Errorf("the status line carries the invite's code: %q", stripANSI(s.m.status))
	}
	s.land(s.key("enter"))
	if strings.Contains(stripANSI(s.m.status), in.Code) || strings.Contains(s.render(100), in.Code) {
		return fmt.Errorf("the invite's code is echoed after the join")
	}
	return nil
}
func (s *emptySimpleBDD) fullViewUnchanged() error {
	if err := s.doesNotSay(look.EmptyEdgeState); err != nil {
		return err
	}
	return contains(s.out, "YOUR EDGE NETWORK")
}
func (s *emptySimpleBDD) ejExistingMeanings() error {
	s.key("e")
	if s.m.edge.setup != nil {
		return fmt.Errorf("e opened the setup on an enrolled machine")
	}
	s.key("j")
	if s.m.edge.join.open {
		return fmt.Errorf("j opened the join prompt on an enrolled machine")
	}
	return nil
}
func (s *emptySimpleBDD) fleetPalette() error {
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(termenv.Ascii)
	raw := s.m.edgeView(100)
	pal := map[look.RGB]bool{}
	for _, c := range look.Palette() {
		pal[c] = true
	}
	used := rgbIn(raw)
	if len(used) == 0 {
		return fmt.Errorf("the empty Edge sets no colors in truecolor")
	}
	for c := range used {
		if !pal[c] {
			return fmt.Errorf("the empty Edge wears %v, which is not in the Health/Fleet palette", c)
		}
	}
	if !used[look.Live] || !used[look.Muted] || !used[look.Paper] {
		return fmt.Errorf("the empty Edge lacks the one red, the muted eyebrow or paper: %v", used)
	}
	return nil
}

// ---- look, widths, windowshade, NO_COLOR, golden -------------------------

func (s *emptySimpleBDD) mutedEyebrowNoRule(brand string) error {
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(termenv.Ascii)
	raw := s.m.edgeView(100)
	for _, ln := range strings.Split(raw, "\n") {
		if strings.Contains(stripANSI(ln), brand) {
			if !rgbIn(ln)[look.Muted] {
				return fmt.Errorf("the eyebrow row is not muted: %q", ln)
			}
			continue
		}
		p := strings.TrimSpace(stripANSI(ln))
		if p != "" && strings.Trim(p, "═━─") == "" {
			return fmt.Errorf("a heavy rule is drawn: %q", p)
		}
	}
	if !strings.Contains(stripANSI(raw), brand) {
		return fmt.Errorf("no %q eyebrow", brand)
	}
	return nil
}

var sgrBG = regexp.MustCompile(`\x1b\[[0-9;]*?48;2;(\d+);(\d+);(\d+)`)

func (s *emptySimpleBDD) paperKeyCaps() error {
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(termenv.Ascii)
	raw := s.m.edgeView(100)
	caps := 0
	for _, ln := range strings.Split(raw, "\n") {
		p := stripANSI(ln)
		if strings.Contains(p, look.EmptyEdgeStart) || strings.Contains(p, look.EmptyEdgeJoin) {
			for _, m := range sgrBG.FindAllStringSubmatch(ln, -1) {
				if m[1]+";"+m[2]+";"+m[3] == fmt.Sprintf("%d;%d;%d", look.Paper.R, look.Paper.G, look.Paper.B) {
					caps++
				}
			}
		}
	}
	if caps < 2 {
		return fmt.Errorf("%d paper key caps on the e/j lines, want 2", caps)
	}
	return nil
}

func (s *emptySimpleBDD) noLineOverflows() error {
	for _, ln := range strings.Split(s.out, "\n") {
		if lipgloss.Width(ln) > s.width() {
			return fmt.Errorf("a line is %d wide at %d columns: %q", lipgloss.Width(ln), s.width(), ln)
		}
	}
	return nil
}

// width is the columns the last render used (recorded by open).
func (s *emptySimpleBDD) width() int {
	if s.cols == 0 {
		return 100
	}
	return s.cols
}

func (s *emptySimpleBDD) whatFolds(what string) error {
	eLine, jLine := "", ""
	for _, ln := range strings.Split(s.out, "\n") {
		if strings.Contains(ln, look.EmptyEdgeStart) && strings.Contains(ln, s.cap("e")) {
			eLine = ln
		}
		if strings.Contains(ln, look.EmptyEdgeJoin) && strings.Contains(ln, s.cap("j")) {
			jLine = ln
		}
	}
	if eLine == "" || jLine == "" {
		return fmt.Errorf("the e and j lines are not both whole:\n%s", s.out)
	}
	if err := contains(s.joined(), look.EmptyEdgeSay, look.EmptyEdgeStartIs, look.EmptyEdgeJoinIs); err != nil {
		return err
	}
	beside := strings.Contains(eLine, look.EmptyEdgeStartIs) && strings.Contains(jLine, look.EmptyEdgeJoinIs)
	switch what {
	case "nothing is cut":
		if !beside {
			return fmt.Errorf("the description column was dropped at %d columns", s.width())
		}
	default:
		if beside {
			return fmt.Errorf("the description column is still beside the labels at %d columns", s.width())
		}
	}
	return nil
}

func (s *emptySimpleBDD) compactOn() error { s.compact = true; return nil }
func (s *emptySimpleBDD) showsOneLine(w string) error {
	f := s.frame()
	if !strings.Contains(f, w) {
		return fmt.Errorf("the windowshade lacks %q:\n%s", w, f)
	}
	return nil
}
func (s *emptySimpleBDD) compactHeaderNamesEdge() error {
	head := stripANSI(s.m.compactHeader(100))
	if !strings.Contains(head, "[3] EDGE") || strings.Contains(head, "TUNE IN") {
		return fmt.Errorf("the compact header reads %q", head)
	}
	return nil
}

func (s *emptySimpleBDD) noColor() error { s.t.Setenv("NO_COLOR", "1"); return nil }
func (s *emptySimpleBDD) readsAsText() error {
	if strings.Contains(s.raw, "\x1b[") {
		return fmt.Errorf("the frame carries ANSI escapes under NO_COLOR")
	}
	if err := contains(s.out, "[e]", "[j]", look.EmptyEdgeStart, look.EmptyEdgeJoin, look.EmptyEdgeState, look.EmptyEdgeStartIs, look.EmptyEdgeJoinIs); err != nil {
		return err
	}
	return s.scanLeadsWith(look.EyeWait)
}
func (s *emptySimpleBDD) everyGlyphFolds() error {
	for _, ln := range strings.Split(s.out, "\n") {
		for _, r := range glyphs.FoldASCII(ln) {
			if r >= 128 {
				return fmt.Errorf("%q does not fold to ASCII: %q", string(r), ln)
			}
		}
	}
	return nil
}

func (s *emptySimpleBDD) renderedAt(a, b, c int) error {
	for _, w := range []int{a, b, c} {
		s.built = false
		s.m = model{}
		s.build()
		s.update(tea.WindowSizeMsg{Width: w, Height: 40})
		if err := s.open(w); err != nil {
			return err
		}
		s.frames[w] = s.frame()
	}
	return nil
}
func (s *emptySimpleBDD) framesHold() error {
	for w, f := range s.frames {
		if err := contains(strings.Join(strings.Fields(f), " "), look.EmptyEdgeState, look.EmptyEdgeSay, look.EmptyEdgeStart, look.EmptyEdgeJoin, s.cap("e"), s.cap("j")); err != nil {
			return fmt.Errorf("at %d columns: %v", w, err)
		}
		for _, ln := range strings.Split(f, "\n") {
			if lipgloss.Width(ln) > w {
				return fmt.Errorf("at %d columns a line is %d wide: %q", w, lipgloss.Width(ln), ln)
			}
		}
	}
	return nil
}
func (s *emptySimpleBDD) matchesGolden(w int) error {
	if s.frames[w] == "" {
		return fmt.Errorf("no %d-column frame was rendered", w)
	}
	// The 100-column frame is the spec's; the 80 and 60 ones are pinned beside it.
	for cols, f := range s.frames {
		if err := goldenFile(s.t, fmt.Sprintf("empty_edge_%d.golden", cols), f); err != nil {
			return err
		}
	}
	return nil
}

func TestEmptyEdgeSimpleFeatureTUI(t *testing.T) {
	q := quiet
	quiet = true
	t.Cleanup(func() { quiet = q; lipgloss.SetColorProfile(termenv.Ascii) })
	st := &emptySimpleBDD{t: t}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			sc.Before(func(c context.Context, _ *godog.Scenario) (context.Context, error) { st.reset(); return c, nil })
			sc.Given(`^this machine is in no Edge \(not enrolled and not an authority\)$`, st.inNoEdge)
			sc.Given(`^discovery passes every (\d+) seconds$`, st.passesEvery)
			sc.Given(`^no scan pass has completed yet$`, st.noPassYet)
			sc.Given(`^a completed pass saw (\d+) other roger machines, (\d+) of them in an Edge$`, st.passSaw)
			sc.Given(`^discovery is switched off with ROGERAI_EDGE_DISCOVERY=0$`, st.discoveryOff)
			sc.Given(`^the owner opened \[3\] EDGE and a scan is running$`, st.openedAndScanning)
			sc.Given(`^setup is not available in this build$`, st.setupUnavailable)
			sc.Given(`^the owner pressed e$`, st.pressedE)
			sc.Given(`^the owner pressed e and is not logged in$`, st.pressedENotLoggedIn)
			sc.Given(`^the machine has no internet$`, st.noInternet)
			sc.Given(`^the owner finished "start an Edge here" as "([^"]*)"$`, st.finishedStartAs)
			sc.Given(`^this machine is enrolled as "([^"]*)" with two other nodes$`, st.enrolledWithTwo)
			sc.Given(`^this machine is enrolled as "([^"]*)"$`, st.enrolledAs)
			sc.Given(`^the owner pressed j$`, st.pressedJ)
			sc.Given(`^an ask by address is waiting$`, st.askWaiting)

			sc.When(`^the owner opens \[3\] EDGE at (\d+) columns$`, st.opensAt)
			sc.When(`^the owner opens \[3\] EDGE$`, st.opens)
			sc.When(`^a scan pass completes$`, st.passCompletes)
			sc.When(`^the owner presses (enter|esc|e|j)$`, st.presses)
			sc.When(`^the owner presses j on \[3\] EDGE$`, st.pressesJOnEdge)
			sc.When(`^the owner chooses "through RogerAI"$`, st.choosesRogerAI)
			sc.When(`^the owner presses e and accepts the defaults$`, st.pressesEAndDefaults)
			sc.When(`^the owner pastes a valid Edge invite and presses enter$`, st.pastesInviteEnter)
			sc.When(`^the owner pastes an invite at the j prompt$`, st.pastesInvite)
			sc.When(`^the owner types an authority's address and presses enter$`, st.typesAddressEnter)
			sc.When(`^the authority's owner approves it$`, st.ownerApproves)
			sc.When(`^(the invite was already used|the invite expired|the authority refused the ask|the ask timed out|the address does not answer|the invite is for a Tower, not an Edge|the authority's key changed)$`, st.whatHappens)

			sc.Then(`^it shows "([^"]*)" and "([^"]*)"$`, st.shows)
			sc.Then(`^one sentence says Roger Edge links your own devices \(laptops, desktops, Pis\) so they can use each other's models, on your network$`, st.saysSentence)
			sc.Then(`^it offers "([^"]*)" with "([^"]*)" and "([^"]*)"$`, st.offers)
			sc.Then(`^it does not say "([^"]*)"$`, st.doesNotSay)
			sc.Then(`^it does not name "([^"]*)", "([^"]*)" or "([^"]*)"$`, st.namesNone)
			sc.Then(`^it does not mention Core$`, func() error { return st.doesNotSay("Core") })
			sc.Then(`^no ON AIR banner or lamp is drawn$`, st.noLampOrBanner)
			sc.Then(`^the footer offers no "a adopt"$`, st.noAdopt)
			sc.Then(`^the action bar reads "([^"]*)", "([^"]*)" and "([^"]*)"$`, st.actionBar)
			sc.Then(`^it says "([^"]*)"$`, st.says)
			sc.Then(`^the scan line leads with (◌◌|◉◎)$`, st.scanLeadsWith)
			sc.Then(`^the second scan line says no pass has completed yet$`, st.secondScanNoPass)
			sc.Then(`^it never says "([^"]*)"$`, st.neverSays)
			sc.Then(`^the second scan line says "([^"]*)"$`, st.secondScanSays)
			sc.Then(`^the scan line says scanning is off and names the switch$`, st.scanOff)
			sc.Then(`^the e and j lines are still offered$`, st.ejStillOffered)
			sc.Then(`^the scan stops within about a second \(features/edge/scan_on_request\.feature\)$`, st.scanStops)
			sc.Then(`^the setup opens at "([^"]*)"$`, st.setupOpensAt)
			sc.Then(`^the status names "([^"]*)" and "([^"]*)"$`, st.statusNames)
			sc.Then(`^it never asks whether to start or join \(join has its own key\)$`, st.neverAsksStartOrJoin)
			sc.Then(`^the authority step offers "([^"]*)" selected by default$`, st.authorityLocalDefault)
			sc.Then(`^it offers "([^"]*)"$`, st.offersLine)
			sc.Then(`^enter alone continues with the local choice$`, st.enterContinuesLocal)
			sc.Then(`^the step says a login is needed and offers it in place$`, st.loginNeededInPlace)
			sc.Then(`^esc returns to the authority step without changing anything$`, st.escBackToAuthority)
			sc.Then(`^this machine becomes the local authority and enrolls itself$`, st.becomesLocalAuthority)
			sc.Then(`^no network beyond this LAN is contacted$`, st.noNetworkBeyondLAN)
			sc.Then(`^\[3\] EDGE shows the existing full view with "([^"]*)" as this machine and the authority$`, st.fullViewWith)
			sc.Then(`^it says how to add the next device: roger edge invite, then roger join on that device$`, st.saysHowToAddNext)
			sc.Then(`^the selection moves down one row, as before$`, st.selectionMovedDown)
			sc.Then(`^no join prompt opens$`, st.noJoinPrompt)
			sc.Then(`^a prompt asks to paste an invite, or type the authority's address$`, st.promptAsks)
			sc.Then("^this machine joins that Edge using the same invite check as `roger join`$", st.joinsWithSameCheck)
			sc.Then(`^the full Edge view replaces the empty state$`, st.fullViewReplaces)
			sc.Then(`^a matching phrase is shown with "the owner of that Edge should see the same phrase"$`, st.phraseShown)
			sc.Then(`^the screen waits for approval with a countdown and esc to cancel$`, st.waitsWithCountdown)
			sc.Then(`^this machine is a member and the full Edge view replaces the empty state$`, st.memberFullView)
			sc.Then(`^the prompt says "([^"]*)" and the empty state is unchanged$`, st.promptSaysUnchanged)
			sc.Then(`^the invite's code appears in no log or status line$`, st.codeNowhere)
			sc.Then(`^the existing full Edge view is shown, unchanged$`, st.fullViewUnchanged)
			sc.Then(`^e and j have their existing meanings there \(none for j\)$`, st.ejExistingMeanings)
			sc.Then(`^it uses the Health/Fleet palette \(paper, ink, a muted eyebrow, the one red\)$`, st.fleetPalette)
			sc.Then(`^under the TUI's own brand, "([^"]*)" is a muted eyebrow row with no second heavy rule$`, st.mutedEyebrowNoRule)
			sc.Then(`^the keys are drawn as paper key caps$`, st.paperKeyCaps)
			sc.Then(`^the scan line leads with the magic-eye glyph$`, func() error { return st.scanLeadsWith(look.EyeWait) })
			sc.Then(`^no line overflows$`, st.noLineOverflows)
			sc.Then(`^(nothing is cut|the description column after each key is dropped before a label)$`, st.whatFolds)
			sc.Given(`^the compact windowshade mode is on$`, st.compactOn)
			sc.Then(`^it shows one line: "([^"]*)"$`, st.showsOneLine)
			sc.Then(`^the compact header names \[3\] EDGE, never TUNE IN$`, st.compactHeaderNamesEdge)
			sc.Given(`^NO_COLOR is set$`, st.noColor)
			sc.Then(`^key caps read as bracketed keys, the magic eye folds to an ASCII mark, and every word is still there$`, st.readsAsText)
			sc.Then(`^every glyph on the screen is one glyphs\.Fold folds$`, st.everyGlyphFolds)
			st.nearbySteps(sc)
			sc.When(`^the empty Edge is rendered by the TUI at (\d+), (\d+) and (\d+) columns$`, st.renderedAt)
			sc.Then(`^each frame contains every key and word, and no line overflows$`, st.framesHold)
			sc.Then(`^the (\d+)-column frame matches the committed golden file for the empty Edge$`, st.matchesGolden)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@tui",
			Paths: []string{"../../features/edge/empty_edge_simple.feature"},
		},
	}
	if bddrun.Run(suite) != 0 {
		t.Fatal("the simple empty Edge scenarios failed")
	}
}
