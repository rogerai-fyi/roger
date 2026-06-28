package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func altM() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}, Alt: true} }

// TestAltMMinimizesFromAnywhere: the founder's "press m from anywhere to minimize" - alt+m is
// the typing-safe global that toggles the dense compact windowshade from EVERY mode, including
// the text-entry ones (chat / AGENT / command palette) where plain m is a literal character.
func TestAltMMinimizesFromAnywhere(t *testing.T) {
	for _, md := range []mode{modeBrowse, modeChat, modeAgent, modeCommand, modeShare, modeLimits} {
		m := seedFor(120, md, false)
		if m.compact {
			t.Fatalf("mode %v precondition: should start expanded", md)
		}
		out, _ := m.Update(altM())
		if !asModel(out).compact {
			t.Errorf("alt+m in mode %v should minimize (compact=true)", md)
		}
	}
}

// TestAltMDoesNotTypeIntoChat: alt+m must be intercepted globally, never inserted as 'm' into
// a focused chat input (the whole reason it's alt-chorded, not plain m, in text-entry modes).
func TestAltMDoesNotTypeIntoChat(t *testing.T) {
	m := seedFor(120, modeChat, false)
	m.chatIn.SetValue("")
	m.chatIn.Focus()
	out, _ := m.Update(altM())
	om := asModel(out)
	if om.chatIn.Value() != "" {
		t.Errorf("alt+m must not type 'm' into the chat input; got %q", om.chatIn.Value())
	}
	if !om.compact {
		t.Error("alt+m from chat should still minimize")
	}
}

// TestAltMRestoresFromCompact: minimize toggles - alt+m from compact expands back.
func TestAltMRestoresFromCompact(t *testing.T) {
	m := seedFor(120, modeChat, true)
	if !m.compact {
		t.Fatal("precondition: should start compact")
	}
	out, _ := m.Update(altM())
	if asModel(out).compact {
		t.Error("alt+m from compact should restore (expand)")
	}
}

// TestSlashCompactToggles: /compact + /min minimize from a channel, and the palette verb works
// too - the discoverable, typing-safe routes alongside alt+m.
func TestSlashCompactToggles(t *testing.T) {
	for _, v := range []string{"/compact", "/min"} {
		out, _ := seedFor(120, modeChat, false).runSession(v)
		if !asModel(out).compact {
			t.Errorf("%s should minimize to compact", v)
		}
	}
	if out, _ := seedFor(120, modeBrowse, false).run("compact"); !asModel(out).compact {
		t.Error("palette 'compact' should minimize")
	}
}

// TestPlainMStillMinimizesInBrowse: regression - the convenient single-key m must keep working
// on the nav screens (the presetForKey path), unchanged by adding the alt+m global.
func TestPlainMStillMinimizesInBrowse(t *testing.T) {
	out, _ := seedFor(120, modeBrowse, false).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	if !asModel(out).compact {
		t.Error("plain m in browse should still minimize (presetForKey path)")
	}
}

// ---------------------------------------------------------------------------
// MP3-player polish: the compact windowshade's spectrum/EQ strip
// ---------------------------------------------------------------------------

// TestMiniSpectrumMapping: 0..100 signal scores map to the 8 block bars, always exactly n
// runes, low/missing channels read as the floor bar, and out-of-range clamps.
func TestMiniSpectrumMapping(t *testing.T) {
	if got := miniSpectrum([]int{0, 50, 100}, 3); got != "▁▄█" {
		t.Errorf("miniSpectrum([0 50 100],3) = %q, want %q", got, "▁▄█")
	}
	if got := miniSpectrum([]int{90}, 4); got != "▇▁▁▁" { // one hot bar, then floor padding
		t.Errorf("miniSpectrum([90],4) = %q, want %q (floor-padded)", got, "▇▁▁▁")
	}
	if got := miniSpectrum(nil, 5); len([]rune(got)) != 5 {
		t.Errorf("miniSpectrum(nil,5) should be exactly 5 floor bars, got %q", got)
	}
	if miniSpectrum(nil, 0) != "" {
		t.Error("miniSpectrum(_,0) should be empty")
	}
	if got := miniSpectrum([]int{150, -20}, 2); got != "█▁" { // clamp high->█, low->floor
		t.Errorf("miniSpectrum clamp = %q, want %q", got, "█▁")
	}
}

// TestCompactHeaderShowsSpectrumWideDropsNarrow: the visualizer pane (▕…▏) appears in a wide
// windowshade and is dropped on a tight one - and the strip never overflows the width.
func TestCompactHeaderShowsSpectrumWideDropsNarrow(t *testing.T) {
	m := seedFor(120, modeBrowse, true) // compact
	wide := stripANSI(m.compactHeader(120))
	firstLine := strings.SplitN(wide, "\n", 2)[0]
	if !strings.Contains(firstLine, "▕") || !strings.Contains(firstLine, "▏") {
		t.Errorf("wide compact header should show the visualizer pane ▕…▏:\n%s", firstLine)
	}
	if vis := lipgloss.Width(strings.SplitN(m.compactHeader(120), "\n", 2)[0]); vis > 120 {
		t.Errorf("wide compact header overflows: %d > 120", vis)
	}
	narrow := strings.SplitN(stripANSI(m.compactHeader(34)), "\n", 2)[0]
	if strings.Contains(narrow, "▕") {
		t.Errorf("narrow compact header should DROP the visualizer to fit:\n%s", narrow)
	}
	if vis := lipgloss.Width(strings.SplitN(m.compactHeader(34), "\n", 2)[0]); vis > 34 {
		t.Errorf("narrow compact header overflows: %d > 34", vis)
	}
}

// TestTintSpectrumTwoTone covers the EQ two-tone branch: hot peaks (▆▇█) glow via stLive, the
// rest stay stDim. Asserted as the exact transform (color-independent under a NO_COLOR test).
func TestTintSpectrumTwoTone(t *testing.T) {
	if got, want := tintSpectrum("▇▁"), stLive.Render("▇")+stDim.Render("▁"); got != want {
		t.Errorf("tintSpectrum two-tone = %q, want peak=stLive floor=stDim (%q)", got, want)
	}
	if got, want := tintSpectrum("█▆▅"), stLive.Render("█")+stLive.Render("▆")+stDim.Render("▅"); got != want {
		t.Errorf("tintSpectrum = %q, want █▆ as peaks (stLive), ▅ dim (%q)", got, want)
	}
}

// TestTopSignalsSortsAndCaps covers the truncation branch: only on-air bands, strongest first,
// capped at n.
func TestTopSignalsSortsAndCaps(t *testing.T) {
	var offers []offer
	for i := 0; i < 12; i++ {
		offers = append(offers, offer{Online: true, Signal: i * 7}) // 0,7,...,77
	}
	offers = append(offers, offer{Online: false, Signal: 100}) // offline: excluded
	got := topSignals(offers, 8)
	if len(got) != 8 {
		t.Fatalf("topSignals should cap at 8, got %d", len(got))
	}
	if got[0] != 77 || got[1] != 70 {
		t.Errorf("topSignals should be strongest-first, got %v", got[:2])
	}
	for i := 1; i < len(got); i++ {
		if got[i] > got[i-1] {
			t.Errorf("topSignals not sorted descending at %d: %v", i, got)
		}
	}
}
