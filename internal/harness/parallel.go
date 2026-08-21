package harness

import (
	"context"
	"sync"
)

// parallel.go - overlapping the SLOW half of a batch of tool calls.
//
// One assistant message often queues several calls: read three files, or search and
// then read. Run serially, the turn waits for the sum of them; run overlapped, it waits
// for the slowest. Nothing about the model changes - this is purely how the harness
// spends the wall-clock time between one model call and the next.
//
// WHAT MAY OVERLAP, AND WHAT MUST NOT. Only the tool BODY overlaps. Everything that
// decides whether a call happens, and everything that records that it did, stays
// strictly ordered:
//
//   DECIDE (serial, in the model's order) - guards, the confirm gate, the retrieval
//     budget, the EventToolCall. Guards read the calls before them, the budget is a
//     running counter, and a confirm is a modal question to a human; every one of those
//     is order-dependent, and racing them would make refusals depend on scheduling.
//   RUN (overlapped) - tool.Run only.
//   SETTLE (serial, in the model's order) - appending the tool result to the
//     conversation and emitting EventToolResult. The transcript must read in the order
//     the model asked, and a strict OpenAI-compatible station expects each tool_call_id
//     answered in order.
//
// So a batch that overlaps produces a byte-identical conversation to the same batch run
// serially. That is the property worth having: parallelism you cannot see in the
// output, only in the clock.
//
// WHAT IS SAFE. Only tools that declare Concurrent, which today means the read-only
// ones. A side-effecting tool is a barrier: it runs alone, after everything queued
// before it has settled, so two writes can never interleave and an approved run_shell
// never overlaps a read of the file it is about to change.
//
// BILLING is unaffected: each relayed call is billed by the broker on its own, and each
// keeps its own receipt. Overlapping changes when requests leave, never what they cost.

// maxParallelTools caps how many bodies overlap at once. Small on purpose: these are a
// laptop's file reads and a handful of HTTP fetches, not a fleet job, and an unbounded
// fan-out from a model that queued twenty calls would be a self-inflicted flood.
const maxParallelTools = 4

// concurrentGroup returns how many calls starting at i may run together: a run of
// consecutive calls whose tools all declare Concurrent, capped at maxParallelTools. A
// group of one is the ordinary serial path, which is what an unknown or exclusive tool
// always gets.
func (l *Loop) concurrentGroup(calls []ToolCall, i int) int {
	n := 0
	for ; i+n < len(calls) && n < maxParallelTools; n++ {
		tool, ok := l.toolByName[calls[i+n].Function.Name]
		if !ok || !tool.Concurrent {
			break
		}
	}
	if n == 0 {
		return 1 // the call at i is exclusive (or unknown): it runs alone
	}
	return n
}

// runBodies runs each planned call's tool body, overlapped, and returns the outputs and
// errors positionally. A plan that was already decided (refused, denied, budget-spent)
// has no body to run and passes straight through.
//
// Each body gets the SAME ctx, so one esc cancels the whole group - a cancelled turn
// must not leave three fetches running.
func (l *Loop) runBodies(ctx context.Context, plans []plannedCall) ([]string, []error) {
	outs := make([]string, len(plans))
	errs := make([]error, len(plans))
	var wg sync.WaitGroup
	for i := range plans {
		p := plans[i]
		if p.settled {
			continue
		}
		wg.Add(1)
		go func(i int, p plannedCall) {
			defer wg.Done()
			// A panicking tool must fail its own call, not take the turn's goroutine with
			// it. Recovered as an error so it settles like any other failure.
			defer func() {
				if r := recover(); r != nil {
					errs[i] = &toolPanic{tool: p.tool.Name, val: r}
				}
			}()
			outs[i], errs[i] = runWithTimeout(ctx, p)
		}(i, p)
	}
	wg.Wait()
	return outs, errs
}

// toolPanic is a recovered tool panic, surfaced to the model as an ordinary tool error
// so one broken tool cannot end a session.
type toolPanic struct {
	tool string
	val  any
}

func (e *toolPanic) Error() string { return e.tool + " panicked and was stopped" }
