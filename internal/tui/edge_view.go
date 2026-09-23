package tui

// THE EDGE SCREEN's drawing. A pure function of the snapshot in edgeState: given the same
// snapshot and the same width it draws the same frame, which is what makes a fleet that
// changes mid-render harmless.
//
// GEOMETRY IS MEASURED, NEVER GUESSED (the rule helpers.go states, learned on the [4] CONFIG
// edit box). Every row is built from columns whose widths are computed from the terminal,
// every cell is padded to its column, and every finished line is clipped to the terminal.
// The drawing degrades in two honest steps rather than breaking: a compact graph when the
// runs no longer fit, and a LIST - which says how many of how many it is showing - when the
// picture would be a lie.
//
// Spec: features/edge/topology_view.feature.

import (
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/glyphs"
	"rogerai.fm/roger/v6/internal/store"
)

// The drawing vocabulary. The three edge textures are three DIFFERENT glyphs, not three
// colours: under NO_COLOR, in a pipe, and on a legacy console they stay distinguishable.
const (
	edgeGlyphSolid  = '─' // LAN-direct, verified by this instance
	edgeGlyphDim    = '┈' // a path through a relay
	edgeGlyphBroken = '╌' // a node whose heartbeat aged out (drawn gappy)
	edgeGlyphPulse  = '●' // a heartbeat, on the edge it arrived on
	edgeGlyphSelf   = "▣"
	edgeGlyphSel    = '›'
)

const (
	edgeLayoutGraph   = "graph"
	edgeLayoutCompact = "compact graph"
	edgeLayoutList    = "list"
)

// edgeASCII folds this screen's own glyphs for a legacy console. Solid/dim/broken stay
// three different characters there too.
var edgeASCII = strings.NewReplacer(
	"─", "-", "┈", ".", "╌", "=", "●", "*", "▣", "#", "›", ">",
	// device emblems: self / board / phone (host is ▣ above)
	"◉", "@", "▤", "b", "▯", "m", "◆", "A", "◇", "a",
	// The session layer's three marks stay three DIFFERENT characters here too.
	"▸", "}", "▲", "^", "✕", "x", "→", "->", "×", "*",
	"├", "+", "└", "+", "│", "|", "╭", "+", "╮", "+", "╰", "+", "╯", "+", "┬", "+",
)

func edgeFold(s string) string {
	if glyphs.ASCII() {
		return edgeASCII.Replace(s)
	}
	return s
}

// edgeGeom is the measured layout for one width.
type edgeGeom struct {
	w         int
	layout    string
	spine     int // the column the connector sits in
	runW      int // cells in a node's edge run to self
	childRunW int // cells in a relayed node's own run to its relay
	lead      int // the column the name column starts in
	nameW     int
	marksW    int
	detailW   int
}

// edgeLayoutFor picks the honest drawing for this terminal and this fleet.
func edgeLayoutFor(w, nodes int) string {
	switch {
	case w < 56 || nodes > edgeMaxGraphNodes:
		return edgeLayoutList
	case w < 80:
		return edgeLayoutCompact
	default:
		return edgeLayoutGraph
	}
}

func edgeGeomFor(w, nodes int) edgeGeom {
	if w <= 0 {
		w = 80
	}
	g := edgeGeom{w: w, spine: 5, layout: edgeLayoutFor(w, nodes)}
	minDetail := 0
	switch g.layout {
	case edgeLayoutGraph:
		g.runW, g.nameW, g.marksW = 10, 22, 19
	case edgeLayoutCompact:
		g.runW, g.nameW, g.marksW = 4, 14, 19
	default:
		// A list has no runs to draw, so its columns start at the margin - and it keeps a
		// state column, because a list with no state is just names.
		g.runW, g.nameW, g.marksW, minDetail = 0, 20, 19, 12
	}
	g.childRunW = g.runW / 2
	g.lead = 2
	if g.layout != edgeLayoutList {
		g.lead = g.spine + g.runW + 2
	}
	fits := func() bool { return g.lead+g.nameW+1+g.marksW+1+minDetail <= w }
	for g.marksW > 6 && !fits() {
		g.marksW--
	}
	for g.nameW > 8 && !fits() {
		g.nameW--
	}
	g.detailW = w - (g.lead + g.nameW + 1 + g.marksW + 1)
	if g.detailW < 0 {
		g.detailW = 0
	}
	return g
}

// edgeSelfName is what this instance is called on its own graph.
func (m model) edgeSelfName() string {
	if n := strings.TrimSpace(m.hooks.EdgeSelf); n != "" {
		return n
	}
	if n := strings.TrimSpace(m.hooks.Station); n != "" {
		return n
	}
	return "this machine"
}

// edgeMarks renders a node's capability marks: a distinct two-letter mark per capability,
// UPPER for VERIFIED and lower for CLAIMED (or, for actuate, awaiting the owner). Nothing
// routes on a lower-case mark, and the case says so without a colour.
func edgeMarks(n store.EdgeNode) string {
	var out []string
	for _, c := range edge.Capabilities() {
		for _, d := range n.Caps {
			if d.Name != string(c) {
				continue
			}
			mk := edgeCapMark[c]
			if d.State == string(edge.Verified) {
				mk = strings.ToUpper(mk)
			}
			out = append(out, mk)
		}
	}
	if len(out) == 0 {
		return ""
	}
	return "[" + strings.Join(out, " ") + "]"
}

