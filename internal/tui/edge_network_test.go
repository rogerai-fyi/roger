package tui

// Rendering guards for the [3] EDGE network view (features/edge/patchbay.feature): the device
// emblems by kind, the agent sub-node for an operate-capable node, the one-node hub map, and the
// width guarantee across every supported terminal. The godog topology/empty suites drive the
// broad behaviour; these pin the new game-map pieces and the branches those suites do not hit.

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

func networkModel(t *testing.T, self edge.SelfStatus, nodes ...store.EdgeNode) model {
	t.Helper()
	fl := edge.NewFleet(store.NewMem(), "acct-1")
	now := time.Unix(100000, 0)
	fl.SetClock(func() time.Time { return now })
	for _, n := range nodes {
		if _, err := fl.Enroll(n); err != nil {
			t.Fatal(err)
		}
	}
	h := Hooks{Station: "gm-93", EdgeFleet: fl, EdgeSelf: self.Name,
		EdgeCandidates: func() []store.EdgeNode { return nil },
		EdgeStatus:     func() edge.SelfStatus { return self }}
	var tm tea.Model = NewWithHooks("http://b", "t", nil, h)
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 104, Height: 44})
	m := tm.(model)
	m.edgeClock = func() time.Time { return now }
	m.enterEdge()
	return m
}

func nodeOf(id, name, kind string, caps ...store.EdgeCap) store.EdgeNode {
	return store.EdgeNode{
		ID: "n_" + strings.Repeat(id, 48)[:48], Name: name, Kind: kind,
		LastSeen:   time.Unix(100000, 0).Unix() - 3,
		Presence:   "live",
		Transports: []store.EdgeTransport{{Kind: "lan", Addr: "192.168.1.9:8791", Fingerprint: strings.Repeat("ab", 32)}},
		Caps:       caps,
	}
}

func TestNetworkEmblemsAndAgent(t *testing.T) {
	// The GRAPH view (a non-set-up self viewing a fleet): emblems by kind, the agent sub-node, the
	// legend. A SET-UP machine sees the navigable map instead (TestMapAdoptPhone et al.); the graph
	// remains for a machine that is not itself enrolled and for fleets too large to draw as a map.
	self := edge.SelfStatus{Root: edge.RootLocal, Name: "desk", Discovery: edge.DiscoveryScanning, IntervalS: 30}
	mac := nodeOf("a", "studio-mac", "host", store.EdgeCap{Name: "operate", State: string(edge.Verified)})
	mac.Instances = []store.EdgeInstance{{Name: "scout", Agent: true}} // a real agent runs on it
	m := networkModel(t, self,
		mac,
		nodeOf("b", "pi-greenhouse", "board", store.EdgeCap{Name: "sense", State: string(edge.Verified)}),
		nodeOf("c", "pixel-8", "mobile", store.EdgeCap{Name: "classify", State: string(edge.Verified)}),
	)
	out := stripANSI(m.edgeView(104))
	for _, want := range []string{"▣ studio-mac", "▤ pi-greenhouse", "▯ pixel-8", "◉ desk"} {
		if !strings.Contains(out, want) {
			t.Errorf("network view missing %q:\n%s", want, out)
		}
	}
	// The operate-capable node shows an agent sub-node (└◆); exactly one, for the one operate
	// node - the legend's own ◆ is not a sub-node.
	if strings.Count(out, "└◆") != 1 {
		t.Errorf("expected exactly one agent sub-node (└◆):\n%s", out)
	}
	if !strings.Contains(out, "agent") {
		t.Errorf("agent sub-node missing its label:\n%s", out)
	}
	// The NODES legend explains the emblems.
	if !strings.Contains(out, "NODES") {
		t.Errorf("no emblem legend:\n%s", out)
	}
}

func TestNetworkHubMapOneNode(t *testing.T) {
	self := edge.SelfStatus{Root: edge.RootLocal, Authority: "desk", AuthorityLocal: true, AuthorityHere: true,
		AuthorityAddr: "http://192.168.1.69:33537", Enrolled: true, Name: "desk",
		Discovery: edge.DiscoveryScanning, IntervalS: 30}
	m := networkModel(t, self)
	out := stripANSI(m.edgeView(104))
	for _, want := range []string{"YOUR EDGE NETWORK", "the authority controls your", "◉ desk", "no rogers running here", "WHAT YOU CAN DO"} {
		if !strings.Contains(out, want) {
			t.Errorf("hub map missing %q:\n%s", want, out)
		}
	}
}

