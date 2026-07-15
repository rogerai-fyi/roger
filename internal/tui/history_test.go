package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// keyUp / keyDown are the arrow keys: while the INPUT owns focus they recall history
// (the wheel scrolls as real mouse events - capture is on by default). ctrl+p / ctrl+n
// remain shell-style recall aliases.
func keyUp() tea.KeyMsg         { return tea.KeyMsg{Type: tea.KeyUp} }
func keyDown() tea.KeyMsg       { return tea.KeyMsg{Type: tea.KeyDown} }
func keyRecallPrev() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyCtrlP} }
func keyRecallNext() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyCtrlN} }

// --- the inputHistory store itself (unit level) -------------------------------

// TestHistoryWalkUpDown: Up recalls the last sent, repeated Up walks older and stops
// at the oldest, Down walks back toward newest and restores the in-progress draft.
func TestHistoryWalkUpDown(t *testing.T) {
	h := &inputHistory{} // in-memory (no path)
	h.add("one")
	h.add("two")
	h.add("three")

	// First Up recalls the most recent; the live draft "draft-x" is stashed.
	if v, ok := h.prev("draft-x"); !ok || v != "three" {
		t.Fatalf("first Up = %q,%v; want three,true", v, ok)
	}
	if v, ok := h.prev(""); !ok || v != "two" {
		t.Fatalf("second Up = %q,%v; want two,true", v, ok)
	}
	if v, ok := h.prev(""); !ok || v != "one" {
		t.Fatalf("third Up = %q,%v; want one,true", v, ok)
	}
	// Up at the oldest stays put (still shows the oldest, ok stays true).
	if v, ok := h.prev(""); !ok || v != "one" {
		t.Fatalf("Up past oldest = %q,%v; want one,true (clamp)", v, ok)
	}
	// Down walks back toward newest.
	if v, ok := h.next(); !ok || v != "two" {
		t.Fatalf("Down = %q,%v; want two,true", v, ok)
	}
	if v, ok := h.next(); !ok || v != "three" {
		t.Fatalf("Down = %q,%v; want three,true", v, ok)
	}
	// Down past the newest restores the stashed draft.
	if v, ok := h.next(); !ok || v != "draft-x" {
		t.Fatalf("Down past newest = %q,%v; want draft-x,true (restore draft)", v, ok)
	}
	// Down at the bottom is a no-op.
	if _, ok := h.next(); ok {
		t.Fatalf("Down at the bottom should be a no-op (ok=false)")
	}
}

// TestHistoryEmptyUpNoop: Up on an empty history recalls nothing.
func TestHistoryEmptyUpNoop(t *testing.T) {
	h := &inputHistory{}
	if v, ok := h.prev("keep"); ok || v != "" {
		t.Fatalf("Up on empty history = %q,%v; want \"\",false", v, ok)
	}
}

// TestHistoryNotStoredEmpty: empty / whitespace-only sends are not stored.
func TestHistoryNotStoredEmpty(t *testing.T) {
	h := &inputHistory{}
	h.add("")
	h.add("   ")
	h.add("\t\n")
	if len(h.entries) != 0 {
		t.Fatalf("empty sends stored: %v", h.entries)
	}
}

// TestHistoryDedupConsecutive: consecutive duplicates collapse to a single entry, but a
// non-adjacent repeat is kept.
func TestHistoryDedupConsecutive(t *testing.T) {
	h := &inputHistory{}
	h.add("a")
	h.add("a") // consecutive dup - dropped
	h.add("b")
	h.add("a") // not adjacent to the earlier "a" - kept
	want := []string{"a", "b", "a"}
	if strings.Join(h.entries, ",") != strings.Join(want, ",") {
		t.Fatalf("entries = %v; want %v", h.entries, want)
	}
}

// TestHistoryCap: the buffer caps to the last historyCap entries.
func TestHistoryCap(t *testing.T) {
	h := &inputHistory{}
	for i := 0; i < historyCap+50; i++ {
		h.add(strings.Repeat("x", 1) + itoa(i)) // distinct, non-consecutive-dup entries
	}
	if len(h.entries) != historyCap {
		t.Fatalf("len = %d; want %d", len(h.entries), historyCap)
	}
	// The newest entry is the last one added.
	if h.entries[len(h.entries)-1] != "x"+itoa(historyCap+49) {
		t.Fatalf("newest = %q; want the last added", h.entries[len(h.entries)-1])
	}
}

