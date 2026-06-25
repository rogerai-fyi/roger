package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/rogerai-fyi/roger/internal/protocol"
	"github.com/rogerai-fyi/roger/internal/store"
)

// countingStore wraps a store.Store and counts the calls to the queries the cache
// accelerators are meant to remove, so a test can assert the per-request savings.
type countingStore struct {
	store.Store
	mu            sync.Mutex
	monthSpend    int
	monthlyCap    int
	ownerByPubkey int
	accountOfNode int
	balanceOf     int
}

func (c *countingStore) MonthSpendOf(holder string, now time.Time) (float64, error) {
	c.mu.Lock()
	c.monthSpend++
	c.mu.Unlock()
	return c.Store.MonthSpendOf(holder, now)
}
func (c *countingStore) MonthlyCapOf(holder string) (float64, error) {
	c.mu.Lock()
	c.monthlyCap++
	c.mu.Unlock()
	return c.Store.MonthlyCapOf(holder)
}
func (c *countingStore) OwnerByPubkey(pub string) (store.Owner, bool, error) {
	c.mu.Lock()
	c.ownerByPubkey++
	c.mu.Unlock()
	return c.Store.OwnerByPubkey(pub)
}
func (c *countingStore) AccountOfNode(node string) (string, bool, error) {
	c.mu.Lock()
	c.accountOfNode++
	c.mu.Unlock()
	return c.Store.AccountOfNode(node)
}
func (c *countingStore) BalanceOf(user string, seed float64) (float64, error) {
	c.mu.Lock()
	c.balanceOf++
	c.mu.Unlock()
	return c.Store.BalanceOf(user, seed)
}

func (c *countingStore) counts() (monthSpend, monthlyCap, ownerByPubkey, accountOfNode, balanceOf int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.monthSpend, c.monthlyCap, c.ownerByPubkey, c.accountOfNode, c.balanceOf
}

// testValkeyShared spins an in-process miniredis and returns a wired valkeyStore.
func testValkeyShared(t *testing.T) (*valkeyStore, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	vs, err := newValkeyStore("redis://" + mr.Addr())
	if err != nil {
		t.Fatalf("newValkeyStore: %v", err)
	}
	t.Cleanup(func() { _ = vs.Close() })
	return vs, mr
}

// --- B1: singleflight collapses concurrent misses to one compute -------------

// TestServeCachedJSONSingleflight proves the dogpile fix: many concurrent requests that
// all MISS the same hot key collapse to exactly ONE compute (not one-per-request),
// because serveCachedJSON runs compute under a per-key singleflight. All callers get the
// identical serialized bytes.
func TestServeCachedJSONSingleflight(t *testing.T) {
	vs, _ := testValkeyShared(t)
	b := &broker{shared: vs}

	var computes int
	var mu sync.Mutex
	release := make(chan struct{})
	compute := func() any {
		mu.Lock()
		computes++
		mu.Unlock()
		<-release // hold every in-flight compute open so the herd overlaps on the miss
		return map[string]any{"v": "shared"}
	}

	const n = 24
	bodies := make([]string, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			w := httptest.NewRecorder()
			b.serveCachedJSON(w, "hot", 5*time.Second, compute)
			bodies[i] = w.Body.String()
		}(i)
	}
	// Give the goroutines time to all arrive at the miss + block in compute, then release.
	time.Sleep(150 * time.Millisecond)
	close(release)
	wg.Wait()

	mu.Lock()
	got := computes
	mu.Unlock()
	if got != 1 {
		t.Errorf("singleflight should collapse %d concurrent misses to ONE compute, got %d", n, got)
	}
	for i, body := range bodies {
		if !strings.Contains(body, "shared") {
			t.Errorf("caller %d got %q, want the shared computed body", i, body)
		}
	}
}

// --- B2: authed wrapper refuses anon + isolates per wallet -------------------

