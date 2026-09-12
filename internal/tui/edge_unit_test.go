package tui

// The Edge screen's units: the corners the topology scenarios do not each walk - the age
// formatter, the measured geometry at every width, the relay arrangement's refusals, the
// adopt path's failures, the legacy-console fold, and the screens with nothing to draw.
//
// Table-driven, stdlib testing + testify, against the real fleet model.

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/require"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

func TestEdgeAge(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{-time.Second, "0s"},
		{0, "0s"},
		{45 * time.Second, "45s"},
		{90 * time.Second, "1m"},
		{59 * time.Minute, "59m"},
		{3 * time.Hour, "3h"},
		{50 * time.Hour, "2d"},
	} {
		require.Equal(t, tc.want, edgeAge(tc.d), "%v", tc.d)
	}
}

// The layout is a decision about the terminal AND the fleet: a picture nobody can read is
// worse than a list.
func TestEdgeLayoutFor(t *testing.T) {
	for _, tc := range []struct {
		w, nodes int
		want     string
	}{
		{200, 3, edgeLayoutGraph},
		{80, 3, edgeLayoutGraph},
		{79, 3, edgeLayoutCompact},
		{56, 3, edgeLayoutCompact},
		{55, 3, edgeLayoutList},
		{40, 3, edgeLayoutList},
		{200, edgeMaxGraphNodes, edgeLayoutGraph},
		{200, edgeMaxGraphNodes + 1, edgeLayoutList},
	} {
		require.Equal(t, tc.want, edgeLayoutFor(tc.w, tc.nodes), "w=%d n=%d", tc.w, tc.nodes)
	}
}

// Every column budget must fit the terminal it was computed for - including the degenerate
// widths a window drag can produce.
func TestEdgeGeomAlwaysFits(t *testing.T) {
	for _, w := range []int{0, 1, 20, 30, 40, 56, 60, 79, 80, 100, 120, 200, 400} {
		for _, n := range []int{0, 1, 25, 200} {
			g := edgeGeomFor(w, n)
			total := g.lead + g.nameW + 1 + g.marksW + 1 + g.detailW
			require.LessOrEqual(t, total, max(w, 80), "w=%d n=%d columns overflow", w, n)
			require.Positive(t, g.nameW, "w=%d n=%d: a name column of nothing", w, n)
			require.GreaterOrEqual(t, g.detailW, 0)
		}
	}
}

func TestEdgeViaAndHasLAN(t *testing.T) {
	lan := store.EdgeNode{Transports: []store.EdgeTransport{{Kind: "lan", Addr: "10.0.0.2"}}}
	relayed := store.EdgeNode{Transports: []store.EdgeTransport{{Kind: "relay", Addr: "tower-1"}}}
	both := store.EdgeNode{Transports: []store.EdgeTransport{
		{Kind: "lan", Addr: "10.0.0.3"}, {Kind: "relay", Addr: "tower-1"}}}

	require.True(t, edgeHasLAN(lan))
	require.False(t, edgeHasLAN(relayed))
	require.Empty(t, edgeVia(lan), "a LAN peer is reached directly")
	require.Equal(t, "tower-1", edgeVia(relayed))
	require.Empty(t, edgeVia(both), "the preferred transport is the LAN one, so it is not relayed")
	require.Empty(t, edgeVia(store.EdgeNode{}), "no transports, no path")
}

