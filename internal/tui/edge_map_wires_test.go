package tui

// THE MAP IS A GRAPH, and its wires are the founder's ruling for this screen: "a graphical
// animated ascii graph representing how your Edge is connected". The rule the whole screen obeys
// (features/edge/topology_view.feature) is that it NEVER DRAWS A LINK IT HAS NOT SEEN, so each
// member hangs on a wire that says how it is really reached, and a heartbeat moves on that wire.

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

func wireOf(t *testing.T, n store.EdgeNode) string {
	t.Helper()
	g, _ := edgeDropWire(edgeNetItem{member: true, node: &n, id: n.ID})
	return g
}

func TestAWireIsOnlyDrawnAsGoodAsTheLinkItHasSeen(t *testing.T) {
	lan := nodeOf("a", "studio-mac", "host")
	require := func(what, got, want string) {
		if got != want {
			t.Errorf("%s wire = %q, want %q", what, got, want)
		}
	}
	require("a LAN-direct member's", wireOf(t, lan), "│")

	relayed := nodeOf("b", "shed-pi", "board")
	relayed.Transports = nil // nothing this machine can dial directly: the path goes through a relay
	require("a relayed member's", wireOf(t, relayed), "┊")

	dark := nodeOf("c", "quiet-pi", "board")
	dark.Presence = string(edge.PresenceDark)
	require("a member we have stopped hearing from", wireOf(t, dark), "╎")

	// A roger on THIS machine has no network between it and the hub, so there is nothing to doubt.
	g, _ := edgeDropWire(edgeNetItem{agent: true})
	require("a roger running here", g, "│")

	// The three are three DIFFERENT glyphs, not three colours, so they survive NO_COLOR and a pipe.
	seen := map[string]bool{}
	for _, w := range []string{wireOf(t, lan), wireOf(t, relayed), wireOf(t, dark)} {
		if seen[w] {
			t.Errorf("two link kinds share the glyph %q, so NO_COLOR cannot tell them apart", w)
		}
		seen[w] = true
	}
}

func TestTheWireLegendNamesOnlyWhatIsOnScreen(t *testing.T) {
	lan := nodeOf("a", "studio-mac", "host")
	relayed := nodeOf("b", "shed-pi", "board")
	relayed.Transports = nil
	dark := nodeOf("c", "quiet-pi", "board")
	dark.Presence = string(edge.PresenceDark)

	item := func(n store.EdgeNode) edgeNetItem { return edgeNetItem{member: true, node: &n, id: n.ID} }

	// A fleet that is all LAN-direct is not taught three glyphs to read one.
	if got := edgeWireLegend([]edgeNetItem{item(lan)}); got != "" {
		t.Errorf("an all-direct fleet still drew a legend: %q", got)
	}
	got := stripANSI(edgeWireLegend([]edgeNetItem{item(lan), item(relayed)}))
	if !strings.Contains(got, "direct") || !strings.Contains(got, "through a relay") {
		t.Errorf("the legend does not name the wires on screen: %q", got)
	}
	if strings.Contains(got, "not heard from") {
		t.Errorf("the legend names a wire nothing on screen has: %q", got)
	}
	got = stripANSI(edgeWireLegend([]edgeNetItem{item(dark)}))
	if !strings.Contains(got, "not heard from") || strings.Contains(got, "direct") {
		t.Errorf("a dark-only fleet's legend is wrong: %q", got)
	}
}

// The whole band, drawn: every member hangs off the hub, and the drop line carries one glyph per
// card rather than a rail that says the same thing about every node.
func TestTheFleetBandIsWiredToTheHub(t *testing.T) {
	m := mapReadableModel(t)
	for i := range m.edge.rows {
		if m.edge.rows[i].n.Name == "pi-greenhouse" {
			m.edge.rows[i].n.Transports = nil
		}
	}
	out := stripANSI(m.edgeView(116))
	band := ""
	lines := strings.Split(out, "\n")
	for i, ln := range lines {
		if strings.Contains(ln, "ON THE EDGE") && i+3 < len(lines) {
			band = lines[i+2] // the bus, then the drops
			if strings.Contains(lines[i+2], "─") {
				band = lines[i+3]
			}
		}
	}
	if band == "" {
		t.Fatalf("no fleet band on the map:\n%s", out)
	}
	if !strings.Contains(band, "│") || !strings.Contains(band, "┊") {
		t.Errorf("the drop line does not carry a wire per node (%q):\n%s", band, out)
	}
}