// TestServeCachedAuthedRefusesAnon proves the hardened wrapper NEVER caches a response
// for an anon/empty identity: with neither a consumer wallet nor an operator pubkey, it
// computes every call (no cache entry keyed on "") so an unauthenticated payload can
// never be served from an empty-identity key.
func TestServeCachedAuthedRefusesAnon(t *testing.T) {
	vs, _ := testValkeyShared(t)
	b := &broker{shared: vs}
	calls := 0
	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		b.serveCachedAuthedJSON(w, "series", "|d=7", "", false, "", false, 30*time.Second, func() any {
			calls++
			return map[string]any{"anon": true}
		})
		if w.Code != http.StatusOK {
			t.Fatalf("code = %d", w.Code)
		}
	}
	if calls != 3 {
		t.Errorf("anon caller must NEVER be cached (compute every call), computed %d/3", calls)
	}
}

// TestServeCachedAuthedIsolatesPerWallet proves two different authed wallets never share
// a cache entry (the structural cross-identity isolation) AND that a wallet's own entry
// hits on a repeat (caching actually works for an authed identity).
func TestServeCachedAuthedIsolatesPerWallet(t *testing.T) {
	vs, _ := testValkeyShared(t)
	b := &broker{shared: vs}
	serve := func(wallet, who string) (string, *int) {
		calls := 0
		w := httptest.NewRecorder()
		b.serveCachedAuthedJSON(w, "series", "|d=7", wallet, true, "", false, 30*time.Second, func() any {
			calls++
			return map[string]any{"owner": who}
		})
		return w.Body.String(), &calls
	}
	a1, _ := serve("u_gh_1", "alice") // populate alice
	bB, _ := serve("u_gh_2", "bob")   // different wallet -> bob's own data, never alice's
	if strings.Contains(bB, "alice") {
		t.Fatalf("wallet B response %q leaked wallet A's cached data", bB)
	}
	// Alice's key still returns alice's data on a re-fetch (a real cache hit: the second
	// compute body would say "alice-DIFFERENT" but the cached bytes win).
	a2, calls2 := serve("u_gh_1", "alice-DIFFERENT")
	if a2 != a1 {
		t.Errorf("wallet A should keep returning A's cached payload, got %q want %q", a2, a1)
	}
	if *calls2 != 0 {
		t.Errorf("a cached authed hit should not recompute, computed %d", *calls2)
	}
}

// --- W2a: the cap check runs ONE spend SUM + ONE cap read per paid request ----

// TestMonthlyCapCheckSingleQuery proves the W2a refactor: an ALLOWED paid request runs
// exactly ONE MonthlyCapOf and ONE MonthSpendOf (the old code re-queried both in
// monthlyCapState for the headers, doubling them). Flag OFF (no Redis), so monthSpend is
// the direct ledger SUM.
func TestMonthlyCapCheckSingleQuery(t *testing.T) {
	cs := &countingStore{Store: store.NewMem()}
	b := &broker{db: cs}
	holder := "u_gh_7"
	if err := cs.SetMonthlyCap(holder, 100); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	if st, _ := b.monthlyCapCheck(w, holder, 1.0, time.Now()); st != 0 {
		t.Fatalf("request within cap should be allowed, got status %d", st)
	}
	ms, mc, _, _, _ := cs.counts()
	if ms != 1 {
		t.Errorf("an allowed paid request should run MonthSpendOf exactly ONCE, ran %d", ms)
	}
	if mc != 1 {
		t.Errorf("an allowed paid request should run MonthlyCapOf exactly ONCE, ran %d", mc)
	}
	// Headers still reflect the spend/cap (the refactor must not drop the notice).
	if got := w.Header().Get("X-RogerAI-Monthly-Cap"); got == "" {
		t.Error("cap headers should still be emitted on an allowed request")
	}
}

// --- W1: binding cache hit + invalidation -------------------------------------

