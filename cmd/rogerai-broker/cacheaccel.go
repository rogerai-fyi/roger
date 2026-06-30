package main

import (
	"net/http"
	"time"
)

// cacheaccel.go holds the flag-gated (ROGERAI_REDIS_URL) Redis FAST-PATH accelerators
// for the hot money/auth paths. They all share ONE iron rule: Redis is NEVER the source
// of truth. Postgres (the ledger, wallet balances, seed_grants/seed_counter, the owner/
// node bindings) stays authoritative; every helper here RECONCILES from Postgres on a
// Redis miss/expiry/error and a money gate FAILS CLOSED (a miss recomputes the real
// value, never treats it as $0/"allowed"). Flag OFF (b.shared == nil) => every helper is
// byte-for-byte the original direct-Postgres path (zero behavior change).

// --- W1: immutable-binding cache (OwnerByPubkey / AccountOfNode) -------------
//
// A pubkey->owner-wallet mapping and a node->owner-account binding are effectively
// immutable per session, yet today they hit Postgres on every authed relay and every
// /balance. Caching them (short TTL) removes ~2-3 point reads per paid request. They
// are NOT money truth (the ledger is), so a brief staleness is safe; the bind WRITE
// invalidates the entry so a re-bind is reflected at once.

// bindingCacheTTL bounds how long a cached binding survives without a refresh. Short
// enough that a rare re-bind self-heals quickly even without explicit invalidation; the
// bind write also invalidates directly, so the TTL is just the backstop.
const bindingCacheTTL = 60 * time.Second

// walletKeyForPubkey is the cache key for the resolved github-scoped wallet of a signing
// pubkey ("" sentinel meaning "no logged-in owner / keep the pubkey-derived id"). We
// cache the RESOLVED wallet string (the output of walletOf), not the whole Owner, since
// the relay/balance hot paths only need the money key.
func walletKeyForPubkey(pub string) string { return "ownerwallet:" + pub }

// accountKeyForNode is the cache key for a node's owner account binding.
func accountKeyForNode(node string) string { return "nodeacct:" + node }

// cachedOwnerWallet resolves a signing pubkey to its github-scoped wallet, with a
// flag-gated Redis read-through. On a hit it returns the cached mapping; on a miss/flag-
// off it falls back to the AUTHORITATIVE Postgres OwnerByPubkey lookup (via resolve) and
// populates the cache. resolve returns ("", false) when the pubkey is not bound to a
// non-anonymized logged-in owner; we cache that NEGATIVE result too (as "-") so an
// anonymous caller does not re-hit Postgres every request. Empty pub never caches.
func (b *broker) cachedOwnerWallet(pub string, resolve func() (string, bool)) (string, bool) {
	if b.shared == nil || pub == "" {
		return resolve()
	}
	if v, found, err := b.shared.cacheGet(walletKeyForPubkey(pub)); err == nil && found {
		s := string(v)
		if s == "-" {
			return "", false // cached negative: not a logged-in owner
		}
		return s, true
	}
	w, ok := resolve()
	stored := w
	if !ok {
		stored = "-"
	}
	_ = b.shared.cacheSet(walletKeyForPubkey(pub), []byte(stored), cacheTTLJitter(bindingCacheTTL))
	return w, ok
}

// invalidateOwnerWallet drops the cached pubkey->wallet mapping after a bind write, so a
// (re)login that changes the owner binding is reflected immediately, not after the TTL.
func (b *broker) invalidateOwnerWallet(pub string) {
	if b.shared == nil || pub == "" {
		return
	}
	_ = b.shared.cacheDel(walletKeyForPubkey(pub))
}

// cachedAccountOfNode resolves a node's owner account binding with a flag-gated Redis
// read-through (immutable TOFU binding). Miss/flag-off falls back to the authoritative
// Postgres AccountOfNode via resolve and populates the cache (negative result cached as
// "-"). The bind write invalidates the entry.
func (b *broker) cachedAccountOfNode(node string, resolve func() (string, bool)) (string, bool) {
	if b.shared == nil || node == "" {
		return resolve()
	}
	if v, found, err := b.shared.cacheGet(accountKeyForNode(node)); err == nil && found {
		s := string(v)
		if s == "-" {
			return "", false
		}
		return s, true
	}
	acct, ok := resolve()
	stored := acct
	if !ok {
		stored = "-"
	}
	_ = b.shared.cacheSet(accountKeyForNode(node), []byte(stored), cacheTTLJitter(bindingCacheTTL))
	return acct, ok
}