func TestNetworkRunningHere(t *testing.T) {
	self := edge.SelfStatus{Root: edge.RootLocal, Authority: "RogGentooEdged", AuthorityLocal: true, AuthorityHere: true,
		Enrolled: true, Name: "RogGentooEdged", Discovery: edge.DiscoveryScanning, IntervalS: 30}
	m := networkModel(t, self)
	m.hooks.EdgeHousehold = func() []edge.Instance {
		return []edge.Instance{
			{Name: "desk", Bands: []string{"grok-4.5"}},
			{Name: "scout", Agent: true},
		}
	}
	m.hooks.EdgeSelfInstance = func() string { return "scout" } // you are looking at the agent
	m.enterEdge()
	out := stripANSI(m.edgeView(104))
	// The rogers running here are drawn as cards on the topology, hung under the machine: the
	// serving one as ● with its band, the agent as ◆. This is the "add agent got represented"
	// the founder asked to SEE (2026-09-21).
	if !strings.Contains(out, "● desk") || !strings.Contains(out, "grok-4.5") {
		t.Errorf("serving instance card not shown with its band:\n%s", out)
	}
	if !strings.Contains(out, "◆ scout") {
		t.Errorf("agent instance not drawn as an agent card:\n%s", out)
	}
	// The one you are looking at is marked "this one".
	if !strings.Contains(out, "this one") {
		t.Errorf("the roger you are looking at is not marked:\n%s", out)
	}
}

func TestNetworkPersistentAgentDark(t *testing.T) {
	self := edge.SelfStatus{Root: edge.RootLocal, Authority: "desk", AuthorityLocal: true, AuthorityHere: true,
		Enrolled: true, Name: "desk", Discovery: edge.DiscoveryScanning, IntervalS: 30}
	m := networkModel(t, self)
	// "desk" is live; "scout" is a persistent agent whose process has stopped.
	m.hooks.EdgeHousehold = func() []edge.Instance { return []edge.Instance{{Name: "desk"}} }
	m.hooks.EdgePersistentAgents = func() []edge.PersistentAgent {
		return []edge.PersistentAgent{{Name: "desk"}, {Name: "scout"}}
	}
	m.hooks.EdgeSelfInstance = func() string { return "desk" }
	m.hooks.EdgeResumeAgent = func(string) error { return nil }
	m.hooks.EdgeRemoveAgent = func(string) error { return nil }
	m.enterEdge()
	out := stripANSI(m.edgeView(104))
	// A stopped persistent agent is drawn as a dark ◇ card, kept, marked offline.
	if !strings.Contains(out, "◇ scout") || !strings.Contains(out, "offline") {
		t.Errorf("a stopped persistent agent is not drawn dark and offline:\n%s", out)
	}
	// "desk" is live and persistent - it is drawn once (live), never repeated as dark.
	if strings.Contains(out, "◇ desk") {
		t.Errorf("a live persistent agent was also drawn as dark:\n%s", out)
	}
	// Selecting the dark agent (it is the last node) offers Resume and Remove, in place.
	m.edge.netSel = len(m.edgeNetItems()) - 1
	m.edge.netSelSet = true
	out = stripANSI(m.edgeView(104))
	if !strings.Contains(out, "scout · offline agent") {
		t.Errorf("the panel does not name the selected dark agent:\n%s", out)
	}
	if !strings.Contains(out, "Resume") || !strings.Contains(out, "Remove") {
		t.Errorf("no resume/remove offered for the selected dark agent:\n%s", out)
	}
	// Pressing u resumes it, x removes it - both act on the selected node.
	resumed := false
	m.hooks.EdgeResumeAgent = func(n string) error { resumed = n == "scout"; return nil }
	tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	m = asModel(tm)
	if !resumed {
		t.Errorf("pressing u did not resume the selected dark agent")
	}
}

