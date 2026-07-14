package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// The transcript scroll contract: in the CHANNEL chat and the AGENT view the
// response/transcript area is an INDEPENDENT scroll region (a viewport) from the
// text-input box. A reply taller than the pane can be scrolled (PgUp/PgDn, Ctrl+U/D,
// mouse wheel, and arrow keys once history is exhausted) without breaking typing,
// send, or the input's Up-arrow command-history recall. New output sticks to the
// bottom only while the user is already at the bottom.

func keyPgUp() tea.KeyMsg   { return tea.KeyMsg{Type: tea.KeyPgUp} }
func keyPgDown() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyPgDown} }
func wheelUp() tea.MouseMsg {
	return tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp}
}
func wheelDown() tea.MouseMsg {
	return tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown}
}

// tallChat builds a connected CHANNEL model with n numbered transcript lines, sized so
// the viewport can show only a fraction of them.
func tallChat(t *testing.T, n int) tea.Model {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	mm := New("http://broker.local", "tester")
	mm.connected = &offer{NodeID: "nyx", Model: "gpt-oss-20b", Online: true}
	mm.offers = []offer{{NodeID: "nyx", Model: "gpt-oss-20b", Online: true}}
	mm.bands = []band{{model: "gpt-oss-20b", online: true}}
	mm.mode = modeChat
	mm.chatIn.Focus()
	for i := 0; i < n; i++ {
		mm.transcript = append(mm.transcript, fmt.Sprintf("LINE-%03d", i))
	}
	var m tea.Model = mm
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	return m
}

func tallAgent(t *testing.T, n int) tea.Model {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	mm := New("http://broker.local", "tester")
	mm.mode = modeAgent
	mm.agentIn.Focus()
	for i := 0; i < n; i++ {
		mm.agentLines = append(mm.agentLines, fmt.Sprintf("LINE-%03d", i))
	}
	var m tea.Model = mm
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	return m
}

// TestChatTranscriptStartsAtBottom: a transcript taller than the pane shows the most
// recent lines (auto-stuck to bottom), not the first lines.
func TestChatTranscriptStartsAtBottom(t *testing.T) {
	m := tallChat(t, 60)
	v := asModel(m).View()
	if !strings.Contains(v, "LINE-059") {
		t.Fatalf("expected the newest line to be visible at the bottom; view:\n%s", v)
	}
	if strings.Contains(v, "LINE-000") {
		t.Fatalf("the oldest line must be scrolled off the top, but it is visible")
	}
}

// TestChatPgUpRevealsEarlierLines: PgUp scrolls the transcript up to reveal earlier
// lines that were off the top.
func TestChatPgUpRevealsEarlierLines(t *testing.T) {
	m := tallChat(t, 60)
	// Page up enough times to reach the very top.
	for i := 0; i < 10; i++ {
		m, _ = m.Update(keyPgUp())
	}
	v := asModel(m).View()
	if !strings.Contains(v, "LINE-000") {
		t.Fatalf("PgUp should reveal the oldest line; view:\n%s", v)
	}
	if strings.Contains(v, "LINE-059") {
		t.Fatalf("after paging to the top the newest line should be off-screen")
	}
	// PgDown returns toward the bottom.
	for i := 0; i < 10; i++ {
		m, _ = m.Update(keyPgDown())
	}
	v = asModel(m).View()
	if !strings.Contains(v, "LINE-059") {
		t.Fatalf("PgDown should return to the bottom; view:\n%s", v)
	}
}

// TestChatMouseWheelScrolls: the mouse wheel scrolls the transcript region.
func TestChatMouseWheelScrolls(t *testing.T) {
	m := tallChat(t, 60)
	for i := 0; i < 20; i++ {
		m, _ = m.Update(wheelUp())
	}
	v := asModel(m).View()
	if !strings.Contains(v, "LINE-000") {
		t.Fatalf("mouse wheel up should reveal the oldest line; view:\n%s", v)
	}
	for i := 0; i < 40; i++ {
		m, _ = m.Update(wheelDown())
	}
	v = asModel(m).View()
	if !strings.Contains(v, "LINE-059") {
		t.Fatalf("mouse wheel down should return to the bottom; view:\n%s", v)
	}
}

// TestChatAutoStickOnlyAtBottom: new output auto-scrolls to the bottom ONLY when the
// user is already at the bottom; if they scrolled up, their position holds.
func TestChatAutoStickOnlyAtBottom(t *testing.T) {
	m := tallChat(t, 60)
	// At bottom: appending a new line and pumping an update sticks to it.
	mm := asModel(m)
	mm.transcript = append(mm.transcript, "FRESH-AT-BOTTOM")
	m = mm
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	if !strings.Contains(asModel(m).View(), "FRESH-AT-BOTTOM") {
		t.Fatalf("new output while at the bottom should auto-stick and show")
	}
	// Scroll up, then append: the position must hold (the new tail line stays hidden).
	for i := 0; i < 10; i++ {
		m, _ = m.Update(keyPgUp())
	}
	mm = asModel(m)
	mm.transcript = append(mm.transcript, "HIDDEN-WHILE-SCROLLED")
	m = mm
	m, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	v := asModel(m).View()
	if strings.Contains(v, "HIDDEN-WHILE-SCROLLED") {
		t.Fatalf("new output while scrolled up must NOT yank the view to the bottom")
	}
	if !strings.Contains(v, "LINE-000") {
		t.Fatalf("the scrolled position should be held; view:\n%s", v)
	}
}