// A heartbeat lights the wire it arrived on. A fleet with no traffic draws a still picture, which
// is the honest one: the animation is evidence, not decoration.
func TestAHeartbeatMovesOnItsOwnWire(t *testing.T) {
	m := mapReadableModel(t)
	var id string
	for _, r := range m.edge.rows {
		id = r.n.ID
		break
	}
	if id == "" {
		t.Fatal("no member to pulse")
	}
	it := edgeNetItem{member: true, id: id}
	if m.edgePulsing(it) {
		t.Errorf("a node is pulsing before any heartbeat arrived")
	}
	m.edge.pulses = append(m.edge.pulses, edgePulse{id: id})
	if !m.edgePulsing(it) {
		t.Errorf("the heartbeat did not reach the node's wire")
	}
	if m.edgePulsing(edgeNetItem{member: true, id: "n_someone_else"}) {
		t.Errorf("a heartbeat lit a wire it never travelled")
	}
	if m.edgePulsing(edgeNetItem{agent: true}) {
		t.Errorf("a node with no id was matched by a pulse")
	}
}

// THE PATH is the fact this screen exists to show and no other view can: how the authority reaches
// a unit, through whom, and what goes with the relay when it goes dark.
func TestTheMapNamesTheRelayAndTheWholePath(t *testing.T) {
	self := edge.SelfStatus{Root: edge.RootLocal, Authority: "desk", AuthorityLocal: true, AuthorityHere: true,
		AuthorityAddr: "http://192.168.1.9:8791", Enrolled: true, Name: "desk",
		Discovery: edge.DiscoveryScanning, IntervalS: 30}
	relay := nodeOf("r", "wiltshire", "host")
	behind := nodeOf("b", "nina", "mobile")
	behind.Transports = []store.EdgeTransport{{Kind: "relay", Addr: "wiltshire"}}
	m := networkModel(t, self, relay, behind)
	m.hooks.EdgeHousehold = func() []edge.Instance { return []edge.Instance{{Name: "desk", Agent: true}} }
	m.hooks.EdgeSelfInstance = func() string { return "desk" }
	m.enterEdge()

	out := stripANSI(m.edgeView(116))
	if !strings.Contains(out, "via wiltshire") {
		t.Errorf("the card does not name the relay the unit is reached through:\n%s", out)
	}
	// The relay is drawn BEFORE what hangs off it, whatever order the fleet listed them in.
	if r, b := strings.Index(out, "wiltshire"), strings.Index(out, "nina"); r < 0 || b < 0 || r > b {
		t.Errorf("the relay is not drawn before the unit reached through it:\n%s", out)
	}
	// And the node page gives the whole route with its blast radius.
	for i, it := range m.edgeNetItems() {
		if it.member && it.name == "nina" {
			m.edge.netSel, m.edge.netSelSet, m.edge.netDetail = i, true, true
			if it.hops != 2 {
				t.Errorf("nina is %d hops away, want 2", it.hops)
			}
		}
	}
	page := stripANSI(m.edgeView(116))
	for _, want := range []string{"PATH", "desk → wiltshire → nina", "2 hops", "if wiltshire goes dark"} {
		if !strings.Contains(page, want) {
			t.Errorf("the node page is missing %q from the path:\n%s", want, page)
		}
	}

	// A unit the authority reaches itself is one hop, and is not dressed up as a route.
	for i, it := range m.edgeNetItems() {
		if it.member && it.name == "wiltshire" {
			m.edge.netSel, m.edge.netSelSet = i, true
		}
	}
	direct := stripANSI(m.edgeView(116))
	if !strings.Contains(direct, "1 hop") || strings.Contains(direct, "1 hops") {
		t.Errorf("a direct unit's path does not read as one hop:\n%s", direct)
	}
	if strings.Contains(direct, "desk → wiltshire → wiltshire") {
		t.Errorf("a direct unit was drawn with itself as its own relay:\n%s", direct)
	}
}

