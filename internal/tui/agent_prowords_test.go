package tui

// Increment 2 of the TUI design overhaul: the self-evident PROWORDS layer (§2,
// middle-ground ruling). On-screen TEXT only, zero audio. The transcript's turn-
// taking events read as radio hand-off words a person understands cold - STANDBY
// (parked behind a busy turn), WILCO (received + doing it - a tool run approved),
// BREAK (interrupt/stop) - the cheapest, most ownable differentiator we have. The
// glyph glints stay (⏳/✓/✕, founder 2026-07-15); only the WORD changes.
//
// Spec approved 2026-07-15 (increment 2). No mocks: the REAL onAgentKey handlers
// are driven through Update() over the queue / confirm / stop paths.

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// allLines joins the transcript, ANSI stripped, for substring assertions.
func allLines(m model) string { return stripANSI(strings.Join(m.agentLines, "\n")) }

// W1/W3/W4 - approving a tool at the confirm gate reads WILCO (received + complying),
// keeps the ✓ go-glint, drops the old "approved · running", and still answers the
// loop's resp channel; denying stays PLAIN English (no proword for deny).
func TestProwordWilcoOnApprove(t *testing.T) {
	m := agentSeed(t, "http://broker.local")
	resp := make(chan bool, 1)
	m.agentPendingConfirm = &agentConfirm{tool: "run_shell", resp: resp}

	out, _ := m.Update(keyMsg("y"))
	m = asModel(out)

	line := allLines(m)
	if !strings.Contains(line, "WILCO") || !strings.Contains(line, "run_shell") {
		t.Errorf("W1: approve should read `WILCO · run_shell`, got %q", line)
	}
	if !strings.Contains(line, "✓") {
		t.Error("W1: keep the ✓ go-glint on the WILCO line")
	}
	if strings.Contains(line, "approved · running") {
		t.Error("W1: the old `approved · running` copy must be gone")
	}
	if got := <-resp; !got {
		t.Error("W3: the loop's resp channel must still receive true (copy-only change)")
	}
}

// W2 - the ctrl+p-escalate-to-auto-approve path resolves the SAME gate and must emit
// the same WILCO line (shared helper, not a second copy). write_file is covered the
// moment perms reach `edits`, so one ctrl+p approves it.
func TestProwordWilcoOnEscalateApprove(t *testing.T) {
	m := agentSeed(t, "http://broker.local")
	resp := make(chan bool, 1)
	m.agentPendingConfirm = &agentConfirm{tool: "write_file", resp: resp}

	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	m = asModel(out)

	if line := allLines(m); !strings.Contains(line, "WILCO") || !strings.Contains(line, "write_file") {
		t.Errorf("W2: ctrl+p auto-approve should also read `WILCO · write_file`, got %q", line)
	}
	if m.agentPendingConfirm != nil || len(resp) != 1 || !<-resp {
		t.Fatal("W2: edits covers write_file - the gate should resolve as approved")
	}
}

// W4 - denying keeps PLAIN English (the ruling: only self-evident radio words; deny
// has none), so it stays `denied · <tool> was not run`, no proword.
func TestProwordDenyStaysPlain(t *testing.T) {
	m := agentSeed(t, "http://broker.local")
	resp := make(chan bool, 1)
	m.agentPendingConfirm = &agentConfirm{tool: "run_shell", resp: resp}

	out, _ := m.Update(keyMsg("n"))
	m = asModel(out)

	line := allLines(m)
	if !strings.Contains(line, "denied") {
		t.Errorf("W4: deny should stay plain `denied …`, got %q", line)
	}
	if strings.Contains(line, "WILCO") {
		t.Error("W4: deny is not a WILCO")
	}
	if got := <-resp; got {
		t.Error("W4: deny must answer the gate false")
	}
}

// S1/S2 - a prompt typed while a turn runs is parked as STANDBY (was `queued`),
// keeping the ⏳ glyph, with the prompt text still shown.
func TestProwordStandbyOnQueue(t *testing.T) {
	m := agentSeed(t, "http://broker.local")
	m.agentBusy = true
	m.agentIn.SetValue("fix the failing suite too")

	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = asModel(out)

	line := allLines(m)
	if !strings.Contains(line, "STANDBY") {
		t.Errorf("S1: a busy-queued ask should read STANDBY, got %q", line)
	}
	if !strings.Contains(line, "⏳") {
		t.Error("S1: keep the ⏳ glyph on the STANDBY line (founder)")
	}
	if strings.Contains(line, "queued") {
		t.Error("S1: the old `queued` word must be gone")
	}
	if !strings.Contains(line, "fix the failing suite too") {
		t.Errorf("S2: the parked prompt text must still show, got %q", line)
	}
	if len(m.agentQueued) != 1 {
		t.Fatalf("S: the ask must still be parked (queue behavior unchanged), got %d", len(m.agentQueued))
	}
}

// S3 - the jump-the-queue path (submitAgentPrompt while the loop is still unwinding a
// force-stopped turn) also reads STANDBY, not `queued`.
func TestProwordStandbyOnJumpQueue(t *testing.T) {
	m := agentSeed(t, "http://broker.local")
	m.agent.running.Store(true) // the previous turn's goroutine is still alive

	nm, _ := m.submitAgentPrompt(queuedPrompt{text: "next ask"})
	line := allLines(nm)
	if !strings.Contains(line, "STANDBY") || strings.Contains(line, "queued") {
		t.Errorf("S3: the jump-queue park should read STANDBY, got %q", line)
	}
}

// B1/B2 - force-stopping a running turn reads BREAK (the interrupt proword), keeps the
// ✕ glint, and actually stops the turn (agentBusy cleared).
func TestProwordBreakOnForceStop(t *testing.T) {
	m := agentSeed(t, "http://broker.local")
	m.agentBusy = true
	m.agentCanceling = true // as if the first esc already fired; this esc force-stops

	out, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = asModel(out)

	line := allLines(m)
	if !strings.Contains(line, "BREAK") {
		t.Errorf("B1: a force-stop should read BREAK, got %q", line)
	}
	if !strings.Contains(line, "✕") {
		t.Error("B1: keep the ✕ glint on the BREAK line")
	}
	if m.agentBusy {
		t.Error("B2: force-stop must actually stop the turn (behavior unchanged)")
	}
}

// ON AIR (catalog #4) is already the sharing copy (tui.go go-live status / onAirPanel)
// and is deliberately UNCHANGED by this proword pass - no swap, no new test.
