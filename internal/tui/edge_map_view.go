package tui

// THE EDGE NETWORK MAP ([3], set-up machine) - a UniFi-style topology in ASCII: the authority
// machine as a card, the rogers and agents on it as cards hung under it on a branch, the SELECTED
// card glowing, moved with the arrow keys. It is the "where is my network, and what is on it"
// picture the founder asked for (2026-09-21: "imagine how UniFi looks ... but in ASCII"), and the
// place the owner acts on a node in place.

import (
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"rogerai.fm/roger/v6/internal/edge"
	"rogerai.fm/roger/v6/internal/store"
)

// edgeNetItem is one selectable node on the map: the machine, or a roger/agent running on it.
type edgeNetItem struct {
	machine bool             // the self machine (the hub)
	name    string           // the machine or instance name
	role    string           // the machine's role line (machine only)
	agent   bool             // this instance is an agent
	dark    bool             // a persistent agent whose process has stopped
	self    bool             // this instance is the roger you are looking at
	bands   []string         // models this instance serves
	inst    *edge.Instance   // the live instance record, when this node is a running roger
	status  *edge.SelfStatus // this machine's status, when this node is the machine
	job     string           // a one-word job label (serve/watch/relay) when assigned, for the card
	member  bool             // another machine already on this Edge (a member node)
	cand    bool             // a discovered machine not yet on this Edge (adopt with a)
	node    *store.EdgeNode  // the fleet/candidate node record, for member and candidate items
	id      string           // the node id, for adopt/detail of a member or candidate
	kind    string           // host | board | mobile, for a member/candidate emblem
	alerts  []string         // the alert rules firing for this node right now, by wording
	// via is the MEMBER this node is reached THROUGH, and hops is how far from the authority it is:
	// 1 for a machine this one reaches itself, 2 through one relay, 3 through a relay's relay
	// (edge.Arrange). The wire says a relay is in the path; these say which one and how many.
	via  string
	hops int
}

// alertTag is the short count a card wears when rules are firing: "1 alert", "2 alerts".
func (it edgeNetItem) alertTag() string {
	switch n := len(it.alerts); {
	case n == 0:
		return ""
	case n == 1:
		return "1 alert"
	default:
		return strconv.Itoa(n) + " alerts"
	}
}

// icon returns the node's glyph and its style.
func (it edgeNetItem) icon() (string, lipgloss.Style) {
	switch {
	case it.machine:
		return "◉", lampStyle(roleLive)
	case it.cand:
		g, _ := edgeKindEmblem(it.kind)
		return g, stDim // a candidate is drawn quiet until adopted
	case it.member && it.dark:
		g, _ := edgeKindEmblem(it.kind)
		return g, stDim
	case it.member:
		return edgeKindEmblem(it.kind)
	case it.dark:
		return "◇", stDim
	case it.agent:
		return "◆", lampStyle(roleLive)
	default:
		return "●", lampStyle(roleSignal)
	}
}

// hardware is the short form of the model this unit reported, or "" when it has not said.
func (it edgeNetItem) hardware() string {
	if it.node == nil {
		return ""
	}
	return edge.MachineShort(edge.MachineOf(*it.node))
}

// kindWord is what this unit IS, for the card: PC, PHONE, BOARD - or ROGER for a process. An owner
// must be able to tell a phone from a machine without decoding a glyph.
func (it edgeNetItem) kindWord() string { return it.kindOr("DEVICE") }

// kindOr is the kind word, or def when the record carries no kind at all. A card with no kind must
// still read as a sentence, so each caller says what it would rather print than a blank.
func (it edgeNetItem) kindOr(def string) string {
	if w := edgeKindWord(it.kind); w != "" {
		return w
	}
	return def
}

// sub returns the one-line description under a node's name, with the alert count when rules fire.
// HARDWARE is named in capitals (PC, PHONE, BOARD) and SOFTWARE in lower case (agent), so a glance
// at the band tells an owner which of the two they are looking at - the thing this screen was worst
// at before.
func (it edgeNetItem) sub() string {
	s := it.subBase()
	tag := it.alertTag()
	switch {
	case tag == "" || !(it.machine || it.member):
	case it.member && it.dark:
		// OFFLINE LEADS, and the kind gives up its place. Both facts want this one narrow line, and
		// when a unit is both offline and alerting those two words are the urgent pair: the kind is
		// still told by the card's glyph and spelled out in the readout, but "offline" is told
		// nowhere else on the card. An earlier version dropped it and that was a step backwards.
		s = "offline · " + tag
	default:
		s += " · " + tag
	}
	return s
}

func (it edgeNetItem) subBase() string {
	switch {
	case it.machine:
		// "YOU ARE HERE" and not "THIS MACHINE": the readout below uses THIS MACHINE as a fact label,
		// and two lines with the same words is exactly how this screen got confusing.
		return "YOU ARE HERE · " + it.role
	case it.cand:
		return it.kindWord() + " · adopt with a"
	case it.member && it.dark:
		return it.kindWord() + " · offline"
	case it.member && it.via != "":
		// WHICH relay, not just that there is one. The wire already says the path goes through
		// something; a picture of how the Edge is connected has to name it.
		return it.kindWord() + " · via " + it.via
	case it.member && it.hardware() != "":
		// WHAT IT IS, when the machine has said: "MACHINE · Raspberry Pi 4" tells an owner more than
		// "MACHINE · on your Edge" ever could, and it is the node's own report, never a guess.
		return it.kindWord() + " · " + it.hardware()
	case it.member:
		return it.kindWord() + " · on your Edge"
	case it.dark && it.agent:
		return "agent · offline"
	case it.dark:
		return "roger · offline"
	case it.agent && it.job != "":
		return "agent · " + it.job
	case it.agent && len(it.bands) > 0:
		// AN AGENT THAT SERVES SAYS WHAT IT SERVES. It read "agent · idle" while it was on air with a
		// model, because the agent case answered before the bands case ever ran
		// (features/edge/agents.feature: an agent that is serving a model shows the band it serves).
		return "agent · " + strings.Join(it.bands, " ")
	case it.agent:
		return "agent · idle"
	case len(it.bands) > 0:
		return strings.Join(it.bands, " ")
	default:
		return "on the Edge"
	}
}

