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

// A DECLINED COMPACTION MUST SAY WHY (founder: "why didn't it auto compact"). Declining
// silently looks identical to being broken, and the operator is left to guess which.
func TestDeclinedCompactionExplainsItself(t *testing.T) {
	complete := func(ctx context.Context, msgs []Message, _ []map[string]any) (Message, error) {
		return Message{}, errors.New("Exceeded model context window size")
	}
	l := NewLoop(t.TempDir(), "sys", complete, nil)
	l.MaxSteps = 4
	// A window full of CONVERSATION - which compaction may never drop - rather than of
	// old tool output.
	l.messages = append(l.messages,
		Message{Role: "user", Content: strings.Repeat("q", 4000)},
		Message{Role: "assistant", Content: strings.Repeat("a", 8000)},
	)
	var notices []string
	_, err := l.Send(context.Background(), "and now this", func(e Event) {
		if e.Kind == EventNotice {
			notices = append(notices, e.Text)
		}
	})
	if err == nil {
		t.Fatal("with nothing to compact the turn must still fail")
	}
	if len(notices) == 0 {
		t.Fatal("the operator must be told compaction was considered and declined")
	}
	if !strings.Contains(notices[0], "nothing to compact") {
		t.Errorf("the notice must say what happened: %q", notices[0])
	}
	// ...and it must explain the RULE, not just report the outcome.
	if !strings.Contains(notices[0], "never drops what was said") {
		t.Errorf("the notice should say why it could not help: %q", notices[0])
	}
}

// ── SMALL WINDOWS ────────────────────────────────────────────────────────────
// Founder 2026-08-21: "are we able to manage low context window models like foundation
// ... i understand it only has 8k". A cleared session and one web_fetch was enough to
// overflow it. Measured on 8192 tokens (~24 KB at 3 B/token): persona 5058 B + tool
// schemas 2897 B = 32% of the window before the question is asked, and one tool result
// could take another 25%. Two calls put a fresh session at 82%.

func TestSmallBandGetsTheCompactPersona(t *testing.T) {
	full := DefaultPersona
	if got := PersonaFor(full, 8192); got != CompactPersona {
		t.Error("an 8k band must get the compact brief")
	}
	if len(CompactPersona) >= len(full) {
		t.Errorf("the compact brief must actually be smaller: %d vs %d", len(CompactPersona), len(full))
	}
	// EVERY RULE THAT CHANGES BEHAVIOUR SURVIVES. Trimming the coaching is fine;
	// trimming a rule that produced a real failure is how the failure comes back.
	for _, must := range []string{
		"rogerai.fm",                  // the identity brief - two wrong companies without it
		"never search the web",        // the third namesake
		"Never invent",                // invented URLs and file contents
		"read_file a file before you", // the blind overwrite
		"Do not use a tool when",      // tool calls on conversational turns
	} {
		if !strings.Contains(CompactPersona, must) {
			t.Errorf("the compact brief dropped a load-bearing rule: %q", must)
		}
	}
}

// A roomy band is untouched: this must not quietly cut instructions for the models most
// people actually use.
func TestRoomyBandKeepsTheFullPersona(t *testing.T) {
	for _, ctx := range []int{16384, 32768, 131072} {
		if got := PersonaFor(DefaultPersona, ctx); got != DefaultPersona {
			t.Errorf("ctx %d must keep the full persona", ctx)
		}
	}
	// UNKNOWN is not small. Guessing a band is tight and silently cutting its
	// instructions is worse than a turn that overflows and says so.
	if got := PersonaFor(DefaultPersona, 0); got != DefaultPersona {
		t.Error("an unknown window must keep the full persona")
	}
}

// A tight band also gets a smaller slice for one tool result - a quarter of the window
// is reasonable when overhead is 2%, not when it is already a third.
func TestSmallBandGetsASmallerToolSlice(t *testing.T) {
	small, big := ToolOutputBudget(8192), ToolOutputBudget(32768)
	if small >= big {
		t.Errorf("a tight band must get a smaller slice: %d vs %d", small, big)
	}
	if small < minToolOutput {
		t.Errorf("...but never below the floor (%d), or the result is too mutilated to be worth the call", minToolOutput)
	}
	// The whole point: the fixed overhead plus one result must leave room to think.
	fixed := len(CompactPersona) + 2897 // measured schema size
	if fixed+small > 8192*bytesPerToken/2 {
		t.Errorf("compact persona + schemas + one result = %d B, over half of an 8k window", fixed+small)
	}
}

// Swapping the persona rewrites the system message rather than adding a second one -
// two system messages would send both, costing more than the swap saves.
func TestSetPersonaRewritesRatherThanAppends(t *testing.T) {
	l := NewLoop(t.TempDir(), DefaultPersona, nil, nil)
	l.SetPersona(CompactPersona)
	n := 0
	for _, m := range l.messages {
		if m.Role == "system" {
			n++
			if m.Content != CompactPersona {
				t.Error("the system message must carry the new brief")
			}
		}
	}
	if n != 1 {
		t.Errorf("exactly one system message, got %d", n)
	}
}
