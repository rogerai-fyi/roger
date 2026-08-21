package harness

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
)

// subagent.go - DELEGATION: a child agent that answers one narrow question and reports
// back, so the parent's context is spent on the answer rather than on the raw material
// used to reach it.
//
// FOUNDER 2026-08-21: subagents carry their OWN receipt, aggregated afterwards. That is
// the right call - attribution is exact, the broker already signs a receipt per relayed
// call, and a rollup over the leaves is a pure sum rather than an invented number. The
// cons worth naming, and what each one cost:
//
//   1. THE CEILING. A child owning its own retrieval budget would let a parent spawn
//      four children and spend 4x the allowance on one question. Fixed by splitting the
//      axes: attribution per agent, AUTHORITY per turn (budget.go). The child charges
//      the parent's budget and reports its own spend.
//   2. A PARTIAL TREE UNDERSTATES. Sum a tree with a cancelled or crashed child and the
//      total is quietly too low - and understating a cost is the one direction that is
//      dishonest. Every Receipt carries Complete, and a rollup that includes an
//      incomplete leaf is itself incomplete (see Rollup).
//   3. TWO NUMBERS. "What did this turn cost" stops being a field and becomes a query,
//      and a UI showing the parent's OWN spend as the turn's cost would understate it.
//      Rollup is the only thing that should ever be shown as a turn total.
//   4. RUNAWAY DEPTH. A child that can delegate can build a tree nobody authorized.
//      Depth is capped at one: a subagent has no delegate tool.
//
// WHAT A SUBAGENT MAY DO. Read-only tools only. It cannot write files, cannot run
// shell, and therefore never needs the confirm gate - which matters because the confirm
// is a modal question to a human, and a child running inside an overlapped tool body
// has no sane way to ask one. A delegated task that needs to change something is the
// parent's job, with the parent's confirm.

// maxSubagentSteps bounds a child's tool loop. Deliberately tighter than the parent's:
// a subagent exists to answer ONE narrow question, and a child that needs a dozen steps
// is a sign the task should have been split by the parent instead.
const maxSubagentSteps = 5

// Receipt is one agent's spend on one turn. Leaves are subagents; the root is the
// operator's own turn.
type Receipt struct {
	Agent    string // "" for the operator's own turn, otherwise the subagent's task label
	Steps    int    // model calls this agent made
	Searches int    // retrievals charged, for attribution (the ceiling itself is shared)
	Fetches  int
	Complete bool // false when the agent was cancelled or failed before finishing
}

// Rollup totals a tree of receipts. Complete is AND-ed, never assumed: a sum over a
// tree with an unfinished leaf is a lower bound, and saying so is the difference
// between a receipt and a guess.
type Rollup struct {
	Own      Receipt
	Children []Receipt
	Steps    int
	Searches int
	Fetches  int
	Complete bool
}

func NewRollup(own Receipt, children []Receipt) Rollup {
	r := Rollup{Own: own, Children: children,
		Steps: own.Steps, Searches: own.Searches, Fetches: own.Fetches, Complete: own.Complete}
	for _, c := range children {
		r.Steps += c.Steps
		r.Searches += c.Searches
		r.Fetches += c.Fetches
		r.Complete = r.Complete && c.Complete
	}
	return r
}

// Total renders the rollup for display. An incomplete tree says so, rather than
// printing a number that reads as final.
func (r Rollup) Total() string {
	s := fmt.Sprintf("%d steps · %d searches · %d fetches", r.Steps, r.Searches, r.Fetches)
	if !r.Complete {
		s += " (incomplete - a delegated task did not finish)"
	}
	return s
}

// subagentPersona is the child's whole brief. Short on purpose: a subagent that
// inherits the DJ persona would inherit its voice, its radio color and its sense that
// it is talking to a person, none of which apply to something reporting to another
// program.
const subagentPersona = `You are a research subagent inside the RogerAI agent. You have
been given ONE narrow task by the main agent. Do it and report back.

- You are talking to a PROGRAM, not a person. No greeting, no sign-off, no radio voice.
- Use the read-only tools to find real information. Do not guess.
- Report the ANSWER and the facts behind it, compactly. The main agent cannot see your
  tool output - only what you write - so include what it needs and nothing else.
- If you cannot find it, say exactly that and what you tried. A wrong answer is worse
  than a missing one.
- You cannot delegate further and you cannot change anything. Read, then report.`

// subagentCounter labels children within a turn so two concurrent ones are tellable
// apart in the transcript.
var subagentCounter atomic.Int64

// delegateTool builds the parent's `delegate` tool. It is Concurrent: two delegated
// questions are independent by construction (a child is read-only and shares nothing
// but the budget, which is mutex-guarded), so a parent that asks two things at once
// waits for the slower rather than the sum.
func (l *Loop) delegateTool() Tool {
	return Tool{
		Name: "delegate",
		Description: "Hand ONE narrow research question to a subagent that can read files, " +
			"list directories, search and fetch, and have it report back a compact answer. " +
			"Use it when finding something would fill your context with raw material you do " +
			"not need to keep - the subagent reads, you get the answer. It cannot write, run " +
			"commands, or delegate further.",
		Mutating:   false,
		Concurrent: true,
		Params: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task": map[string]any{
					"type": "string",
					"description": "The single question to answer, stated completely - the " +
						"subagent cannot see your conversation.",
				},
			},
			"required": []string{"task"},
		},
		Run: func(ctx context.Context, root string, args map[string]any) (string, error) {
			task := strings.TrimSpace(argString(args, "task"))
			if task == "" {
				return "", fmt.Errorf("delegate needs a task to hand over")
			}
			child := l.newSubagent(root)
			label := fmt.Sprintf("subagent %d", subagentCounter.Add(1))

			out, err := child.Send(ctx, task, func(Event) {})
			// The receipt is recorded either way. A child that failed still spent the
			// budget it spent, and a rollup that quietly dropped it would understate.
			searches, fetches := child.budget.spent()
			rec := Receipt{Agent: label, Steps: child.steps, Complete: err == nil}
			rec.Searches, rec.Fetches = searches, fetches
			l.childReceipts = append(l.childReceipts, rec)
			if err != nil {
				return "", fmt.Errorf("%s could not finish: %w", label, err)
			}
			if strings.TrimSpace(out) == "" {
				return "", fmt.Errorf("%s returned nothing", label)
			}
			return out, nil
		},
	}
}

// newSubagent builds the child: the parent's model and root, a read-only toolset, the
// parent's guards, and - the load-bearing part - the parent's BUDGET.
func (l *Loop) newSubagent(root string) *Loop {
	var tools []Tool
	for _, t := range l.tools {
		// Read-only only, and never the delegate tool itself: depth is capped at one.
		if t.Mutating || t.Name == "delegate" {
			continue
		}
		tools = append(tools, t)
	}
	byName := make(map[string]Tool, len(tools))
	for _, t := range tools {
		byName[t.Name] = t
	}
	c := &Loop{
		Root:       root,
		Persona:    subagentPersona,
		tools:      tools,
		toolByName: byName,
		complete:   l.complete,
		// No confirm: a read-only child never reaches the gate, and a modal question
		// from inside an overlapped tool body has nobody to ask.
		confirm:       nil,
		MaxSteps:      maxSubagentSteps,
		MaxToolOutput: l.MaxToolOutput,
		Guards:        l.Guards,
		budget:        l.budget, // SHARED: the ceiling is the turn's, not the child's
	}
	c.messages = append(c.messages, Message{Role: "system", Content: subagentPersona})
	return c
}
