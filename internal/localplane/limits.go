package localplane

import (
	"sync"
	"time"
)

// Resource safety for the consumer plane: no single client may flood the Tower with rapid
// requests, and the number of requests in flight at once is bounded, so one abusive client
// cannot starve the stations for the others. Body size is capped separately, at read time.

// Default limits. A local plant is not a public endpoint, so these are generous - enough for
// an operator and a few agents working normally, tight enough that a runaway loop is refused
// rather than allowed to exhaust the box.
const (
	defaultPerClientRate  = 5.0 // sustained requests per second per admitted client
	defaultPerClientBurst = 20.0
	defaultMaxInFlight    = 64 // concurrent completions across all clients
)

// rateLimiter is a per-client token bucket. Each admitted client refills at `rate` tokens a
// second up to `burst`; a request costs one token, and a client with none is refused until it
// refills. Keyed by client key hash, so one client's flood never spends another's budget.
type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	rate    float64
	burst   float64
	now     func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(now func() time.Time, rate, burst float64) *rateLimiter {
	if now == nil {
		now = time.Now
	}
	return &rateLimiter{buckets: map[string]*bucket{}, rate: rate, burst: burst, now: now}
}

// allow spends one token for a client, refilling by elapsed time first, and reports whether the
// request may proceed.
func (r *rateLimiter) allow(client string) bool {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.buckets[client]
	if !ok {
		// A new client starts with a full burst, then a request spends one.
		r.buckets[client] = &bucket{tokens: r.burst - 1, last: now}
		return true
	}
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * r.rate
		if b.tokens > r.burst {
			b.tokens = r.burst
		}
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// semaphore bounds how many completions run at once. tryAcquire never blocks: a request that
// cannot get a slot is refused (503) rather than queued behind an unbounded backlog.
type semaphore chan struct{}

func newSemaphore(n int) semaphore { return make(semaphore, n) }

func (s semaphore) tryAcquire() bool {
	select {
	case s <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s semaphore) release() {
	select {
	case <-s:
	default:
	}
}

// defaultMaxInFlightPerClient bounds how many completions ONE client may hold at once. It is
// well below the global cap, so a single admitted client cannot accumulate every slot (each
// held for up to the completion timeout) and starve the stations for the others - the fairness
// the global semaphore alone does not provide.
const defaultMaxInFlightPerClient = 8

// clientInflight counts in-flight completions per client key and refuses a client that is
// already holding its share. It is the per-key half of resource fairness; the global semaphore
// is the whole-Tower half.
type clientInflight struct {
	mu    sync.Mutex
	count map[string]int
	max   int
}

func newClientInflight(max int) *clientInflight {
	return &clientInflight{count: map[string]int{}, max: max}
}

// acquire reserves a slot for a client, or reports false if the client already holds its max.
func (c *clientInflight) acquire(client string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.count[client] >= c.max {
		return false
	}
	c.count[client]++
	return true
}

// release returns a client's slot. A client that drops to zero is removed so the map does not
// grow with every client ever seen.
func (c *clientInflight) release(client string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.count[client] <= 1 {
		delete(c.count, client)
		return
	}
	c.count[client]--
}

// Station-side rate. A station LONG-POLLS - it blocks up to the poll timeout, then re-polls -
// so its legitimate request rate is low; these bounds are far above that and exist only to cap
// a flood (a station whose key was compromised, replaying or hammering) so the replay guard's
// nonce set stays bounded by rate x window rather than by an attacker's willingness to send.
const (
	defaultStationRate  = 20.0
	defaultStationBurst = 40.0
)
