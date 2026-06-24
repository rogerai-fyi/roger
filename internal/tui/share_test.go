package tui

import (
	"strings"
	"testing"
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

	// A failure surfaces inline in the transcript, red ✕, with the broker's reason.
	m, _ = m.Update(chatErrMsg("no node offers gpt-oss-20b"))
	if v := m.View(); !strings.Contains(v, "✕") || !strings.Contains(v, "no node offers gpt-oss-20b") {
		t.Errorf("chat failure not surfaced inline in the transcript:\n%s", v)
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
	if v := m.View(); !strings.Contains(v, "no station on air for gpt-oss-20b") {
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
	detectShares = func(extra ...string) []detect.Found {
		return []detect.Found{{Name: "test", BaseURL: "http://x/v1", Chat: "http://x/v1/chat/completions", Models: []string{"gpt-oss-20b"}}}
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
	detectShares = func(extra ...string) []detect.Found { return nil }
	defer func() { detectShares = old }()

	mm := New("http://127.0.0.1:1", "tester")
	mm.width, mm.height = 100, 30
	var m tea.Model = mm
	// /share -> guided setup (no model detected). Assert via the view (the model may
	// come back as a value or pointer through the command chain).
	m = runCmd(m, "share")
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
		modeBrowse, modeCommand, modeChat, modeHelp, modeConnectConfirm,
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
