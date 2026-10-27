package tui

// THE ENROLLED [3] EDGE IN THE HEALTH/FLEET LOOK (features/edge/enrolled_edge_look.feature,
// approved 2026-10-13). The screen a set-up machine opens on: a muted ROGER · EDGE brand band with
// its ON AIR lamp, the YOUR EDGE title row, three one-row facts, the map as a tree hanging from the
// hub's left edge (or the list), NEARBY, WHAT YOU CAN DO and the key-cap action bar. Every key,
// selection rule and map rule is the one edge.go and edge_map_view.go already keep; only the
// drawing lives here.
//
// THE HEIGHT IS MEASURED (D3, and the founder's ruling R1 of 2026-10-14). View hands the body the
// rows the chrome leaves it, and rows give way in a fixed order until the frame fits: the blank
// rows (the lowest first), NO DEVICES YET folds to one row, ADOPTING and SESSIONS fold to one row
// each, the buttons' explanations, the THIS MACHINE and AUTHORITY rows, the action bar; then the
// map becomes the list, says so, and the order starts over. A list still too long shows a window
// that follows the selection and says how many of how many. The chrome above (the tabs, the brand)
// and below (the status line) is never cut. A frame with no measured height draws everything.

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"rogerai.fm/roger/v7/internal/edge"
	"rogerai.fm/roger/v7/internal/glyphs"
	"rogerai.fm/roger/v7/internal/look"
)

// edgeRailCardW is a rail card's width after its rail; edgeRailCardText the text a row holds.
const (
	edgeRailCardW    = 34
	edgeRailCardText = edgeRailCardW - 2
)

// The list's notes, said on the YOUR EDGE NETWORK row when the list stands in for the map.
const (
	edgeShortNote  = "short pane: the list"
	edgeNarrowNote = "narrow pane: the list"
)

// lookPal is the look's styles for one frame; under NO_COLOR they carry no colour.
type lookPal struct {
	mono                              bool
	muted, ink, paper, live, dim, ash lipgloss.Style
	eye                               lipgloss.Style
}

func lookPalette(mono bool) lookPal {
	p := lookPal{mono: mono, muted: lookStyle(look.Muted), ink: lookStyle(look.Ink), paper: lookStyle(look.Paper),
		live: lookStyle(look.Live), dim: lookStyle(look.Dim), ash: lookStyle(look.Ash), eye: lookStyle(look.Signal)}
	if mono {
		for _, s := range []*lipgloss.Style{&p.muted, &p.ink, &p.paper, &p.live, &p.dim, &p.ash, &p.eye} {
			*s = s.UnsetForeground()
		}
	}
	return p
}

// rail is a card's or a row's left rail: red when it is the selection, dim otherwise. With no
// colour (or on a legacy console) the two read ">" and "|", so the selection is still told.
func (p lookPal) rail(sel bool) string {
	if sel {
		return p.live.Render(look.Fold(look.Rail, p.mono))
	}
	if p.mono || glyphs.ASCII() {
		return p.dim.Render("|")
	}
	return p.dim.Render(look.Rail)
}

// lookBrandRow is the muted "▟•▙ ROGER · EDGE" eyebrow both [3] states open with.
func lookBrandRow(p lookPal) string {
	return "  " + p.muted.Render(look.Fold(look.EmptyEdgeBrand, p.mono))
}

// The tree's geometry: the trunk hangs under the hub's glyph, each hop one step further in.
const (
	edgeTreeTrunk = 4
	edgeTreeStep  = 2
)

// edgeTreeLegend is the widest wire words a card carries beside it.
var edgeTreeLegend = lipgloss.Width("┊ through a relay")

// edgeMapFitsTree reports whether the content width holds the tree: a member three hops deep (the
// trunk and two steps), its rail, a whole card, and the widest wire words beside it. Narrower, the
// picture says less than the list does, so the list stands in and says so.
func edgeMapFitsTree(cw int) bool {
	return cw >= edgeTreeTrunk+2*edgeTreeStep+2+edgeRailCardW+edgeTreeLegend
}

