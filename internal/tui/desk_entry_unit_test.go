package tui

// desk_entry_unit_test.go - focused unit tests for the AGENT [0] desk-entry primitives
// that the BDD exercises only indirectly: the noteOnce tail-compare dedup (the spam
// guard) and the clearFindingBeat single-line splice (the echo-preservation fix).

import "testing"

// TestNoteOnceDedup pins noteOnce's contract: an identical block already AT THE TAIL is
// skipped; a block whose tail differs is appended. Multi-line blocks dedup as a unit.
func TestNoteOnceDedup(t *testing.T) {
	m := &model{}
	a, b := "✕ no station on air right now", "  [1] tune in · [2] go on air"

	m.noteOnce(a, b)
	m.noteOnce(a, b) // identical block at the tail -> skipped
	if got := len(m.agentLines); got != 2 {
		t.Fatalf("noteOnce stacked an identical tail block: %d lines %q", got, m.agentLines)
	}

	m.agentLines = append(m.agentLines, "▸ a user turn") // an intervening line
	m.noteOnce(a, b)                                     // tail differs now -> appended
	if got := len(m.agentLines); got != 5 {
		t.Fatalf("noteOnce should re-append after a different tail: %d lines %q", got, m.agentLines)
	}

	// A single-line note dedups against a single-line tail too.
	m.agentLines = []string{"x"}
	m.noteOnce("x")
	if got := len(m.agentLines); got != 1 {
		t.Fatalf("single-line noteOnce stacked: %d", got)
	}
}

// TestClearFindingBeatKeepsParkedEcho pins the echo-preservation fix: clearing the beat
// removes ONLY the beat line, never a prompt echo parked after it.
func TestClearFindingBeatKeepsParkedEcho(t *testing.T) {
	m := &model{
		agentLines:      []string{"· AGENT ready", "· finding a free band…", "▸ my parked prompt"},
		autoTuneBeatLen: 1, // the beat sits at index 1
	}
	m.clearFindingBeat()
	if len(m.agentLines) != 2 || m.agentLines[1] != "▸ my parked prompt" {
		t.Fatalf("clearFindingBeat did not preserve the parked echo: %q", m.agentLines)
	}
	if m.autoTuneBeatLen != 0 {
		t.Fatalf("clearFindingBeat should reset autoTuneBeatLen, got %d", m.autoTuneBeatLen)
	}
}

// TestClearFindingBeatGuardsNonBeat pins the content guard: if the indexed line is not
// the beat (the transcript shifted underneath), nothing is removed.
func TestClearFindingBeatGuardsNonBeat(t *testing.T) {
	m := &model{
		agentLines:      []string{"· AGENT ready", "▸ some other line"},
		autoTuneBeatLen: 1,
	}
	m.clearFindingBeat()
	if len(m.agentLines) != 2 {
		t.Fatalf("clearFindingBeat removed a non-beat line: %q", m.agentLines)
	}
}
