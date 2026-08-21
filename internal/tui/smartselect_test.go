package tui

// smartselect_test.go - the unit contract for the "Smart mouse mode copies an
// application-owned selection on release" and "Smart select-and-copy is the
// default" rules in features/tui/conversation_hierarchy_and_selection.feature.
//
// The selection is APPLICATION-owned: it lives in screen cells over the transcript
// viewport, highlights during the drag, and on release copies the exact visible
// text - decorative gutters (▏ ◂ ▌), role labels (YOU › / ROGER ›), indent padding,
// and ANSI styling excluded; soft-wrap breaks rejoined (with the space the wrapper
// consumed restored from the source line); explicit entry boundaries preserved as
// newlines; wide characters and combining marks never split. Copy feedback is
// honest: a success toast only when a clipboard mechanism succeeded, an explicit
// no-tool/no-OSC-52 status (with the selection retained) when none did.

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/stretchr/testify/require"
)

// ---------- helpers ----------

func mousePress(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}
}
func mouseMotion(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft}
}
func mouseRelease(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft}
}

// smartChat is a connected CHANNEL model in smart mouse mode (capture opted in)
// with the given transcript entries, sized to 100x40.
func smartChat(t *testing.T, entries ...string) model {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	mm := New("http://broker.local", "tester")
	mm.connected = &offer{NodeID: "nyx", Model: "gpt-oss-20b", Online: true}
	mm.offers = []offer{{NodeID: "nyx", Model: "gpt-oss-20b", Online: true}}
	mm.bands = []band{{model: "gpt-oss-20b", online: true}}
	mm.mode = modeChat
	mm.chatIn.Focus()
	mm.mouseOff = false // smart mouse mode: capture opted in via ctrl+o / /mouse
	mm.transcript = append(mm.transcript, entries...)
	out, _ := mm.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return asModel(out)
}

// captureClip reroutes the smart-copy clipboard mechanisms for one test: the
// local tool records what it was asked to copy and reports toolOK; the OSC 52
// path reports oscOK. Returns the copies observed.
func captureClip(t *testing.T, toolOK, oscOK bool) *[]string {
	t.Helper()
	var copies []string
	origTool, origOSC := smartClipTool, smartOSC52
	smartClipTool = func(s string) bool {
		copies = append(copies, s)
		return toolOK
	}
	smartOSC52 = func(s string) bool { return oscOK }
	t.Cleanup(func() { smartClipTool, smartOSC52 = origTool, origOSC })
	return &copies
}

// runSmartCopy drives one full drag (press -> motion -> release) through Update
// and, when the release produced a copy command, executes it and feeds the result
// message back - the same round trip the bubbletea runtime performs.
func runSmartCopy(t *testing.T, m model, x1, y1, x2, y2 int) model {
	t.Helper()
	out, _ := m.Update(mousePress(x1, y1))
	out, _ = asModel(out).Update(mouseMotion(x2, y2))
	out, cmd := asModel(out).Update(mouseRelease(x2, y2))
	m = asModel(out)
	if cmd == nil {
		return m
	}
	if msg := cmd(); msg != nil {
		out, _ = m.Update(msg)
		m = asModel(out)
	}
	return m
}

// ---------- selectionRows: chrome, wrap, gaps ----------

func TestSelectionRowsChromeWrapAndGaps(t *testing.T) {
	cases := []struct {
		name    string
		entries []string
		width   int
		want    []selRow
	}{
		{
			name:    "plain entry",
			entries: []string{"hello"},
			width:   80,
			want:    []selRow{{text: "hello", startCol: 2, hard: true}},
		},
		{
			name:    "empty spacer entry",
			entries: []string{""},
			width:   80,
			want:    []selRow{{text: "", startCol: 2, hard: true}},
		},
		{
			// AMENDED 2026-08-21: chatUserBlock/chatAnswerBlock TAG a turn now and the
			// rows are painted at display time, so these feed the RENDERED rows - which
			// is also what selection actually sees (smartselect reads displayChatLines).
			// The guarantee is unchanged: the bar and the role label are chrome, the
			// words are content.
			name:    "user band bar and YOU label are chrome",
			entries: chatUserRows("hi there", 78),
			width:   80,
			want:    []selRow{{text: "hi there", startCol: 10, hard: true}},
		},
		{
			name:    "answer head is a label row; gutter rows keep content",
			entries: chatReplyRows("gpt-oss-20b", "line one\nline two", 78),
			width:   80,
			want: []selRow{
				{text: "", startCol: 2, hard: true},
				{text: "line one", startCol: 4, hard: true},
				{text: "line two", startCol: 4, hard: true},
			},
		},
		{
			name:    "soft wrap keeps the eaten space as the continuation gap",
			entries: []string{"alpha beta gamma"},
			width:   12, // wrapAt 10: "alpha beta" + "gamma", source space consumed by the break
			want: []selRow{
				{text: "alpha beta", startCol: 2, hard: false},
				{text: "gamma", startCol: 2, hard: true, gap: " "},
			},
		},
		{
			name:    "hard token break has no gap",
			entries: []string{"abcdefghijklmn"},
			width:   12,
			want: []selRow{
				{text: "abcdefghij", startCol: 2, hard: false},
				{text: "klmn", startCol: 2, hard: true},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, selectionRows(tc.entries, tc.width))
		})
	}
}

