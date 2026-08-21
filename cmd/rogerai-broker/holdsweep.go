package main

import (
	"log"
	"os"
	"time"
)

// defaultHoldTTL bounds how long a relay pre-auth hold may live before the backstop sweep
// reclaims it. It must exceed the longest LEGITIMATE relay (a 300s stream) with margin so a
// live relay is never reclaimed mid-flight; 10 minutes is ~2x the longest stream. The
// graceful drain handles an orderly redeploy; this sweep is the backstop for a hard SIGKILL.
// Override with ROGERAI_HOLD_TTL (a Go duration, e.g. "10m"); <=0 disables the sweep.
const defaultHoldTTL = 10 * time.Minute

// minHoldTTL is the floor under a CONFIGURED hold TTL, and it is derived rather than chosen.
//
// The edge settlement window is an attempt's lifetime plus edgeSettleGrace(), and the whole
// design of that grace is that the window stays strictly inside holdTTL: a receipt that arrives
// late but valid must not find the consumer's hold already swept, because then the work
// settles for free and the operator is unpaid. edgeSettleGrace() derives itself from holdTTL to
// keep that true - but it also has a one-minute floor, and below a certain holdTTL the floor
// wins and the relationship inverts. At ROGERAI_HOLD_TTL=2m the grace clamps to 1m, the window
// is 2m, and the hold no longer outlives it. The invariant was asserted in a test across the
// "realistic" range and was simply false outside it.
//
// So the floor is the two terms it has to clear plus a minute of margin, written as the sum
// rather than as a number so that changing either term moves it, and the result is asserted at
// production values - including the ones this clamps - by the settle-window test in
// toweredge_billing_test.go.
func minHoldTTL() time.Duration { return towerAttemptLifetime + minEdgeSettleGrace + time.Minute }

// holdTTL is the configured hold lifetime, floored so the settlement window it bounds cannot
// outrun it. A value of zero or less is left alone: that DISABLES the sweep, and a hold that is
// never reclaimed cannot be reclaimed too early.
func holdTTL() time.Duration {
	if v := os.Getenv("ROGERAI_HOLD_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			if d > 0 && d < minHoldTTL() {
				return minHoldTTL()
			}
			return d
		}
	}
	return defaultHoldTTL
}

// holdSweepInterval picks the sweep cadence: half the TTL (so an orphan is reclaimed within
// ~1.5 TTLs), capped at 1h so a long TTL still sweeps regularly, and never <=0.
func holdSweepInterval(ttl time.Duration) time.Duration {
	iv := ttl / 2
	if iv <= 0 {
		iv = time.Second
	}
	if iv > time.Hour {
		iv = time.Hour
	}
	return iv
}

// releaseStaleHoldsSweep is the deploy-orphan backstop (modeled on recountHoldSweep /
// nodeBanSweep): on a ticker it reclaims any relay pre-auth hold older than holdTTL - a hold
// stranded because the relay's deferred release never ran when DO SIGKILLed the instance
// mid-redeploy. The store op is atomic + single-actor, so both instances may run it safely.
// stop is the nil-in-production test seam (a nil channel case never fires).
func (b *broker) releaseStaleHoldsSweep(stop <-chan struct{}) {
	if b.holdTTL <= 0 {
		log.Printf("hold-backstop: stale-hold sweep DISABLED (ROGERAI_HOLD_TTL<=0) - a SIGKILLed relay's hold clears only via the graceful drain")
		return
	}
	if b.db == nil {
		return
	}
	interval := holdSweepInterval(b.holdTTL)
	log.Printf("hold-backstop: reclaiming relay pre-auth holds older than %s (sweep every %s) so a SIGKILLed relay never strands a consumer hold", b.holdTTL, interval)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			b.releaseStaleHoldsSweepOnce(time.Now().Add(-b.holdTTL))
		}
	}
}

// releaseStaleHoldsSweepOnce reclaims every tracked hold placed at or before cutoff (one
// sweep iteration). Split out of the loop so the reclaim work is testable without the ticker.
func (b *broker) releaseStaleHoldsSweepOnce(cutoff time.Time) {
	if n, err := b.db.ReleaseStaleHolds(cutoff); err != nil {
		log.Printf("hold-backstop: stale-hold sweep failed: %v", err)
	} else if n > 0 {
		log.Printf("hold-backstop: reclaimed %d stale relay hold(s) older than %s (relay killed mid-flight) - consumer credits restored in full", n, b.holdTTL)
	}
}
