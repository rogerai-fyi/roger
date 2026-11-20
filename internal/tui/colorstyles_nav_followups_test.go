package tui

// Follow-ups to the arrow window, the slash prompt and the color work: AGENT's
// auto-tune and drafts, LOGIN's prompt, the command history, the SHARE scan landing,
// the countdown at 80 columns, and how a theme or color-blind switch reaches the
// screens and lines that do not draw through the palette slots.

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"rogerai.fm/roger/v7/internal/detect"
	"rogerai.fm/roger/v7/internal/look"
)

// browseFound is one detected local server with one model, as browseModel's stub finds.
func browseFound() []detect.Found {
	return []detect.Found{{Name: "t", Chat: "http://x/v1/chat/completions", Models: []string{"gpt-oss-20b"}}}
}

// agentWithFreeBand is a sized BROWSE model with a free band already scanned, so an
// AGENT auto-tune would bind it the moment it runs.
func agentWithFreeBand(t *testing.T) tea.Model {
	t.Helper()
	var tm tea.Model = New("http://broker.local", "tester")
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	tm, _ = tm.Update(offersMsg([]offer{freeOffer("gpt-oss-20b", 32768)}))
	return tm
}

// Passing through AGENT on an arrow walk tunes nothing and keeps the auto-tune owed;
// settling there (its key, or the window running out) runs it.
func TestArrowWalkThroughAgentDoesNotTune(t *testing.T) {
	advance := freezeNavClock(t)
	tm := agentWithFreeBand(t)
	tm, _ = tm.Update(keyLeft()) // TUNE IN -> AGENT, passing through
	m := asModel(tm)
	if m.mode != modeAgent || m.autoTuning || !m.agentTuneOwed {
		t.Fatalf("passing into AGENT must owe the auto-tune, not run it (mode=%v autoTuning=%v owed=%v)",
			m.mode, m.autoTuning, m.agentTuneOwed)
	}
	tm, _ = tm.Update(keyLeft()) // AGENT -> HELP
	m = asModel(tm)
	if m.connected != nil || m.proxyHolder != nil || m.autoTuning {
		t.Fatalf("a walk through AGENT must not bind a band")
	}
	if !m.agentTuneOwed {
		t.Fatalf("leaving mid-walk must keep the auto-tune owed, not use it up")
	}
	tm, _ = tm.Update(keyMsg("0")) // back to AGENT by its own key: settled
	m = asModel(tm)
	if m.mode != modeAgent || !m.autoTuning || m.agentTuneOwed {
		t.Errorf("settling on AGENT should run the owed auto-tune (autoTuning=%v owed=%v)", m.autoTuning, m.agentTuneOwed)
	}

	// The window running out on AGENT settles it too.
	tm = agentWithFreeBand(t)
	tm, _ = tm.Update(keyLeft())
	seq := asModel(tm).navSeq
	advance(navWindow)
	tm, _ = tm.Update(navTickMsg{seq: seq})
	if m := asModel(tm); !m.autoTuning || m.agentTuneOwed {
		t.Errorf("the window lapsing on AGENT should run the auto-tune (autoTuning=%v owed=%v)", m.autoTuning, m.agentTuneOwed)
	}
}

// A kept draft or a suggested next action keeps AGENT's arrows inside the window:
// Left moves the draft's cursor and Right accepts the hint instead of leaving.
func TestAgentDraftAndHintKeepArrows(t *testing.T) {
	freezeNavClock(t)
	m := browseModel(t)
	m, _ = m.Update(keyLeft()) // -> AGENT
	for _, r := range "hi" {
		m, _ = m.Update(runeKey(string(r)))
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc}) // leave; the draft is kept
	if got := asModel(m).mode; got != modeBrowse {
		t.Fatalf("esc should leave AGENT, got %v", got)
	}
	m, _ = m.Update(keyLeft()) // back into AGENT on an arrow, onto the kept draft
	if got := asModel(m); got.mode != modeAgent || got.agentIn.Value() != "hi" {
		t.Fatalf("expected AGENT with the kept draft, got %v %q", got.mode, got.agentIn.Value())
	}
	m, _ = m.Update(keyLeft())
	if got := asModel(m).mode; got != modeAgent {
		t.Errorf("Left in a kept draft must move its cursor, not switch sections (got %v)", got)
	}

	h := browseModel(t)
	h, _ = h.Update(keyLeft()) // -> AGENT, empty composer
	hm := asModel(h)
	hm.agentNextHint = "continue with the next step"
	if hm.navLive() {
		t.Errorf("a suggested next action must give Right back to AGENT")
	}
}

