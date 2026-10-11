package tui

// THE [3] EDGE SCREEN'S LETTERED ACTIONS, each from every side: the node it acts on is the wrong
// kind (a nudge, nothing called), the build has no hook for it (the equivalent CLI command, nothing
// called), the host refuses (its words on the status line), and the host does it (the hook saw the
// selected node, the status says what happened). Driven by key presses through Update on a real
// model over a real edge.Fleet - the hooks only record what they were asked.

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

var errHostSaysNo = errors.New("the host says no")

// edgeActionsRig is an enrolled authority ("desk") with: a member device serving a model through one
// of its instances, this roger ("desk") and a serving roger ("worker") in the household, and a dark
// persistent agent ("ghost").
func edgeActionsRig(t *testing.T) model {
	t.Helper()
	self := edge.SelfStatus{Root: edge.RootLocal, Authority: "desk", AuthorityLocal: true, AuthorityHere: true,
		AuthorityAddr: "http://192.168.1.9:8791", Enrolled: true, Name: "desk",
		Discovery: edge.DiscoveryScanning, IntervalS: 30}
	attic := nodeOf("a", "attic-pi", "board")
	attic.Instances = []store.EdgeInstance{{Name: "pi-roger", Bands: []string{"qwen-3"},
		Caps: []store.EdgeCap{{Name: string(edge.Serve), State: string(edge.Verified)}}}}
	bare := nodeOf("b", "bare-pi", "board")
	m := networkModel(t, self, attic, bare)
	m.hooks.EdgeSelfInstance = func() string { return "desk" }
	m.hooks.EdgeHousehold = func() []edge.Instance {
		return []edge.Instance{
			{Name: "desk", Agent: true},
			{Name: "worker", Bands: []string{"pico"}, Caps: []store.EdgeCap{{Name: string(edge.Serve), State: string(edge.Verified)}}},
		}
	}
	m.hooks.EdgePersistentAgents = func() []edge.PersistentAgent { return []edge.PersistentAgent{{Name: "ghost"}} }
	m.enterEdge()
	return m
}

// selectNet parks the map highlight on the first item matching pred.
func selectNet(t *testing.T, m model, pred func(edgeNetItem) bool) model {
	t.Helper()
	for i, it := range m.edgeNetItems() {
		if pred(it) {
			m.edge.netSel, m.edge.netSelSet = i, true
			return m
		}
	}
	t.Fatalf("no net item matches")
	return m
}

func named(n string) func(edgeNetItem) bool { return func(it edgeNetItem) bool { return it.name == n } }
func theMachine(it edgeNetItem) bool        { return it.machine }

func edgePress(m model, keys ...string) model {
	var tm tea.Model = m
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "backspace":
			msg = tea.KeyMsg{Type: tea.KeyBackspace}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		tm, _ = tm.Update(msg)
	}
	return asModel(tm)
}

func statusOf(m model) string { return stripANSI(m.status) }

func TestEdgeAskNamesTheModelOrOpensThePrompt(t *testing.T) {
	m := edgeActionsRig(t)

	// A node that serves nothing has nothing to ask.
	got := edgePress(selectNet(t, m, named("bare-pi")), "p")
	require.Contains(t, statusOf(got), "serves no model yet")

	// A member serving through an instance: the command names the node AND the model.
	got = edgePress(selectNet(t, m, named("attic-pi")), "p")
	require.Contains(t, statusOf(got), "roger use attic-pi --model qwen-3")

	// A local serving roger is addressed by its model, with no --model.
	got = edgePress(selectNet(t, m, named("worker")), "p")
	require.Contains(t, statusOf(got), "run `roger use pico` to prompt pico")

	// With the hook wired it opens the pinned prompt, addressed the same way.
	var asked []string
	m.hooks.EdgeAsk = func(addr, model string) error { asked = append(asked, addr+"/"+model); return nil }
	edgePress(selectNet(t, m, named("attic-pi")), "p")
	require.Equal(t, []string{"attic-pi/qwen-3"}, asked)

	m.hooks.EdgeAsk = func(string, string) error { return errHostSaysNo }
	got = edgePress(selectNet(t, m, named("attic-pi")), "p")
	require.Contains(t, statusOf(got), "the host says no")
}