// edgeWidestMarks is the widest capability cell in this snapshot, floored so the column
// never collapses to nothing.
func edgeWidestMarks(rows []edgeRow) int {
	widest := 4
	for _, r := range rows {
		if n := len([]rune(edgeMarks(r.n))); n > widest {
			widest = n
		}
	}
	return widest
}

func edgeIsDark(n store.EdgeNode) bool { return n.Presence == string(edge.PresenceDark) }

// edgeTexture is the edge a node has EARNED: solid only for a LAN-direct transport, dim for
// a relayed path, broken for a node we have stopped hearing from.
func edgeTexture(n store.EdgeNode) rune {
	switch {
	case edgeIsDark(n):
		return edgeGlyphBroken
	case edgeHasLAN(n):
		return edgeGlyphSolid
	default:
		return edgeGlyphDim
	}
}

// ---- the view ------------------------------------------------------------

func (m model) edgeView(w int) string {
	if w <= 0 {
		w = 80
	}
	var b strings.Builder
	line := func(s string) {
		b.WriteString(edgeFold(truncVisible(strings.TrimRight(s, " "), w)) + "\n")
	}
	st := m.edge

	// The onboarding wizard is a modal sub-state (features/edge/onboard.feature): while it
	// is open it owns the panel. The fleet's own animation loop keeps running behind it (the
	// pulse loop is never touched), and closing the wizard returns straight to this screen.
	if st.setup != nil && st.setup.open {
		m.edgeSetupView(w, line)
		return b.String()
	}

	if st.detail {
		m.edgeDetailView(w, line)
		return b.String()
	}
	if st.netDetail {
		m.edgeNetDetailView(w, line)
		return b.String()
	}

	// THE GAME CABINET (features/edge/patchbay.feature §8): the whole [3] surface wears the same
	// arcade marquee the setup wizard wears, so the tab reads as one instrument. Its Wave-Spectrum
	// shoulders shift only while real traffic moves (the pulse steps), and sit still on a still
	// fleet - the same motion-from-real-events rule the graph obeys.
	cw := edgeSetupContentWidth(w)
	off := 0
	for _, p := range st.pulses {
		off += p.step + 1
	}
	for _, ln := range strings.Split(edgeCabinet(cw, "◉ YOUR EDGE ◉", off), "\n") {
		line("  " + ln)
	}

	if st.err != "" {
		line("  " + stEmber.Render("the Edge could not be read: "+st.err))
	}
	// A SET-UP machine gets the NETWORK MAP - its hub, the rogers on it, the other member machines,
	// and any discovered candidate waiting to be adopted (a phone on the LAN shows up here). The
	// map draws the whole picture up to edgeMaxGraphNodes; a larger fleet falls through to the
	// graph/list below until the manual grid/list switch lands (features/edge/map_scale.feature).
	if m.edgeMapActive() {
		m.edgeNetworkMap(w, cw, line)
		m.edgeSessionBlock(edgeGeomFor(w, 0), line)
		return b.String()
	}
	if len(st.rows) == 0 && len(st.cands) == 0 {
		if st.err != "" {
			// An Edge that could not be READ is not an empty Edge. Saying "this machine
			// is the only node" here would be the screen's one unforgivable lie.
			line("  " + stDim.Render("nothing can be drawn until the fleet can be read · r retries"))
			return b.String()
		}
		// A machine that has set nothing up gets the onboarding facts + the press-e way in. (A
		// set-up machine with no other nodes already went to the map above.)
		m.edgeEmptyView(w, line)
		// A fleet of one still carries traffic: `roger use` against a market station is
		// a session with no second node on the graph, and hiding it would be the same
		// lie as hiding a node.
		m.edgeSessionBlock(edgeGeomFor(w, 0), line)
		return b.String()
	}

	g := edgeGeomFor(w, len(st.rows))
	g.marksW = min(g.marksW, edgeWidestMarks(st.rows))
	g.detailW = w - (g.lead + g.nameW + 1 + g.marksW + 1)
	if g.detailW < 0 {
		g.detailW = 0
	}
	line("  " + edgeSetupCenter(edgeColorSegments(edgeHeadline(st)), cw))
	line("")
	switch g.layout {
	case edgeLayoutList:
		m.edgeListBody(g, line)
	default:
		m.edgeGraphBody(g, line)
	}
	m.edgeSessionBlock(g, line)
	if len(st.cands) > 0 {
		m.edgeCandidateBlock(g, line)
	}
	line("")
	line("  " + stDim.Render(edgeHints(w)))
	return b.String()
}

// edgeColorSegments tints a " · "-separated faceplate so the header reads as a lit radio
// faceplate (patchbay §8): the root badge in the live hue, local traffic in the signal hue,
// market in the warming hue, everything else in ink. It rejoins on the SAME separator, so the
// text a reader (or a test, or NO_COLOR) sees is byte-identical to the plain headline; only the
// colour is added, and every segment keeps its own words.
func edgeColorSegments(text string) string {
	segs := strings.Split(text, " · ")
	for i, sg := range segs {
		var style lipgloss.Style
		switch {
		case i == 0:
			style = stKey
		case strings.Contains(sg, "ROOT"), strings.Contains(sg, "CORE"):
			style = lampStyle(roleLive)
		case strings.Contains(sg, "market"):
			style = lampStyle(roleDialGlow)
		case strings.Contains(sg, "local"), strings.Contains(sg, "prefers"):
			style = lampStyle(roleSignal)
		case strings.Contains(sg, "dark"):
			style = stDim
		default:
			style = stKey
		}
		segs[i] = style.Render(sg)
	}
	return strings.Join(segs, stDim.Render(" · "))
}

