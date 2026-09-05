package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"rogerai.fm/roger/v6/internal/agent"
	"rogerai.fm/roger/v6/internal/node"
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
	model string // the local model behind it ("" = no share row matched)
	chat  string // local chat-completions URL, when the model is served here
	key   string // bearer for a key-protected local server
	onAir bool   // that model is registered on a private band RIGHT NOW
	// here reports that the band's node id belongs to THIS STATION, even when no share row
	// matched it. The two are not the same fact, and conflating them was a bug the founder
	// hit immediately: a band on eager-puma-54 read "another machine · needs its code"
	// purely because its model server was not running at that moment. The remedy for a
	// stopped server ("start it") is nothing like the remedy for a remote band ("find the
	// code"), so the two cases must never share a message.
	here bool
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
	// The station PREFIX is a separate, weaker test that still answers a different
	// question. agent.ShareNodeID builds "<slugified station>-<slugified model>", so
	// matching the slugified station plus its separator is a prefix test against a KNOWN
	// string - not the guess-where-the-boundary-is that splitting on "-" would be.
	//
	// Two machines CAN share a callsign, so this can be wrong. The blast radius is bounded
	// on purpose: `here` only ever changes the WORDING. Offering a direct channel still
	// requires a resolved share row with a real upstream, so a false `here` can never route
	// a turn anywhere.
	prefix := agent.ShareNodeID(station, "", 0) + "-"
	out := make([]privRow, 0, len(m.rcBands))
	for _, bd := range m.rcBands {
		row := privRow{band: bd, here: strings.HasPrefix(bd.NodeID, prefix)}
		if sr, ok := byNode[bd.NodeID]; ok {
			row.model, row.chat, row.key = sr.model, sr.upstream, sr.upstreamKey
			row.onAir = m.sharePrivate[sr.model]
			row.here = true
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
		return m, m.rescanPrivate(), true
	case "a", " ", "space":
		// ON AIR / OFF AIR, right here (founder 2026-08-21: "i want it to be easy to use my
		// own bands, basically just as simple as we are able to share ... i want to do the
		// same with the private bands"). SHARE toggles a row with a/space; this is the same
		// key on the same controller call, so the two screens cannot grow two behaviours.
		//
		// It routes through ToggleOnAir, NOT TogglePrivate: TogglePrivate flips VISIBILITY,
		// so pressing it on a model that is already private would put it on the OPEN
		// MARKET - the last thing an operator wants from a screen called PRIVATE. Off air
		// here means off air; the band and its privacy are remembered.
		return m.toggleBandOnAir()
	case "b", "B":
		// THE BAND CARD for this band's model - the same key every other list uses.
		r, ok := m.privSelected()
		if !ok || r.model == "" {
			m.status = stDim.Render("that band's model is not on this machine - nothing to configure here")
			return m, nil, true
		}
		mm, cmd := m.openBandConfig(r.model, modeBrowse)
		return mm, cmd, true
	case "n", "N":
		// n = a NEW CODE for the band under the cursor, in place. The founder's "reset the
		// key": the band keeps its dial, model, label and slot; only the secret changes.
		r, ok := m.privSelected()
		if !ok {
			return m, nil, true
		}
		if r.band.Status != "active" {
			m.status = stEmber.Render("a revoked band cannot be rotated - its code is burnt")
			return m, nil, true
		}
		return m.openBandRotateConfirm(r.band), nil, true
	case "f", "F":
		// f = FORGET a revoked row. The founder's "i also don't see a way to delete them":
		// revoking left a row nothing could remove, so dead entries piled up around the
		// live band. Only ever offered on a band that is already dead.
		r, ok := m.privSelected()
		if !ok {
			return m, nil, true
		}
		if r.band.Status == "active" {
			m.status = stEmber.Render("only a revoked band can be forgotten - revoke it first in BASE STATION [") +
				stKey.Render("p") + stEmber.Render("]")
			return m, nil, true
		}
		m.status = stDim.Render("forgetting ") + stKey.Render(bandDial(r.band)) + stDim.Render("…")
		return m, m.forgetBand(r.band.ID), true
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

// rescanPrivate refreshes BOTH things a private band's row depends on: the band roster
// (what you own, from the broker) and the LOCAL DETECTION SCAN (which models this machine
// is serving right now).
//
// The second half is the one that was missing, and its absence made the whole message
// wrong. When a row says "no local server is serving <model> - start it, then press r",
// the thing that must be re-read is the DETECTION, not the band list: the band never
// changed, the operator just started a server. Re-fetching only the roster made `r` a key
// that visibly did nothing.
//
// It is NOT detectSharesCmd. That lands in onSharesDetected, which sets mode = modeShare -
// so firing it from here would teleport the operator to the share table while they were
// looking at a band. Same result, no relocation.
func (m model) rescanPrivate() tea.Cmd {
	m.status = stDim.Render("re-scanning your bands and this machine's model servers…")
	return tea.Batch(m.fetchRemoteRoster(), privateRescanCmd(m.shareUp, m.shareKey))
}

// privateRescanCmd runs the SAME local detection detectSharesCmd runs, but reports it as a
// privateRescanMsg so the handler can fold the rows in WITHOUT moving the operator.
func privateRescanCmd(extra, key string) tea.Cmd {
	inner := detectSharesCmd(extra, key)
	return func() tea.Msg {
		if msg, ok := inner().(sharesDetectedMsg); ok {
			return privateRescanMsg{found: msg.found}
		}
		return privateRescanMsg{}
	}
}

// toggleBandOnAir puts the band's model on or off air without ever changing its
// visibility. It is the PRIVATE tab's half of "as simple as share".
func (m model) toggleBandOnAir() (tea.Model, tea.Cmd, bool) {
	r, ok := m.privSelected()
	if !ok {
		return m, nil, true
	}
	switch {
	case r.band.Status != "active":
		m.status = stEmber.Render("this band is revoked - there is nothing to put on air. ") +
			stKey.Render("f") + stEmber.Render(" clears the row")
		return m, nil, true
	case r.model == "":
		// We cannot name the model, so we cannot start it. Say which of the two reasons
		// applies rather than a single vague refusal.
		if r.here {
			m.status = stDim.Render("nothing on this machine is serving that band's model · ") +
				stKey.Render("r") + stDim.Render(" re-scan, or ") + stKey.Render("m") +
				stDim.Render(" moves the band to a model you do have")
			return m, nil, true
		}
		m.status = stDim.Render("that band is on another machine - put it on air over there")
		return m, nil, true
	}
	res := m.ctrl.ToggleOnAir(r.model)
	m.syncShareCache()
	switch {
	case res.WentOff:
		m.status = stDim.Render("off air - ") + stKey.Render(r.model) +
			stDim.Render(" is no longer reachable on ") + stKey.Render(bandDial(r.band)) +
			stDim.Render(" · press ") + stKey.Render("a") + stDim.Render(" to bring it back")
	case res.LoginNeeded:
		m.status = stEmber.Render("log in to put a private band on air - run ") + stKey.Render("/login")
	case res.AtLimit:
		m.status = m.onAirLimitMsg()
	case res.Err != nil:
		m.status = stEmber.Render("! " + node.ErrReason(res.Err))
	case res.NowPrivate:
		m.status = stRed.Render(glyphOnAir+" on air ") + stDim.Render("on ") + stKey.Render(bandDial(r.band)) +
			stDim.Render(" - hidden, reachable only with its code")
	default:
		// It came back on the OPEN MARKET. That should be impossible from this screen -
		// ToggleOnAir resumes at the row's recorded visibility - so say it loudly rather
		// than reporting a bland success over a model that just became public.
		m.status = stEmber.Render("! ") + stKey.Render(r.model) +
			stEmber.Render(" went on air PUBLICLY - press ") + stKey.Render("h") +
			stEmber.Render(" in [2] SHARE to hide it again")
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
	return m.tuneInPrivateRow(r)
}

// tuneInPrivateRow is the shared opener. BASE STATION's manage card routes through it too,
// so the two surfaces can never drift into disagreeing about whether a band is reachable.
func (m model) tuneInPrivateRow(r privRow) (tea.Model, tea.Cmd, bool) {
	switch {
	case r.band.Status != "active":
		m.status = stEmber.Render("this band is revoked - its code is burnt. Press ") +
			stKey.Render("h") + stEmber.Render(" on the model in [2] SHARE for a fresh one")
		return m, nil, true
	case r.chat != "":
		return m.openLocalChannel(r), nil, true
	case r.here:
		// THIS station, but nothing is serving the model right now. Saying "another
		// machine" here (as the first cut did) would send the operator hunting for a code
		// they already cannot use.
		//
		// TWO remedies, because there are two causes and only the operator knows which.
		// The server may be stopped - start it and re-scan. Or the model may simply be
		// gone from this machine, which is the likelier case for a band minted a while
		// ago; then no amount of re-scanning helps and the fix is to MOVE the band onto a
		// model you do have, which keeps the code. Naming only the first leaves someone in
		// the second case pressing r forever.
		what := stKey.Render(r.band.NodeID)
		if r.model != "" {
			what = stKey.Render(r.model)
		}
		m.status = stDim.Render("on this machine, but nothing is serving ") + what +
			stDim.Render(" · ") + stKey.Render("r") + stDim.Render(" re-scan after starting it, or ") +
			stKey.Render("m") + stDim.Render(" moves the band to a model you do have (keeps the code)")
		return m, nil, true
	default:
		// Another machine. We hold the hash of its code, never the code, so there is
		// genuinely nothing here to tune WITH - say so and name the key that can, rather
		// than opening something that would fail on the first turn.
		m.status = stDim.Render("this band is on ") + stKey.Render(r.band.NodeID) +
			stDim.Render(" - not this machine. Tune it with its code: ") + stKey.Render("~")
		return m, nil, true
	}
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
	m.chatUnstuck = false // a fresh transcript starts stuck
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
	// The count separates LIVE from DEAD. "3 bands" over a list holding one live band and
	// two corpses is the same overstatement the BASE STATION footnote made - it reads as
	// three things you can use.
	live, dead := 0, 0
	for _, r := range rows {
		if r.band.Status == "active" {
			live++
			continue
		}
		dead++
	}
	count := plural(live, "band")
	if dead > 0 {
		count += " · " + plural(dead, "revoked row")
	}
	head := "  " + stRed.Render("▌") + " " + stBrand.Render("YOUR BANDS") +
		stDim.Render("   "+count) +
		stDim.Render(" · ") + stRed.Render(glyphOnAir+" PRIVATE") +
		stDim.Render(" · ") + stKey.Render("t") + stDim.Render(" open market")
	b.WriteString(truncVisible(head, w) + "\n")
	// The one-line contract for the whole tab, in the same voice BASE STATION uses. It
	// states the privacy property we actually have (hidden from the market) and does not
	// claim one we do not (a local turn is direct; a relayed one is not end-to-end
	// encrypted) - the reachable rows below are the ones that never touch the broker.
	line(stDim.Render("hidden from the open market · ") + stKey.Render("a") +
		stDim.Render(" on/off air · ") + stKey.Render("⏎") +
		stDim.Render(" use it here (direct, works even off air)"))
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
		return stDim.Render("revoked · f forgets it")
	case r.chat != "" && r.onAir:
		return stRed.Render(glyphOnAir) + stLive.Render(" on air") + stDim.Render(" · ⏎ direct")
	case r.chat != "":
		// Bound to a model this machine serves, but not registered on the band right now.
		// The distinction is exact and worth stating: YOU can still use it (⏎ is a direct
		// call to your own server and never needed the band), but nobody else can reach it
		// with the code until it is on air.
		return stDim.Render("off air") + stDim.Render(" · ⏎ direct · ") + stKey.Render("a") + stDim.Render(" on air")
	case r.here:
		return stDim.Render("here · its server is not running")
	default:
		return stDim.Render("another machine · needs its code")
	}
}

// privReachPlain is the selected row's verdict with the styling stripped: the cursor row is
// reverse-video and one accent governs it, so nested colours would fight the highlight.
func privReachPlain(r privRow) string {
	switch {
	case r.band.Status != "active":
		return "revoked · f forgets it"
	case r.chat != "" && r.onAir:
		return glyphOnAir + " on air · ⏎ direct"
	case r.chat != "":
		return "off air · ⏎ direct · a on air"
	case r.here:
		return "here · its server is not running"
	default:
		return "another machine · needs its code"
	}
}
