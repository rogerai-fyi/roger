# Locality-aware relay selection — validating the "one share, brokered placement" design

**Status: PROPOSAL, not approved.** Written 2026-08-18 against the tree at `6b49cf43`.
Founder's direction: `roger share` should not require a flag to reach the relay fabric; the
relay should be chosen when a consumer actually tunes in, on locality and load, from a pool
in which our own brokers are just more relays; and the binding should be per-session rather
than permanent.

This document validates that against the code and proposes the smallest path to it.

---

## 1. What is actually true today

### 1.1 There are two disjoint serving paths, and the flag picks one

`roger share` long-polls `GET /agent/poll` on the broker (`cmd/rogerai-broker/main.go`,
25s holds). It never touches a Tower. `roger share --tower` takes an entirely different
branch (`cmd/rogerai/main.go`, inside `if *towerServe`, which returns) into
`agent.ServeTower`: self-attach, then poll one tower's hub forever.

So the flag does not mean "prefer towers". It selects which of two serving fabrics the node
lives on for its whole run. That is not a routing preference and it should not have been a
user-facing choice.

### 1.2 The classic path already has almost everything the direction asks for

This is the part worth knowing before designing anything new:

| Wanted | Already exists | Where |
|---|---|---|
| Connect to the broker first, then be discoverable | node dials out and long-polls | `/agent/poll`, `tunnel.go` |
| Performance + canary probes | EWMA TTFT and TPS per node, folded into a trust state | `probe.go` `recordProbe` |
| Serve a health-annotated model list | `/market`, `/discover` | `market.go` |
| Capacity-aware placement | `score = ucb(reliability × speedFit × priceMod) × loadFactor`, then power-of-two-choices over a reliability-bounded band | `router.go`, `pickFor` in `tunnel.go` |

`router.go` even says its purpose out loud: "so no rig becomes a magnet and no honest laptop
starves." It carries a latency term (`ttftCapMs`) already.

### 1.3 The tower path bypasses all of it

`edgeTargetFor` (`toweredge.go:377`) is the entire edge-path placement policy:

```go
rows, err := ts.routable.Candidates(model, time.Now())
for _, row := range rows {
    ... skip empty endpoint, non-self- offers, towers that may not take work ...
    return target, row, true          // <- the FIRST survivor wins
}
```

It does not consult `router.go`, the probe trust state, load, or price. And `Candidates`
imposes no order: the Postgres query has **no `ORDER BY`** (`fleet/pgstore.go:82`) and the
memory store ranges a **Go map** (`fleet/memstore.go:44`). So edge placement is not merely
first-fit, it is *arbitrary* — and in the memory store, non-deterministic between calls.

### 1.3b A `--tower` node is invisible and unmeasured

This is the finding that matters most, and it was not obvious until the two paths were read
side by side.

`agent.ServeTower` does **not** register the node, and does **not** long-poll `/agent/poll`.
It attaches, pins Core's keys, and runs hub workers. That is the whole lifecycle. So a node
started with `roger share --tower`:

- never enters the classic node registry, so it is absent from `/discover` and `/market` —
  it drops off the public band list entirely;
- is never probed, so `recordProbe` never runs for it and it has no TTFT, no TPS and no
  trust state;
- exists only as rows in the `tower_routable` projection, which nothing but `edgeTargetFor`
  reads.

It also runs under a *different identity*: `ServeTower` mints a separate persistent station
key (`station.Init(dir/tower-station)`) beside the ordinary node key, and the classic
metrics are keyed on the node id. So even if we wanted to rank edge candidates by measured
health, there is no health under that key to rank by.

This explains why `edgeTargetFor` is arbitrary rather than merely unsorted: **there was
never anything to rank with.** A `fleet.Station` row carries capacity, endpoint and prices,
and nothing about whether the station is any good.

It also means the flag currently costs a provider their listing, which nothing warns them
about.

### 1.4 The relay is bound at the wrong moment

`edgeTargetFor` returns `row.TowerID`. That tower was chosen at **provider attach time** by
`towerEdgeAttach` (`toweredgeattach.go:167`), first-fit over `LiveTowers()`, with a comment
conceding "matchmaking beyond first-fit is a later refinement". `MayTakeWork` gates on lease
and state only — an eligibility check, not a capacity one — so a busy tower is never skipped
in favour of an idle one.

The consequence is the founder's objection, precisely: the tower is picked before any
consumer exists, and every consumer of that node is then dragged through it. A node in New
York that attached to a Los Angeles tower will route a New York consumer through Los Angeles
forever.

### 1.5 There is no geography in the system at all

`Region` exists on `protocol.NodeRegistration` and defaults to the literal string `"home"`
(`--region`, `main.go:1005`). Every read of it is **display**: the TUI band list, `/market`,
the stations dashboard. No router reads it. The `tower_routable` projection does not even
carry a region column.

