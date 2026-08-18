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

## 5. Decided: signed hub polls (the credential is gone; the channel is still plaintext)

**Status: DECIDED and IMPLEMENTED — option B of the three below, founder-approved, landed on
`release/v5.7.0`, and AMENDED once after review (see 5.4b and the Tower half of 5.5).** This
section was written up as NEEDS A DECISION; it is kept as the record of what was decided and why,
because the reasoning is the part a later reader needs — including the parts of it that were
wrong.

### 5.1 What was true

`internal/agent/tower.go`'s `hubBaseURL` used to claim that "an endpoint that carries its own
scheme is honored verbatim… this is how a TLS-fronted hub is reached". It cannot be, and no
deployment has ever done it. Both places a relay endpoint enters the system parse it with
`net.SplitHostPort` — `internal/towercore/link/towerlink.go` on the Tower's `Hello`, and
`cmd/roger-tower/serve.go` on its own configuration — and `net.SplitHostPort` rejects
`"https://relay.example:443"` outright ("too many colons in address"). So the scheme branch is
unreachable through every real ingress, the `"http://" + endpoint` branch is the only one that has
ever run, and **a serving node long-polled its hub over plain HTTP, presenting its per-Station
bearer token in an `Authorization` header, for the life of the process.**

What that did and did not expose:

- **Not the payload.** The job and its answer are sealed to keys the relay does not hold. An
  on-path observer sees ciphertext, exactly as the relay does.
- **The node's polling credential.** Anyone on the path could capture the `HubToken` and poll for
  that Station's work — a theft-of-work and **denial-of-earnings** primitive, not merely an
  eavesdropping one. Steal it once and it works forever, from anywhere, because the token was
  minted at attach and never rotated or expired.

`--tower` made this one operator's opt-in. Its removal made it the default for every signed-in
`roger share`, which is why it was worth a section rather than a TODO.

### 5.2 The options, and what each cost

**A. Let the endpoint express a scheme.** Change the advertised endpoint from `host:port` to a
URL, or add a sibling `scheme`/`tls` field on the Tower's `Hello` and on the attach response.
Touches `link.Hello` validation, `cmd/roger-tower/serve.go` config, `RelayEndpoint`, the
`tower_routable` projection's endpoint column, `towerEdgeAuthorize`'s `endpoint` response field,
the consumer client that dials it, and `hubBaseURL`. The cheap half is a parse that accepts both
forms; the expensive half is that a Tower operator now needs a certificate for a name the node
will verify, which is a real operational burden on volunteers.

**B. Sign each request instead of presenting a reusable secret.** Leave the transport alone and
make a captured request useless.

**C. Both.** B is the cheaper and larger risk reduction and does not depend on A; A is the
complete answer.

### 5.3 The decision: B now, A still open

B shipped. A node signs every hub request with the Station's **assertion key** — the Ed25519 key
it already signs receipts with, which Core already records on the attachment — using
`protocol.SignRequest`, the same scheme the node already authenticates to Core with. Core ships
the public half to the Tower on `/tower/hub/nodes` (the Tower previously had no way to learn it),
and `towerhub.Server` verifies against it instead of comparing a token string. No certificates,
no key distribution, nothing asked of Tower operators.

**A is not closed and is not scheduled.** After B the plaintext channel leaks traffic *shape* —
when a node polls, how large each sealed body is — which the Tower operator can see in any case
by virtue of being the Tower. It also leaves the link unauthenticated in the other direction: a
node still cannot tell it is talking to the hub Core named. Those are the reasons to do A
eventually. They are not reasons to hold B.

### 5.4 Replay, which is the part that needed thinking about

`protocol`'s scheme is timestamp-window based (`SigMaxSkew`, five minutes), not nonce based, so
signatures alone do not stop reuse. What a reuse *buys* differs per route, and only one route
matters:

| route | what a replay achieves |
|---|---|
| `POST /complete` | nothing — `hub.Complete` consumes the waiter once and `ConsumeDispatched` consumes the dispatch record once |
| `POST /audit/transcript` | nothing — the want is cleared on first success, so a repeat is refused a courier ride |
| `GET /audit/wanted` | a read whose answer the on-path attacker already watched go past in the clear |
| `GET /poll` | **dequeues a job** the attacker cannot open and the honest node therefore never serves |

The tempting conclusion — "the window narrows the attack from forever to five minutes, which is a
large improvement" — is wrong, and it is worth writing down why. A node long-polls continuously,
so an on-path attacker captures a fresh, unexpired poll signature every twenty-five seconds and
never runs out. Timestamp skew alone would have narrowed "steal the token once, deny forever, from
anywhere" to "stay on the path and deny continuously" — real, but not the fix.

