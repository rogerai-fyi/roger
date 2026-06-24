package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rogerai-fyi/roger/internal/harness"
)

// TestZeroEntersAgentMode: pressing 0 in BROWSE jumps to the [0] AGENT mode, and the
// preset bar lights AGENT.
func TestZeroEntersAgentMode(t *testing.T) {
	m := browseSeed(100)
	var tm tea.Model = m
	tm, _ = tm.Update(keyMsg("0"))
	got := asModel(tm)
	if got.mode != modeAgent {
		t.Fatalf("0 should enter modeAgent, got mode %d", got.mode)
	}
	out := stripANSI(got.View())
	if !strings.Contains(out, "AGENT") {
		t.Errorf("AGENT view/preset not shown:\n%s", out)
	}
}

// TestAgentPresetAtFront: the preset bar leads with [0] AGENT, then [1] TUNE IN, etc.
func TestAgentPresetAtFront(t *testing.T) {
	m := browseSeed(120)
	out := stripANSI(m.View())
	bar := firstLineContaining(out, "AGENT")
	if bar == "" {
		t.Fatalf("no preset bar line with AGENT:\n%s", out)
	}
	ai := strings.Index(bar, "AGENT")
	ti := strings.Index(bar, "TUNE IN")
	if ai < 0 || ti < 0 || ai > ti {
		t.Errorf("AGENT must come before TUNE IN on the preset bar: %q", bar)
	}
}

// TestZeroNotStolenDuringTextEntry: a typed 0 in the command palette and in the agent
// prompt is a literal digit, NOT a jump back into AGENT (the guard the spec requires).
func TestZeroNotStolenDuringTextEntry(t *testing.T) {
	// In the command palette (/), 0 is typed text.
	m := browseSeed(100)
	var tm tea.Model = m
	tm, _ = tm.Update(keyMsg("/"))
	tm, _ = tm.Update(keyMsg("0"))
	cm := asModel(tm)
	if cm.mode != modeCommand {
		t.Fatalf("/ then 0 should stay in modeCommand, got %d", cm.mode)
	}
	if !strings.Contains(cm.cmd.Value(), "0") {
		t.Errorf("0 should be typed into the command input, got %q", cm.cmd.Value())
	}

	// In the AGENT prompt, 0 is typed text (not a re-entry / not stolen).
	var am tea.Model = browseSeed(100)
	am, _ = am.Update(keyMsg("0")) // enter AGENT
	am, _ = am.Update(keyMsg("0")) // type a 0
	gm := asModel(am)
	if gm.mode != modeAgent {
		t.Fatalf("should still be in modeAgent after typing 0, got %d", gm.mode)
	}
	if !strings.Contains(gm.agentIn.Value(), "0") {
		t.Errorf("0 should be typed into the agent prompt, got %q", gm.agentIn.Value())
	}
}

// TestAgentConfirmGate: a pending mutating-tool confirm renders an obvious y/N gate,
// and answering n denies it (releases the loop with false). We drive the model's
// confirm message directly (no network) to keep it deterministic.
func TestAgentConfirmGate(t *testing.T) {
	var am tea.Model = browseSeed(100)
	am, _ = am.Update(keyMsg("0"))
	resp := make(chan bool, 1)
	am, _ = am.Update(agentConfirmMsg(agentConfirm{tool: "run_shell", args: map[string]any{"cmd": "rm -rf /"}, resp: resp}))
	gm := asModel(am)
	if gm.agentPendingConfirm == nil {
		t.Fatalf("a confirm message should set a pending confirm")
	}
	out := stripANSI(gm.View())
	if !strings.Contains(out, "run_shell: rm -rf /") || !strings.Contains(out, "[y/N]") {
		t.Errorf("confirm gate should show the tool + y/N:\n%s", out)
	}
	// Deny with n.
	am, _ = gm.Update(keyMsg("n"))
	if asModel(am).agentPendingConfirm != nil {
		t.Errorf("n should clear the pending confirm")
	}
	if got := <-resp; got != false {
		t.Errorf("n should answer the loop with false (deny), got %v", got)
	}
}

// TestAgentEventRendering: streamed loop events render the tool call + result lines
// with the shared iconography and the final answer.
func TestAgentEventRendering(t *testing.T) {
	var am tea.Model = browseSeed(100)
	am, _ = am.Update(keyMsg("0"))
	am, _ = am.Update(agentEventMsg{Kind: harness.EventToolCall, Tool: "list_dir", Args: map[string]any{"path": "."}})
	am, _ = am.Update(agentEventMsg{Kind: harness.EventToolResult, Tool: "list_dir", Result: "a.go\nb.go\n"})
	am, _ = am.Update(agentEventMsg{Kind: harness.EventFinal, Text: "there are two go files"})
	out := stripANSI(asModel(am).View())
	for _, want := range []string{"list_dir", glyphOnAir, "ok", "there are two go files"} {
		if !strings.Contains(out, want) {
			t.Errorf("agent transcript missing %q:\n%s", want, out)
		}
	}
}

// TestAgentNoColorNarrowSafe: AGENT renders without ANSI under NO_COLOR and never
// overflows narrow widths, including with a pending confirm and a streamed turn.
func TestAgentNoColorNarrowSafe(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	assertSafe := func(w int, am tea.Model) {
		out := am.View()
		if strings.Contains(out, "\x1b[") {
			t.Errorf("width %d: AGENT emitted ANSI under NO_COLOR", w)
		}
		for _, line := range strings.Split(out, "\n") {
			if vis := utf8.RuneCountInString(stripANSI(line)); vis > w {
				t.Errorf("width %d: AGENT line overflows (%d cols): %q", w, vis, stripANSI(line))
			}
		}
	}
	for _, w := range []int{40, 50, 64, 80, 120} {
		// Plain AGENT view (empty input -> placeholder; the prompt + help + footer lines).
		var plain tea.Model = browseSeed(w)
		plain, _ = plain.Update(tea.WindowSizeMsg{Width: w, Height: 24})
		plain, _ = plain.Update(keyMsg("0"))
		assertSafe(w, plain)
		// A streamed turn + a pending confirm (long args, long results).
		var am tea.Model = browseSeed(w)
		am, _ = am.Update(tea.WindowSizeMsg{Width: w, Height: 24})
		am, _ = am.Update(keyMsg("0"))
		am, _ = am.Update(agentEventMsg{Kind: harness.EventToolCall, Tool: "run_shell", Args: map[string]any{"cmd": "ls -la /some/very/long/path/that/keeps/going/on/and/on"}})
		am, _ = am.Update(agentEventMsg{Kind: harness.EventToolResult, Tool: "run_shell", Result: strings.Repeat("x", 500)})
		am, _ = am.Update(agentConfirmMsg(agentConfirm{tool: "write_file", args: map[string]any{"path": "x.txt", "content": "yy"}, resp: make(chan bool, 1)}))
		assertSafe(w, am)
	}
}

// firstLineContaining returns the first line of s that contains sub ("" if none).
func firstLineContaining(s, sub string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, sub) {
			return line
		}
	}
	return ""
}
