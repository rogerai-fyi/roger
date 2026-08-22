# RogerAI Tower network plan

> **ARCHIVAL — a plan, largely executed and partly superseded.** The network this document
> planned shipped in a different final shape: the leaf-station generation (roger-station,
> the TLS-splice relay, invite files) was built, then retired in favor of the sealed hub -
> providers run `roger share` and self-attach, towers host the sealed data plane,
> and settlement pays node 70 / tower 10 / platform 20 at the node's own listed per-token
> price. Read `docs/tower.md` for what runs today; this file remains as the planning record.

Status: **proposed for founder approval; no implementation is authorized by this document**

Last updated: 2026-08-02

## Research contract

### Outcome

Ship a downloadable `roger-tower` package that can run in one of two deliberately
separate modes:

1. A **joined Tower** is an independently operated, broker-like child relay in the
   existing public RogerAI network. RogerAI remains the admission, routing, policy,
   trust, and settlement authority.
2. A **standalone/private Tower** is a locally governed broker network for a machine,
   LAN, or cluster. It cannot advertise to, settle through, or silently contact the
   public RogerAI network.

The public network must be able to grow without giving an independent Tower operator
access to RogerAI's databases, message bus, wallets, platform signing keys, or admin
authority. A compromised joined Tower may interrupt traffic, but it must not be able to
forge work, alter a result unnoticed, replay work for payment, or change settlement.

### Constraints

- RogerAI remains the public-network authority in the first release. Eliminating that
  authority is not a goal.
- Existing clients continue to connect to RogerAI, not to a hand-picked community Tower.
- Public moderation, holds, recounting, settlement, bans, and final receipts remain in
  Roger Core.
- A community operator is assumed to have root on its host and may modify the binary,
  read its process memory, lie, replay, collude, or selectively drop traffic.
- The design must work on an ordinary Linux host, container runtime, or Kubernetes.
- The repository's spec-first approval gate applies before tests, step definitions, or
  production code are written.
- Public artifacts and documentation remain infrastructure-provider-neutral.

### Proposed success thresholds

These are release gates, not current measurements:

- Mutation, substitution, replay, or fork of every security-relevant control, job,
  settlement, compensation, and payout field is rejected before it gains authority.
- A duplicate job or receipt can debit and credit the ledgers at most once.
- Compensation funding slices conserve job cost and Station earning exactly; payment events,
  compensation deltas, payout reservations, and rail calls are idempotent under duplication,
  reordering, crash, and ambiguous response.
- A balanced per-currency journal and independently replayed control totals prove that the
  current entitlement target across every operator equals the grant-rate-weighted policy
  ceiling and never exceeds source-derived eligible net revenue.
- No Tower share becomes payable before its exact external-cash lineage, processor fee,
  reversal maturity, operator eligibility, and Core-observed session attribution are known.
- Fee finality and rounding dust have finite incident/review deadlines; neither uncertainty
  nor account closure guesses a fee, loses an operator liability, or creates a zero payout.
- A revoked or expired Tower receives no new jobs within 60 seconds.
- Joined-Tower loss drains or fails active work cleanly and releases every unsettled hold.
- The joined Tower has no public-network database, message-bus, payment, admin, session,
  moderation, or platform-signing credential.
- Packet capture at the joined Tower contains no prompt or completion plaintext unless
  that operator also controls the selected serving Station.
- A default standalone Tower makes no RogerAI network connection and exposes no public
  advertisement or RogerAI settlement route.
- A 1-vCPU/1-GiB joined Tower sustains 100 concurrent text streams without OOM, with less
  than 5% throughput loss and no more than 50 ms added p95 time-to-first-token in a
  same-region controlled benchmark.
- Release artifacts fail verification if a signature, digest, identity, or provenance
  predicate is missing or wrong.

The resource and performance numbers are engineering targets. They become published
minimums only after reproducible load, long-stream, audio, reconnect, and chaos tests.

## Vocabulary

| Term | Meaning |
|---|---|
| **Tower** | Public product name for software that performs broker-like registry, relay, and routing work. "Broker" remains the current code/API term. |
| **Roger Core** | RogerAI-controlled public authority: identity, directory, policy, routing, moderation, holds, settlement, revocation, and final signatures. |
| **Joined Tower** | Independently hosted child Tower admitted to the public RogerAI network as an untrusted, scoped data-plane relay. |
| **Standalone/private Tower** | A separate local network with its own trust root and no path to public RogerAI discovery or money. The configuration value is `standalone`. |
| **Station** | A leaf inference provider attached directly to Roger Core or through a Tower. |
| **Private band** | An existing hidden band on the public broker. It is not a standalone/private Tower and must not be marketed as one. |

## Decision summary

1. Do **not** distribute the current `rogerai-broker` to community operators.
2. Build a separate, least-privilege `roger-tower` daemon.
3. Treat every joined Tower as hostile. It gets short-lived, capability-scoped identity,
   never shared trusted-replica credentials.
4. Keep each remote Station visible to and selected by Roger Core. A Tower is transport
   and local fan-out, not an opaque provider, price authority, or token-count authority.
5. Tunnel an authenticated, encrypted Roger-Core-to-Station session through the Tower so
   the Tower relays ciphertext. Roger Core still sees content for the present policy and
   recount contract.
6. Replace the mutable receipt with a Station assertion and a separate Roger Core
   settlement receipt. A Tower transit statement is attribution only.
7. Start with a centralized append-only SQL ledger and signed checkpoints. Blockchain is
   unnecessary while RogerAI intentionally remains the authority.
8. Ship standalone/private mode before accepting arbitrary joined public operators.
9. Founder ruling (2026-08-01): Tower operators CAN earn - an opt-in **compensated tier**
   paying a founder-set share (initially **10% of net platform revenue**) on token sales
   settled through their Tower. Admission alone still earns nothing. Accrual requires all
   three verifications the founder named: (a) the settlement receipt binds the issued
   dispatch lease to Core-observed envelopes on that Tower-authenticated session plus a
   consistent Tower statement (identity attribution, not physical-path proof), (b) the
   consumer charge is captured, funded by real money (not grant
   credit), and past the reversal window (we actually received the funds), and (c) the
   operator holds a verified payout identity with terms accepted (the operator is payout-
   eligible, not operationally trusted, and can be suspended/revoked with withhold,
   forfeiture, and clawback). Full behavior:
   `features/tower/operator_revenue_share.feature`. Compensation ships no earlier than the
   Phase 3 beta, after the Phase 0 money-trust repairs and the joined protocol are proven.
   A ledger-wide per-currency backstop independently derives the sum of current nonnegative
   per-settlement net revenue and rate-weighted entitlement across all operators. This keeps
   the founder's per-settlement ruling—an unrelated negative-margin job contributes zero but
   does not claw another operator—while preventing duplicated or omitted source lineage from
   pushing aggregate entitlement above the source-derived program ceiling.

## Why the current broker cannot be the package

The current broker combines trusted control-plane and data-plane responsibilities:
public authentication, user sessions, Station registry, relay, moderation, wallets,
holds, settlement, payouts, grants, admin functions, and platform signatures. Its
multi-instance mode is a trusted-replica design in which instances share PostgreSQL,
Valkey, registrations, bearer bridge tokens, jobs, results, and the same broker identity.
That is appropriate only inside one administrative trust boundary.

A community copy of that process or access to its shared store would be able to impersonate
Stations, inspect confidential registrations, affect money, and acquire authority far beyond
relay. The existing client-side "federation" setting merely points a client at a different
broker URL, creating a separate network; it does not add a child to RogerAI's network.

The package therefore needs a new protocol and command, not a deployment guide for the
existing trusted broker.

## Threat model

### Protected assets

- prompts, completions, and user identity;
- public Station/Tower directory and routing decisions;
- consumer balances, holds, charges, provider earnings, and grants;
- job and receipt integrity, ordering, and auditability;
- RogerAI signing, admin, session, pseudonym, evidence, payment, database, and bus secrets;
- availability and the reputation of the public network;
- update artifacts and enrollment credentials.

### Adversaries

- a malicious Tower operator with root and a modified daemon;
- a malicious or compromised Station, with or without Tower collusion;
- a client colluding with a Station or Tower;
- an attacker with a copied Tower key or enrollment token;
- an on-path attacker, replaying peer, or dependency/update compromise;
- a well-meaning operator with a stale version, bad clock, wrong mode, broken storage, or
  overloaded node.

### What the mechanisms do and do not prove

- A digital signature proves that a key signed exact bytes. It does not prove a person's
  identity, geography, hardware, model, token count, or honesty.
- Mutual TLS authenticates possession of an admitted peer key and protects a channel. It
  does not make the peer's host or software trustworthy.
- An artifact signature proves which artifact was released. It does not prove that a root
  operator is still running that artifact.
- A hash link makes a supplied link tamper-evident. Without an authority-held sequence and
  head, it does not prove completeness, order, or absence of forks and resets.
- A TEE may later prove a measured boot state tied to a nonce and key. It is not required
  for v1 and must not be presented as proof of exact model weights, output, or accounting
  without separate evidence.

Availability cannot be cryptographically forced: a Tower can always delay, censor, or drop
traffic. The controls are timeouts, no-charge failure, health scoring, circuit breaking,
probation, and failover.

## Target architecture

```text
                                  public control + money plane
Client  ===== TLS =====>  Roger Core
                          |  auth / moderation / routing
                          |  hold / recount / settlement / audit
                          |
                          | outer TLS 1.3 mTLS (Tower identity)
                          | + inner TLS 1.3 Core<->Station mTLS session
                          v
                    Joined Tower             untrusted child data plane
                    |     |     |
                    v     v     v
                 Station Station Station      signed leaf assertions
```

The joined Tower opens the parent connection outbound over TCP 443, which works through
ordinary NAT and avoids a mandatory public inbound control port. Station-facing listeners
are independently configured and authenticated. A Kubernetes Service is needed only when
Stations cannot reach the Tower through the pod network.

Both outer and inner channels require TLS 1.3 and disable TLS early data/0-RTT for every
control or job message. Each signed `channel_binding_hash` is a domain-separated hash of the
RFC 9266 `tls-exporter` binding plus network ID, protocol version, endpoint roles, and the
Core-assigned session epoch. It is an identifier, not a secret. Resumption or reconnect gets
a new epoch and binding; v1 never moves an attempt onto it.

Roger Core flattens approved remote Station offers into its directory and selects the exact
Station. The Tower multiplexes streams, applies local resource limits, and reports transport
evidence. It cannot choose a more expensive Station, change the quote, settle usage, or make
itself publicly discoverable.

The first release deliberately leaves Roger Core in the byte path. Joined Towers expand
Station attachment fan-out, operator diversity, and relay failure-domain choices; they do
not by themselves scale Roger Core. Core remains the public byte-bandwidth, availability,
routing, moderation, recount, ledger, and settlement bottleneck, and a single region also
bounds client latency. Multi-Tower load must therefore be gated against Core saturation and
Core failover. Regional Core ingress or direct client-to-Tower routing is a later phase
requiring a separate privacy and authoritative-metering design.

## Trust and key hierarchy

Use separate keys for separate powers:

- offline public-network root and replaceable enrollment intermediates;
- Roger Core TLS service identity;
- Tower admission certificate issuer;
- Station secure-session certificate issuer;
- Tower admission-lease signer;
- Tower lifecycle-event signer;
- Station lifecycle-event signer;
- Station admission/origin signer;
- Station epoch-reset signer;
- public-directory signer;
- trust-document signer;
- root-delegated trust-document publication signer, distinct from the document signer;
- Tower-compensation-policy, funding-allocation-policy, payout-policy, fee-finality-policy,
  maturity-policy, payout-eligibility-policy, compensation-enforcement-policy, and
  debt-writeoff-policy signers, all distinct from the trust-document and publication signers;
- compensated-capability, consumer-cash-credit, platform-grant-credit, and
  funding-source-ledger signers;
- payout-identity-verification, operator-account-status, payout-terms-acceptance,
  sanctions-screening, payout-jurisdiction, payout-destination-verification, and
  tax-profile-fact signers;
- compensation-enforcement-finding and debt-writeoff-approval signers;
- attempt-state signer, distinct from both dispatch and execution-grant signers;
- Tower dispatch-lease signer;
- inner execution-grant signer;
- Core-transit-observation signer;
- final settlement-receipt signer;
- Tower-compensation ledger signer, compensation-forfeiture decision signer,
  debt-writeoff decision signer, and payout authorization credential;
- distinct maturity-authority signer;
- payout-eligibility decision signer and payout-eligibility incident signer;
- tax-withholding decision signer and tax-correction incident signer;
- fee-finality incident signer;
- payment-webhook authentication, payment-reconciliation, and payout-rail credentials;
- mandatory compensation-ledger-head signer and separate public-transparency checkpoint signer;
- web-session HMAC;
- pseudonymization HMAC;
- admin authentication;
- moderation/legal-evidence encryption;
- a local Tower identity statement key;
- a rotating Tower TLS key and short-lived certificate;
- distinct Tower-local Station bridge-authority and bridge-certificate keys that have no Roger
  Core validity;
- each Station's own signing and secure-session key.

No joined Tower receives any Roger Core private key. No two roles may resolve to the same
raw key, KMS/HSM key identifier or alias, or derived-key root. In particular, the current
overloaded broker seed must not be reused as a Tower secret or admin credential.

Tower certificates use ordinary X.509 validation and a stable workload identity such as
`spiffe://rogerai.fm/tower/<tower-id>` in the URI SAN. Certificates are short-lived,
automatically rotated, serial-numbered, and checked against active admission state. A copied
credential can be revoked or allowed to expire; it cannot acquire another capability.

## Joined-Tower enrollment and lifecycle

### Enrollment

1. The operator downloads a signed release and runs `roger-tower init --mode=joined`.
2. The command creates distinct local identity and TLS keys, stores private material with
   owner-only permissions, and prints a short browser URL/code.
3. The operator signs in to RogerAI, accepts operator terms, names the Tower, and consumes a
   short-lived one-time enrollment token.
4. The Tower opens an outbound TLS connection, answers a fresh server nonce, proves its
   identity key, and submits a CSR bound to the approved account and Tower ID.
5. Roger Core checks token use, owner state, protocol and minimum version, clock, quotas,
   requested capabilities, and key uniqueness. It issues a scoped short-lived certificate
   and lease.
6. The Tower enters `quarantine`. Health checks, randomized canaries, resource limits, and
   inventory validation run before any traffic is assigned.
7. Roger Core alone changes the lifecycle state and public directory visibility.

### States

```text
pending -> quarantine -> active -> draining -> expired
                    \-> suspended -> revoked
```

- `pending`: enrollment exists but proof or approval is incomplete.
- `quarantine`: authenticated but restricted to probes or explicitly bounded beta traffic.
- `active`: eligible within centrally assigned weights, scopes, and limits.
- `draining`: no new jobs; existing leases may finish until their deadlines.
- `suspended`: reversible policy or health exclusion; no new jobs.
- `revoked`: credential and Tower identity are denied; re-enrollment requires policy action.
- `expired`: the lease/certificate lapsed; no offline work may later claim public settlement.

The exhaustive allowed/forbidden transition and active-attempt-action matrices are in
`public_enrollment.feature`; `revoked` is terminal, while an expired identity can return
only to quarantine after fresh proof, credentials, lease, and probes.
Every `TowerLifecycleEventV1` and `StationLifecycleEventV1` signs its independently assigned
lifecycle-ledger commit tuple; effective time cannot predate that commit, and key validity is
resolved there. Admission leases and Station attach/origin/reset/rehome authorities likewise
carry their own authorization- or origin-ledger commit tuples and exact atomic-group indices.
Their current heads and one-use sources are CASed at commit and again wherever routing,
granting, evidence, or payout authority consumes them; signer issue claims never select trust.

All claimed region, hardware, capacity, and model data remain untrusted metadata until
measured or otherwise verified. Roger Core records declared and measured values separately.
Public-network membership requires an account-bound owner. The separately authorized
compensated tier additionally requires verified payout identity, tax/region eligibility,
current terms, and centrally enforced withholding, forfeiture, and clawback controls. V1
issues payouts only when the authoritative tax decision requires zero withholding. A
nonzero or unknown withholding requirement keeps the operator's lots in signed `withheld`
state; it never creates a payout instruction. Supporting nonzero withholding requires a
separately approved withholding-liability/remittance state machine and authenticated tax
authority integration.
Current payout eligibility is a separate purpose-signed, operator-scoped revision chain. It
binds account, identity, destination, terms, region/sanctions, currency, and rail scope. The
payout send fence compare-and-swaps that decision; a restriction before the fence aborts or
voids the preparation, while a restriction discovered after the fence cannot redirect or
erase the immutable rail instruction and instead creates a separately signed incident.
Account deletion becomes `closure_pending` until prepared/submitted work is resolved and can
never erase retained monetary liability, debt, receipts, or reconciliation evidence.
Every payout-failure descriptor binds exact `ClassificationAuthoritySetV1` and
`HoldReferenceSetV1` values; other classification transitions bind their closed direct
authority/evidence fields. The resulting live payout lot persists its transition-effective
`HoldReferenceSetV1` hash. Hold kinds and authority/result shapes are closed; all applicable tax,
eligibility, reconciliation, policy, enforcement, provider-unavailability, and dust-review
holds coexist instead of overwriting one another. A rail failure cancels pending-negative
affected ranges first and classifies every surviving range with the complete canonical set.
Terminal lots carry the canonical empty operational hold set while their signed transition
and prior state preserve forensic authority history, so later hold closure cannot mutate a
terminal monetary record.

## Inventory and routing protocol

Every joined link negotiates a version and capabilities before inventory or work can flow.
Unsupported versions, missing mandatory security capabilities, expired policy versions, or
versions below the centrally signed floor are rejected.

The Tower sends revisioned, signed inventory snapshots or deltas. Admission leases,
inventories, Station offers, public-directory snapshots, and transparency checkpoints each
have a fixed series identity, revision-1/prior-absence genesis, exact next revision and
immediate-prior hash, one current-head CAS, idempotent exact replay, and gap/fork/overflow
rejection. A full inventory resynchronization is still the exact next chained revision, not a
new genesis. Each leaf offer includes a Station identity and Station signature; direct offers
use the same offer-head contract through a Core registry transaction without inventing a
Tower inventory. Roger Core verifies the current offer, origin, lifecycle, Tower lease,
session, and inventory heads, applies bans, price policy, owner policy, liveness, and measured
capacity, then stores the offer with its exact origin. Public-directory and checkpoint
objects add independently assigned Core publication tuples and derived accepted trust
publication anchors; live use also requires current nonrevoked keys, while historical
verification retains its anchored time-qualified result. Local Station bridge tokens never
leave the Tower.

### Attaching Stations

`roger-tower station invite` starts an explicit one-time flow. A public Station is admitted
as either direct-to-Core or joined-behind-one-Tower, and that origin kind is immutable for its
Station ID in v1. The verified owner authorizes exact assertion and secure-session public keys
plus one portable capability-ceiling hash. Direct admission creates a chained
`DirectStationOriginAuthorityV1`; joined admission creates a chained
`StationOriginLeaseRevisionAuthorityV1` and `StationOriginLeaseV1`. Both carry a Station
origin epoch for placement and a separate Station assertion epoch for the signed receipt
chain, and both are checked independently from the state-only `StationLifecycleEventV1`
head. Joined-to-joined rehome advances only the origin epoch and continues the same assertion
epoch/head. Direct-to-joined or joined-to-direct migration requires revoking the old identity
and allocating a new Station ID.

The Station proves both private keys before the one-time authorization is consumed and its
short-lived inner-session certificate becomes usable. Epoch reset is one acyclic all-or-none
bundle: `StationEpochResetV1`, then the exact joined revision-authority/origin-lease branch or
direct-origin-authority branch with replacement keys/certificate, then the next active
lifecycle event. Failure leaves the old epoch closed and nonserving. In standalone mode, the
same structural separation is authorized solely by the local administrator and local trust
root. Rotation, detach, revocation, reset, and joined rehome fence old credentials and
preserve history; `station_attachment.feature` is the exact contract.

