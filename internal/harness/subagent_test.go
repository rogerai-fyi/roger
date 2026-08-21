package harness

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// subagent_test.go - delegation, and the receipt model behind it.
//
// Founder 2026-08-21 chose per-subagent receipts, aggregated afterwards. These pin the
// three things that choice made load-bearing: the shared ceiling, the honest rollup,
// and the depth cap.

// turnStartOf finds the last user message, i.e. where this turn began.
func turnStartOf(msgs []Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return i
		}
	}
	return 0
}

// delegateLoop builds a root whose model delegates once per turn, then answers.
func delegateLoop(t *testing.T, childBehaviour func(msgs []Message) (Message, error)) *Loop {
	t.Helper()
	var mu sync.Mutex
	complete := func(ctx context.Context, msgs []Message, _ []map[string]any) (Message, error) {
		mu.Lock()
		defer mu.Unlock()
		// A child's conversation opens with the subagent persona; that is how this stub
		// tells parent turns from child turns.
		if len(msgs) > 0 && strings.Contains(msgs[0].Content, "research subagent") {
			return childBehaviour(msgs)
		}
		// Delegate once per TURN, decided from the conversation rather than a counter:
		// a counter carried across turns and made the second turn never delegate, which
		// looked like a receipt bug and was a stub bug.
		delegated := false
		for _, m := range msgs[turnStartOf(msgs):] {
			if m.Role == "tool" && m.Name == "delegate" {
				delegated = true
			}
		}
		if !delegated {
			var c ToolCall
			c.ID = "d1"
			c.Function.Name = "delegate"
			c.Function.Arguments = `{"task":"find the thing"}`
			return Message{Role: "assistant", ToolCalls: []ToolCall{c}}, nil
		}
		return Message{Role: "assistant", Content: "parent done"}, nil
	}
	l := NewLoop(t.TempDir(), "sys", complete, nil)
	l.Guards = []Guard{}
	l.MaxSteps = 4
	return l
}

// THE CEILING IS THE TURN'S, NOT THE CHILD'S. This is the con that changed the design:
// if a child owned its budget, a parent could spawn children to multiply its allowance.
func TestSubagentSharesTheParentsBudget(t *testing.T) {
	l := NewLoop(t.TempDir(), "sys", nil, nil)
	l.budget = &turnBudget{}
	child := l.newSubagent(t.TempDir())
	if child.budget != l.budget {
		t.Fatal("a subagent must charge the PARENT's budget, or the ceiling scales with children")
	}
	// Spend the parent's search allowance from the CHILD and the parent is out too.
	for i := 0; i < maxSearchesPerTurn; i++ {
		if r := child.chargeRetrieval("web_search"); r != "" {
			t.Fatalf("child search %d refused early: %s", i, r)
		}
	}
	if r := l.chargeRetrieval("web_search"); r == "" {
		t.Error("the parent must be out of budget once its child spent it")
	}
}

// A subagent is READ-ONLY and cannot delegate: depth is capped by construction, not by
// a counter someone has to remember to check.
func TestSubagentToolsetIsReadOnlyAndCannotDelegate(t *testing.T) {
	l := NewLoop(t.TempDir(), "sys", nil, nil)
	child := l.newSubagent(t.TempDir())
	for _, tl := range child.Tools() {
		if tl.Mutating {
			t.Errorf("a subagent must not carry the mutating tool %q", tl.Name)
		}
		if tl.Name == "delegate" {
			t.Error("a subagent must not be able to delegate - depth is capped at one")
		}
	}
	if child.confirm != nil {
		t.Error("a read-only child never reaches the confirm gate and must not hold one")
	}
	// ...and the parent DOES have it.
	found := false
	for _, tl := range l.Tools() {
		if tl.Name == "delegate" {
			found = true
		}
	}
	if !found {
		t.Error("the root loop must advertise delegate")
	}
}

