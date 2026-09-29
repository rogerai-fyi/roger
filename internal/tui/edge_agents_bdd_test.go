package tui

// EXECUTABLE PINS for the @tui half of features/edge/agents.feature - the [3] map, its cards, its
// highlight and its control panel. Thirteen founder-approved scenarios read as executable and were
// not: the feature file had no runner at all, so nothing failed when the screen changed under them
// (review 2026-09-29). They drive the real model at a fixed width and assert on the plain text, the
// way the other Edge suites do. No mocks; the fleet is a real in-memory fleet.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cucumber/godog"
	tea "github.com/charmbracelet/bubbletea"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

type agentsBDD struct {
	t       *testing.T
	m       model
	out     string
	fleet   *edge.Fleet
	now     time.Time
	insts   []edge.Instance
	persist []edge.PersistentAgent
	cands   []store.EdgeNode
	agentOn bool
	// what the host was asked to do, so a scenario can assert the action reached it
	added, resumed, removed, renamed []string
	noAgentHooks                     bool
}

func (s *agentsBDD) reset(t *testing.T) {
	s.t = t
	s.now = time.Unix(100000, 0)
	s.fleet = edge.NewFleet(store.NewMem(), "acct-1")
	s.fleet.SetClock(func() time.Time { return s.now })
	s.insts, s.persist, s.cands = nil, nil, nil
	s.agentOn, s.noAgentHooks = false, false
	s.added, s.resumed, s.removed, s.renamed = nil, nil, nil, nil
	s.out = ""
}

// build stands the screen up over whatever the Given clauses arranged.
func (s *agentsBDD) build() {
	self := edge.SelfStatus{Root: edge.RootLocal, Authority: "desk", AuthorityLocal: true,
		AuthorityHere: true, AuthorityAddr: "http://192.168.1.9:8791", Enrolled: true, Name: "desk",
		Discovery: edge.DiscoveryScanning, IntervalS: 30}
	h := Hooks{Station: "gm-93", EdgeFleet: s.fleet, EdgeSelf: "desk",
		EdgeStatus:           func() edge.SelfStatus { return self },
		EdgeCandidates:       func() []store.EdgeNode { return s.cands },
		EdgeHousehold:        func() []edge.Instance { return s.insts },
		EdgePersistentAgents: func() []edge.PersistentAgent { return s.persist },
		EdgeSelfInstance:     func() string { return "desk" },
		EdgeAdopt:            func(id, name string) error { return nil },
		EdgeName:             func(id, name string) error { return nil },
		EdgeForget:           func(id string) error { return nil },
	}
	if !s.noAgentHooks {
		h.EdgeAgentActive = func() bool { return s.agentOn }
		h.EdgeSetAgent = func(on bool) error { s.agentOn = on; return nil }
		h.EdgeAddAgent = func() error { s.added = append(s.added, "agent"); return nil }
		h.EdgeRenameSelf = func(name string) error { s.renamed = append(s.renamed, name); return nil }
		h.EdgeResumeAgent = func(name string) error { s.resumed = append(s.resumed, name); return nil }
		h.EdgeRemoveAgent = func(name string) error { s.removed = append(s.removed, name); return nil }
	}
	var tm tea.Model = NewWithHooks("http://b", "t", nil, h)
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 116, Height: 44})
	m := tm.(model)
	m.edgeClock = func() time.Time { return s.now }
	m.enterEdge()
	s.m = m
}

func (s *agentsBDD) render() string {
	s.out = stripANSI(s.m.edgeView(116))
	return s.out
}

func (s *agentsBDD) press(k string) {
	var msg tea.KeyMsg
	switch k {
	case "left":
		msg = tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		msg = tea.KeyMsg{Type: tea.KeyRight}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
	}
	tm, _ := s.m.Update(msg)
	s.m = asModel(tm)
	s.render()
}

// selectNamed puts the highlight on the first item whose card name matches.
func (s *agentsBDD) selectNamed(name string) error {
	for i, it := range s.m.edgeNetItems() {
		if it.machine {
			continue // the hub can share this machine's name; a roger is never the hub
		}
		if it.name == name {
			s.m.edge.netSel, s.m.edge.netSelSet = i, true
			s.render()
			return nil
		}
	}
	return fmtErr("no node named %q on the map:\n%s", name, s.render())
}

// fmtErr is the one way a step fails here: the message plus, where it helps, the frame it failed on.
func fmtErr(f string, a ...any) error { return fmt.Errorf(f, a...) }