// edgeNetItems is the map's ordered nodes: the machine first (index 0), then the rogers running
// here, then any stopped persistent agent - the same set the owner navigates and acts on.
func (m model) edgeNetItems() []edgeNetItem {
	role := "you"
	if st := m.edge.status; st != nil {
		mode := "CORE"
		if st.Root == edge.RootLocal {
			mode = "LOCAL"
		}
		switch {
		case st.AuthorityHere:
			role = "authority · " + mode
		case st.Enrolled:
			role = "member · " + mode
		}
	}
	items := []edgeNetItem{{machine: true, name: m.edgeSelfName(), role: role, status: m.edge.status}}
	if m.edge.status != nil {
		items[0].alerts = m.edge.alerts[m.edge.status.NodeID]
	}
	self := ""
	if m.hooks.EdgeSelfInstance != nil {
		self = m.hooks.EdgeSelfInstance()
	}
	live := map[string]bool{}
	if m.hooks.EdgeHousehold != nil {
		for _, in := range m.hooks.EdgeHousehold() {
			in := in // capture for the pointer
			live[in.Name] = true
			it := edgeNetItem{name: in.Name, agent: in.Agent, bands: in.Bands, self: in.Name == self && self != "", inst: &in}
			if j, ok := m.edgeJobOf(in.Name); ok {
				it.job = edgeJobLabel(j)
			}
			items = append(items, it)
		}
	}
	if m.hooks.EdgePersistentAgents != nil {
		for _, p := range m.hooks.EdgePersistentAgents() {
			if live[p.Name] {
				continue
			}
			items = append(items, edgeNetItem{name: p.Name, agent: true, dark: true})
		}
	}
	// The OTHER machines already on this Edge - members, in the order the topology arranges them, so
	// a machine reached through a relay is drawn after the relay it hangs off rather than wherever
	// the fleet listing happened to put it (edge.Arrange).
	fleet := make([]store.EdgeNode, 0, len(m.edge.rows))
	byID := map[string]*store.EdgeNode{}
	for i := range m.edge.rows {
		fleet = append(fleet, m.edge.rows[i].n)
		byID[m.edge.rows[i].n.ID] = &m.edge.rows[i].n
	}
	selfID := ""
	if m.edge.status != nil {
		selfID = m.edge.status.NodeID
	}
	for _, r := range edge.Arrange(fleet) {
		n := r.Node
		if n.ID == selfID && selfID != "" {
			continue // the machine you are at is the hub, not also one of the others
		}
		items = append(items, edgeNetItem{name: edge.Label(n.Name, n.ID), member: true, node: byID[n.ID], id: n.ID, kind: n.Kind,
			dark: edge.Presence(n.Presence) == edge.PresenceDark, alerts: m.edge.alerts[n.ID],
			via: r.Via, hops: r.Depth})
	}
	// The DISCOVERED machines not yet on this Edge - candidates, adopt with a.
	for i := range m.edge.cands {
		n := m.edge.cands[i]
		items = append(items, edgeNetItem{name: edge.Label(n.Name, n.ID), cand: true, node: &m.edge.cands[i], id: n.ID, kind: n.Kind})
	}
	return items
}

// edgeMapActive reports whether the navigable network map (not the multi-node graph) is on screen.
// The map is the home view of a SET-UP machine: it draws this machine's hub, the rogers on it, the
// other member machines on the Edge, and the discovered candidates waiting to be adopted - so a
// phone that appears on the LAN shows up here to adopt (2026-09-21). Past edgeMaxGraphNodes the
// map would stop being a picture, so a large fleet falls back to the graph/list until the manual
// grid/list switch lands (features/edge/map_scale.feature).
func (m model) edgeMapActive() bool {
	st := m.edge.status
	if st == nil || st.Err != "" || !st.Enrolled {
		return false
	}
	if m.edge.err != "" && len(m.edge.rows) == 0 && len(m.edge.cands) == 0 {
		return false // the fleet could not be read and there is nothing to draw
	}
	return len(m.edge.rows)+len(m.edge.cands) <= edgeMaxGraphNodes
}

// edgeMoveNetSel moves the map highlight by delta, clamped to the nodes. The first move starts
// from the default highlight (this roger), so ←→ picks up where the eye already is.
func (m *model) edgeMoveNetSel(delta int) {
	items := m.edgeNetItems()
	n := len(items)
	if n == 0 {
		return
	}
	if !m.edge.netSelSet {
		m.edge.netSel = edgeDefaultNetSel(items)
		m.edge.netSelSet = true
	}
	m.edge.netSel += delta
	if m.edge.netSel < 0 {
		m.edge.netSel = 0
	}
	if m.edge.netSel >= n {
		m.edge.netSel = n - 1
	}
}

