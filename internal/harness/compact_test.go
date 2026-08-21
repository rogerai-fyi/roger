package harness

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// compact_test.go - automatic compaction on a context-window overflow.
//
// Founder 2026-08-20, watching a turn die with "the conversation outgrew foundation's
// context window": "shouldn't we automatically trigger a compaction or something like
// that?" It should, and now does.

func TestContextOverflowIsRecognizedInEverySpelling(t *testing.T) {
	for _, s := range []string{
		"Exceeded model context window size",          // Apple foundation
		"context length exceeded",                     // OpenAI-compatible
		"context_length_exceeded",                     // the error code form
		"This model's maximum context length is 8192", // verbatim server text
		"too many tokens in prompt",
		"failed to allocate kv cache",
	} {
		if !IsContextOverflow(s) {
			t.Errorf("must recognize %q as an overflow - a spelling we miss is a turn we fail to save", s)
		}
	}
	for _, s := range []string{"no node offers that model", "connection refused", ""} {
		if IsContextOverflow(s) {
			t.Errorf("%q is not an overflow and must not trigger compaction", s)
		}
	}
}

// WHAT SURVIVES. Compaction drops raw tool material and never what anyone SAID: the
// operator's questions and the agent's conclusions are the session.
func TestCompactionDropsToolOutputAndKeepsTheConversation(t *testing.T) {
	l := NewLoop(t.TempDir(), "sys", nil, nil)
	big := strings.Repeat("x", 4000)
	l.messages = []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "read the docs and summarize"},
		{Role: "tool", Name: "web_fetch", Content: big},
		{Role: "assistant", Content: "the docs say the endpoint is local"},
		{Role: "tool", Name: "read_file", Content: big},
		{Role: "assistant", Content: "and the config lives in .roger"},
	}
	l.turnStart = len(l.messages)

	freed, dropped := l.compactForWindow(8000)
	if dropped != 2 || freed < 7000 {
		t.Fatalf("expected both tool results dropped, got dropped=%d freed=%d", dropped, freed)
	}
	for _, m := range l.messages {
		switch m.Role {
		case "user":
			if m.Content != "read the docs and summarize" {
				t.Error("a user message must never be pruned")
			}
		case "assistant":
			if !strings.HasPrefix(m.Content, "the docs say") && !strings.HasPrefix(m.Content, "and the config") {
				t.Error("an assistant reply must never be pruned - it is what the raw material became")
			}
		case "system":
			if m.Content != "sys" {
				t.Error("the system prompt must never be pruned")
			}
		}
	}
}

// The marker is HONEST: it names the tool and the size, so the model knows the material
// existed and is gone and can ask again, rather than believing the call returned nothing.
func TestPrunedMarkerTellsTheTruth(t *testing.T) {
	m := prunedMarker("web_fetch", 4000)
	for _, want := range []string{"4000", "web_fetch", "dropped", "again"} {
		if !strings.Contains(m, want) {
			t.Errorf("the marker must mention %q: %q", want, m)
		}
	}
}

// Compaction must not eat the CURRENT turn's material. Pruning what the model just
// fetched, in the turn it fetched it, strands the turn and invites an immediate re-fetch
// - trading an overflow for a loop.
func TestCompactionLeavesTheCurrentTurnAlone(t *testing.T) {
	l := NewLoop(t.TempDir(), "sys", nil, nil)
	big := strings.Repeat("y", 4000)
	l.messages = []Message{
		{Role: "tool", Name: "read_file", Content: big}, // an earlier turn: fair game
		{Role: "user", Content: "now what"},
		{Role: "tool", Name: "web_fetch", Content: big}, // THIS turn: off limits
	}
	l.turnStart = 1
	_, dropped := l.compactForWindow(1 << 20)
	if dropped != 1 {
		t.Fatalf("only the earlier turn's material may be pruned, dropped=%d", dropped)
	}
	if l.messages[2].Content != big {
		t.Error("the current turn's tool result must survive")
	}
}

// Already-pruned messages are not re-counted or re-pruned, so repeated compaction
// converges instead of shrinking markers into nothing.
func TestCompactionIsIdempotent(t *testing.T) {
	l := NewLoop(t.TempDir(), "sys", nil, nil)
	l.messages = []Message{{Role: "tool", Name: "read_file", Content: strings.Repeat("z", 4000)}}
	l.turnStart = 1
	l.compactForWindow(1 << 20)
	first := l.messages[0].Content
	if got := l.compactableBytes(); got != 0 {
		t.Errorf("a pruned result must not be prunable again, got %d bytes", got)
	}
	l.compactForWindow(1 << 20)
	if l.messages[0].Content != first {
		t.Error("compacting twice must not change an already-pruned message")
	}
}

// THE WHOLE POINT: an overflowing turn recovers by itself, once, and tells the operator.
func TestTurnRecoversFromOverflowByCompacting(t *testing.T) {
	calls := 0
	complete := func(ctx context.Context, msgs []Message, _ []map[string]any) (Message, error) {
		calls++
		if calls == 1 {
			return Message{}, errors.New("Exceeded model context window size")
		}
		return Message{Role: "assistant", Content: "10"}, nil
	}
	l := NewLoop(t.TempDir(), "sys", complete, nil)
	l.messages = append(l.messages, Message{Role: "tool", Name: "web_fetch", Content: strings.Repeat("q", 8000)})
	l.MaxSteps = 4

	var notices []string
	out, err := l.Send(context.Background(), "what is 5+5", func(e Event) {
		if e.Kind == EventNotice {
			notices = append(notices, e.Text)
		}
	})
	if err != nil {
		t.Fatalf("the turn should have recovered, got %v", err)
	}
	if out != "10" {
		t.Errorf("answer = %q, want the retried answer", out)
	}
	if calls != 2 {
		t.Errorf("expected one retry after compacting, got %d model calls", calls)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "compacted") {
		t.Errorf("the operator must be told the session was compacted, got %v", notices)
	}
}

// With nothing left to free, a retry would spend another billed call to fail the same
// way. The turn fails honestly instead, and the TUI's /clear advice is the real fix.
func TestNoPointlessRetryWhenThereIsNothingToFree(t *testing.T) {
	calls := 0
	complete := func(ctx context.Context, msgs []Message, _ []map[string]any) (Message, error) {
		calls++
		return Message{}, errors.New("context length exceeded")
	}
	l := NewLoop(t.TempDir(), "sys", complete, nil)
	l.MaxSteps = 4
	if _, err := l.Send(context.Background(), "hello", func(Event) {}); err == nil {
		t.Fatal("with nothing to compact the turn must fail")
	}
	if calls != 1 {
		t.Errorf("must not retry when compaction can free nothing, got %d calls", calls)
	}
}