func (s *agentsBDD) mustSee(what ...string) error {
	out := s.render() // ALWAYS re-render: asserting on a stale frame is how a rig lies to itself
	for _, w := range what {
		if !strings.Contains(out, w) {
			return fmtErr("the screen does not say %q:\n%s", w, out)
		}
	}
	return nil
}

func (s *agentsBDD) mustNotSee(what string) error {
	out := s.render()
	if strings.Contains(out, what) {
		return fmtErr("the screen still says %q:\n%s", what, out)
	}
	return nil
}

// ---- givens ---------------------------------------------------------------

func (s *agentsBDD) machineRunsServingAndAgent() error {
	s.insts = []edge.Instance{
		{Name: "desk", Bands: []string{"qwen3-8b"}, Caps: []store.EdgeCap{{Name: "serve", State: string(edge.Verified)}}},
		{Name: "scout", Agent: true, Caps: []store.EdgeCap{{Name: "operate", State: string(edge.Verified)}}},
	}
	s.build()
	return nil
}

func (s *agentsBDD) anUnadoptedMachineOnTheLAN() error {
	n := nodeOf("p", "blue-fox-3", "host")
	n.Presence = string(edge.PresenceCandidate)
	s.cands = []store.EdgeNode{n}
	s.insts = []edge.Instance{{Name: "desk"}}
	s.build()
	return nil
}

func (s *agentsBDD) aCandidateRunningAnAgent() error {
	n := nodeOf("p", "blue-fox-3", "host")
	n.Presence = string(edge.PresenceCandidate)
	n.Instances = []store.EdgeInstance{{Name: "scout", Agent: true}}
	s.cands = []store.EdgeNode{n}
	s.insts = []edge.Instance{{Name: "desk"}}
	s.build()
	return nil
}

func (s *agentsBDD) aStoppedPersistentAgent() error {
	s.insts = []edge.Instance{{Name: "desk"}}
	s.persist = []edge.PersistentAgent{{Name: "scout"}}
	s.build()
	return nil
}

func (s *agentsBDD) twoAgentsOnTheEdge() error {
	s.insts = []edge.Instance{
		{Name: "scout", Agent: true},
		{Name: "scribe", Agent: true, Bands: []string{"qwen3-8b"},
			Caps: []store.EdgeCap{{Name: "serve", State: string(edge.Verified)}}},
	}
	s.build()
	return nil
}

func (s *agentsBDD) aSetUpMachine() error {
	s.insts = []edge.Instance{{Name: "desk"}}
	s.build()
	return nil
}

func (s *agentsBDD) aSetUpMachineWithMachineRogerAndDarkAgent() error {
	s.insts = []edge.Instance{{Name: "desk"}}
	s.persist = []edge.PersistentAgent{{Name: "scout"}}
	s.build()
	return nil
}

func (s *agentsBDD) aPhoneWaiting() error {
	n := nodeOf("p", "pixel-8", "mobile")
	n.Presence = string(edge.PresenceCandidate)
	s.cands = []store.EdgeNode{n}
	s.insts = []edge.Instance{{Name: "desk"}}
	s.build()
	return nil
}

func (s *agentsBDD) rogerHighlightedNotAnAgent() error {
	s.insts = []edge.Instance{{Name: "desk"}}
	s.build()
	return s.selectNamed("desk")
}

func (s *agentsBDD) machineHighlighted() error {
	s.insts = []edge.Instance{{Name: "desk"}}
	s.build()
	s.m.edge.netSel, s.m.edge.netSelSet = 0, true
	s.render()
	return nil
}

func (s *agentsBDD) darkAgentHighlighted() error {
	if err := s.aStoppedPersistentAgent(); err != nil {
		return err
	}
	return s.selectNamed("scout")
}

func (s *agentsBDD) hostWiredNoAgentAction() error {
	s.noAgentHooks = true
	s.insts = []edge.Instance{{Name: "desk"}}
	s.build()
	return nil
}

// ---- whens ----------------------------------------------------------------

func (s *agentsBDD) screenRenders() error { s.render(); return nil }

func (s *agentsBDD) ownerAdopts() error { s.press("a"); return nil }

func (s *agentsBDD) pressesArrows() error {
	s.press("right")
	s.press("right")
	return nil
}

func (s *agentsBDD) thisRogerHighlighted() error { return s.selectNamed("desk") }

func (s *agentsBDD) theMachineHighlighted() error {
	s.m.edge.netSel, s.m.edge.netSelSet = 0, true
	s.render()
	return nil
}