// TestHistoryPersistAndReload: a write then a fresh load round-trips the entries (oldest
// first), and the recall walk works against the reloaded store.
func TestHistoryPersistAndReload(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	h := newInputHistory("history-chat")
	h.add("first")
	h.add("second")

	// A fresh store reads the same file back.
	h2 := newInputHistory("history-chat")
	if strings.Join(h2.entries, ",") != "first,second" {
		t.Fatalf("reloaded entries = %v; want [first second]", h2.entries)
	}
	if v, ok := h2.prev(""); !ok || v != "second" {
		t.Fatalf("reloaded Up = %q,%v; want second,true", v, ok)
	}
	// The file lives under <config>/rogerai/history-chat.
	p := historyPath("history-chat")
	if filepath.Base(p) != "history-chat" || !strings.Contains(p, "rogerai") {
		t.Fatalf("history path %q not under rogerai/history-chat", p)
	}
}

// TestHistoryReloadCapAndDedup: a file with > cap lines and consecutive dups loads
// clamped + collapsed (a corrupt-but-readable file is still tolerated).
func TestHistoryReloadCapAndDedup(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, "rogerai", "history-agent")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("dup\ndup\n") // consecutive dup -> one
	b.WriteString("\n   \n")    // blank/whitespace -> skipped
	for i := 0; i < historyCap+20; i++ {
		b.WriteString("e" + itoa(i) + "\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newInputHistory("history-agent")
	if len(h.entries) != historyCap {
		t.Fatalf("loaded len = %d; want clamped to %d", len(h.entries), historyCap)
	}
}

// TestHistoryMissingFileEmpty / corrupt-binary file: never crashes, starts empty.
func TestHistoryMissingFileEmpty(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	h := newInputHistory("nope-does-not-exist")
	if len(h.entries) != 0 {
		t.Fatalf("missing file should yield empty history, got %v", h.entries)
	}
	// A binary blob (no newline) still loads without panicking.
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	path := filepath.Join(dir, "rogerai", "history-chat")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte{0x00, 0x01, 0xff, 0xfe}, 0o600); err != nil {
		t.Fatal(err)
	}
	h2 := newInputHistory("history-chat") // must not panic
	_ = h2
}

// --- per-surface separation: chat vs agent histories are distinct ------------

func TestChatAndAgentHistoriesSeparate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	chat := newInputHistory("history-chat")
	agent := newInputHistory("history-agent")
	chat.add("chat-line")
	agent.add("agent-line")

	// Reload both from disk and confirm no bleed.
	c2 := newInputHistory("history-chat")
	a2 := newInputHistory("history-agent")
	if strings.Join(c2.entries, ",") != "chat-line" {
		t.Fatalf("chat history bled: %v", c2.entries)
	}
	if strings.Join(a2.entries, ",") != "agent-line" {
		t.Fatalf("agent history bled: %v", a2.entries)
	}
}

// --- end-to-end through the model key handlers -------------------------------

// TestChatInputUpDownRecall: typing + sending in modeChat records the line, and Up
// recalls it into the chat input; Down past the newest restores the in-progress draft.
func TestChatInputUpDownRecall(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	mm := New("http://broker.local", "tester")
	mm.width, mm.height = 100, 30
	mm.connected = &offer{NodeID: "nyx", Model: "gpt-oss-20b", Online: true}
	// Pretend the band is on air so enter records + would relay (we only assert recall).
	mm.offers = []offer{{NodeID: "nyx", Model: "gpt-oss-20b", Online: true}}
	mm.mode = modeChat
	mm.chatIn.Focus()

	var m tea.Model = mm
	mm2 := asModel(m)
	mm2.chatIn.SetValue("hello there")
	m = mm2
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := asModel(m).chatIn.Value(); got != "" {
		t.Fatalf("after send the input should clear, got %q", got)
	}
	if ents := asModel(m).chatHist.entries; len(ents) != 1 || ents[0] != "hello there" {
		t.Fatalf("send not recorded in chatHist: %v", ents)
	}

	// Up (input focused) stashes the draft and recalls the sent line.
	mm3 := asModel(m)
	mm3.chatIn.SetValue("a draft")
	m = mm3
	m, _ = m.Update(keyUp())
	if got := asModel(m).chatIn.Value(); got != "hello there" {
		t.Fatalf("Up should recall the sent line, got %q", got)
	}
	// Down past the newest restores the stashed draft.
	m, _ = m.Update(keyDown())
	if got := asModel(m).chatIn.Value(); got != "a draft" {
		t.Fatalf("Down should restore the draft, got %q", got)
	}
	// ctrl+p / ctrl+n remain recall aliases.
	m, _ = m.Update(keyRecallPrev())
	if got := asModel(m).chatIn.Value(); got != "hello there" {
		t.Fatalf("ctrl+p should recall the sent line, got %q", got)
	}
	m, _ = m.Update(keyRecallNext())
	if got := asModel(m).chatIn.Value(); got != "a draft" {
		t.Fatalf("ctrl+n should restore the draft, got %q", got)
	}
}

