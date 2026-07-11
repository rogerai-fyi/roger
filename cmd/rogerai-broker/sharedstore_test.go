package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/rogerai-fyi/roger/internal/store"
)

// newTestValkey spins up an in-process miniredis (Redis-protocol) server and returns
// a valkeyStore wired to it, so the flag-ON path is exercised WITHOUT a live server
// in CI. miniredis speaks the same protocol go-redis uses (incl. EVAL for the token
// bucket script), so this is a true interface-level test of valkeyStore.
func newTestValkey(t *testing.T) (*valkeyStore, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	vs, err := newValkeyStore("redis://" + mr.Addr())
	if err != nil {
		t.Fatalf("newValkeyStore: %v", err)
	}
	t.Cleanup(func() { _ = vs.Close() })
	return vs, mr
}

// TestSharedStoreInterface asserts both impls satisfy the sharedStore interface so a
// call site can swap them. This is the contract test (compile + minimal behavior).
func TestSharedStoreInterface(t *testing.T) {
	var _ sharedStore = newMemStore()
	var _ sharedStore = (*valkeyStore)(nil)

	// memStore is intentionally inert: every method signals "use in-memory" via a
	// non-nil error, so call sites fall back. healthy() is false (no backend).
	m := newMemStore()
	if _, _, err := m.rateAllow("k", 60, 3, time.Now()); err == nil {
		t.Error("memStore.rateAllow should return a non-nil error so the caller falls back")
	}
	if err := m.markSeen("n", time.Now()); err == nil {
		t.Error("memStore.markSeen should return a non-nil error")
	}
	if _, err := m.liveness(); err == nil {
		t.Error("memStore.liveness should return a non-nil error")
	}
	if _, found, err := m.cacheGet("k"); found || err == nil {
		t.Error("memStore.cacheGet must be a miss with a non-nil error so the caller computes")
	}
	if err := m.cacheSet("k", []byte("v"), time.Second); err == nil {
		t.Error("memStore.cacheSet is a no-op and should report unavailable")
	}
	if err := m.putCapsule("lk", []byte("blob"), time.Minute); err == nil {
		t.Error("memStore.putCapsule should report unavailable so the caller uses the local map")
	}
	if _, found, err := m.takeCapsule("lk"); found || err == nil {
		t.Error("memStore.takeCapsule must be a miss with a non-nil error")
	}
	if m.healthy() {
		t.Error("memStore is never healthy (no backend)")
	}
	if err := m.Close(); err != nil {
		t.Errorf("memStore.Close: %v", err)
	}
}

// TestValkeyCapsuleRoundTrip exercises the content-blind capsule rendezvous end-to-end on a
// live backend: put stores the opaque blob namespaced under rogerai:cap:, takeCapsule GETDELs
// it exactly once (one-time), a miss is (nil,false,nil), and the TTL expires the blob.
func TestValkeyCapsuleRoundTrip(t *testing.T) {
	vs, mr := newTestValkey(t)

	// miss -> (nil,false,nil): a clean miss, no logged backend error.
	if blob, found, err := vs.takeCapsule("absent"); found || err != nil || blob != nil {
		t.Fatalf("miss = (%v,%v,%v), want (nil,false,nil)", blob, found, err)
	}
	// put then take returns the exact ciphertext ONCE.
	want := []byte("opaque-ciphertext")
	if err := vs.putCapsule("lk1", want, time.Minute); err != nil {
		t.Fatalf("putCapsule: %v", err)
	}
	for _, k := range mr.Keys() { // shared-instance namespacing
		if !strings.HasPrefix(k, capsuleKeyPrefix) {
			t.Errorf("capsule key %q is NOT under %q", k, capsuleKeyPrefix)
		}
	}
	got, found, err := vs.takeCapsule("lk1")
	if err != nil || !found || string(got) != string(want) {
		t.Fatalf("take = (%q,%v,%v), want (%q,true,nil)", got, found, err, want)
	}
	// one-time: a second take is a miss (delete-on-read).
	if _, found, _ := vs.takeCapsule("lk1"); found {
		t.Error("second takeCapsule must miss (one-time, delete-on-read)")
	}
	// ttl<=0 is a no-op.
	if err := vs.putCapsule("noop", []byte("x"), 0); err != nil {
		t.Errorf("putCapsule ttl<=0 should be a no-op nil, got %v", err)
	}
	if _, found, _ := vs.takeCapsule("noop"); found {
		t.Error("putCapsule ttl<=0 must not write a key")
	}
	// TTL expiry -> miss.
	if err := vs.putCapsule("lk2", want, 2*time.Second); err != nil {
		t.Fatalf("putCapsule: %v", err)
	}
	mr.FastForward(3 * time.Second)
	if _, found, _ := vs.takeCapsule("lk2"); found {
		t.Error("capsule blob should have expired after its TTL")
	}
}