// Arrowing off LOGIN takes its prompt with it, so "press enter" or "y confirms" never
// shows on a screen where those keys do something else.
func TestLoginArrowDropsItsPrompt(t *testing.T) {
	advance := freezeNavClock(t)
	m := browseModel(t)
	m, _ = m.Update(runeKey("4")) // CONFIG
	m, _ = m.Update(keyRight())   // -> LOGIN
	if got := stripANSI(asModel(m).status); !strings.Contains(got, "press enter") {
		t.Fatalf("LOGIN should prompt for enter, status = %q", got)
	}
	advance(navWindow + time.Second)
	m, _ = m.Update(keyRight()) // -> HELP
	if got := stripANSI(asModel(m).status); strings.Contains(got, "press enter") || strings.Contains(got, "y confirms") {
		t.Errorf("LOGIN's prompt followed the operator to %v: %q", asModel(m).mode, got)
	}
}

// Leading blanks and slashes in any mix are normalized once, so the history scrub sees
// the real verb and a frequency code never reaches the history file.
func TestFreqCodeNeverReachesHistory(t *testing.T) {
	for in, want := range map[string]string{" / //freq abc123": "freq abc123", "//search": "search", "\t/x ": "x", "": ""} {
		if got := normCommand(in); got != want {
			t.Errorf("normCommand(%q) = %q, want %q", in, got, want)
		}
	}
	if got := scrubSecretArgs("//freq abc123"); got != "//freq" {
		t.Errorf("the scrub must recognize the verb behind any slashes, got %q", got)
	}
	m := asModel(browseModel(t))
	m.cmdHist = &inputHistory{}
	m.mode = modeCommand
	m.cmd.SetValue(" / //freq abc123")
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	for _, e := range asModel(nm).cmdHist.entries {
		if strings.Contains(e, "abc123") {
			t.Fatalf("the frequency code reached the history: %q", e)
		}
	}
	if es := asModel(nm).cmdHist.entries; len(es) != 1 || es[0] != "freq" {
		t.Errorf("history should hold just the verb, got %q", es)
	}
}

// An empty first scan that lands while the operator is away is parked, and the wizard
// (here its key prompt) shows as soon as esc brings them back to SHARE.
func TestHeldShareScanLandsOnReturn(t *testing.T) {
	freezeNavClock(t)
	m := browseModel(t)
	m, _ = m.Update(keyRight()) // TUNE IN -> SHARE (first scan starts)
	m, _ = m.Update(keyRight()) // SHARE -> EDGE before it returns
	edgeStatus := asModel(m).status
	url := "http://127.0.0.1:8080"
	m, _ = m.Update(sharesDetectedMsg{needKey: []string{url}})
	am := asModel(m)
	if am.mode != modeEdge || am.shareHeld == nil {
		t.Fatalf("an away result must be parked without moving the operator (mode=%v held=%v)", am.mode, am.shareHeld != nil)
	}
	if am.status != edgeStatus {
		t.Errorf("an away result must leave EDGE's status alone: %q", stripANSI(am.status))
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc}) // esc returns to SHARE
	am = asModel(m)
	if am.mode != modeShareSetup || !am.setupAwaitKey || am.setupPaste != url || am.shareHeld != nil {
		t.Fatalf("back on SHARE the key prompt should show (mode=%v awaitKey=%v paste=%q)", am.mode, am.setupAwaitKey, am.setupPaste)
	}

	// [2] SHARE itself scans afresh instead, and drops the parked result.
	r := browseModel(t)
	r, _ = r.Update(keyRight())
	r, _ = r.Update(keyRight())
	r, _ = r.Update(sharesDetectedMsg{})
	r, _ = r.Update(runeKey("2"))
	if rm := asModel(r); rm.mode != modeShare || !rm.shareLoading || rm.shareHeld != nil {
		t.Errorf("[2] should start a fresh scan (mode=%v loading=%v held=%v)", rm.mode, rm.shareLoading, rm.shareHeld != nil)
	}
}

// One scan at a time: a visit while a scan runs starts no second one, and a late empty
// result never resets what the operator has typed into the wizard.
func TestShareScanNoOverlap(t *testing.T) {
	m := asModel(browseModel(t))
	m.shareLoading = true // a first scan is still out
	nm, cmd := m.doShare(nil)
	if cmd != nil {
		t.Errorf("a visit while a scan runs must not start another")
	}
	if asModel(nm).mode != modeShare {
		t.Errorf("the visit still shows SHARE")
	}
	m.shareLoading = false
	if _, cmd := m.doShare(nil); cmd == nil {
		t.Errorf("with nothing in flight, a visit scans")
	}

	w := asModel(browseModel(t)).enterShareSetup()
	w.setupCursor = len(setupOptions) - 1
	w.setupPaste, w.setupAwaitKey, w.setupKey = "http://127.0.0.1:8080", true, "sk-12"
	nm, _ = w.onSharesDetected(nil, []string{"http://127.0.0.1:9999"})
	if got := asModel(nm); got.mode != modeShareSetup || got.setupKey != "sk-12" || got.setupPaste != "http://127.0.0.1:8080" {
		t.Errorf("a late empty scan reset the wizard: mode=%v key=%q paste=%q", got.mode, got.setupKey, got.setupPaste)
	}
}

