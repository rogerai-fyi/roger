package tui

// owner_join.go is the owner's side of joining in the TUI (features/join/approve.feature,
// invite.feature, surfaces.feature, tower_identity.feature): the Towers this user serves and
// their fingerprints, a waiting join request answered with y or n, a one-paste invite with its QR
// that says who joined in place, and the Towers this machine joined and the keys they are pinned
// to. The host fills Hooks.Owner from the same code the CLI uses (internal/ownerjoin), and the
// sentences come from ownerjoin, so the CLI, this screen and the console say the same thing.
//
// The request card is shown only where y and n are never typing - [3] EDGE and the band dial -
// and a one-line notice everywhere else.

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"rogerai.fm/roger/v7/internal/ownerjoin"
	"rogerai.fm/roger/v7/internal/qr"
	towerpkg "rogerai.fm/roger/v7/internal/tower"
)

// ownerPollEvery is how often the owner's view is refreshed while roger runs.
var ownerPollEvery = 2 * time.Second

// OwnerJoinRefresh asks the TUI to refresh the owner's view now (the poll loop sends it).
type OwnerJoinRefresh struct{}

type ownerPolledMsg struct {
	towers  []ownerjoin.Tower
	reqs    []ownerjoin.Request
	pinned  []ownerjoin.Pinned
	outcome string // the open invite's outcome, once it is over
	over    bool
}

type ownerDecidedMsg struct {
	req     ownerjoin.Request
	approve bool
	err     error
}

type ownerInviteMsg struct {
	inv ownerjoin.Invite
	err error
}

// ownerJoin is the owner's view as last read.
type ownerJoin struct {
	towers  []ownerjoin.Tower
	reqs    []ownerjoin.Request
	pinned  []ownerjoin.Pinned
	invite  *ownerjoin.Invite // the open invite panel, nil when closed
	outcome string            // who joined with it, or that it ran out unused
	next    int               // the Tower the next invite is for, when several are served
	polling bool
}

// ownerJoinCmd is the first refresh, when the host wired the owner's operations.
func (m model) ownerJoinCmd() tea.Cmd {
	if m.hooks.Owner == nil {
		return nil
	}
	return func() tea.Msg { return OwnerJoinRefresh{} }
}

// ownerPoll reads the owner's view once, off the update loop.
func (m model) ownerPoll() tea.Cmd {
	h := m.hooks.Owner
	inv := m.own.invite
	return func() tea.Msg {
		var got ownerPolledMsg
		if h.Towers != nil {
			got.towers = h.Towers()
		}
		if h.Requests != nil {
			for _, t := range h.Answering(got.towers) {
				reqs, err := h.Requests(t)
				if err != nil {
					continue // a Tower that stopped between the two reads
				}
				for _, r := range reqs {
					got.reqs = append(got.reqs, ownerjoin.RequestFor(t, r))
				}
			}
		}
		if h.Pinned != nil {
			got.pinned = h.Pinned()
		}
		if inv != nil && h.Outcome != nil {
			got.outcome, got.over = h.Outcome(inv.Dir, inv.ID)
		}
		return got
	}
}

// onOwnerJoinMsg handles the owner's view's messages; ok=false for any other message.
func (m model) onOwnerJoinMsg(msg tea.Msg) (model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case OwnerJoinRefresh:
		if m.hooks.Owner == nil || m.own.polling {
			return m, nil, true
		}
		m.own.polling = true
		return m, m.ownerPoll(), true
	case ownerPolledMsg:
		m.own.polling = false
		m.own.towers, m.own.reqs, m.own.pinned = msg.towers, msg.reqs, msg.pinned
		if m.own.invite != nil && msg.over && m.own.outcome == "" {
			m.own.outcome = msg.outcome
		}
		return m, tea.Tick(ownerPollEvery, func(time.Time) tea.Msg { return OwnerJoinRefresh{} }), true
	case ownerDecidedMsg:
		switch {
		case msg.err != nil:
			m.status = stEmber.Render("could not answer " + msg.req.Name + ": " + msg.err.Error())
		case msg.approve:
			m.status = stLive.Render("✓ ") + stKey.Render(msg.req.Name+" can join "+msg.req.Tower)
		default:
			m.status = stDim.Render("refused " + msg.req.Name)
		}
		m.own.reqs = dropRequest(m.own.reqs, msg.req.ID)
		if msg.err == nil {
			m.edgeNearbyDecided(msg.req)
			if m.mode == modeEdge {
				m.refreshEdge() // an admitted device is a member now, and leaves the NEARBY rows
			}
		}
		return m, m.ownerJoinCmd(), true
	case ownerInviteMsg:
		if msg.err != nil {
			m.status = stEmber.Render("could not make an invite: " + msg.err.Error())
			return m, nil, true
		}
		inv := msg.inv
		m.own.invite, m.own.outcome = &inv, ""
		return m, nil, true
	}
	return m, nil, false
}

func dropRequest(reqs []ownerjoin.Request, id string) []ownerjoin.Request {
	out := reqs[:0:0]
	for _, r := range reqs {
		if r.ID != id {
			out = append(out, r)
		}
	}
	return out
}

