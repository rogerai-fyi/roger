package towerhub

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The whole point: a consumer's sealed job reaches the polling node and the node's sealed result
// reaches the waiting consumer - the tower carrying opaque bytes, keyed only by Station/attempt.
func TestAJobReachesTheNodeAndTheResultReachesTheConsumer(t *testing.T) {
	h := New()
	h.Register("st-1")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// The node, polling.
	got := make(chan Job, 1)
	go func() {
		job, ok := h.Poll(ctx, "st-1")
		require.True(t, ok)
		got <- job
		h.Complete(job.StationID, Result{AttemptID: job.AttemptID, Envelope: []byte("sealed-answer"), Receipt: []byte("receipt")})
	}()

	res, err := h.Submit(ctx, Job{AttemptID: "att-1", StationID: "st-1", Grant: []byte("g"), Envelope: []byte("sealed-req")})
	require.NoError(t, err)
	require.Equal(t, "att-1", res.AttemptID)
	require.Equal(t, []byte("sealed-answer"), res.Envelope)
	require.Equal(t, []byte("receipt"), res.Receipt)

	job := <-got
	require.Equal(t, "att-1", job.AttemptID)
	require.Equal(t, []byte("sealed-req"), job.Envelope, "the node gets the consumer's sealed request")
}

// A job for a Station no node serves here is refused, not silently queued forever.
func TestSubmitToAnUnservedStationIsRefused(t *testing.T) {
	h := New()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := h.Submit(ctx, Job{AttemptID: "att-1", StationID: "st-nope"})
	require.ErrorIs(t, err, ErrNoStation)
}

// Two submits for the same attempt id must not share a waiter - that would let one node result
// settle two consumer requests. The second is refused.
func TestADuplicateAttemptIsRefused(t *testing.T) {
	h := New()
	h.Register("st-1")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	// First submit parks a waiter (no node polls, so it stays in flight).
	started := make(chan struct{})
	go func() {
		close(started)
		_, _ = h.Submit(ctx, Job{AttemptID: "att-1", StationID: "st-1"})
	}()
	<-started
	// Give the first submit a moment to register its waiter.
	require.Eventually(t, func() bool {
		h.mu.Lock()
		_, ok := h.waiters["att-1"]
		h.mu.Unlock()
		return ok
	}, time.Second, time.Millisecond)

	_, err := h.Submit(ctx, Job{AttemptID: "att-1", StationID: "st-1"})
	require.ErrorIs(t, err, ErrDuplicateAttempt)
}

// A poll on an empty queue returns when its context ends - the ordinary long-poll timeout, after
// which the node just polls again.
func TestPollTimesOutCleanlyOnAnEmptyQueue(t *testing.T) {
	h := New()
	h.Register("st-1")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, ok := h.Poll(ctx, "st-1")
	require.False(t, ok, "an empty queue times out rather than blocking forever")
}

// Polling an unregistered Station returns immediately (ok=false), not a block.
func TestPollAnUnregisteredStationReturnsFalse(t *testing.T) {
	h := New()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, ok := h.Poll(ctx, "st-unknown")
	require.False(t, ok)
}

// A result for an unknown or already-completed attempt is dropped, never a panic - a node
// retrying a return cannot double-deliver.
func TestCompleteForAnUnknownAttemptIsANoOp(t *testing.T) {
	h := New()
	require.NotPanics(t, func() {
		h.Complete("st-any", Result{AttemptID: "nobody-waiting", Envelope: []byte("x")})
	})
}

// If the consumer gives up (ctx cancelled) while waiting, Submit returns the context error and
// the waiter is cleared so a later result is dropped rather than leaking.
func TestSubmitReturnsWhenTheConsumerGivesUp(t *testing.T) {
	h := New()
	h.Register("st-1")
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := h.Submit(ctx, Job{AttemptID: "att-1", StationID: "st-1"})
		done <- err
	}()
	// Let the node pull the job so Submit is parked on the result wait, then cancel.
	pctx, pcancel := context.WithTimeout(context.Background(), time.Second)
	defer pcancel()
	_, ok := h.Poll(pctx, "st-1")
	require.True(t, ok)
	cancel()

	require.ErrorIs(t, <-done, context.Canceled)
	// The waiter is gone; a late Complete is harmlessly dropped.
	require.Eventually(t, func() bool {
		h.mu.Lock()
		_, exists := h.waiters["att-1"]
		h.mu.Unlock()
		return !exists
	}, time.Second, time.Millisecond)
	require.NotPanics(t, func() { h.Complete("st-1", Result{AttemptID: "att-1", Envelope: []byte("late")}) })
}