// TestChatTypingWorksWhileScrolled: typing into the input still echoes while the
// transcript is scrolled away from the bottom, and the scroll position holds.
func TestChatTypingWorksWhileScrolled(t *testing.T) {
	m := tallChat(t, 60)
	for i := 0; i < 5; i++ {
		m, _ = m.Update(keyPgUp())
	}
	m, _ = m.Update(keyRunes("hi"))
	if got := asModel(m).chatIn.Value(); got != "hi" {
		t.Fatalf("typing should still feed the input while scrolled, got %q", got)
	}
	if strings.Contains(asModel(m).View(), "LINE-059") {
		t.Fatalf("typing must not yank the transcript back to the bottom")
	}
}

// TestChatArrowScrollsWhenNoHistory: with no command history, Up scrolls the
// transcript by a line (input stays empty); with history, Up still recalls.
func TestChatArrowScrollsWhenNoHistory(t *testing.T) {
	m := tallChat(t, 60) // no history added
	m, _ = m.Update(keyUp())
	if got := asModel(m).chatIn.Value(); got != "" {
		t.Fatalf("with no history Up must not populate the input, got %q", got)
	}
	if strings.Contains(asModel(m).View(), "LINE-059") {
		t.Fatalf("with no history Up should scroll the transcript up a line")
	}
}

// TestChatHistoryRecallStillWorks: command-history recall is preserved on ctrl+p with
// the scrollable transcript in place, and Up scrolls instead of recalling (the wheel
// arrives as arrow keys, so arrows must never type old messages).
func TestChatHistoryRecallStillWorks(t *testing.T) {
	m := tallChat(t, 60)
	mm := asModel(m)
	mm.chatHist.add("recall me")
	m = mm
	m, _ = m.Update(keyUp())
	if got := asModel(m).chatIn.Value(); got != "" {
		t.Fatalf("Up must scroll, not recall; input became %q", got)
	}
	m, _ = m.Update(keyRecallPrev())
	if got := asModel(m).chatIn.Value(); got != "recall me" {
		t.Fatalf("ctrl+p should recall command history, got %q", got)
	}
}

// TestChatEnterStillSends: Enter sends a turn (records history, clears input, relays)
// with the viewport in place.
func TestChatEnterStillSends(t *testing.T) {
	m := tallChat(t, 5)
	mm := asModel(m)
	mm.chatIn.SetValue("hello there")
	m = mm
	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := asModel(m).chatIn.Value(); got != "" {
		t.Fatalf("Enter should clear the input, got %q", got)
	}
	if ents := asModel(m).chatHist.entries; len(ents) != 1 || ents[0] != "hello there" {
		t.Fatalf("Enter should record history, got %v", ents)
	}
	if !asModel(m).relaying {
		t.Fatalf("Enter should start a relay")
	}
	if cmd == nil {
		t.Fatalf("Enter should return a send command")
	}
}

// TestChatEscStillDisconnects: Esc leaves the channel (disconnect), unchanged.
func TestChatEscStillDisconnects(t *testing.T) {
	m := tallChat(t, 5)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if asModel(m).mode == modeChat {
		t.Fatalf("Esc should disconnect out of the channel")
	}
}

// TestChatTabStillPeeks: Tab is the non-destructive peek to BROWSE (channel stays open).
func TestChatTabStillPeeks(t *testing.T) {
	m := tallChat(t, 5)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if asModel(m).mode != modeBrowse {
		t.Fatalf("Tab should peek at the band (modeBrowse)")
	}
	if asModel(m).connected == nil {
		t.Fatalf("Tab peek must keep the channel open")
	}
}

// TestAgentTranscriptScrolls: the AGENT transcript is independently scrollable too.
func TestAgentTranscriptScrolls(t *testing.T) {
	m := tallAgent(t, 60)
	v := asModel(m).View()
	if !strings.Contains(v, "LINE-059") || strings.Contains(v, "LINE-000") {
		t.Fatalf("agent transcript should start at the bottom; view:\n%s", v)
	}
	for i := 0; i < 12; i++ {
		m, _ = m.Update(keyPgUp())
	}
	if !strings.Contains(asModel(m).View(), "LINE-000") {
		t.Fatalf("PgUp should reveal earlier agent lines")
	}
}

// TestAgentScrollsWhileBusy: while a turn streams the user can still page back to read
// earlier output (PgUp is not swallowed by the busy guard).
func TestAgentScrollsWhileBusy(t *testing.T) {
	m := tallAgent(t, 60)
	mm := asModel(m)
	mm.agentBusy = true
	m = mm
	for i := 0; i < 12; i++ {
		m, _ = m.Update(keyPgUp())
	}
	if !strings.Contains(asModel(m).View(), "LINE-000") {
		t.Fatalf("PgUp should scroll the agent transcript even while busy")
	}
}

// TestAgentTypingAndHistoryStillWork: the AGENT prompt still types and recalls history.
func TestAgentTypingAndHistoryStillWork(t *testing.T) {
	m := tallAgent(t, 5)
	m, _ = m.Update(keyRunes("do it"))
	if got := asModel(m).agentIn.Value(); got != "do it" {
		t.Fatalf("agent prompt should type, got %q", got)
	}
	mm := asModel(m)
	mm.agentIn.SetValue("")
	mm.agentHist.add("earlier prompt")
	m = mm
	m, _ = m.Update(keyRecallPrev())
	if got := asModel(m).agentIn.Value(); got != "earlier prompt" {
		t.Fatalf("ctrl+p should recall the agent history, got %q", got)
	}
}
