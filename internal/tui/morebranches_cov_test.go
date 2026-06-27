package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestPickAgentModelBranches covers pickAgentModel: a nil agent and an empty model are
// no-ops, picking a different model switches the runtime + logs it, and re-picking the
// same model reports "already running" without changing the model.
func TestPickAgentModelBranches(t *testing.T) {
	// nil agent + empty model: no panic, no state change.
	bare := browseSeed(100)
	bare.agent = nil
	bare.pickAgentModel("anything") // nil agent -> early return
	if bare.agentPicked {
		t.Error("pickAgentModel on a nil agent should be a no-op")
	}

	// Enter AGENT so a runtime exists.
	var am tea.Model = browseSeed(100)
	am, _ = am.Update(keyMsg("0"))
	m := asModel(am)
	if m.agent == nil {
		t.Fatal("entering AGENT should build the runtime")
	}
	m.agent.model = "gpt-oss-20b"

	// Empty model -> no-op.
	before := len(m.agentLines)
	m.pickAgentModel("")
	if len(m.agentLines) != before {
		t.Error("pickAgentModel(\"\") should not log anything")
	}

	// Switch to a different model.
	m.pickAgentModel("llama-3.3-70b-instruct")
	if m.agent.model != "llama-3.3-70b-instruct" || !m.agentPicked {
		t.Fatalf("a switch should re-point the runtime, model=%q picked=%v", m.agent.model, m.agentPicked)
	}
	if !strings.Contains(stripANSI(m.agentLines[len(m.agentLines)-1]), "switched") {
		t.Errorf("a switch should log a 'switched' line, got %q", stripANSI(m.agentLines[len(m.agentLines)-1]))
	}

	// Re-pick the SAME model -> "already running", no change to the model.
	m.pickAgentModel("llama-3.3-70b-instruct")
	if m.agent.model != "llama-3.3-70b-instruct" {
		t.Error("re-picking the same model should not change it")
	}
	if !strings.Contains(stripANSI(m.agentLines[len(m.agentLines)-1]), "already running") {
		t.Errorf("re-picking should log 'already running', got %q", stripANSI(m.agentLines[len(m.agentLines)-1]))
	}
}

// TestSuccessCellClamps covers successCell's clamps: a negative rate floors to 0%, an
// over-unity rate ceils to 100%.
func TestSuccessCellClamps(t *testing.T) {
	if got := successCell(-0.5, true); got != "0%" {
		t.Errorf("negative rate should floor to 0%%, got %q", got)
	}
	if got := successCell(1.7, true); got != "100%" {
		t.Errorf("over-unity rate should ceil to 100%%, got %q", got)
	}
	if got := successCell(0.5, true); got != "50%" {
		t.Errorf("mid rate should round to 50%%, got %q", got)
	}
}

// TestOnSharesDetectedPending: the `/share <model>` shortcut flips that exact model on
// air, and a re-detect that finds nothing while shareRescan is set leaves the wizard note.
func TestOnSharesDetectedPending(t *testing.T) {
	// An empty re-scan in the wizard with shareRescan set writes the "give it a moment" note.
	w := New("http://broker.local", "tester")
	w.width, w.height = 100, 30
	w.setupOnEmpty = true
	w.shareRescan = true
	wm, _ := w.onSharesDetected(nil, nil)
	got := asModel(wm)
	if got.mode != modeShareSetup {
		t.Fatalf("an empty re-scan should stay in the wizard, mode=%v", got.mode)
	}
	if !strings.Contains(got.setupErr, "still nothing") && !strings.Contains(got.setupErr, "give it a moment") {
		t.Errorf("a re-scan note should be set, got %q", got.setupErr)
	}
}

// TestHistoryPathHeadless: with neither XDG_CONFIG_HOME nor HOME resolvable, historyPath
// returns "" (the in-memory-only fallback).
func TestHistoryPathHeadless(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	if got := historyPath("history-chat"); got != "" {
		t.Errorf("with no config/home dir, historyPath should be empty, got %q", got)
	}
}