func TestNetworkSelfNotDuplicatedAsRow(t *testing.T) {
	// A stale self node record must not be drawn as a ghost row below the hub: this machine is
	// the hub, drawn once.
	self := edge.SelfStatus{Root: edge.RootLocal, Authority: "RogGentooEdged", AuthorityLocal: true, AuthorityHere: true,
		Enrolled: true, Name: "RogGentooEdged", Discovery: edge.DiscoveryScanning, IntervalS: 30}
	stale := nodeOf("s", "RogGentooEdged", "host")
	stale.Presence = string(edge.PresenceDark)
	m := networkModel(t, self, stale)
	out := stripANSI(m.edgeView(104))
	if strings.Contains(out, "DARK") {
		t.Errorf("the stale self record was drawn as a dark ghost row:\n%s", out)
	}
	// With self excluded, the one-node hub map is shown, not a graph of a ghost.
	if !strings.Contains(out, "YOUR EDGE NETWORK") {
		t.Errorf("expected the hub map once self is excluded:\n%s", out)
	}
}

func TestNetworkWidthHolds(t *testing.T) {
	self := edge.SelfStatus{Root: edge.RootLocal, Authority: "desk", AuthorityLocal: true, AuthorityHere: true,
		Enrolled: true, Name: "desk", Discovery: edge.DiscoveryScanning, IntervalS: 30}
	m := networkModel(t, self,
		nodeOf("a", "studio-mac", "host", store.EdgeCap{Name: "operate", State: string(edge.Verified)}),
		nodeOf("b", "pi-greenhouse-sensor-array", "board"),
	)
	for _, w := range []int{120, 100, 80, 60, 48} {
		out := stripANSI(m.edgeView(w))
		for i, ln := range strings.Split(out, "\n") {
			if lipgloss.Width(ln) > w {
				t.Fatalf("w=%d line %d is %d wide: %q", w, i, lipgloss.Width(ln), ln)
			}
		}
	}
}

func TestNetworkControlPanel(t *testing.T) {
	self := edge.SelfStatus{Root: edge.RootLocal, Authority: "desk", AuthorityLocal: true, AuthorityHere: true,
		AuthorityAddr: "http://192.168.1.9:8791", Enrolled: true, Name: "desk", Discovery: edge.DiscoveryScanning, IntervalS: 30}
	m := networkModel(t, self)
	agent := false
	m.hooks.EdgeHousehold = func() []edge.Instance { return []edge.Instance{{Name: "desk", Agent: agent}} }
	m.hooks.EdgeSelfInstance = func() string { return "desk" }
	m.hooks.EdgeAgentActive = func() bool { return agent }
	m.hooks.EdgeSetAgent = func(on bool) error { agent = on; return nil }
	m.hooks.EdgeAddAgent = func() error { return nil }
	m.hooks.EdgeRenameSelf = func(string) error { return nil }
	m.enterEdge()

	// The highlight defaults to THIS roger, so its actions - Make Agent, Rename - are right there.
	out := stripANSI(m.edgeView(100))
	for _, want := range []string{"WHAT YOU CAN DO", "desk · this roger", "Make Agent", "Rename"} {
		if !strings.Contains(out, want) {
			t.Errorf("control panel (this roger selected) missing %q:\n%s", want, out)
		}
	}

	// Pressing g makes this roger an agent, in place - the button label flips to Stop Agent.
	tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	m = asModel(tm)
	if !agent {
		t.Fatalf("pressing g did not make this roger an agent")
	}
	out = stripANSI(m.edgeView(100))
	if !strings.Contains(out, "Stop Agent") {
		t.Errorf("after making an agent, the button does not offer to stop:\n%s", out)
	}
	if !strings.Contains(out, "◆ desk") {
		t.Errorf("the agent is not drawn ◆ after the in-place toggle:\n%s", out)
	}

	// Moving the highlight to the machine (index 0) swaps in the network-growth actions.
	m.edge.netSel = 0
	m.edge.netSelSet = true
	out = stripANSI(m.edgeView(100))
	for _, want := range []string{"this machine", "Add Agent", "Add Device", "Allow Machine"} {
		if !strings.Contains(out, want) {
			t.Errorf("control panel (machine selected) missing %q:\n%s", want, out)
		}
	}
}

