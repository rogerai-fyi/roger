package tui

// NEARBY DEVICES (features/edge/empty_edge_simple.feature §7, founder ruling 2026-10-12): a device
// that pings on this network - a phone's advert, a roger with no Edge yet - is drawn as a NEARBY row,
// on the empty state and on the enrolled map alike, and the owner acts on the row:
//
//   i  mints an invite bound to THAT device (its node id derives from its node key, so nothing
//      else can redeem it), shown as the QR and the join line; the row then reads invited · waiting
//   a  starts request-and-approve: the screen waits for the device to ask (roger join <authority>),
//      the matching phrase appears beside its row, y admits, n refuses. A claim-only device (no
//      certificate: a phone) cannot ask, so a grants its claim, as i does.
//
// No address is typed anywhere, and `roger edge adopt` is named nowhere on this screen.

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"rogerai.fm/roger/v6/internal/joininvite"
	"rogerai.fm/roger/v6/internal/look"
	"rogerai.fm/roger/v6/internal/ownerjoin"
	"rogerai.fm/roger/v6/internal/qr"
	"rogerai.fm/roger/v6/internal/store"
)

// edgeNearbyState is what the owner has done from the rows this session.
type edgeNearbyState struct {
	invite   *edgeNearbyInvite // the open invite panel, nil when closed
	invited  map[string]bool   // node ids invited from here (the row reads invited · waiting)
	approve  string            // the node id a primed: the screen waits for that device to ask
	approveN string            // its name, which is how its request is told apart
}

// edgeNearbyInvite is one minted invite, bound to one device.
type edgeNearbyInvite struct {
	id, name string
	line     string
	until    time.Time
}

// edgeNearbyInviteMsg lands EdgeInviteFor.
type edgeNearbyInviteMsg struct {
	n     store.EdgeNode
	line  string
	until time.Time
	err   error
}

// edgeNearbyItems are the rows: every candidate the host hands over, then any device the owner
// already invited or granted that has not checked in yet (the host drops a granted device from the
// candidate list, and its row must not vanish between invite and membership).
func (m model) edgeNearbyItems() []store.EdgeNode {
	out := append([]store.EdgeNode(nil), m.edge.cands...)
	seen := map[string]bool{}
	for _, c := range out {
		seen[c.ID] = true
	}
	if m.hooks.EdgeAdopting != nil {
		for _, a := range m.hooks.EdgeAdopting() {
			if !seen[a.ID] {
				out = append(out, a)
				seen[a.ID] = true
			}
		}
	}
	return out
}

// edgeNearbyInvited reports whether a row reads invited · waiting: invited from here, or holding a
// live claim grant (the host's adopting list), which is the same promise.
func (m model) edgeNearbyInvited(id string) bool {
	if m.edge.nearby.invited[id] {
		return true
	}
	if m.hooks.EdgeAdopting != nil {
		for _, a := range m.hooks.EdgeAdopting() {
			if a.ID == id {
				return true
			}
		}
	}
	return false
}

// edgeNearbySelectedID is the node id the highlight is on, on the map or in the empty state.
func (m model) edgeNearbySelectedID() string {
	if m.edgeMapActive() {
		it, _ := m.edgeSelectedNet()
		if it.cand {
			return it.id
		}
		return ""
	}
	return m.edge.sel
}

// edgeNearbyRequest is the waiting join request from the device a primed, if it has asked.
func (m model) edgeNearbyRequest(name string) (ownerjoin.Request, bool) {
	for _, r := range m.own.reqs {
		if r.Dir == "edge:authority" && r.Name == name {
			return r, true
		}
	}
	return ownerjoin.Request{}, false
}