// ---------- selectionText: spans, order, joins, unicode ----------

func TestSelectionTextSingleRow(t *testing.T) {
	rows := selectionRows([]string{"There are six words."}, 80)
	cases := []struct {
		name   string
		sc, ec int
		want   string
	}{
		{"exact span", 2, 21, "There are six words."},
		{"clamped past both edges", 0, 60, "There are six words."},
		{"partial word", 2, 6, "There"},
		{"starts inside padding", 0, 6, "There"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, selectionText(rows, 0, tc.sc, 0, tc.ec))
		})
	}
}

func TestSelectionTextReverseDragNormalizes(t *testing.T) {
	rows := selectionRows([]string{"There are six words."}, 80)
	require.Equal(t, "There are six words.", selectionText(rows, 0, 21, 0, 2))

	multi := selectionRows([]string{"one", "two"}, 80)
	require.Equal(t, "one\ntwo", selectionText(multi, 1, 80, 0, 0))
}

func TestSelectionTextJoins(t *testing.T) {
	t.Run("soft wrap restores the source space, no newline", func(t *testing.T) {
		rows := selectionRows([]string{"alpha beta gamma"}, 12)
		require.Equal(t, "alpha beta gamma", selectionText(rows, 0, 0, 1, 80))
	})
	t.Run("hard token break joins bare", func(t *testing.T) {
		rows := selectionRows([]string{"abcdefghijklmn"}, 12)
		require.Equal(t, "abcdefghijklmn", selectionText(rows, 0, 0, 1, 80))
	})
	t.Run("entry boundaries are explicit newlines", func(t *testing.T) {
		rows := selectionRows([]string{"one", "two", "three"}, 80)
		require.Equal(t, "one\ntwo\nthree", selectionText(rows, 0, 0, 2, 80))
	})
	t.Run("label and blank edge rows are trimmed away", func(t *testing.T) {
		// AMENDED 2026-08-21: the rendered rows, for the same reason as the cases above -
		// and one row fewer, since the leading blank separator belongs to the transcript
		// between blocks rather than to the reply itself.
		rows := selectionRows(chatReplyRows("gpt-oss-20b", "line one\nline two", 78), 80)
		require.Equal(t, "line one\nline two", selectionText(rows, 0, 0, 2, 80))
	})
}

func TestSelectionTextWideAndCombining(t *testing.T) {
	t.Run("wide char selected by either of its cells", func(t *testing.T) {
		rows := selectionRows([]string{"日本語 test"}, 80)
		// 日 occupies cells 2-3, 本 4-5, 語 6-7.
		require.Equal(t, "日", selectionText(rows, 0, 2, 0, 3))
		require.Equal(t, "日本", selectionText(rows, 0, 3, 0, 4))
		require.Equal(t, "日本語 test", selectionText(rows, 0, 2, 0, 80))
	})
	t.Run("combining mark travels with its base", func(t *testing.T) {
		rows := selectionRows([]string{"étude"}, 80)
		require.Equal(t, "é", selectionText(rows, 0, 2, 0, 2))
	})
	t.Run("emoji is never split", func(t *testing.T) {
		rows := selectionRows([]string{"🙂ok"}, 80)
		require.Equal(t, "🙂", selectionText(rows, 0, 2, 0, 3))
		require.Equal(t, "🙂ok", selectionText(rows, 0, 3, 0, 80))
	})
}

