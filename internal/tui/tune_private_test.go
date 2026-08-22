package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"rogerai.fm/roger/v6/internal/agent"
	"rogerai.fm/roger/v6/internal/detect"
	"rogerai.fm/roger/v6/internal/node"
)

// testFoundGrok is a local server serving the same model the private band is bound to.
func testFoundGrok() []detect.Found {
	return []detect.Found{{
		Name: "ollama", BaseURL: "http://127.0.0.1:11434/v1",
		Chat:   "http://127.0.0.1:11434/v1/chat/completions",
		Models: []string{"grok-4.6"},
	}}
}

// THE PRIVATE TAB IN [1] TUNE IN (tune_private.go).
//
// The gap it closes: an operator who minted a private band was shown its code once, told it
// was never stored, and then could not find the band anywhere - /discover hides private
// nodes with no owner exemption, so their own model was missing from the list they browse.
// Spec: features/discovery/bands.feature.

// privateTab returns a [1] TUNE IN model on the PRIVATE half, carrying three bands that
// cover the three outcomes: one reachable HERE, one on another machine, one revoked.
func privateTab(t *testing.T) model {
	t.Helper()
	m := New("http://broker.local", "tester")
	m.width, m.height = 100, 40
	m.mode = modeBrowse
	m.tuneTab = tabPrivate
	m.loggedIn = true
	// A station callsign WITH hyphens: the node-id join must survive it (see the split test).
	m.ctrl.Rename("eager-puma-54")
	m.station = "eager-puma-54"
	// Seed the CONTROLLER, not m.shareRows: syncShareCache re-derives the share table from
	// the controller on every update, so a directly-assigned m.shareRows is gone by the
	// time a keypress is handled. Seeding the real source is also what the product does.
	m.ctrl.SetRows([]node.ShareRow{
		{Model: "grok-4.6", Upstream: "http://127.0.0.1:11434/v1/chat/completions"},
	})
	m.syncShareCache()
	m.sharePrivate = map[string]bool{"grok-4.6": true}
	m.rcBands = []BandRow{
		{ID: "band_here", Display: "145.225 MHz", Status: "active",
			NodeID: agent.ShareNodeID("eager-puma-54", "grok-4.6", 0)},
		{ID: "band_away", Display: "147.520 MHz", Status: "active", NodeID: "other-box-llama-3"},
		{ID: "band_dead", Display: "149.100 MHz", Status: "revoked", NodeID: "other-box-mistral"},
	}
	return m
}

// THE HEADLINE. m.bands is EMPTY here on purpose: a private band is hidden from /discover
// by design, so the exact moment the operator has private bands to see can be a moment the
// market list is empty. Before the tab check moved ahead of the empty-market branch, this
// screen printed "no stations on air" over a list of the operator's own models.
func TestPrivateTabRendersOverAnEmptyMarket(t *testing.T) {
	m := privateTab(t)
	if len(m.bands) != 0 {
		t.Fatal("this lock is only meaningful with an empty market")
	}
	out := stripANSI(m.browseView(m.width))
	if !strings.Contains(out, "grok-4.6") {
		t.Errorf("the PRIVATE tab must list your own band's model over an empty market, got:\n%s", out)
	}
	if strings.Contains(out, "no stations on air") {
		t.Errorf("the empty-MARKET copy leaked into the PRIVATE tab:\n%s", out)
	}
}

// NEGATIVE HALF of the same guarantee: on the OPEN MARKET half an empty band list must
// still say so. Without this the lock above could be satisfied by deleting the empty state.
func TestOpenMarketStillReportsAnEmptyBand(t *testing.T) {
	m := privateTab(t)
	m.tuneTab = tabOpenMarket
	m.scanned = true
	out := stripANSI(m.browseView(m.width))
	if strings.Contains(out, "145.225 MHz") {
		t.Errorf("the PRIVATE list leaked onto the OPEN MARKET half:\n%s", out)
	}
}