So the hole is closed with a per-Station nonce carried in the request target (`?nonce=`, so
`protocol.CanonicalRequest` binds it without forking the canonical string) and a nonce cache on
the Tower. It applies to every route rather than only `/poll`, because a per-route exemption is a
trap for whoever adds the next route.

The cache is bounded, which for an attacker-facing map is the design rather than a detail: the
signature is verified **before** anything is recorded, so only the holder of the Station's private
key can make it grow; entries are per Station, so no station can evict another's; and each
Station's set is two generations rotated on age or size, bounding memory at
`2 × maxNoncesPerStation` regardless of traffic.

### 5.4b Round two: the gate as first shipped was not closed, and this section said it was

An independent correctness audit and an independent cryptographic review both went over the
shipped change. They agreed the hard parts hold — no downgrade, no cross-station poisoning, clean
domain separation between hub requests and receipts, redirects refused. They also converged, from
different directions, on the same conclusion: the property shipped was **narrower than this
section claimed**. The claims are corrected here rather than quietly edited out, because a design
record that only ever describes what turned out to be right is a record nobody can trust.

**"The hole is closed" was true of a nonce gate with no clock skew in it.** `protocol.VerifyRequest`
accepts a timestamp within `SigMaxSkew` in *either* direction, so a single signature is acceptable
across `2 × SigMaxSkew` of Tower time — while the gate rotated its generations on `SigMaxSkew`,
guaranteeing only half that much memory. A node whose clock leads its Tower's by L therefore had
each of its requests forgotten L before it expired. Proved end to end with a six-second lead: the
replay is refused immediately, then accepted with a 200 and the job dequeued after two rotations.
Retention is `2 × SigMaxSkew` now, and the invariant — *a nonce is remembered for at least as long
as the timestamp that came with it will be accepted* — is stated where it is enforced.

The cheaper fix, refusing timestamps more than a few seconds in the future, was **considered and
rejected**. A node has no legitimate *need* to be ahead of its Tower, but plenty of nodes are: an
unsynchronised clock is the ordinary condition of a machine in a spare room. Refusing those
requests turns a clock problem into a node that silently stops earning, which is the harm this
whole change exists to remove. Remembering longer costs bounded memory; refusing costs an honest
operator their income.

**"At the real cadence a node needs hours to reach the cap" was wrong, and so was calling the cap
a fleet-management problem rather than an outsider's lever.** The nonce is recorded when a request
authenticates, which is *before* the long poll blocks, and `ServeLoop` floors an empty poll cycle
at 200ms — so an on-path attacker who forwards each poll and answers `204` himself drives the
node's own key at roughly five signatures per second per worker: two full generations in about
102 seconds at eight workers, inside one skew window. Proved: after 4104 genuine signed polls, a
poll captured before them was accepted and dequeued the victim's job, while the same bytes
replayed before the overflow were correctly refused.

The bound is no longer a claim about traffic. **Every rotation records the newest timestamp the
dropped generation held, and any request at or before that floor is refused outright** — so the
gate either still remembers a nonce or refuses its whole era, at any cap and any rate. The floor
is a request timestamp rather than a wall clock, so a node with a consistently offset clock is
measured against its own past; at the ordinary cadence the floor sits two generations back and
refuses nothing. The residual is now the other direction: an attacker who drives a node hard
enough to move its floor can get an honest request refused if that node's clock *lags*. That is a
denial available to anyone already on the path by simpler means, and it fails closed.

**Nothing bound the Tower, so a captured signature was good at any hub process.** The canonical
string covers method, target, timestamp and body digest — not the host — and the nonce ring is
per process and in memory. The Tower id now rides in the signed target (`?tower=`), which both
sides already know. On its own that would have been decorative, because the ways this was
actually reachable all involve the *same* Tower id:

| reachable via | closed by |
|---|---|
| the hub restarts or redeploys inside the skew window | the gate refuses anything signed before this process started |
| Core's answer briefly omits a Station, so the refresher unregisters it and its ring is dropped | unregistering leaves a floor behind: the memory goes, the refusal stays |
| a signature carried to a different Tower that has the same Station registered | the Tower id in the signed target |
| **two hub processes answering one endpoint** | **nothing — see below** |

**A Tower runs exactly one hub process per endpoint. That is now a constraint, not an
assumption.** Two processes cannot agree on a nonce without shared state, and neither the ring nor
the settle spool is shared. Putting a load balancer in front of two `roger-tower serve --hub`
instances re-opens replay for the whole skew window; a Tower that needs to scale needs a shared
gate first.