The broker does resolve a real client IP (`concierge.go`: CF-Connecting-IP → X-Forwarded-For
→ RemoteAddr), but it is used for rate limiting, never for placement.

And note what TTFT measures today: it is probed **broker → node**. That is a proxy for node
speed plus broker-to-node latency. It says nothing about consumer-to-node distance, which is
the quantity that actually matters once relays are geographically spread.

### 1.6 Verdict

The direction is correct, and the work is **convergence rather than invention**. One path
grew a real router, probes and a health model; the other forked before any of it and picked
arbitrarily. Nothing here needs a new scheduler — it needs the edge path to use the
scheduler we have, the relay decision moved to dispatch time, and one signal we have never
collected.

---

## 2. The target shape

**Control plane (persistent, cheap).** `roger share` — no flag — registers with the broker
and holds one outbound long-poll. The broker probes it, scores it, and publishes it to
`/discover` and `/market` with health. This is what exists; nothing changes for the operator.

**Data plane (ephemeral, per session).** When a consumer tunes in, the broker knows for the
first time *both* endpoints. Only then does it choose the relay — which may be one of our
brokers or an operator's Tower, ranked identically — and instructs the node over its
existing long-poll to open a hub session there. The node serves, and the session is torn
down. A relay binding never outlives the work.

**Our brokers are relays.** A Tower and a broker data plane are the same role with different
owners. Selection ranks them together; the only difference at settlement is that a
third-party relay mints a 10% earning lot and ours does not.

**The flag disappears.** `roger share --tower` stops being how a node reaches the fabric. If
anything survives it is a *constraint* — "only relays I am willing to be carried by" — which
is a different feature with a different name, and is not needed for a node to participate.

---

## 3. What has to be built

Ordered so each step is shippable and none is wasted if the next is deferred.

**M0 — One registration for every node.**
This is now the first step, not an afterthought, because §1.3b makes the rest impossible
without it. A node that intends to serve through the fabric still registers and long-polls
the broker exactly as a plain `roger share` does, so it is discoverable, probed and scored
like everything else. The relay attachment becomes an *additional* capability of a
registered node rather than a replacement for registration. Requires reconciling the station
identity with the node identity, or carrying the node id on the attachment so the two join.

Nothing downstream can rank edge candidates until this exists, because until this exists
there is nothing measured to rank them by.

**M1 — Stop the arbitrary pick.**
With M0 landed there is health data under a joinable key. Give `Candidates` a deterministic
order and run edge candidates through the existing scorer instead of taking `rows[0]`. Adds
`ORDER BY` in PG, a sort in mem, and a call into `router.go`. Removes both the
non-determinism and the magnet effect. No schema beyond M0, no protocol change.

**M2 — Collect locality.**
A relay's advertised endpoint gets a coarse location, set by Core at admission (never
self-declared — see §4). A node gets one at registration, resolved from the connecting IP
rather than the `--region` string, which stays cosmetic. Carry both into `tower_routable`.

**M3 — Move the binding to dispatch.**
Split "which relay" out of `towerEdgeAttach` and into the authorize path. Attach becomes
"this node is servable"; the tower field on the attachment becomes a default, not a
commitment. `edgeTargetFor` gains the consumer's location and picks the relay per request.

**M4 — Ephemeral hub sessions.**
The node's long-poll response can carry a "serve this attempt at relay R" instruction; the
node opens a hub session to R for that work and closes it. Requires the hub to accept a
short-lived, per-attempt credential rather than only the long-lived `HubToken`.

**M5 — Locality in the score.**
Add a distance term to `router.go`'s product, tuned so it breaks ties and shifts marginal
choices rather than overriding reliability. A fast, trusted, far node must still beat a slow,
unproven, near one — otherwise the optimizer degrades the service it is meant to improve.

---

## 4. Decisions this design does not get to make quietly

1. **Location must not be self-declared.** `--region` is a string the operator types. If it
   ever feeds placement, it becomes a lever: claim to be everywhere, receive everything. Core
   must derive location from the connecting IP, and the operator-supplied string stays
   decorative or is removed.

2. **Locality cannot be allowed to defeat the load factor.** The whole point of `loadFactor`
   and power-of-two-choices is that no node becomes a magnet. A naive "nearest wins" rule
   recreates exactly that failure with a geographic shape: the one relay in a city absorbs
   every request in it.

3. **Our own brokers being in the pool is a conflict of interest.** We keep 20% on a relayed
   request and 30% on one we carry ourselves. Any ranking that puts our relays ahead of an
   operator's must be justified by a measured signal, not by tie-breaking, and the rule
   should be written down before the money depends on it.

4. **Ephemeral sessions cost round trips.** Per-session relay setup adds a handshake to the
   first token of each session. Whether that is cheaper than the bandwidth it saves is an
   empirical question, and M1–M3 are worth doing whether or not M4 pays for itself.
