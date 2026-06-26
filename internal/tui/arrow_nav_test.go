package tui

import (
	"strings"
	"testing"

	"github.com/rogerai-fyi/roger/internal/detect"

	tea "github.com/charmbracelet/bubbletea"
)

// left / right are sequential tab navigation across the preset bank:
// 0 AGENT -> 1 TUNE IN -> 2 SHARE -> 3 CONFIG -> L LOGIN -> ? HELP -> wrap, and left
// the other way. They reuse the SAME jump action as the number/letter preset keys,
// and they ONLY fire as preset-cycle in top-level navigation contexts (BROWSE / SHARE
// table / CONFIG / HELP) - never where left/right already mean something or text is
// being entered (the schedule editor, the command palette, chat, the AGENT prompt,
// the `f` band filter).

func keyLeft() tea.KeyMsg  { return tea.KeyMsg{Type: tea.KeyLeft} }
func keyRight() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRight} }

// browseModel builds a sized BROWSE model with deterministic local detection so
// [2] SHARE opens the provider table instead of a real network scan.
func browseModel(t *testing.T) tea.Model {
	t.Helper()
	old := detectShares
	detectShares = func(extra ...string) ([]detect.Found, []string) {
		return []detect.Found{{Name: "t", Chat: "http://x/v1/chat/completions", Models: []string{"gpt-oss-20b"}}}, nil
	}
	t.Cleanup(func() { detectShares = old })
	mm := New("http://broker.local", "tester")
	mm.width, mm.height = 100, 30
	return mm
}

// TestArrowCyclesPresetsFromBrowse: from BROWSE (TUNE IN lit), Right steps to the
// NEXT preset and Left to the previous - identical to pressing the number/letter.
func TestArrowCyclesPresetsFromBrowse(t *testing.T) {
	// Right from TUNE IN -> SHARE (the next preset after TUNE IN is [2] SHARE).
	m := browseModel(t)
	m, _ = m.Update(keyRight())
	if got := asModel(m).mode; got != modeShare {
		t.Errorf("Right from BROWSE (TUNE IN) should advance to SHARE, got %v", got)
	}

	// Left from TUNE IN -> AGENT (the previous preset before TUNE IN is [0] AGENT).
	m = browseModel(t)
	m, _ = m.Update(keyLeft())
	if got := asModel(m).mode; got != modeAgent {
		t.Errorf("Left from BROWSE (TUNE IN) should step back to AGENT, got %v", got)
	}
}

// TestArrowCycleWraps: stepping Left from TUNE IN past AGENT does NOT fall off the
// end - and a Right walk visits SHARE then CONFIG then HELP, proving the sequential
// order 1 -> 2 -> 3 -> ... is followed (LOGIN has no resting mode, so a Right onto it
// lands in whatever doLogin returns; we assert the ordered, observable jumps).
func TestArrowCycleWraps(t *testing.T) {
	// Right walk: TUNE IN -> SHARE -> CONFIG.
	m := browseModel(t)
	m, _ = m.Update(keyRight()) // -> SHARE
	if got := asModel(m).mode; got != modeShare {
		t.Fatalf("step 1 should be SHARE, got %v", got)
	}
	m, _ = m.Update(keyRight()) // -> CONFIG (limits)
	if got := asModel(m).mode; got != modeLimits {
		t.Errorf("step 2 should be CONFIG (limits), got %v", got)
	}

	// Left walk wraps the other way: from CONFIG, Left -> SHARE -> TUNE IN (browse).
	m, _ = m.Update(keyLeft()) // CONFIG -> SHARE
	if got := asModel(m).mode; got != modeShare {
		t.Errorf("Left from CONFIG should step back to SHARE, got %v", got)
	}
	m, _ = m.Update(keyLeft()) // SHARE -> TUNE IN
	if got := asModel(m).mode; got != modeBrowse {
		t.Errorf("Left from SHARE should step back to TUNE IN (browse), got %v", got)
	}

	// Wrap: Left once more from TUNE IN reaches AGENT (the first preset, [0]).
	m, _ = m.Update(keyLeft())
	if got := asModel(m).mode; got != modeAgent {
		t.Errorf("Left from TUNE IN should wrap-step to AGENT, got %v", got)
	}

	// And from HELP, Right wraps forward off the end. ? is the last preset; entering
	// HELP then Right should not stay stuck in HELP (it advances/wraps).
	h := browseModel(t)
	h, _ = h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if asModel(h).mode != modeHelp {
		t.Fatalf("? should open HELP")
	}
	h, _ = h.Update(keyRight())
	if asModel(h).mode == modeHelp {
		t.Errorf("Right from HELP (the last preset) should wrap forward, not stay in HELP")
	}
}

// TestArrowDoesNotCycleInScheduleEditor: the price/schedule editor owns left/right
// for its window sub-fields (Start/End/In/Out). Arrows must move the sub-field and
// stay in the editor, NOT cycle the preset bar.
func TestArrowDoesNotCycleInScheduleEditor(t *testing.T) {
	mm := New("http://broker.local", "tester")
	mm.width, mm.height = 100, 30
	mm.setShareRows([]shareRow{{model: "gpt-oss-20b"}})
	mm.shareCursor = 0
	mm.mode = modeShare
	var m tea.Model = mm
	// Logging in unlocks the earn-gated editor.
	m, _ = m.Update(balanceMsg{balance: 5, loggedIn: true})
	// p opens the price + schedule editor.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	em := asModel(m)
	if em.mode != modeShareEditor {
		t.Fatalf("p should open the schedule editor, got %v", em.mode)
	}
	// Add a window and focus it so left/right have a sub-field to cycle.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	before := asModel(m).edWinSub
	m, _ = m.Update(keyRight())
	after := asModel(m)
	if after.mode != modeShareEditor {
		t.Errorf("Right in the schedule editor must NOT cycle the preset bar (mode=%v)", after.mode)
	}
	if after.edWinSub == before {
		t.Errorf("Right in the schedule editor should advance the window sub-field (got %d, was %d)", after.edWinSub, before)
	}
	// Left moves it back and still stays in the editor.
	m, _ = m.Update(keyLeft())
	if asModel(m).mode != modeShareEditor {
		t.Errorf("Left in the schedule editor must NOT cycle the preset bar")
	}
}

