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

// envStr returns the env var or def when unset/empty.
func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
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

// loadAnonRateLimiter builds the per-IP limiter for the UNAUTHENTICATED public
// surfaces (the free/anon relay + /discover), keyed on the validated CF-Connecting-IP.
// It is intentionally TIGHTER than the per-identity relay limiter: an anonymous source
// IP gets a smaller sustained rate + burst than a signed wallet, since the anon surface
// is the abuse-prone one and a logged-in caller has its own per-identity bucket.
// Tunable via ROGERAI_ANON_RATE_RPM / ROGERAI_ANON_RATE_BURST; RPM <= 0 disables it.
func loadAnonRateLimiter() *rateLimiter {
	return &rateLimiter{
		buckets: map[string]*tokenBucket{},
		rpm:     envFloat("ROGERAI_ANON_RATE_RPM", 30),
		burst:   envFloat("ROGERAI_ANON_RATE_BURST", 15),
	}
}

// allow consumes one token for key and reports whether it may proceed. When denied,
// retryAfter is a seconds hint. RPM <= 0 disables limiting (always allow).
func (rl *rateLimiter) allow(key string) (ok bool, retryAfter int) {
	return rl.allowAt(key, 0, 0)
}

// allowAt is allow with a per-key rate override (rpm/burst). A zero rpm or burst
// falls back to the limiter's configured default - this is what lets a grant carry
// its own caps while sharing one limiter instance keyed by grant id. rpmOverride
// <= 0 AND the limiter default <= 0 means "no limit" (always allow).
func (rl *rateLimiter) allowAt(key string, rpmOverride, burstOverride float64) (ok bool, retryAfter int) {
	if rl == nil {
		return true, 0
	}
	rpm, burst := rl.rpm, rl.burst
	if rpmOverride > 0 {
		rpm = rpmOverride
	}
	if burstOverride > 0 {
		burst = burstOverride
	}
	if rpm <= 0 {
		return true, 0
	}
	if burst <= 0 {
		burst = rpm // a sane default depth when none is set
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
		b = &tokenBucket{tokens: burst, last: now}
		rl.buckets[key] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * (rpm / 60.0)
	b.last = now
	if b.tokens > burst {
		b.tokens = burst
	}
	if b.tokens < 1 {
		retry := int((1 - b.tokens) / (rpm / 60.0))
		if retry < 1 {
			retry = 1
		}
		return false, retry
	}
	b.tokens -= 1
	return true, 0
}