// A quiet refresh that lands after the operator moved on changes nothing on their screen.
func TestQuietShareRescanAwayKeepsStatus(t *testing.T) {
	m := asModel(browseModel(t))
	nm, _ := m.onSharesDetected(browseFound(), nil) // rows on the table
	m = asModel(nm)
	nm, _ = m.doShare(nil) // a return visit: a quiet refresh behind the rows
	m = asModel(nm)
	if !m.shareRefreshing {
		t.Fatalf("a visit with rows should refresh quietly")
	}
	m.enterEdge()
	edgeStatus := m.status
	for _, found := range [][]detect.Found{nil, browseFound()} {
		got, _ := m.onSharesDetected(found, nil)
		if g := asModel(got); g.mode != modeEdge || g.status != edgeStatus {
			t.Errorf("a quiet result (%d found) changed EDGE: mode=%v status=%q", len(found), g.mode, stripANSI(g.status))
		}
	}
}

// At 80 columns the bank (87 wide) leaves no room beside it, so the countdown takes the
// blank row under it, and the frame keeps its height.
func TestCountdownShowsAt80Columns(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	advance := freezeNavClock(t)
	mm := asModel(browseModel(t))
	mm.width = 80
	m, _ := mm.Update(keyRight()) // -> SHARE, window open
	am := asModel(m)
	if strings.Contains(stripANSI(am.presetBar(80)), "←/→") {
		t.Fatalf("the countdown cannot fit beside the bank at 80 columns")
	}
	open := strings.Split(stripANSI(am.View()), "\n")
	if len(open) < 2 || !strings.Contains(open[1], "←/→ 2s") {
		t.Errorf("the countdown should sit on the row under the bank:\n%s", strings.Join(open[:min(4, len(open))], "\n"))
	}
	if am.presetSpacer(200) != "" {
		t.Errorf("when it fits beside the bank, the spacer row stays blank")
	}
	advance(navWindow + time.Second)
	closed := strings.Split(stripANSI(am.View()), "\n")
	if len(closed) != len(open) || strings.TrimSpace(closed[1]) != "" {
		t.Errorf("the countdown must not add a row, and the row is blank again after (%d -> %d rows)", len(open), len(closed))
	}
}

// The screensaver stands on a forced theme's page too.
func TestPingWorldOnForcedThemePage(t *testing.T) {
	restoreColorState(t)
	colorOn(t, true)
	restore := quiet
	t.Cleanup(func() { quiet = restore })
	quiet = false
	m := asModel(browseModel(t))
	nm, _ := m.enterPingWorld()
	pw := asModel(nm)
	SetTheme("light")
	applyTheme()
	prefix := groundPrefix()
	if got := pw.View(); !strings.HasPrefix(got, prefix) {
		t.Errorf("Ping World under a forced theme should be painted on its page")
	}
	SetTheme("auto")
	applyTheme()
	if got := pw.View(); strings.Contains(got, prefix) {
		t.Errorf("Ping World in auto keeps the terminal's own background")
	}
}

// The look palette ([3] EDGE, the welcome card) is its own value on the dark ground,
// takes a dark ink on the light page, and follows the color-blind board.
func TestLookColorsFollowThemeAndBoard(t *testing.T) {
	restoreColorState(t)
	colorOn(t, true)
	if got := lookStyle(look.Paper).Render("x"); !strings.Contains(got, "243;241;234") {
		t.Errorf("dark ground: Paper should be the look's own #F3F1EA: %q", got)
	}
	if got := lookStyle(look.Live).Render("x"); !strings.Contains(got, "255;68;56") {
		t.Errorf("dark ground: Live should be the look's own #FF4438: %q", got)
	}
	lipgloss.DefaultRenderer().SetHasDarkBackground(false)
	for c, rgb := range map[look.RGB]string{look.Paper: rgbFromHex(cInk.Light), look.Ink: rgbFromHex(cBody.Light), look.Muted: rgbFromHex(cDim.Light)} {
		if got := lookStyle(c).Render("x"); !strings.Contains(got, rgb) {
			t.Errorf("light page: %v should take the dark ink %s: %q", c, rgb, got)
		}
	}
	if cap := lookKeyCap("e", false); !strings.Contains(cap, "38;2;251;251;250") || !strings.Contains(cap, "48;2;21;20;15") {
		t.Errorf("light page: the key cap should be a dark cap with a light letter: %q", cap)
	}
	lipgloss.DefaultRenderer().SetHasDarkBackground(true)
	SetColorblind("protan")
	if got := lookStyle(look.Live).Render("x"); !strings.Contains(got, "79;157;255") {
		t.Errorf("protan: EDGE's red should turn blue like the status line: %q", got)
	}
	if got := lookStyle(look.Signal).Render("x"); strings.Contains(got, "132;194;85") {
		t.Errorf("protan: EDGE's green should not stay green: %q", got)
	}
}

