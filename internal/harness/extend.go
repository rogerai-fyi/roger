package harness

import (
	"context"
	"sync"
	"time"
)

// extendableCtx is a cancellable context whose deadline can be pushed back while the
// context is live. Deadline() reports the CURRENT deadline so downstream code that
// checks "does this ctx already carry a deadline?" (BrokerCompleter's default-timeout
// fallback) sees one and stays out of the way.
type extendableCtx struct {
	context.Context
	mu       *sync.Mutex
	deadline *time.Time
}

func (c *extendableCtx) Deadline() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return *c.deadline, true
}

// ExtendableTimeout is context.WithTimeout whose deadline can be extended while the
// work is in flight. It exists for the agent's per-call cap: PerCallCap is surfaced
// as a SOFT ceiling in the TUI, and the user can grant a legitimately slow call more
// time (a big prompt on a spill-bound MoE station) instead of it being hard-killed at
// a fixed deadline. Semantics:
//
//   - The context is cancelled with cause context.DeadlineExceeded once the deadline
//     passes, so the caller's error surface reads as a timeout (not a user abort).
//   - extend(d) pushes the CURRENT deadline back by d (not "d from now"), so repeated
//     grants stack predictably.
//   - cancel MUST be called on every exit path, like context.WithTimeout's CancelFunc;
//     it cancels with context.Canceled (a user abort / normal completion).
func ExtendableTimeout(parent context.Context, d time.Duration) (ctx context.Context, extend func(time.Duration), cancel context.CancelFunc) {
	inner, cause := context.WithCancelCause(parent)
	mu := &sync.Mutex{}
	deadline := time.Now().Add(d)
	ec := &extendableCtx{Context: inner, mu: mu, deadline: &deadline}
	var timer *time.Timer
	timer = time.AfterFunc(d, func() {
		// An extend may have raced the timer: only expire when the deadline truly
		// passed, otherwise re-arm for the remainder.
		mu.Lock()
		rem := time.Until(deadline)
		mu.Unlock()
		if rem > 0 {
			timer.Reset(rem)
			return
		}
		cause(context.DeadlineExceeded)
	})
	extend = func(delta time.Duration) {
		mu.Lock()
		deadline = deadline.Add(delta)
		rem := time.Until(deadline)
		mu.Unlock()
		if rem > 0 {
			timer.Reset(rem)
		}
	}
	cancel = func() {
		timer.Stop()
		cause(context.Canceled)
	}
	return ec, extend, cancel
}
