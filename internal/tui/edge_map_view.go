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
	cand    bool             // a device pinging nearby, not yet on this Edge (i invite · a approve)
	node    *store.EdgeNode  // the fleet/candidate node record, for member and candidate items
	id      string           // the node id, for adopt/detail of a member or candidate
	kind    string           // host | board | mobile, for a member/candidate emblem
	alerts  []string         // the alert rules firing for this node right now, by wording
	// via is the MEMBER this node is reached THROUGH, and hops is how far from the authority it is:
	// 1 for a machine this one reaches itself, 2 through one relay, 3 through a relay's relay
	// (edge.Arrange). The wire says a relay is in the path; these say which one and how many.
	via  string
	hops int
	at   time.Time // the screen's clock, for the state word's age
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
		s = it.stateShort() + " · " + tag
	default:
		s += " · " + tag
	}
	return s
}

// stateShort is the shared state word and its age, for a member card (edge.UnitWord).
func (it edgeNetItem) stateShort() string {
	if it.node == nil {
		if it.dark {
			return edge.WordDark // no record to age it by, and still dark
		}
		return ""
	}
	w, line := edge.UnitWord(*it.node, len(it.alerts), it.at)
	if i := strings.Index(line, " · "); i > 0 && w == edge.WordHeard {
		return line[:i] // the card keeps the word and age; the page names the missing channel
	}
	return line
}

func (it edgeNetItem) subBase() string {
	switch {
	case it.machine:
		// "YOU ARE HERE" and not "THIS MACHINE": the readout below uses THIS MACHINE as a fact label,
		// and two lines with the same words is exactly how this screen got confusing.
		return "YOU ARE HERE · " + it.role
	case it.cand:
		return it.kindWord() + " · nearby · i invite · a approve"
	case it.member:
		// WHAT IT IS and HOW IT IS, with its age (features/edge/fleet_window.feature): the kind, the
		// hardware when it fits, the relay it is reached through, and the shared state word - never
		// only "on your Edge". The card is narrow, so the hardware gives way first.
		state := it.stateShort()
		base := it.kindWord()
		if it.via != "" {
			// WHICH relay, not just that there is one: the wire says a path goes through something.
			base += " · via " + it.via
		} else if hw := it.hardware(); hw != "" && lipgloss.Width(base+" · "+hw+" · "+state) <= edgeRailCardText {
			base += " · " + hw
		}
		return base + " · " + state
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
	// What every unit is called: its owner's name, or what it IS (edge.UnitName), never an id.
	names := edge.UnitNames(append(append([]store.EdgeNode(nil), fleet...), m.edge.cands...))
	for _, r := range edge.Arrange(fleet) {
		n := r.Node
		if n.ID == selfID && selfID != "" {
			continue // the machine you are at is the hub, not also one of the others
		}
		items = append(items, edgeNetItem{name: names[n.ID], member: true, node: byID[n.ID], id: n.ID, kind: n.Kind,
			dark: edge.Presence(n.Presence) == edge.PresenceDark, alerts: m.edge.alerts[n.ID],
			via: r.Via, hops: r.Depth, at: m.edge.at})
	}
	// The devices pinging NEARBY, not yet on this Edge - i invites one, a approves it.
	for i := range m.edge.cands {
		n := m.edge.cands[i]
		items = append(items, edgeNetItem{name: names[n.ID], cand: true, node: &m.edge.cands[i], id: n.ID, kind: n.Kind})
	}
	return items
}

// edgeMapActive reports whether the navigable network map (not the multi-node graph) is on screen.
// The map is the home view of a SET-UP machine: it draws this machine's hub, the rogers on it, the
// other member machines on the Edge, and the devices pinging NEARBY, not on it yet - so a phone
// that appears on the LAN shows up here to invite or approve (2026-09-21, 2026-10-12). Past edgeMaxGraphNodes the
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

// edgeNetKey names a map item across re-reads: a device or member by its node id, anything else
// by what it is and its name.
func edgeNetKey(it edgeNetItem) string {
	switch {
	case it.id != "":
		return "id " + it.id
	case it.machine:
		return "machine"
	case it.agent:
		return "agent " + it.name
	}
	return "roger " + it.name
}

// edgeHeldNet is the map highlight a re-read must keep on the same item: one the owner moved to,
// or a nearby device the default put it on. The highlight is on a device, not a slot, so a device
// appearing above it, or the highlighted one leaving, never moves i or a onto another device.
func (m model) edgeHeldNet() (string, bool) {
	if m.edge.status == nil || !m.edgeMapActive() {
		return "", false
	}
	it, _ := m.edgeSelectedNet()
	if !m.edge.netSelSet && !it.cand {
		return "", false
	}
	return edgeNetKey(it), true
}

// edgeHoldNet puts the highlight back on the item named key, or on this roger (else the machine)
// when that item has gone.
func (m *model) edgeHoldNet(key string) {
	items := m.edgeNetItems()
	at := -1
	for i, it := range items {
		if edgeNetKey(it) == key {
			at = i
			break
		}
	}
	if at < 0 {
		at = 0
		for i, it := range items {
			if it.self {
				at = i
				break
			}
		}
	}
	m.edge.netSel, m.edge.netSelSet = at, true
}

// edgeDefaultNetSel is the highlight before the owner moves: THIS roger if one is running here,
// else the machine. It puts the commonest actions (Make Agent, Rename) under the cursor at once.
func edgeDefaultNetSel(items []edgeNetItem) int {
	// A DEVICE PINGING NEARBY wins: it is why the owner came to this screen, and landing on it puts
	// "i invite · a approve" one keystroke away instead of behind several arrow presses. Nothing is waiting
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
			wrap("JOIN", "another machine runs  "+st.JoinAgainstLine())
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
			label("STATUS", stEmber.Render("not on your Edge")+stDim.Render(" · press ")+lampStyle(roleLive).Render("i")+stDim.Render(" to invite it, ")+lampStyle(roleLive).Render("a")+stDim.Render(" to approve it"))
		} else {
			// The shared state word with its age, and the missing channel named when it is heard but
			// cannot be controlled (features/edge/fleet_window.feature).
			_, pres := edge.UnitWord(*n, len(it.alerts), m.edge.at)
			if it.dark {
				pres += " · its last heartbeat aged out; its values below are as old as that"
			} else if !edge.Controllable(*n) {
				pres += " - actions that need its face (serve model, maintenance, ask) cannot reach it"
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
		if ml := edge.ModelLineFor(*n); ml != "" {
			label("MODEL", ml)
		}
		// A phone's own vitals, as it signed them - absent readings left out, never zeros.
		if pl := edge.PhoneLine(n.Phone); pl != "" {
			label("PHONE", pl+stDim.Render("  · "+edgeAge(m.edge.at.Sub(time.Unix(n.Phone.ReadAt, 0)))+" ago"))
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
		members := make([]store.EdgeNode, 0, len(m.edge.rows))
		for _, r := range m.edge.rows {
			members = append(members, r.n)
		}
		for i, ev := range edgeLastEvents(n.History, 3) {
			k := ""
			if i == 0 {
				k = "LATELY"
			}
			// Who asked is said by name; a caller renamed since, by its name now too.
			label(k, pad(edgeAge(m.edge.at.Sub(time.Unix(ev.At, 0)))+" ago", 10)+stDim.Render(pad(ev.What, 12)+edge.ActorDetail(ev, members)))
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
