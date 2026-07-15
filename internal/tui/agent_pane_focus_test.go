package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestAgentPaneFocus locks the two-pane focus model: tab hands the keyboard to the
// transcript (highlighted seam, arrows scroll), esc/tab/typing hand it back, and the
// slash-completion + input-history behaviors survive on the input side.
func TestAgentPaneFocus(t *testing.T) {
	base := browseSeed(120)
	base.connected = &offer{NodeID: "n", Model: "gpt-oss-20b", Online: true}
	nm, _ := base.enterAgent()
	am := asModel(nm)
	for i := 0; i < 80; i++ {
		am.agentLines = append(am.agentLines, "LINE")
	}
	am.agentIn.Focus()

	// Tab from a non-slash input focuses the transcript.
	var m tea.Model = am
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if !asModel(m).agentPaneFocus {
		t.Fatal("tab should focus the transcript pane")
	}
	// The seam lights up as the focus cue.
	if !strings.Contains(stripANSI(m.View()), "● transcript") {
		t.Error("focused pane should render the lit seam cue")
	}
	// Arrows scroll, not recall, while the pane is focused.
	mm := asModel(m)
	mm.agentHist.add("older")
	m = mm
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if got := asModel(m).agentIn.Value(); got != "" {
		t.Errorf("up with the pane focused must scroll, not recall; input became %q", got)
	}
	// Esc returns the keyboard to the input (and does NOT leave AGENT).
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	got := asModel(m)
	if got.agentPaneFocus || got.mode != modeAgent {
		t.Fatalf("esc should refocus the input and stay in AGENT (paneFocus=%v mode=%v)", got.agentPaneFocus, got.mode)
	}

	// Type-through: focusing the pane then typing a rune snaps back and types it.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	got = asModel(m)
	if got.agentPaneFocus || got.agentIn.Value() != "h" {
		t.Fatalf("typing should snap focus back to the input and type (paneFocus=%v input=%q)", got.agentPaneFocus, got.agentIn.Value())
	}

	// A slash word keeps tab as completion, never a pane toggle.
	mm = asModel(m)
	mm.agentIn.SetValue("/he")
	m = mm
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	got = asModel(m)
	if got.agentPaneFocus {
		t.Error("tab on a slash word must slash-complete, not switch panes")
	}
	if !strings.HasPrefix(got.agentIn.Value(), "/help") {
		t.Errorf("tab should complete /he -> /help, got %q", got.agentIn.Value())
	}
}
