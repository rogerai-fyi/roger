package tui

import (
	"fmt"
	"strings"
	"testing"

	"rogerai.fm/roger/v6/internal/harness"
)

func TestTuneInConversationHasStructuralRoleHierarchy(t *testing.T) {
	m := browseSeed(100)
	m.mode = modeChat
	m.connected = &offer{NodeID: "station", Model: "grok-4.3", Online: true}
	m.transcript = append(m.transcript, chatUserBlock("count the words in this sentence"))

	out, _ := m.update(chatMsg{reply: "There are six words.", tokensIn: 8, tokensOut: 5})
	m = asModel(out)
	// AMENDED 2026-08-21: the CHANNEL's turns are tagged and rendered at display time
	// now (the telegram blocks span the view, and only the display path knows how wide
	// that is), so the assertions read the rendered transcript rather than the raw
	// buffer. The guarantee - explicit roles, a breathing row between blocks, prose
	// before its metadata - is unchanged.
	plain := stripANSI(transcriptContent(m.displayChatLines(100), 100))
	if !strings.Contains(plain, "YOU ›") || !strings.Contains(plain, "ROGER ›") {
		t.Fatalf("conversation roles are not explicit:\n%s", plain)
	}
	if !strings.Contains(plain, "\n  \n") {
		t.Fatalf("user and answer blocks have no breathing row:\n%s", plain)
	}
	if strings.Index(plain, "There are six words.") > strings.Index(plain, "↑8") {
		t.Fatalf("reply metadata appeared before the prose:\n%s", plain)
	}
}

func TestOverCapComposerWindowFollowsCursor(t *testing.T) {
	value := make([]string, 10)
	for i := range value {
		value[i] = fmt.Sprintf("line-%02d", i+1)
	}
	draft := strings.Join(value, "\n")

	for _, tc := range []struct {
		name  string
		lines func(model) []string
		set   func(*model)
	}{
		{"AGENT", func(m model) []string { return m.agentPromptLines(80) }, func(m *model) {
			m.agentIn.SetValue(draft)
			m.agentIn.CursorEnd()
		}},
		{"TUNE IN", func(m model) []string { return m.chatPromptLines(80) }, func(m *model) {
			m.chatIn.SetValue(draft)
			m.chatIn.CursorEnd()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := browseSeed(80)
			tc.set(&m)
			plain := stripANSI(strings.Join(tc.lines(m), "\n"))
			if !strings.Contains(plain, "line-10") {
				t.Fatalf("cursor row is hidden:\n%s", plain)
			}
			if strings.Contains(plain, "line-01") {
				t.Fatalf("over-cap window did not follow the cursor to the tail:\n%s", plain)
			}
			if len(strings.Split(plain, "\n")) > 6 {
				t.Fatalf("composer exceeded its six-row cap:\n%s", plain)
			}
		})
	}
}

func TestGroundedSuggestionAdvertisesRightArrow(t *testing.T) {
	m := browseSeed(80)
	m.agentIn.Focus()
	m.agentNextHint = "run the tests or review the change"
	view := stripANSI(strings.Join(m.agentPromptLines(80), "\n"))
	if !strings.Contains(view, "→ accept") {
		t.Fatalf("grounded suggestion has no acceptance affordance: %q", view)
	}
	if strings.Contains(m.agentIn.Value(), "accept") {
		t.Fatal("acceptance affordance leaked into authored prompt value")
	}
}

func TestRecoveredApprovalCardAbsorbsResult(t *testing.T) {
	m := browseSeed(100)
	m.agent = m.newAgentRuntime()
	m.markAgentActivityApproved("run_shell")
	out, _ := m.onAgentEvent(agentEventMsg{
		Kind: harness.EventToolResult, Tool: "run_shell", Result: "hi",
	})
	m = asModel(out)
	// Rendered: a call is a record now and the buffer holds a reference to it.
	m.showToolCalls = true
	plain := stripANSI(strings.Join(m.displayAgentLines(100), "\n"))
	// Count CARDS, not name occurrences - the lid names its tools too.
	if strings.Count(plain, "run_shell   ") > 1 {
		t.Fatalf("fallback approval produced duplicate cards:\n%s", plain)
	}
	if !strings.Contains(plain, "approved") || !strings.Contains(plain, "2 bytes") {
		t.Fatalf("result did not update recovered approval card:\n%s", plain)
	}
}
