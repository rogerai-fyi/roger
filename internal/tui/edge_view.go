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

	if st.err != "" {
		line("  " + stEmber.Render("the Edge could not be read: "+st.err))
	}
	if st.detail {
		m.edgeDetailView(w, line)
		return b.String()
	}
	if len(st.rows) == 0 && len(st.cands) == 0 {
		if st.err != "" {
			// An Edge that could not be READ is not an empty Edge. Saying "this machine
			// is the only node" here would be the screen's one unforgivable lie.
			line("  " + stDim.Render("nothing can be drawn until the fleet can be read · r retries"))
			return b.String()
		}
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
	line("  " + stDim.Render(edgeHeadline(st)))
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

// edgeHeadline counts what is on the Edge, including what is not answering.
func edgeHeadline(st edgeState) string {
	dark := 0
	for _, r := range st.rows {
		if edgeIsDark(r.n) {
			dark++
		}
	}
	out := "EDGE · " + plural(len(st.rows), "node")
	if dark > 0 {
		out += " · " + strconv.Itoa(dark) + " dark"
	}
	if n := len(st.cands); n > 0 {
		out += " · " + plural(n, "candidate")
	}
	return out
}

func edgeHints(w int) string {
	if w < 60 {
		return "↑↓ · ⏎ detail · a adopt · esc"
	}
	return "↑↓ select  ·  ⏎ detail  ·  a adopt a candidate  ·  r re-read  ·  esc back"
}

// edgeEmptyView explains an empty Edge instead of drawing an empty box, and names the ONE
// action that would add a node to it.
func (m model) edgeEmptyView(w int, line func(string)) {
	line("  " + stDim.Render("EDGE"))
	line("")
	line("  " + stKey.Render(m.edgeSelfName()) + stDim.Render(" is the only node on the Edge."))
	line("  " + stDim.Render("Nothing else has been seen, and this screen draws only what it has seen."))
	line("")
	line("  " + stKey.Render("ADD A NODE") + stDim.Render("  run RogerAI on another machine on this network."))
	line("  " + stDim.Render("              It appears here as a CANDIDATE; a adopts it onto your Edge."))
}

// edgeGraphBody draws self at the centre and every node in relation to it.
func (m model) edgeGraphBody(g edgeGeom, line func(string)) {
	label := edgeGlyphSelf + " " + m.edgeSelfName()
	if g.layout == edgeLayoutGraph {
		inner := g.nameW + 14
		if inner > g.w-6 {
			inner = g.w - 6
		}
		if inner < 20 {
			inner = 20
		}
		line("  ╭" + strings.Repeat("─", inner) + "╮")
		line("  │ " + pad(label, inner-6) + "SELF │")
		line("  ╰──┬" + strings.Repeat("─", inner-3) + "╯")
	} else {
		// Compact: no box. The same information, one line, still marked as self.
		line("  " + pad(label, g.nameW) + " " + stDim.Render("SELF"))
	}
	line(strings.Repeat(" ", g.spine) + "│")
	for i := range m.edge.rows {
		line(m.edgeNodeLine(g, i))
	}
	if g.layout == edgeLayoutGraph {
		line("")
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
	field := []rune(strings.Repeat(" ", g.spine+g.runW+1))
	if r.n.ID == m.edge.sel {
		field[0] = edgeGlyphSel
	}
	start, runW := g.spine, g.runW
	if r.child {
		start, runW = g.spine+g.runW-g.childRunW, g.childRunW
	}
	field[start] = m.edgeConnector(i)
	tex := edgeTexture(r.n)
	for c := start + 1; c <= g.spine+g.runW; c++ {
		ch := tex
		// A broken edge is drawn GAPPY, so it does not merely read as a paler line.
		if tex == edgeGlyphBroken && (g.spine+g.runW-c)%2 == 1 {
			ch = ' '
		}
		field[c] = ch
	}
	if at := m.edgePulseAt(g, i); at >= 0 && at < runW {
		field[g.spine+g.runW-at] = edgeGlyphPulse
	}
	out := string(field) + " " + pad(r.n.Name, g.nameW) + " " + pad(edgeMarks(r.n), g.marksW)
	if g.detailW > 0 {
		out += " " + pad(m.edgeDetailCell(r), g.detailW)
	}
	if edgeIsDark(r.n) {
		return stDim.Render(out)
	}
	return out
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
			return "lan " + a + " · " + age
		}
		return "lan · " + age
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
