# Towers: what they are, what they enforce, and why they are worth paying for

The single description of the Tower system. `tower-architecture.md` covers the Core-relayed
path in more detail and `tower-edge-design.md` records the decisions behind the edge path;
this document is the one to read first.

Everything here is checked against the code as of the edge-dispatch build. Where something is
specified but not built, it says so — see `features/tower/*.feature`, each of which carries a
`BUILD STATUS` line that `internal/towercore/featurestatus_test.go` holds it to.

---

## 1. The three parties

**Roger Core** (the broker) is the authority. It decides who may serve, selects who serves
each request, issues every authorization, and settles. Nothing else on the network decides
anything.

**A Station** is the machine with the model, and the only party that executes. It holds two
private keys and the Tower in front of it holds neither:

| key | what it does |
|---|---|
| **assertion key** (Ed25519) | signs this Station's *offers* — "I will serve model X at this price until this time" — and its *receipts* — "I returned exactly these bytes" |
| **session key** (X25519) | receives requests sealed to it on the relayed path, so the Tower carrying them reads ciphertext |
| **TLS key** (ECDSA P-256) | terminates the *consumer's* TLS session on the edge path |

**A Tower** is a relay somebody else runs. It is a courier and an aggregator:

- it carries bytes it cannot read;
- it presents a fleet of Stations under one accountable account and one lease;
- it gives Stations on a private network public reachability they do not have themselves.

A Tower does not execute, does not choose what it receives, and cannot alter what it carries.
**If a Tower could sign for a Station, "signed by the Station" would mean "signed by whoever
is relaying", and every guarantee below would collapse.** That single sentence is why the key
separation is the load-bearing part of the design.

---

## 2. Two paths, and why the second one exists

### The relayed path (built, in production use)

```
consumer ──▶ Roger Core ──▶ [ Tower: opaque relay ] ──▶ Station ──▶ model
                 ▲                                                   │
                 └───────────── result + signed receipt ─────────────┘
```

Core sees the content, counts the bytes itself, and settles on its own observation.

**It does not reduce Core's load.** Every byte crosses Core twice, plus link overhead. The
only thing offloaded is GPU time — which Core was never spending. A request served this way
costs Core *slightly more* than one served by a directly-registered node. There is nothing
here worth paying an operator for, and that is a fact about the design rather than an
implementation shortcut: Core cannot leave the path while it is required to inspect content.

### The edge path (built through settlement; canaries, audit and compensation remain)

```
                 ┌────── control plane: two small, constant-size messages ──────┐
                 │  1. authorize          2. settle                             │
consumer ────────┴──▶ Roger Core ◀────────── receipt ── + ── ack ───────────────┘
   │                                                        ▲       ▲
   │                                                        │       │
   └── data plane: the whole payload ──▶ Tower ──▶ Station ─┘       │
                                     (blind relay)  (executes)      │
   ◀──────────────────── the answer ────────────────────────────────┘
```

Core handles **an authorize and an ack** per request instead of the entire prompt and
completion. For a long completion that is orders of magnitude less traffic through Core.

**That difference is the operator's contribution, and it is what makes compensation coherent
rather than charity.**

---

## 3. How the bandwidth actually leaves Core

Concretely, on the edge path Core no longer touches:

- the request body (prompt, tool definitions, images, audio);
- the response body (completion, tool calls, transcripts);
- any streaming chunks.

What Core still handles per request:

| message | rough size | what it carries |
|---|---|---|
| authorize | a few hundred bytes | grant: attempt, Station, Tower, model, max in/out, deadline, nonce, relay name |
| settle | a few hundred bytes | the Station's signed receipt, relayed by the Tower on its existing link |
| ack (optional) | a few hundred bytes | the consumer's signed statement of what it received |

Both control messages are **constant size** — they do not grow with the payload. A 2 KB prompt
and a 200 KB completion cost Core the same two small messages as a 20-byte one.

The Tower absorbs the egress, the connection count, and the TLS termination load. That is the
resource being bought.

---

## 4. Encryption: what is protected, by what, from whom

### On the edge path — TLS passthrough

The Tower is a **TCP relay that never terminates TLS**. It reads the SNI to learn which
Station a connection is for and splices bytes. The session runs end to end from the consumer
to the Station.

- The Tower sees: server name, IP addresses, byte counts, timings, connection lifetimes.
- The Tower sees no prompt, no completion, no path, no headers — all inside the TLS record layer.
- **There is no configuration in which it could see them**: it holds no certificate and no
  private key for the names it routes.

The SNI is read using Go's own TLS parser rather than a hand-rolled one — a recorder tees the
bytes and the handshake callback refuses — and the recorded ClientHello is replayed upstream
**unaltered**, so nothing is normalised or reconstructed on the way through.