// t switches halves, both ways. This is the founder's ask in one key.
func TestTSwitchesBetweenOpenMarketAndPrivate(t *testing.T) {
	m := privateTab(t)
	m.tuneTab = tabOpenMarket
	var tm tea.Model = m
	tm, _ = tm.Update(keyMsg("t"))
	if asModel(tm).tuneTab != tabPrivate {
		t.Fatal("t did not switch [1] TUNE IN onto the PRIVATE half")
	}
	tm, _ = tm.Update(keyMsg("t"))
	if asModel(tm).tuneTab != tabOpenMarket {
		t.Fatal("t did not switch back to the OPEN MARKET")
	}
}

// A frequency code is stored only as a hash, so there is nothing to show and a placeholder
// would read as the real thing. The tab may show the cosmetic DIAL and nothing more.
func TestPrivateTabNeverRendersACode(t *testing.T) {
	m := privateTab(t)
	m.tuneFreq = "8F3K-QQ21-ZZ90" // as if a code were in flight elsewhere in the session
	out := stripANSI(m.browseView(m.width))
	if strings.Contains(out, "8F3K") {
		t.Errorf("the PRIVATE tab rendered a frequency code:\n%s", out)
	}
	if !strings.Contains(out, "145.225 MHz") {
		t.Errorf("the PRIVATE tab must still show the cosmetic dial, got:\n%s", out)
	}
}

// The three reachability verdicts must each be present and distinct: the operator's next
// action differs completely between them.
func TestPrivateTabSaysWhatEachBandCanDo(t *testing.T) {
	out := stripANSI(privateTab(t).browseView(100))
	for _, want := range []string{
		"here",            // the band on this machine
		"another machine", // the band elsewhere
		"revoked",         // the burnt one
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the PRIVATE tab must say %q about one of the bands, got:\n%s", want, out)
		}
	}
}

// Enter on a REACHABLE band opens a DIRECT channel: bound to the local server, never the
// broker. chatLocalChat is what every downstream branch keys off, so it is the lock.
func TestEnterOnALocalBandOpensADirectChannel(t *testing.T) {
	m := privateTab(t)
	m.privCursor = 0
	var tm tea.Model = m
	tm, _ = tm.Update(keyMsg2(tea.KeyEnter))
	gm := asModel(tm)
	if gm.mode != modeChat {
		t.Fatalf("enter on a reachable private band did not open the channel (mode %v)", gm.mode)
	}
	if gm.chatLocalChat != "http://127.0.0.1:11434/v1/chat/completions" {
		t.Fatalf("the channel is not bound to the local server, got %q", gm.chatLocalChat)
	}
	if gm.connected == nil || gm.connected.Model != "grok-4.6" {
		t.Fatal("the channel must be connected to the band's model")
	}
	// A synthesized offer must carry NO market claims: a price here would be invented, and
	// FreeNow is a marketplace word for a paid band that is currently free - not this.
	if gm.connected.PriceIn != 0 || gm.connected.PriceOut != 0 || gm.connected.FreeNow {
		t.Error("a direct channel's offer must make no price or market claim")
	}
}

// Enter on a band on ANOTHER machine must not open anything. We hold the hash of its code,
// never the code, so there is genuinely nothing here to tune with - and the message has to
// name the key that can (~), or the operator is left with a dead Enter.
func TestEnterOnARemoteBandRefusesAndNamesTheCodeKey(t *testing.T) {
	m := privateTab(t)
	m.privCursor = 1 // band_away
	var tm tea.Model = m
	tm, _ = tm.Update(keyMsg2(tea.KeyEnter))
	gm := asModel(tm)
	if gm.mode == modeChat {
		t.Fatal("enter opened a channel on a band this machine cannot reach")
	}
	if gm.chatLocalChat != "" {
		t.Fatal("a remote band must never bind the local route")
	}
	st := stripANSI(gm.status)
	if !strings.Contains(st, "not this machine") || !strings.Contains(st, "~") {
		t.Errorf("the refusal must say where the band is and name ~, got %q", st)
	}
}

// A revoked band's code is burnt. Enter must refuse and point at the action that mints a
// fresh one, rather than opening a channel that could never carry a turn.
func TestEnterOnARevokedBandRefuses(t *testing.T) {
	m := privateTab(t)
	m.privCursor = 2 // band_dead
	var tm tea.Model = m
	tm, _ = tm.Update(keyMsg2(tea.KeyEnter))
	gm := asModel(tm)
	if gm.mode == modeChat {
		t.Fatal("enter opened a channel on a revoked band")
	}
	if !strings.Contains(stripANSI(gm.status), "revoked") {
		t.Errorf("the refusal must say the band is revoked, got %q", stripANSI(gm.status))
	}
}

