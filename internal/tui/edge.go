package tui

// THE EDGE SCREEN ([3] EDGE) - "what is on my Edge, and how is it connected?"
//
// This file is the screen's STATE and its EVENTS; edge_view.go is the drawing. They are
// split on the line the whole feature turns on: the view is a pure function of a SNAPSHOT,
// so a fleet that changes while a frame is being drawn can never produce a frame that shows
// an edge to a node it does not draw.
//
// THE RULE THE WHOLE SCREEN OBEYS: it never draws a link it has not seen. A solid edge is a
// LAN-direct transport this instance verified; a dim edge is a path through a relay, and the
// relay is drawn as its OWN node so the hop is visible rather than implied; a node whose
// heartbeat aged out is drawn DARK, its edge broken, and it is KEPT.
//
// The animation is driven by REAL events (edgeHeartbeatMsg), never by a bare timer: the tick
// only advances pulses that a heartbeat created, so a fleet with no traffic draws a still
// graph, and a still graph is information.
//
// Spec: features/edge/topology_view.feature.

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

const (
	// edgeAnimEvery is the pulse cadence. It is a cadence, not a heartbeat: nothing
	// advances unless a real heartbeat put a pulse on an edge.
	edgeAnimEvery = 120 * time.Millisecond
	// edgeMaxGraphNodes is how many nodes can be drawn as a graph before the drawing
	// stops being a drawing. Past it the screen degrades to a list that says so, rather
	// than to a broken picture.
	edgeMaxGraphNodes = 24
	// edgeListRows bounds the list so a 200-node fleet cannot scroll the terminal; the
	// list says how many of how many it is showing.
	edgeListRows = 20
)

// edgePulse is one heartbeat travelling the edge it arrived on. step counts CELLS from the
// node end toward self, so the geometry can map it onto whatever run width the current
// terminal affords without the event carrying a pixel.
type edgePulse struct {
	id   string
	step int
}

// edgeRow is one node's place in the drawing. child/via record that the node is reached
// THROUGH the relay drawn above it - the hop the screen refuses to imply.
type edgeRow struct {
	n     store.EdgeNode
	relay bool   // other nodes are reached through this one
	child bool   // this node is reached through the relay above it
	via   string // that relay's name
}

// edgeState is the screen's whole state: one snapshot, one selection, the live pulses.
type edgeState struct {
	rows   []edgeRow
	cands  []store.EdgeNode
	at     time.Time // when the snapshot was taken; every age on screen is measured from it
	err    string    // the fleet could not be read (shown, never swallowed)
	sel    string    // the selected node/candidate id - STICKY across fleet changes
	// sessions is the frame's session snapshot: the traffic this Edge really carried,
	// read once per frame like everything else here. It is READ-ONLY to the screen -
	// there is no path from the view back into the ledger that could open one.
	sessions []edge.Session
	detail bool
	pulses []edgePulse
	ret    mode // where esc goes back to
	retSet bool
}

// edgeNow is the screen's clock. Injectable so a test can assert on "last seen 6m ago"
// without racing the wall clock.
func (m model) edgeNow() time.Time {
	if m.edgeClock != nil {
		return m.edgeClock()
	}
	return time.Now()
}

// enterEdge opens the screen and remembers where the operator came from, so esc returns
// there rather than dumping them on a screen they never chose.
func (m *model) enterEdge() {
	if m.mode != modeEdge {
		m.edge.ret, m.edge.retSet = edgeReturnFor(m.mode), true
	}
	m.mode = modeEdge
	m.edge.detail = false
	m.refreshEdge()
	m.status = stDim.Render("EDGE - your fleet, and how it is connected")
}

// edgeReturnFor picks the screen esc goes back to. Only a RESTING screen is a place to
// return to; anything transient falls back to the dial.
func edgeReturnFor(md mode) mode {
	switch md {
	case modeBrowse, modeShare, modeLimits, modeAgent, modeHelp, modePrivate:
		return md
	}
	return modeBrowse
}

func (m *model) leaveEdge() {
	m.mode = modeBrowse
	if m.edge.retSet {
		m.mode = m.edge.ret
		m.edge.ret, m.edge.retSet = 0, false
	}
	m.edge.detail = false
}

