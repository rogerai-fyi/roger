package main

// sharedstore_retry_test.go pins the RETRY-BEFORE-FALLBACK behavior added to the Valkey bus
// subscribe path. The production symptom (2026-07-18/22): DO App Platform reaches the managed
// Valkey over PUBLIC networking, and the pub/sub RE-SUBSCRIBE re-resolves the public hostname,
// so it intermittently hits DO's slow public DNS -> `dial tcp: lookup ...: i/o timeout`. Before
// this change, a SINGLE such blip dropped that subscription straight to the in-memory fallback.
// retrySubscribe absorbs an isolated transient failure with a bounded retry so the cross-instance
// bus stays live; a genuinely-down bus still falls back after the attempts are exhausted.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

// U - the retry helper's contract, table-driven. fn is a counter that fails a configured number
// of times before succeeding (failUntil<0 = always fail).
func TestRetrySubscribe(t *testing.T) {
	errBoom := errors.New("dial tcp: lookup host: i/o timeout")
	cases := []struct {
		name      string
		attempts  int
		failUntil int // number of leading calls that fail; the rest succeed
		wantCalls int
		wantErr   bool
	}{
		{"success first try", 3, 0, 1, false},
		{"one transient then ok", 3, 1, 2, false},
		{"ok on the last allowed attempt", 3, 2, 3, false},
		{"all attempts fail", 3, -1, 3, true},
		{"attempts=1 disables retry (today's behavior)", 1, -1, 1, true},
		{"attempts=1 still succeeds", 1, 0, 1, false},
		{"attempts<=0 coerced to 1", 0, -1, 1, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			calls := 0
			err := retrySubscribe(context.Background(), c.attempts, time.Millisecond, func() error {
				calls++
				if c.failUntil < 0 || calls <= c.failUntil {
					return errBoom
				}
				return nil
			})
			if calls != c.wantCalls {
				t.Errorf("fn called %d times, want %d", calls, c.wantCalls)
			}
			if (err != nil) != c.wantErr {
				t.Errorf("err=%v, wantErr=%v", err, c.wantErr)
			}
			if c.wantErr && !errors.Is(err, errBoom) {
				t.Errorf("on exhaustion the LAST subscribe error must surface, got %v", err)
			}
		})
	}
}

// U - a cancelled ctx during the backoff wait stops the retry loop PROMPTLY (it must not sleep
// the remaining long backoff) and surfaces the last subscribe error.
func TestRetrySubscribeCtxCancelDuringBackoff(t *testing.T) {
	errBoom := errors.New("i/o timeout")
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()

	start := time.Now()
	calls := 0
	err := retrySubscribe(ctx, 3, 10*time.Second, func() error { calls++; return errBoom }) // huge backoff
	elapsed := time.Since(start)

	if elapsed >= 10*time.Second {
		t.Fatalf("cancel during backoff must return promptly, took %v", elapsed)
	}
	if !errors.Is(err, errBoom) {
		t.Errorf("want the last subscribe error, got %v", err)
	}
	if calls < 1 {
		t.Errorf("fn should have been attempted at least once, got %d", calls)
	}
}

// U - backoff is slept BETWEEN attempts only, never after the final attempt (no wasted trailing
// sleep) and never after a success.
func TestRetrySubscribeNoTrailingSleep(t *testing.T) {
	const backoff = 40 * time.Millisecond // wide enough that scheduler jitter can't brush the bounds
	// all-fail, 3 attempts => exactly 2 inter-attempt sleeps (~40ms), NOT 3 (~60ms).
	start := time.Now()
	_ = retrySubscribe(context.Background(), 3, backoff, func() error { return errors.New("x") })
	elapsed := time.Since(start)
	if elapsed >= 3*backoff {
		t.Errorf("no sleep must follow the final attempt: 3 attempts slept %v (>= 3*backoff=%v)", elapsed, 3*backoff)
	}
	if elapsed < backoff {
		t.Errorf("expected at least one inter-attempt backoff (~2*%v), got %v", backoff, elapsed)
	}
	// success first try => zero sleeps.
	start = time.Now()
	_ = retrySubscribe(context.Background(), 3, backoff, func() error { return nil })
	if el := time.Since(start); el >= backoff {
		t.Errorf("a first-try success must not sleep, took %v", el)
	}
}

// I - integration: on a bus BELIEVED healthy, an unreachable subscribe exhausts all retries and
// returns an error (caller then falls back to in-memory), exercising the closure's failure
// branch, the inter-attempt backoffs, and the error-return path. setUp(true) simulates a live
// bus that just started blipping (newValkeyStore already marked it down via the failed PING).
func TestBusSubscribeUnreachableExhaustsRetriesThenErrors(t *testing.T) {
	vs, _ := newValkeyStore("redis://127.0.0.1:1") // refused; store returned, ping err ignored
	defer vs.Close()
	vs.setUp(true) // believed-healthy => full retry budget

	start := time.Now()
	ch, cancel, err := vs.busSubscribe(context.Background(), "test:chan")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("busSubscribe against an unreachable bus must return an error, not nil")
	}
	if ch != nil {
		t.Error("no channel should be returned on failure")
	}
	if cancel != nil {
		cancel() // the API returns a no-op canceller on failure; calling it must be safe
	}
	if vs.healthy() {
		t.Error("a failed subscribe should have marked the store not-healthy (noteErr)")
	}
	// 3 attempts => 2 inter-attempt backoffs actually happened (proves the retry ran, not a
	// single fail-fast). Connection-refused per-attempt is ~instant, so elapsed ~= 2*backoff.
	if elapsed < busSubscribeBackoff {
		t.Errorf("a healthy-but-blipping bus should retry (>=1 backoff), took only %v", elapsed)
	}
}

// I - a bus ALREADY marked down skips the retry entirely (attempts=1) and fails fast: no
// inter-attempt backoffs, so a sustained outage does not tax every request with the full retry.
func TestBusSubscribeSkipsRetryWhenAlreadyUnhealthy(t *testing.T) {
	vs, _ := newValkeyStore("redis://127.0.0.1:1") // refused; ping already marked it down
	defer vs.Close()
	if vs.healthy() {
		t.Fatal("precondition: an unreachable store should start unhealthy from the failed PING")
	}

	start := time.Now()
	_, _, err := vs.busSubscribe(context.Background(), "test:chan")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("still expected an error against the unreachable bus")
	}
	if elapsed >= busSubscribeBackoff {
		t.Errorf("an already-unhealthy store must fail fast (attempts=1, no backoff), took %v", elapsed)
	}
}

// I - integration: with the retry wrapper in place, busSubscribe still delivers a published
// message end-to-end over a real (miniredis) Valkey - the happy path is unbroken.
func TestBusSubscribeDeliversThroughRetryWrapper(t *testing.T) {
	mr := miniredis.RunT(t)
	vs, err := newValkeyStore("redis://" + mr.Addr())
	if err != nil {
		t.Fatalf("newValkeyStore: %v", err)
	}
	defer vs.Close()

	ctx := context.Background()
	ch, cancel, err := vs.busSubscribe(ctx, "test:chan")
	if err != nil {
		t.Fatalf("busSubscribe: %v", err)
	}
	defer cancel()

	if err := vs.rdb.Publish(ctx, "test:chan", "hello").Err(); err != nil {
		t.Fatalf("publish: %v", err)
	}
	select {
	case msg := <-ch:
		if string(msg) != "hello" {
			t.Errorf("got %q, want %q", msg, "hello")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the published message")
	}
}
