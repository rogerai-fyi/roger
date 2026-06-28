package tui

import (
	"strings"
	"testing"
)

// TestPaletteMatchFilters: the / command palette filters by a live substring query (the
// progressive-disclosure core of A.5) - empty lists everything, a prefix narrows, case is
// ignored, and a miss is empty.
func TestPaletteMatchFilters(t *testing.T) {
	if all := paletteMatch(""); len(all) < 8 {
		t.Errorf("empty query should list the full palette, got %d", len(all))
	}
	names := map[string]bool{}
	for _, c := range paletteMatch("co") {
		names[c.name] = true
	}
	for _, want := range []string{"/connect", "/compact", "/config", "/confidential"} {
		if !names[want] {
			t.Errorf("query 'co' should match %s", want)
		}
	}
	if names["/quit"] {
		t.Error("query 'co' should NOT match /quit")
	}
	if got := paletteMatch("zzzzzz"); len(got) != 0 {
		t.Errorf("a no-match query should be empty, got %d", len(got))
	}
	if len(paletteMatch("CONN")) == 0 {
		t.Error("matching must be case-insensitive")
	}
}

// TestCommandModeRendersPalette: while typing a command, the View surfaces the filtered palette
// (command + description) - and filters out non-matches.
func TestCommandModeRendersPalette(t *testing.T) {
	m := seedFor(120, modeCommand, false)
	m.cmd.SetValue("co")
	v := stripANSI(m.View())
	for _, want := range []string{"/connect", "/compact", "/confidential"} {
		if !strings.Contains(v, want) {
			t.Errorf("command palette should show %s while typing 'co':\n%s", want, v)
		}
	}
	if strings.Contains(v, "/quit") {
		t.Error("palette should filter OUT /quit for query 'co'")
	}
}

// TestPaletteViewBranches covers paletteView's overflow ('+N more') and empty-match branches.
func TestPaletteViewBranches(t *testing.T) {
	m := seedFor(120, modeCommand, false)
	m.cmd.SetValue("") // empty query -> all 16 entries, capped at 8 -> a "+N more" footer
	if v := stripANSI(m.paletteView(120)); !strings.Contains(v, "more") {
		t.Errorf("empty query should overflow with a '+N more' footer:\n%s", v)
	}
	m.cmd.SetValue("zzzzzz") // no match
	if v := stripANSI(m.paletteView(120)); !strings.Contains(v, "no command matches") {
		t.Errorf("a no-match query should say 'no command matches':\n%s", v)
	}
}
