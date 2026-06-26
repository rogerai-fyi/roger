package tui

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rogerai-fyi/roger/internal/detect"
)

// TestShareViewNarrowSafe: the k9s provider table must not overflow at narrow
// widths (it drops the metrics columns under 64 cols, like the band grid).
func TestShareViewNarrowSafe(t *testing.T) {
	for _, w := range []int{40, 50, 64, 80, 120} {
		mm := New("http://broker.local", "tester")
		mm.width, mm.height = w, 30
		mm.mode = modeShare
		mm.shareRows = []shareRow{
			{model: "gpt-oss-20b", ctx: 32768},
			{model: "qwen3-coder-30b-a3b-instruct", ctx: 32768},
		}
		var m tea.Model = mm
		m, _ = m.Update(balanceMsg{balance: 5, loggedIn: true})
		m, _ = m.Update(tickMsg{})
		for _, line := range strings.Split(m.View(), "\n") {
			if vis := utf8.RuneCountInString(stripANSI(line)); vis > w {
				t.Errorf("width %d: share view line overflows (%d cols): %q", w, vis, stripANSI(line))
			}
		}
	}
}

// TestChatFailureIsInline is the regression guard for the founder's silent
// no-response: a failed chat turn must land IN the CHANNEL transcript (not just
// the footer), and an empty reply must show a clear note rather than a blank line.
func TestChatFailureIsInline(t *testing.T) {
	mm := New("http://broker.local", "tester")
	mm.width, mm.height = 100, 30
	mm.connected = &offer{NodeID: "roggentoo", Model: "gpt-oss-20b", Online: true}
	mm.mode = modeChat
	var m tea.Model = mm

	// A failure surfaces inline in the transcript, red ✕, with a concise cause AND the
	// actionable [1] tune in / [2] share next step (not a bare status / dead end).
	m, _ = m.Update(chatErrMsg("the station returned status 504 with no reply"))
	if v := m.View(); !strings.Contains(v, "✕") || !strings.Contains(v, "(504)") {
		t.Errorf("chat failure not surfaced inline with a concise cause:\n%s", v)
	}
	if v := m.View(); !strings.Contains(v, "[1]") || !strings.Contains(v, "[2]") {
		t.Errorf("chat failure should carry the actionable [1]/[2] hint:\n%s", v)
	}

	// An empty reply (no error) shows a clear "(no text)" note, never a blank arrow.
	m, _ = m.Update(chatMsg{reply: "   ", status: "roggentoo · $0"})
	if !strings.Contains(m.View(), "replied with no text") {
		t.Errorf("empty reply should show a note, not a blank line:\n%s", m.View())
	}

	// A real reply still renders.
	m, _ = m.Update(chatMsg{reply: "roger that", status: "roggentoo · $0"})
	if !strings.Contains(m.View(), "roger that") {
		t.Errorf("a real reply should render:\n%s", m.View())
	}
}

// TestChatPreflightNoStation: sending in CHANNEL when no station is on air for the
// band must report it inline immediately, not fire a doomed request silently.
func TestChatPreflightNoStation(t *testing.T) {
	mm := New("http://broker.local", "tester")
	mm.width, mm.height = 100, 30
	mm.connected = &offer{NodeID: "roggentoo", Model: "gpt-oss-20b"}
	mm.mode = modeChat
	mm.chatIn.Focus()
	var m tea.Model = mm
	// type a turn + enter; bands is empty so bandOnAir is false -> inline notice.
	for _, r := range "hello" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if v := m.View(); !strings.Contains(v, "no station is serving gpt-oss-20b right now") {
		t.Errorf("pre-flight no-station notice missing:\n%s", v)
	}
}

// TestShareViewK9s: /share opens the provider table (no silent auto-share); it
// lists detected models with an OFF-AIR status + FREE price, a visible selection
// cursor (the `>` carat under NO_COLOR), and a contextual key footer.
func TestShareViewK9s(t *testing.T) {
	mm := New("http://broker.local", "tester")
	mm.width, mm.height = 100, 30
	mm.mode = modeShare
	mm.shareRows = []shareRow{
		{model: "gpt-oss-20b", ctx: 32768},
		{model: "llama-3.3-70b", ctx: 32768},
	}
	mm.shareCursor = 1
	var m tea.Model = mm
	v := m.View()
	for _, want := range []string{"SHARE", "MODEL", "STATUS", "OFF-AIR", "FREE", "toggle"} {
		if !strings.Contains(v, want) {
			t.Errorf("share view missing %q:\n%s", want, v)
		}
	}
	// The selection carat marks the cursor row (row 1 = llama) under NO_COLOR.
	var caratLine string
	for _, line := range strings.Split(v, "\n") {
		if strings.Contains(line, "llama-3.3-70b") {
			caratLine = line
		}
	}
	if !strings.Contains(stripANSI(caratLine), ">") {
		t.Errorf("selected row should carry the `>` selection carat: %q", stripANSI(caratLine))
	}
}

