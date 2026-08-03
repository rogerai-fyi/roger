# PROPOSED SPEC — founder approval is required before step definitions or implementation.
#
# Scope: exhaustive field/operator tamper matrices for Tower enrollment, admission,
# inventory, Station offers, lifecycle, rehoming, public directory, trust metadata, and
# transparency checkpoints. Standard X.509 path validation remains in
# public_enrollment.feature; release metadata validation remains in packaging.feature.

Feature: Every signed Tower control-plane object has one strict canonical authority
  A valid key cannot lend authority to a changed, ambiguous, replayed, or cross-purpose
  control-plane object.

  Background:
    Given strict decoding rejects unknown fields, trailing bytes, duplicate fields, invalid Unicode, non-canonical numbers or timestamps, and explicit null where absence is canonical
    And the universal post-signing mutations are:
      | mutation |
      | replace with a different valid in-range value of the same semantic type |
      | remove the field |
      | encode the field as explicit null |
      | duplicate the field with the same encoded value |
      | duplicate the field with a conflicting encoded value |
      | encode the field using a different wire type |
    And each object's signature slot alone is excluded from its canonical signing bytes

  Scenario Outline: Every control-plane collection has one exact canonical preimage
    Given control collection "<set>" is owned by "<owner>" with scope fields "<scope>"
    When its complete hash is computed
    Then its strict JCS object contains only schema version, network ID, protocol version, exact set-kind tag, the listed owner fields, the listed scope fields, canonical nonnegative member count, and one complete member array
    And each member contains only "<member fields>"
    And members are ordered by "<order>" and satisfy "<contract>"
    And count equals array length, duplicate keys or bytes are rejected, and empty is permitted only where the contract explicitly permits it
    And an unknown field, explicit null, omitted or extra member, alternate member order, alternate empty form, noncanonical integer, or hash in place of a complete member changes the complete hash and rejects the containing signed object

    Examples:
      | set | owner | scope | member fields | order | contract |
      | RequestedTowerCapabilitySetV1 | TowerEnrollmentProofV1 enrollment transaction ID | one-time token ID, operator ID, Tower ID, proof protocol version, software version | registered Tower capability ID, capability revision, boolean or bounded-uint64 value-kind tag, exactly its one typed value, and canonical absence of the foreign value | bytewise capability ID then unsigned revision | nonempty unique IDs; every ID/revision/value is allowed by the token and protocol-version capability registry |
      | AdmittedTowerCapabilitySetV1 | TowerAdmissionLeaseV1 lease ID | lease sequence, Tower ID, operator ID, policy version, minimum software version | registered Tower capability ID, capability revision, boolean or bounded-uint64 value-kind tag, exactly its one typed value, and canonical absence of the foreign value | bytewise capability ID then unsigned revision | nonempty unique IDs; membership is a policy-approved subset of the requested set and no boolean or numeric value expands the request |
      | TowerDeclaredCapabilitySetV1 | Tower ID and inventory revision | certificate serial, session epoch, channel-binding hash, software version | registered Tower capability ID, capability revision, boolean or bounded-uint64 value-kind tag, exactly its one typed value, and canonical absence of the foreign value | bytewise capability ID then unsigned revision | nonempty unique IDs; membership and values are a runtime-supported subset of the current admitted set and cannot expand it |
      | TowerInventoryOperationSetV1 | Tower ID and inventory revision | full or delta kind and base revision or canonical full absence | zero-based ordinal, present or add or replace or remove kind, Station ID, offer ID, prior StationOfferV1 complete hash or canonical absence, and new StationOfferV1 complete hash or canonical absence | bytewise Station ID then offer ID, with ordinal equal to array index | full permits empty and uses only present with prior absence and new presence; delta is nonempty and uses add with only new, replace with both, or remove with only prior; targets are unique |
      | StationOfferStateSetV1 | Tower ID and inventory revision | certificate serial, session epoch, channel-binding hash | Station ID, offer ID, offer revision, Station origin epoch, and StationOfferV1 complete hash | bytewise Station ID then offer ID | permits empty; membership is exactly the post-operation live offer state, IDs are unique, and count equals TowerInventoryV1 resulting Station count |
      | StationEpochClosureEvidenceSetV1 | StationLifecycleEventV1 lifecycle event ID | Station ID, owner ID, Station assertion epoch, prior terminal assertion-head hash, closure reason, and review-deadline Core tuple | bounded zero-based ordinal, observed or missing or fork_claim kind, assertion sequence, ProviderAssertionV2 complete hash or canonical missing absence, claimed previous assertion hash or canonical first/missing absence, closed adjudication status, and Core-observed tuple or canonical missing absence | unsigned assertion sequence, closed kind order, then bytewise assertion hash/absence, with ordinal equal to array index | nonempty and bounded; membership equals every accepted, buffered, explicitly missing, and fork-claim item needed to close that assertion epoch, ordinals are contiguous from zero, no sequence claim is omitted, and total is not_applicable |
      | StationCapabilitySetV1 | Station ID, offer ID, and offer revision | Station origin epoch, direct or joined origin kind, origin Tower ID or canonical direct absence, model ID | registered Station capability ID, capability revision, boolean or bounded-uint64 value-kind tag, exactly its one typed value, and canonical absence of the foreign value | bytewise capability ID then unsigned revision | nonempty unique IDs; every value is within the closed current origin authority—StationOriginLeaseV1 for joined or DirectStationOriginAuthorityV1 for direct—and a joined offer is also within the Tower's current admitted and declared ceilings |
      | StationModalitySetV1 | Station ID, offer ID, and offer revision | Station origin epoch, direct or joined origin kind, origin Tower ID or canonical direct absence, model ID | exactly one closed modality value chat, chat_streaming, speech_to_text, or text_to_speech | bytewise UTF-8 modality value | nonempty unique values and every modality is supported by StationCapabilitySetV1 and the named model |
      | StationCapabilityCeilingSetV1 | Station owner ID and preallocated Station ID | policy version | registered Station capability ID, capability revision, boolean or bounded-uint64 value-kind tag, exactly its one typed ceiling value, and canonical absence of the foreign value | bytewise capability ID then unsigned revision | nonempty unique IDs; membership is authorized by central Station-admission policy, is portable across an owner-authorized joined rehome because Tower ID is not in its preimage, and the consumed authorization plus every resulting StationOriginLeaseRevisionAuthorityV1, StationOriginLeaseV1, and DirectStationOriginAuthorityV1 reuse this identical complete hash |
      | SupportedProtocolVersionSetV1 | RogerTrustDocumentV1 trust-document version | public network ID, issue time, expiry time | one bounded positive protocol-version integer | unsigned protocol version | nonempty strictly increasing unique versions; each resolves to one immutable protocol schema and its mandatory capability registry |
      | PublicDirectoryEntrySetV1 | PublicDirectorySnapshotV1 snapshot ID and revision | routing-policy version and trust-document version | Station ID, Station assertion key ID, direct or joined origin kind, Tower ID or canonical direct absence, StationOfferV1 complete hash, model ID, authoritative price fields, eligibility tier, and observed-health version | bytewise Station ID then offer hash | permits empty; Station IDs are unique, membership equals every and only public-discoverable entry at the snapshot authority tuple, and internal member count equals the outer PublicDirectorySnapshotV1 entry count |
      | RootDelegationKeySetV1 | RootDelegationPayloadV1 delegation ID and revision | public network ID, positive signature threshold, activation Core tuple, and expiry Core tuple | root key ID, signature algorithm, and canonical public-key bytes | bytewise root key ID | nonempty unique key IDs and public-key bytes; threshold is no greater than member count and every algorithm belongs to the closed bootstrap trust profile |
      | RootDelegatedTrustSignerSetV1 | RootDelegationPayloadV1 delegation ID and revision | public network ID, activation Core tuple, and expiry Core tuple | trust-document signer key ID, signature algorithm, canonical public-key bytes, fixed trust_document_signer purpose, not-before Core tuple, and not-after Core tuple | bytewise signer key ID | nonempty unique IDs and public-key bytes; every validity interval is within the root delegation interval and membership equals every online signer that this root permits to sign RogerTrustDocumentV1 |
      | RootDelegatedTrustPublisherSetV1 | RootDelegationPayloadV1 delegation ID and revision | public network ID, activation Core tuple, and expiry Core tuple | trust-publication signer key ID, signature algorithm, canonical public-key bytes, fixed trust_document_publication_signer purpose, not-before Core tuple, and not-after Core tuple | bytewise signer key ID | nonempty unique IDs and public-key bytes; every validity interval is within the root delegation interval, membership equals every independent signer permitted to accept RogerTrustPublicationV1, and no ID or public-key bytes occur in RootDelegatedTrustSignerSetV1 |
      | RootDelegationSignatureSetV1 | RootDelegationPayloadV1 complete hash | public network ID, delegation ID/revision, bootstrap or overlap transition kind, and previous RootDelegationV1 complete hash or canonical bootstrap absence | signer key ID, signature algorithm, and signature bytes over the domain-separated exact RootDelegationPayloadV1 JCS bytes | bytewise signer key ID | nonempty unique signers; membership and quorum follow the exact transition rule below and total is not applicable |
      | PurposeBoundKeyDirectorySetV1 | RogerTrustDocumentV1 trust-document version | public network ID and current RootDelegationV1 complete hash | key ID, algorithm, canonical public-key bytes, closed purpose, and issuer RootDelegationV1 ID/complete hash | bytewise key ID | nonempty; key IDs and public-key bytes are unique across accepted history, every currently supported purpose has an active current-root key, and membership equals every current key plus every retained historical verification key from the immediately prior trust document |
      | KeyValidityRevocationSetV1 | RogerTrustDocumentV1 trust-document version | public network ID and PurposeBoundKeyDirectorySetV1 complete hash | key ID, not-before Core tuple, not-after Core tuple, active or retired or revoked state, revocation reason/Core tuple or canonical nonrevoked absence, and replacement key ID or canonical no-replacement absence | bytewise key ID | nonempty with exactly one member for every PurposeBoundKeyDirectorySetV1 member in identical key-ID order; intervals are valid and immutable after first appearance, replacements name another directory key without a cycle, and state/revocation/replacement transitions follow the exact accepted-history rule below |
      | CompensationPolicyDirectorySetV1 | RogerTrustDocumentV1 trust-document version | public network ID, document issue/expiry, and current RootDelegationV1 complete hash | closed policy kind, exact policy object type, stable series-or-scope key, positive revision, policy complete hash, purpose-specific signer key ID/purpose, effective Core tuple, expiry Core tuple | b:policy-kind,b:stable-series-or-scope-key,u:revision | nonempty; kinds are exactly tower_compensation, funding_allocation, payout, fee_finality, maturity, payout_eligibility, compensation_enforcement, and debt_writeoff; every duplicated member field is byte-identical to the corresponding field of the named strict purpose-signed policy object whose complete hash/signature verifies, every series begins at revision 1 and advances only by immediate prior hash, one complete hash occurs once, and no two members create an ambiguous greatest applicable revision for one kind/scope/tuple |

  Scenario: Capability collection relationships can only narrow authority
    Given one enrollment proof, admission lease, accepted inventory, and Station offer form one relationship chain
    When Roger Core verifies their named capability sets
    Then every admitted member has the same ID, revision, and value kind as its requested member and a boolean true or bounded integer may only remain equal or narrow under the signed policy
    And every declared Tower member is similarly equal to or narrower than its admitted member, while every Station member is equal to or narrower than the owner authorization, origin lease, admitted Tower, and declared Tower intersections
    And every mandatory capability for the negotiated protocol is present and true at every applicable layer
    And compensated capability is never inferred from these operational sets because it remains a distinct Core authorization bound by GrantCompensationSnapshotV1
    And an omitted mandatory member, unknown ID/revision, type change, expansion, contradictory duplicate, or capability supplied only by ambient session state rejects admission, inventory, or routing

  Scenario: Inventory operations deterministically produce one offer state
    Given TowerInventoryOperationSetV1 and the previously accepted StationOfferStateSetV1 or canonical first-inventory absence are fixed
    When a full or delta inventory relationship is checked
    Then a full inventory's present members produce exactly its signed resulting StationOfferStateSetV1, while a delta applies add, replace, and remove members atomically to exactly its signed base state
    And every prior hash equals the targeted accepted leaf, every new hash identifies the member's exact signed StationOfferV1, and the computed complete set bytes equal the resulting state set bound by TowerInventoryV1
    And a stale base, absent target, existing add target, mismatched replace/remove prior, result mismatch, or partial application rejects the entire inventory revision

  Scenario: TowerEnrollmentProofV1 exhaustive field and mutation Cartesian product
    Given TowerEnrollmentProofV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | enrollment transaction ID |
      | one-time token ID |
      | approved operator ID |
      | preallocated Tower ID |
      | challenge ID |
      | challenge nonce |
      | challenge issue time |
      | challenge expiry time |
      | Tower identity key ID |
      | Tower identity public-key hash |
      | TLS CSR complete hash |
      | RequestedTowerCapabilitySetV1 complete hash |
      | software version |
      | accepted-terms version |
    When every universal mutation is independently applied to every listed field while retaining the Tower identity signature
    Then every field and mutation pair is rejected before token consumption or authority creation
    And inserting an unknown field, changing the signature, or presenting another key purpose is rejected

  Scenario: TowerAdmissionLeaseV1 exhaustive field and mutation Cartesian product
    Given TowerAdmissionLeaseV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | admission-lease signer key ID |
      | stable admission-lease series ID |
      | lease ID |
      | lease sequence |
      | previous admission-lease complete hash or canonical first-sequence absence |
      | Tower ID |
      | operator ID |
      | Tower identity key ID |
      | Tower certificate serial |
      | retiring Tower certificate serial and already-authenticated-session finish cutoff Core tuple or canonical no-overlap absence |
      | AdmittedTowerCapabilitySetV1 complete hash |
      | current TowerLifecycleEventV1 revision, complete hash, and lifecycle state |
      | routing-weight ceiling |
      | Station-count ceiling |
      | stream-count ceiling |
      | byte-rate ceiling |
      | policy version |
      | minimum software version |
      | issue time |
      | not-before time |
      | expiry time |
      | Core lease-ledger commit authority tuple |
    When every universal mutation is independently applied to every listed field while retaining the admission-lease signature
    Then every field and mutation pair is rejected before channel admission, inventory, or routing
    And inserting an unknown field, changing the signature, or presenting another key purpose is rejected

  Scenario: TowerAdmissionLeaseV1 has one per-Tower current head
    Given Tower T has current admission-lease sequence R and complete hash H
    When an initial lease, renewal, certificate rotation, capability change, or serving-permitted lifecycle refresh commits
    Then stable series ID is the fixed-length unpadded case-preserving base64url SHA-256 digest over UTF-8 strict JCS [TowerAdmissionLeaseV1-series-v1,network-ID,Tower-ID], and lease ID is the identically encoded digest over [TowerAdmissionLeaseV1-id-v1,network-ID,stable-series-ID,lease-sequence]
    And creation is sequence 1 with canonical prior absence; every successor is exactly R plus one with immediate prior complete hash H, and one serializable CAS advances the unique current series head
    And every successor preserves Tower/operator/identity-key identity, binds the exact current TowerLifecycleEventV1 revision/hash/state and one currently issued certificate serial, and derives issue/not-before/expiry from its independently assigned Core lease-ledger commit tuple within policy ceilings
    And renewal or TLS rotation makes its new certificate usable for new sessions only if the certificate and successor lease commit together; the retiring serial may finish only sessions authenticated before its signed overlap cutoff and opens no new session after head replacement
    And a restrictive lifecycle transition synchronously invalidates the stale lifecycle snapshot for new authority even if no successor lease can be signed; serving resumes only after an active current lifecycle and successor lease bind one another
    And new session, inventory, routing, DispatchLeaseV1, and joined ExecutionGrantV1 authority requires the exact current series sequence/hash plus exact current lifecycle revision/hash, while an already-authenticated session has only its signed bounded drain/overlap authority
    And exact replay is idempotent, while zero/gap/overflow sequence, wrong or absent prior, creation prior, fork, stale lifecycle, stale certificate, changed immutable identity, signer backdate, or conflicting bytes at one sequence is rejected before serving

  Scenario: TowerInventoryV1 exhaustive field and mutation Cartesian product
    Given a delta TowerInventoryV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | Tower statement key ID |
      | Tower ID |
      | Tower certificate serial |
      | Tower session epoch |
      | Tower channel-binding hash |
      | current TowerAdmissionLeaseV1 ID/sequence/complete hash |
      | current TowerLifecycleEventV1 revision/complete hash |
      | inventory kind |
      | inventory revision |
      | base revision |
      | previous inventory complete hash or canonical first-revision absence |
      | issue time |
      | expiry time |
      | software version |
      | TowerDeclaredCapabilitySetV1 complete hash |
      | TowerInventoryOperationSetV1 complete hash |
      | resulting StationOfferStateSetV1 complete hash |
      | resulting Station count |
      | encoded byte count |
    When every universal mutation is independently applied to every listed field while retaining the Tower statement signature
    Then every field and mutation pair rejects the entire inventory revision before any leaf is routable
    And inserting an unknown field, changing the signature, or presenting another key purpose is rejected

  Scenario Outline: TowerInventoryV1 kind has one revision shape
    Given a valid "<kind>" TowerInventoryV1
    When its variant fields have "<defect>"
    Then the entire revision is rejected and the last accepted revision remains authoritative until expiry

    Examples:
      | kind | defect |
      | full | base revision is present as null, zero, stale, current, or future instead of canonically absent |
      | full | operation array contains a delta removal rather than the complete ordered leaf set |
      | delta | base revision is missing, null, zero, not the accepted revision, or at least the target revision |
      | delta | two operations address the same Station or offer identity |
      | delta | computed result root or count differs from the signed result |

  Scenario: TowerInventoryV1 has one monotonic per-Tower current head
    Given Tower T has accepted inventory revision R and complete hash H
    When a full snapshot, delta, or full resynchronization is accepted
    Then creation is full revision 1 with base revision and prior hash canonically absent
    And every later full or delta revision is exactly R plus one with immediate prior complete hash H; delta alone has base revision R, while full has canonical base absence and still cannot skip the prior chain
    And one serializable CAS verifies the current admission-lease and lifecycle heads, session/certificate binding, declared-capability subset, operations, resulting offer-state set/count, expiry, byte ceiling, and advances the unique current inventory head or commits none
    And a requested full resynchronization is the exact next revision over H rather than a new genesis or arbitrary jump
    And exact replay returns the existing accepted result, while a zero/gap/overflow revision, stale/forked prior, same revision with different bytes, delta base mismatch, full prior absence after creation, or old lease/lifecycle/session rejects the whole revision

  Scenario: Every delta operation is signed and deterministic
    Given a TowerInventoryV1 delta binds a TowerInventoryOperationSetV1 containing add, replace, and remove operations
    When every universal mutation is independently applied to each operation's ordinal, kind, Station ID, offer ID, prior StationOfferV1 complete hash or canonical absence, and new StationOfferV1 complete hash or canonical absence
    Then the entire delta is rejected before partial application
    And an unknown operation kind, duplicate target, missing prior hash, or forbidden new hash is rejected

  Scenario: StationOfferV1 exhaustive field and mutation Cartesian product
    Given StationOfferV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | Station assertion key ID |
      | Station ID |
      | Station origin epoch |
      | Station assertion epoch |
      | Station secure-session key ID |
      | Station secure-session public-key hash |
      | admitted owner-authorization hash |
      | current StationLifecycleEventV1 revision and complete hash |
      | current origin-authority kind plus StationOriginLeaseV1 or DirectStationOriginAuthorityV1 ID/revision/complete hash with the foreign variant canonically absent |
      | direct or joined origin kind |
      | origin Tower ID or canonical direct absence |
      | offer ID |
      | offer revision |
      | previous StationOfferV1 complete hash or canonical first-revision absence |
      | model ID |
      | StationCapabilitySetV1 complete hash |
      | StationModalitySetV1 complete hash |
      | consumer input rate |
      | consumer output rate |
      | Station-earning input rate |
      | Station-earning output rate |
      | currency |
      | price unit |
      | accounting scale |
      | maximum context tokens |
      | maximum output tokens |
      | maximum request bytes |
      | maximum result bytes |
      | capacity ceiling |
      | declared-region value or canonical absence |
      | declared-hardware hash or canonical absence |
      | confidential-execution claim or canonical absence |
      | attestation-evidence hash or canonical absence |
      | issue time |
      | expiry time |
    When every universal mutation is independently applied to every listed field while retaining the Station signature
    Then every field and mutation pair excludes that leaf before routing
    And inserting an unknown field, changing the signature, or presenting another key purpose is rejected

  Scenario: StationOfferV1 has one current head per stable offer ID
    Given Station S offer ID O has current revision R and complete hash H
    When the Station signs a creation or successor offer
    Then creation is revision 1 with canonical prior absence and every successor is exactly R plus one with immediate prior complete hash H
    And one current-head CAS verifies the exact current Station lifecycle and joined-lease or direct-origin authority, assertion key/epoch, secure-session key/certificate, capability ceiling, model, price, bounds, and expiry before admission
    And joined admission accepts that head only through the exact next TowerInventoryV1 state, while direct admission records the same offer head through a serializable Core receive transaction without inventing a Tower inventory
    And replacing offer ID O with a new offer ID for the same Station/model atomically removes O and adds the new revision-1 offer in one joined inventory state or one direct-offer registry transaction; the two cannot coexist as current heads
    And exact replay is idempotent, while zero/gap/overflow revision, stale/forked prior, same revision with different bytes, creation prior, absent successor prior, reintroduced expired/removed head, or noncurrent origin/lifecycle rejects the offer before routing

  Scenario Outline: Optional Station claims have one canonical presence shape
    Given a StationOfferV1 has optional claim "<claim>"
    When the claim is "<encoding>"
    Then the offer is rejected instead of silently converting it to another trust meaning

    Examples:
      | claim | encoding |
      | declared region | explicit null, empty string, invalid code, or a value outside its declared-claim schema |
      | declared hardware | explicit null, empty hash, unknown hash algorithm, or malformed digest |
      | confidential execution | explicit null, empty value, or an unsupported enum |
      | attestation evidence | explicit null, empty hash, unsupported evidence kind, or evidence inconsistent with the signed claim |

  Scenario: TowerLifecycleEventV1 exhaustive field and mutation Cartesian product
    Given TowerLifecycleEventV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | Tower lifecycle signer key ID |
      | lifecycle event ID |
      | lifecycle revision |
      | previous lifecycle-event complete hash or canonical first-revision absence |
      | Tower ID |
      | prior lifecycle state |
      | new lifecycle state |
      | reason class |
      | Core effective-time tuple |
      | active-attempt action kind |
      | active-attempt cutoff tuple or canonical not_applicable absence |
      | compensation disposition |
      | consumed enrollment proof ID/complete hash or canonical non-genesis absence |
      | prescribed successor TowerAdmissionLeaseV1 stable series ID/lease ID/sequence/expected later group index or canonical non-serving-transition absence |
      | prescribed replacement certificate serial or canonical no-certificate-change absence |
      | administrator-evidence hash |
      | policy-evidence hash |
      | policy version |
      | independently assigned Core lifecycle-ledger commit time and global sequence |
    When every universal mutation is independently applied to every listed field while retaining the Tower lifecycle signature
    Then every field and mutation pair is rejected before lifecycle, routing, settlement-cutoff, or compensation authority changes
    And inserting an unknown field, changing the signature, or presenting another key purpose is rejected

  Scenario: Tower lifecycle history and its payout fence have one current CAS head
    Given Tower T has accepted lifecycle revision R and complete hash H
    When lifecycle, routing, grant, payout-preparation, or payout-send work races a Tower transition
    Then creation is revision 1 with canonical prior-event absence and every later lifecycle event is exactly R plus one with prior complete hash H
    And at most one event compare-and-swaps current R/H at its independently assigned lifecycle-ledger commit tuple, effective is no earlier than commit, key validity/compromise state derives from commit, exact replay returns the existing event, and a zero/gap/overflow revision, absent later prior, present creation prior, stale prior, wrong-Tower prior, fork, or conflicting bytes is rejected
    And routing and new leases/grants use that exact current R/H, while TowerLifecyclePayoutAuthorityV1 copies it with its compensation disposition/effective tuple and preparation/send compare-and-swap the same current R/H
    And a restrictive transition winning first blocks the raced authority; no asynchronous hold materialization, second fence alias, local timestamp, or stale lifecycle read can reopen routing or payout

  Scenario: Tower lifecycle and admission lease use one acyclic serving bundle
    Given an actual permitted lifecycle transition will first admit a Tower to quarantine or will restore quarantine/active serving authority after expiry, suspension, draining, or clearance
    When Roger Core commits the transition
    Then the lifecycle event is constructed first and binds the exact consumed enrollment/clearance/key-proof evidence plus deterministic successor lease stable series ID, lease ID, sequence, and expected later group index, but no successor lease complete hash
    And the certificate serial is preallocated and bound when a new credential is required; the later TowerAdmissionLeaseV1 binds the lifecycle event complete hash and that serial
    And initial admission is lifecycle revision 1 with prior-event absence and prior central enrollment state pending, new state quarantine, consumed TowerEnrollmentProofV1, and prescribed sequence-1 lease; the proof/token, event, certificate, and lease commit in one serializable transaction or none do
    And every later lifecycle-changing serving bundle compare-and-swaps the lifecycle and lease heads, appends the exact next lifecycle event, then appends the prescribed exact-next lease at the signed greater group index; a nonserving restrictive event may commit without a successor lease and immediately invalidates the old lease for new authority
    And an ordinary lease renewal, capability refresh, or certificate rotation appends only the exact next lease head binding the unchanged current lifecycle revision/hash under the separate lease-chain rule and cannot invent an active-to-active or quarantine-to-quarantine lifecycle event
    And no lifecycle, lease, certificate, session, inventory, or route is externally authoritative between group members, while exact whole-bundle replay is idempotent
    And an omitted lease, lease-before-lifecycle order, child hash inside its parent, wrong index/serial/head, partial token consumption, or failed child leaves the prior heads and credentials unchanged

  Scenario Outline: A lifecycle action has one canonical shape
    Given a TowerLifecycleEventV1 carries action "<action>"
    When its cutoff or state transition has "<defect>"
    Then no lifecycle revision commits and stale routing cannot create a new grant

    Examples:
      | action | defect |
      | not_applicable | a cutoff is present, the transition restricts active work, or the transition reason requires cancellation |
      | drain_until | cutoff missing, null, at or before the effective-time tuple, or outside the maximum drain ceiling |
      | drain_until | reason is security revocation, credential compromise, or mandatory emergency cancellation |
      | cancel_at | cutoff missing, null, before the effective-time tuple, or accompanied by a drain field |
      | cancel_at | compensation disposition is missing or contradicts the reason-class policy |
      | any | multiple action encodings, no action encoding, an unknown action, or a transition forbidden by the lifecycle graph |

  Scenario Outline: Lifecycle compensation disposition is closed and relationship-checked
    Given TowerLifecycleEventV1 carries compensation disposition "<disposition>"
    When it has "<defect>"
    Then no lifecycle revision or compensation hold authority commits

    Examples:
      | disposition | defect |
      | not_applicable | any compensation-affecting transition, hold, release, reason, review, or money disposition is present |
      | preserve_historical | the reason class requires an unpaid hold or security review |
      | withhold_unpaid | no signed reason-class policy authorizes the exact hold scope, or the event claims to cancel, forfeit, pay, or write off any amount directly |
      | forfeiture_decision_required | no distinct forfeiture-review reason/evidence exists or the event claims forfeiture already happened |
      | unknown | any unrecognized, empty, null, numeric, or multi-valued encoding |

  Scenario: StationLifecycleEventV1 has one exact signed state and cutoff authority
    Given StationLifecycleEventV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | Station-lifecycle signer key ID |
      | lifecycle event ID |
      | positive lifecycle revision |
      | previous StationLifecycleEventV1 complete hash or canonical first-revision absence |
      | Station ID |
      | owner ID |
      | prior lifecycle state or canonical initial absence |
      | new lifecycle state |
      | reason class |
      | Core effective authority tuple |
      | not_applicable, drain_until, or cancel_at active-attempt action |
      | action cutoff Core tuple or canonical not_applicable absence |
      | StationEpochClosureEvidenceSetV1 complete hash/member count or canonical non-gap/fork absence |
      | StationEpochResetV1 ID/complete hash or canonical non-reset absence |
      | owner, administrator, security, key/origin-authority, and policy evidence hashes in their reason-specific presence shape |
      | policy version |
      | independently assigned Core lifecycle-ledger commit time and global sequence |
    When every universal mutation is independently applied to every listed field while retaining the Station-lifecycle signature
    Then every field and mutation pair is rejected before origin, offer, certificate, session, lease, grant, attempt-cutoff, epoch, or held-earning state changes
    And inserting an unknown field, changing the signature, presenting another key purpose, or supplying both closure-evidence and reset sources is rejected

  Scenario Outline: Station lifecycle transitions and sources are closed
    Given StationLifecycleEventV1 transition is "<transition>"
    Then its exact authority is "<authority>"
    And every unlisted prior/new state, action, source presence, reason, or cutoff relationship is rejected

    Examples:
      | transition | authority |
      | initial admission absent to active | revision 1/prior absence, not_applicable action, consumed direct-or-joined StationAttachAuthorizationV1 and current origin-authority evidence |
      | active to draining | drain_until with future bounded cutoff and owner/policy evidence |
      | active or draining to suspended | cancel_at with cutoff at or after effective tuple and exact administrative/security evidence |
      | active, draining, or suspended to epoch_closed | cancel_at with cutoff and exact current joined-lease or direct-origin-authority/key-compromise evidence, or a complete StationEpochClosureEvidenceSetV1 for gap/fork closure |
      | any nonterminal state to revoked | cancel_at with exact current origin-authority and terminal revocation evidence |
      | suspended to active | not_applicable with signed nonsecurity clearance and no prior compromise reason |
      | epoch_closed to active through reset | exactly next revision/prior hash, StationEpochResetV1 complete hash, not_applicable action, reset-policy evidence, and exact same-group current epoch_reset StationOriginLeaseV1 for joined or DirectStationOriginAuthorityV1 for direct |

  Scenario: Station lifecycle history and all serving decisions use one current CAS authority
    Given Station S has accepted lifecycle revision R and complete hash H
    When a lifecycle event, offer, lease, certificate/session admission, routing choice, or execution grant races a restriction
    Then creation is revision 1 with canonical prior absence and every later lifecycle event is exactly R plus one with prior hash H
    And at most one lifecycle event compare-and-swaps R/H at its independently assigned lifecycle-ledger commit tuple, effective is no earlier than commit, key validity/compromise state derives from commit, exact replay is idempotent, and a gap, overflow, fork, stale prior, or conflicting event fails
    And StationOfferV1 and ExecutionGrantV1 bind the exact lifecycle revision/hash used plus their independent current joined-lease or direct-origin authority, while certificate/session admission and routing compare-and-swap the same current R/H before granting authority
    And a restrictive event winning first prevents new offers, leases, certificates, sessions, or grants; an already issued attempt follows only that event's signed drain/cancel cutoff

  Scenario: StationRehomeLeaseV1 exhaustive field and mutation Cartesian product
    Given StationRehomeLeaseV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | Station-admission/origin signer key ID |
      | rehome event ID |
      | Station ID |
      | Station assertion key ID |
      | owner-authorization hash |
      | prior Tower ID |
      | new Tower ID |
      | prior Station origin epoch |
      | new Station origin epoch |
      | unchanged Station assertion epoch |
      | continued assertion-head sequence and complete hash |
      | prior-origin fence sequence |
      | new-origin not-before tuple |
      | expiry tuple |
      | policy version |
      | independently assigned Core origin-ledger commit time and global sequence |
    When every universal mutation is independently applied to every listed field while retaining the Station-admission/origin signature
    Then every field and mutation pair is rejected before either origin changes
    And the old origin is fenced before the new origin can become routable, new-origin not-before is no earlier than the independently assigned origin-ledger commit tuple, and key validity/compromise selection derives from that tuple

  Scenario: StationEpochResetV1 exhaustive field and mutation Cartesian product
    Given StationEpochResetV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | Station-epoch signer key ID |
      | reset event ID |
      | Station ID |
      | owner ID |
      | unchanged direct or joined origin kind and Tower ID or canonical direct absence |
      | unchanged StationCapabilityCeilingSetV1 complete hash |
      | prior assertion key ID |
      | replacement assertion key ID |
      | prior secure-session key ID |
      | replacement secure-session key ID |
      | unchanged Station origin epoch |
      | prior Station assertion epoch |
      | replacement Station assertion epoch |
      | prior terminal assertion-head hash |
      | terminal StationLifecycleEventV1 ID/revision/complete hash |
      | StationEpochClosureEvidenceSetV1 complete hash or canonical non-gap/fork absence |
      | owner-authorization complete hash |
      | reason class |
      | one-use reset nonce |
      | effective authority tuple |
      | old-epoch cutoff authority tuple |
      | replacement origin-authority branch tag direct or joined |
      | joined StationOriginLeaseRevisionAuthorityV1 stable authority ID and expected same-transaction group index or canonical direct absence |
      | joined replacement StationOriginLeaseV1 stable series ID/exact next revision and expected same-transaction group index or canonical direct absence |
      | direct replacement DirectStationOriginAuthorityV1 stable series ID/exact next revision and expected same-transaction group index or canonical joined absence |
      | replacement certificate serial |
      | replacement active StationLifecycleEventV1 stable ID and expected same-transaction group index |
      | replacement initial sequence |
      | independently assigned Core lifecycle/origin-group commit time, global sequence, and reset group index |
    When every universal mutation is independently applied to every listed field while retaining the Station-epoch signature
    Then every field and mutation pair is rejected before epoch creation, routing, assertion ingestion, or payout release
    And inserting an unknown field, changing the signature, or presenting another key purpose is rejected

  Scenario: StationEpochResetV1 replay and ordering are fenced
    Given one Station reset nonce or replacement epoch already committed
    When the same object, another object with the nonce, a lower/equal replacement epoch, a noninitial sequence, or a reset not linked to the terminal prior head arrives
    Then no second epoch or altered historical link is created
    And exact replay returns the existing reset while every divergent reuse is a conflict
    And reset requires an epoch_closed current lifecycle event and compare-and-swaps its exact lifecycle head, current joined-lease or direct-authority head, reset nonce, prior assertion-epoch terminal head, replacement credential serial, and the independently assigned lifecycle/origin-group commit tuple in one serializable transaction
    And reset effective/cutoff authority is no earlier than that commit, every child copies the same base transaction tuple plus its signed group index, and key validity/compromise selection derives from that non-signer-controlled tuple
    And the acyclic ordered group is exactly reset then, for joined, epoch_reset StationOriginLeaseRevisionAuthorityV1 then its resulting StationOriginLeaseV1, or, for direct, epoch_reset DirectStationOriginAuthorityV1, and finally the bound exact-next-revision active StationLifecycleEventV1 at the signed expected indices
    And each child binds the already constructible prior object complete hash, the reset complete hash, and its prescribed stable identity/index as applicable; the active lifecycle event additionally binds the resulting origin-authority complete hash, while the reset binds no child complete hash
    And the replacement certificate serial/key material is issued only from that resulting origin authority and is unusable for a new session until the entire group including active lifecycle commits
    And owner, origin kind, Tower ID/direct absence, Station origin epoch, and StationCapabilityCeilingSetV1 remain byte-identical through reset and its epoch_reset joined-lease or direct-origin authority; an origin change cannot hide inside reset
    And exact replay returns the whole committed bundle, while an omitted/reordered child, wrong branch presence, stale head, wrong index, conflicting child bytes, partial credential issue, or failed lifecycle append commits none and leaves the Station epoch_closed and nonserving

  Scenario: PublicDirectorySnapshotV1 exhaustive field and mutation Cartesian product
    Given PublicDirectorySnapshotV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | public-directory signer key ID |
      | directory snapshot ID |
      | directory revision |
      | previous directory-snapshot complete hash or canonical first-revision absence |
      | issue time |
      | expiry time |
      | Core directory-publication authority time and global sequence |
      | routing-policy version |
      | RogerTrustPublicationV1 sequence/complete hash and RogerTrustDocumentV1 version/complete hash |
      | PublicDirectoryEntrySetV1 complete hash |
      | entry count |
    When every universal mutation is independently applied to every listed field while retaining the public-directory signature
    Then every field and mutation pair is rejected before public discovery or routing use
    And inserting an unknown field, changing the signature, or presenting another key purpose is rejected

  Scenario: PublicDirectorySnapshotV1 has one nonbackdatable current head
    Given the fixed public-network directory snapshot-series ID has current revision R and complete hash H
    When Roger Core publishes the next snapshot
    Then creation is revision 1 with canonical prior absence and every successor is exactly R plus one with immediate prior complete hash H under one serializable current-head CAS
    And directory snapshot ID is the fixed-length unpadded case-preserving base64url SHA-256 digest over UTF-8 strict JCS [PublicDirectorySnapshotV1-series-v1,public-network-ID]
    And its Core directory-publication tuple is independently assigned at commit; the named RogerTrustPublicationV1 is the unique greatest accepted publication no later than that tuple and its document version/hash is byte-identical
    And membership contains every and only discoverable current StationOfferV1 head whose lifecycle/origin/admission authorities are current at that tuple, with no stale or duplicate Station identity
    And a client persists the highest accepted revision/hash per network and rejects any lower revision, same revision with different bytes, gap, fork, wrong prior, regressed Core tuple, older trust publication, expired snapshot, or reintroduced stale offer even if its signature and expiry otherwise verify
    And exact replay is idempotent; a fresh client verifies the complete chain from revision 1 or a separately approved authenticated compact consistency proof, which is not a v1 shortcut

  Scenario: Each public directory entry is relationship-bound
    Given a signed directory contains direct and joined Station entries
    When every universal mutation is independently applied to an entry's Station ID, Station assertion key ID, origin kind, Tower ID or canonical absence, offer complete-object hash, model, authoritative price, eligibility tier, or observed-health version
    Then the directory signature or relationship verification fails
    And a joined entry cannot be converted to direct or another Tower by changing presence fields

  Scenario: RootDelegationV1 has one exact acyclic complete object
    Given a root delegation is encoded for the public network
    When its RootDelegationV1 complete hash is computed
    Then the complete strict JCS object contains only one complete RootDelegationPayloadV1 object and one complete RootDelegationSignatureSetV1 object
    And RootDelegationPayloadV1 contains only schema version, public network ID, root-trust-profile version, delegation ID, positive revision with fixed first revision 1, previous RootDelegationV1 complete hash or canonical bootstrap absence, bootstrap or overlap transition kind, activation Core tuple, expiry Core tuple, positive signature threshold, RootDelegationKeySetV1 complete hash/member count, RootDelegatedTrustSignerSetV1 complete hash/member count, and RootDelegatedTrustPublisherSetV1 complete hash/member count
    And each payload count equals its named set member count, activation is before expiry, and the payload contains no signature, signature-set hash, trust-document hash, or its own complete hash
    And RootDelegationSignatureSetV1 owner/scope fields are byte-identical to that payload and its signatures cover only the domain-separated exact payload JCS bytes
    And RootDelegationV1 complete hash is computed only after payload, root-key set, delegated trust-signer set, delegated trust-publisher set, and signature-set bytes exist, so no member or payload contains that resulting complete hash

  Scenario Outline: Root delegation transitions require the exact pinned quorum
    Given RootDelegationPayloadV1 transition kind is "<kind>"
    Then its authority contract is "<contract>"
    And a zero or excessive threshold, duplicate signer, signer outside the named authority, bad signature, stale/omitted prior hash, skipped revision, invalid interval, unauthorized key reuse, or insufficient quorum rejects RootDelegationV1 before a trust document can name it
    And emergency root recovery is not a v1 wire variant and requires a separately approved typed recovery authority, incident, delay, quorum, and client bootstrap procedure

    Examples:
      | kind | contract |
      | bootstrap | revision is 1, prior hash is canonically absent, and RootDelegationSignatureSetV1 reaches the installed bootstrap-profile threshold using only pinned bootstrap key IDs |
      | overlap | revision is exactly prior revision plus one, prior complete hash names the accepted current delegation, activation/expiry preserve the signed overlap window, and the signature set independently reaches the prior delegation threshold and the proposed delegation threshold under their exact key sets |

  Scenario: Root delegation fields, members, and rollover are mutation-exhaustive
    Given one valid RootDelegationV1 and its exact prior authority are fixed
    When every universal mutation is independently applied to a RootDelegationPayloadV1 field, RootDelegationKeySetV1 member, RootDelegatedTrustSignerSetV1 member, RootDelegatedTrustPublisherSetV1 member, RootDelegationSignatureSetV1 member, member count, order, threshold, transition kind, prior relationship, or signature while the remaining bytes stay fixed
    Then strict decoding, a named set complete-hash relationship, payload signature verification, or the transition quorum fails before any root, trust document, delegated key, directory, protocol, receipt, or checkpoint becomes trusted
    And inserting an unknown field, alternate empty form, duplicate member, unregistered algorithm, or signature over a reconstructed rather than exact payload is rejected

  Scenario: RogerTrustDocumentV1 exhaustive field and mutation Cartesian product
    Given RogerTrustDocumentV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | public network ID |
      | trust-document signer key ID |
      | trust-document version |
      | previous trust-document complete hash or canonical first-version absence |
      | issue time |
      | expiry time |
      | current RootDelegationV1 complete hash |
      | next RootDelegationV1 complete hash or canonical absence |
      | PurposeBoundKeyDirectorySetV1 complete hash |
      | KeyValidityRevocationSetV1 complete hash |
      | SupportedProtocolVersionSetV1 complete hash |
      | signed minimum-software-version policy hash |
      | routing-policy hash |
      | CompensationPolicyDirectorySetV1 complete hash and member count |
      | canonical-encoding-policy hash |
      | latest RogerLedgerCheckpointV1 log ID/sequence/complete hash or canonical pre-checkpoint absence |
    When every universal mutation is independently applied to every listed field while retaining the trust-document signature
    Then every field and mutation pair is rejected before any delegated key, protocol, policy, or checkpoint becomes trusted
    And inserting an unknown field, changing the signature, or presenting another key purpose is rejected

  Scenario: RogerTrustPublicationV1 gives each trust document an independent non-backdatable acceptance anchor
    Given an exact RogerTrustDocumentV1 has passed root-delegated trust-document signature and predecessor checks but is not yet accepted
    When Roger Core serializably accepts it into the trust-publication log
    Then RogerTrustPublicationV1 strict signing bytes contain only schema version, public network ID, root-trust-profile version, fixed publication-log ID, positive publication sequence, previous RogerTrustPublicationV1 complete hash or canonical first-publication absence, RogerTrustDocumentV1 version/complete hash, previous trust-document complete hash or canonical first-version absence, current RootDelegationV1 ID/complete hash, publication Core authority tuple containing independently assigned bounded Core time and global authority sequence, and trust-publication signer key ID
    And the purpose-separated publication signature is the only excluded signing slot and verifies through one active byte-identical RootDelegatedTrustPublisherSetV1 member under the named current root, never through the trust-document signer or a directory key introduced by that document
    And publication 1 binds document version 1 plus both canonical predecessor absences, while each later publication and document version are exactly their accepted predecessors plus one and bind both immediately prior complete hashes
    And the serializable commit assigns a publication tuple no earlier than current trusted Core time/sequence, requires document issue time no later than that tuple and that tuple strictly before document expiry, accepts one document per version/sequence, and atomically advances the publication head or commits nothing
    And at publication the named current root is still the unique latest active accepted root, optional next is still the one unactivated overlap successor, every first-appearing directory key has signed not-before no earlier than that tuple, and an ancestor-root historical key remains nonactive
    And the document acquires no verification authority before this record commits; exact replay returns the existing record, while a fork, gap, overflow, stale/root-mismatched predecessor, signer-selected backdate, wrong-purpose signer, trust-signer/publication-signer key reuse, or conflicting document at one sequence fails before publication

  Scenario: RogerTrustPublicationV1 fields and relationships are mutation-exhaustive
    Given one valid accepted RogerTrustPublicationV1 and its exact predecessor/root/document relationships are fixed
    When every universal mutation is independently applied to a listed signing field, the publication signature, the document bytes, a predecessor relationship, or the Core publication tuple
    Then strict decoding, root-delegated publisher verification, serializable history verification, or the document relationship fails before that document or any newly introduced key gains authority
    And inserting an unknown field, alternate first-absence shape, reused global authority sequence, nonmonotonic Core time, or a signature over reconstructed rather than exact signing bytes is rejected

  Scenario: Published money policies require both a purpose signature and independent trust publication
    Given RogerTrustDocumentV1 binds one CompensationPolicyDirectorySetV1
    When its RogerTrustPublicationV1 is accepted
    Then every directory member resolves to the exact strict policy bytes/hash, stable series/scope key, revision/prior relationship, effective/expiry tuples, and a byte-identical PurposeBoundKeyDirectorySetV1 key whose closed purpose is tower_compensation_policy_signer, funding_allocation_policy_signer, payout_policy_signer, fee_finality_policy_signer, maturity_policy_signer, payout_eligibility_policy_signer, compensation_enforcement_policy_signer, or debt_writeoff_policy_signer as prescribed by the policy kind
    And the policy signer is distinct from the trust-document signer and trust-publication signer, its cryptographic signature is checked through the candidate directory/root relationship, and the policy gains no selection authority until this independent publication commits
    And a policy's Core policy-ledger commit tuple is no later than the containing publication tuple, its effective tuple is no earlier than both, and its effective tuple is strictly before its expiry tuple and the containing document expiry
    And the first accepted containing publication sequence/hash/tuple is the immutable policy publication anchor; signer issue time, policy applicability claims, document issue time, or a later reconstructed set cannot backdate it
    And a new decision, lot, preparation, grant snapshot, or fee deadline selects the unique greatest accepted policy revision effective and unexpired at its independently assigned Core tuple for the exact kind/scope, while a historical object retains its exact selected published policy subject to later compromise-effective revocation
    And an unlisted policy, naked policy signature, trust-document-signer signature, wrong purpose, changed member copy, same-scope ambiguity, chain gap/fork, prepublication effective tuple, postexpiry selection, or explicit older document that differs from the derived publication grants no money authority

  Scenario: Trust document root selection is exact
    Given RogerTrustDocumentV1 names a current RootDelegationV1 and an optional next RootDelegationV1
    When its root relationships are verified against pinned accepted history
    Then current is active and unexpired at trust-document issue, and the trust-document signer key ID/algorithm/public-key bytes/purpose/validity are byte-identical to one active RootDelegatedTrustSignerSetV1 member
    And the PurposeBoundKeyDirectorySetV1 record for that signer is byte-identical to the same root-delegated signer identity and purpose, so the first trust document cannot introduce its own signing authority
    And every member eligible to authorize a new object names current's delegation ID and complete hash as issuer, while a member issued by an accepted ancestor root is retained only in retired or revoked state for verification of objects inside its historical authority interval
    And trust-document expiry is no later than both current RootDelegationV1 expiry and that signer's not-after tuple
    And every KeyValidityRevocationSetV1 member eligible for new signatures has not-before no earlier than both its issuer-root activation and its first accepted containing RogerTrustPublicationV1 Core authority tuple, and not-after no later than both that first document's expiry and issuer-root expiry; historical verification after expiry uses the recorded object-authority tuple and accepted history but grants no new signing authority
    And next is either canonically absent or the one valid unactivated overlap successor whose previous hash names current and whose activation is after issue but no later than current expiry
    And a bootstrap root after accepted history, unrelated next root, skipped revision, expired current root, next root already active, trust signer outside current authority, active ancestor-root key, document expiry beyond issuer authority, or delegated-key interval beyond first-document/root authority rejects the document

  Scenario: Trust document history and optional checkpoint have one genesis shape
    Given RogerTrustDocumentV1 version V is checked against pinned accepted history
    Then version 1 requires canonical previous-document absence and every later version is exactly prior plus one with the immediately prior complete hash
    And every document has issue time strictly before expiry, every successor issue time is strictly after its predecessor issue time, and its accepted publication Core tuple is at or after issue and strictly before expiry
    And latest checkpoint reference is canonically absent until one accepted RogerLedgerCheckpointV1 exists, then names the greatest accepted log sequence at or before the document's publication Core authority tuple and cannot regress in a successor document
    And a zero/skipped/overflowing version, wrong or cross-network prior, present prior at version 1, absent prior later, inverted/future/regressed issue interval, publication before issue or at/after expiry, future/unaccepted checkpoint, checkpoint regression, or conflicting bytes at one version rejects the document

  Scenario: Accepted trust history cannot manufacture retroactive key authority
    Given Roger Core verifies trust document D against the complete accepted predecessor chain
    When it derives each key's first-seen anchor from that chain
    Then the anchor is the first accepted RogerTrustPublicationV1 sequence, complete hash, and publication Core authority tuple whose exact document contains that key ID, algorithm, public-key bytes, purpose, and issuer-root identity, and no field or time claim supplied by D may replace or backdate it
    And a first-appearing key is active, has canonical revocation/replacement absence, names the current root, has never appeared with that key ID or public-key bytes, and has not-before no earlier than that publication tuple
    And every key from the immediate predecessor remains present; its key ID, algorithm, public-key bytes, purpose, issuer, not-before, and not-after are byte-identical, while state may move only active to active/retired/revoked, retired to retired/revoked, or revoked to revoked
    And leaving active may set at most one replacement that is an active same-purpose current-root key with a strictly later first-seen anchor and bounded overlap, whether first introduced in D or a predecessor; once nonactive, its state-specific revocation reason/tuple and replacement are immutable except that a retained key moving retired to revoked records its one immutable revocation reason/tuple without changing replacement
    And when current root changes, every retained ancestor-root active key becomes retired or revoked in that first successor document, and only fresh current-root keys may remain active
    And for each directory-resolved object other than RogerTrustDocumentV1 and a policy whose exact hash is accepted through CompensationPolicyDirectorySetV1, the verifier first derives the unique greatest accepted publication tuple no later than the object's independently authoritative Core tuple and uses that document's exact key state
    And any explicit trust-document version or version/complete-hash carried by the object's closed schema must equal the derived document rather than selecting an older one, while absence is permitted only for an exact schema that defines no such field
    And the derived publication must be at or after the key's first-seen publication and the object tuple must be inside the immutable key interval; RogerTrustDocumentV1 itself instead verifies only through RootDelegatedTrustSignerSetV1, while a directory-listed policy uses its first accepted containing publication as its nonbackdatable authority anchor without self-reference
    And the greatest later accepted document containing that historical key overlays any revoked state and signed compromise-effective tuple: retirement is prospective, while revocation rejects an object whose authoritative tuple is at or after the cutoff even if an earlier selected document showed the key active
    And omission of a predecessor key, reintroduction or reactivation, key-ID or public-key reuse, a first appearance as retired/revoked, backdated not-before/publication/object authority, identity/interval mutation, state regression, changed terminal metadata, ambiguous derived document selection, or an explicit pre-membership document reference rejects the trust document or object before authority is granted

  Scenario: Every delegated trust key is independently bound across two exact sets
    Given PurposeBoundKeyDirectorySetV1 and KeyValidityRevocationSetV1 are bound by one trust document
    When every universal mutation is independently applied to a directory member's key ID, algorithm, public-key bytes, purpose, or issuer, or to a validity member's key ID, not-before tuple, not-after tuple, state, revocation reason/tuple, or replacement key ID
    Then the applicable set complete-hash relationship and trust document are rejected before that key verifies an object
    And the two sets have byte-identical ordered key-ID membership, while duplicate or historically reused key IDs/public keys, overlapping conflicting purposes, invalid or backdated intervals, skipped or omitted revocation history, unknown state/purpose, illegal rollover retention, and replacement or authority cycles are rejected

  Scenario: RogerLedgerCheckpointV1 exhaustive field and mutation Cartesian product
    Given RogerLedgerCheckpointV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | checkpoint signer key ID |
      | log ID |
      | checkpoint sequence |
      | previous checkpoint complete hash or canonical first-sequence absence |
      | tree size |
      | Merkle root hash |
      | closed Merkle tree and hash algorithm suite ID |
      | Core-observed checkpoint time and global authority sequence |
      | RogerTrustPublicationV1 sequence/complete hash and RogerTrustDocumentV1 version/complete hash |
      | ledger schema version |
    When every universal mutation is independently applied to every listed field while retaining the checkpoint signature
    Then every field and mutation pair fails checkpoint verification
    And inserting an unknown field, changing the signature, decreasing tree size, or breaking consistency with a prior accepted checkpoint is rejected

  Scenario: RogerLedgerCheckpointV1 has one current append-only checkpoint head
    Given fixed log ID L has accepted checkpoint sequence R, complete hash H, and tree size N
    When a later checkpoint is published
    Then creation is sequence 1 with canonical prior absence and positive tree size, while every successor is exactly R plus one with immediate prior complete hash H and strictly greater tree size
    And its independently assigned Core tuple, derived greatest accepted RogerTrustPublicationV1 and document version/hash, ledger schema, Merkle root, and consistency proof from N are verified before one current-head CAS advances L
    And exact replay is idempotent, while zero/gap/overflow sequence, creation prior, absent successor prior, stale/forked prior, same sequence with different bytes, nonincreasing tree size, inconsistent prefix, signer backdate, or older explicit trust publication is rejected
    And clients persist the greatest accepted sequence/hash/tree size for each log, reject rollback or equivocation, and may still verify historical inclusion against an older retained checkpoint without treating it as current transparency authority

  Scenario: Live routing and transparency authority uses current revocation state
    Given a historically signed unexpired lease, inventory, offer, public directory, or checkpoint verifies at its original Core authority tuple
    When it is evaluated for new live routing, session, grant, discovery, or current-transparency authority
    Then its signer and every bound authority key must also be active and nonrevoked in the greatest currently accepted RogerTrustPublicationV1 at evaluation time, and the object must be the exact current unexpired domain head
    And historical receipt or inclusion verification may report valid-at-its-anchored-pre-cutoff-tuple without granting new live authority
    And a forged backdated object, retired/revoked current signer, stale head, or explicit older trust-document reference cannot regain live authority

  Scenario Outline: Control-plane role signatures are never interchangeable
    Given a valid object requiring "<required role>"
    When its signature bytes come from a valid "<wrong role>" signer
    Then verification fails before the object gains authority

    Examples:
      | required role | wrong role |
      | Tower enrollment proof | Tower TLS |
      | Tower inventory | Station offer |
      | Station offer | Tower inventory |
      | admission lease | Tower lifecycle |
      | Tower lifecycle | public directory |
      | Station rehome | Station offer |
      | Station epoch reset | Station offer |
      | public directory | trust document |
      | trust document | trust publication |
      | trust publication | trust document |
      | checkpoint | settlement |