// edgeNearbyLines draws the NEARBY eyebrow and one row per device, or nothing when nothing pings.
// Every line is at most w wide; the caller clips anyway.
func (m model) edgeNearbyLines(w int, mono bool) []string {
	items := m.edgeNearbyItems()
	if len(items) == 0 {
		return nil
	}
	muted, ink, paper, live := lookStyle(look.Muted), lookStyle(look.Ink), lookStyle(look.Paper), lookStyle(look.Live)
	if mono {
		muted, ink, paper, live = muted.UnsetForeground(), ink.UnsetForeground(), paper.UnsetForeground(), live.UnsetForeground()
	}
	sel := m.edgeNearbySelectedID()
	now := m.edgeNow()
	nameW := 0
	for _, n := range items {
		nameW = max(nameW, len([]rune(edgeNearbyName(n))))
	}
	nameW = min(nameW, 24)
	var out []string
	out = append(out, "  "+muted.Render(look.Eyebrow(look.NearbyEyebrow))+"  "+muted.Render(look.NearbyActions))
	for _, n := range items {
		mark := "  "
		if n.ID == sel {
			mark = live.Render(look.Fold(look.Rail, mono)) + " "
		}
		name := ink.Render(pad(truncVisible(edgeNearbyName(n), nameW), nameW))
		if n.ID == sel {
			name = paper.Bold(true).Render(pad(truncVisible(edgeNearbyName(n), nameW), nameW))
		}
		var facts []string
		if n.Kind != "" {
			facts = append(facts, n.Kind)
		}
		since := time.Duration(0)
		if n.LastSeen > 0 {
			since = now.Sub(time.Unix(n.LastSeen, 0))
			facts = append(facts, look.NearbySeen(since))
		}
		label, short := look.NearbyKey(n.Pin, n.ID)
		facts = append(facts, label+" "+short)
		state := ""
		switch {
		case m.edge.nearby.approve == n.ID:
			if r, ok := m.edgeNearbyRequest(m.edge.nearby.approveN); ok {
				state = paper.Bold(true).Render(r.Ask) + "  " + muted.Render("y approve · n refuse")
			} else {
				state = muted.Render(look.NearbyWaitAsk(edgeNearbyName(n)))
			}
		case m.edgeNearbyInvited(n.ID):
			state = live.Render(look.NearbyInvited)
			if since >= look.NearbyStaleAfter {
				state += "  " + muted.Render(look.NearbyComeBack)
			}
		}
		row := "  " + mark + name + "  " + muted.Render(strings.Join(facts, " · "))
		if lipgloss.Width(row) > w {
			// The fingerprint is what tells two same-named devices apart, so it is never clipped:
			// it moves under the row with the state, and the state falls to its own line if need be.
			out = append(out, "  "+mark+name+"  "+muted.Render(strings.Join(facts[:len(facts)-1], " · ")))
			row = "      " + muted.Render(look.Fold("↳", mono)+" "+facts[len(facts)-1])
		}
		switch {
		case state == "":
		case lipgloss.Width(row)+2+lipgloss.Width(state) <= w:
			row += "  " + state
		default:
			// Too wide for one row: the state goes under the row, never clipped away.
			out = append(out, row)
			row = "      " + muted.Render(look.Fold("↳", mono)) + " " + state
		}
		out = append(out, row)
	}
	return out
}

// edgeNearbyName is what a row calls a device: its name, else its short id.
func edgeNearbyName(n store.EdgeNode) string {
	if strings.TrimSpace(n.Name) != "" && n.Name != n.ID {
		return n.Name
	}
	return look.NearbyShort(strings.TrimPrefix(n.ID, "n_"))
}

// edgeNearbyInviteLines is the invite panel: the heading, what to do with it, the join line in
// full (split at the width, c copies it whole), the minutes left and the QR.
func (m model) edgeNearbyInviteLines(w int, mono bool) []string {
	inv := m.edge.nearby.invite
	muted, ink, paper, live := lookStyle(look.Muted), lookStyle(look.Ink), lookStyle(look.Paper), lookStyle(look.Live)
	if mono {
		muted, ink, paper, live = muted.UnsetForeground(), ink.UnsetForeground(), paper.UnsetForeground(), live.UnsetForeground()
	}
	var out []string
	out = append(out, "  "+live.Render(look.Fold(look.Rail, mono))+" "+paper.Bold(true).Render(look.NearbyInviteHead(inv.name)))
	for _, ln := range wordWrap(look.NearbyInviteNext, max(30, w-4)) {
		out = append(out, "  "+ink.Render(ln))
	}
	for _, row := range splitRunes(joininvite.JoinLine(inv.line), max(20, w-2)) {
		out = append(out, "  "+paper.Render(row))
	}
	left := inv.until.Sub(m.edgeNow())
	if left < 0 {
		left = 0
	}
	out = append(out, "  "+muted.Render("works once, for "+edgeMinutesLeft(left)+" · c copies the line · esc closes"))
	for _, ln := range strings.Split(strings.TrimRight(qr.ForTerminal(inv.line, w, edgeSetupPlain()), "\n"), "\n") {
		out = append(out, "  "+ln)
	}
	return out
}

// onEdgeNearbyInviteKey owns every key while the invite panel is open.
func (m *model) onEdgeNearbyInviteKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "q", "enter", "i":
		m.edge.nearby.invite = nil
	case "c":
		line := m.edge.nearby.invite.line
		m.status = copiedToast("the join line")
		return m, clipboardWrite(joininvite.JoinLine(line))
	}
	return m, nil
}

// edgeNearbyInvite is i on a row: mint the bound invite through the host, off the update loop.
func (m *model) edgeNearbyInvite() tea.Cmd {
	n, ok := m.edgeNearbySelected()
	if !ok {
		m.status = stDim.Render("i invites a NEARBY device - select one first")
		return nil
	}
	if st := m.edge.status; st == nil || !st.SetUp() {
		m.status = stEmber.Render(look.NearbyNoEdgeYet)
		return nil
	}
	if m.hooks.EdgeInviteFor == nil {
		m.status = stEmber.Render("this build cannot invite from here: roger edge invite --for " + n.ID)
		return nil
	}
	mint := m.hooks.EdgeInviteFor
	return func() tea.Msg {
		line, until, err := mint(n)
		return edgeNearbyInviteMsg{n: n, line: line, until: until, err: err}
	}
}

