package main

import (
	"context"
	"encoding/json"
	"log"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// PRE-SCALE Stage 1: a flag-gated shared-state layer so multiple broker instances
// can share the SAFE (non-money-critical) parts of broker state. It is OFF by
// default: when ROGERAI_REDIS_URL is unset, sharedStore is nil everywhere and the
// broker behaves byte-for-byte as it does today (the in-memory maps + the existing
// token-bucket rateLimiter). When the flag is set, the SAFE state is mirrored to a
// Redis-protocol store (DigitalOcean Valkey):
//
//  1. The per-IP / anon / concierge rate-limit buckets - so a limit is enforced
//     ACROSS instances, not per-instance (see rateLimiter.allowAt).
//  2. Node-registry LIVENESS (last_seen / heartbeat timestamps) - so any instance
//     sees any node's freshness (see broker.markSeen + the liveness sync loop).
//
// DEFERRED to Stage 2 (money / correctness critical, left fully in-memory here):
//   - the credit Hold/Finalize accounting,
//   - the job/result/stream long-poll RENDEZVOUS (the tunnels/streams channels),
//   - inflight concurrency counters (in-memory only; reset-on-restart is acceptable
//     and they are read on the hot pick path under metricsMu).
//
// SHARED-INSTANCE NOTE: the Valkey instance (db-halo-cache) is SHARED across other
// DO projects, so EVERY key this layer writes MUST carry the keyPrefix below. Never
// issue an un-prefixed command (no FLUSHDB, no un-prefixed SCAN), or we collide with
// another project's data.
const keyPrefix = "rogerai:"

// sharedStore is the swappable shared-state abstraction. There are two impls:
//   - memStore: the default; a thin no-op that signals "use the in-memory path".
//   - valkeyStore: backs the SAFE state with a Redis-protocol server (Valkey).
//
// Every method is total and SAFE to call: an impl that cannot serve a request
// returns an error, and EVERY call site is required to fall back to the in-memory
// path on a non-nil error. A connection failure NEVER propagates as a broker error.
type sharedStore interface {
	// rateAllow is the shared token-bucket: it consumes one token for key under the
	// given rpm/burst and reports whether the caller may proceed (mirrors
	// rateLimiter.allowAt semantics). retryAfter is a seconds hint when denied. A
	// non-nil err means the backend was unreachable - the caller MUST fall back to
	// the local in-memory bucket and treat the shared decision as unavailable.
	rateAllow(key string, rpm, burst float64, now time.Time) (ok bool, retryAfter int, err error)

	// markSeen records a node's liveness timestamp so peer instances can observe it.
	markSeen(node string, now time.Time) error

	// liveness returns the last_seen timestamp this layer knows for every node it has
	// seen (across instances). The broker merges the FRESHER of {local, shared} into
	// its in-memory lastSeen map on a background loop, so the hot read path stays
	// purely in-memory. A non-nil err means the snapshot is unavailable this round.
	liveness() (map[string]time.Time, error)

	// cacheGet returns the cached bytes for key (found == true) or a miss
	// (found == false). It is a READ-ONLY accelerator for the hot, expensive read
	// paths (/discover + /market, /metrics/series + /console): NEVER a money/mutating
	// path. A non-nil err means the backend was unreachable - the caller MUST treat it
	// as a miss and recompute directly (a cache failure never fails a request). key is
	// the caller's logical key; impls prepend the rogerai:cache: namespace.
	cacheGet(key string) (val []byte, found bool, err error)

	// cacheSet stores val for key with the given TTL (a short window: 2-3s for the
	// public market views, 10-30s for the per-identity feeds). A non-nil err is
	// non-fatal - the caller already served the freshly computed value, so a failed
	// SET only means the next request recomputes. ttl<=0 is a no-op.
	cacheSet(key string, val []byte, ttl time.Duration) error

	// healthy reports whether the backend currently looks reachable (best-effort).
	healthy() bool

	// Close releases any resources (connections). Safe to call on a nil-ish store.
	Close() error
}

// memStore is the default impl. It is intentionally INERT: it does not store
// anything and signals "not available" so every call site uses its existing
// in-memory path. It exists so the broker can hold a non-nil sharedStore in tests
// and so the interface has two concrete impls, while the flag-OFF production path
// simply leaves b.shared == nil (zero behavior change, zero allocation).
type memStore struct{}

func newMemStore() *memStore { return &memStore{} }

// rateAllow on memStore returns ErrNoSharedStore so the rate limiter uses its local
// bucket. ok is true only to be safe if a caller ignored the error (it never should).
func (m *memStore) rateAllow(string, float64, float64, time.Time) (bool, int, error) {
	return true, 0, errNoSharedStore
}
func (m *memStore) markSeen(string, time.Time) error        { return errNoSharedStore }
func (m *memStore) liveness() (map[string]time.Time, error) { return nil, errNoSharedStore }

// cacheGet on memStore is always a MISS (no backend), so call sites compute directly.
func (m *memStore) cacheGet(string) ([]byte, bool, error) { return nil, false, errNoSharedStore }

// cacheSet on memStore is a no-op (nothing to store); the caller already served the
// freshly computed value.
func (m *memStore) cacheSet(string, []byte, time.Duration) error { return errNoSharedStore }

func (m *memStore) healthy() bool { return false }
func (m *memStore) Close() error  { return nil }

// errNoSharedStore signals "no shared backend; use the in-memory path". It is a
// sentinel, not a failure - call sites treat ANY non-nil error the same way (fall
// back), so this just keeps memStore's no-op explicit.
var errNoSharedStore = redis.Nil

// valkeyStore backs the SAFE state with a Redis-protocol server (DigitalOcean
// Valkey). All keys are namespaced under keyPrefix so they never collide with the
// other projects on the shared db-halo-cache instance.
type valkeyStore struct {
	rdb *redis.Client

	mu      sync.Mutex
	up      bool // last observed reachability (for healthy())
	lastLog time.Time
}

// rateBucketTTL bounds how long an idle rate-limit bucket lives in Valkey. It only
// needs to outlive a plausible refill window; well past that, an absent bucket is
// indistinguishable from a full one, so we let it expire to keep the shared keyspace
// from accumulating dead per-IP keys.
const rateBucketTTL = 10 * time.Minute

// livenessTTL bounds how long a node's shared last_seen survives without a refresh.
// It is generous relative to nodeTTL (45s) so a brief heartbeat gap does not drop a
// node from the shared view, while a long-dead node eventually ages out of Valkey.
const livenessTTL = 10 * time.Minute

// sharedOpTimeout caps every individual Valkey call so a slow/hung backend can never
// stall a broker request path; on timeout the call returns an error and the caller
// falls back to in-memory.
const sharedOpTimeout = 750 * time.Millisecond

// newValkeyStore parses a rediss://... (or redis://...) URL and connects. It does a
// single bounded PING so a bad URL / unreachable server is detected at startup; the
// CALLER decides what to do with the error (the broker logs a warning and falls back
// to in-memory - it never crashes). Returns the store even when the ping fails so a
// later recovery is possible, but reports the error so startup can log + degrade.
func newValkeyStore(url string) (*valkeyStore, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	// Keep timeouts tight: this is a hot-path cache, not a primary store.
	opt.DialTimeout = 2 * time.Second
	opt.ReadTimeout = sharedOpTimeout
	opt.WriteTimeout = sharedOpTimeout
	opt.MaxRetries = 1
	vs := &valkeyStore{rdb: redis.NewClient(opt)}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := vs.rdb.Ping(ctx).Err(); err != nil {
		vs.setUp(false)
		return vs, err
	}
	vs.setUp(true)
	return vs, nil
}

func (v *valkeyStore) setUp(up bool) {
	v.mu.Lock()
	v.up = up
	v.mu.Unlock()
}

func (v *valkeyStore) healthy() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.up
}

