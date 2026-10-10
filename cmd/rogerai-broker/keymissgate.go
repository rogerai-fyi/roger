package main

import (
	"sync"
	"time"
)

// keyMissGate bounds how many relays from one address may read the key store with a key that does
// not resolve, without ever refusing a key that does. A relay reserves a token from the address's
// key-miss bucket before the store read; a live key gives it back, an unknown one keeps it spent.
// When the bucket is empty but other reads from that address are still in flight, the relay waits
// for them to settle instead of failing: if they were live keys their tokens come back and it
// proceeds; if they were unknown keys, nothing is in flight with an empty bucket and it is refused.
// So unknown-key store reads never exceed the bucket, and a burst of live-key requests is only
// ever delayed by the store's own latency, never refused.
type keyMissGate struct {
	mu      sync.Mutex
	cond    *sync.Cond
	pending map[string]int // reservations in flight per address
}

// keyMissWait caps how long a relay waits for in-flight reads to settle (a stuck store read must
// not hold relays forever); past it the relay is refused like any rate-limited one.
var keyMissWait = 2 * time.Second

func newKeyMissGate() *keyMissGate {
	g := &keyMissGate{pending: map[string]int{}}
	g.cond = sync.NewCond(&g.mu)
	return g
}

// reserve draws one token for addr from rl, waiting while other reservations for addr are in
// flight and the bucket is empty. ok=false carries a Retry-After hint.
func (g *keyMissGate) reserve(rl *rateLimiter, addr string) (ok bool, retry int) {
	deadline := time.Now().Add(keyMissWait)
	g.mu.Lock()
	defer g.mu.Unlock()
	for {
		ok, retry = rl.allow(addr)
		if ok {
			g.pending[addr]++
			return true, 0
		}
		if g.pending[addr] == 0 || !time.Now().Before(deadline) {
			return false, retry
		}
		t := time.AfterFunc(time.Until(deadline), g.cond.Broadcast)
		g.cond.Wait()
		t.Stop()
	}
}

// release ends addr's reservation; live gives the token back (a key that resolved is never charged).
func (g *keyMissGate) release(rl *rateLimiter, addr string, live bool) {
	g.mu.Lock()
	if g.pending[addr]--; g.pending[addr] <= 0 {
		delete(g.pending, addr)
	}
	if live {
		rl.refund(addr)
	}
	g.mu.Unlock()
	g.cond.Broadcast()
}