// The rows a frame may keep or let go (D3, R1). A row tagged for one variant is drawn only in it.
const (
	lookRow       = iota // always drawn
	lookBlank            // the first to give way, the lowest first
	lookNoDevFull        // NO DEVICES YET in full
	lookNoDevFold        // NO DEVICES YET folded to one row
	lookNotesFull        // ADOPTING's codes and the SESSIONS rows, in full
	lookNotesFold        // ADOPTING and SESSIONS folded to one row each
	lookFact             // THIS MACHINE and AUTHORITY (DISCOVERY is a lookRow)
	lookTree             // the map as the tree, with its heading
	lookList             // the map as the list, with its heading
	lookListUnit         // one unit's row in the list (its item index rides with it)
	lookExplained        // a button with its explanation beside it
	lookCaps             // the buttons' caps sharing a row, once the explanations gave way
	lookBar              // the key-cap action bar
)

// edgeLookLine is one row of the frame, the variant it belongs to, and for a list unit its item.
type edgeLookLine struct {
	text string
	tag  int
	item int
}

// edgeLookOpts is what has given way in a frame.
type edgeLookOpts struct{ list, foldNoDev, foldNotes, capsRow, noFacts, noBar bool }

// keeps reports whether a row of this tag is drawn under these options.
func (o edgeLookOpts) keeps(tag int) bool {
	switch tag {
	case lookNoDevFull:
		return !o.foldNoDev
	case lookNoDevFold:
		return o.foldNoDev
	case lookNotesFull:
		return !o.foldNotes
	case lookNotesFold:
		return o.foldNotes
	case lookFact:
		return !o.noFacts
	case lookTree:
		return !o.list
	case lookList, lookListUnit:
		return o.list
	case lookExplained:
		return !o.capsRow
	case lookCaps:
		return o.capsRow
	case lookBar:
		return !o.noBar
	}
	return true
}

// edgeLargeNote is what the list says when the Edge has more units than the tree draws (R3).
const edgeLargeNote = "a large Edge: the list"

// edgeListNote is why the list stands in for the map, or "" when the owner chose it (v) or the
// tree is drawn.
func (m model) edgeListNote(w int) string {
	switch {
	case m.edge.mapList:
		return ""
	case len(m.edge.rows)+len(m.edge.cands) > edgeMaxGraphNodes:
		return edgeLargeNote
	case !edgeMapFitsTree(edgeSetupContentWidth(w)):
		return edgeNarrowNote
	}
	return edgeShortNote
}

// edgeForcedListWhy says why the map cannot be drawn when the pane or the Edge forces the list,
// or "" when v can switch (L1 of the 2026-10-14 review).
func (m model) edgeForcedListWhy() string {
	if !m.edgeMapActive() || m.edge.mapList {
		return ""
	}
	switch m.edgeListNote(m.effWidth()) {
	case edgeLargeNote:
		return "a large Edge is drawn as the list"
	case edgeNarrowNote:
		return "the map needs a wider pane"
	}
	if m.height > 0 && strings.Contains(m.View(), edgeShortNote) {
		return "the map needs a taller pane"
	}
	return ""
}

// edgeEnrolledLines draws the enrolled screen in at most h rows (h <= 0: every row). The frame is
// drawn once with every variant tagged, and the fitter only chooses rows among them.
func (m model) edgeEnrolledLines(w, h int) []string {
	note := m.edgeListNote(w)
	all := m.edgeEnrolledBuild(w, edgeSetupContentWidth(w), note)
	phases := []edgeLookOpts{{list: true}}
	if note == edgeShortNote {
		phases = []edgeLookOpts{{}, {list: true}}
	}
	steps := []func(*edgeLookOpts){
		func(o *edgeLookOpts) { o.foldNoDev = true },
		func(o *edgeLookOpts) { o.foldNotes = true },
		func(o *edgeLookOpts) { o.capsRow = true },
		func(o *edgeLookOpts) { o.noFacts = true },
		func(o *edgeLookOpts) { o.noBar = true },
	}
	var kept []edgeLookLine
	for _, base := range phases {
		o := base
		var fits bool
		if kept, fits = edgeLookFit(all, o, h, false); fits {
			return edgeLookTexts(kept)
		}
		for _, step := range steps {
			step(&o)
			// Once anything else has given way, every blank row has too.
			if kept, fits = edgeLookFit(all, o, h, true); fits {
				return edgeLookTexts(kept)
			}
		}
	}
	return edgeLookTexts(m.edgeLookWindow(kept, h, w))
}