func TestSelectionTextEmptyAndPadding(t *testing.T) {
	rows := selectionRows([]string{"", "text", ""}, 80)
	t.Run("blank edge rows trimmed", func(t *testing.T) {
		require.Equal(t, "text", selectionText(rows, 0, 0, 2, 80))
	})
	t.Run("only blank rows selects nothing", func(t *testing.T) {
		require.Equal(t, "", selectionText(rows, 0, 0, 0, 80))
	})
	t.Run("out of range rows select nothing", func(t *testing.T) {
		require.Equal(t, "", selectionText(rows, 9, 0, 12, 80))
	})
	t.Run("span past the row text selects nothing", func(t *testing.T) {
		one := selectionRows([]string{"hi"}, 80)
		require.Equal(t, "", selectionText(one, 0, 40, 0, 60))
	})
}

// ---------- geometry: the computed viewport top matches the rendered frame ----------

func TestTranscriptTopMatchesRenderedFrame(t *testing.T) {
	const sentinel = "SENTINEL-ROW-XYZ"
	t.Run("channel", func(t *testing.T) {
		m := smartChat(t, sentinel)
		rows := strings.Split(m.View(), "\n")
		top := m.transcriptTop()
		require.GreaterOrEqual(t, top, 0)
		require.Less(t, top, len(rows))
		require.Contains(t, ansi.Strip(rows[top]), sentinel)
	})
	t.Run("channel compact", func(t *testing.T) {
		m := smartChat(t, sentinel)
		m.compact = true
		rows := strings.Split(m.View(), "\n")
		top := m.transcriptTop()
		require.Contains(t, ansi.Strip(rows[top]), sentinel)
	})
	t.Run("agent", func(t *testing.T) {
		m := smartChat(t)
		m.mode = modeAgent
		m.agentLines = []string{sentinel}
		out, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
		m = asModel(out)
		rows := strings.Split(m.View(), "\n")
		top := m.transcriptTop()
		require.GreaterOrEqual(t, top, 0)
		require.Contains(t, ansi.Strip(rows[top]), sentinel)
	})
}

// ---------- wiring: the drag life cycle ----------

func TestDefaultMouseOwnershipIsSmartSelect(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	m := New("http://broker.local", "tester")
	require.False(t, m.mouseOff, "mouse capture must be ON by default so release can copy and notify")
}

func TestSmartDragCopiesExactVisibleSelection(t *testing.T) {
	copies := captureClip(t, true, true)
	m := smartChat(t, "There are six words.")
	top := m.transcriptTop()
	m = runSmartCopy(t, m, 2, top, 21, top)

	require.Equal(t, []string{"There are six words."}, *copies, "written to the clipboard exactly once")
	require.Contains(t, ansi.Strip(m.status), "Copied 20 characters to clipboard")
}

func TestSmartDragOnAgentTranscript(t *testing.T) {
	copies := captureClip(t, true, true)
	m := smartChat(t)
	m.mode = modeAgent
	m.agentLines = []string{"agent says hi"}
	out, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = asModel(out)
	top := m.transcriptTop()
	m = runSmartCopy(t, m, 2, top, 14, top)

	require.Equal(t, []string{"agent says hi"}, *copies)
	require.Contains(t, ansi.Strip(m.status), "Copied 13 characters to clipboard")
}

func TestSmartCharacterCountUsesRunesNotCells(t *testing.T) {
	copies := captureClip(t, true, true)
	m := smartChat(t, "日本語") // 3 runes over 6 terminal cells
	top := m.transcriptTop()
	m = runSmartCopy(t, m, 2, top, 7, top)

	require.Equal(t, []string{"日本語"}, *copies)
	require.Contains(t, ansi.Strip(m.status), "Copied 3 characters to clipboard")
}

func TestSmartClickWithoutDragDoesNotCopy(t *testing.T) {
	copies := captureClip(t, true, true)
	m := smartChat(t, "There are six words.")
	top := m.transcriptTop()
	out, _ := m.Update(mousePress(4, top))
	out, cmd := asModel(out).Update(mouseRelease(4, top))
	m = asModel(out)

	require.Nil(t, cmd, "a click is not a drag - no clipboard write")
	require.Empty(t, *copies)
	require.NotContains(t, ansi.Strip(m.status), "Copied")
}

func TestSmartDragInPaddingCopiesNothing(t *testing.T) {
	copies := captureClip(t, true, true)
	m := smartChat(t, "hi")
	top := m.transcriptTop()
	// Entirely to the right of the two visible characters: padding only.
	m = runSmartCopy(t, m, 40, top, 60, top)

	require.Empty(t, *copies, "an empty normalized selection performs no clipboard write")
	require.NotContains(t, ansi.Strip(m.status), "Copied")
}