// edgeFactLabelStyle gives each empty-board fact label its lamp hue: this machine in the signal
// hue, the authority in the live hue, discovery in the dial hue, adding a node bright. Colour is
// decoration over a word that already carries it.
func edgeFactLabelStyle(label string) lipgloss.Style {
	switch label {
	case "THIS MACHINE":
		return lampStyle(roleSignal)
	case "AUTHORITY":
		return lampStyle(roleLive)
	case "DISCOVERY":
		return lampStyle(roleDial)
	case "STATUS":
		return stEmber
	default:
		return stKey
	}
}

// edgeHeadline counts what is on the Edge, including what is not answering.
func edgeHeadline(st edgeState) string {
	dark := 0
	for _, r := range st.rows {
		if edgeIsDark(r.n) {
			dark++
		}
	}
	out := "EDGE"
	if b := edgeModeBadge(st); b != "" {
		out += " · " + b
	}
	out += " · " + plural(len(st.rows), "node")
	if dark > 0 {
		out += " · " + strconv.Itoa(dark) + " dark"
	}
	if n := len(st.cands); n > 0 {
		out += " · " + plural(n, "candidate")
	}
	if mix := edgeRouteMix(st.sessions); mix != "" {
		out += " · " + mix
	}
	return out
}

// edgeModeBadge is the ROOT and the PREFERENCE, side by side and never collapsed into one
// word (features/edge/mode.feature): "LOCAL ROOT · shed · prefers local", or "CORE ·
// LOCAL-ONLY". Absent when no status is wired.
func edgeModeBadge(st edgeState) string {
	if st.status == nil {
		return ""
	}
	return st.status.RootBadge() + " · " + st.status.PreferBadge()
}

// edgeRouteMix says where the live sessions went: "3 local · 1 market". A fleet that is
// leaking to the market is obvious from the header alone.
func edgeRouteMix(sessions []edge.Session) string {
	local, market := 0, 0
	for _, s := range sessions {
		if s.Where == edge.WhereMarket {
			market++
		} else if s.Where == edge.WhereLocal {
			local++
		}
	}
	if local+market == 0 {
		return ""
	}
	return strconv.Itoa(local) + " local · " + strconv.Itoa(market) + " market"
}

func edgeHints(w int) string {
	if w < 60 {
		return "↑↓ · ⏎ detail · a adopt · esc"
	}
	return "↑↓ select  ·  ⏎ detail  ·  a adopt a candidate  ·  r re-read  ·  esc back"
}

// edgeNetworkMap is the [3] screen once this machine is set up: its HUB with the rogers running
// on it drawn as connected nodes (not a "waiting" placeholder that would lie when rogers are
// running), WHAT'S NEXT up front so the owner always knows the move, and the details below. It is
// the picture that answers "where is my Edge network, and what do I do next".
func (m model) edgeNetworkMap(w, cw int, line func(string)) {
	st := m.edge.status
	line("  " + edgeColorSegments(edgeEmptyHeadline(m.edge)))
	line("")
	line("  " + edgeSetupCenter(stKey.Render("YOUR EDGE NETWORK")+stDim.Render("   the authority controls your machines, agents & devices"), cw))

	// The topology: cards (spatial) or a compact list (dense), the selected node glowing. `v` toggles.
	if m.edge.mapList {
		m.edgeMapList(cw, line)
	} else {
		m.edgeMapTopology(cw, line)
	}
	line("  " + edgeSetupCenter(stDim.Render("↑↓←→ move · ⏎ open · ")+stKey.Render("↓/Tab")+stDim.Render(" for buttons · ")+stKey.Render("v")+stDim.Render(" map/list"), cw))
	line("")

	// FINDING DEVICES: when devices are discovered they already show in the topology; this only
	// speaks up when nothing is found, to explain where a device appears.
	m.edgeFindDevicesHint(cw, line)

	// WHAT YOU CAN DO - big buttons the owner presses to act, in place, from this screen.
	m.edgeActionBar(cw, line)
	line("")

	// SPARSE vs POPULATED. A machine with nothing else on its Edge yet is in the ONBOARDING state:
	// it gets the full getting-started facts and how-to-add-a-node (features/edge/empty_edge.feature).
	// Once there are other rogers, members or candidates to see, the screen is tight, so it drops to
	// a single live DISCOVERY line - the role is on the card, the how-tos are behind the buttons
	// (founder 2026-09-23: "optimize space ... minimal").
	household := 0
	if m.hooks.EdgeHousehold != nil {
		household = len(m.hooks.EdgeHousehold())
	}
	sparse := len(m.edge.rows) == 0 && len(m.edge.cands) == 0 && household <= 1
	if st == nil {
		return
	}
	if !sparse {
		line("  " + edgeFactLabelStyle("DISCOVERY").Render(pad("DISCOVERY", edgeFactW)) +
			truncVisible(st.DiscoveryLine(m.edge.at), max(20, w-2-edgeFactW)))
		return
	}
	// Onboarding: the enroll command another MACHINE runs, then the three facts in full.
	if st.AuthorityHere && st.AuthorityAddr != "" {
		line("  " + edgeSetupCenter(stDim.Render("another machine joins with  ")+stEmber.Render(st.EnrollAgainstLine()), cw))
		line("")
	}
	fact := func(label string, lines ...string) {
		for i, t := range lines {
			for j, part := range wrapCommand(t, max(20, w-2-edgeFactW)) {
				k := ""
				if i == 0 && j == 0 {
					k = label
				}
				line("  " + edgeFactLabelStyle(label).Render(pad(k, edgeFactW)) + part)
			}
		}
	}
	fact("THIS MACHINE", st.MachineLine())
	fact("AUTHORITY", st.AuthorityLines()...)
	fact("DISCOVERY", st.DiscoveryLine(m.edge.at))
}