// TestCachedOwnerWalletHit proves the pubkey->wallet binding is read ONCE from Postgres
// then served from Redis on subsequent calls, and that invalidation forces a re-read.
func TestCachedOwnerWalletHit(t *testing.T) {
	vs, _ := testValkeyShared(t)
	cs := &countingStore{Store: store.NewMem()}
	b := &broker{db: cs, shared: vs}
	_ = cs.BindOwner(store.Owner{GitHubID: 7, Login: "octocat", Pubkey: "pkA"})

	resolve := func() (string, bool) {
		if o, ok, err := cs.OwnerByPubkey("pkA"); err == nil && ok && !o.Anonymized && o.GitHubID != 0 {
			return "u_gh_7", true
		}
		return "", false
	}
	for i := 0; i < 3; i++ {
		w, ok := b.cachedOwnerWallet("pkA", resolve)
		if !ok || w != "u_gh_7" {
			t.Fatalf("call %d: got (%q,%v), want (u_gh_7,true)", i, w, ok)
		}
	}
	// counting via OwnerByPubkey inside resolve: but resolve only runs on a miss. Count
	// the actual DB hits.
	_, _, ownerReads, _, _ := cs.counts()
	if ownerReads != 1 {
		t.Errorf("binding should hit Postgres ONCE then serve from cache, hit %d times", ownerReads)
	}

	// Invalidate -> the next read re-resolves from Postgres.
	b.invalidateOwnerWallet("pkA")
	if _, ok := b.cachedOwnerWallet("pkA", resolve); !ok {
		t.Fatal("post-invalidation read should still resolve")
	}
	if _, _, ownerReads2, _, _ := cs.counts(); ownerReads2 != 2 {
		t.Errorf("invalidation should force a re-read (2 total), got %d", ownerReads2)
	}
}

// TestCachedAccountOfNodeHit proves the node->account binding caches + invalidates.
func TestCachedAccountOfNodeHit(t *testing.T) {
	vs, _ := testValkeyShared(t)
	cs := &countingStore{Store: store.NewMem()}
	b := &broker{db: cs, shared: vs}
	_ = cs.BindNode("node-1", "ownerPub")
	resolve := func() (string, bool) {
		a, ok, _ := cs.AccountOfNode("node-1")
		return a, ok
	}
	for i := 0; i < 3; i++ {
		if a, ok := b.cachedAccountOfNode("node-1", resolve); !ok || a != "ownerPub" {
			t.Fatalf("call %d got (%q,%v)", i, a, ok)
		}
	}
	if _, _, _, acctReads, _ := cs.counts(); acctReads != 1 {
		t.Errorf("node binding should hit Postgres ONCE, hit %d", acctReads)
	}
	b.invalidateAccountOfNode("node-1")
	_, _ = b.cachedAccountOfNode("node-1", resolve)
	if _, _, _, acctReads2, _ := cs.counts(); acctReads2 != 2 {
		t.Errorf("invalidation should re-read (2 total), got %d", acctReads2)
	}
}

// TestCachedBindingFlagOff proves flag-OFF is the direct path (resolve runs every call,
// no caching).
func TestCachedBindingFlagOff(t *testing.T) {
	b := &broker{db: store.NewMem()} // shared == nil
	calls := 0
	resolve := func() (string, bool) { calls++; return "u_gh_7", true }
	for i := 0; i < 3; i++ {
		b.cachedOwnerWallet("pkA", resolve)
	}
	if calls != 3 {
		t.Errorf("flag-off must resolve every call, resolved %d/3", calls)
	}
}

// --- W4: seeded-flag skip + the PG guard still holds --------------------------

// TestEnsureSeededSkipsAfterFirst proves the seeded-flag fast path: the first ensureSeeded
// runs the Postgres upsert/seed (BalanceOf), and subsequent calls SKIP it (the Redis
// SETNX flag is present). The Postgres seed_grants guard remains the real authority.
func TestEnsureSeededSkipsAfterFirst(t *testing.T) {
	vs, _ := testValkeyShared(t)
	cs := &countingStore{Store: store.NewMem()}
	b := &broker{db: cs, shared: vs, seedFunds: 100}

	b.ensureSeeded("u_gh_7") // first: runs BalanceOf (upsert + seed)
	b.ensureSeeded("u_gh_7") // flag present: skip
	b.ensureSeeded("u_gh_7") // skip

	if _, _, _, _, bal := cs.counts(); bal != 1 {
		t.Errorf("ensureSeeded should run the Postgres seed tx ONCE then skip, ran %d times", bal)
	}
	// The wallet still got seeded exactly once (the PG guard, the truth).
	if got, _ := cs.PeekBalance("u_gh_7"); got != 100 {
		t.Errorf("wallet should be seeded once to 100, got %v", got)
	}
}