// treeModel is an Edge with a three-deep relay chain and two machines the authority reaches itself:
//
//	authority ─ studio-mac
//	          ─ wiltshire ─ nina ─ greenhouse
//	                      ─ shed-1
func treeModel(t *testing.T) model {
	t.Helper()
	self := edge.SelfStatus{Root: edge.RootLocal, Authority: "desk", AuthorityLocal: true, AuthorityHere: true,
		AuthorityAddr: "http://192.168.1.9:8791", Enrolled: true, Name: "desk",
		Discovery: edge.DiscoveryScanning, IntervalS: 30}
	through := func(id, name, kind, via string) store.EdgeNode {
		n := nodeOf(id, name, kind)
		n.Transports = []store.EdgeTransport{{Kind: "relay", Addr: via}}
		return n
	}
	m := networkModel(t, self,
		nodeOf("a", "studio-mac", "host"),
		nodeOf("b", "wiltshire", "host"),
		through("c", "nina", "mobile", "wiltshire"),
		through("d", "shed-1", "board", "wiltshire"),
		through("e", "greenhouse", "board", "nina"),
	)
	m.hooks.EdgeHousehold = func() []edge.Instance { return []edge.Instance{{Name: "desk", Agent: true}} }
	m.hooks.EdgeSelfInstance = func() string { return "desk" }
	m.enterEdge()
	return m
}

// A HOP IS DRAWN AS A HOP. Every member used to hang off the hub whatever its real path was, which
// is the flattest thing a picture of a network can be. A relay chain three deep draws three levels.
func TestTheDrawingHasAsManyLevelsAsTheEdgeHasHops(t *testing.T) {
	out := stripANSI(treeModel(t).edgeView(116))
	lineOf := func(name string) int {
		for i, ln := range strings.Split(out, "\n") {
			if strings.Contains(ln, name) {
				return i
			}
		}
		return -1
	}
	hub, wilt, nina, green := lineOf("YOU ARE HERE"), lineOf("wiltshire"), lineOf("nina"), lineOf("greenhouse")
	for name, at := range map[string]int{"hub": hub, "wiltshire": wilt, "nina": nina, "greenhouse": green} {
		if at < 0 {
			t.Fatalf("%s is not drawn at all:\n%s", name, out)
		}
	}
	if !(hub < wilt && wilt < nina && nina < green) {
		t.Errorf("the chain is not drawn one level per hop (hub %d, wiltshire %d, nina %d, greenhouse %d):\n%s",
			hub, wilt, nina, green, out)
	}
	// And each says which machine it is reached through.
	for _, want := range []string{"via wiltshire", "via nina"} {
		if !strings.Contains(out, want) {
			t.Errorf("the drawing does not name the relay in the path: missing %q\n%s", want, out)
		}
	}
}

// A TREE NEVER CROSSES. ┼ is the glyph for two wires meeting, and in a rooted drawing that cannot
// happen: if one appears, the layout is wrong and the glyph is saying so.
func TestNoWireEverCrossesAnother(t *testing.T) {
	for _, w := range []int{80, 100, 116, 160, 200} {
		out := stripANSI(treeModel(t).edgeView(w))
		if i := strings.Index(out, "┼"); i >= 0 {
			t.Errorf("at %d columns a wire crosses another, which a tree cannot do:\n%s", w, out)
		}
	}
	// A single card directly below its parent hangs on a straight line, not a crossing.
	if got := stripANSI(edgeBranchLine(40, 20, []int{20})); !strings.Contains(got, "│") || strings.Contains(got, "┼") {
		t.Errorf("one card below its parent = %q, want a straight line", got)
	}
	// Several cards spread either side of the trunk do meet it, and that is a tee.
	if got := stripANSI(edgeBranchLine(40, 20, []int{10, 30})); !strings.Contains(got, "┴") {
		t.Errorf("a band either side of its trunk = %q, want a tee at the trunk", got)
	}
}

