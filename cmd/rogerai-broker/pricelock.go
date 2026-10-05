package main

// pricelock.go - a price lock protects against hikes only (contract §14.12, #24). Within a
// lock billing is min(lock, current), unchanged. A lock minted under a price that had been
// posted for less than ROGERAI_LOCK_MIN_POSTED (default 1 h) is a PROMO lock: it ends as soon
// as that price stops being in effect (or at the lock window, whichever is first), so a
// ten-minute promo cannot be held for 24 hours against the owner.
//
// STATE: the time a (station, model, price) was first posted lives in the shared store
// (set-if-absent, so the first instance to see the price owns the time and every other
// instance reads it), recorded when the registration carrying the price arrives and,
// failing that, when a lock is minted under it. The local map is ONLY the fallback for a
// broker with no shared store configured. An unknown posted-since (an outage) mints the full
// lock - today's consumer-protective rule.

import (
	"strconv"
	"sync"
	"time"

	"rogerai.fm/roger/v6/internal/protocol"
)

func lockMinPosted() time.Duration { return envDuration("ROGERAI_LOCK_MIN_POSTED", time.Hour) }

// postedSinceTTL keeps a posted-since record well past any lock it can shorten.
const postedSinceTTL = 8 * 24 * time.Hour

type postedLocal struct {
	mu sync.Mutex
	m  map[string]time.Time
}

func postedKey(node, model string, in, out float64) string {
	return "posted:" + node + "|" + model + "|" + strconv.FormatFloat(in, 'g', -1, 64) + "|" + strconv.FormatFloat(out, 'g', -1, 64)
}

// notePosted records (if not already known) that the price is posted as of now, and returns
// when it was first posted (ok=false when the shared store cannot say).
func (b *broker) notePosted(node, model string, in, out float64) (time.Time, bool) {
	key, now := postedKey(node, model, in, out), b.now()
	if b.shared != nil {
		_, err := b.shared.setIfAbsent(key, strconv.FormatInt(now.UnixMilli(), 10), postedSinceTTL)
		if err == nil {
			if v, found, err := b.shared.counterGet(key); err == nil && found {
				return time.UnixMilli(int64(v)), true
			}
			return time.Time{}, false
		}
		if err != errNoSharedStore {
			return time.Time{}, false // outage: unknown
		}
	}
	b.postedLocal.mu.Lock()
	defer b.postedLocal.mu.Unlock()
	if b.postedLocal.m == nil {
		b.postedLocal.m = map[string]time.Time{}
	}
	if t, ok := b.postedLocal.m[key]; ok {
		return t, true
	}
	b.postedLocal.m[key] = now
	return now, true
}

// notePostedPrices records every flat-priced offer of a registration (a time-of-use offer is
// never locked, so it needs no record).
func (b *broker) notePostedPrices(reg protocol.NodeRegistration) {
	for _, o := range reg.Offers {
		if len(o.Schedule) == 0 {
			b.notePosted(reg.NodeID, o.Model, o.PriceIn, o.PriceOut)
		}
	}
}

// promoLock reports whether a lock minted now under (in, out) is a promo lock.
func (b *broker) promoLock(node, model string, in, out float64) bool {
	since, ok := b.notePosted(node, model, in, out)
	return ok && b.now().Sub(since) < lockMinPosted()
}