// edgeArrange draws a hop only when it can see BOTH ends: a relay that is not on this Edge
// is not a node, so the peer behind it hangs off self on its own dim edge instead.
func TestEdgeArrangeOnlyHopsItCanSee(t *testing.T) {
	list := []store.EdgeNode{
		{ID: "1", Name: "tower-1", Transports: []store.EdgeTransport{{Kind: "relay", Addr: "relay.rogerai.fm"}}},
		{ID: "2", Name: "jetson", Transports: []store.EdgeTransport{{Kind: "relay", Addr: "tower-1"}}},
		{ID: "3", Name: "stranger", Transports: []store.EdgeTransport{{Kind: "relay", Addr: "somebody-elses-tower"}}},
		{ID: "4", Name: "loop", Transports: []store.EdgeTransport{{Kind: "relay", Addr: "loop"}}},
	}
	rows := edgeArrange(list)
	require.Len(t, rows, 4, "every node is drawn exactly once")

	by := map[string]edgeRow{}
	for _, r := range rows {
		by[r.n.Name] = r
	}
	require.True(t, by["tower-1"].relay, "tower-1 carries a peer, so it is a relay hop")
	require.True(t, by["jetson"].child)
	require.Equal(t, "tower-1", by["jetson"].via)
	require.False(t, by["stranger"].child, "a relay this Edge cannot see is not drawn as a hop")
	require.False(t, by["loop"].child, "a node is never its own relay")

	// The child is drawn immediately under its relay, which is what makes the hop visible.
	var relayAt, childAt int
	for i, r := range rows {
		if r.n.Name == "tower-1" {
			relayAt = i
		}
		if r.n.Name == "jetson" {
			childAt = i
		}
	}
	require.Equal(t, relayAt+1, childAt)
}

// A FLEET VIEW NEVER LOSES A MEMBER. A relay chain and a pair of nodes that name each
// other as their relay both used to strand a node off the drawing entirely - which is the
// exact failure a fleet view exists to show.
func TestEdgeArrangeNeverDropsANode(t *testing.T) {
	relay := func(id, name, via string) store.EdgeNode {
		return store.EdgeNode{ID: id, Name: name,
			Transports: []store.EdgeTransport{{Kind: "relay", Addr: via}}}
	}
	for _, tc := range []struct {
		name string
		list []store.EdgeNode
	}{
		{"a three-deep chain", []store.EdgeNode{
			relay("1", "tower-1", "relay.rogerai.fm"),
			relay("2", "shed", "tower-1"),
			relay("3", "gate", "shed"),
		}},
		{"two nodes that name each other", []store.EdgeNode{
			relay("1", "a", "b"),
			relay("2", "b", "a"),
		}},
		{"a three-node cycle", []store.EdgeNode{
			relay("1", "a", "b"), relay("2", "b", "c"), relay("3", "c", "a"),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := edgeArrange(tc.list)
			require.Len(t, rows, len(tc.list), "a node was dropped from the drawing")
			seen := map[string]bool{}
			for _, r := range rows {
				require.False(t, seen[r.n.ID], "%s is drawn twice", r.n.Name)
				seen[r.n.ID] = true
				if r.child {
					// A child is drawn UNDER the relay it names - never above it, and
					// never pointing at a node the frame does not draw.
					require.True(t, seen[relayIDByName(tc.list, r.via)], "%s hangs off a relay drawn after it", r.n.Name)
				}
			}
		})
	}
}

func relayIDByName(list []store.EdgeNode, name string) string {
	for _, n := range list {
		if n.Name == name {
			return n.ID
		}
	}
	return ""
}

func TestEdgeMarks(t *testing.T) {
	require.Empty(t, edgeMarks(store.EdgeNode{}), "nothing declared, nothing marked")
	n := store.EdgeNode{Caps: []store.EdgeCap{
		{Name: "relay", State: string(edge.Claimed)},
		{Name: "serve", State: string(edge.Verified)},
		{Name: "actuate", State: string(edge.PendingConfirmation)},
	}}
	// Canonical order, not declaration order; UPPER only for VERIFIED.
	require.Equal(t, "[SV ac rl]", edgeMarks(n))
}

func TestEdgeSelfNameFallsBackHonestly(t *testing.T) {
	m := New("http://b", "u")
	require.Equal(t, "this machine", m.edgeSelfName())
	m.hooks.Station = "brave-otter"
	require.Equal(t, "brave-otter", m.edgeSelfName())
	m.hooks.EdgeSelf = "roger-desk"
	require.Equal(t, "roger-desk", m.edgeSelfName())
}

