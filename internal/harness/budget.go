package harness

import (
	"fmt"
	"sync"
)

// budget.go - THE TURN CEILING, shared down the agent tree.
//
// FOUNDER QUESTION 2026-08-21, on whether a subagent should carry its own receipt:
// "this seems more viable and then we can always aggregate but what might be some cons?"
//
// Own receipts are the right call - attribution is exact, the broker already signs one
// per relayed call, and a rollup over the leaves is a pure sum. But receipts and
// BUDGETS are different axes and must not be split the same way, which is the one con
// sharp enough to change the design:
//
//   The retrieval ceiling is a per-turn counter (3 searches, 8 fetches). If a child
//   owned its own, a parent could spawn four children and spend 12 searches on one
//   question - the ceiling would scale with the number of children, which is to say it
//   would stop being a ceiling. Worse, that is exactly the shape a hostile page would
//   push an agent toward.
//
// So: ATTRIBUTION is per-agent, AUTHORITY is per-turn. Every loop in one turn's tree
// charges the SAME budget, and the child's spend shows up on the child's receipt while
// coming out of the parent's allowance.
//
// Mutex-guarded because subagents may run inside overlapped tool bodies (parallel.go),
// so two children can charge the same budget at once. Without the lock the ceiling
// would be racy - and a ceiling that leaks under concurrency is the same bug as no
// ceiling, just harder to see.

type turnBudget struct {
	mu       sync.Mutex
	searches int
	fetches  int
}

// reset clears the counters for a new turn. Called on the ROOT loop only: a child that
// reset the shared budget would hand its parent a fresh allowance mid-turn, which is
// the leak this file exists to prevent.
func (b *turnBudget) reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.searches, b.fetches = 0, 0
}

// charge takes one retrieval of the given kind, returning "" when the call may proceed
// or the refusal to feed back when the ceiling is reached.
func (b *turnBudget) charge(name string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch name {
	case "web_search":
		if b.searches >= maxSearchesPerTurn {
			return fmt.Sprintf("retrieval budget for this turn is used up (%d searches) - answer with what you already have", maxSearchesPerTurn)
		}
		b.searches++
	case "web_fetch":
		if b.fetches >= maxFetchesPerTurn {
			return fmt.Sprintf("retrieval budget for this turn is used up (%d fetches) - answer with what you already have", maxFetchesPerTurn)
		}
		b.fetches++
	}
	return ""
}

// spent reports the turn's retrieval spend so far, across the whole tree.
func (b *turnBudget) spent() (searches, fetches int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.searches, b.fetches
}
