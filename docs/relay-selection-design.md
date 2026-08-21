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

**M1 correction, round three (independent audit of `17d15cdd` and `9e53f30e`).** Two more, and
both are the same species as the last round: a sentence in a comment that was true of the case
the test happened to exercise.

- *Every classic Station was retired seven days after it attached, permanently, by its own
  Tower.* `detachIdleAttachments` was added to stop the attachment table growing, and its
  evidence is `last_routable`, stamped by `publishRoutable` where an attachment's node id joins
  to a live broker registration. A CLASSIC operator-invited Station has no node id - it is
  reached through its Tower's signed inventory and its machine never registers with a broker at
  all - so `publishRoutable` skips it before it can be stamped, by construction. But
  `DetachIdle`'s WHERE clause was scoped by origin tower and live state only, so
  `COALESCE(last_routable, attached_at)` sat at `attached_at` forever, the row crossed the
  horizon on schedule, and the sweep retired it. `StateDetached` is terminal AND unrecoverable
  (`checkBindings`: "this Station ID has been retired and cannot be reattached"), and the id is
  not freed for a month. `publishRoutable` runs on every inventory push and for every live tower
  on the housekeeping tick, so a Tower did this to its own operator's Stations. Both stores now
  retire only rows that carry a node id, which is exactly the set the stamp can reach: a sweep
  that retires on ABSENCE of evidence may only judge a row that could have produced some. The
  alternative - giving classic attachments a liveness source - has nothing to build from; there
  is no signal on this side of the wire that has ever seen that machine.
  The suite missed it because `TestRetirementDoesNotReachAcrossTowers` created exactly the
  Station being wrongly retired and then swept the OTHER tower: it passed on tower scoping while
  concealing the defect it had constructed.
- *A self-declared string moved edge placement by 2x.* `edgeEligible` derives capacity with
  `capacityOf(concurrentTPS, hw)`, and the commit claimed "an unmeasured node falls back to the
  same conservative hardware prior `pickFor` uses, which is 1 - so nothing about placement on an
  unmeasured fleet changes". `capacityOf` returns 1 only for cpu/unknown/empty; `single-gpu` and
  `apple` return 2 and `multi-gpu` returns 4, and `hw` is a field on
  `protocol.NodeRegistration` that the node's own binary fills in, sitting next to `Region` -
  the field §4.1 names as the thing the supply side may never declare. Measured: `hw=""` scored
  0.2500 against `hw="multi-gpu"` 0.5000 on identical evidence and load, with the P2C tie-break
  (`load/capacity`) quartered by the same word. The edge path now uses the MEASURED branch only
  (`edgeCapacityOf`), which makes the sentence above true of every hw value rather than of the
  one the test used.
  **The classic router keeps `hw`, and that is not an inconsistency.** There the claim is
  self-correcting: `loadFactor` is 1 at zero load whatever the capacity, so the prior only bites
  once the node is busy, and `recordServed` measures `concurrentTPS` under load and replaces it -
  after which degraded TTFT and reliability follow the over-claim down. None of that loop exists
  on the edge: edge work deliberately does not feed the classic counters, and `edgeQuality`
  admits evidence downward only, so the claim would never be corrected. The cost of dropping it
  is that a real rig sharing only through the fabric is normalized as one slot until it takes
  classic traffic - under-using a rig, rather than over-trusting a claim.

**M1 correction, round four (adversarial review with executable reproducers).** Both of the
round-three fixes were correct and both were incomplete in the same way: they fixed the *instance*
and left the *shape*.

- *The surviving capacity input was still self-declared — and it was a bigger lever than the
  string that had just been removed.* Dropping `hw` from `edgeCapacityOf` left `concurrentTPS`,
  described everywhere as the MEASURED input. It is measured off `rec.CompletionTokens`, **the
  node's own claim**, on a line three below the block that computes
  `billedCompletion = min(claim, broker re-count)` and settles money on it — under a log that
  prints `(billed/claim)`. One receipt field fed two decisions and only the money half was
  clamped, so an over-reporting node was billed honestly and *ranked* dishonestly. Measured by the
  review at 1.88x placement advantage at load 1 rising to 6.00x at load 8, with the P2C tie-break
  moving 1.000 against 0.062; reproduced on the EWMA itself at 561x, because `recordServed` has no
  prior to average against and one served-under-load request sets the estimate outright. The
  under-load gate was never the hole — it stops an idle canary, and says nothing about the token
  count inside a genuinely concurrent request. Both the relay and the STREAM paths now use the
  re-counted figure; a capacity input verified on one path and self-declared on the other is one
  with a `"stream":true` bypass, and streaming is the path most traffic takes.
  **The residual, stated:** `settleRecount` fails OPEN when the tokenizer sidecar is disabled or
  unreachable, so on a broker with no re-count capability the billed figure *is* the claim and
  this signal is exactly as trustworthy as the billing on the same request. That is a property of
  the deployment, and it is the trade the money already makes there.
  Re-audited while fixing it: every other edge-placement input (`edgeQuality`'s trust reading,
  `edgeLoadLocked`, the peer-load snapshot) is broker-observed, and `concurrentTPS` has exactly
  one writer. But `updateTPS` has a third caller — `recordProbe` — and it had the same defect:
  a canary's tok/s came from the node's claimed `CompletionTokens`, feeding `b.tps`, which
  `pickFor`'s `speedFit` ranks on and the `minTPS` filter drops on. A canary asks for one bare
  word, so claiming ten thousand output tokens for a five-letter answer moved that band by three
  orders of magnitude. Bounded by the zero-doubt byte floor (no tokenizer can emit more tokens
  than the text has UTF-8 bytes), the same defence `settleRecountPrompt` already applies to the
  input axis.

- *Seven days unseen was a PERMANENT loss of the Station identity, and scoping the sweep did not
  change that.* Round three stopped `DetachIdle` judging rows it could never find evidence for.
  It did nothing about the rows it CAN see going quiet for ordinary reasons: the state it assigned
  was `StateDetached`, which is terminal and unrecoverable, and the very next `roger share`
  re-attaches with the same persistent on-disk identity — same id, same keys, by design — and got
  "this Station ID has been retired and cannot be reattached", with the row not even freed for a
  month. A two-week holiday did it. A fortnight of downtime did it. And because the stamp has
  exactly one writer, a liveness mirror broken for a week on the instance holding a Tower's link
  retired **every** self-attached Station behind that Tower, with nobody deciding anything. A
  single thin dependency in front of an irreversible action is the wrong shape whatever the
  dependency is.
  **Split into soft and terminal.** The idle sweep writes `StateDormant`: out of service — not
  live, not published, not in the Tower's node list, not counted against the owner's cap, routed
  nowhere — and RECOVERABLE by the machine that holds its keys. `RetireDormant`, a separate
  fleet-wide pass on a horizon an order of magnitude longer (180 days), is where an identity
  ends without an owner asking; `Revoke` is where an owner ends it immediately. Both horizons
  measure the same `COALESCE(last_routable, attached_at)`, so they are two points on one timeline
  rather than two timers that can disagree.
  A dormant Station **keeps its keys reserved** (`Attachment.Held()`, and a partial unique index
  over the same three states). The assertion key is public and rides in the clear on every poll,
  so if dormancy freed it, anyone watching the link could bind it elsewhere and the rightful
  owner's return would be refused for a key they never gave up — recovery a stranger can block is
  not recovery. Waking is narrow: dormant state, same owner, same origin kind, both keys. The
  epoch advances (a revival may land on a different Tower, and the epoch is *meant* to be the
  fence — see §6.6: it is minted, signed into the grant and stored on the dispatch record, and it
  is compared against nothing at settle, so the fence is a field and not yet a check), the audit
  proof carries forward (a fact about the machine, not the attachment), and `last_routable` is
  cleared (or the fresh attachment would sit past the idle horizon and be swept again seconds
  after coming home).
  Found while fixing it: the Mem store's `Admit` refused an existing Station ID only when the
  existing row was LIVE, so a terminal row was silently replaced in memory while Postgres refused
  the insert on its primary key — a parity divergence `checkBindings` happened to hide
  sequentially. And the STAMP predicate (`Model != "" && SelfAttached()`) and the RETIRE predicate
  (`node_id <> ''`) were not the same set: a row with a node id and no model would be skipped by
  the stamp and judged by the sweep. Unreachable today because self-attach requires a model — and
  fixed anyway, because "a sweep may only judge a row it could have found evidence for" is only
  true while the two predicates are one predicate, and the previous version of this defect was
  also unreachable right up until the classic invite flow made it reachable.

**M2 — Collect locality.**
A relay's advertised endpoint gets a coarse location, set by Core at admission (never
self-declared — see §4). A node gets one at registration, resolved from the connecting IP
rather than the `--region` string, which stays cosmetic. Carry both into `tower_routable`.

**M3 — Move the binding to dispatch. NOT INDEPENDENT OF M4 — see section 6.**
Split "which relay" out of `towerEdgeAttach` and into the authorize path. Attach becomes
"this node is servable"; the tower field on the attachment becomes a default, not a
commitment. `edgeTargetFor` gains the consumer's location and picks the relay per request.

*Corrected 2026-08-20.* This entry was written as if it could ship on its own and it cannot.
Choosing a different relay at dispatch is meaningless unless the node can be REACHED at that
relay, and being reachable at a relay it did not attach to is exactly what M4 is. A node attaches
once per share and polls that one hub; the tower is not a variable at dispatch time, it is a
property of the station (`attach.Origin.TowerID`, enforced at placement by `targetFromAttachment`
and again at settlement). Section 6 works this through, costs the three candidate shapes, and
gives the order the two must actually land in - which also turns out to include a third piece
neither entry mentions: the relay has to know the station's assertion key BEFORE the consumer's
request arrives, and today it learns that from a 30-second pull.

**M4 — Ephemeral hub sessions. A PREREQUISITE OF M3, NOT A SEQUEL — see section 6.**
The node's long-poll response can carry a "serve this attempt at relay R" instruction; the
node opens a hub session to R for that work and closes it. Requires the hub to accept a
short-lived, per-attempt credential rather than only the long-lived `HubToken`.

*Corrected 2026-08-20.* M0 built the channel this needs and the entry predates it: every node now
holds an ordinary broker long-poll (`/agent/poll`, 25s, bus-backed and single-delivery across
instances), so Core already has an authenticated sub-second push to every node in the fleet.
What M4 still owes is the per-session state - the epoch, the nonce ring, the latch, the pin and
the key registration - and section 6.5 is the itemized list of what each of them costs when it
stops being per-tower.

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

   *Corrected 2026-08-20.* The last clause is wrong about M3, which cannot ship without M4 at all
   (see section 6). It is right about M1 and M2. The empirical question stands and section 6.9
   slice 2 is how to answer it - a shadow session against the tower the node is already attached
   to, measured before any traffic depends on the answer. Section 6.4 costs it in advance: one
   node-to-relay round trip, provided the registration problem is solved in the grant rather than
   with a call to Core.

---

## 5. Decided: signed hub polls, and a channel that can now be encrypted and verified

**Status: DECIDED and IMPLEMENTED — option B of the three below, founder-approved, landed on
`release/v5.7.0`, and AMENDED FOUR TIMES after review (5.4b, 5.4c, 5.4d, and 5.7 — which is
option A, the one this section spent four rounds calling deferred).** This section was written up
as NEEDS A DECISION; it is kept as the record of what was decided and why, because the reasoning
is the part a later reader needs — including the parts of it that were wrong, which by now is
most of the first draft's confident sentences.

~~the credential is gone; the channel is still plaintext~~ was this section's title through round
four. **Option A shipped in round five (5.7): a Tower advertises the fingerprint of its hub's
certificate, Core relays it, and a node and a consumer accept that certificate and no other.** It
is not mandatory, so "the channel is plaintext" remains true of every relay whose operator has
not turned TLS on — which is why the property list below now states each item TWICE, once for a
pinned link and once for an unpinned one, rather than replacing one blanket claim with another.

