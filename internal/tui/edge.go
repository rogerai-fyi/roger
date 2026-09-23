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
	"strings"
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
	rows  []edgeRow
	cands []store.EdgeNode
	at    time.Time // when the snapshot was taken; every age on screen is measured from it
	err   string    // the fleet could not be read (shown, never swallowed)
	sel   string    // the selected node/candidate id - STICKY across fleet changes
	// sessions is the frame's session snapshot: the traffic this Edge really carried,
	// read once per frame like everything else here. It is READ-ONLY to the screen -
	// there is no path from the view back into the ledger that could open one.
	sessions []edge.Session
	// status is this machine's own place on the Edge (enrolled? rooted where? scanning?),
	// read once per frame like everything else; nil when no hook is wired.
	status *edge.SelfStatus
	detail bool
	pulses []edgePulse
	ret    mode // where esc goes back to
	retSet bool
	// setup is the onboarding wizard's state when it is open; nil when closed. It is a
	// sub-state of this screen (features/edge/onboard.feature), never a separate mode, so
	// the fleet keeps drawing behind it and esc returns here.
	setup *edgeSetup
	// renaming + renameBuf drive the inline "Rename" prompt for this roger.
	renaming  bool
	renameBuf string
	// binding + bindBuf + bindTarget drive the inline "Bind Model" prompt (jobs.feature): the
	// owner types a model to put on air for the selected agent. bindTarget is that agent's name.
	binding    bool
	bindBuf    string
	bindTarget string
	// netSel is the highlighted node in the network map (index into edgeNetItems): the machine,
	// then the rogers/agents on it. Arrow keys move it; the actions act on it. Until the owner first
	// moves (netSelSet), the highlight defaults to THIS roger, so the commonest action (make this
	// roger an agent, rename) is right there without navigating.
	netSel    int
	netSelSet bool
	// netDetail is open when the owner pressed ⏎ on a node in the network map: a full-card readout
	// of that node - its kind, capabilities, the models it serves (shares) or uses, its resources,
	// and its job. esc/⏎ closes it back to the map.
	netDetail bool
	// actionFocused moves the highlight OFF the node map and onto the WHAT YOU CAN DO buttons, so
	// the owner can arrow to a button and press enter to run it (2026-09-23: the founder's instinct
	// was to arrow-key to "Bind Model"). actionSel is the highlighted button while focused. The
	// letter shortcuts (g/b/n/...) still work regardless.
	actionFocused bool
	actionSel     int
	// mapList toggles the topology between the card MAP (default, spatial) and a compact LIST/table
	// (one row per node), for a dense read when there are many nodes or a small screen. `v` switches.
	mapList bool
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
	m.edge.netDetail = false
	m.edge.actionFocused = false
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
	m.edge.netDetail = false
}