func TestEdgeResumeAndRemoveActOnlyOnADarkAgent(t *testing.T) {
	for _, key := range []string{"u", "x"} {
		m := edgeActionsRig(t)
		got := edgePress(selectNet(t, m, named("attic-pi")), key)
		require.Contains(t, statusOf(got), "select a ◇ node first", key)

		// Unwired: the CLI command for the agent the owner is looking at.
		got = edgePress(selectNet(t, m, named("ghost")), key)
		require.Contains(t, statusOf(got), "roger edge agent", key)
		require.Contains(t, statusOf(got), "ghost", key)

		var calls []string
		hook := func(name string) error { calls = append(calls, name); return nil }
		fail := func(string) error { return errHostSaysNo }
		if key == "u" {
			m.hooks.EdgeResumeAgent = fail
		} else {
			m.hooks.EdgeRemoveAgent = fail
		}
		got = edgePress(selectNet(t, m, named("ghost")), key)
		require.Contains(t, statusOf(got), "could not", key)
		require.Contains(t, statusOf(got), "the host says no", key)

		if key == "u" {
			m.hooks.EdgeResumeAgent = hook
		} else {
			m.hooks.EdgeRemoveAgent = hook
		}
		got = edgePress(selectNet(t, m, named("ghost")), key)
		require.Equal(t, []string{"ghost"}, calls, key)
		if key == "u" {
			require.Contains(t, statusOf(got), "resuming ghost")
		} else {
			require.Contains(t, statusOf(got), "removed ghost from the Edge")
		}
	}
}

func TestEdgeAgentRoleToggleAndAdd(t *testing.T) {
	m := edgeActionsRig(t)
	require.Contains(t, statusOf(edgePress(m, "g")), "cannot change the agent role")
	require.Contains(t, statusOf(edgePress(m, "+")), "cannot add an agent")

	active := false
	var set []bool
	m.hooks.EdgeAgentActive = func() bool { return active }
	m.hooks.EdgeSetAgent = func(on bool) error { set = append(set, on); return nil }
	require.Contains(t, statusOf(edgePress(m, "g")), "now an agent")
	active = true
	require.Contains(t, statusOf(edgePress(m, "g")), "no longer an agent")
	require.Equal(t, []bool{true, false}, set)

	m.hooks.EdgeSetAgent = func(bool) error { return errHostSaysNo }
	require.Contains(t, statusOf(edgePress(m, "g")), "could not change the agent role: the host says no")

	added := 0
	m.hooks.EdgeAddAgent = func() error { added++; return nil }
	require.Contains(t, statusOf(edgePress(m, "+")), "launched a new agent")
	require.Equal(t, 1, added)
	m.hooks.EdgeAddAgent = func() error { return errHostSaysNo }
	require.Contains(t, statusOf(edgePress(m, "+")), "could not add an agent: the host says no")
}

func TestEdgeAddDeviceHandsOverTheJoinCommand(t *testing.T) {
	m := edgeActionsRig(t)
	got := edgePress(m, "s")
	require.Contains(t, statusOf(got), "on the other machine, run: roger join http://192.168.1.9:8791")
	require.Contains(t, statusOf(got), "approve it here with y")
}

func TestEdgeAllowMachineShowsThisMachinesKey(t *testing.T) {
	m := edgeActionsRig(t)
	require.Contains(t, statusOf(edgePress(m, "y")), "roger edge authority allow <key>")
	m.hooks.EdgeSetup = &EdgeSetup{UserKeyHex: func() string { return "c0ffee" }}
	require.Contains(t, statusOf(edgePress(m, "y")), "roger edge authority allow c0ffee")
}

func TestEdgeRenameAMemberOrThisRoger(t *testing.T) {
	m := edgeActionsRig(t)

	// Unwired, for a member and for this roger.
	require.Contains(t, statusOf(edgePress(selectNet(t, m, named("attic-pi")), "n")), "roger edge name attic-pi <name>")
	require.Contains(t, statusOf(edgePress(selectNet(t, m, theMachine), "n")), "cannot rename this roger here")

	// A member: seeded with its name, edited, applied through EdgeName with the node id.
	var renamed []string
	m.hooks.EdgeName = func(id, name string) error { renamed = append(renamed, id+"="+name); return nil }
	sel := selectNet(t, m, named("attic-pi"))
	atticID := sel.edgeNetItems()[sel.edge.netSel].id
	got := edgePress(sel, "n")
	require.True(t, got.edge.renaming)
	require.Equal(t, "attic-pi", got.edge.renameBuf)
	got = edgePress(got, "backspace", "backspace", "backspace", "-", "x", "enter")
	require.Equal(t, []string{atticID + "=attic-x"}, renamed)
	require.Contains(t, statusOf(got), "renamed to attic-x")

	m.hooks.EdgeName = func(string, string) error { return errHostSaysNo }
	got = edgePress(selectNet(t, m, named("attic-pi")), "n", "enter")
	require.Contains(t, statusOf(got), "rename: the host says no")

	// This roger: seeded with its instance name, applied through EdgeRenameSelf.
	var selfNames []string
	m.hooks.EdgeRenameSelf = func(name string) error { selfNames = append(selfNames, name); return nil }
	got = edgePress(selectNet(t, m, theMachine), "n")
	require.Equal(t, "desk", got.edge.renameBuf)
	got = edgePress(got, "2", "enter")
	require.Equal(t, []string{"desk2"}, selfNames)
	require.Contains(t, statusOf(got), "renamed this roger to desk2")

	m.hooks.EdgeRenameSelf = func(string) error { return errHostSaysNo }
	got = edgePress(selectNet(t, m, theMachine), "n", "enter")
	require.Contains(t, statusOf(got), "rename: the host says no")

	// esc cancels, and an emptied name does nothing.
	selfNames = nil
	m.hooks.EdgeRenameSelf = func(name string) error { selfNames = append(selfNames, name); return nil }
	got = edgePress(selectNet(t, m, theMachine), "n", "esc")
	require.False(t, got.edge.renaming)
	got = edgePress(selectNet(t, m, theMachine), "n", "backspace", "backspace", "backspace", "backspace", "enter")
	require.False(t, got.edge.renaming)
	require.Empty(t, selfNames)
}

