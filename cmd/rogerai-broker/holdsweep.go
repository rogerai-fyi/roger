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

func holdTTL() time.Duration {
	if v := os.Getenv("ROGERAI_HOLD_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
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