// asModel unwraps a tea.Model that may be a model value or a *model pointer (the
// onKey dispatch returns either, depending on whether the handler has a value or
// pointer receiver). Tests use it to read fields without caring which it is.
func asModel(m tea.Model) model {
	if p, ok := m.(*model); ok {
		return *p
	}
	return m.(model)
}

// TestDisconnectVsQuit: in a CHANNEL, esc DISCONNECTS (drops the channel, back to
// the band browser) - it does NOT quit the app. Quitting is a deliberate q from
// BROWSE. This is the disconnect-vs-quit contract.
func TestDisconnectVsQuit(t *testing.T) {
	mm := New("http://broker.local", "tester")
	mm.proxyAddr = "127.0.0.1:0"
	mm.width, mm.height = 100, 30
	mm.connected = &offer{NodeID: "nyx", Model: "gpt-oss-20b", Online: true}
	mm.transcript = []string{"hello"}
	mm.mode = modeChat
	mm.chatIn.Focus()
	var m tea.Model = mm

	// esc in a CHANNEL disconnects (no tea.Quit), returns to BROWSE, drops the channel.
	dm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		t.Fatal("esc in a channel must NOT quit the app (it disconnects)")
	}
	dmm := asModel(dm)
	if dmm.connected != nil || dmm.mode != modeBrowse {
		t.Fatalf("esc should disconnect -> browse with no channel, got mode=%v connected=%v", dmm.mode, dmm.connected)
	}
	if !strings.Contains(stripANSI(dm.View()), "disconnected") {
		t.Errorf("disconnect should say so:\n%s", stripANSI(dm.View()))
	}

	// Now from BROWSE (off air, not connected), q quits the app.
	if _, qcmd := dmm.requestQuit(); qcmd == nil {
		t.Fatal("q from BROWSE (off air) should quit the app")
	}
}

// TestSectionToggle: the `s` key flips between the TUNE IN and SHARE sections, and
// the header names the active section so it is never ambiguous you can do both.
func TestSectionToggle(t *testing.T) {
	// Deterministic: pretend one model is detected so s -> the provider table (SHARE).
	old := detectShares
	detectShares = func(extra ...string) ([]detect.Found, []string) {
		return []detect.Found{{Name: "test", BaseURL: "http://x/v1", Chat: "http://x/v1/chat/completions", Models: []string{"gpt-oss-20b"}}}, nil
	}
	defer func() { detectShares = old }()

	mm := New("http://broker.local", "tester")
	mm.width, mm.height = 100, 30
	var m tea.Model = mm
	m, _ = m.Update(balanceMsg{balance: 5, loggedIn: true})
	// Browsing -> header reads TUNE IN.
	if !strings.Contains(stripANSI(m.View()), "TUNE IN") {
		t.Errorf("browse header should show TUNE IN:\n%s", stripANSI(m.View()))
	}
	// s toggles to SHARE (no local model -> guided setup, still the SHARE section).
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	v := stripANSI(m.View())
	if !strings.Contains(v, "SHARE") {
		t.Errorf("s should switch to the SHARE section:\n%s", v)
	}
}

