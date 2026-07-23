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

	// --- VERIFIED tool-call capability, as FIRST-CLASS shared state (features/trust/
	// toolcall_probe.feature). The verified "tools" bit is per-(node, model) and lives in the
	// shared store, NOT a per-instance map, so a regression the authoritative poll host clears
	// propagates to every peer (a peer never re-poisons a cleared verdict). The broker merges
	// toolsVerified() into an in-memory read map on the same sync loop as liveness, keeping the
	// hot /discover + /market read purely in-memory.

	// markToolsVerified records/refreshes a model's VERIFIED tool-call bit (a passing canary),
	// keyed by field=node+"\x00"+model, with a freshness TTL: a verified model re-probed within
	// the ceiling stays fresh; a host that dies without regressing lets the bit age out. A
	// non-nil err is best-effort (the local record stays this instance's own truth).
	markToolsVerified(node, model string, ttl time.Duration) error

	// clearToolsVerified retracts a model's verified bit (a definitive regression on the
	// authoritative poll host). It is the CROSS-INSTANCE removal path a per-instance map lacked:
	// once the host clears the shared field, every peer's next toolsVerified() drops it too.
	clearToolsVerified(node, model string) error

	// toolsVerified returns the UNION of verified (node,model) bits across all instances that
	// are still FRESH (field timestamp within ttl). Merged into the broker's in-memory read map
	// on the sync loop. A non-nil err means the snapshot is unavailable this round (the caller
	// keeps the last merged view). Keyed by node+"\x00"+model.
	toolsVerified(ttl time.Duration) (map[string]bool, error)

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

	// --- content-blind capsule rendezvous (capsule.go), keyed on the LOOKUP hash ---
	//
	// putCapsule stores an OPAQUE encrypted capsule blob under lookup with a TTL, so a
	// mint on one instance resolves on another (the multi-instance content-blind handoff).
	// It holds ONLY {lookup, ciphertext}: never the code, the key, or the plaintext. A
	// non-nil err (incl. errNoSharedStore on memStore) means the shared path is unavailable
	// and the caller uses its per-instance fallback map. ttl<=0 is treated as no-store.
	putCapsule(lookup string, blob []byte, ttl time.Duration) error

	// takeCapsule ATOMICALLY returns AND deletes the blob under lookup (one-time,
	// delete-on-read via GETDEL), so exactly one of N concurrent resolves across all
	// instances wins and every later resolve is a miss. found==false for an absent/expired
	// lookup. A non-nil err (incl. errNoSharedStore) routes the caller to its fallback map.
	takeCapsule(lookup string) (blob []byte, found bool, err error)

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

	// markInstance records THIS broker process's presence heartbeat so peers (and the ops
	// panel's topology block) can count the live instance fleet. It (re)writes a per-instance
	// presence key under instanceTTL and tracks the id in a prefixed set, refreshed each sync
	// tick. Best-effort/non-fatal: a non-nil err just means the panel degrades to a self-only
	// count this round. Only invoked in multi-instance mode (the single-instance count is 1).
	markInstance(instanceID string, now time.Time) error

	// liveInstances returns the number of DISTINCT broker instances whose presence key is
	// still live (unexpired) in the shared store - the fleet size the ops panel renders a
	// redundancy posture from. A non-nil err means the snapshot is unavailable this round, and
	// the caller falls back to a self-only count of 1 (this instance is always live).
	liveInstances() (int, error)

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

	// busClaimJob atomically claims a dispatched job for SINGLE delivery. busPublishJob is a
	// fan-out PUBLISH, so every one of a node's parallel pollers (Parallel=4) - across all
	// instances - receives the same job. Each poller must call busClaimJob(job.ID) before
	// serving; exactly the FIRST caller gets won=true and serves, every other gets won=false
	// and re-polls (204). Without this a job is served N times (N-fold billing; interleaved
	// corrupted streams). A non-nil err (claim store hiccup) lets the caller fall back to
	// serving - degrading to today's fan-out on a rare outage rather than stranding the job.
	busClaimJob(jobID string) (won bool, err error)

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

	// --- BASE STATION / remote control (v5.0.0), keyed on the SESSION id ---
	// busPublishRCIn publishes a viewer inbound (turn/confirm/backfill, serialized) so the
	// host's poll receives it no matter which instance the host is polling.
	busPublishRCIn(sid string, in []byte) error
	// busSubscribeRCIn subscribes the host's poll to the session's inbound channel.
	busSubscribeRCIn(ctx context.Context, sid string) (<-chan []byte, func(), error)
	// busPublishRCOut publishes a host frame (serialized RCFrame) to every viewer's stream on
	// any instance.
	busPublishRCOut(sid string, frame []byte) error
	// busSubscribeRCOut subscribes a viewer's stream to the session's frame channel.
	busSubscribeRCOut(ctx context.Context, sid string) (<-chan []byte, func(), error)
	// busNextRCSeq returns the next monotonic frame seq for a session via a shared INCR, so
	// viewers on different instances see one consistent ordering. TTL'd so it ages out.
	busNextRCSeq(sid string) (uint64, error)

	// busPopRCIn removes+returns ONE viewer inbound that busPublishRCIn buffered because it was
	// published while the host had no live poll subscribed (a PUBLISH to 0 subscribers, which
	// pub/sub drops). ok=false when the gap buffer is empty. The host's poll drains one per round.
	busPopRCIn(sid string) (in []byte, ok bool, err error)

	// putNode mirrors a node's full registration JSON (incl. BridgeToken) into the
	// SHARED registry so EVERY instance can pick it AND authenticate its poll/result -
	// not only the instance the node dialed. ttl is refreshed on each heartbeat
	// (markSeen extends it), so a node that stops heartbeating ages out. Written
	// whenever a shared backend is wired - REGARDLESS of the ROGERAI_MULTI_INSTANCE
	// bus flag - so registration state always travels with the liveness state markSeen
	// mirrors (task #52: the flag=0 churn fix; only job DISPATCH stays flag-gated).
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

	// putPrivateNode mirrors a PRIVATE (band) node's registration into a SEPARATE shared
	// namespace (preg:/pregset), so a peer can RESOLVE + ROUTE the band yet it NEVER appears
	// in the public allNodes() the /discover mirror reads (private secrecy by construction).
	// getPrivateNode/allPrivateNodes are the targeted + bulk reads; markSeen extends this
	// namespace's TTL on every heartbeat (private nodes re-register rarely) exactly as it does
	// the public registry, so a live band is not dropped between its infrequent re-registers.
	putPrivateNode(id string, reg []byte, ttl time.Duration) error
	getPrivateNode(id string) ([]byte, bool, error)
	allPrivateNodes() (map[string][]byte, error)

	// dropSharedNode removes a node from BOTH shared registries (public + private). register
	// calls it before re-publishing so a node that FLIPS private<->public never leaves a
	// stale entry in the OTHER namespace (which markSeen would otherwise keep alive forever),
	// keeping each node in EXACTLY ONE namespace and upholding private/public isolation.
	dropSharedNode(id string) error

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

