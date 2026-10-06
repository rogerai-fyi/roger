package main

// ratelimit_blocked_test.go: blocked reads a local bucket without drawing from it.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRateLimiterBlocked(t *testing.T) {
	var none *rateLimiter
	b, _ := none.blocked("ip")
	require.False(t, b, "a nil limiter blocks nothing")
	b, _ = (&rateLimiter{buckets: map[string]*tokenBucket{}}).blocked("ip")
	require.False(t, b, "rpm 0 is no limit")

	rl := &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 60, burst: 2}
	b, _ = rl.blocked("ip")
	require.False(t, b, "an address with no bucket yet is not blocked")
	rl.allow("ip")
	rl.allow("ip")
	b, retry := rl.blocked("ip")
	require.True(t, b, "an empty bucket blocks")
	require.GreaterOrEqual(t, retry, 1)
	b, _ = rl.blocked("ip")
	require.True(t, b, "blocked draws nothing, so it stays blocked")
	rl.mu.Lock()
	rl.buckets["ip"].last = rl.buckets["ip"].last.Add(-2 * time.Second)
	rl.mu.Unlock()
	b, _ = rl.blocked("ip")
	require.False(t, b, "a refilled bucket no longer blocks")
}