The Station's private key is generated **on the Station** (`roger-station csr`) and is never
an input or an output of any command. Only a CSR goes out and only a certificate comes back.
Core issues for a name under a domain Core controls, because a Station that named itself could
answer for another Station.

Unmodified OpenAI-compatible clients work unchanged: from the client's side it is an ordinary
HTTPS host.

### On the relayed path — sealed envelopes

Core seals the request to the Station's X25519 session key; the Station seals the result back
to Core's published envelope key.

- ephemeral X25519 ECDH → **HKDF-SHA256** → **ChaCha20-Poly1305**;
- the **attempt id is the AEAD additional data and part of the HKDF info**, so an envelope for
  one attempt will not open for another even though the relay holds both.

Sealing is **stateless on both ends** — each direction is sealed to the recipient's *static*
key. This matters more than it sounds: a per-exchange session key would live in the memory of
the broker that sent the request, and the answer comes back to whichever broker the Tower
reached. Static-key sealing is what lets any instance open a reply.

**Honest limitation:** authentication is carried by the *objects* (signatures on the grant and
the receipt) rather than by a channel. There is no TLS-level channel binding and no forward
secrecy against a compromised Station session key on this path. A real inner TLS session is
still the destination. This removes the plaintext without pretending to be full end-to-end.

### Object signing

Every authorization, offer, receipt, inventory and acknowledgement is an **Ed25519-signed
canonical JSON object** (RFC 8785 JCS; integers as bounded base-10 strings; absence is an
omitted member, never a null).

The signed bytes are domain-separated and NUL-delimited:

```
"rogerobj-v1\0" + network + "\0" + type + "\0" + version + "\0" + canonical-object
```

The separators are what stop `"ab"+"c"` colliding with `"a"+"bc"`, so **no object of one type
can ever be replayed as another**, and no object from one network is valid on another. The
network is *bound into the signature* rather than compared afterwards — which is why there is
no separate network check in the parsers, and a check there would be a branch no input can
reach.

The CA root is ECDSA P-256; the grant signer, the attempt-state signer and the envelope key
are each derived from it under **separate labels**, so compromising one cannot forge the
others. Attempt state — the thing money is decided from — is signed with a different key than
grants deliberately.

---

## 5. Approvals: how anything gets onto the network

**Nothing self-approves. At every step the party asking to be trusted is not the party
granting it.**

### Enrolling a Tower

1. An operator mints a one-time enrollment token against their account.
2. The Tower fetches a **challenge** (a nonce) and signs it with its identity key.
3. Core enrolls it, binding the Tower ID to a hash of that key, and issues a short-lived
   certificate.
4. The Tower lands in **quarantine** — admitted, identified, and not yet allowed to carry work.
5. An **administrator** promotes it to active.

Enrollment is idempotent and the challenge survives restarts and instances, so a retry cannot
mint a second Tower.

### Attaching a Station

1. On the Station: `roger-station init` mints its keys and prints the public halves.
2. On the operator's workstation: `roger-tower station invite` authorizes those exact keys —
   signed by the **account**, because putting a machine on the network under your name is an
   account decision.
3. On the Tower: `roger-tower station attach` redeems the invitation — signed by the **Tower**,
   because Core takes the Station's origin from whoever redeems. A relay cannot attach a
   Station behind somebody else's origin.
4. The invitation carries a **one-use secret**. Possession of the two public keys is not
   enough: the operator chose those keys at invite time, so anyone who learned them and the
   authorization id could otherwise attach in the Station's place.
5. The Station lands in **quarantine**; an administrator promotes it.

### The lifecycle states

| party | states |
|---|---|
| Tower | `pending` → `quarantine` → `active` ⇄ `draining` → `revoked` / `suspended` / `expired` |
| Station | `quarantine` → `active` → `revoked` / `detached` |
| Attempt | `issued` → `leased` → `executing` → `evidence_complete` → `settled`, or `failed` / `expired` / `cancelled` |

`settled`, `failed`, `expired` and `cancelled` are **terminal — not revivable by anyone,
including Core**.

An operator may move their own Tower `active ⇄ draining` and may revoke it. An operator may
**not** promote out of quarantine — that is the administrator's edge, and permission is keyed
on the *from*-state so the promote edge cannot be reached by an operator. (An earlier version
allowed `active` as a destination with a comment claiming the transition table would refuse
`quarantine → active`. It does not. That was a privilege escalation and was caught before it
shipped.)

---

## 6. Removals: how something comes off, and what that means

