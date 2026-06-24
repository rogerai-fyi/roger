package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

// keyMsg is a tiny helper to feed a single-rune key press to Update.
func keyMsg(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// browseSeed builds a model in BROWSE with a couple of bands + a logged-in balance,
// sized to w, so the compact tests exercise the band table, header, and footer.
func browseSeed(w int) model {
	var m tea.Model = New("http://broker.local", "tester")
	m, _ = m.Update(tea.WindowSizeMsg{Width: w, Height: 30})
	m, _ = m.Update(offersMsg{
		{NodeID: "demo-node", Region: "home", Model: "gpt-oss-20b", PriceIn: 0.2, PriceOut: 0.3, Online: true, TPS: 62, Confidential: true},
		{NodeID: "alt-node", Region: "us-w", Model: "llama-3.3-70b-instruct", PriceIn: 0.25, PriceOut: 0.41, Online: true, TPS: 40},
	})
	m, _ = m.Update(balanceMsg{balance: 42.17, loggedIn: true})
	m, _ = m.Update(tickMsg{})
	return m.(model)
}

// TestCompactToggle: the [m] key flips compact on and off in BROWSE (a non-text-entry
// context), and round-trips full<->compact.
func TestCompactToggle(t *testing.T) {
	m := browseSeed(100)
	if m.compact {
		t.Fatalf("compact should default off")
	}
	var tm tea.Model = m
	tm, _ = tm.Update(keyMsg("m"))
	if !asModel(tm).compact {
		t.Fatalf("m should turn compact ON")
	}
	tm, _ = tm.Update(keyMsg("m"))
	if asModel(tm).compact {
		t.Fatalf("m should round-trip back to full (OFF)")
	}
}

// TestCompactToggleInShareAndLimits: [m] also toggles compact from the SHARE table and
// the limits/CONFIG screen (both non-text-entry contexts that dispatch via the preset
// bank), so the windowshade is reachable everywhere it should be.
func TestCompactToggleInShareAndLimits(t *testing.T) {
	// SHARE table.
	m := browseSeed(100)
	m.mode = modeShare
	m.shareRows = []shareRow{{model: "gpt-oss-20b", ctx: 32768}}
	var tm tea.Model = m
	tm, _ = tm.Update(keyMsg("m"))
	if !asModel(tm).compact {
		t.Errorf("m should toggle compact from the SHARE table")
	}
	// Limits / CONFIG (not editing a field).
	l := browseSeed(100)
	l.mode = modeLimits
	l.editField = -1
	l.limModels = []string{"gpt-oss-20b"}
	var lm tea.Model = l
	lm, _ = lm.Update(keyMsg("m"))
	if !asModel(lm).compact {
		t.Errorf("m should toggle compact from the limits screen")
	}
}

// TestCompactNotStolenWhileTyping: [m] must NOT toggle compact while typing - in the
// command palette, the chat input, or a numeric editor - it has to reach the text
// input as a literal keystroke instead.
func TestCompactNotStolenWhileTyping(t *testing.T) {
	// Command palette: m is typed into the command buffer, compact stays off.
	m := browseSeed(100)
	m.mode = modeCommand
	m.cmd.Focus()
	var tm tea.Model = m
	tm, _ = tm.Update(keyMsg("m"))
	if asModel(tm).compact {
		t.Errorf("m must not toggle compact while in the command palette")
	}
	if !strings.Contains(asModel(tm).cmd.Value(), "m") {
		t.Errorf("m should be typed into the command buffer, got %q", asModel(tm).cmd.Value())
	}

	// Chat input: m is a chat keystroke, not a toggle.
	c := browseSeed(100)
	c.mode = modeChat
	c.connected = &offer{NodeID: "n", Model: "gpt-oss-20b", Online: true}
	c.chatIn.Focus()
	var cm tea.Model = c
	cm, _ = cm.Update(keyMsg("m"))
	if asModel(cm).compact {
		t.Errorf("m must not toggle compact while typing in a channel")
	}

	// Numeric price editor: m is ignored as a digit, never a toggle.
	e := browseSeed(100)
	e.mode = modeShareEditor
	e.edModel = "gpt-oss-20b"
	var em tea.Model = e
	em, _ = em.Update(keyMsg("m"))
	if asModel(em).compact {
		t.Errorf("m must not toggle compact inside the price/schedule editor")
	}
}

// TestCompactHeaderOneLine: the compact header is exactly ONE dense strip plus a
// single hairline rule (two lines total in the header block), with no big banner.
func TestCompactHeaderOneLine(t *testing.T) {
	m := browseSeed(100)
	m.compact = true
	head := m.compactHeader(100)
	lines := strings.Split(strings.TrimRight(head, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("compact header should be one strip + one rule (2 lines), got %d:\n%s", len(lines), head)
	}
	plain := stripANSI(lines[0])
	if strings.Contains(plain, "R O G E R") {
		t.Errorf("compact header must not carry the big spaced banner:\n%s", plain)
	}
	if !strings.Contains(plain, "ROGER") || !strings.Contains(plain, "m:expand") {
		t.Errorf("compact strip should carry the brand + m:expand hint:\n%s", plain)
	}
	// The strip never overflows the width.
	if vis := utf8.RuneCountInString(plain); vis > 100 {
		t.Errorf("compact strip overflows: %d cols\n%s", vis, plain)
	}
}

// TestCompactStaticAnimation: in compact mode the rendered frame must NOT change as
// the carrier beat advances - it is genuinely static (the app's reduced-motion). The
// full (non-compact) view, by contrast, animates between frames.
func TestCompactStaticAnimation(t *testing.T) {
	t.Setenv("NO_COLOR", "1") // assert on the stable, color-free render

	// Compact: two very different frames must produce identical output.
	a := browseSeed(100)
	a.compact = true
	a.frame = 1
	b := a
	b.frame = 999
	if a.View() != b.View() {
		t.Errorf("compact render changed across frames - it must be static:\n--- f1 ---\n%s\n--- f999 ---\n%s", a.View(), b.View())
	}

	// In-flight relay under compact: the spinner is the static (•) glyph, never the
	// animated ((•)) rings, and stays identical across frames.
	r := browseSeed(100)
	r.compact = true
	r.mode = modeChat
	r.connected = &offer{NodeID: "n", Model: "gpt-oss-20b", Online: true, TPS: 62}
	r.relaying = true
	r.frame = 1
	r2 := r
	r2.frame = 777
	v1, v2 := r.View(), r2.View()
	if v1 != v2 {
		t.Errorf("compact relay spinner animated across frames:\n%s\n---\n%s", v1, v2)
	}
	if !strings.Contains(stripANSI(v1), "(•)") {
		t.Errorf("compact relay should render the static (•) spinner glyph:\n%s", stripANSI(v1))
	}
	if strings.Contains(stripANSI(v1), "((") {
		t.Errorf("compact relay must not render the animated (( rings:\n%s", stripANSI(v1))
	}
}

// seedFor builds a compact model in mode md, sized to w, with the per-mode fixtures.
func seedFor(w int, md mode, compact bool) model {
	m := browseSeed(w)
	m.compact = compact
	m.connected = &offer{NodeID: "nyx-home", Model: "gpt-oss-20b", PriceOut: 0.3, Online: true, TPS: 62}
	m.endpoint = "http://127.0.0.1:4141/v1"
	m.apikey = "roger-local"
	m.mode = md
	switch md {
	case modeShare:
		m.shareRows = []shareRow{{model: "gpt-oss-20b", ctx: 32768}, {model: "qwen3-coder-30b", ctx: 65536}}
	case modeLimits:
		m.editField = -1
		m.limModels = []string{"gpt-oss-20b"}
	case modeChat:
		m.transcript = []string{"hello", "world"}
	}
	return m
}

// maxLineWidth is the widest visible (ANSI-stripped) line in a render.
func maxLineWidth(out string) int {
	max := 0
	for _, line := range strings.Split(out, "\n") {
		if v := utf8.RuneCountInString(stripANSI(line)); v > max {
			max = v
		}
	}
	return max
}

// TestCompactNarrowSafe: the densified compact surfaces (the band table + the SHARE
// provider table) render in compact + NO_COLOR across widths 40-120 with no ANSI, no
// overflow, and a visible selection cursor on the band table.
func TestCompactNarrowSafe(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	for _, w := range []int{40, 50, 64, 80, 100, 120} {
		for _, md := range []mode{modeBrowse, modeShare} {
			out := seedFor(w, md, true).View()
			if strings.Contains(out, "\x1b[") {
				t.Errorf("width %d mode %v emitted ANSI under NO_COLOR", w, md)
			}
			for _, line := range strings.Split(out, "\n") {
				if vis := utf8.RuneCountInString(stripANSI(line)); vis > w {
					t.Errorf("width %d mode %v line overflows (%d cols): %q", w, md, vis, stripANSI(line))
				}
			}
			if md == modeBrowse && !strings.Contains(stripANSI(out), ">") {
				t.Errorf("width %d compact browse lost the selection carat:\n%s", w, stripANSI(out))
			}
		}
	}
}

// TestCompactNeverWiderThanFull: across every mode and width, the compact render emits
// no ANSI under NO_COLOR and is never WIDER than the full render - so compact is a
// strict width win (the channel + limits views have pre-existing wide elements at the
// smallest widths that are out of the windowshade's scope, but compact never makes
// them worse, and densifies the rest).
func TestCompactNeverWiderThanFull(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	modes := []mode{modeBrowse, modeChat, modeShare, modeLimits, modeHelp}
	for _, w := range []int{40, 64, 80, 120} {
		for _, md := range modes {
			full := seedFor(w, md, false).View()
			comp := seedFor(w, md, true).View()
			if strings.Contains(comp, "\x1b[") {
				t.Errorf("width %d mode %v: compact emitted ANSI under NO_COLOR", w, md)
			}
			if cw, fw := maxLineWidth(comp), maxLineWidth(full); cw > fw {
				t.Errorf("width %d mode %v: compact (%d cols) wider than full (%d cols)", w, md, cw, fw)
			}
		}
	}
}

// TestCompactPersist: pressing [m] calls the host SaveCompact hook with the new value,
// and the seeded Hooks.Compact restores the choice at launch (sticks across sessions).
func TestCompactPersist(t *testing.T) {
	var saved bool
	var savedCalled bool
	h := Hooks{
		Compact:     true, // seeded from a previous session -> launches compact
		SaveCompact: func(on bool) { saved = on; savedCalled = true },
	}
	m := NewWithHooks("http://broker.local", "tester", nil, h)
	if !m.compact {
		t.Fatalf("Hooks.Compact=true should seed the model into compact mode")
	}
	var tm tea.Model = m
	m2, _ := tm.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m2, _ = m2.Update(keyMsg("m")) // toggle OFF
	if !savedCalled {
		t.Fatalf("toggling compact should call SaveCompact")
	}
	if saved {
		t.Errorf("SaveCompact should have persisted false after the toggle, got true")
	}
	if asModel(m2).compact {
		t.Errorf("model should be expanded after the toggle")
	}
}

// TestCompactHelpStatic: the help screen lists `m compact`, renders without panic or
// ANSI under NO_COLOR, and is static across frames in compact (Ping is frozen).
func TestCompactHelpStatic(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := browseSeed(100)
	m.compact = true
	m.mode = modeHelp
	m.frame = 1
	b := m
	b.frame = 333
	if m.View() != b.View() {
		t.Errorf("compact help animated across frames (Ping must be frozen):\n%s\n---\n%s", m.View(), b.View())
	}
	out := stripANSI(m.View())
	if strings.Contains(m.View(), "\x1b[") {
		t.Errorf("compact help emitted ANSI under NO_COLOR")
	}
	if !strings.Contains(out, "m") || !strings.Contains(strings.ToLower(out), "compact") {
		t.Errorf("help should advertise the m compact toggle:\n%s", out)
	}
}