// A live switch carries the transcripts across: lines drawn before /theme or
// /colorblind take the new colors, and their text is untouched.
func TestSwitchRecolorsStoredLines(t *testing.T) {
	restoreColorState(t)
	colorOn(t, true)
	themeMode = "auto"
	m := asModel(browseModel(t))
	m.agentLines = []string{stKey.Render("an answer") + " " + stDim.Render("aside")}
	m.transcript = []string{stLive.Render("chat line")}
	text := stripANSI(m.agentLines[0])

	nm, _ := m.run("theme light")
	m = asModel(nm)
	if got := m.agentLines[0]; !strings.Contains(got, "21;20;15") || strings.Contains(got, "243;241;234") {
		t.Errorf("after /theme light an earlier line should use the dark ink: %q", got)
	}
	if got := m.transcript[0]; strings.Contains(got, rgbFromHex(cBody.Dark)) {
		t.Errorf("the chat transcript should be recolored too: %q", got)
	}
	if stripANSI(m.agentLines[0]) != text {
		t.Errorf("recoloring must not change the text")
	}
	nm, _ = m.run("theme dark")
	if got := asModel(nm).agentLines[0]; !strings.Contains(got, "243;241;234") {
		t.Errorf("after /theme dark the line should be back on the light ink: %q", got)
	}

	m = asModel(nm)
	m.agentLines = []string{stRed.Render("on air")}
	nm, _ = m.run("colorblind protan")
	if got := asModel(nm).agentLines[0]; !strings.Contains(got, "79;157;255") {
		t.Errorf("after /colorblind protan an earlier red should turn blue: %q", got)
	}
	nm, _ = asModel(nm).run("colorblind off")
	if got := asModel(nm).agentLines[0]; !strings.Contains(got, rgbLive) {
		t.Errorf("/colorblind off should bring the red back: %q", got)
	}
}

// recolorSGR maps whole color parameters at every profile and leaves the rest alone.
func TestRecolorSGR(t *testing.T) {
	restoreColorState(t)
	tbl := map[string]string{"38;2;1;2;3": "38;2;9;9;9", "48;5;235": "48;5;255", "97": "30"}
	in := "\x1b[1;38;2;1;2;3mA\x1b[0m \x1b[48;5;235;4mB\x1b[0m \x1b[97mC\x1b[0m \x1b[38;2;1;2;4mD\x1b[0m"
	want := "\x1b[1;38;2;9;9;9mA\x1b[0m \x1b[48;5;255;4mB\x1b[0m \x1b[30mC\x1b[0m \x1b[38;2;1;2;4mD\x1b[0m"
	if got := recolorSGR(in, tbl); got != want {
		t.Errorf("recolorSGR:\n got %q\nwant %q", got, want)
	}
	lipgloss.DefaultRenderer().SetColorProfile(termenv.Ascii)
	if sgrMap([][2]string{{"#000000", "#FFFFFF"}}) != nil {
		t.Errorf("with no color there is nothing to map")
	}
}

// `roger resume` draws the restored transcript before Bubble Tea starts, so the theme
// has to be in force by then.
func TestResumeRendersInForcedTheme(t *testing.T) {
	restoreColorState(t)
	colorOn(t, true)
	SetTheme("light")
	var got tea.Model
	orig := runProgram
	runProgram = func(m tea.Model, _ ...tea.ProgramOption) error { got = m; return nil }
	t.Cleanup(func() { runProgram = orig })
	if err := RunResumedWithController("broker", "user", nil, "", Hooks{}, NewController("broker", Hooks{}), resumedFixture()); err != nil {
		t.Fatalf("resume: %v", err)
	}
	lines := strings.Join(asModel(got).agentLines, "\n")
	if strings.Contains(lines, "243;241;234") || strings.Contains(lines, rgbFromHex(cBody.Dark)) {
		t.Errorf("restored lines were drawn in the dark arm under a forced light theme:\n%q", lines)
	}
}

func rgbFromHex(hex string) string {
	c := termenv.TrueColor.Color(hex).Sequence(false)
	return strings.TrimPrefix(c, "38;2;")
}
