package tui

// The [3] map has to be READABLE by somebody who did not build it. These are the four faults the
// founder hit on 2026-09-27, each pinned so it cannot come back:
//
//  1. the box you are sitting at was titled with a 48-character node id;
//  2. nothing on a card said whether it was a PC, a phone, a board or a roger;
//  3. the hub's own line reused "THIS MACHINE", which is also a fact label in the readout below;
//  4. moving onto a node the build cannot act on in place read "nothing to do on this node here".

import (
	"strings"
	"testing"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

// mapReadableModel is a set-up authority with one of each kind on it: a PC, a board, a phone whose
// record carries no name (so its "name" is its id), a second roger, a dark agent, and a phone
// waiting to be adopted.
func mapReadableModel(t *testing.T) model {
	t.Helper()
	self := edge.SelfStatus{Root: edge.RootLocal, Authority: "RogGentooEdged", AuthorityLocal: true,
		AuthorityHere: true, AuthorityAddr: "http://192.168.1.9:8791", Enrolled: true,
		Name: "RogGentooEdged", Discovery: edge.DiscoveryScanning, IntervalS: 30}
	nameless := nodeOf("c", "", "mobile")
	nameless.Name = nameless.ID // a machine enrolled without a name carries its id as its record name
	m := networkModel(t, self, nodeOf("a", "studio-mac", "host"), nodeOf("b", "pi-greenhouse", "board"), nameless)
	phone := nodeOf("p", "pixel-8", "mobile")
	phone.Presence = string(edge.PresenceCandidate)
	m.hooks.EdgeCandidates = func() []store.EdgeNode { return []store.EdgeNode{phone} }
	m.hooks.EdgeHousehold = func() []edge.Instance {
		return []edge.Instance{{Name: "RogGentooEdged", Agent: true}, {Name: "worker", Agent: true}}
	}
	m.hooks.EdgeSelfInstance = func() string { return "RogGentooEdged" }
	m.hooks.EdgePersistentAgents = func() []edge.PersistentAgent { return []edge.PersistentAgent{{Name: "scout"}} }
	m.hooks.EdgeAdopt = func(id, name string) error { return nil }
	m.hooks.EdgeName = func(id, name string) error { return nil }
	m.hooks.EdgeForget = func(id string) error { return nil }
	m.enterEdge()
	return m
}

func TestEveryCardSaysWhatTheThingIs(t *testing.T) {
	m := mapReadableModel(t)
	out := stripANSI(m.edgeView(116))
	// Hardware in capitals, software in lower case, so the two are never confused at a glance.
	for _, want := range []string{"PC · on your Edge", "BOARD · on your Edge", "PHONE · on your Edge",
		"PHONE · adopt with a", "agent · idle", "agent · offline"} {
		if !strings.Contains(out, want) {
			t.Errorf("the map does not say what a unit IS: missing %q\n%s", want, out)
		}
	}
	// The hub says you are here, and NOT "THIS MACHINE" - that is the readout's fact label, and two
	// lines with the same words is what made this screen hard to read.
	if !strings.Contains(out, "YOU ARE HERE · authority") {
		t.Errorf("the hub card does not say you are here:\n%s", out)
	}
	if strings.Count(out, "THIS MACHINE") > 1 {
		t.Errorf("THIS MACHINE appears more than once, so the fact label is ambiguous:\n%s", out)
	}
}

// The screen says how many of each thing is on it before it draws any of them.
func TestTheMapCountsWhatIsOnIt(t *testing.T) {
	m := mapReadableModel(t)
	out := stripANSI(m.edgeView(116))
	for _, want := range []string{"3 machines on your Edge", "1 device waiting to be adopted", "2 rogers running here"} {
		if !strings.Contains(out, want) {
			t.Errorf("the map does not count what is on it: missing %q\n%s", want, out)
		}
	}
	// A clause with nothing in it is left out rather than reading "0 machines".
	if got := edgeMapTally([]edgeNetItem{{machine: true}}); got != "" {
		t.Errorf("a bare machine should tally nothing, got %q", got)
	}
	if got := edgeMapTally([]edgeNetItem{{machine: true}, {member: true}, {agent: true}}); got != "1 machine on your Edge  ·  1 roger running here" {
		t.Errorf("singulars are wrong: %q", got)
	}
}

func TestANodesIdIsNeverDrawnAsItsName(t *testing.T) {
	m := mapReadableModel(t)
	out := stripANSI(m.edgeView(116))
	for _, ln := range strings.Split(out, "\n") {
		for _, w := range strings.Fields(ln) {
			if strings.HasPrefix(w, "n_") && len(w) > len(edge.ShortID("n_"+strings.Repeat("c", 48))) {
				t.Errorf("a full node id is drawn on the screen (%q):\n%s", w, out)
			}
		}
	}
}

func TestThePanelIsNeverADeadEnd(t *testing.T) {
	m := mapReadableModel(t)
	items := m.edgeNetItems()
	for i, it := range items {
		m.edge.netSel, m.edge.netSelSet = i, true
		out := stripANSI(m.edgeView(116))
		if !it.member && strings.Contains(out, "nothing to do on this node here") {
			t.Errorf("node %d (%s) is a dead end:\n%s", i, it.name, out)
		}
		if !strings.Contains(out, "WHAT YOU CAN DO") {
			t.Errorf("node %d (%s) has no control panel at all:\n%s", i, it.name, out)
		}
	}
}

func TestAWaitingDeviceIsWhereTheCursorLands(t *testing.T) {
	m := mapReadableModel(t)
	it, _ := m.edgeSelectedNet()
	if !it.cand {
		t.Fatalf("the default highlight is not the device waiting to be adopted, it is %q", it.name)
	}
	out := stripANSI(m.edgeView(116))
	if !strings.Contains(out, "a  Adopt") {
		t.Errorf("Adopt is not one keystroke away on opening:\n%s", out)
	}
	// With nothing waiting, the highlight is THIS roger, whose actions are then to hand without
	// moving (features/edge/agents.feature).
	m.hooks.EdgeCandidates = func() []store.EdgeNode { return nil }
	m.edge.netSelSet = false
	m.refreshEdge()
	if it, _ := m.edgeSelectedNet(); !it.self {
		t.Errorf("with nothing waiting the highlight is not this roger, it is %q", it.name)
	}
}

func TestTheJoinAddressIsOnTheScreen(t *testing.T) {
	// An owner must never have to remember where their own Edge lives. With nothing discovered, the
	// screen prints the address a phone points at.
	self := edge.SelfStatus{Root: edge.RootLocal, Authority: "hub", AuthorityLocal: true, AuthorityHere: true,
		AuthorityAddr: "http://192.168.1.9:8791", Enrolled: true, Name: "hub",
		Discovery: edge.DiscoveryScanning, IntervalS: 30}
	m := networkModel(t, self)
	m.hooks.EdgeHousehold = func() []edge.Instance { return []edge.Instance{{Name: "hub", Agent: true}} }
	m.hooks.EdgeSelfInstance = func() string { return "hub" }
	m.enterEdge()
	out := stripANSI(m.edgeView(100))
	for _, want := range []string{"NO DEVICES YET", "http://192.168.1.9:8791", "open the Roger app"} {
		if !strings.Contains(out, want) {
			t.Errorf("the join address is not on the screen: missing %q\n%s", want, out)
		}
	}
}