// TestValkeyCacheGetSet exercises the cache primitive: a miss returns found=false with
// a nil error (so the caller recomputes WITHOUT a logged backend error), a set+get
// round-trips the exact bytes, every key is namespaced under rogerai:cache:, and the
// entry expires after its TTL.
func TestValkeyCacheGetSet(t *testing.T) {
	vs, mr := newTestValkey(t)

	// Clean miss: found=false, err=nil.
	if val, found, err := vs.cacheGet("disco"); found || err != nil || val != nil {
		t.Fatalf("miss = (val=%v found=%v err=%v), want (nil,false,nil)", val, found, err)
	}

	// Set then get returns the exact bytes.
	want := []byte(`{"offers":[]}`)
	if err := vs.cacheSet("disco", want, 3*time.Second); err != nil {
		t.Fatalf("cacheSet: %v", err)
	}
	got, found, err := vs.cacheGet("disco")
	if err != nil || !found || string(got) != string(want) {
		t.Fatalf("hit = (val=%q found=%v err=%v), want (%q,true,nil)", got, found, err, want)
	}

	// Every cache key is namespaced (shared-instance safety).
	for _, k := range mr.Keys() {
		if !strings.HasPrefix(k, cacheKeyPrefix) {
			t.Errorf("cache key %q is NOT under %q - would collide on the shared instance", k, cacheKeyPrefix)
		}
	}

	// TTL: advancing past the window expires the entry -> back to a miss.
	mr.FastForward(4 * time.Second)
	if _, found, _ := vs.cacheGet("disco"); found {
		t.Error("cache entry should have expired after its TTL")
	}

	// ttl<=0 is a no-op (no key written, no error).
	if err := vs.cacheSet("noop", []byte("x"), 0); err != nil {
		t.Errorf("cacheSet ttl=0 should be a no-op nil, got %v", err)
	}
	if _, found, _ := vs.cacheGet("noop"); found {
		t.Error("cacheSet with ttl<=0 must not write a key")
	}
}

// TestValkeyCacheDegradesOnClose proves a dead backend surfaces cacheGet/cacheSet
// errors (so serveCachedJSON treats it as a miss and computes directly) instead of
// panicking - a cache failure never fails a request.
func TestValkeyCacheDegradesOnClose(t *testing.T) {
	vs, mr := newTestValkey(t)
	mr.Close()
	if _, _, err := vs.cacheGet("k"); err == nil {
		t.Error("cacheGet on a dead backend should error so the caller computes")
	}
	if err := vs.cacheSet("k", []byte("v"), time.Second); err == nil {
		t.Error("cacheSet on a dead backend should error (non-fatal to the caller)")
	}
}

// TestServeCachedJSONFlagOff proves the flag-OFF invariant: with b.shared == nil,
// serveCachedJSON computes + serves DIRECTLY every call (no caching), byte-for-byte
// today's behavior.
// TestServeCachedJSONFlagOff: with NO shared (Redis) backend, serveCachedJSON now amortizes via
// the IN-PROCESS TTL cache (P3) - three calls within the TTL collapse to ONE compute and serve
// the identical body. (Before P3 flag-off recomputed every call; the local fallback gives a
// single-instance / no-Redis deploy the same hot-path amortization as the shared cache path,
// keyed by the same scoped key so it's leak-safe.)
func TestServeCachedJSONFlagOff(t *testing.T) {
	b := &broker{} // shared == nil
	calls := 0
	var bodies []string
	h := func(w http.ResponseWriter, r *http.Request) {
		b.serveCachedJSON(w, "k", 3*time.Second, func() any {
			calls++
			return map[string]any{"n": calls}
		})
	}
	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest(http.MethodGet, "/x", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("code = %d", w.Code)
		}
		bodies = append(bodies, w.Body.String())
	}
	if calls != 1 {
		t.Errorf("flag-off now caches in-process: want 1 compute within the TTL, got %d", calls)
	}
	if bodies[0] != bodies[2] {
		t.Errorf("flag-off responses within the TTL must be the identical cached body:\n%q\n%q", bodies[0], bodies[2])
	}
}

