package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"rogerai.fm/roger/v5/internal/agent"
)

// THE PRIVATE TAB IN [1] TUNE IN.
//
// FOUNDER ASK (2026-08-21): "in the TUNE IN tab there should be a way to switch between
// OPEN MARKET bands and PRIVATE bands that you can tune in."
//
// The problem it fixes: an operator who shares a model on a PRIVATE band is shown its
// frequency code exactly once and told it is never stored again. From that moment their
// own band is invisible to them - /discover skips private nodes with no owner exemption,
// so the band is missing from the very list they browse - and the ONLY documented way back
// in is the code they were told they would not need. The founder's own case: grok-4.6 sits
// in his SHARE on a private band, and he could not reach it.
//
// WHY /bands AND NOT A NEW BROKER ENDPOINT (founder's call). Two routes were on the table:
// teach /discover an owner exemption (a new signed endpoint returning your own hidden
// offers), or list what the broker ALREADY lets you see - /bands, the band metadata the
// BASE STATION roster reads - and reach the model locally. The second was chosen, and it
// turns out to be the better design rather than merely the cheaper one:
//
//   - /bands is the DURABLE identity. A band survives its model going off air; an offer
//     does not. Listing bands means the tab shows a band you minted this morning and have
//     not switched on yet, which an offer-based list structurally cannot.
//   - Resolve is keyed on the HASH of the code. Even an owner-scoped /discover could not
//     hand back a code to route with, so an owner exemption would have listed bands the
//     TUI still could not tune. The relay is not the way home; the local server is.
//   - A band on THIS machine never needed the broker at all. The model is running on the
//     operator's own box - routing their turn out to the broker so it can relay back is a
//     round trip through the network to reach localhost, priced and metered on the way.
//
// So the tab is honest about a split it did not invent: your bands divide into the ones
// running HERE, which open a direct channel, and the ones on another machine, which need
// their code. It says which is which and never offers an action it cannot perform.
//
// THREE THINGS IT MUST NEVER DO:
//  1. Show a frequency code. Only the hash is stored; there is nothing to show, and a
//     placeholder would read as the real thing. The dial LABEL ("147.520 MHz") is cosmetic
//     and safe - it is what /bands already prints in BASE STATION.
//  2. Price a local row. Nothing is metered on your own hardware, and a "$0.00" reads as a
//     measured charge rather than the absence of one.
//  3. Make a private band look like a market listing. The tab keeps the accent red and the
//     ◉ on-air mark the PRIVATE FREQ header uses, so leaving the open market is never
//     ambiguous.

// tuneTab is which half of [1] TUNE IN the operator is looking at.
type tuneTab int

const (
	tabOpenMarket tuneTab = iota // the public dial: every band on /discover
	tabPrivate                   // your own bands, from /bands
)

// privRow is one band you own, joined to the model behind it.
//
// The join is what makes the row useful: /bands knows a node id, the share table knows
// which models run here, and only together do they answer "can I actually use this?".
type privRow struct {
	band  BandRow
	model string // the local model behind it ("" = the band is on another machine)
	chat  string // local chat-completions URL, when the model is served here
	key   string // bearer for a key-protected local server
	onAir bool   // that model is registered on a private band RIGHT NOW
}

// reachable reports whether Enter can do anything. A band is reachable when it is live
// AND its model is served by a local server we know the URL of - the direct route.
func (r privRow) reachable() bool {
	return r.band.Status == "active" && r.chat != ""
}

// privRows builds the PRIVATE tab's list.
//
// Every band the account owns is listed, including the ones on other machines and the
// revoked ones: the tab is the operator's inventory, and hiding a band they cannot reach
// here would recreate the invisibility this whole screen exists to fix. What varies is
// what each row OFFERS.
func (m model) privRows() []privRow {
	station := m.ctrl.Station()
	// Index the share table by node id once: a band resolves by comparing against
	// agent.ShareNodeID per row, never by splitting the node id on "-" (a station callsign
	// can contain hyphens, so a split is a guess - and a wrong guess would show someone
	// the wrong model as the thing behind their band).
	byNode := map[string]shareRow{}
	for _, r := range m.shareRows {
		byNode[agent.ShareNodeID(station, r.model, 0)] = r
	}
	out := make([]privRow, 0, len(m.rcBands))
	for _, bd := range m.rcBands {
		row := privRow{band: bd}
		if sr, ok := byNode[bd.NodeID]; ok {
			row.model, row.chat, row.key = sr.model, sr.upstream, sr.upstreamKey
			row.onAir = m.sharePrivate[sr.model]
		}
		out = append(out, row)
	}
	return out
}

// privSelected is the row under the private cursor.
func (m model) privSelected() (privRow, bool) {
	rows := m.privRows()
	if m.privCursor < 0 || m.privCursor >= len(rows) {
		return privRow{}, false
	}
	return rows[m.privCursor], true
}

