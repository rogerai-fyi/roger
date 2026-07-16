package tui

// Increment 7 of the TUI design overhaul: MACHINERY DIMS TO TEXTURE (§4). The tool CALL
// line recedes to a single dim ⚙-prefixed line so the tool chatter no longer competes
// with the answer prose; the result line's ✓/✕ still carries the outcome.

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/rogerai-fyi/roger/internal/harness"
)

// agentWithToolOutput drives an AGENT to a state with one previewable tool result.
func agentWithToolOutput(t *testing.T) model {
	t.Helper()
	var am tea.Model = browseSeed(100)
	am, _ = am.Update(keyMsg("0")) // enter AGENT
	am, _ = am.Update(agentEventMsg{Kind: harness.EventToolResult, Tool: "list_dir", Result: "a.go\nb.go\nc.go\n"})
	return asModel(am)
}

// E1 - tool OUTPUT is HIDDEN by default; the result line carries a d·output hint instead.
func TestToolOutputHiddenByDefault(t *testing.T) {
	m := agentWithToolOutput(t)
	joined := stripANSI(strings.Join(m.displayAgentLines(), "\n"))
	if strings.Contains(joined, "a.go") {
		t.Errorf("tool output must be hidden by default:\n%s", joined)
	}
	if !strings.Contains(joined, "d·output") {
		t.Errorf("a hidden preview should advertise the d·output toggle:\n%s", joined)
	}
}

// E2 - with showToolOutput the preview content expands (retroactive, over the whole view).
func TestToolOutputExpands(t *testing.T) {
	m := agentWithToolOutput(t)
	m.showToolOutput = true
	joined := stripANSI(strings.Join(m.displayAgentLines(), "\n"))
	if !strings.Contains(joined, "a.go") || !strings.Contains(joined, "c.go") {
		t.Errorf("expanded output should show the preview content:\n%s", joined)
	}
	if strings.Contains(joined, "d·output") {
		t.Error("when expanded, the d·output hint should be gone")
	}
}

// E3 - `d` while the transcript pane is focused toggles the output (never while typing).
func TestToolOutputDToggle(t *testing.T) {
	var am tea.Model = agentWithToolOutput(t)
	am, _ = am.Update(tea.KeyMsg{Type: tea.KeyTab}) // focus the transcript pane
	if !asModel(am).agentPaneFocus {
		t.Fatal("tab should focus the transcript pane")
	}
	am, _ = am.Update(keyMsg("d"))
	if !asModel(am).showToolOutput {
		t.Error("d (pane focused) should expand the tool output")
	}
	am, _ = am.Update(keyMsg("d"))
	if asModel(am).showToolOutput {
		t.Error("d again should collapse it")
	}
}

// E4 - the toolOutMark (\x1e RS control byte) must NEVER leak into the copy / RC-backfill /
// operator-park transcript. ansi.Strip preserves C0 control bytes, so agentTranscriptText has
// to un-mark the tagged preview lines itself; otherwise every tool-output line ships an
// invisible \x1e into the clipboard and across the RC wire (claude-audit regression).
func TestAgentTranscriptTextHasNoToolOutMark(t *testing.T) {
	m := agentWithToolOutput(t)
	// The mark is present in the raw buffer (that is how the toggle works)...
	marked := false
	for _, l := range m.agentLines {
		if strings.HasPrefix(l, toolOutMark) {
			marked = true
			break
		}
	}
	if !marked {
		t.Fatal("precondition: the buffer should hold a toolOutMark-tagged preview line")
	}
	// ...but the exported transcript (copy / RC / park) must be clean of it, and must still
	// carry the actual preview content.
	txt := m.agentTranscriptText()
	if strings.Contains(txt, toolOutMark) {
		t.Errorf("agentTranscriptText leaked the toolOutMark control byte:\n%q", txt)
	}
	if !strings.Contains(txt, "a.go") {
		t.Errorf("the transcript should still include the tool-output content:\n%q", txt)
	}
}

// D1 - the tool call line is one DIM ⚙-prefixed line (tool + arg summary), never the old
// bright ◉ + bright tool-name treatment.
func TestAgentToolCallLineIsDim(t *testing.T) {
	colorOn(t, true)
	line := agentToolCallLine("run_shell", "git diff")
	flat := stripANSI(line)
	if !strings.Contains(flat, "run_shell") || !strings.Contains(flat, "git diff") {
		t.Errorf("the call line names the tool + args: %q", flat)
	}
	if !strings.Contains(flat, "⚙") {
		t.Errorf("the call line carries the ⚙ gear: %q", flat)
	}
	if strings.Contains(line, stKey.Render("run_shell")) {
		t.Error("the tool name must be DIM machinery texture, not the bright key style")
	}
	if !strings.Contains(line, stDim.Render("⚙ run_shell git diff")) {
		t.Error("the whole call line should be one dim string")
	}
}

// D2 - no args: just the dim ⚙ + tool.
func TestAgentToolCallLineNoArgs(t *testing.T) {
	if flat := stripANSI(agentToolCallLine("read_file", "")); !strings.Contains(flat, "⚙ read_file") {
		t.Errorf("bare call line: %q", flat)
	}
}

// D3 - the ⚙ gear has an ASCII fallback (it must read where Unicode won't render).
func TestAgentToolCallLineASCII(t *testing.T) {
	t.Setenv("ROGERAI_ASCII", "1")
	if flat := stripANSI(agentToolCallLine("run_shell", "ls")); strings.Contains(flat, "⚙") {
		t.Errorf("ASCII: the ⚙ gear must fold away: %q", flat)
	}
}