// Many concurrent attempts across Stations each get their own result - no cross-talk.
func TestConcurrentAttemptsDoNotCrossTalk(t *testing.T) {
	h := New()
	for _, s := range []string{"st-a", "st-b"} {
		h.Register(s)
	}
	// One server goroutine per station echoing the attempt id into the sealed result.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, s := range []string{"st-a", "st-b"} {
		go func(station string) {
			for {
				job, ok := h.Poll(ctx, station)
				if !ok {
					return
				}
				h.Complete(job.StationID, Result{AttemptID: job.AttemptID, Envelope: []byte(job.AttemptID)})
			}
		}(s)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			station := "st-a"
			if i%2 == 1 {
				station = "st-b"
			}
			att := station + "-att-" + time.Duration(i).String()
			res, err := h.Submit(ctx, Job{AttemptID: att, StationID: station})
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			if string(res.Envelope) != att {
				mu.Lock()
				if firstErr == nil {
					firstErr = errors.New("crossed result: got " + string(res.Envelope) + " want " + att)
				}
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	require.NoError(t, firstErr)
}

// Empty attempt or Station ids are refused - an empty key would let unrelated jobs collide.
func TestEmptyIdsAreRefused(t *testing.T) {
	h := New()
	h.Register("st-1")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := h.Submit(ctx, Job{AttemptID: "", StationID: "st-1"})
	require.ErrorIs(t, err, ErrEmptyID)
	_, err = h.Submit(ctx, Job{AttemptID: "att-1", StationID: ""})
	require.ErrorIs(t, err, ErrEmptyID)
}

// The double-settle guard the whole design hinges on: two Completes for one attempt deliver
// EXACTLY once. The second finds no waiter and is dropped - one node result can never settle
// two consumer submits.
func TestTwoCompletesDeliverExactlyOnce(t *testing.T) {
	h := New()
	h.Register("st-1")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan Result, 1)
	go func() {
		res, err := h.Submit(ctx, Job{AttemptID: "att-1", StationID: "st-1"})
		require.NoError(t, err)
		done <- res
	}()
	// Pull the job so the submit is parked on its result wait.
	job, ok := h.Poll(ctx, "st-1")
	require.True(t, ok)

	h.Complete(job.StationID, Result{AttemptID: job.AttemptID, Envelope: []byte("first")})
	require.NotPanics(t, func() { h.Complete(job.StationID, Result{AttemptID: job.AttemptID, Envelope: []byte("second")}) })

	res := <-done
	require.Equal(t, []byte("first"), res.Envelope, "the submit gets the first result, exactly once")
	// A third, late Complete is still a harmless no-op.
	require.NotPanics(t, func() { h.Complete(job.StationID, Result{AttemptID: job.AttemptID, Envelope: []byte("third")}) })
}

// A full Station queue (no node keeping up) makes Submit wait on the context deadline rather
// than blocking forever or growing memory without bound.
func TestAFullQueueRespectsTheContextDeadline(t *testing.T) {
	h := New()
	h.Register("st-1")
	// Park jobQueueDepth submits with no poller: each enqueues one job and waits.
	base, baseCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer baseCancel()
	for i := 0; i < jobQueueDepth; i++ {
		go func(i int) {
			_, _ = h.Submit(base, Job{AttemptID: "fill-" + time.Duration(i).String(), StationID: "st-1"})
		}(i)
	}
	// Wait until the queue is full (all depth jobs enqueued).
	require.Eventually(t, func() bool {
		h.mu.Lock()
		sq := h.stations["st-1"]
		h.mu.Unlock()
		return len(sq.jobs) == jobQueueDepth
	}, 2*time.Second, time.Millisecond)

	// The next submit cannot enqueue and must time out on its own short context.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	_, err := h.Submit(ctx, Job{AttemptID: "overflow", StationID: "st-1"})
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

// A node serving one Station cannot resolve (and thereby deny) an attempt parked for another,
// even if it learns the attempt id: Complete is bound to the completing node's Station.
func TestCompleteFromAWrongStationIsDropped(t *testing.T) {
	h := New()
	h.Register("st-1")
	h.Register("st-2")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan Result, 1)
	go func() {
		res, err := h.Submit(ctx, Job{AttemptID: "att-1", StationID: "st-1"})
		require.NoError(t, err)
		done <- res
	}()
	require.Eventually(t, func() bool {
		h.mu.Lock()
		_, ok := h.waiters["att-1"]
		h.mu.Unlock()
		return ok
	}, time.Second, time.Millisecond)

	// st-2's node tries to steal/deny att-1 (which belongs to st-1): dropped, waiter survives.
	h.Complete("st-2", Result{AttemptID: "att-1", Envelope: []byte("stolen")})
	h.mu.Lock()
	_, stillParked := h.waiters["att-1"]
	h.mu.Unlock()
	require.True(t, stillParked, "a wrong-Station completion must not consume the waiter")

	// The owning Station's completion delivers.
	h.Complete("st-1", Result{AttemptID: "att-1", Envelope: []byte("legit")})
	require.Equal(t, []byte("legit"), (<-done).Envelope)
}

// In-flight accounting returns to zero after submits finish, so the cap does not leak capacity.
func TestInFlightAccountingReturnsToZero(t *testing.T) {
	h := New()
	h.Register("st-1")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		for {
			job, ok := h.Poll(ctx, "st-1")
			if !ok {
				return
			}
			h.Complete(job.StationID, Result{AttemptID: job.AttemptID})
		}
	}()
	for i := 0; i < 200; i++ {
		_, err := h.Submit(ctx, Job{AttemptID: "att-" + time.Duration(i).String(), StationID: "st-1"})
		require.NoError(t, err)
	}
	h.mu.Lock()
	inFlight := h.inFlight
	h.mu.Unlock()
	require.Equal(t, 0, inFlight, "every completed submit released its in-flight slot")
}