// TestSharePriceEditorLoginGate: opening the per-model price editor requires login.
// Anonymous -> "log in to earn" prompt, no editor. Logged in -> the editor opens
// and a saved price persists via the hook.
func TestSharePriceEditorLoginGate(t *testing.T) {
	// Anonymous: p shows the login gate, editor does NOT open.
	mm := New("http://broker.local", "tester")
	mm.width, mm.height = 100, 30
	mm.mode = modeShare
	mm.shareRows = []shareRow{{model: "gpt-oss-20b", ctx: 32768}}
	var m tea.Model = mm
	m, _ = m.Update(balanceMsg{loggedIn: false})
	pm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if asModel(pm).mode == modeShareEditor {
		t.Fatal("anonymous user must NOT enter the price editor")
	}
	if !strings.Contains(stripANSI(pm.View()), "log in to earn") {
		t.Errorf("anon price attempt should show the login gate:\n%s", stripANSI(pm.View()))
	}

	// Logged in: p opens the editor; typing an out-price + enter saves via the hook.
	var saved struct {
		model string
		p     Pricing
	}
	hooks := Hooks{SavePrice: func(model string, p Pricing) { saved.model, saved.p = model, p }}
	lm := NewWithHooks("http://broker.local", "tester", nil, hooks)
	lm.width, lm.height = 100, 30
	lm.mode = modeShare
	lm.shareRows = []shareRow{{model: "gpt-oss-20b", ctx: 32768}}
	var l tea.Model = lm
	l, _ = l.Update(balanceMsg{balance: 5, loggedIn: true})
	l, _ = l.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	if asModel(l).mode != modeShareEditor {
		t.Fatalf("logged-in p should open the editor, mode=%v", asModel(l).mode)
	}
	ev := stripANSI(l.View())
	for _, want := range []string{"PRICE + SCHEDULE", "input", "output", "time-of-use"} {
		if !strings.Contains(ev, want) {
			t.Errorf("editor view missing %q:\n%s", want, ev)
		}
	}
	// The editor opens on the OUT field; type 0.5 then save.
	for _, r := range "0.5" {
		l, _ = l.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	l, _ = l.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if saved.model != "gpt-oss-20b" || saved.p.Out != 0.5 {
		t.Fatalf("price not saved via hook: %+v", saved)
	}
	// Back in the table, the model now shows the priced cell, not FREE.
	if !strings.Contains(stripANSI(l.View()), "0.50/1M") {
		t.Errorf("priced model should show its $/1M out in the table:\n%s", stripANSI(l.View()))
	}
}

// TestShareScheduleWindow: adding a time-of-use window in the editor records it and
// flags the model in the table; the saved pricing carries the window.
func TestShareScheduleWindow(t *testing.T) {
	var saved Pricing
	hooks := Hooks{SavePrice: func(model string, p Pricing) { saved = p }}
	lm := NewWithHooks("http://broker.local", "tester", nil, hooks)
	lm.width, lm.height = 100, 30
	lm.mode = modeShare
	lm.shareRows = []shareRow{{model: "m", ctx: 8192}}
	var l tea.Model = lm
	l, _ = l.Update(balanceMsg{balance: 5, loggedIn: true})
	l, _ = l.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}}) // open editor
	l, _ = l.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}) // add a window
	if !strings.Contains(stripANSI(l.View()), "18:00-22:00") {
		t.Errorf("added window not shown:\n%s", stripANSI(l.View()))
	}
	l, _ = l.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}}) // flip it FREE
	l, _ = l.Update(tea.KeyMsg{Type: tea.KeyEnter})                     // save
	if len(saved.Windows) != 1 || !saved.Windows[0].Free {
		t.Fatalf("schedule window not saved free: %+v", saved)
	}
}