// edgeLookFit keeps the rows o keeps, then drops blank rows, the lowest first (every one of them
// when all is set), until the frame is at most h rows; it reports whether the frame then fits.
func edgeLookFit(ls []edgeLookLine, o edgeLookOpts, h int, all bool) ([]edgeLookLine, bool) {
	var kept []edgeLookLine
	for _, l := range ls {
		if o.keeps(l.tag) {
			kept = append(kept, l)
		}
	}
	extra := len(kept) - h
	drop := make([]bool, len(kept))
	for i := len(kept) - 1; i >= 0 && h > 0 && (all || extra > 0); i-- {
		if kept[i].tag == lookBlank {
			drop[i] = true
			extra--
		}
	}
	out := make([]edgeLookLine, 0, len(kept))
	for i, l := range kept {
		if !drop[i] {
			out = append(out, l)
		}
	}
	return out, h <= 0 || len(out) <= h
}

// edgeLookWindow is the last resort for a list still too long for the pane: the unit rows become a
// window that holds the selection, followed by a row saying how many of how many it shows. If even
// that cannot fit, the lowest rows go, so the chrome above and below is never cut.
func (m model) edgeLookWindow(ls []edgeLookLine, h, w int) []edgeLookLine {
	var units []int // indexes into ls of the list's unit rows
	selAt := 0
	_, sel := m.edgeSelectedNet()
	for i, l := range ls {
		if l.tag == lookListUnit {
			if l.item == sel {
				selAt = len(units)
			}
			units = append(units, i)
		}
	}
	room := len(units) - (len(ls) - h) - 1 // the unit rows that fit beside the "showing" row
	if len(units) > 1 && room >= 1 {
		top := max(0, min(selAt-room/2, len(units)-room))
		p := lookPalette(lookMono())
		showing := edgeLookLine{tag: lookRow, text: edgeLookClip("  "+p.muted.Render(
			"showing "+strconv.Itoa(top+1)+"-"+strconv.Itoa(top+room)+" of "+strconv.Itoa(len(units))+" units · ↑↓ moves through them"), w)}
		var out []edgeLookLine
		for k, i := range units {
			if k == 0 {
				out = append(out, ls[:i]...)
			}
			if k >= top && k < top+room {
				out = append(out, ls[i])
			}
			if k == len(units)-1 {
				out = append(out, showing)
				out = append(out, ls[i+1:]...)
			}
		}
		ls = out
	}
	if h > 0 && len(ls) > h {
		ls = ls[:h]
	}
	return ls
}

func edgeLookTexts(ls []edgeLookLine) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		out[i] = l.text
	}
	return out
}