// A legacy console gets ASCII stand-ins, and solid / dim / broken stay three different
// characters there too - the whole point of choosing glyphs over colours.
func TestEdgeFoldKeepsTheTexturesApart(t *testing.T) {
	require.Equal(t, "─┈╌", edgeFold("─┈╌"), "a UTF-8 terminal keeps the drawing")
	t.Setenv("ROGERAI_ASCII", "1")
	got := edgeFold("─┈╌●▣›├└│╭╮╰╯┬")
	require.Equal(t, "-.=*#>++|++++", got[:len("-.=*#>++|++++")])
	require.Equal(t, 3, len(map[byte]bool{got[0]: true, got[1]: true, got[2]: true}),
		"solid, dim and broken must not fold onto one another")
}

// ---- the screen's own corners --------------------------------------------

// edgeFixture is a model with a real fleet, on the Edge screen, at a fixed clock.
func edgeFixture(t *testing.T, hooks Hooks) (model, *edge.Fleet, time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	f := edge.NewFleet(store.NewMem(), "acct-1")
	f.SetClock(func() time.Time { return now })
	hooks.EdgeFleet = f
	if hooks.EdgeSelf == "" {
		hooks.EdgeSelf = "roger-desk"
	}
	m := NewWithHooks("http://b", "u", nil, hooks)
	m.width, m.height = 100, 40
	m.edgeClock = func() time.Time { return now }
	return m, f, now
}

func edgeAddNode(t *testing.T, f *edge.Fleet, name string, ts []store.EdgeTransport, caps ...edge.Capability) store.EdgeNode {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	n := edge.NewNode(pub, name, edge.Host, caps...)
	n.Transports = ts
	got, err := f.Enroll(n)
	require.NoError(t, err)
	return got
}

// esc goes back to a RESTING screen; a transient one is not a place to return to.
func TestEdgeReturnFor(t *testing.T) {
	for _, md := range []mode{modeBrowse, modeShare, modeLimits, modeAgent, modeHelp, modePrivate} {
		require.Equal(t, md, edgeReturnFor(md))
	}
	for _, md := range []mode{modeChat, modeConnecting, modeQuitConfirm, modeVoicePicker} {
		require.Equal(t, modeBrowse, edgeReturnFor(md), "mode %v", md)
	}
}

func TestEdgeKeysMoveAndOpen(t *testing.T) {
	m, f, _ := edgeFixture(t, Hooks{})
	edgeAddNode(t, f, "a-node", []store.EdgeTransport{{Kind: "lan", Addr: "10.0.0.1"}}, edge.Sense)
	b := edgeAddNode(t, f, "b-node", []store.EdgeTransport{{Kind: "lan", Addr: "10.0.0.2"}}, edge.Sense)
	m.enterEdge()

	press := func(key string) {
		var tm tea.Model = m
		tm, _ = tm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		m = asModel(tm)
	}
	press("j")
	require.Equal(t, b.ID, m.edge.sel, "j moves down")
	press("j")
	require.Equal(t, b.ID, m.edge.sel, "the selection stops at the end rather than wrapping")
	press("k")
	require.NotEqual(t, b.ID, m.edge.sel, "k moves back up")
	press("i")
	require.True(t, m.edge.detail, "i opens the detail")
	press("i")
	require.False(t, m.edge.detail, "and closes it")

	// r re-reads the fleet: a node enrolled behind the screen's back shows up.
	edgeAddNode(t, f, "c-node", []store.EdgeTransport{{Kind: "lan", Addr: "10.0.0.3"}}, edge.Sense)
	require.Len(t, m.edge.rows, 2)
	press("r")
	require.Len(t, m.edge.rows, 3)
}