### 5.0 What signed hub polls actually buy, with every qualifier attached

This list exists because each round of review has found the previous round's summary to be
*narrower in reality than in prose*, and a compressed claim is how the next reader inherits a
hole. Read it as the security statement; everything below is the working.

**Every item now has to be read against ONE OF TWO CHANNELS**, because since 5.7 a hub link is
either *pinned* (the Tower advertises its hub certificate's fingerprint, Core relays it, the node
and the consumer accept that certificate and no other) or *unpinned* (plaintext, exactly as
before). TLS is not mandatory and is not scheduled to become mandatory in this release, so both
channels are live in the fleet at once. Where an item differs, it says so; **where it does not
differ, that is the more important sentence** — TLS is defence in depth here, and several of the
properties below were bought by the in-band work and would survive its removal.

1. **A serving node transmits no reusable credential** — with one exception, `--hub-legacy-bearer`
   (default on), which is honoured only for a Station that has never signed to that tower and is
   deleted one release from now.
2. **A captured request cannot be replayed** at the same hub process, for any route, bounded by a
   per-Station nonce ring that remembers for at least as long as a timestamp is accepted.
3. **A captured request cannot be carried to a different tower**, because the tower id is in the
   signed target.
4. **A captured request cannot survive that hub's restart**, because the hub *process* is in the
   signed target too.
5. **The epoch a node signs over is the hub's value, not the answering party's** — the hub proves
   it with the identity key Core admitted it under, bound to the request's own nonce, and the node
   checks it against the fingerprint Core hands it at attach. Before this, a forged `401` on the
   plaintext link made a node mint a genuine signature over any epoch an attacker chose.
   **Unchanged by TLS, and deliberately not conditional on it.** TLS closes the forged 401 from a
   third party as a side effect, but the proof is still demanded on a pinned link: making an
   in-band defence dependent on the transport would mean the fleet's unpinned half quietly lost
   it, and it is the half that needs it.
6. **One hub process per endpoint is a hard deployment constraint, not a preference.** Two live
   processes behind one endpoint still let an on-path attacker harvest usable signatures, by
   relaying rather than by forging. The client now *detects* that configuration exactly and stops;
   it does not make it safe. See 5.4d. **TLS narrows this and does not close it**: on a pinned
   link the harvested signature is no longer readable by a party on the path, but both processes
   hold the same certificate, so each still refuses the other's signature and leaves it unconsumed
   at a peer that would accept it. The deployment stays unsupported.
7. **Pre-auth work is bounded by possession and by a connection cap.** The body of `/complete`
   and `/audit/transcript` must be read before it can be authenticated; admission now requires a
   signature rather than a public identifier, and the listener caps concurrent connections at 512.
8. **A stolen bearer does not come back after a redeploy**, because the signed-latch is durable.
   It still works for a Station whose node has never signed — that is the tolerance, and it is
   what the flag turns off. On a **pinned** link a bearer from a node too old to sign is at least
   no longer readable off the wire; it remains readable by the tower, and the tolerance is deleted
   next release regardless.
9. ~~**The channel is still plaintext, and always was.**~~ **The channel is plaintext unless the
   Tower's operator has turned TLS on, and TLS is now a thing they can turn on.** Content is
   sealed end to end either way. On an **unpinned** link traffic shape leaks and every poll puts
   the Station's long-term assertion **public key** in the clear — a stable identifier linking
   that identity to an IP address across networks and towers. On a **pinned** link both are
   inside TLS 1.3: an observer sees a connection to the tower and its byte counts and timings,
   and nothing else. Option A is DONE (5.7) and NOT REQUIRED; the fleet will run mixed until a
   deadline is set.
10. **A node cannot authenticate the hub for anything except its epoch — on an unpinned link.**
    The epoch proof is the only statement a relay makes that such a node can check. Everything
    else the relay says — a job, a 204, a 401 — is taken on trust, and **anyone on the path can
    forge those answers**, not merely the relay. On a **pinned** link the whole channel is
    authenticated to the certificate Core named, so response injection by a third party is gone
    and every answer is attributable to the relay itself.
    **And since re-attachment landed, an injector on an unpinned link can hold a node on a dead
    relay indefinitely.** The re-attach trigger deliberately excludes a `401` — for an honest hub
    that is right, because attach is idempotent and Core would answer with the same tower and the
    same refusal from every mismatched pair in the fleet — but the exclusion is a decision made
    about an answer this node cannot attribute. A party on the path who feeds a node `401`s (or
    `204`s: an empty long poll is not an error and reports nothing) suppresses the standing-failure
    streak entirely, so the node never asks Core again and never learns that its relay moved.
    The suppression is not new; what is new is that there is now something to suppress. It is one
    more item on the list of things a pinned link buys and an unpinned one does not, and it is
    another reason 5.7's recommendation is the direction of travel rather than an optional extra.
11. **A HOSTILE RELAY IS UNCHANGED BY ANY OF THIS, pinned or not.** It can refuse to serve a node,
    drop its work, or lie about what it saw; the sealed envelope is what makes that a denial
    rather than a theft. TLS authenticates *which* relay is answering. It has never had anything
    to say about whether that relay is honest, and a reader who takes "verified" for "trusted"
    has inherited the next hole.
12. **A pinned link does not check a hostname, a chain, or an expiry**, and that is the design
    rather than a shortcut. The question a node needs answered is "is this the tower Roger Core
    assigned me?", not "is this relay.example?"; the pin answers the first directly and the Web
    PKI only ever answered it by proxy. What withdraws trust in a key is Core ceasing to advertise
    its fingerprint, on the same channel that distributed it — see 5.7.

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
forms; ~~the expensive half is that a Tower operator now needs a certificate for a name the node
will verify, which is a real operational burden on volunteers.~~

**THE EXPENSIVE HALF WAS IMAGINARY, and believing in it is what deferred this for four rounds.**
It assumed the node would verify the certificate the way a browser does — a chain to a public
root, for a name in DNS — which is the one form of verification a volunteer on a dynamic address
cannot supply and, more to the point, *the wrong question*. A node does not care whether it is
talking to `relay.example`; it cares whether it is talking to the tower Core assigned it, and
Core can simply say which certificate that is. The change that shipped (5.7) needs no certificate
authority, no domain name, and no new key material, and the accurate cost of it is one additive
string field carried through the same six places the endpoint already travels. The list of
touched components above was right; the conclusion drawn from it was not. Note also that the
URL-shaped variant of A stayed rejected: the sibling field is what makes it backward compatible.

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

~~**A is not closed and is not scheduled.**~~ **A shipped in round five — see 5.7.** The
paragraph that followed was right about the residual and wrong about its size: "the Tower
operator can see it in any case" is true of the tower and says nothing about the *thousands of
other parties* on a home operator's path, and "a node cannot tell it is talking to the hub Core
named" was written as a wistful note rather than as what it was, an unauthenticated channel a
node reasons about its own pay over. What stands is the last sentence, and it is the reason the
order was right: **those were reasons to do A eventually; they were never reasons to hold B.** B
is what makes a captured request worthless, and it needed no certificate on anybody's box.

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
| the hub restarts or redeploys inside the skew window | the gate refuses anything signed before this process started — **wrong in both clock directions; see 5.4c** |
| Core's answer briefly omits a Station, so the refresher unregisters it and its ring is dropped | unregistering leaves a floor behind: the memory goes, the refusal stays |
| a signature carried to a different Tower that has the same Station registered | the Tower id in the signed target |
| **two hub processes answering one endpoint** | **nothing when this was written. 5.4c claimed the per-process epoch closed it. THAT CLAIM IS FALSE — see 5.4d; the client now detects the configuration and stops, which is not the same thing** |

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

### 5.4c Round three: the process-start floor was a clock, and clocks were the thing