// The tool-call verdict is inert on memStore: single-instance reads its own b.toolsOK map, so
// these are no-ops (the flag-OFF / no-Redis path never mirrors a verdict).
func (m *memStore) markToolsVerified(string, string, time.Duration) error { return errNoSharedStore }
func (m *memStore) clearToolsVerified(string, string) error               { return errNoSharedStore }
func (m *memStore) toolsVerified(time.Duration) (map[string]bool, error)  { return nil, errNoSharedStore }

// cacheGet on memStore is always a MISS (no backend), so call sites compute directly.
func (m *memStore) cacheGet(string) ([]byte, bool, error) { return nil, false, errNoSharedStore }

// cacheSet on memStore is a no-op (nothing to store); the caller already served the
// freshly computed value.
func (m *memStore) cacheSet(string, []byte, time.Duration) error { return errNoSharedStore }

// cacheDel on memStore is a no-op (nothing cached to invalidate).
func (m *memStore) cacheDel(string) error { return errNoSharedStore }

// The capsule rendezvous is inert on memStore: no shared backend, so the broker uses its
// per-instance capsuleStore map (single-instance / no-Valkey path).
func (m *memStore) putCapsule(string, []byte, time.Duration) error { return errNoSharedStore }
func (m *memStore) takeCapsule(string) ([]byte, bool, error)       { return nil, false, errNoSharedStore }

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
func (m *memStore) markInstance(string, time.Time) error              { return errNoSharedStore }
func (m *memStore) liveInstances() (int, error)                       { return 0, errNoSharedStore }

