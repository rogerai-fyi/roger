package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"rogerai.fm/roger/v6/internal/harness"
	"rogerai.fm/roger/v6/internal/operator"
)

func TestRightArrowAcceptsGroundedHintWithoutSending(t *testing.T) {
	m := browseSeed(100)
	m.mode = modeAgent
	m.agent = m.newAgentRuntime()
	m.agentIn.Focus()
	m.agentNextHint = "run the tests or review the change"

	out, cmd := m.onAgentKey(tea.KeyMsg{Type: tea.KeyRight})
	got := asModel(out)
	if cmd != nil {
		t.Fatal("accepting a suggestion must not start a model request")
	}
	if got.agentIn.Value() != "run the tests or review the change" {
		t.Fatalf("accepted prompt = %q", got.agentIn.Value())
	}
	if got.agentBusy {
		t.Fatal("Right Arrow accepted and sent the suggestion")
	}
}

func TestRightArrowNeverOverwritesAuthoredText(t *testing.T) {
	m := browseSeed(100)
	m.mode = modeAgent
	m.agent = m.newAgentRuntime()
	m.agentIn.Focus()
	m.agentIn.SetValue("review only the parser")
	m.agentIn.CursorStart()
	m.agentNextHint = "run the tests"

	out, _ := m.onAgentKey(tea.KeyMsg{Type: tea.KeyRight})
	got := asModel(out)
	if got.agentIn.Value() != "review only the parser" {
		t.Fatalf("Right Arrow overwrote authored text: %q", got.agentIn.Value())
	}
	if got.agentIn.LineInfo().ColumnOffset == 0 {
		t.Fatal("Right Arrow did not retain normal cursor movement")
	}
}

func TestSuggestionRetiresOnTypingClearAndLeave(t *testing.T) {
	m := browseSeed(100)
	m.mode = modeAgent
	m.agent = m.newAgentRuntime()
	m.agentIn.Focus()
	m.agentNextHint = "run the tests"

	out, _ := m.onAgentKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = asModel(out)
	if m.agentNextHint != "" {
		t.Fatal("typing did not retire the stale suggestion")
	}

	m.agentNextHint = "run the tests"
	out, _ = m.runAgentCommand("/clear")
	m = asModel(out)
	if m.agentNextHint != "" {
		t.Fatal("/clear did not retire the stale suggestion")
	}

	m.agentNextHint = "run the tests"
	out, _ = m.onAgentKey(tea.KeyMsg{Type: tea.KeyEsc})
	m = asModel(out)
	if m.agentNextHint != "" || m.mode == modeAgent {
		t.Fatal("leaving AGENT retained the stale suggestion")
	}
}

func TestUntouchedCompactHeadingHasNoOrphanRailSeparator(t *testing.T) {
	m := browseSeed(100)
	m.mode = modeAgent
	m.compact = true
	m.agent = m.newAgentRuntime()
	head := stripANSI(strings.Split(m.agentView(100), "\n")[0])
	if strings.Contains(head, " ·  · ") || strings.HasSuffix(strings.TrimSpace(head), "·") {
		t.Fatalf("untouched compact heading has separator debris: %q", head)
	}
}

func TestContextOnlyDeskRowsDescribeVendorAccountTruthfully(t *testing.T) {
	m := browseSeed(140)
	for _, guest := range operator.Registry() {
		if guest.Name == "claude" || guest.Name == "codex" {
			m.operatorDetections = append(m.operatorDetections, operator.Detection{
				Guest: guest, Path: "/fake/" + guest.Bin, Version: guest.KnownGood,
			})
		}
	}
	view := stripANSI(m.deskRosterView(140, 1, true))
	if strings.Contains(view, "patches into your open channel") {
		t.Fatalf("context-only desk row falsely claimed band wiring:\n%s", view)
	}
	for _, want := range []string{"OpenAI account", "Anthropic account"} {
		if !strings.Contains(view, want) {
			t.Fatalf("context-only desk row omitted %q:\n%s", want, view)
		}
	}
}

func TestUntouchedLandingHidesDeadSessionRail(t *testing.T) {
	m := browseSeed(120)
	m.mode = modeAgent
	m.agent = m.newAgentRuntime()
	if got := stripANSI(m.agentSessionDeck(120)); got != "" {
		t.Fatalf("untouched landing rail = %q, want hidden", got)
	}
	m.agentBusy = true
	if got := stripANSI(m.agentSessionDeck(120)); !strings.Contains(got, "SESSION") || strings.Contains(got, "—/8") {
		t.Fatalf("active rail = %q", got)
	}
}

