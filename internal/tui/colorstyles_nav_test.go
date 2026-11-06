package tui

// Tests for three operator fixes: the 2s ←/→ section window (arrownav.go), the
// visible `/` in the command palette, and the light/dark themes + color-blind
// boards (palette.go, colorcmd.go).

import (
	"strings"
	"testing"
	"time"

	"rogerai.fm/roger/v7/internal/detect"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// freezeNavClock pins the arrow-window clock and returns a function that moves it.
func freezeNavClock(t *testing.T) func(time.Duration) {
	t.Helper()
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	old := navNow
	navNow = func() time.Time { return now }
	t.Cleanup(func() { navNow = old })
	return func(d time.Duration) { now = now.Add(d) }
}

func runeKey(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

// Left from TUNE IN lands in the AGENT prompt; a Right inside the window must step
// back to TUNE IN instead of moving the prompt cursor (the "stuck in AGENT" bug).
func TestArrowWindowLeavesAgentBothWays(t *testing.T) {
	freezeNavClock(t)
	m := browseModel(t)
	m, _ = m.Update(keyLeft())
	if got := asModel(m).mode; got != modeAgent {
		t.Fatalf("Left from TUNE IN should reach AGENT, got %v", got)
	}
	m, _ = m.Update(keyRight())
	if got := asModel(m).mode; got != modeBrowse {
		t.Fatalf("Right inside the window should go back to TUNE IN, got %v", got)
	}

	// Right from HELP wraps into AGENT; another Right keeps going to TUNE IN.
	h := browseModel(t)
	h, _ = h.Update(runeKey("?"))
	h, _ = h.Update(keyRight())
	if got := asModel(h).mode; got != modeAgent {
		t.Fatalf("Right from HELP should wrap to AGENT, got %v", got)
	}
	h, _ = h.Update(keyRight())
	if got := asModel(h).mode; got != modeBrowse {
		t.Errorf("a second Right inside the window should continue to TUNE IN, got %v", got)
	}
	// And Left from AGENT inside the window wraps back to HELP.
	h, _ = h.Update(keyLeft()) // TUNE IN -> AGENT
	h, _ = h.Update(keyLeft()) // AGENT -> HELP
	if got := asModel(h).mode; got != modeHelp {
		t.Errorf("Left from AGENT inside the window should wrap to HELP, got %v", got)
	}
}

// Any other key closes the window and goes to the section: in AGENT it types, and
// the arrows that follow are the prompt's own again.
func TestArrowWindowOtherKeyTypes(t *testing.T) {
	freezeNavClock(t)
	m := browseModel(t)
	m, _ = m.Update(keyLeft()) // -> AGENT, window open
	m, _ = m.Update(runeKey("h"))
	am := asModel(m)
	if am.mode != modeAgent {
		t.Fatalf("typing in AGENT must stay in AGENT, got %v", am.mode)
	}
	if !strings.Contains(am.agentIn.Value(), "h") {
		t.Fatalf("the typed key should land in the prompt, got %q", am.agentIn.Value())
	}
	if am.navLive() {
		t.Fatalf("a non-arrow key must close the window")
	}
	m, _ = m.Update(keyRight())
	if got := asModel(m).mode; got != modeAgent {
		t.Errorf("after typing, Right is the prompt's cursor key and must stay in AGENT, got %v", got)
	}
}

// After the window lapses the arrows belong to the section again.
func TestArrowWindowExpires(t *testing.T) {
	advance := freezeNavClock(t)
	m := browseModel(t)
	m, _ = m.Update(keyLeft()) // -> AGENT
	advance(navWindow + 100*time.Millisecond)
	m, _ = m.Update(keyRight())
	if got := asModel(m).mode; got != modeAgent {
		t.Errorf("after %v the window is closed; Right must stay in AGENT, got %v", navWindow, got)
	}
}

// Every arrow inside the window re-opens it, so a long walk never stalls.
func TestArrowWindowRefreshesOnEachStep(t *testing.T) {
	advance := freezeNavClock(t)
	m := browseModel(t)
	m, _ = m.Update(keyLeft()) // TUNE IN -> AGENT
	advance(1500 * time.Millisecond)
	m, _ = m.Update(keyLeft()) // AGENT -> HELP (window re-opened)
	advance(1500 * time.Millisecond)
	m, _ = m.Update(keyLeft()) // HELP -> LOGIN
	if got := asModel(m).mode; got != modeLogin {
		t.Fatalf("expected LOGIN after three Lefts, got %v", got)
	}
	advance(1500 * time.Millisecond)
	m, _ = m.Update(keyLeft()) // LOGIN -> CONFIG
	if got := asModel(m).mode; got != modeLimits {
		t.Errorf("expected CONFIG after four Lefts, got %v", got)
	}
}

// EDGE owns ←/→ for its node map, so it used to stop an arrow walk the same way AGENT
// did. Inside the window the walk carries on to CONFIG; after it, EDGE has its arrows.
func TestArrowWindowPassesThroughEdge(t *testing.T) {
	advance := freezeNavClock(t)
	m := browseModel(t)
	m, _ = m.Update(keyRight()) // TUNE IN -> SHARE
	m, _ = m.Update(keyRight()) // SHARE -> EDGE
	if got := asModel(m).mode; got != modeEdge {
		t.Fatalf("expected EDGE, got %v", got)
	}
	m, _ = m.Update(keyRight())
	if got := asModel(m).mode; got != modeLimits {
		t.Errorf("Right inside the window should carry on from EDGE to CONFIG, got %v", got)
	}
	e := browseModel(t)
	e, _ = e.Update(keyRight())
	e, _ = e.Update(keyRight()) // -> EDGE
	advance(navWindow + time.Second)
	e, _ = e.Update(keyRight())
	if got := asModel(e).mode; got != modeEdge {
		t.Errorf("after the window, Right belongs to EDGE's map and must stay on EDGE, got %v", got)
	}
}

// LOGIN no longer bounces back: Right moves on to HELP even after the window.
func TestLoginArrowsMoveOn(t *testing.T) {
	advance := freezeNavClock(t)
	m := browseModel(t)
	m, _ = m.Update(runeKey("4")) // CONFIG
	m, _ = m.Update(keyRight())   // -> LOGIN
	if got := asModel(m).mode; got != modeLogin {
		t.Fatalf("Right from CONFIG should open LOGIN, got %v", got)
	}
	advance(navWindow + time.Second)
	m, _ = m.Update(keyRight())
	if got := asModel(m).mode; got != modeHelp {
		t.Errorf("Right from LOGIN should move on to HELP, got %v", got)
	}
	m, _ = m.Update(keyLeft()) // HELP -> LOGIN
	advance(navWindow + time.Second)
	m, _ = m.Update(keyLeft())
	if got := asModel(m).mode; got != modeLimits {
		t.Errorf("Left from LOGIN should move back to CONFIG, got %v", got)
	}
	// esc still dismisses to where LOGIN was opened from.
	l := browseModel(t)
	l, _ = l.Update(runeKey("L"))
	l, _ = l.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if got := asModel(l).mode; got != modeBrowse {
		t.Errorf("esc should dismiss LOGIN back to TUNE IN, got %v", got)
	}
}

// The tick keeps the countdown going, ignores ticks from an earlier opening, and
// closes the window when it runs out.
func TestNavTickLifecycle(t *testing.T) {
	advance := freezeNavClock(t)
	m := browseModel(t)
	m, _ = m.Update(keyRight()) // -> SHARE, window open
	seq := asModel(m).navSeq

	nm, cmd := m.Update(navTickMsg{seq: seq - 1})
	if cmd != nil || !asModel(nm).navLive() {
		t.Fatalf("a stale tick must be ignored")
	}
	advance(time.Second)
	nm, cmd = nm.Update(navTickMsg{seq: seq})
	if cmd == nil || !asModel(nm).navLive() {
		t.Fatalf("mid-window tick should keep the window open and schedule the next tick")
	}
	advance(time.Second)
	nm, cmd = nm.Update(navTickMsg{seq: seq})
	if cmd != nil || asModel(nm).navLive() || !asModel(nm).navUntil.IsZero() {
		t.Errorf("the tick at the end of the window should close it")
	}
}

// The preset bar shows the countdown while the window is open, and drops it after.
func TestNavHintCountdown(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	advance := freezeNavClock(t)
	m := browseModel(t)
	if got := stripANSI(asModel(m).presetBar(120)); strings.Contains(got, "←/→ ") {
		t.Fatalf("no countdown before any arrow press:\n%s", got)
	}
	m, _ = m.Update(keyRight())
	if got := stripANSI(asModel(m).presetBar(120)); !strings.Contains(got, "←/→ 2s") {
		t.Errorf("countdown should read 2s right after the jump:\n%s", got)
	}
	advance(1200 * time.Millisecond)
	if got := stripANSI(asModel(m).presetBar(120)); !strings.Contains(got, "←/→ 1s") {
		t.Errorf("countdown should read 1s a second later:\n%s", got)
	}
	advance(time.Second)
	if got := stripANSI(asModel(m).presetBar(120)); strings.Contains(got, "←/→ ") {
		t.Errorf("countdown should be gone once the window lapses:\n%s", got)
	}
	// Compact mode carries it in the hint slot instead.
	c := asModel(browseModel(t))
	c.compact = true
	var cm tea.Model = c
	cm, _ = cm.Update(keyRight())
	if got := stripANSI(asModel(cm).compactHeader(120)); !strings.Contains(got, "←/→ 2s") {
		t.Errorf("compact header should show the countdown:\n%s", got)
	}
}

// `/` opens the palette and the prompt shows it; a second `/` is not doubled, and
// `/search`, `//search` and `search` all run the search.
func TestSlashIsVisibleAndDoubleSlashWorks(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := browseModel(t)
	m, _ = m.Update(runeKey("/"))
	if asModel(m).mode != modeCommand {
		t.Fatalf("/ should open the command palette")
	}
	if got := stripANSI(asModel(m).promptLine(100)); !strings.Contains(got, "rog › /") {
		t.Errorf("the prompt should show the / that opened it: %q", got)
	}
	m, _ = m.Update(runeKey("/")) // the muscle-memory second slash
	for _, r := range "search" {
		m, _ = m.Update(runeKey(string(r)))
	}
	cm := asModel(m)
	if cm.cmd.Value() != "search" {
		t.Errorf("a second leading / should be dropped, value = %q", cm.cmd.Value())
	}
	if got := stripANSI(cm.promptLine(100)); !strings.Contains(got, "rog › /search") || strings.Contains(got, "//") {
		t.Errorf("the prompt should read /search once: %q", got)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := asModel(m).status; !strings.Contains(got, "re-scanning") {
		t.Errorf("enter should run search, status = %q", got)
	}

	// A pasted or typed-through leading slash still runs.
	for _, in := range []string{"/search", "//search", "search"} {
		rm, _ := asModel(browseModel(t)).run(in)
		if got := asModel(rm).status; !strings.Contains(got, "re-scanning") {
			t.Errorf("run(%q) should search, status = %q", in, got)
		}
	}
	// The palette filter ignores the slashes too.
	if got := paletteMatch("//sea"); len(got) == 0 || got[0].name != "/search" {
		t.Errorf("paletteMatch(//sea) should find /search, got %v", got)
	}
}

// restoreColorState snapshots the global color switches and the renderer, and puts
// them back after the test.
func restoreColorState(t *testing.T) {
	t.Helper()
	r := lipgloss.DefaultRenderer()
	oldP, oldD := r.ColorProfile(), r.HasDarkBackground()
	oldTheme, oldDet, oldDetDark, oldCB := themeMode, themeDetected, themeDetectedDark, colorblindMode
	t.Cleanup(func() {
		SetColorblind(oldCB)
		themeMode, themeDetected, themeDetectedDark = oldTheme, oldDet, oldDetDark
		r.SetColorProfile(oldP)
		r.SetHasDarkBackground(oldD)
	})
}

// A color-blind board retints styles that were built at package init (stRed, the
// selection row) without rebuilding them, and "off" restores the full board.
func TestColorblindRetintsPrebuiltStyles(t *testing.T) {
	restoreColorState(t)
	colorOn(t, true) // truecolor, dark arm
	if got := stRed.Render("x"); !strings.Contains(got, rgbLive) {
		t.Fatalf("full board: stRed should be the warm red %s: %q", rgbLive, got)
	}
	for _, mode := range []string{"protan", "deutan"} {
		SetColorblind(mode)
		got := stRed.Render("x")
		if strings.Contains(got, rgbLive) || !strings.Contains(got, "79;157;255") {
			t.Errorf("%s: stRed should turn blue #4F9DFF: %q", mode, got)
		}
		if got := stRowSel.Render("x"); !strings.Contains(got, "48;2;79;157;255") {
			t.Errorf("%s: the selection row background should turn blue: %q", mode, got)
		}
		if got := lampStyle(roleSignal).Render("x"); strings.Contains(got, "132;194;85") {
			t.Errorf("%s: the green signal lamp must not stay green: %q", mode, got)
		}
	}
	SetColorblind("tritan")
	if got := stRed.Render("x"); !strings.Contains(got, rgbLive) {
		t.Errorf("tritan keeps the red accent: %q", got)
	}
	if got := lampStyle(roleDial).Render("x"); strings.Contains(got, rgbDial) {
		t.Errorf("tritan moves the blue info lamp off blue: %q", got)
	}
	SetColorblind("off")
	if got := stRed.Render("x"); !strings.Contains(got, rgbLive) {
		t.Errorf("off restores the warm red: %q", got)
	}
	SetColorblind("nonsense")
	if colorblindMode != "off" {
		t.Errorf("an unknown mode falls back to off, got %q", colorblindMode)
	}
}

// groundPrefix is the SGR a forced theme opens every line with: its ink on the deck.
func groundPrefix() string {
	probe := lipgloss.NewStyle().Foreground(cInk).Background(cDeck).Render("X")
	return probe[:strings.Index(probe, "X")]
}

// The light theme forces the light arm and paints the light page (the deck's paper)
// with dark ink under every line, re-applied after each reset, even in a dark terminal.
func TestLightThemePaintsLightPage(t *testing.T) {
	restoreColorState(t)
	colorOn(t, true) // the terminal says dark; the theme must win
	restore := quiet
	t.Cleanup(func() { quiet = restore })
	quiet = false

	SetTheme("light")
	applyTheme()
	if lipgloss.HasDarkBackground() {
		t.Fatalf("light theme must force the light color arm")
	}
	if got := stRed.Render("x"); !strings.Contains(got, "200;56;26") {
		t.Errorf("light theme keeps the red highlight (light arm #C8391A): %q", got)
	}
	prefix := groundPrefix()
	paper := lipgloss.NewStyle().Background(lipgloss.Color(cDeck.Light)).Render("X")
	paper = paper[:strings.Index(paper, "X")]
	if !strings.Contains(prefix, strings.TrimSuffix(strings.TrimPrefix(paper, "\x1b["), "m")) {
		t.Fatalf("the forced page should be the light paper %q, got %q", paper, prefix)
	}
	m := asModel(browseModel(t))
	w := m.effWidth()
	for i, line := range strings.Split(m.View(), "\n") {
		if !strings.HasPrefix(line, prefix) {
			t.Fatalf("line %d does not open on the light page: %q", i, line)
		}
		if got := lipgloss.Width(line); got != w {
			t.Fatalf("line %d should fill the width %d, got %d", i, w, got)
		}
		rest := line
		for {
			j := strings.Index(rest, "\x1b[0m")
			if j < 0 || j+4 >= len(rest) {
				break
			}
			if !strings.HasPrefix(rest[j+4:], "\x1b[") {
				t.Fatalf("line %d: text after a reset is off the page: %q", i, line)
			}
			rest = rest[j+4:]
		}
	}

	// The page holds even with the deck switched off: the theme needs its ground.
	oldDeck := deckGround
	t.Cleanup(func() { deckGround = oldDeck })
	SetDeck(false)
	if got := m.View(); !strings.HasPrefix(got, prefix) {
		t.Errorf("a forced theme must paint even with the deck off")
	}
	SetDeck(oldDeck)

	// Back to auto restores the detected background; the forced ink is gone.
	SetTheme("auto")
	applyTheme()
	if !lipgloss.HasDarkBackground() {
		t.Errorf("auto should restore the terminal's dark background")
	}
	if got := m.View(); strings.Contains(got, prefix) {
		t.Errorf("auto must not paint the forced light page")
	}
}

// With no color at all a forced theme paints nothing, so headless output is unchanged.
func TestForcedThemeNoopWithoutColor(t *testing.T) {
	restoreColorState(t)
	lipgloss.DefaultRenderer().SetColorProfile(termenv.Ascii)
	themeMode = "light"
	if got := paintDeck("a\nb", 10); got != "a\nb" {
		t.Errorf("Ascii profile must not paint, got %q", got)
	}
}

// /theme and /colorblind switch live, persist through the hooks, cycle when bare,
// and reject unknown values without changing anything.
func TestThemeAndColorblindCommands(t *testing.T) {
	restoreColorState(t)
	colorOn(t, true)
	var savedTheme, savedCB []string
	m := asModel(browseModel(t))
	m.hooks.SaveTheme = func(s string) { savedTheme = append(savedTheme, s) }
	m.hooks.SaveColorblind = func(s string) { savedCB = append(savedCB, s) }
	themeMode = "auto"

	nm, _ := m.run("theme light")
	if themeMode != "light" || lipgloss.HasDarkBackground() {
		t.Fatalf("/theme light should force light (themeMode=%q)", themeMode)
	}
	if !strings.Contains(stripANSI(asModel(nm).status), "theme light") {
		t.Errorf("status should confirm the theme: %q", asModel(nm).status)
	}
	m.run("/theme") // bare cycles light -> dark
	if themeMode != "dark" || !lipgloss.HasDarkBackground() {
		t.Errorf("bare /theme should step light -> dark, got %q", themeMode)
	}
	nm, _ = m.run("theme sepia")
	if themeMode != "dark" || !strings.Contains(stripANSI(asModel(nm).status), "usage") {
		t.Errorf("an unknown theme must be rejected with usage, themeMode=%q", themeMode)
	}
	m.run("theme auto")
	if got := strings.Join(savedTheme, ","); got != "light,dark,auto" {
		t.Errorf("SaveTheme calls = %q, want light,dark,auto", got)
	}

	m.run("colorblind protan")
	if colorblindMode != "protan" || cRed.Dark != boardRedGreen.live.Dark {
		t.Errorf("/colorblind protan should apply the red-green board")
	}
	m.run("colorblind") // bare cycles protan -> deutan
	if colorblindMode != "deutan" {
		t.Errorf("bare /colorblind should step protan -> deutan, got %q", colorblindMode)
	}
	m.run("cb off")
	if colorblindMode != "off" || cRed.Dark != boardFull.live.Dark {
		t.Errorf("/cb off should restore the full board")
	}
	if got := strings.Join(savedCB, ","); got != "protan,deutan,off" {
		t.Errorf("SaveColorblind calls = %q, want protan,deutan,off", got)
	}
	// Both commands are listed in the / palette.
	for _, name := range []string{"/theme", "/colorblind"} {
		if got := paletteMatch(strings.TrimPrefix(name, "/")); len(got) == 0 || got[0].name != name {
			t.Errorf("%s should be in the command palette, got %v", name, got)
		}
	}
}

// The bug behind "it sometimes goes back after 2-5 seconds": entering SHARE starts a
// background model scan, and its result used to switch the screen to SHARE (or the
// setup wizard) even after the operator had arrowed on. A late result must leave the
// screen alone, while a result that lands on SHARE still shows the table / wizard.
func TestLateShareScanDoesNotPullBack(t *testing.T) {
	freezeNavClock(t)
	found := []detect.Found{{Name: "t", Chat: "http://x/v1/chat/completions", Models: []string{"gpt-oss-20b"}}}
	for _, tc := range []struct {
		name  string
		found []detect.Found
	}{{"empty scan", nil}, {"models found", found}} {
		m := browseModel(t)
		m, _ = m.Update(keyRight()) // TUNE IN -> SHARE (scan starts)
		m, _ = m.Update(keyRight()) // SHARE -> EDGE before the scan returns
		if got := asModel(m).mode; got != modeEdge {
			t.Fatalf("%s: expected EDGE, got %v", tc.name, got)
		}
		m, _ = m.Update(sharesDetectedMsg{found: tc.found})
		if got := asModel(m).mode; got != modeEdge {
			t.Errorf("%s: a late scan result pulled the screen back to %v", tc.name, got)
		}
		if asModel(m).shareLoading {
			t.Errorf("%s: the scan result should still clear the loading state", tc.name)
		}
	}
	// Still on SHARE when it lands: the table (or the wizard) shows as before.
	m := browseModel(t)
	m, _ = m.Update(keyRight())
	m, _ = m.Update(sharesDetectedMsg{found: found})
	if got := asModel(m).mode; got != modeShare {
		t.Errorf("a scan that lands on SHARE should show the table, got %v", got)
	}
	e := browseModel(t)
	e, _ = e.Update(keyRight())
	e, _ = e.Update(sharesDetectedMsg{})
	if got := asModel(e).mode; got != modeShareSetup {
		t.Errorf("an empty scan that lands on SHARE should open the setup wizard, got %v", got)
	}
}
