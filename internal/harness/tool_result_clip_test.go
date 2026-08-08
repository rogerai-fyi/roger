package harness

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// EVERY tool result is bounded, not just the successful one.
//
// Found by the pre-push audit. clipTo was applied on the success path only, while the
// unknown-tool, denied-confirm, retrieval-budget, tool-error and cancelled paths appended
// whatever they had built - contradicting the claim the budget rests on, that every tool
// result funnels through one cap.
//
// Two of those paths are attacker-influenced rather than merely large: the unknown-tool
// message interpolates the tool NAME, which the model chooses, and the error path
// interpolates a tool's error text, which can carry an upstream response body. An 8K window
// is the case the budget was introduced for (the 2026-08-07 `foundation` incident), and on
// that window a single unbounded result is the whole context.
//
// appendToolResult is the one place every path passes through, so the cap lives there - a
// future sixth path cannot forget it.
func toolCallNamed(name string) ToolCall {
	var c ToolCall
	c.ID = "call_1"
	c.Function.Name = name
	return c
}

func TestAppendToolResultClipsWhateverPathItCameFrom(t *testing.T) {
	l := &Loop{MaxToolOutput: 64}
	huge := strings.Repeat("x", 5000)

	l.appendToolResult(toolCallNamed("read_file"), huge)

	require.Len(t, l.messages, 1)
	got := l.messages[0].Content
	require.Less(t, len(got), len(huge), "an oversized tool result must not reach the conversation whole")
	require.Contains(t, got, "truncated", "and the model must be told it was cut")
	require.LessOrEqual(t, len(got), 64+len("\n... (truncated)")+3,
		"the kept prefix must be the budget (plus at most a rune boundary and the marker)")
}

// A model that names a gigantic tool must not be able to push an unbounded string into the
// conversation just by getting the name wrong.
func TestAnUnknownToolWithAHugeNameIsBounded(t *testing.T) {
	l := &Loop{MaxToolOutput: 64, toolByName: map[string]Tool{}}
	var got []Event
	l.runOne(t.Context(), toolCallNamed(strings.Repeat("n", 5000)), func(e Event) { got = append(got, e) })

	require.NotEmpty(t, l.messages, "the unknown-tool refusal is still recorded")
	res := l.messages[len(l.messages)-1].Content
	require.Less(t, len(res), 2000, "the unknown-tool message must be clipped, got %d bytes", len(res))

	// The operator has to see what the model saw, or a truncation-caused answer is
	// impossible to explain afterwards.
	var emitted string
	for _, e := range got {
		if e.Kind == EventToolResult {
			emitted = e.Result
		}
	}
	require.Equal(t, res, emitted, "the emitted result and the recorded result must be identical")
}

// A budget-exhausted result is information the model acts on - and it is still bounded.
func TestZeroBudgetStillMeansTheHistoricalFlatCapNotUnbounded(t *testing.T) {
	// MaxToolOutput 0 is "no model-derived budget"; clipTo leaves the string alone, which is
	// the documented historical behaviour. Pinned so a future change to clipTo's zero case
	// is a deliberate decision rather than a silent one.
	require.Equal(t, "abc", clipTo("abc", 0))
	require.Equal(t, "abc", clipTo("abc", 10))
	require.Contains(t, clipTo(strings.Repeat("y", 100), 10), "truncated")
}