func TestEdgeForgetAsksThenGoesThroughTheHost(t *testing.T) {
	m := edgeActionsRig(t)
	require.Contains(t, statusOf(edgePress(selectNet(t, m, named("ghost")), "f")), "select one first")
	require.Contains(t, statusOf(edgePress(selectNet(t, m, named("attic-pi")), "f")), "roger edge forget attic-pi")

	var forgot []string
	m.hooks.EdgeForget = func(id string) error { forgot = append(forgot, id); return nil }
	got := edgePress(selectNet(t, m, named("attic-pi")), "f")
	require.Contains(t, statusOf(got), "forget attic-pi?")
	require.Contains(t, statusOf(got), "[y/N]")
	require.Empty(t, forgot, "arming the question forgets nothing")
	require.Contains(t, statusOf(edgePress(got, "n")), "left alone")
	require.Empty(t, forgot)

	got = edgePress(got, "y")
	require.Len(t, forgot, 1)
	require.Contains(t, statusOf(got), "forgot attic-pi")

	m.hooks.EdgeForget = func(string) error { return errHostSaysNo }
	got = edgePress(selectNet(t, m, named("attic-pi")), "f", "y")
	require.Contains(t, statusOf(got), "could not forget attic-pi: the host says no")
}

func TestEdgeActuateCeremonyRefusalsAndHostFailure(t *testing.T) {
	m, _ := actuateModel(t, edge.Claimed)
	// Not pending: a member without an actuate claim.
	notPending, _ := actuateModel(t, edge.Verified)
	require.Contains(t, statusOf(edgePress(notPending, "c")), "waiting on you")

	// Unwired.
	m.hooks.EdgeConfirmActuate = nil
	require.Contains(t, statusOf(edgePress(m, "c")), "roger edge actuate shed-door")

	m.hooks.EdgeConfirmActuate = func(string) error { return errHostSaysNo }
	got := edgePress(m, "c", "y")
	require.Contains(t, statusOf(got), "could not confirm shed-door: the host says no")
}

func TestEdgeAdoptOnTheMapActsOnlyOnACandidate(t *testing.T) {
	m := edgeActionsRig(t)
	require.Contains(t, statusOf(edgePress(selectNet(t, m, named("attic-pi")), "a")), "adopts a DISCOVERED machine")

	cand := nodeOf("c", "new-phone", "mobile")
	cand.Presence = ""
	m.edge.cands = []store.EdgeNode{cand}
	m.hooks.EdgeAdopt = nil
	require.Contains(t, statusOf(edgePress(selectNet(t, m, named("new-phone")), "a")), "cannot adopt")

	m.hooks.EdgeAdopt = func(id, name string) error { return errHostSaysNo }
	require.Contains(t, statusOf(edgePress(selectNet(t, m, named("new-phone")), "a")), "adopt new-phone: the host says no")

	var adopted []string
	m.hooks.EdgeAdopt = func(id, name string) error { adopted = append(adopted, name); return nil }
	got := edgePress(selectNet(t, m, named("new-phone")), "a")
	require.Equal(t, []string{"new-phone"}, adopted)
	// It advertised no certificate pin, so it joins by CLAIM - the message says so.
	require.Contains(t, statusOf(got), "new-phone can now claim its certificate")
}