// ownerAnswersHere is whether this screen shows the request card (y and n are never typing here).
func (m model) ownerAnswersHere() bool {
	return m.mode == modeEdge || m.mode == modeBrowse
}

// onOwnerJoinKey takes the keys the owner's view owns: the invite panel while open, y and n for a
// waiting request where the card is shown, and the invite key on [3] EDGE.
func (m model) onOwnerJoinKey(k tea.KeyMsg) (model, tea.Cmd, bool) {
	if m.hooks.Owner == nil {
		return m, nil, false
	}
	key := k.String()
	if m.own.invite != nil && m.mode == modeEdge {
		switch key {
		case "esc", "q", "enter":
			m.own.invite, m.own.outcome = nil, ""
		case "c":
			m.status = copiedToast("the join line")
			return m, clipboardWrite(m.own.invite.Line), true
		}
		return m, nil, true // the panel owns every key while it is open
	}
	if m.mode == modeEdge && (key == "y" || key == "n") {
		// A device the owner primed with a (NEARBY, §7) is answered by name, whatever else waits.
		if cmd, ok := m.edgeNearbyDecide(key == "y"); ok {
			return m, cmd, true
		}
	}
	if len(m.own.reqs) > 0 && m.ownerAnswersHere() && (key == "y" || key == "n") {
		r, h := m.own.reqs[0], m.hooks.Owner
		approve := key == "y"
		if h.Decide == nil {
			return m, nil, true
		}
		return m, func() tea.Msg { return ownerDecidedMsg{req: r, approve: approve, err: h.Decide(r.Dir, r.ID, approve)} }, true
	}
	// The invite key on [3] EDGE. "i" and "+" are already bound there (open a node, add one), so
	// the invite is the capital I.
	if key == "I" && m.mode == modeEdge && len(m.own.towers) > 0 && m.hooks.Owner.Invite != nil {
		t := m.own.towers[m.own.next%len(m.own.towers)]
		m.own.next++
		h := m.hooks.Owner
		return m, func() tea.Msg { inv, err := h.Invite(t); return ownerInviteMsg{inv: inv, err: err} }, true
	}
	return m, nil, false
}

// ownerJoinBanner is what the owner's view adds under the header: the request card on [3] EDGE
// and the dial, a one-line notice elsewhere.
func (m model) ownerJoinBanner(w int) string {
	if len(m.own.reqs) == 0 {
		return ""
	}
	if !m.ownerAnswersHere() {
		return "  " + stGold.Render(ownerjoin.Notice(m.own.reqs)) + "\n"
	}
	r := m.own.reqs[0]
	body := stKey.Render(r.Card()) + "\n  " + stDim.Render(r.Hint)
	if more := len(m.own.reqs) - 1; more > 0 {
		body += "\n  " + stDim.Render("and "+plural(more, "more request")+" after this one")
	}
	return panelFit(body, w) + "\n"
}

// ownerEdgeView is the owner's part of [3] EDGE: the invite panel while open, else the Towers this
// user serves (with fingerprints and the invite key) and the Towers this machine joined.
func (m model) ownerEdgeView(w int) string {
	if inv := m.own.invite; inv != nil {
		// The join line is shown in full, split into rows at the screen width (rows are clipped,
		// not wrapped, so one long row would be cut off). c copies it whole.
		var b strings.Builder
		b.WriteString(panelFit(stKey.Render(inv.Heading)+"\n"+stDim.Render(inv.Next), w) + "\n")
		for _, row := range splitRunes(inv.Line, max(20, w)) {
			b.WriteString(row + "\n")
		}
		b.WriteString("  " + stDim.Render("c copies the line · esc closes") + "\n")
		if m.own.outcome != "" {
			b.WriteString("  " + stLive.Render(m.own.outcome) + "\n")
			return b.String()
		}
		b.WriteString("  " + stDim.Render(towerpkg.MinutesLeft(inv.ExpiresAt, time.Now())) + "\n")
		b.WriteString(qr.ForTerminal(inv.Line, w, edgeSetupPlain()))
		return b.String()
	}
	if len(m.own.towers) == 0 && len(m.own.pinned) == 0 {
		return ""
	}
	var lines []string
	if len(m.own.towers) > 0 {
		lines = append(lines, stKey.Render("YOUR TOWERS"))
		for _, t := range m.own.towers {
			lines = append(lines, "  "+ownerjoin.TowerLine(t))
		}
		lines = append(lines, "  "+stDim.Render("I invites a device"))
	}
	if len(m.own.pinned) > 0 {
		lines = append(lines, stKey.Render("TOWERS YOU JOINED"))
		for _, p := range m.own.pinned {
			lines = append(lines, "  "+ownerjoin.PinnedLine(p))
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

// splitRunes cuts s into rows of at most n runes.
func splitRunes(s string, n int) []string {
	r := []rune(s)
	var out []string
	for len(r) > n {
		out = append(out, string(r[:n]))
		r = r[n:]
	}
	return append(out, string(r))
}