func TestNetworkRenameFlow(t *testing.T) {
	self := edge.SelfStatus{Root: edge.RootLocal, Authority: "desk", AuthorityLocal: true, AuthorityHere: true,
		Enrolled: true, Name: "desk", Discovery: edge.DiscoveryScanning, IntervalS: 30}
	m := networkModel(t, self)
	renamedTo := ""
	m.hooks.EdgeSelfInstance = func() string { return "desk" }
	m.hooks.EdgeRenameSelf = func(n string) error { renamedTo = n; return nil }
	m.enterEdge()

	// n opens the rename prompt, seeded with the current name.
	tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = asModel(tm)
	if !m.edge.renaming {
		t.Fatalf("n did not open the rename prompt")
	}
	if !strings.Contains(stripANSI(m.edgeView(100)), "RENAME THIS ROGER") {
		t.Errorf("rename prompt not drawn")
	}
	// clear and type a new name, then enter.
	for range "desk" {
		tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		m = asModel(tm)
	}
	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("greenhouse")})
	m = asModel(tm)
	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = asModel(tm)
	if renamedTo != "greenhouse" {
		t.Fatalf("rename applied %q, want greenhouse", renamedTo)
	}
	if m.edge.renaming {
		t.Errorf("rename prompt stayed open after apply")
	}
}

// TestTopologyNavigation drives the founder's "control the highlighted box" (2026-09-21): the
// arrow keys move the highlight across the machine and the rogers on it, and the WHAT YOU CAN DO
// panel swaps to the actions that fit the SELECTED node - machine actions, this-roger actions, or
// a dark agent's resume/remove.
func TestTopologyNavigation(t *testing.T) {
	self := edge.SelfStatus{Root: edge.RootLocal, Authority: "hub", AuthorityLocal: true, AuthorityHere: true,
		AuthorityAddr: "http://192.168.1.9:8791", Enrolled: true, Name: "hub", Discovery: edge.DiscoveryScanning, IntervalS: 30}
	m := networkModel(t, self)
	m.hooks.EdgeHousehold = func() []edge.Instance { return []edge.Instance{{Name: "hub", Agent: false}} }
	m.hooks.EdgePersistentAgents = func() []edge.PersistentAgent { return []edge.PersistentAgent{{Name: "sleeper"}} }
	m.hooks.EdgeSelfInstance = func() string { return "hub" }
	m.hooks.EdgeAgentActive = func() bool { return false }
	m.hooks.EdgeSetAgent = func(bool) error { return nil }
	m.hooks.EdgeAddAgent = func() error { return nil }
	m.hooks.EdgeRenameSelf = func(string) error { return nil }
	m.hooks.EdgeResumeAgent = func(string) error { return nil }
	m.hooks.EdgeRemoveAgent = func(string) error { return nil }
	m.enterEdge()

	// Nodes are: [0] machine "hub", [1] this roger "hub", [2] dark agent "sleeper".
	// The default highlight is THIS roger - Make Agent + Rename are offered.
	out := stripANSI(m.edgeView(100))
	if !strings.Contains(out, "hub · this roger") || !strings.Contains(out, "Make Agent") {
		t.Errorf("default highlight is not this roger with its actions:\n%s", out)
	}

	// Up (k) moves to the machine - Add Agent, Add Device, Allow Machine.
	tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	m = asModel(tm)
	out = stripANSI(m.edgeView(100))
	if !strings.Contains(out, "hub · this machine") || !strings.Contains(out, "Add Agent") || !strings.Contains(out, "Add Device") {
		t.Errorf("moving up did not select the machine with its actions:\n%s", out)
	}

	// Down twice reaches the dark agent - Resume, Remove.
	for i := 0; i < 2; i++ {
		tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		m = asModel(tm)
	}
	out = stripANSI(m.edgeView(100))
	if !strings.Contains(out, "sleeper · offline agent") || !strings.Contains(out, "Resume") || !strings.Contains(out, "Remove") {
		t.Errorf("moving down did not select the dark agent with resume/remove:\n%s", out)
	}

	// x removes it: the durable record is dropped and the highlight steps back off the gone node.
	removed := ""
	m.hooks.EdgeRemoveAgent = func(n string) error { removed = n; return nil }
	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	m = asModel(tm)
	if removed != "sleeper" {
		t.Errorf("x did not remove the selected dark agent, got %q", removed)
	}

	// The highlight never runs past the ends: many downs clamp at the last node.
	for i := 0; i < 10; i++ {
		tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		m = asModel(tm)
	}
	if got := m.edge.netSel; got >= len(m.edgeNetItems()) {
		t.Errorf("selection %d ran past the %d nodes", got, len(m.edgeNetItems()))
	}
}

