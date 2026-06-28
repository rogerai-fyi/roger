# Phase 5 — enhancement + performance audit

> Static-analysis pass (reading the hot paths, not profiled under load). Treat the perf items
> as *hypotheses to confirm with `pprof` + a load benchmark* before optimizing — but each is a
> real, located smell. Scale context: the broker runs a **deliberate 2-instance cap**
> ([[scaling-posture]]), so "contention at scale" items are not urgent today; they're logged
> for when the cap lifts. Implement order at the bottom.

## Performance

### P1 — `pickFor` holds the write-lock for the whole routing pass · [perf, scale] · M
`cmd/rogerai-broker/tunnel.go:1947`. The per-relay routing takes `b.metricsMu.Lock()` (a WRITE
lock) and holds it across the *entire* candidate eligibility + two-pass scoring loop over every
node — including, when any owner is banned, a `b.db.AccountOfNode()` **store lookup inside the
lock**. Every relay serializes on this. At 2 instances it's fine; past the cap it caps routing
throughput. Fix: snapshot eligible candidates under a short read-lock (or `RLock` via an
`RWMutex`), score outside the lock, and resolve the owner-ban lookup before/after the critical
section. Confirm with a relay benchmark + mutex profile first.

### P2 — `pickFor` allocates a `[]cand` per relay · [perf] · S · low
Same function: a fresh `cands` slice (+ the two-pass) per request. Bounded by node count (small
now). A pre-sized slice or `sync.Pool` would cut alloc churn at scale. Low priority until P1.

### P3 — hot read-path cache only exists with Redis · [perf/ops] · S–M
`/discover` + `/market` recompute the full market (locks + sort + per-offer signal) per request,
collapsed by `serveCachedJSON` **only when `ROGERAI_REDIS_URL` is set** (`market.go:173`,
`computeMarket`). A single-instance / no-Redis deploy recomputes on every hit. The data is
PUBLIC + shareable, so a tiny **in-process TTL cache fallback** (when Redis is off) would give
the same amortization everywhere. Action: (a) verify Redis is configured in prod; (b) add the
in-process fallback. Effort S–M.

### P4 — screensaver allocates a full frame buffer every tick · [perf] · S · low
`internal/tui/pingworld.go`: `worldBuffer` allocates a fresh `[][]worldCell` (h×w) and
`compositeWorld` a `strings.Builder` + per-run `[]rune` segs EVERY frame (~8fps), forever while
the screensaver is up. Go's GC shrugs at this, but it's continuous churn for an idle relax view.
Fix: have `pingWorldModel` own a reusable buffer (reset in place) + reuse the builder. Low impact.

### P5 — admin overview issues ~15 sequential round-trips · [perf] · M · low
`internal/store/admin_postgres.go` runs ~15 separate `QueryRow`/`Query` calls per load. It's the
**founder-only** dashboard (low frequency), so low priority — but it could collapse into fewer
round-trips (a CTE / batched query) if the page ever feels slow.

## Enhancements

### E1 — stale TUI version fallback · [hygiene] · XS · DONE-able now
`internal/tui/tui.go:7292` `var helpVersion = "v4.8.5"` (overridden at runtime by `SetVersion`,
but a stale mismatch if ever skipped). Sync to the current version. *(Flagged by the v4.11.0
release audit.)*

### E2 — release.yml should stamp the version from the tag · [release robustness] · S
`.github/workflows/release.yml:29` builds with `-ldflags "-s -w"` — no `-X main.Version`, so the
binary's version is whatever `var Version` happens to be compiled as. Prior releases left it at
`4.8.5` (binaries under-reported; the upgrade check always saw "newer"). Make the **git tag the
single source of truth**: `-X main.Version=${GITHUB_REF_NAME#v}`. Then a forgotten source bump
can never ship a wrong-version binary again. High value, tiny change.

### E3 — faster pre-push for doc/web-only changes · [DX] · S · optional
Every push runs the full cover-gate (real Postgres via testcontainers) + claude-audit (~5 min),
even for a one-line doc/web edit. A `make pre-push-fast` that skips the Postgres money-path tests
when no Go in those packages changed would speed iteration. Optional.

### E4 — surface `roger --ping` + the screensaver in `roger help` / the web · [polish] · S
The new screensaver is discoverable in the TUI (`z`/`/ping`/help) but `roger --help` (CLI) and
the web manual's command list may not mention `roger --ping`. A one-line add each. Low effort.

## Suggested order
1. **E1 + E2** now — trivial + they harden the release we just cut (do together).
2. **P3** (in-process discover/market cache fallback) — real perf win for non-Redis deploys,
   self-contained, testable.
3. **P1** (pickFor lock) — after a relay benchmark + mutex profile confirms it; the biggest
   structural perf item, but measure first.
4. **P4 / P2 / P5 / E3 / E4** — opportunistic polish.

---

## P1 — measured (BenchmarkPickFor, cmd/rogerai-broker/pick_bench_test.go)

Ran on the dev box (Threadripper, GOMAXPROCS=4):

| nodes | ns/op | B/op | allocs/op |
|---|---|---|---|
| 10 | 2.5µs | 12.6 KB | 9 |
| 100 | 25µs | 104 KB | 15 |
| 500 | 95µs | 417 KB | 19 |
| parallel (100) | 21.7µs | 104 KB | 15 |

**Verdict — DEFER P1.** At the 2-instance cap (tens of nodes) routing is 2.5–25µs/relay,
negligible. The parallel run ≈ the sequential nodes=100 run, which *confirms* the lock fully
serializes picks (no parallel speedup) — but that only bites past the cap at high node counts.
The benchmark stands as the baseline for when the cap lifts. The more interesting latent cost is
**allocations** (104KB→417KB/pick, ~1KB/candidate) — that's the real P2 target (investigate the
per-candidate alloc) before the lock. Neither is worth optimizing at today's scale.
