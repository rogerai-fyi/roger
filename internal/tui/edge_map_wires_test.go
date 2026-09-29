package tui

// THE MAP IS A GRAPH, and its wires are the founder's ruling for this screen: "a graphical
// animated ascii graph representing how your Edge is connected". The rule the whole screen obeys
// (features/edge/topology_view.feature) is that it NEVER DRAWS A LINK IT HAS NOT SEEN, so each
// member hangs on a wire that says how it is really reached, and a heartbeat moves on that wire.

import (
	"strings"
	"testing"

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