// TestNetDetail drives ⏎ on a map node: the readout answers "what is this node doing, what model,
// use or share?" (2026-09-21) - the job, the shared/used models, the capabilities, the resources -
// and esc returns to the map.
func TestNetDetail(t *testing.T) {
	self := edge.SelfStatus{Root: edge.RootLocal, Authority: "hub", AuthorityLocal: true, AuthorityHere: true,
		AuthorityAddr: "http://192.168.1.9:8791", Enrolled: true, Name: "hub", Discovery: edge.DiscoveryScanning, IntervalS: 30}
	m := networkModel(t, self)
	m.hooks.EdgeHousehold = func() []edge.Instance {
		return []edge.Instance{
			{Name: "hub", Agent: true, Caps: []store.EdgeCap{{Name: "operate", State: "claimed"}},
				Resources: &store.EdgeResources{GPU: "RTX 4090", RAMUsedGB: 8, RAMTotalGB: 64}, Port: 8791},
			{Name: "worker", Bands: []string{"qwen3-30b"}},
		}
	}
	m.hooks.EdgeSelfInstance = func() string { return "hub" }
	m.enterEdge()

	// ⏎ on this roger (the agent) opens its readout: job, models, capabilities, resources.
	tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = asModel(tm)
	if !m.edge.netDetail {
		t.Fatalf("enter did not open the node detail")
	}
	out := stripANSI(m.edgeView(100))
	for _, want := range []string{"agent · this roger", "DOES", "agent · can act on other nodes", "JOB", "none assigned", "USES", "CAN DO", "operate", "GPU", "RTX 4090", "RAM"} {
		if !strings.Contains(out, want) {
			t.Errorf("agent detail missing %q:\n%s", want, out)
		}
	}

	// esc returns to the map (not out of the Edge screen).
	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = asModel(tm)
	if m.edge.netDetail {
		t.Errorf("esc did not close the node detail")
	}
	if m.mode != modeEdge {
		t.Errorf("esc from detail left the Edge screen entirely")
	}

	// Move to the serving worker and open it: it SHARES its band to the Edge.
	m.edge.netSel = 2 // machine[0], hub[1], worker[2]
	m.edge.netSelSet = true
	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = asModel(tm)
	out = stripANSI(m.edgeView(100))
	if !strings.Contains(out, "SHARES") || !strings.Contains(out, "qwen3-30b") {
		t.Errorf("serving instance detail does not show the shared model:\n%s", out)
	}
}