// The rendezvous-bus primitives are all "unavailable" no-ops on memStore: the
// multi-instance flag is only ever ON with a valkeyStore, so the in-memory path never
// touches these. They return errNoSharedStore so any accidental caller fails cleanly.
func (m *memStore) busPublishJob(string, []byte) (int, error) { return 0, errNoSharedStore }
func (m *memStore) busSubscribeJobs(context.Context, string) (<-chan []byte, func(), error) {
	return nil, func() {}, errNoSharedStore
}
func (m *memStore) busClaimJob(string) (bool, error) { return false, errNoSharedStore }
func (m *memStore) busPublishResult(string, []byte) error { return errNoSharedStore }
func (m *memStore) busSubscribeResult(context.Context, string) (<-chan []byte, func(), error) {
	return nil, func() {}, errNoSharedStore
}
func (m *memStore) busPublishStreamChunk(string, []byte) error { return errNoSharedStore }
func (m *memStore) busPublishStreamDone(string) error          { return errNoSharedStore }
func (m *memStore) busSubscribeStream(context.Context, string) (<-chan streamFrame, func(), error) {
	return nil, func() {}, errNoSharedStore
}
func (m *memStore) busPublishRCIn(string, []byte) error  { return errNoSharedStore }
func (m *memStore) busPublishRCOut(string, []byte) error { return errNoSharedStore }
func (m *memStore) busSubscribeRCIn(context.Context, string) (<-chan []byte, func(), error) {
	return nil, func() {}, errNoSharedStore
}
func (m *memStore) busSubscribeRCOut(context.Context, string) (<-chan []byte, func(), error) {
	return nil, func() {}, errNoSharedStore
}
func (m *memStore) busNextRCSeq(string) (uint64, error)                { return 0, errNoSharedStore }
func (m *memStore) busPopRCIn(string) ([]byte, bool, error)            { return nil, false, errNoSharedStore }
func (m *memStore) putNode(string, []byte, time.Duration) error        { return errNoSharedStore }
func (m *memStore) getNode(string) ([]byte, bool, error)               { return nil, false, errNoSharedStore }
func (m *memStore) allNodes() (map[string][]byte, error)               { return nil, errNoSharedStore }
func (m *memStore) putPrivateNode(string, []byte, time.Duration) error { return errNoSharedStore }
func (m *memStore) getPrivateNode(string) ([]byte, bool, error)        { return nil, false, errNoSharedStore }
func (m *memStore) allPrivateNodes() (map[string][]byte, error)        { return nil, errNoSharedStore }
func (m *memStore) dropSharedNode(string) error                        { return errNoSharedStore }

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

// busSubscribe RETRY tuning. DO App Platform reaches the managed Valkey over PUBLIC networking,
// so the pub/sub re-subscribe re-resolves the public hostname and intermittently hits DO's slow
// public DNS (`dial tcp: lookup ...: i/o timeout`). A SINGLE such blip used to drop the whole
// subscription to the in-memory fallback; these bound a short retry so an isolated timeout is
// absorbed. A genuinely-down bus still falls back after the attempts are spent (worst case ~=
// attempts*sharedOpTimeout + (attempts-1)*backoff, only on the already-degraded path).
const (
	busSubscribeAttempts = 3
	busSubscribeBackoff  = 150 * time.Millisecond
)

// retrySubscribe runs fn up to attempts times, waiting backoff BETWEEN tries (never after the
// final attempt or after a success), returning nil on the first success and the LAST error once
// the attempts are exhausted. The backoff wait is interruptible: a cancelled ctx returns
// promptly (surfacing the last subscribe error) instead of sleeping the remaining backoff.
func retrySubscribe(ctx context.Context, attempts int, backoff time.Duration, fn func() error) error {
	if attempts < 1 {
		attempts = 1
	}
	var err error
	for i := 0; i < attempts; i++ {
		if err = fn(); err == nil {
			return nil
		}
		if i == attempts-1 {
			break // no sleep after the final attempt
		}
		select {
		case <-ctx.Done():
			return err // cancelled mid-backoff: stop early, surface the real subscribe error
		case <-time.After(backoff):
		}
	}
	return err
}

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
	// Keep the PRIVATE registry value + its index alive on the same heartbeat (no-ops on a
	// public node, whose preg/pregset keys do not exist): private band nodes re-register
	// rarely, so without this their mirrored reg would expire after livenessTTL and a peer
	// could no longer resolve/route a still-live band. Mirrors the public reg/regset handling.
	pipe.PExpire(ctx, pregKey(node), livenessTTL)
	pipe.PExpire(ctx, pregsetKey, livenessTTL)
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

