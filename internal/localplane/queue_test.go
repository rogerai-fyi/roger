package localplane

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestQueueDeliversAJobToAPollingStation(t *testing.T) {
	q := newQueue()
	j := q.submit("req-1", "llama-8b", []byte(`{"prompt":"hi"}`))

	// A station polling for a matching model gets the job.
	got, ok := q.poll(context.Background(), "st-1", []string{"llama-8b", "qwen"})
	require.True(t, ok)
	require.Equal(t, "req-1", got.id)
	require.Equal(t, []byte(`{"prompt":"hi"}`), got.body)

	// It completes back to the waiting consumer.
	require.True(t, q.complete("req-1", "st-1", []byte(`{"answer":"hello"}`)))
	select {
	case res := <-j.result:
		require.Equal(t, "st-1", res.stationID)
		require.Equal(t, []byte(`{"answer":"hello"}`), res.answer)
	case <-time.After(time.Second):
		t.Fatal("consumer never received the answer")
	}
}

func TestPollIgnoresJobsForOtherModels(t *testing.T) {
	q := newQueue()
	q.submit("req-x", "mistral", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, ok := q.poll(ctx, "st-test", []string{"llama-8b"})
	require.False(t, ok, "a station must not be handed a model it does not serve")
}

func TestPollBlocksUntilAJobArrives(t *testing.T) {
	q := newQueue()
	done := make(chan *job, 1)
	go func() {
		j, ok := q.poll(context.Background(), "st-test", []string{"llama-8b"})
		if ok {
			done <- j
		}
	}()
	// Nothing yet; submit after a beat and the poller wakes.
	time.Sleep(50 * time.Millisecond)
	q.submit("late", "llama-8b", nil)
	select {
	case j := <-done:
		require.Equal(t, "late", j.id)
	case <-time.After(time.Second):
		t.Fatal("a blocked poll never woke on submit")
	}
}

func TestPollReturnsOnContextCancel(t *testing.T) {
	q := newQueue()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, ok := q.poll(ctx, "st-test", []string{"llama-8b"})
	require.False(t, ok)
}

func TestExactlyOneStationTakesAJob(t *testing.T) {
	q := newQueue()
	q.submit("only", "m", nil)
	var wg sync.WaitGroup
	var mu sync.Mutex
	got := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			if _, ok := q.poll(ctx, "st-test", []string{"m"}); ok {
				mu.Lock()
				got++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	require.Equal(t, 1, got, "a job is handed to exactly one station")
}

func TestCompleteForUnknownJobIsANoOp(t *testing.T) {
	q := newQueue()
	require.False(t, q.complete("never-existed", "st", []byte("x")), "a completion for an unknown id changes nothing")
}

func TestAbandonedJobCannotBeCompleted(t *testing.T) {
	q := newQueue()
	q.submit("gone", "m", nil)
	_, ok := q.poll(context.Background(), "st", []string{"m"})
	require.True(t, ok)
	q.abandon("gone")
	require.False(t, q.complete("gone", "st", []byte("x")), "an abandoned job's late completion is dropped")
}

func TestOnlyTheTakingStationMayComplete(t *testing.T) {
	q := newQueue()
	q.submit("j1", "m", nil)
	_, ok := q.poll(context.Background(), "st-owner", []string{"m"})
	require.True(t, ok)
	// A different station guessing the id cannot complete it.
	require.False(t, q.complete("j1", "st-impostor", []byte("x")), "only the station that took the job may complete it")
	// The real owner can.
	require.True(t, q.complete("j1", "st-owner", []byte("ok")))
}

// A job abandoned before any station took it must leave the PENDING set, or it leaks there
// forever and a later poll would hand a station stale work the consumer already gave up on.
func TestAbandonRemovesANeverTakenJobFromPending(t *testing.T) {
	q := newQueue()
	q.submit("timed-out", "m", make([]byte, 1024))
	q.abandon("timed-out") // consumer gave up before any station polled

	q.mu.Lock()
	pending := len(q.pending)
	q.mu.Unlock()
	require.Zero(t, pending, "an abandoned never-taken job must not linger in pending")

	// A station polling now finds nothing - the stale job is gone, not served.
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	_, ok := q.poll(ctx, "st", []string{"m"})
	require.False(t, ok, "an abandoned job is never handed to a station")
}