// TestServeCachedJSONHitMiss proves the flag-ON behavior: the first call MISSES
// (computes + populates), subsequent calls within the TTL HIT (serve cached bytes,
// skip compute), and after the TTL it recomputes.
func TestServeCachedJSONHitMiss(t *testing.T) {
	vs, mr := newTestValkey(t)
	b := &broker{shared: vs}
	calls := 0
	serve := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		b.serveCachedJSON(w, "k", 5*time.Second, func() any {
			calls++
			return map[string]any{"n": calls}
		})
		return w
	}

	w1 := serve() // miss -> compute #1 + populate
	if calls != 1 {
		t.Fatalf("first call should compute once, calls=%d", calls)
	}
	w2 := serve() // hit -> no compute
	if calls != 1 {
		t.Errorf("second call within TTL should HIT (no recompute), calls=%d", calls)
	}
	if w1.Body.String() != w2.Body.String() {
		t.Errorf("hit body %q != miss body %q", w2.Body.String(), w1.Body.String())
	}
	if ct := w2.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("cached hit Content-Type = %q, want application/json", ct)
	}

	// After the TTL the entry is gone -> recompute.
	mr.FastForward(7 * time.Second)
	_ = serve()
	if calls != 2 {
		t.Errorf("after TTL expiry the value should recompute, calls=%d", calls)
	}
}

// TestServeCachedJSONPerKeyIsolation is the cross-user isolation guarantee at the
// helper level: two DIFFERENT keys (e.g. two authed identities) never share an entry,
// so one account's cached payload is never served to another.
func TestServeCachedJSONPerKeyIsolation(t *testing.T) {
	vs, _ := newTestValkey(t)
	b := &broker{shared: vs}
	serve := func(key, who string) string {
		w := httptest.NewRecorder()
		b.serveCachedJSON(w, key, 30*time.Second, func() any {
			return map[string]any{"secret_owner": who}
		})
		return w.Body.String()
	}
	a := serve("series:w=userA|o=", "userA")  // populate A
	bb := serve("series:w=userB|o=", "userB") // different key -> userB's own data
	if strings.Contains(bb, "userA") {
		t.Fatalf("user B response %q leaked user A's cached data", bb)
	}
	// A re-fetch of A's key still returns A's data (and never B's).
	if a2 := serve("series:w=userA|o=", "userA-DIFFERENT-IGNORED"); a2 != a {
		t.Errorf("user A key should keep returning A's cached payload, got %q want %q", a2, a)
	}
}

// TestServeCachedJSONCacheErrorDegrades proves a Valkey error degrades to a direct
// compute: with a dead backend, every call computes (the cache GET errors -> miss,
// the SET errors -> non-fatal) and the request still succeeds.
func TestServeCachedJSONCacheErrorDegrades(t *testing.T) {
	vs, mr := newTestValkey(t)
	mr.Close() // backend is down
	b := &broker{shared: vs}
	calls := 0
	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		b.serveCachedJSON(w, "k", 5*time.Second, func() any {
			calls++
			return map[string]any{"ok": true}
		})
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "ok") {
			t.Fatalf("a cache error must still serve a computed 200, got %d %q", w.Code, w.Body.String())
		}
	}
	if calls != 3 {
		t.Errorf("with a dead cache every call must compute directly, computed %d/3", calls)
	}
}