// edgeEnrolledBuild draws every region, each variant tagged; note is what the list says it is.
func (m model) edgeEnrolledBuild(w, cw int, note string) []edgeLookLine {
	p := lookPalette(lookMono())
	var out []edgeLookLine
	put := func(tag int, s string) { out = append(out, edgeLookLine{text: edgeLookClip(s, w), tag: tag, item: -1}) }
	add := func(s string) { put(lookRow, s) }
	blank := func() { put(lookBlank, "") }
	st := m.edge.status
	items := m.edgeNetItems()
	sel := edgeSelIndex(m.edge, items)
	sparse := m.edgeLookSparse()

	// The brand band and the title row (in the windowshade they are one row).
	for _, ln := range m.edgeLookHead(p, edgeEmptyHeadline(m.edge)) {
		add(ln)
	}

	// The facts: one row each, eyebrow and ink, too long ending in an ellipsis.
	fact := func(label, val string) {
		put(lookFact, "  "+p.muted.Render(pad(label, edgeFactW))+p.ink.Render(edgeTrunc(val, max(8, w-2-edgeFactW))))
	}
	// This machine by its name, never a raw node id (then by the name the hub card wears).
	named := *st
	named.Name = edge.SelfLabel(st.Name, m.edgeSelfName())
	fact("THIS MACHINE", named.MachineLine())
	// ONBOARDING: alone on its Edge, the authority fact is told in full, wrapped, a row per line
	// (features/edge/empty_edge.feature); once anything else is on it, its first line, and the rest
	// is one enter away on the hub.
	for i, ln := range st.AuthorityLines() {
		label := ""
		if i == 0 {
			label = "AUTHORITY"
		}
		if !sparse {
			if i == 0 {
				fact(label, ln)
			}
			continue
		}
		for _, part := range wrapCommand(ln, max(20, w-2-edgeFactW)) {
			put(lookFact, "  "+p.muted.Render(pad(label, edgeFactW))+p.ink.Render(part))
			label = ""
		}
	}
	scan := st.Scan()
	eye := p.eye
	if scan.Off {
		eye = p.muted
	}
	add("  " + p.muted.Render(pad("DISCOVERY", edgeFactW)) + eye.Render(look.Fold(scan.Eye(), p.mono)) + " " +
		p.ink.Render(edgeTrunc(st.DiscoveryLine(m.edge.at), max(8, w-5-edgeFactW))))
	if m.edge.err != "" {
		add("  " + p.live.Render(edgeTrunc("the Edge could not be read: "+m.edge.err, max(8, w-2))))
	}
	blank()

	// YOUR EDGE NETWORK and its tally, then the map: the tree and the list, each with its heading.
	heading := func(note string) string {
		head := "YOUR EDGE NETWORK"
		tailW := w - 2 - len(head) - 2
		if note != "" {
			tailW -= len("  ·  " + note)
		}
		tail := edgeMapTally(items, max(1, tailW))
		if note != "" {
			if tail != "" {
				tail += "  ·  "
			}
			tail += note
		}
		return "  " + p.muted.Render(head) + p.muted.Render("  "+tail)
	}
	put(lookTree, heading(""))
	edgeLookTree(p, w, items, sel, m.edgePulsing, func(s string) { put(lookTree, s) })
	put(lookList, heading(note))
	edgeLookList(p, w, cw, items, sel, func(i int, s string) {
		out = append(out, edgeLookLine{text: edgeLookClip(s, w), tag: lookListUnit, item: i})
	})
	if len(m.edge.rows) == 0 && len(m.edge.cands) == 0 {
		// A fresh authority alone on its Edge is told how the next device joins.
		if next := edgeAddNextLine(st); next != "" {
			for _, part := range wrapCommand(next, max(20, w-4)) {
				add("  " + p.muted.Render(part))
			}
		}
	}

	// NEARBY, as §7 draws it at this width, then the adopting and no-devices notes.
	for _, ln := range m.edgeNearbyLines(w, p.mono) {
		add(ln)
	}
	m.edgeLookAdopting(p, put)
	m.edgeLookNoDevices(p, put)

	// The traffic this Edge carried, and the one row it folds to (R1).
	if n := len(m.edge.sessions); n > 0 {
		m.edgeSessionBlock(edgeGeomFor(w, 0), func(s string) {
			if strings.TrimSpace(s) == "" {
				blank()
				return
			}
			put(lookNotesFull, s)
		})
		put(lookNotesFold, "  "+p.muted.Render(plural(n, "session")+" · a taller pane shows them"))
	}

	// WHAT YOU CAN DO and the buttons, or the prompt that owns the panel; then the action bar.
	m.edgeLookActions(p, w, items[sel], put)
	put(lookBar, m.edgeLookBar(w))
	return out
}