// cachedOwnerOf resolves a node's bound owner account through the immutable-binding cache
// (Redis read-through when configured; the authoritative Postgres AccountOfNode on miss).
// Call it OUTSIDE metricsMu/mu: it may do store/Redis I/O, which must NEVER run under the
// hot-path global locks. A per-candidate AccountOfNode under metricsMu was the routing cliff
// this fixes - the moment one owner was banned, every relay pick + market/discover recompute
// serialized on N store round-trips under the global lock. nil db (tests) -> ("",false).
func (b *broker) cachedOwnerOf(node string) (string, bool) {
	if b.db == nil || node == "" {
		return "", false
	}
	return b.cachedAccountOfNode(node, func() (string, bool) {
		acct, ok, _ := b.db.AccountOfNode(node)
		return acct, ok
	})
}

// invalidateAccountOfNode drops a node's cached binding after a BindNode write.
func (b *broker) invalidateAccountOfNode(node string) {
	if b.shared == nil || node == "" {
		return
	}
	_ = b.shared.cacheDel(accountKeyForNode(node))
}

// --- W2b: monthly-spend fast-path counter (FAIL-CLOSED) ----------------------
//
// The cap gate's only aggregate query on the hot paid path is MonthSpendOf (a ledger
// SUM). Back it with a Redis month-to-date counter incremented at Finalize. The ledger
// stays the SOURCE OF TRUTH: on ANY Redis miss/expiry/error, monthSpend RECONCILES by
// recomputing the SUM from Postgres (and re-seeds the counter), NEVER treating the miss
// as $0. So the cap can never be silently bypassed by a Redis eviction - it fails closed
// to the ledger truth.

// capSpendKey is the per-wallet, per-calendar-month spend counter key.
func capSpendKey(holder string, now time.Time) string {
	return "cap:spend:" + holder + ":" + now.UTC().Format("200601")
}

// capCounterTTL keeps the month-to-date counter alive past the END of its calendar month
// (so a request near month-end can't lose the counter mid-month) but lets a stale prior
// month expire. We expire ~40 days out from the read so it always outlives the current
// month; the key is month-stamped, so a new month uses a fresh key regardless.
const capCounterTTL = 40 * 24 * time.Hour

// monthSpend returns the wallet's captured month-to-date spend. With the flag ON it reads
// the Redis fast-path counter; on a HIT it returns it directly (one O(1) GET instead of a
// ledger SUM scan). On a MISS/expiry/error it RECONCILES: it recomputes the authoritative
// SUM from Postgres (MonthSpendOf) and seeds the counter with that truth, then returns the
// truth. Flag OFF, or any Redis trouble, is exactly the original ledger SUM. This is the
// fail-closed contract: a Redis miss NEVER yields $0 - it yields the ledger truth, so the
// cap stays enforced.
func (b *broker) monthSpend(holder string, now time.Time) float64 {
	authoritative := func() float64 {
		s, _ := b.db.MonthSpendOf(holder, now)
		return s
	}
	if b.shared == nil {
		return authoritative()
	}
	if val, found, err := b.shared.counterGet(capSpendKey(holder, now)); err == nil && found {
		return val // fast-path hit
	}
	// Miss / expiry / error => reconcile from the authoritative ledger SUM and re-seed
	// the counter with that truth (so subsequent requests hit the fast path). A failed
	// re-seed is non-fatal: the next read just reconciles again. NEVER return $0 here.
	truth := authoritative()
	_ = b.shared.counterSet(capSpendKey(holder, now), truth, capCounterTTL)
	return truth
}

// recordMonthSpend bumps the month-to-date fast-path counter by a CAPTURED spend amount
// at Finalize, keeping the accelerator current. It is best-effort: a failed/absent
// increment only means the next monthSpend read reconciles the true SUM from the ledger
// (fail-closed), so the cap is never under-enforced for long. cost<=0 (free/self) and
// flag-off are no-ops. The ledger row written by Finalize remains the source of truth.
func (b *broker) recordMonthSpend(holder string, cost float64, now time.Time) {
	if b.shared == nil || cost <= 0 || holder == "" {
		return
	}
	_, _ = b.shared.counterIncr(capSpendKey(holder, now), cost)
}

// --- W4: seeded-flag fast-path (skip the per-request seed upsert tx) ---------
//
// Today BalanceOf runs the wallet-upsert + seed-guard transaction on EVERY paid relay
// and EVERY /balance even for long-seeded users. A Redis SETNX "seeded:<wallet>" flag
// lets an already-seeded wallet skip that write tx. The Postgres seed_grants ON-CONFLICT
// stays the REAL guard: a lost/evicted Redis flag just re-runs the harmless no-op upsert,
// so this can never double-seed or skip a genuinely-needed seed.

