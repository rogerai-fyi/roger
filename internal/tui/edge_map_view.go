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

// sub returns the one-line description under a node's name.
func (it edgeNetItem) sub() string {
	switch {
	case it.machine:
		return it.role
	case it.cand:
		return "adopt with a"
	case it.member && it.dark:
		return "member · offline"
	case it.member:
		return "member"
	case it.dark:
		return "offline"
	case it.agent && it.job != "":
		return it.job
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
	// The OTHER machines already on this Edge - members, drawn as their own cards in a fleet band.
	for i := range m.edge.rows {
		n := m.edge.rows[i].n
		items = append(items, edgeNetItem{name: n.Name, member: true, node: &m.edge.rows[i].n, id: n.ID, kind: n.Kind, dark: edge.Presence(n.Presence) == edge.PresenceDark})
	}
	// The DISCOVERED machines not yet on this Edge - candidates, adopt with a.
	for i := range m.edge.cands {
		n := m.edge.cands[i]
		items = append(items, edgeNetItem{name: n.Name, cand: true, node: &m.edge.cands[i], id: n.ID, kind: n.Kind})
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
	for i, it := range items {
		if it.self {
			return i
		}
	}
	return 0
}

const edgeNetCardW = 24 // card width (border included); wide enough for "serving <model>" at a glance

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
		m.edgeCardBand(cw, trunk, hubIt, hubIx, sel, line)
	}

	// The other machines already on this Edge - the fleet band, a heading then centred cards.
	if len(memIt) > 0 {
		line("")
		line("  " + edgeSetupCenter(stKey.Render("ON THE EDGE")+stDim.Render("  other machines you have adopted"), cw))
		m.edgeCardBand(cw, -1, memIt, memIx, sel, line)
	}

	// The discovered machines not yet adopted - the adopt band. No wire: they are not on the Edge
	// until the owner adopts one. This is the UniFi moment - a phone on the LAN shows up here.
	if len(candIt) > 0 {
		line("")
		line("  " + edgeSetupCenter(stKey.Render("DISCOVERED")+stDim.Render("  on your network, not yet on your Edge · press a to adopt"), cw))
		m.edgeCardBand(cw, -1, candIt, candIx, sel, line)
	}
}

// edgeCardBand draws a set of node cards packed into centred rows. When trunk >= 0 each row is
// tied to the hub with a branch connector; when trunk < 0 the cards stand on their own (a fleet or
// discovered band, already separated by a heading). idx[j] is card j's global selection index.
func (m model) edgeCardBand(cw, trunk int, its []edgeNetItem, idx []int, sel int, line func(string)) {
	perRow := max(1, (cw+2)/(edgeNetCardW+2))
	for i := 0; i < len(its); i += perRow {
		end := min(i+perRow, len(its))
		row := its[i:end]
		groupW := len(row)*edgeNetCardW + (len(row)-1)*2
		start := (cw - groupW) / 2
		if start < 0 {
			start = 0
		}
		if trunk >= 0 {
			centers := make([]int, len(row))
			for j := range row {
				centers[j] = start + j*(edgeNetCardW+2) + edgeNetCardW/2
			}
			line("  " + edgeBranchLine(cw, trunk, centers))
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
	if !drop[trunk] {
		set(trunk, "┴")
	} else {
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
		sub = lampStyle(roleLive).Render(edgeTrunc("‹ this one", avail))
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
		kind = "member machine · offline"
	case it.member:
		kind = "member machine"
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
		label("KIND", n.Kind)
		if len(n.Caps) == 0 {
			label("CAN DO", stDim.Render("none declared"))
		}
		for i, c := range n.Caps {
			k := ""
			if i == 0 {
				k = "CAN DO"
			}
			label(k, pad(c.Name, 10)+stDim.Render(c.State))
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