// TestIdentityCacheKeyDistinct locks the key scheme that gives the per-user isolation:
// every distinct authenticated identity (consumer-only, provider-only, both, and a
// different identity) maps to a DISTINCT key, so no two accounts can collide.
func TestIdentityCacheKeyDistinct(t *testing.T) {
	keys := map[string]string{
		"consumerA":  identityCacheKey("series", "walletA", true, "", false),
		"consumerB":  identityCacheKey("series", "walletB", true, "", false),
		"providerA":  identityCacheKey("series", "", false, "pkA", true),
		"providerB":  identityCacheKey("series", "", false, "pkB", true),
		"bothA":      identityCacheKey("series", "walletA", true, "pkA", true),
		"console_cA": identityCacheKey("console", "walletA", true, "", false),
	}
	seen := map[string]string{}
	for name, k := range keys {
		if other, dup := seen[k]; dup {
			t.Errorf("identities %q and %q collide on cache key %q", name, other, k)
		}
		seen[k] = name
	}
	// A pure consumer's key must NOT embed an unauthenticated owner pubkey: even if a
	// stray pubkey is passed, provider=false zeroes it out (no cross-side leakage).
	if got := identityCacheKey("series", "walletA", true, "pk-NOT-AUTHED", false); strings.Contains(got, "pk-NOT-AUTHED") {
		t.Errorf("an unauthenticated pubkey must not enter the key: %q", got)
	}
}

// TestValkeyRateAllow checks the shared token bucket: the burst is allowed back to
// back, then the next is denied with a positive retry hint - mirroring the in-memory
// rateLimiter semantics so the shared decision matches the local one.
func TestValkeyRateAllow(t *testing.T) {
	vs, _ := newTestValkey(t)
	now := time.Now()
	// burst 3 allowed back to back
	for i := 0; i < 3; i++ {
		ok, _, err := vs.rateAllow("ip-1", 60, 3, now)
		if err != nil {
			t.Fatalf("rateAllow err: %v", err)
		}
		if !ok {
			t.Fatalf("burst token %d should be allowed", i)
		}
	}
	// over-burst denied with retry>=1
	ok, retry, err := vs.rateAllow("ip-1", 60, 3, now)
	if err != nil {
		t.Fatalf("rateAllow err: %v", err)
	}
	if ok || retry < 1 {
		t.Errorf("over-burst should deny with retry>=1, got ok=%v retry=%d", ok, retry)
	}
	// a different key has its own bucket
	if ok, _, _ := vs.rateAllow("ip-2", 60, 3, now); !ok {
		t.Error("a different key should be allowed")
	}
	// refill: advancing time past one token's worth re-allows
	later := now.Add(2 * time.Second) // 60rpm => 1 tok/sec, 2s => ~2 tokens
	if ok, _, _ := vs.rateAllow("ip-1", 60, 3, later); !ok {
		t.Error("after refill the bucket should allow again")
	}
}

// TestValkeyRateAllowKeyPrefix proves every rate-limit key is namespaced under the
// rogerai: prefix so it can never collide with another project on the shared Valkey
// instance.
func TestValkeyRateAllowKeyPrefix(t *testing.T) {
	vs, mr := newTestValkey(t)
	if _, _, err := vs.rateAllow("abc", 60, 3, time.Now()); err != nil {
		t.Fatalf("rateAllow: %v", err)
	}
	found := false
	for _, k := range mr.Keys() {
		if !strings.HasPrefix(k, keyPrefix) {
			t.Errorf("key %q is NOT namespaced under %q - would collide on the shared instance", k, keyPrefix)
		}
		if k == keyPrefix+"rl:abc" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the bucket key %q, got %v", keyPrefix+"rl:abc", mr.Keys())
	}
}

// TestValkeyLiveness checks the cross-instance liveness path: markSeen records a
// node's timestamp and liveness() reads it back, all namespaced.
func TestValkeyLiveness(t *testing.T) {
	vs, mr := newTestValkey(t)
	now := time.Now().Truncate(time.Millisecond)
	if err := vs.markSeen("node-a", now); err != nil {
		t.Fatalf("markSeen: %v", err)
	}
	if err := vs.markSeen("node-b", now.Add(time.Second)); err != nil {
		t.Fatalf("markSeen: %v", err)
	}
	snap, err := vs.liveness()
	if err != nil {
		t.Fatalf("liveness: %v", err)
	}
	if len(snap) != 2 {
		t.Fatalf("want 2 nodes in liveness snapshot, got %d (%v)", len(snap), snap)
	}
	if got := snap["node-a"]; !got.Equal(now) {
		t.Errorf("node-a last_seen = %v, want %v", got, now)
	}
	// every key namespaced
	for _, k := range mr.Keys() {
		if !strings.HasPrefix(k, keyPrefix) {
			t.Errorf("liveness key %q is NOT namespaced under %q", k, keyPrefix)
		}
	}
}