// THE JOIN MUST NOT SPLIT ON "-". A station callsign can contain hyphens ("eager-puma-54"),
// so splitting a node id to recover the model is a guess - and a wrong guess would show the
// operator the wrong model as the thing behind their band, or offer a direct channel to it.
func TestNodeIDJoinSurvivesAHyphenatedStation(t *testing.T) {
	m := privateTab(t)
	rows := m.privRows()
	if len(rows) != 3 {
		t.Fatalf("want 3 rows, got %d", len(rows))
	}
	if rows[0].model != "grok-4.6" {
		t.Fatalf("the band on this machine did not resolve to its model, got %q", rows[0].model)
	}
	// And a band on another machine must resolve to NOTHING rather than to a near match.
	if rows[1].model != "" {
		t.Fatalf("a band on another machine resolved to a local model %q", rows[1].model)
	}
}

// Leaving the channel must clear the direct binding. Left set, the NEXT channel - a real
// broker band - would send its turns to this local server under the new band's name.
func TestDisconnectClearsTheDirectBinding(t *testing.T) {
	m := privateTab(t)
	m.privCursor = 0
	var tm tea.Model = m
	tm, _ = tm.Update(keyMsg2(tea.KeyEnter))
	gm := asModel(tm)
	if gm.chatLocalChat == "" {
		t.Fatal("precondition: the channel should be bound")
	}
	after, _ := gm.disconnect()
	if asModel(after).chatLocalChat != "" || asModel(after).chatLocalKey != "" {
		t.Fatal("disconnect left the direct route bound - the next band would inherit it")
	}
}

// A direct turn is not metered. "$0.00" would claim a meter ran and happened to round to
// zero; the truth is that no wallet was touched at all.
func TestALocalTurnPrintsNoDollarFigure(t *testing.T) {
	out := strings.Join(replyFooter(chatMsg{local: true, latency: 1200000000, status: "direct"}, false), "\n")
	out = stripANSI(out)
	if strings.Contains(out, "$") {
		t.Errorf("a direct turn's receipt must print no dollar figure, got %q", out)
	}
	if !strings.Contains(out, "nothing metered") {
		t.Errorf("a direct turn's receipt must say nothing was metered, got %q", out)
	}
	// NEGATIVE HALF: a MARKET turn must still print its cost, or the lock above could be
	// satisfied by removing the cost from every receipt.
	mkt := stripANSI(strings.Join(replyFooter(chatMsg{cost: 0.0123, latency: 1200000000}, false), "\n"))
	if !strings.Contains(mkt, "$") {
		t.Errorf("a market turn must still print its cost, got %q", mkt)
	}
}

// The channel header follows the same rule: a direct channel shows the route where a market
// channel shows the running cost.
func TestDirectChannelHeaderShowsTheRouteNotACost(t *testing.T) {
	m := privateTab(t)
	m.privCursor = 0
	var tm tea.Model = m
	tm, _ = tm.Update(keyMsg2(tea.KeyEnter))
	gm := asModel(tm)
	head := stripANSI(gm.chatView(gm.width))
	if strings.Contains(head, "cost $") {
		t.Errorf("a direct channel's header printed a cost:\n%s", head)
	}
	if !strings.Contains(head, "nothing metered") {
		t.Errorf("a direct channel's header must name the route, got:\n%s", head)
	}
}

// The footer is the one place an operator learns what a screen does. The PRIVATE half must
// not teach the market's keys - the exact failure BASE STATION had before it got its own
// footer case.
func TestPrivateTabFooterTeachesItsOwnKeys(t *testing.T) {
	m := privateTab(t)
	foot := stripANSI(m.footer(m.width))
	for _, want := range []string{"use it", "on/off air", "new code", "forget", "OPEN MARKET"} {
		if !strings.Contains(foot, want) {
			t.Errorf("the PRIVATE footer must teach %q, got %q", want, foot)
		}
	}
	for _, never := range []string{"s sort", "f filter", "←/→ section"} {
		if strings.Contains(foot, never) {
			t.Errorf("the PRIVATE footer taught the market key %q: %q", never, foot)
		}
	}
}