// TestAgentPromptUpDownRecall: the AGENT prompt records sent prompts to its OWN history
// and Up recalls them; the chat history is untouched (separation through the model).
func TestAgentPromptUpDownRecall(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	mm := New("http://broker.local", "tester")
	mm.width, mm.height = 100, 30
	var m tea.Model = mm
	// [0] opens AGENT (no model tuned in is fine - a /command still records).
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'0'}})
	if asModel(m).mode != modeAgent {
		t.Fatalf("[0] should open AGENT")
	}
	am := asModel(m)
	am.agentIn.SetValue("/help")
	m = am
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if ents := asModel(m).agentHist.entries; len(ents) != 1 || ents[0] != "/help" {
		t.Fatalf("agent send not recorded: %v", ents)
	}
	// The chat history must NOT have picked it up.
	if len(asModel(m).chatHist.entries) != 0 {
		t.Fatalf("chat history bled from the agent: %v", asModel(m).chatHist.entries)
	}
	// Up (input focused) recalls the agent's sent prompt.
	m, _ = m.Update(keyUp())
	if got := asModel(m).agentIn.Value(); got != "/help" {
		t.Fatalf("Up in AGENT should recall /help, got %q", got)
	}
	// With the TRANSCRIPT focused, Up scrolls instead of recalling.
	am2 := asModel(m)
	am2.agentIn.SetValue("")
	am2.agentHist.cursor = len(am2.agentHist.entries)
	am2.agentPaneFocus = true
	m = am2
	m, _ = m.Update(keyUp())
	if got := asModel(m).agentIn.Value(); got != "" {
		t.Fatalf("Up with the transcript focused must scroll, not recall; input became %q", got)
	}
}

// TestUpDownDoNotRecallOutsideInput: Up/Down in BROWSE move the band cursor (scroll),
// they must NOT touch the chat input value or run history recall.
func TestUpDownDoNotRecallOutsideInput(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	mm := New("http://broker.local", "tester")
	mm.width, mm.height = 100, 30
	mm.mode = modeBrowse
	mm.chatHist.add("ghost") // a prior chat entry that must NOT leak into browse
	var m tea.Model = mm
	m, _ = m.Update(keyUp())
	if asModel(m).chatIn.Value() != "" {
		t.Fatalf("Up in BROWSE must not populate the chat input, got %q", asModel(m).chatIn.Value())
	}
	if asModel(m).mode != modeBrowse {
		t.Fatalf("Up in BROWSE must stay in BROWSE, got %v", asModel(m).mode)
	}
}

// TestEmptyChatSendNotRecorded: pressing enter on a blank chat input records nothing.
func TestEmptyChatSendNotRecorded(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	mm := New("http://broker.local", "tester")
	mm.width, mm.height = 100, 30
	mm.connected = &offer{NodeID: "nyx", Model: "gpt-oss-20b", Online: true}
	mm.mode = modeChat
	mm.chatIn.Focus()
	mm.chatIn.SetValue("   ")
	var m tea.Model = mm
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if len(asModel(m).chatHist.entries) != 0 {
		t.Fatalf("a blank send was recorded: %v", asModel(m).chatHist.entries)
	}
}

// itoa is a tiny dependency-free int->string for test data (avoids importing strconv
// just for the loop labels).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
