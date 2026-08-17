package towerhub

import (
	"context"
	"time"
)

// pollBackoff is how long a node waits after a hard poll/complete failure before retrying, so a
// tower that is down or rejecting is not hammered. A normal empty long-poll is NOT an error and
// carries no backoff.
const pollBackoff = 2 * time.Second

// emptyPollFloor is the minimum time an empty-poll cycle may take, a guard against a fast or
// misbehaving tower returning 204 immediately (which would otherwise busy-spin the worker). A
// well-behaved server long-polls for its whole TTL, so this floor is never reached in practice.
const emptyPollFloor = 200 * time.Millisecond

// Executor serves one authorized job: given the Core-signed grant and the sealed request, it
// returns the sealed result and the node's signed receipt (or a failure string). It is the seam
// that keeps towerhub free of any station/serving dependency - internal/agent adapts the real
// station.Executor to it. A failure returns no receipt: a failure must never settle an attempt.
//
// Serve MUST honor ctx: a cancel (the worker shutting down) should interrupt a long-running
// serve, or that worker cannot be reclaimed.
type Executor interface {
	Serve(ctx context.Context, grant, envelope []byte) (resultEnvelope, receipt []byte, failure string)
}

// ServeLoop is one NODE worker: long-poll the tower for jobs on `station`, serve each via exec,
// and return the sealed result + receipt. It runs until ctx is done, then returns ctx.Err().
//
// SEQUENTIAL by design - one job at a time per worker, mirroring the agent's poll-worker model.
// An operator runs several ServeLoops for concurrency rather than this spawning unbounded
// goroutines (which would let a slow model fan out without limit). Transient poll/complete
// failures are reported via onError (if non-nil) and the worker backs off and continues - a
// tower blip must not take a node offline.
func ServeLoop(ctx context.Context, c *Client, station string, exec Executor, onError func(error)) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		start := time.Now()
		job, ok, err := c.PollJob(ctx, station)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			report(onError, err)
			select {
			case <-time.After(pollBackoff):
			case <-ctx.Done():
				return ctx.Err()
			}
			continue
		}
		if !ok {
			// Empty long poll - poll again, but never faster than emptyPollFloor so a tower that
			// returns 204 immediately cannot busy-spin this worker.
			if d := time.Since(start); d < emptyPollFloor {
				select {
				case <-time.After(emptyPollFloor - d):
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			continue
		}
		env, receipt, failure := exec.Serve(ctx, job.Grant, job.Envelope)
		if failure != "" {
			// A FAILURE NEVER CARRIES A SETTLEABLE RECEIPT, whatever the executor returned. Zero
			// it here so a buggy or hostile executor cannot smuggle a receipt through on a failed
			// serve - the receipt is what settles money, and a failure is not a result.
			receipt = nil
		}
		if cerr := c.CompleteResult(ctx, station, Result{
			AttemptID: job.AttemptID, Envelope: env, Receipt: receipt, Failure: failure,
		}); cerr != nil {
			// The result could not be returned (tower blip / the consumer already gave up). The
			// consumer will time out and the attempt is left unsettled - the safe direction; nothing
			// is charged for a result nobody received. Report and carry on.
			report(onError, cerr)
		}
	}
}

func report(onError func(error), err error) {
	if onError != nil {
		onError(err)
	}
}
