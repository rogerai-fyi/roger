# APPROVED SPEC - founder approved 2026-08-03. Changes to an approved scenario need
# re-approval; they are not a diff to be reviewed.
#
# BUILD STATUS: PARTIAL. Approval is not implementation - this line says which.
# Enforced by internal/towercore/featurestatus_test.go against the "Contract:"
# references in the code. Changing the status without changing the code fails.
#
# Scope: operator-facing Station invite/attach, proof of assertion and inner-TLS keys,
# Core admission/origin lease, Tower-local bridge credentials, certificate lifecycle,
# detach, revocation, and rehome for joined and standalone Towers.

# SUPERSEDED MECHANISM NOTE (not a spec change): the invite-file CLI this spec's scenarios
# ride (`roger-tower station invite` / `station attach`, the roger-station binary) was
# RETIRED with the leaf-station generation. Its living successor is SELF-ATTACH: one signed
# `/tower/edge/attach` call from `roger share --tower`, which reuses this spec's admission
# machinery (authorization + one-use redemption + uniqueness + caps) atomically in-process.
# The trust properties specified here are enforced there; the ceremony that carried them is
# gone. Revocation and promotion remain as specified.
Feature: A Tower can transport only a Station that proved its own keys and exact origin authority
  Tower-local access is not public Station authority. Joined service requires Roger Core's
  owner-bound admission and an end-to-end Station TLS identity the Tower cannot mint.

  Scenario Outline: The operator starts an explicit Station invitation
    Given a Tower is "<mode>" and its administrator is freshly authenticated
    When `roger-tower station invite` is requested for Station assertion key A and secure-session key K
    Then it creates "<authority>"
    And the invitation binds Tower ID, network ID, intended Station ID or new-Station request, A, K, role, capability ceiling, one-use nonce, issue/expiry, and inviter authorization
    And plaintext invitation material is shown once and stored only as a secret-safe verifier

    Examples:
      | mode | authority |
      | joined | a browser/account flow whose resulting StationAttachAuthorizationV1 is signed by Roger Core and bound to the verified Station owner and this Tower |
      | standalone | one strict one-use LocalStationAttachAuthorizationV1 signed for the current local trust/policy heads after consuming a matching LocalAdminAuthorizationV1 |

  Scenario: A Station proves both independent private keys during attachment
    Given a fresh exact invitation bound to assertion key A and secure-session key K
    When the Station attaches over a Tower-local TLS channel
    Then it signs the fresh attachment challenge with A
    And proves possession of K through the exact CSR/TLS proof bound by the challenge
    And proof transcripts bind the invitation, Tower, Station, network, keys, protocol, and expiry
    And possession of only A, only K, a bridge credential, or the Tower key cannot complete attachment

  Scenario: Joined attachment commits Core authority before local bridge access
    Given a joined Station completes exact key proof and owner authorization
    When Roger Core admits the attachment
    Then one transaction consumes StationAttachAuthorizationV1 and records Station ID, owner, assertion key, secure-session key, Tower origin, new Station origin epoch, StationCapabilityCeilingSetV1 complete hash, and policy revision
    And Roger Core signs initial_attach StationOriginLeaseRevisionAuthorityV1 followed by StationOriginLeaseV1, appends revision-1 active StationLifecycleEventV1, and issues a short-lived Station secure-session certificate for K in one atomic authority group
    And only after that commit may the Tower issue a Tower-local bridge credential bound to the same Station, Tower, origin epoch, and local TLS key
    And the Station is quarantine inventory until central probes and policy make its offer eligible

  Scenario: A public direct Station has an exact Tower-absent origin authority
    Given a verified Station owner requests direct public admission without a Tower
    When Roger Core accepts the same independent assertion-key and secure-session-key proofs
    Then StationAttachAuthorizationV1 has direct origin kind and canonical Tower-ID absence
    And one transaction consumes it, signs revision-1 DirectStationOriginAuthorityV1, appends revision-1 active StationLifecycleEventV1, and issues the bound direct Station certificate
    And no joined StationOriginLeaseV1, Tower bridge credential, Tower capability, Tower identity, or joined-origin field is inferred

  Scenario: Standalone attachment creates no RogerAI authority
    Given a standalone Station completes exact local key proof and invitation
    When the local transaction commits
    Then one transaction consumes LocalStationAttachAuthorizationV1 and commits revision-1 current LocalStationOriginAuthorityV1 with its local owner, independent assertion and secure-session keys, origin/assertion epochs, LocalStationCapabilityCeilingV1, and distinct-online-issuer local Station certificate
    And it may issue a transport-only Tower-local bridge credential under distinct bridge-authority and bridge-certificate keys, never the pinned offline root or local Station-certificate issuer
    And no RogerAI Station ID, certificate, owner authorization, origin lease, directory entry, or network call exists

  Scenario Outline: Invalid attachment creates no partial Station authority
    Given an attachment has "<defect>"
    When Tower and authority validation run
    Then no invitation is successfully consumed, certificate or bridge credential is issued, origin changes, or inventory leaf becomes routable
    And a bounded redacted failure is recorded

    Examples:
      | defect |
      | missing, unknown, expired, revoked, or consumed invitation |
      | invitation for another Tower, network, Station, owner, or role |
      | assertion challenge missing, altered, replayed, or signed by another key |
      | CSR missing, altered, or for another secure-session key |
      | one private key reused for assertion and secure-session purposes |
      | Station ID already bound to another assertion key |
      | secure-session key already bound to another Station |
      | owner suspended, quota exceeded, or terms stale |
      | Tower not admitted for the requested capability |
      | unsupported protocol, cipher, signature suite, or software version |
      | capability request above the invitation ceiling |
      | a standalone authorization presented to joined mode or the reverse |
      | invalid canonical encoding, unknown field, duplicate field, or oversized proof |

  Scenario: Concurrent attachment consumes one authorization once
    Given two processes race with one valid invitation and identical key proofs
    When the authority transaction serializes them
    Then one Station origin and credential outcome commits
    And exact response-loss retry after fresh key proof returns that outcome
    And divergent reuse is rejected without another origin, certificate, or credential

  # --- signed attachment objects -------------------------------------------

  Scenario: StationAttachAuthorizationV1 exhaustive tamper Cartesian product
    Given its signed fields are schema, network, protocol, station-admission signer, authorization ID, owner ID, direct or joined origin kind, Tower ID or canonical direct absence, exact preallocated Station ID, assertion key ID/hash, secure-session key ID/hash, StationCapabilityCeilingSetV1 complete hash owned by that owner/Station, software floor, terms/policy versions, nonce, issue time, expiry time, and independently assigned Core authorization-ledger commit time/global sequence
    When each field is independently replaced, removed, null, duplicated equally, duplicated conflictingly, or retyped while retaining the signature
    Then every pair is rejected before key proof or authorization consumption
    And an unknown field or wrong-purpose signature is rejected
    And authorization commit is no earlier than issue and strictly before expiry, its purpose key is valid at that independently assigned tuple, and consumption CASes the unconsumed authorization at its current ledger head

  Scenario: StationAttachAuthorizationV1 origin presence is closed
    Given one StationAttachAuthorizationV1 is decoded
    Then joined requires exactly one admitted Tower ID and direct requires canonical Tower-ID absence
    And standalone authorization is LocalStationAttachAuthorizationV1 under its pinned LocalTrustPublicationV1 and LocalPolicyV1 heads rather than either public variant
    And an unknown origin, joined absence, direct presence, null/empty Tower, or conversion between variants is rejected before consumption

  Scenario Outline: DirectStationOriginAuthorityV1 has one exact revision source
    Given a direct public Station origin change has kind "<kind>"
    Then DirectStationOriginAuthorityV1 contains only schema/network/protocol, station-admission signer key ID, stable authority-series ID, positive revision, previous DirectStationOriginAuthorityV1 complete hash or canonical first-revision absence, Station/owner IDs, fixed direct origin kind and canonical Tower absence, Station origin epoch, Station assertion epoch, assertion key ID, secure-session key ID/public-key hash, certificate serial, retiring certificate serial or canonical absence, closed no_overlap or finish_authenticated overlap disposition, already-authenticated-session finish-cutoff Core tuple or canonical no-overlap absence, StationCapabilityCeilingSetV1 complete hash, software/policy versions, issue/not-before/expiry Core tuples, independently assigned Core origin-ledger commit time/global sequence/group index, closed kind, and exactly "<source>"
    And every foreign source field is canonically absent

    Examples:
      | kind | source |
      | initial_attach | consumed direct StationAttachAuthorizationV1 ID/complete hash; revision/origin/assertion epochs are 1, prior/retiring certificate and cutoff are absent with no_overlap, and owner/Station/keys plus StationCapabilityCeilingSetV1 complete hash are copied byte-identically from that authorization |
      | same_key_renewal | renewal challenge ID/nonce/issue/expiry, CSR hash, prior authenticated session binding, unchanged epochs/keys/ceiling, replacement certificate serial, retiring prior serial, finish_authenticated disposition, and bounded cutoff |
      | secure_key_rotation | owner rotation authorization hash, current assertion-key proof, replacement CSR hash, unchanged epochs/assertion/ceiling, replacement secure-session key/certificate, and signed planned finish_authenticated or compromise no_overlap disposition/cutoff shape |
      | epoch_reset | StationEpochResetV1 ID/complete hash, unchanged origin epoch, exact next assertion epoch, replacement assertion/secure-session keys, replacement certificate, unchanged ceiling, retiring prior serial, and no_overlap with canonical cutoff absence |

  Scenario: DirectStationOriginAuthorityV1 has one current chain and bounded credential overlap
    Given a direct Station has current authority revision R and complete hash H
    When initial admission, renewal, secure-key rotation, or epoch reset commits
    Then stable authority-series ID is the fixed-length unpadded case-preserving base64url SHA-256 digest over the UTF-8 bytes of strict JCS [DirectStationOriginAuthorityV1-series-v1,network-ID,Station-ID]
    And creation is revision 1/prior absence and every later authority preserves that series ID and is exactly R plus one with prior H; one current-head CAS at the independently assigned origin-ledger commit tuple wins, not-before is no earlier than commit, key status derives from commit, and exact replay is idempotent
    And new offers, sessions, and grants bind the current revision/hash; a retiring certificate may finish only already-authenticated sessions through its signed overlap cutoff and cannot open a new session after head replacement
    And finish_authenticated requires a retiring serial different from the replacement, a cutoff after the new authority tuple and no later than both the retiring certificate expiry and policy maximum, and only a session authenticated under that serial before head replacement may finish through the cutoff; no_overlap requires canonical cutoff absence and, when a retiring serial is present, immediately forbids that serial from any further session use
    And initial_attach copies the consumed authorization ceiling byte-identically and every later revision preserves it byte-identically; a gap, overflow, fork, stale prior, key/certificate mismatch, Tower presence, changed/re-encoded ceiling, or source-kind mismatch rejects the authority before serving

  Scenario: StationOriginLeaseV1 exhaustive tamper Cartesian product
    Given its signed fields are schema, network, protocol, station-admission signer, stable origin-lease series ID, positive revision, prior lease complete hash or canonical first-revision absence, StationOriginLeaseRevisionAuthorityV1 ID/complete hash, Station ID, owner ID, assertion key ID, secure-session key ID, Tower ID, Station origin epoch, Station assertion epoch, StationCapabilityCeilingSetV1 complete hash copied byte-identically from that authority, policy versions, certificate serial, retiring certificate serial or canonical absence, no_overlap or finish_authenticated disposition, already-authenticated-session finish-cutoff Core tuple or canonical no-overlap absence, issue/not-before/expiry times, independently assigned Core origin-ledger commit time/global sequence/group index, and lease sequence
    When each field is independently replaced, removed, null, duplicated equally, duplicated conflictingly, or retyped while retaining the signature
    Then every pair is rejected before inner-session admission, inventory, or routing
    And an unknown field or wrong-purpose signature is rejected

  Scenario Outline: StationOriginLeaseRevisionAuthorityV1 has one closed signed shape
    Given Roger Core authorizes origin-lease change kind "<kind>"
    Then StationOriginLeaseRevisionAuthorityV1 contains only schema/network/protocol, station-admission signer key ID, authority ID, closed kind, Station/owner IDs, resulting stable StationOriginLeaseV1 series ID/exact revision, prior StationOriginLeaseV1 stable series ID/revision/complete hash or canonical initial absence, prior/new Tower IDs or canonical kind-specific absence, prior/new Station origin epochs, prior/new Station assertion epochs, prior/new assertion and secure-session key IDs, prior/replacement certificate serials, closed no_overlap or finish_authenticated disposition, already-authenticated-session finish-cutoff Core tuple or canonical no-overlap absence, StationCapabilityCeilingSetV1 complete hash, software/policy versions, issue/expiry Core tuples, independently assigned Core origin-ledger commit time/global sequence/group index, and exactly the kind-specific source fields in "<source>"
    And every foreign kind-specific source field is canonically absent

    Examples:
      | kind | source |
      | initial_attach | consumed joined StationAttachAuthorizationV1 ID/complete hash; prior lease/Tower/epochs/keys/certificate are canonically absent, both epochs start at 1, no_overlap has canonical cutoff absence, and the ceiling is copied byte-identically from that authorization |
      | same_key_renewal | renewal challenge ID/nonce/issue/expiry, exact CSR DER hash, authenticated prior inner-session channel-binding hash, unchanged owner/Tower/origin epoch/assertion epoch/keys/ceiling fields, retiring prior serial, finish_authenticated disposition, and bounded cutoff |
      | secure_key_rotation | rotation challenge ID/nonce/issue/expiry, owner-authorization event ID/complete hash, current assertion-key proof hash, replacement CSR DER hash, unchanged owner/Tower/origin epoch/assertion epoch/assertion key/ceiling, changed secure-session key/certificate, and signed planned finish_authenticated or compromise no_overlap disposition/cutoff shape |
      | rehome | StationRehomeLeaseV1 ID/complete hash, fenced prior Tower/origin tuple, exact new Tower/higher origin epoch, unchanged assertion epoch/keys/certificate, both key proofs, byte-identically unchanged ceiling, continued assertion-chain head, and no_overlap with canonical cutoff absence because no certificate changes |
      | epoch_reset | StationEpochResetV1 ID/complete hash, unchanged owner/Tower/origin epoch, exact next assertion epoch and replacement keys/certificate, byte-identically unchanged ceiling, and no_overlap with canonical cutoff absence; any Tower change requires separate rehome authority |

  Scenario: Station origin lease revisions and capability ceiling are exact
    Given one StationOriginLeaseV1 is issued from StationOriginLeaseRevisionAuthorityV1
    When its revision chain and capability relationship are checked
    Then stable series ID is the fixed-length unpadded case-preserving base64url SHA-256 digest over the UTF-8 bytes of strict JCS [StationOriginLeaseV1-series-v1,network-ID,Station-ID]
    And a first lease has revision 1, lease sequence 1, and canonical prior-lease absence, while every later lease preserves that series ID, increments both bounded integers by one, and binds the immediately prior complete hash
    And the revision authority prescribes that exact resulting series ID/revision, one current-head CAS advances the series at its independently assigned origin-ledger tuple, authority and lease copy the transaction tuple with ordered group indices, not-before is no earlier than commit, key status derives from commit, exact replay is idempotent, and competing bytes for one next revision are a fork
    And every copied Station/owner/Tower/origin/key/certificate/ceiling/policy field is byte-identical to that exact authority, whose kind matches the initiating flow
    And finish_authenticated requires a retiring serial different from the replacement, a cutoff after the new authority tuple and no later than both the retiring certificate expiry and policy maximum, and only a session authenticated under that serial before head replacement may finish through the cutoff; no_overlap requires canonical cutoff absence and, when a retiring serial is present, immediately forbids that serial from any further session use
    And the revision authority, resulting origin lease, StationOfferV1, certificate/session admission, and ExecutionGrantV1 have byte-identical Station/owner/joined-origin/Tower/origin-epoch/assertion-epoch/assertion-key/secure-key/certificate fields wherever their closed schemas duplicate them
    And initial_attach copies the consumed StationAttachAuthorizationV1 ceiling; every later authority and lease preserve that exact complete hash byte-identically, while every StationCapabilitySetV1 offer may be an equal-or-narrower subset of the ceiling
    And a zero, skipped, overflowed, stale, forked, changed-series, differently owned, expanded, differently encoded, or reconstructed ceiling rejects the lease or offer before routing

  Scenario: Origin-lease revision authority is mutation-exhaustive
    Given one valid StationOriginLeaseRevisionAuthorityV1 and its kind-specific source are fixed
    When each common or kind-specific field is independently replaced, removed, null, duplicated equally, duplicated conflictingly, or retyped while retaining its signature
    Then strict decoding, source relationship, ceiling preservation, or signature verification fails before a lease or certificate is issued
    And an unknown kind/field, two source variants, a stale proof/challenge, expanded capability, or authority replay for another lease revision is rejected

  Scenario: Station origin kind is immutable in v1
    Given a public Station was initially admitted as direct or joined
    When an owner requests direct-to-joined or joined-to-direct migration
    Then no DirectStationOriginAuthorityV1, StationOriginLeaseRevisionAuthorityV1, or StationRehomeLeaseV1 variant can authorize that cross-kind change
    And the old Station ID must reach terminal revoked state before a fresh StationAttachAuthorizationV1 may allocate a different Station ID under the other origin kind
    And no assertion head, offer, lease, credential, earning lineage, capacity, or held compensation transfers implicitly between those identities

  # --- local bridge scope ---------------------------------------------------

  Scenario: A Station proves a third purpose-separated Tower-local bridge key
    Given public assertion key A and end-to-end secure-session key K already passed attachment proof
    When the Station requests a Tower-local bridge credential for bridge TLS key B
    Then it proves possession of B over a fresh challenge binding network, Tower-local namespace, Tower, Station, current origin authority, A, K, B, requested listener role, and expiry
    And B is distinct from A, K, every Tower key, and every issuer key
    And failure or replay of this transport proof changes no Station origin, public authority, or bridge head

  Scenario: TowerLocalStationBridgeCredentialV1 has one exact current transport-only authority
    Given a Station has proved bridge TLS key B against its current joined or standalone origin authority
    When the Tower-local bridge-authority signer commits a credential revision
    Then TowerLocalStationBridgeCredentialV1 contains only schema/protocol, closed joined_tower_local or standalone_local namespace kind, public network plus Tower-local namespace IDs or standalone local-network ID with the foreign network fields canonically absent, Tower-local bridge-authority signer key ID, Tower-local bridge-certificate issuer key ID, deterministic bridge-series ID, positive revision, immediate prior complete hash or canonical first absence, active or revoked state, Tower ID or standalone local-Tower ID, Station/owner IDs, assertion key ID, secure-session key ID, bridge TLS key ID/algorithm/canonical public-key hash, listener role, exact current StationOriginLeaseV1 ID/revision/complete hash or LocalStationOriginAuthorityV1 series/revision/complete hash selected by namespace with the foreign authority absent, Station origin epoch, unique credential serial, certificate complete hash, immutable certificate-issuance bridge revision/commit tuple, retiring serial or canonical absence, no_overlap or finish_authenticated disposition and bounded cutoff or canonical absence, issue/not-before/expiry tuples, independently assigned Tower-local bridge-ledger commit time/global sequence, closed initial_issue, same_key_renewal, bridge_key_rotation, origin_rebind, or revoke change kind, and its exact proof/prior-origin/revocation source hash
    And bridge-series ID derives from strict JCS [TowerLocalStationBridgeCredentialV1-series-v1,namespace-kind,network-or-local-network-ID,Tower-ID,Station-ID], revision 1 has prior absence, and each successor is exactly current revision plus one/immediate prior hash under one current-head CAS
    And the certificate signs namespace/network/Tower/Station/owner/B/listener-role/serial/bridge-series/immutable certificate-issuance revision and tuple/validity without the containing authority hash, while the authority binds the certificate complete hash one-way
    And every revision requires the bridge-authority signer current at commit; initial_issue, same_key_renewal, bridge_key_rotation, and origin_rebind additionally produce a certificate whose issuance revision/tuple equal that authority revision/commit and require the distinct bridge-certificate issuer current, while revoke retains the prior certificate-issuer ID, serial, hash, issuance revision/tuple, bridge key, listener role, and certificate validity byte-identically and needs no certificate issuance
    And standalone resolves those signers only as the distinct local_station_bridge_authority and local_station_bridge_certificate purposes in the exact current LocalTrustPublicationV1; joined resolves two distinct Tower-locally pinned keys that can never resolve through RogerTrustDocumentV1
    And commit compare-and-swaps the exact current origin and bridge heads, authority not-before is no earlier than bridge commit, authority expiry is no later than origin and bridge-authority-signer validity and certificate-producing expiry is also no later than bridge-certificate-issuer validity, exact replay is idempotent, and new connections require the exact current active head and B proof
    And an origin change, detach, revocation, or bridge-key change advances/revokes the same head; either bridge key's compromise, an origin head mismatch, or required-key removal independently blocks every new connection even if no bridge successor can be signed, finish_authenticated can finish only sessions authenticated before its cutoff, and no_overlap rejects them immediately
    And unknown fields, wrong namespace/root/origin/Tower/Station/key/role, stale or skipped revision, duplicate serial, invalid overlap, signer clock, or a public-purpose signature is rejected before bridge access

  Scenario: A Tower-local bridge credential has transport scope only
    Given a Station has a valid current TowerLocalStationBridgeCredentialV1 and proves its exact bridge TLS key
    When it authenticates to the Tower listener
    Then the credential is valid only for its namespace, network, Station, Tower, distinct bridge-authority and certificate-issuer purposes, current origin authority/epoch, bridge key, listener role, serial, state, and bounded validity interval
    And every relayed payload remains authenticated under the exact origin-bound secure-session key K, inner-session epoch/channel-binding hash, and assertion key A; bridge key B supplies only the outer listener transport and grants no payload identity or content authority
    And it cannot sign ProviderAssertionV2 or LocalProviderAssertionV1, open or terminate a Core or standalone Station inner secure session, decrypt, alter, inject, replay, or impersonate an execution grant/request/result, change inventory claims, act as another Station, enroll a Tower, call Core, settle, or earn

  Scenario Outline: Bridge credential misuse is rejected locally and centrally powerless
    Given a bridge credential is "<condition>"
    When it is presented
    Then local authentication fails or remains limited to its exact recorded bridge scope
    And Roger Core never accepts it as public authority

    Examples:
      | condition |
      | copied without its local TLS private key |
      | expired or revoked |
      | from another Tower or Station |
      | from an old origin epoch |
      | for another listener role |
      | replayed after replacement |
      | minted by a compromised Tower-local CA without matching Core inner identity |

  Scenario: Bridge rotation has a bounded overlap and fences the old credential
    Given an attached Station requests bridge rotation over its authenticated local channel
    When it proves its assertion key and new local TLS key under the current origin epoch
    Then one replacement credential is issued with a new serial and bounded overlap
    And after overlap the old serial cannot poll, receive a stream, refresh inventory, or recover by reconnecting
    And concurrent exact rotation retry returns the one replacement outcome

  # --- Station inner certificate lifecycle --------------------------------

  Scenario: Station inner-session certificate issuance binds owner, origin, and both keys
    Given joined attachment or renewal is authorized
    When Roger Core's Station certificate issuer signs the CSR for K
    Then the certificate has the exact Station URI identity, secure-session public key, network, Station-session key usage, serial, not-before/not-after, and policy constraints
    And StationOriginLeaseV1 binds that serial plus the assertion and secure-session key identities
    And it conveys no Tower, settlement, grant-signing, admin, or certificate-issuing authority

  Scenario: Routine same-key Station certificate renewal is bounded and idempotent
    Given an active joined Station has current certificate serial S for secure-session key K, an unexpired origin lease, and a fresh renewal challenge
    When it proves the current inner session, assertion key, K through the exact CSR, owner/origin authority, and accepted software/policy versions
    Then one transaction consumes the renewal challenge, commits same_key_renewal StationOriginLeaseRevisionAuthorityV1 and the next StationOriginLeaseV1, and issues one replacement serial S2 for K with a new bounded validity interval
    And S and S2 overlap only for the centrally signed rotation interval while every grant names the exact selected serial
    And S cannot open a new inner session after that overlap or be restored by reconnect, clock rollback, or stale lease
    And an exact response-loss retry or concurrent identical CSR returns S2 without another serial or validity extension
    And a concurrent divergent CSR, nonce, key, origin, or requested scope is rejected without changing S or S2

  Scenario Outline: Station certificate renewal fails safely
    Given an attached Station requests renewal with "<condition>"
    When Roger Core validates it over the current authenticated inner session
    Then no replacement certificate or extended authority is issued
    And the current certificate receives no extra lifetime or scope

    Examples:
      | condition |
      | no current authenticated inner session |
      | wrong assertion-key proof |
      | replayed or expired renewal nonce |
      | CSR for another secure-session key without an authorized key rotation |
      | owner, Station, Tower, or origin suspended/revoked |
      | origin lease stale or expired |
      | unsupported Station software or protocol |
      | requested capability or key-usage expansion |
      | concurrent divergent renewal CSR |

  Scenario: Station secure-session key rotation is separately authorized
    Given an attached Station needs to replace secure-session key K with K2
    When its owner and current assertion key authorize a fresh rotation challenge and K2 CSR
    Then Roger Core commits secure_key_rotation StationOriginLeaseRevisionAuthorityV1, the next StationOriginLeaseV1, and a new key version/certificate with bounded overlap under the same origin epoch
    And grants identify the exact selected certificate serial and session binding
    And K stops opening new sessions after overlap while historical evidence retains its Core-observed validity anchor

  Scenario: Station key compromise applies an authoritative cutoff
    Given assertion key or secure-session key compromise is reported and verified
    When Roger Core signs exact next-revision StationLifecycleEventV1 with state epoch_closed, the compromised key/origin/lease snapshot, security evidence, and cancel_at(C)
    Then no new offer, certificate, session, lease, grant, or bridge rotation is authorized
    And incomplete attempts at or after C are cancelled and holds released once
    And pre-cutoff complete evidence and later recourse follow the approved lifecycle rules
    And serving again requires StationEpochResetV1 plus its same-transaction replacement active StationLifecycleEventV1 rather than a new key alone

  # --- detach, revoke, and rehome -------------------------------------------

  Scenario: Detach fences local transport before removing the origin
    Given an attached Station is detached by its owner or authorized Tower administrator
    When the detach transaction commits
    Then the Tower stops new local streams and revokes bridge credentials
    And joined mode asks Roger Core to drain or cancel the Station origin under signed policy
    And inventory expiry/removal cannot erase historical offers, attempts, assertions, or receipts

  Scenario: A Tower cannot detach or rehome a Station it does not own
    Given a Tower operator names a Station whose owner did not authorize the action
    When detach, key rotation, epoch reset, or rehome is requested
    Then Roger Core rejects it before origin or credential mutation
    And Tower-local credential control does not become Station-owner authority

  Scenario: Rehome creates a new origin only after the old one is fenced
    Given owner-authorized Station S is active behind Tower A
    When Roger Core commits exact StationRehomeLeaseV1 for Tower B and a higher origin epoch
    Then A's new-work lease and bridge credentials are fenced before B becomes eligible
    And S proves both keys through B, Core commits rehome StationOriginLeaseRevisionAuthorityV1 and the next StationOriginLeaseV1, and S receives a B-scoped bridge credential
    And old-origin sessions, offers, results, and credentials cannot act in the new epoch

  Scenario: Attachment-state failure blocks serving
    Given the Tower or Roger Core cannot durably read invitation use, Station keys, owner authorization, origin lease, certificate, bridge serial, or epoch fence
    When attachment, renewal, inventory, or a job is attempted
    Then no new credential, offer, session, or grant is accepted
    And readiness for that Station is false until authoritative state recovers

  Scenario: Station attachment secrets are absent from observability and support artifacts
    Given invite, proof, certificate, bridge, rotation, revocation, and rehome paths run through success and failure
    When logs, metrics, traces, status, doctor, config output, and support bundles are inspected
    Then plaintext invite codes, private keys, bridge credentials, CSR private material, and owner bearer credentials are absent
    And only bounded public IDs, key fingerprints, serials, versions, states, and redacted reasons appear