// toolsKey is the single shared hash of VERIFIED tool-call bits: field = node+"\x00"+model,
// value = the last-verified UnixMilli. One hash keeps the read a single HGETALL round-trip
// (like liveness) and per-field HDEL gives the authoritative host a precise cross-instance clear.
func toolsKey() string { return keyPrefix + "toolsok" }

func (v *valkeyStore) markToolsVerified(node, model string, ttl time.Duration) error {
	if v == nil || v.rdb == nil {
		return errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	pipe := v.rdb.Pipeline()
	pipe.HSet(ctx, toolsKey(), node+"\x00"+model, time.Now().UnixMilli())
	pipe.PExpire(ctx, toolsKey(), ttl) // refresh the hash TTL on every mark (like liveness)
	if _, err := pipe.Exec(ctx); err != nil {
		v.noteErr("markToolsVerified", err)
		return err
	}
	v.setUp(true)
	return nil
}

func (v *valkeyStore) clearToolsVerified(node, model string) error {
	if v == nil || v.rdb == nil {
		return errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	if err := v.rdb.HDel(ctx, toolsKey(), node+"\x00"+model).Err(); err != nil && err != redis.Nil {
		v.noteErr("clearToolsVerified", err)
		return err
	}
	v.setUp(true)
	return nil
}

func (v *valkeyStore) toolsVerified(ttl time.Duration) (map[string]bool, error) {
	if v == nil || v.rdb == nil {
		return nil, errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	fields, err := v.rdb.HGetAll(ctx, toolsKey()).Result()
	if err != nil {
		v.noteErr("toolsVerified", err)
		return nil, err
	}
	out := make(map[string]bool, len(fields))
	cutoff := time.Now().Add(-ttl).UnixMilli()
	var stale []string
	for field, val := range fields {
		ms, perr := strconv.ParseInt(val, 10, 64)
		if perr != nil || ms < cutoff {
			stale = append(stale, field) // unparseable or STALE: treat as undetermined AND sweep
			continue
		}
		out[field] = true
	}
	// Lazily sweep stale/unparseable fields so a dead node's field cannot accumulate forever (one
	// actively-verified model refreshes the whole hash TTL, so stale fields never expire on their
	// own). The sweep RE-CHECKS each field's CURRENT value before deleting (sweepStaleToolsFields),
	// so a field that was stale in THIS HGETALL snapshot but re-marked fresh by a concurrent
	// markToolsVerified (a different instance's passing canary) between the read and the delete is
	// SPARED - closing the flicker race (PR #33 review, minor #2). Best-effort: a sweep error does
	// not affect the fresh result already computed.
	if len(stale) > 0 {
		_ = v.sweepStaleToolsFields(ctx, cutoff, stale)
	}
	v.setUp(true)
	return out, nil
}

// sweepStaleToolsSrc is the atomic re-check-then-delete the toolsVerified sweep runs: for each
// candidate field it re-reads the CURRENT value and deletes ONLY if the field is still absent-of-
// freshness (unparseable or older than cutoff). Doing the freshness check and the HDEL in one
// server-side script closes the read-then-blind-HDEL race: a field re-marked fresh AFTER the
// caller's HGETALL snapshot but BEFORE this runs now carries a value >= cutoff and is left intact.
// Safe-direction: the worst case is UNDER-claiming (a field that goes stale between check and
// delete simply survives one more cycle), never dropping a fresh verified bit.
const sweepStaleToolsSrc = `
local cutoff = tonumber(ARGV[1])
local deleted = 0
for i = 2, #ARGV do
  local f = ARGV[i]
  local v = redis.call('HGET', KEYS[1], f)
  if v then
    local ms = tonumber(v)
    if ms == nil or ms < cutoff then
      redis.call('HDEL', KEYS[1], f)
      deleted = deleted + 1
    end
  end
end
return deleted
`

// sweepStaleToolsFields atomically deletes the given candidate fields that are STILL stale (or
// unparseable) at execution time, re-checking freshness against cutoff (a UnixMilli) inside the
// script so a concurrently re-marked field survives. Best-effort; the caller ignores the error.
func (v *valkeyStore) sweepStaleToolsFields(ctx context.Context, cutoff int64, fields []string) error {
	if v == nil || v.rdb == nil || len(fields) == 0 {
		return nil
	}
	args := make([]any, 0, len(fields)+1)
	args = append(args, cutoff)
	for _, f := range fields {
		args = append(args, f)
	}
	return v.rdb.Eval(ctx, sweepStaleToolsSrc, []string{toolsKey()}, args...).Err()
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

// pregKey holds a PRIVATE band node's full registration JSON; pregsetKey is its index set.
// Both live under a SEPARATE namespace from the public regKey/regset, so a private node is
// mirrored for cross-instance routing yet can NEVER surface in the public allNodes() the
// /discover mirror enumerates. Same shape as the public pair; only the prefix differs.
func pregKey(node string) string { return keyPrefix + "preg:" + node }

const pregsetKey = keyPrefix + "pregset"

func (v *valkeyStore) putPrivateNode(id string, reg []byte, ttl time.Duration) error {
	if v == nil || v.rdb == nil {
		return errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	pipe := v.rdb.Pipeline()
	pipe.Set(ctx, pregKey(id), reg, ttl)
	pipe.SAdd(ctx, pregsetKey, id)
	pipe.PExpire(ctx, pregsetKey, ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		v.noteErr("putPrivateNode", err)
		return err
	}
	v.setUp(true)
	return nil
}

func (v *valkeyStore) getPrivateNode(id string) ([]byte, bool, error) {
	if v == nil || v.rdb == nil {
		return nil, false, errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	raw, err := v.rdb.Get(ctx, pregKey(id)).Bytes()
	if err == redis.Nil {
		return nil, false, nil // no such private node in the shared registry
	}
	if err != nil {
		v.noteErr("getPrivateNode", err)
		return nil, false, err
	}
	v.setUp(true)
	return raw, true, nil
}

func (v *valkeyStore) allPrivateNodes() (map[string][]byte, error) {
	if v == nil || v.rdb == nil {
		return nil, errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	ids, err := v.rdb.SMembers(ctx, pregsetKey).Result()
	if err != nil {
		v.noteErr("allPrivateNodes", err)
		return nil, err
	}
	if len(ids) == 0 {
		v.setUp(true)
		return map[string][]byte{}, nil
	}
	pipe := v.rdb.Pipeline()
	cmds := make([]*redis.StringCmd, len(ids))
	for i, id := range ids {
		cmds[i] = pipe.Get(ctx, pregKey(id))
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		v.noteErr("allPrivateNodes", err)
		return nil, err
	}
	out := make(map[string][]byte, len(ids))
	for i, id := range ids {
		raw, err := cmds[i].Bytes()
		if err == redis.Nil {
			continue // reg expired since the set listing - skip
		}
		if err != nil {
			v.noteErr("allPrivateNodes", err)
			return out, err
		}
		out[id] = raw
	}
	v.setUp(true)
	return out, nil
}

// dropSharedNode removes a node from BOTH the public (reg/regset) and private (preg/pregset)
// shared registries in one pipeline. Called by register before re-publishing so a private<->
// public flip never leaves a stale mirror in the other namespace.
func (v *valkeyStore) dropSharedNode(id string) error {
	if v == nil || v.rdb == nil {
		return errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	pipe := v.rdb.Pipeline()
	pipe.Del(ctx, regKey(id))
	pipe.SRem(ctx, keyPrefix+"regset", id)
	pipe.Del(ctx, pregKey(id))
	pipe.SRem(ctx, pregsetKey, id)
	if _, err := pipe.Exec(ctx); err != nil {
		v.noteErr("dropSharedNode", err)
		return err
	}
	v.setUp(true)
	return nil
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

// --- instance presence: the live broker-fleet heartbeat (ops topology). ---
//
// Each instance write-throughs its OWN presence under a per-instance key with instanceTTL and
// tracks its id in a prefixed set, so any instance (and the admin ops panel) can count the live
// fleet without an un-prefixed SCAN over the SHARED keyspace. Modeled on the liveness/inflight
// write-through: forward-only freshness via a TTL, so a crashed instance ages out of the count.
func instanceKey(id string) string { return keyPrefix + "inst:" + id }

const instancesSetKey = keyPrefix + "instances"

// instanceTTL bounds how long an instance's presence survives without a refresh. Generous
// relative to the 5s sync tick so a brief GC pause never drops a live instance, while a truly
// dead instance ages out within a minute (the panel then shows the reduced fleet).
const instanceTTL = 60 * time.Second

func (v *valkeyStore) markInstance(instanceID string, now time.Time) error {
	if v == nil || v.rdb == nil {
		return errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	pipe := v.rdb.Pipeline()
	pipe.Set(ctx, instanceKey(instanceID), now.UnixMilli(), instanceTTL)
	pipe.SAdd(ctx, instancesSetKey, instanceID)
	pipe.PExpire(ctx, instancesSetKey, instanceTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		v.noteErr("markInstance", err)
		return err
	}
	v.setUp(true)
	return nil
}

func (v *valkeyStore) liveInstances() (int, error) {
	if v == nil || v.rdb == nil {
		return 0, errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	ids, err := v.rdb.SMembers(ctx, instancesSetKey).Result()
	if err != nil {
		v.noteErr("liveInstances", err)
		return 0, err
	}
	if len(ids) == 0 {
		v.setUp(true)
		return 0, nil
	}
	// Batch the per-instance EXISTS into ONE pipeline: an id still in the set whose presence
	// key has expired is a dead instance - count only the ids whose key is still live.
	pipe := v.rdb.Pipeline()
	cmds := make([]*redis.IntCmd, len(ids))
	for i, id := range ids {
		cmds[i] = pipe.Exists(ctx, instanceKey(id))
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		v.noteErr("liveInstances", err)
		return 0, err
	}
	live := 0
	var stale []string
	for i := range ids {
		if n, err := cmds[i].Result(); err == nil && n > 0 {
			live++
		} else if err == nil {
			// EXISTS returned 0: the presence key aged out. Prune the dead id so the set does
			// not accumulate a new random instance id on every restart/deploy (the whole set
			// only wholesale-expires once EVERY instance stops marking). The count above is
			// already correct; this just keeps SMembers bounded to the live fleet.
			stale = append(stale, ids[i])
		}
	}
	if len(stale) > 0 {
		members := make([]any, len(stale))
		for i, id := range stale {
			members[i] = id
		}
		_ = v.rdb.SRem(ctx, instancesSetKey, members...).Err() // best-effort; count is unaffected
	}
	v.setUp(true)
	return live, nil
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

// capsuleKeyPrefix namespaces the content-blind capsule blobs under the shared keyspace so
// they never collide with another project or with the rl:/node:/cache: keys. Every capsule
// key is rogerai:cap:<lookup>. The value is opaque ciphertext; the broker never reads it.
const capsuleKeyPrefix = keyPrefix + "cap:"

// putCapsule SETs the opaque blob under the lookup with a TTL (atomic set+expire), so an
// expired blob can never outlive its window. ttl<=0 is a no-op. Content-blind: only the
// lookup + ciphertext are written.
func (v *valkeyStore) putCapsule(lookup string, blob []byte, ttl time.Duration) error {
	if v == nil || v.rdb == nil {
		return errNoSharedStore
	}
	if ttl <= 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	if err := v.rdb.Set(ctx, capsuleKeyPrefix+lookup, blob, ttl).Err(); err != nil {
		v.noteErr("putCapsule", err)
		return err
	}
	v.setUp(true)
	return nil
}

// takeCapsule GETDELs the blob under the lookup: a single atomic get-and-delete, so exactly
// one of N concurrent resolves (across all instances) gets the bytes and every later resolve
// is a clean miss (delete-on-read, one-time). A miss (absent/expired) returns found=false
// with a nil error so the handler returns the uniform 404 without logging a backend error.
func (v *valkeyStore) takeCapsule(lookup string) ([]byte, bool, error) {
	if v == nil || v.rdb == nil {
		return nil, false, errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	raw, err := v.rdb.GetDel(ctx, capsuleKeyPrefix+lookup).Bytes()
	if err == redis.Nil {
		v.setUp(true)
		return nil, false, nil // clean miss / expired / already consumed
	}
	if err != nil {
		v.noteErr("takeCapsule", err)
		return nil, false, err
	}
	v.setUp(true)
	return raw, true, nil
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
	// rogerai:bus:claim:<jobID> - the single-delivery claim key: the first poller to SET NX it
	// wins the job; every other poller (the fan-out duplicates) re-polls. Keyed on the unique
	// per-request job id, so it never collides with a future job.
	busClaimPrefix = keyPrefix + "bus:claim:"
	// BASE STATION / remote control (v5.0.0), keyed on the SESSION id:
	//	rogerai:bus:rc:in:<sid>   - viewer -> host inbounds (the host's poll subscribes)
	//	rogerai:bus:rc:out:<sid>  - host -> viewer frames (every viewer's stream subscribes)
	//	rogerai:rc:seq:<sid>      - a shared INCR seq so viewers on any instance order alike
	busRCInPrefix  = keyPrefix + "bus:rc:in:"
	busRCOutPrefix = keyPrefix + "bus:rc:out:"
	rcSeqPrefix    = keyPrefix + "rc:seq:"
	// rogerai:bus:rc:inbuf:<sid> - a short-TTL LIST that retains a viewer inbound published
	// while the host was BETWEEN polls (a PUBLISH to 0 subscribers, which pub/sub drops). The
	// host's next poll drains it one-per-poll, so a turn/confirm sent in the poll gap is not lost.
	busRCInBufPrefix = keyPrefix + "bus:rc:inbuf:"
)

// rcSeqTTL keeps the shared per-session seq counter alive as long as a session could plausibly
// be active; it ages out with the idle-GC window so a long-dead session's key never lingers.
const rcSeqTTL = 7 * 24 * time.Hour

// rcInboundBufTTL bounds how long a gap-buffered viewer inbound is retained: it only has to
// outlive the host's re-poll / brief reconnect, then age out so a dead session leaves nothing.
const rcInboundBufTTL = 2 * time.Minute

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

// busClaimTTL bounds how long a single-delivery claim lives. It only has to outlive the brief
// window in which a node's pollers race to claim the same fan-out job (they all receive the
// PUBLISH within milliseconds), but we set it comfortably past the longest serve window (the
// 300s stream cap) so a claim can never lapse while its job is still in flight, then auto-expire
// to reclaim the key. Job ids are unique per request, so a lingering claim never blocks a new job.
const busClaimTTL = 5 * time.Minute

func (v *valkeyStore) busClaimJob(jobID string) (bool, error) {
	if v == nil || v.rdb == nil {
		return false, errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	won, err := v.rdb.SetNX(ctx, busClaimPrefix+jobID, "1", busClaimTTL).Result()
	if err != nil {
		v.noteErr("busClaimJob", err)
		return false, err
	}
	v.setUp(true)
	return won, nil
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
	// Establish + confirm the subscription, retrying a transient failure (e.g. a DO public-DNS
	// i/o timeout on the re-subscribe) before giving up: Receive blocks for the subscribe
	// confirmation so a Publish racing the Subscribe is not missed, bounded by sharedOpTimeout
	// so a hung backend cannot stall the caller. Each attempt re-creates the PubSub (the prior
	// one is closed on failure), so on success `ps` is the live, confirmed subscription.
	// Retry only while the bus is BELIEVED healthy: an isolated blip on an otherwise-live bus
	// is worth absorbing, but once the store is already marked down (a sustained outage) the
	// full retry would just add latency to EVERY cross-instance request before the inevitable
	// in-memory fallback - so fail fast (attempts=1). The first success flips healthy() back on,
	// so recovery costs one attempt, not zero retry-budget.
	attempts := busSubscribeAttempts
	if !v.healthy() {
		attempts = 1
	}
	var ps *redis.PubSub
	err := retrySubscribe(subCtx, attempts, busSubscribeBackoff, func() error {
		ps = v.rdb.Subscribe(subCtx, channel)
		recvCtx, recvCancel := context.WithTimeout(subCtx, sharedOpTimeout)
		_, e := ps.Receive(recvCtx)
		recvCancel()
		if e != nil {
			_ = ps.Close()
			ps = nil
		}
		return e
	})
	if err != nil {
		cancel()
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

// --- BASE STATION / remote control pub-sub (v5.0.0) ---

func (v *valkeyStore) busPublishRCIn(sid string, in []byte) error {
	if v == nil || v.rdb == nil {
		return errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), busPublishTimeout)
	defer cancel()
	n, err := v.rdb.Publish(ctx, busRCInPrefix+sid, in).Result()
	if err != nil {
		v.noteErr("busPublishRCIn", err)
		return err
	}
	if n == 0 {
		// Nobody heard it: the host is between polls (its subscription is torn down after each
		// long-poll). Retain the inbound on a short-TTL list so the next poll can drain it,
		// instead of dropping it as pub/sub does (audit #5). The single-instance h.in buffer
		// (cap 64) already covers the non-bus path.
		key := busRCInBufPrefix + sid
		if perr := v.rdb.RPush(ctx, key, in).Err(); perr != nil {
			v.noteErr("busPublishRCIn", perr)
			return perr
		}
		v.rdb.Expire(ctx, key, rcInboundBufTTL)
	}
	v.setUp(true)
	return nil
}

func (v *valkeyStore) busPopRCIn(sid string) ([]byte, bool, error) {
	if v == nil || v.rdb == nil {
		return nil, false, errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	raw, err := v.rdb.LPop(ctx, busRCInBufPrefix+sid).Bytes()
	if err == redis.Nil {
		return nil, false, nil // empty buffer (the common steady-state case)
	}
	if err != nil {
		v.noteErr("busPopRCIn", err)
		return nil, false, err
	}
	v.setUp(true)
	return raw, true, nil
}
func (v *valkeyStore) busPublishRCOut(sid string, frame []byte) error {
	return v.busPublishTo(busRCOutPrefix+sid, frame, "busPublishRCOut")
}
func (v *valkeyStore) busSubscribeRCIn(ctx context.Context, sid string) (<-chan []byte, func(), error) {
	return v.busSubscribe(ctx, busRCInPrefix+sid)
}
func (v *valkeyStore) busSubscribeRCOut(ctx context.Context, sid string) (<-chan []byte, func(), error) {
	return v.busSubscribe(ctx, busRCOutPrefix+sid)
}

// busPublishTo is the shared one-shot PUBLISH used by the RC channels.
func (v *valkeyStore) busPublishTo(channel string, payload []byte, op string) error {
	if v == nil || v.rdb == nil {
		return errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), busPublishTimeout)
	defer cancel()
	if err := v.rdb.Publish(ctx, channel, payload).Err(); err != nil {
		v.noteErr(op, err)
		return err
	}
	v.setUp(true)
	return nil
}

// busNextRCSeq atomically increments a per-session seq (TTL'd so an ended session's key ages
// out). A failure returns 0,err and the caller falls back to a local seq — a reconnect-replay
// gap at worst, never a lost frame.
func (v *valkeyStore) busNextRCSeq(sid string) (uint64, error) {
	if v == nil || v.rdb == nil {
		return 0, errNoSharedStore
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedOpTimeout)
	defer cancel()
	key := rcSeqPrefix + sid
	n, err := v.rdb.Incr(ctx, key).Result()
	if err != nil {
		v.noteErr("busNextRCSeq", err)
		return 0, err
	}
	v.rdb.Expire(ctx, key, rcSeqTTL)
	v.setUp(true)
	return uint64(n), nil
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
// localCacheEntry is one in-process cache slot: the serialized JSON body + its expiry.
type localCacheEntry struct {
	body   []byte
	expiry time.Time
}

// localCacheCap bounds the in-process fallback map so a pathological variety of query/identity
// keys can't grow it unboundedly; past it the map is reset (a coarse but safe eviction - this
// path is the small-scale / single-instance fallback; real scale uses the shared Redis cache).
const localCacheCap = 256

// localCachedJSON is the in-process fallback for serveCachedJSON when no shared (Redis) backend
// is set: it returns the cached JSON bytes for key if still fresh, else computes + marshals +
// stores them under ttl. compute() is run OUTSIDE localCacheMu (it takes b.mu/metricsMu), so a
// rare concurrent miss may double-compute - acceptable for this fallback. nil on a marshal error.
func (b *broker) localCachedJSON(key string, ttl time.Duration, compute func() any) []byte {
	now := time.Now()
	b.localCacheMu.Lock()
	if e, ok := b.localCache[key]; ok && now.Before(e.expiry) {
		body := e.body
		b.localCacheMu.Unlock()
		return body
	}
	b.localCacheMu.Unlock()

	body, err := json.Marshal(compute())
	if err != nil {
		return nil
	}
	b.localCacheMu.Lock()
	if b.localCache == nil || len(b.localCache) > localCacheCap {
		b.localCache = make(map[string]localCacheEntry)
	}
	b.localCache[key] = localCacheEntry{body: body, expiry: now.Add(ttl)}
	b.localCacheMu.Unlock()
	return body
}

func (b *broker) serveCachedJSON(w http.ResponseWriter, key string, ttl time.Duration, compute func() any) {
	// No shared (Redis) backend: still amortize via the IN-PROCESS TTL cache so a single
	// instance doesn't recompute the full market on every hit. Safe - same key scoping as the
	// shared path. On a marshal error, fall back to the direct encoder so the request still serves.
	if b.shared == nil {
		if body := b.localCachedJSON(key, ttl, compute); body != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body)
			return
		}
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
