package tui

import (
	"strings"
	"testing"
)

// TestPermModeMachinery locks the approval-mode table: what each mode auto-approves,
// the accepted /perms spellings, and the shared help copy.
func TestPermModeMachinery(t *testing.T) {
	if permAllows(permConfirm, "write_file") || permAllows(permConfirm, "run_shell") {
		t.Error("confirm must auto-approve nothing")
	}
	if !permAllows(permEdits, "write_file") || permAllows(permEdits, "run_shell") {
		t.Error("edits must auto-approve write_file only")
	}
	if !permAllows(permAll, "write_file") || !permAllows(permAll, "run_shell") {
		t.Error("all must auto-approve everything")
	}
	for in, want := range map[string]agentPermMode{
		"confirm": permConfirm, "ask": permConfirm,
		"edits": permEdits, "auto-edits": permEdits,
		"all": permAll, "yolo": permAll, "bypass": permAll,
	} {
		if got, ok := parsePermMode(in); !ok || got != want {
			t.Errorf("parsePermMode(%q) = %v,%v want %v,true", in, got, ok, want)
		}
	}
	if _, ok := parsePermMode("nope"); ok {
		t.Error("parsePermMode should reject junk")
	}
}

// TestPermsCommandCycles: bare /perms cycles confirm -> edits -> all -> confirm, the
// explicit form sets, /yolo jumps to all, and the masthead chip appears only when
// permissive (ember at the full bypass).
func TestPermsCommandCycles(t *testing.T) {
	base := browseSeed(120)
	base.connected = &offer{NodeID: "n", Model: "gpt-oss-20b", Online: true}
	nm, _ := base.enterAgent()
	m := asModel(nm)
	if got := agentPermMode(m.agent.perms.Load()); got != permConfirm {
		t.Fatalf("default mode = %v want confirm", got)
	}
	if m.agentPermTag() != "" {
		t.Error("confirm default must add NO masthead chip")
	}

	step := func(cmdline string, want agentPermMode) {
		t.Helper()
		out, _ := m.runAgentCommand(cmdline)
		m = asModel(out)
		if got := agentPermMode(m.agent.perms.Load()); got != want {
			t.Fatalf("%s -> %v want %v", cmdline, got, want)
		}
	}
	step("/perms", permEdits)
	if !strings.Contains(stripANSI(m.agentPermTag()), "auto-edits") {
		t.Error("edits mode should chip the masthead")
	}
	step("/perms", permAll)
	if !strings.Contains(stripANSI(m.agentPermTag()), "AUTO-ALL") {
		t.Error("all mode should chip the masthead loudly")
	}
	step("/perms", permConfirm)
	step("/perms all", permAll)
	step("/perms confirm", permConfirm)
	step("/yolo", permAll)
	// The bypass line is loud (ember, bang) in the transcript.
	joined := stripANSI(strings.Join(m.agentLines, "\n"))
	if !strings.Contains(joined, "! tools auto-all") {
		t.Errorf("bypass should print the loud line, got:\n%s", joined)
	}
	// The idle help tail follows the mode.
	m.agentBusy = false
	if v := stripANSI(m.agentView(120)); !strings.Contains(v, "ALL tools auto-run") {
		t.Error("idle help should carry the auto-all warning tail")
	}
}