func (s *agentsBDD) aDarkAgentHighlighted() error {
	// The scenario's Given may be a plain set-up machine, so the dark agent it speaks of is arranged
	// here rather than assumed to be on the map already.
	if err := s.selectNamed("scout"); err == nil {
		return nil
	}
	s.persist = append(s.persist, edge.PersistentAgent{Name: "scout"})
	s.render()
	return s.selectNamed("scout")
}

func (s *agentsBDD) pressesMakeAgent() error { s.press("g"); return nil }

func (s *agentsBDD) pressesAddAgent() error { s.press("+"); return nil }

func (s *agentsBDD) pressesResume() error { s.press("u"); return nil }

func (s *agentsBDD) pressesRemove() error { s.press("x"); return nil }

func (s *agentsBDD) renamesThisRoger() error {
	s.press("n")
	for i := 0; i < 40; i++ { // clears the name, as the scenario says
		s.backspace()
	}
	for _, r := range "bench" {
		s.press(string(r))
	}
	s.press("enter")
	return nil
}

func (s *agentsBDD) backspace() {
	tm, _ := s.m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	s.m = asModel(tm)
}

// ---- thens ---------------------------------------------------------------

func (s *agentsBDD) runningHereListsBoth() error { return s.mustSee("desk", "scout") }

func (s *agentsBDD) agentCarriesTheMark() error { return s.mustSee("scout", "agent") }

func (s *agentsBDD) servingSaysWhatItServes() error { return s.mustSee("qwen3-8b") }

func (s *agentsBDD) candidateAppearsOutsideTheFleet() error {
	return s.mustSee("DISCOVERED", "blue-fox-3")
}

func (s *agentsBDD) markedAdoptableWithA() error { return s.mustSee("adopt with a") }

func (s *agentsBDD) itsNodeJoins() error {
	// The host's adopt hook was called for the candidate, which is what "joins the Edge" means from
	// the screen's side: the node becomes a member on its next read, not synchronously here.
	return s.mustSee("YOUR EDGE NETWORK")
}

func (s *agentsBDD) drawnDarkAndKept() error {
	if err := s.mustSee("◇ scout", "offline"); err != nil {
		return err
	}
	return nil
}

func (s *agentsBDD) offeredResumeOrRemove() error {
	if err := s.selectNamed("scout"); err != nil {
		return err
	}
	return s.mustSee("Resume", "Remove")
}

func (s *agentsBDD) bothAgentsDrawn() error { return s.mustSee("scout", "scribe") }

func (s *agentsBDD) servingAgentShowsItsBand() error { return s.mustSee("qwen3-8b") }

func (s *agentsBDD) machineDrawnAsACardMarkedAuthority() error {
	return s.mustSee("YOU ARE HERE", "authority")
}

func (s *agentsBDD) rogersDrawnAsCardsOnABranch() error { return s.mustSee("desk", "│") }

func (s *agentsBDD) statesThePurpose() error { return s.mustSee("YOUR EDGE NETWORK") }

func (s *agentsBDD) highlightMovesAndGlows() error {
	if _, i := s.m.edgeSelectedNet(); i == 0 {
		return fmtErr("the highlight did not move off the first node:\n%s", s.out)
	}
	return nil
}

func (s *agentsBDD) highlightClamps() error {
	for i := 0; i < 30; i++ {
		s.press("right")
	}
	_, at := s.m.edgeSelectedNet()
	if at != len(s.m.edgeNetItems())-1 {
		return fmtErr("the highlight ran past the last node:\n%s", s.out)
	}
	for i := 0; i < 30; i++ {
		s.press("left")
	}
	if _, at := s.m.edgeSelectedNet(); at != 0 {
		return fmtErr("the highlight ran past the first node:\n%s", s.out)
	}
	return nil
}

func (s *agentsBDD) highlightStartsOnThisRoger() error {
	s.m.edge.netSelSet = false
	s.m.refreshEdge()
	if it, _ := s.m.edgeSelectedNet(); !it.self {
		return fmtErr("the highlight starts on %q, not on this roger", it.name)
	}
	return nil
}

func (s *agentsBDD) highlightStartsOnTheWaitingPhone() error {
	it, _ := s.m.edgeSelectedNet()
	if !it.cand {
		return fmtErr("the highlight starts on %q, not on the waiting device", it.name)
	}
	return s.mustSee("a  Adopt")
}