// TestEnsureSeededPGGuardHoldsWithoutFlag proves a LOST/absent Redis flag is harmless:
// running ensureSeeded twice with the flag cleared between calls re-runs the upsert, but
// the Postgres seed_grants ON-CONFLICT guard ensures the seed is applied AT MOST ONCE
// (no double-seed) - the real guard, not Redis.
func TestEnsureSeededPGGuardHoldsWithoutFlag(t *testing.T) {
	vs, mr := testValkeyShared(t)
	cs := &countingStore{Store: store.NewMem()}
	b := &broker{db: cs, shared: vs, seedFunds: 100}

	b.ensureSeeded("u_gh_9") // first seed
	mr.FlushAll()            // simulate Redis eviction: the flag is gone
	b.ensureSeeded("u_gh_9") // re-runs BalanceOf (no flag) - but PG guard no-ops the seed

	if _, _, _, _, bal := cs.counts(); bal != 2 {
		t.Errorf("a lost flag should re-run BalanceOf (2 calls), ran %d", bal)
	}
	if got, _ := cs.PeekBalance("u_gh_9"); got != 100 {
		t.Errorf("the PG guard must prevent a double-seed: balance %v, want 100", got)
	}
}

// --- W2b: monthly-spend counter reconciles on a miss + FAILS CLOSED -----------

// TestMonthSpendCounterFastPath proves the happy path: after recordMonthSpend, monthSpend
// reads the Redis counter (a fast-path hit) without a ledger SUM.
func TestMonthSpendCounterFastPath(t *testing.T) {
	vs, _ := testValkeyShared(t)
	cs := &countingStore{Store: store.NewMem()}
	b := &broker{db: cs, shared: vs}
	now := time.Now()
	holder := "u_gh_7"

	// Seed the counter via the Finalize-time increment path.
	b.recordMonthSpend(holder, 3.0, now)
	b.recordMonthSpend(holder, 2.0, now)

	// First monthSpend read: counter exists -> fast-path hit (no reconcile SUM).
	if got := b.monthSpend(holder, now); got != 5.0 {
		t.Errorf("fast-path month spend = %v, want 5.0", got)
	}
	if ms, _, _, _, _ := cs.counts(); ms != 0 {
		t.Errorf("a counter HIT must not run the ledger SUM, ran %d", ms)
	}
}

// TestMonthSpendReconcilesOnMiss is the W2b reconcile proof: with NO Redis counter (a
// miss/eviction) but REAL spend in the Postgres ledger, monthSpend recomputes the
// authoritative SUM (NOT $0) and re-seeds the counter. A Redis miss never reads as $0.
func TestMonthSpendReconcilesOnMiss(t *testing.T) {
	vs, mr := testValkeyShared(t)
	cs := &countingStore{Store: store.NewMem()}
	b := &broker{db: cs, shared: vs}
	now := time.Now()
	holder := "u_gh_7"

	// Real captured spend in the ledger, but the Redis counter was never set / was evicted.
	if _, err := cs.Settle(holder, "paid", 7.50, 0, protocol.UsageReceipt{RequestID: "r1", TS: now.Unix()}); err != nil {
		t.Fatal(err)
	}
	mr.FlushAll() // ensure no counter exists -> a miss

	got := b.monthSpend(holder, now)
	if got != 7.50 {
		t.Fatalf("a Redis miss must RECONCILE from the ledger (7.50), not read $0: got %v", got)
	}
	if ms, _, _, _, _ := cs.counts(); ms != 1 {
		t.Errorf("a miss should reconcile via ONE ledger SUM, ran %d", ms)
	}
	// The counter was re-seeded with the truth: a second read now hits the fast path.
	if got2 := b.monthSpend(holder, now); got2 != 7.50 {
		t.Errorf("re-seeded counter should serve the reconciled value, got %v", got2)
	}
	if ms2, _, _, _, _ := cs.counts(); ms2 != 1 {
		t.Errorf("the re-seeded counter should be a HIT (still 1 SUM total), ran %d", ms2)
	}
}

