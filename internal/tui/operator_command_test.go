package tui

// operator_command_test.go — RED-phase spec tests for the /operator command joining the
// canonical agentCommands registry (features/operator/operator_command.feature; Guest
// Operators Phase 2). These tests reference ONLY existing symbols so the tui package
// still compiles: they fail ASSERTIVELY today ("/operator" is not registered yet), which
// is the RED evidence. Picker-state and handoff scenarios need new model fields and are
// carried by the .feature files (the approval artifacts) + the godog runner
// (operator_bdd_test.go) until GREEN.

import (
	"strings"
	"testing"
)

// TestOperatorInCommandRegistry: /operator is a first-class registry entry — the strip,
// /commands, and TestAgentCommandRegistrySeam pick it up from the ONE list
// (agent.go:585). The aliases follow the /dj //y pattern: typable, never registered.
func TestOperatorInCommandRegistry(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range agentCommands {
		seen[c] = true
	}
	if !seen["/operator"] {
		t.Fatalf("agentCommands must list /operator (Guest Operators Phase 2), got %v", agentCommands)
	}
	for _, alias := range []string{"/mic", "/guest", "/op"} {
		if seen[alias] {
			t.Fatalf("alias %s must be typable but NOT registered/suggested (the /dj //y pattern), got %v", alias, agentCommands)
		}
	}
}

// TestOperatorDispatches: /operator and each alias dispatch in runAgentCommand — a
// suggested or documented command can never come back "unknown:".
func TestOperatorDispatches(t *testing.T) {
	for _, cmd := range []string{"/operator", "/mic", "/guest", "/op"} {
		t.Run(cmd, func(t *testing.T) {
			m := asModel(agentReady(t))
			nm, _ := m.runAgentCommand(cmd)
			if view := stripANSI(asModel(nm).View()); strings.Contains(view, "unknown:") {
				t.Fatalf("%s must dispatch, runAgentCommand said unknown:\n%s", cmd, view)
			}
		})
	}
}

// TestOperatorZeroGuestsNote: with no guest detected (the default test model — nothing
// on the fake desk), bare /operator prints the desk note instead of opening a one-row
// picker (§3: "zero guests: /operator never opens a one-row picker").
func TestOperatorZeroGuestsNote(t *testing.T) {
	m := asModel(agentReady(t))
	nm, _ := m.runAgentCommand("/operator")
	view := stripANSI(asModel(nm).View())
	if !strings.Contains(view, "no guests at the desk") {
		t.Fatalf("bare /operator with zero guests must print the desk note, got:\n%s", view)
	}
}

// TestOperatorUnknownNameIsNoteNotTurn: /operator <garbage> is a local note — it must
// never be submitted as a chat turn (no spend from a typo).
func TestOperatorUnknownNameIsNoteNotTurn(t *testing.T) {
	m := asModel(agentReady(t))
	nm, _ := m.runAgentCommand("/operator warez9000")
	got := asModel(nm)
	if got.agentBusy {
		t.Fatalf("/operator <unknown> must never start a turn")
	}
	view := stripANSI(got.View())
	if strings.Contains(view, "unknown: /operator") {
		t.Fatalf("/operator with an argument must dispatch as the operator command:\n%s", view)
	}
}

// TestOperatorSlashStripSuggestsCanonicalOnly: typing /o suggests /operator; the
// aliases never appear in the candidate set (they are not registry entries).
func TestOperatorSlashStripSuggestsCanonicalOnly(t *testing.T) {
	cands := agentSlashCandidates("/o")
	found := false
	for _, c := range cands {
		if c == "/operator" {
			found = true
		}
		if c == "/mic" || c == "/guest" {
			t.Fatalf("aliases must never be suggested, got %v", cands)
		}
	}
	if !found {
		t.Fatalf("agentSlashCandidates(\"/o\") must include /operator, got %v", cands)
	}
}