// enterPrivateTab switches [1] TUNE IN onto the PRIVATE half and refreshes the band list.
//
// The refresh is not optional. The list is the point of the screen, and a stale one would
// show a band the operator revoked minutes ago as still theirs to tune.
func (m model) enterPrivateTab() (tea.Model, tea.Cmd) {
	m.tuneTab = tabPrivate
	m.privCursor = 0
	if !m.loggedInState() {
		// A band is an account-scoped resource: there is no anonymous /bands. Say that
		// rather than showing an empty list, which would read as "you have none".
		m.status = stEmber.Render("your bands live on your account - ") + stKey.Render("type /login") +
			stDim.Render(" to see them")
		return m, nil
	}
	m.status = stDim.Render("your private bands · ") + stKey.Render("t") + stDim.Render(" back to the open market")
	return m, m.fetchRemoteRoster()
}

// leavePrivateTab returns to the open market.
func (m model) leavePrivateTab() (tea.Model, tea.Cmd) {
	m.tuneTab = tabOpenMarket
	m.status = stDim.Render("open market · ") + stKey.Render("t") + stDim.Render(" for your private bands")
	return m, nil
}

// onPrivateTabKey handles [1] TUNE IN while the PRIVATE half is showing. It deliberately
// keeps the open market's movement keys (↑↓/jk, enter) so switching tabs does not switch
// vocabularies mid-screen.
func (m model) onPrivateTabKey(k tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	rows := m.privRows()
	switch k.String() {
	case "t", "T", "esc":
		mm, cmd := m.leavePrivateTab()
		return mm, cmd, true
	case "up", "k":
		if m.privCursor > 0 {
			m.privCursor--
		}
		return m, nil, true
	case "down", "j":
		if m.privCursor < len(rows)-1 {
			m.privCursor++
		}
		return m, nil, true
	case "r":
		m.status = stDim.Render("refreshing your bands…")
		return m, m.fetchRemoteRoster(), true
	case "enter":
		return m.tuneInPrivate()
	}
	// Everything else either belongs to the whole TUI or belongs to the open market.
	// The global keys fall through unchanged so the tab never becomes a trap; the
	// market-only keys (sort, filter, inspect, connect/disconnect) are SWALLOWED, because
	// silently applying them to a list they were not written for is worse than a no-op -
	// [d] would drop a channel the operator cannot see from here, and [f] would open a
	// filter that narrows a list this view does not render.
	switch k.String() {
	case "q", "w", "z", "/", ":", "?", "~", "p", "P", "v", "V", "0", "1", "2", "3", "l", "L":
		return m, nil, false
	}
	return m, nil, true
}

// tuneInPrivate opens a DIRECT channel on the band under the cursor, or explains exactly
// why it cannot - never a dead Enter, and never a silent one.
func (m model) tuneInPrivate() (tea.Model, tea.Cmd, bool) {
	r, ok := m.privSelected()
	if !ok {
		return m, nil, true
	}
	switch {
	case r.band.Status != "active":
		m.status = stEmber.Render("this band is revoked - its code is burnt. Press ") +
			stKey.Render("h") + stEmber.Render(" on the model in [2] SHARE for a fresh one")
		return m, nil, true
	case r.model == "":
		// The band points at another machine. We hold the hash of its code, never the
		// code, so there is genuinely nothing here to tune WITH - say so and name the key
		// that can, rather than opening something that would fail on the first turn.
		m.status = stDim.Render("this band is on ") + stKey.Render(r.band.NodeID) +
			stDim.Render(" - not this machine. Tune it with its code: ") + stKey.Render("~")
		return m, nil, true
	case r.chat == "":
		// Known model, unknown endpoint: the share row exists but carries no upstream (the
		// server it was detected on is gone). Naming the server as the thing to fix is the
		// remedy; sending them to the broker would not help.
		m.status = stEmber.Render("no local server is serving ") + stKey.Render(r.model) +
			stEmber.Render(" right now - start it and press ") + stKey.Render("r")
		return m, nil, true
	}
	return m.openLocalChannel(r), nil, true
}

// openLocalChannel binds the CHANNEL to a model on this machine, bypassing the broker.
//
// The synthesized offer is not a fake market listing - it is this node, named by the same
// agent.ShareNodeID the share path registers - but it carries NO price, NO tier and NO
// FreeNow: the channel header keys off localChat and prints the direct-route line instead
// of a cost, because there is no cost to print rather than a cost that happens to be zero.
func (m model) openLocalChannel(r privRow) model {
	node := agent.ShareNodeID(m.ctrl.Station(), r.model, 0)
	m.connected = &offer{NodeID: node, Model: r.model, Online: true}
	m.chatLocalChat, m.chatLocalKey = r.chat, r.key
	m.transcript = nil
	m.sessCost = 0
	m.sessTokensIn, m.sessTokensOut = 0, 0
	m.lastReply = ""
	m.mode = modeChat
	m.chatIn.Focus()
	m.status = stRed.Render(glyphOnAir+" PRIVATE BAND ") + stDim.Render(bandDial(r.band)) +
		stDim.Render(" · direct to ") + stKey.Render(r.model) + stDim.Render(" on this machine")
	return m
}

