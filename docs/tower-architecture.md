# Towers, Stations and Roger Core: who does what

Written because the roles are easy to get wrong in a way that only shows up as a security or
billing surprise. Grounded in `features/tower/job_and_settlement.feature` and
`features/tower/attempt_lifecycle.feature`, both founder-approved, and in what the code
actually does today. Where the two differ, that is called out rather than smoothed over.

## The three parties

**Roger Core** (the broker) is the authority and the transit path. It decides who may serve,
selects who serves each request, sees the content, issues every authorization, observes the
bytes, and settles the money. Nothing else on the network decides anything.

**A Station** is the machine with the model. It is the only party that executes. It holds two
private keys and signs two kinds of statement:

- **offers** — "I will serve model X, at this price, with this capacity, until this time",
  signed with its *assertion key*.
- **receipts** — "I returned exactly these bytes for exactly this attempt", signed with the
  same key.

A Station does **not** route, does not choose what it receives, and does not modify anyone
else's content. It receives an authorization naming it specifically, checks that
authorization came from Core, executes, and signs for the result.

**A Tower** is a relay an operator runs. It is a **courier and an aggregator**, and those are
its only two jobs:

- it carries bytes between Core and the Stations behind it;
- it presents a fleet of Stations under one accountable account and one lease.

A Tower does **not** execute, does **not** route (Core selects), and **cannot** alter what it
carries. It never holds a Station's keys — if it could sign for a Station, "signed by the
Station" would mean "signed by whoever is relaying", and every guarantee below would collapse.

## The data path

```
consumer ──▶ Roger Core ──▶ [ Tower (opaque byte relay) ] ──▶ Station ──▶ model
                 ▲                                                        │
                 └──────────────── result + signed receipt ───────────────┘
```

Traffic flows **through Core**, and that is deliberate rather than incidental:

- Core must inspect content. `job_and_settlement.feature` is explicit: "Roger Core can inspect
  the request before dispatch and the result before settlement", and, in the same scenario,
  "no UI or documentation calls this client-to-Station end-to-end privacy". Screening,
  routing and recounting all need the content, so Core cannot be cut out of the path.
- Core must observe transit. A joined settlement "requires a complete matching Core transit
  observation" — Core's own byte counts and receive times, not a number a Tower reported
  about itself.
- A Tower dials **out**. It is somebody's self-hosted box, usually behind NAT, so it can never
  accept an inbound connection from a consumer.

### What the Tower can and cannot see

The spec's answer is a nested pair of sessions:

- the **outer** channel is Tower ↔ Core, mutually authenticated, binding the connection to
  that Tower's identity and session;
- the **inner** session is Core ↔ **Station**, mutually authenticated *through* the Tower,
  which relays it as opaque bytes. "The Tower does not possess either inner-session private
  key."

So a Tower operator is meant to see routing metadata, ciphertext digests, timing, sizes and
error classes — and **no prompt, tool argument, image, audio, transcript or completion
plaintext**.

**How it is implemented today.** Not as nested TLS, but with the same confidentiality
property: Core seals the request to the Station's secure-session key (X25519, recorded at
attachment) and the Station seals its result back to Core's published envelope key. The Tower
carries an ephemeral key, a nonce and ciphertext in both directions and can read neither.

What that gives, and what it does not: authentication is carried by the OBJECTS rather than
by a channel — the Station authenticates Core by the signature on the grant, Core
authenticates the Station by the signature on the receipt — so there is no TLS-level channel
binding and no forward secrecy against a compromised Station session key. A real inner TLS
session is still the destination. This removes the plaintext without pretending to be it.

Sealing is **stateless on both ends**, which matters more than it sounds: a per-exchange
session key would live in the memory of the broker that sent the request, and the answer
comes back to whichever broker the Tower reached. Sealing each direction to the recipient's
static key is what lets any instance open a reply.

## What a Tower actually buys, and what it does not

Worth stating plainly, because the intuitive answer is wrong.

**A Tower does not reduce Roger Core's load.** Every byte still crosses Core, twice, plus the
control traffic for the link. A request served through a Tower costs Core *slightly more*
bandwidth and connection work than the same request served by a directly-registered node.

What a Tower buys is **supply and accountability**:

- an operator can bring a whole fleet onto the network under one account, one lease and one
  lifecycle, instead of registering and maintaining N separate nodes;