The control plane uses separate strict canonical objects rather than unsigned JSON with an
ambient session identity: `TowerEnrollmentProofV1`, `TowerAdmissionLeaseV1`,
`TowerInventoryV1`, `StationOfferV1`, `TowerLifecycleEventV1`,
`StationLifecycleEventV1`, `StationAttachAuthorizationV1`,
`StationOriginLeaseRevisionAuthorityV1`, `StationOriginLeaseV1`,
`DirectStationOriginAuthorityV1`, `StationRehomeLeaseV1`, `StationEpochResetV1`,
`PublicDirectorySnapshotV1`, `RootDelegationV1`, `RogerTrustDocumentV1`,
`RogerTrustPublicationV1`, and the later `RogerLedgerCheckpointV1`. Initial Tower admission
is an acyclic lifecycle-then-certificate/lease atomic bundle; renewal and TLS rotation append
only the next lease against the unchanged lifecycle, while restrictions invalidate the old
lease synchronously. Each object carries its network, schema, purpose-bound signer,
revision/expiry, and relationship identifiers; full field and mutation contracts are in
`control_plane_tamper_matrix.feature`. Standard X.509 path and key-usage checks remain an
independent requirement rather than a replacement for application-object signatures.
Every security-relevant control collection has a named strict JCS preimage rather than an
implementation-defined "array hash": requested, admitted, and runtime-declared Tower
capabilities; inventory operations and resulting Station-offer state; Station capabilities
and modalities; supported protocol versions; and public-directory entries. Each set binds
its owner and scope, exact closed member schema, member count, canonical sort key, duplicate
rule, and permitted empty shape. Relationship checks only narrow capability authority from
request through admission, runtime declaration, Station authorization, and routing.

Before creating a hold or attempt, Roger Core atomically consumes a high-entropy,
client-generated nonce/idempotency key that is covered by the client's request signature
together with account, method, path, network, and body digest. An exact retry resolves to the
one existing authoritative request; reuse with different signed context is a conflict. A
timestamp window by itself is not replay protection, and failure of the durable replay store
prevents a new hold or grant.

For each selected job, Roger Core first creates a Tower-visible, signed
`DispatchLeaseV1`. It contains only the network/protocol version, job and attempt IDs,
Tower and Station IDs, Tower certificate serial, Tower session epoch/channel binding, exact
current Tower admission-lease series/ID/sequence/hash and lifecycle revision/hash, an opaque
encrypted-request-envelope digest, a preselected `AttemptIssueCommitmentV1` ID/index and
independently assigned Core attempt-issue tuple, deadline, stream/byte limits, and one-time
nonce. It contains no
prompt-derived digest, price, model parameter, user identity, or plaintext. The Tower can
route only a lease scoped to itself and cannot change its Station, ciphertext, bounds, or
deadline.

Inside the encrypted Roger-Core-to-Station session, Roger Core sends an immutable, signed
`ExecutionGrantV1` containing exactly the following semantic fields (the exhaustive wire
field and origin-shape contract is in `job_and_settlement.feature`):

- origin kind, network and protocol version;
- job ID, request ID, attempt ID, client key ID, client nonce/idempotency key, and separate
  grant nonce;
- Station ID, assertion key ID, separate Station origin and assertion epochs, secure-session
  certificate serial, inner-session epoch/channel binding, exact current Station lifecycle
  revision/hash, and exact current joined-origin-lease or direct-origin-authority
  ID/revision/hash for every origin;
- Tower outer-session epoch/channel binding plus Tower ID, certificate serial, exact current
  admission-lease series/ID/sequence/hash, and exact current Tower lifecycle revision/hash for
  joined origin, or canonical absence of every Tower field for direct origin;
- model and offer/quote ID;
- authoritative consumer and Station-earning input/output integer rates, currency/unit/scale,
  maximum tokens, and maximum cost;
- request-body digest, policy version, issue time, execution deadline, and signed
  settlement-finalization/hold ceiling;
- exact universal funding reservation/set and published funding-allocation-policy hashes,
  plus the grant-time compensation-snapshot/policy hashes or their canonical uncompensated
  absences;
- permitted result size and modality.

The signed `ExecutionGrantV1`, optional `DispatchLeaseV1`, disclosure-safe
`AttemptIssueCommitmentV1`, and private money/hold-bearing issued `AttemptEventV1` are built
acyclically and commit atomically. The grant and lease preselect the commitment ID/index/Core
tuple; the commitment signs their complete hashes; the event signs the commitment hash and
the exact private state. Towers and Stations receive and verify the commitment, while the
money- and client-bearing attempt event remains Core-private. The independent attempt-ledger
tuple—not a signer clock—anchors all four issuance relationships.

The Station verifies the Core signature, its own and Tower identities, the outer lease,
expiry, nonce, sequence, model, limits, and request digest before executing. Roger Core
keeps the one state machine specified in `attempt_lifecycle.feature`; each transition is a
purpose-signed revision linked by immediate prior hash and an independently assigned
attempt-ledger commit tuple: `issued`, `leased`,
`executing`, `evidence_complete`, then exactly one terminal `settled`, `failed`, `expired`,
or `cancelled` state. There is no in-place retry or `superseded` state. V1 attempts do not
migrate across Tower sessions: reconnect fences the old session,
unfinished attempts fail, and retry receives a new attempt, nonce, and session-bound lease.

The settlement-finalization/hold ceiling is strictly later than the execution deadline and
no later than the centrally signed maximum finalization interval. Timely complete evidence
may retry settlement only before that ceiling. If the ceiling sweep wins the settlement CAS,
the attempt fails with `core-finalization-timeout`, the consumer hold is released, no Station
or Tower earning or protocol-level platform liability is created, and a durable incident
reference preserves the evidence for separately authorized remediation. If the authoritative
attempt/hold store itself is unavailable at the ceiling, the first recovery transaction
applies that ceiling outcome rather than charging late. Thus an unavailable settlement signer
cannot reserve a consumer balance indefinitely.

Prompts and completions travel inside a Roger-Core-to-Station TLS 1.3 mutually authenticated
session
tunneled over the Tower's outer mTLS link. The Tower necessarily sees routing metadata,
timing, byte counts, peer addresses, and failures, but it does not receive plaintext merely
because it relays the stream. Roger Core sees plaintext under the current moderation and
recount contract. This is not client-to-Station end-to-end privacy.

## Receipt v2 and authoritative ledger

Do not mutate a Station-signed receipt after verification. Keep leaf claims, relay claims,
Core-observed transport, final settlement, and later compensation as separate objects.

V1 signed application objects use strict UTF-8 JSON and RFC 8785 JCS canonical bytes.
Decoding rejects duplicates, unknown fields, trailing bytes, explicit null for absent fields,
and non-NFC schema strings before signature verification. Security, sequence, time, count,
rate, and money integers are bounded canonical base-10 strings (zero or a nonzero digit
followed by digits), never JSON/IEEE-754 numbers; digests, public keys, and signatures use
fixed-length unpadded base64url. Signing bytes omit only that object's signature member and
prepend a network/object/version domain separator. Complete-object hashes cover the exact
canonical object including its signature. The initial application-signature suite is
Ed25519; a different suite requires a new signed protocol policy/version rather than
algorithm negotiation from an untrusted object.

### `ProviderAssertionV2`

`ProviderAssertionV2` is a public-network object for only the `direct` and `joined`
origins. A standalone network never emits or accepts it.

The Station signs an immutable, versioned canonical encoding of:

- network/protocol, job, request, attempt, dispatch lease, grant, Tower, Tower certificate,
  client, Station, and signer key IDs;
- the client nonce/idempotency key, grant nonce, lease sequence, signed execution deadline,
  settlement-finalization/hold ceiling, and bounds;
- origin kind, Station inner-session binding, optional joined Tower outer-session binding,
  Station origin epoch, and direct-versus-joined canonical presence/absence rules;
- model and offer/quote ID;
- request and response digests;
- provider-claimed input/output counts and result status;
- start/end timestamps;
- per-Station persistent epoch, sequence, and previous assertion hash;
- the full `ExecutionGrantV1` hash.

### `TowerTransitStatementV1`

The Tower may sign the dispatch-lease hash, opaque encrypted request/result envelope
digests, byte counts, receipt times, local route ID, Tower sequence, and status. This
supplies attribution and operational evidence without exposing content-derived hashes to
the relay. It proves only that the Tower key made that statement; it is never authoritative
for physical location, price, tokens, content, settlement, or compensation by itself.

### `CoreTransitObservationV1`

Roger Core records and signs what it directly observed on the authenticated Tower session:
Tower ID and certificate serial, session epoch/channel binding, dispatch-lease hash, opaque
request/result envelope digests and byte counts, job/attempt IDs, and first/complete receive
times from Core's clock. After the complete result and `ProviderAssertionV2` are durably
stored, Core also assigns one evidence-complete authority tuple of Core time plus a unique
commit sequence. This proves that Core exchanged the bound envelopes on a session
authenticated to that Tower key. It does not prove where the operator physically ran the
key. A consistent Tower statement may corroborate this Core evidence but cannot replace it.

### `SettlementReceiptV2`

Roger Core verifies exact job context before signing an immutable settlement receipt:

- origin kind and the complete provider-assertion, Core-transit-observation, and optional
  Tower-statement hashes;
- all job, request, attempt, dispatch-lease, grant, Tower, Tower-certificate, Station, model,
  session, and key IDs;
- client/grant nonces, lease sequence, deadline, and every byte/token/result/cost bound;
- request/response digests and authoritative policy/quote versions;
- provider claims and independently recounted counts;
- effective consumer and Station-earning input/output rates, billed counts, hold, actual
  consumer cost, Station earning, grant, and disposition;
- Core-observed evidence times, append-only ledger sequence, previous ledger hash,
  settlement timestamp, and signer key ID;
- exact immutable grant/external-cash funding slices whose signed source and job-cost
  intervals, funding-policy order, and consumer/Station allocations conserve the settled totals;
- compensated-tier policy/candidate status and reason, without pretending later payment
  events have already become final.

The settlement signature covers every final field. Clients verify the Core signature and,
when desired, the Station and Tower evidence without moving one signature into another
field or reconstructing a mutable object.

For a direct Station settlement, Tower-statement status is present as `not_applicable`, while
Tower identity/certificate/session, Core Tower-session observation, transit-statement hash,
rejection reason, and compensation fields are canonically absent. For a joined attempt, a valid
Core transit observation is required; the Tower statement is optional for Station/client
settlement but required, with the Core observation, for a compensation candidate. Missing or
bad Tower corroboration produces a deterministic ineligible-compensation reason and trust
event without giving the Tower power to block an otherwise exact Station settlement.
Missing, invalid, or late required Core transit observation is different: a joined attempt
cannot settle and therefore has no candidate. A Tower statement received after a
missing-corroboration receipt is audit-only and cannot upgrade the immutable candidate.

Before any debit or credit, Roger Core must compare every returned identifier, digest,
model, key, time, limit, and sequence with the authoritative issued job. The ledger
transaction uses the authoritative job/request ID, never an untrusted receipt ID, and
atomically marks the attempt settled. Any exact-context mismatch, duplicate conflict,
missing required state, storage error, or bad signature fails closed and releases or
preserves the hold according to the single authoritative job state. Provider-chain gaps are
bounded audit/reputation events and must never hold a consumer balance indefinitely; an
actual duplicate-sequence fork freezes affected provider payouts and triggers quarantine.

Compensation uses named authorities at three stages rather than one ambient "eligible"
flag. Grant issue commits an exact `GrantCompensationSnapshotV1` binding the Tower/operator,
lifecycle, current `CompensatedTowerCapabilityV1`, all five eligibility-fact heads, current
`TaxProfileFactV1`, payout verification, accepted terms, the byte-identical tax-profile
identity/jurisdiction relationships, policy/rate, universal `FundingAllocationPolicyV1`, exact
funding reservation, and Core issue tuple;
the execution grant and later settlement bind its complete hash. Job settlement fixes the
immutable candidate from that snapshot and timely required evidence. Later entitlement
reconciliation uses current authenticated payment and maturity facts; enforcement disposition
is a separate target-neutral state machine. Payout preparation requires current
identity, payout policy, purpose-signed operator eligibility, and transactional control totals,
then commits with tax decision, signed compensation head, instruction, and send fence absent.
Tax authority decides that exact preparation; the later instruction and send fence separately
bind and recheck every current authority revision. Drain, ordinary expiry, or a
non-security suspension stops new grants but allows already-issued work whose complete
evidence Core observed before the minimum of its grant deadline and any signed drain ceiling.
A lifecycle event has exactly one validated active-attempt action:
`not_applicable`, `drain_until(cutoff)`, or `cancel_at(cutoff)`. Nonrestrictive transitions
use `not_applicable`; security revocation and key compromise always use `cancel_at`.
Evidence not completely and durably observed before the applicable cutoff cannot settle,
even if a prefix arrived earlier. Settlement therefore verifies the grant-time
snapshot and evidence-observed cutoff, not merely whether the Tower happens to be `active`
when the database transaction runs.

Timeliness uses half-open ordering. The evidence-complete authority tuple must be strictly
before the lifecycle cutoff tuple, and its Core time must be strictly before the signed job
deadline. Equality is late. If completion and a lifecycle event share the same stored clock
precision, their unique Core commit sequences break the tie; Station and Tower timestamps
are signed claims only.

### `TowerCompensationReceiptV1`

Job settlement creates only a compensation candidate. Later append-only, signed compensation
events reference the immutable `SettlementReceiptV2` plus authoritative cash-capture,
processor-fee allocation, refund/dispute, maturity, operator-eligibility, and enforcement
events. Candidate state is immutable `eligible` or terminal `ineligible(reason)`. Changing
cash entitlement is a separate cumulative aggregate. Each delta signs a closed plan of
nonoverlapping half-open atom-range applications ordered by contiguous plan-local delta range.
Each descriptor separately binds its causal positive source-event range selected by the signed
recognition or reverse-recognition rules. Every descriptor owns exactly one transaction-start root and splits at lot, debt, recovery,
funding-slice, or application-kind boundaries. Its application events and all affected
aggregate, lot, debt, pending-recourse, enforcement-disposition coverage, dust-cycle,
journal, and control states commit in one serializable transaction. Positive applications
recover same-currency debt in one total order—root create sequence/group, root stable ID,
then leaf range and stable ID—and assign any excess to a separate `lot_create` descriptor.
Payout-lot states are `immature`, `mature_payable`, `withheld`, `partitioned`,
`reserved_prepared`, `reserved_submitted`, `paid`, `cancelled`, or `forfeited` only through
`compensation_state_machines.feature`; paid forensic lots remain paid and partial recourse is
represented by separate nonoverlapping `DebtRangeV1` records. A preparation with no instruction is signed-aborted;
one with a signed instruction is signed-voided only before its durable rail-send fence. After
that fence, the submitted reservation remains locked until
authenticated rail reconciliation. A negative revision against submitted atoms creates one
exact `PendingSubmittedNegativeV1` per transaction-start lot range without splitting or
releasing the instruction. Authenticated failure partitions each returned lot once across
the complete nonoverlapping pending set, cancels only affected ranges, and classifies every
unaffected range against the complete current authority/hold set. Success atomically appends
`payout_succeeded` plus one canonically ordered zero-additional-posting `debt_create` per
pending record, exhausting the set. A later reversal of compensation that previously
recovered old debt reopens the same affine-mapped recovery/debt subrange only. A later
authoritative payment reduction first derecognizes either current `unpaid_forfeiture` or
`paid_clawback` enforcement coverage without rewriting its forfeited/paid forensic lot or
creating duplicate debt. Operator debt is a separate currency-scoped state. This
separation preserves an immutable job receipt while funds can clear, reverse, or be
disputed. No Tower statement or job receipt alone can create a payable balance.

Maturity is likewise not a timer asserted by the compensation signer. Each new external-cash
lot binds the unique greatest applicable signed `MaturityPolicyV1` and the exact causal
`AuthoritativePaymentRevisionV1` member from its funding lineage; those inputs derive its
required Core time and sequence without caller choice. A distinct maturity authority may
transition exact ranges only after an exhaustive `MaturitySourceRevisionSetV1` proves the
latest authenticated payment state and its serializable actual tuple passes every bound
deadline. Policy expiry stops selection for new lots but does not strand already-bound
historical lots; signed compromise cutoffs still apply. Window values remain deliberately
unset until rail rules and reversal-lag evidence support the compensated-beta gate.

All compensation arithmetic uses checked integer accounting quanta and an integer
parts-per-million rate (10% = 100000 ppm), never floating point. For payment revision `v`,
`N = max(0, mature_nonreversed_cash_G - allocated_station_cost_S - allocated_processor_fee_F)`;
one share atom is exactly one-millionth of an accounting quantum, so
`A_atoms = checked_multiply(N_quanta, rate_ppm)`. Thus 1000000 ppm is exactly `N`, not one
million times `N` in monetary value. The new ledger event is only the atom delta from prior
recognition for that settlement.

The compensation ledger also carries balanced per-currency journal postings under one global
sequence. Transactional control leaves have a closed schema and independently fold unique
source/application ranges, aggregate targets, active liability states, pending submitted
recourse, rail clearing, current/derecognized enforcement-disposition coverage, dust-cycle
generations, debt ranges, recovery, reopening, and writeoff. Each event signs a canonical
`ControlValueProjectionV1` over exact post-state member projections but never a value whose
preimage contains that event's own bytes, signature, ledger hash, or post-commit control
leaf. Post-commit leaves add the final ledger position/hash; a later signed head commits the
canonical per-currency leaf set. Exact JCS containers, closed member schemas, counts, stable
sort keys, and current-group stable IDs/group indices make the hash graph reproducible and
acyclic across implementations.
For each currency, `T_N` is the checked sum of immutable eligible candidates' current
nonnegative externally funded settlement `N` values in share atoms, `T_C` is the checked sum
of each such `N * grant_rate_ppm`, and `T_A` is the checked
sum of current entitlement aggregates. Every completed atomic event group must preserve
`T_A = T_C <= T_N` across all operators and balance its closed journal dispositions or no
positive money transition commits. A periodic full replay must reproduce the same canonical
`CompensationControlTotalSetV1` complete hash. This is a derived
ledger-wide invariant, not a second mutable budget or signature that can mint money. A late
reversal lowers the current totals and records any already-paid excess as exact operator debt;
it cannot pretend the historical rail transfer never happened.

No money-path collection is an unnamed set alias. The proposed contract registers typed,
owner-bound JCS sets for payment revisions, application descriptors and results, affected
state, journal postings, payout lot/range selection, Tower and lifecycle scope, funding
slices, settlement references, pending-success/failure resolution, enforcement, debt
writeoff, classification/hold scope, payout eligibility evidence, fee-finality incidents,
and dust lot references. Counts, totals, half-open range geometry, canonical sort keys,
empty forms, exact cross-set projections, and acyclic construction order are part of the wire
contract in the `features/tower/tamper/` matrices; a signature over an implementation-chosen array or hash
does not satisfy it.

Every public grant, compensated or not, first reserves exact `FundingSourceLotV1` ranges under
the current published `FundingAllocationPolicyV1`. Cash lots originate only in a purpose-signed
`ConsumerCashCreditAuthorityV1` bound to authenticated payment history; grant lots originate
only in a separate purpose-signed `PlatformGrantCreditV1`. A serializable reservation CASes
every source head before the grant. Settlement consumes the actual-cost prefix and releases
the remainder; a signed terminal attempt releases the whole reservation when no settlement
receipt exists. Grant credit is structurally excluded from compensation, and newly learned
compromise of an original cash/grant provenance signer blocks live use even after ordinary
key rotation.

The same leaf independently proves allocation conservation: current `T_A` must equal active
unpaid liability minus pending submitted recourse, plus current uncovered paid source ranges,
active unreversed debt-recovery applications, and current enforcement-disposition coverage. Range-set
exclusion constraints classify every surviving positive source atom exactly once; cancelled,
debt-covered paid, reversed recovery, and derecognized enforcement-coverage ranges remain
forensic but contribute zero to current entitlement. Fraud is a separate disposition rather
than a synthetic payment revision: each homogeneous `unpaid_forfeiture` or `paid_clawback`
decision is one-use and must map every signed range one-to-one to an exhaustive coverage set.
Its deterministic identity comes from an acyclic target-scope digest and binds the current
published `CompensationEnforcementPolicyV1`, a separate current final substantiated
`CompensationEnforcementFindingV1`, and exact historical accepted-terms facts proven through
each earning's immutable grant-snapshot lineage. Debt writeoff similarly requires the current
published `DebtWriteoffPolicyV1`, a distinct current one-use `DebtWriteoffApprovalV1`, and the
historical accepted-terms lineage copied through each originating payout instruction. Decision
commit and destructive consumption both CAS target ranges, nonoverlap, input heads, purpose-key
states, and an independently assigned decision-ledger tuple; opaque policy versions, evidence
hashes, or approval hashes have no authority by themselves. Paid clawback also maps one-to-one
to debt ranges. A finding spanning unpaid and paid ranges
therefore requires two independently revisioned decisions. Later real payment reductions
derecognize either coverage kind through its kind-specific balanced journal source while
leaving any already-created paid-clawback debt intact.

The initial ruling defines eligible program net as the sum of each settlement's nonnegative
`N`; an unrelated negative-margin settlement contributes zero but does not reduce another
operator's historical job share. Changing to a cohort-wide `max(0, sum(raw margin))` pool
would change operator economics and requires a separately founder-approved allocation policy.