// edgeLookClip folds a row for a legacy console and bounds it to the terminal; a row that has to
// be cut ends in an ellipsis, never a cut word.
func edgeLookClip(s string, w int) string {
	s = strings.TrimRight(s, " ")
	if glyphs.ASCII() {
		s = glyphs.FoldASCII(edgeASCII.Replace(s))
	}
	return truncVisibleTail(s, w)
}

// edgeLookSparse reports whether this machine is alone on its Edge: no member, no device nearby
// and no other roger here.
func (m model) edgeLookSparse() bool {
	household := 0
	if m.hooks.EdgeHousehold != nil {
		household = len(m.hooks.EdgeHousehold())
	}
	return len(m.edge.rows) == 0 && len(m.edge.cands) == 0 && household <= 1
}

// edgeLookHead is the brand band and the title row: "▟•▙ ROGER · EDGE   ● ON AIR" over
// "▌ YOUR EDGE · LOCAL ROOT · shed · prefers local". The lamp is lit only while this machine is
// enrolled, and it never breathes (D4). In the windowshade the band folds into the title row.
func (m model) edgeLookHead(p lookPal, headline string) []string {
	lamp := p.muted.Render("○ off air")
	if st := m.edge.status; st != nil && st.Enrolled {
		lamp = p.live.Render("● ON AIR")
	}
	title := "  " + p.rail(true) + " " + p.paper.Bold(true).Render("YOUR EDGE")
	for _, seg := range strings.Split(strings.TrimPrefix(headline, "EDGE"), " · ") {
		if seg = strings.TrimSpace(seg); seg != "" {
			title += p.muted.Render(" · ") + p.ink.Render(seg)
		}
	}
	if m.compact {
		return []string{title + "   " + lamp}
	}
	return []string{lookBrandRow(p) + "   " + lamp, title}
}

// edgeLookCard is one rail card: the glyph and name, its words, and for the hub where to join.
// legend, when set, rides on the first row after the card.
func edgeLookCard(p lookPal, it edgeNetItem, sel bool, textW int, legend string) []string {
	glyph, gs := it.icon()
	nameSt := p.ink
	switch {
	case sel:
		nameSt = p.paper.Bold(true)
	case it.dark:
		gs, nameSt = p.ash, p.ash
	}
	rail := p.rail(sel) + " "
	if legend != "" {
		textW = min(edgeRailCardText, textW) // a card with wire words beside it keeps its width, so they line up
	}
	textW = max(8, textW)
	first := rail + gs.Render(glyph) + " " + nameSt.Render(edgeTrunc(it.name, textW-lipgloss.Width(glyph)-1))
	if legend != "" {
		first += strings.Repeat(" ", max(1, 2+edgeRailCardW-lipgloss.Width(first))) + legend
	}
	words := p.muted
	if it.dark {
		words = p.ash
	}
	sub := words.Render(edgeTrunc(it.sub(), textW))
	if it.self {
		// "this one" is WHICH roger, and what it does rides beside it (features/edge/agents.feature).
		what := "‹ this one"
		if b := it.subBase(); b != "" && b != "on the Edge" {
			what += " · " + b
		}
		sub = p.live.Render(edgeTrunc(what, textW))
	}
	rows := []string{first, rail + sub}
	// THE JOIN ADDRESS LIVES ON THE HUB: on the box that IS the authority, in view always.
	if st := it.status; it.machine && st != nil && st.AuthorityHere && st.AuthorityAddr != "" {
		rows = append(rows, rail+p.muted.Render("join here  ")+p.ink.Render(st.AuthorityAddr))
	}
	return rows
}