// TestGuidedFallbackWizard: /share with nothing detected opens the in-TUI guided
// fallback (pick a tool / paste a URL), not a dead-end status line.
func TestGuidedFallbackWizard(t *testing.T) {
	// Force "nothing detected" so the guided fallback opens deterministically (the
	// real detector would scan the host's open ports).
	old := detectShares
	detectShares = func(extra ...string) ([]detect.Found, []string) { return nil, nil }
	defer func() { detectShares = old }()

	mm := New("http://127.0.0.1:1", "tester")
	mm.width, mm.height = 100, 30
	var m tea.Model = mm
	// /share -> guided setup (no model detected). Detection is async now, so runShare
	// drives the returned command. Assert via the view (the model may come back as a
	// value or pointer through the command chain).
	m = runShare(m)
	v := stripANSI(m.View())
	if !strings.Contains(v, "SET UP A MODEL") {
		t.Fatalf("no-detection /share should open the guided setup:\n%s", v)
	}
	for _, want := range []string{"SET UP A MODEL", "Ollama", "LM Studio", "vLLM", "llama.cpp", "Other"} {
		if !strings.Contains(v, want) {
			t.Errorf("guided fallback missing %q:\n%s", want, v)
		}
	}
	// Move to the "Other - paste a URL" row; it becomes a URL input.
	for i := 0; i < len(setupOptions)-1; i++ {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	for _, r := range "127.0.0.1:1" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if !strings.Contains(stripANSI(m.View()), "127.0.0.1:1") {
		t.Errorf("pasted URL should echo in the input:\n%s", stripANSI(m.View()))
	}
	// Enter on a dead endpoint reports it could not verify (stays in the wizard).
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	ev := stripANSI(m.View())
	if !strings.Contains(ev, "SET UP A MODEL") || !strings.Contains(ev, "no OpenAI-compatible server") {
		t.Errorf("dead paste should report unverified and stay in the wizard:\n%s", ev)
	}
}

// TestAllModesRenderSafe: every mode's View renders without panic across narrow +
// wide terminals (the NO_COLOR/non-TTY pipe-safety guard for the new SHARE editor /
// setup screens too), and the SHARE editor/setup bodies stay within the narrow
// width like the rest of the reflow contract.
func TestAllModesRenderSafe(t *testing.T) {
	modes := []mode{
		modeBrowse, modeCommand, modeChat, modeHelp, modeConnectConfirm, modeConnecting,
		modeOverLimit, modeLimits, modeShare, modeShareEditor, modeShareSetup, modeQuitConfirm,
	}
	for _, w := range []int{40, 50, 64, 80, 120} {
		for _, md := range modes {
			mm := New("http://broker.local", "tester")
			mm.width, mm.height = w, 30
			mm.mode = md
			// Seed the bits each mode needs so it renders its real body, not an empty case.
			mm.connected = &offer{NodeID: "nyx", Model: "gpt-oss-20b", PriceOut: 0.3, Online: true}
			mm.q = quote{b: band{model: "gpt-oss-20b", online: true, stations: 1, cheapest: mm.connected}, typical: 800}
			mm.shareRows = []shareRow{{model: "gpt-oss-20b", ctx: 32768}}
			mm.limModels = []string{"gpt-oss-20b"}
			mm.edModel = "gpt-oss-20b"
			mm.edWindows = []SchedWindow{{Start: "18:00", End: "22:00"}}
			// SHARE-section modes are not reached while connected, so don't render the
			// connected header (a pre-existing narrow-header concern, unrelated here).
			if md == modeShare || md == modeShareEditor || md == modeShareSetup {
				mm.connected = nil
				m := tea.Model(mm)
				m, _ = m.Update(balanceMsg{balance: 5, loggedIn: true})
				m, _ = m.Update(tickMsg{})
				out := m.View() // must not panic
				if w <= narrowCols {
					for _, line := range strings.Split(out, "\n") {
						if vis := utf8.RuneCountInString(stripANSI(line)); vis > w {
							t.Errorf("mode %d width %d overflows (%d): %q", md, w, vis, stripANSI(line))
						}
					}
				}
				continue
			}
			var m tea.Model = mm
			m, _ = m.Update(balanceMsg{balance: 5, loggedIn: true})
			m, _ = m.Update(tickMsg{})
			_ = m.View() // must not panic at any width (the pipe-safety guard)
		}
	}
}

// TestBandHighlightCarat: the band browser selection is k9s-grade - the selected
// row carries the `>` carat (NO_COLOR fallback for the reverse-video bar).
func TestBandHighlightCarat(t *testing.T) {
	var m tea.Model = New("http://broker.local", "tester")
	m, _ = m.Update(tea.WindowSizeMsg{Width: 96, Height: 30})
	m, _ = m.Update(offersMsg{
		{NodeID: "a", Model: "gpt-oss-20b", PriceOut: 0, Online: true, FreeNow: true},
		{NodeID: "b", Model: "llama-3.3-70b", PriceOut: 0.41, Online: true},
	})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown}) // cursor -> row 1
	var line string
	for _, l := range strings.Split(m.View(), "\n") {
		if strings.Contains(l, "llama-3.3-70b") {
			line = l
		}
	}
	if !strings.Contains(stripANSI(line), ">") {
		t.Errorf("selected band row should carry the `>` carat: %q", stripANSI(line))
	}
}

// TestScheduleWindowEditEndAndPrice is the P1-3 feature regression: a time-of-use
// window must be able to edit its End time and in/out prices (not just Start), so
// windows don't publish with In=Out=0 unintentionally. We add a window, cycle the
// sub-field with the right arrow, type each value, and assert all of them are saved.
func TestScheduleWindowEditEndAndPrice(t *testing.T) {
	var saved Pricing
	hooks := Hooks{SavePrice: func(model string, p Pricing) { saved = p }}
	lm := NewWithHooks("http://broker.local", "tester", nil, hooks)
	lm.width, lm.height = 100, 30
	lm.mode = modeShare
	lm.shareRows = []shareRow{{model: "m", ctx: 8192}}
	var l tea.Model = lm
	l, _ = l.Update(balanceMsg{balance: 5, loggedIn: true})

	key := func(s string) { l, _ = l.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}) }
	special := func(t tea.KeyType) { l, _ = l.Update(tea.KeyMsg{Type: t}) }

	key("p") // open editor (logged in)
	key("a") // add a window, focus jumps to it (sub-field = Start)

	// Edit START: clear the default "18:00" then type "09:00".
	for i := 0; i < 5; i++ {
		special(tea.KeyBackspace)
	}
	key("0")
	key("9")
	key(":")
	key("0")
	key("0")

	// right -> END sub-field; clear "22:00", type "17:30".
	special(tea.KeyRight)
	for i := 0; i < 5; i++ {
		special(tea.KeyBackspace)
	}
	key("1")
	key("7")
	key(":")
	key("3")
	key("0")

	// right -> IN price; type "0.15".
	special(tea.KeyRight)
	key("0")
	key(".")
	key("1")
	key("5")

	// right -> OUT price; type "0.40".
	special(tea.KeyRight)
	key("0")
	key(".")
	key("4")
	key("0")

	special(tea.KeyEnter) // save

	if len(saved.Windows) != 1 {
		t.Fatalf("expected one saved window, got %+v", saved.Windows)
	}
	w := saved.Windows[0]
	if w.Start != "09:00" || w.End != "17:30" {
		t.Errorf("window times = %q-%q, want 09:00-17:30", w.Start, w.End)
	}
	if w.In != 0.15 || w.Out != 0.40 {
		t.Errorf("window prices in=%v out=%v, want 0.15/0.40 (not 0/0)", w.In, w.Out)
	}
}