For a signed currency/rail `PayoutPolicyV1` declaring positive integers
`Q = accounting_quanta_per_rail_minor` and `M = minimum_payout_minor`,
one rail minor unit is `K = checked_multiply(1000000, Q)` share atoms. From a canonical
selected available amount `B_atoms`, payout preparation computes
`gross_minor = floor(B_atoms / K)`, `reserved_atoms = gross_minor * K`, and
`remainder_atoms = B_atoms mod K`, with `0 <= remainder_atoms < K`. Only
`reserved_atoms` can enter preparation only when `gross_minor >= M`; otherwise no partition,
reservation, instruction, or rail call occurs. The remainder stays payable in the operator
ledger and combines with later same-operator, same-currency atoms.
Any boundary lot is atomically replaced by immutable conserving child lots before
reservation. Funding and fee allocations are deterministic, conserving, source-lot slices
so splitting or reordering events cannot increase the result. A zero, noninteger,
currency-mismatched, or overflowing conversion factor fails before reservation.
The policy also binds a finite dust-review interval. The first signed transition that leaves
positive mature liability below threshold anchors one immutable dust-cycle deadline. That
transition can be maturity, hold/release, a negative adjustment, scoped forfeiture, payout
failure, payout remainder, or application of a stricter policy. Later changes advance the same
generation rather than resetting its first anchor or deadline; after a terminal zero balance,
new dust starts the next monotonic generation and binds the prior terminal hash. The acyclic
`DustCycleValueProjectionV1` commits only stable lot IDs, immutable ranges, and atoms—never
lot projections/full hashes—while payout-lot projections point to the dust projection and the
control leaf commits the complete dust-cycle projection set. Crossing the deadline creates a
durable hold, incident, and operator notice. V1 never expires, rounds away, donates, escheats,
or writes off untainted positive dust; only the ordinary exact homogeneous fraud-forfeiture
authority may forfeit tainted dust. Closure remains pending while that exact liability exists,
and any other terminal legal disposition needs a separately approved state machine.

Fee uncertainty has a separate finite ceiling. A signed adapter/source-kind policy derives
`fee_finality_deadline` from the Core capture tuple. Equality is timed out. If a fee-pending
source is reached by at least one current compensation aggregate, one purpose-signed
`FeeFinalityIncidentV1` opens with the nonempty exact affected-aggregate set, every candidate
remains pending, and no estimate or zero fee is synthesized. A captured source not allocated
to any compensation aggregate has no compensation incident to encode. A later authenticated
monotonic provider revision closes the incident and recomputes one cumulative delta;
permanent lack of finality disables new compensated allocations through that adapter rather
than leaving an invisible pending record.

The v1 payout instruction requires `withholding_minor = 0` and therefore
`gross_minor = net_minor`; all reserved atoms are discharged only by the authenticated net
rail result. If the bound tax decision requires a positive or unknown withholding amount,
the existing `reserved_prepared` preparation signed-aborts before an instruction exists, or
signed-voids an already-created instruction before its send fence, and moves the exact lots to
`withheld` without a rail call.
RogerAI must not represent retained atoms as tax remittance without the future separately
signed liability, instruction, send-fence, reconciliation, and discharge contract.
The decision is not an ambient flag. A revisioned current `TaxProfileFactV1` first binds the
verified payout identity, jurisdiction, tax profile and ruleset under an independently
assigned tax-profile-ledger tuple and policy-bounded freshness. Purpose-signed
`TaxWithholdingDecisionV1` then binds its
monotonic series/revision and prior hash to the exact operator and `PayoutPreparationV1`
ID/complete hash, verified
identity, tax-evidence hash, jurisdiction, destination, currency/rail, conversion, amounts,
result shape, applicability cutoff, expiry, and independently assigned tax-decision-ledger
tuple. The payout send fence
CAS-checks that decision head. A correction applicable at or before an already committed
send fence appends purpose-signed `TaxDecisionCorrectionIncidentV1`, leaves the one rail
instruction immutable, and withholds all later operator payouts. That incident has its own
deterministic revision head and independently assigned tax-incident-ledger tuple; correction,
send-fence, rail state, payout hold, and signer currentness are transactionally rechecked. V1 never labels the gross
disbursement compliant or remitted.
V1 likewise never initiates an external debit to reverse a paid Tower payout. A negative
adjustment to paid compensation creates signed operator debt that can offset future
same-currency compensation; any later external recovery mechanism requires its own approved
instruction, consent, send-fence, and reconciliation contract.

Payout creation is deliberately acyclic: one SQL transaction reserves exact lots and appends
`prepare_payout` with tax decision, compensation head, instruction, and send fence absent;
the tax authority then decides the exact preparation; an independent
`CompensationLedgerHeadV1` attests the completed ledger prefix plus a closed authority-set leaf
binding that preparation, zero tax decision, current payout eligibility/policy, destination,
Tower scope, and bounded preparation-authorization deadline; only then may the payout signer
create `TowerPayoutInstructionV1`; finally one send-fence transaction attaches/verifies that
instruction, rechecks all current decision heads, changes lots to `reserved_submitted`, and
commits before the single rail call. A crash before an instruction uses `abort_preparation`;
one after instruction but before the fence uses `void_payout`.
One operator/currency payout may aggregate lots from several of that operator's Towers only
when preparation, head authority leaf, instruction, and every payout-family event bind the
same payout-owned `TowerIDScopeSetV1` complete hash.

RogerAI clients and Tower packages ship or explicitly pin a small public-network bootstrap
trust anchor. Publish a signed, cacheable trust document such as
`/.well-known/rogerai-trust.json` containing current and historical verification keys, key
IDs, validity intervals, purpose, rotations, revocations, a monotonic document version, and
expiry. Its `PurposeBoundKeyDirectorySetV1` and `KeyValidityRevocationSetV1` have identical
ordered key-ID membership and exact owner, member, ordering, count, interval, replacement,
and revocation contracts. Its `CompensationPolicyDirectorySetV1` binds the exact hashes,
series/revisions, scopes, effective/expiry tuples, and distinct purpose signers for
`TowerCompensationPolicyV1`, `FundingAllocationPolicyV1`, `PayoutPolicyV1`,
`FeeFinalityPolicyV1`, `MaturityPolicyV1`, `PayoutEligibilityPolicyV1`,
`CompensationEnforcementPolicyV1`, and `DebtWriteoffPolicyV1`. A policy signature alone has no authority: a separate
root-delegated publication signer appends exact
`RogerTrustPublicationV1` records with a serially assigned Core time/global sequence; a
signed document, its new keys, and its listed policies have no authority before that
independent record commits, and no policy effective tuple may predate its first accepted
containing publication.
Accepted publication history derives an immutable first-seen sequence/hash/time anchor for
every key: a new key cannot authorize a backdated object, every predecessor key is retained
with immutable identity and validity, and state moves only forward. After root rollover,
ancestor-root keys remain only as retired/revoked historical verifiers; fresh current-root
keys alone can authorize new objects. V1 rollover uses only the exact
old-and-new-quorum signed overlap contract; emergency recovery is disabled until a separate
authority/incident/delay/client-bootstrap specification is approved. Station-key resolution
is versioned and bound to receipt time and any recorded compromise-effective time.

An append-only SQL ledger with uniqueness constraints, transactional head comparison, audit
replication, and signed monotonic heads is sufficient. A fresh purpose-signed
`CompensationLedgerHeadV1` over the SQL compensation sequence/hash, journal-template version,
`CompensationControlTotalSetV1` and `CoveredPayoutAuthoritySetV1` complete hashes is mandatory
for every payout instruction. It carries an independently assigned head-ledger commit tuple,
chains by current-head CAS, and selects signer validity from that tuple rather than its issue
claim. `TowerPayoutInstructionV1` likewise carries an independently assigned
payout-authorization-ledger tuple committed before the send fence. All payouts stop if either
distinct signer is unavailable or no longer current. At
send, the bound unexpired head may be
an ancestor of the current SQL head so unrelated later events do not starve payout, but every
selected lot, preparation, decision, policy, destination, and instruction relationship is
freshly compare-and-swapped unchanged. The later
`RogerLedgerCheckpointV1` uses a separate public-transparency signer and Merkle root for
inclusion/consistency proofs; its absence never substitutes for or blocks the mandatory
compensation head. A blockchain would add
consensus, custody, governance, and operating cost without removing RogerAI's intentionally
central admission and settlement authority.

## Standalone/private mode

`roger-tower init --mode=standalone` creates a separate pinned offline local trust root,
network ID, one-time administrator-bootstrap verifier, and data directory. The first scoped
administrator credential is issued only when that bound invitation is consumed with key proof.
It has these hard invariants:

- no public enrollment client, public directory route, RogerAI settlement route, or public
  trust badge is enabled;
- no RogerAI URL, certificate, account token, credit, grant, or public network ID is accepted;
- public advertisement is structurally disabled, not a boolean that can be accidentally
  flipped;
- the default listener is loopback; LAN, cluster, and ingress binds require explicit config;
- exactly one first `local_operator` bootstraps through a root-genesis one-time invitation,
  pins the offline-root fingerprint and monotonic publication chain, then uses a scoped client
  credential and a distinct published online certificate issuer; later invitations require
  current administrator authorization; if the pristine invitation expires or locks before any
  client/job history, only a chained one-use offline-root recovery may reissue its next
  generation;
- a signed `LocalBootstrapVerifierHeadV1` separates monotonic head revision from HMAC-key
  generation and commits the complete outstanding-invitation set. Every create, failed attempt,
  consume, expiry, or lock advances the head without extending that generation; an active
  operator can rotate a lost/compromised verifier after any history, atomically invalidating all
  outstanding codes. Offline-root reissue remains pristine-only;
- `LocalOperatorAuthorityHeadSetV1` is empty only before first bootstrap and has exactly one
  member thereafter. V1 rejects a second operator and ordinary demotion/revocation of the sole
  operator; renewal/key rotation preserves the singleton, and offline-root recovery atomically
  fences and replaces that exact member;
- billing is disabled in v1; routing among attached local Stations is free/local accounting;
- default telemetry and update checks make no RogerAI connection;
- changing between `joined` and `standalone` requires a new data directory and enrollment.

The standalone mode may use its own PostgreSQL ledger and, for trusted local HA only, a
local Valkey bus. Those credentials never connect it to RogerAI. In-memory state is allowed
only for an explicitly labeled development/demo profile.

Standalone is a complete local broker contract, not merely a no-egress build flag. Its pinned
offline root anchors a monotonic `LocalTrustDocumentV1`/`LocalTrustPublicationV1` history with
an exact purpose-key directory, independent local trust-ledger tuples, rollback rejection, and
retained historical verification. A strict revisioned `LocalPolicyV1` closes moderation,
tool, network, route, bounds, and result-disposition choices and has its own policy-ledger
commit authority. Post-bootstrap administration uses scoped, one-use
`LocalAdminAuthorizationV1` objects rather than ambient administrator state.
If the current local policy expires or no usable `local_operator` credential remains,
ordinary serving and administration fail closed. Recovery is an explicit offline
pinned-root ceremony using one revisioned, one-use
`LocalBreakGlassRecoveryAuthorizationV1`: pristine bootstrap recovery may only advance the
terminal invitation/verifier generation, policy recovery may only extend the byte-identical
business projection while regenerating the revision-owned private-allowlist envelope and hash,
and sole-administrator recovery may only replace that stable client's key after proof.
It cannot change trust, routing, Stations, money, or public-network behavior; expired or
missed-renewal management keys must first be replaced through the exact pinned-root
`LocalPurposeKeyRecoverySetV1` root-recovery publication path.

Server-key recovery is a separate, explicitly requested, one-use contract. Strict
`LocalKeyEscrowExportAuthorizationV1` and `LocalKeyEscrowExportResultV1` objects bind the exact
trust/policy/verifier/operator heads, recovery public key, ordered server-key public manifest,
archive algorithms, no-overwrite destination, independent authorization/result tuples, and a
single terminal reservation. Each reservation generates a fresh X25519 sender-ephemeral key,
publishes its canonical public key in the signed reservation/header/result, and uses the one
canonical JCS member and binary ciphertext framing defined by the spec so the recovery holder
can derive and parse the archive. It is reachable only from an owner-local Unix socket plus
controlling TTY with OS-peer and fresh singleton-operator proof—never the remote administration
API. Client/operator/Station private keys and bootstrap HMAC secrets are excluded. The offline
root is excluded by default; including it requires a separate root-signed approval and direct
physical-media input to an isolated command, and root bytes never enter the serving daemon.

Local clients enter through strict one-use `LocalClientInvitationV1` and revisioned current
`LocalClientCredentialAuthorityV1` objects. Stations enter through strict one-use
`LocalStationAttachAuthorizationV1` and revisioned current `LocalStationOriginAuthorityV1`
objects that bind independent assertion and secure-session keys, certificate serials, origin
epoch, and an exact capability ceiling. Distinct local bridge-authority, bridge-certificate,
assertion, and secure-session keys keep transport admission from becoming Station authority.
Every grant transaction CASes current trust, policy, client, Station, and per-Station grant
sequence heads. Station assertion observation is separate from attempt acceptance: every strict
next signed assertion advances the observed chain, an eligible pre-cutoff assertion also becomes
evidence-complete, and valid late/cancelled evidence advances only that chain plus a rejection
audit so one late result cannot brick the Station. Provider assertions have only `complete` and
`provider_error` variants; Tower-local cancellation/deadline states derive from durable Tower
authority and produce no assertion-backed receipt. Client cancellation uses a strict signed
`LocalCancellationAuthorizationV1` bound to the exact current client credential, original
request, attempt, grant, nonce/idempotency key, and a Tower-assigned durable cutoff; a Station
status string or timestamp never supplies that authority.
Requests use signed `LocalRequestAuthorizationV1` plus durable idempotency. The data path uses
three further local-only, domain-separated objects:
`LocalExecutionGrantV1`, `LocalProviderAssertionV1`, and `LocalSettlementReceiptV1`.
They bind exact local jobs under the standalone network ID and trust root and cannot be
decoded as `ExecutionGrantV1`, `ProviderAssertionV2`, or `SettlementReceiptV2`. The local
receipt records free/private accounting with every public credit, hold, payout, and
compensation field structurally absent. Its commit binds a current local trust head that must
be the grant-bound head or a contiguous descendant, while historical grant authority remains
unchanged. Moderation/policy is an explicit signed local choice and never calls or claims
RogerAI policy. Their exhaustive field and mutation contracts are
in `standalone_jobs.feature`; the common attachment lifecycle is in
`station_attachment.feature`.

## Package and operator experience

### Commands

```text
roger-tower init --mode=joined|standalone
roger-tower enroll
roger-tower config validate
roger-tower config print --redact
roger-tower doctor
roger-tower serve
roger-tower status
roger-tower station invite
roger-tower station list
roger-tower station rotate
roger-tower station detach
roger-tower station rehome
roger-tower drain
roger-tower revoke
```

Use a versioned, strictly validated YAML configuration. Reject unknown fields and invalid
cross-mode combinations. Accept secrets through owner-only files or platform secret mounts,
not command-line values. Consume and delete one-time enrollment material.

Illustrative shape:

```yaml
apiVersion: tower.rogerai.fm/v1alpha1
kind: Tower
mode: joined

identity:
  dir: /var/lib/roger-tower/identity

joined:
  authority: https://broker.rogerai.fm
  enrollmentTokenFile: /run/secrets/enrollment-token

stationListener:
  address: 127.0.0.1:7070

adminListener:
  unixSocket: /run/roger-tower/admin.sock

limits:
  maxStations: 100
  maxInflight: 64
  maxAudioInflight: 8

observability:
  logFormat: json
  metricsAddress: 127.0.0.1:9090
```

The final public hostname and schema are compatibility contracts and require a separate
review; examples must not encode deployment-provider details.

### Release artifacts

- signed Linux `amd64` and `arm64` static archives;
- multi-architecture OCI images referenced by immutable digest;
- checksums, Sigstore/Cosign verification bundle, SBOM, vulnerability report, and build
  provenance for every artifact;
- threshold-signed TUF root/targets/snapshot/timestamp metadata with expiry, rollback/freeze
  protection, and an auditable transparency entry; split-view detection is claimed only when
  views are compared or witnessed;
- systemd unit and host installer;
- joined and standalone Docker Compose profiles;
- Helm chart with security context, Secret/ConfigMap, probes, identity volume, optional
  Station Service, NetworkPolicy, disruption budget, and migration Job;
- JSON Schema, annotated configs, compatibility matrix, backup/restore, revocation,
  incident, upgrade, and disaster-recovery guides.

The installer fails closed when verification metadata or a verification tool is unavailable.
The runtime is non-root, drops Linux capabilities, uses a read-only root filesystem, writes
only to declared volumes, redacts secrets, and never logs prompts, completions, credentials,
raw bridge tokens, or complete receipts by default.

### Initial resource envelope

These are conservative engineering estimates pending measurement:

| Mode | Initial pilot target |
|---|---|
| Joined | Linux amd64/arm64, 1 vCPU, 1 GiB RAM, about 1 GiB durable identity/certificate storage, correct clock, outbound DNS and TCP 443; no GPU |
| Standalone durable | 2 vCPU, 2 GiB RAM for Tower plus PostgreSQL 16, 5 GiB initial durable storage; no GPU in the Tower |
| Standalone HA | At least two Tower replicas behind a local balancer, PostgreSQL 16, Valkey 8, and separate durable backup storage |
| Development | In-memory single process, explicitly non-durable and never public |

Attached Stations have their own model, RAM/VRAM, accelerator, and disk requirements. The
published Tower minimum must be derived from real concurrency and maximum-body tests,
including the supported audio size; it must not be inferred from the current broker alone.

## Operations and abuse controls

- Central health includes authenticated liveness, randomized canaries, observed latency,
  mismatch/replay/fork counters, reconnects, and capacity saturation.
- New Towers start at low weight and concurrency. Promotion is evidence-based; suspension
  and draining are centrally enforced.
- Tower loss or parent disconnect stops new leases. Offline work cannot later settle.
- Backpressure is bounded by bytes and streams, not just request count. Slow readers,
  oversized frames, decompression expansion, and stream floods have explicit limits.
- Logs and metrics carry opaque IDs, not content or credentials.
- Anonymous reports may be accepted as tips, but automatic money or availability penalties
  require authenticated evidence bound to a Roger Core receipt.
- Software supports protocol versions N and N-1 during a documented upgrade window. An
  obsolete or vulnerable version is quarantined by a signed minimum-version policy.
- Upgrade drains stop new assignment centrally, allow bounded active leases to complete,
  rotate the process, and then re-admit it. Database changes use versioned, locked,
  expand/migrate/contract migrations and verified backups.

## Implementation sequence and approval gates

### Phase 0: repair the existing trust foundation

Before public Tower work:

1. Add regression specifications for exact receipt-to-job binding on chat, stream, and audio.
2. Make settlement address the authoritative held job, never an untrusted receipt ID.
3. Introduce immutable provider and settlement canonical forms with independent verification.
4. Stop rewriting Station-signed price fields.
5. Add a client-signed nonce/idempotency key consumed before holds, attempt-level replay
   protection, and persistent per-Station epoch/sequence/head audit checks.
6. Split the broker key by purpose and publish verification keys with key IDs.
7. Fail closed on required settlement/idempotency state errors.

Gate: every field mutation, mismatched context, duplicate, restart, and store-failure scenario
fails safely under real ledger dependencies.

### Phase 1: standalone/private MVP

1. Extract a small Tower core and build `cmd/roger-tower` with typed configuration and
   code-level mode allowlists.
2. Add pinned offline-root/bootstrap, monotonic local trust publication, purpose-separated
   online issuers, revisioned local policy/client/Station authorities, durable routing and
   migrations, break-glass recovery, and explicit free/local accounting.
3. Ship signed archive, OCI, Compose, and systemd artifacts plus `doctor` and no-egress tests.

Gate: a fresh user can initialize, serve, restart, back up, restore, and connect a client and
Station; packet capture proves no RogerAI egress; no public credential or route is accepted.

### Phase 2: joined protocol on RogerAI-controlled test Towers

1. Add admission registry, certificate issuance/rotation/revocation, and lifecycle states.
2. Add outbound multiplexed link, inner Station sessions, inventory revisions, signed grants,
   exact result binding, and receipt v2.
3. Add origin-aware routing/dispatch interfaces without sharing the trusted replica bus.
4. Run failure injection, replay/tamper, resource, reconnect, rolling-upgrade, and latency
   benchmarks.

Gate: all correctness, privacy, performance, fit, reproducibility, and revocation thresholds
in the research contract pass with preserved raw results.

### Phase 3: limited public beta

1. Account-bound manual admission, quarantine, low weights, explicit limits, support and
   incident runbooks.