// refreshEdge takes the frame's snapshot. Everything the view draws comes from here, and
// nothing the view draws is read live - that is what makes a frame internally consistent
// even when the fleet moves underneath it.
func (m *model) refreshEdge() {
	m.edge.at = m.edgeNow()
	m.edge.err = ""
	var list []store.EdgeNode
	if m.hooks.EdgeFleet != nil {
		got, err := m.hooks.EdgeFleet.List()
		if err != nil {
			m.edge.err = err.Error()
		}
		list = got
	}
	m.edge.rows = edgeArrange(list) // one row per member, always
	m.edge.cands = nil
	if m.hooks.EdgeCandidates != nil {
		m.edge.cands = m.hooks.EdgeCandidates()
	}
	m.edge.sessions = m.edgeSessions()
	m.clampEdgeSel()
}

// edgeNodes is the snapshot the current frame draws, in draw order.
func (m model) edgeNodes() []store.EdgeNode {
	out := make([]store.EdgeNode, 0, len(m.edge.rows))
	for _, r := range m.edge.rows {
		out = append(out, r.n)
	}
	return out
}

// edgeHasLAN reports a LAN-direct transport - the one a solid edge means.
func edgeHasLAN(n store.EdgeNode) bool {
	for _, t := range n.Transports {
		if t.Kind == "lan" {
			return true
		}
	}
	return false
}

// edgeVia names the relay a node is reached THROUGH, or "" when it is reached directly.
// A node with both transports is NOT relayed: the fleet keeps them in preference order, and
// the preferred one is the LAN link, so the node is drawn once on the edge it actually uses.
func edgeVia(n store.EdgeNode) string {
	if edgeHasLAN(n) {
		return ""
	}
	for _, t := range n.Transports {
		if t.Kind == "relay" {
			return t.Addr
		}
	}
	return ""
}

// edgeArrange orders the fleet for drawing: every node hangs off self, and a node reached
// through a relay that is ITSELF on this Edge is drawn immediately under that relay, so the
// hop is a thing you can see rather than a thing you have to know.
//
// It draws EVERY member exactly once, which is the half of the rule that is easy to lose: a
// relay chain, or two nodes that name each other as their relay, must not make a node
// disappear off the fleet view - so anything the walk did not reach is drawn on its own dim
// edge at the end.
func edgeArrange(list []store.EdgeNode) []edgeRow {
	member := make(map[string]bool, len(list))
	for _, n := range list {
		member[n.Name] = true
	}
	via := map[string]string{}
	children := map[string][]store.EdgeNode{}
	for _, n := range list {
		v := edgeVia(n)
		if v == "" || v == n.Name || !member[v] {
			continue
		}
		via[n.ID] = v
		children[v] = append(children[v], n)
	}
	rows := make([]edgeRow, 0, len(list))
	drawn := make(map[string]bool, len(list))
	var emit func(n store.EdgeNode, parent string)
	emit = func(n store.EdgeNode, parent string) {
		if drawn[n.ID] {
			return // a cycle, or a node already placed under its relay
		}
		drawn[n.ID] = true
		rows = append(rows, edgeRow{
			n: n, relay: len(children[n.Name]) > 0, child: parent != "", via: parent})
		for _, c := range children[n.Name] {
			emit(c, n.Name)
		}
	}
	for _, n := range list {
		if via[n.ID] == "" {
			emit(n, "")
		}
	}
	for _, n := range list {
		emit(n, "") // whatever a cycle stranded, drawn rather than dropped
	}
	return rows
}

// edgeSelectable is the selection order: the fleet as drawn, then the candidates.
func (m model) edgeSelectable() []store.EdgeNode {
	out := m.edgeNodes()
	return append(out, m.edge.cands...)
}

// clampEdgeSel keeps the selection on the SAME node across snapshots. A node going dark,
// a node arriving, a node forgotten - none of them may move the operator's cursor onto a
// different machine, because the next key they press acts on it.
func (m *model) clampEdgeSel() {
	sel := m.edgeSelectable()
	for _, n := range sel {
		if n.ID == m.edge.sel {
			return
		}
	}
	m.edge.sel = ""
	if len(sel) > 0 {
		m.edge.sel = sel[0].ID
	}
}

