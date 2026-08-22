package tui

// Increment 7 of the TUI design overhaul: MACHINERY DIMS TO TEXTURE (§4). The tool CALL
// line recedes to a single dim ⚙-prefixed line so the tool chatter no longer competes
// with the answer prose; the result line's ✓/✕ still carries the outcome.

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"rogerai.fm/roger/v6/internal/harness"
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
//
// AMENDED 2026-08-20: there are TWO doors now. The machinery box (⌃o) is the outer one
// and is shut by default, and with it shut the output preview and its hint are inside
// it - correctly, since a hint hanging off a closed lid would point at a row the output
// does not belong to (TestClosedBoxSwallowsTheOutputHint owns that). This test is about
// the INNER door, `d`, so it opens the outer one first. The guarantee is unchanged.
func TestToolOutputHiddenByDefault(t *testing.T) {
	m := agentWithToolOutput(t)
	m.showToolCalls = true // open the machinery box; `d` is what is under test
	joined := stripANSI(strings.Join(m.displayAgentLines(80), "\n"))
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
	m.showToolCalls = true // the outer ⌃o door; see E1
	m.showToolOutput = true
	joined := stripANSI(strings.Join(m.displayAgentLines(80), "\n"))
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
	// AMENDED 2026-08-20: previews used to ride the buffer as toolOutMark-tagged lines;
	// they hang off the tool RECORD now (toolrun.go), which is what stopped the fold lid
	// from scraping tool names out of fetched page text. The precondition moves to where
	// the preview lives; the guarantee below - the exported transcript carries the
	// content and none of the control bytes - is unchanged and is the point of the test.
	held := false
	for _, r := range m.agentRuns {
		if len(r.Preview) > 0 {
			held = true
			break
		}
	}
	if !held {
		t.Fatal("precondition: a tool record should hold its preview")
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

// D1 - the running activity card uses one blue state lamp plus dim machinery text.
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
	if !strings.Contains(flat, "◐") || !strings.Contains(flat, "running") {
		t.Error("the running card should carry its blue state lamp and state")
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