func TestEdgeAdoptRefusesHonestly(t *testing.T) {
	cand := store.EdgeNode{ID: "n_cand", Name: "new-pi5",
		Transports: []store.EdgeTransport{{Kind: "lan", Addr: "10.0.0.9"}}}

	// Nothing to adopt: the selection is a member, not a candidate.
	m, f, _ := edgeFixture(t, Hooks{})
	edgeAddNode(t, f, "a-node", []store.EdgeTransport{{Kind: "lan", Addr: "10.0.0.1"}}, edge.Sense)
	m.enterEdge()
	m.adoptEdgeCandidate()
	require.Contains(t, stripANSI(m.status), "adopts a CANDIDATE")

	// A candidate, but no way to adopt it: say so, do not pretend.
	m2, _, _ := edgeFixture(t, Hooks{EdgeCandidates: func() []store.EdgeNode { return []store.EdgeNode{cand} }})
	m2.enterEdge()
	m2.adoptEdgeCandidate()
	require.Contains(t, stripANSI(m2.status), "cannot adopt")

	// The host refuses: the refusal is shown, and nothing joins the Edge.
	m3, _, _ := edgeFixture(t, Hooks{
		EdgeCandidates: func() []store.EdgeNode { return []store.EdgeNode{cand} },
		EdgeAdopt:      func(string, string) error { return errAdoptRefused },
	})
	m3.enterEdge()
	m3.adoptEdgeCandidate()
	require.Contains(t, stripANSI(m3.status), "already belongs")
	require.Empty(t, m3.edge.rows)
}

var errAdoptRefused = errAdopt("that node already belongs to another Edge")

type errAdopt string

func (e errAdopt) Error() string { return string(e) }

func TestEdgeAdoptEnrolls(t *testing.T) {
	var f *edge.Fleet
	cand := store.EdgeNode{ID: "n_cand", Name: "new-pi5", Kind: string(edge.Host),
		Transports: []store.EdgeTransport{{Kind: "lan", Addr: "10.0.0.9", Fingerprint: "beef"}}}
	live := []store.EdgeNode{cand}
	m, fleet, _ := edgeFixture(t, Hooks{
		EdgeCandidates: func() []store.EdgeNode { return live },
		EdgeAdopt: func(id, name string) error {
			c := cand
			c.Name, c.Presence = name, string(edge.PresenceVerified)
			_, err := f.Enroll(c)
			live = nil
			return err
		},
	})
	f = fleet
	m.enterEdge()
	m.adoptEdgeCandidate()
	require.Contains(t, stripANSI(m.status), "adopted onto your Edge")
	require.Len(t, m.edge.rows, 1)
	require.Empty(t, m.edge.cands)
}

// A heartbeat from a node the graph does not draw animates nothing: the screen never moves
// for a link it cannot see.
func TestEdgeHeartbeatOnlyForDrawnNodes(t *testing.T) {
	m, f, _ := edgeFixture(t, Hooks{})
	edgeAddNode(t, f, "a-node", []store.EdgeTransport{{Kind: "lan", Addr: "10.0.0.1"}}, edge.Sense)
	m.enterEdge()

	tm, cmd := m.Update(EdgeHeartbeat("n_nobody"))
	require.Nil(t, cmd)
	require.Empty(t, asModel(tm).edge.pulses)

	tm, cmd = m.Update(EdgeHeartbeat(m.edge.rows[0].n.ID))
	require.NotNil(t, cmd, "a real event asks for the next frame")
	mm := asModel(tm)
	require.Len(t, mm.edge.pulses, 1)

	// A second heartbeat restarts the same edge's pulse rather than stacking a new one.
	tm, _ = mm.Update(EdgeHeartbeat(mm.edge.rows[0].n.ID))
	mm = asModel(tm)
	require.Len(t, mm.edge.pulses, 1)

	// Off the screen: the state is kept, nothing is scheduled, nothing advances.
	mm.mode = modeBrowse
	tm, cmd = mm.Update(edgeAnimMsg{})
	require.Nil(t, cmd)
	require.Equal(t, mm.edge.pulses[0].step, asModel(tm).edge.pulses[0].step)
	require.Nil(t, mm.edgeResume(), "a screen nobody is looking at asks for no frames")
}

// A pulse is reaped when it arrives, so a quiet fleet ends up still on its own.
func TestEdgePulseArrivesAndStops(t *testing.T) {
	m, f, _ := edgeFixture(t, Hooks{})
	n := edgeAddNode(t, f, "a-node", []store.EdgeTransport{{Kind: "lan", Addr: "10.0.0.1"}}, edge.Sense)
	m.enterEdge()
	var tm tea.Model = m
	tm, _ = tm.Update(EdgeHeartbeat(n.ID))
	m = asModel(tm)

	var cmd tea.Cmd
	for i := 0; i < 64 && len(m.edge.pulses) > 0; i++ {
		tm, cmd = m.Update(edgeAnimMsg{})
		m = asModel(tm)
	}
	require.Empty(t, m.edge.pulses, "the pulse arrived")
	require.Nil(t, cmd, "and nothing further was scheduled")
	require.NotContains(t, stripANSI(m.edgeView(100)), string(edgeGlyphPulse))
}