func (m *model) moveEdgeSel(d int) {
	sel := m.edgeSelectable()
	if len(sel) == 0 {
		return
	}
	at := 0
	for i, n := range sel {
		if n.ID == m.edge.sel {
			at = i
			break
		}
	}
	at += d
	if at < 0 {
		at = 0
	}
	if at >= len(sel) {
		at = len(sel) - 1
	}
	m.edge.sel = sel[at].ID
}

// edgeSelected returns the selected record and whether it is a candidate.
func (m model) edgeSelected() (store.EdgeNode, bool, bool) {
	for _, r := range m.edge.rows {
		if r.n.ID == m.edge.sel {
			return r.n, false, true
		}
	}
	for _, c := range m.edge.cands {
		if c.ID == m.edge.sel {
			return c, true, true
		}
	}
	return store.EdgeNode{}, false, false
}

// onEdgeKey drives the screen: move the selection, open a node, adopt a candidate, leave.
func (m *model) onEdgeKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := k.String()
	if m.edge.detail {
		switch key {
		case "esc", "q", "enter", "i":
			m.edge.detail = false
			return m, nil
		}
	}
	// The preset bank jumps, exactly as the other numbered screens allow. 3 is this
	// screen, so it is a no-op rather than a re-entry that would reset the selection.
	if key != "3" {
		if nm, cmd, ok := m.presetForKey(key); ok {
			return nm, cmd
		}
	}
	switch key {
	case "esc", "q":
		m.leaveEdge()
	case "up", "k":
		m.moveEdgeSel(-1)
	case "down", "j":
		m.moveEdgeSel(+1)
	case "enter", "i":
		if m.edge.sel != "" {
			m.edge.detail = true
		}
	case "r":
		m.refreshEdge()
		m.status = stDim.Render("EDGE - re-read the fleet")
	case "a":
		m.adoptEdgeCandidate()
	}
	return m, nil
}

// adoptEdgeCandidate is the owner's explicit action that turns a candidate into a member.
// Nothing else does: seeing a machine on your network is not deciding it is yours.
func (m *model) adoptEdgeCandidate() {
	n, isCand, ok := m.edgeSelected()
	if !ok || !isCand {
		m.status = stDim.Render("a adopts a CANDIDATE - select one first")
		return
	}
	if m.hooks.EdgeAdopt == nil {
		m.status = stEmber.Render("this build cannot adopt - no fleet is wired to the Edge screen")
		return
	}
	if err := m.hooks.EdgeAdopt(n.ID, n.Name); err != nil {
		m.status = stEmber.Render("adopt " + n.Name + ": " + err.Error())
		return
	}
	m.refreshEdge()
	m.status = stLive.Render(n.Name + " adopted onto your Edge")
}

// ---- the animation -------------------------------------------------------

// edgeHeartbeatMsg is a REAL event: this node was heard from, over the transport named by
// the graph. It is the ONLY thing that starts a pulse.
type edgeHeartbeatMsg struct{ node string }

// edgeAnimMsg advances the pulses that are already travelling. On its own it moves nothing.
type edgeAnimMsg struct{}

// EdgeHeartbeat is how the host tells the TUI a node was heard from.
func EdgeHeartbeat(nodeID string) tea.Msg { return edgeHeartbeatMsg{node: nodeID} }

// edgeBeatsClosedMsg says the host's heartbeat channel is closed - the host has gone away.
// It is a distinct message because it must NOT re-arm the drain: reading a closed channel
// returns instantly, so re-arming would spin a goroutine flat out for the life of the run.
type edgeBeatsClosedMsg struct{}

// waitEdgeHeartbeat is the drain over the host's heartbeat channel: one read, one message,
// re-armed by the update loop. It is the only path a REAL sighting takes into the
// animation, and with no host wired (nil channel) there is no drain at all.
func waitEdgeHeartbeat(ch <-chan string) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		id, ok := <-ch
		if !ok {
			return edgeBeatsClosedMsg{}
		}
		return EdgeHeartbeat(id)
	}
}

func edgeAnimCmd() tea.Cmd {
	return tea.Tick(edgeAnimEvery, func(time.Time) tea.Msg { return edgeAnimMsg{} })
}

