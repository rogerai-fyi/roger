package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"rogerai.fm/roger/v6/internal/operator"
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

func TestAgentViewNeverPushesTopMascotOffscreen(t *testing.T) {
	for _, h := range []int{18, 24, 32} {
		m := browseSeed(140)
		m.width, m.height = 140, h
		m.mode = modeAgent
		m.connected = &offer{NodeID: "station", Model: "model", Online: true}
		m.agent = m.newAgentRuntime()
		for i := 0; i < 80; i++ {
			m.agentLines = append(m.agentLines, agentAnswerMark+"a substantial response line that must scroll inside the transcript")
		}
		got := m.View()
		if rows := strings.Count(got, "\n") + 1; rows > h {
			t.Fatalf("height %d: AGENT frame rendered %d rows and can scroll the top mascot away", h, rows)
		}
		if !strings.Contains(stripANSI(got), "R O G E R") {
			t.Fatalf("height %d: top Roger identity missing", h)
		}
	}
}

// TestAgentWrappedPromptRemainsVisibleWithLandingDesk is the exact regression from the
// founder's 2026-07-29 capture: the idle landing roster consumed enough vertical space
// that a second textarea row pushed the frame beyond the terminal and the terminal
// scrolled the composer away. Decorative landing chrome must yield to authored input.
func TestAgentWrappedPromptRemainsVisibleWithLandingDesk(t *testing.T) {
	const prompt = "lets test if things are going to remain visible when this sentence wraps onto another terminal row"
	for _, h := range []int{18, 24, 32} {
		m := browseSeed(80)
		m.width, m.height = 80, h
		m.mode = modeAgent
		m.connected = &offer{NodeID: "station", Model: "model", Online: true}
		m.agent = m.newAgentRuntime()
		m.agentLandingLines = len(m.agentLines)
		for _, g := range operator.Registry()[:3] {
			m.operatorDetections = append(m.operatorDetections, operator.Detection{
				Guest: g, Path: "/fake/" + g.Bin, Version: g.KnownGood,
			})
		}
		m.agentIn.SetValue(prompt)
		m.agentIn.CursorEnd()

		got := m.View()
		plain := stripANSI(got)
		if rows := strings.Count(got, "\n") + 1; rows > h {
			t.Fatalf("height %d: wrapped prompt + landing desk rendered %d rows:\n%s", h, rows, plain)
		}
		for _, fragment := range []string{"lets test if things", "another terminal row"} {
			if !strings.Contains(plain, fragment) {
				t.Fatalf("height %d: authored prompt fragment %q disappeared:\n%s", h, fragment, plain)
			}
		}
		if strings.Contains(plain, "THE DESK") {
			t.Fatalf("height %d: decorative landing desk must collapse while authored input is present:\n%s", h, plain)
		}
	}
}

func TestUnfocusedLandingDeskIsCompact(t *testing.T) {
	m := browseSeed(120)
	m.width, m.height = 120, 32
	m.mode = modeAgent
	m.agent = m.newAgentRuntime()
	m.agentLandingLines = len(m.agentLines)
	m.operatorDetections = []operator.Detection{{
		Guest: operator.Registry()[0], Path: "/fake/opencode", Version: operator.Registry()[0].KnownGood,
	}}
	m.deskFocused = false

	plain := stripANSI(m.deskRosterBlock(m.width))
	var nonEmpty int
	for _, line := range strings.Split(plain, "\n") {
		if strings.TrimSpace(line) != "" {
			nonEmpty++
		}
	}
	if nonEmpty > 2 {
		t.Fatalf("unfocused landing desk must be a compact status panel; got %d rows:\n%s", nonEmpty, plain)
	}
	if strings.Contains(plain, "the house agent") || strings.Contains(plain, "operator     wire") {
		t.Fatalf("unfocused landing desk must reserve brand art and full table for /operator:\n%s", plain)
	}
}

func TestTuneInWrappedPromptRemainsVisibleAndFits(t *testing.T) {
	const prompt = "we are testing how long this text is and the beginning and ending must both remain visible after wrapping twice"
	m := browseSeed(80)
	m.width, m.height = 80, 18
	m.mode = modeChat
	m.connected = &offer{NodeID: "station", Model: "model", Online: true}
	m.chatIn.Focus()
	m.chatIn.SetValue(prompt)
	m.chatIn.CursorEnd()

	got := m.View()
	plain := stripANSI(got)
	for _, fragment := range []string{"we are testing how long", "visible after wrapping twice"} {
		if !strings.Contains(plain, fragment) {
			t.Fatalf("TUNE IN prompt fragment %q disappeared:\n%s", fragment, plain)
		}
	}
	if rows := strings.Count(got, "\n") + 1; rows > m.height {
		t.Fatalf("TUNE IN frame rendered %d rows into a %d-row terminal:\n%s", rows, m.height, plain)
	}
	for _, line := range strings.Split(got, "\n") {
		if width := lipgloss.Width(line); width > m.width {
			t.Fatalf("TUNE IN line rendered %d cells into %d columns: %q", width, m.width, stripANSI(line))
		}
	}
}