// edgeDefaultNetSel is the highlight before the owner moves: THIS roger if one is running here,
// else the machine. It puts the commonest actions (Make Agent, Rename) under the cursor at once.
func edgeDefaultNetSel(items []edgeNetItem) int {
	// A DEVICE WAITING TO BE ADOPTED wins: it is why the owner came to this screen, and landing on it
	// puts "a adopt" one keystroke away instead of behind several arrow presses. Nothing is waiting
	// most of the time, so this changes nothing on an ordinary day.
	for i, it := range items {
		if it.cand {
			return i
		}
	}
	// Otherwise THIS roger, whose actions are then to hand without moving
	// (features/edge/agents.feature).
	for i, it := range items {
		if it.self {
			return i
		}
	}
	return 0
}

const edgeNetCardW = 30 // card width (border included); fits a kind and its state ("MACHINE · on your Edge")

// edgeMapTopology draws the topology: the machine card, a branch, and the instance/agent cards in
// centred rows, the selected card glowing red. It returns nothing; it draws through line().
func (m model) edgeMapTopology(cw int, line func(string)) {
	items := m.edgeNetItems()
	_, sel := m.edgeSelectedNet()

	// The machine card, centred.
	machine := edgeNetCardBig(items[0], sel == 0, min(cw-4, 52))
	mW := lipgloss.Width(machine)
	mLead := (cw - mW) / 2
	if mLead < 0 {
		mLead = 0
	}
	for _, ln := range strings.Split(machine, "\n") {
		line("  " + strings.Repeat(" ", mLead) + ln)
	}
	trunk := mLead + mW/2

	// Partition the rest into the rogers ON this machine (hung under the hub), the other member
	// machines (the fleet band), and the discovered candidates (the adopt band). Each card keeps
	// its GLOBAL index so the highlight lands on the right one wherever it is drawn.
	var hubIt, memIt, candIt []edgeNetItem
	var hubIx, memIx, candIx []int
	for g := 1; g < len(items); g++ {
		it := items[g]
		switch {
		case it.cand:
			candIt, candIx = append(candIt, it), append(candIx, g)
		case it.member:
			memIt, memIx = append(memIt, it), append(memIx, g)
		default:
			hubIt, hubIx = append(hubIt, it), append(hubIx, g)
		}
	}

	// The rogers running on this machine, hung under the hub with a branch.
	line("  " + strings.Repeat(" ", trunk) + lampStyle(roleDial).Render("│"))
	if len(hubIt) == 0 {
		line("  " + edgeSetupCenter(stDim.Render("┈┈ no rogers running here yet ┈┈"), cw))
	} else {
		_ = m.edgeCardBand(cw, trunk, hubIt, hubIx, sel, line)
	}

	// The other machines already on this Edge - the fleet band, WIRED to the hub. The founder's
	// ruling for this screen was "a graphical animated ascii graph representing how your Edge is
	// connected", and a band of cards floating under a heading was not that: it showed what is on
	// the Edge and said nothing about how any of it is reached. Each card now hangs on its own
	// wire, and the wire is the link this machine has actually seen.
	if len(memIt) > 0 {
		rail := lampStyle(roleDial)
		line("  " + strings.Repeat(" ", trunk) + rail.Render("│"))
		line("  " + edgeSetupCenter(stKey.Render("ON THE EDGE")+stDim.Render("  other machines you have adopted"), cw))
		line("  " + strings.Repeat(" ", trunk) + rail.Render("│"))
		// THE HOPS ARE DRAWN AS HOPS. The machines this one reaches ITSELF hang off the hub; a machine
		// reached THROUGH one of them hangs off that machine, on its own bus, one level down. Before
		// this every member hung off the hub whatever its real path was, which is the flat drawing the
		// topology could not say anything better than while Row carried only a boolean.
		m.edgeMemberTree(cw, trunk, memIt, memIx, sel, "", line)
		line("  " + edgeSetupCenter(edgeWireLegend(memIt), cw))
	}

	// The discovered machines not yet adopted - the adopt band. No wire: they are not on the Edge
	// until the owner adopts one. This is the UniFi moment - a phone on the LAN shows up here.
	if len(candIt) > 0 {
		line("")
		line("  " + edgeSetupCenter(stKey.Render("DISCOVERED")+stDim.Render("  on your network, not yet on your Edge · press a to adopt"), cw))
		_ = m.edgeCardBand(cw, -1, candIt, candIx, sel, line)
	}
}

// edgeMemberTree draws the members reached through `parent` ("" = the authority itself) as one band
// tied to the column `trunk`, then recurses: whatever is reached through each of THOSE machines is
// drawn under it, on a bus centred on that machine's own card. A relay chain three deep therefore
// draws three levels deep, which is the whole point of the topology knowing its depth.
//
// It is bounded by the fact that edge.Arrange draws every member exactly once and never places a
// node under itself, so the recursion is over a forest and cannot loop.
func (m model) edgeMemberTree(cw, trunk int, its []edgeNetItem, idx []int, sel int, parent string, line func(string)) {
	var here []edgeNetItem
	var hereIx []int
	for j, it := range its {
		if it.via == parent {
			here, hereIx = append(here, it), append(hereIx, idx[j])
		}
	}
	if len(here) == 0 {
		return
	}
	centres := m.edgeCardBand(cw, trunk, here, hereIx, sel, line)
	for j, it := range here {
		// Only a machine something is reached THROUGH opens a level of its own, and it is centred on
		// that machine's card so the wire says which machine the hop goes through.
		if it.node == nil {
			continue
		}
		if c, ok := centres[hereIx[j]]; ok {
			m.edgeMemberTree(cw, c, its, idx, sel, it.node.Name, line)
		}
	}
}

