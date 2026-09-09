package main

import (
	"context"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

// alertstore.go is the CROSS-INSTANCE half of the founder ops alerts (alerts.go): the onset
// dedup, the flap counter and the stabilized-summary claim live in the shared store, so
// one condition pages ONCE per onset regardless of how many broker instances observe it.
// It follows emailstore.go: a thin layer over the valkeyStore client (type-asserted, so a
// memory/absent shared store simply means "this instance decides alone"), with every
// error routed to the per-process fallback the alerts had before - never a lost page.
//
// LAYOUT (all under ONE namespace no other subsystem writes; pinned by the feature):
//
//	rogerai:alert:<key>          STRING "1"     SETNX + dedupTTL   the onset page is taken
//	rogerai:alert:flap:<key>     STRING <count> PEXPIRE flapWindow onsets inside the window
//	rogerai:alert:stable:<key>   STRING "1"     SETNX + flapQuiet  the summary is taken
const alertKeyPrefix = keyPrefix + "alert:"

// alertFlapScript is a fixed-window counter (INCR, arm the expiry on first use, return the
// count) - the same primitive emailstore.go's allowScript uses for its budgets.
var alertFlapScript = redis.NewScript(`
local n = redis.call('INCR', KEYS[1])
if n == 1 then redis.call('PEXPIRE', KEYS[1], ARGV[1]) end
return n
`)

// alertValkey returns the shared client when the shared store is a live Valkey, else nil.
func (b *broker) alertValkey() *valkeyStore {
	v, ok := b.shared.(*valkeyStore)
	if !ok || v == nil || v.rdb == nil {
		return nil
	}
	return v
}

// alertSharedFail records a shared-store failure: the ops error counter, and ONE log line
// per process saying the alerts are on their per-process fallback.
func (b *broker) alertSharedFail(v *valkeyStore, op string, err error) {
	v.noteErr(op, err)
	b.alertFallbackOnce.Do(func() {
		log.Printf("alert: shared store unreachable - per-process onset dedup fallback (each instance pages once): %v", err)
	})
}

// sharedOnset reports whether THIS instance owns the page for key's current onset: SETNX
// under the dedup TTL. No shared store, or an unreachable one, means yes (fallback). A key
// whose CLEAR could not reach the store (a pending clear) is still claimed by a stale
// onset, so a refused SETNX on it is retried once after the DEL it owes: a failed clear
// must never silence the next real onset for the rest of the TTL.
func (b *broker) sharedOnset(key string) bool {
	v := b.alertValkey()
	if v == nil {
		return true
	}
	set, err := b.setNXOnset(v, key)
	if err == nil && !set && b.clearPending(key) {
		if b.sharedDel(key, "alertOnset") {
			set, err = b.setNXOnset(v, key)
		}
	}
	if err != nil {
		b.alertSharedFail(v, "alertOnset", err)
		return true
	}
	if set {
		b.resolveClear(key)
	}
	return set
}

func (b *broker) setNXOnset(v *valkeyStore, key string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	set, err := v.rdb.SetNX(ctx, alertKeyPrefix+key, "1", b.dedupTTL()).Result()
	if err == nil {
		v.setUp(true)
	}
	return set, err
}

// sharedClear deletes the onset key so the next onset pages again (CLEARED). A failed DEL
// is remembered and retried (retryPendingClears / sharedOnset) instead of leaving the key
// claimed until its TTL.
func (b *broker) sharedClear(key string) {
	if b.alertValkey() == nil || b.sharedDel(key, "alertClear") {
		return
	}
	b.alertMu.Lock()
	if b.alertClearPending == nil {
		b.alertClearPending = map[string]bool{}
	}
	b.alertClearPending[key] = true
	b.alertMu.Unlock()
	log.Printf("alert: shared clear of %q failed - pending retry", key)
}

// clearPending reports whether key owes the store a DEL.
func (b *broker) clearPending(key string) bool {
	b.alertMu.Lock()
	defer b.alertMu.Unlock()
	return b.alertClearPending[key]
}

// resolveClear forgets a pending clear once the key has been deleted or re-claimed.
func (b *broker) resolveClear(key string) {
	b.alertMu.Lock()
	was := b.alertClearPending[key]
	delete(b.alertClearPending, key)
	b.alertMu.Unlock()
	if was {
		log.Printf("alert: pending clear of %q resolved", key)
	}
}

// retryPendingClears re-issues the DEL every pending clear owes (once per checker tick).
func (b *broker) retryPendingClears() {
	b.alertMu.Lock()
	keys := make([]string, 0, len(b.alertClearPending))
	for k := range b.alertClearPending {
		keys = append(keys, k)
	}
	b.alertMu.Unlock()
	for _, k := range keys {
		if b.sharedDel(k, "alertClearRetry") {
			b.resolveClear(k)
		}
	}
}

// sharedDel deletes one key under the alert namespace, reporting success.
func (b *broker) sharedDel(key, op string) bool {
	v := b.alertValkey()
	if v == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	if err := v.rdb.Del(ctx, alertKeyPrefix+key).Err(); err != nil {
		b.alertSharedFail(v, op, err)
		return false
	}
	v.setUp(true)
	return true
}

// sharedFlapIncr counts one onset of key inside the flap window and returns the total so
// far across instances. errNoSharedStore (or a backend error) sends the caller to its
// per-process window.
func (b *broker) sharedFlapIncr(key string, window time.Duration) (int, error) {
	v := b.alertValkey()
	if v == nil {
		return 0, errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	n, err := alertFlapScript.Run(ctx, v.rdb, []string{alertKeyPrefix + "flap:" + key}, window.Milliseconds()).Int()
	if err != nil {
		b.alertSharedFail(v, "alertFlap", err)
		return 0, err
	}
	v.setUp(true)
	return n, nil
}

// sharedFlapReset forgets key's flap count (the mute was lifted).
func (b *broker) sharedFlapReset(key string) { b.sharedDel("flap:"+key, "alertFlapReset") }

// sharedStableOnce reports whether THIS instance sends the "stabilized" summary for key:
// SETNX for the quiet window, so two instances that both watched it go quiet send one.
func (b *broker) sharedStableOnce(key string, quiet time.Duration) bool {
	v := b.alertValkey()
	if v == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	set, err := v.rdb.SetNX(ctx, alertKeyPrefix+"stable:"+key, "1", quiet).Result()
	if err != nil {
		b.alertSharedFail(v, "alertStable", err)
		return true
	}
	v.setUp(true)
	return set
}