// seededFlagKey marks a wallet as already wallet-upserted+seeded.
func seededFlagKey(wallet string) string { return "seeded:" + wallet }

// seededFlagTTL keeps the seeded flag long enough to skip many requests; eviction is
// harmless (the upsert re-runs as a no-op). A week balances skip-rate vs keyspace.
const seededFlagTTL = 7 * 24 * time.Hour

// ensureSeeded runs the seed/upsert path for a wallet exactly as today (b.db.BalanceOf),
// EXCEPT when the flag is ON and a Redis "seeded:<wallet>" marker says it has already
// been done - in which case it SKIPS the Postgres write tx (the fast path). On the first
// time (the SETNX set it), or on any Redis trouble, it runs the real BalanceOf and the
// Postgres ON-CONFLICT guard is authoritative. Returns nothing useful (the relay only
// needs the side effect: the wallet row exists so the subsequent Hold can land).
func (b *broker) ensureSeeded(wallet string) {
	if b.shared == nil || wallet == "" {
		_, _ = b.db.BalanceOf(wallet, b.seedFunds) // unchanged direct path
		return
	}
	// SETNX: if the flag already existed (set==false), this wallet was upserted+seeded
	// before, so skip the write tx. If we just set it (set==true) OR Redis errored, run
	// the authoritative BalanceOf (its seed_grants ON-CONFLICT is the real guard).
	set, err := b.shared.setIfAbsent(seededFlagKey(wallet), "1", seededFlagTTL)
	if err == nil && !set {
		return // already seeded -> skip the Postgres upsert/seed tx
	}
	_, _ = b.db.BalanceOf(wallet, b.seedFunds)
}

// --- W6: seed-remaining counter + the public /promo endpoint -----------------
//
// The homepage promo ("free credits remaining") should auto-hide at 0. Reading the
// authoritative seed_counter via SeedStatus on every homepage load is a Postgres point
// read; mirror it in a Redis counter that RECONCILES from Postgres on a miss. Like every
// other counter here, Postgres (seed_counter) is the source of truth; Redis only
// accelerates the read.

// seedRemainingKey is the (single, global) seed-remaining mirror counter.
const seedRemainingKey = "seed:remaining"

// seedRemainingTTL refreshes the mirror periodically so it can't drift far from the
// authoritative count even without explicit invalidation.
const seedRemainingTTL = 60 * time.Second

// promoStatus returns the seeds remaining and whether the promo is active (remaining>0),
// reading the Redis fast-path mirror when the flag is ON (reconciling from the
// authoritative SeedStatus on a miss), else reading Postgres directly. An unlimited cap
// (remaining<0) reports active=true with unlimited=true. Fail-safe: any error returns the
// authoritative Postgres answer.
func (b *broker) promoStatus() (remaining int, unlimited, active bool) {
	auth := func() (int, bool) {
		_, _, rem, err := b.db.SeedStatus()
		if err != nil {
			return 0, false // unknown -> treat as no seeds remaining (promo hidden)
		}
		return rem, rem < 0
	}
	if b.shared == nil {
		rem, unl := auth()
		return rem, unl, unl || rem > 0
	}
	if val, found, err := b.shared.counterGet(seedRemainingKey); err == nil && found {
		rem := int(val)
		unl := rem < 0
		return rem, unl, unl || rem > 0
	}
	rem, unl := auth()
	_ = b.shared.counterSet(seedRemainingKey, float64(rem), seedRemainingTTL)
	return rem, unl, unl || rem > 0
}

// invalidateSeedRemaining refreshes the mirror after a seed grant lands (so the promo
// decrements promptly). Best-effort; the TTL is the backstop.
func (b *broker) invalidateSeedRemaining() {
	if b.shared == nil {
		return
	}
	if _, _, rem, err := b.db.SeedStatus(); err == nil {
		_ = b.shared.counterSet(seedRemainingKey, float64(rem), seedRemainingTTL)
	}
}

// promo handles GET /promo: a tiny PUBLIC (no-auth) read of the free-credit promo state
// so the homepage can show "N free credits remaining" and auto-hide at 0. Read-only; no
// identity; safe to share/cache. seeds_remaining is -1 when the seed cap is unlimited.
func (b *broker) promo(w http.ResponseWriter, r *http.Request) {
	if corsPreflight(w, r) {
		return
	}
	if !allow(w, r, http.MethodGet) {
		return
	}
	cors(w) // public data - let the website fetch it
	rem, unlimited, active := b.promoStatus()
	writeJSON(w, http.StatusOK, map[string]any{
		"seeds_remaining": rem,
		"unlimited":       unlimited,
		"active":          active,
	})
}
