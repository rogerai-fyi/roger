package localplane

import (
	"sync"
	"time"

	"rogerai.fm/roger/v6/internal/protocol"
)

// replayWindow is how long an accepted nonce is remembered. It must cover the WHOLE span a
// signature can be presented in, not just SigMaxSkew from first receipt: VerifyRequestNonce
// accepts a timestamp up to SigMaxSkew in the FUTURE as well as the past, so a request signed at
// ts stays valid until ts+SigMaxSkew, while it could first be received as early as ts-SigMaxSkew
// (a client clock skewed ahead). The maximum gap between first receipt and the signature finally
// going stale is therefore 2*SigMaxSkew - so the nonce is remembered for that long. A shorter
// window would forget the nonce while the signature was still valid, reopening a replay gap on
// exactly the no-NTP drifted-clock plant this targets. The guard reads time only through the
// Tower's clock seam, so the window stays airgap-consistent.
const replayWindow = 2 * protocol.SigMaxSkew

// replayGuard remembers accepted request NONCES for the window and refuses a second use of one.
// Unlike a signature (which is deterministic over key/ts-seconds/method/path/body, so two honest
// same-second duplicates collide), a nonce is random and unique per request - so a genuinely new
// request is never mistaken for a replay, while a verbatim replay reuses the nonce and is caught.
type replayGuard struct {
	mu        sync.Mutex
	seen      map[string]time.Time // nonce key -> expiry (on the Tower's clock)
	ttl       time.Duration
	now       func() time.Time
	lastPrune time.Time
}

func newReplayGuard(now func() time.Time, ttl time.Duration) *replayGuard {
	if now == nil {
		now = time.Now
	}
	return &replayGuard{seen: map[string]time.Time{}, ttl: ttl, now: now, lastPrune: now()}
}

// seen reports whether this nonce key has already been recorded within the window (a replay),
// WITHOUT recording it. Split from record so a caller can check for a replay BEFORE spending a
// rate token, and record only AFTER passing the rate limit - so a replay drains no rate, and a
// throttled request records no nonce (which keeps memory bounded by rate x window per key,
// since only an admitted, rate-limited key ever reaches record).
func (g *replayGuard) isReplay(nonceKey string) bool {
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pruneLocked(now)
	exp, ok := g.seen[nonceKey]
	return ok && now.Before(exp)
}

// record marks a nonce key used for the window. Called only for a request that is proceeding.
func (g *replayGuard) record(nonceKey string) {
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pruneLocked(now)
	g.seen[nonceKey] = now.Add(g.ttl)
}

// used is the ATOMIC check-and-record: it reports whether the nonce was already recorded and,
// if not, records it - all under ONE lock hold, so two goroutines racing with the same nonce
// cannot both see it absent. Exactly one gets false (proceed); every other gets true (replay).
// This is the final gate after the rate limiter; the handlers use isReplay first, before the
// rate gate, as a lock-cheap fast reject of an already-known replay.
func (g *replayGuard) used(nonceKey string) bool {
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pruneLocked(now)
	if exp, ok := g.seen[nonceKey]; ok && now.Before(exp) {
		return true
	}
	g.seen[nonceKey] = now.Add(g.ttl)
	return false
}

// pruneLocked drops expired entries, at most once per ttl so a burst of requests does not each
// pay a full sweep. Caller holds the lock.
func (g *replayGuard) pruneLocked(now time.Time) {
	if now.Sub(g.lastPrune) < g.ttl {
		return
	}
	for k, exp := range g.seen {
		if !now.Before(exp) {
			delete(g.seen, k)
		}
	}
	g.lastPrune = now
}