// Every band hangs UNDER what it hangs off, and nothing is drawn off the screen doing it.
func TestABandHangsUnderItsParentAndStaysOnScreen(t *testing.T) {
	for _, w := range []int{80, 100, 116, 200} {
		out := stripANSI(treeModel(t).edgeView(w))
		for _, ln := range strings.Split(out, "\n") {
			if lipgloss.Width(ln) > w {
				t.Fatalf("at %d columns a line is %d wide:\n%s", w, lipgloss.Width(ln), ln)
			}
		}
		// nina's card is drawn to the right of the left edge of wiltshire's band, which is what
		// "under its parent" means once the row is centred on the parent's column.
		nina, wilt := -1, -1
		for _, ln := range strings.Split(out, "\n") {
			if strings.Contains(ln, "nina") && nina < 0 {
				nina = strings.Index(ln, "▯")
			}
			if strings.Contains(ln, "wiltshire") && wilt < 0 {
				wilt = strings.Index(ln, "▣")
			}
		}
		if nina < 0 || wilt < 0 {
			t.Fatalf("at %d columns the chain is not drawn:\n%s", w, out)
		}
	}
}

// THE AUTHORITY IS ONE BOX. Its own fleet record (the node id it enrolled itself under) was drawn a
// second time as an "other machine you have adopted", under its raw id, carrying the same alerts as
// the hub - found on the founder's own authority 2026-10-01.
func TestTheMachineYouAreAtIsNotAlsoAnotherMachine(t *testing.T) {
	self := edge.SelfStatus{Root: edge.RootLocal, Authority: "desk", AuthorityLocal: true, AuthorityHere: true,
		AuthorityAddr: "http://192.168.1.9:8791", Enrolled: true, Name: "n_ebab8e241f058e18234af760ad2a1ca9",
		NodeID: "n_ebab8e241f058e18234af760ad2a1ca9", Discovery: edge.DiscoveryScanning, IntervalS: 30}
	mine := nodeOf("x", "", "host")
	mine.ID, mine.Name = self.NodeID, self.NodeID // enrolled under its own id, never named
	m := networkModel(t, self, mine, nodeOf("b", "jetson", "host"))
	m.enterEdge()
	members := 0
	for _, it := range m.edgeNetItems() {
		if it.member {
			members++
			if it.id == self.NodeID {
				t.Errorf("this machine is drawn again as a member, as %q", it.name)
			}
		}
	}
	if members != 1 {
		t.Errorf("want only jetson as another machine, got %d members", members)
	}
	if out := stripANSI(m.edgeView(116)); strings.Contains(out, "n_ebab8e24") {
		t.Errorf("an id is drawn as a name:\n%s", out)
	}
}

// features/edge/fleet_window.feature §2: a card says how a unit is with its age, and a unit that is
// heard but has no face to dial says HEARD - its page names what that means for the actions.
func TestACardSaysHowAUnitIsAndAHeardOnlyUnitSaysSo(t *testing.T) {
	self := edge.SelfStatus{Root: edge.RootLocal, Authority: "desk", AuthorityLocal: true, AuthorityHere: true,
		AuthorityAddr: "http://192.168.1.9:8791", Enrolled: true, Name: "desk", Discovery: edge.DiscoveryScanning, IntervalS: 30}
	phone := nodeOf("p", "nina", "mobile")
	phone.Transports = nil // heard through its presence, with no LAN face to dial
	phone.LastSeen -= 40
	m := networkModel(t, self, nodeOf("a", "studio", "host"), phone)
	out := stripANSI(m.edgeView(116))
	for _, want := range []string{"MACHINE · ON AIR now", "PHONE · HEARD 43s"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	for i, it := range m.edgeNetItems() {
		if it.member && it.name == "nina" {
			m.edge.netSel, m.edge.netSelSet, m.edge.netDetail = i, true, true
		}
	}
	page := stripANSI(m.edgeView(160))
	if !strings.Contains(page, "HEARD 43s · no control path") || !strings.Contains(page, "cannot reach it") {
		t.Errorf("the page does not say what heard-only means:\n%s", page)
	}
}