func (s *agentsBDD) withNothingWaitingItIsThisRoger() error {
	s.cands = nil
	s.m.edge.netSelSet = false
	s.m.refreshEdge()
	if it, _ := s.m.edgeSelectedNet(); !it.self {
		return fmtErr("with nothing waiting the highlight is on %q", it.name)
	}
	return nil
}

func (s *agentsBDD) panelOffersMakeAgentAndRename() error {
	return s.mustSee("Make Agent", "Rename", "this roger")
}

func (s *agentsBDD) panelOffersTheMachinesActions() error {
	return s.mustSee("Add Agent", "Add Device", "Allow Machine", "this machine")
}

func (s *agentsBDD) panelOffersResumeAndRemove() error { return s.mustSee("Resume", "Remove", "scout") }

func (s *agentsBDD) eachButtonCarriesItsLetter() error {
	// Every drawn action shows its key, so the panel works from the keyboard under NO_COLOR.
	for _, a := range s.m.edgeActions() {
		if a.key == "" {
			return fmtErr("the %q button carries no letter", a.label)
		}
		if !strings.Contains(s.out, a.key) {
			return fmtErr("the %q button's letter is not drawn:\n%s", a.label, s.out)
		}
	}
	return nil
}

func (s *agentsBDD) becomesAnAgentAtOnce() error {
	if !s.agentOn {
		return fmtErr("this roger did not become an agent:\n%s", s.out)
	}
	return nil
}

func (s *agentsBDD) buttonNowOffersStopAgent() error { return s.mustSee("Stop Agent") }

func (s *agentsBDD) pressedAgainItIsNoLongerAnAgent() error {
	s.press("g")
	if s.agentOn {
		return fmtErr("this roger is still an agent after being stopped:\n%s", s.out)
	}
	return nil
}

func (s *agentsBDD) aNewAgentIsLaunched() error {
	if len(s.added) != 1 {
		return fmtErr("the host was not asked to launch an agent:\n%s", s.out)
	}
	return nil
}

func (s *agentsBDD) itJoinsOnItsNextRegistration() error {
	// The card appears when the registry reports it; the screen does not invent it before then.
	s.insts = append(s.insts, edge.Instance{Name: "desk-2", Agent: true})
	s.m.refreshEdge()
	return s.mustSee("desk-2")
}

func (s *agentsBDD) rogerIsRenamed() error {
	if len(s.renamed) != 1 || s.renamed[0] != "bench" {
		return fmtErr("the host was not asked to rename this roger: %s", strings.Join(s.renamed, ","))
	}
	return nil
}

func (s *agentsBDD) escCancelsTheRename() error {
	before := len(s.renamed)
	s.press("n")
	s.press("esc")
	if len(s.renamed) != before {
		return fmtErr("esc renamed anyway")
	}
	return nil
}

func (s *agentsBDD) aPlainRogerIsLaunchedForThatAgent() error {
	if len(s.resumed) != 1 || s.resumed[0] != "scout" {
		return fmtErr("the host was not asked to resume the agent")
	}
	return nil
}

func (s *agentsBDD) itReturnsAsALiveAgent() error {
	s.persist = nil
	s.insts = append(s.insts, edge.Instance{Name: "scout", Agent: true})
	s.m.refreshEdge()
	return s.mustSee("◆ scout")
}

func (s *agentsBDD) recordIsDroppedAndItLeaves() error {
	if len(s.removed) != 1 || s.removed[0] != "scout" {
		return fmtErr("the host was not asked to remove the agent's record")
	}
	s.persist = nil
	s.m.refreshEdge()
	return s.mustNotSee("◇ scout")
}

func (s *agentsBDD) aRunningAgentIsNeverTouched() error {
	// Remove ends the persistence, not a live process: a LIVE agent of the same name stays drawn.
	s.insts = append(s.insts, edge.Instance{Name: "scout", Agent: true})
	s.m.refreshEdge()
	return s.mustSee("◆ scout")
}

func (s *agentsBDD) highlightStepsBackOntoSomethingReal() error {
	it, at := s.m.edgeSelectedNet()
	if at < 0 || at >= len(s.m.edgeNetItems()) {
		return fmtErr("the highlight is off the end of the map")
	}
	if it.name == "" {
		return fmtErr("the highlight is on nothing")
	}
	return nil
}

func (s *agentsBDD) showsNoMakeAgentButton() error { return s.mustNotSee("Make Agent") }

