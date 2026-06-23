package main

import (
	"os"
	"strconv"
	"sync"
	"time"
)

// rateLimiter is a per-key token bucket (keyed by caller identity on the relay) that
// smooths bursts and caps sustained request rate, so one caller cannot flood the
// broker or a provider. In-memory per broker instance - fine for a single node; a
// shared store (Redis) is the multi-instance follow-up. Tunable via ROGERAI_RATE_RPM
// (sustained requests/min) and ROGERAI_RATE_BURST (bucket depth); RPM <= 0 disables.
type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	rpm     float64
	burst   float64
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func loadRateLimiter() *rateLimiter {
	return &rateLimiter{
		buckets: map[string]*tokenBucket{},
		rpm:     envFloat("ROGERAI_RATE_RPM", 120),
		burst:   envFloat("ROGERAI_RATE_BURST", 40),
	}
}

// allow consumes one token for key and reports whether it may proceed. When denied,
// retryAfter is a seconds hint. RPM <= 0 disables limiting (always allow).
func (rl *rateLimiter) allow(key string) (ok bool, retryAfter int) {
	if rl == nil || rl.rpm <= 0 {
		return true, 0
	}
	now := time.Now()
	rl.mu.Lock()
	defer rl.mu.Unlock()
	// Opportunistic prune so the map cannot grow without bound under churn.
	if len(rl.buckets) > 20000 {
		for k, b := range rl.buckets {
			if now.Sub(b.last) > 10*time.Minute {
				delete(rl.buckets, k)
			}
		}
	}
	b := rl.buckets[key]
	if b == nil {
		b = &tokenBucket{tokens: rl.burst, last: now}
		rl.buckets[key] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * (rl.rpm / 60.0)
	b.last = now
	if b.tokens > rl.burst {
		b.tokens = rl.burst
	}
	if b.tokens < 1 {
		retry := int((1 - b.tokens) / (rl.rpm / 60.0))
		if retry < 1 {
			retry = 1
		}
		return false, retry
	}
	b.tokens -= 1
	return true, 0
}