// noteErr records reachability and rate-limits the warning log so a backend outage
// does not spam the broker log on every request.
func (v *valkeyStore) noteErr(op string, err error) {
	if err == nil || err == redis.Nil {
		v.setUp(true)
		return
	}
	v.mu.Lock()
	v.up = false
	logNow := time.Since(v.lastLog) > 30*time.Second
	if logNow {
		v.lastLog = time.Now()
	}
	v.mu.Unlock()
	if logNow {
		log.Printf("shared-state: valkey %s failed, using in-memory fallback: %v", op, err)
	}
}

func (v *valkeyStore) Close() error {
	if v == nil || v.rdb == nil {
		return nil
	}
	return v.rdb.Close()
}

// tokenBucketScript is an atomic Redis token-bucket. It mirrors the exact refill +
// consume math in rateLimiter.allowAt so the shared decision matches the local one:
//
//	tokens += elapsed_seconds * (rpm/60); cap at burst; allow if >= 1, else deny.
//
// State is stored as a hash {t: tokens, ts: last_ms} under one prefixed key with a
// TTL refreshed each call. Doing the read-modify-write in a single script makes it
// atomic ACROSS broker instances (the whole point of sharing the bucket).
//
// KEYS[1] = prefixed bucket key
// ARGV[1] = rpm, ARGV[2] = burst, ARGV[3] = now_ms, ARGV[4] = ttl_ms
// returns {allowed (1/0), retry_after_seconds}
var tokenBucketScript = redis.NewScript(`
local key   = KEYS[1]
local rpm   = tonumber(ARGV[1])
local burst = tonumber(ARGV[2])
local now   = tonumber(ARGV[3])
local ttl   = tonumber(ARGV[4])

local data   = redis.call('HMGET', key, 't', 'ts')
local tokens = tonumber(data[1])
local last   = tonumber(data[2])
if tokens == nil then
  tokens = burst
  last   = now
end

local rate = rpm / 60.0
tokens = tokens + ((now - last) / 1000.0) * rate
if tokens > burst then tokens = burst end

local allowed = 0
local retry   = 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
else
  retry = math.ceil((1 - tokens) / rate)
  if retry < 1 then retry = 1 end
end

redis.call('HSET', key, 't', tokens, 'ts', now)
redis.call('PEXPIRE', key, ttl)
return {allowed, retry}
`)