// TestNoColorNonTTYRender: every mode must render with NO ANSI escape codes under
// NO_COLOR (the non-TTY / piped path), at width 0 and a narrow width, without panic.
func TestNoColorNonTTYRender(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	modes := []mode{
		modeBrowse, modeCommand, modeChat, modeHelp, modeConnectConfirm, modeConnecting,
		modeOverLimit, modeLimits, modeShare, modeShareEditor, modeShareSetup, modeQuitConfirm,
	}
	for _, w := range []int{0, 30} {
		for _, md := range modes {
			mm := New("http://broker.local", "tester")
			mm.width, mm.height = w, 24
			mm.mode = md
			mm.connected = &offer{NodeID: "nyx", Model: "m", PriceOut: 0.3, Online: true}
			mm.q = quote{b: band{model: "m", online: true, stations: 1, cheapest: mm.connected}, typical: 800}
			mm.shareRows = []shareRow{{model: "m", ctx: 32768}}
			mm.limModels = []string{"m"}
			mm.edModel = "m"
			mm.edWindows = []SchedWindow{{Start: "18:00", End: "22:00", In: 0.1, Out: 0.2}}
			if md == modeShare || md == modeShareEditor || md == modeShareSetup {
				mm.connected = nil
			}
			var m tea.Model = mm
			m, _ = m.Update(balanceMsg{balance: 5, loggedIn: true})
			out := m.View() // must not panic at any width
			if strings.Contains(out, "\x1b[") {
				t.Errorf("mode %d width %d emitted ANSI under NO_COLOR: %q", md, w, out)
			}
		}
	}
}