2. Signed public release artifacts, Tower download page, status UI, operator documentation,
   and version-floor enforcement.
3. Canary free or tightly bounded traffic before ordinary paid workloads.
4. Enable the compensated tier only after real-fund allocation, reversal, payout
   idempotency, ledger-wide cap/control-total replay, fee-finality ceiling/recovery,
   threshold/dust retention, current payout-eligibility races, zero-withholding eligibility/
   hold, and clawback scenarios pass against real payment and ledger state. Nonzero
   withholding remittance remains disabled until its own approved state machine and
   authoritative integration exist.
5. Require `CompensationLedgerHeadV1` in every payout instruction and prove that its signer
   outage stops all payout sends, its `CompensationControlTotalSetV1` complete hash replays exactly, and an unexpired
   ancestor head plus selected-state CAS permits unrelated ledger progress without stale send.
6. Crash-inject every preparation -> tax decision -> signed head -> instruction -> send-fence
   boundary and prove abort-versus-void behavior with one external rail call at most.

Gate: no unresolved P0/P1 finding, reproducible releases, successful rollback/drain drills,
and founder approval to admit external operators.

### Phase 4: scale and transparency

Enable policy-driven self-service enrollment into quarantine only after a consecutive
30-day external pilot includes at least 25 independent verified operator accounts and
100,000 completed joined attempts, every tested revocation meets the 60-second gate, there
is no unresolved P0/P1 incident or unauthorized debit/credit/payout, and the signed pilot
policy's overload and fraud-loss budgets pass. Automated issuance still enforces account,
key, region, version, quota, payout-risk, and abuse-history checks; activation remains a
separate Core decision, and one signed policy switch can roll issuance back to manual mode.

Then add measured regional routing and `RogerLedgerCheckpointV1` public Merkle
inclusion/consistency proofs over the authoritative SQL ledger, reputation based
on centrally observed outcomes, and optional hardware-attested tiers. Consider regional
Core ingress. Consider direct client ingress only after it can preserve privacy, moderation,
authoritative holds/metering, failover, and settlement under hostile-Tower assumptions.

## Proposed code seams after approval

- Define a versioned, importable public wire package rather than expanding private structs in
  `internal/protocol` indefinitely.
- Add `Origin`/`Dispatcher` abstractions so the existing router can select a direct Station or
  a Station behind a Tower without learning transport details.
- Keep the trusted cross-instance Valkey store private to Roger Core. Add a separate scoped
  joined-Tower link and lease store.
- Put money transitions behind one exact-context settlement interface and transaction.
- Build `cmd/roger-tower` from the minimum registry/relay primitives, not from the current
  full broker route table.
- Create versioned database migrations before Tower tables are deployed.

Interfaces and filenames are deliberately not frozen until the behavioral specs are
approved and the red-test design identifies the smallest reusable boundary.

## Founder decisions requested

Recommended defaults are shown in bold:

1. Joined Tower compensation: **RULED 2026-08-01 - yes, via the opt-in compensated tier at
   10% of net platform revenue**, gated on Core-observed authenticated-session attribution
   corroborated by a consistent Tower statement, matured real-fund capture, and verified
   payout identity (`operator_revenue_share.feature`); admission alone still earns nothing,
   and the rate stays central signed policy so it can change without a protocol change.
   **Recommended cap interpretation for this approval:** program net is the independently
   source-derived sum of each settlement's nonnegative `max(0, G-S-F)` in a currency; the
   rate-weighted entitlement sum across all operators must equal its policy ceiling and never
   exceed that net. Negative-margin jobs contribute zero rather than clawing unrelated jobs.
2. Enrollment: **verified RogerAI account plus manual beta approval**, moving to automated
   admission only after abuse data exists.
3. Standalone money: **free/local routing only in v1**, with no RogerAI credits or payouts.
4. Public path: **client -> Roger Core -> joined Tower -> Station**; no direct client ingress.
5. Privacy: **inner Core-to-Station secure session**, while Roger Core retains current content
   visibility for policy and recount.
6. Ledger: **central append-only SQL first, signed Merkle checkpoints later**, no blockchain.
7. Release order: **trust repairs, standalone MVP, controlled joined pilot, public beta**.
8. Naming: public copy says **Tower (the broker-like service)**; configuration uses `joined`
   and `standalone`, avoiding collision with today's private bands.
9. Refund loss position: **the platform absorbs the full loss on a refunded or charged-back
   job in v1.** The Station earning is already paid and is not reversed, so a refunded job
   leaves platform revenue negative while the operator's 10% is only clawed back to zero.
   The alternative - aggregate-loss sharing, where operators carry a share of the downside
   they helped originate - needs a new effective-dated policy and allocation state machine.
   This was previously implicit in a single scenario; it is surfaced here because it decides
   how much refund fraud the platform eats. Recommended default: **absorb in v1**, revisit
   once real chargeback rates exist.
10. Operator debt collectability: **v1 never initiates an external bank debit**; recourse is
   a signed same-currency offset against future compensation, now backed by three new
   controls - debt bars the compensated capability across every Tower the operator owns, a
   rolling reserve holds back a policy share of each accrual until maturity closes, and a
   per-operator unmatured exposure cap bounds the maximum loss per operator. Recommended
   default: **keep offset-only recourse**, since the reserve and cap bound exposure without
   the cost and risk of debiting operator bank accounts.

Approval applies to the seventeen Tower feature files named in the research log below, not to an
implementation diff. After approval, the next step is red tests and only then production code.

## Research log

This log is append-only. Evidence was collected from repository revision
`794f9e8f9d0a4ceb904711f559fcdd4a949c6dc2`; unrelated working-tree changes were not used or
modified.

### 2026-08-01 — current-state audit

- Read the broker route table, registration/tunnel paths, trusted shared-store path,
  settlement stores, receipt canonicalization, agent chain state, client broker selection,
  release configuration, installer, container, licensing, Tower page, and existing BDD specs.
- Ran the focused baseline command recorded in E12. Raw result:

  ```text
  ok  rogerai.fm/roger/v6/internal/protocol       0.005s
  ok  rogerai.fm/roger/v6/cmd/rogerai-broker     0.009s
  ```

- Classified the existing trusted-replica path as unsuitable for independent operators and
  the receipt/job, countersignature, key-separation, and chain-head findings as Phase-0 gates.

### 2026-08-01 — proposed design and specification

- Compared the design needs with TLS 1.3, SPIFFE X.509-SVID, Sigstore/Cosign, TUF, and RFC
  9162 transparency-tree mechanisms.
- Wrote this architecture and the nine proposed feature files under `features/tower/`.
- Parsed all nine files with the repository-pinned Cucumber Gherkin v26 parser. Raw result:

  ```text
  PASS features/tower/inventory_and_routing.feature
  PASS features/tower/job_and_settlement.feature
  PASS features/tower/key_separation.feature
  PASS features/tower/modes.feature
  PASS features/tower/operations.feature
  PASS features/tower/packaging.feature
  PASS features/tower/public_enrollment.feature
  PASS features/tower/receipt_v2.feature
  PASS features/tower/trust_tiers.feature
  ```

### 2026-08-01 — founder ruling: operator revenue share

- Founder directed that Tower operators be incentivized with a 10% revenue share on token
  sales through their Tower, conditional on verifying funds receipt, transit attribution,
  and operator trust (register/promote/authorize/ban).
- Wrote `features/tower/operator_revenue_share.feature` (compensated tier: eligibility,
  attribution, funds verification, net-revenue base, idempotent append-only accrual,
  self-dealing/wash-traffic holds, lifecycle-gated accrual, forfeiture and clawback,
  structural absence in standalone mode).
- Revised decision summary item 9 and founder decision 1 accordingly; lifecycle, ban, and
  trust controls already covered by `public_enrollment.feature` and `trust_tiers.feature`
  carry the register/authorize/ban requirement.
- Re-parsed all ten feature files with the repository-pinned Gherkin v26 parser. Raw
  result: all ten PASS, including `operator_revenue_share.feature`.

### 2026-08-01 — adversarial review hardening and complete parse

- Added the exact control-plane mutation matrix, attempt and compensation state machines,
  provider/rail payment authority, Station attachment lifecycle, and standalone job
  contract. Tightened client replay authorization, TLS exporter binding, settlement
  arithmetic, funding/reversal allocation, lifecycle cutoffs, key roles, backup/admin
  security, package-update metadata, and the self-service admission gate.
- Re-parsed the complete proposed contract with the repository-pinned Cucumber Gherkin v26
  parser. Raw result:

  ```text
  PASS features/tower/attempt_lifecycle.feature
  PASS features/tower/compensation_state_machines.feature
  PASS features/tower/control_plane_tamper_matrix.feature
  PASS features/tower/inventory_and_routing.feature
  PASS features/tower/job_and_settlement.feature
  PASS features/tower/key_separation.feature
  PASS features/tower/modes.feature
  PASS features/tower/operations.feature
  PASS features/tower/operator_revenue_share.feature
  PASS features/tower/packaging.feature
  PASS features/tower/payment_authority.feature
  PASS features/tower/public_enrollment.feature
  PASS features/tower/receipt_v2.feature
  PASS features/tower/standalone_jobs.feature
  PASS features/tower/station_attachment.feature
  PASS features/tower/tamper_matrix.feature
  PASS features/tower/trust_tiers.feature
  ```

### 2026-08-01 — final money-authority and concurrency closure

- Closed the final adversarial-review findings with exact payout-lot partitioning,
  zero-withholding-only v1 payouts, purpose-signed tax decisions and correction incidents,
  internal debt instead of unspecified external reversal, mandatory compensation-ledger
  heads, deterministic concurrent-session fencing, a bounded settlement-finalization
  ceiling, and exhaustive field/mutation contracts for every compensation authority.
- Re-parsed all seventeen files after those changes with the same repository-pinned Gherkin
  v26 parser: all seventeen PASS. Independent architecture, security, and holistic spec
  rechecks reported approval-ready with no remaining blocker.

### 2026-08-02 — follow-up feedback reconciliation and exact-schema closure

- This entry supersedes the prior entry's then-current "no remaining blocker" conclusion.
  The founder's pre-approval audit found seven additional money-contract issues: no
  ledger-wide compensation cap; nonterminal rounding dust; unbounded fee finality;
  sanctions/account-deletion payout races; a non-value rate example; contradictory treatment
  of missing Tower evidence; and inconsistent grant-, settlement-, and payout-time authority.
- Revised the proposed contract so `T_N`, `T_C`, and `T_A` provide an independent
  per-currency program backstop; dust has an explicit generation/deadline/hold lifecycle; fee
  uncertainty opens one bounded durable incident; restrictions are serialized at preparation,
  instruction, send fence, and final rail result; rate examples are typed explicit JSON
  values; missing Tower corroboration makes compensation ineligible without blocking an
  otherwise valid settlement; and each authority is read at exactly its named stage.
- Follow-up adversarial passes also replaced implementation-defined collection hashes with
  owner-bound typed JCS sets, exact half-open range/result projections, deterministic partial
  selection and child geometry, provider-neutral payment revisions, event-local affected-state
  commitments, closed balanced journal templates, explicit success/failure child-event DAGs,
  and exact classification/hold, payout, enforcement, debt, eligibility-evidence, capability,
  inventory, modality, protocol, and directory set schemas. Cross-event rail-success journal
  keys are now explicitly relationship-checked to their prescribed later child result rather
  than falsely attributed to the parent event's local state.
- Root trust now has one acyclic overlap-only `RootDelegationV1`, an externally delegated
  trust-document signer, authority-bounded key lifetimes, and exact first-link absence rules.
  Both central ledgers begin from purpose-separated signed `RogerLedgerGenesisV1` objects.
  A currency's first exact compensation event atomically materializes its deterministic zero
  control leaf and binds that hash as its real prior authority, eliminating both an implicit
  implementation default and a separate undefined currency-admission transaction.
- Re-parsed the current seventeen-file proposal with the repository-pinned Cucumber Gherkin
  v26 parser. Raw result:

  ```text
  PASS: parsed and compiled 17 Tower feature files with Gherkin v26
  ```

- No step definition, migration, daemon, package, payout integration, or Tower-page download
  was implemented. These remain proposed invariants pending the founder approval gate and
  executable red tests against real PostgreSQL and rail/provider boundaries.

### 2026-08-02 — non-signer authority and standalone closure

- Replaced signer-controlled or opaque authority references with independently assigned,
  signed ledger anchors for attempts, lifecycle/origin changes, eligibility and tax decisions
  and incidents, destructive compensation decisions, compensation heads, and payout
  instructions. Each positive/destructive use now names exact current-head, purpose-key,
  expiry, and transactional CAS rules rather than relying on an issue-time claim.
- Added universal public-grant funding reservations with typed cash/grant provenance,
  deterministic allocation, exact settle/release transitions, and original-commit compromise
  handling. Compensated capability also proves byte-identical payout-identity and tax/
  jurisdiction relationships at capability and grant issue.
- Replaced opaque forfeiture/writeoff policy and approval labels with published
  `CompensationEnforcementPolicyV1`/`DebtWriteoffPolicyV1`, independently signed current
  finding/approval authorities, deterministic acyclic target scope, historical accepted-terms
  lineage, nonoverlap, and issue/consume fences.
- Expanded standalone from three data-path objects into a complete pinned-root local authority
  system: monotonic trust publications, exact purpose keys, revisioned local policy, one-use
  client and Station admission, current credential/origin heads, scoped administration,
  offline break-glass recovery, strict private-dependency allowlists, and exact local grant,
  assertion, and receipt contracts. Public discovery and money remain structurally absent.
- The current final-candidate proposal parses and compiles all seventeen Gherkin files with
  repository-pinned v26, passes the focused existing protocol/broker baseline in E12, and has
  clean whitespace/diff checks. Independent reviewers must still approve one exact combined
  hash; any later byte change invalidates that approval. This entry does not claim
  implementation completion.

### 2026-08-02 — independent validation pass: money-loss closure and implementability

- An independent review re-verified the prior summary's claims against the tree. Mechanical
  claims held: all feature files parse under the repository-pinned Gherkin v26 parser, the Go
  tree was untouched, `go build ./...` was clean, and the named protocol/broker tests passed.
  Two claims were overstated and are corrected here: "independent reviews report
  approval-ready" coexisted with three `fail` and twenty-four `unresolved` evidence rows and
  should be read as spec-completeness only, not design validation; and "existing focused
  protocol and broker tests pass" covered seven named test functions in two packages, not the
  suite and not `make cover-gate`.
- The review found that the prior pass had closed the cheap findings while leaving the
  money-loss cluster open. Verified status before this entry: debt did not bar the compensated
  capability, there was no rolling reserve or exposure cap, the authenticated final rail result
  was a closed two-value set with no return transition, the payout instruction arithmetically
  forbade any rail charging a fee, flat dispute fees were allocated ad valorem, and self-dealing
  caught only literally-identical verified parties.
- Closed in this pass, all re-parsed clean:
  - Debt now bars the compensated capability across every Tower an operator owns, is an
    operator-scoped fact-head input rather than a per-Tower condition, and is cleared only by
    recovery, offset, or purpose-signed writeoff. Fresh keys and re-enrollment do not orphan it.
  - A signed `reserve_ppm` rolling reserve withholds a policy share of every accrual until the
    reversal-maturity window closes, with idempotent release the operator cannot accelerate, and
    a per-operator unmatured exposure cap whose over-cap amounts defer rather than forfeit.
  - An authenticated post-success rail return is now a distinct transition that restores
    entitlement to `mature_payable`, reduces cumulative disbursement, and creates no debt -
    separated by an explicit classification outline from confirmed failure, success, negative
    entitlement adjustment, and unauthenticated advice. Partial returns, replayed returns, and
    returns over debt-covered ranges each have exact scenarios.
  - The payout instruction binds a signed `rail_fee_minor` and a closed platform-or-operator
    fee bearer, so gross equals net plus operator-borne fee rather than requiring equality.
  - Processor fees are allocated by a declared closed fee kind: ad valorem prorates, flat is
    assigned whole to its causing source interval. A flat fee exceeding its own settlement's
    net revenue becomes platform expense rather than spreading to unrelated settlements, and a
    fee with a missing or out-of-set kind fails closed into a bounded incident.
  - Self-dealing now evaluates linkage on evidence - shared payout destination, funding
    instrument, beneficial owner, business/tax identity, postal identity, device credential, or
    declared affiliation - with one shared anomaly budget across linked accounts, a bounded
    review deadline with a terminal disposition, and concurrent disjoint-origin sessions
    treated as compensation-relevant rather than routing-only evidence.
- Implementability was audited separately and is the gating risk for execution, not
  correctness. Measured: 3,963 steps of which 3,872 were distinct phrasings (97.7%), meaning
  effectively every step needs its own definition; roughly 290 steps packed seven or more
  independent assertions into one line, making failures undiagnosable; and no glossary existed
  for terms used hundreds of times.
  - Added `features/tower/glossary.feature` as the normative vocabulary source of truth,
    pinning `complete hash` (preimage only, signature excluded, schema domain-separated),
    canonical absence as an explicit sentinel rather than an omitted key, strict decoding as a
    closed rejection table, `atom` versus rail minor unit, Core tuple, freshness, fail-closed,
    closed sets, and typed sets. It also fixes the fact-head enumeration at exactly six and
    declares any smaller enumeration elsewhere a defect in that file.
  - Corrected the one live instance: the grant-time snapshot step named five heads and omitted
    the tax-profile head; it now references the glossary's six.
  - Converted the worst field-enumeration mega-steps to `| field |` tables, including the
    1,510-character key-role inventory and the standalone administration authorization, and
    replaced the duplicated ExecutionGrantV1 field prose with a reference to the tamper matrix
    that is already declared the source of truth - removing the drift the earlier review found.
- Surfaced two implicit money rulings as explicit founder decisions 9 and 10: the platform
  absorbs the full loss on refunded jobs in v1, and operator debt recourse remains offset-only
  now that the reserve and exposure cap bound the exposure.
- Not done, and deliberately left as follow-up: roughly 67 field-enumeration steps still need
  the same mechanical table treatment; the step-vocabulary unification pass that would collapse
  about a thousand near-duplicate steps into roughly twenty parameterized definitions has not
  been attempted; and `tamper_matrix.feature` remains a single 2,395-line file that should be
  split five ways along its existing seams, replicating its source-of-truth header into each.
- No step definition, migration, daemon, package, payout integration, or Tower-page download
  was implemented. No production code was changed.

### 2026-08-02 — implementability restructuring

- Measured the current tree: 4,140 steps of which 4,030 are distinct after normalizing quoted
  strings, placeholders, and integers (97.3%); 498 steps over 250 characters carrying 36% of
  all step text; 203 distinct signed object names.
- Corrected an earlier misreading recorded here: the 203 names are not 203 schemas. Seventy of
  them are `CanonicalTypedSetV1` registry rows, which is the correct generic-plus-registry
  design and must not be "simplified". The real schema count is 133.
- Added `docs/tower-spec-implementation-guide.md`: the canonical step vocabulary that step
  definitions will implement, the rule that a field list appears in exactly one place, a
  phase-tiered object surface (Tier 0 production repair through Tier 4 deferred), and the
  order of work. It changes no asserted behavior.
- Split the 2,395-line `tamper_matrix.feature` into five sub-domain files under
  `features/tower/tamper/`: job authority, transit and receipt, typed sets, compensation
  variants, and policy and ledger. Verified content-identical at 2,359 non-blank body lines
  before and after with all 128 scenarios conserved, the source-of-truth header and universal
  mutation note replicated into all five, and every cross-reference in the set repointed from
  the deleted filename to the specific matrix that now owns the contract.
- The specification set is now 22 files, ~10,350 lines, 810 scenarios, all parsing under the
  repository-pinned Gherkin v26 parser.
- Tier 4 needs a founder decision: roughly twenty objects (all value projections, control
  totals, dust, escrow, writeoff decision, and tax correction incident families) add
  auditability rather than safety, and each costs a schema, a tamper matrix, step definitions,
  and a migration. Deferring them weakens no money invariant that Tiers 0 through 3 assert.
- Still outstanding and gating step definitions: the vocabulary unification pass targeting
  under 400 distinct step phrasings, and roughly 67 remaining field-enumeration steps.
- No production code was changed.

### 2026-08-02 — Phase-0 production repairs shipped

The three `fail` rows in this ledger were defects in the EXISTING production money path,
not in the Tower design. All three were re-verified against the code before any change.

- **E4 (fixed).** `UsageReceipt.signingBytes()` zeroed `GrantID`, `BrokerPromptTokens`, and
  `BrokerCompletionTokens` for BOTH signatures, so the broker's own re-counts - the numbers
  `billedTokens()` uses to charge the consumer and credit the provider - were covered by no
  signature at all, and no `VerifyBroker` existed anywhere in production. Three code comments
  asserted the opposite. Repair: split into `nodeSigningBytes` (unchanged, so node signatures
  and the wire format stay compatible) and `brokerSigningBytes` (a superset excluding only the
  two signature fields), added `VerifyBroker`, and kept `Hash()` on the node form so the
  per-node chain is unaffected.