// edgeAction is one button on the [3] control panel: a hotkey, a label, a one-line why, and
// whether it is the primary/lit state.
type edgeAction struct {
	key, label, sub string
	lit             bool
}

// edgeActions is the set of buttons for the SELECTED node, in order - the panel acts in place on
// whatever the highlight is on, so the buttons change as you move (2026-09-21, "control the
// highlighted box"). Only actions the build can really perform are offered (an unwired one is
// simply omitted, never a dead button):
//   - a DARK persistent agent: Resume it, or Remove it from the Edge.
//   - THIS roger (the one you are looking at): Make/Stop Agent, Rename.
//   - the machine, or any other node: Add Agent, Add Device, Allow Machine.
func (m model) edgeActions() []edgeAction {
	it, _ := m.edgeSelectedNet()
	var out []edgeAction
	switch {
	case it.cand:
		if m.hooks.EdgeAdopt != nil {
			out = append(out, edgeAction{"a", "Adopt", "bring it onto your Edge", true})
		}
	case it.member:
		// A member machine: nothing to do in place yet beyond opening its detail (⏎).
	case it.dark:
		if m.hooks.EdgeResumeAgent != nil {
			out = append(out, edgeAction{"u", "Resume", "relaunch this agent", true})
		}
		if m.hooks.EdgeRemoveAgent != nil {
			out = append(out, edgeAction{"x", "Remove", "forget this agent", false})
		}
	case it.self:
		if m.hooks.EdgeSetAgent != nil {
			if m.hooks.EdgeAgentActive != nil && m.hooks.EdgeAgentActive() {
				out = append(out, edgeAction{"g", "Stop Agent", "this roger is an agent now", true})
			} else {
				out = append(out, edgeAction{"g", "Make Agent", "let this roger act on the fleet", false})
			}
		}
		if m.hooks.EdgeSetAgentJob != nil {
			out = append(out, edgeAction{"b", "Bind Model", "put a model on air for it", false})
		}
		if m.hooks.EdgeRenameSelf != nil {
			out = append(out, edgeAction{"n", "Rename", "rename this roger", false})
		}
	case it.inst != nil:
		// Another running roger on this machine: bind a model to it in place.
		if m.hooks.EdgeSetAgentJob != nil {
			out = append(out, edgeAction{"b", "Bind Model", "put a model on air for it", false})
		}
	default:
		if m.hooks.EdgeAddAgent != nil {
			out = append(out, edgeAction{"+", "Add Agent", "run a new agent here", false})
		}
		out = append(out, edgeAction{"s", "Add Device", "another machine joins", false})
		if m.edge.status != nil && m.edge.status.AuthorityHere {
			out = append(out, edgeAction{"y", "Allow Machine", "show your key to admit one", false})
		}
	}
	return out
}

// edgeFindDevicesHint tells the owner what is discoverable, or - when nothing is - how a device
// shows up and how to add one, so "where is my phone?" has an answer on the screen (2026-09-23).
func (m model) edgeFindDevicesHint(cw int, line func(string)) {
	// When devices ARE discovered they already show in the DISCOVERED band with "adopt with a" and
	// the Adopt button, so say nothing more here - the screen is tight (2026-09-23). Only when
	// nothing is found is a single quiet line worth the space, to explain where a device appears.
	if len(m.edge.cands) > 0 {
		return
	}
	line("  " + edgeSetupCenter(stDim.Render("no devices yet · a phone or machine running roger on this network appears here to adopt"), cw))
}

