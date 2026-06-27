package main

import (
	"context"
	"encoding/json"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
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

	// cacheDel removes a cached entry so the NEXT read misses and re-resolves from the
	// source of truth. Used to invalidate an immutable-binding cache on a bind WRITE, so
	// a re-bind is reflected at once instead of after the TTL. A non-nil err is non-fatal
	// (the TTL is the backstop). A missing key is not an error.
	cacheDel(key string) error

	// counterGet reads a numeric counter (a stringified float) for key. found==false on a
	// miss; a non-nil err means the backend was unreachable. It is a fast-path accelerator
	// for a value the caller can always RECONCILE from Postgres (the source of truth) on a
	// miss/error - NEVER the authority. key is the caller's logical key (impls namespace it
	// under rogerai:ctr:).
	counterGet(key string) (val float64, found bool, err error)

	// counterSet seeds a counter to val with a TTL (reconciliation: writing the Postgres
	// truth into the fast-path). ttl<=0 persists it. A non-nil err is non-fatal.
	counterSet(key string, val float64, ttl time.Duration) error

	// counterIncr atomically adds delta to a counter and returns the new value. It is used
	// to keep a money fast-path (the monthly-spend counter) current at Finalize. A non-nil
	// err means the increment did not land - the caller treats the counter as unreliable
	// and reconciles from Postgres on the next read. A counter that does not yet exist
	// starts at 0 before the add (so the FIRST increment after an eviction under-counts
	// until the next reconcile - which is why a money read NEVER trusts a bare counter as
	// authoritative without a reconcile path).
	counterIncr(key string, delta float64) (val float64, err error)

	// setIfAbsent sets key=val only if it does not already exist (SETNX), with a TTL, and
	// reports whether THIS call set it (set==true) or it already existed (set==false). It
	// backs idempotent fast-path flags (e.g. "seeded:<wallet>") whose REAL guard is a
	// Postgres ON-CONFLICT - so a lost/evicted flag is harmless (the guard re-runs). A
	// non-nil err means the backend was unreachable; the caller must fall back to doing
	// the underlying (idempotent) work.
	setIfAbsent(key, val string, ttl time.Duration) (set bool, err error)

	// healthy reports whether the backend currently looks reachable (best-effort).
	healthy() bool

	// markInflight write-throughs THIS instance's current in-flight count for a node
	// (mirrors the Stage-1 liveness write-through): it stores count under a per-instance
	// field in the node's shared inflight hash with a TTL, so a peer can sum it. A non-nil
	// err is best-effort/non-fatal (the local count stays authoritative for this
	// instance's own dispatch decisions).
	markInflight(instanceID, node string, count int, now time.Time) error

	// inflightByNode returns, for every node any instance has reported, the SUM of all
	// instances' in-flight counts EXCEPT this instance's own (selfInstanceID is excluded
	// so the caller can add its exact live local count without double-counting). The
	// broker merges this peer-sum into a peerInflight map on the same background loop as
	// liveness, so capacity-aware pick sees cross-instance load without a Valkey hop on
	// the hot path. A non-nil err means the snapshot is unavailable this round (the
	// caller keeps the last merged peer-sum, degrading to local-only capacity).
	inflightByNode(selfInstanceID string) (map[string]int, error)

	// --- PRE-SCALE Stage 2: the cross-instance job/result/stream RENDEZVOUS bus. ---
	//
	// These back the multi-instance relay path (ROGERAI_MULTI_INSTANCE=1). They are a
	// thin pub/sub: a relay on instance A dispatches a job onto the bus channel for the
	// picked node; whichever instance holds that node's long-poll receives it, serves
	// the local upstream, and publishes the result/stream-chunks back on a per-JOB
	// channel that the ORIGINATING instance is subscribed to. Pub/sub (at-most-once) is
	// the right primitive here, NOT Streams: the originating instance holds the live
	// consumer HTTP connection AND the pre-dispatch credit Hold; if it dies the request
	// is already lost (the consumer socket is gone) and the deferred ReleaseHold refunds
	// the hold, so durability/replay buys nothing. A dropped/missed message simply lets
	// the waiter time out and fail the request CLEANLY (never a double-serve or
	// double-charge, since Hold + the Postgres Finalize are the durable money truth, not
	// the bus). Every method is bounded; ALL of them are no-ops on memStore (the flag is
	// only ever ON with a valkeyStore), and a non-nil err fails the request cleanly.

	// busPublishJob hands a serialized job onto the bus channel for nodeID so the
	// instance currently long-polling that node (any instance) receives it. delivered
	// reports the number of subscribers the message reached (0 = no poller is listening
	// on any instance right now -> the caller treats it as "node busy / no poller free",
	// exactly like a full local job channel today). A non-nil err means the publish did
	// not happen and the caller must fail the request cleanly.
	busPublishJob(nodeID string, job []byte) (delivered int, err error)

	// busSubscribeJobs returns a channel of serialized jobs dispatched to nodeID from
	// ANY instance, plus a cancel to release the subscription. The poll handler waits on
	// it exactly as it waits on the local job channel today. The subscription is torn
	// down when ctx is cancelled (the poll returning) or cancel() is called.
	busSubscribeJobs(ctx context.Context, nodeID string) (<-chan []byte, func(), error)

	// busPublishResult publishes a serialized non-stream JobResult back on the per-job
	// channel the ORIGINATING instance subscribed to. Best-effort delivery: a non-nil
	// err is returned to the node-facing handler but the originating instance's own
	// timeout is the backstop (it fails the relay cleanly and refunds the hold).
	busPublishResult(jobID string, result []byte) error

	// busSubscribeResult subscribes the originating instance to the per-job result
	// channel and returns it plus a cancel. The non-stream relay selects on it instead
	// of the local resCh. Torn down on ctx cancel / cancel().
	busSubscribeResult(ctx context.Context, jobID string) (<-chan []byte, func(), error)

	// busPublishStream publishes one framed stream message back on the per-job stream
	// channel: a CHUNK (raw SSE bytes the originating instance writes+flushes to the
	// client in order) or the terminal DONE marker. Redis pub/sub preserves per-channel
	// order from a single publisher (the one poller serving the stream), so chunks arrive
	// in order on the originating instance. A non-nil err is returned to the streaming
	// node handler.
	busPublishStreamChunk(jobID string, chunk []byte) error
	busPublishStreamDone(jobID string) error

	// busSubscribeStream subscribes the originating instance to the per-job stream
	// channel. Each received frame is either a chunk (isDone=false, payload=raw bytes) or
	// the terminal marker (isDone=true). Torn down on ctx cancel / cancel().
	busSubscribeStream(ctx context.Context, jobID string) (<-chan streamFrame, func(), error)

	// putNode mirrors a node's full registration JSON (incl. BridgeToken) into the
	// SHARED registry so EVERY instance can pick it AND authenticate its poll/result -
	// not only the instance the node dialed. ttl is refreshed on each heartbeat
	// (markSeen extends it), so a node that stops heartbeating ages out. Multi-instance
	// scale-out only; flag-off never calls it.
	putNode(id string, reg []byte, ttl time.Duration) error
	// getNode returns ONE shared node registration JSON by id (found == false on a miss).
	// The cheap, targeted twin of allNodes(): the poll/heartbeat/result handlers use it to
	// LAZILY learn a node that registered on a PEER instance the instant a request for it
	// arrives - instead of 404ing, which the node misreads as "broker restarted" and
	// re-registers (the cross-instance re-registration storm). A non-nil err = treat as miss.
	getNode(id string) ([]byte, bool, error)
	// allNodes returns every shared node registration (id -> JSON) for the registry
	// mirror that each instance syncs into its in-memory b.nodes/b.tunnels.
	allNodes() (map[string][]byte, error)

	// Close releases any resources (connections). Safe to call on a nil-ish store.
	Close() error
}