func TestAgentTypedPromptRemainsPaintedAtWideTerminal(t *testing.T) {
	const prompt = "lets verify that live keyboard input remains painted after several words and continues visibly through the ending"
	m := browseSeed(170)
	m.width, m.height = 170, 32
	m.mode = modeAgent
	m.connected = &offer{NodeID: "station", Model: "model", Online: true}
	m.agent = m.newAgentRuntime()
	m.agentIn.Focus()
	tm := typeRunes(m, prompt)
	m = asModel(tm)

	plain := stripANSI(m.View())
	for _, fragment := range []string{"lets verify that live", "visibly through the ending"} {
		if !strings.Contains(plain, fragment) {
			t.Fatalf("wide AGENT live input fragment %q disappeared:\n%s", fragment, plain)
		}
	}
}

func TestAgentBatchedRunesRemainPaintedAtWideTerminal(t *testing.T) {
	const prompt = "lets test whether these words remain visible in the agent composer"
	m := browseSeed(170)
	m.width, m.height = 170, 32
	m.mode = modeAgent
	m.connected = &offer{NodeID: "station", Model: "model", Online: true}
	m.agent = m.newAgentRuntime()
	m.agentIn.Focus()

	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(prompt), Paste: true})
	m = asModel(out)
	if got := m.agentIn.Value(); got != prompt {
		t.Fatalf("batched terminal input value = %q, want %q", got, prompt)
	}
	if plain := stripANSI(m.View()); !strings.Contains(plain, prompt) {
		t.Fatalf("batched terminal input is stored but not painted:\n%s", plain)
	}
}

func TestWindowSizePersistsComposerGeometryBeforeInputUpdates(t *testing.T) {
	m := browseSeed(100)
	out, _ := m.Update(tea.WindowSizeMsg{Width: 170, Height: 32})
	m = asModel(out)
	if got := m.agentIn.Width(); got != 170-agentPromptLeadWidth {
		t.Fatalf("AGENT textarea content width = %d after resize, want %d persisted on the editable model", got, 170-agentPromptLeadWidth)
	}
	if got := m.chatIn.Width(); got != 170-chatPromptLeadWidth {
		t.Fatalf("TUNE IN textarea content width = %d after resize, want %d persisted on the editable model", got, 170-chatPromptLeadWidth)
	}
}

func TestComposerGrowthKeepsEveryWrappedRowVisible(t *testing.T) {
	const prompt = "can we test how long this text can be i just want it to count the number of characters and the number of words and give me a histogram of the words i am writing on this new line"
	tests := []struct {
		name string
		mode mode
		view func(model) string
	}{
		{"TUNE IN", modeChat, func(m model) string { return strings.Join(m.chatPromptLines(m.effWidth()), "\n") }},
		{"AGENT", modeAgent, func(m model) string { return strings.Join(m.agentPromptLines(m.effWidth()), "\n") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := browseSeed(170)
			out, _ := m.Update(tea.WindowSizeMsg{Width: 170, Height: 32})
			m = asModel(out)
			m.mode = tt.mode
			m.connected = &offer{NodeID: "station", Model: "model", Online: true}
			m.agent = m.newAgentRuntime()
			if tt.mode == modeChat {
				m.chatIn.Focus()
			} else {
				m.agentIn.Focus()
			}
			// Real terminals batch a rapid phrase into chunks. The first fills the
			// one-row viewport; the second crosses the wrap boundary in one update.
			runes := []rune(prompt)
			split := 150
			out, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: runes[:split]})
			m = asModel(out)
			_ = m.View() // Bubble Tea paints the still-one-row intermediate frame.
			out, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: runes[split:]})
			m = asModel(out)
			painted := stripANSI(tt.view(m))
			for _, fragment := range []string{"can we test how long", "writing on this new line"} {
				if !strings.Contains(painted, fragment) {
					t.Fatalf("%s lost wrapped fragment %q when composer grew:\n%s", tt.name, fragment, painted)
				}
			}
		})
	}
}