// bandDial is the cosmetic dial label for a band ("147.520 MHz"), the SAME string /bands
// already prints in BASE STATION. It is not the frequency code and cannot be tuned with -
// the code exists only as a hash. Falls back to the band id so the column is never blank.
func bandDial(bd BandRow) string {
	if s := strings.TrimSpace(bd.Display); s != "" {
		return s
	}
	return bd.ID
}

// privateTabView renders the PRIVATE half of [1] TUNE IN.
func (m model) privateTabView(w int) string {
	var b strings.Builder
	line := func(s string) { b.WriteString("  " + truncVisible(s, w-2) + "\n") }

	rows := m.privRows()
	head := "  " + stRed.Render("▌") + " " + stBrand.Render("YOUR BANDS") +
		stDim.Render("   "+plural(len(rows), "band")) +
		stDim.Render(" · ") + stRed.Render(glyphOnAir+" PRIVATE") +
		stDim.Render(" · ") + stKey.Render("t") + stDim.Render(" open market")
	b.WriteString(truncVisible(head, w) + "\n")
	// The one-line contract for the whole tab, in the same voice BASE STATION uses. It
	// states the privacy property we actually have (hidden from the market) and does not
	// claim one we do not (a local turn is direct; a relayed one is not end-to-end
	// encrypted) - the reachable rows below are the ones that never touch the broker.
	line(stDim.Render("hidden from the open market · nobody can tune one without its code"))
	b.WriteString("\n")

	if !m.loggedInState() {
		line(stDim.Render("your bands live on your account - ") + stKey.Render("type /login") + stDim.Render(" to see them"))
		return b.String()
	}
	if m.rcErr != "" {
		line(stEmber.Render("could not read your bands: ") + stDim.Render(m.rcErr))
		line(stDim.Render("press ") + stKey.Render("r") + stDim.Render(" to retry"))
		return b.String()
	}
	if len(rows) == 0 {
		// An empty inventory is a real state, not a failure: name the action that creates
		// a band rather than leaving a blank screen.
		line(stDim.Render("no private bands yet - press ") + stKey.Render("[2]") +
			stDim.Render(" SHARE, then ") + stKey.Render("h") + stDim.Render(" on a model to mint one"))
		return b.String()
	}

	for i, r := range rows {
		sel := i == m.privCursor
		b.WriteString("  " + truncVisible(m.privRowLine(r, sel), w-2) + "\n")
	}
	// NO in-view key line. The footer (Zone 4) is the ONE place keys are taught - the same
	// de-crowd rule the CHANNEL follows - and this tab has its own footer case that teaches
	// exactly these keys. Printing them twice costs a row and reads as two sources of truth.
	return b.String()
}

// privRowLine renders one band row: the dial, the model behind it, and - the load-bearing
// column - what this machine can DO with it.
func (m model) privRowLine(r privRow, sel bool) string {
	dial := pad(bandDial(r.band), 16)
	name := r.model
	if name == "" {
		name = r.band.NodeID // elsewhere: the node id whole, never split on "-"
	}
	name = pad(name, 26)
	if sel {
		return stSelText.Render(" ▸ " + dial + "  " + name + "  " + privReachPlain(r))
	}
	return stDim.Render("   "+dial) + "  " + stKey.Render(name) + "  " + m.privReach(r)
}

// privReach is the honest one-phrase verdict for a row. Each case names the route or the
// obstacle; none of them overstates what the operator can do from here.
func (m model) privReach(r privRow) string {
	switch {
	case r.band.Status != "active":
		return stDim.Render("revoked · code burnt")
	case r.chat != "" && r.onAir:
		return stLive.Render(glyphOnAir+" here") + stDim.Render(" · direct, nothing metered")
	case r.chat != "":
		// The band is bound to a model this machine serves, but that model is not
		// currently registered private. The channel still works (it is a direct call to
		// the local server), so this is a note, not a refusal.
		return stLive.Render("here") + stDim.Render(" · direct · not on air")
	case r.model != "":
		return stDim.Render("here · no server running")
	default:
		return stDim.Render("another machine · needs its code")
	}
}

// privReachPlain is the selected row's verdict with the styling stripped: the cursor row is
// reverse-video and one accent governs it, so nested colours would fight the highlight.
func privReachPlain(r privRow) string {
	switch {
	case r.band.Status != "active":
		return "revoked · code burnt"
	case r.chat != "" && r.onAir:
		return glyphOnAir + " here · direct, nothing metered"
	case r.chat != "":
		return "here · direct · not on air"
	case r.model != "":
		return "here · no server running"
	default:
		return "another machine · needs its code"
	}
}