// refreshEdge takes the frame's snapshot. Everything the view draws comes from here, and
// nothing the view draws is read live - that is what makes a frame internally consistent
// even when the fleet moves underneath it.
func (m *model) refreshEdge() {
	m.edge.at = m.edgeNow()
	m.edge.err = ""
	m.edge.status = nil
	if m.hooks.EdgeStatus != nil {
		st := m.hooks.EdgeStatus()
		m.edge.status = &st
	}
	var list []store.EdgeNode
	if m.hooks.EdgeFleet != nil {
		got, err := m.hooks.EdgeFleet.List()
		if err != nil {
			m.edge.err = err.Error()
		}
		list = got
	}
	// This machine is drawn ONCE - as the SELF hub, never also as a fleet row. When it is set
	// up, drop its own (possibly stale/dark) node record from the rows so it does not appear as
	// a ghost of itself below the hub. Its live state comes from the hub + RUNNING HERE.
	if st := m.edge.status; st != nil && (st.Enrolled || st.AuthorityHere) {
		self := m.edgeSelfName()
		kept := list[:0:0]
		for _, n := range list {
			if n.Name == self {
				continue
			}
			kept = append(kept, n)
		}
		list = kept
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

// edgeHasLAN, edgeVia and edgeArrange are the SHARED view rules (internal/edge/view.go):
// the console's EDGE tab arranges the same fleet with the same function, so the two
// windows can never disagree about which relay a node is drawn under.
func edgeHasLAN(n store.EdgeNode) bool { return edge.HasLANTransport(n) }

func edgeVia(n store.EdgeNode) string { return edge.Via(n) }

func edgeArrange(list []store.EdgeNode) []edgeRow {
	shared := edge.Arrange(list)
	rows := make([]edgeRow, 0, len(shared))
	for _, r := range shared {
		rows = append(rows, edgeRow{n: r.Node, relay: r.Relay, child: r.Child, via: r.Via})
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
	// The onboarding wizard, when open, owns every key - it is a modal sub-state of this
	// screen (features/edge/onboard.feature), never a separate mode. It swallows the preset
	// bank too, so typing a name that starts with a digit does not jump screens.
	if m.edge.setup != nil && m.edge.setup.open {
		mm, cmd, _ := m.onEdgeSetupKey(k)
		return mm, cmd
	}
	// The inline Rename prompt owns every key while it is open.
	if m.edge.renaming {
		m.onEdgeRenameKey(k)
		return m, nil
	}
	if m.edge.binding {
		m.onEdgeBindKey(k)
		return m, nil
	}
	if m.edge.detail {
		switch key {
		case "esc", "q", "enter", "i":
			m.edge.detail = false
			return m, nil
		}
	}
	if m.edge.netDetail {
		switch key {
		case "esc", "q", "enter", "i":
			m.edge.netDetail = false
			return m, nil
		case "up", "k", "left":
			m.edgeMoveNetSel(-1)
			return m, nil
		case "down", "j", "right":
			m.edgeMoveNetSel(+1)
			return m, nil
		}
	}
	// Navigation and the action buttons own the arrow keys on THIS screen, so a stray left/right
	// never falls through to the preset bank and jumps to another tab mid-navigation (2026-09-23:
	// the founder pressed right to reach a button and landed on [4]). esc/q leave; number keys jump.
	if nm, cmd, handled := m.onEdgeNavKey(key); handled {
		return nm, cmd
	}
	// The preset bank jumps by NUMBER (0-4 / L / ?); 3 is this screen, so it is a no-op.
	if key != "3" {
		if nm, cmd, ok := m.presetForKey(key); ok {
			return nm, cmd
		}
	}
	switch key {
	case "esc", "q":
		m.leaveEdge()
	case "r":
		m.refreshEdge()
		m.status = stDim.Render("EDGE - re-read the fleet")
	case "v":
		m.edge.mapList = !m.edge.mapList
		if m.edge.mapList {
			m.status = stDim.Render("EDGE - list view (v for the map)")
		} else {
			m.status = stDim.Render("EDGE - map view (v for the list)")
		}
	case "e":
		// Open the onboarding wizard - but only where it makes sense: on a machine that is
		// not already a member. Once enrolled, e is a no-op (the screen is unchanged), so a
		// stray press never re-runs setup on a working node.
		if !m.edgeSetupState().Enrolled {
			m.openEdgeSetup()
		}
	case "g", "+", "s", "n", "y", "u", "x", "b", "a":
		// The lettered actions - each also reachable by focusing its button (Tab / down) and
		// pressing enter. One dispatch, so a key press and a button press do the same thing.
		return m.edgeDispatchAction(key)
	}
	return m, nil
}

// onEdgeNavKey moves the highlight - across the NODE map, or (once focus drops into the panel) along
// the WHAT YOU CAN DO buttons. It returns handled=true for any key it consumed, so those keys never
// reach the preset bank. Tab or down-past-the-last-node moves focus onto the buttons; up leaves them.
func (m *model) onEdgeNavKey(key string) (tea.Model, tea.Cmd, bool) {
	acts := m.edgeActions()
	if m.edge.actionFocused && len(acts) == 0 {
		m.edge.actionFocused = false
	}
	if m.edge.actionSel >= len(acts) {
		m.edge.actionSel = max(0, len(acts)-1)
	}
	switch key {
	case "tab":
		if !m.edge.actionFocused {
			if len(acts) > 0 {
				m.edge.actionFocused, m.edge.actionSel = true, 0
			}
		} else if m.edge.actionSel++; m.edge.actionSel >= len(acts) {
			m.edge.actionFocused, m.edge.actionSel = false, 0 // past the last button, back to the map
		}
		return m, nil, true
	case "shift+tab":
		if m.edge.actionFocused {
			if m.edge.actionSel > 0 {
				m.edge.actionSel--
			} else {
				m.edge.actionFocused = false
			}
		}
		return m, nil, true
	case "left":
		if m.edge.actionFocused {
			if m.edge.actionSel > 0 {
				m.edge.actionSel--
			}
		} else {
			m.edgeMoveSel(-1)
		}
		return m, nil, true
	case "right":
		if m.edge.actionFocused {
			if m.edge.actionSel < len(acts)-1 {
				m.edge.actionSel++
			}
		} else {
			m.edgeMoveSel(+1)
		}
		return m, nil, true
	case "up", "k":
		if m.edge.actionFocused {
			m.edge.actionFocused = false // back up onto the node map
		} else {
			m.edgeMoveSel(-1)
		}
		return m, nil, true
	case "down", "j":
		switch {
		case m.edge.actionFocused:
			// already in the bottom zone
		case m.edgeAtLastNode() && len(acts) > 0:
			m.edge.actionFocused, m.edge.actionSel = true, 0 // drop into the actions
		default:
			m.edgeMoveSel(+1)
		}
		return m, nil, true
	case "enter", "i":
		if m.edge.actionFocused {
			nm, cmd := m.edgeActivateFocusedAction()
			return nm, cmd, true
		}
		switch {
		case m.edgeMapActive():
			m.edge.netDetail = true
		case m.edge.sel != "":
			m.edge.detail = true
		}
		return m, nil, true
	}
	return m, nil, false
}

// edgeMoveSel moves the node highlight one step (map or graph), and drops any action-button focus -
// moving through nodes is a map gesture, so it returns attention to the map.
func (m *model) edgeMoveSel(d int) {
	m.edge.actionFocused = false
	if m.edgeMapActive() {
		m.edgeMoveNetSel(d)
	} else {
		m.moveEdgeSel(d)
	}
}

// edgeAtLastNode reports whether the map highlight is on the last node, so a further "down" flows
// into the action buttons instead of clamping. Only the map does this; the graph list keeps its own.
func (m model) edgeAtLastNode() bool {
	if !m.edgeMapActive() {
		return false
	}
	n := len(m.edgeNetItems())
	_, sel := m.edgeSelectedNet()
	return n == 0 || sel >= n-1
}

// edgeActivateFocusedAction runs the button the panel highlight is on - the same as pressing its
// letter. After it, the focus stays on the panel so the owner can keep acting.
func (m *model) edgeActivateFocusedAction() (tea.Model, tea.Cmd) {
	acts := m.edgeActions()
	if m.edge.actionSel < 0 || m.edge.actionSel >= len(acts) {
		return m, nil
	}
	return m.edgeDispatchAction(acts[m.edge.actionSel].key)
}

// edgeDispatchAction performs one lettered action - the single place a key press and a focused
// button press both land, so they never drift apart.
func (m *model) edgeDispatchAction(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "g":
		return m, m.edgeToggleAgent()
	case "+":
		return m, m.edgeAddAgent()
	case "s":
		// Add a device: on a machine that has set nothing up, open the wizard to set THIS one up;
		// on a set-up machine, show the exact command the OTHER machine runs to join.
		if st := m.edge.status; st != nil && st.Enrolled {
			m.edgeShowAddDevice()
		} else {
			m.openEdgeSetup()
		}
	case "n":
		m.edgeStartRename()
	case "y":
		m.edgeShowAllowKey()
	case "u":
		m.edgeResumeSelected()
	case "x":
		m.edgeRemoveSelected()
	case "b":
		m.edgeStartBind()
	case "a":
		if m.edgeMapActive() {
			m.edgeAdoptSelectedNet()
		} else {
			m.adoptEdgeCandidate()
		}
	}
	return m, nil
}

// edgeStartBind opens the inline "Bind Model" prompt for the selected agent - the founder's
// agent-1 uses qwen, agent-2 uses pico (2026-09-21). It binds to a running instance node only;
// the machine and a dark agent have nothing to serve.
func (m *model) edgeStartBind() {
	it, _ := m.edgeSelectedNet()
	if it.machine || it.dark || it.inst == nil {
		m.status = stDim.Render("Bind Model acts on a running roger - select one first")
		return
	}
	if m.hooks.EdgeSetAgentJob == nil {
		m.status = stEmber.Render("this build cannot bind a model here - run `roger edge model " + it.name + " <model>`")
		return
	}
	m.edge.binding = true
	m.edge.bindTarget = it.name
	m.edge.bindBuf = ""
	if j, ok := m.edgeJobOf(it.name); ok {
		m.edge.bindBuf = j.Model
	}
}

// onEdgeBindKey drives the bind prompt: type a model, enter to put it on air (serve + share, the
// common path), esc to cancel. Precise postures (use-only) are the `roger edge model` command.
func (m *model) onEdgeBindKey(k tea.KeyMsg) {
	switch k.Type {
	case tea.KeyEsc:
		m.edge.binding = false
	case tea.KeyEnter:
		model := strings.TrimSpace(m.edge.bindBuf)
		target := m.edge.bindTarget
		m.edge.binding = false
		if model == "" {
			return
		}
		j := edge.AgentJob{Name: target, Model: model, Share: true, Job: "serve"}
		if err := m.hooks.EdgeSetAgentJob(j); err != nil {
			m.status = stEmber.Render("bind: " + err.Error())
			return
		}
		m.status = stLive.Render(target + " now serves " + model + " on the Edge")
		m.refreshEdge()
	case tea.KeyBackspace, tea.KeyDelete:
		if n := len(m.edge.bindBuf); n > 0 {
			m.edge.bindBuf = m.edge.bindBuf[:n-1]
		}
	case tea.KeyRunes:
		m.edge.bindBuf += string(k.Runes)
	}
}

// edgeResumeSelected relaunches the selected DARK persistent agent - the "Resume" button. It acts
// on the highlighted node, so the owner resumes the agent they are looking at, not a fixed one.
func (m *model) edgeResumeSelected() {
	it, _ := m.edgeSelectedNet()
	if !it.dark {
		m.status = stDim.Render("Resume acts on an offline agent - select a ◇ node first")
		return
	}
	if m.hooks.EdgeResumeAgent == nil {
		m.status = stEmber.Render("this build cannot resume an agent - run `roger edge agent resume " + it.name + "`")
		return
	}
	if err := m.hooks.EdgeResumeAgent(it.name); err != nil {
		m.status = stEmber.Render("could not resume " + it.name + ": " + err.Error())
		return
	}
	m.status = stLive.Render("resuming " + it.name + " - it returns to the rogers here shortly")
}

// edgeRemoveSelected forgets the selected persistent agent - the "Remove" button. It drops the
// durable record only; a live process is left alone (remove ends the persistence, not the run).
func (m *model) edgeRemoveSelected() {
	it, _ := m.edgeSelectedNet()
	if !it.dark {
		m.status = stDim.Render("Remove forgets an offline agent - select a ◇ node first")
		return
	}
	if m.hooks.EdgeRemoveAgent == nil {
		m.status = stEmber.Render("this build cannot remove an agent - run `roger edge agent remove " + it.name + "`")
		return
	}
	if err := m.hooks.EdgeRemoveAgent(it.name); err != nil {
		m.status = stEmber.Render("could not remove " + it.name + ": " + err.Error())
		return
	}
	if m.edge.netSel > 0 {
		m.edge.netSel--
	}
	m.status = stDim.Render("removed " + it.name + " from the Edge - it is no longer offered to resume")
	m.refreshEdge()
}

// edgeToggleAgent makes this roger an agent, or stops it, in place - the "Make/Stop Agent" button.
func (m *model) edgeToggleAgent() tea.Cmd {
	if m.hooks.EdgeSetAgent == nil {
		m.status = stEmber.Render("this build cannot change the agent role here")
		return nil
	}
	on := !(m.hooks.EdgeAgentActive != nil && m.hooks.EdgeAgentActive())
	if err := m.hooks.EdgeSetAgent(on); err != nil {
		m.status = stEmber.Render("could not change the agent role: " + err.Error())
		return nil
	}
	if on {
		m.status = stLive.Render("this roger is now an agent (◆) - it can act on the fleet")
	} else {
		m.status = stDim.Render("this roger is no longer an agent")
	}
	m.refreshEdge()
	return nil
}

// edgeAddAgent launches a new agent roger on this machine - the "Add Agent" button.
func (m *model) edgeAddAgent() tea.Cmd {
	if m.hooks.EdgeAddAgent == nil {
		m.status = stEmber.Render("this build cannot add an agent here")
		return nil
	}
	if err := m.hooks.EdgeAddAgent(); err != nil {
		m.status = stEmber.Render("could not add an agent: " + err.Error())
		return nil
	}
	m.status = stLive.Render("launched a new agent - it joins the rogers here shortly")
	return nil
}

// edgeShowAddDevice puts the exact command another machine runs to join on the status line - the
// "Add Device" button. Adding a device is the other machine's action, so this is what to hand it.
func (m *model) edgeShowAddDevice() {
	line := "roger edge setup"
	if st := m.edge.status; st != nil {
		line = st.EnrollAgainstLine()
	}
	m.status = stKey.Render("on the other machine, run: ") + stEmber.Render(line) +
		stDim.Render("  (or roger edge setup to join interactively)")
}

// edgeStartRename opens the inline rename prompt, seeded with this roger's current name.
func (m *model) edgeStartRename() {
	if m.hooks.EdgeRenameSelf == nil {
		m.status = stEmber.Render("this build cannot rename this roger here")
		return
	}
	m.edge.renaming = true
	m.edge.renameBuf = ""
	if m.hooks.EdgeSelfInstance != nil {
		m.edge.renameBuf = m.hooks.EdgeSelfInstance()
	}
}

// onEdgeRenameKey drives the inline rename prompt: type, enter to apply, esc to cancel.
func (m *model) onEdgeRenameKey(k tea.KeyMsg) {
	switch k.Type {
	case tea.KeyEsc:
		m.edge.renaming = false
	case tea.KeyEnter:
		name := strings.TrimSpace(m.edge.renameBuf)
		m.edge.renaming = false
		if name == "" {
			return
		}
		if err := m.hooks.EdgeRenameSelf(name); err != nil {
			m.status = stEmber.Render("rename: " + err.Error())
			return
		}
		m.status = stLive.Render("renamed this roger to " + name)
		m.refreshEdge()
	case tea.KeyBackspace, tea.KeyDelete:
		if n := len(m.edge.renameBuf); n > 0 {
			m.edge.renameBuf = m.edge.renameBuf[:n-1]
		}
	case tea.KeyRunes:
		m.edge.renameBuf += string(k.Runes)
	}
}

// edgeShowAllowKey puts this machine's user key on the status line so the owner can hand it to a
// machine they want to admit - the "Allow Machine" button.
func (m *model) edgeShowAllowKey() {
	if m.hooks.EdgeSetup == nil || m.hooks.EdgeSetup.UserKeyHex == nil {
		m.status = stEmber.Render("run `roger account` on the other machine for its key, then `roger edge authority allow <key>` here")
		return
	}
	m.status = stKey.Render("allow this on another machine's owner: ") + stEmber.Render("roger edge authority allow "+m.hooks.EdgeSetup.UserKeyHex())
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
	m.status = edgeAdoptedMessage(n.Name, n.Pin)
}

// edgeAdoptSelectedNet adopts the candidate the network-map highlight is on - the UniFi moment for
// a phone or machine discovered on the LAN. It acts only on a DISCOVERED node; anything else is a
// no-op with a nudge, so a stray press never adopts the wrong thing.
func (m *model) edgeAdoptSelectedNet() {
	it, _ := m.edgeSelectedNet()
	if !it.cand {
		m.status = stDim.Render("a adopts a DISCOVERED machine - select one in that band first")
		return
	}
	if m.hooks.EdgeAdopt == nil {
		m.status = stEmber.Render("this build cannot adopt - no fleet is wired to the Edge screen")
		return
	}
	if err := m.hooks.EdgeAdopt(it.id, it.name); err != nil {
		m.status = stEmber.Render("adopt " + it.name + ": " + err.Error())
		return
	}
	m.refreshEdge()
	pin := ""
	if it.node != nil {
		pin = it.node.Pin
	}
	m.status = edgeAdoptedMessage(it.name, pin)
}

// edgeAdoptedMessage is the confirmation after adopting a candidate. A candidate that advertised no
// certificate (an empty pin) joins by CLAIM - it is not a member yet, it is authorised to become
// one - so the message says so rather than claiming a membership that has not happened.
func edgeAdoptedMessage(name, pin string) string {
	if pin == "" {
		return stLive.Render(name+" can now claim its certificate") +
			stDim.Render(" - it appears as a member when it checks in")
	}
	return stLive.Render(name + " adopted onto your Edge")
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

// edgeAge is the shared age wording (edge.Age): "6m", "3h", "2d".
func edgeAge(d time.Duration) string { return edge.Age(d) }

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