// The market-only keys must STOP at the private tab rather than silently acting on a list
// this view does not render. d in particular would drop a channel the operator cannot see.
func TestMarketKeysDoNotActFromThePrivateTab(t *testing.T) {
	m := privateTab(t)
	var tm tea.Model = m
	for _, k := range []string{"f", "s", "d"} {
		tm, _ = tm.Update(keyMsg(k))
		gm := asModel(tm)
		if gm.filterMode {
			t.Fatalf("%q opened the market filter from the PRIVATE tab", k)
		}
		if gm.mode != modeBrowse || gm.tuneTab != tabPrivate {
			t.Fatalf("%q moved off the PRIVATE tab (mode %v, tab %v)", k, gm.mode, gm.tuneTab)
		}
	}
}

// "and they should always show in the agent section" - a local row that is ALSO on a
// private band must say so, so the operator hunting for the band they minted recognises it.
// The badge is read from local controller state, never from a broker fetch, so it is
// present every time the picker opens rather than only after a roster landed.
func TestAgentPickerNamesYourPrivateBand(t *testing.T) {
	m := privateTab(t)
	m.localFound = testFoundGrok()
	rows := m.localAgentRows()
	if len(rows) == 0 {
		t.Fatal("the local scan produced no rows")
	}
	if !rows[0].band {
		t.Fatal("a local model on a private band must be marked in the agent picker")
	}
	// NEGATIVE HALF: a local model that is NOT on a private band must not be marked, or
	// the badge would say nothing at all.
	m.sharePrivate = map[string]bool{}
	if m.localAgentRows()[0].band {
		t.Fatal("a plain local model was marked as a private band")
	}
}

// ── THE STATION-PREFIX FIX ───────────────────────────────────────────────────
//
// FOUNDER, on the first cut: "but it is on the same machine". A band whose node id was
// eager-puma-54-grok-4-6 read "another machine · needs its code" purely because no share
// row matched it at that instant - the model's server was not running. The remedy for a
// stopped server ("start it") is nothing like the remedy for a remote band ("find the
// code"), so the two cases must never share a message.

// privateTabNoServer is the founder's exact state: the band is on THIS station, and nothing
// is serving its model right now.
func privateTabNoServer(t *testing.T) model {
	t.Helper()
	m := privateTab(t)
	m.ctrl.SetRows(nil) // the local server went away
	m.syncShareCache()
	return m
}

func TestABandOnThisStationIsNotCalledAnotherMachine(t *testing.T) {
	m := privateTabNoServer(t)
	rows := m.privRows()
	if !rows[0].here {
		t.Fatal("a band whose node id carries this station must resolve as here")
	}
	if rows[0].chat != "" {
		t.Fatal("precondition: no server should be serving it")
	}
	// Assert on THIS band's row, not the whole view - a genuinely remote band lives two
	// rows down and must keep saying "another machine" (locked below).
	row := stripANSI(m.privRowLine(rows[0], false))
	if strings.Contains(row, "another machine") {
		t.Errorf("a band on THIS station was reported as another machine: %q", row)
	}
	if !strings.Contains(row, "server is not running") {
		t.Errorf("the row must name the stopped server as the thing to fix, got %q", row)
	}
}

// Enter on it must send the operator to the LOCAL remedies, not to a code they cannot use.
// There are TWO, because there are two causes and only the operator knows which: the server
// is stopped (start it, re-scan), or the model is gone from this machine (move the band to
// one they do have, keeping the code). Naming only the first leaves the second case
// pressing r forever - which is exactly what the founder hit.
func TestEnterOnAStoppedLocalBandNamesBothLocalRemedies(t *testing.T) {
	m := privateTabNoServer(t)
	m.privCursor = 0
	var tm tea.Model = m
	tm, _ = tm.Update(keyMsg2(tea.KeyEnter))
	st := stripANSI(asModel(tm).status)
	if !strings.Contains(st, "on this machine") {
		t.Errorf("the refusal must say the band is local, got %q", st)
	}
	if !strings.Contains(st, "re-scan") {
		t.Errorf("the refusal must offer the re-scan, got %q", st)
	}
	if !strings.Contains(st, "moves the band") {
		t.Errorf("the refusal must offer the move for a model that is simply gone, got %q", st)
	}
	// And it must never suggest hunting for a code: we hold the hash, not the code, and
	// the band is local anyway.
	if strings.Contains(st, "another machine") {
		t.Errorf("a local band was described as remote: %q", st)
	}
}