// edgeCardBand draws a set of node cards packed into centred rows. When trunk >= 0 each row is
// tied to the hub with a branch connector; when trunk < 0 the cards stand on their own (a fleet or
// discovered band, already separated by a heading). idx[j] is card j's global selection index.
// It returns each card's centre column by GLOBAL index, so a caller can hang another band off one
// of them.
func (m model) edgeCardBand(cw, trunk int, its []edgeNetItem, idx []int, sel int, line func(string)) map[int]int {
	centres := map[int]int{}
	perRow := max(1, (cw+2)/(edgeNetCardW+2))
	for i := 0; i < len(its); i += perRow {
		end := min(i+perRow, len(its))
		row := its[i:end]
		groupW := len(row)*edgeNetCardW + (len(row)-1)*2
		// A band hangs UNDER what it hangs off: centred on its trunk column, not on the screen. Centring
		// every level on the screen made a child sit beside its parent instead of below it, and the bus
		// then ran sideways across the picture saying nothing. Clamped into the screen, so a relay near
		// an edge still draws its children whole.
		start := (cw - groupW) / 2
		if trunk >= 0 {
			start = trunk - groupW/2
		}
		if start+groupW > cw {
			start = cw - groupW
		}
		if start < 0 {
			start = 0
		}
		centers := make([]int, len(row))
		for j := range row {
			centers[j] = start + j*(edgeNetCardW+2) + edgeNetCardW/2
			centres[idx[i+j]] = centers[j]
		}
		if trunk >= 0 {
			line("  " + edgeBranchLine(cw, trunk, centers))
			line("  " + m.edgeDropLine(cw, centers, row))
		}
		var cards []string
		for j, it := range row {
			if j > 0 {
				cards = append(cards, "  ")
			}
			cards = append(cards, edgeNetCard(it, sel == idx[i+j]))
		}
		block := lipgloss.JoinHorizontal(lipgloss.Top, cards...)
		for _, ln := range strings.Split(block, "\n") {
			line("  " + strings.Repeat(" ", start) + ln)
		}
	}
	return centres
}

// edgeDropLine is the short wire from the bus down to each card, ONE GLYPH PER NODE, and it is
// the only part of the picture that says how that node is reached. The screen never draws a link
// it has not seen: a solid drop is a LAN transport this machine verified, a dotted one is a path
// through a relay, a dashed one is a node whose heartbeat aged out. A heartbeat that arrives
// lights its own drop, so a live fleet moves and a still one is honestly still.
func (m model) edgeDropLine(cw int, centers []int, row []edgeNetItem) string {
	cells := make([]string, cw)
	for i := range cells {
		cells[i] = " "
	}
	for j, c := range centers {
		if c < 0 || c >= cw {
			continue
		}
		glyph, st := edgeDropWire(row[j])
		if m.edgePulsing(row[j]) {
			glyph, st = string(edgeGlyphPulse), lampStyle(roleLive)
		}
		cells[c] = st.Render(glyph)
	}
	return strings.TrimRight(strings.Join(cells, ""), " ")
}

// edgeDropWire is one node's wire: its glyph and its tint. The GLYPH carries the meaning, so the
// three links stay apart under NO_COLOR and in a pipe; the tint only repeats what the glyph says.
func edgeDropWire(it edgeNetItem) (string, lipgloss.Style) {
	if it.node == nil {
		// A roger on THIS machine. There is no network between us, so there is nothing to doubt.
		return "│", lampStyle(roleSignal)
	}
	switch edgeTexture(*it.node) {
	case edgeGlyphSolid:
		return "│", lampStyle(roleSignal)
	case edgeGlyphDim:
		return "┊", lampStyle(roleDial)
	default:
		return "╎", stDim
	}
}

// edgePulsing reports whether a heartbeat is travelling on this node's wire right now.
func (m model) edgePulsing(it edgeNetItem) bool {
	if it.id == "" {
		return false
	}
	for _, p := range m.edge.pulses {
		if p.id == it.id {
			return true
		}
	}
	return false
}

// edgeWireLegend names only the wires actually on screen. A legend for a link nobody has is
// clutter, and a fleet that is all LAN-direct should not be taught three glyphs to read one.
func edgeWireLegend(its []edgeNetItem) string {
	var solid, relay, dark bool
	for _, it := range its {
		if it.node == nil {
			continue
		}
		switch edgeTexture(*it.node) {
		case edgeGlyphSolid:
			solid = true
		case edgeGlyphDim:
			relay = true
		default:
			dark = true
		}
	}
	if !relay && !dark {
		return "" // every wire on screen means the same thing: nothing to explain
	}
	var out []string
	if solid {
		out = append(out, lampStyle(roleSignal).Render("│")+stDim.Render(" direct"))
	}
	if relay {
		out = append(out, lampStyle(roleDial).Render("┊")+stDim.Render(" through a relay"))
	}
	if dark {
		out = append(out, stDim.Render("╎ not heard from"))
	}
	return strings.Join(out, stDim.Render("  ·  "))
}

// edgeBranchLine draws one connector row: a horizontal bus tying the trunk column to each card
// centre with a ┬ drop over each card. Every glyph is one cell so widths stay exact.
func edgeBranchLine(cw, trunk int, centers []int) string {
	lo, hi := trunk, trunk
	for _, c := range centers {
		if c < lo {
			lo = c
		}
		if c > hi {
			hi = c
		}
	}
	cells := make([]string, cw)
	for i := range cells {
		cells[i] = " "
	}
	set := func(i int, s string) {
		if i >= 0 && i < cw {
			cells[i] = lampStyle(roleDial).Render(s)
		}
	}
	for i := lo; i <= hi; i++ {
		set(i, "─")
	}
	drop := map[int]bool{}
	for _, c := range centers {
		drop[c] = true
		set(c, "┬")
	}
	switch {
	case !drop[trunk]:
		set(trunk, "┴")
	case lo == hi:
		// ONE CARD, DIRECTLY BELOW. A ┼ says two wires cross, and in a tree they never do, so the
		// honest glyph for "straight down" is a straight line.
		set(trunk, "│")
	default:
		set(trunk, "┼")
	}
	return strings.TrimRight(strings.Join(cells, ""), " ")
}