// TestBindModelFlow drives the in-place model binding (jobs.feature, 2026-09-21): the Bind Model
// button opens a prompt, typing a model and confirming puts it on air for the selected agent, and
// the card + detail then show the agent SERVING that model instead of being idle.
func TestBindModelFlow(t *testing.T) {
	self := edge.SelfStatus{Root: edge.RootLocal, Authority: "hub", AuthorityLocal: true, AuthorityHere: true,
		Enrolled: true, Name: "hub", Discovery: edge.DiscoveryScanning, IntervalS: 30}
	m := networkModel(t, self)
	jobs := map[string]edge.AgentJob{}
	m.hooks.EdgeHousehold = func() []edge.Instance {
		return []edge.Instance{{Name: "hub", Agent: true}, {Name: "worker", Agent: true}}
	}
	m.hooks.EdgeSelfInstance = func() string { return "hub" }
	m.hooks.EdgeAgentActive = func() bool { return true }
	m.hooks.EdgeSetAgent = func(bool) error { return nil }
	m.hooks.EdgeAgentJobs = func() []edge.AgentJob {
		out := []edge.AgentJob{}
		for _, j := range jobs {
			out = append(out, j)
		}
		return out
	}
	m.hooks.EdgeSetAgentJob = func(j edge.AgentJob) error { jobs[j.Name] = j; return nil }
	m.enterEdge()

	// Select the worker agent (machine[0], hub[1], worker[2]); idle, it offers Bind Model.
	m.edge.netSel = 2
	m.edge.netSelSet = true
	out := stripANSI(m.edgeView(100))
	if !strings.Contains(out, "Bind Model") {
		t.Fatalf("no Bind Model button for the idle agent:\n%s", out)
	}
	if !strings.Contains(out, "agent · idle") {
		t.Errorf("an idle agent is not marked idle on its card:\n%s", out)
	}

	// b opens the prompt.
	tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	m = asModel(tm)
	if !m.edge.binding {
		t.Fatalf("b did not open the bind prompt")
	}
	if !strings.Contains(stripANSI(m.edgeView(100)), "PUT A MODEL ON AIR FOR WORKER") {
		t.Errorf("the bind prompt is not shown for the selected agent")
	}

	// Type a model and confirm.
	for _, r := range "qwen3-30b" {
		tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = asModel(tm)
	}
	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = asModel(tm)
	if m.edge.binding {
		t.Fatalf("enter did not close the bind prompt")
	}
	j, ok := jobs["worker"]
	if !ok || j.Model != "qwen3-30b" || !j.Share || j.Job != "serve" {
		t.Fatalf("the binding was not set as serve+share: %+v", j)
	}

	// The worker card now shows it serving, not idle.
	out = stripANSI(m.edgeView(100))
	if !strings.Contains(out, "serving qwen3-30b") && !strings.Contains(out, "serving qwen3") {
		t.Errorf("the card does not show the agent serving after binding:\n%s", out)
	}

	// esc from a fresh prompt cancels without change.
	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	m = asModel(tm)
	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = asModel(tm)
	if m.edge.binding {
		t.Errorf("esc did not cancel the bind prompt")
	}
}

// TestMapAdoptPhone drives the founder's phone story (2026-09-21): a roger on a phone on the LAN
// appears on the map as a DISCOVERED candidate, is navigable, its detail says it is not on the
// Edge, and pressing a adopts it onto RogGentooEdged.
func TestMapAdoptPhone(t *testing.T) {
	self := edge.SelfStatus{Root: edge.RootLocal, Authority: "RogGentooEdged", AuthorityLocal: true, AuthorityHere: true,
		AuthorityAddr: "http://192.168.1.9:8791", Enrolled: true, Name: "RogGentooEdged", Discovery: edge.DiscoveryScanning, IntervalS: 30}
	phone := nodeOf("p", "pixel-8", "mobile", store.EdgeCap{Name: "classify", State: string(edge.Verified)})
	phone.Presence = string(edge.PresenceCandidate)
	cands := []store.EdgeNode{phone}
	m := networkModel(t, self)
	m.hooks.EdgeCandidates = func() []store.EdgeNode { return cands }
	m.hooks.EdgeHousehold = func() []edge.Instance { return []edge.Instance{{Name: "RogGentooEdged", Agent: true}} }
	m.hooks.EdgeSelfInstance = func() string { return "RogGentooEdged" }
	adopted := ""
	m.hooks.EdgeAdopt = func(id, name string) error { adopted = name; cands = nil; return nil }
	m.enterEdge()

	// The map (not the graph) is on screen even though a candidate exists, and it draws the phone
	// in a DISCOVERED band with the adopt hint.
	if !m.edgeMapActive() {
		t.Fatalf("the map is not active with a candidate present")
	}
	out := stripANSI(m.edgeView(100))
	for _, want := range []string{"DISCOVERED", "▯ pixel-8", "adopt with a", "YOUR EDGE NETWORK"} {
		if !strings.Contains(out, want) {
			t.Errorf("the phone candidate is not drawn on the map: missing %q\n%s", want, out)
		}
	}

	// Navigate to the candidate (it is the last node) and open its detail.
	m.edge.netSel = len(m.edgeNetItems()) - 1
	m.edge.netSelSet = true
	it, _ := m.edgeSelectedNet()
	if !it.cand || it.name != "pixel-8" {
		t.Fatalf("the last node is not the phone candidate: %+v", it)
	}
	m.edge.netDetail = true
	out = stripANSI(m.edgeView(100))
	if !strings.Contains(out, "not on your Edge") {
		t.Errorf("the candidate detail does not say it is not on the Edge:\n%s", out)
	}
	m.edge.netDetail = false

	// The panel offers Adopt for the selected candidate.
	out = stripANSI(m.edgeView(100))
	if !strings.Contains(out, "pixel-8 · discovered") || !strings.Contains(out, "Adopt") {
		t.Errorf("no Adopt action for the selected candidate:\n%s", out)
	}

	// a adopts it.
	tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = asModel(tm)
	if adopted != "pixel-8" {
		t.Fatalf("a did not adopt the phone, got %q", adopted)
	}
}

