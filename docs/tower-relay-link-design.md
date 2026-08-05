# The joined relay link: how a Tower carries traffic

Status: **proposed for founder approval. No implementation is authorized by this document.**

Registration is shipped: a Tower is admitted, holds a certificate, and sits in quarantine.
This is the design for the part that makes it carry work, written against the already-
approved specs (`inventory_and_routing`, `station_attachment`, `job_and_settlement`,
`receipt_v2`) rather than inventing a second design beside them.

Three requirements shape every decision below, and they pull against each other:

1. **Roger Core stays the authority.** A Tower may transport, aggregate, and report. It may
   never decide who runs a job, what it costs, or what it earned.
2. **Chatter must be rare.** No polling, no per-request round trip to the database for
   things that did not change, no periodic full-state pushes. Communication is *event-driven
   or expiry-driven*, never clock-driven-for-its-own-sake.
3. **The operator is assumed hostile.** They have root on the box, can read process memory,
   and may lie, replay, collude, or selectively drop.

---

## 1. Components, and why each exists

Four pieces. Each is separable because each has a different failure mode and a different
cadence.

### The link (`internal/towerlink`)

**One long-lived, Tower-initiated, multiplexed connection.**

Outbound because the Tower dials us: an operator behind NAT or a corporate firewall opens no
inbound port, publishes no DNS, and holds no public address that can be scanned or attacked.
It is also the single fact that makes everything else cheap — once the link is up, inventory,
dispatch, results, and liveness all ride it, and none of them costs a new handshake.

Multiplexed because a Tower carries many Stations and many concurrent attempts. One stream
per attempt over one connection; head-of-line blocking on one job must not stall the rest.

Authenticated by mutual TLS with the enrollment certificate. Session identity is bound at
handshake to `(network, protocol version, Tower ID, session ID)` and every later frame
carries it, so a frame cannot be lifted from one session into another.

### The certificate manager (`internal/towercert`, extended)

Certificates are deliberately short-lived (24h default), which is only safe if renewal is
boring. The manager owns:

- **Renewal**, initiated by the Tower over the *existing link* at ~⅔ of lifetime. No new
  connection, no re-enrollment, no operator involvement. A renewal proves possession of the
  current key and issues against the *same* Tower ID; it is not a second admission.
- **Rotation** of the channel key on the Tower's schedule, without touching the identity
  key. This is why the two are separate: the identity key is the Tower's name and rotating a
  certificate must never rename it.
- **Revocation status**, held in memory and consulted per handshake. See §3 — this must
  never be a database read on the connection path.

### The validator (`internal/towerverify`)

Everything a Tower or Station says arrives as a signed object. The validator is the one
place that checks them, so the rules cannot drift between callers:

- canonical encoding and signature suite (`receipt_v2` fixes both)
- signer key status *at the time of the statement*, not merely now
- the relationship checks: does this `ProviderAssertionV2` belong to the
  `ExecutionGrantV1` that was issued, for the attempt Core durably recorded?

It is separate from the link because it must also run on paths that never touched a link -
replay of stored evidence, dispute handling, recount.

### The dispatcher (origin-aware, in the broker)

Routing already selects an exact leaf. The dispatcher's only new job is to send the grant
through the *recorded origin* for that Station and no other. The approved spec is explicit
that direct and Tower-backed Stations share one policy but separate dispatchers, and that no
local bridge token or transport handle crosses origins.

---

## 2. The efficiency budget

This is the part the design lives or dies on. Stated as: for each thing that happens, what
does it cost us?

| Event | Frequency | Network | Postgres |
|---|---|---|---|
| Tower connects | once per Tower per outage/restart | 1 TLS handshake | **0** (cert + revocation from memory) |
| Full inventory snapshot | on connect, on resync only | 1 signed message | 1 write (revision head) |
| Inventory delta | when the operator's fleet actually changes | 1 small signed message | 1 write (revision head) |
| Liveness | freshness window, ~30s | 1 tiny frame on the open link | **0** |
| Certificate renewal | ~every 16h per Tower | 1 message on the open link | 1 write |
| **Job dispatched** | per job | 1 stream on the open link | **1 write** (required) |
| **Job settled** | per job | (same stream) | **1 write** (required) |
| Routing eligibility | per request | **0** | **0** (in-memory snapshot) |

Three consequences worth stating plainly.

**Routing reads nothing.** Eligibility, price, capacity, and liveness are answered from an
in-memory snapshot, refreshed by the same background sync loop the broker already runs for
node liveness and verified-tool bits. A request that picks a Station touches no database and
no Tower. This is the single biggest win and it is free — the machinery exists.

**Liveness is a frame, not a query.** The link being open *is* the liveness signal; a small
periodic frame only distinguishes "open" from "open but wedged". Nothing about liveness
reaches Postgres. The approved spec's requirement that "no heartbeat fabricated by another
Tower refreshes it" falls out for free: the frame arrives on a session already bound to that
Tower's identity, so there is no cross-Tower path to forge one.

**Inventory is push-on-change, never poll.** Full snapshots only on connect or after a
resync; otherwise hash-chained deltas. A Tower whose fleet is stable sends nothing at all
between heartbeats. Staleness is bounded by the inventory's own expiry rather than by us
asking - if a Tower goes quiet, its leaves age out of routing on their own.

**The two per-job writes are not negotiable and I am not proposing to batch them.**
`job_and_settlement` requires the attempt event and its commitment to commit atomically
*before* dispatch. That is the money path: batching it would create a window where work is
running that we have no durable record of, and the failure mode is paying for work we cannot
prove or failing to bill for work we did. Everything else in this design is optimised so
that these two writes are the *only* database traffic on the request path.

