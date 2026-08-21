package harness

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// parallel_test.go - overlapping tool bodies.
//
// The property worth having is not "it is faster", it is "it is faster and you cannot
// tell from the output". Most of these assert the second half.

// slowTool records concurrency and sleeps, so a test can prove bodies actually overlap.
func slowTool(name string, concurrent bool, live *int32, peak *int32, order *[]string, mu *sync.Mutex) Tool {
	return Tool{
		Name: name, Description: name, Concurrent: concurrent,
		Params: map[string]any{"type": "object"},
		Run: func(ctx context.Context, root string, args map[string]any) (string, error) {
			n := atomic.AddInt32(live, 1)
			for {
				old := atomic.LoadInt32(peak)
				if n <= old || atomic.CompareAndSwapInt32(peak, old, n) {
					break
				}
			}
			time.Sleep(25 * time.Millisecond)
			atomic.AddInt32(live, -1)
			mu.Lock()
			*order = append(*order, name)
			mu.Unlock()
			return name + " done", nil
		},
	}
}

func batchLoop(t *testing.T, tools []Tool, calls []ToolCall) (*Loop, []Event) {
	t.Helper()
	step := 0
	complete := func(ctx context.Context, msgs []Message, _ []map[string]any) (Message, error) {
		step++
		if step == 1 {
			return Message{Role: "assistant", ToolCalls: calls}, nil
		}
		return Message{Role: "assistant", Content: "done"}, nil
	}
	l := NewLoop(t.TempDir(), "sys", complete, func(string, map[string]any) bool { return true })
	l.tools = tools
	l.toolByName = map[string]Tool{}
	for _, tl := range tools {
		l.toolByName[tl.Name] = tl
	}
	l.Guards = []Guard{} // guard behaviour has its own suite; this one is about scheduling
	l.MaxSteps = 4
	var events []Event
	if _, err := l.Send(context.Background(), "go", func(e Event) { events = append(events, e) }); err != nil {
		t.Fatalf("turn failed: %v", err)
	}
	return l, events
}

// pcall builds a tool call. Named pcall because injection_bdd_test.go already owns
// `call` in this package.
func pcall(id, name string) ToolCall {
	var c ToolCall
	c.ID = id
	c.Function.Name = name
	c.Function.Arguments = "{}"
	return c
}

// Read-only calls actually overlap.
func TestConcurrentReadsOverlap(t *testing.T) {
	var live, peak int32
	var mu sync.Mutex
	var order []string
	tools := []Tool{
		slowTool("r1", true, &live, &peak, &order, &mu),
		slowTool("r2", true, &live, &peak, &order, &mu),
		slowTool("r3", true, &live, &peak, &order, &mu),
	}
	batchLoop(t, tools, []ToolCall{pcall("a", "r1"), pcall("b", "r2"), pcall("c", "r3")})
	if peak < 2 {
		t.Errorf("read-only bodies should overlap, peak concurrency was %d", peak)
	}
}

// A side-effecting tool is a BARRIER: it never shares the wire with anything.
func TestMutatingToolNeverOverlaps(t *testing.T) {
	var live, peak int32
	var mu sync.Mutex
	var order []string
	w := slowTool("writer", false, &live, &peak, &order, &mu)
	w.Mutating = true
	tools := []Tool{
		slowTool("r1", true, &live, &peak, &order, &mu),
		w,
		slowTool("r2", true, &live, &peak, &order, &mu),
	}
	batchLoop(t, tools, []ToolCall{pcall("a", "r1"), pcall("b", "writer"), pcall("c", "r2")})
	if peak != 1 {
		t.Errorf("a mutating call must run alone: peak concurrency %d", peak)
	}
}