// TestShareSetupPasteVerifyFailure: in the guided setup, pasting an unreachable URL
// surfaces a verify error and KEEPS the user in the setup wizard (no dead-end, no
// crash, no bogus share).
func TestShareSetupPasteVerifyFailure(t *testing.T) {
	old := detectShares
	detectShares = func(extra ...string) ([]detect.Found, []string) { return nil, nil }
	defer func() { detectShares = old }()

	mm := New("http://127.0.0.1:1", "tester")
	mm.width, mm.height = 100, 30
	var m tea.Model = mm
	m = runShare(m) // -> guided setup (nothing detected, async detection driven)

	// Move to the "Other - paste a URL" row (last option).
	for i := 0; i < len(setupOptions)-1; i++ {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	for _, r := range "http://127.0.0.1:1" { // reliably closed -> Probe fails
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // verify -> fails

	mm2 := asModel(m)
	if mm2.mode != modeShareSetup {
		t.Errorf("a failed paste-verify must stay in the setup wizard, got mode %v", mm2.mode)
	}
	if mm2.setupErr == "" {
		t.Error("a failed paste-verify must surface an error message")
	}
	if !strings.Contains(stripANSI(m.View()), "SET UP A MODEL") {
		t.Errorf("still expected the setup wizard view:\n%s", stripANSI(m.View()))
	}
}

// TestQuitGuardDeclineRestoresMode: declining the on-air quit guard restores the
// mode the user was in when they pressed q (via quitReturn), not a hardcoded one.
func TestQuitGuardDeclineRestoresMode(t *testing.T) {
	mm := New("http://broker.local", "tester")
	mm.width, mm.height = 100, 30
	mm.mode = modeQuitConfirm // the on-air quit guard is showing
	mm.quitReturn = modeShare // requestQuit recorded the prior section before showing it
	dm, dcmd := mm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if dcmd != nil {
		t.Fatal("declining the quit guard must not quit")
	}
	if asModel(dm).mode != modeShare {
		t.Errorf("decline should restore the prior mode (SHARE), got %v", asModel(dm).mode)
	}
}

// TestShareDetectionIsAsync: entering SHARE must NOT block the event loop on
// detection. doShare returns the SHARE table in a LOADING pose plus a tea.Cmd
// (detection runs off the loop); the view shows the animated working spinner + a
// "scanning the band" line; then a sharesDetectedMsg populates the rows and clears
// the loading flag without re-running detection synchronously.
func TestShareDetectionIsAsync(t *testing.T) {
	// detectShares must NOT be called inside Update (it would block the loop). Track it.
	old := detectShares
	called := false
	detectShares = func(extra ...string) ([]detect.Found, []string) {
		called = true
		return []detect.Found{{Name: "test", BaseURL: "http://x/v1", Chat: "http://x/v1/chat/completions", Models: []string{"gpt-oss-20b"}}}, nil
	}
	defer func() { detectShares = old }()

	mm := New("http://broker.local", "tester")
	mm.width, mm.height = 100, 30
	var m tea.Model = mm
	m, _ = m.Update(balanceMsg{balance: 5, loggedIn: true})

	// Press [2] SHARE: immediately in modeShare + LOADING, with a detection cmd queued.
	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	sm := asModel(m)
	if sm.mode != modeShare {
		t.Fatalf("SHARE should enter modeShare at once, got %v", sm.mode)
	}
	if !sm.shareLoading {
		t.Error("SHARE should enter the LOADING state (shareLoading) before detection lands")
	}
	if called {
		t.Error("detection must NOT run synchronously inside Update - it would block the loop")
	}
	if cmd == nil {
		t.Fatal("SHARE should return a tea.Cmd that runs detection off the event loop")
	}
	// While loading, the view shows the working spinner + the scanning line, never rows.
	lv := stripANSI(m.View())
	if !strings.Contains(lv, "scanning the band for local models") {
		t.Errorf("loading view should show the scanning indicator:\n%s", lv)
	}

	// Run the queued command (this is what the runtime does off the loop): it probes
	// and yields a sharesDetectedMsg.
	msg := cmd()
	if _, ok := msg.(sharesDetectedMsg); !ok {
		t.Fatalf("the detection cmd should yield a sharesDetectedMsg, got %T", msg)
	}
	if !called {
		t.Error("running the detection cmd should have invoked detectShares")
	}

	// Folding the message in populates the rows and clears the loading flag.
	m, _ = m.Update(msg)
	dm := asModel(m)
	if dm.shareLoading {
		t.Error("sharesDetectedMsg must clear the loading flag")
	}
	if len(dm.shareRows) != 1 || dm.shareRows[0].model != "gpt-oss-20b" {
		t.Errorf("sharesDetectedMsg should populate the provider rows, got %+v", dm.shareRows)
	}
	if !strings.Contains(stripANSI(m.View()), "gpt-oss-20b") {
		t.Errorf("the settled table should list the detected model:\n%s", stripANSI(m.View()))
	}
}

// TestShareDetectionEmptyEntersWizard: an empty async detection on the INITIAL SHARE
// open lands the guided setup wizard (preserving the old behavior), but only AFTER
// detection, not before.
func TestShareDetectionEmptyEntersWizard(t *testing.T) {
	old := detectShares
	detectShares = func(extra ...string) ([]detect.Found, []string) { return nil, nil }
	defer func() { detectShares = old }()

	mm := New("http://broker.local", "tester")
	mm.width, mm.height = 100, 30
	var m tea.Model = mm

	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	if !asModel(m).shareLoading {
		t.Fatal("SHARE should be loading before the empty result lands")
	}
	// Fold the empty detection result: now (and only now) the wizard opens.
	m, _ = m.Update(cmd())
	wm := asModel(m)
	if wm.shareLoading {
		t.Error("an empty result must clear the loading flag")
	}
	if wm.mode != modeShareSetup {
		t.Errorf("an empty initial detection should open the guided wizard, got %v", wm.mode)
	}
	if !strings.Contains(stripANSI(m.View()), "SET UP A MODEL") {
		t.Errorf("wizard view expected after empty detection:\n%s", stripANSI(m.View()))
	}
}

// TestShareLoadingSpinnerAnimates: the loading indicator reuses the ((•)) working
// spinner and advances with the tick (m.frame), so it visibly animates while
// detection is in flight (not a frozen UI). Under live (non-quiet) rendering the
// spinner phrase rotates across frames.
func TestShareLoadingSpinnerAnimates(t *testing.T) {
	if quiet {
		t.Skip("animation is frozen under NO_COLOR / non-TTY (covered by the static-fallback test)")
	}
	mm := New("http://broker.local", "tester")
	mm.width, mm.height = 100, 30
	mm.mode = modeShare
	mm.shareLoading = true
	seen := map[string]bool{}
	for f := 0; f < 40; f += 8 {
		mm.frame = f
		seen[stripANSI(mm.shareView(100))] = true
	}
	if len(seen) < 2 {
		t.Errorf("loading indicator should animate across frames, got %d distinct frames", len(seen))
	}
}

// TestShareEditorValidationBlocksBadInput: the pricing editor blocks save on a
// malformed HH:MM window, an unparseable price, and an over-ceiling price - each with
// an inline error - instead of silently persisting a dead window / stale price.
func TestShareEditorValidationBlocksBadInput(t *testing.T) {
	// Keep the fat-finger guard off the network (no market signal -> no warn).
	old := marketMedianOut
	marketMedianOut = func(broker, model string) (float64, bool) { return 0, false }
	defer func() { marketMedianOut = old }()

	// Malformed HH:MM window time ("25:99" never matches at runtime).
	t.Run("bad-hhmm", func(t *testing.T) {
		var saved *Pricing
		m := New("http://broker.local", "tester")
		m.hooks = Hooks{SavePrice: func(_ string, p Pricing) { c := p; saved = &c }}
		m.edModel = "m"
		m.edPriceOut = "1"
		m.edWindows = []SchedWindow{{Start: "25:99", End: "03:30"}}
		if m.commitShareEditor() {
			t.Fatal("commit should fail on malformed HH:MM")
		}
		if saved != nil {
			t.Fatal("nothing should be saved when validation fails")
		}
		if !strings.Contains(m.edErr, "HH:MM") {
			t.Errorf("edErr = %q, want an HH:MM message", m.edErr)
		}
	})

	// Unparseable price buffer.
	t.Run("bad-price", func(t *testing.T) {
		m := New("http://broker.local", "tester")
		m.edModel = "m"
		m.edPriceOut = "1.2.3"
		if m.commitShareEditor() {
			t.Fatal("commit should fail on an unparseable price")
		}
		if !strings.Contains(m.edErr, "number") {
			t.Errorf("edErr = %q, want a number-parse message", m.edErr)
		}
	})

	// Over the public output ceiling ($100/1M).
	t.Run("over-ceiling", func(t *testing.T) {
		m := New("http://broker.local", "tester")
		m.edModel = "m"
		m.edPriceOut = "250"
		if m.commitShareEditor() {
			t.Fatal("commit should fail over the $100/1M ceiling")
		}
		if !strings.Contains(m.edErr, "ceiling") {
			t.Errorf("edErr = %q, want a ceiling message", m.edErr)
		}
	})

	// A clean price + valid window saves and clears the error.
	t.Run("clean-saves", func(t *testing.T) {
		var saved *Pricing
		m := New("http://broker.local", "tester")
		m.hooks = Hooks{SavePrice: func(_ string, p Pricing) { c := p; saved = &c }}
		m.edModel = "m"
		m.edPriceOut = "0.7"
		m.edWindows = []SchedWindow{{Start: "03:00", End: "03:30", Free: true}}
		if !m.commitShareEditor() {
			t.Fatalf("clean commit should succeed; edErr=%q", m.edErr)
		}
		if saved == nil || saved.Out != 0.7 || len(saved.Windows) != 1 {
			t.Errorf("saved = %+v, want out 0.7 + one window", saved)
		}
		if m.edErr != "" {
			t.Errorf("edErr should clear on a clean commit, got %q", m.edErr)
		}
	})
}

// TestShareEditorFatFingerWarn: a price far above the market median (>3x) warns on
// commit (the TUI mirror of the CLI softPriceWarn), even though it is under the hard
// ceiling. A normal price does not warn.
func TestShareEditorFatFingerWarn(t *testing.T) {
	old := marketMedianOut
	marketMedianOut = func(broker, model string) (float64, bool) { return 1.0, true } // median $1/1M
	defer func() { marketMedianOut = old }()

	// $50/1M out is 50x the $1 median (under the $100 ceiling) -> warn.
	m := New("http://broker.local", "tester")
	m.edModel = "m"
	m.edPriceOut = "50"
	if !m.commitShareEditor() {
		t.Fatalf("commit should succeed (under ceiling); edErr=%q", m.edErr)
	}
	if !strings.Contains(stripANSI(m.status), "typo") {
		t.Errorf("status should carry the fat-finger warn, got %q", stripANSI(m.status))
	}

	// A normal $2/1M (2x median) does not warn.
	m2 := New("http://broker.local", "tester")
	m2.edModel = "m"
	m2.edPriceOut = "2"
	if !m2.commitShareEditor() {
		t.Fatalf("commit should succeed; edErr=%q", m2.edErr)
	}
	if strings.Contains(stripANSI(m2.status), "typo") {
		t.Errorf("a 2x price should not warn, got %q", stripANSI(m2.status))
	}
}

// TestShareEditorLivePreview: the editor's live-preview line reflects ActivePrice(now)
// - showing FREE inside an all-day free window, the base price with no window.
func TestShareEditorLivePreview(t *testing.T) {
	// No window -> base price.
	m := New("http://broker.local", "tester")
	m.edModel = "m"
	m.edPriceOut = "0.7"
	m.edPriceIn = "0.3"
	prev := stripANSI(m.editorLivePreview())
	if !strings.Contains(prev, "right now you would charge") || !strings.Contains(prev, "base") {
		t.Errorf("base preview = %q, want a base-price line", prev)
	}
	if !strings.Contains(prev, "0.70") {
		t.Errorf("base preview should show the out price, got %q", prev)
	}

	// A FREE window covering exactly the current minute (start = now, end = now+1min)
	// -> FREE right now, regardless of the wall clock.
	now := time.Now().UTC()
	m.edWindows = []SchedWindow{{Start: now.Format("15:04"), End: now.Add(time.Minute).Format("15:04"), Free: true}}
	prev = stripANSI(m.editorLivePreview())
	if !strings.Contains(prev, "FREE") {
		t.Errorf("wrapping free window preview = %q, want FREE", prev)
	}
}

// TestSaveUpstreamHook: loading a freshly verified keyed upstream persists its base
// URL + key via SaveUpstream exactly once, and re-detecting the SAME endpoint does
// NOT rewrite config (only a real change fires the hook).
func TestSaveUpstreamHook(t *testing.T) {
	var savedUp, savedKey string
	var calls int
	hooks := Hooks{SaveUpstream: func(upstream, key string) {
		calls++
		savedUp, savedKey = upstream, key
	}}
	m := NewWithHooks("http://broker.local", "tester", nil, hooks)

	// A newly detected key-protected server -> persisted once.
	keyed := detect.Found{
		Name: "vllm", BaseURL: "http://127.0.0.1:8000/v1",
		Chat: "http://127.0.0.1:8000/v1/chat/completions",
		Models: []string{"qwen3"}, Key: "sk-secret",
	}
	m.loadShareRows([]detect.Found{keyed})
	if calls != 1 || savedUp != "http://127.0.0.1:8000/v1" || savedKey != "sk-secret" {
		t.Fatalf("first load: calls=%d up=%q key=%q, want 1 / base / sk-secret", calls, savedUp, savedKey)
	}

	// Re-detecting the same endpoint+key is a no-op (no config churn).
	m.loadShareRows([]detect.Found{keyed})
	if calls != 1 {
		t.Errorf("re-detecting the same endpoint rewrote config: calls=%d, want 1", calls)
	}

	// A changed key (e.g. the server's key rotated) persists again.
	rotated := keyed
	rotated.Key = "sk-rotated"
	m.loadShareRows([]detect.Found{rotated})
	if calls != 2 || savedKey != "sk-rotated" {
		t.Errorf("rotated key: calls=%d key=%q, want 2 / sk-rotated", calls, savedKey)
	}
}

// TestSaveUpstreamHookSeededNoChurn: a config-seeded upstream/key (ShareUpstream*)
// that re-detects to the same values must NOT rewrite config on the first scan.
func TestSaveUpstreamHookSeededNoChurn(t *testing.T) {
	var calls int
	hooks := Hooks{
		ShareUpstream:    "http://127.0.0.1:8000/v1",
		ShareUpstreamKey: "sk-secret",
		SaveUpstream:     func(upstream, key string) { calls++ },
	}
	m := NewWithHooks("http://broker.local", "tester", nil, hooks)
	m.loadShareRows([]detect.Found{{
		BaseURL: "http://127.0.0.1:8000/v1",
		Chat:    "http://127.0.0.1:8000/v1/chat/completions",
		Models:  []string{"qwen3"}, Key: "sk-secret",
	}})
	if calls != 0 {
		t.Errorf("re-detecting the seeded endpoint should not persist: calls=%d, want 0", calls)
	}
}
