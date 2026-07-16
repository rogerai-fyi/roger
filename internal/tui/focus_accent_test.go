package tui

// Increment 10 (part 2): FOCUS ACCENT. This TUI is deliberately borderless (k9s-style),
// so the "focus accent" is the cDial focus-blue lit on the EXISTING focused-pane seam
// marker (● transcript) rather than a new border - the pane that owns the keyboard reads
// in the dial-blue focus color. Mono collapses it to ink via lampStyle.

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// F1 - while the transcript pane is focused, its seam ● lights the cDial focus-blue.
func TestFocusSeamAccent(t *testing.T) {
	colorOn(t, true)
	var am tea.Model = agentAt(t, permConfirm)
	am, _ = am.Update(tea.KeyMsg{Type: tea.KeyTab}) // focus the transcript pane
	m := asModel(am)
	if !m.agentPaneFocus {
		t.Fatal("tab should focus the transcript pane")
	}
	view := m.View()
	if !strings.Contains(view, lampStyle(roleDial).Render("●")) {
		t.Error("F1: the focused-pane seam ● should light the cDial focus-blue")
	}
	if !strings.Contains(stripANSI(view), "● transcript") {
		t.Error("F1: the seam still reads `● transcript`")
	}
}