func (v *valkeyStore) rateAllow(key string, rpm, burst float64, now time.Time) (bool, int, error) {
	if v == nil || v.rdb == nil {
		return true, 0, errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	fullKey := keyPrefix + "rl:" + key
	res, err := tokenBucketScript.Run(ctx, v.rdb, []string{fullKey},
		rpm, burst, now.UnixMilli(), rateBucketTTL.Milliseconds()).Result()
	if err != nil {
		v.noteErr("rateAllow", err)
		return true, 0, err
	}
	v.setUp(true)
	arr, ok := res.([]interface{})
	if !ok || len(arr) != 2 {
		return true, 0, errNoSharedStore
	}
	allowed, _ := arr[0].(int64)
	retry, _ := arr[1].(int64)
	return allowed == 1, int(retry), nil
}

// livenessKey is the prefixed hash holding node -> last_seen unix-ms across instances.
const livenessField = "ls"

func livenessKey(node string) string { return keyPrefix + "node:" + node }

func (v *valkeyStore) markSeen(node string, now time.Time) error {
	if v == nil || v.rdb == nil {
		return errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	key := livenessKey(node)
	pipe := v.rdb.Pipeline()
	pipe.HSet(ctx, key, livenessField, now.UnixMilli())
	pipe.PExpire(ctx, key, livenessTTL)
	// Track the node id in a prefixed set so liveness() can enumerate without an
	// un-prefixed SCAN over the SHARED keyspace (which would touch other projects).
	pipe.SAdd(ctx, keyPrefix+"nodes", node)
	pipe.PExpire(ctx, keyPrefix+"nodes", livenessTTL)
	_, err := pipe.Exec(ctx)
	if err != nil {
		v.noteErr("markSeen", err)
		return err
	}
	v.setUp(true)
	return nil
}

func (v *valkeyStore) liveness() (map[string]time.Time, error) {
	if v == nil || v.rdb == nil {
		return nil, errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	ids, err := v.rdb.SMembers(ctx, keyPrefix+"nodes").Result()
	if err != nil {
		v.noteErr("liveness", err)
		return nil, err
	}
	out := make(map[string]time.Time, len(ids))
	for _, id := range ids {
		ms, err := v.rdb.HGet(ctx, livenessKey(id), livenessField).Int64()
		if err == redis.Nil {
			continue // expired since the set listing - skip
		}
		if err != nil {
			v.noteErr("liveness", err)
			return out, err
		}
		out[id] = time.UnixMilli(ms)
	}
	v.setUp(true)
	return out, nil
}

// cacheKeyPrefix namespaces the response cache under the shared keyspace so it never
// collides with another project (or with the rl:/node:/nodes keys this layer also
// writes). Every cache key is rogerai:cache:<logical key>.
const cacheKeyPrefix = keyPrefix + "cache:"

// cacheGet fetches the cached bytes for a logical key. A miss (key absent) returns
// found=false with a nil error so the caller recomputes WITHOUT logging a backend
// error. Any real backend error returns err (caller treats it as a miss + recompute);
// it never fails the request.
func (v *valkeyStore) cacheGet(key string) ([]byte, bool, error) {
	if v == nil || v.rdb == nil {
		return nil, false, errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	val, err := v.rdb.Get(ctx, cacheKeyPrefix+key).Bytes()
	if err == redis.Nil {
		v.setUp(true)
		return nil, false, nil // clean miss
	}
	if err != nil {
		v.noteErr("cacheGet", err)
		return nil, false, err
	}
	v.setUp(true)
	return val, true, nil
}

// cacheSet stores val under the logical key with a TTL via SETEX (atomic set+expire),
// so a stale entry can never outlive its short window. ttl<=0 is a no-op. A failure is
// non-fatal: the caller already served the fresh value, so we only note the error.
func (v *valkeyStore) cacheSet(key string, val []byte, ttl time.Duration) error {
	if v == nil || v.rdb == nil {
		return errNoSharedStore
	}
	if ttl <= 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	if err := v.rdb.Set(ctx, cacheKeyPrefix+key, val, ttl).Err(); err != nil {
		v.noteErr("cacheSet", err)
		return err
	}
	v.setUp(true)
	return nil
}

// openSharedStore builds the shared-state layer from ROGERAI_REDIS_URL. UNSET (the
// default + the fallback) returns nil: the broker uses its in-memory maps with ZERO
// behavior change. SET connects a valkeyStore; a connection failure at startup
// DEGRADES GRACEFULLY - it logs a warning and returns nil so the broker boots on the
// in-memory path and NEVER crashes. (The returned store is closed on a connect
// failure so we leak no client.)
func openSharedStore() sharedStore {
	url := envStr("ROGERAI_REDIS_URL", "")
	if url == "" {
		return nil // flag OFF: in-memory, byte-for-byte today's behavior.
	}
	vs, err := newValkeyStore(url)
	if err != nil {
		if vs != nil {
			_ = vs.Close()
		}
		log.Printf("shared-state: ROGERAI_REDIS_URL set but connect failed, falling back to in-memory (broker continues): %v", err)
		return nil
	}
	log.Printf("shared-state: valkey connected (keys namespaced under %q) - sharing anon/concierge rate limits + node liveness across instances", keyPrefix)
	return vs
}

// cacheTTLJitter adds a small (+0..15%) random jitter to a cache TTL so many entries
// written in the same burst do not all expire on the same tick (a thundering-herd /
// stampede where every instance recomputes at once). The jitter only ever LENGTHENS
// the TTL within the same small order, so the freshness window stays within spec.
func cacheTTLJitter(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return ttl
	}
	return ttl + time.Duration(rand.Int63n(int64(ttl/100*15)+1))
}

// serveCachedJSON is the read-through cache wrapper for the hot, expensive, READ-ONLY
// market/metrics paths. It NEVER touches a money/mutating path - callers pass it only
// idempotent read computations. Flow:
//
//   - flag OFF (b.shared == nil): compute() and write directly - ZERO behavior change.
//   - flag ON, cache HIT (bytes within TTL): serve the cached JSON, skip compute.
//   - flag ON, cache MISS: compute(), serve it, then populate the cache with the SHORT
//     TTL (jittered) for the next caller. Shared across instances.
//   - any Valkey error on GET or SET falls through to a direct compute/serve: a cache
//     failure can NEVER fail or stall a request.
//
// The key MUST already encode every input that changes the response. For an AUTHED
// feed the key MUST include the authenticated identity so one account's cached payload
// is NEVER served to another (the caller is responsible for that namespacing; this
// helper just keys/stores the bytes it is given). compute returns the value to
// JSON-encode; serveCachedJSON marshals it once and caches the serialized bytes.
func (b *broker) serveCachedJSON(w http.ResponseWriter, key string, ttl time.Duration, compute func() any) {
	// Flag OFF / no shared backend: the existing direct path, byte-for-byte.
	if b.shared == nil {
		writeJSON(w, http.StatusOK, compute())
		return
	}
	// Cache HIT: serve the stored JSON verbatim (already serialized, small payload).
	if val, found, err := b.shared.cacheGet(key); err == nil && found {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(val)
		return
	}
	// MISS (or a cache error): compute, serialize once, serve, then populate.
	payload := compute()
	body, err := json.Marshal(payload)
	if err != nil {
		// Should never happen for these views; fall back to the standard encoder.
		writeJSON(w, http.StatusOK, payload)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
	// Populate for the next caller. A SET failure is non-fatal (we already served).
	_ = b.shared.cacheSet(key, body, cacheTTLJitter(ttl))
}

// Cache TTLs. The PUBLIC market views (/discover, /market) get a very short window:
// they change at most every probe round or as traffic shifts, so a 2-3s window is
// invisible to users while collapsing repeated full-market recomputes. The AUTHED
// feeds (/metrics/series, /console) get a longer window since a single user's
// receipts/series move slowly and the payload is per-identity.
const (
	publicMarketTTL = 3 * time.Second
	authedFeedTTL   = 20 * time.Second
)