- **E3 (fixed).** The broker verified only the node signature on a returned receipt and never
  checked that the receipt described the job it dispatched. Settlement claims the hold keyed on
  `rec.RequestID`, so a foreign, empty, or replayed id cleared the wrong row while the real hold
  was later swept back to the payer - served inference that was never billed. Repair: added
  `UsageReceipt.BindsTo(requestID, nodeID)` and gated all three relay paths (chat, streaming,
  audio) on it, treating a non-binding receipt exactly like a forged one. One guard closes the
  mismatch, empty-id, and replay cases together, since a replayed receipt necessarily names an
  earlier request.
- **E5 (partially fixed).** The per-node hash chain used a single process-global `lastHash`, so
  one agent process serving several nodes interleaved them into one chain and every link pointed
  at another node's receipt. Repair: `chainSign(nodeID, …)` keys the chain by node and is now the
  single place it is read and advanced. **Deferred:** the broker still stores no per-node chain
  head, so omission, reorder, and restart-reset remain undetectable. Closing that requires a
  `prev_hash` column plus a head table and a migration against the live database, and enforcing
  it before nodes are upgraded would reject every restarted node. It should ship as detect-and-
  record first, enforce second, and needs a founder decision on the migration window.

Evidence: spec scenarios added to `features/trust/lineage_receipts.feature` (53 scenarios,
304 steps, all passing); red tests written and shown failing for the right reason before each
repair; full suite green at 24 packages; `make cover-gate` PASS at 92.0% with every package at
or above 90%.

A fresh-context review of this diff caught a real defect in the FIRST cut of these tests: the
binding scenarios asserted the money outcome ("no earning is minted", "the hold is refunded")
from a Given that never ran a relay, so the fields read were zero-valued and the assertions
passed by coincidence. A mutation check confirmed the suite stayed green with both guards
deleted. The scenarios now drive the real relay against the real ledger, the tautological
per-path outline was removed, and dedicated end-to-end tests were added for the streaming and
audio paths. Every guard is now mutation-verified: deleting any one of the three fails the
suite. The review also flagged the duplicate per-request `VerifyNode` call (now a single
`recOK`) and the stale GROUND TRUTH header in the feature (corrected).

Open items from that review, deliberately not actioned here: an unbound receipt is refused
silently (no trust strike or counter, so a buggy node could serve for free indefinitely with
only a log line), and `SignBroker`'s new covered bytes mean every already-persisted co-signed
receipt would fail a future `VerifyBroker` - the receipt carries no version tag, so a format
field or a documented cutover is needed before any verifier ships.

### 2026-08-02 — detect-and-record, evidence, and the operator's station view

Founder direction: take the detect-and-record migration first and work toward enforcement
after; strike a refused receipt; give operators an account-bound view of what they run; fix
verification and add a version tag if one is needed. All four shipped.

- **Receipt signature versioning.** `SigVersion` records WHICH canonical form `BrokerSig`
  covers: absent/0 = legacy (the node form, which did not cover the billed counts), 1 = the
  broker superset form. Without it every already-persisted co-signed receipt would read as
  forged the moment a verifier shipped. `VerifyBrokerCoverage` returns `(ok, covers)` so a
  caller learns that a legacy signature is genuine but is NOT evidence of the amount billed.
  The tag lives inside the broker-signed bytes, so a v1 receipt cannot be downgraded to the
  legacy rule and then edited. Spec: `features/trust/receipt_signature_versions.feature`,
  executable from `internal/protocol` because a legacy fixture needs the unexported node form.
- **A refused receipt now strikes.** An unbound receipt means the work was served and cannot
  be billed; refusing it silently let a broken or hostile node do that indefinitely. It now
  records a `receipt-unbound` strike naming the dispatched and returned request ids,
  idempotent per request so a retry cannot stack strikes. Not zero-doubt: a broken node is
  likelier than a hostile one, so it escalates through the normal thresholds.
- **Chain continuity: detect and record.** The broker now persists a per-node chain head
  (`rogerai.node_chain`, purely additive DDL alongside the existing idempotent migrations),
  compares each accepted receipt, counts breaks, and advances regardless so one break is not
  reported forever. Re-applying a receipt is idempotent. A store failure records nothing
  rather than a false break - the money path never depends on chain bookkeeping.
  - A first cut struck on every break and immediately failed an existing test: an honest
    reasoning node accrued five strikes. That was the right failure. A strike freezes the
    owner's earning lots and escalates toward a ban, which is ENFORCEMENT, and enforcement is
    wrong today for two reasons - the node-side chain does not survive a restart, so honest
    restarted nodes would be punished, and the broker has only just begun recording heads, so
    every existing node's first receipt legitimately fails to continue a chain nobody tracked.
    Breaks are now durable evidence plus a log, surfaced to the owner. The unused
    `chain-break` strike kind was removed rather than left dead.
  - Enforcement remains a later stage needing its own approved spec, a durable node-side
    chain, and a measured baseline of real break rates.
- **The operator's station view.** `GET /stations` returns the authenticated owner's own
  stations - registration, on-air state, offers, earnings, served volume, chain status and
  account strike evidence - assembled ONLY from that owner's account bindings, never from a
  request-supplied node id. It deliberately carries no consumer identity, prompt or completion
  text, bridge token, or private band code, and each of those exclusions is an asserted
  scenario. Page at `web/src/stations.html`, linked from the account hub. The chain state is
  presented as an audit signal, with copy saying plainly that a restart reads as a break and
  does not affect earnings or standing.

Validation: `features/trust/lineage_receipts.feature` 54 scenarios / 310 steps;
`features/trust/receipt_signature_versions.feature` 7 / 24;
`features/stations/stations_dashboard.feature` 11 / 56; all passing. Full suite green at 24
packages, `go vet` and `gofmt` clean, `make cover-gate` PASS at 91.8%. Web build green with
328 tests passing (the two failures in the tree are `company.html` copy from another session,
confirmed unrelated by stashing them).

Two guardrails caught real mistakes in this pass and are worth recording: the dist link
crawler rejected `/account` and `/payouts` because this host serves no clean URLs, and the
TUI guest-operator suite globs the whole `features/operator` directory, which swept up the new
station spec until it was moved to `features/stations/`.

### 2026-08-02 — the Tower operator dashboard is specified, not built

A founder correction worth recording: the dashboard shipped earlier in the day is the
STATION view (`features/stations/stations_dashboard.feature`, `GET /stations`) — a machine
serving inference under `roger share`, earning the provider share. That is a different
object from a TOWER, which is a broker-like relay earning a share of net platform revenue
on traffic settled through it. Both are wanted; only the Station one was built.

The Tower operator view cannot be built yet, and this is the reason to state plainly: there
is **no Tower implementation in the codebase at all**. No `roger-tower` binary, no Tower
identity, enrollment, admission lease, inventory, transit observation, or compensation
ledger exists in Go — the entire program is proposed spec awaiting approval. A Tower
dashboard today would have nothing real to read, and stubbing it with placeholder numbers
would be worse than shipping nothing.

So the deliverable is the spec: `features/tower/operator_dashboard.feature`. It covers
identity and admission state with a per-state explanation in the operator's terms;
certificate and lease validity, expiry warnings, and revocation with its appeal route;
attached Stations with DECLARED and OBSERVED values kept separate; traffic reported from
Roger Core's own observations rather than the Tower's self-report, with divergence shown as
divergence; and monetization broken out by lifecycle stage — immature accrual, reserve
held, mature payable, reserved in flight, paid, and outstanding debt — with each figure
naming the policy version that produced it and each ineligible settlement explaining its
deterministic reason. It also pins the honest labels: attribution is identity, not physical
proof; RogerAI is the authority, not a peer; and the two dashboards may never present one
kind of object as the other.

It ships with Phase 2 (joined protocol) for the identity, lifecycle, inventory and traffic
panels, and Phase 3 (compensation) for the money panel. Not before.

### 2026-08-02 — founder approval; Phase 1 begins

The founder approved the Tower spec set. Per the workflow the next step is red tests and
then the smallest implementation, so Phase 1 (the standalone MVP) has started.

Shipped in this pass, spec-first with red evidence before each step:

- **`internal/tower` config core.** `Mode` admits exactly two values, spelled exactly - a
  Tower that guesses what "Joined " meant could guess wrong about which network it belongs
  to. Configuration decodes with `KnownFields(true)`, so an unknown field is an error rather
  than a silently ignored control, and `apiVersion`/`kind` must both be named.
  - The load-bearing property is that standalone isolation is STRUCTURAL. A standalone
    config cannot express a public authority, enrollment token, joined certificate, payout
    setting, or public advertisement - those are rejected outright, not defaulted off where
    a later edit could flip them. Symmetrically, a joined config cannot express the local
    offline root, trust publication, or settlement signer that only a standalone network has.
  - Secrets are supplied by owner-only file, never as a scalar. Inline `identity.key`,
    `joined.enrollmentToken`, and a `storage.url` carrying a password are rejected, and the
    rejection names the field without echoing the value - a rejection must not itself leak
    the secret into a log or scrollback.
  - Defaults are loopback for every listener: nothing becomes reachable by omission.
- **`Init`/`Open`/`RequireMode`/`Lock`.** A data directory is initialized as one mode for
  life; `RequireMode` refuses the other and tells the operator to make a new directory,
  because switching in place would carry an identity, trust root, or Station registry across
  the boundary the modes exist to separate. Init is all-or-nothing - an invalid mode or a
  non-empty directory fails before anything is written, and a mid-way failure removes the
  directory so no key material survives for a later run to adopt. A standalone init mints a
  unique local network ID that can never equal the public one, plus a pinned offline root; a
  joined init mints neither, because Roger Core is the authority. Private material is 0600,
  the directory 0700, and an advisory flock gives one process ownership while releasing on
  crash.
- **`Doctor`.** Reports mode, whether the Tower can reach the public network at all, and
  whether every listener is loopback - so "standalone makes no RogerAI connection" is a claim
  an operator can check before the Phase 1 packet-capture gate proves it again. A
  non-loopback bind is supported but always called out.
- **`cmd/roger-tower`.** `init`, `config validate`, `config print`, `doctor`, `status`,
  `version`. `serve`, `enroll`, `drain` and `revoke` exist only to say plainly that they need
  the joined protocol from Phase 2, rather than failing obscurely or pretending to work.
  `config print` is always redacted; `--redact=false` is deliberately unsupported, because a
  flag that can dump key material is one someone eventually runs in a shared terminal.

Two pieces of code were REMOVED during the pass rather than tested: an unused `State.Dir()`
accessor, and a `Doctor` branch flagging standalone public reachability that could never fire
because both accessors are already gated on mode. A branch that cannot execute implies a
check that is not real; the test now asserts the actual guarantee instead.

Validation: full suite green at 26 packages, `go vet` and `gofmt` clean, `make cover-gate`
PASS at 91.8% total with `cmd/roger-tower` at 90.1% and `internal/tower` at 90.3%. The gate
initially FAILED this work at 0% and 80.3%, which is what forced `run` to be refactored to an
`io.Writer` seam and the IO-failure paths to be covered.

Next in Phase 1: the local CA and bootstrap flow, the local Station registry and routing,
durable migrations, and the no-egress test that closes the Phase 1 gate.

### 2026-08-02 — Phase 1.3: the standalone bootstrap flow

The moment a standalone network hands out authority, so the increment is deliberately
unforgiving. `internal/tower` now mints, verifies and consumes local bootstrap
invitations, and `roger-tower invite` / `admit` expose it.

What the implementation guarantees, each pinned by a test:

- **The code is high-entropy and shown once.** 128 bits from the OS CSPRNG, base32, and
  the plaintext is returned exactly once by `invite`. It is never written anywhere - a
  test walks the whole data directory asserting the code appears in no file, and a live
  run confirms it. Only an HMAC-SHA-256 verifier is stored, so reading the data directory
  is not equivalent to holding the invitation.
- **Every rejection is identical.** One error value for a wrong id, a wrong code, a wrong
  client, an empty code, and a 5,000-byte code. A distinguishable error is an oracle, so
  the test asserts all five paths produce literally the same string.
- **The attempt budget is claimed BEFORE the code is compared, and is durable.** Guessing
  costs a slot whether or not the guess was close, and reopening the directory - the test's
  stand-in for a restart - does not reset it. An exhausted budget locks the invitation even
  against the correct code.
- **A wrong binding never burns the legitimate invitation.** An invitation is bound to the
  requesting client's public-key hash at creation. A correct code presented by a different
  client spends an attempt but does NOT mark the invitation consumed, so an attacker who
  learns a code cannot destroy it out of spite. The first cut of this was missing and the
  test caught it: the spec binds the client at creation, and the implementation had dropped
  that field.
- **Consumption is one-time and atomic.** Eight concurrent goroutines racing one invitation
  produce exactly one winner. The consumed flag and the issued operator live in the same
  file written by temp-plus-rename, so the two facts persist together or not at all; a
  crash before the rename leaves an unused code and no credential, which is consistent and
  safe in the direction that matters.
- **Standalone v1 has one and only one local operator**, for the life of the network, and a
  joined Tower has no local bootstrap at all - its clients are admitted by Roger Core.

Two of these were mutation-verified rather than assumed: moving the budget claim after the
code comparison, and marking an invitation consumed on a binding mismatch, each fail the
suite.

`saveBootstrap` was simplified during the pass rather than exempted from coverage. Its
fsync-and-chmod plumbing added five unreachable error branches for no invariant - the
atomicity guarantee comes from the rename and from both facts sharing one file, not from
the fsync - so it is now four statements and the reasoning is written down.

Validation: full suite green at 26 packages, `go vet` and `gofmt` clean, `make cover-gate`
PASS at 91.8% with `cmd/roger-tower` 91.7% and `internal/tower` 90.7%. The gate failed this
work twice at 84.0% and 88.7% before the failure paths were covered and the atomic write
simplified.

Still open in Phase 1: the local Station registry and routing, durable storage beyond the
data directory, and the no-egress packet-capture test that actually closes the Phase 1 gate.

### 2026-08-02 — Phase 1.4 and the Phase 1 gate

**Local Station registry and routing.** A standalone Tower now attaches Stations, lists
them, and routes a request to one. The guarantees, each tested: attaching requires an
admitted operator (a network with nobody in charge cannot admit anything); a Station id
can only be updated by the same key, so a second Station cannot take over the first's
identity by re-attaching under it; only the admitted local client may route, because a
standalone Tower is not an open relay; and the receipt is unmistakably local - it carries
the standalone network id, says "local network" in plain words, costs zero, and a test
asserts the rendered string contains no mention of RogerAI at all.

**The Phase 1 gate is closed, and the way it is closed matters.**

The first version of the gate installed a dialer seam and asserted a full local flow
recorded zero dials. That test passed - but it passed partly because nothing in the
package could dial at all, and it would have kept passing while someone added a raw
`net.Dial` beside the seam. That is the same shape of vacuous test the earlier
receipt-binding review caught, so it was replaced rather than kept.

The gate is now a SOURCE-level assertion: it reads every non-test file in the package and
fails if any of them acquires the ability to reach the network - `net.Dial`, `http.Get`,
`http.Client`, any resolver call, or `exec.Command`. `egress.go` is exempt because it is
the allowlist and makes no call itself, only parsing and comparing addresses. It is
mutation-verified: adding `net.Dial("tcp", "broker.rogerai.fm:443")` to `station.go` fails
the suite with the file and symbol named. The dead dialer seam was deleted.

Alongside it, `EgressGuard` makes isolation a positive rule rather than an omission. Every
outbound destination must be a literal IP inside a declared private allowlist - loopback
and RFC1918 by default, deliberately excluding 169.254.0.0/16 so cloud instance-metadata
is not reachable. A hostname is refused rather than resolved, because resolving it IS the
DNS lookup the gate forbids and because a name that resolves to an allowed address once
can resolve to a forbidden one on the next connection. Declaring an allowlist REPLACES the
default rather than extending it, so an operator cannot accidentally keep a broader range
they forgot about. Request-supplied targets are never fetched in v1 at all: with the
caller choosing the destination, no allowlist can stop a fetcher from being an egress
proxy.

**Phase 1 status.** The standalone MVP now does the whole story end to end, verified
against the real binary: init, invite, admit, attach, list, route, with an unadmitted
client refused. Full suite green at 26 packages, `go vet` and `gofmt` clean,
`make cover-gate` PASS at 91.8% with `cmd/roger-tower` 91.5% and `internal/tower` 90.6%.

What Phase 1 still owes before a public download: durable PostgreSQL storage with the
fail-closed startup contract (`standalone_jobs.feature` specifies it; today state lives in
the data directory), the signed release artifacts and installer, and the packaging work in
`packaging.feature`. Those are build-and-release work rather than protocol work, and none
of them changes the isolation guarantee just proven.

### 2026-08-02 — founder ruling on anonymous registration; packaging and the account flow

**The ruling, and the reasoning behind it.** The founder first proposed letting Towers
register with no login, then asked whether that was a security concern. It is, and the
ruling landed at: **the line is drawn at "does this Tower carry other people's traffic?",
not at money.**

- **Standalone: no account, ever.** Nothing leaves the operator's machine, so there is
  nobody to be accountable to. This keeps the whole try-it-now path free of signup, and
  it is all of Phase 1.
- **Joined: an account is required to register at all**, which is what the already-approved
  `public_enrollment.feature` says, so no spec change was needed.

Worth recording why, so it is not re-litigated from scratch. Anonymity grants a joined
Tower no new cryptographic power - it is already treated as hostile, relays ciphertext
through an inner Core-to-Station session, and cannot forge work, alter a result unnoticed,
or change settlement. What it changes is enforcement economics. Availability cannot be
forced cryptographically, so the defences are health scoring, probation and revocation -
every one of them per-identity. If identities are free, revocation stops being a penalty
and becomes a speed bump: ban a Tower, it re-registers in seconds, and an attacker can
run a fleet that receives real routed traffic and selectively drops it. An account is what
makes revocation cost something. It also bounds the sybil surface the linked-entity
collusion controls in `operator_revenue_share.feature` depend on.

The `roger share` precedent (free on air, login to earn) does not transfer: a free Station
is a *provider* whose output Core recounts and can void, while a Tower is *infrastructure
in the path of other people's traffic*.

**Packaging.** `roger-tower` is now a released artifact: a GoReleaser build (Linux
amd64/arm64 only - a Tower is a long-running server process, so shipping a macOS or
Windows binary would hand someone a production relay they should not run), a raw archive
so the installer fetches a predictable name, and coverage by the same `checksums.txt` as
the client. The existing installer learned a `ROGERAI_COMPONENT=tower` switch rather than
gaining a second, less-tested script, so the Tower install inherits the client's platform
detection, release resolution, checksum verification, and fail-closed behaviour. It refuses
non-Linux platforms by name instead of fetching an asset that was deliberately never built.

`packaging/tower/` adds a hardened systemd unit - unprivileged user, `ProtectSystem=strict`,
no capabilities, no new privileges, the data directory as the only writable path - plus a
commented `IPAddressDeny=any` block that makes standalone isolation kernel-enforced rather
than only a property of the code. The README leads with the standalone-versus-joined table
and states plainly that standalone needs no account.

**The account flow.** `roger-tower login`, `logout`, and `register` reuse the proven GitHub
device flow rather than inventing one. `internal/towerjoin` holds all of it, deliberately
OUTSIDE `internal/tower`: signing in needs the network, and the no-egress gate fails if any
file in the standalone core gains the ability to reach it. The boundary means a standalone
operator links no network code at all into the path they run - the gate forced a better
architecture than I would have chosen unprompted.

Behaviour, verified against the real binary: standalone refuses login as meaningless
("nothing it does leaves this machine") rather than accepting a no-op; standalone refuses
registration as a category error and says a new data directory is the answer; joined
refuses registration when signed out, before any network call, and explains the
accountability reason. Real enrollment reports that Phase 2 has not shipped instead of
pretending. Signing out removes the credential and leaves the Tower identity untouched.

Spec: `features/tower/operator_login.feature`, which also records the ruling's reasoning.

Validation: full suite green at 27 packages, `go vet` and `gofmt` clean, `make cover-gate`
PASS at 91.8% with `cmd/roger-tower` 91.1%, `internal/tower` 90.3%, `internal/towerjoin`
96.6%. Two tests were corrected during the pass rather than the code: one asserted a
read-only directory blocks overwriting an existing file (it does not - that needs
permission on the file, not the directory), and one still listed `enroll` as unimplemented
after `register` replaced it.

