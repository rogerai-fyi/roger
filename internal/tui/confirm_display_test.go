package tui

import (
	"strings"
	"testing"
)

// WHAT THE OPERATOR READS AT THE GATE (founder 2026-08-21, on seeing the confirm work:
// "how can we improve and enhance this").

// A COMMAND MUST NOT BREAK MID-TOKEN. wrapPlain cut at exactly n runes, so the approval
// block turned `... | grep -v testdata | head -40` into "…testda" / "ta | head -40". That
// is the worst place in the product to break a word: the operator is being asked to
// approve THAT EXACT COMMAND, and a split token reads as a different one.
func TestTheApprovedCommandWrapsAtWhitespace(t *testing.T) {
	cmd := "ls projects 2>/dev/null; find projects roger-tower-lab -name Cargo.toml | grep -v node_modules | grep -v testdata | head -40"
	for _, w := range []int{30, 40, 60, 80} {
		lines := wrapCommand(cmd, w)
		for _, ln := range lines {
			if len([]rune(ln)) > w {
				t.Errorf("w=%d: a wrapped line is %d wide: %q", w, len([]rune(ln)), ln)
			}
		}
		// Every token must survive intact somewhere - none split across two lines.
		joined := strings.Join(lines, " ")
		for _, tok := range strings.Fields(cmd) {
			if !strings.Contains(joined, tok) {
				t.Errorf("w=%d: token %q was broken across lines:\n%s", w, tok, strings.Join(lines, "\n"))
			}
		}
	}
}

// A TOKEN LONGER THAN THE LINE must still be SHOWN, split, rather than dropped or
// truncated - hiding part of the command is hiding the thing being approved.
func TestAnOverlongTokenIsSplitNotDropped(t *testing.T) {
	long := strings.Repeat("a", 100)
	lines := wrapCommand("curl "+long, 20)
	if got := strings.ReplaceAll(strings.Join(lines, ""), " ", ""); !strings.Contains(got, long) {
		t.Errorf("an over-long token lost characters:\n%s", strings.Join(lines, "\n"))
	}
	for _, ln := range lines {
		if len([]rune(ln)) > 20 {
			t.Errorf("a split line is still too wide: %q", ln)
		}
	}
}

// THE GATE IS THE SOLE SURFACE while approval is pending, and the transcript must not keep
// a second, un-settleable copy. A "? <summary>" line used to be appended here and never
// removed or updated, so every ANSWERED confirm left a permanent "?" claiming to still be
// waiting - a turn with three shell calls showed three stale questions plus the live one.
func TestAPendingConfirmLeavesNoStaleTranscriptLine(t *testing.T) {
	m := privateTab(t)
	m.mode = modeAgent
	m.agent = m.newAgentRuntime()
	before := len(m.agentLines)

	c := agentConfirm{tool: "run_shell", args: map[string]any{"cmd": "echo hi"}, resp: make(chan bool, 1)}
	out, _ := m.Update(agentConfirmMsg(c))
	gm := asModel(out)

	if len(gm.agentLines) != before {
		t.Errorf("a pending confirm appended %d transcript line(s) it can never settle: %q",
			len(gm.agentLines)-before, gm.agentLines[before:])
	}
	// And the gate itself must still show the command - removing the duplicate must not
	// have removed the ONE place it is shown.
	if gm.agentPendingConfirm == nil {
		t.Fatal("the confirm did not become pending")
	}
	view := stripANSI(gm.agentView(100))
	if !strings.Contains(view, "APPROVAL REQUIRED") || !strings.Contains(view, "echo hi") {
		t.Errorf("the gate must show the command it is asking about:\n%s", view)
	}
}