// NEGATIVE HALF: a band on a DIFFERENT station must still say so, or the fix above could be
// satisfied by calling every band local.
func TestABandOnAnotherStationStillSaysSo(t *testing.T) {
	m := privateTabNoServer(t)
	rows := m.privRows()
	if rows[1].here {
		t.Fatal("a band on other-box was reported as being on this station")
	}
	out := stripANSI(m.browseView(m.width))
	if !strings.Contains(out, "another machine") {
		t.Errorf("a genuinely remote band must still say so:\n%s", out)
	}
}

// ── ROTATE ──────────────────────────────────────────────────────────────────

// n on a live band asks first. A rotation looks like a move until you notice it cuts off
// everyone tuned in, so it can never be one keystroke.
func TestNewCodeConfirmsBeforeRotating(t *testing.T) {
	m := privateTab(t)
	m.privCursor = 0
	var tm tea.Model = m
	tm, _ = tm.Update(keyMsg("n"))
	gm := asModel(tm)
	if gm.mode != modeBandRotateConfirm {
		t.Fatalf("n did not open the rotate confirm (mode %v)", gm.mode)
	}
	out := stripANSI(gm.bandRotateConfirmView(100))
	// The confirm must lead with the COST, because that is the only thing separating this
	// from a move.
	if !strings.Contains(out, "cut off") {
		t.Errorf("the rotate confirm must say who it cuts off, got:\n%s", out)
	}
	if !strings.Contains(out, "move it instead") {
		t.Errorf("the confirm must name the non-destructive alternative, got:\n%s", out)
	}
	// Any key but y backs out.
	tm, _ = gm.Update(keyMsg("z"))
	if asModel(tm).mode == modeBandRotateConfirm {
		t.Error("a non-y key must back out of the rotate confirm")
	}
}

// A revoked band cannot be rotated: its code is burnt, and rotating would resurrect it.
func TestNewCodeRefusedOnARevokedBand(t *testing.T) {
	m := privateTab(t)
	m.privCursor = 2 // band_dead
	var tm tea.Model = m
	tm, _ = tm.Update(keyMsg("n"))
	gm := asModel(tm)
	if gm.mode == modeBandRotateConfirm {
		t.Fatal("a revoked band opened the rotate confirm")
	}
	if !strings.Contains(stripANSI(gm.status), "burnt") {
		t.Errorf("the refusal must say the code is burnt, got %q", stripANSI(gm.status))
	}
}

// A landed rotation routes the new code to the SHOW-ONCE card, and back to where the
// operator started - not to the SHARE table the card was originally written for.
func TestARotatedCodeGoesToTheShowOnceCard(t *testing.T) {
	m := privateTab(t)
	var tm tea.Model = m
	tm, _ = tm.Update(bandActionMsg{rotated: true, code: "145.225 MHz · AAAA-BBBB", display: "145.225 MHz · ••••-••••"})
	gm := asModel(tm)
	if gm.mode != modeBandCard {
		t.Fatalf("a rotation did not open the one-time card (mode %v)", gm.mode)
	}
	if gm.bandCardCode != "145.225 MHz · AAAA-BBBB" {
		t.Fatalf("the card is not holding the new code, got %q", gm.bandCardCode)
	}
	// Leaving the card clears the secret (shown exactly once) and returns to the tab.
	after, _ := (&gm).onBandCardKey(keyMsg("x"))
	am := asModel(after)
	if am.bandCardCode != "" {
		t.Error("the one-time code survived leaving the card")
	}
	if am.mode != modeBrowse {
		t.Errorf("a rotation started in the PRIVATE tab returned to mode %v, not the tab", am.mode)
	}
}

// ── FORGET ──────────────────────────────────────────────────────────────────