### 2026-08-02 — broker-mediated device login (spec + core)

**Why.** A question about Apple sign-in turned up a real asymmetry: the account system is
already multi-provider (GitHub and Apple on the web, Apple natively on iOS, and
`linkedProviders` returns either or both), but `roger login` is GitHub-only. Worse, it does
not go through us at all - it calls `github.com/login/device/code` directly with a client id
compiled into the binary, then posts the resulting GitHub token to `/auth/github` to bind it.

Three costs. Adding Apple to the CLI would mean a second provider-specific flow inside the
binary, then a third. Every CLI reaches a third party we do not control, which is worst for
`roger-tower`, whose whole story is a bounded declared egress surface. And a provider change
- client-id rotation, an endpoint move, a policy shift - needs a NEW BINARY and breaks
installs that have not updated.

**The change.** The CLI asks the BROKER to start a login, prints a RogerAI URL and a short
code, and polls. A human opens that URL, signs in with whichever provider they like, and
approves. Which providers exist becomes a server-side decision that already-installed
binaries inherit, and the CLI's only outbound host is the broker. Same flow serves `roger`
and `roger-tower`.

**Spec:** `features/auth/broker_mediated_login.feature`, 24 scenarios.

**Core shipped:** `internal/deviceauth`, the start/poll/approve/deny state machine.

The property everything rests on is that the device code is bound to the requesting key AT
ISSUE. No later step accepts a key as input, so there is no point at which a different key
can be substituted - approval decides WHICH ACCOUNT, never which key. Mutation-verified:
making the key comparison a no-op fails the suite.

Also enforced and mutation-verified: the guessing budget is spent BEFORE the code is looked
up, so a wrong guess costs an attacker a slot whether or not it was close. Every approval
rejection returns one identical error, so a guess cannot reveal whether a code exists. A
poll after approval consumes the code once. Polling faster than the interval slows the
caller down rather than failing them. User codes use an alphabet without I, L, O, U, 0 or 1,
because these get read aloud down a phone.

**The residual risk, stated plainly.** Every device flow shares one weakness: an attacker
starts a flow on their machine and talks a victim into approving the resulting code, binding
the ATTACKER's key to the victim's account. That cannot be solved in a state machine - it is
solved by what the approval screen shows. So `Describe` deliberately exposes the request time
and withholds the device code (an approver who learned it could redeem the login themselves),
and the spec requires the screen to show the code for comparison, the request time and
origin, an explicit deny, and wording that warns the approver they may be authorising a
device they do not recognise.

**Not yet built:** the broker HTTP routes, the `/device` approval page, and swapping the CLI
over. The existing GitHub device flow keeps working throughout - the spec requires
already-installed binaries to land on the same account and wallet the new flow would produce.

Validation: full suite green at 28 packages, `go vet` and `gofmt` clean, `make cover-gate`
PASS at 91.8% with `internal/deviceauth` at 95.7%.

### 2026-08-03 — adversarial review before commit, and what it caught

Two fresh-context reviewers went over the whole session's work. They found real defects,
including several that would have shipped. Recording them because the pattern matters more
than the individual bugs.

**Would have embarrassed us publicly:**

- `roger-tower login` could NEVER have worked in a released binary. It called the client's
  GitHub device flow, whose public client id lives in the roger client's own `package main`
  and is not compiled into `roger-tower`; every operator would have hit "no GitHub client id
  configured". It would also have bound the account to the CLIENT's key in the user config
  directory rather than to the Tower identity - and under the shipped systemd unit
  (`ProtectHome=yes`, a system user with no home) that write has nowhere to go. And it
  contradicted `features/auth/broker_mediated_login.feature`, written the same day, whose
  whole point is that a Tower never reaches a third-party host. Now the command says plainly
  that sign-in needs the broker-mediated flow from Phase 2.
- The systemd unit ran `roger-tower serve`, which does not exist, with `Restart=on-failure`
  and `RestartSec=5s` - a permanent crash loop for anyone following the README's
  `systemctl enable --now`. The unit and the README now both say not to enable it yet, and
  the README no longer walks a reader into it.
- The README documented `roger-tower enroll`, which is not a command and falls through to
  "unknown command" - the exact obscure failure the sentence promised to avoid. It also
  omitted `login`, `logout`, `register`, `config validate` and `version`, and its walkthrough
  mixed `--dir` and `--config` so step 2 failed verbatim.
- The README claimed the installer "refuses to install if it cannot" verify. It does not: it
  says so and continues when no sha256 tool is present or the asset is absent from
  `checksums.txt`. The checksums also come from the same origin as the binary. Corrected to
  say what it actually detects (corruption and truncation, not a compromised release host)
  and to point at the unbuilt signing work.
- `install.sh`'s source-build fallback printed `go build ./cmd/rogerai` even for the tower
  component - building the client under the Tower's name.

**Security defects in new code:**

- **A denial of service I introduced myself.** `deviceauth`'s wrong-code budget was ONE
  global counter: ten wrong guesses from any attacker and nobody could complete a sign-in
  until the broker restarted. My own test asserted that as correct, and it contradicted my
  own spec ("counted against the submitting session"). Now per-submitter, mutation-verified
  in both directions - removing the budget fails, and making it global again fails.
- `Describe` was an unmetered existence oracle: it bypassed the budget entirely, so an
  attacker could enumerate user codes for free through the approval screen and spend exactly
  one Approve on a confirmed hit. It now costs a slot like Approve.
- The slow-down penalty was monotonic and did not record the poll, so a tight loop grew the
  interval without bound past the TTL and could permanently strand the legitimate CLI from
  its own login. Capped, and the poll is recorded.
- No reaper: expired, denied and consumed logins were never deleted, so any signed caller
  could grow broker memory without limit.
- `tower/bootstrap.go`'s global probe budget was permanent - 50 bogus ids and the network
  could NEVER be bootstrapped, with no reset short of editing JSON by hand. Now decays over
  a window.
- `rootFingerprint` swallowed its error and returned "", so a credential could be issued
  pinning NOTHING, silently disabling the "reject a different root on reconnect" protection.
  It now returns an error and admission fails closed.
- The comment claimed cross-process exclusion via the identity-directory lock, but
  `State.Lock()` was never called anywhere. Two concurrent `admit` processes could each pass
  "no operator yet" and both be admitted, defeating the singleton-operator invariant. Every
  command now takes the lock.
- `Invitation()` returned the HMAC verifier that `String()` is careful never to show.
- `CreateInvitation` accepted a zero budget or window, minting invitations born locked or
  born expired that then failed with the uniform rejection, telling the operator nothing.

**Correctness:**

- **A node's FIRST receipt was recorded as a break.** An absent row gave `prior == ""`, so
  every node that already had an in-process chain would have been flagged the moment head
  tracking shipped - precisely what the detect-and-record design says must not happen. First
  sighting is now a baseline, in both backends.
- Expiry used second granularity, silently truncating any window shorter than a second to
  "already expired". Now nanosecond-precise.
- The stations dashboard rendered a ledger read failure as **$0.00 earnings** on a money
  page. It now reports `earnings_unavailable` - a fabricated zero an operator reads as "I
  earned nothing" is worse than an honest gap. The `served` count was also silently capped
  at 100; renamed `recent_served` and labelled "Recent requests", because it is a window.
- Mem and Postgres disagreed on whether an idempotent replay stamps the check time.

**Acknowledged, not fixed:** `internal/deviceauth`, `EgressGuard` and `VerifyBroker` have no
production callers yet. That is the known sequencing - the broker routes, the `/device` page
and the CLI swap are the next task - but it is worth naming rather than letting three
well-tested packages look wired when they are not.

Validation after all fixes: 28 packages green, `go vet` and `gofmt` clean, `make cover-gate`
PASS at 91.8% (roger-tower 91.3%, deviceauth 92.3%, protocol 96.6%, store 90.6%, tower 90.2%,
towerjoin 96.6%). Web build green; the two failing web tests are another session's
`company.html` copy, confirmed unrelated by stashing them.

### 2026-08-03 — the auth flow, wired end to end

Understanding the existing flow first turned up the real shape of the problem. There were
three login paths, and they did not have the same providers: the web had GitHub and Apple,
iOS had native Apple, and the CLI had GitHub only - reaching github.com directly with a
client id compiled into the binary.

Now wired, and tested against a live broker over real HTTP:

- **Five broker routes.** `/auth/device/start` and `/auth/device/token` are signed by the
  CLI; `/auth/device/pending`, `/approve` and `/deny` carry a browser session. The split IS
  the design: the device code never crosses to the browser, and the session never crosses
  to the CLI.
- **The `/device` page**, carrying the approved copy. It leads with what is happening, gives
  one rule a person can follow, names the three ways a code reaches you from someone else,
  and labels the deny button "Not me". No alarm styling: someone frightened by a routine
  sign-in learns to click through warnings.
- **Both binaries** now sign in through the broker. `roger login` keeps a fallback to the old
  provider-direct flow so a new CLI still works against a broker deployed before this, and
  falls back only on a transport failure - a denial or an expiry is the person's decision,
  not a reason to try another route. `roger-tower login` works at all for the first time,
  because the brokered flow needs no client id this binary never carried.

**What made Apple-on-CLI possible.** The Apple web flow deliberately binds no owner row -
"the browser has no device pubkey to bind". But a device approval DOES have one: the CLI's.
The blocker was that the session carries only a hash of the Apple sub, which is
irreversible, so approval had nothing to create the owner row from. The session now carries
the sub as an optional fifth field, and `verifySessionFull` accepts both the four- and
five-field payloads so sessions minted before this keep working. An older Apple session is
told to sign out and back in rather than failing opaquely.

Two tests were corrected rather than the code, and one of them mattered: the foreign-key
rejection test pointed the client package at a new config dir to get a "second device", but
the identity is cached process-wide, so it was signing with the SAME key and passing while
proving nothing. It now signs with an explicit second key over real HTTP. The other assumed
`DeviceLoginPoll` persisted the account; the save lives in `DeviceLoginComplete`, which was
extracted so the wait-and-store half is testable without the interactive printing.

Validation: 28 packages green, `go vet` and `gofmt` clean, `make cover-gate` PASS at 91.6%
with `internal/deviceauth` 94.5% and `internal/client` 92.6%. The gate caught deviceauth
falling to 87.3% when `BoundKey` and the reaper arrived uncovered.

### 2026-08-03 — finishing the auth flow

Three gaps closed, and one of them was a plain UX break.

**A signed-out approver lost their code.** Someone landing on /device.html without a
session was sent to sign in and then dropped on the dashboard, having lost the code they
were about to approve. The web login routes now carry a `next`, and the callback returns
them there.

That parameter is the classic open-redirect hole - `?next=https://evil.example` and a link
that genuinely came from us bounces the victim off-site, which is exactly the shape a
phishing flow wants. So `safeNext` is a strict allowlist rather than a blocklist of known
tricks: a same-site absolute path only, rejecting protocol-relative `//host` and `/\host`,
control characters and CRLF injection, anything carrying a scheme or host, and the same
checks again on the decoded path. The destination rides in its own short-lived cookie
rather than the OAuth state, so it never leaves our origin and a provider cannot echo back
an altered one - and it is RE-validated on the way out, because a cookie is client-supplied
and trusting it merely because we set it once is how these holes get reopened. Anything
unsafe falls back to the dashboard rather than erroring: a person who was phished should
still land somewhere sane.

**The TUI had a second, different login.** It still ran the provider-direct flow, so the
same product offered two sign-ins - which is how someone ends up with two accounts. The
begin/poll pair now uses the brokered flow, and the handle records which flow started so
the two can never be crossed.

**The spec was prose-only.** It now runs against the real routes, the real state machine
and the real client package. Four scenarios are fully driven - the start payload,
provider-agnostic approval, denial, and what the approval screen may read. Thirty-two
remain undefined and the feature file says so plainly: they are claims about page wording
and styling, which need a browser to assert honestly, and CLI-side behaviour better covered
where it lives. godog reports the undefined steps on every run, so the gap stays visible
rather than reading as covered.

Validation: 28 packages green, `go vet` and `gofmt` clean, `make cover-gate` PASS at 91.6%.

### 2026-08-03 — durable startup: refuse rather than lose state quietly

The last Phase 1 gap. A Tower that cannot keep its state must refuse to serve, not serve
and discover the loss later.

`storage.profile` is now an explicit contract with two values. **development** is the
default and says so on every readiness report - identity, admission state and attached
Stations may be lost on restart. **durable** promises they survive, so `roger-tower ready`
verifies every dependency that promise rests on and exits non-zero when one is missing.

The spec named six dependency classes rather than saying "check the dependencies", and the
reason is that each has a different repair. So every problem carries a `Repair` alongside
its `Detail`, and a test asserts the two are never the same string - a repair that restates
the fault is not a repair. A missing offline root says to restore the identity volume from
backup and explains that a standalone network cannot be re-rooted without invalidating
every admitted client; a network with no operator says to run invite and admit; a missing
database secret says to mount it or drop the setting.

`ExecStartPre` in the systemd unit runs the preflight, so the unit refuses to start rather
than crash-looping into an empty state.

**What the durable profile does not yet mean**, stated in the README rather than left to be
discovered: Tower state still lives in the data directory, not in PostgreSQL. The profile
verifies the dependencies; moving the state itself is separate work. Claiming durability we
have not built would be exactly the silent loss this preflight exists to prevent.

One test was corrected rather than the code: it demanded that an empty `profile:` be
rejected, but YAML cannot distinguish that from the key being absent, and absence
legitimately means development - so the assertion would have rejected the common case.

Validation: `internal/tower` and `cmd/roger-tower` green, `make cover-gate` PASS at 91.6%.
Verified against the real binary: a complete durable Tower reports READY, and one whose
offline root has been removed exits non-zero with the repair named.

### 2026-08-03 — persistence, and why the driver lives outside the core

Durable storage now durably stores. `internal/tower` defines a `Store` seam;
`internal/towerstore` implements it on PostgreSQL.

**The driver is in a separate package deliberately.** `internal/tower` is covered by a gate
test that fails if any file in it gains the ability to reach the network, and a database
driver dials. Keeping the driver outside is what lets the standalone core stay provably
egress-free while still having a database - the same boundary the joined account flow
needed, and the gate forcing a better architecture than would have been chosen unprompted.

**The concurrency contract carries as much weight as the durability one.** A file-backed
Tower is serialised by the identity-directory lock, but a database-backed one can have
several processes, so writes are compare-and-swap on a persisted revision: a write from a
stale read is REFUSED. Two operators being admitted and one silently vanishing is the
failure that prevents. Verified against a live PostgreSQL - round trip, stale-write
refusal, and the whole admission flow surviving a restart with a consumed code still
consumed.

**The allowlist applies to the database too.** A Tower pointed at a hosted database is not
the standalone thing it claims to be, so a public host, a public IP, or instance-metadata
is refused. One exception, made explicitly rather than by loosening the guard: `localhost`
is accepted as the CONSTANT it is - substituted for the loopback literal, never resolved -
because every PostgreSQL DSN a person writes says localhost and refusing it would break the
documented path for everyone. Substituting a literal performs no lookup, so the no-DNS
property is untouched, and every other hostname is still refused rather than looked up
(`LOCALHOST` and `localhost.evil.example` included).

The state is one row holding a JSON snapshot plus a revision. That is deliberate for v1:
the admission state is small, is always read and written whole, and its correctness rests
on the compare-and-swap rather than on relational structure. Normalising it buys nothing
until something needs to query inside it.

Validation: 29 packages green, `make cover-gate` PASS at 91.6% with `internal/tower` 90.9%
and `internal/towerstore` 90.5%. The gate failed this work first at 89.6% and 77.8%, which
is what forced the store seam and the driver failure paths to be covered rather than
assumed.

### Session: Phase 2.1, the admission registry (2026-08-03)

`internal/toweradmit` is Roger Core's record of which joined Towers exist, who owns them,
and what each may do right now. Everything else in Phase 2 - certificates, the outbound
link, dispatch leases, execution grants - hangs off that question, so it is the first
increment.

One idea runs through the whole package: Roger Core alone decides a Tower's state. No
function accepts a state from the Tower. `RecordClaim` exists precisely so a Tower that
asserts a state it does not hold has that recorded as evidence (`FalseClaims`) rather than
applied. Enforcement on those counts is a separate, approved decision - the same
detect-and-record posture the founder chose for the receipt chain.

The properties the tests pin, each mutation-verified (the guard was deleted or neutered and
the suite was confirmed to fail):

- A Tower's own claim cannot change its state (4 tests fail when the claim is applied).
- `revoked` and `expired` are terminal (5 tests fail when revoked can return to active).
- An expired lease takes no new work even while the state still reads `active` (4 tests
  fail when the lease check is dropped) - the lease is what bounds offline drift.