// streamFrame is one message off the per-job stream bus channel: a raw SSE chunk to
// relay to the waiting client, or the terminal done marker (payload empty).
type streamFrame struct {
	payload []byte
	isDone  bool
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

// cacheDel on memStore is a no-op (nothing cached to invalidate).
func (m *memStore) cacheDel(string) error { return errNoSharedStore }

// The counter / setIfAbsent primitives on memStore are all "unavailable" no-ops, so
// every money/seed fast-path falls back to its Postgres-authoritative computation.
func (m *memStore) counterGet(string) (float64, bool, error) { return 0, false, errNoSharedStore }
func (m *memStore) counterSet(string, float64, time.Duration) error {
	return errNoSharedStore
}
func (m *memStore) counterIncr(string, float64) (float64, error) { return 0, errNoSharedStore }
func (m *memStore) setIfAbsent(string, string, time.Duration) (bool, error) {
	return false, errNoSharedStore
}

func (m *memStore) healthy() bool { return false }
func (m *memStore) Close() error  { return nil }

func (m *memStore) markInflight(string, string, int, time.Time) error { return errNoSharedStore }
func (m *memStore) inflightByNode(string) (map[string]int, error)     { return nil, errNoSharedStore }

// The rendezvous-bus primitives are all "unavailable" no-ops on memStore: the
// multi-instance flag is only ever ON with a valkeyStore, so the in-memory path never
// touches these. They return errNoSharedStore so any accidental caller fails cleanly.
func (m *memStore) busPublishJob(string, []byte) (int, error) { return 0, errNoSharedStore }
func (m *memStore) busSubscribeJobs(context.Context, string) (<-chan []byte, func(), error) {
	return nil, func() {}, errNoSharedStore
}
func (m *memStore) busPublishResult(string, []byte) error { return errNoSharedStore }
func (m *memStore) busSubscribeResult(context.Context, string) (<-chan []byte, func(), error) {
	return nil, func() {}, errNoSharedStore
}
func (m *memStore) busPublishStreamChunk(string, []byte) error { return errNoSharedStore }
func (m *memStore) busPublishStreamDone(string) error          { return errNoSharedStore }
func (m *memStore) busSubscribeStream(context.Context, string) (<-chan streamFrame, func(), error) {
	return nil, func() {}, errNoSharedStore
}
func (m *memStore) putNode(string, []byte, time.Duration) error { return errNoSharedStore }
func (m *memStore) getNode(string) ([]byte, bool, error)        { return nil, false, errNoSharedStore }
func (m *memStore) allNodes() (map[string][]byte, error)        { return nil, errNoSharedStore }

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

	// opErrors is a monotonic count of EVERY failed Valkey op (publish/subscribe/get/set/
	// script/...), funneled through noteErr. It is an atomic so the (rare) error path adds
	// no lock contention beyond the existing mu, and it is surfaced read-only on the admin
	// overview so a growing bus/cache error rate is visible instead of buried in the
	// rate-limited warning log. redis.Nil (a clean miss / no-shared-store sentinel) is NOT
	// an error and is never counted.
	opErrors atomic.Int64
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
	v.opErrors.Add(1)
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
	// Keep the shared REGISTRY entry (if any) alive as long as the node heartbeats, even
	// though it only re-registers rarely: a heartbeat that lands on ANY instance extends
	// the reg TTL so the registry mirror doesn't drop a live node. No-op if no reg key.
	pipe.PExpire(ctx, regKey(node), livenessTTL)
	// CRITICAL: also extend the regset INDEX that allNodes() enumerates through. The
	// reg:<node> value key above is only ever *read* via this set; refreshing the value
	// without the index lets the index expire after livenessTTL (nodes re-register
	// rarely), orphaning the kept-alive reg keys -> a peer that restarts or scales out
	// after the TTL can't re-learn the node (the deferred C2 503/404 break). Mirror the
	// keyPrefix+"nodes" handling exactly so heartbeats keep BOTH the value and its index.
	pipe.PExpire(ctx, keyPrefix+"regset", livenessTTL)
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
	if len(ids) == 0 {
		v.setUp(true)
		return map[string]time.Time{}, nil
	}
	// Batch the per-node HGETs into ONE pipeline (one round-trip) instead of N sequential
	// round-trips: same result, but the sync-loop latency no longer grows linearly with
	// the node count (each saved round-trip is a full Valkey RTT in production).
	pipe := v.rdb.Pipeline()
	cmds := make([]*redis.StringCmd, len(ids))
	for i, id := range ids {
		cmds[i] = pipe.HGet(ctx, livenessKey(id), livenessField)
	}
	// Exec surfaces the first command error; a per-key redis.Nil (expired since the
	// SMEMBERS listing) is reported on that command, not as a fatal Exec error - so we
	// ignore a redis.Nil from Exec and inspect each command below, skipping the misses.
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		v.noteErr("liveness", err)
		return nil, err
	}
	out := make(map[string]time.Time, len(ids))
	for i, id := range ids {
		ms, err := cmds[i].Int64()
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

// regKey holds a node's full registration JSON, shared so any instance can mirror it.
func regKey(node string) string { return keyPrefix + "reg:" + node }

func (v *valkeyStore) putNode(id string, reg []byte, ttl time.Duration) error {
	if v == nil || v.rdb == nil {
		return errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	pipe := v.rdb.Pipeline()
	pipe.Set(ctx, regKey(id), reg, ttl)
	pipe.SAdd(ctx, keyPrefix+"regset", id)
	pipe.PExpire(ctx, keyPrefix+"regset", ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		v.noteErr("putNode", err)
		return err
	}
	v.setUp(true)
	return nil
}

func (v *valkeyStore) getNode(id string) ([]byte, bool, error) {
	if v == nil || v.rdb == nil {
		return nil, false, errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	raw, err := v.rdb.Get(ctx, regKey(id)).Bytes()
	if err == redis.Nil {
		return nil, false, nil // no such node in the shared registry
	}
	if err != nil {
		v.noteErr("getNode", err)
		return nil, false, err
	}
	v.setUp(true)
	return raw, true, nil
}

func (v *valkeyStore) allNodes() (map[string][]byte, error) {
	if v == nil || v.rdb == nil {
		return nil, errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	ids, err := v.rdb.SMembers(ctx, keyPrefix+"regset").Result()
	if err != nil {
		v.noteErr("allNodes", err)
		return nil, err
	}
	if len(ids) == 0 {
		v.setUp(true)
		return map[string][]byte{}, nil
	}
	// Batch the per-node GETs into ONE pipeline (one round-trip) instead of N sequential
	// round-trips - the registry mirror sync no longer scales its latency with node count.
	pipe := v.rdb.Pipeline()
	cmds := make([]*redis.StringCmd, len(ids))
	for i, id := range ids {
		cmds[i] = pipe.Get(ctx, regKey(id))
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		v.noteErr("allNodes", err)
		return nil, err
	}
	out := make(map[string][]byte, len(ids))
	for i, id := range ids {
		raw, err := cmds[i].Bytes()
		if err == redis.Nil {
			continue // reg expired since the set listing - skip
		}
		if err != nil {
			v.noteErr("allNodes", err)
			return out, err
		}
		out[id] = raw
	}
	v.setUp(true)
	return out, nil
}

// --- PRE-SCALE Stage 2: cross-instance inflight (write-through + merge). ---
//
// Each instance write-throughs its OWN inflight count per node under a hash field keyed
// by the instance id; a peer sums the OTHER instances' fields and adds its exact local
// count. Modeled on the liveness write-through: forward-only freshness via a TTL, no
// hot-path Valkey hop (the merge runs on the background loop). inflightKey is the hash;
// inflightNodesKey enumerates the nodes without an un-prefixed SCAN over the shared
// keyspace.
func inflightKey(node string) string { return keyPrefix + "inflight:" + node }

const inflightNodesKey = keyPrefix + "inflight:nodes"

// inflightTTL bounds how long an instance's reported inflight survives without a
// refresh, so a crashed instance's stale load ages out and a node's hash cannot linger
// forever on the shared keyspace.
const inflightTTL = 60 * time.Second

func (v *valkeyStore) markInflight(instanceID, node string, count int, now time.Time) error {
	if v == nil || v.rdb == nil {
		return errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	key := inflightKey(node)
	pipe := v.rdb.Pipeline()
	pipe.HSet(ctx, key, instanceID, count)
	pipe.PExpire(ctx, key, inflightTTL)
	pipe.SAdd(ctx, inflightNodesKey, node)
	pipe.PExpire(ctx, inflightNodesKey, inflightTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		v.noteErr("markInflight", err)
		return err
	}
	v.setUp(true)
	return nil
}

func (v *valkeyStore) inflightByNode(selfInstanceID string) (map[string]int, error) {
	if v == nil || v.rdb == nil {
		return nil, errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	nodes, err := v.rdb.SMembers(ctx, inflightNodesKey).Result()
	if err != nil {
		v.noteErr("inflightByNode", err)
		return nil, err
	}
	if len(nodes) == 0 {
		v.setUp(true)
		return map[string]int{}, nil
	}
	// Batch the per-node HGETALLs into ONE pipeline (one round-trip) instead of N
	// sequential round-trips - the peer-inflight merge stops scaling with node count.
	pipe := v.rdb.Pipeline()
	cmds := make([]*redis.MapStringStringCmd, len(nodes))
	for i, node := range nodes {
		cmds[i] = pipe.HGetAll(ctx, inflightKey(node))
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		v.noteErr("inflightByNode", err)
		return nil, err
	}
	out := make(map[string]int, len(nodes))
	for i, node := range nodes {
		fields, err := cmds[i].Result()
		if err == redis.Nil {
			continue
		}
		if err != nil {
			v.noteErr("inflightByNode", err)
			return out, err
		}
		sum := 0
		for inst, val := range fields {
			if inst == selfInstanceID {
				continue // exclude self: the caller adds its exact local count
			}
			n, _ := strconv.Atoi(val)
			if n > 0 {
				sum += n
			}
		}
		if sum > 0 {
			out[node] = sum
		}
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

// cacheDel removes a cached entry (DEL), so the next read misses and re-resolves. A
// missing key is not an error (DEL returns 0). A backend error is noted + returned
// (non-fatal: the TTL is the backstop).
func (v *valkeyStore) cacheDel(key string) error {
	if v == nil || v.rdb == nil {
		return errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	if err := v.rdb.Del(ctx, cacheKeyPrefix+key).Err(); err != nil {
		v.noteErr("cacheDel", err)
		return err
	}
	v.setUp(true)
	return nil
}

// counterKeyPrefix namespaces the numeric fast-path counters (the monthly-spend
// accelerator, the seed-remaining mirror) under the shared keyspace so they never
// collide with another project or with the rl:/node:/cache: keys.
const counterKeyPrefix = keyPrefix + "ctr:"

func (v *valkeyStore) counterGet(key string) (float64, bool, error) {
	if v == nil || v.rdb == nil {
		return 0, false, errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	val, err := v.rdb.Get(ctx, counterKeyPrefix+key).Float64()
	if err == redis.Nil {
		v.setUp(true)
		return 0, false, nil // clean miss -> caller reconciles from Postgres
	}
	if err != nil {
		v.noteErr("counterGet", err)
		return 0, false, err
	}
	v.setUp(true)
	return val, true, nil
}

func (v *valkeyStore) counterSet(key string, val float64, ttl time.Duration) error {
	if v == nil || v.rdb == nil {
		return errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	// ttl<=0 means persist (no expiry); else set with the expiry in one call.
	if err := v.rdb.Set(ctx, counterKeyPrefix+key, val, ttl).Err(); err != nil {
		v.noteErr("counterSet", err)
		return err
	}
	v.setUp(true)
	return nil
}

func (v *valkeyStore) counterIncr(key string, delta float64) (float64, error) {
	if v == nil || v.rdb == nil {
		return 0, errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	val, err := v.rdb.IncrByFloat(ctx, counterKeyPrefix+key, delta).Result()
	if err != nil {
		v.noteErr("counterIncr", err)
		return 0, err
	}
	v.setUp(true)
	return val, nil
}

func (v *valkeyStore) setIfAbsent(key, val string, ttl time.Duration) (bool, error) {
	if v == nil || v.rdb == nil {
		return false, errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	set, err := v.rdb.SetNX(ctx, counterKeyPrefix+key, val, ttl).Result()
	if err != nil {
		v.noteErr("setIfAbsent", err)
		return false, err
	}
	v.setUp(true)
	return set, nil
}

// --- PRE-SCALE Stage 2: the rendezvous bus on valkeyStore (Redis pub/sub). ---
//
// Channel namespaces (all under keyPrefix so they never collide on the shared Valkey):
//
//	rogerai:bus:job:<nodeID>   - jobs dispatched to a node (poller subscribes per node)
//	rogerai:bus:res:<jobID>    - the non-stream result back to the originating instance
//	rogerai:bus:strm:<jobID>   - the SSE stream frames back to the originating instance
const (
	busJobPrefix    = keyPrefix + "bus:job:"
	busResultPrefix = keyPrefix + "bus:res:"
	busStreamPrefix = keyPrefix + "bus:strm:"
)

// streamDoneMarker is the single-byte sentinel published on a job's stream channel to
// signal end-of-stream. A real SSE chunk is never a bare 0x04, so it cannot be confused
// with a chunk. (We also length-frame nothing else: pub/sub delivers each Publish as one
// message, so a chunk is exactly the bytes published.)
var streamDoneMarker = []byte{0x04}

// busPublishTimeout bounds a single bus PUBLISH. It is independent of sharedOpTimeout
// so a slow publish on the node-facing handler can never wedge a poller/streamer.
const busPublishTimeout = sharedOpTimeout

func (v *valkeyStore) busPublishJob(nodeID string, job []byte) (int, error) {
	if v == nil || v.rdb == nil {
		return 0, errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), busPublishTimeout)
	defer cancel()
	n, err := v.rdb.Publish(ctx, busJobPrefix+nodeID, job).Result()
	if err != nil {
		v.noteErr("busPublishJob", err)
		return 0, err
	}
	v.setUp(true)
	return int(n), nil
}

// busSubscribe is the shared subscribe helper: it opens a pub/sub subscription on
// channel, hands back a []byte channel of the message payloads, and a cancel that
// closes the subscription. A goroutine pumps messages until ctx is done or the
// subscription closes. The buffered out channel (depth 64) absorbs a burst of stream
// chunks without blocking the redis receive loop; if a slow consumer fills it we drop
// the receive loop on the next ctx check (the consumer's own timeout is the backstop).
func (v *valkeyStore) busSubscribe(ctx context.Context, channel string) (<-chan []byte, func(), error) {
	if v == nil || v.rdb == nil {
		return nil, func() {}, errNoSharedStore
	}
	subCtx, cancel := context.WithCancel(ctx)
	ps := v.rdb.Subscribe(subCtx, channel)
	// Wait for the subscription to be confirmed so a Publish racing the Subscribe is not
	// missed: Receive blocks for the subscribe confirmation. Bound it so a hung backend
	// cannot stall the caller.
	recvCtx, recvCancel := context.WithTimeout(subCtx, sharedOpTimeout)
	_, err := ps.Receive(recvCtx)
	recvCancel()
	if err != nil {
		cancel()
		_ = ps.Close()
		v.noteErr("busSubscribe", err)
		return nil, func() {}, err
	}
	v.setUp(true)
	out := make(chan []byte, 64)
	ch := ps.Channel()
	go func() {
		defer close(out)
		for {
			select {
			case <-subCtx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				select {
				case out <- []byte(msg.Payload):
				case <-subCtx.Done():
					return
				}
			}
		}
	}()
	closeFn := func() {
		cancel()
		_ = ps.Close()
	}
	return out, closeFn, nil
}

func (v *valkeyStore) busSubscribeJobs(ctx context.Context, nodeID string) (<-chan []byte, func(), error) {
	return v.busSubscribe(ctx, busJobPrefix+nodeID)
}

func (v *valkeyStore) busPublishResult(jobID string, result []byte) error {
	if v == nil || v.rdb == nil {
		return errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), busPublishTimeout)
	defer cancel()
	if err := v.rdb.Publish(ctx, busResultPrefix+jobID, result).Err(); err != nil {
		v.noteErr("busPublishResult", err)
		return err
	}
	v.setUp(true)
	return nil
}

func (v *valkeyStore) busSubscribeResult(ctx context.Context, jobID string) (<-chan []byte, func(), error) {
	return v.busSubscribe(ctx, busResultPrefix+jobID)
}

func (v *valkeyStore) busPublishStreamChunk(jobID string, chunk []byte) error {
	if v == nil || v.rdb == nil {
		return errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), busPublishTimeout)
	defer cancel()
	if err := v.rdb.Publish(ctx, busStreamPrefix+jobID, chunk).Err(); err != nil {
		v.noteErr("busPublishStreamChunk", err)
		return err
	}
	v.setUp(true)
	return nil
}

func (v *valkeyStore) busPublishStreamDone(jobID string) error {
	if v == nil || v.rdb == nil {
		return errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), busPublishTimeout)
	defer cancel()
	if err := v.rdb.Publish(ctx, busStreamPrefix+jobID, streamDoneMarker).Err(); err != nil {
		v.noteErr("busPublishStreamDone", err)
		return err
	}
	v.setUp(true)
	return nil
}

func (v *valkeyStore) busSubscribeStream(ctx context.Context, jobID string) (<-chan streamFrame, func(), error) {
	raw, cancel, err := v.busSubscribe(ctx, busStreamPrefix+jobID)
	if err != nil {
		return nil, cancel, err
	}
	out := make(chan streamFrame, 64)
	go func() {
		defer close(out)
		for payload := range raw {
			if len(payload) == 1 && payload[0] == streamDoneMarker[0] {
				select {
				case out <- streamFrame{isDone: true}:
				case <-ctx.Done():
				}
				return
			}
			select {
			case out <- streamFrame{payload: payload}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, cancel, nil
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
// The key MUST already encode every input that changes the response. For PUBLIC views
// (/discover, /market) a single shared entry is safe. For an AUTHED, per-identity feed
// DO NOT call this directly with a hand-built key - use serveCachedAuthedJSON, which
// takes the resolved identity as typed arguments and builds the wallet-namespaced key
// itself (and refuses to cache an anon caller), so one account's payload can never be
// served to another. compute returns the value to JSON-encode; serveCachedJSON marshals
// it once and caches the serialized bytes.
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
	// MISS (or a cache error): compute under a per-KEY singleflight so a CONCURRENT
	// miss/expiry on this one hot key collapses to ONE compute (+ one cache populate)
	// instead of a thundering herd each re-running the full (b.mu-locked) recompute.
	// Only one goroutine per key runs compute(); the rest share its serialized bytes.
	body := b.computeCachedJSON(key, ttl, compute)
	if body == nil {
		// Marshal failed for this view (should never happen); fall back to the standard
		// encoder on a fresh compute so the request still serves a body.
		writeJSON(w, http.StatusOK, compute())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// computeCachedJSON runs compute() under the broker's per-key singleflight, returning
// the serialized JSON bytes (nil on a marshal error). The leader marshals once, serves
// itself, and populates the cache; concurrent callers on the SAME key block on the
// leader and receive the identical bytes WITHOUT recomputing - this is the dogpile fix
// (B1). The cache SET is best-effort (a failure only means the next window recomputes).
func (b *broker) computeCachedJSON(key string, ttl time.Duration, compute func() any) []byte {
	v, _, _ := b.cacheFlight.Do(key, func() (any, error) {
		body, err := json.Marshal(compute())
		if err != nil {
			return []byte(nil), nil
		}
		// Populate for the next caller. A SET failure is non-fatal (we already serve).
		_ = b.shared.cacheSet(key, body, cacheTTLJitter(ttl))
		return body, nil
	})
	body, _ := v.([]byte)
	return body
}

// serveCachedAuthedJSON is the HARDENED read-through cache for a PER-IDENTITY (authed)
// feed. Unlike serveCachedJSON it does NOT accept a free-form key: it takes the RESOLVED,
// authenticated identity (the wallet and/or the operator pubkey, each only when that
// side is present) plus a feed name + variant suffix, and BUILDS the cache key itself
// via identityCacheKey. This makes cross-identity isolation STRUCTURAL: a caller can
// never hand it a key that omits (or spoofs) the identity, so one account's cached
// receipts/series/console can never be served to another (B2).
//
// REFUSE-TO-CACHE rule: when NEITHER identity side is present (an anon/empty caller),
// it computes + serves directly and NEVER writes a cache entry keyed on "" - so an
// unauthenticated response is never cached under (and later served from) an empty
// identity key. Flag OFF (shared == nil) is the direct path, byte-for-byte unchanged.
func (b *broker) serveCachedAuthedJSON(w http.ResponseWriter, feed, variant, wallet string, consumer bool, ownerPubkey string, provider bool, ttl time.Duration, compute func() any) {
	// Resolve the namespaced identities exactly as the key builder would, so the
	// refuse-when-anon decision matches the bytes that would be keyed.
	cacheW, cacheO := "", ""
	if consumer {
		cacheW = wallet
	}
	if provider {
		cacheO = ownerPubkey
	}
	// REFUSE to cache an anon/empty identity: no authenticated side -> never share an
	// entry keyed on "". Serve directly (cache OFF for this request) so we can't leak.
	if cacheW == "" && cacheO == "" {
		writeJSON(w, http.StatusOK, compute())
		return
	}
	key := identityCacheKey(feed, wallet, consumer, ownerPubkey, provider) + variant
	b.serveCachedJSON(w, key, ttl, compute)
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