// edgeActionBar draws the buttons as bordered cards, packed into rows that fit the width. The lit
// one glows red; each carries its hotkey so it works under NO_COLOR and is obvious what to press.
func (m model) edgeActionBar(cw int, line func(string)) {
	if m.edge.renaming {
		line("  " + stKey.Render("RENAME THIS ROGER") + stDim.Render("  ⏎ apply · esc cancel"))
		line("  " + lampStyle(roleLive).Render("▶ ") + stEmber.Render(m.edge.renameBuf) + lampStyle(roleLive).Render("_"))
		return
	}
	if m.edge.binding {
		line("  " + stKey.Render("PUT A MODEL ON AIR FOR "+strings.ToUpper(m.edge.bindTarget)) + stDim.Render("  ⏎ serve · esc cancel"))
		line("  " + lampStyle(roleLive).Render("▶ ") + stEmber.Render(m.edge.bindBuf) + lampStyle(roleLive).Render("_"))
		line("  " + stDim.Render("shares the model to the Edge · `roger edge model "+m.edge.bindTarget+" <model> use` for use-only"))
		return
	}
	// WHAT YOU CAN DO names the SELECTED node, so it is always clear what the buttons act on -
	// the panel follows the highlight (2026-09-21, "control the highlighted box").
	it, _ := m.edgeSelectedNet()
	glyph, gs := it.icon()
	who := it.name
	switch {
	case it.machine:
		who = it.name + " · this machine"
	case it.self:
		who = it.name + " · this roger"
	case it.dark:
		who = it.name + " · offline agent"
	case it.cand:
		who = it.name + " · discovered, not adopted"
	case it.member:
		who = it.name + " · member machine"
	}
	tail := stDim.Render("  · press the letter, or ↓/Tab then ←→")
	if m.edge.actionFocused {
		tail = stDim.Render("  · ") + lampStyle(roleLive).Render("←→ choose · ⏎ run · ↑ back to map")
	}
	line("  " + stKey.Render("WHAT YOU CAN DO") + stDim.Render("  on  ") + gs.Render(glyph) + " " + stEmber.Render(who) + tail)
	acts := m.edgeActions()
	if len(acts) == 0 {
		line("  " + stDim.Render("nothing to do on this node here - ←→ to select another"))
		return
	}
	// Clamp a stale focus index so the highlight lands on a real button.
	focus := -1
	if m.edge.actionFocused {
		focus = m.edge.actionSel
		if focus >= len(acts) {
			focus = len(acts) - 1
		}
	}
	cardW := 24
	perRow := (cw + 1) / (cardW + 1)
	if perRow < 1 {
		perRow = 1
	}
	for i := 0; i < len(acts); i += perRow {
		end := i + perRow
		if end > len(acts) {
			end = len(acts)
		}
		var cards []string
		for j, a := range acts[i:end] {
			if j > 0 {
				cards = append(cards, " ")
			}
			idx := i + j
			// Only the KEYBOARD-FOCUSED button carries a bright frame, so nothing reads as
			// "selected" unless it really is (2026-09-23: a red "Stop Agent" border was misread as
			// the selection). A .lit action - the active toggle, e.g. Stop Agent - just adds a small
			// on dot, not a competing red border.
			key := lampStyle(roleLive).Render(a.key)
			label := stKey.Render(a.label)
			if a.lit {
				label = stKey.Render(a.label) + lampStyle(roleLive).Render(" ●")
			}
			style := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cRule)
			if idx == focus {
				style = lipgloss.NewStyle().Border(lipgloss.DoubleBorder()).BorderForeground(cLive)
				key = lampStyle(roleLive).Render("▸" + a.key)
			}
			body := key + "  " + label + "\n" + stDim.Render(a.sub)
			cards = append(cards, style.Width(cardW-2).Padding(0, 1).Render(body))
		}
		row := lipgloss.JoinHorizontal(lipgloss.Top, cards...)
		for _, ln := range strings.Split(row, "\n") {
			line("  " + ln)
		}
	}
}

// edgeHouseholdBlock lists the rogers running on THIS machine right now - this instance among
// them, and any AGENT (an instance with the operate capability). It is read live from the local
// instance registry, so an owner who starts a second roger, or an agent, SEES it here at once,
// even on one machine where mDNS cannot cross loopback. This is the "where is my other instance"
// answer, and the seed of adopting instances/agents onto the Edge.
func (m model) edgeHouseholdBlock(cw int, line func(string)) {
	var insts []edge.Instance
	if m.hooks.EdgeHousehold != nil {
		insts = m.hooks.EdgeHousehold()
	}
	var persistent []edge.PersistentAgent
	if m.hooks.EdgePersistentAgents != nil {
		persistent = m.hooks.EdgePersistentAgents()
	}
	if len(insts) == 0 && len(persistent) == 0 {
		return
	}
	line("  " + stKey.Render("RUNNING HERE") + stDim.Render("  the rogers on this machine, this one among them"))
	for _, in := range insts {
		agent := in.Agent // the owner's declaration, not merely a claimed operate capability
		glyph, gstyle, what := "●", lampStyle(roleSignal), "on the Edge"
		if len(in.Bands) > 0 {
			what = "serving " + strings.Join(in.Bands, ", ")
		}
		if agent {
			glyph, gstyle, what = "◆", lampStyle(roleLive), "agent · can operate on other nodes"
			if len(in.Bands) > 0 {
				what += " · " + strings.Join(in.Bands, ", ")
			}
		}
		row := "  " + gstyle.Render(glyph) + " " + stKey.Render(pad(in.Name, 18)) + stDim.Render(what)
		line(truncVisible(row, cw+2))
	}
	// A PERSISTENT agent that is not in the live household has stopped: draw it DARK and kept, and
	// offer to resume or remove it (features/edge/agents.feature). A persistent agent that IS live
	// already showed above, so it is not repeated here.
	if len(persistent) > 0 {
		liveNames := map[string]bool{}
		for _, in := range insts {
			liveNames[in.Name] = true
		}
		for _, p := range persistent {
			if liveNames[p.Name] {
				continue
			}
			row := "  " + stDim.Render("◇ "+pad(p.Name, 18)+"agent · DARK · not running · resume: roger edge agent resume "+p.Name)
			line(truncVisible(row, cw+2))
		}
	}
	line("")
}

// edgeEmptyView explains an empty Edge instead of drawing an empty box, and names the ONE
// action that would add a node to it.
// edgeEmptyHeadline is the "EDGE · <mode badge> · <route mix>" faceplate line, shared by the
// onboarding empty view and the set-up network map.
func edgeEmptyHeadline(st edgeState) string {
	head := "EDGE"
	if b := edgeModeBadge(st); b != "" {
		head += " · " + b
	}
	if mix := edgeRouteMix(st.sessions); mix != "" {
		head += " · " + mix // a fleet of one still carries traffic, and where it went matters
	}
	return head
}