// onEdgeNearbyInviteMsg lands the minted invite: the panel opens, the row reads invited · waiting.
func (m *model) onEdgeNearbyInviteMsg(msg edgeNearbyInviteMsg) {
	if msg.err != nil {
		m.status = stEmber.Render("could not invite " + edgeNearbyName(msg.n) + ": " + msg.err.Error())
		return
	}
	if m.edge.nearby.invited == nil {
		m.edge.nearby.invited = map[string]bool{}
	}
	m.edge.nearby.invited[msg.n.ID] = true
	m.edge.nearby.invite = &edgeNearbyInvite{id: msg.n.ID, name: edgeNearbyName(msg.n), line: msg.line, until: msg.until}
	m.status = stLive.Render("invite for "+edgeNearbyName(msg.n)) + stDim.Render(" · only that device can use it")
	m.refreshEdge()
}

// edgeNearbySelected is the device the highlight is on, among the rows.
func (m model) edgeNearbySelected() (store.EdgeNode, bool) {
	id := m.edgeNearbySelectedID()
	if id == "" {
		return store.EdgeNode{}, false
	}
	for _, n := range m.edgeNearbyItems() {
		if n.ID == id {
			return n, true
		}
	}
	return store.EdgeNode{}, false
}

// edgeNearbyApprove is a on a row. A claim-only device (no certificate) cannot ask: a grants its
// claim, which is what i does for it too, and the one-time code is said. A device that serves a
// certificate asks: the screen waits for its request and names what it runs.
func (m *model) edgeNearbyApprove() {
	n, ok := m.edgeNearbySelected()
	if !ok {
		m.status = stDim.Render("a approves a NEARBY device - select one first")
		return
	}
	if st := m.edge.status; st == nil || !st.SetUp() {
		m.status = stEmber.Render(look.NearbyNoEdgeYet)
		return
	}
	if n.Pin == "" {
		m.edgeNearbyGrant(n)
		return
	}
	addr := ""
	if m.hooks.Owner != nil && m.hooks.Owner.Edge != nil {
		if t, ok := m.hooks.Owner.Edge(); ok {
			addr = t.Addr
		}
	}
	if st := m.edge.status; st != nil && st.AuthorityAddr != "" {
		addr = strings.TrimPrefix(strings.TrimPrefix(st.AuthorityAddr, "http://"), "https://")
	}
	if addr == "" {
		m.status = stEmber.Render(look.NearbyNotServing)
		return
	}
	m.edge.nearby.approve, m.edge.nearby.approveN = n.ID, edgeNearbyName(n)
	m.status = stKey.Render(look.NearbyWaitAsk(edgeNearbyName(n))) + stDim.Render(" · "+look.NearbyRunLine(addr))
}

// edgeNearbyGrant grants a claim-only device its claim, through the same host path as before.
func (m *model) edgeNearbyGrant(n store.EdgeNode) {
	if m.hooks.EdgeAdopt == nil {
		m.status = stEmber.Render("this build cannot approve from here: roger edge approve " + n.ID)
		return
	}
	if err := m.hooks.EdgeAdopt(n.ID, n.Name); err != nil {
		m.status = stEmber.Render("approve " + edgeNearbyName(n) + ": " + err.Error())
		return
	}
	if m.edge.nearby.invited == nil {
		m.edge.nearby.invited = map[string]bool{}
	}
	m.edge.nearby.invited[n.ID] = true
	m.refreshEdge()
	m.status = edgeAdoptedMessage(edgeNearbyName(n), n.Pin) + m.edgeAdoptCodeNote(n.ID, n.Pin)
}

// edgeNearbyDecide is y or n while a device the owner primed has asked: it answers THAT request,
// whatever else is waiting. ok=false when nothing primed is asking, so the general card answers.
func (m model) edgeNearbyDecide(approve bool) (tea.Cmd, bool) {
	if m.edge.nearby.approve == "" || m.hooks.Owner == nil || m.hooks.Owner.Decide == nil {
		return nil, false
	}
	r, ok := m.edgeNearbyRequest(m.edge.nearby.approveN)
	if !ok {
		return nil, false
	}
	h := m.hooks.Owner
	return func() tea.Msg { return ownerDecidedMsg{req: r, approve: approve, err: h.Decide(r.Dir, r.ID, approve)} }, true
}

// edgeNearbyDecided clears the priming once its request was answered.
func (m *model) edgeNearbyDecided(req ownerjoin.Request) {
	if m.edge.nearby.approve != "" && req.Name == m.edge.nearby.approveN {
		m.edge.nearby.approve, m.edge.nearby.approveN = "", ""
	}
}

// edgeNearbyJoined notices, on a snapshot, an invited device that is a member now: it leaves the
// rows and the status says so. Called from refreshEdge, with the fresh rows.
func (m *model) edgeNearbyJoined() {
	if len(m.edge.nearby.invited) == 0 {
		return
	}
	for _, r := range m.edge.rows {
		if m.edge.nearby.invited[r.n.ID] {
			delete(m.edge.nearby.invited, r.n.ID)
			m.status = stLive.Render(edgeNearbyName(r.n)+" joined") + stDim.Render(" · a member of your Edge now")
		}
	}
}
