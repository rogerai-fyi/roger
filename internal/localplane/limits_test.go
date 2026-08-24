package localplane

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRateLimiterRefusesAFloodThenRefills(t *testing.T) {
	now := time.Now()
	clock := func() time.Time { return now }
	rl := newRateLimiter(clock, 5, 20) // 5/s sustained, burst 20

	// The initial burst is allowed, then the next is refused (bucket empty, no time passed).
	for i := 0; i < 20; i++ {
		require.True(t, rl.allow("alice"), "burst request %d should pass", i)
	}
	require.False(t, rl.allow("alice"), "past the burst with no refill, alice is throttled")

	// Another client is unaffected: budgets are per-client.
	require.True(t, rl.allow("bob"))

	// After a second, ~5 tokens refill.
	now = now.Add(time.Second)
	allowed := 0
	for i := 0; i < 10; i++ {
		if rl.allow("alice") {
			allowed++
		}
	}
	require.Equal(t, 5, allowed, "about rate tokens refill per second")
}

func TestSemaphoreBoundsConcurrency(t *testing.T) {
	s := newSemaphore(2)
	require.True(t, s.tryAcquire())
	require.True(t, s.tryAcquire())
	require.False(t, s.tryAcquire(), "a third concurrent acquire is refused, not queued")
	s.release()
	require.True(t, s.tryAcquire(), "a released slot can be re-acquired")
	// Releasing an empty semaphore is harmless.
	s.release()
	s.release()
	s.release()
}

func TestClientInflightBoundsPerClient(t *testing.T) {
	c := newClientInflight(2)
	require.True(t, c.acquire("alice"))
	require.True(t, c.acquire("alice"))
	require.False(t, c.acquire("alice"), "alice may not exceed her per-client cap")
	require.True(t, c.acquire("bob"), "bob has his own budget")
	c.release("alice")
	require.True(t, c.acquire("alice"), "a released slot frees the client")
	// Releasing to zero prunes the entry.
	c.release("alice")
	c.release("alice")
	c.mu.Lock()
	_, present := c.count["alice"]
	c.mu.Unlock()
	require.False(t, present, "a client at zero in-flight is pruned from the map")
}