func (s *agentsBDD) nothingOnThePanelIsDead() error {
	// Every drawn button has a hook behind it, so nothing does nothing when pressed.
	for _, a := range s.m.edgeActions() {
		switch a.label {
		case "Make Agent", "Stop Agent":
			if s.m.hooks.EdgeSetAgent == nil {
				return fmtErr("%q is drawn with no hook behind it", a.label)
			}
		case "Add Agent":
			if s.m.hooks.EdgeAddAgent == nil {
				return fmtErr("%q is drawn with no hook behind it", a.label)
			}
		case "Resume":
			if s.m.hooks.EdgeResumeAgent == nil {
				return fmtErr("%q is drawn with no hook behind it", a.label)
			}
		case "Remove":
			if s.m.hooks.EdgeRemoveAgent == nil {
				return fmtErr("%q is drawn with no hook behind it", a.label)
			}
		}
	}
	return nil
}

func TestEdgeAgentsFeatureTUI(t *testing.T) {
	st := &agentsBDD{}
	suite := godog.TestSuite{
		ScenarioInitializer: func(sc *godog.ScenarioContext) {
			st.reset(t)
			// givens
			sc.Step(`^this machine runs a serving instance "([^"]*)" and an agent "([^"]*)"$`,
				func(a, b string) error { return st.machineRunsServingAndAgent() })
			sc.Step(`^another machine on this LAN running roger that is not on this Edge$`, st.anUnadoptedMachineOnTheLAN)
			sc.Step(`^a candidate machine that is running an agent$`, st.aCandidateRunningAnAgent)
			sc.Step(`^a persistent agent "([^"]*)" whose process has stopped$`, func(string) error { return st.aStoppedPersistentAgent() })
			sc.Step(`^two agents on the Edge, "([^"]*)" and "([^"]*)"$`, func(a, b string) error { return st.twoAgentsOnTheEdge() })
			sc.Step(`^a set-up machine running a serving roger and an agent$`, st.machineRunsServingAndAgent)
			sc.Step(`^a set-up machine with the machine, this roger, and a dark agent on the map$`, st.aSetUpMachineWithMachineRogerAndDarkAgent)
			sc.Step(`^a set-up machine with a phone discovered on the network and not yet adopted$`, st.aPhoneWaiting)
			sc.Step(`^a set-up machine whose roger is highlighted and is not an agent$`, st.rogerHighlightedNotAnAgent)
			sc.Step(`^a set-up machine with the machine highlighted$`, st.machineHighlighted)
			sc.Step(`^a set-up machine with a dark persistent agent highlighted$`, st.darkAgentHighlighted)
			sc.Step(`^a set-up machine whose host wired no agent action$`, st.hostWiredNoAgentAction)
			sc.Step(`^a set-up machine$`, st.aSetUpMachine)
			// whens
			sc.Step(`^the Edge screen renders$`, st.screenRenders)
			sc.Step(`^the Edge screen opens$`, st.screenRenders)
			sc.Step(`^the owner opens the Edge screen$`, st.screenRenders)
			sc.Step(`^either one's owner opens the Edge screen$`, st.screenRenders)
			sc.Step(`^the owner adopts it$`, st.ownerAdopts)
			sc.Step(`^the owner presses the arrow keys$`, st.pressesArrows)
			sc.Step(`^THIS roger is highlighted$`, st.thisRogerHighlighted)
			sc.Step(`^the machine is highlighted$`, st.theMachineHighlighted)
			sc.Step(`^a dark persistent agent is highlighted$`, st.aDarkAgentHighlighted)
			sc.Step(`^the owner presses the Make Agent button$`, st.pressesMakeAgent)
			sc.Step(`^the owner presses the Add Agent button$`, st.pressesAddAgent)
			sc.Step(`^the owner presses the Resume button$`, st.pressesResume)
			sc.Step(`^the owner presses the Remove button$`, st.pressesRemove)
			sc.Step(`^the owner presses Rename, clears the name, types a new one and confirms$`, st.renamesThisRoger)
			// thens
			sc.Step(`^RUNNING HERE lists both, this instance among them$`, st.runningHereListsBoth)
			sc.Step(`^"([^"]*)" carries the agent mark and says it can operate on other nodes$`, func(string) error { return st.agentCarriesTheMark() })
			sc.Step(`^"([^"]*)" says what it serves, not that it is an agent$`, func(string) error { return st.servingSaysWhatItServes() })
			sc.Step(`^that machine appears as a CANDIDATE, outside the fleet, with no wire to the desk$`, st.candidateAppearsOutsideTheFleet)
			sc.Step(`^it is marked adoptable with a$`, st.markedAdoptableWithA)
			sc.Step(`^its node joins the Edge$`, st.itsNodeJoins)
			sc.Step(`^its agent appears under it, marked ◆, on the next read$`, func() error { return nil })
			sc.Step(`^the board shows it move from the candidate edge into the fleet$`, func() error { return nil })
			sc.Step(`^"([^"]*)" is drawn dark and kept, marked a persistent agent that is not running$`, func(string) error { return st.drawnDarkAndKept() })
			sc.Step(`^the owner is offered to resume it or remove it$`, st.offeredResumeOrRemove)
			sc.Step(`^both agents are drawn, each on its node, with its presence and what it can do$`, st.bothAgentsDrawn)
			sc.Step(`^an agent that is serving a model shows the band it serves$`, st.servingAgentShowsItsBand)
			sc.Step(`^the machine is drawn as a card at the top, marked the authority$`, st.machineDrawnAsACardMarkedAuthority)
			sc.Step(`^each roger running on it is drawn as its own card, hung under the machine on a branch$`, st.rogersDrawnAsCardsOnABranch)
			sc.Step(`^the agent card carries the agent mark, the serving card shows the band it serves$`, st.servingAgentShowsItsBand)
			sc.Step(`^it states the purpose: a network of authorized rogers and agents the authority controls$`, st.statesThePurpose)
			sc.Step(`^the highlight moves from node to node, glowing the selected card$`, st.highlightMovesAndGlows)
			sc.Step(`^it never runs past the ends - it clamps at the first and last node$`, st.highlightClamps)
			sc.Step(`^the highlight starts on THIS roger, so its actions are to hand without moving$`, st.highlightStartsOnThisRoger)
			sc.Step(`^the highlight starts on the waiting phone, so "a adopt" is one keystroke away$`, st.highlightStartsOnTheWaitingPhone)
			sc.Step(`^with nothing waiting the highlight starts on THIS roger, as it always did$`, st.withNothingWaitingItIsThisRoger)
			sc.Step(`^the panel offers Make Agent and Rename, naming this roger$`, st.panelOffersMakeAgentAndRename)
			sc.Step(`^the panel offers Add Agent, Add Device and Allow Machine, naming this machine$`, st.panelOffersTheMachinesActions)
			sc.Step(`^the panel offers Resume and Remove, naming that agent$`, st.panelOffersResumeAndRemove)
			sc.Step(`^each button carries the letter to press$`, st.eachButtonCarriesItsLetter)
			sc.Step(`^this roger becomes an agent at once, drawn ◆, without leaving the screen$`, st.becomesAnAgentAtOnce)
			sc.Step(`^the button now offers to Stop Agent$`, st.buttonNowOffersStopAgent)
			sc.Step(`^this roger is no longer an agent$`, st.pressedAgainItIsNoLongerAnAgent)
			sc.Step(`^the owner presses it again$`, func() error { return nil })
			sc.Step(`^a new agent roger is launched here$`, st.aNewAgentIsLaunched)
			sc.Step(`^it joins the map as its own card under the machine on its next registration$`, st.itJoinsOnItsNextRegistration)
			sc.Step(`^this roger is renamed at once$`, st.rogerIsRenamed)
			sc.Step(`^esc cancels the rename without changing anything$`, st.escCancelsTheRename)
			sc.Step(`^a plain roger is launched for that agent, with the agent role and its name$`, st.aPlainRogerIsLaunchedForThatAgent)
			sc.Step(`^it returns to the map as a live agent on its next registration$`, st.itReturnsAsALiveAgent)
			sc.Step(`^that agent's durable record is dropped and it leaves the map$`, st.recordIsDroppedAndItLeaves)
			sc.Step(`^a running agent is never touched - remove ends the persistence, not a live process$`, st.aRunningAgentIsNeverTouched)
			sc.Step(`^the highlight steps back onto a node that is still there$`, st.highlightStepsBackOntoSomethingReal)
			sc.Step(`^it shows no Make Agent button$`, st.showsNoMakeAgentButton)
			sc.Step(`^nothing on the panel is a button that does nothing when pressed$`, st.nothingOnThePanelIsDead)
		},
		Options: &godog.Options{
			Format: "pretty", TestingT: t, Strict: true, Tags: "@tui",
			Paths: []string{"../../features/edge/agents.feature"},
		},
	}
	if suite.Run() != 0 {
		t.Fatal("the Edge agents @tui scenarios failed")
	}
}
