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

**The flag disappears.** *(Done — see the M0/flag-removal commits.)* `roger share --tower` stops being how a node reaches the fabric. If
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

**M0 — done.** Every share registers, is probed and is discoverable; the attachment carries
a Core-verified `node_id` joining a station to its broker registration; and `--tower` is
gone, with an ordinary `roger share` offering itself to the relay fabric best-effort.

**M1 — done.** `Candidates` now returns a total, stable order (`ORDER BY station_id, offer_id`
in PG; the same sort in mem, so the reference implementation stops diverging from the durable
one under map randomisation). `fleet.Station` carries the join, and `edgeTargetFor` ranks with
`edgeCandidateScore` — `quality / (1 + inflight)`, mirroring `router.go`'s shape — instead of
returning `rows[0]`. Unmeasured scores neutral, not zero, so a newly attached station is not
frozen out of the traffic it would need to earn a score.

**M1 correction (audit).** As first shipped, M1 claimed the load divisor "spreads work without
overriding quality" and it did not. `enterInflight` is called only from `tunnel.go`, the
relayed path, so nothing on the edge path ever moved the counter: every candidate scored at
load zero forever, and a total order plus a strictly-greater comparison made the
lexicographically first station a permanent all-to-one magnet — 200 placements, one station.
The shipped test passed only because it wrote `b.inflight` by hand, a value the real path never
sets. Both halves are now closed: an edge attempt increments the same per-node counter the
classic router divides by (bracketed at authorize and released at settle, with an expiry bound
by the attempt's own finalization ceiling, since the edge has no dispatch loop to unwind), and
placement draws with `selectP2C` — the same power-of-two-choices this document's §1.5 already
names as the anti-magnet mechanism, rather than a second one invented for this path.

Deliberately not folded in yet, and now stated in `edgeCandidateScore`'s own comment rather
than implied: price (an edge consumer already authorizes against the station's pinned price, so
undercutting is not this function's call); speed-fit (TTFT is measured broker-to-node and says
little about a path that avoids the broker); capacity normalization (`router.go` divides by
`1+inflight/capacity`, the edge by `1+inflight` — `publishRoutable` hardcodes every
self-attached row's capacity to 1, so normalizing by it today would divide by a constant and
merely look meaningful); and a decaying UCB radius (an unmeasured edge row scores a flat
neutral that never self-extinguishes). The first two belong with the locality term in M5; the
capacity one wants real per-node capacity in the projection, which is M2-shaped work.

**M1 correction, round two (security + enhancement review).** Three more things were wrong, and
all three were promoted from "opt-in defect" to "fleet default" by the flag removal rather than
caused by it.

- *A never-probed node scored the ceiling.* `edgeCandidateScore` read `tq, probed :=
  b.trust[id]` — MAP PRESENCE, not `tq.probed`. `observeRecount` creates an entry on the first
  re-count of any served request with `probed=false`, and `trustState.score()` starts at 1.0, so
  one served request promoted a station from the 0.75 neutral to the score reserved for a
  canary-verified node, permanently, on zero liveness evidence. `edgeQuality` now distinguishes
  three states, and admits recount evidence in one direction only: it measures HONESTY, not
  liveness, so it may pull a station below neutral and may never lift one above it.
- *Edge load was the classic router's counter.* "One counter, not two, because it is one machine"
  was right about load and wrong about what `b.inflight` IS — the paid router divides by it, peers
  merge it, and `probeOnce` skips any node with a non-zero count. Since an edge attempt is opened
  at AUTHORIZE, before the consumer submits anything, and costs a refundable fraction of a cent,
  that handed any signed-in account a lever to suppress a victim's canary probing and depress its
  paid-fabric score (measured: 500 pins for $0.0001; edge score 0.7500 → 0.0147 at 50). The
  counters are separate now and the read is one-way: edge placement adds both, the classic router
  and the prober see only work they dispatched. `/tower/edge/authorize` is also rate-limited per
  account and capped on simultaneously-open attempts, and the reservation expires with the grant's
  EXECUTION deadline rather than its settlement window.
- *There was no eligibility gate at all.* `edgeTargetFor` filtered on three properties of the
  tower and the projection row and asked nothing about the machine. `edgeEligible` now mirrors
  `pickFor`'s hard filters — stale heartbeat, node ban, durable owner ban, private band — under one
  acquisition of `b.mu` then `metricsMu` for the whole fleet, so two rows are never compared across
  two instants. Probe health is graded rather than hard (Tier A/B), because the edge fleet is small
  and a broker-to-node probe streak says little about a path that avoids the broker. This is what
  closes two live holes: `MayEnroll` is checked at attach time only, so a ban applied afterwards —
  which is how the fraud pipeline works — left a node earning; and nothing assigns `StateDetached`
  outside terminal reaping, so a machine that ran `roger share` once and pressed Ctrl-C stayed a
  candidate indefinitely.

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

---

## 5. Open decision: the hub channel is structurally forced to plaintext

**Status: NEEDS A DECISION. Nothing here is implemented.** This section exists because the code
now says the true thing about the channel and the true thing is bad.

### 5.1 What is actually true

`internal/agent/tower.go`'s `hubBaseURL` used to claim that "an endpoint that carries its own
scheme is honored verbatim… this is how a TLS-fronted hub is reached". It cannot be, and no
deployment has ever done it. Both places a relay endpoint enters the system parse it with
`net.SplitHostPort` — `internal/towercore/link/towerlink.go` on the Tower's `Hello`, and
`cmd/roger-tower/serve.go` on its own configuration — and `net.SplitHostPort` rejects
`"https://relay.example:443"` outright ("too many colons in address"). So the scheme branch is
unreachable through every real ingress, the `"http://" + endpoint` branch is the only one that has
ever run, and **a serving node long-polls its hub over plain HTTP, presenting its per-Station
bearer token in an `Authorization` header, for the life of the process.**

What that does and does not expose:

- **Not the payload.** The job and its answer are sealed to keys the relay does not hold. An
  on-path observer sees ciphertext, exactly as the relay does.
- **The node's polling credential.** Anyone on the path can capture the `HubToken` and poll for
  that Station's work — which is a theft-of-work and a denial-of-earnings primitive, not merely an
  eavesdropping one.

`--tower` made this one operator's opt-in. Its removal made it the default for every signed-in
`roger share`, which is why it is worth a section rather than a TODO.

### 5.2 What has been done in the meantime (and what it does not fix)

1. `hubBaseURL` no longer claims a capability that does not exist, and reports whether the channel
   it produced is plaintext.
2. `towerhub.Client` refuses redirects outright (stricter than `protocol.NoDowngradeRedirect`,
   which the broker calls use: a relay is an untrusted counterparty and has no legitimate reason to
   redirect a poll, so "not a downgrade" is too weak a bar for a request carrying a bearer token).
3. The node **says so, once, on a channel that is not discarded** (`agent.ErrHubChannelPlaintext`
   through `agent.Notice`).
4. `PRIVACY.md` no longer asserts "everything is TLS/HTTPS" without the carve-out.

None of that encrypts anything. The channel is still plaintext.

### 5.3 The options, and what each costs

**A. Let the endpoint express a scheme.** Change the advertised endpoint from `host:port` to a
URL, or add a sibling `scheme`/`tls` field on the Tower's `Hello` and on the attach response.

- *Touches:* `link.Hello` validation, `cmd/roger-tower/serve.go` config, `RelayEndpoint`, the
  `tower_routable` projection's endpoint column, `towerEdgeAuthorize`'s `endpoint` response field,
  the consumer client that dials it, and `hubBaseURL`.
- *Migration:* the endpoint is consumed by three independently-deployed programs, so it must be
  additive and tolerant in both directions for at least one release — old Towers advertising
  `host:port` must keep working while new ones advertise a scheme, and an old node receiving a
  scheme must not crash. A parse that accepts both forms is the cheap half; the expensive half is
  that a Tower operator now needs a certificate for a name the node will verify, which is a real
  operational burden on volunteers and the reason this was not done at the start.
- *Note:* the consumer-facing side already has an answer to the certificate problem — the grant
  carries `relay_name` (`<station>.relay.rogerai.fm`) and the consumer connects with that as the
  TLS server name. Reusing that name for the hub link is the obvious shape, and it makes the
  certificate Core's problem rather than each operator's.

**B. Channel-bound (or short-lived) credentials instead of a long-lived bearer.** Leave the
transport alone and make the captured token useless: sign each poll with the Station's assertion
key (it already holds one, and Core already knows the public half from the attachment) rather than
presenting a reusable secret.

- *Touches:* `towerhub` client and server, the hub's `checkToken` seam, `towerHubNodes`.
- *Migration:* additive — the hub can accept either for a release. No certificates, no operator
  burden, and it fixes the credential-capture case completely.
- *Does not fix:* traffic analysis, and it leaves the channel unauthenticated in the other
  direction (a node cannot tell it is talking to the hub Core named).

**C. Both.** B is the cheaper and larger risk reduction and does not depend on A; A is the
complete answer. They are independent, and B first is the sensible order.

### 5.4 The question that actually needs answering first

Given that the join is now automatic and fleet-wide: **should offering a share to the relay
fabric be opt-out until at least option B ships?** The engineering answer is that the exposure is
the operator's credential rather than the consumer's content, which argues it can wait. The
counter-argument is that operators did not choose it and are not in a position to weigh it — and
that "we made it the default while it was known-broken" is a much worse sentence than "we shipped
the flag one release later". That is a founder call, not an engineering one.