// THE POINT. An overlapped batch produces exactly the conversation a serial one would:
// results in the model's order, each tool_call_id answered once, in sequence.
func TestOverlapIsInvisibleInTheConversation(t *testing.T) {
	var live, peak int32
	var mu sync.Mutex
	var order []string
	// r1 is the SLOWEST, so if settling followed completion order it would land last.
	slow := Tool{
		Name: "r1", Description: "r1", Concurrent: true, Params: map[string]any{"type": "object"},
		Run: func(ctx context.Context, root string, args map[string]any) (string, error) {
			time.Sleep(60 * time.Millisecond)
			return "r1 done", nil
		},
	}
	tools := []Tool{slow,
		slowTool("r2", true, &live, &peak, &order, &mu),
		slowTool("r3", true, &live, &peak, &order, &mu),
	}
	l, events := batchLoop(t, tools, []ToolCall{pcall("a", "r1"), pcall("b", "r2"), pcall("c", "r3")})

	var ids, names []string
	for _, m := range l.messages {
		if m.Role == "tool" {
			ids = append(ids, m.ToolCallID)
			names = append(names, m.Name)
		}
	}
	if got := strings.Join(ids, ","); got != "a,b,c" {
		t.Errorf("tool results must land in the model's order, got %q", got)
	}
	if got := strings.Join(names, ","); got != "r1,r2,r3" {
		t.Errorf("result names out of order: %q", got)
	}
	// The emitted events the operator sees follow the same order.
	var seen []string
	for _, e := range events {
		if e.Kind == EventToolResult {
			seen = append(seen, e.Tool)
		}
	}
	if got := strings.Join(seen, ","); got != "r1,r2,r3" {
		t.Errorf("result EVENTS must follow the model's order too, got %q", got)
	}
}

// Grouping stops at the first tool that has not opted in, and never runs past the cap.
func TestConcurrentGroupBoundaries(t *testing.T) {
	l := NewLoop(t.TempDir(), "sys", nil, nil)
	l.toolByName = map[string]Tool{
		"safe": {Name: "safe", Concurrent: true},
		"excl": {Name: "excl"},
	}
	calls := []ToolCall{pcall("1", "safe"), pcall("2", "safe"), pcall("3", "excl"), pcall("4", "safe")}
	if n := l.concurrentGroup(calls, 0); n != 2 {
		t.Errorf("group should stop at the exclusive tool, got %d", n)
	}
	if n := l.concurrentGroup(calls, 2); n != 1 {
		t.Errorf("an exclusive tool runs alone, got %d", n)
	}
	// An UNKNOWN tool is never grouped - it has made no promise about its Run.
	if n := l.concurrentGroup([]ToolCall{pcall("1", "mystery"), pcall("2", "safe")}, 0); n != 1 {
		t.Errorf("an unknown tool must run alone, got %d", n)
	}
	// The fan-out is capped.
	var many []ToolCall
	for i := 0; i < maxParallelTools+3; i++ {
		many = append(many, pcall(fmt.Sprint(i), "safe"))
	}
	if n := l.concurrentGroup(many, 0); n != maxParallelTools {
		t.Errorf("fan-out must cap at %d, got %d", maxParallelTools, n)
	}
}

// A panicking tool fails its own call instead of taking the turn's goroutine with it.
func TestPanickingToolFailsOnlyItself(t *testing.T) {
	boom := Tool{
		Name: "boom", Description: "boom", Concurrent: true, Params: map[string]any{"type": "object"},
		Run: func(ctx context.Context, root string, args map[string]any) (string, error) {
			panic("tool exploded")
		},
	}
	ok := Tool{
		Name: "ok", Description: "ok", Concurrent: true, Params: map[string]any{"type": "object"},
		Run: func(ctx context.Context, root string, args map[string]any) (string, error) {
			return "fine", nil
		},
	}
	l, _ := batchLoop(t, []Tool{boom, ok}, []ToolCall{pcall("a", "boom"), pcall("b", "ok")})
	var results []string
	for _, m := range l.messages {
		if m.Role == "tool" {
			results = append(results, m.Content)
		}
	}
	if len(results) != 2 {
		t.Fatalf("both calls must settle, got %d results", len(results))
	}
	if !strings.Contains(results[0], "panicked") {
		t.Errorf("the panicking call must report it: %q", results[0])
	}
	if results[1] != "fine" {
		t.Errorf("its sibling must be unaffected: %q", results[1])
	}
}