// The child's answer comes back as the tool result, and its receipt is recorded.
func TestDelegateReturnsTheChildsAnswerAndItsReceipt(t *testing.T) {
	l := delegateLoop(t, func([]Message) (Message, error) {
		return Message{Role: "assistant", Content: "the thing is 42"}, nil
	})
	out, err := l.Send(context.Background(), "go find it", func(Event) {})
	if err != nil {
		t.Fatalf("turn failed: %v", err)
	}
	if out != "parent done" {
		t.Errorf("parent answer = %q", out)
	}
	var toolResult string
	for _, m := range l.messages {
		if m.Role == "tool" && m.Name == "delegate" {
			toolResult = m.Content
		}
	}
	if !strings.Contains(toolResult, "42") {
		t.Errorf("the child's answer must come back as the tool result, got %q", toolResult)
	}
	rc := l.TurnReceipt()
	if len(rc.Children) != 1 {
		t.Fatalf("one delegation should record one child receipt, got %d", len(rc.Children))
	}
	if !rc.Children[0].Complete || !rc.Complete {
		t.Error("a child that finished must read complete, and so must the rollup")
	}
	if rc.Steps <= rc.Own.Steps {
		t.Error("the rollup must be larger than the parent's own spend - that is what it is for")
	}
}

// A FAILED CHILD STILL COSTS. Its receipt is kept and marked incomplete, and the rollup
// says so rather than printing a total that reads as final. Understating a cost is the
// one direction that is dishonest.
func TestFailedChildMakesTheRollupIncomplete(t *testing.T) {
	l := delegateLoop(t, func([]Message) (Message, error) {
		return Message{}, errors.New("station dropped")
	})
	if _, err := l.Send(context.Background(), "go find it", func(Event) {}); err != nil {
		t.Fatalf("the PARENT turn should survive a failed child: %v", err)
	}
	rc := l.TurnReceipt()
	if len(rc.Children) != 1 {
		t.Fatalf("a failed child must still be on the receipt, got %d children", len(rc.Children))
	}
	if rc.Children[0].Complete {
		t.Error("a child that failed must not read complete")
	}
	if rc.Complete {
		t.Error("a rollup containing an unfinished leaf must be incomplete")
	}
	if !strings.Contains(rc.Total(), "incomplete") {
		t.Errorf("the total must SAY it is a lower bound: %q", rc.Total())
	}
	// The failure reaches the model as the tool's error, so it can adapt.
	var res string
	for _, m := range l.messages {
		if m.Role == "tool" && m.Name == "delegate" {
			res = m.Content
		}
	}
	if !strings.Contains(res, "could not finish") {
		t.Errorf("the parent's model must be told the delegation failed, got %q", res)
	}
}

// Receipts describe ONE turn. Carrying them forward would bill a question for the
// previous one's work.
func TestReceiptsResetEachTurn(t *testing.T) {
	l := delegateLoop(t, func([]Message) (Message, error) {
		return Message{Role: "assistant", Content: "ok"}, nil
	})
	_, _ = l.Send(context.Background(), "first", func(Event) {})
	first := l.TurnReceipt()
	_, _ = l.Send(context.Background(), "second", func(Event) {})
	second := l.TurnReceipt()
	if len(second.Children) != len(first.Children) {
		t.Errorf("each turn records its own children: %d then %d", len(first.Children), len(second.Children))
	}
	if second.Steps > first.Steps*2 {
		t.Errorf("steps accumulated across turns: %d then %d", first.Steps, second.Steps)
	}
}

// The rollup is a lower bound made explicit, not a guess.
func TestRollupArithmetic(t *testing.T) {
	r := NewRollup(
		Receipt{Steps: 2, Searches: 1, Complete: true},
		[]Receipt{{Steps: 3, Searches: 2, Complete: true}, {Steps: 1, Fetches: 4, Complete: true}},
	)
	if r.Steps != 6 || r.Searches != 3 || r.Fetches != 4 {
		t.Errorf("rollup = %d steps / %d searches / %d fetches", r.Steps, r.Searches, r.Fetches)
	}
	if !r.Complete || strings.Contains(r.Total(), "incomplete") {
		t.Error("a tree of finished agents is complete")
	}
}