// edgeNetCardBig is the machine (hub) card: the one red-framed node, name and role.
func edgeNetCardBig(it edgeNetItem, selected bool, w int) string {
	glyph, gs := it.icon()
	border := cLive
	marker := ""
	if selected {
		marker = lampStyle(roleLive).Render(" ◄")
	}
	body := gs.Render(glyph) + " " + stKey.Render(it.name) + marker + "\n" + stDim.Render(it.sub())
	// THE JOIN ADDRESS LIVES ON THE HUB. It is the one fact an owner was made to carry in their
	// head between two commands ("why I need to remember that address"), and the hub card is where
	// it belongs: on the box that IS the authority, in view whether or not anything is waiting.
	if st := it.status; st != nil && st.AuthorityHere && st.AuthorityAddr != "" {
		body += "\n" + stDim.Render("join here  ") + stEmber.Render(st.AuthorityAddr)
	}
	st := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(border).Padding(0, 2)
	if selected {
		st = st.Border(lipgloss.DoubleBorder())
	}
	return st.Width(w - 2).Render(body)
}

// edgeTrunc shortens s to at most w display cells, marking a cut with an ellipsis.
func edgeTrunc(s string, w int) string {
	if w < 1 {
		w = 1
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	r := []rune(s)
	if len(r) > w-1 {
		r = r[:max(1, w-1)]
	}
	return string(r) + "…"
}

// edgeNetCard is one roger/agent card, glowing when selected. The icon and (truncated) name share
// the first line; the second line is the state, or "this one" for the roger you are looking at.
func edgeNetCard(it edgeNetItem, selected bool) string {
	glyph, gs := it.icon()
	// The card's content box is boxW; its horizontal padding (2) leaves boxW-2 usable for text.
	// Everything is truncated to that usable width, or lipgloss re-wraps a long line and breaks the
	// card height. (Total rendered width is boxW+2 for the border.)
	boxW := edgeNetCardW - 4
	avail := boxW - 2
	// The name shares the first line with the glyph and a space; some glyphs (◆ ◇ ◉) render two
	// cells, so budget by the glyph's real width.
	name := edgeTrunc(it.name, avail-lipgloss.Width(glyph)-1)
	head := gs.Render(glyph) + " " + stKey.Render(name)
	sub := stDim.Render(edgeTrunc(it.sub(), avail))
	if it.self {
		// "this one" is WHICH roger, not WHAT IT DOES, and the card has room for both. The roger you
		// are sitting in front of was the one card that never said what it was serving, which is a
		// strange thing for the screen to keep from you (features/edge/agents.feature).
		words := "‹ this one"
		if what := it.subBase(); what != "" && what != "on the Edge" {
			words += " · " + what
		}
		sub = lampStyle(roleLive).Render(edgeTrunc(words, avail))
	}
	stl := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(cRule).Padding(0, 1).Width(boxW)
	if selected {
		stl = stl.Border(lipgloss.DoubleBorder()).BorderForeground(cLive)
	}
	return stl.Render(head + "\n" + sub)
}

// edgeSelectedNet returns the node the map highlight is on, and its index. It is what the control
// panel acts on: the machine, this roger, another roger, or a dark persistent agent - so the
// buttons offer the actions that make sense for THAT node, not one fixed set (2026-09-21).
func (m model) edgeSelectedNet() (edgeNetItem, int) {
	items := m.edgeNetItems()
	if len(items) == 0 {
		return edgeNetItem{}, 0
	}
	sel := m.edge.netSel
	if !m.edge.netSelSet {
		sel = edgeDefaultNetSel(items)
	}
	if sel < 0 {
		sel = 0
	}
	if sel >= len(items) {
		sel = len(items) - 1
	}
	return items[sel], sel
}

// edgeNetDetailView is the ⏎ readout of the SELECTED node on the network map: what it is, what it
// DOES (its job), the models it SHARES to the Edge or USES for its own work, its capabilities, its
// resources, and where to reach it. It answers the founder's "what are these agents doing, what
// model, use or share?" (2026-09-21) - drawing exactly what the node reports, and saying plainly
// where a job or a model binding is not assigned yet rather than inventing one.
func (m model) edgeNetDetailView(w int, line func(string)) {
	it, _ := m.edgeSelectedNet()
	cw := edgeSetupContentWidth(w)
	glyph, gs := it.icon()

	kind := "instance"
	switch {
	case it.machine:
		kind = "this machine · the authority hub"
	case it.cand:
		kind = "discovered · not yet on your Edge"
	case it.member && it.dark:
		// The WORD for what it is, not "machine" for everything: a phone on the Edge said "member
		// machine", which is the confusion this line is meant to settle.
		kind = "member " + strings.ToLower(it.kindOr("unit")) + " · offline"
	case it.member:
		kind = "member " + strings.ToLower(it.kindOr("unit"))
	case it.dark:
		kind = "persistent agent · offline"
	case it.agent && it.self:
		kind = "agent · this roger"
	case it.agent:
		kind = "agent"
	case it.self:
		kind = "this roger"
	}
	// The header, in the arcade frame so the detail reads as part of the same instrument.
	line("  " + edgeSetupCenter(gs.Render(glyph)+"  "+stKey.Render(it.name)+stDim.Render("   "+kind), cw))
	line("")

	lw := 14
	label := func(k, v string) { line("  " + edgeFactLabelStyle(k).Render(pad(k, lw)) + v) }
	wrap := func(k string, v string) {
		for i, ln := range wrapCommand(v, max(24, cw-lw)) {
			kk := ""
			if i == 0 {
				kk = k
			}
			label(kk, ln)
		}
	}

	// THE MACHINE: its role on the Edge, how another node joins, and discovery.
	if it.machine {
		st := it.status
		if st == nil {
			label("machine", "this machine")
			return
		}
		wrap("MACHINE", st.MachineLine())
		for _, ln := range st.AuthorityLines() {
			wrap("AUTHORITY", ln)
		}
		if st.AuthorityHere && st.AuthorityAddr != "" {
			wrap("JOIN", "another machine runs  "+st.EnrollAgainstLine())
		}
		wrap("DISCOVERY", st.DiscoveryLine(m.edge.at))
		edgeAlertLabels(it, label)
		line("")
		line("  " + stDim.Render("esc back to the map · ←→ move to another node"))
		return
	}

	// A MEMBER machine or a DISCOVERED candidate: a node record, not a local instance. Draw what
	// the node reports - its kind, its capabilities, how it is reached, when last seen - and, for a
	// candidate, the one action that brings it onto the Edge.
	if it.node != nil {
		n := it.node
		if it.cand {
			label("STATUS", stEmber.Render("not on your Edge")+stDim.Render(" · press ")+lampStyle(roleLive).Render("a")+stDim.Render(" to adopt it"))
		} else {
			pres := "on the Edge"
			if it.dark {
				pres = "offline · last heartbeat aged out"
			}
			label("STATUS", pres)
		}
		// The kind in the shared word, with the record's own value beside it when they differ, so the
		// screen is readable AND the owner can still see what the record says.
		if w := edge.KindWord(n.Kind); w != "" && w != n.Kind {
			label("KIND", w+stDim.Render("  ("+n.Kind+")"))
		} else {
			label("KIND", n.Kind)
		}
		// THE HARDWARE, as the machine reported it and at full length: the card has room for "Raspberry
		// Pi 4", this has room for the revision too.
		if hw := edge.MachineOf(*n); hw != "" {
			label("HARDWARE", hw)
		}
		// A Pi's power, from the freshest reading any of its rogers sent: the classic Pi failure, so
		// it sits beside the hardware it is about. Happening now is lit; history is dim.
		// WHAT IT SERVES of its own, and what it is fetching - still naming what is served NOW while a
		// fetch for something else runs (features/edge/remote_model.feature).
		if mm := edge.ModelOf(*n); mm != nil {
			label("MODEL", edge.ModelLine(mm))
		}
		if w, ok := edgePiPower(*n); ok {
			if now, _ := edge.PowerWords(w); len(now) > 0 {
				label("POWER", stEmber.Render(edge.PowerLine(w)))
			} else {
				label("POWER", stDim.Render(edge.PowerLine(w)))
			}
		}
		// The FULL id, because this is the one screen with room for it and every `roger edge` command
		// takes one. A node the owner never named is called by a short id everywhere else.
		label("ID", stDim.Render(n.ID))
		if len(n.Caps) == 0 {
			label("CAN DO", stDim.Render("none declared"))
		}
		for i, c := range n.Caps {
			k := ""
			if i == 0 {
				k = "CAN DO"
			}
			state := stDim.Render(c.State)
			switch {
			case c.Name == string(edge.Actuate) && c.State != string(edge.Verified):
				// A node stuck pending is one the owner adopted and cannot use, so the page says what
				// is missing and who can supply it rather than showing a bare state word.
				state = stEmber.Render("awaiting your confirmation") + stDim.Render(" · press ") +
					lampStyle(roleLive).Render("c") + stDim.Render(" to let it change the physical world")
			case c.Name == string(edge.Actuate) && c.ConfirmedBy != "":
				state = stDim.Render(c.State + " · confirmed by " + c.ConfirmedBy)
			}
			label(k, pad(c.Name, 10)+state)
		}
		// THE PATH: how the authority reaches this unit, and through whom. It is the one fact neither
		// the rack nor the card band can show, and it is what turns "via relay" into something an
		// owner can act on: when the relay goes dark, everything behind it goes with it.
		if it.hops > 0 {
			label("PATH", edgePathWords(m.edgeSelfName(), it))
		}
		for i, t := range n.Transports {
			k := ""
			if i == 0 {
				k = "REACH"
			}
			row := pad(t.Kind, 6) + t.Addr
			if i == 0 && len(n.Transports) > 1 {
				row = pad(row, 34) + stDim.Render("preferred")
			}
			label(k, row)
		}
		if n.LastSeen > 0 {
			label("LAST SEEN", edgeAge(m.edge.at.Sub(time.Unix(n.LastSeen, 0)))+" ago")
		}
		// THE OWNER'S OWN WORDS for this unit (features/edge/tags.feature): the groups a rule or a
		// schedule can be scoped to. They are the owner's, never read from the node, so they are
		// shown as typed.
		if len(n.Tags) > 0 {
			label("TAGS", strings.Join(n.Tags, "  "))
		}
		// WHAT IT IS RUNNING (features/edge/inventory.feature). A fleet manager that cannot answer
		// "which of these is behind" is a fleet manager in name only, so the version each roger
		// reports is on the node's own page, beside the newest anything on this Edge reports.
		newest := edgeNewestRogerOn(m.edge.rows)
		for i, in := range n.Instances {
			k := ""
			if i == 0 {
				k = "RUNNING"
			}
			words := edge.SoftwareWords(in.Software)
			if words == "" {
				words = stDim.Render("version not reported")
			} else if ahead := edge.Behind(in, newest); ahead != "" {
				words += stDim.Render("  · behind ") + stEmber.Render(ahead)
			}
			label(k, pad("/"+in.Name, 16)+words)
		}
		// WHAT HAS HAPPENED TO IT (features/edge/events.feature), newest first and only the few that
		// fit: the node page is where an owner asks "why is this thing like this".
		for i, ev := range edgeLastEvents(n.History, 3) {
			k := ""
			if i == 0 {
				k = "LATELY"
			}
			label(k, pad(edgeAge(m.edge.at.Sub(time.Unix(ev.At, 0)))+" ago", 10)+stDim.Render(pad(ev.What, 12)+ev.Detail))
		}
		// WHERE IT DOES NOT MATCH WHAT YOU SAID IT SHOULD BE (features/edge/drift.feature). This is
		// the difference between a fleet you are watching and a fleet you are running, so it sits
		// on the node's own page rather than only in a report the owner has to go and ask for.
		for i, d := range m.edgeDriftOf(n.ID) {
			k := ""
			if i == 0 {
				k = "DRIFT"
			}
			label(k, stEmber.Render(d))
		}
		edgeAlertLabels(it, label)
		// ASK: whether this node can be prompted, and with which model (addressed_inference.feature).
		if model, ok := edgeItemServes(it); ok {
			label("ASK", stKey.Render("p")+stDim.Render(" to prompt ")+stEmber.Render(model)+stDim.Render(" on this node"))
		} else if it.member {
			label("ASK", stDim.Render("serves no model yet - nothing to ask"))
		}
		line("")
		line("  " + stDim.Render("esc back to the map · ←→ move to another node"))
		return
	}

	// WHAT IT DOES - the job and its assignment (jobs.feature). An agent operates on the Edge; a
	// serving instance answers requests; a plain instance is on the Edge. The real assigned work
	// comes from the durable AgentJob record when one is set - never faked.
	job, hasJob := m.edgeJobOf(it.name)
	switch {
	case it.dark:
		label("DOES", stDim.Render("offline - its process is not running"))
	case it.agent:
		label("DOES", "agent · can act on other nodes on the Edge")
	case len(it.bands) > 0:
		label("DOES", "serves a model to the Edge")
	default:
		label("DOES", "on the Edge · not an agent, not serving")
	}
	if !it.dark {
		switch {
		case hasJob && job.Job != "":
			jl := job.Job
			if len(job.Targets) > 0 {
				jl += " · " + strings.Join(job.Targets, ", ")
			}
			label("JOB", stKey.Render(jl))
		default:
			label("JOB", stDim.Render("none assigned · press b to bind a model or job"))
		}
	}

	// MODELS - the SHARE and USE the founder asked for. A shared model is on air to the Edge; a
	// used model is drawn on for the agent's own work. Bands on air also count as shared inference.
	if !it.dark {
		var shares []string
		if s := job.SharesModel(); s != "" {
			shares = append(shares, s)
		}
		shares = append(shares, it.bands...)
		if len(shares) > 0 {
			wrap("SHARES", strings.Join(dedupeNonEmpty(shares), ", ")+stDim.Render("  · local inference, on air to the Edge"))
		} else {
			label("SHARES", stDim.Render("nothing on air · it is not sharing a local model"))
		}
		if u := job.UsesModel(); u != "" {
			wrap("USES", u+stDim.Render("  · draws on this for its own work"))
		} else {
			label("USES", stDim.Render("not assigned · defaults to the Edge/market when it needs one"))
		}
	}

	// CAPABILITIES and RESOURCES, from the live instance record when we have it.
	if it.inst != nil {
		in := it.inst
		if len(in.Caps) == 0 {
			label("CAN DO", stDim.Render("no capabilities declared"))
		}
		for i, c := range in.Caps {
			k := ""
			if i == 0 {
				k = "CAN DO"
			}
			label(k, pad(c.Name, 10)+stDim.Render(c.State))
		}
		if r := in.Resources; r != nil {
			if r.GPU != "" {
				used := ""
				if r.GPUMemUsed > 0 {
					used = stDim.Render(fmtPct(r.GPUMemUsed) + " mem")
				}
				wrap("GPU", r.GPU+"  "+used)
			}
			if r.RAMTotalGB > 0 {
				label("RAM", fmtGB(r.RAMUsedGB)+" / "+fmtGB(r.RAMTotalGB)+" GB")
			}
		}
		if in.Port > 0 {
			label("ADDRESS", "this machine, port "+strconv.Itoa(in.Port))
		}
	}

	// A DARK agent's two actions, right here in its readout.
	if it.dark {
		line("")
		label("RESUME", stDim.Render("press ")+lampStyle(roleLive).Render("u")+stDim.Render(" to relaunch it here"))
		label("REMOVE", stDim.Render("press ")+lampStyle(roleLive).Render("x")+stDim.Render(" to forget it"))
	}

	line("")
	line("  " + stDim.Render("esc back to the map · ←→ move to another node"))
}

// fmtPct renders a 0..1 fraction as a whole percent.
func fmtPct(f float64) string { return strconv.Itoa(int(f*100+0.5)) + "%" }

// fmtGB renders a GB figure with one decimal, trimming a trailing .0.
func fmtGB(g float64) string {
	s := strconv.FormatFloat(g, 'f', 1, 64)
	return strings.TrimSuffix(s, ".0")
}

// edgeJobLabel is the short card label for an assignment: its job (with a model when it serves
// one), or the bound model's posture when there is a model but no named job. "" when idle.
func edgeJobLabel(j edge.AgentJob) string {
	switch {
	case j.Job == "serve" && j.Model != "":
		return "serving " + j.Model
	case j.Job != "":
		return j.Job
	case j.SharesModel() != "":
		return "sharing " + j.SharesModel()
	case j.UsesModel() != "":
		return "using " + j.UsesModel()
	default:
		return ""
	}
}

// edgeJobOf returns the durable assignment for a node by name, and whether it has one. It is how
// the map and the detail learn what an agent is DOING - its bound model and job (jobs.feature).
func (m model) edgeJobOf(name string) (edge.AgentJob, bool) {
	if m.hooks.EdgeAgentJobs == nil || name == "" {
		return edge.AgentJob{}, false
	}
	for _, j := range m.hooks.EdgeAgentJobs() {
		if j.Name == name {
			return j, true
		}
	}
	return edge.AgentJob{}, false
}

// kindLabel is the one-word kind in the list view's KIND column, from the one shared vocabulary
// (edge.KindWord). A MEMBER used to read "machine" whatever it really was, so an adopted phone sat
// in the fleet labelled a machine - the very confusion this column exists to clear up.
func (it edgeNetItem) kindLabel() string {
	switch {
	case it.machine:
		// The hub is the machine you are on. Its kind is not in doubt and the STATE column already
		// says "YOU ARE HERE", so this column names the role rather than guessing at hardware.
		return "machine"
	case it.cand, it.member:
		if w := edge.KindWord(it.kind); w != "" {
			return w
		}
		return "device"
	case it.dark, it.agent:
		return "agent"
	default:
		return "roger"
	}
}

// edgeMapList draws the topology as a compact table - one row per node - for a dense read when the
// card map would run off the screen (founder 2026-09-23). Same nodes, same selection (netSel), same
// actions; `v` switches back to the map. The selected row glows with a ▸.
func (m model) edgeMapList(cw int, line func(string)) {
	items := m.edgeNetItems()
	_, sel := m.edgeSelectedNet()
	nameW := 22
	kindW := 9
	line("  " + stDim.Render(pad("  NODE", nameW+4)+pad("KIND", kindW)+"STATE"))
	for i, it := range items {
		glyph, gs := it.icon()
		name := edgeTrunc(it.name, nameW-2)
		state := it.sub()
		if it.self {
			state = "‹ this one · " + state
		}
		marker := "  "
		if i == sel {
			marker = lampStyle(roleLive).Render("▸ ")
		}
		row := marker + gs.Render(glyph) + " " + stKey.Render(pad(name, nameW)) +
			stDim.Render(pad(it.kindLabel(), kindW)+state)
		line("  " + truncVisible(row, cw))
	}
}

// edgePathWords is the route to a unit as an operator reads it: "desk → wiltshire → nina · 2 hops".
// One hop is drawn as the direct thing it is rather than as a route with a middle.
func edgePathWords(self string, it edgeNetItem) string {
	hop := " hops"
	if it.hops == 1 {
		hop = " hop"
	}
	route := self + " → " + it.name
	if it.via != "" {
		route = self + " → " + it.via + " → " + it.name
	}
	out := route + stDim.Render("  ·  "+strconv.Itoa(it.hops)+hop)
	if it.via != "" {
		out += stDim.Render(" · if " + it.via + " goes dark, so does this")
	}
	return out
}

// edgeDriftOf is what this node is failing to be, or nothing. A build that wired no expectations
// has no drift to report, which is different from a node that is in step - so nothing is drawn
// rather than a reassuring line nobody checked.
func (m model) edgeDriftOf(id string) []string {
	if m.hooks.EdgeDrift == nil || id == "" {
		return nil
	}
	return m.hooks.EdgeDrift()[id]
}

// edgeLastEvents is the newest n events on a node, newest first. The record keeps them oldest
// first and bounded; a node page has room for a few, and the rest are `roger edge events`.
func edgeLastEvents(in []store.EdgeEvent, n int) []store.EdgeEvent {
	out := make([]store.EdgeEvent, 0, n)
	for i := len(in) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, in[i])
	}
	return out
}

