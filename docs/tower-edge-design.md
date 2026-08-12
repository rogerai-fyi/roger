# Towers as edge brokers: taking load off Core

**Status: approved direction (founder, this session).** It changes the data path that
`features/tower/job_and_settlement.feature` describes; the scenarios listed at the end are
superseded and need their spec text updated to match.

Three decisions were taken with it:

- **All traffic is Tower-eligible**, with screening moving entirely to sampled post-hoc audit.
- **TLS passthrough with Core-provisioned certificates**, so unmodified clients work.
- **An attempt without a client acknowledgement still settles**, marked uncorroborated.

## The problem with what exists

Today every byte of a Tower-served request crosses Roger Core twice. A Tower adds a hop, a
long-poll and a queue, and offloads only the GPU time — which Core was never spending anyway.
It is a cost centre with extra steps, and there is nothing there worth paying an operator for.

For a Tower to be worth compensating it has to carry the **data plane**, leaving Core the
**control plane**. That is the whole design below.

## The shape

```
                 ┌───────────── control plane: small, constant-size ─────────────┐
                 │  1. authorize   2. settle                                     │
consumer ────────┴──▶ Roger Core ◀────────────────── receipt ── + ── ack ────────┘
   │                     (selects, grants, bills, enforces)          ▲     ▲
   │                                                                 │     │
   └──── data plane: the whole payload ────▶ Tower ────▶ Station ────┘     │
                                          (blind relay)   (executes)       │
   ◀────────────────────── answer ─────────────────────────────────────────┘
```

**Per request, Core handles two small messages** instead of the entire prompt and completion.
For a long completion that is three or four orders of magnitude less traffic. *That* is the
contribution a Tower operator is paid for: bandwidth, egress and reachability.

## Keeping the operator out of the content

A Tower operator is a stranger. Two ways to stop them reading traffic; they are not equally
good and the difference is client compatibility.

### Option A — TLS passthrough (recommended)

The Tower is a **TCP relay that never terminates TLS**. It reads the SNI to know which Station
a connection is for, and splices bytes. The TLS session runs end to end between the
**consumer** and the **Station**.

- The Tower sees: SNI, IP addresses, byte counts, timing, connection lifetimes. No content.
- **Unmodified clients work.** Anything that can speak to an OpenAI-compatible endpoint can
  use a Tower, because from the client's side it is an ordinary HTTPS host.
- Cost: each Station needs a certificate a normal client already trusts, for a name like
  `st-abc123.relay.rogerai.fm`. Core provisions it (DNS-01 under a domain Core controls) and
  hands it to the Station with its short-lived identity cert. Stations never see each other's.

### Option B — sealed application envelopes

What the code does today between Core and the Station: seal the payload to the recipient's
key and let the relay carry ciphertext. Extended to the edge, the *consumer* would seal to the
Station.

- No certificate machinery.
- **Only first-party clients can participate.** A plain OpenAI SDK cannot seal an envelope, so
  the entire third-party surface loses Tower capacity.

**Chosen: A.** B stays in the tree for the Core-to-Station path already built, which is still
what carries work on the existing relay route.

## Detecting a dishonest Tower without reading the traffic

Core cannot see the bytes, so it verifies the ends against each other. **Both parties either
side of the Tower report to Core independently, and the Tower can forge neither.**

| What a bad Tower might try | What catches it |
|---|---|
| Alter the request | Station's receipt commits to the request digest; the grant commits to the same. Mismatch = attributable to the relay |
| Alter the answer | Station's receipt commits to the response digest; the **client's ack** carries what it actually received. Two independent digests, and the Tower is between them |
| Drop or stall work | Client ack never arrives, or arrives with a failure. Repeated across a Tower's attempts, that is a reliability signal |
| Serve stale/cached answers | Grant nonce is one-use; a receipt for a nonce already settled is refused |
| Inflate usage to earn more | Settlement takes the **lower** of the Station's claimed usage and the client's observed usage |
| Route to a Station Core did not pick | The grant names one Station; only that Station's key can sign an acceptable receipt |
| Read the content | It never holds plaintext (Option A or B) |
| Silently serve nothing at all | **Canary attempts** — see below |

### The client acknowledgement

