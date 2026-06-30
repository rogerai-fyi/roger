package tui

import (
	"strings"
	"testing"
)

// TestChatKeybarShiftTabLabel: the agent shortcut in the chat keybar must read as a
// plain "shift-tab" label, NOT the ⇧⇥ glyph pair - those render as garbled boxes in
// many terminal fonts, and the founder couldn't tell what key it meant.
func TestChatKeybarShiftTabLabel(t *testing.T) {
	m := New("http://broker.local", "tester")
	m.width, m.height = 100, 40
	m.mode = modeChat
	foot := stripANSI(m.footer(100))
	if !strings.Contains(foot, "shift-tab") {
		t.Fatalf("chat keybar should label the agent shortcut 'shift-tab', got:\n%s", foot)
	}
	if strings.Contains(foot, "⇧⇥") {
		t.Errorf("chat keybar must not use the confusing ⇧⇥ glyph, got:\n%s", foot)
	}
}

// TestViewFillsTerminalHeight: a SHORT frame (here the share "scanning" state) must
// repaint the FULL terminal height, so under the alt-screen renderer it fully
// overwrites a TALLER previous frame (a long model list that overflowed a small
// terminal) instead of leaving ghost remnants - the duplicated brand / header /
// "scanning…" the founder hit after going on-air. Guarded on height>0.
func TestViewFillsTerminalHeight(t *testing.T) {
	m := New("http://broker.local", "tester")
	m.width, m.height = 100, 40
	m.mode = modeShare
	m.shareLoading = true
	got := strings.Count(m.View(), "\n") + 1
	if got != 40 {
		t.Fatalf("View() must fill the %d-line terminal so a short frame overwrites a taller one; got %d lines", m.height, got)
	}
}

// TestViewNoPadWhenHeightUnknown: before the first WindowSizeMsg (height 0, e.g. a
// headless test) View() must NOT pad to a fixed height - it stays its natural length,
// so existing tests keep their exact, unpadded output.
func TestViewNoPadWhenHeightUnknown(t *testing.T) {
	m := New("http://broker.local", "tester")
	m.width, m.height = 100, 0
	m.mode = modeShare
	m.shareLoading = true
	got := strings.Count(m.View(), "\n") + 1
	if got >= 40 {
		t.Fatalf("with height unknown, View() must not pad to a tall fixed height; got %d lines", got)
	}
}