// TestMonthlyCapFailsClosedOnRedisMiss is the MONEY fail-closed proof: with the cap set
// and real spend AT the limit recorded in the ledger but the Redis counter EVICTED, the
// cap gate must still REJECT an over-budget request (it reconciles the true spend from
// the ledger). A Redis miss can never silently let a user blow past their cap.
func TestMonthlyCapFailsClosedOnRedisMiss(t *testing.T) {
	vs, mr := testValkeyShared(t)
	cs := &countingStore{Store: store.NewMem()}
	b := &broker{db: cs, shared: vs}
	now := time.Now()
	holder := "u_gh_7"
	if err := cs.SetMonthlyCap(holder, 10.0); err != nil {
		t.Fatal(err)
	}
	// Ledger truth: $9.50 already spent this month.
	if _, err := cs.Settle(holder, "paid", 9.50, 0, protocol.UsageReceipt{RequestID: "r1", TS: now.Unix()}); err != nil {
		t.Fatal(err)
	}
	mr.FlushAll() // Redis counter evicted -> a miss. A fail-OPEN bug would read $0 and allow.

	// A $1.00 request would push spend to $10.50 > $10 cap: it MUST be rejected, proving
	// the gate reconciled from the ledger (not the absent/zero Redis counter).
	w := httptest.NewRecorder()
	st, _ := b.monthlyCapCheck(w, holder, 1.0, now)
	if st != http.StatusPaymentRequired {
		t.Fatalf("cap must FAIL CLOSED on a Redis miss (reject over-budget), got status %d", st)
	}
}

// --- W6: seed-remaining counter + the /promo endpoint -------------------------

// TestPromoStatusReconcilesAndCounts proves the seed-remaining mirror: it reads the Redis
// counter on a hit and reconciles from the authoritative SeedStatus on a miss.
func TestPromoStatusReconciles(t *testing.T) {
	vs, mr := testValkeyShared(t)
	db := store.NewMem()
	db.SetSeedLimit(5)
	b := &broker{db: db, shared: vs}

	// Three wallets seeded -> 2 remaining (authoritative).
	for _, w := range []string{"a", "b", "c"} {
		if _, _, err := db.SeedOnce(w, 100); err != nil {
			t.Fatal(err)
		}
	}
	mr.FlushAll() // force a miss -> reconcile from SeedStatus
	rem, unlimited, active := b.promoStatus()
	if unlimited || !active || rem != 2 {
		t.Errorf("promoStatus = (rem=%d unlimited=%v active=%v), want (2,false,true)", rem, unlimited, active)
	}

	// Exhaust the cap -> remaining 0, promo inactive.
	for _, w := range []string{"d", "e"} {
		_, _, _ = db.SeedOnce(w, 100)
	}
	b.invalidateSeedRemaining()
	rem, _, active = b.promoStatus()
	if active || rem != 0 {
		t.Errorf("at the cap promoStatus = (rem=%d active=%v), want (0,false)", rem, active)
	}
}

// TestPromoEndpoint proves GET /promo returns the seeds-remaining JSON (flag OFF path:
// reads Postgres directly).
func TestPromoEndpoint(t *testing.T) {
	db := store.NewMem()
	db.SetSeedLimit(3)
	_, _, _ = db.SeedOnce("a", 100) // 2 remaining
	b := &broker{db: db}            // shared == nil

	w := httptest.NewRecorder()
	b.promo(w, httptest.NewRequest(http.MethodGet, "/promo", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /promo code = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"seeds_remaining":2`) || !strings.Contains(body, `"active":true`) {
		t.Errorf("GET /promo body = %q, want seeds_remaining=2 active=true", body)
	}
}

// TestPromoUnlimited proves an unlimited seed cap reports remaining=-1, unlimited+active.
func TestPromoUnlimited(t *testing.T) {
	db := store.NewMem() // no SetSeedLimit -> unlimited
	b := &broker{db: db}
	rem, unlimited, active := b.promoStatus()
	if rem != -1 || !unlimited || !active {
		t.Errorf("unlimited promoStatus = (%d,%v,%v), want (-1,true,true)", rem, unlimited, active)
	}
}