func TestEdgeBindModelPrompt(t *testing.T) {
	m := edgeActionsRig(t)
	require.Contains(t, statusOf(edgePress(selectNet(t, m, named("ghost")), "b")), "acts on a running roger")
	require.Contains(t, statusOf(edgePress(selectNet(t, m, named("worker")), "b")), "roger edge model worker <model>")

	var jobs []edge.AgentJob
	m.hooks.EdgeSetAgentJob = func(j edge.AgentJob) error { jobs = append(jobs, j); return nil }
	got := edgePress(selectNet(t, m, named("worker")), "b")
	require.True(t, got.edge.binding)
	got = edgePress(got, "q", "w", "x", "backspace", "enter")
	require.Equal(t, []edge.AgentJob{{Name: "worker", Model: "qw", Share: true, Job: "serve"}}, jobs)
	require.Contains(t, statusOf(got), "worker now serves qw on the Edge")

	// esc and an empty model change nothing.
	got = edgePress(selectNet(t, m, named("worker")), "b", "esc")
	require.False(t, got.edge.binding)
	edgePress(selectNet(t, m, named("worker")), "b", "enter")
	require.Len(t, jobs, 1)

	m.hooks.EdgeSetAgentJob = func(edge.AgentJob) error { return errHostSaysNo }
	got = edgePress(selectNet(t, m, named("worker")), "b", "m", "enter")
	require.Contains(t, statusOf(got), "bind: the host says no")
}

func TestEdgeButtonsAreTheSameAsTheirLetters(t *testing.T) {
	m := edgeActionsRig(t)
	added := 0
	m.hooks.EdgeAddAgent = func() error { added++; return nil }
	m.hooks.EdgeSetAgent = func(bool) error { return nil }
	m = selectNet(t, m, theMachine)
	acts := m.edgeActions()
	idx := -1
	for i, a := range acts {
		if a.key == "+" {
			idx = i
		}
	}
	require.GreaterOrEqual(t, idx, 0, "Add Agent is offered once the hook is wired")

	// Tab focuses the first button; shift+tab on it returns to the map.
	got := asModel(mustUpdate(m, tea.KeyMsg{Type: tea.KeyTab}))
	require.True(t, got.edge.actionFocused)
	require.Equal(t, 0, got.edge.actionSel)
	back := asModel(mustUpdate(got, tea.KeyMsg{Type: tea.KeyShiftTab}))
	require.False(t, back.edge.actionFocused)

	// right walks along the buttons, left walks back, never past either end.
	for i := 0; i < idx; i++ {
		got = asModel(mustUpdate(got, tea.KeyMsg{Type: tea.KeyRight}))
	}
	require.Equal(t, idx, got.edge.actionSel)
	got = asModel(mustUpdate(got, tea.KeyMsg{Type: tea.KeyEnter}))
	require.Equal(t, 1, added, "enter on the focused Add Agent button does what + does")
	require.True(t, got.edge.actionFocused, "focus stays on the panel after acting")

	for i := 0; i < len(acts)+2; i++ {
		got = asModel(mustUpdate(got, tea.KeyMsg{Type: tea.KeyRight}))
	}
	require.Equal(t, len(m.edgeActions())-1, got.edge.actionSel)
	for i := 0; i < len(acts)+2; i++ {
		got = asModel(mustUpdate(got, tea.KeyMsg{Type: tea.KeyLeft}))
	}
	require.Equal(t, 0, got.edge.actionSel)
	// shift+tab from a later button steps back one; tab past the last returns to the map.
	got = asModel(mustUpdate(got, tea.KeyMsg{Type: tea.KeyRight}))
	got = asModel(mustUpdate(got, tea.KeyMsg{Type: tea.KeyShiftTab}))
	require.Equal(t, 0, got.edge.actionSel)
	require.True(t, got.edge.actionFocused)
	for i := 0; i < len(acts); i++ {
		got = asModel(mustUpdate(got, tea.KeyMsg{Type: tea.KeyTab}))
	}
	require.False(t, got.edge.actionFocused)
	// up leaves the buttons for the map.
	got = asModel(mustUpdate(got, tea.KeyMsg{Type: tea.KeyTab}))
	got = asModel(mustUpdate(got, tea.KeyMsg{Type: tea.KeyUp}))
	require.False(t, got.edge.actionFocused)
}

func mustUpdate(m model, msg tea.Msg) tea.Model {
	tm, _ := m.Update(msg)
	return tm
}

func TestEdgeOpenNodeDetailFromTheMap(t *testing.T) {
	m := selectNet(t, edgeActionsRig(t), named("attic-pi"))
	got := asModel(mustUpdate(m, tea.KeyMsg{Type: tea.KeyEnter}))
	require.True(t, got.edge.netDetail)
	require.True(t, strings.Contains(stripANSI(got.View()), "attic-pi"))
}
