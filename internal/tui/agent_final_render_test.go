package tui

import (
	"strings"
	"testing"

	"github.com/rogerai-fyi/roger/internal/harness"
)

// lastAgentLines returns the transcript entries appended after n existing ones,
// ANSI-stripped, for readable assertions.
func lastAgentLines(m model, n int) []string {
	out := make([]string, 0, len(m.agentLines)-n)
	for _, l := range m.agentLines[n:] {
		out = append(out, stripANSI(l))
	}
	return out
}

// TestAgentFinalRenderShapes locks the EventFinal render contract: a spoken answer
// gets the block gutter, a thought-only final is labeled + dimmed + tail-clipped, a
// truncated empty final names the budget, and a plain empty final keeps the old line.
func TestAgentFinalRenderShapes(t *testing.T) {
	base := browseSeed(120)

	// Spoken multi-line answer is stored canonically so copy preserves markdown,
	// then shaped only at display time: "◂" first, quiet bar on the rest.
	m := base
	n := len(m.agentLines)
	nm, _ := m.Update(agentEventMsg{Kind: harness.EventFinal, Text: "first\nsecond"})
	rendered := asModel(nm)
	stored := rendered.agentLines[n:]
	if len(stored) != 1 || stored[0] != agentAnswerMark+"first\nsecond" {
		t.Fatalf("canonical answer storage = %q", stored)
	}
	got := rendered.displayAgentLines()
	got = got[len(got)-2:]
	if !strings.HasPrefix(stripANSI(got[0]), "◂ first") || !strings.HasPrefix(stripANSI(got[1]), "▏ second") {
		t.Errorf("display answer block = %q", got)
	}
	if transcript := rendered.agentTranscriptText(); !strings.Contains(transcript, "first\nsecond") {
		t.Errorf("copied transcript lost canonical answer: %q", transcript)
	}

	// Thought-only final: labeled as thinking aloud, gutters dimmed, tail clipped.
	long := strings.Repeat("preamble\n", agentThoughtClip+5) + "the actual conclusion"
	m = base
	n = len(m.agentLines)
	nm, _ = m.Update(agentEventMsg{Kind: harness.EventFinal, Text: long, Thought: true})
	joined := strings.Join(lastAgentLines(asModel(nm), n), "\n")
	if !strings.Contains(joined, "thought aloud, no spoken answer") {
		t.Errorf("thought final missing label:\n%s", joined)
	}
	if !strings.Contains(joined, "earlier thought lines") || !strings.Contains(joined, "the actual conclusion") {
		t.Errorf("thought final should clip to the tail and keep the conclusion:\n%s", joined)
	}

	// Truncated thought names the budget in the label.
	m = base
	n = len(m.agentLines)
	nm, _ = m.Update(agentEventMsg{Kind: harness.EventFinal, Text: "half a think", Thought: true, Truncated: true})
	joined = strings.Join(lastAgentLines(asModel(nm), n), "\n")
	if !strings.Contains(joined, "ran out of answer budget") {
		t.Errorf("truncated thought should name the budget:\n%s", joined)
	}

	// Empty + truncated: the actionable budget message, not the dead-end "(no text)".
	m = base
	n = len(m.agentLines)
	nm, _ = m.Update(agentEventMsg{Kind: harness.EventFinal, Text: "", Truncated: true})
	joined = strings.Join(lastAgentLines(asModel(nm), n), "\n")
	if !strings.Contains(joined, "answer budget ran out") {
		t.Errorf("empty truncated final should explain the budget:\n%s", joined)
	}

	// Plain empty final keeps the honest fallback.
	m = base
	n = len(m.agentLines)
	nm, _ = m.Update(agentEventMsg{Kind: harness.EventFinal, Text: ""})
	joined = strings.Join(lastAgentLines(asModel(nm), n), "\n")
	if !strings.Contains(joined, "finished with no text") {
		t.Errorf("plain empty final = %q", joined)
	}
}

// TestAgentAskSeparator: the first ask echoes bare; later asks are preceded by the
// dim time-stamped rule that chunks the transcript into turns.
func TestAgentAskSeparator(t *testing.T) {
	m := browseSeed(120)
	m.agentLines = nil
	first := m.agentAskLines("one")
	if len(first) != 1 || !strings.Contains(stripANSI(first[0]), "▌ one") {
		t.Errorf("first ask should echo bare, got %q", first)
	}
	m.agentLines = append(m.agentLines, first...)
	second := m.agentAskLines("two")
	if len(second) != 3 {
		t.Fatalf("later asks should carry blank+rule+ask, got %q", second)
	}
	if !strings.Contains(stripANSI(second[1]), "──") {
		t.Errorf("separator rule missing, got %q", stripANSI(second[1]))
	}
	if !strings.Contains(stripANSI(second[2]), "▌ two") {
		t.Errorf("ask line missing, got %q", second)
	}
}