| action | who | effect |
|---|---|---|
| **drain** | operator | stops new work; in-flight work finishes. The reversible one. |
| **suspend** | admin | stops work immediately, identity retained |
| **revoke a Station** | operator or admin | terminal for that Station ID; its offers stop being routable |
| **revoke a Tower** | operator or admin | terminal; the whole fleet behind it goes with it |
| **expire a lease** | admin | takes a Tower off the link now |
| **quarantine** | admin | live-but-not-eligible; identity is fine, work is withheld |
| **rehome (epoch bump)** | admin | fences in-flight work: the old origin's grants can no longer be completed |

**Renewal.** Certificates and leases are both 24 hours by default, and a Tower renews at two
thirds of that on its own — signed by the Tower, not the operator, because renewal spends no
token, creates no Tower and changes no identity or lifecycle state. A human asked to
re-authenticate a fleet daily acquires exactly the habit a phishing mail needs. The old
certificate is not revoked at renewal: the overlap is the point, since revoking would cut the
connection the renewal arrived on. A **revoked or expired** Tower cannot renew, which is what
makes removal stick without needing to reach a process that may be hostile.

Two things make removals real rather than advisory:

- **Eligibility is checked at dispatch**, separately from identity. A quarantined or draining
  Tower is *known* and still gets no work.
- **Certificates are short-lived and leases expire.** Removal does not depend on reaching a
  process that may be offline or hostile — a revoked Tower stops being able to renew.

An attempt already in flight when a revocation lands still settles or expires on its own
terms. Terminal attempt states are terminal in both directions.

---

## 7. Enforcement: what stops each thing going wrong

### A hostile Tower

| what it might try | what stops it |
|---|---|
| read the content | edge: it holds no key for the names it routes. Relayed: each direction is sealed to the recipient's static key |
| forge an offer | every leaf is verified against the assertion key from the **attachment record**, never against anything in the offer |
| alter the request | edge: it is inside TLS the Tower cannot read. Relayed: the grant commits to the request digest and the Station checks before executing |
| alter the answer | the receipt commits to the response digest; on the edge path the **consumer's ack** carries a second, independent digest, and the Tower is between them |
| fabricate a result | a receipt needs the Station's assertion key, which the Tower has never held |
| serve work twice | claiming is a **compare-and-swap** in a store every broker shares (`FOR UPDATE SKIP LOCKED`) |
| replay a settled attempt | the grant's nonce is one-use, and settled is terminal |
| serve somebody else's work | attempts are keyed by Tower; another Tower gets "no such attempt" |
| settle for a Station that is not its own | the attachment record names the origin Tower; a mismatch is refused |
| inflate the usage it reports | settlement never reads the Tower's numbers. It takes the **lower** of the Station's receipt and the consumer's ack |
| route to a Station Core did not pick | the grant names one Station; only that Station's key signs an acceptable receipt |
| take work while quarantined | eligibility is checked at dispatch |
| serve nothing at all | **canary attempts** — specified, not yet built |

### A dishonest Station

The Station is the party being paid, so its own account of its own work is exactly the claim
that needs a counterweight. That counterweight is the **consumer's acknowledgement**:

- both sign a digest of the exact response bytes. A disagreement is **refused, not settled at
  the lower figure** — it is not a rounding difference, and it is attributable;
- billed usage is `min(station_claim, consumer_claim)`. Each party's incentive runs one way —
  the Station gains by over-reporting, the consumer by under-reporting — so the minimum means
  **neither profits by lying**;
- a **response-digest disagreement does not void settlement**. Core cannot tell from two
  conflicting digests whether the relay tampered or the consumer lied, so the attempt settles
  on the Station's receipt (uncorroborated), is marked *disputed* as a rate signal, and is
  force-audited — a single disagreement cannot deny the Station its pay or penalise the Tower;
- the **acknowledgement is bound to the authorized consumer**: the grant carries the consumer
  key, and an ack from any other account (or for an attempt that was never authorized) is
  refused, so a third party cannot void or dispute somebody else's attempt;
- because both ends signed a digest, **neither can produce a different transcript afterwards**,
  which is what makes sampled audit possible at all.

### An absent consumer

An attempt with no acknowledgement **still settles**, on the receipt alone, marked
**uncorroborated**. This is the decision most likely to look like a hole and it is deliberate:
customers close laptops mid-stream and third-party clients will never acknowledge at all. An
operator who lost money every time is an operator who leaves, and a network with no operators
is not more secure — it is empty.

The signal is the **rate**, not the single attempt. A Tower whose uncorroborated share is
unlike the fleet's is investigated; attempts already settled are not reversed by a rate alone.

### An honest gap: the certificate does not yet authenticate anything

Nothing in production performs mutual TLS. A Tower authenticates by **signing its HTTP
requests** with the identity key recorded at enrollment — real authentication, and not what
`job_and_settlement.feature` describes, which is a TLS 1.3 channel mutually authenticated and
bound to the Tower's identity and session.

