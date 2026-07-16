package tui

// Increment 7 of the TUI design overhaul: MACHINERY DIMS TO TEXTURE (§4). The tool CALL
// line recedes to a single dim ⚙-prefixed line so the tool chatter no longer competes
// with the answer prose; the result line's ✓/✕ still carries the outcome.

import (
	"strings"
	"testing"
)

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