func (m model) edgeEmptyView(w int, line func(string)) {
	line("  " + edgeColorSegments(edgeEmptyHeadline(m.edge)))
	line("")
	if m.hooks.EdgeFleet == nil {
		// AN UNREAD EDGE IS NOT AN EMPTY ONE. The host did not start (the launch log said
		// why), so nothing about the fleet is known - and "the only node" would be the
		// screen's one unforgivable lie.
		line("  " + stEmber.Render("the Edge host did not start this run, so nothing here can be drawn - the log says why."))
		line("  " + stDim.Render("roger edge list reads the last-known Edge; start roger again to bring the host up."))
		return
	}
	line("  " + stKey.Render(m.edgeSelfName()) + stDim.Render(" is the only node on the Edge."))
	line("  " + stDim.Render("Nothing else has been seen, and this screen draws only what it has seen."))
	line("")
	// A set-up machine is drawn by edgeNetworkMap (the hub + its rogers + what's next); this empty
	// view is only for a machine that has set NOTHING up, so it shows the onboarding facts below.
	// THE THREE FACTS. What is true about THIS machine, and the next command for each
	// fact that is not yet what the owner wants (features/edge/empty_edge.feature).
	// Each fact label wears its own lamp hue so the empty board reads as an instrument at a
	// glance, and each still carries its word so NO_COLOR reads the same three facts.
	fact := func(label string, lines ...string) {
		for i, t := range lines {
			for j, part := range wrapCommand(t, max(20, w-2-edgeFactW)) {
				k := ""
				if i == 0 && j == 0 {
					k = label
				}
				line("  " + edgeFactLabelStyle(label).Render(pad(k, edgeFactW)) + part)
			}
		}
	}
	if st := m.edge.status; st != nil {
		if st.Err != "" {
			fact("STATUS", stEmber.Render("could not be read: "+st.Err))
		} else {
			fact("THIS MACHINE", st.MachineLine())
			fact("AUTHORITY", st.AuthorityLines()...)
			fact("DISCOVERY", st.DiscoveryLine(m.edge.at))
		}
		line("")
		fact("ADD A NODE", st.AddNodeLines()...)
		if st.Enrolled || st.AuthorityHere {
			// The easy path the owner just used: run the wizard on the OTHER machine and join.
			line("  " + pad("", edgeFactW) + stDim.Render("or run ") + stKey.Render("roger edge setup") +
				stDim.Render(" on that machine to join this Edge interactively"))
		}
		m.edgeSetupInvite(line)
		return
	}
	fact("ADD A NODE", edge.SelfStatus{}.AddNodeLines()...)
	m.edgeSetupInvite(line)
}

// edgeSetupInvite adds the wizard's one entry line to the honest empty screen - additive,
// never a replacement for the named commands above it (features/edge/onboard.feature). It
// only appears where setup can happen: a build with the hooks wired, on a machine not yet a
// member. If a step was missed on the CLI (this machine roots an Edge but never enrolled
// itself), it offers to FINISH that instead of starting over.
func (m model) edgeSetupInvite(line func(string)) {
	if !m.canSetup() {
		return
	}
	// Read from the frame's own status snapshot (already taken once in refreshEdge), NOT a
	// second live hook - so an idle empty screen does no per-frame file I/O and `e` stays snappy.
	enrolled, authorityHere := false, false
	if st := m.edge.status; st != nil {
		enrolled, authorityHere = st.Enrolled, st.AuthorityHere
	}
	if enrolled {
		return
	}
	line("")
	if authorityHere {
		line("  " + lampStyle(roleLive).Render("press e") +
			stDim.Render(" to finish setting up by enrolling this machine onto its Edge"))
		return
	}
	line("  " + lampStyle(roleLive).Render("press e") +
		stDim.Render(" to set up this machine: start a new Edge here, or join one you already run"))
}

// edgeFactW is the label column of the empty screen's fact block.
const edgeFactW = 14

// edgeGraphBody draws self at the centre and every node in relation to it.
func (m model) edgeGraphBody(g edgeGeom, line func(string)) {
	label := "◉ " + m.edgeSelfName() // self is the lit dial ◉; hosts are ▣, so you are never a host
	if g.layout == edgeLayoutGraph {
		inner := g.nameW + 14
		if inner > g.w-6 {
			inner = g.w - 6
		}
		if inner < 20 {
			inner = 20
		}
		// The desk is YOU, so it wears the one red accent on its frame (patchbay: the desk on
		// the left). The label stays ink; the box carries the glow.
		desk := lampStyle(roleLive)
		line("  " + desk.Render("╭"+strings.Repeat("─", inner)+"╮"))
		line("  " + desk.Render("│") + " " + stKey.Render(pad(label, inner-6)+"SELF") + " " + desk.Render("│"))
		line("  " + desk.Render("╰──┬"+strings.Repeat("─", inner-3)+"╯"))
	} else {
		// Compact: no box. The same information, one line, still marked as self.
		line("  " + pad(label, g.nameW) + " " + stDim.Render("SELF"))
	}
	line(strings.Repeat(" ", g.spine) + "│")
	m.edgeHouseholdBlock(edgeSetupContentWidth(g.w), line)
	for i := range m.edge.rows {
		line(m.edgeNodeLine(g, i))
		if s := m.edgeAgentSubLine(g, i); s != "" {
			line(s)
		}
	}
	if g.layout == edgeLayoutGraph {
		line("")
		line("  " + stDim.Render("NODES ") + stKey.Render("◉") + stDim.Render(" you · ") + stKey.Render("▣") + stDim.Render(" host · ") + lampStyle(roleSignal).Render("▤") + stDim.Render(" board · ") + lampStyle(roleDial).Render("▯") + stDim.Render(" phone · ") + lampStyle(roleLive).Render("◆") + stDim.Render(" agent"))
		line("  " + stDim.Render("CAPS  "+edgeCapLegend()))
		line("  " + stDim.Render("      UPPER = VERIFIED · lower = CLAIMED (nothing routes on a claim)"))
	}
}