A small signed statement from the consumer: attempt id, response digest, token count, first-
byte and completion times. It costs one tiny request and it is what makes the Station's own
claim checkable by somebody with an opposing interest.

A consumer that never acks is not fatal: settlement rests on the Station's receipt alone and
the attempt is marked **uncorroborated**. Customers close laptops mid-stream and third-party
clients will never ack at all, and an operator who loses money for that is an operator who
leaves. A Tower whose uncorroborated share is unusual is investigated instead — the signal is
in the rate, not in the single attempt.

### Canaries

Core periodically issues attempts of its own through a Tower, indistinguishable from customer
traffic, whose correct answer it already knows. They cost a rounding error and they detect the
one thing attestation cannot: a Tower that is quietly not doing the job.

### Sampled transcript audit

Both ends have signed a digest of the exact bytes, so **neither can produce a different
transcript afterwards**. For a sampled fraction of attempts, Core asks the Station for the
full transcript and checks it against the digests both ends signed. That gives real audit
without carrying every byte, and it is the only route by which content moderation can still
happen for Tower-served traffic.

## What Core gives up, honestly

**Pre-dispatch content screening.** Core cannot moderate what it never sees, and the decision
is that all traffic is eligible anyway: screening for Tower-served requests becomes entirely
post-hoc, on a sampled fraction. Unscreened content can therefore be served and only found
afterwards. That risk is accepted deliberately and sits with us rather than with the operator,
which is why the sampled audit below is a build item and not a nice-to-have.

**Transit observation.** Settlement currently rests on Core's own byte counts. It would rest
instead on two signed statements from parties with opposing interests, which is weaker than
first-hand observation and stronger than trusting either alone.

## What this makes compensable

Now there is something to pay for, and it can be measured from things Core can verify:

- **bytes relayed** — from the agreed usage in the receipt/ack pair, not the Tower's word;
- **attempts corroborated** — a Tower whose attempts consistently reconcile is worth more than
  one whose do not;
- **availability** — canary success rate.

A Tower's earnings can be withheld on the same evidence that detects it misbehaving, which is
what makes the incentive point the right way.

## Approved scenarios this contradicts

These are in `features/tower/job_and_settlement.feature` and need re-approval before build:

1. *"Roger Core still sees content under the v1 policy contract"* — Core inspects the request
   before dispatch and the result before settlement. **Directly contradicted.**
2. *"Roger Core records channel-bound transit from its own observation"* — Core's own byte
   counts and receive times. **Replaced** by the receipt/ack pair.
3. *"A joined settlement requires a complete matching Core transit observation"* —
   **Replaced** by corroboration between the two ends.
4. *"The inner TLS session authenticates Roger Core and the selected Station end to end"* —
   the inner session becomes **consumer**-to-Station, not Core-to-Station.

Everything else survives unchanged: the attempt ledger, one-use grants, receipts bound to
digests, attachment-recorded keys, quarantine, the lifecycle.

## Build order

1. Station serves HTTPS directly, with a Core-provisioned certificate.
2. Tower gains SNI-based TCP passthrough, replacing the poll/relay path for the data plane.
3. Consumer flow: `authorize` → connect to Tower → `ack`.
4. Client acknowledgement object and the corroboration rule in settlement.
5. Canaries and sampled transcript audit.
6. Compensation, on the funding ledger, paying against corroborated usage.

**Prerequisite for step 6, found in the audit.** There are two attempt state machines:
`towercore/dispatch` (the operational queue - issued/claimed/settled, enforcing one-use under
a compare-and-swap) and `towercore/attempt` (the hash-chained audit ledger the money will
rest on). The broker keeps them in step with `noteAttempt`, which is **best effort**: a failed
`Commit` is logged and swallowed, and the deadline sweep eventually closes a chain that missed
an event.

That is correct today, because no money moves. It stops being correct at step 6, when a
dropped `SettlementCommitted` means work that was served and cannot be paid for - or, worse,
the reverse. Before compensation lands, the ledger write has to become part of the same
transaction as the settlement it records, or settlement has to be derived from the ledger
rather than mirrored into it. Do not build step 6 on top of a best-effort write.

Steps 1–3 are the load reduction. Steps 4–5 are what make it safe to pay for.
