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

// A DIRECT turn's failure must not send the operator to the marketplace. Nothing about it
// went near the broker, so "put one on air with [2], or tune in [1]" is advice to fix a
// thing that was never involved.
func TestALocalFailureNamesTheLocalRemedy(t *testing.T) {
	lines := localFailureHint("connection refused", "grok-4.6", false)
	joined := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(joined, "DIRECT") && !strings.Contains(joined, "model server") {
		t.Errorf("a direct failure must name the local server, got %q", joined)
	}
	if strings.Contains(joined, "on air with") {
		t.Errorf("a direct failure sent the operator to the marketplace: %q", joined)
	}
	// A CONTEXT OVERFLOW is the one shape whose remedy is the same everywhere - the
	// conversation outgrew the window, and neither the market nor the server is broken.
	over := stripANSI(strings.Join(localFailureHint("maximum context length exceeded", "m", false), "\n"))
	if !strings.Contains(over, "/clear") {
		t.Errorf("an overflow must still offer /clear, got %q", over)
	}
	// Narrow keeps it short but still local.
	nar := stripANSI(strings.Join(localFailureHint("connection refused", "m", true), "\n"))
	if !strings.Contains(nar, "direct") {
		t.Errorf("the narrow form lost the route: %q", nar)
	}
}

// A DENIED tool settles the open row as refused rather than leaving it running forever.
func TestADeniedToolSettlesItsRow(t *testing.T) {
	m := privateTab(t)
	mm := &m
	mm.agentRuns = []toolRun{{Name: "run_shell", Status: toolRunning}}
	mm.agentOpenRun = 0
	mm.markAgentActivityDenied("run_shell")
	if mm.agentRuns[0].Status == toolRunning {
		t.Error("a denied tool was left showing as running")
	}
	// And with NO open row it records one, so the denial is visible at all.
	m2 := privateTab(t)
	m2m := &m2
	m2m.agentRuns = nil
	m2m.agentOpenRun = -1
	m2m.markAgentActivityDenied("write_file")
	if len(m2m.agentRuns) == 0 {
		t.Error("a denial with no open row vanished")
	}
}