// edgeResume re-arms the animation on returning to the screen - from where the pulses
// actually are, not from the start.
func (m model) edgeResume() tea.Cmd {
	if m.mode != modeEdge || len(m.edge.pulses) == 0 {
		return nil
	}
	return edgeAnimCmd()
}

// onEdgeHeartbeat puts a pulse at the far end of the edge this heartbeat arrived on - and
// only that edge. A heartbeat from a node the graph does not draw animates nothing.
func (m model) onEdgeHeartbeat(msg edgeHeartbeatMsg) (tea.Model, tea.Cmd) {
	if msg.node == "" || m.edgeRowOf(msg.node) < 0 {
		return m, nil
	}
	// COPY before mutating: the model is passed by value but a slice is not, so writing
	// through the old backing array would reach every other copy of the model.
	pulses := make([]edgePulse, 0, len(m.edge.pulses)+1)
	restarted := false
	for _, p := range m.edge.pulses {
		if p.id == msg.node {
			p.step, restarted = 0, true
		}
		pulses = append(pulses, p)
	}
	if !restarted {
		pulses = append(pulses, edgePulse{id: msg.node})
	}
	m.edge.pulses = pulses
	if m.mode != modeEdge {
		// Not visible: the state is kept so returning resumes from it, but nothing is
		// animated for a screen nobody is looking at.
		return m, nil
	}
	return m, edgeAnimCmd()
}

// onEdgeAnim advances every live pulse one cell and asks for the next frame only while
// there is something to animate AND somebody looking at it.
func (m model) onEdgeAnim() (tea.Model, tea.Cmd) {
	if m.mode != modeEdge || len(m.edge.pulses) == 0 {
		return m, nil
	}
	next := make([]edgePulse, 0, len(m.edge.pulses))
	for _, p := range m.edge.pulses {
		p.step++
		if p.step < m.edgePulseCells(p.id) {
			next = append(next, p) // still travelling; otherwise it has arrived
		}
	}
	m.edge.pulses = next
	if len(next) == 0 {
		return m, nil
	}
	return m, edgeAnimCmd()
}

func (m model) edgeRowOf(id string) int {
	for i, r := range m.edge.rows {
		if r.n.ID == id {
			return i
		}
	}
	return -1
}

// edgePulseCells is how many cells this node's path to self is, at the CURRENT geometry: a
// direct node's own run, and a relayed node's own run plus the relay's run to self.
func (m model) edgePulseCells(id string) int {
	at := m.edgeRowOf(id)
	if at < 0 {
		return 0
	}
	g := edgeGeomFor(m.effWidth(), len(m.edge.rows))
	if g.layout == edgeLayoutList {
		return 0 // a list has no edges to travel
	}
	if m.edge.rows[at].child {
		return g.childRunW + g.runW
	}
	return g.runW
}

// edgePulseAt returns the pulse cell on row i, or -1. A relayed node's pulse crosses TWO
// rows in order: its own run first, then the relay's run to self.
func (m model) edgePulseAt(g edgeGeom, i int) int {
	for _, p := range m.edge.pulses {
		at := m.edgeRowOf(p.id)
		if at < 0 {
			continue
		}
		r := m.edge.rows[at]
		if !r.child {
			if at == i {
				return p.step
			}
			continue
		}
		if at == i && p.step < g.childRunW {
			return p.step
		}
		// Past its own run the pulse is on the relay's edge to self.
		if p.step >= g.childRunW && m.edge.rows[i].n.Name == r.via {
			return p.step - g.childRunW
		}
	}
	return -1
}

// edgeAge renders how long ago something was, in one cell's worth of characters.
func edgeAge(d time.Duration) string {
	switch {
	case d < 0:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// edgeCapMark is the capability vocabulary as MARKS: a distinct two-letter mark per
// capability, UPPER for VERIFIED and lower for merely CLAIMED. Case, not colour, so the
// difference survives NO_COLOR and a pipe.
var edgeCapMark = map[edge.Capability]string{
	edge.Serve:    "sv",
	edge.Classify: "cl",
	edge.Sense:    "sn",
	edge.Actuate:  "ac",
	edge.Relay:    "rl",
	edge.Operate:  "op",
}