func TestPromptRenderingDoesNotMutateComposerGeometry(t *testing.T) {
	m := browseSeed(80)
	m.mode = modeAgent
	m.agentIn.Focus()
	m.agentIn.SetValue(strings.Repeat("wrapped ", 20))
	m = m.syncComposerGeometry()
	aw, ah, av, al := m.agentIn.Width(), m.agentIn.Height(), m.agentIn.Value(), m.agentIn.Line()
	cw, ch := m.chatIn.Width(), m.chatIn.Height()
	_ = m.agentPromptLines(40)
	_ = m.chatPromptLines(40)
	if m.agentIn.Width() != aw || m.agentIn.Height() != ah || m.agentIn.Value() != av || m.agentIn.Line() != al {
		t.Fatal("AGENT render mutated persistent composer geometry")
	}
	if m.chatIn.Width() != cw || m.chatIn.Height() != ch {
		t.Fatal("TUNE IN render mutated persistent composer geometry")
	}
}

func TestApprovalUpdatesSingleToolCard(t *testing.T) {
	m := browseSeed(100)
	m.mode = modeAgent
	m.agent = m.newAgentRuntime()
	out, _ := m.onAgentEvent(agentEventMsg{Kind: harness.EventToolCall, Tool: "run_shell", Args: map[string]any{"cmd": "echo hi"}})
	m = asModel(out)
	m.agentPendingConfirm = &agentConfirm{tool: "run_shell", args: map[string]any{"cmd": "echo hi"}, resp: make(chan bool, 1)}
	out, _ = m.onAgentKey(keyMsg("y"))
	m = asModel(out)
	// Rendered, not raw: a call is a record now and the buffer holds a reference.
	m.showToolCalls = true
	joined := stripANSI(strings.Join(m.displayAgentLines(100), "\n"))
	// Count CARDS, not name occurrences: the fold lid also names the tools it
	// contains, so "run_shell" legitimately appears twice in a rendered box. What
	// must never double is the card itself.
	if strings.Count(joined, "⚙ run_shell") > 1 || strings.Contains(joined, "WILCO") {
		t.Fatalf("approval duplicated tool narration:\n%s", joined)
	}
	if !strings.Contains(joined, "approved") {
		t.Fatalf("single card did not transition to approved:\n%s", joined)
	}
}

func TestPendingApprovalShowsOneCommandSurface(t *testing.T) {
	m := browseSeed(100)
	m.mode = modeAgent
	m.agent = m.newAgentRuntime()
	out, _ := m.onAgentEvent(agentEventMsg{Kind: harness.EventToolCall, Tool: "run_shell", Args: map[string]any{"cmd": "echo hi"}})
	m = asModel(out)
	m.agentPendingConfirm = &agentConfirm{tool: "run_shell", args: map[string]any{"cmd": "echo hi"}, resp: make(chan bool, 1)}

	view := stripANSI(m.agentView(100))
	if strings.Count(view, "echo hi") != 1 {
		t.Fatalf("approval gate repeated the command:\n%s", view)
	}
}

func TestComposerRowsUseSolidSurfaceInColor(t *testing.T) {
	colorOn(t, true)
	restore := paletteMono
	paletteMono = false
	t.Cleanup(func() { paletteMono = restore })

	m := browseSeed(40)
	m.agentIn.Focus()
	m.agentIn.SetValue(strings.Repeat("agent prompt ", 8))
	m.chatIn.Focus()
	m.chatIn.SetValue(strings.Repeat("chat prompt ", 8))
	m = m.syncComposerGeometry()

	for name, lines := range map[string][]string{
		"AGENT":   m.agentPromptLines(40),
		"TUNE IN": m.chatPromptLines(40),
	} {
		if len(lines) < 2 {
			t.Fatalf("%s prompt did not wrap: %q", name, stripANSI(strings.Join(lines, "\n")))
		}
		for _, line := range lines {
			if !strings.Contains(line, "\x1b[4") {
				t.Fatalf("%s row lacks a solid background: %q", name, line)
			}
		}
	}
}