func TestSmartDragEscapeCancels(t *testing.T) {
	copies := captureClip(t, true, true)
	m := smartChat(t, "There are six words.")
	top := m.transcriptTop()
	out, _ := m.Update(mousePress(2, top))
	out, _ = asModel(out).Update(mouseMotion(10, top))
	out, _ = asModel(out).Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = asModel(out)

	require.Equal(t, modeChat, m.mode, "escape during a drag cancels the selection, not the view")
	require.False(t, m.smartSel.active)
	// The release that follows the cancelled drag must not copy.
	out, cmd := m.Update(mouseRelease(10, top))
	m = asModel(out)
	require.Nil(t, cmd)
	require.Empty(t, *copies)
}

func TestSmartDragResizeCancels(t *testing.T) {
	copies := captureClip(t, true, true)
	m := smartChat(t, "There are six words.")
	top := m.transcriptTop()
	out, _ := m.Update(mousePress(2, top))
	out, _ = asModel(out).Update(mouseMotion(10, top))
	out, _ = asModel(out).Update(tea.WindowSizeMsg{Width: 90, Height: 30})
	m = asModel(out)

	require.False(t, m.smartSel.active)
	out, cmd := m.Update(mouseRelease(10, top))
	require.Nil(t, cmd)
	require.Empty(t, *copies)
	_ = out
}

func TestSmartDragNeverTouchesComposer(t *testing.T) {
	captureClip(t, true, true)
	m := smartChat(t, "There are six words.")
	m.chatIn.SetValue("my draft")
	top := m.transcriptTop()
	m = runSmartCopy(t, m, 2, top, 21, top)

	require.Equal(t, "my draft", m.chatIn.Value())
}

func TestSmartCopyFailureIsHonest(t *testing.T) {
	captureClip(t, false, false) // no clipboard tool, no OSC 52 path
	m := smartChat(t, "There are six words.")
	top := m.transcriptTop()
	m = runSmartCopy(t, m, 2, top, 21, top)

	status := ansi.Strip(m.status)
	require.NotContains(t, status, "Copied", "never claim a copy that did not happen")
	require.Contains(t, status, "OSC 52", "the status explains that no clipboard tool or OSC 52 path succeeded")
	require.True(t, m.smartSel.held, "the selected text remains visibly recoverable")
}

func TestCtrlOClearsSelectionAndRestoresNative(t *testing.T) {
	captureClip(t, true, true)
	m := smartChat(t, "There are six words.")
	top := m.transcriptTop()
	out, _ := m.Update(mousePress(2, top))
	out, _ = asModel(out).Update(mouseMotion(10, top))
	out, _ = asModel(out).Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	m = asModel(out)

	require.True(t, m.mouseOff)
	require.False(t, m.smartSel.active)
	require.Contains(t, ansi.Strip(m.status), "native select")
}

func TestWheelStillScrollsInSmartMode(t *testing.T) {
	entries := make([]string, 0, 60)
	for i := 0; i < 60; i++ {
		entries = append(entries, "row")
	}
	m := smartChat(t, entries...)
	require.False(t, m.chatVP.AtTop())
	out, _ := m.Update(wheelUp())
	m = asModel(out)
	require.Less(t, m.chatVP.YOffset, m.chatVP.TotalLineCount())
}

// ---------- highlight ----------

func TestHighlightSpanIsReverseVideoAndLossless(t *testing.T) {
	r := lipgloss.DefaultRenderer()
	old := r.ColorProfile()
	r.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { r.SetColorProfile(old) })

	row := "  hello world"
	got := highlightSpan(row, 2, 6)
	require.Equal(t, row, ansi.Strip(got), "highlight restyles, never rewrites, the row")
	require.Contains(t, got, "\x1b[7m", "the selected cells render reverse-video")
}

func TestViewHighlightsDuringDrag(t *testing.T) {
	r := lipgloss.DefaultRenderer()
	old := r.ColorProfile()
	r.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { r.SetColorProfile(old) })

	m := smartChat(t, "There are six words.")
	top := m.transcriptTop()
	out, _ := m.Update(mousePress(2, top))
	out, _ = asModel(out).Update(mouseMotion(10, top))
	m = asModel(out)

	require.True(t, m.smartSel.active)
	require.Contains(t, m.View(), "\x1b[7m", "Roger highlights the selected cells during the drag")
}