The consequence worth stating: **certificate revocation is not enforced**, because
certificates are not checked at a channel anywhere. Tower revocation *is* enforced — in the
admission registry, which is what gates dispatch — so a revoked Tower gets no work. The gap
is the channel layer, not the removal. `cert.Authenticate`, `AuthenticateAs`, `ProveMatches`
and `RevokedSerials` are the verifying half of that unbuilt layer and are kept for when it
lands.

### Structural enforcement (not runtime)

- `internal/tower` (the standalone half) **may make no outbound network call**, and may not so
  much as *link* Core's code in — both enforced by tests.
- `make reach` fails the build on any function nothing in the binary calls. This has caught
  five real bugs, including the Tower's entire durable-storage wiring (`storeFor`), which
  shipped tested-but-unwired and silently kept state on local disk.
- **What `make reach` cannot see**, measured rather than assumed: `deadcode` does not report
  an exported method whose receiver type is instantiated in production. That blind spot hid
  the entire renewal path — Core side and Tower side, written, tested, routed nowhere — which
  would have expired every Tower 24 hours after enrollment. `scripts/reachability.sh
  --methods` is the complementary sweep, and it runs as an advisory on every `make reach`.
- Every durable store has a **mem/Postgres parity suite** written deliberately differently — a
  held mutex against a conditional UPDATE — so agreement is a result rather than the same code
  asserted twice. It has caught real divergence.
- 90% coverage floor per package, no zero-coverage packages.

---

## 8. What Core gives up on the edge path, stated plainly

**Pre-dispatch content screening.** Core cannot moderate what it never sees. Screening for
Tower-served traffic becomes entirely post-hoc on a sampled fraction, so unscreened content
can be served and only found afterwards. That risk is accepted deliberately and sits with us,
not with the operator — which is why sampled audit is a build item rather than a nice-to-have.
No user-facing surface may describe edge traffic as pre-screened.

**Transit observation.** Settlement rested on Core's own byte counts. It now rests on two
signed statements from parties with opposing interests. That is **weaker than first-hand
observation and stronger than trusting either alone**.

---

## 9. Compensation

Not yet built. The attempt ledger exists so that *which attempt executed, exactly once, and
what its one terminal outcome was* is recorded before money moves, rather than reconstructed
from logs when somebody disputes it. Today Tower-backed work is free and says so
(`X-RogerAI-Cost: 0`).

When it lands, the measurable, Core-verifiable quantities are:

- **bytes relayed** — from the agreed usage in the receipt/ack pair, never the Tower's word;
- **attempts corroborated** — a Tower whose attempts reconcile is worth more than one whose
  do not;
- **availability** — canary success rate.

Earnings can be withheld on the same evidence that detects misbehaviour, which is what points
the incentive the right way.

**Prerequisite, from the audit:** there are two attempt state machines — `towercore/dispatch`
(the operational queue) and `towercore/attempt` (the hash-chained ledger). The broker keeps
them in step with a **best-effort** write that logs and swallows failures. Correct today, when
no money moves; a landmine at compensation, when a dropped settlement event means work served
and unpayable, or the reverse. The ledger write must become part of the same transaction as
the settlement, or settlement must be derived from the ledger rather than mirrored into it.

---

## 10. Current state

| piece | status |
|---|---|
| Tower enrollment, quarantine, lifecycle, leases | built |
| Station invitation, attachment, promotion, revocation, rehoming | built |
| Signed inventory, leaf verification, routable fleet projection | built |
| Relayed dispatch: grants, one-use claiming, receipts, settlement | built |
| Sealed envelopes (relayed path) | built |
| Attempt ledger (hash-chained, signed) | built |
| Blind SNI relay (`internal/relay`) | built |
| Station TLS identity, CSR/install, HTTPS serving | built |
| Edge grant (scope-bounded), consumer ack, corroborated settlement | built |
| Certificate + lease renewal (Core route and Tower schedule) | built |
| Reputation ledger (per-Tower outcomes, rate, flag/suspend) | built |
| Canaries (Core probes a Tower by using it) | built |
| Sampled transcript audit (Station-signed, checked against receipt digests) | built |
| Core-issued edge TLS certificates (`/tower/station/edge-cert`) | built |
| First-party edge consumer (`internal/edgeclient`, `roger-tower probe`) | built |
| Mutual TLS on the link, and therefore certificate revocation | **not built** — the Tower authenticates by signed requests instead |
| Canaries | **not built** |
| Sampled transcript audit | **not built** |
| Compensation / funding-source ledger | **not built** |
| Streaming to Towers | **not built** — a streamed answer can only be verified after the consumer has the bytes |
| Multiplexed link (today it long-polls) | **not built** — correct but chattier |