### What we do NOT do, and why

- **No per-request certificate or revocation lookup.** Revocations are pushed into memory
  when made (the broker already has the shared-store fan-out for exactly this shape).
- **No polling for inventory.** A poll is a request for information that usually has not
  changed. Expiry plus push-on-change gets the same staleness bound for a fraction of the
  traffic.
- **No per-Tower background goroutine doing periodic work.** Cadence lives on the existing
  shared sync tick, so a thousand Towers do not become a thousand timers.
- **No Tower-side caching of authority.** A Tower may cache its own inventory and its own
  certificate. It may never cache an eligibility decision, a price, or a grant.

---

## 3. Postgres: what is added, and what must never touch it

Additive and idempotent, in the `rogerai` schema, matching what registration already added.

| Table | Written when | Read when |
|---|---|---|
| `tower_inventory_head` | inventory revision accepted | on connect (to validate the next delta's base) |
| `tower_sessions` | connect / disconnect | never on the hot path; operations and forensics |
| `tower_attempts` | per attempt (the required write) | settlement, dispute, recount |
| `tower_receipts` | per settlement (the required write) | dispute, recount, payout |
| `tower_cert_history` | issue / renew / revoke | audit, and revocation load at startup |

`tower_inventory_head` stores the head **hash and revision only** - not the inventory body.
The body is large, changes often, and is fully reconstructible from the Tower on resync;
storing it would turn every fleet change into a large write for data we can ask for.

**Never on the request path:** revocation checks, eligibility, price, capacity, liveness,
session lookup. If a future change puts a query on that path, the efficiency argument above
is void and the change needs re-approval.

---

## 4. Security: the surface this opens

Registration's threat was somebody getting admitted who should not be. The link's threat is
different - the operator is *already* admitted and now wants more than they were given.

**Substitution.** A Tower is told to run Station A and runs Station B, which it also owns
and which is cheaper to run or scores better. Prevented by the grant naming the exact
Station and the result being verifiable only against that Station's own signature - the
approved spec's "the Tower cannot substitute the other Station". The Tower never learns the
grant's contents; it sees an opaque envelope and a lease.

**Replay.** An old grant, lease, or assertion is presented again. Prevented by one-use
grants, session-bound frames, and attempt-bound receipts. Every object carries the identity
of the session it was issued into.

**Inflation.** A Tower reports more tokens, more time, or more work than happened. This is
the hard one, and the honest answer is that it is not fully preventable by cryptography: the
Tower is the only party that observed execution. The controls are Core's own transit
observation (what *we* saw on the authenticated session), the Station's independent
assertion, recount, and the fact that a Tower's earnings are held and reversible. This is
detection and consequence, not prevention, and the spec is right to treat it that way.

**Immortal inventory.** A disconnected Tower's leaves keep taking work. Prevented by the
freshness window: leaves age out without anyone needing to notice the disconnect.

**Phishing and social engineering.** The realistic attacks here are aimed at *operators*,
not at the protocol:

- *A fake "your Tower needs re-enrollment" mail.* Countered structurally: enrollment
  requires a signed-in session AND a token issued to that account, so a link in an email
  cannot enroll anything. It is also why renewal happens on the existing link with no
  operator involvement - an operator who is never asked to re-authenticate a Tower has no
  habit for an attacker to exploit.
- *A fake operator dashboard harvesting account credentials.* Countered by first-party
  sign-in having no password to harvest and by device approval showing what is being
  authorized. This is why the mailed code is never a clickable link.
- *A malicious "join this Tower" invitation.* A Station attaching to a Tower is an explicit,
  signed, owner-approved act; there is no flow where following a URL moves a Station.

**Bad-actor economics.** Identities must cost something to burn. An enrollment is
account-bound, quota-limited per account, starts in quarantine, and its key is burned on
revocation - so churning identities means churning *accounts*, which is a much slower and
more visible thing to do.

---

## 5. Sequencing

Each of these is a session or more, and each leaves the tree green and deployable.

1. **The link**: outbound multiplexed transport, mTLS, negotiation, session binding,
   heartbeat. No inventory, no jobs - a Tower connects, stays connected, and is visible as
   connected. Nothing routes to it yet.
2. **Certificate renewal on the link**, before any Tower has been alive long enough to need
   it. Renewal must exist before the first certificate expires in production.
3. **Inventory**: signed full snapshots, hash-chained deltas, expiry, resync.
4. **Dispatch**: origin-aware grants, leases, inner Station sessions.
5. **Receipt v2 and settlement**: the validator, the relationship checks, recount.

Rolling out in this order means quarantine Towers can connect and be observed - real traffic,
real disconnects, real clock skew - long before any money depends on the link.

## 6. Decisions requested

1. **Transport.** Proposal: HTTP/2 over mTLS, streams as the multiplexing primitive. It
   traverses corporate networks that block anything exotic, and the standard library already
   implements it - no new dependency for the most security-critical path we have.
2. **Heartbeat interval and freshness window.** Proposal: 30s heartbeat, 90s freshness. A
   Tower that dies is out of routing within 90s without anybody polling.
3. **Certificate lifetime and renewal point.** Proposal: keep 24h, renew at 16h. Renewal
   failures then have 8h of runway before anything is interrupted.
4. **Quarantine promotion.** Who decides, on what evidence, and is it manual for the first
   external operators? The spec says promotion is an explicit central decision; it does not
   say who signs it.
5. **Inventory size ceiling.** A cap per Tower bounds both memory and the cost of a resync.
   Proposal: a signed policy value, defaulting to something well above a plausible fleet.
