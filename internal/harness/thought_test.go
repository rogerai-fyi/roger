package harness

import (
	"context"
	"strings"
	"testing"
)

// TestParseCompletionReasoningContent: a thought-only reply (llama.cpp's
// `reasoning_content`, DeepSeek style) surfaces as Thought, never as Content, and
// finish_reason=length sets Truncated. This was the founder's "the agent finished
// with no text": the words were in reasoning_content and the parser read `reasoning`.
func TestParseCompletionReasoningContent(t *testing.T) {
	raw := []byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"","reasoning_content":"the repo has 22 agentic runs"}}]}`)
	msg, err := parseCompletion(raw, 200)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if msg.Content != "" {
		t.Errorf("reasoning must not masquerade as Content, got %q", msg.Content)
	}
	if msg.Thought != "the repo has 22 agentic runs" {
		t.Errorf("Thought = %q", msg.Thought)
	}
	if msg.Truncated {
		t.Error("stop finish must not read as truncated")
	}

	// The other backend key still works.
	raw = []byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"","reasoning":"alt key"}}]}`)
	msg, _ = parseCompletion(raw, 200)
	if msg.Thought != "alt key" {
		t.Errorf("legacy reasoning key Thought = %q", msg.Thought)
	}

	// Budget exhaustion is marked.
	raw = []byte(`{"choices":[{"finish_reason":"length","message":{"role":"assistant","content":"","reasoning_content":"half a think"}}]}`)
	msg, _ = parseCompletion(raw, 200)
	if !msg.Truncated || msg.Thought != "half a think" {
		t.Errorf("length finish: Truncated=%v Thought=%q", msg.Truncated, msg.Thought)
	}

	// A spoken answer never picks up a Thought.
	raw = []byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"spoken","reasoning_content":"hidden"}}]}`)
	msg, _ = parseCompletion(raw, 200)
	if msg.Content != "spoken" || msg.Thought != "" {
		t.Errorf("spoken reply: Content=%q Thought=%q", msg.Content, msg.Thought)
	}
}

// TestLoopSurfacesThoughtFinal: the loop marks a thought-only final so the UI can
// render it as thinking aloud (Thought=true rides the EventFinal).
func TestLoopSurfacesThoughtFinal(t *testing.T) {
	completer := func(_ context.Context, _ []Message, _ []map[string]any) (Message, error) {
		return Message{Role: "assistant", Thought: "wrapped up inside the think", Truncated: true}, nil
	}
	l := NewLoop(t.TempDir(), "persona", completer, func(string, map[string]any) bool { return false })
	var final Event
	_, err := l.Send(context.Background(), "hi", func(e Event) {
		if e.Kind == EventFinal {
			final = e
		}
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if !final.Thought || !final.Truncated {
		t.Errorf("final should carry Thought+Truncated, got %+v", final)
	}
	if !strings.Contains(final.Text, "wrapped up") {
		t.Errorf("final text should be the reasoning, got %q", final.Text)
	}
}