// TestValkeyGracefulDegradeOnClose proves a backend failure surfaces as an error (so
// the broker call site can fall back to in-memory) rather than panicking. We close
// the underlying server and confirm rateAllow/markSeen/liveness return errors and the
// store reports unhealthy.
func TestValkeyGracefulDegradeOnClose(t *testing.T) {
	vs, mr := newTestValkey(t)
	mr.Close() // simulate the backend going away
	if _, _, err := vs.rateAllow("ip", 60, 3, time.Now()); err == nil {
		t.Error("rateAllow against a dead backend should return an error (caller falls back)")
	}
	if err := vs.markSeen("n", time.Now()); err == nil {
		t.Error("markSeen against a dead backend should return an error")
	}
	if _, err := vs.liveness(); err == nil {
		t.Error("liveness against a dead backend should return an error")
	}
	if vs.healthy() {
		t.Error("store should report unhealthy after backend failures")
	}
}

// TestNewValkeyStoreBadURL proves a bad URL is reported (so openSharedStore logs and
// returns nil -> the broker stays on the in-memory path) and never panics.
func TestNewValkeyStoreBadURL(t *testing.T) {
	if _, err := newValkeyStore("not-a-valid-url"); err == nil {
		t.Error("newValkeyStore should reject an unparseable URL")
	}
	// A well-formed URL pointing nowhere should fail the startup ping but still return
	// a non-nil store (so we can Close it) plus an error.
	vs, err := newValkeyStore("redis://127.0.0.1:1") // nothing listens on :1
	if err == nil {
		t.Error("connecting to a dead address should fail the startup ping")
	}
	if vs != nil {
		_ = vs.Close()
	}
}

// TestOpenSharedStoreUnset proves the flag-OFF default: with ROGERAI_REDIS_URL unset,
// openSharedStore returns nil (the broker uses its in-memory maps, zero change).
func TestOpenSharedStoreUnset(t *testing.T) {
	t.Setenv("ROGERAI_REDIS_URL", "")
	if s := openSharedStore(); s != nil {
		t.Errorf("unset ROGERAI_REDIS_URL must yield a nil shared store, got %T", s)
	}
}

// TestOpenSharedStoreBadURLDegrades proves a SET-but-broken flag degrades gracefully:
// openSharedStore returns nil (logged warning) instead of crashing, so the broker
// boots on the in-memory path.
func TestOpenSharedStoreBadURLDegrades(t *testing.T) {
	t.Setenv("ROGERAI_REDIS_URL", "redis://127.0.0.1:1") // nothing listens
	if s := openSharedStore(); s != nil {
		t.Errorf("a broken ROGERAI_REDIS_URL must degrade to a nil store, got %T", s)
	}
}

// TestOpenSharedStoreConnects proves the flag-ON happy path wires a live valkeyStore.
func TestOpenSharedStoreConnects(t *testing.T) {
	mr := miniredis.RunT(t)
	t.Setenv("ROGERAI_REDIS_URL", "redis://"+mr.Addr())
	s := openSharedStore()
	if s == nil {
		t.Fatal("openSharedStore should connect when ROGERAI_REDIS_URL points at a live server")
	}
	defer s.Close()
	if !s.healthy() {
		t.Error("a freshly-connected store should be healthy")
	}
}