- those machines can live on a private network with no public reachability of their own;
- the fleet has one identity to suspend, drain or revoke when something goes wrong;
- a standalone Tower can serve its own private network with no RogerAI involvement at all.

The work that gets offloaded is **inference** — the GPU time — and that is offloaded to the
**Station**, which is what should be compensated for it. The Tower operator is compensated for
relaying and for standing behind the fleet, which is a different and smaller contribution.

If the goal is to reduce Core's own bandwidth and connection load, Towers as specified do not
do it, and the reason they cannot is the content-inspection requirement above, not an
implementation shortcut. That would need a different design — consumers connecting to Towers
directly, with Core reduced to issuing grants and accepting receipts — and it would give up
pre-dispatch screening and Core-observed transit, which is what settlement currently rests on.

## The workflow, end to end

**Setup**, once:

1. On the Station: `roger-station init` mints its two keys and prints the public halves.
2. On your workstation: `roger-tower station invite` authorizes them — signed by your
   **account**, because putting a machine on the network under your name is an account
   decision.
3. On the Tower: `roger-tower station attach` redeems it — signed by the **Tower**, because
   Core takes the Station's origin from whoever redeems, so a relay cannot attach a Station
   behind somebody else's origin.
4. Both the Tower and the Station land in **quarantine**. An administrator promotes them; the
   party asking to be trusted cannot be the one granting it.

**Serving**, continuously:

5. The Station signs offers and keeps them fresh (`roger-station offer --refresh`); an offer
   expires, and a file does not refresh itself.
6. The Tower relays those offers to Core in a signed, revisioned inventory, and re-pushes
   before it expires. Core verifies each leaf against the key recorded at **attachment** —
   never against anything in the offer itself.
7. Core publishes the routable result to a fleet view every broker can read, so a request
   arriving at any instance can reach a Tower connected to any other.

**One request**:

8. A request arrives that no directly-registered node can serve.
9. Core selects a Station, **mints** a grant naming exactly that attempt, Station, Tower,
   request digest and deadline.
10. Core **records the attempt** — revision 1 of a signed, hash-chained ledger. Only then does
    the grant become collectable: an attempt nobody recorded is work whose outcome cannot be
    established afterwards.
11. The Tower collects it. Collecting **is** the claim, under a compare-and-swap, so two
    brokers polled at once hand it out exactly once.
12. The Station verifies the grant came from Core, that it names *this* Station, and that the
    request is the one the grant commits to — then executes and signs a receipt over exactly
    what it is returning.
13. Core verifies the receipt against the attachment-recorded key and the response digest,
    advances the attempt to settled, and returns the bytes.

**Money**: today, none. Tower-backed work is free and says so (`X-RogerAI-Cost: 0`). The
attempt ledger exists so that when compensation lands, *which attempt executed, exactly once,
and what its one terminal outcome was* is a fact recorded before the money moves rather than
something reconstructed from logs when somebody disputes it.

## Where each guarantee comes from

| Property | What enforces it |
|---|---|
| A Tower cannot forge an offer | Core verifies each leaf against the key from the **attachment record** |
| A Tower cannot alter a request | the grant commits to its digest; the Station checks before executing |
| A Tower cannot alter a result | the receipt commits to its digest; Core checks before settling |
| A Tower cannot fabricate a result | the receipt needs the Station's assertion key, which the Tower never holds |
| A Tower cannot serve work twice | claiming is a compare-and-swap in a store every broker shares |
| A Tower cannot serve somebody else's work | attempts are keyed by Tower; another Tower gets "no such attempt" |
| A quarantined Tower cannot take work | eligibility is checked at dispatch, separately from identity |
| A Tower cannot read content | each direction is sealed to the recipient's key; the relay holds ciphertext |

## Known gaps

1. **No inner TLS session.** Content is sealed (above), so a Tower cannot read it, but the
   channel-level binding and forward secrecy of the spec's version are not there.
2. **No compensation.** The attempt ledger is in; the funding-source ledger is not.
3. **No streaming to Towers.** A `stream: true` request is never routed to one, because a
   streamed answer can only be verified after the consumer already has the bytes. The inner
   session is what fixes this properly.
4. **Polling, not a multiplexed link.** The spec's Tower holds one persistent outbound
   multiplexed connection; today it long-polls. Correct but chattier, and it is why a Tower's
   work has to be found in a shared store rather than pushed down the link it is already
   holding.