**An unauthenticated caller could make the Tower buffer its whole body first.** `/complete` reads
16MB and `/audit/transcript` 8MB before `authNode` runs, and that ordering is *required* — the
signature covers a digest of the bytes that arrived, so verifying a re-serialization would verify
the wrong thing. It stays, behind a cheap door: a request presenting no credential this Tower has
registered for any Station is refused on its headers, before a byte is read. (The hub listener
still has no connection cap and a two-minute read timeout. That is a separate, smaller exposure
and it is not fixed here.)

Two smaller corrections, neither exploitable on today's four routes and both fixed because the
sentences they falsify are what the next route will be written against. The canonical target
joined the percent-*decoded* path to the raw query, so `/poll?station=st-1&nonce=N` and
`/poll%3Fstation=st-1&nonce=N` produced one identical canonical string — it uses `EscapedPath`
now. And the GET routes passed `nil` as the body regardless of what arrived, so "the signature
covers the body" was true of half the surface; they hand over what they read.

### 5.5 Transition: hard cutover on the node, one release of tolerance on the Tower

`roger` and `roger-tower` are installed and updated separately, so a node and its Tower can be at
different versions in both directions. The two halves are deliberately **asymmetric**:

- **The node never sends the token again.** `towerhub.Client` has no `Token` field. Accepting a
  fall-back would mean the credential is still on the wire, which is the whole exposure — and a
  downgrade an attacker can provoke (strip the signature headers; answer 401 until the node gives
  up) is not a security property. A current node therefore cannot serve a Tower older than this
  change, and says so on the notice channel (`agent.ErrHubRefusedThisNode`) rather than retrying
  into a discarded writer forever.
- **The Tower accepts either, for one release.** `roger-tower serve --hub-legacy-bearer` (default
  true; `hub.allowLegacyBearer` in the config file) keeps a v5.7.1 node — which still presents a
  bearer — earning while it updates. What it preserves is an exposure an already-released binary
  already has.

  **"It costs the fleet nothing" was wrong, and it was wrong for exactly the operators this change
  was written for.** The token was registered for *every* Station, including ones whose node had
  already upgraded and was signing, and Core never rotates it — it returns the same value on every
  re-attach. So a token lifted off the cleartext wire at any point *before* a node upgraded still
  opened that node's queue afterwards, repeatably, from off-path, for a whole release. The claim
  was about the NODE's behaviour (a current node transmits nothing capturable) and missed that the
  TOWER still honoured yesterday's capture. Proved end to end against the real `Server`.

  It ends per Station, on behaviour, the way Core's audit leniency does: **the first request a
  Tower verifies as a genuine signature from a Station kills the bearer for that Station**, on
  that Tower, from that instant. An old node never signs and keeps earning; an upgraded node
  closes its own hole on its first poll. Neither an attacker holding the token nor one on the path
  can produce the signature that flips the latch, or unflip it.

  Two alternatives were rejected. Registering the token only for Stations with no assertion key is
  the obvious one-liner — and since Core sends an assertion key for every self-attached Station,
  it is `AllowLegacyBearer=false` in disguise: it would refuse every un-upgraded node on the
  fleet, one commit after promising not to. Rotating the token at Core on each re-attach is
  theatre against this attacker: a node old enough to present a bearer presents it in the clear
  every twenty-five seconds, so the replacement is captured as easily as the original.

  It was also, until now, **unturnable-off**: the field was documented here as "default true",
  which promises a false, while the only assignment to it outside its constructor lived in a unit
  test. It is a flag and a config field now, and immutable after construction rather than an
  exported field read under a lock nothing ever wrote it under.

**Delete the bearer path, `NodeAuth.LegacyToken`, `newHubToken`, `--hub-legacy-bearer` and the
`hub_token` field one release after this ships.** Nothing else keys on the column: the three
readers that used to ask "is this a hub-polling node?" by testing `HubToken != ""` — including the
one deciding which Stations Core tells a Tower about at all — ask `attach.Attachment.SelfAttached()`
now. Followed literally against the old code, that deletion instruction would have emptied every
Tower's node list and taken the relay fabric offline.

**DEPLOY CORE FIRST.** The ordering was known — `authNode` has a bespoke sentence for it — and
written down nowhere. Core is the only source of a Station's assertion key, so a new node behind a
new Tower whose Core has not been updated is 401'd forever: it signs, and the Tower has nothing to
check the signature against. **Core, then Towers, then nodes.**

### 5.6 What is still open

The question §5 originally asked first — *should offering a share to the relay fabric be opt-out
until the exposure is fixed?* — is answered by B shipping in the same release as the automatic
join: there is no window in which the fleet is joined by default and holding a stealable
credential. TLS (option A) remains deferred, and the endpoint wire format stays `host:port`.