// TestRateLimiterSharedDelegation proves the rateLimiter delegates to the shared
// backend when one is wired in: the limit is enforced via the SHARED bucket, and on a
// shared error the limiter falls back to its local in-memory bucket (so it never
// fails open/closed incorrectly). This is the call-site-level swap test.
func TestRateLimiterSharedDelegation(t *testing.T) {
	vs, _ := newTestValkey(t)
	rl := &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 60, burst: 2, shared: vs}
	// burst 2 from the SHARED bucket
	if ok, _ := rl.allow("k"); !ok {
		t.Fatal("first shared token should be allowed")
	}
	if ok, _ := rl.allow("k"); !ok {
		t.Fatal("second shared token should be allowed")
	}
	if ok, _ := rl.allow("k"); ok {
		t.Fatal("third should be denied by the shared bucket")
	}

	// Fallback: a limiter pointed at a memStore (always-error) must use its LOCAL
	// bucket, behaving exactly like a non-shared limiter.
	rl2 := &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 60, burst: 2, shared: newMemStore()}
	got := 0
	for i := 0; i < 5; i++ {
		if ok, _ := rl2.allow("k"); ok {
			got++
		}
	}
	if got != 2 {
		t.Errorf("with an unavailable shared backend the limiter should fall back to its local burst=2, allowed %d", got)
	}
}

// TestBrokerLivenessCrossInstance proves the end goal: a heartbeat on broker A is
// VISIBLE to broker B through the shared layer. Both brokers share one miniredis.
// markSeen on A write-throughs the timestamp; B's merge (the body of syncLiveness)
// then pulls it into B's in-memory lastSeen map, so B sees A's node as fresh even
// though it never received the heartbeat itself.
func TestBrokerLivenessCrossInstance(t *testing.T) {
	mr := miniredis.RunT(t)
	vsA, err := newValkeyStore("redis://" + mr.Addr())
	if err != nil {
		t.Fatalf("valkey A: %v", err)
	}
	defer vsA.Close()
	vsB, err := newValkeyStore("redis://" + mr.Addr())
	if err != nil {
		t.Fatalf("valkey B: %v", err)
	}
	defer vsB.Close()

	a := newBroker(store.NewMem())
	a.shared = vsA
	a.lastSharedSeen = map[string]time.Time{}
	b := newBroker(store.NewMem())
	b.shared = vsB

	// Heartbeat lands ONLY on broker A.
	a.markSeen("node-x")

	// B has never seen node-x in memory yet.
	b.mu.Lock()
	_, had := b.lastSeen["node-x"]
	b.mu.Unlock()
	if had {
		t.Fatal("precondition: broker B should not know node-x before the merge")
	}

	// Run one iteration of B's liveness merge (same logic as syncLiveness's body).
	snap, err := b.shared.liveness()
	if err != nil {
		t.Fatalf("B liveness: %v", err)
	}
	b.mu.Lock()
	for node, ts := range snap {
		if cur, ok := b.lastSeen[node]; !ok || ts.After(cur) {
			b.lastSeen[node] = ts
		}
	}
	got, ok := b.lastSeen["node-x"]
	b.mu.Unlock()
	if !ok {
		t.Fatal("broker B should see node-x's liveness through the shared layer")
	}
	if time.Since(got) > time.Minute {
		t.Errorf("broker B sees node-x but with a stale timestamp: %v", got)
	}
}

// TestMarkSeenNoSharedUnchanged guards the flag-OFF invariant on the heartbeat path:
// with no shared backend, markSeen only touches in-memory lastSeen (and the DB), and
// never panics on the nil shared store.
func TestMarkSeenNoSharedUnchanged(t *testing.T) {
	b := newBroker(store.NewMem())
	if b.shared != nil {
		t.Fatal("default broker must have a nil shared backend")
	}
	b.markSeen("node-y")
	b.mu.Lock()
	_, ok := b.lastSeen["node-y"]
	b.mu.Unlock()
	if !ok {
		t.Error("markSeen should update in-memory lastSeen even with no shared backend")
	}
}

// TestRateLimiterNoSharedUnchanged guards the flag-OFF invariant at the limiter
// level: a limiter with shared==nil behaves byte-for-byte as before (local bucket).
func TestRateLimiterNoSharedUnchanged(t *testing.T) {
	rl := &rateLimiter{buckets: map[string]*tokenBucket{}, rpm: 60, burst: 3}
	if rl.shared != nil {
		t.Fatal("default limiter must have a nil shared backend")
	}
	got := 0
	for i := 0; i < 5; i++ {
		if ok, _ := rl.allow("u"); ok {
			got++
		}
	}
	if got != 3 {
		t.Errorf("local burst should be 3, allowed %d", got)
	}
}
