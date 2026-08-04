# APPROVED SPEC - founder approved 2026-08-03. Changes to an approved scenario need
# re-approval; they are not a diff to be reviewed.
#
# Scope: exhaustive Cartesian tamper matrices for request authorization, dispatch lease, execution grant, attempt anchors, grant compensation snapshot, compensated capability, funding lots and reservations.
#
# One of five files split from the former single tamper_matrix.feature. The
# source-of-truth rule below applies to ALL FIVE collectively.
# The field lists below are the source of truth; summary field-mutation outlines in the
# subdomain specs do not reduce this matrix. Every new semantic field must be added here in
# the same spec change that introduces it.

Feature: Every signed Tower-network field rejects every universal post-signing mutation
  Strict decoding or role-specific signature verification rejects each independently
  applied field/operator pair before routing, trust, money, or compensation authority.

  Background:
    Given valid canonical objects with every field required by their selected origin and state
    And independent purpose-bound signing keys for every object role

  # Each object scenario executes the full Cartesian product of its field table and this
  # universal mutation set. This is not a sample: every pair is a separate test case.
  Scenario: ClientRequestAuthorizationV1 exhaustive field and mutation Cartesian product
    Given ClientRequestAuthorizationV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | client key ID |
      | authenticated account ID |
      | network ID |
      | HTTP method |
      | canonical path |
      | canonical query hash |
      | canonical body digest |
      | model |
      | modality |
      | client nonce/idempotency key |
      | issue time |
      | expiry time |
    When each mutation is applied independently to each listed field while retaining the original client signature:
      | mutation |
      | replace with a different valid in-range value of the same semantic type |
      | remove the field |
      | encode the field as explicit null |
      | duplicate the field with the same encoded value |
      | duplicate the field with a conflicting encoded value |
      | encode the field using a different wire type |
    Then every field and mutation pair is rejected before idempotency lookup, hold, or attempt creation
    And inserting any unknown field or altering the client signature is rejected

  Scenario: DispatchLeaseV1 exhaustive field and mutation Cartesian product
    Given DispatchLeaseV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | dispatch-signer key ID |
      | network ID |
      | protocol version |
      | issue time |
      | Core attempt-issue authority time and global sequence |
      | AttemptIssueCommitmentV1 stable ID and expected attempt-ledger index |
      | job ID |
      | attempt ID |
      | dispatch nonce |
      | Tower ID |
      | Tower certificate serial |
      | Tower session epoch |
      | Tower channel-binding hash |
      | Station ID |
      | Station origin epoch |
      | encrypted request-envelope digest |
      | execution deadline |
      | maximum request bytes |
      | maximum result bytes |
      | maximum streams |
      | Tower admission-lease stable series ID, lease ID, sequence, and complete hash |
      | current TowerLifecycleEventV1 revision and complete hash |
    When each mutation is applied independently to each listed field while retaining the original signature:
      | mutation |
      | replace with a different valid in-range value of the same semantic type |
      | remove the field |
      | encode the field as explicit null |
      | duplicate the field with the same encoded value |
      | duplicate the field with a conflicting encoded value |
      | encode the field using a different wire type |
    Then every field and mutation pair is rejected before dispatch
    And inserting any unknown field or altering the signature is rejected

  Scenario: ExecutionGrantV1 exhaustive field and mutation Cartesian product
    Given a joined ExecutionGrantV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | grant-signer key ID |
      | origin kind |
      | network ID |
      | protocol version |
      | job ID |
      | request ID |
      | attempt ID |
      | client key ID |
      | client nonce/idempotency key |
      | grant nonce |
      | Core attempt-issue authority time and global sequence |
      | AttemptIssueCommitmentV1 stable ID and expected attempt-ledger index |
      | Tower ID |
      | Tower certificate serial |
      | Tower session epoch |
      | Tower channel-binding hash |
      | Tower admission-lease stable series ID, lease ID, sequence, and complete hash |
      | current TowerLifecycleEventV1 revision and complete hash |
      | Station ID |
      | Station assertion key ID |
      | Station origin epoch |
      | Station assertion epoch |
      | current StationLifecycleEventV1 revision and complete hash |
      | current origin-authority kind plus StationOriginLeaseV1 or DirectStationOriginAuthorityV1 ID/revision/complete hash |
      | Station secure-session certificate serial |
      | Station inner-session epoch |
      | Station inner-channel-binding hash |
      | model |
      | offer ID |
      | quote ID |
      | consumer input rate |
      | consumer output rate |
      | Station-earning input rate |
      | Station-earning output rate |
      | currency |
      | price unit |
      | accounting scale |
      | maximum input tokens |
      | maximum output tokens |
      | maximum cost |
      | maximum request bytes |
      | maximum result bytes |
      | maximum streams |
      | request digest |
      | FundingSourceReservationV1 complete hash |
      | FundingSourceReservationSetV1 complete hash |
      | FundingAllocationPolicyV1 series/revision/complete hash |
      | policy version |
      | TowerCompensationPolicyV1 series/revision/complete hash or canonical uncompensated absence |
      | grant-time compensation-snapshot complete hash or canonical absence |
      | issue time |
      | execution deadline |
      | settlement-finalization/hold ceiling |
      | modality |
    When each mutation is applied independently to each listed field while retaining the original signature:
      | mutation |
      | replace with a different valid in-range value of the same semantic type |
      | remove the field |
      | encode the field as explicit null |
      | duplicate the field with the same encoded value |
      | duplicate the field with a conflicting encoded value |
      | encode the field using a different wire type |
    Then every field and mutation pair is rejected before Station execution
    And inserting any unknown field or altering the signature is rejected

  Scenario: AttemptEventV1 exhaustive signed state and issue-anchor contract
    Given AttemptEventV1 has only schema/network/protocol, attempt-state signer key ID, deterministic event ID, job/request/attempt IDs, positive revision, previous AttemptEventV1 complete hash or canonical issued absence, closed event kind/resulting state, AttemptIssueCommitmentV1 complete hash, ExecutionGrantV1 complete hash, DispatchLeaseV1 complete hash or canonical direct absence, FundingSourceReservationV1 and FundingSourceReservationSetV1 complete hashes, GrantCompensationSnapshotV1 complete hash or canonical absence, hold ID/currency/unit/scale/amount/state, Tower/Station authority revisions, execution deadline, settlement-finalization/hold ceiling, event-kind evidence complete hash or canonical issued absence, terminal reason or canonical nonterminal absence, FundingSourceReleaseTransitionV1 stable ID/expected funding-ledger index or canonical nonrelease absence, and independently assigned Core attempt-ledger commit tuple
    Then event ID derives from strict JCS [AttemptEventV1-id-v1,network-ID,attempt-ID,revision], issued is revision 1/prior absence, every successor is current revision plus one/immediate prior under CAS, and exact replay is idempotent
    And issued commit tuple/index is byte-identical to its DispatchLeaseV1 and ExecutionGrantV1 preselected commitment ID/index/Core issue fields and the issued event's commitment signs both exact object hashes; only failed, expired, or cancelled terminal states bind a release stable ID/index, while settled forbids release
    When each universal mutation is independently applied to a common/state/authority/hold/revision/tuple/signer field or signature
    Then strict decoding, deterministic-ID, attempt-head CAS, purpose-signature, issue-tuple equality, funding relationship, or terminal-shape verification fails before dispatch, execution, settlement, or release

  Scenario: AttemptIssueCommitmentV1 exhaustive disclosure-safe mutation contract
    Given AttemptIssueCommitmentV1 has only schema/network/protocol, attempt-state signer key ID, deterministic commitment ID, job/attempt IDs, direct or joined origin kind, ExecutionGrantV1 complete hash, DispatchLeaseV1 complete hash or canonical direct absence, execution deadline, settlement-finalization ceiling, expected attempt-ledger index, and independently assigned Core issue time/global sequence
    Then commitment ID derives from strict JCS [AttemptIssueCommitmentV1-id-v1,network-ID,attempt-ID], the origin/hash shape is exact, and its index/tuple/object hashes equal the issued AttemptEventV1 and the preselected grant/lease fields
    When each universal mutation is independently applied or a hold/client/account/price/currency/funding field is inserted while retaining the signature
    Then strict decoding, deterministic-ID, purpose signature, event relationship, or privacy-schema verification fails before relay or execution

  Scenario: Direct ExecutionGrantV1 exhaustive origin-shape matrix
    Given a valid direct ExecutionGrantV1
    When each joined-only field is independently inserted as null, zero, empty, or a valid-looking value:
      | joined-only field |
      | Tower ID |
      | Tower certificate serial |
      | Tower session epoch |
      | Tower channel-binding hash |
      | Tower admission-lease stable series ID, lease ID, sequence, complete hash, and current Tower lifecycle revision/complete hash |
    Then every insertion is rejected instead of normalized to absence
    And inserting a grant-time compensation snapshot in a direct grant is rejected
    When each required direct field is independently removed, null, zero, empty, or replaced:
      | direct field |
      | origin kind direct |
      | direct Station grant sequence |
    Then every direct field and mutation pair is rejected

  Scenario: Joined ExecutionGrantV1 exhaustive origin-shape matrix
    Given a valid joined ExecutionGrantV1
    When each required joined field is independently removed, null, zero, empty, or replaced:
      | joined field |
      | origin kind joined |
      | Tower ID |
      | Tower certificate serial |
      | Tower session epoch |
      | Tower channel-binding hash |
      | Tower admission-lease stable series ID, lease ID, sequence, complete hash, and current Tower lifecycle revision/complete hash |
    Then every joined field and mutation pair is rejected
    And inserting a direct Station grant sequence is rejected

  Scenario Outline: Joined grant compensation-snapshot shape is exclusive
    Given a joined ExecutionGrantV1 is "<tier>"
    Then its snapshot shape is "<shape>"
    And every public grant requires one current published FundingAllocationPolicyV1 series/revision/complete hash byte-identical to its reservation, while compensated also requires the exact TowerCompensationPolicyV1 series/revision/complete hash byte-identical to its snapshot and uncompensated/direct grants require only that compensation-policy field to be absent

    Examples:
      | tier | shape |
      | compensated | one nonempty GrantCompensationSnapshotV1 complete hash is required |
      | uncompensated | the snapshot member is canonically absent and an empty, zero, null, or valid-looking hash is rejected |

  Scenario: GrantCompensationSnapshotV1 exhaustive field and mutation Cartesian product
    Given a compensated GrantCompensationSnapshotV1 has these independently addressable fields:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | job ID |
      | attempt ID |
      | Tower ID |
      | operator ID |
      | Tower lifecycle revision and complete hash |
      | program currency/unit/scale and payout rail |
      | CompensatedTowerCapabilityV1 type, stable series ID, revision, complete hash, effective Core tuple, and expiry Core tuple |
      | payout_identity PayoutEligibilityFactV1 stable series ID, revision, complete hash, effective Core tuple, and expiry Core tuple |
      | operator_account PayoutEligibilityFactV1 stable series ID, revision, complete hash, effective Core tuple, and expiry Core tuple |
      | accepted_terms PayoutEligibilityFactV1 stable series ID, revision, complete hash, effective Core tuple, and expiry Core tuple |
      | sanctions_screening PayoutEligibilityFactV1 stable series ID, revision, complete hash, effective Core tuple, and expiry Core tuple |
      | jurisdiction_determination PayoutEligibilityFactV1 stable series ID, revision, complete hash, effective Core tuple, and expiry Core tuple |
      | TaxProfileFactV1 stable series ID, revision, complete hash, verified result, effective/expiry, and fact-ledger commit Core tuples |
      | PayoutEligibilityPolicyV1 series/revision/complete hash and PayoutEligibilityEvidenceRegistryV1 complete hash |
      | PayoutPolicyV1 series/revision/complete hash and maximum tax-profile validity/age intervals |
      | TowerCompensationPolicyV1 series/revision/complete hash and expiry Core tuple |
      | FundingAllocationPolicyV1 series/revision/complete hash, closed allocation rule, effective Core tuple, and expiry Core tuple |
      | FundingSourceReservationV1 and FundingSourceReservationSetV1 complete hashes |
      | rate parts per million |
      | policy effective Core tuple |
      | grant issue Core authority time and sequence |
    When each universal mutation is independently applied to each field while retaining the ExecutionGrantV1 snapshot hash and signature
    Then every field and mutation pair fails snapshot-hash relationship or execution-grant signature verification
    And current-head equality, fact result/freshness, policy publication, capability/lifecycle state, expiry, duplicated-field equality, and every source purpose key's greatest-current-trust state are verified in the serializable grant-issue transaction
    And an unknown, duplicate, null, noncanonical, wrong-job, wrong-Tower, wrong-operator, stale source head, alternate capability series, or post-grant substitution is rejected before attempt creation

  Scenario: CompensatedTowerCapabilityV1 exhaustive authority and mutation contract
    Given CompensatedTowerCapabilityV1 has only schema/network/protocol, compensated-capability signer key ID, deterministic Tower-scoped series ID, positive revision, previous complete hash or canonical first absence, Tower ID, operator ID, Tower lifecycle revision/complete hash, program currency/unit/scale/rail scope, enabled/suspended/revoked state, exact payout_identity/operator_account/accepted_terms/sanctions_screening/jurisdiction_determination PayoutEligibilityFactV1 kind/series/revision/complete-hash/effective/expiry tuples, exact TaxProfileFactV1 series/revision/complete-hash/result/effective/expiry/commit tuples, PayoutEligibilityPolicyV1 series/revision/complete hash and evidence-registry complete hash, PayoutPolicyV1 series/revision/complete hash and maximum tax-profile validity/age intervals, TowerCompensationPolicyV1 series/revision/complete hash and maximum capability-validity interval, effective/expiry Core tuples, evidence complete hash, issue tuple, and independently assigned capability-ledger Core commit tuple
    When each universal mutation is independently applied to each common, state, fact, policy, lifecycle, time, evidence, signer, or revision field while retaining the capability signature
    Then strict decoding, deterministic-series derivation, immediate-prior/current-head CAS, purpose signature, active-current-key, fact/policy/lifecycle relationship, or bounded-expiry verification fails before a grant snapshot exists
    And enabled requires TaxProfileFactV1 payout-identity version byte-identical to the payout_identity PayoutEligibilityFactV1 payload and its tax jurisdiction byte-identical to both the jurisdiction_determination payload and program jurisdiction
    And capability issue and compensated grant issue compare-and-swap those exact payload relationships with all six fact heads, so a same-lock identity or jurisdiction race cannot produce an enabled capability or snapshot with mixed revisions
    And enabled alone grants compensated candidacy, suspended or revoked grants none, state is never inferred from admission, and exact replay is idempotent

  Scenario: FundingSourceLotV1 and its availability set have one exact signed state
    Given FundingSourceLotV1 and FundingSourceAvailabilityRangeSetV1 are encoded
    Then the lot has only schema/network/protocol, funding-source-ledger signer key ID, deterministic stable source-lot ID, positive revision, immediate prior complete hash or canonical creation absence, consumer-account stable ID, closed funding kind, currency/unit/scale, immutable original range/atoms, availability-set complete hash, available/reserved/consumed totals, expiry Core tuple or canonical nonexpiring absence, Core credit authority sequence, closed provenance union, closed causal-authority union containing exact creation credit-authority ID/hash with transition absence or exact FundingSourceReservationV1/FundingSourceSettlementTransitionV1/FundingSourceReleaseTransitionV1 stable ID and expected funding-ledger index with creation-authority absence, issue tuple, and independently assigned funding-ledger commit tuple
    And the availability set has only schema/network/protocol, source-lot ID/revision, owner, currency/unit/scale, immutable original range, sorted disjoint maximal available/reserved/consumed member ranges with state and owning reservation ID or canonical nonreserved absence, exact state totals, member count, and members
    And external_cash provenance has only ConsumerCashCreditAuthorityV1 ID/complete hash and canonical grant-credit absence; expiring_grant and nonexpiring_grant provenance have only PlatformGrantCreditV1 ID/complete hash and canonical cash-credit absence
    And stable source-lot ID derives from strict JCS [FundingSourceLotV1-id-v1,network-ID,provenance-kind,immutable-provenance-identity,source-range-start,source-range-end], creation is revision 1/prior absence bound to its credit authority, and every successor is current revision plus one/immediate prior under CAS and byte-identically names the reservation, settlement, or release transition that produces it
    And member ranges exactly partition the immutable original range once, totals equal their state sums, adjacent equal-state/equal-owner members are merged, and no cross-owner, relabeled-kind, duplicated-cash, overissued-grant, gap, overlap, or signer-created authenticated-payment fact is accepted

  Scenario: ConsumerCashCreditAuthorityV1 and PlatformGrantCreditV1 have exact independent preimages
    Given a funding source is backed by captured cash or a platform grant
    Then ConsumerCashCreditAuthorityV1 contains only schema/network/protocol, consumer-cash-credit signer key ID, deterministic authority ID, consumer-account stable ID, provider-neutral adapter/merchant/payment-source identity, exact AuthoritativePaymentRevisionV1 sequence/complete hash and ProviderPaymentEventIDSetV1 hash, one captured unallocated half-open source interval/atoms, currency/unit/scale, effective/expiry Core tuples, evidence complete hash, issue tuple, and independently assigned consumer-credit-ledger Core commit tuple
    And PlatformGrantCreditV1 contains only schema/network/protocol, platform-grant-credit signer key ID, deterministic grant-credit ID, consumer-account stable ID, grant-program ID, expiring_grant or nonexpiring_grant kind, exact atoms and currency/unit/scale, spend expiry or canonical nonexpiring absence, evidence complete hash, issue/use-expiry tuples, and independently assigned grant-credit-ledger Core commit tuple
    And cash authority ID derives from strict JCS [ConsumerCashCreditAuthorityV1-id-v1,network-ID,consumer-ID,adapter-ID,merchant-binding,payment-source-ID,source-start,source-end], while grant-credit ID derives from [PlatformGrantCreditV1-id-v1,network-ID,consumer-ID,program-ID,unique-issuance-nonce]
    And each immutable authority is consumed exactly once by one derived FundingSourceLotV1 creation, cash intervals cannot overlap any other authority, the current authenticated captured principal covers the interval, and all duplicated owner/kind/currency/range/amount/expiry/provenance fields are byte-identical
    When each universal mutation is independently applied to a field, provenance relationship, signer, authority tuple, one-use mapping, or signature
    Then strict decoding, deterministic-ID, purpose-signature, payment/grant authority, nonoverlap, or one-use verification fails before a funding lot or reservation exists

  Scenario: FundingSourceReservationSetV1 and FundingSourceReservationV1 are mutation-exhaustive
    Given a reservation is created before its bound ExecutionGrantV1
    Then FundingSourceReservationSetV1 contains only schema/network/protocol, job/attempt/consumer, currency/unit/scale, exact authorized hold and maximum cost, FundingAllocationPolicyV1 series/revision/complete hash and allocation rule, member count/total, and its complete ordered members
    And each member contains only source-lot stable ID, transaction-start revision/complete hash, consumer, kind, currency/unit/scale, expiry-or-absence, Core credit sequence, provenance type/ID/complete hash, selected available half-open range, and exact atoms
    And FundingSourceReservationV1 contains only schema/network/protocol, funding-source-ledger signer key ID, deterministic reservation ID, job/attempt/consumer, set complete hash, exact transaction-start and resulting FundingSourceHeadSetV1 complete hashes, hold/deadline, policy reference, issue tuple, and independently assigned funding-ledger commit tuple
    And reservation ID derives from strict JCS [FundingSourceReservationV1-id-v1,network-ID,attempt-ID], members follow the exact published policy order and lowest-range order, sum exactly to the hold/maximum cost, and the serializable commit compare-and-swaps every prior head and changes only selected available ranges to reserved under that reservation ID
    When each universal mutation is independently applied to a lot, availability member, reservation member, set scope/order/total, provenance, head, state, signer, policy, tuple, or signature
    Then strict decoding, deterministic-ID, source-purpose signature, current-head, provenance, order, conservation, or grant/set relationship verification fails before a grant, debit, settlement, Station earning, or compensation candidate exists

  Scenario: FundingSourceHeadSetV1 has one owner, phase, and exact source projection
    Given a funding reservation, settlement, or release transition binds a prior and resulting FundingSourceHeadSetV1
    Then each set contains only schema/network/protocol, transition type/stable ID, prior or result phase, attempt/reservation ID, consumer, currency/unit/scale, member count, available/reserved/consumed totals, and complete ordered members
    And each member contains only source-lot stable ID, revision, FundingSourceLotV1 complete hash, FundingSourceAvailabilityRangeSetV1 complete hash, immutable original range/atoms, and available/reserved/consumed totals
    And members sort by bytewise source-lot ID, occur exactly once for every lot touched by the transition, totals equal the member sums, prior members equal transaction-start current heads, and result members equal the directly signed successor heads
    And owner, phase, member, order, count, total, omitted touched head, extra untouched head, stale revision, or alternate hash mismatch rejects the whole transition

  Scenario: Funding settlement and release transitions close every reserved range exactly once
    Given FundingSourceReservationV1 with ordered total C reaches successful settlement cost A or a no-settlement terminal release
    Then FundingSourceDispositionRangeSetV1 contains only schema/network/protocol, reservation/attempt, consumed or released disposition, currency/unit/scale, member count/total, and ordered members containing reservation ordinal, source-lot ID, reserved range, selected subrange, and atoms
    And successful settlement has consumed set equal to the exact ordered prefix A and released set equal to the exact ordered suffix C minus A, while no-settlement has the canonical empty consumed set and a released set equal to all C
    And FundingSourceSettlementTransitionV1 or FundingSourceReleaseTransitionV1 contains only schema/network/protocol, funding-source-ledger signer key ID, deterministic transition ID, closed settled or released_without_settlement result, job/attempt/consumer, reservation and reservation-set complete hashes, actual cost or canonical no-settlement zero, consumed/released disposition-set hashes and totals, prior/result FundingSourceHeadSetV1 hashes, SettlementReceiptV2 stable ID/expected settlement-ledger index with canonical terminal-attempt absence or exact failed/expired/cancelled AttemptEventV1 ID/revision/complete hash/state/reason/commit tuple with canonical receipt absence, issue tuple, and independently assigned funding-ledger Core commit tuple
    And each resulting FundingSourceLotV1 successor names only the transition stable ID/expected funding-ledger index in its signed state to avoid a hash cycle; the transition binds the successor hashes, and SettlementReceiptV2 later binds the transition complete hash
    And settlement requires the receipt shape and forbids a terminal AttemptEvent, while release requires the exact current terminal AttemptEvent whose signed bytes preselect that release stable ID/expected index and forbids settled or any receipt; one serializable atomic group compare-and-swaps attempt/reservation/source heads
    And that group consumes/releases every reserved range, updates each source head, and records the reservation terminal exactly once; exact replay is idempotent and no later transition can spend or release the same range
    When any source, range, prefix/suffix boundary, total, head, result, receipt/terminal authority, tuple, signer, or relationship is mutated
    Then the transition is rejected before consumer debit, Station credit, receipt finalization, Tower candidacy, or hold release