// TestArrowDoesNotCycleInCommandPalette: left/right typed in the command palette are
// cursor movement on the input, never a preset jump.
func TestArrowDoesNotCycleInCommandPalette(t *testing.T) {
	mm := browseModel(t)
	var m tea.Model = mm
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ab")})
	m, _ = m.Update(keyLeft())
	if asModel(m).mode != modeCommand {
		t.Errorf("Left in the command palette must stay in the palette, got %v", asModel(m).mode)
	}
	m, _ = m.Update(keyRight())
	cm := asModel(m)
	if cm.mode != modeCommand {
		t.Errorf("Right in the command palette must stay in the palette, got %v", cm.mode)
	}
	if cm.cmd.Value() != "ab" {
		t.Errorf("arrows must not corrupt the palette text (got %q)", cm.cmd.Value())
	}
}

// TestArrowDoesNotCycleInChat: left/right in a CHANNEL feed the chat input (cursor),
// never a preset jump.
func TestArrowDoesNotCycleInChat(t *testing.T) {
	mm := New("http://broker.local", "tester")
	mm.width, mm.height = 100, 30
	mm.connected = &offer{NodeID: "nyx", Model: "gpt-oss-20b", Online: true}
	mm.mode = modeChat
	mm.chatIn.Focus()
	var m tea.Model = mm
	m, _ = m.Update(keyLeft())
	if asModel(m).mode != modeChat {
		t.Errorf("Left in chat must stay in the channel, got %v", asModel(m).mode)
	}
	m, _ = m.Update(keyRight())
	if asModel(m).mode != modeChat {
		t.Errorf("Right in chat must stay in the channel, got %v", asModel(m).mode)
	}
}

// TestArrowDoesNotCycleInAgentPrompt: the AGENT prompt is text entry and owns its
// keys; left/right move the prompt cursor, never a preset jump.
func TestArrowDoesNotCycleInAgentPrompt(t *testing.T) {
	mm := New("http://broker.local", "tester")
	mm.width, mm.height = 100, 30
	var m tea.Model = mm
	// [0] opens AGENT.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'0'}})
	if asModel(m).mode != modeAgent {
		t.Fatalf("[0] should open AGENT, got %v", asModel(m).mode)
	}
	m, _ = m.Update(keyLeft())
	if asModel(m).mode != modeAgent {
		t.Errorf("Left in the AGENT prompt must stay in AGENT, got %v", asModel(m).mode)
	}
	m, _ = m.Update(keyRight())
	if asModel(m).mode != modeAgent {
		t.Errorf("Right in the AGENT prompt must stay in AGENT, got %v", asModel(m).mode)
	}
}

// TestArrowDoesNotCycleInFilter: while the live `f` band filter is open it owns every
// key; left/right edit the filter buffer, they must NOT cycle the preset bar (the
// filter is a text-entry input and stays modeBrowse+filterMode).
func TestArrowDoesNotCycleInFilter(t *testing.T) {
	mm := browseModel(t)
	var m tea.Model = mm
	// f opens the live filter input.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}})
	if !asModel(m).filterMode {
		t.Fatalf("f should open the live filter")
	}
	m, _ = m.Update(keyLeft())
	fm := asModel(m)
	if fm.mode != modeBrowse || !fm.filterMode {
		t.Errorf("Left while filtering must stay in the filter (mode=%v filterMode=%v)", fm.mode, fm.filterMode)
	}
	m, _ = m.Update(keyRight())
	fm = asModel(m)
	if fm.mode != modeBrowse || !fm.filterMode {
		t.Errorf("Right while filtering must stay in the filter (mode=%v filterMode=%v)", fm.mode, fm.filterMode)
	}
}

// TestArrowHintNoColorNarrow: the footer + help teach `←/→` and stay readable under
// NO_COLOR at a narrow width (no overflow, hint present).
func TestArrowHintNoColorNarrow(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	// Wide BROWSE footer teaches the section switch.
	wide := New("http://broker.local", "tester")
	wide.width, wide.height = 100, 30
	if got := stripANSI(wide.View()); !strings.Contains(got, "section") {
		t.Errorf("wide footer should teach the ←/→ section switch:\n%s", got)
	}

	// Narrow stays a clean single column and still mentions the section arrows.
	narrow := New("http://broker.local", "tester")
	narrow.width, narrow.height = 50, 30
	nv := stripANSI(narrow.View())
	if !strings.Contains(nv, "section") {
		t.Errorf("narrow footer should still teach the ←→ section switch:\n%s", nv)
	}
	for _, line := range strings.Split(nv, "\n") {
		if w := []rune(line); len(w) > narrow.width {
			t.Errorf("narrow line overflows width %d (%d runes): %q", narrow.width, len(w), line)
		}
	}

	// HELP names the arrow keys.
	h := New("http://broker.local", "tester")
	h.width, h.height = 100, 40
	h.mode = modeHelp
	if got := stripANSI(h.View()); !strings.Contains(got, "switch section") {
		t.Errorf("HELP should document ←/→ switch section:\n%s", got)
	}
}