// f clears a revoked row - the founder's "i also don't see a way to delete them".
func TestForgetActsOnARevokedRow(t *testing.T) {
	m := privateTab(t)
	m.privCursor = 2 // band_dead
	var tm tea.Model = m
	_, cmd := tm.Update(keyMsg("f"))
	if cmd == nil {
		t.Fatal("f on a revoked band issued no action")
	}
}

// NEGATIVE HALF: f must never touch a LIVE band. Deleting a live row would strand every
// consumer holding its code with no revoke anywhere.
func TestForgetRefusesALiveBandInTheTUI(t *testing.T) {
	m := privateTab(t)
	m.privCursor = 0 // the live one
	var tm tea.Model = m
	tm, cmd := tm.Update(keyMsg("f"))
	if cmd != nil {
		t.Fatal("f on a LIVE band issued an action")
	}
	if !strings.Contains(stripANSI(asModel(tm).status), "revoke it first") {
		t.Errorf("the refusal must name the required first step, got %q", stripANSI(asModel(tm).status))
	}
}

// ── ON AIR / OFF AIR FROM THE PRIVATE TAB ────────────────────────────────────
//
// FOUNDER 2026-08-21: "i want it to be easy to use my own bands, basically just as simple
// as we are able to share (put the bands on/off air) and use them". a/space is the same key
// SHARE uses, on the same controller call.

// THE LOAD-BEARING ROUTING CHOICE - a goes through ToggleOnAir, never TogglePrivate,
// because TogglePrivate flips VISIBILITY and would publish an already-private model to the
// OPEN MARKET. That guarantee is a CONTROLLER invariant and is locked where it lives:
// internal/node TestOnAirResumesPrivateNotPublic (plus its public-row negative half). It is
// deliberately not duplicated here - exercising it through the TUI means a real register,
// which would make this suite dial a broker.
//
// What the TUI owns, and locks below, is the refusals and the row copy.

// A revoked band has nothing to put on air, and the refusal must name the key that clears
// the row rather than leaving a dead press.
func TestOnAirRefusedOnARevokedBand(t *testing.T) {
	m := privateTab(t)
	m.privCursor = 2 // band_dead
	var tm tea.Model = m
	tm, _ = tm.Update(keyMsg("a"))
	st := stripANSI(asModel(tm).status)
	if !strings.Contains(st, "revoked") {
		t.Errorf("the refusal must say the band is revoked, got %q", st)
	}
}

// A band on another machine cannot be started from here - the model is not ours to run.
func TestOnAirRefusedOnARemoteBand(t *testing.T) {
	m := privateTab(t)
	m.privCursor = 1 // band_away
	var tm tea.Model = m
	tm, _ = tm.Update(keyMsg("a"))
	st := stripANSI(asModel(tm).status)
	if !strings.Contains(st, "another machine") {
		t.Errorf("the refusal must say where the band lives, got %q", st)
	}
}

// The row must teach BOTH halves of what the founder asked for: use it here, and put it on
// air for everyone else. The distinction is exact - ⏎ is a direct call to your own server
// and works even off air; `a` is what makes the code reachable by anyone else.
func TestAnOffAirRowTeachesBothHalves(t *testing.T) {
	m := privateTab(t)
	m.sharePrivate = map[string]bool{} // served here, but not registered on the band
	rows := m.privRows()
	line := stripANSI(m.privRowLine(rows[0], false))
	if !strings.Contains(line, "off air") {
		t.Errorf("an off-air band must say so, got %q", line)
	}
	if !strings.Contains(line, "⏎") || !strings.Contains(line, "a") {
		t.Errorf("the row must teach both ⏎ (use it here) and a (on air), got %q", line)
	}
}

// ── "I'M PRESSING r BUT NOTHING IS HAPPENING" ────────────────────────────────
//
// The band card can answer "no local server is serving <model> - start it, then press r",
// and the card had no r at all. Worse, even where r existed (the PRIVATE tab) it only
// re-fetched the BAND ROSTER - but the band never changed; what needed re-reading was the
// LOCAL DETECTION SCAN. A message that names a key must be shown on a screen where that
// key works, and the key must refresh the thing the message is about.