The gate's last remaining defence against a redeploy was `nonceGate.since = time.Now()` - refuse
anything stamped before this process began. It compared a **tower wall clock** to `ts`, a
**node-domain unix second**, which is precisely the mistake the ring's own eviction floor
documents avoiding one field below it ("these are request timestamps, not wall clocks, so a node
with a consistently offset clock is measured against its own past"). It failed in both
directions, and the row in 5.4b's table that reads "the gate refuses anything signed before this
process started" was true only of a node whose clock was level or behind.

- **A node leading by L kept its captured signatures replayable for L seconds after every
  restart.** Proved with a 60-second lead: replay after a redeploy returned 200 with the job -
  the dequeue-the-victim's-job attack, surviving the change that closed it. This section argues
  at length that leading clocks are ordinary, which is what makes it the reachable case rather
  than the exotic one. (A perfectly synchronised node had a sub-second window too, from the
  `Truncate(time.Second)` that was added so the floor would not refuse requests signed in the
  second the process started.)
- **A node lagging by L was refused for L seconds after every restart, and told it was a
  replay.** Up to `SigMaxSkew`, five minutes of an honest operator not earning per deploy - the
  harm this whole change exists to prevent, and the stated reason for not refusing future
  timestamps, applied in one direction only. The 401 read "this exact request has already been
  made", for a request the node had never made, and the test pinned that wording.

**The comparison cannot be repaired.** A signature's only tie to time is the timestamp its signer
chose, so a fresh request from a node leading by L and a stale one from a node leading by L plus
its age are the same bytes making the same claim; separating them needs memory of that node from
before the restart, and a restart is the loss of exactly that memory. Persisting a per-station
high-water mark would work and was considered - it is the only alternative that does - and it was
rejected for putting disk state, and a crash window, into a hub that deliberately holds none.

So the process is named in the signature instead. `towerhub.Server` mints a random **epoch** per
run, every signed request carries it in the target as `?hub=`, and the hub publishes it on every
node-facing response in `X-Roger-Hub-Epoch` so a client that does not know it yet learns it and
re-signs. This is the same move the tower id made one level finer, and it costs one extra round
trip per hub restart against a poll every twenty-five seconds. No clock is consulted, so there is
nothing left for a clock to be wrong about, and 5.4b's residual about the ring floor is now the
only refusal in the gate that turns on a timestamp - which is why it has been given a sentence of
its own instead of borrowing the replay one.

~~It also closes the row 5.4b's table left open. **Two hub processes behind one endpoint** now have
two epochs, so a signature made for one is refused by the other rather than silently replayable
at it. That configuration is still unsupported and will flap; it now fails closed.~~

**THAT PARAGRAPH IS WITHDRAWN. It is false, and it is false in the direction that matters.** Two
epochs do mean each process refuses the other's signature — and a refused signature is an
*unconsumed* one, sitting in the clear on a plaintext link, still valid at the process it was
made for. The flap does not close the hole; it MANUFACTURES the material the hole needs. Measured
with no attacker present at all: 7 of 12 recorded requests were signatures for a hub that never
saw them, and replaying one at its matching hub dequeued a submitted job. "Fails closed" described
each individual 401 and missed what the pile of them added up to. See 5.4d.

There is no compatibility cost: signed polls have not shipped in any tagged release, so both
halves of the wire change land together.

One smaller correction to this section, because the next person to weigh the same option deserves
the real constraint. Persisting a per-station high-water mark was rejected here for "putting disk
state, and a crash window, into a hub that deliberately holds none". **The hub already holds disk
state**: `cmd/roger-tower/spool.go` writes every pending receipt to the tower's data dir precisely
because losing one costs a node its pay, and 5.4d adds the signed-latch beside it. The honest
objection to a persisted high-water mark is that it is a *clock* in the node's domain that a hub
would have to trust and could never re-derive for a Station it has not seen — not that the hub is
stateless, because it is not.

### 5.4d Round four: the epoch was the attacker's value, the cheap door opened on a public key, and the two-process row was never closed

A third independent review — adversarial this time, with executable reproducers against the real
`towerhub.Server`, the real `Client` and the real broker — went over everything 5.4b and 5.4c
landed. The hub-side checks all hold. What it found is that two of them were checking a value
somebody else got to choose, and that one row in 5.4b's table had been marked closed by a
paragraph rather than by code.

#### The epoch's PROVENANCE, which nothing established

`signedDo` answered **any** `401` by caching whatever `X-Roger-Hub-Epoch` said and re-signing.
That `401` is unauthenticated and the channel is plaintext by construction. So an on-path
attacker answering a node's poll with a forged `401 + X-Roger-Hub-Epoch: <anything>` made that
node emit a **genuine Ed25519 signature over a target naming an epoch of the attacker's
choosing**, with a fresh nonce and a fresh timestamp — bytes no hub has seen, and therefore an
*unconsumed* signature rather than a replay, which is the one thing the whole gate is built to
refuse. The poisoned value then stuck in `c.epoch` for every worker sharing the `Client`.

The epoch CHECK was exact. Everything the epoch bought was conditional on the `401` being honest.

**The hub proves its epoch now, with the identity key Core admitted it under.** No new key is
enrolled or distributed: `roger-tower` already holds that key, Core already keeps its fingerprint
and verifies every one of the tower's own requests against it, and the attach response now hands
that fingerprint to the node beside the tower id and the endpoint it already trusted Core for.
The hub signs `(label, tower id, epoch, THE NONCE OF THE REQUEST BEING REFUSED)` and publishes
the key and the signature on every node-facing answer; a node refuses to adopt an epoch it cannot
check.

The nonce is in the statement deliberately. Without it the proof is a bearer token for an epoch —
captured once before a redeploy, replayable forever to point a node back at a dead run. With it,
a proof answers one request and cannot be stockpiled.

**Not TLS, and not because TLS is wrong.** TLS is the complete answer and it is a separate change
(option A, §5.2); making this wait on it would leave the hole open for tidiness. Core is already
the node's source of truth for the tower id, the endpoint and the grant key, so one more field on
the same response needs no new trust and no certificate on a volunteer's box.

**A node whose Core is too old to send the fingerprint refuses to attach**, rather than carrying
on unverified. Same posture as the credential itself: a downgrade an attacker can provoke is not
a security property. Signed polls have not shipped in a tagged release, so nothing in the field is
stranded — and the deployment order was already written down below: Core, then Towers, then nodes.

#### Two hub processes: what is actually true

The ACTIVE attack the review demonstrated — read hub B's epoch off an unauthenticated `GET /poll`,
assert it to the node in a forged `401` in front of hub A, capture the re-signed poll, replay it
at B for a `200` and the victim's job — is dissolved by the fix above. An assertion is not a
proof.

**What survives is relaying, and it needs two live processes to exist.** An on-path attacker can
forward the node's own request to hub B and hand back B's genuine, nonce-bound answer; the node
then signs for B's epoch and the attacker replays that at B. Nothing about the epoch's provenance
prevents this, because B's answer is not forged.

And the PASSIVE half needed no attacker at all, which is the part 5.4c got backwards: a
round-robin load balancer over two hub processes makes the client flap between epochs, and every
request that lands on the process it did not sign for leaves an unconsumed genuine signature for
the other one, in the clear. The flap *manufactures* replay material — arguably a worse state than
before the epoch existed.

**So the client detects the configuration exactly and stops.** An epoch is 128 bits of
`crypto/rand` minted once per process, so a hub that RESTARTED can never name an epoch this client
has already moved off — only a second live process can. The client keeps a bounded set of retired
epochs and fails with `ErrHubMultipleProcesses` when one comes back, which reaches the operator on
the notice channel. There is no false positive available to anybody, and it fails closed after two
requests instead of flapping all day.

**This is detection, not a fix.** A tower that needs to scale still needs a shared nonce gate
first. The constraint stated in 5.4b — *a Tower runs exactly one hub process per endpoint* — is
unchanged and is now enforced by the node refusing to serve rather than by hoping nobody tries.

#### The cheap door opened on a public identifier

`knownCredential` was added in 5.4b to stop an unauthenticated stranger making the tower buffer
16MB before being told no. It asked the only question headers can answer — *is this
`X-Roger-Pubkey` a key we have registered for somebody* — and that question has a free answer. The
assertion key is on the plaintext wire on every poll (the node's own notice channel warns the
operator that it is), and a hostile Station on the same tower holds a registered one **by
definition**: its own. Measured at 12,582,947 bytes buffered pre-auth on `/complete` presenting
nothing but the victim's public key. With no connection cap and a two-minute read timeout, that is
a memory-exhaustion denial of earnings against every Station on a tower for the price of one
connection.

The index change 5.5 describes did fix what it targeted — the CPU-and-lock amplifier — and left
the memory one open, because the question was still the wrong question.

**The door asks for POSSESSION now.** A *door signature* rides beside the request signature: the
same key over the same method and target with **no body**, which is the only proof available
before a body has been read. It is domain-separated by the method it covers, so neither signature
can be presented as the other. It is deliberately NOT recorded in the nonce ring — the ring is
bounded by "nothing is stored until a signature has verified against a named Station", and a
pre-auth write would hand an attacker exactly the growable map that ordering denies them. So a
door signature IS replayable inside the skew window by someone already on the path, who could
equally drop the packet, and by nobody else.

The bearer half of the door cannot be improved: a node old enough to present a token cannot
produce a door signature, so requiring one would be `AllowLegacyBearer=false` in disguise. What
bounds it is that a token is a secret rather than a public identifier, and that the path is
deleted one release from now.

**And the listener has a connection cap**, which 5.4b explicitly left open ("that is a separate,
smaller exposure and it is not fixed here"). Two pre-auth costs cannot be removed — the body read
that must precede authentication, and the Ed25519 epoch proof on every node-facing answer — and
both are per-connection. 512 concurrent, generous against any real tower's poll workers. Excess
connections *wait* rather than being refused: on a hub whose purpose is letting providers earn, a
poll delayed is a poll and a poll refused is a node that stopped serving.

#### A restart reopened the stolen bearer

The latch that retires the bearer per Station lived in one process's memory, and Core never
rotates `HubToken` — the same value, every re-attach, for the life of the attachment. So after
every redeploy, a token lifted off the wire before that node upgraded opened its queue again. The
window was not one round trip: the node's first post-restart request carries the old epoch and is
refused, so the latch closes on its SECOND request, and (before the fix above) an attacker could
hold that window open at will. The honest statement was *"a bearer captured before a node upgraded
works again for a window an on-path attacker controls, every time the tower redeploys"*.

The latch is durable now — a directory under the tower's own data dir, one file per Station, so
an `Add` is a create with no read-modify-write. The objection recorded when the latch was written
("no operator should have to migrate a database row for a credential that is being deleted") still
stands and is not what this is: the hub already writes receipts to that same directory, because
losing one costs a node its pay. Losing this costs a node its queue. Best effort both ways, and it
never refuses an honest request for a full disk.

**`--hub-legacy-bearer` stays default true, and the reason has changed rather than been
restated.** It was defensible before only if you accepted that every redeploy reopened the hole.
It no longer does, so the population the flag covers is now exactly the population it was written
for: nodes that have never signed to this tower. An operator who knows their fleet has updated
still turns it off, and the whole path goes one release from now.

#### One more sentence corrected

The epoch refusal told a client that had sent **no** epoch that the hub "has restarted since". A
first request cannot carry an epoch — the `401` is the only way to learn one — so that sent
operators hunting a redeploy that never happened. Two causes, two sentences; the same correction
the nonce gate's two refusals got in 5.4b.

And `learnEpoch` reported "new" against the CACHE rather than against what the request had SENT,
so on a redeploy the first worker to notice learned the epoch and retried while every other worker
hard-failed. Seven of eight, measured. In `ServeLoop` each is a 2s backoff plus an
`agent.ErrHubRefusedThisNode` notice — an operator-facing "your relay refuses this node's identity"
alarm on every routine redeploy, fired on the one channel deliberately built not to be
discardable. The retry now turns on the worker's own request, and the second answer's epoch is
learned too.

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

  **"Neither an attacker holding the token nor one on the path can produce the signature that
  flips the latch, or unflip it" was false, and so was "the only thing that clears it is the
  Station being dropped by Core".** Two events cleared it, and neither is an attack because
  neither has to be.

  `UnregisterNode` deleted the latch outright - and the hub's refresher unregisters any Station
  missing from a SINGLE answer from Core, re-registering it on the next tick. That is the exact
  occurrence the nonce ring's tombstone was added for, and 5.4b calls it "entirely outside
  anybody's control": the ring got a tombstone, the latch got a plain delete. Since Core never
  rotates the token, one bad refresh handed a stolen bearer its whole life back.
  And `RegisterNode` cleared it whenever the incoming assertion key differed from the one held,
  on the reasoning that a different key means a different node. That trigger is unreachable -
  `checkBindings` makes a Station's assertion key immutable for the life of the Station ID, and a
  retired ID can never be reattached - so the branch existed in practice only to be tripped by
  the one thing that produces a differing key without a different node: a key Core sent that
  would not decode, which `registerHubNodes` drops to nil while registering the token beside it.
  Both were proved end to end: 204 on a signed Station's queue with the stolen bearer.

  The latch is **set-only within a process** now. Nothing deletes from it, because every event
  that did was a registration flap rather than evidence about the node. It costs a bool per
  Station id this tower has verified a signature from - a set only the holder of that Station's
  private key can add to.

  And an **unusable** assertion key no longer registers a bearer alongside it. The two cases were
  conflated: an EMPTY key describes the old node the tolerance was written for and still gets its
  token, while a key that is present and will not decode describes corruption on a Station that
  certainly has a good key at Core. Honouring a bearer there opens a queue, on a plaintext link,
  to a string any on-path observer holds, for a Station that can no longer authenticate any other
  way - so nothing would ever flip the latch that closes it again. It fails closed and says so
  once.

  One more, from the same audit and on the same door. `knownCredential` - the cheap door added to
  stop an unauthenticated stranger making the tower buffer sixteen megabytes - scanned every
  registered Station calling `hex.EncodeToString` per station, under the read lock `authNode`
  needs. Measured at 128KB of allocation per bogus request on a thousand-station tower: a
  CPU-and-lock amplifier standing where a memory amplifier used to be, which is not closing one.
  The hex is precomputed at registration and indexed now.

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
credential. ~~TLS (option A) remains deferred, and the endpoint wire format stays `host:port`.~~
**Half of that is still true and the half that matters is not:** the endpoint wire format is
still `host:port`, deliberately — but TLS shipped in round five (5.7) as an additive pin carried
beside it, and what remains deferred is not the capability, only the decision to require it.

Still open after round five, and worth naming so the next reader does not have to re-derive them:

- ~~**TLS (option A).** Unchanged, and it is what closes the remaining plaintext leaks…~~
  **DONE in round five (5.7), and it did not close traffic shape** — which the bullet promised it
  would. TLS hides the Station's assertion public key and authenticates the relay's answers; it
  leaves the timing and the byte counts of a long-polling connection exactly as visible as they
  were. What remains open here is not the capability but the DEADLINE: TLS is optional, most of
  the fleet is still plaintext, and whether and when to require it is a founder decision with the
  migration cost written out at the end of 5.7.
- **A shared nonce gate.** Until there is one, a Tower runs exactly one hub process per endpoint.
  The node now refuses to serve a flapping endpoint rather than manufacturing signatures for it,
  but that is detection, not scale.
- **The door signature is replayable on-path** inside the skew window, deliberately: recording it
  would make the nonce ring attacker-growable, which is the one property that bounds it. It costs
  an on-path attacker one buffered body per captured request, and an on-path attacker can drop
  the request instead.
- **The re-count fail-open.** A broker with no tokenizer sidecar bills, and now ranks, on the
  node's claim. The two are consistent; neither is verified.
- **Key squatting on an unattached assertion key.** `/tower/edge/attach` takes the assertion key
  from the request body and does not require a signature from it, so a party who learns a key
  before its owner first attaches can bind it to a Station of their own. Pre-existing, unrelated
  to dormancy (a dormant Station's keys are reserved), and the obvious fix — require the attach
  to be co-signed by the assertion key — is a wire change on a path §5.4d has already touched
  once this release.

### 5.7 Round five: option A, and why it needed no certificate authority

**Status: DECIDED and IMPLEMENTED, founder-approved, on `release/v5.7.0`. NOT MANDATORY — see
"should it be required" at the end, which is a founder decision this section does not take.**

#### What was actually broken

The Tower could already serve TLS and no node could ever use it. `cmd/roger-tower/serve.go` had
`--hub-tls-cert`/`--hub-tls-key`, `internal/tower/config.go` had `Hub.TLSCert`/`TLSKey`, and
`cmd/roger-tower/hub.go` called `ServeTLS`. But a Tower advertises its data plane as bare
`host:port` in `link.Hello.RelayEndpoint`, both ingress points validate that with
`net.SplitHostPort` (which rejects anything containing `://`), and so `internal/agent`'s
`hubBaseURL` had exactly one reachable branch: `"http://" + endpoint`. **An operator who obtained
a certificate got a TLS listener that every node in the fleet connected to in plaintext and
failed against.** The flags were not a path to safety; they were a trap, and the more diligent
the operator the worse it treated them.

#### The decision: pin the hub's certificate through Core

A Tower computes the SHA-256 of the **SubjectPublicKeyInfo** of the certificate its hub presents
and advertises it in its `Hello` (`relay_tls_spki`, additive, absent meaning plaintext meaning
today's behaviour). Core stamps it onto the routable projection beside the endpoint and hands it
to both parties that dial the hub — the serving node at `/tower/edge/attach`, the consumer at
`/tower/edge/authorize` — as `endpoint_tls_spki`. Each dials `https` and accepts **that
certificate and no other**: no chain, no hostname, no expiry.

**There is deliberately no separate "does this hub speak TLS" boolean.** The pin is the
advertisement, so the state the whole change exists to prevent — a TLS listener whose clients
cannot check it — has no representation on the wire. It is also why the endpoint format is
untouched: a URL would have been a breaking change to a field three clients concatenate onto.

`roger-tower serve --hub-tls` with no certificate files mints an Ed25519 self-signed certificate,
keeps it in the data directory, and advertises its fingerprint. That is the whole operator
experience: one flag, no authority, no domain, no renewal.

#### Why not the alternatives

- **Require a publicly-trusted certificate.** This is the option the design assumed for four
  rounds. It would not have made volunteer towers secure; it would have made them *ineligible*.
  A home connection on a dynamic address with no domain cannot obtain one at any price, so the
  policy silently restricts Towers to operators who already run infrastructure — against the
  point of the programme. And it answers the wrong question: a public certificate proves control
  of a **name**, which is only ever a proxy for "the tower Core admitted", and the name in
  question is one the tower itself asserted. The pin answers the real question directly, and does
  it without trusting ~150 certificate authorities not to mis-issue.
- **Support both.** Rejected as a false generosity. It doubles the verification matrix, splits
  the fleet into two classes of operator, and adds a second trust root that is strictly weaker
  for this purpose than the one already in hand. An operator who *has* a publicly-trusted
  certificate loses nothing: `--hub-tls-cert` still works, and the pin is computed from whatever
  certificate they supplied.
- **ACME.** Same domain requirement as the above, plus renewal machinery, plus port-80/DNS
  reachability, plus a dependency, inside a binary that volunteers run on home boxes. It solves a
  problem the pin does not have.
- **Bind the certificate to the tower's IDENTITY key** (make the SPKI *be* `admit.Tower.KeyHash`,
  or attest the certificate with an identity-key signature). Seriously considered, and it is the
  most elegant version: no new field at all, since the node already receives `tower_key_hash`
  (commit `6480cd05`). Rejected on two counts. It forces the tower's long-term identity key — the
  key that authenticates every request it makes to Core, including settlement — into the TLS
  stack, collapsing a separation `admit.Tower` maintains on purpose ("a stolen TLS key proves
  nothing about its identity"). And it forbids an operator from using any certificate they did
  not mint from that key, including a corporate or ACME one. The pin costs one string on a
  message that already carries the endpoint, and Core is already the node's source of truth for
  that endpoint — so it adds no trust root that was not already load-bearing.

#### What this closes, and what it does not

**Closes, on a pinned link:**

- **Response injection.** Nothing authenticated the hub's answers, so anyone on the path could
  forge the status codes a node reasons about its own pay with — a `204` "nothing for you" while
  real work went elsewhere, a `401` that reads as a revoked attachment. Now every answer is
  attributable to the certificate Core named.
- **The Station's payment identity in the clear.** `X-Roger-Pubkey` carried the long-term
  assertion public key on every poll: stable for the life of the station, the key its receipts
  are verified against and its earnings paid to, and therefore a linkable identifier tying that
  identity to an IP address across networks, towers and re-attachments. It is now inside TLS 1.3.
- **The forged-401 epoch attack, a second time and independently.** The in-band proof already
  closed it; a party who cannot speak on the channel cannot mount it at all.
- **The bearer still on the wire during the legacy-tolerance window**, for towers that turn TLS
  on before the tolerance is deleted.

**Does NOT close:**

- **Anything a hostile relay does.** The relay terminates the TLS. It can refuse to serve, drop
  work, or lie about what it saw, exactly as before; the sealed envelope is what makes that a
  denial rather than a theft. *Verified is not trusted.*
- **Traffic analysis.** An observer still sees a connection to that tower, its timing and its
  byte counts. Long polls and sealed bodies have shape.
- **Two hub processes behind one endpoint.** Both hold the same certificate. See 5.0 item 6.
- **The unpinned half of the fleet**, which is every tower whose operator has not turned TLS on
  and, until it is required, may always be most of them.
- **Certificate validity, revocation, and names.** A pinned client accepts an expired,
  nameless, self-signed certificate — that is the design, since the pin is what confers trust and
  Core ceasing to advertise a fingerprint is what withdraws it. The consequence to be honest
  about: **withdrawal is only as fast as re-attachment.** A node holds its pin for the life of
  its process, so a tower whose key is compromised is believed by already-attached nodes until
  they re-attach. There is no push revocation, and building one is a bigger change than this.
- **Off-box TLS termination.** A reverse proxy in front of the hub presents its own certificate,
  which is not the one the tower computed its pin from. Such a deployment must advertise
  plaintext today. The extension is one string field (an operator-supplied pin override) and is
  not built, because the load-balanced case it usually accompanies is already unsupported.

#### Should it be required? A recommendation, not a decision

**Recommendation: require it, but not in this release, and on a date rather than on a build.**
Make it work first — that is this change — then set a deadline, announce it to tower operators,
and enforce it at Core rather than at the node.

The mechanism, when the founder calls it: `towerEdgeAttach` and `towerEdgeAuthorize` refuse to
route to a Tower whose live session advertises no pin, and `publishRoutable` stops stamping rows
for one. Enforcing at Core rather than in the node's client is what makes it a fleet decision
with one switch and one revert, instead of a property of whichever binary each node happens to be
running.

**The migration cost, stated plainly:**

- **Every joined tower operator must act**, and the action is one flag (`--hub-tls`, or
  `hub.tls: true`) plus a restart. No certificate to obtain, nothing to renew.
- **A tower that does not act goes dark on the edge path** the day it is enforced: its Stations
  stop being routable and its nodes stop earning. That is the whole cost, and it is why it needs
  a date and an announcement rather than a release note.
- **Every node attached before its tower turns TLS on must be RESTARTED, and nothing in the
  product does that for it.** This was checked rather than assumed, and the assumption was wrong.
  `cmd/rogerai/relayfabric.go` calls `agent.ServeTower` exactly once per `roger share` process,
  and `ServeTower` attaches once: the endpoint and the pin are read at attach and held for the
  life of the process, while the serve workers retry a failing hub every two seconds forever.
  So a tower that restarts with TLS on strands every node already serving through it — they
  retry into a handshake that cannot succeed until their operator restarts the share. The same
  is true of any certificate ROTATION, which is the ongoing version of the same cost.

  It is at least no longer silent: a pin mismatch now goes to the notice channel with the
  instruction to restart, rather than looking exactly like a relay that is down. Two things
  would remove the cost rather than announce it, and neither is built: re-attaching on a
  standing hub failure, or a tower that keeps serving plaintext for a grace period beside its
  new TLS listener. **Whichever is chosen should land before a deadline is set, not after.**

  **UPDATE 2026-08-20: the first of the two is built, and this bullet is now historical.**
  `agent.ServeTower` is a loop over TENANCIES rather than a single attach: a relay that has been
  continuously unusable for ninety seconds - and a certificate that is not the one Core named,
  immediately, because that one cannot be ninety seconds of bad luck - ends the tenancy, and the
  node goes back to Core on a jittered exponential backoff for the relay's current advertisement.
  The endpoint, the pin, the tower id and the identity fingerprint are all re-read; the station
  identity, the transcripts, the attempt cache and Core's pinned keys are not, because they are
  facts about the machine rather than about the relay. So a tower turning TLS on, rotating its
  certificate, moving, or coming back on a different address costs its nodes a couple of minutes
  rather than a manual restart each, and **nothing in the migration now requires an operator to
  touch a node they did not know was affected.** The one failure deliberately excluded is a 401:
  the hub is answering and has refused this node, attach is idempotent for a live attachment, and
  Core repeating itself changes nothing - so that stays a notice with an instruction, which is the
  whole of the available remedy. The operator-facing pin-mismatch sentence no longer says "restart
  this share"; a ritual an operator learns outlives the reason for it.
- **Consumers need no action**: the pin arrives per authorization, and an unpinned authorization
  keeps working for as long as unpinned towers exist.
- **Off-box TLS terminators and multi-process hubs cannot comply** without the pin-override
  extension above. Anyone running one should be identified before a deadline is set; today they
  are already outside the supported deployment.

---

## 6. M3 and M4 are one change, and the money is where you find out

**Status: PROPOSAL. Written 2026-08-20 against `release/v5.7.0`, after re-reading the three
files that actually decide where a request goes.** Section 3 lists M3 ("move the binding to
dispatch") and M4 ("ephemeral hub sessions") as sequential, independent milestones. They are
neither. M3 cannot ship without M4, for a reason that is one sentence long and was not written
down anywhere: **choosing a different relay at dispatch time is meaningless unless the node can
be REACHED at that relay, and being reachable at a relay is exactly what M4 is.** Section 3's
entries have been corrected; this section is the working.

### 6.1 The three lines that make it true

Nothing here is inference. The path is:

- `cmd/rogerai-broker/toweredgeattach.go:207-219` picks the tower **first-fit** from
  `ts.link.LiveTowers()` at ATTACH time and bakes it into the attachment as
  `attach.Origin{Kind: OriginJoined, TowerID: towerID}`.
- `cmd/rogerai-broker/toweredge.go` `edgeTargetFor` ranks STATIONS and then reads `row.TowerID`
  off the projection row — i.e. whatever tower the PROVIDER landed on, hours before any consumer
  existed. `resolveEdgeCandidates` → `targetFromAttachment` (`towerdispatch.go:162`) then
  *enforces* that pairing: `if at.Origin.TowerID != towerID { return false }`.
- `internal/agent/tower.go` attaches once per share and its `ServeLoop` workers poll that one
  hub. (As of this branch it re-attaches on a standing hub failure — see §6.9 slice 2 for why that
  is the seed of M4 and not a substitute for it. What a re-attach can change is everything about
  the SAME tower — its address, its certificate pin, its identity fingerprint — because those are
  re-read from the tower's live link session. It cannot change the TOWER, for the reason this
  section is about: Core's attach handler answers a live attachment from its idempotent-retry
  branch and never re-runs placement, and no writer anywhere moves a live station's
  `origin_tower`.)

So a consumer in Lisbon asking for a node in Lisbon is dragged through whichever tower that node
happened to attach to, possibly in Ohio, and the placement code that would like to do better has
no other choice to offer: **the tower is not a variable at dispatch time, it is a property of the
station.** M3 rewrites the sentence that reads it. M4 is what makes a second value legal.

### 6.2 What the founder actually described

> "as soon as someone is going to tune-in ... this is when the connectivity magic happens ...
> only at that point do we know who is using it and what tower or broker is closest or should be
> better to serve the requests, and it should not be live forever, it should disconnect when
> done."

Three claims, and they are load-bearing in different places. *Only at that point do we know* is a
statement about information: the relay decision needs an input (the consumer) that does not exist
at attach. *Closest or better* is a ranking problem, which is M2/M5 and is not the hard part.
*It should not be live forever, it should disconnect when done* is the part that sounds like a
nicety and is in fact the mechanism: a binding that outlives the work is a binding that had to be
made before the work existed.

**And M0 already built the channel this needs.** Every node now holds an ordinary broker
long-poll (`GET /agent/poll`, `tunnel.go:1276`, held 25s) in addition to whatever it does on the
relay plane. In multi-instance mode that poll is served off a per-node bus channel
(`busSubscribeJobs`), with `busClaimJob` guaranteeing exactly one poller receives each message.
So Core already has an authenticated, sub-second, cross-instance push channel to every node in
the fleet, with single-delivery semantics, that nobody has to build. That is almost certainly the
"connectivity magic" the founder was describing, and it is the strongest argument for shape (c)
below.

### 6.3 The three shapes, costed

The unit of comparison is one consumer's first token on the edge path. Today's baseline:
`POST /tower/edge/authorize` (one round trip to Core, which returns the grant, the endpoint, the
pin and the station's session key), then one connect + submit to the relay. The node's presence
at that relay costs **zero** on the critical path because it was established hours earlier. Every
shape below is measured against that.

| | (a) multi-attach | (b) choose among the already-attached | (c) ephemeral session per attempt |
|---|---|---|---|
| **First token** | baseline (node already present everywhere) | baseline | baseline **+ ~1 RTT** worst case; see §6.4 |
| **Connections held per node** | N long polls × `Parallel` workers, standing, whether or not anyone tunes in | same as (a) — it *is* (a) | 1, and it is the broker poll the node already holds |
| **Connections held per tower** | N× the stations it serves, against a hard cap of 512 (`maxHubConns`) | same | only stations with live work |
| **State at Core** | N attachment rows per station; `Origin.TowerID` stops being singular, which is a schema and an identity change | same | 1 attachment (unchanged) + a per-attempt relay, which `dispatch.Record.TowerID` already is |
| **State at tower** | NodeAuth + nonce ring + latch + wanted-audits for N× more stations than it serves | same | the same objects, created per session and reaped with it (§6.5) |
| **Delivers "disconnect when done"** | no — it is the exact opposite | no | yes |
| **Ships without the others** | yes, and it is wasted if (c) later wins | **no: with today's single attach the choice set has one member** | yes |

**(b) is not a shape.** It is the second half of (a), written as if it were independent. With one
attachment per station, "dispatch chooses among the towers the node is already attached to" is a
loop over a list of length one. This is worth saying plainly because (b) is the version that
looks cheapest on a slide.

**(a)'s real cost is the tower's connection cap, not the node's sockets.** A hub caps concurrent
connections at 512 (`cmd/roger-tower/hub.go:34`), so it can host roughly 256 station-workers at
`Parallel: 2` — and multi-attach divides that by N. Fanning every node out to five relays does
not give the fabric five times the choice; it gives each relay one fifth of the fleet it could
otherwise carry, in exchange for choice that (c) provides for free. It also inverts the
economics: a volunteer's home connection would hold hundreds of idle long polls for traffic that
mostly goes somewhere else.

**Recommendation: (c), and do not build (a) as a stepping stone.** (a) is not a subset of (c)
and nothing built for it survives — the identity model change (multi-valued `Origin.TowerID`) is
work that (c) explicitly does not want, because in (c) the attachment stays singular and the
relay lives on the grant, where it already is.

> **SUPERSEDED IN PART, 2026-08-20 — read §6.3b before acting on this.** The founder's ruling
> keeps (c)'s *mechanism* (an ephemeral session opened at a relay and closed when done) and
> rejects its *cadence*: the placement is chosen per STATION and stays sticky, not per attempt.
> The costing above is unchanged and still the reason (a) and (b) are dead.

### 6.3b DECIDED: sticky placement with supervised mobility (founder, 2026-08-20)

**A Station keeps one relay binding. Core may move it only while the Station is IDLE, or when
the current relay is genuinely bad — failing, too slow, or gone.** That is the model. It is not
a weaker version of (c); it is a different answer to the question (c) was trying to solve, and
it is worth writing down what it trades.

**What it gives up.** Locality precision. A consumer in Lisbon reaching a node whose sticky
binding is in Ohio gets Ohio until the node next goes quiet, rather than being re-placed for the
duration of their request. §6.2's "only at that point do we know" is answered approximately — by
the last consumer to arrive while the node was free — instead of exactly.

**What it buys, and this is the trade the ruling is making.** The money surface shrinks to
almost nothing. Every hard problem in §6.6, §6.7 and §6.8 comes from the relay being a
*per-attempt* variable while the money is settled against *standing* facts. Sticky placement
removes the mid-flight case rather than solving it:

- The self-dealing lever §6.7 item 1 spends a section defending against — a consumer steering
  their own spend to a relay they own — needs a per-request choice to pull. There isn't one. A
  consumer's arrival can at most influence where an IDLE node is placed next, which is a much
  poorer lever and a much slower one.
- §6.8's ranked-two-relays grant, the second hold, the re-target state machine: not needed. The
  relay is known before the consumer arrives, exactly as it is today.
- The attribution question — who carried this attempt — stays answerable from standing state
  for the whole life of an attempt, because the binding cannot move while the attempt exists.

**And a move that catches live work is a FAILED DELIVERY, not an attribution puzzle.** No
partial payment, no paying the Station but not the relay, no courier tracing. The attempt is
void, nobody is paid, the consumer's hold refunds and the consumer retries. That is the policy
the epoch fence below implements, and it is what makes the fence four lines rather than a
subsystem.

**Two facts already in the tree make it cheap, and neither needed building.** *Idle is already
knowable*: `edgeLoadLocked(nodeID)` in `cmd/rogerai-broker/toweredge.go` sums
`b.inflight + b.peerInflight + b.edgeLoad`, and zero means no work in flight on either the
classic or the edge path — that is the quiescence signal a placement change gates on.
**With one qualifier that matters for M4**: `b.edgeLoad` is this instance's own map and
`peerInflight` is a periodically merged snapshot of other instances' *classic* counts
(`mergeSharedInflight`), so "zero" is zero as one broker currently sees it, not a fleet-wide
proof of quiescence. Good enough to *choose* to re-place; not good enough to be the only check
before a node acts on it, which is why §6.10 has the node re-check locally. *An
unsettled attempt is already handled*: the orphan sweep (`holdsweep.go`,
`store.ReleaseStaleHolds`) reclaims a consumer's hold after `holdTTL` with no reference to a
receipt or a dispatch row, and `edgeSettleGrace` is capped strictly under `holdTTL` so the hold
always outlives the settlement window. "The relay moved, cancel that request" is the existing
failure path.

**What is still open under this model.** *Bad* is a policy word and nothing defines it yet —
which failures, over what window, before Core re-places a node whose relay is degrading rather
than gone. And a re-placement still has to be delivered to a node that is not attached to
anything new yet, which is §6.10's instruction channel.

### 6.4 Where the round trip goes, exactly

The honest cost of (c) is not "a handshake". It is three specific things, two of which can be
made free.

1. **The consumer's submit must not arrive before the relay knows the station.** Today
   `Hub.Submit` refuses an unregistered station with `ErrNoStation` and the server answers
   **404** while kicking an async refresh (`OnUnknownStation`, debounced to once a second,
   `cmd/roger-tower/hub.go:414`). The comment says this "closes the window to roughly one round
   trip"; what it actually does is convert a 30-second window into a 404 the consumer has to
   retry. For per-request relay that is the *normal* path, not the corner. **Fix it in the grant,
   not with a push** — see §6.5 item 5.
2. **The node's first signed request to a relay costs an extra round trip**, because it does not
   know that hub's process epoch and may only learn it from the hub's own 401 plus an Ed25519
   proof bound to the request's nonce (`internal/towerhub/client.go` `epochFrom`). This one is
   **unavoidable and must be budgeted**: Core cannot supply the epoch, because Core does not know
   when a tower last restarted — that is the entire reason the epoch is learned from the hub.
   What removes it in the common case is a per-relay epoch CACHE on the node that outlives one
   session, so the second attempt at the same relay within the process's lifetime costs nothing.
   That keeps the proof (the value was proved when it was learned) and keeps the two-live-processes
   detection working (`Client.retired` sees more, not less).
3. **Whether any of it is on the critical path depends on ordering.** `Hub.Submit` enqueues into
   a buffered channel (`jobQueueDepth = 64`) and blocks for `submitTTL`; the node does not have to
   be polling at the instant the job arrives, only before the TTL expires. So if Core pushes
   "serve attempt A at relay R" down the node's already-parked `/agent/poll` *at the same moment*
   it answers the consumer's authorize, the node's two round trips to R overlap the consumer's
   one. The added first-token latency is then `max(0, node→R − consumer→R)`, roughly one RTT
   between the node and the relay: single-digit milliseconds on a well-placed relay, ~100ms
   across an ocean. Against a first token that is typically hundreds of milliseconds of model
   time, that is the number §4.4 asked for and it is small — **but it is only small if the
   registration problem in item 1 is solved without a Core round trip.**

### 6.5 The per-tower state that becomes per-session

| State | Where it lives | What per-session does to it |
|---|---|---|
| **Hub epoch** | `towerhub.Client.epoch`, per hub process, learned from a proved 401 | The one unavoidable cost. Cache it per relay across sessions (§6.4 item 2); it is per-PROCESS, not per-session, so nothing about ephemerality makes it wrong. |
| **Retired-epoch memory** | `towerhub.Client.retired`, bounded at 8 | Must be cached with the epoch, per relay. A fresh list per session is a fresh list that cannot detect two hub processes — the one thing it exists for. This is also why the re-attachment landed on this branch deliberately excludes `ErrHubMultipleProcesses` from its triggers. |
| **Nonce ring** | `internal/towerhub/nodeauth.go`, per (station, hub process), in memory, floored at process start | **The tombstone stops being a corner case and becomes the mechanism.** `forget` keeps a floor after unregistering so that register→unregister→register does not reopen the replay window. Under (c) that cycle is the normal traffic pattern, many times an hour per station. A tower must therefore hold tombstones for **every station that passed through it inside the signature skew window (5 minutes)**, not for the handful attached to it. Bounded and cheap, but it must be sized on purpose, and it must be reaped on a clock rather than on unregister. |
| **Signed-bearer latch** | `Server.signed[stationID]`, durable, per (tower, station) | Must **not** be reaped with the session. It is a one-way fact ("this station has signed here, so its legacy bearer is dead") and reaping it per session would re-open the stolen-bearer path on every attempt. It is deleted one release from now anyway; until then, session teardown must be explicit about what it does not touch. |
| **TLS pin** | published by Core beside the endpoint; read at attach today | Gets **better**. It travels with the per-attempt instruction, so it is re-read every attempt: a certificate rotation costs at most one attempt instead of stranding a node until it restarts. Ephemeral sessions retire the whole class of problem §5.7's migration-cost bullet is about. |
| **Assertion-key registration** (`POST /tower/hub/nodes`) | PULL: the tower asks Core every 30s (`hubNodeRefresh`) plus on-demand on an unknown-station submit | The one that has to change. 30 seconds is not a cadence for a relay chosen milliseconds ago, and the on-demand path answers the triggering submit with a 404. |

**The registration answer: put the key in the grant, not in a push.** The hub already verifies the
Core-signed grant on every submit, against Core's own dispatch key AND scoped to its own tower id
- `dispatch.EdgeGrantMeta(grant, coreKey, link.PublicNetwork, st.TowerID, now)`,
`cmd/roger-tower/hub.go:156`. Two things follow from that line, and the second is the important
one. First, Core already records the station's assertion and session keys on the attachment, so
the grant could CARRY the assertion key and the hub could register the station from the object it
is already verifying. Second - **the relay is ALREADY a per-attempt fact on the wire.** The grant
names one tower and a hub refuses a grant minted for another. What is per-attachment is only the
SELECTION (`edgeTargetFor` reading `row.TowerID`) and the settlement gate (§6.6). The transport
has been ready for M3 the whole time. That removes the
Core round trip entirely, removes the 404-and-retry, and tightens a property rather than
loosening one: a relay could then only serve a station Core authorized **for this attempt**,
instead of any station on a list it pulled thirty seconds ago.

Two things to say out loud about it. The grant is presented by the CONSUMER, so this discloses
the station's assertion public key to consumers; that key is public by nature and consumers
already receive `station_session_key` from authorize, so it is not a new class of exposure — but
it is the key an operator's earnings are paid against, and §5.0 item 9 already treats putting it
on an unpinned wire as a privacy cost worth naming. And the pull must **stay**, scoped to
revocation: registration-from-grant can only ever add a station, and something has to be able to
take one away.

### 6.6 Money: what attribution keys on today, and why M3 is one gate rather than a schema

This is the part that had to be checked rather than assumed, and the answer is better than
expected in one place and worse in another.

**Checked:**

- The tower that earns the 10% is `req.TowerID` — **a field in the settle body the Tower sends
  about itself** (`toweredge.go:1354`). It is bound three ways: the request must be signed by the
  key Core admitted that tower under (`towerCaller`); `at.Origin.TowerID != req.TowerID` → 403;
  and `ClaimByID(attemptID, req.TowerID)` must match the dispatch record's `tower_id`, on both
  stores (`memstore.go:59`, `pgstore.go:136`).
- **The receipt carries no relay identifier at all.** `dispatch.SignReceipt` signs network, type,
  version, attempt id, station id, the two digests, and usage. The Station never attests who
  carried its bytes, and cannot: it talks to one hub and has no way to know what that hub is
  called beyond what Core told it.
- **`earning_lots` has no tower column.** The relay share is a lot whose `node` is
  `"tower:" + towerID` (a string prefix, `toweredge.go:83`) and whose `account_id` is the
  operator's canonical account key. Payout and clawback key on `account_id` and `request_id`
  only; the prefix is provenance for a dashboard.

**So is per-request attribution easy? Two of the three bindings are already per-attempt.**
`rec.TowerID` comes from the grant, minted at authorize; `towerCaller` is per-request. The only
thing in the way is the middle gate, `at.Origin.TowerID != req.TowerID` — and **that gate is
redundant with `ClaimByID` for the property it claims to protect.** Its comment says it stops "a
Tower settling for a Station behind somebody else's origin", and `ClaimByID` already refuses any
tower that is not the one the grant named. So M3's money change is: delete the standing-fact gate
and rely on the per-attempt one. One gate, no schema, no new column.

**But it is doing a second job nobody named, and removing it exposes that.** It is currently the
only reason `at.Owner` — the payee of the 70% — is read from an attachment known to still be
behind the settling tower. Without it, the station owner is read from whatever the attachment says
at settle time. That is still correct (the station's owner is the station's owner) and it should
be stated rather than discovered.

**And the gate would lose money the moment M3 lands — but it is NOT losing money today, and an
earlier version of this section said it was.** The claim was that a station rehomed, swept dormant
or revoked between authorize and settle makes the attempt **unsettleable by anybody**: the relay
that carried the bytes and holds the receipt is refused 403 by this gate, a different tower is
refused 404 by `ClaimByID`, and `towerjoin.SettleEdgeReceipt` treats any 4xx as
`ErrSettlePermanent` and abandons, so nothing can repair it. The mechanism is right and the
*reachability* was never checked. It was checked afterwards, and none of the three triggers fires
inside a settlement window:

- **Rehome cannot happen at all.** `origin_tower` has exactly ONE writer in either store — Admit's
  upsert (`attach/pgstore.go:322-332`, `attach/memstore.go:141`) — and its `WHERE` requires
  `state = 'dormant'`. Dormancy requires seven days with no `last_routable` stamp. A station that
  has just done real work has a fresh stamp by construction, so the precondition cannot be met
  inside a window of the grant's lifetime plus ten minutes. There is no other writer: nothing in
  the codebase moves a live station's origin, which is also why re-attachment cannot move a node
  off a dead tower (§6.9 slice 2, and `internal/agent/tower.go`).
- **Revoke and DetachIdle do not touch `origin_tower` at all.** Both write `state` and nothing
  else (`UPDATE ... SET state = $3`), so a revoked or swept-dormant station still satisfies
  `at.Origin.TowerID == req.TowerID` and settles normally. For two of the three triggers this
  section named, the stated mechanism was simply wrong.

**So it is an M3 RISK, not a live bug, and it is a real one.** M3's whole point is that the relay
becomes a per-attempt choice — at which moment "the tower that carried this attempt" and "the tower
this station is attached to" stop being equal by construction, and this gate starts refusing the
honest relay on the ordinary path rather than on a path nothing can reach. Slice 0 is therefore
worth landing *before* M3 for the reason it was always worth landing — it makes "which relay
carried this" a per-attempt fact on the money path — and not because it recovers money today. It
recovers none.

**And the fence M3 counts on does not exist yet — IT DOES NOW; see §6.6b.** The paragraph below
is left as written because it is the diagnosis, and the fix is recorded separately rather than
folded in. `dispatch.Record.StationEpoch` is documented
as the thing that "fences a rehome: work granted under the old origin cannot be completed"
(`internal/towercore/dispatch/dispatch.go:85`). It is minted from `at.Epoch` at authorize, signed
into the grant, carried on the wire and stored on the dispatch record — and it is **never compared
against anything** in `towerEdgeSettle`, or anywhere else. Removing the standing-fact gate without
building that comparison leaves nothing at all between an in-flight attempt and an origin that
moved under it. Today that is harmless, because nothing moves a live origin; the day something
does, this is the check that has to exist first.

One thing this design does **not** need: a relay identifier on the receipt. It was the obvious
first idea and it is wrong twice over. The Station cannot know it (it knows the endpoint it was
sent to, not the id Core bills against), and even if it could, it is the party being paid — an
attribution the payee signs is not an attribution.

### 6.6b The Station-epoch fence, as built (landed 2026-08-20)

`towerEdgeSettle` now compares the epoch the grant was minted under against the epoch the
attachment reads at settle time. Everything below is the reasoning that is not obvious from the
diff.

**Where it sits, and why the position is the design.** Immediately after the attachment read and
the origin-tower gate (`toweredge.go`), and **before** `hex.DecodeString(at.AssertionKey)` /
`ParseReceipt`. Three boundaries decide what a refusal writes, and this is the only slot that
clears all of them:

- Before `settleEdgeAttempt`, whose error path writes `noteAttempt(KindExecutionFailed)`. A
  placement that moved is not an execution that failed, and blaming the Station on the evidence
  trail for Core's own re-placement would be the wrong record permanently.
- Before `ClaimByID`, so the one-use claim is not consumed — the same reason `resolveEdgeParties`
  sits where it does.
- **Before `ParseReceipt`, which is the non-obvious one.** The receipt is verified against
  `at.AssertionKey` — the CURRENT attachment's key, not `rec.AssertionKey` from the dispatch row.
  An epoch mismatch is precisely the statement "this is not the attachment the grant was minted
  from", so verifying against it first can produce a **403 on the key** for the epoch's reason.
  A 403 is `ErrSettlePermanent`, which drops the receipt from the tower's spool. The fence has to
  answer before the key is used, or the fence's careful status code is bypassed by an accident of
  ordering.

  **A second finding fell out of checking that, and it is the same defect as the one this section
  fixes.** `dispatch.Record.AssertionKey` is written by `openEdgeAttempt` and read by NOTHING —
  `grep '\.AssertionKey' cmd/rogerai-broker` finds one write and no read. Its own comment says it
  exists "because the RECEIPT is verified against it, and the instance verifying is very often not
  the instance that issued", which is exactly what the settle path does not do. Today the two keys
  are the same by construction (`Admit` refuses a revival that presents a different assertion key
  — `checkBindings`, "this Station ID is already bound to another assertion key" — so a key change
  requires revoke-and-new-Station, which is a new attempt id too), so this is latent in the same
  way and for the same reason the epoch was. **Not fixed here**, deliberately: switching the
  verification key is a change to what "signed by the Station" means on the money path and wants
  its own change and its own argument, not a rider on a fence.

So a refusal writes **nothing**: no evidence, no claim, no settle, no capture, no lot. The
consumer's hold stays exactly where authorize put it, and the Station's in-flight reservation
comes down on its own (`edgeEnterInflight` arms a `time.AfterFunc` at the grant deadline).

**It gates ENTRY into a settlement, not the completion of one Core already began.** A record
that is not `issued` is not re-judged. This handler is the only caller of `ClaimByID` or `Settle`
on an edge attempt anywhere in the tree — `dispatch.Registry`'s `Claim` and `ClaimNext` have no
production callers — so a record in `claimed` or `settled` got there through these lines, past
this fence, when the placement agreed. The handler is deliberately built to finish such a
settlement (claim, settle and wallet capture are three non-atomic steps and the courier re-drives
across a fault between any two of them), and refusing that repair because the placement has since
moved would punish an operator for *our* interruption, on the one path where Core has already
judged the attempt payable — and would leave the money half-committed with the consumer's hold
swept for work that really happened. The failed-delivery rule is about work whose settlement
never started. This is the reachability that makes it worth a branch: `edgeExitInflight` runs
*after* the settle, so a half-committed settlement is exactly the window in which a Station looks
idle to `edgeLoadLocked` while its receipt is still outstanding — which is when a
quiescence-gated re-placement would fire.

**It compares `rec.StationEpoch`, not a re-parse of the signed grant.** Same trust domain —
Core's own row, the same row whose `StationID` already gated this request twenty lines above —
and re-parsing to recover a value we stored *from* that parse adds a failure mode without adding
authority.

**Which way it fails, in two directions rather than one.**

| verdict | condition | answer | why |
|---|---|---|---|
| agrees | equal | continue | the only verdict that reaches the money |
| unstated | either side is 0 | continue, and log | 0 is "not stated", never "epoch zero" |
| moved | grant < attachment | **410 Gone**, permanent | the epoch is monotonic; no retry un-supersedes it |
| regressed | grant > attachment | **503**, transient | no writer produces this; it is a read that lagged |

**Why `moved` is the one case where a permanent refusal is right.** The rule on this path is that
any 4xx but 409 becomes `towerjoin.ErrSettlePermanent` and the tower's courier drops the receipt
from a spool that survives restarts — which is why the party-resolution path chose 503 (commit
`69340c40`). That rule is about *unknowns*, and a superseded placement is not one:
`attach.Registry.Admit` is the only writer of an epoch and it only ever raises it, so retrying
cannot change the answer. A 503 here would not preserve any possibility of payment; it would
spend the settlement window returning the same refusal every fifteen seconds and then hand the
operator a silent expiry instead of a loud, named abandonment on their own console.

**And it was checked that the money is not stranded by that, because "permanent refusal" is only
acceptable if something else releases the hold.** It does: `releaseStaleHoldsSweepOnce` →
`store.ReleaseStaleHolds(cutoff)` reclaims every tracked hold older than `holdTTL` with no
reference to a receipt, a dispatch row or an attempt state, and `edgeSettleGrace` is capped
strictly under `holdTTL` so the hold always outlives the window it guards. Consumer whole,
operator unpaid, nothing attributed to an account that did not earn it — the residual §6.7
already named as the one to prefer.

**What it deliberately does not do: work out an alternative payee.** No "pay the Station but not
the relay". `features/tower/operator_revenue_share.feature` — "Ledger or payment-store failure
fails closed for share money": *no share is accrued, **cancelled**, or paid* — forbids zeroing a
share on unverified state in the same breath as paying it, and there is no withheld-lot state to
park one in. Under §6.3b's ruling this is also simply the right answer: a move that catches live
work is a failed delivery.

**The rollout guard, and the archaeology that corrected the reason for it.** The fence treats a
zero on *either* side as "not stated" and passes. The stated worry was that grants minted before
this change carry `StationEpoch` 0 while attachments say 1, and that a strict fence would refuse
the entire existing fleet on the deploy. Checked, and the fleet was never actually at risk:
`tower_attempts.station_epoch` and the attachment `epoch` column have both been in their
`CREATE TABLE` bodies since those tables existed (`git log --diff-filter=A`), `Admit` assigns 1
or higher, and every minting path in the tree sources the value from `at.Epoch` via
`targetFromAttachment`. So a production zero is not reachable through any path that exists today.

The guard stays anyway, for a better reason than the one it was asked for: **a zero-valued int64
is the shape a future refactor's silence takes.** If something stops stating the epoch, a strict
fence turns into a fleet-wide permanent refusal that deletes pay, and a lenient one turns the
whole check off in silence. So the exemption logs every time it fires, and that line is its own
retirement notice — zero occurrences in a healthy fleet means the arm can be deleted; a sudden
volume of them means the fence has been disarmed by something upstream. This was watched: with
the exemption removed, a grant stating no epoch is answered **410**, permanently.

**What this does NOT do.** It does not move a placement, refuse a re-placement while work is in
flight, or define "idle" for the mobility rule — the gate that stops a move landing on a busy
Station is the *other* half of §6.3b and is not built. The fence is the backstop for the case
where that gate is wrong or races, not a substitute for it.

### 6.7 Self-dealing: a check that has never had to work

There are exactly two checks, both at settle, both in `cmd/rogerai-broker/toweredge.go`:

- `accrueEarnings:1758` compares the consumer against the **station owner** only, and writes a
  `self_dealing` boolean on the evidence trail.
- `captureEdgeCharge:1909-1917` compares the consumer against the **station owner** and,
  independently, against the **tower operator**, and zeroes the offending share. The consumer is
  still charged in full; nothing is refused; one line is logged.

`sameAccount:1937` is identity equality: literal pubkey match, or a shared GitHub id, Apple
subject, or login.

**Does it cover per-request relay? On the pair it compares, yes — and that is the problem.**
Today a consumer cannot choose the relay at all; they are handed whichever tower the *provider*
attached to. Steering is not a thing that can be done, so the consumer-vs-tower check has never
had to work. M3 makes the relay a function of inputs the consumer partly supplies, and turns a
dormant check into a load-bearing one. Four findings, in the order they cost money:

1. **The rule that keeps it bounded is a ranking rule, not a settlement rule.** §4.1 already says
   location must never be self-declared on the supply side, because "claim to be everywhere,
   receive everything" is a lever. The same sentence has to be written for the DEMAND side, and it
   is the whole defence: **the relay must be chosen by Core from server-observed inputs only — the
   connecting IP, never a client-declared hint, and never a list the client picks from.** If the
   authorize response ever becomes "here are three relays, choose", a consumer who runs a tower
   steers 10% of their own spend to themselves and `sameAccount` is the only thing standing in the
   way. Three distinct verified identities defeat it; that is already logged as unresolved risk
   E44 in `docs/tower-network-plan.md`. This is the single decision in this section that must be
   made before code is written, because it is a shape, not a check.
2. **The third pair is not compared anywhere: station owner vs tower operator.** An operator who
   owns both collects 70% + 10% = 80% of gross from an arms-length consumer, unflagged. Today they
   cannot arrange it. Under locality-aware relay selection they can, and **often should** — the
   nearest relay to a node is frequently on the node's own network, which is exactly the outcome
   the milestone exists to produce. The recommendation is therefore not to block it: **record the
   pair on the lot so it is measurable, and set a policy threshold on the fraction of an
   operator's relay earnings that come from their own stations.** Unmeasurable is the current
   state and it is the only unacceptable one.
3. **`sameAccount` fails open on a store error.** Any error resolving either owner returns `false`,
   which pays out. Under (c) that turns a settlement-time database blip into money. It should
   fail *closed* — withhold and flag for review, which is what
   `features/tower/operator_revenue_share.feature:832` already specs and the code does not do.
4. **The tower check is skipped entirely when `towerAcct == ""`** (`towerShare > 0 && towerAcct
   != "" && sameAccount(...)`). Benign today, because an unresolvable account mints no lot either
   way — but it is a guard whose falsity silently disables a check, and it should be an explicit
   "no account, no lot, no check needed" rather than a conjunct.

**Items 2, 3 and 4 are DONE, landed on `release/v5.7.0`. Item 1 is unchanged and still the one
decision that has to be made before M3's code is written.** What follows is what was built and
the reasoning that is not obvious from the diff.

**One resolution, taken before the settlement commits.** All three comparisons now happen in
`resolveEdgeParties` (`toweredge.go`), called from `towerEdgeSettle` *after* `settleEdgeAttempt`
and *before* `ClaimByID`. The position is the fix, not the checks: the old checks ran inside
`captureEdgeCharge`, which is downstream of the one-use settle, where the only two answers
available are pay and do-not-pay. In front of the commit there is a third answer that costs
nobody anything.

**Item 3, the fail-open, leans to REFUSE - neither pay nor withhold.** `sameAccount` now returns
`(bool, error)`, and a store error at settle answers **503**, commits nothing, and lets the
Tower's spooled courier re-forward the same receipt fifteen seconds later. The reasoning, since
this section previously said only "fail closed":

- *Fail open pays a self-dealer for the price of a database blip.* That was the live behaviour.
- *Fail closed burns an honest operator's pay for the same blip*, permanently. A lot is minted
  once and nothing revisits one that was never minted. Section 6.7 item 3's own recommendation -
  "withhold and flag for review" - would need a withheld-lot state that does not exist; without
  it, "withhold" is just "delete", quietly.
- *Both convert an unknown into a wrong answer, and this exchange does not have to.* It has a
  durable retry rail underneath it (`cmd/roger-tower/hub.go`, 15s, spooled across restarts) and a
  Core handler already written to complete a half-finished settlement on re-drive.
- The spec agrees, and it is **not** the scenario item 3 cited. `operator_revenue_share.feature`
  line 832 is about *actual* self-dealing being withheld. The line that covers *not knowing* is
  "Ledger or payment-store failure fails closed for share money": "no share is accrued,
  **cancelled**, or paid" and "the operation is retried only from durable authoritative state".
  Note *cancelled* - zeroing the share on unverified state is forbidden by the same sentence that
  forbids paying it.

The residual cost is real, bounded, and the one to prefer: if the store is still unreachable when
the settlement window closes, the attempt expires, the orphan sweep returns the consumer's hold,
and the work was done for free. Consumer whole, operator unpaid, nothing attributed to the wrong
account. The **status code is load-bearing** - `towerjoin.SettleEdgeReceipt` treats any 4xx but
409 as `ErrSettlePermanent` and drops the receipt from the spool, so refusing with a 4xx would
turn a five-second blip into an operator's pay deleted forever.

Two neighbouring fail-opens on the same path were closed by the same move, because the resolution
now needs them anyway: `towerOperatorAccount` conflated "no wallet account" with "could not
look" (the second silently minted no relay lot), and `edgeConsumerWallet` did the same, on a
branch where `captureEdgeCharge` returns **200 having billed nobody** - a store blip could hand a
consumer free inference and both operators nothing, with the courier told it succeeded.

**`sameAccount` had a second hole, of the same shape, entered from the other side.**
`accountkey.go`'s `accountOwnerOf` folds an account's device rows into one canonical row on
AppleSub, Login+GitHubID, then **verified email** - and `sameAccount` had never learned the last
one. So two device keys the *money* path treats as one account (it mints and reads their lots
under one key) were two strangers to the self-dealing check. It now compares verified email
directly and, as a backstop, compares canonical keys - which is by construction everything
`accountOwnerOf` knows today and after the next linkage is added to it. The explicit switch stays
in front because it is deliberately broader in two cases `accountOwnerOf` refuses (a shared
GitHub id with no login; a shared login under different GitHub ids): a rename must not re-key an
operator's earnings, but here a false positive only withholds a share and invites a look.

What none of this touches is **E44** - several distinct verified identities held by one person.
That is still an evidence problem, not an equality test, and item 1 below remains the defence.

**Item 2, the station-owner-versus-tower-operator pair, is now RECORDED and still not enforced.**
`earning_lots` gained one additive column, `self_relayed BOOLEAN NOT NULL DEFAULT FALSE`, stamped
on **both** of an attempt's lots (the concentration being recorded is 80% of one request landing
in one account, and the Station's 70% is most of it). `store.SelfRelayedRollup(accountID)` is the
read side: the per-node gross of an account's self-relayed, non-clawed lots, in the same shape and
the same order as `EarningRollups`' by-node half, so "what fraction of this node's earnings were
self-relayed" is one division rather than a second schema. Parity-tested on mem and Postgres.

*Why a column and not a query.* For the literal case the pair was already recoverable - both lots
carry one `request_id` and both `account_id`s are canonical (`at.Owner` is stamped through
`accountKeyOfPubkey` at attach) - so a self-join finds them. What a self-join cannot recover is the
**linkage verdict**: two device keys under one GitHub id, one Apple subject or one verified email
are one account to `sameAccount` and two unequal strings to SQL. The column stores that verdict,
taken once, by the code that already had to take it, at the only moment every input was in hand.
That is the whole of what it adds, and it is stated here rather than oversold.

*Why evidence and not enforcement.* Because under the milestone that makes it reachable it is
frequently the right answer - your own node behind your own relay in the same building genuinely
is the low-latency path - and because the alternative to measuring it is a threshold invented
during an incident, with no data behind it and no column to put it in.

### 6.8 Failure, fallback, and the hold

The consumer's hold is placed at authorize, before the attempt is recorded, at the worst case of
the grant's ceiling (`toweredge.go:331`), and it is released only by a settle or by the orphan
sweep at `holdTTL`. So "the relay was unreachable at dispatch" must never resolve to "authorize
again": a second authorize is a second hold against the same wallet, a second slot against
`maxOpenEdgeAttemptsPerAccount`, and a second hit on the per-account rate limiter — a consumer
whose chosen relay died would be rate-limited for their relay's fault.

**Recommendation: name two relays in the grant, ranked.** `ClaimByID` accepts either; the
consumer tries the first and falls back to the second on a connect failure. This needs one extra
field on the grant, no new state machine, no second hold, and no re-signing. It is safe on the
money because only one relay can ever produce a receipt: the hub's courier gate
(`ConsumeDispatched`) means only the hub that actually HANDED the job to the node may forward its
receipt, so the relay that did not carry it has nothing to file. The cost to state honestly is
that a captured grant is now good at two hubs instead of one — which changes nothing, because the
grant is the consumer's to present and the envelope is sealed to the node either way.

The alternative — re-targeting the attempt to a new relay under the same id and the same hold —
is strictly more powerful and needs `dispatch.Record.TowerID` to become mutable under a state
machine while the nonce and the one-use claim stay intact. That is the right end state and it is
not the right first move.

### 6.9 Migration, in slices, with the ones that are safe alone marked

- **Slice 0 — replace the settle gate. SAFE ALONE. IT FIXES NO LIVE BUG; it removes an M3
  blocker.** Drop `at.Origin.TowerID != req.TowerID` in favour of the grant-derived binding
  `ClaimByID` already enforces. Today the two are equal by construction, so there is no behaviour
  change on any path — including the ones an earlier draft of this list claimed it repaired, which
  are unreachable (§6.6). It must land first because it is what makes "which relay carried this" a
  per-attempt fact on the money path, and because the gate refuses the honest relay the moment M3
  makes the two values differ.

  **Two cautions that are narrow and real.** First, `StationEpoch` was not a fence — carried and
  never compared — so this slice would have removed the only standing-fact check without the
  per-attempt one it was supposed to be redundant against ever having been built. **That
  prerequisite is now done (§6.6b), landed on its own ahead of this slice**, and slice 0 no
  longer carries it. Second — and this
  is about the POSITION rather than the check — the gate currently sits at `toweredge.go:1414`,
  **before** `ParseReceipt` (`:1424`) and before `settleEdgeAttempt` (`:1440`), and
  `settleEdgeAttempt`'s error path WRITES `noteAttempt(KindExecutionFailed)` (`:1443`).
  `ClaimByID` is at `:1463`. So "redundant with `ClaimByID`" is true of the CHECK and false of
  where it stands: deleting the gate and relying on `ClaimByID` lets a tower that was never granted
  this attempt reach a write on the evidence trail before it is turned away. Narrow, and the answer
  is narrow too — the grant-derived refusal has to move UP to where the gate is, not the gate down
  to where `ClaimByID` is.
- **Slice 1 — carry the station's assertion key in the Core-signed grant, and let the hub register
  from it. SAFE ALONE.** Keep the `/tower/hub/nodes` pull for revocation. No placement change.
  Measurable immediately as the disappearance of `OnUnknownStation` 404s.
- **Slice 2 — the ephemeral session, as a shadow path. SAFE ALONE.** Build the node-side "open a
  session at relay R, serve, close" driven by an instruction on the broker long-poll, and point it
  at the SAME tower the node is already attached to, while the standing attachment keeps serving.
  This ships M4's mechanism with none of M3's risk and answers §4.4's empirical question — *is the
  handshake cheaper than the bandwidth it saves* — with real numbers before any traffic depends on
  the answer. Depends on slice 1: a session at a relay that does not know the station is a 404.
  The re-attachment on this branch (`internal/agent/tower.go`, `serveTowerTenancy`) is the seed of
  this: it already makes a node's relay ENDPOINT a value that can change without a restart, and a
  tenancy is a session with a very long lifetime.

  **What it is NOT is rehoming, and the gap is worth stating because the milestone above needs the
  thing it does not have.** A re-attach re-reads the tower's link plane, so it recovers a tower
  that moved, rotated its certificate or turned TLS on. It cannot move a node to a *different*
  tower, because Core will not re-place a live station: `toweredgeattach.go`'s idempotent-retry
  branch fires on `prior.Live()` and answers with `prior.Origin.TowerID`. The only thing that ever
  makes a station's origin writable again is `DetachIdle` — it writes `state = 'dormant'` and
  nothing else, which is exactly the one precondition Admit's upsert needs before it may write
  `origin_tower` — and `DetachIdle` is driven from `publishRoutable`, which the housekeeping tick
  runs only `for _, tw := range link.LiveTowers()` (`towerlink.go:816`), on a seven-day horizon.
  A tower that is permanently gone is in no such list, so its stations are never swept: the one
  escape hatch does not fire for the one case that needs it. What Core does instead, since this branch, is
  REFUSE honestly when the origin has no relay plane rather than answering 200 with an empty
  endpoint — it used to discard `RelayPlane`'s `has` — so the node backs off, keeps asking, and
  tells its operator. That is bounded and visible instead of silent, and it is not a recovery.
  Slice 3 is where a live station's relay becomes re-choosable; the `StationEpoch` fence it had to
  build first is built (§6.6b), and under §6.3b slice 3 is now "re-place an idle station" rather
  than "choose a relay per attempt".
- **Slice 3 — move the choice. NOT SAFE ALONE; this is M3 and it needs 0, 1 and 2.**
  `edgeTargetFor` splits into two decisions, station and relay; the attachment's tower becomes a
  default rather than a commitment; the grant names two relays (§6.8).
- **Slice 4 — locality enters the relay half** (M2 and M5), with the demand-side rule from §6.7
  item 1 written into the ranking rather than into a comment.

**The correction §3 needed:** M3 depends on M4, and M4's own dependency is the registration
change (slice 1), which section 3 does not mention at all. The order is 0 → 1 → 2 → 3 → 4, and
only the first three are individually shippable.

### 6.10 The M4 instruction channel: the shape, recorded, NOT built

Written while the epoch work was fresh, at the coordinator's request. **Nothing here is
implemented.** Under §6.3b this is a smaller object than it would have been under per-attempt
placement: it re-places an **idle Station**, it does not route an individual attempt. The
forgery concern is unchanged, because the thing an attacker wants is the same either way — a
node dialling a relay of their choosing.

**The carrier already exists and nothing has to be built for it.** M0 restored an ordinary
broker long poll for every node (`GET /agent/poll`, `tunnel.go`, held 25s), served in
multi-instance mode off a per-node bus channel with `busClaimJob` guaranteeing single delivery.
Core already has an authenticated, sub-second, cross-instance push to every node in the fleet.

**Why `protocol.Job` cannot carry it as it stands.** `Job` is `{ID, User, Body, Path}` and a
node's bridge POSTs `Body` to the upstream named by `Path`. There is no field that says "this is
not work", so an instruction shaped as a Job would be *served* by any node that does not know
about it — the compatibility hazard is not that old nodes ignore the instruction, it is that
they execute it.

#### The minimal field set

One new optional field on `protocol.Job`:

```go
// Placement, when present, means this message is not work: it is a Core-signed instruction to
// re-place this node's relay binding. A node that does not understand it MUST discard the whole
// message rather than fall through to serving Body.
Placement json.RawMessage `json:"placement,omitempty"`
```

**That rule binds new nodes and cannot bind old ones, so the field is not sufficient on its
own.** An old binary unmarshals `Job`, ignores the unknown member, and serves an empty `Body` at
`Path` — the failure mode is a spurious upstream call, not a dropped message. There is no node
version or capability on the registration path today (`protocol` carries chat `Capabilities`
only, and those are about models), so **Core must not send a placement to a node that has not
declared it can read one.** The cheapest declaration is the one the node already makes: it
self-attaches, and the attachment is a Core-side row the placement sender already has to read.
Whatever carries it, the gate belongs at the SEND, because the receive side is exactly the code
that is too old to have a gate.

Its content is one `towerobj`-signed object, in the same shape as every other signed object in
the tree (`internal/towerobj`, string-encoded integers, one signature member):

| member | why it must be inside the signature |
|---|---|
| `network`, `type` (`"dispatch.placement"`), `version` | the standard anti-substitution triple: without a distinct type, a signed object of another kind with a compatible field set can be replayed as this one |
| `station_id` | binds the instruction to one Station. The poll is authenticated by the node's bridge token, i.e. by NODE identity; the money and the keys hang off STATION identity, and the two must be tied together inside Core's signature |
| `epoch` | **the new placement epoch this instruction establishes.** It is what makes the instruction ordered rather than merely fresh: a node that has already moved to epoch N must ignore an instruction for N−1, which is the only defence against a captured-but-genuine instruction being replayed to drag a node back to a relay the operator has since been moved off. It is also the value that must reach `attach.Epoch`, so the settle fence in §6.6b keeps meaning what it means |
| `tower_id` | the relay's identity as Core bills it — the value the hub checks a grant against (`EdgeGrantMeta(..., st.TowerID, ...)`) and the value that ends up on the relay lot |
| `endpoint` | where to dial |
| `endpoint_tls_spki` | the certificate pin, **and its emptiness must be signed too.** An unsigned pin field is a downgrade primitive: an on-path party strips it and the node dials plain HTTP. Signed, "no pin" is a statement Core made rather than an omission an attacker produced |
| `issued_at`, `expires` | a short window (seconds to a minute). The instruction is a push, not a credential, and does not need to outlive its delivery |
| `nonce` | one-use per station, against the same replay ring the hub polls already use |
| `core_sig` | Ed25519 over the canonical object, by **Core's grant key — the key the node already pins**, fetched from Core over a trusted base and never from the tower (`internal/agent/tower.go`, `ErrCoreKeysUnpinned`). No new key, no new distribution problem, no certificate authority |

#### What the node must check before it acts, and why each one is load-bearing

1. **`core_sig` verifies against the pinned grant key**, for this network, this type and this
   version. This is the whole of the defence against a hostile broker instance or an on-path
   party: the poll channel is authenticated by a bearer token the broker holds, so the broker is
   *not* a trusted source for where a node should take its work. The instruction is trusted
   because Core signed it, not because it arrived on the poll.
2. **`station_id` is this node's own Station.** A signed instruction for somebody else's Station
   is a valid object and still not addressed here.
3. **`epoch` is strictly greater than the epoch the node is currently serving under.** Equal or
   lower is a replay of a genuine instruction and must be dropped silently. **The node does not
   know its epoch today** — the attach response returns endpoint, pin, hub token, `tower_key_hash`
   and state, and no epoch — so this check needs `attach.Epoch` added to that reply first. It is
   one field and it is a prerequisite, not a detail: without it the node's only ordering signal is
   the arrival order of pushes on a channel that makes no ordering promise.
4. **`expires` has not passed**, against the node's own clock, with the skew the rest of the
   tree already uses.
5. **The node is idle** — §6.3b's rule, enforced at BOTH ends. Core gates the send on
   `edgeLoadLocked(nodeID) == 0`; the node re-checks locally, because Core's view is a cache and
   the node is the only party that knows what it is actually serving. If the node is busy it
   declines and Core retries later; it does not queue the instruction, because a queued placement
   is a placement made against stale information.

#### What still has to be decided before it is built

- **Where the epoch is written, and in what order.** The instruction's epoch has to become
  `attach.Epoch`, and `attach.Registry.Admit` is currently the only writer of one and reaches it
  only through dormancy. A second writer is a real change to the attachment's state machine and
  is the substance of the work, not this object.
- **Whether the node acknowledges.** Core needs to know the move landed before it starts
  authorizing against the new relay, or the fence in §6.6b will refuse honest work — the
  attachment would read the new epoch while the node is still serving at the old relay. The
  cheapest shape is that the node's next signed poll at the NEW relay is the acknowledgement, and
  Core does not advance the attachment until it sees one. **That ordering is the correctness
  question of M4** and it is not answered here.
- **Whether the instruction carries the station's assertion key** so the new relay can register
  it from the object it is already verifying (slice 1's idea, applied to the placement object
  instead of the grant). It removes the `OnUnknownStation` 404 on the first submit after a move.