// edgeNewestRogerOn is the newest roger version anything on this Edge reports, which is what
// "behind" is measured against. It is what the fleet HAS, never a release feed: this screen makes
// no claim about a version it has not seen on a unit of its own.
func edgeNewestRogerOn(rows []edgeRow) string {
	nodes := make([]store.EdgeNode, 0, len(rows))
	for _, r := range rows {
		nodes = append(nodes, r.n)
	}
	return edge.NewestRoger(nodes)
}

// edgeAlertLabels writes the rules firing for a node into its readout, one line each, in the live
// color: an alert is a signal. Nothing is written when nothing fires - "no alerts" is the default
// state and would only dilute the cards that do carry one.
func edgeAlertLabels(it edgeNetItem, label func(k, v string)) {
	for i, a := range it.alerts {
		k := ""
		if i == 0 {
			k = "ALERT"
		}
		label(k, lampStyle(roleLive).Render("firing · "+a))
	}
}

// edgePiPower is the freshest power word any roger on the node reported, and whether there is one.
func edgePiPower(n store.EdgeNode) (uint32, bool) {
	var best *store.EdgeResources
	for _, in := range n.Instances {
		if r := in.Resources; r != nil && r.PiPower != nil && (best == nil || r.ReadAt > best.ReadAt) {
			best = r
		}
	}
	if best == nil {
		return 0, false
	}
	return *best.PiPower, true
}