// r on the BAND CARD must do something.
func TestRescanWorksOnTheBandCard(t *testing.T) {
	m := privateTab(t)
	m.mode = modeBandManage
	m.bandManageID, m.bandManageDisp, m.bandManageNode = "band_here", "145.225 MHz", "eager-puma-54-grok-4-6"
	var tm tea.Model = m
	_, cmd := tm.Update(keyMsg("r"))
	if cmd == nil {
		t.Fatal("r on the band card did nothing - the card's own refusal tells the operator to press it")
	}
}

// The footer must teach it, or it stays undiscoverable.
func TestBandCardFooterTeachesRescan(t *testing.T) {
	m := privateTab(t)
	m.mode = modeBandManage
	m.bandManageID = "band_here"
	foot := stripANSI(m.footer(m.width))
	if !strings.Contains(foot, "re-scan") {
		t.Errorf("the band card footer must teach r, got %q", foot)
	}
}

// A re-scan must land WITHOUT relocating the operator. detectSharesCmd ends in
// onSharesDetected, which sets mode = modeShare - firing it from a band screen would
// teleport them to the share table mid-look.
func TestARescanDoesNotTeleportToShare(t *testing.T) {
	m := privateTab(t)
	m.mode = modeBandManage
	var tm tea.Model = m
	tm, _ = tm.Update(privateRescanMsg{found: nil})
	if got := asModel(tm).mode; got != modeBandManage {
		t.Errorf("a re-scan moved the operator off the band card to mode %v", got)
	}
	// And from the tab.
	m2 := privateTab(t)
	var tm2 tea.Model = m2
	tm2, _ = tm2.Update(privateRescanMsg{found: nil})
	gm := asModel(tm2)
	if gm.mode != modeBrowse || gm.tuneTab != tabPrivate {
		t.Errorf("a re-scan moved the operator off the PRIVATE tab (mode %v tab %v)", gm.mode, gm.tuneTab)
	}
}

// An empty re-scan must name the real remedy - start a server - rather than reporting a
// bland success over a machine that is still serving nothing.
func TestAnEmptyRescanNamesTheRemedy(t *testing.T) {
	m := privateTab(t)
	m.ctrl.SetRows(nil)
	m.syncShareCache()
	var tm tea.Model = m
	tm, _ = tm.Update(privateRescanMsg{found: nil})
	st := stripANSI(asModel(tm).status)
	if !strings.Contains(st, "no local model server") {
		t.Errorf("an empty re-scan must say no server was found, got %q", st)
	}
}

// privReachPlain is the SELECTED row's verdict with styling stripped - the cursor row is
// reverse-video and one accent governs it, so nested colours would fight the highlight.
// It must say the same THING as the styled version, or the row changes meaning when you
// move the cursor onto it.
func TestTheSelectedRowSaysTheSameThing(t *testing.T) {
	m := privateTab(t)
	for i, r := range m.privRows() {
		styled := stripANSI(m.privReach(r))
		plain := privReachPlain(r)
		// Compare the load-bearing word rather than the exact string: the styled form may
		// carry a glyph the plain one drops.
		for _, word := range []string{"on air", "off air", "revoked", "another machine", "not running"} {
			if strings.Contains(styled, word) != strings.Contains(plain, word) {
				t.Errorf("row %d disagrees about %q: styled=%q plain=%q", i, word, styled, plain)
			}
		}
	}
	// And the off-air row, which only appears when a model is served but not registered.
	m.sharePrivate = map[string]bool{}
	r := m.privRows()[0]
	if !strings.Contains(privReachPlain(r), "off air") {
		t.Errorf("the selected off-air row lost its state: %q", privReachPlain(r))
	}
}

// A re-scan fired from the PRIVATE tab reports as a privateRescanMsg, NOT a
// sharesDetectedMsg - the whole reason it exists is that the latter ends on the SHARE
// table, which would teleport an operator looking at a band.
func TestTheRescanCommandReportsAsItsOwnMessage(t *testing.T) {
	cmd := privateRescanCmd("", "")
	if cmd == nil {
		t.Fatal("no re-scan command")
	}
	switch cmd().(type) {
	case privateRescanMsg:
	default:
		t.Errorf("the re-scan reported as %T - it must not travel through onSharesDetected", cmd())
	}
}