// edgeLookTree draws the map as a tree hanging from the hub's left edge (D1): the hub card, the
// trunk under its glyph with the rogers running here, then the members on their own drops, each
// reached through a relay one level further in.
func edgeLookTree(p lookPal, w int, items []edgeNetItem, sel int, pulsing func(edgeNetItem) bool, add func(string)) {
	for _, r := range edgeLookCard(p, items[0], sel == 0, w-6, "") {
		add("  " + r)
	}
	const trunk = edgeTreeTrunk // the column under the hub's glyph
	var hubIx, memIx []int
	var mems []edgeNetItem
	for i := 1; i < len(items); i++ {
		switch it := items[i]; {
		case it.cand:
			// drawn as NEARBY rows, never on the tree
		case it.member:
			memIx, mems = append(memIx, i), append(mems, it)
		default:
			hubIx = append(hubIx, i)
		}
	}
	if len(hubIx) == 0 {
		add(strings.Repeat(" ", trunk) + lampStyle(roleDial).Render("│") + "  " + p.muted.Render("no rogers running here yet"))
	}
	for _, i := range hubIx {
		g, gs := edgeDropWire(items[i])
		add(strings.Repeat(" ", trunk) + gs.Render(g))
		for _, r := range edgeLookCard(p, items[i], sel == i, w-trunk-2, "") {
			add(strings.Repeat(" ", trunk) + r)
		}
	}
	legend := edgeWireLegendOn(mems)
	heading := true
	edgeLookMembers(p, w, trunk, mems, memIx, sel, "", legend, &heading, pulsing, add)
}

// edgeLookMembers hangs the members reached through parent ("" is the hub) at indent, each on its
// own drop, and recurses one level in for what each of them relays. edge.Arrange draws every member
// once and never under itself, so this walks a forest and cannot loop.
func edgeLookMembers(p lookPal, w, indent int, its []edgeNetItem, idx []int, sel int, parent string,
	legend bool, heading *bool, pulsing func(edgeNetItem) bool, add func(string)) {
	for j, it := range its {
		if it.via != parent {
			continue
		}
		g, gs := edgeDropWire(it)
		if pulsing(it) {
			g, gs = "●", p.live // a heartbeat arriving on this wire, for this frame
		}
		drop := strings.Repeat(" ", indent) + gs.Render(g)
		if *heading {
			drop += "  " + p.muted.Render("ON THE EDGE")
			*heading = false
		}
		add(drop)
		words := ""
		if legend {
			words = edgeWireWords(p, it)
		}
		for _, r := range edgeLookCard(p, it, sel == idx[j], w-indent-2, words) {
			add(strings.Repeat(" ", indent) + r)
		}
		if it.node != nil {
			edgeLookMembers(p, w, indent+edgeTreeStep, its, idx, sel, it.node.Name, legend, heading, pulsing, add)
		}
	}
}

// edgeWireLegendOn reports whether any wire among these members is not LAN-direct. A fleet whose
// every wire means the same thing is not taught three glyphs to read one.
func edgeWireLegendOn(its []edgeNetItem) bool {
	for _, it := range its {
		if it.node != nil && edgeTexture(*it.node) != edgeGlyphSolid {
			return true
		}
	}
	return false
}

// edgeWireWords names one member's wire beside its card: the glyph in its tint, then its meaning.
func edgeWireWords(p lookPal, it edgeNetItem) string {
	if it.node == nil {
		return ""
	}
	g, gs := edgeDropWire(it)
	switch edgeTexture(*it.node) {
	case edgeGlyphSolid:
		return gs.Render(g) + p.muted.Render(" direct")
	case edgeGlyphDim:
		return gs.Render(g) + p.muted.Render(" through a relay")
	}
	return gs.Render(g) + p.muted.Render(" not heard from")
}

// edgeLookList draws the map as one row per unit: a rail, the glyph and name, and the state.
func edgeLookList(p lookPal, w, cw int, items []edgeNetItem, sel int, add func(int, string)) {
	const nameW = 22
	right := min(w, cw+2)
	for i, it := range items {
		if it.cand {
			continue // drawn as NEARBY rows, as on the tree
		}
		glyph, gs := it.icon()
		nameSt := p.ink
		switch {
		case i == sel:
			nameSt = p.paper.Bold(true)
		case it.dark:
			gs, nameSt = p.ash, p.ash
		}
		row := "  " + p.rail(i == sel) + " " + gs.Render(glyph) + " " + nameSt.Render(pad(edgeTrunc(it.name, nameW-2), nameW))
		avail := max(4, right-lipgloss.Width(row))
		state := it.sub()
		if it.self {
			lead := "‹ this one · "
			row += p.live.Render(edgeTrunc(lead, avail))
			avail -= len([]rune(lead))
		}
		if avail > 0 {
			row += p.muted.Render(edgeTrunc(state, avail))
		}
		add(i, row)
	}
}