func edgeCapLegend() string {
	var out []string
	for _, c := range edge.Capabilities() {
		out = append(out, edgeCapMark[c]+"="+string(c))
	}
	return strings.Join(out, " ")
}

// edgeNodeLine draws one node: its edge back to self (or to its relay), its name, its
// capability marks, and what the fleet last saw of it.
func (m model) edgeNodeLine(g edgeGeom, i int) string {
	r := m.edge.rows[i]
	// The wire is built cell by cell as coloured spans (patchbay §2: each texture a colour AND a
	// pattern), so it can be tinted by how it is reached and carry a red packet, while every cell
	// stays exactly one column wide - the stripped line is byte-identical to the mono drawing.
	n := g.spine + g.runW + 1
	cells := make([]string, n)
	for k := range cells {
		cells[k] = " "
	}
	start, runW := g.spine, g.runW
	if r.child {
		start, runW = g.spine+g.runW-g.childRunW, g.childRunW
	}
	tex := edgeTexture(r.n)
	wire := edgeWireStyle(tex)
	cells[start] = wire.Render(string(m.edgeConnector(i)))
	for c := start + 1; c <= g.spine+g.runW; c++ {
		ch := tex
		// A broken edge is drawn GAPPY, so it does not merely read as a paler line.
		if tex == edgeGlyphBroken && (g.spine+g.runW-c)%2 == 1 {
			ch = ' '
		}
		cells[c] = wire.Render(string(ch))
	}
	if at := m.edgePulseAt(g, i); at >= 0 && at < runW {
		cells[g.spine+g.runW-at] = lampStyle(roleLive).Render(string(edgeGlyphPulse)) // a hot packet
	}
	// The selection cursor sits ON the node it selects (right before the emblem), not adrift in
	// the far margin, and an emblem says WHAT the node is - a host, a board, a phone.
	sel := " "
	if r.n.ID == m.edge.sel {
		sel = stRed.Render(string(edgeGlyphSel))
	}
	emblem, emStyle := edgeKindEmblem(r.n.Kind)
	nameStyle := stKey
	if edgeIsDark(r.n) {
		nameStyle, emStyle = stDim, stDim
	}
	label := emStyle.Render(emblem) + " " + nameStyle.Render(pad(r.n.Name, max(1, g.nameW-2)))
	out := strings.Join(cells, "") + sel + label +
		" " + stDim.Render(pad(edgeMarks(r.n), g.marksW))
	if g.detailW > 0 {
		out += " " + stDim.Render(pad(m.edgeDetailCell(r), g.detailW))
	}
	return out
}

// edgeAgentSubLine draws an AGENT as a sub-node hanging off the node that runs it - the visible
// answer to "make this an agent". An agent is a node with the `operate` capability (it can act on
// other nodes); it is drawn only when the node really carries that capability, never invented.
func (m model) edgeAgentSubLine(g edgeGeom, i int) string {
	r := m.edge.rows[i]
	if edgeIsDark(r.n) {
		return ""
	}
	// A node runs an agent when one of its instances is declared an agent (features/edge/
	// agents.feature). That is the honest signal - not a merely-claimed operate capability.
	name := ""
	for _, in := range r.n.Instances {
		if in.Agent {
			name = in.Name
			break
		}
	}
	if name == "" {
		return ""
	}
	lead := strings.Repeat(" ", g.spine+g.runW-6)
	return lead + lampStyle(roleLive).Render("└◆") + " " +
		stKey.Render(name) + stDim.Render(" · agent · operates on this Edge")
}

// edgeKindEmblem is the little device glyph on a node's strip - the honest answer to "what is
// this?", driven purely by the node's kind (host / board / mobile), so a Pi looks like a board
// and a Mac like a host without inventing detail the data does not carry.
func edgeKindEmblem(kind string) (string, lipgloss.Style) {
	switch kind {
	case string(edge.Board):
		return "▤", lampStyle(roleSignal) // a board (Raspberry Pi / ESP32)
	case string(edge.Mobile):
		return "▯", lampStyle(roleDial) // a phone / tablet
	default:
		return "▣", stKey // a host (Mac / PC / server)
	}
}

// edgeWireStyle tints a wire by how the node is reached: LAN-direct is the live signal hue,
// a relayed hop the dial hue, a dark node the faint ink - each still its own glyph pattern so
// NO_COLOR reads the same wire (patchbay §2).
func edgeWireStyle(tex rune) lipgloss.Style {
	switch tex {
	case edgeGlyphSolid:
		return lampStyle(roleSignal)
	case edgeGlyphDim:
		return lampStyle(roleDial)
	default:
		return stDim
	}
}

// edgeConnector is └ for the last node at its own level, ├ otherwise - so the drawing
// closes off instead of trailing into nothing.
func (m model) edgeConnector(i int) rune {
	r := m.edge.rows[i]
	for j := i + 1; j < len(m.edge.rows); j++ {
		n := m.edge.rows[j]
		if r.child {
			if n.child && n.via == r.via {
				return '├'
			}
			if !n.child {
				break
			}
			continue
		}
		if !n.child {
			return '├'
		}
	}
	return '└'
}

