package harness

import (
	"context"
	"strings"
	"testing"
)

// The Loop is the choke point: EVERY tool result passes through it, so a tool that forgot
// to clip internally (or one added later) still cannot blow a small model's window.
// Continues features/agent/agent.feature; see ctxbudget_test.go for the incident.

// newCall builds a tool call for "name" (ToolCall.Function is an anonymous struct).
func newCall(id, name string) ToolCall {
	var c ToolCall
	c.ID, c.Type = id, "function"
	c.Function.Name, c.Function.Arguments = name, `{}`
	return c
}

// oneToolLoop builds a Loop whose single tool returns `out`, and whose completer calls that
// tool once and then answers. It returns the loop and a pointer to the tool result the
// model actually received.
func oneToolLoop(t *testing.T, out string, ctxWindow int) (*Loop, *string) {
	t.Helper()
	var seen string
	step := 0
	complete := func(_ context.Context, msgs []Message, _ []map[string]any) (Message, error) {
		step++
		if step == 1 {
			return Message{Role: "assistant", ToolCalls: []ToolCall{newCall("c1", "big_tool")}}, nil
		}
		// Second pass: capture what the tool handed back.
		for _, m := range msgs {
			if m.Role == "tool" && m.Name == "big_tool" {
				seen = m.Content
			}
		}
		return Message{Role: "assistant", Content: "done"}, nil
	}
	l := NewLoop(t.TempDir(), "", complete, func(string, map[string]any) bool { return true })
	l.MaxToolOutput = toolOutputBudget(ctxWindow)
	big := Tool{
		Name: "big_tool",
		Run: func(context.Context, string, map[string]any) (string, error) {
			return out, nil // deliberately NOT self-clipping
		},
	}
	l.tools = append(l.tools, big)
	l.toolByName["big_tool"] = big
	return l, &seen
}

// The incident, end to end through the real loop: a ~10KB tool result on an 8K band.
func TestLoopClipsToolOutputForASmallWindow(t *testing.T) {
	huge := strings.Repeat("y", 10103) // the founder's actual web_fetch size
	l, seen := oneToolLoop(t, huge, 8192)

	if _, err := l.Send(context.Background(), "go", func(Event) {}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if *seen == "" {
		t.Fatal("the tool result never reached the model")
	}
	if len(*seen) >= len(huge) {
		t.Fatalf("the loop passed %d bytes through unclipped on an 8K band", len(*seen))
	}
	// AMENDED 2026-08-21: an oversized result is now SPILLED to a file and the notice
	// names the path instead of just saying "truncated" (spill.go). The guarantee is
	// unchanged and is what matters - the model must never be handed a silently partial
	// result - so this asserts the CUT IS DECLARED, in whichever of the two ways applies.
	if !strings.Contains(*seen, "truncated") && !strings.Contains(*seen, "read_file it for the rest") {
		t.Errorf("the clipped result must declare that it was cut: %q", tailOf(*seen))
	}
}

// A big band must be unaffected: this fix must not silently shrink everyone else's tools.
func TestLoopLeavesLargeWindowsAlone(t *testing.T) {
	body := strings.Repeat("y", 10103)
	l, seen := oneToolLoop(t, body, 1_000_000)

	if _, err := l.Send(context.Background(), "go", func(Event) {}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if *seen != body {
		t.Errorf("a large-window loop altered a %d-byte result (got %d bytes)", len(body), len(*seen))
	}
}

// An unset budget (the zero value) must behave exactly as the loop always did, so every
// existing caller that never sets MaxToolOutput is unchanged.
func TestLoopUnsetBudgetIsUnbounded(t *testing.T) {
	body := strings.Repeat("y", 20000)
	var seen string
	step := 0
	complete := func(_ context.Context, msgs []Message, _ []map[string]any) (Message, error) {
		step++
		if step == 1 {
			return Message{Role: "assistant", ToolCalls: []ToolCall{newCall("c1", "big_tool")}}, nil
		}
		for _, m := range msgs {
			if m.Role == "tool" {
				seen = m.Content
			}
		}
		return Message{Role: "assistant", Content: "done"}, nil
	}
	l := NewLoop(t.TempDir(), "", complete, func(string, map[string]any) bool { return true })
	// MaxToolOutput deliberately left at its zero value.
	big := Tool{Name: "big_tool", Run: func(context.Context, string, map[string]any) (string, error) { return body, nil }}
	l.tools = append(l.tools, big)
	l.toolByName["big_tool"] = big

	if _, err := l.Send(context.Background(), "go", func(Event) {}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if seen != body {
		t.Errorf("an unset budget clipped the result (%d bytes of %d)", len(seen), len(body))
	}
}

// tailOf is the last chunk of a long result, for a readable failure message.
func tailOf(s string) string {
	if len(s) <= 200 {
		return s
	}
	return "..." + s[len(s)-200:]
}