// A list too long to draw scrolls to keep the selection visible - a cursor nobody can see
// is a cursor they act on blind.
func TestEdgeListFollowsTheSelection(t *testing.T) {
	m, f, _ := edgeFixture(t, Hooks{})
	var last store.EdgeNode
	for i := 0; i < edgeListRows*2; i++ {
		last = edgeAddNode(t, f, fmt.Sprintf("node-%03d", i),
			[]store.EdgeTransport{{Kind: "lan", Addr: "10.0.0.1"}}, edge.Sense)
	}
	m.enterEdge()
	require.Equal(t, edgeLayoutList, edgeLayoutFor(100, len(m.edge.rows)))
	out := stripANSI(m.edgeView(100))
	require.Contains(t, out, "showing 20 of 40 nodes")
	require.NotContains(t, out, last.Name, "the far end is off the window to start with")

	m.edge.sel = last.ID
	out = stripANSI(m.edgeView(100))
	require.Contains(t, out, last.Name, "the window did not follow the selection")
	require.Contains(t, out, string(edgeGlyphSel)+" "+last.Name[:8])
}

func TestEdgeViewCorners(t *testing.T) {
	m, f, _ := edgeFixture(t, Hooks{})
	m.enterEdge()

	// Nothing selected: the detail says so rather than drawing a node that is not there.
	m.edge.detail = true
	require.Contains(t, stripANSI(m.edgeView(100)), "nothing selected")
	m.edge.detail = false

	// A fleet that could not be read is SAID, never swallowed.
	m.edge.err = "the store is unreachable"
	require.Contains(t, stripANSI(m.edgeView(100)), "the Edge could not be read: the store is unreachable")
	m.edge.err = ""

	// A candidate's detail names what it is and what to do about it.
	cand := store.EdgeNode{ID: "n_cand", Name: "new-pi5", Kind: string(edge.Host)}
	m.hooks.EdgeCandidates = func() []store.EdgeNode { return []store.EdgeNode{cand} }
	m.refreshEdge()
	m.edge.sel, m.edge.detail = cand.ID, true
	out := stripANSI(m.edgeView(100))
	require.Contains(t, out, "CANDIDATE · new-pi5")
	require.Contains(t, out, "not on your Edge")
	require.NotContains(t, out, "fingerprint", "an unadopted peer has no pin to show")

	// A member with nothing declared says so rather than showing an empty column.
	n := edgeAddNode(t, f, "bare", []store.EdgeTransport{{Kind: "lan", Addr: "10.0.0.1"}})
	m.refreshEdge()
	m.edge.sel, m.edge.detail = n.ID, true
	require.Contains(t, stripANSI(m.edgeView(100)), "none declared")
}

// The width backstop holds for the DETAIL too, at every width the screen will meet.
func TestEdgeDetailFitsEveryWidth(t *testing.T) {
	m, f, _ := edgeFixture(t, Hooks{})
	n := edgeAddNode(t, f, strings.Repeat("n", edge.MaxNameLen),
		[]store.EdgeTransport{
			{Kind: "lan", Addr: "192.168.1.20", Fingerprint: strings.Repeat("ab", 32)},
			{Kind: "relay", Addr: "tower-1"},
		}, edge.Serve, edge.Classify, edge.Sense, edge.Actuate, edge.Relay, edge.Operate)
	m.enterEdge()
	m.edge.sel, m.edge.detail = n.ID, true
	for _, w := range []int{40, 60, 80, 100, 120, 200} {
		for i, ln := range strings.Split(stripANSI(m.edgeView(w)), "\n") {
			require.LessOrEqual(t, lipgloss.Width(ln), w, "w=%d line %d: %q", w, i, ln)
		}
	}
}