// edgeLookAdopting names the devices adopted and not yet checked in, with each one's code. When the
// notes fold (R1) it is one row that names them and says where the codes are.
func (m model) edgeLookAdopting(p lookPal, put func(int, string)) {
	if m.hooks.EdgeAdopting == nil {
		return
	}
	ad := m.hooks.EdgeAdopting()
	if len(ad) == 0 {
		return
	}
	names := make([]string, 0, len(ad))
	for _, n := range ad {
		nm := n.Name
		if nm == "" {
			nm = n.ID
		}
		if strings.HasPrefix(nm, "n_") && len(nm) > 12 {
			nm = nm[:12] + "…"
		}
		names = append(names, nm)
	}
	head := "  " + p.muted.Render("ADOPTING  ") + p.ink.Render(strings.Join(names, ", "))
	if m.hooks.EdgeAdoptCode == nil {
		put(lookRow, head+p.muted.Render("  · claiming its certificate, will appear as a member"))
		return
	}
	put(lookNotesFull, head+p.muted.Render("  · claiming its certificate, will appear as a member"))
	put(lookNotesFold, head+p.muted.Render("  · a taller pane shows the code"))
	// The one-time code each adopted device enters to keep this Edge's root, until it is spent or
	// expires (features/edge/enrollment_root_trust.feature). Shown whole, one device per line.
	for i, n := range ad {
		if code, until, ok := m.hooks.EdgeAdoptCode(n.ID); ok {
			put(lookNotesFull, "  "+p.ink.Render(names[i])+p.muted.Render(" · code ")+p.paper.Render(code)+
				p.muted.Render(" · "+edgeMinutesLeft(until.Sub(m.edgeNow()))+" left"))
			continue
		}
		put(lookNotesFull, "  "+p.ink.Render(names[i])+p.muted.Render(" · code expired - ")+
			p.ink.Render("roger edge invite --for "+names[i])+p.muted.Render(" (or i on its row) for a fresh code"))
	}
}

// edgeLookNoDevices says where a device shows up when nothing is pinging, with this Edge's address;
// folded (R1), one row.
func (m model) edgeLookNoDevices(p lookPal, put func(int, string)) {
	if len(m.edgeNearbyItems()) > 0 {
		return
	}
	full := func(s string) { put(lookNoDevFull, s) }
	put(lookBlank, "")
	full("  " + p.muted.Render("NO DEVICES YET") + p.muted.Render("  a phone or machine running roger nearby shows here, to invite or approve"))
	if st := m.edge.status; st != nil && st.AuthorityHere && st.AuthorityAddr != "" {
		full("  " + p.muted.Render("open the Roger app and point it at this Edge:"))
		full("  " + p.ink.Render(st.AuthorityAddr) + p.muted.Render("   ("+m.edgeSelfName()+")"))
		full("  " + p.muted.Render("it should find this Edge on its own; the address is here for when it cannot"))
		put(lookNoDevFold, "  "+p.muted.Render("no devices yet · point the Roger app at this Edge"))
		return
	}
	full("  " + p.muted.Render("this machine roots no Edge, so there is nothing for a device to join here"))
	put(lookNoDevFold, "  "+p.muted.Render("no devices yet · this machine roots no Edge"))
}