// edgeDetailCell is the right-hand column: the transport actually in use, and how long ago
// the fleet last heard anything.
func (m model) edgeDetailCell(r edgeRow) string {
	age := edgeAge(m.edge.at.Sub(time.Unix(r.n.LastSeen, 0)))
	switch {
	case edgeIsDark(r.n):
		return "DARK · last seen " + age
	case r.child:
		return "via relay · " + age
	case r.relay:
		return "RELAY · relay · " + age
	case edgeHasLAN(r.n):
		if a := edge.LANAddr(r.n); a != "" {
			return "lan " + a + " · live · " + age
		}
		return "lan · live · " + age
	default:
		return "relay · " + age
	}
}

// edgeListBody is the degraded drawing: no picture, but every fact, and an honest count of
// what it is not showing.
func (m model) edgeListBody(g edgeGeom, line func(string)) {
	line("  " + edgeGlyphSelf + " " + pad(m.edgeSelfName(), g.nameW) + " " + stDim.Render("SELF"))
	shown := min(len(m.edge.rows), edgeListRows)
	// The window FOLLOWS the selection: a cursor the operator cannot see is a cursor
	// they will act on blind.
	top := 0
	if at := m.edgeRowOf(m.edge.sel); at >= shown {
		top = at - shown + 1
	}
	line("  " + stDim.Render("showing "+strconv.Itoa(shown)+" of "+strconv.Itoa(len(m.edge.rows))+" nodes"))
	line("")
	for i := top; i < top+shown; i++ {
		r := m.edge.rows[i]
		sel := "  "
		if r.n.ID == m.edge.sel {
			sel = string(edgeGlyphSel) + " "
		}
		out := sel + pad(r.n.Name, g.nameW) + " " + pad(edgeMarks(r.n), g.marksW)
		if g.detailW > 0 {
			out += " " + pad(m.edgeDetailCell(r), g.detailW)
		}
		if edgeIsDark(r.n) {
			out = stDim.Render(out)
		}
		line(out)
	}
}

// edgeCandidateBlock draws what is NOT on the Edge: peers seen on this LAN with no link to
// self, because nothing is connected until the owner says so.
func (m model) edgeCandidateBlock(g edgeGeom, line func(string)) {
	line("")
	line("  " + stKey.Render("CANDIDATES") + stDim.Render(" · seen on this LAN, not on your Edge"))
	for _, c := range m.edge.cands {
		sel := "  "
		if c.ID == m.edge.sel {
			sel = string(edgeGlyphSel) + " "
		}
		where := edge.LANAddr(c)
		line(sel + "  " + pad(c.Name, g.nameW) + " " + pad(where, 18) + " " + stDim.Render("a adopts it"))
	}
}

// ---- the detail ----------------------------------------------------------

// edgeDetailView describes ONE node without leaving the screen. It shows what the owner
// needs to act on and nothing that would be a secret to show.
func (m model) edgeDetailView(w int, line func(string)) {
	n, isCand, ok := m.edgeSelected()
	if !ok {
		line("  " + stDim.Render("nothing selected"))
		return
	}
	label := func(k, v string) { line("  " + stDim.Render(pad(k, 14)) + v) }
	head := "NODE · " + n.Name
	if isCand {
		head = "CANDIDATE · " + n.Name
	}
	line("  " + stKey.Render(head))
	line("")
	label("id", n.ID)
	label("kind", n.Kind)
	if isCand {
		label("status", "not on your Edge - a adopts it")
	}
	caps := n.Caps
	if len(caps) == 0 {
		label("capabilities", stDim.Render("none declared"))
	}
	for i, c := range caps {
		k := ""
		if i == 0 {
			k = "capabilities"
		}
		row := pad(c.Name, 10) + pad(c.State, 22)
		if c.State != string(edge.Verified) {
			row += stDim.Render("verified by " + edge.Capability(c.Name).VerificationMethod())
		}
		label(k, row)
	}
	// Transports in PREFERENCE order - the order the fleet keeps them in, which is the
	// order a dial would try them.
	for i, t := range n.Transports {
		k := ""
		if i == 0 {
			k = "transports"
		}
		row := pad(t.Kind, 7) + t.Addr
		if i == 0 {
			row = pad(row, 40) + stDim.Render("preferred")
		}
		label(k, row)
	}
	// THE CONTRACT IS PART OF THE DEVICE. A classifying node's framing is not a setting
	// on it - model and prompt ship as one unit - so it is shown here in FULL, wrapped
	// at whitespace rather than elided: a framing you can only see half of is a framing
	// you cannot check, and this is the text that travels with every escalation.
	if c := n.Contract; c.Class != "" || c.Framing != "" {
		label("contract", c.Class)
		if len(c.Labels) > 0 {
			label("", stDim.Render("labels  ")+strings.Join(c.Labels, " "))
		}
		for i, ln := range wrapCommand(c.Framing, max(20, w-18)) {
			k := ""
			if i == 0 {
				k = "framing"
			}
			label(k, ln)
		}
	}
	if via := edgeVia(n); via != "" {
		label("reached via", via)
	}
	if n.LastSeen > 0 {
		label("last seen", edgeAge(m.edge.at.Sub(time.Unix(n.LastSeen, 0)))+" ago")
	}
	if n.Pin != "" {
		label("fingerprint", n.Pin)
		line("  " + stDim.Render(pad("", 14)+"pinned - a LAN dial is only safe pinned"))
	}
	line("")
	line("  " + stDim.Render("esc closes · ↑↓ moves the selection"))
}