// TestMapBandsWidthHolds guards that the map with a fleet band and a discovered band never
// overflows any supported width - the new cards and headings included.
func TestMapBandsWidthHolds(t *testing.T) {
	self := edge.SelfStatus{Root: edge.RootLocal, Authority: "RogGentooEdged", AuthorityLocal: true, AuthorityHere: true,
		AuthorityAddr: "http://192.168.1.9:8791", Enrolled: true, Name: "RogGentooEdged", Discovery: edge.DiscoveryScanning, IntervalS: 30}
	phone := nodeOf("p", "pixel-8", "mobile")
	phone.Presence = string(edge.PresenceCandidate)
	m := networkModel(t, self, nodeOf("s", "studio-mac", "host"), nodeOf("b", "pi-greenhouse-sensor", "board"))
	m.hooks.EdgeCandidates = func() []store.EdgeNode { return []store.EdgeNode{phone} }
	m.hooks.EdgeHousehold = func() []edge.Instance { return []edge.Instance{{Name: "RogGentooEdged", Agent: true}} }
	m.hooks.EdgeSelfInstance = func() string { return "RogGentooEdged" }
	m.hooks.EdgeAdopt = func(id, name string) error { return nil }
	m.enterEdge()
	for _, w := range []int{120, 100, 80, 64, 48} {
		out := stripANSI(m.edgeView(w))
		for i, ln := range strings.Split(out, "\n") {
			if lipgloss.Width(ln) > w {
				t.Fatalf("w=%d line %d is %d wide: %q", w, i, lipgloss.Width(ln), ln)
			}
		}
	}
}

// TestEdgeArrowsStayInScreen is the regression for the 2026-09-23 snag: pressing left/right while
// navigating the Edge map must move the highlight, NOT fall through to the preset bank and jump to
// another tab. Before the fix, right went to [4] config.
func TestEdgeArrowsStayInScreen(t *testing.T) {
	self := edge.SelfStatus{Root: edge.RootLocal, Authority: "hub", AuthorityLocal: true, AuthorityHere: true,
		Enrolled: true, Name: "hub", Discovery: edge.DiscoveryScanning, IntervalS: 30}
	m := networkModel(t, self)
	m.hooks.EdgeHousehold = func() []edge.Instance { return []edge.Instance{{Name: "hub", Agent: true}} }
	m.hooks.EdgeSelfInstance = func() string { return "hub" }
	m.hooks.EdgeAgentActive = func() bool { return true }
	m.hooks.EdgeSetAgent = func(bool) error { return nil }
	m.enterEdge()
	for _, ty := range []tea.KeyType{tea.KeyRight, tea.KeyLeft} {
		tm, _ := m.Update(tea.KeyMsg{Type: ty})
		m = asModel(tm)
		if m.mode != modeEdge {
			t.Fatalf("arrow %v left the Edge screen (mode=%v) - it must stay and move the highlight", ty, m.mode)
		}
	}
}