// edgeLookActions is WHAT YOU CAN DO on the selected unit and its buttons, one per row with the
// explanation beside it (D5), or the caps on one row when the explanations gave way. The keyboard-
// focused button carries the red rail, and nothing else does. A prompt that is open owns the panel.
func (m model) edgeLookActions(p lookPal, w int, it edgeNetItem, put func(int, string)) {
	add := func(s string) { put(lookRow, s) }
	prompt := func(eyebrow, keys, buf string) {
		add("  " + p.muted.Render(eyebrow) + p.muted.Render("  "+keys))
		add("  " + p.live.Render("▶ ") + p.ink.Render(buf) + p.live.Render("_"))
	}
	switch {
	case m.edge.renaming:
		prompt("RENAME THIS ROGER", "⏎ apply · esc cancel", m.edge.renameBuf)
		return
	case m.edge.serveModel.open:
		m.edgeServeModelPanel(add)
		return
	case m.edge.binding:
		prompt("WHICH MODEL SHOULD "+strings.ToUpper(m.edge.bindTarget)+" RUN?", "⏎ record · esc cancel", m.edge.bindBuf)
		// It records an assignment; it does not put anything on air (features/edge/jobs.feature).
		add("  " + p.muted.Render("records the assignment · putting a model on air is still `roger share`"))
		return
	}
	glyph, gs := it.icon()
	head := "  " + p.muted.Render("WHAT YOU CAN DO") + p.muted.Render("  on  ") + gs.Render(glyph) + " "
	add(head + p.ink.Render(edgeTrunc(edgeActionsWho(it), max(8, w-lipgloss.Width(head)))))
	acts := m.edgeActions()
	if len(acts) == 0 {
		// Only a member that serves no model yet reaches this (features/edge/addressed_inference.feature).
		add("  " + p.muted.Render(edgeTrunc("this device serves no model yet, so there is nothing to ask it - a served, verified model makes it askable", max(8, w-2))))
		return
	}
	focus := -1
	if m.edge.actionFocused {
		focus = min(m.edge.actionSel, len(acts)-1)
	}
	button := func(i int, a edgeAction) string {
		slot := "  "
		if i == focus {
			slot = p.rail(true) + " "
		}
		s := slot + lookKeyCap(a.key, p.mono) + " " + p.paper.Render(a.label)
		if a.lit {
			s += p.live.Render(" ●")
		}
		return s
	}
	for i, a := range acts {
		b := button(i, a)
		put(lookExplained, b+"  "+p.muted.Render(edgeTrunc(a.sub, max(4, w-lipgloss.Width(b)-2))))
	}
	row := ""
	for i, a := range acts {
		b := button(i, a)
		switch {
		case row == "":
			row = b
		case lipgloss.Width(row)+1+lipgloss.Width(b) > w:
			put(lookCaps, row)
			row = b
		default:
			row += " " + b
		}
	}
	put(lookCaps, row)
}

// edgeActionsWho is what WHAT YOU CAN DO says the buttons act on: the selected unit and its kind.
func edgeActionsWho(it edgeNetItem) string {
	switch {
	case it.machine:
		return it.name + " · this machine"
	case it.self:
		return it.name + " · this roger"
	case it.cand:
		return it.name + " · " + it.kindWord() + " · nearby, not on your Edge yet"
	case it.member:
		// A dark MEMBER is an offline device, not an offline agent; its kind leads.
		who := it.name + " · " + it.kindWord() + " · member"
		if it.dark {
			who += " · offline"
		}
		return who
	case it.dark:
		return it.name + " · offline agent"
	}
	return it.name
}

// edgeLookBar is the action bar at the foot of the screen: the keys in paper, their words muted.
func (m model) edgeLookBar(w int) string {
	if m.edge.actionFocused {
		return lookActionBar([]look.Action{{Key: "←→", Label: "choose"}, {Key: "⏎", Label: "run"}, {Key: "↑", Label: "back to map"}})
	}
	view := "list"
	if m.edge.mapList {
		view = "map"
	}
	full := []look.Action{{Key: "↑↓←→", Label: "move"}, {Key: "⏎", Label: "open"}, {Key: "tab", Label: "buttons"},
		{Key: "v", Label: view}, {Key: "r", Label: "re-read"}, {Key: "esc", Label: "back"}}
	if bar := lookActionBar(full); lipgloss.Width(bar) <= w {
		return bar
	}
	return lookActionBar([]look.Action{{Key: "↑↓", Label: "move"}, {Key: "⏎", Label: "open"}, {Key: "v", Label: view}, {Key: "esc", Label: "back"}})
}