Admission always starts in `quarantine`. Having an account proves who is accountable, not
that the Tower behaves; promotion is earned from centrally observed evidence. Enrollment
tokens are one-time and account-bound, one identity key binds to exactly one Tower (else a
suspension would stop only one of a machine's several admissions), and a rejected
enrollment creates nothing at all - no partial identity for a later attempt to adopt as
real, and no burnt token for the legitimate holder.

`internal/toweradmit` (Core side) and `internal/towerjoin` (Tower side) are complementary,
not duplicated. `towerjoin.enroll` is still a stub; it is the seam this registry will
answer once the Core-side route exists.

A fresh-context reviewer ran an 18-mutation campaign and caught that the first version of
this package had been built from memory rather than from the spec's table. Four edges were
wrong, and two of them mattered:

- `suspended -> active` was implemented AND blessed by a test. The spec's only way out of
  suspension is back to `quarantine`, on explicit review clearance and fresh probes. As
  written, a Tower suspended for a security decision could resume full public traffic with
  no re-quarantine and no probe evidence. A test locking in a spec violation is the worst
  version of this bug, because it looks like coverage.
- `expired` was treated as terminal. The spec re-admits a lapsed Tower through quarantine
  on fresh key proof and fresh probes; only revocation is final. Combined with the quota
  bug below, an expired Tower was permanently dead and its key permanently burned.

Also fixed, all from the same review: `Renew` silently resurrected a lapsed lease (routing
around the entire re-admission control); revoked and expired Towers consumed their owner's
quota forever, so an operator who revoked their Towers was locked out of running any; the
enrollment-token map was never pruned, which an account holder could grow without bound;
a state outside the enum was scored as a false claim instead of refused.

The lesson for the rest of Phase 2: the transition table is now enumerated exhaustively in
the test - all 49 pairs asserted against the spec's own edges - rather than sampled. It was
sampling that let four wrong edges through, and the samples all passed.

The reviewer also showed the concurrency claim was fiction. `TestATokenIsOneTimeAndConcurrentUseAdmitsOne`
called `Enroll` twice sequentially, so deleting the mutex left the suite green. It is now
16 goroutines racing one token under `-race`, asserting exactly one admission - the spec's
headline race - plus a concurrent read/write sweep over the whole surface.

Validation: `make cover-gate` PASS at 91.6% total, `internal/toweradmit` 96.0%, green under
`-race`. Nine mutations were re-run against the corrected guards and eight were killed;
the ninth was a no-op mutation of mine (returning `Tower` by value makes the copy property
a type-system guarantee, so that test documents the signature rather than guarding it).

Open item for the founder: `features/tower/public_enrollment.feature` still carries the
`PROPOSED SPEC` banner. This package now conforms to that table exactly, which is strictly
safer than the invented one it replaced, but the banner should be cleared or the table
revised before Phase 2 builds further on it.

### Session: Phase 2.2, purpose-separated keys (2026-08-03)

The founder approved the Tower spec set on 2026-08-03; the PROPOSED banner is cleared
across all 24 feature files.

`internal/keypurpose` gives every Roger Core signature and secret exactly one named
authority role. The property it exists for is the one the spec opens with: compromising a
relay, a cookie, a pseudonym, an admin channel, or any single signer cannot silently
become settlement authority. A valid signature from the wrong role is not a weaker
credential, it is no credential.

This comes before certificates, dispatch leases, execution grants and settlement because
each of those names the purpose it requires. Building any of them first would mean
inventing that vocabulary twice.

The role enum is READ FROM the approved feature file at test time rather than restated in
Go. Two tests assert both directions - every role the spec names is a known purpose, and
every known purpose is named by the spec - so a hand-copied list cannot drift. On a table
of 57 roles, a quietly missing row is a quietly missing control.

The Cartesian invariant is asserted over all 57x57 ordered pairs, not sampled. That choice
is a direct consequence of Phase 2.1, where sampling a transition table let four wrong
edges through and every sample passed.

Three layers defend the same property in `Verify`: the envelope's claimed purpose, the
key's own purpose, and the purpose bound into the signed bytes. Mutation testing showed
the honest picture - deleting any ONE left the suite green, because each is subsumed by
the others. Two now have isolating tests (a relabelled envelope, and domain separation of
the signed bytes). The third, the envelope check, is strictly subsumed and CANNOT be
isolated; it is kept as a fail-fast layer on the money path and both the code and the test
say so plainly, rather than implying a test proves it.

Also pinned by mutation: the distinctness check catches all four ways one root wears
several hats (shared public key, shared managed-key alias, shared derivation root, shared
fallback), a missing role fails startup rather than the first signature, a missing key
never mints a replacement or borrows another role's, a retired key stops signing when its
overlap ends while its history stays verifiable, and no rendering carries private key
material, symmetric secrets, or a derivation root.

A fresh-context review then ran an 18-mutation campaign and found a genuine forgery
bypass plus a spec property that was true only of a test.

**The bypass.** `Verify` decoded the signature with `hex.DecodeString` and checked the
error. A mutation dropping that error check SURVIVED the whole suite - and it is not a
redundant guard, because Go returns the successfully decoded PREFIX alongside the error.
Confirmed against Go's actual behaviour rather than assumed: a valid 128-character
signature with `zz` appended decodes to 64 intact bytes plus an error. Ignoring the error
means an attacker appends two characters to any valid signature and verification passes.
The guard was correct; the test used `"not hex at all"`, which fails on length instead and
proved nothing. The test now includes trailing-garbage and odd-length cases.

**The property that was only true of a test.** The spec says a retired private key stops
signing after its overlap. `canSignWith` knew that - and had NO production caller. It
existed so the tests could assert the property that `Sign` did not enforce. `Sign` now
consults it, and also enforces the key's own `NotBefore`/`NotAfter`, which nothing checked
at all: an expired key signed indefinitely, making "bounded validity interval" decorative.

Five more real defects, all fixed and mutation-pinned:

- Distinctness compared `KeyID`, a 64-bit exported display label a config loader can set
  independently of the key it names. Two roles could load the IDENTICAL private key under
  different labels and validate - exactly the one-root-many-hats case the check exists to
  stop. It now compares the raw public key.
- `Validate` demanded an offline-root private key in the runtime ring, so a correctly
  operated Core - root in a vault, per the spec - could not start. Roles now carry a
  held-at-runtime classification.
- `Verify` returned a distinguishable error for an unknown key ID, a key-enumeration
  oracle the spec explicitly forbids. The two cases are now indistinguishable.
- `Rotate` returned a pointer to the live record that `Validate` and `Describe` read under
  the lock, inviting an unsynchronised write from the first production caller.
- Retired keys were exempt from distinctness while still resolving during verification, so
  a collision made key lookup nondeterministic.

Two process fixes came out of it. `ed25519.Verify` PANICS on a wrong-size public key, and a
panic report is exactly the surface that must never carry key material, so a malformed
public key is now refused as a bad signature. And `-race` ran nowhere in this repo -
Makefile, cover gate, and CI were all plain `go test` - which meant every concurrency test
in the module, here and in `internal/toweradmit`, asserted nothing as actually run. `make
test` now runs `-race`; the whole `internal/...` tree is clean under it today.

Four test-only helpers were living in the production file, and one of them was the
`canSignWith` above. They moved to `export_test.go`, because a seam that looks like
production code is how a spec property comes to be enforced only in tests.

Validation: `internal/keypurpose` 97.6%, `make cover-gate` PASS at 91.6%, green under
`-race`, and all eleven re-run mutations killed.

### Session: closing the in-scope key_separation gaps (2026-08-03)

Three scenarios the previous increment left unimplemented are now done, and the spec file
is the input to all three rather than my reading of it.

**The key-loading failure table.** 73 rows, of which exactly one was exercised. Its
`<result>` column describes what each failure stops at a higher layer ("no new joined job
can be issued"), which belongs to the component consuming the purpose - but the second
half of the scenario is the keyring's, and it is the half that matters: *it never silently
generates a new production authority or falls back to another role's key*. That is now
asserted for all 52 in-scope rows across all five failure modes the Given names - 260
subtests. Rows are resolved from the feature file, with the handful of wording differences
between the two spec tables written out as an explicit alias map, and the two Tower/Station
rows declared out of scope by name. A row matching neither fails the suite, so "unmapped"
always means someone must look rather than silently skipped.

Only *missing* had any representation before. `LoadFailure` now distinguishes missing,
malformed, unreadable, duplicated-across-roles and unavailable, because an operator
repairing a malformed key does something different from one whose key is merely
unavailable. A failed role drops its material rather than keeping it beside a flag, fails
startup, and cannot be rotated over - repair is an explicit act, not a side effect of
asking for a new key, which is how a known-bad role quietly returns to service.

**Symmetric secrets.** Seven roles are not signers: a session cookie, a pseudonym, an
admin token, an evidence key, a webhook secret and two API credentials. The ring only did
ed25519, so the spec's actual claim about them had no implementation. Now `Kind` separates
the two, the kinds do not interchange in either direction, and each secret is independent
material - deriving them from one root would be precisely the "cross-role key derived from
the possessed bytes" the spec forbids. The concrete test builds an attacker's ring from a
stolen session secret and confirms it authenticates nothing as administrator.

That work exposed a real hole in the existing distinctness check: symmetric roles have no
public key, and the check skips empty values, so **every secret role could have shared one
secret and validated**. Distinctness now compares a material commitment - the public key
for a signer, a one-way digest for a secret - so secrets are compared without ever being
held or rendered.

Fourteen new guards, all mutation-pinned. One needed isolating: the purpose binding inside
the MAC is redundant *given* independent secrets, so no mutation could kill it until a test
put the ring into the state where it is the only remaining defence - two roles sharing one
secret. Validate refuses that at startup, but a running ring can be put into it, and at
that point only the binding stops a session cookie authenticating as a pseudonym.

Validation: `internal/keypurpose` 96.1%, `make cover-gate` PASS at 91.6%, `internal/...`
clean under `-race`.

Still out of scope by the founder-approved phasing: the standalone local-role set, the
certificate objects themselves, and the offline-root ceremony.

### FOUNDER RULING 2026-08-03: security is never out of scope

> "nothing is out of scope if it's enhancing or making better security in our product"

Phase boundaries sequence features; they do not gate security. Nothing that hardens the
product waits its turn, and shipping a partial separation model is itself the risk. If a
security item is genuinely blocked - a founder decision, an external dependency, a ceremony
that cannot be automated - it is BLOCKED and the blocker is named. It is never "out of
scope".

This ruling reversed my own framing from the previous session, where I listed the
standalone local-role set, the Tower/Station separation, and the offline-root handling as
deferred by the approved phasing.

### Session: a key belongs to a network, not only to a role (2026-08-03)

Four separate scenarios in the approved spec turned out to be one property:

- a standalone trust root has no public-network validity;
- a public RogerAI key has no implicit local admin power;
- a joined Tower key cannot exercise central or leaf authority;
- a Station key cannot exercise Tower or central authority.

Each says the same thing: material issued under one trust root carries no authority under
another. So they are implemented as ONE realm check rather than four bespoke rejection
lists. That matters more than the line count - a role added later inherits the separation
automatically, instead of depending on somebody remembering to extend a fifth list.

Four realms now exist: Roger Core (57 roles), a standalone Tower (20), a joined Tower (4),
and a Station (3). A ring is built for exactly one and holds no other realm's keys at all,
because material that is never held cannot be stolen - the realm check is not the only
thing standing between two networks. Cross-realm material is refused as FOREIGN rather
than as a wrong-purpose key: those are different problems and an operator needs to see
which one they have.

The standalone role list is parsed out of the spec's prose scenario, as the Core table
already was. Twenty roles hand-copied is twenty chances to drop a control silently.

The 19 standalone rows of the key-loading failure table are now in scope too, per the
ruling above. The table test covers 71 of its 73 rows across all five failure modes - 355
subtests. The two remaining rows name a Tower's and a Station's own identity keys, which
live in those realms' rings and are covered by their own tests; they are declared by name,
so an unmapped row still fails the suite.

Also closed here: a Tower's persistent identity-statement key is separate from its rotating
TLS key, so rotating a certificate never touches who the Tower is and a stolen TLS key
proves nothing about its identity; a Station's provider-assertion key is separate from its
secure-session TLS key; and a Tower's local bridge authorities are distinct from each
other, from Tower identity and TLS, and hold nothing centrally. Both offline roots are
absent from ordinary runtime and neither absence blocks startup.

Fourteen realm mutations, all killed. Two needed isolating tests: the check on the
PRESENTED material's realm is subsumed by the required-role check unless a foreign
signature is aimed at a role the ring genuinely holds, and `LookupIn` ignoring its realm
argument only shows up when a name from one network is resolved against another.

One real bug found by that campaign rather than by the tests: an edit had silently applied
twice, leaving a duplicated realm block in `VerifyMAC`. It was harmless in behaviour, but
it made the first mutation look like it survived - the second copy was still enforcing.
Worth recording, because "the mutation survived" is a signal I act on, and a duplicated
guard is a way to get a false reading from it.

Validation: `internal/keypurpose` 96.0%, `make cover-gate` PASS at 91.6%, `internal/...`
clean under `-race`.

### Session: a login of our own, and state that outlives the process (2026-08-03)

Founder direction: make sure nothing is left, that persistence is real, and that sign-in is
managed by us the way other AI products manage theirs. Auditing against that turned up three
places where state that decides who you are lived only in one process's memory.

**Device login could not complete behind more than one instance.** `deviceauth.Flow` kept
every pending login in process-local maps and never touched the shared store the rest of the
broker already uses. Behind a load balancer the CLI polls one instance while the human
approves on another, so the approval was written to one map and the poll read another's -
the flow was not degraded, it was uncompletable, and it failed looking like an attack rather
than a bug. A restart had the same shape with a worse message: the login was dropped and
reported with the uniform "that code is not valid", which is the rejection meant for a
guesser aimed at a person whose code was fine.

The flow now runs over a `Store` seam - in-process by default, Valkey-backed when one is
wired, with the CAS done in a Lua script so consumption is ONE atomic decision. That last
part is what makes "a code is spent once across the deployment" true rather than likely: two
instances each reading "not consumed yet" is the classic double-spend. Records carry only
hashes now; in memory the codes were reachable by one process, but in a shared store they
are reachable by a backup, a replica, and anything that can run a scan.

Deliberately NOT following the house `sharedStore` rule that every call site falls back to
memory on error. Every other user of that store is an accelerator whose local answer is
merely less accurate; a pending login is the authority on whether somebody approved
something, and a per-instance fallback is exactly the split-brain being removed. So a store
outage REFUSES to issue a login rather than issuing one it will lose, and a poll during an
outage is retryable rather than invalid - which needed a fix on the CLI side too, where any
non-200 used to end the login outright.

**Sign-in is now ours.** Every identity in the system was borrowed - an owner row keyed on a
GitHub id or an Apple sub - so anyone holding neither could not sign in at all, two third
parties could lock a customer out of an account holding a wallet balance, and provider
outage was total sign-in outage. `internal/emailauth` adds a RogerAI account keyed on a
VERIFIED email, entered with a mailed code. GitHub and Apple remain, unchanged.

Two decisions worth recording. The package never consults an account store, so it cannot
tell a known address from an unknown one - the strongest form of enumeration resistance is
having no branch to leak rather than two branches carefully matched. And the mail carries no
one-click link: a followed link authenticates whoever followed it, in whatever browser
followed it, including a mail scanner that fetches every URL it sees. Typing the code back
into the session that asked is what ties the person who requested to the person who arrives.

Auto-linking is gated on BOTH sides being verified. Linking on a provider's unverified
address is full account takeover: anyone who can set an arbitrary email at a provider could
claim a RogerAI account holding a balance. And linking is never merging - an email account
that also holds a GitHub link keeps the wallet it already had.

**One real leak, found by reading this flow's own test output.** The mailer logged the full
recipient address, and the sign-in subject carried the code - so an ordinary log line held a
live credential next to the address it belonged to. Logs are shipped, retained, searched and
pasted into tickets, with none of the account store's protections. The address is masked in
logs now and the code never enters a subject at all, which is safe by construction rather
than by remembering. Both are permanent regression tests.

**The admission registry is durable now too.** `toweradmit.Registry` - Roger Core's record
of which Towers are admitted, their leases, their lifecycle states, and the false-claim
evidence against them - was also process-local memory. Two of the consequences were not
merely inconvenient: a REVOCATION was undone by the next deploy, which also un-burned the
identity key revocation exists to burn, and FALSE-CLAIM EVIDENCE was erased, so a Tower
that lies about its state reset its record every time we shipped.

It runs over the same Store seam, with a PostgreSQL implementation because this is
authoritative Core state rather than an accelerator, and every state change applied by
compare-and-swap so a decision made from a state we never saw cannot overwrite one we did.
Two rules the database now enforces rather than application code: a UNIQUE key_hash, so
two concurrent enrollments presenting one identity key cannot both win, and a single DELETE
whose row count IS the redemption decision, so one token admits exactly one Tower.

Writing it caught a regression against an approved scenario. Consuming the token as it was
read made redemption atomic but burned a valid token on a REJECTED attempt - which the
existing suite refused, correctly: the legitimate holder still needs it. The token is read
without consuming, and spent last, once every other check has passed. Both properties hold,
and the approved test is what caught it rather than a reviewer.

Also removed rather than kept: an Open() that dialled its own connection. The broker
already holds a pool to the authoritative database, so a second one would double the
connection footprint and give the registry a lifecycle of its own to get wrong.

Still open: the registry has no HTTP surface, so nothing reaches it from outside yet. That
surface is the first half of Phase 2.3.

### Session: Phase 2.3 begins - the credential, and an atomic admission (2026-08-04)

Two slices, both shipped and gated.

**The certificate a joined Tower speaks with** (`internal/towercert`). Everything downstream
trusts the Tower ID a certificate asserts, so the package answers exactly one question: can
a machine end up speaking as a Tower it is not? It names exactly one Tower (two identities
is an ambiguity an attacker resolves), carries no authority beyond the channel (it may not
issue, and it is client-auth only, so a Tower can never become a second admission
authority), and is short-lived, because a certificate cannot be recalled once handed out -
expiry is the ordinary end, revocation the urgent one.

Two places the standard library is not enough on its own, and the code says so: x509.Verify
accepts a CA as a leaf, and it only checks that the requested key usage is PRESENT, so a
certificate carrying extra powers still passes. Both re-checked. What Verify does give free
is rejecting critical extensions it cannot evaluate - the spec's "unsupported critical
constraint" row. The revocation set is a constructor parameter, not accumulated state, so a
revocation cannot be undone by the next deploy.

**The admission bundle is one transaction.** Enrollment consumed the token and then inserted
the Tower; a crash between them spends the token while no Tower exists, leaving the operator
holding a receipt for an admission that never happened. `Admit` does both in one
transaction - DELETE ... RETURNING then insert - which also makes concurrent admissions
serialise on the row lock rather than on luck. Eight racers against a real database confirm
one token admits one Tower.

The other bundle members live on the Tower ROW rather than in tables beside it, so one write
commits all of them: any window where they disagree is a window where a Tower holds a
certificate the registry has no lease for.

This came out BETTER than the approved scenario required. The spec allowed a failed
enrollment to have spent the token; the transaction rolls the consumption back too, so the
operator retries on the same token rather than needing a fresh one for a failure that was
ours.

**Enrollment itself** (`internal/towerenroll`). Three independent proofs, and holding any
one of them is not enough: the token proves an operator was approved, the challenge
signature proves the machine holds the identity key it claims (knowing a public key proves
nothing - it is public), and the CSR proves it holds a SEPARATE channel key. The two keys
may not be the same one, because sharing them means rotating a certificate rotates the
Tower's identity and a stolen channel key becomes proof of who the Tower is.

The challenge is spent BEFORE its signature is checked. A nonce spent only on success lets
an attacker probe one challenge repeatedly, and the signature stays valid forever - the
one-time nonce is the only thing stopping a replay.

A MISSING CONTROL, found by checking what each rejection actually failed ON rather than
that it failed. Three rows of the 23-row table were caught by an earlier check and never
reached the control they were named for, and one of those controls did not exist: the
enrollment token is a bearer credential and nothing bound the request to the authenticated
account, so anyone holding a leaked token could enroll a Tower onto somebody else's account
and be paid for it. Requests now carry the operator the broker authenticated, compared
against the token owner in constant time. The other two rows now have tests that reach
their checks instead of tripping an earlier one. A test that passes for the wrong reason
proves nothing, and this is the second time this week that reading the actual failure - not
the pass/fail - is what found the bug.

**KNOWN GAP, named rather than deferred.** `towercert.NewAuthority` mints a FRESH root each
call. That is correct for tests and first-run bootstrap, and wrong for production: a
restart would issue a new root, and every certificate already in operators' hands would
stop authenticating. `NewAuthorityFrom` exists and takes a persisted root, key, and
revocation set - so the shape is right - but nothing loads one yet, and the root key is
material that wants a deliberate custody decision rather than a default. That decision, and
the HTTP surface that reaches enrollment, are the next slice.

Remaining in 2.3, in order: the enrollment HTTP surface plus CA-root custody (above), the
outbound multiplexed link, inner Station sessions, inventory revisions, and receipt v2.

### Session: registration works end to end, and the root has somewhere to live (2026-08-05)

Founder direction: make this deployable, for production and as open source.

**The CA root gap is closed.** `NewAuthority` minted a fresh root per call, so a restart
would have invalidated every certificate on the network. Three ways to get one now, in
priority order - injected as PEM from a secret store, persisted from an earlier start, or
generated once and logged loudly. A HALF-configured root is refused rather than falling
through to generation: a missing environment variable must not be able to make us issue
under a root nobody chose. Both halves are checked against each other at startup, so a
mismatch reads as a configuration error rather than as every Tower being rejected later.

**Admission is OFF unless it can be durable.** No database means no registry, no persisted
root, no committed-enrollment record - so an admitted Tower would be forgotten by the next
deploy and a revocation would undo itself. The routes refuse plainly instead. Standalone is
unaffected.

**Four routes and a client**, so `roger-tower register` now walks the whole path: mint a
one-time token, take a challenge, enroll, read back your own Towers. The exact bytes to sign
come from the broker rather than being rebuilt client-side; getting that framing subtly
wrong would surface as "invalid enrollment" for a problem that was never the operator's.
The transaction id is generated once and kept across failures, so an interrupted
registration retries as the same enrollment - a test fails the first attempt and asserts
exactly one Tower was ever issued.

**Two production defects found by reviewing rather than by testing.** Email sign-in had been
wired to the in-process store, so it could not complete behind more than one instance and
its rate limits were multiplied by the fleet size. Tower enrollment kept its challenge and
its idempotency record in maps, and losing the second means a spent token with nothing
remembering what it bought - unrecoverable without an administrator. Both now sit behind
their stores. That is three times this week the bug was found by asking what happens in
production rather than by a red test.

**Open source.** A live DKIM selector, our provider-migration rationale, and our cache
inventory were in the public repo against the working agreement. Removed, while keeping
everything a self-hoster needs to configure their own providers. The self-host path is
verified: no Redis, no Postgres for auth, and no mail provider required to run the broker.

Remaining in 2.3: the outbound multiplexed link (which is what `serve`, `drain` and `revoke`
still wait on), inner Station sessions, inventory revisions, and receipt v2.

### Next action queue

1. Tower Phase 2.3 - the outbound multiplexed link next against the already-approved specs: outbound multiplexed link, inner
   Station sessions, inventory revisions, signed grants, exact result binding, receipt v2.
   214 approved scenarios across five feature files; `towerjoin.enroll` and
   `roger-tower serve --joined` are the two stubs it replaces, and the durable admission
   registry is already behind it waiting for its HTTP surface.
2. Founder approves or revises the recommended per-settlement program-cap interpretation in
   decision 1 and decisions 2 through 10, thereby approving the proposed scenarios.
3a. Before step definitions are written, run the step-vocabulary unification pass and finish
   the field-table conversions; at 97.7% distinct phrasings the suite is otherwise roughly
   50,000 lines of glue and will not get written.
4. Add red tests for Phase-0 exact job/receipt binding first, against real persistent money
   dependencies for chat, stream, and audio.
5. Add red canonicalization/key-directory tests for receipt v2.
6. Only after the red evidence, implement the smallest Phase-0 trust repair.
7. Re-run the complete suite, coverage gate, and fresh-context security/minimization review.

## Evidence ledger

| ID | Claim | Class | Failure impact | Validation/oracle | Evidence | Result | Decision |
|---|---|---|---|---|---|---|---|
| E1 | Current multi-instance mode is a trusted replica mechanism, not an external federation boundary. | measured | Shipping it grants independent operators internal registry/bus authority. | Inspect shared-store contents and adoption path. | `cmd/rogerai-broker/sharedstore.go`, `tunnel.go`, `features/multinode/cross_instance_relay.feature` at this revision. | pass | Build a separate scoped joined protocol. |
| E2 | The current broker combines relay, identity, policy, money, admin, and signing powers. | measured | A packaged broker has an excessive attack surface and secret set. | Inspect route table, constructor, and configuration. | `cmd/rogerai-broker/main.go` at this revision. | pass | Build a least-privilege `roger-tower`. |
| E3 | Current settlement does not exhaustively bind a signed receipt to the dispatched job context. | measured | A mismatched signed receipt can address the wrong settlement/hold state. | Trace relay verification to store finalization for chat, stream, and audio. | `cmd/rogerai-broker/tunnel.go`, `audio.go`; `internal/store/postgres.go` at this revision. | fixed | P0 trust repair before federation. |
| E4 | Current broker recount and grant fields are excluded from both existing signatures. | measured | Final adjudication fields can change without invalidating either signature. | Inspect canonicalization and mutation tests. | `internal/protocol/protocol.go`, `receipt_test.go`, `features/trust/lineage_receipts.feature`. | fixed | Separate provider assertion and settlement receipt. |
| E5 | Current receipt hash chaining is local, volatile, and not checked against a central head. | measured | Restart, interleaving, omission, and fork are not detected as a complete ledger. | Inspect agent state and broker validation references. | `internal/agent/agent.go`; repository-wide `PrevHash` references. | partial | Persistent per-Station sequence plus Core CAS head. |
| E6 | Existing release automation packages the client, not a signed Tower distribution. | measured | Operators cannot securely install or update the intended component. | Inspect release config, installer, and container assets. | `.goreleaser.yaml`, `web/src/install.sh`, `Dockerfile`, `packaging/`. | pass | Add dedicated signed Tower pipeline and fail-closed installer. |
| E7 | TLS 1.3 provides authenticated confidential transport; X.509-SVID gives a standard URI SAN workload identity profile. | sourced | A bespoke transport/identity design increases interoperability and audit risk. | Standards specifications. | RFC 8446; SPIFFE X.509-SVID specification. | pass | Use TLS 1.3 mTLS and short-lived URI-bound Tower identities. |
| E8 | Signed transparency trees support inclusion and consistency proofs without a blockchain. | sourced | Selecting an unnecessary consensus network adds cost without matching the trust goal. | Standards specification. | RFC 9162 Merkle tree and proof model. | pass | Central ledger first; signed checkpoints later. |
| E9 | Signed artifacts and provenance do not attest a modified runtime controlled by a hostile root operator. | sourced + threat-model deduction | Marketing could overstate what package signing proves. | Compare artifact-verification guarantees with runtime threat model. | Sigstore Cosign verification model; local root-adversary assumption. | pass | Claim artifact provenance only; optional TEE tier later. |
| E10 | One joined Tower can meet the proposed 1-vCPU/1-GiB concurrency and latency target. | estimate | Published minimums or scaling claims may be false. | Controlled load, long-stream, max-body, reconnect, and chaos benchmark with raw logs. | Not yet run. | unresolved | Do not publish as a minimum; benchmark in Phase 2. |
| E11 | Keeping one Core ingress while adding worldwide children materially reduces end-user RTT. | hypothesis | The network could be marketed with an unsupported latency benefit. | Multi-region end-to-end benchmark against direct Stations. | Not yet run; topology suggests Core remains the bound. | unresolved | Claim capacity/resilience only; defer latency claim. |
| E12 | Current focused protocol/broker tests pass their existing invariants. | measured | Audit observations might merely reflect an already-failing baseline. | `go test ./internal/protocol ./cmd/rogerai-broker -run 'Test(SignAndVerifyNode|SignBrokerIndependent|BrokerSetFieldsDoNotBreakNodeSig|HashChain|SignVerifyRegistration|OpenSharedStoreUnset|OpenSharedStoreConnect)' -count=1` | Both packages passed on 2026-08-01 and again on 2026-08-02 after final-candidate spec edits. | pass | Treat gaps as missing/wrong invariants, not baseline test breakage. |
| E13 | Core-observed envelopes on a Tower-authenticated session are an acceptable policy basis for Tower compensation attribution. | hypothesis / policy | A shared or proxied Tower key could earn without the expected physical relay deployment. | Adversarial proxy/collusion tests plus business-policy review; never use as a physical-path claim. | Not yet run; cryptography proves key/session attribution only. | unresolved | Use as bounded identity attribution with corroboration and abuse controls, not proof of physical transit. |
| E14 | A 10% share of mature net platform revenue is economically sustainable across real model, payment-fee, refund, and dispute mixes. | founder policy + estimate | Negative-margin traffic or abuse could make the program uneconomic. | Replay historical/synthetic settlement and payment cohorts with sensitivity analysis before compensated beta. | Founder selected initial rate; no cohort simulation yet. | unresolved | Keep centrally versioned and do not enable payout before the Phase-3 gate. |
| E15 | Existing funding/payment data can provide exact immutable source-lot lineage and conserving processor-fee allocation for each settlement. | hypothesis | Compensation could be guessed, non-conserving, or impossible to reconcile. | Real Postgres/payment-event integration tests covering mixed funds, fee corrections, refunds, disputes, and out-of-order delivery. | Not yet tested. | unresolved | P0/P3 schema and reconciliation gate; no positive accrual from ambiguous lineage. |
| E16 | The chosen reversal-maturity window adequately bounds payout risk. | estimate | Late refunds/chargebacks could exceed accrued balances. | Payment-rail rules plus observed reversal-lag distribution and stress tail; record by currency/rail. | Window not yet selected or measured. | unresolved | Keep candidates pending and support append-only clawback; choose threshold before beta. |
| E17 | Separate immutable job settlement and cumulative compensation-delta ledgers conserve money across every crash/order case. | hypothesis | Double accrual, lost clawback, or duplicate payout. | Real dependency property/invariant tests and crash injection at every commit/external-call boundary. | Proposed specs only. | unresolved | Must pass before compensated capability is enabled. |
| E18 | RFC 8785 defines deterministic JSON canonicalization, but strict schema rejection remains an application responsibility. | sourced | Two implementations could sign different bytes or accept ambiguous fields. | Cross-language canonical-byte fixtures plus duplicate/unknown/null/type mutation tests. | RFC 8785 and proposed receipt/control-plane tamper specs. | unresolved | Pin JCS plus the stricter closed schemas before interoperability testing. |
| E19 | RFC 9266 `tls-exporter` can bind an application authority object to one TLS 1.3 connection context. | sourced | A valid lease, grant, or statement could be replayed across a reconnect or peer context. | Two-peer, reconnect, resumption, and role-confusion test matrix using fixed exporter labels and contexts. | RFC 9266 and proposed session-binding scenarios. | unresolved | Disable v1 early data and fence every reconnect with a fresh epoch and binding. |
| E20 | Thirty pilot days, 25 verified operators, 100,000 completed attempts, and a 60-second revocation ceiling are sufficient gates for self-service quarantine enrollment. | estimate / policy | Automation could expand faster than the abuse, support, and revocation controls can contain. | Signed pilot report with independent-account checks, attempt counts, incident ledger, revocation drills, overload results, and fraud-loss budget. | Thresholds proposed in Phase 4; no external pilot yet. | unresolved | Manual admission remains required until every threshold passes and the founder approves automation. |
| E21 | Restricting v1 payouts to an exact purpose-signed zero-withholding decision is an acceptable launch boundary. | policy / estimate | Some otherwise eligible operators or jurisdictions may be unable to receive payouts. | Legal/tax review by supported jurisdiction plus payout cohort analysis. | Proposed fail-closed contract only; no jurisdiction review recorded. | unresolved | Hold affected lots and do not enable nonzero withholding without its own approved remittance contract. |
| E22 | Tax-decision corrections and payout-rail races conserve value and produce one compliant incident outcome at every crash boundary. | hypothesis | Duplicate sends, silent noncompliance, or an incorrectly released operator balance. | Real ledger/rail integration tests with revision, send-fence, crash, success/failure, and retroactive-applicability permutations. | Proposed specs only. | unresolved | Must pass before compensated payouts are enabled. |
| E23 | A signed CompensationLedgerHeadV1 plus selected-state recheck prevents payout from an omitted, stale, or forked compensation prefix. | hypothesis | A valid instruction could reserve or pay atoms not present in the authoritative ledger state. | Persistent SQL property tests, signer outage/recovery, concurrent head/preparation, and fork/staleness mutation tests. | Proposed specs only. | unresolved | Mandatory Phase-3 payout gate; public Merkle proofs remain a later transparency layer. |
| E24 | Derived per-currency `T_N`, `T_C`, and `T_A` controls prevent aggregate compensation across all operators from exceeding eligible program net. | hypothesis | A duplicated/omitted source or base-formula defect could overcompensate the program despite valid operator-local settlements. | Real PostgreSQL invariant/property tests, full replay, corruption injection, overflow boundaries, and concurrent multi-operator revisions. | Closed proposed control/projection specs only; no persistent implementation exists. | unresolved | Mandatory compensated-beta gate; quarantine the currency on any fold mismatch. |
| E25 | A finite threshold/dust lifecycle can preserve every atom without immortal invisible payable remainders. | policy / estimate | Small liabilities can remain unseen forever, be silently erased, or block closure without review. | Select `M` and maximum carry interval from rail economics/legal review, then test every trigger, generation, deadline, hold, and terminal transition. | Exact lifecycle and acyclic dust projection are proposed; threshold/interval values are not selected. | unresolved | No age/admin/de-minimis writeoff in v1; founder/legal approval is required for any new terminal disposition. |
| E26 | A finite provider-specific fee-finality ceiling surfaces permanently unresolved fee authority safely. | policy / estimate | A candidate can remain pending forever or accrue against a guessed fee. | Provider contract/API audit plus timeout, outage, late-revision, and adapter-disable integration tests. | Exact incident contract is proposed; provider support and deadlines are unmeasured. | unresolved | Keep accrual pending, open a durable incident, and disable new compensated allocations for a persistently nonfinal adapter. |
| E27 | Purpose-signed payout eligibility plus a send-fence CAS handles sanctions, identity loss, deletion, and tax changes at every payout boundary. | hypothesis | Funds can be redirected, double-sent, erased, or paid after a pre-fence restriction. | Real ledger/rail crash matrix across preparation, instruction, fence, unknown result, success, failure, authority revision, and closure. | Proposed state/incident/hold specs only. | unresolved | Must pass before any compensated payout rail is enabled. |
| E28 | The preparation → tax decision → signed ledger head → instruction → send-fence order is cryptographically acyclic and crash recoverable. | hypothesis | A signer can attest nonexistent state, a hash cycle can make objects impossible to construct, or recovery can resend. | Canonical-byte fixtures, dependency-graph checks, real PostgreSQL crash injection, signer outages, and ambiguous rail responses. | Proposed one-way preimage and event-order contracts only. | unresolved | No instruction before the head and no rail call before the durable fence. |
| E29 | Program net should be the sum of each settlement's nonnegative `max(0,G-S-F)`, so an unrelated negative-margin job contributes zero rather than reducing another operator's entitlement. | proposed founder policy | Selecting cohort-wide raw margin would change operator economics and historical accrual behavior. | Founder approval plus historical/synthetic cohort comparison of both allocation policies. | Recommended interpretation is encoded in the proposed specs; founder approval remains pending. | unresolved | Treat this exact interpretation as part of the spec approval gate, not an implementation detail. |
| E30 | Immutable half-open application/range objects, affine recovery mappings, total debt priority, and exhaustive authority result sets conserve compensation under arbitrary partial revisions. | hypothesis | Double application, ambiguous debt selection, stranded authority, duplicate debt, or lost liability. | Model/property tests over arbitrary partitions/revisions plus real PostgreSQL serializable concurrency and crash tests. | Closed proposed range/application schemas only. | unresolved | Mandatory before positive compensation or payout implementation. |
| E31 | Canonical value projections and one-way post-commit audit linkage eliminate signed self-reference while reproducing every control/state preimage across implementations. | hypothesis | Honest implementations can disagree on hashes or an object can require its own unknown signature/hash to be constructed. | Cross-language JCS fixtures, dependency DAG validation, exhaustive mutation tests, event-group crash tests, and full-ledger replay. | Closed proposed projection/set/sort contracts only. | unresolved | Mandatory protocol/interoperability gate; current-group complete hashes stay outside their own projection preimages. |
| E32 | The seven 2026-08-02 compensation findings are closed at the behavioral-contract level. | specified invariant | Aggregate overpayment, immortal dust, permanently pending fees, payout after restriction, untestable rate validation, evidence-withholding abuse, or time-of-check ambiguity. | Map every finding to an explicit scenario and parse the complete proposal. | Proposed compensation, payment-authority, settlement, and revenue-share scenarios; Gherkin v26 parsed all 17 files. | specified, not implemented | Keep the implementation gate closed until persistent property/concurrency tests prove each invariant. |
| E33 | Named typed sets, exact projections, and one-way group construction are sufficient to give independent implementations one acyclic money-authority hash graph. | hypothesis | Equivalent-looking implementations could sign different preimages, select a different partial range, omit state, or form an unconstructible cycle. | Cross-language canonical fixtures, schema/mutation generation, dependency-DAG validation, and model-based range tests. | Exact proposed registries and relationship scenarios in `tamper_matrix.feature`; parser success is syntax evidence only. | unresolved | Required interoperability/security test gate before compensated traffic. |
| E34 | Event-local state ownership plus the one prescribed payout-success child bridge preserves balanced journals without falsely assigning child state to the parent event. | hypothesis | A valid-looking group can omit debt/pending state, double-post, or require contradictory affected-state membership. | Generate arbitrary pending sets and partial ranges; compare journal expansion, child result sets, affected-state folds, and full control replay under crashes. | Proposed closed disposition/key/authority tables only. | unresolved | Parent owns the bridge posting; prescribed children own and commit the resolved pending and created debt state. |
| E35 | Named capability, inventory, modality, protocol, offer-state, and directory sets prevent control-plane authority from depending on implementation-chosen collection encodings. | hypothesis | A Tower can exploit parser/order/empty-set differences to expand capability or advertise a different routable inventory under the same intended meaning. | Cross-language JCS fixtures, exhaustive member/field mutations, full/delta model tests, and capability-narrowing property tests. | Exact proposed schemas in `control_plane_tamper_matrix.feature`; no wire implementation exists. | unresolved | Mandatory joined-protocol conformance gate before an external Tower is admitted. |
| E36 | Independent ledger-assigned authority tuples prevent purpose signers from backdating public routing, settlement, or money authority. | hypothesis | A valid signer could make a newly issued object appear to predate revocation, policy, a race winner, or another state transition. | Real PostgreSQL concurrency tests at every attempt, lifecycle/origin, eligibility/tax, compensation-decision, head, instruction, and send boundary plus signer-clock mutation tests. | Exact proposed signed fields, anchor registry, and CAS scenarios only. | unresolved | Mandatory Phase-0/Phase-3 authority-time gate; a missing independent anchor is unverifiable. |
| E37 | The typed enforcement/writeoff graph is constructible and blocks destructive use of opaque evidence, policy versions, approvals, stale state, or overlapping target ranges. | specified invariant / hypothesis | Compensation could be forfeited or debt erased by one privileged signer, an untyped hash, or a racing stale decision. | Dependency-DAG fixtures, cross-language canonical bytes, interval-overlap property tests, input/key rotations, appeal/revocation races, and crash injection at issue/consume. | Proposed policy/finding/approval/target/terms schemas passed independent constructibility review; no implementation exists. | unresolved | Require all typed inputs and both issue/use fences before enabling either destructive event. |
| E38 | A pinned-root, monotonic local authority system can provide standalone trust without accepting RogerAI authority or silently depending on public egress. | hypothesis | Local clients could accept rollback, stale credentials, forged Stations/policy, or accidental public-network behavior. | Two-implementation canonical fixtures, trust/policy/client/Station mutation matrix, backup/restore/rollback tests, packet capture, DNS-rebinding tests, signer outages, and offline recovery drills. | Exact proposed `standalone_jobs.feature` contracts only. | unresolved | Standalone MVP gate; private serving fails closed on missing current local authority. |
| E39 | One universal typed funding-reservation lineage prevents both uncompensated and compensated grants from overspending, relabeling grant credit as cash, or accruing against invented funds. | hypothesis | A tier bypass or stale provenance could spend the same consumer value twice or create operator compensation without received cash. | Real PostgreSQL serializable multi-grant tests, cash/grant provenance mutations, settle/release crash injection, compromise cutoffs, and full lineage replay. | Exact proposed funding source/reservation/transition schemas only. | unresolved | Mandatory before any new public execution-grant implementation. |
| E40 | Barring the compensated capability on outstanding operator debt, plus a rolling reserve and a per-operator unmatured exposure cap, bounds maximum loss to an operator who accrues, takes payout, and abandons. | proposed invariant | Offset-only recourse against future compensation is uncollectable once an operator stops trading, so v1 could pay out money it can never recover. | Adversarial scenarios: accrue-payout-abandon; re-enroll under fresh keys; reserve release acceleration; over-cap accrual; cap lowered after accrual. | `operator_revenue_share.feature` debt-follows-operator, rolling-reserve, and exposure-cap scenarios added 2026-08-02. | unresolved | Reserve percentage and cap value must be set by signed policy and validated against real chargeback data before public beta. |
| E41 | An authenticated post-success rail return is a distinct ledger transition from a negative entitlement adjustment. | measured defect, now specified | Encoding a return as an adjustment bills an operator for cash the platform still holds, and understates cash on hand. | Classification outline over confirmed failure, success, return, adjustment, and unauthenticated advice; partial, replayed, and debt-covered returns. | `compensation_state_machines.feature` rail-return scenarios added 2026-08-02; prior closed two-value result set had no return. | pass | Distinct transition restoring entitlement with zero debt. |
| E42 | Payout instructions must tolerate a rail that deducts its own fee. | measured defect, now specified | Requiring gross to equal net deadlocks every fee-charging rail into permanent "result unknown, no second instruction". | Instruction arithmetic with a bound `rail_fee_minor` and a closed platform-or-operator bearer. | `operator_revenue_share.feature` payout-instruction conservation updated 2026-08-02. | pass | Gross equals net plus operator-borne fee; platform-borne fee posts to expense. |
| E43 | Processor fees are not all proportional to principal. | measured defect, now specified | Prorating a flat dispute fee across principal spreads one event's cost onto unrelated settlements and silently absorbs any excess. | Fee-kind outline; flat fee assigned whole to its causing interval; excess to platform expense; missing kind fails closed. | `operator_revenue_share.feature` fee-kind scenarios added 2026-08-02. | pass | Allocation is selected by declared closed fee kind. |
| E44 | Self-dealing detection keyed on identity equality is defeated by three distinct verified identities. | proposed invariant | A collusion ring earns the share indefinitely while satisfying every same-party check. | Linkage-evidence outline over shared destination, instrument, beneficial owner, business/tax identity, postal identity, device credential, declared affiliation; shared anomaly budget. | `operator_revenue_share.feature` linked-entity scenarios added 2026-08-02. | unresolved | Linkage signal quality and false-positive rate are unmeasured; needs pilot data. |
| E45 | The specification set is not implementable at 97.7% distinct step phrasings without a vocabulary unification pass. | measured | A suite requiring roughly 50,000 lines of step-definition glue will not be written, so exhaustive specs would produce zero executable coverage. | Counted 3,963 steps, 3,872 distinct after normalizing quoted strings, placeholders, and integers. | Measured on the 2026-08-02 tree. | fail | Unification pass and field-table conversion are gates before step definitions. |
| E46 | Load-bearing vocabulary used hundreds of times was defined nowhere, permitting incompatible implementations. | measured, now specified | "complete hash" (600+ uses) was ambiguous as to whether it covers the signature; either reading yields a different, non-interoperable system. | Executable definition scenarios pinning each term. | `glossary.feature` added 2026-08-02. | pass | Glossary is the normative source; six fact heads fixed by construction. |

## Standards references

- TLS 1.3: <https://www.rfc-editor.org/rfc/rfc8446>
- TLS 1.3 channel binding (`tls-exporter`): <https://www.rfc-editor.org/rfc/rfc9266>
- JSON Canonicalization Scheme: <https://www.rfc-editor.org/rfc/rfc8785>
- SPIFFE X.509-SVID: <https://spiffe.io/docs/latest/spiffe-specs/x509-svid/>
- Certificate Transparency v2 Merkle proofs: <https://www.rfc-editor.org/rfc/rfc9162>
- Sigstore/Cosign verification: <https://docs.sigstore.dev/cosign/verifying/verify/>
- The Update Framework threat model and metadata roles:
  <https://theupdateframework.io/docs/overview/> and
  <https://theupdateframework.io/docs/metadata/>