// TestEdgeActionFocus drives the founder's instinct: from a node, reach the WHAT YOU CAN DO buttons
// with the keyboard, move across them, and run one with enter - all without leaving the screen.
func TestEdgeActionFocus(t *testing.T) {
	self := edge.SelfStatus{Root: edge.RootLocal, Authority: "hub", AuthorityLocal: true, AuthorityHere: true,
		Enrolled: true, Name: "hub", Discovery: edge.DiscoveryScanning, IntervalS: 30}
	m := networkModel(t, self)
	m.hooks.EdgeHousehold = func() []edge.Instance { return []edge.Instance{{Name: "hub", Agent: true}} }
	m.hooks.EdgeSelfInstance = func() string { return "hub" }
	m.hooks.EdgeAgentActive = func() bool { return true }
	m.hooks.EdgeSetAgent = func(bool) error { return nil }
	bound := ""
	m.hooks.EdgeSetAgentJob = func(j edge.AgentJob) error { bound = j.Model; return nil }
	m.enterEdge()

	// Default highlight is this roger (the last node); down drops focus into the buttons.
	tm, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = asModel(tm)
	if !m.edge.actionFocused {
		t.Fatalf("down from the last node did not move focus into the action buttons")
	}
	// The buttons for this roger are: g Make/Stop Agent, b Bind Model, n Rename. Right lands on Bind.
	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = asModel(tm)
	acts := m.edgeActions()
	if acts[m.edge.actionSel].key != "b" {
		t.Fatalf("right did not highlight Bind Model, got %q", acts[m.edge.actionSel].key)
	}
	out := stripANSI(m.edgeView(92))
	if !strings.Contains(out, "⏎ run") {
		t.Errorf("the panel does not show the focused-action hint:\n%s", out)
	}
	// Enter runs the focused button (Bind Model) - opens the inline bind prompt.
	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = asModel(tm)
	if !m.edge.binding {
		t.Fatalf("enter on the focused Bind Model button did not open the bind prompt")
	}
	// Type a model and confirm; the binding lands.
	for _, r := range "pico" {
		tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = asModel(tm)
	}
	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = asModel(tm)
	if bound != "pico" {
		t.Errorf("the focused-button flow did not bind the model, got %q", bound)
	}
	// Up returns focus to the node map.
	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown}) // back into actions
	m = asModel(tm)
	tm, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = asModel(tm)
	if m.edge.actionFocused {
		t.Errorf("up did not return focus to the node map")
	}
}

// TestEdgeFindDevicesHint checks the screen explains how a device appears when none is discovered.
func TestEdgeFindDevicesHint(t *testing.T) {
	self := edge.SelfStatus{Root: edge.RootLocal, Authority: "hub", AuthorityLocal: true, AuthorityHere: true,
		AuthorityAddr: "http://192.168.1.9:8791", Enrolled: true, Name: "hub", Discovery: edge.DiscoveryScanning, IntervalS: 30}
	m := networkModel(t, self)
	m.hooks.EdgeHousehold = func() []edge.Instance { return []edge.Instance{{Name: "hub", Agent: true}} }
	m.hooks.EdgeSelfInstance = func() string { return "hub" }
	m.enterEdge()
	out := stripANSI(m.edgeView(100))
	for _, want := range []string{"no devices yet", "running roger on this network appears here to adopt"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing find-devices hint %q:\n%s", want, out)
		}
	}
}

// TestAdoptedMessageBranchesOnCert checks the adopt confirmation is honest: a phone (no advertised
// certificate) is authorised to CLAIM, not yet a member, so the message says so.
func TestAdoptedMessageBranchesOnCert(t *testing.T) {
	claim := stripANSI(edgeAdoptedMessage("pixel-8", ""))
	if !strings.Contains(claim, "can now claim") || !strings.Contains(claim, "checks in") {
		t.Errorf("a no-cert candidate should be told it can claim, got: %q", claim)
	}
	member := stripANSI(edgeAdoptedMessage("studio-mac", "deadbeef"))
	if !strings.Contains(member, "adopted onto your Edge") {
		t.Errorf("a serving candidate should read as adopted, got: %q", member)
	}
}
