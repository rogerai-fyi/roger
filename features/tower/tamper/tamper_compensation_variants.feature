# PROPOSED SPEC — founder approval is required before step definitions or implementation.
#
# Scope: exhaustive Cartesian tamper matrices for compensation variants, affected-state projections, payout preparation and send fence, dust, hold and classification references.
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
  Scenario: Every non-entitlement compensation variant has one mutation-exhaustive common envelope
    Given each lot_create, lot_cancel, maturity, dust_reclassify, withhold, release_hold, forfeit, enforcement_coverage_derecognize, partition_lot, prepare_payout, abort_preparation, submit_payout, void_payout, submitted_negative_fence, payout_succeeded, payout_confirmed_failed, debt_create, debt_offset, debt_reopen, and debt_writeoff event has these independently addressable signed common fields:
      | common field |
      | schema version |
      | network ID |
      | protocol version |
      | currency |
      | unit |
      | scale |
      | compensation event ID |
      | compensation event type |
      | closed reason code |
      | causal event ID |
      | TowerIDScopeSetV1 complete hash |
      | operator ID |
      | compensation state-machine kind |
      | AffectedStateEntityKeySetV1 complete hash |
      | prior AffectedStateProjectionSetV1 complete hash |
      | resulting AffectedStateProjectionSetV1 complete hash excluding current-group event hashes |
      | prior committed CompensationControlTotalLeafV1 complete hash |
      | resulting ControlValueProjectionV1 complete hash |
      | JournalPostingSetV1 complete hash |
      | journal-template version and closed disposition ID |
      | ledger sequence |
      | previous Roger compensation-ledger entry hash or RogerLedgerGenesisV1 complete hash at first sequence |
      | Core-observed event time |
      | Core authority sequence |
      | compensation signer key ID |
    When each universal mutation is independently applied to each common field for each listed event type while its compensation signature remains
    Then every event-type, common-field, and mutation tuple fails strict decoding or compensation-signature verification

  Scenario: AffectedStateProjectionSetV1 has one exact acyclic event-local preimage
    Given any compensation event signs prior and resulting AffectedStateProjectionSetV1 complete hashes plus one AffectedStateEntityKeySetV1 complete hash
    When either state set is encoded
    Then its preimage is exactly one strict JCS object containing schema/network/protocol, event stable ID/expected group index, currency/unit/scale, canonical member count, prior or resulting phase tag, and the complete ordered member array
    And each member contains only closed state kind, the exact state-kind-specific EntityKeyV1 JCS array defined below, absent or present tag, and the complete strict state value-projection object when present
    And the closed state kinds are SourceIntervalValueProjectionV1, EntitlementAggregateValueProjectionV1, ApplicationRangeValueProjectionV1, PayoutLotValueProjectionV1, DebtRangeValueProjectionV1, DebtRecoveryApplicationValueProjectionV1, PendingSubmittedNegativeValueProjectionV1, EnforcementDispositionCoverageValueProjectionV1, and DustCycleValueProjectionV1
    And members sort by bytewise comparison of the exact UTF-8 JCS EntityKeyV1 bytes, reject duplicate keys, and count equals array length; the exact empty members array is the only zero-member form
    And prior and resulting sets have the identical ordered entity-key sequence: creation uses a prior absent member and resulting present projection, mutation uses both present, and no compensation state is physically deleted
    And AffectedStateEntityKeySetV1 is exactly the strict JCS projection of that same ordered key sequence with schema/network/protocol, event stable ID/group index, currency/unit/scale, and count but without state values
    And membership exhausts every protocol-defined mutable compensation-state CAS authority entity owned and mutated by this event's closed variant plus each event-owned target, created/result object, partition parent/child, and hold/dust transition, with no unrelated entity
    And a journal state key normally belongs to that same owner event; the only exception is payout_succeeded's pending-recourse-to-debt bridge, whose PendingSubmittedNegative and DebtRange keys belong exclusively to its prescribed later debt_create child's affected-state sets and are relationship-checked through PendingSuccessResolutionDescriptorSetV1, DebtCreateResultSetV1, signed group indices, and the group-final control projection
    And entitlement_delta owns only its source/aggregate and plan-authority mutations: nested ApplicationDescriptorSetV1 targets/results belong exclusively to their application owner and any exact helper subgroup, so they never enter the parent entitlement_delta affected-state set merely because the parent signs the plan
    And immutable external signed authorities and observations such as RailResultValueProjectionV1, payout/eligibility/tax decisions, policies, evidence, and instructions are excluded from this affected-state set because their exact IDs/complete hashes are bound directly by the variant and their applicable control set
    And incidental SQL reads, index probes, locks, retry queries, cache entries, or implementation-specific dependency rows never enter the signed set
    And current-group stable event IDs/indices and intermediate value projections are allowed, but current-group event bytes/signatures/complete hashes, current/resulting ledger positions/hashes, post-commit full-object hashes, and resulting control-leaf hash are forbidden from resulting members

  Scenario Outline: Every affected state kind has one exact EntityKeyV1 projection
    Given affected state kind "<kind>" uses exact EntityKeyV1 JCS array "<key>"
    When the affected-key, prior-set, or resulting-set relationship is verified
    Then every key component is copied exactly from its strict value projection, canonical nonnegative integer components retain canonical decimal text inside JCS, and no omitted, substituted, or extra coordinate is allowed
    And root absence uses the exact empty string only in the named parent position; there is no generic range, inferred ID, or implementation-selected coordinate

    Examples:
      | kind | key |
      | SourceIntervalValueProjectionV1 | [kind,SettlementReceiptV2-hash,funding-slice-ID,source-lot-ID,source-interval-start,source-interval-end,cumulative-cost-start,cumulative-cost-end] |
      | EntitlementAggregateValueProjectionV1 | [kind,aggregate-ID] |
      | ApplicationRangeValueProjectionV1 | [kind,reconciliation-transaction-ID,application-ID,parent-entitlement-delta-ID,plan-local-start,plan-local-end] |
      | PayoutLotValueProjectionV1 | [kind,payout-lot-ID,parent-or-empty,source-range-start,source-range-end] |
      | DebtRangeValueProjectionV1 | [kind,debt-range-ID,parent-or-empty,range-start,range-end] |
      | DebtRecoveryApplicationValueProjectionV1 | [kind,recovery-application-ID,parent-or-empty,source-range-start,source-range-end,target-range-start,target-range-end] |
      | PendingSubmittedNegativeValueProjectionV1 | [kind,pending-ID,negative-entitlement-ID,plan-local-start,plan-local-end,submitted-lot-ID,submitted-range-start,submitted-range-end] |
      | EnforcementDispositionCoverageValueProjectionV1 | [kind,coverage-ID,parent-or-empty,source-lot-ID,source-range-start,source-range-end] |
      | DustCycleValueProjectionV1 | [kind,operator-ID,generation,dust-cycle-ID] |

  Scenario: Affected state set membership and mutation checks are exhaustive
    Given any compensation event has its closed variant shape and application descriptor or nonapplication authority
    When a prior/result member or affected key is omitted, added, duplicated, reordered, assigned another kind/ID/range/presence phase, encoded as a hash instead of the full value projection, or disagrees with the exact variant target/result/CAS entities
    Then strict decoding or the affected-key/prior-set/result-set/event-signature relationship fails before any state, posting, control, or rail action

  Scenario Outline: Every non-entitlement compensation variant field is mutation-exhaustive
    Given TowerCompensationReceiptV1 event type "<type>" requires independently addressable signed variant field "<field>"
    When each universal mutation is independently applied to that field while the compensation signature remains
    Then strict decoding or compensation-signature verification fails before any state or money transition

    Examples:
      | type | field |
      | lot_create | entitlement_delta complete hash |
      | lot_create | ApplicationDescriptorSetV1 complete hash, index, and descriptor complete hash |
      | lot_create | ApplicationRealizedValueProjectionSetV1 complete hash |
      | lot_create | positive source-event half-open atom range |
      | lot_create | one created payout-lot stable ID/range/atoms value-projection hash |
      | lot_create | SettlementReceiptV2 ID/complete hash and funding-allocation-array complete hash, exact funding-slice ID/strict slice fields, entitlement-aggregate ID, source entitlement_delta stable ID/expected group index, and source-event half-open atom range |
      | lot_create | uniquely selected MaturityPolicyV1 series/revision/complete hash |
      | lot_create | exact causal AuthoritativePaymentRevisionSetV1 member's source revision sequence/complete hash/capture-maturity Core tuple and deterministically derived required maturity tuple |
      | lot_cancel | entitlement_delta complete hash |
      | lot_cancel | ApplicationDescriptorSetV1 complete hash, index, and descriptor complete hash |
      | lot_cancel | ApplicationRealizedValueProjectionSetV1 complete hash |
      | lot_cancel | negative source-event half-open atom range |
      | lot_cancel | transaction-start target payout-lot ID/complete hash and half-open atom range |
      | lot_cancel | PayoutLotChildRangeSetV1 complete hash or canonical whole-lot result |
      | lot_cancel | PayoutLotPartitionHelperSetV1 complete hash including canonical empty |
      | lot_cancel | prior/result PayoutLotValueProjectionV1 complete hashes |
      | lot_cancel | dust-cycle prior/result/terminal binding or canonical non-dust absence |
      | lot_cancel | dust-cycle generation and previous terminal-cycle hash or canonical non-dust absence |
      | maturity | PayoutLotRangeSetV1 complete hash |
      | maturity | MaturityPolicyV1 series/revision/complete hash |
      | maturity | MaturityAuthorityV1 ID/complete hash and actual Core tuple |
      | maturity | MaturitySourceRevisionSetV1 complete hash, member count, and total atoms |
      | maturity | current payout-eligibility decision series ID |
      | maturity | current payout-eligibility decision revision and complete hash |
      | maturity | current payout-eligibility result and expiry tuple |
      | maturity | dust-cycle ID or canonical not-below-threshold absence |
      | maturity | dust-cycle generation and previous terminal-cycle hash or canonical first/no-cycle absence |
      | maturity | dust-cycle revision, prior hash, and resulting hash or canonical absence |
      | maturity | first-below-threshold Core authority tuple or canonical absence |
      | maturity | payout-policy series/revision/complete hash or canonical absence |
      | maturity | minimum payout rail-minor units or canonical absence |
      | maturity | maximum dust carry interval or canonical absence |
      | maturity | exact dust-review deadline or canonical absence |
      | maturity | DustLotReferenceSetV1 complete hash, member count, and total share atoms or canonical absence |
      | dust_reclassify | PayoutLotRangeSetV1 complete hash |
      | dust_reclassify | prior payout-policy series/revision/complete hash |
      | dust_reclassify | current payout-policy series/revision/complete hash and applicability tuple |
      | dust_reclassify | prior/current canonical payable-selection atoms B, accounting quanta per rail minor unit Q, checked share-atoms-per-minor K equal to 1000000 times Q, floor(B divided by K), minimum_payout_minor, and below-threshold result |
      | dust_reclassify | dust-cycle ID and prior/resulting revision/hash or canonical first-cycle absence |
      | dust_reclassify | dust-cycle generation and previous terminal-cycle hash or canonical first-cycle absence |
      | dust_reclassify | first-below-threshold Core tuple, carry interval, and exact review deadline |
      | dust_reclassify | resulting DustLotReferenceSetV1 complete hash, member count, and total share atoms |
      | withhold | PayoutLotRangeSetV1 complete hash |
      | withhold | exact hold kind, closed reason/result, and kind-conditional authority object type/ID plus preexisting complete hash or current-group stable ID/expected index |
      | withhold | authority series/revision/prior hash or canonical nonrevisioned absence copied from the external authority, or compensation-created stable hold-series ID/initial authorized range-lineage digest/positive revision/prior withhold-event ID/complete hash or canonical first-revision absence |
      | withhold | external authority evidence complete hash with canonical compensation-causal absence, or compensation-created immutable causal-evidence complete hash plus current revision-evidence complete hash, and effective/expiry-or-review Core tuples in the exact source/kind shape |
      | withhold | prior current AuthorityRangeScopeSetV1 stable ID/complete hash or canonical first-materialization absence plus resulting current-event-owned AuthorityRangeScopeSetV1 stable ID/complete hash whose ordered members equal the exact registered stable-lineage/range projection of the applicable PayoutLotRangeSetV1, with lot ID, parent-or-empty, range, atoms, Tower, and SettlementReceiptV2 fields byte-identical |
      | withhold | prior and resulting HoldReferenceSetV1 complete hashes plus resulting ClassificationAuthoritySetV1 complete hash and exact resulting state |
      | withhold | AbortVoidHoldPlanSetV1 parent stable ID/complete hash/member ordinal or canonical standalone absence |
      | withhold | dust-cycle ID and prior/resulting revision/hash or canonical non-dust absence |
      | withhold | dust-cycle generation or canonical non-dust absence |
      | withhold | dust-review deadline or canonical non-dust absence |
      | release_hold | PayoutLotRangeSetV1 complete hash |
      | release_hold | prior HoldReferenceSetV1 complete hash |
      | release_hold | exact current resolved HoldReferenceV1 complete bytes/hash plus its kind-conditional authority object type/ID/complete hash and series/revision/prior shape, including hold-series/current withhold-event fields only for compensation-created kinds |
      | release_hold | prior and release/result AuthorityRangeScopeSetV1 stable IDs/complete hashes with equal range coordinates/atoms and prescribed distinct owners/decision tuples |
      | release_hold | HoldResolutionAuthorityReferenceV1 complete hash |
      | release_hold | resulting HoldReferenceSetV1 complete hash |
      | release_hold | resulting ClassificationAuthoritySetV1 complete hash and exact resulting lot state |
      | release_hold | dust-cycle ID and prior/resulting revision/hash or canonical non-dust absence |
      | release_hold | dust-cycle generation or canonical non-dust absence |
      | release_hold | unchanged first-below-threshold tuple and dust-review deadline or canonical non-dust absence |
      | forfeit | EnforcementAuthorizedRangeSetV1 complete hash copied from the bound decision |
      | forfeit | forfeiture decision ID |
      | forfeit | forfeiture decision complete hash |
      | forfeit | compensation-forfeiture decision signer key ID |
      | forfeit | CompensationEnforcementPolicyV1 and CompensationEnforcementFindingV1 series/revision/complete hashes copied from the bound decision |
      | forfeit | HistoricalAcceptedTermsAuthoritySetV1 complete hash copied from the bound decision |
      | forfeit | EnforcementCoverageResultSetV1 complete hash |
      | forfeit | PayoutLotPartitionHelperSetV1 complete hash including canonical empty |
      | forfeit | dust-cycle ID and prior/resulting revision/hash or canonical non-dust absence |
      | forfeit | dust-cycle generation or canonical non-dust absence |
      | forfeit | resulting DustLotReferenceSetV1 complete hash/member count/atoms and terminal state/reason or canonical non-dust absence |
      | enforcement_coverage_derecognize | entitlement_delta complete hash |
      | enforcement_coverage_derecognize | ApplicationDescriptorSetV1 complete hash, index, and descriptor complete hash |
      | enforcement_coverage_derecognize | ApplicationRealizedValueProjectionSetV1 complete hash |
      | enforcement_coverage_derecognize | negative source-event half-open atom range |
      | enforcement_coverage_derecognize | forensic payout-lot ID/complete hash and half-open atom range |
      | enforcement_coverage_derecognize | EnforcementDispositionCoverage ID, prior hash, and resulting hash |
      | enforcement_coverage_derecognize | unpaid_forfeiture or paid_clawback coverage kind |
      | enforcement_coverage_derecognize | forfeited_entitlement or paid_clawback_enforcement_coverage journal source account selected only by coverage kind |
      | enforcement_coverage_derecognize | exact derecognized share atoms |
      | partition_lot | parent payout-lot ID |
      | partition_lot | parent payout-lot complete hash |
      | partition_lot | parent share atoms |
      | partition_lot | PayoutLotChildRangeSetV1 complete hash whose parent snapshot equals this event's transaction-start parent |
      | partition_lot | PayoutLotPartitionHelperSetV1 complete hash and this helper's ordinal |
      | partition_lot | exact ApplicationResultRangeSetV1, PayoutLotAtomRangeSetV1, or EnforcementAuthorizedRangeSetV1 consumer authority-set kind and complete hash |
      | partition_lot | intended consumer event kind/stable ID/expected group index |
      | partition_lot | partition purpose |
      | partition_lot | dust-cycle prior/result binding or canonical non-dust absence |
      | prepare_payout | payout ID |
      | prepare_payout | payout-preparation ID |
      | prepare_payout | TowerLifecyclePayoutAuthoritySetV1 complete hash |
      | prepare_payout | PayoutLotAtomRangeSetV1 complete hash |
      | prepare_payout | PayoutLotPartitionHelperSetV1 complete hash including canonical empty |
      | prepare_payout | payout-identity verification version |
      | prepare_payout | immutable destination fingerprint |
      | prepare_payout | payout-eligibility decision series ID |
      | prepare_payout | payout-eligibility decision revision |
      | prepare_payout | payout-eligibility decision complete hash |
      | prepare_payout | payout-policy series ID |
      | prepare_payout | payout-policy revision and complete hash |
      | prepare_payout | minimum payout rail-minor units |
      | prepare_payout | preparation authorization deadline Core time and sequence |
      | prepare_payout | maximum dust carry interval and review deadline or canonical no-cycle absence |
      | prepare_payout | dust-cycle ID, prior/resulting revision/hash, and first anchor or canonical no-cycle absence |
      | prepare_payout | dust-cycle generation and previous terminal-cycle hash or canonical first/no-cycle absence |
      | prepare_payout | dust-cycle terminal state/reason or canonical open/no-cycle absence |
      | prepare_payout | remainder DustLotReferenceSetV1 complete hash, member count, and atoms or canonical zero-remainder absence |
      | prepare_payout | payout rail |
      | prepare_payout | accounting quanta per rail minor unit |
      | prepare_payout | selected available share atoms |
      | prepare_payout | gross reserved share atoms |
      | prepare_payout | gross rail-minor units |
      | prepare_payout | retained share-atom remainder |
      | abort_preparation | payout ID |
      | abort_preparation | payout-preparation ID and complete hash |
      | abort_preparation | PayoutLotAtomRangeSetV1 complete hash |
      | abort_preparation | abort reason code |
      | abort_preparation | no-instruction and no-send-fence database authority tuple |
      | abort_preparation | AbortVoidHoldPlanSetV1 complete hash including canonical empty and final child group index or canonical no-child absence |
      | submit_payout | payout ID |
      | submit_payout | TowerPayoutInstructionV1 complete hash |
      | submit_payout | PayoutLotAtomRangeSetV1 complete hash |
      | submit_payout | stable rail idempotency key |
      | submit_payout | current payout-eligibility decision series/revision/hash |
      | submit_payout | current zero tax-decision series/revision/hash and applicability tuple |
      | submit_payout | current payout-policy series/revision/hash |
      | submit_payout | current TowerLifecyclePayoutAuthoritySetV1 complete hash |
      | submit_payout | unexpired preparation-authorization deadline Core tuple |
      | submit_payout | send-fence ID |
      | submit_payout | send-fence Core authority time |
      | submit_payout | send-fence Core authority sequence |
      | void_payout | payout ID |
      | void_payout | TowerPayoutInstructionV1 complete hash |
      | void_payout | PayoutLotAtomRangeSetV1 complete hash |
      | void_payout | void reason code |
      | void_payout | no-send-fence database authority tuple |
      | void_payout | AbortVoidHoldPlanSetV1 complete hash including canonical empty and final child group index or canonical no-child absence |
      | submitted_negative_fence | entitlement_delta complete hash |
      | submitted_negative_fence | ApplicationDescriptorSetV1 complete hash, index, and descriptor complete hash |
      | submitted_negative_fence | ApplicationRealizedValueProjectionSetV1 complete hash |
      | submitted_negative_fence | negative source-event half-open atom range |
      | submitted_negative_fence | TowerPayoutInstructionV1 and PayoutSendFenceV1 complete hashes |
      | submitted_negative_fence | one transaction-start reserved_submitted payout-lot ID/complete hash and half-open range |
      | submitted_negative_fence | PendingSubmittedNegative ID, prior hash, and resulting hash |
      | submitted_negative_fence | exact pending submitted share atoms |
      | payout_succeeded | payout ID |
      | payout_succeeded | TowerPayoutInstructionV1 complete hash |
      | payout_succeeded | PayoutLotAtomRangeSetV1 complete hash |
      | payout_succeeded | rail result revision |
      | payout_succeeded | rail result ID |
      | payout_succeeded | rail result complete hash |
      | payout_succeeded | platform-account binding hash |
      | payout_succeeded | authenticated net transfer amount |
      | payout_succeeded | rail result authority time |
      | payout_succeeded | payout-eligibility incident complete hash or canonical absence |
      | payout_succeeded | TaxDecisionCorrectionIncidentV1 complete hash or canonical absence |
      | payout_succeeded | PendingSubmittedNegativeSetV1 complete hash or canonical no-pending absence |
      | payout_succeeded | PendingSuccessResolutionDescriptorSetV1 complete hash or canonical no-pending absence |
      | payout_confirmed_failed | payout ID |
      | payout_confirmed_failed | TowerPayoutInstructionV1 complete hash |
      | payout_confirmed_failed | PayoutLotAtomRangeSetV1 complete hash |
      | payout_confirmed_failed | rail result revision |
      | payout_confirmed_failed | rail failure ID |
      | payout_confirmed_failed | rail failure complete hash |
      | payout_confirmed_failed | platform-account binding hash |
      | payout_confirmed_failed | authenticated failure code |
      | payout_confirmed_failed | rail result authority time |
      | payout_confirmed_failed | payout-eligibility incident complete hash or canonical absence |
      | payout_confirmed_failed | TaxDecisionCorrectionIncidentV1 complete hash or canonical absence |
      | payout_confirmed_failed | current PayoutEligibilityDecisionV1 series/revision/complete hash |
      | payout_confirmed_failed | PendingSubmittedNegativeSetV1 complete hash or canonical no-pending absence |
      | payout_confirmed_failed | PendingFailureResolutionDescriptorSetV1 complete hash for every failure |
      | payout_confirmed_failed | dust-cycle ID/generation, prior/result value-projection hashes, first anchor/deadline, and terminal reason or canonical non-dust absence |
      | debt_create | conditional paid-source mapping: one preexisting paid lot ID/full hash/range for economic_reversal, one transaction-start reserved_submitted lot ID/full hash/range plus resulting paid PayoutLotValueProjectionV1 hash for resolved_submitted_success, or the decision's canonical ordered preexisting paid-lot ID/full-hash/range set for paid_enforcement_clawback |
      | debt_create | economic_reversal, resolved_submitted_success, or paid_enforcement_clawback origin |
      | debt_create | negative entitlement event ID or canonical paid-enforcement absence |
      | debt_create | negative entitlement event complete hash or canonical paid-enforcement absence |
      | debt_create | ApplicationDescriptorSetV1 complete hash, index, and descriptor complete hash or canonical non-economic-reversal absence |
      | debt_create | ApplicationRealizedValueProjectionSetV1 complete hash or canonical non-economic-reversal absence |
      | debt_create | negative source-event half-open atom range or canonical paid-enforcement absence |
      | debt_create | DebtCreateResultSetV1 complete hash |
      | debt_create | PendingSubmittedNegative ID/prior and resulting value-projection hashes, pending-success descriptor hash/index, and resolved_paid state or canonical other-origin absence |
      | debt_create | authenticated rail-success complete hash or canonical other-origin absence |
      | debt_create | CompensationForfeitureDecisionV1 complete hash or canonical non-enforcement absence |
      | debt_create | EnforcementCoverageResultSetV1 complete hash or canonical non-enforcement absence |
      | debt_create | originating TowerPayoutInstructionV1 complete hash and its exact accepted_terms PayoutEligibilityFactV1 stable series/revision/complete hash/effective/expiry tuples |
      | debt_create | originating instruction's exact PayoutPolicyV1 series/revision/complete hash/applicability/expiry and fixed same-currency-offset-only/external-debit-forbidden fields |
      | debt_offset | positive entitlement event ID |
      | debt_offset | positive entitlement event complete hash |
      | debt_offset | ApplicationDescriptorSetV1 complete hash, index, and descriptor complete hash |
      | debt_offset | ApplicationRealizedValueProjectionSetV1 complete hash |
      | debt_offset | exact offset share atoms |
      | debt_offset | prior debt share atoms |
      | debt_offset | residual debt share atoms |
      | debt_offset | positive source-event half-open atom range |
      | debt_offset | transaction-start target DebtRange ID/complete hash and half-open atom range |
      | debt_offset | canonical target DebtRange root-priority and leaf tie-break tuple |
      | debt_offset | DebtRangeChildRangeSetV1 complete hash or canonical whole-range result |
      | debt_offset | DebtRecoveryApplication ID, prior hash, and resulting hash |
      | debt_reopen | entitlement_delta complete hash |
      | debt_reopen | ApplicationDescriptorSetV1 complete hash, index, and descriptor complete hash |
      | debt_reopen | ApplicationRealizedValueProjectionSetV1 complete hash |
      | debt_reopen | negative source-event half-open atom range |
      | debt_reopen | transaction-start DebtRecoveryApplication ID/range and prior/resulting hashes |
      | debt_reopen | transaction-start recovered DebtRange ID/range and prior/resulting hashes |
      | debt_reopen | immutable affine source-to-target offset and exact mapped selected source/target ranges |
      | debt_reopen | DebtRecoveryApplicationChildRangeSetV1 and DebtRangeChildRangeSetV1 complete hashes or canonical whole-range results |
      | debt_writeoff | DebtWriteoffAuthorizedRangeSetV1 complete hash copied from the bound decision |
      | debt_writeoff | DebtWriteoffResultSetV1 complete hash |
      | debt_writeoff | writeoff decision ID |
      | debt_writeoff | writeoff decision complete hash |
      | debt_writeoff | debt-writeoff decision signer key ID |
      | debt_writeoff | exact writeoff share atoms |
      | debt_writeoff | DebtWriteoffPolicyV1 and DebtWriteoffApprovalV1 series/revision/complete hashes copied from the bound decision |
      | debt_writeoff | HistoricalAcceptedTermsAuthoritySetV1 complete hash copied from the bound decision |

  Scenario: Compensation variant shapes reject cross-type and omitted fields exhaustively
    Given the exact common-field table and event-type-to-variant-field rows above are the closed TowerCompensationReceiptV1 schema
    When any required common or mapped variant field is independently absent, null, or moved to another event type for which no row maps it
    Then strict decoding rejects the object before compensation-signature or state-transition authority
    And every unlisted field, event type, state kind, or variant combination is rejected rather than ignored

  Scenario: Submitted-negative failure resolution descriptors partition each returned lot once
    Given each strict member of PendingFailureResolutionDescriptorSetV1 is for one transaction-start submitted leaf lot and binds PendingFailureSegmentSetV1, PendingFailureLotResultSetV1 with each result's exact ClassificationAuthoritySetV1/HoldReferenceSetV1/dust authority, parent dust transition or canonical non-dust absence, exact affected/unaffected atoms, and journal allocation
    When each universal mutation is applied to any descriptor field or a descriptor is omitted, duplicated, reordered, or inserted while the payout_confirmed_failed signature remains
    Then strict decoding or the PendingFailureResolutionDescriptorSetV1 complete-hash relationship fails
    And with or without pending recourse the descriptor set contains exactly one descriptor for every transaction-start submitted leaf lot, including an empty pending-segment map for an unaffected lot, and each descriptor's results conserve that full lot once
    And a whole-lot affected or unaffected result uses the original lot stable ID with its one full range, while only a true mixed or partial result uses ordered immutable partition-child IDs/ranges whose atoms conserve the parent
    And descriptors sort by the transaction-start reserved_submitted leaf-lot stable key [payout-lot-ID,parent-or-empty,range-start,range-end], while each descriptor's pending segments sort by [PendingSubmittedNegative-ID,affected-range-start,affected-range-end]
    And the union of all segment maps exhausts every range in the transaction-start ordered pending set exactly once, each PendingSubmittedNegative becomes resolved_failed only after all its segments resolve, affected ranges become cancelled, and unaffected ranges carry the complete canonical set of all current holds
    And no two pending records partition the same full lot independently and no partition_lot or lot_cancel event posts the same resolution

  Scenario: Submitted-negative success descriptors and one-plus-N child events are mutation-exhaustive
    Given payout_succeeded binds PendingSubmittedNegativeSetV1 with N records and PendingSuccessResolutionDescriptorSetV1 with exactly one strict member per record
    When any descriptor field or set member is mutated, omitted, duplicated, reordered, or inserted, or any resolved_submitted_success debt_create child binds a different descriptor/pending/range
    Then strict decoding, payout_succeeded signature verification, PendingSuccessResolutionDescriptorSetV1 hash verification, DebtCreateResultSetV1 verification, or child relationship verification fails
    And N greater than zero requires exactly N consecutive child events after payout_succeeded in ascending pending stable-key order, every descriptor and pending record is consumed once, every prescribed DebtRange is created once, and the group commits atomically
    And payout_succeeded owns the full pending-recourse-to-debt journal posting while every child has canonical zero postings; N equal to zero requires canonical set absence and no child

  Scenario: DebtRecoveryApplication partial reversal preserves one affine map
    Given DebtRecoveryApplicationV1 maps source range [s0,s1) to recovered DebtRange target [t0,t1) with equal positive length and offset t0 minus s0
    When debt_reopen selects source subrange [r0,r1)
    Then its only valid mapped target is [t0 plus r0 minus s0,t0 plus r1 minus s0), and aligned selected/remainder children preserve that offset and conserve both full ranges
    And a source range outside the parent, unequal source/target length, overflow, underflow, shifted target, nonaligned child, alternate debt, or second use fails the descriptor, projection, or exclusion relationship before state or postings change

  Scenario: Debt offset target priority is canonical and mutation-exhaustive
    Given every outstanding DebtRange leaf has priority tuple [root-create-global-sequence,root-event-group-index,root-stable-ID,leaf-range-start,leaf-range-end,leaf-stable-ID]
    When a debt_offset descriptor and event bind one target
    Then its tuple is the least eligible same-operator same-currency outstanding leaf, partition children inherit the first three root components, and earlier ranges are exhausted before later ranges or lot_create
    And mutating or omitting a tuple component, selecting a later target, using database collation/insertion order, combining multiple root targets in one descriptor, or assigning excess atoms to debt_offset fails the plan/event/projection relationship

  Scenario: Partial application boundary direction is mutation-exhaustive
    Given reverse recognition ends inside one surviving positive source leaf or debt priority ends inside one outstanding DebtRange leaf
    When a planner shifts the selected interval while preserving its length, parent, atoms, and every signature-ready field
    Then reverse recognition accepts only the maximal high-end suffix ending at that source leaf's range end
    And debt_offset accepts only the minimal low-end prefix beginning at that DebtRange leaf's range start
    And an interior interval, opposite-end interval, shifted child boundary, alternate remainder geometry, overflow, or underflow fails ApplicationDescriptorSetV1, ApplicationResultRangeSetV1, child-set, and realized/AffectedState relationships before commit

  Scenario: PayoutPreparationV1 is the exact signed prepare_payout receipt identity
    Given one TowerCompensationReceiptV1 passes the common-envelope and prepare_payout-variant Cartesian checks
    When another object refers to PayoutPreparationV1
    Then its payout-preparation ID and complete hash identify those exact canonical signed receipt bytes
    And there is no second preparation schema, mutable projection, unsigned alias, or ID-only relationship

  Scenario: PayoutSendFenceV1 is the exact committed submit_payout receipt identity
    Given one TowerCompensationReceiptV1 passes the common-envelope and submit_payout-variant Cartesian checks and commits before the rail call
    When another object refers to PayoutSendFenceV1
    Then its send-fence ID is byte-identical to that submit_payout compensation-event stable ID, its complete hash identifies those exact canonical signed receipt bytes, and its Core authority tuple is byte-identical to that receipt's commit tuple
    And the submit_payout event's resulting PayoutLotValueProjectionV1 preselects only that stable ID/Core tuple to avoid self-reference, while each post-commit full PayoutLotV1 adds the exact transition-event hash and every later pending/result object relationship-checks it as PayoutSendFenceV1
    And there is no second send-fence schema, independently mutable row authority, unsigned alias, ID-only/hash-only reference, or rail call before the exact signed receipt commits

  Scenario Outline: Compensation range-state value projections have closed mutation-exhaustive schemas
    Given the value projection for range-state object "<object>" contains only "<fields>"
    When each universal mutation is independently applied to each named field or an unknown field is inserted
    Then strict decoding, containing compensation-event signature verification, or prior/result value-projection-hash verification fails before money or state changes
    And ID/range exclusion constraints reject overlapping active coverage or a state transition outside each object's closed state set
    And every projection retains its preselected creating or current-transition compensation-event stable ID and expected group index, while the post-commit full object adds only that event's complete hash/signature linkage and ledger position/hash, which are excluded from the current event's value projection and relationship-checked by later transitions/heads

    Examples:
      | object | fields |
      | DebtRangeV1 | schema/network/protocol, ID, parent ID and transaction-start committed complete hash or canonical root absence, operator, currency/unit/scale, economic_reversal/resolved_submitted_success/paid_enforcement_clawback origin, origin-conditional paid source binding where economic/enforcement uses preexisting paid lot full hash and resolved-submitted uses transaction-start reserved_submitted full hash plus resulting paid lot value-projection hash, originating TowerPayoutInstructionV1 and rail-result hashes, immutable half-open atom range and atoms, parent negative entitlement event/plan-local range plus causal positive source event/range and plan/descriptor hashes or canonical enforcement absence, enforcement decision complete hash and immutable creation/current-at-debt_create coverage value-projection complete hash or canonical economic absence, creating/current-transition compensation-event stable ID and expected group index, instruction-bound accepted_terms PayoutEligibilityFactV1 series/revision/complete-hash/effective/expiry and PayoutPolicyV1 series/revision/complete-hash/applicability/expiry plus fixed same-currency-offset-only/external-debit-forbidden fields, outstanding/recovered/written_off/partitioned state, revision with fixed creation revision 1, prior committed value-projection hash or canonical creation absence, DebtRangeChildRangeSetV1 complete hash or canonical nonpartitioned absence, create Core tuple |
      | DebtRecoveryApplicationV1 | schema/network/protocol, ID, parent ID and transaction-start committed complete hash or canonical root absence, operator, currency/unit/scale, parent positive entitlement event stable ID plus plan/descriptor hashes and plan-local/causal-positive source range, target DebtRange transaction-start parent ID/complete hash/value-projection hash/selected range, actual mapped dependent DebtRange stable ID/parent-or-empty/range/atoms and group-final DebtRangeValueProjectionV1 hash, DebtRangeChildRangeSetV1 hash or canonical whole-target absence, exact equal-length affine source-to-actual-target offset, exact atoms, creating/current-transition compensation-event stable ID and expected group index, active/reversed/partitioned state, revision with fixed creation revision 1, prior committed value-projection hash or canonical creation absence, DebtRecoveryApplicationChildRangeSetV1 complete hash or canonical nonpartitioned absence, Core tuple |
      | PendingSubmittedNegativeV1 | schema/network/protocol, ID, operator, currency/unit/scale, parent negative entitlement event stable ID and plan-local range plus causal positive source event/range and plan/descriptor hashes, TowerPayoutInstructionV1/PayoutSendFenceV1 complete hashes, one transaction-start submitted lot ID/hash/range, exact atoms, creating/current-transition compensation-event stable ID and expected group index, pending/resolved_failed/resolved_paid state, revision with fixed creation revision 1, prior committed value-projection hash or canonical creation absence, preexisting rail-result hash or canonical absence, Core tuple |
      | EnforcementDispositionCoverageV1 | schema/network/protocol, ID, parent ID and transaction-start committed complete hash or canonical root absence, operator, currency/unit/scale, unpaid_forfeiture or paid_clawback kind, enforcement decision/evidence complete hashes, original creating compensation-event stable ID/group index, current-transition compensation-event stable ID and expected group index, transaction-start source lot ID/hash/selected range, expected forensic result-lot stable ID/range/atoms or canonical whole-lot result, exact atoms, current/derecognized/partitioned state, revision with fixed creation revision 1, prior committed value-projection hash or canonical creation absence, EnforcementDispositionCoverageChildRangeSetV1 complete hash or canonical nonpartitioned absence, causal negative descriptor hash or canonical absence, Core tuple |

  Scenario: Every recovery application keeps one actual dependent DebtRange identity
    Given DebtRecoveryApplicationV1 or one immutable recovery child binds an actual mapped dependent DebtRange stable key/range and group-final value-projection hash
    When its state relationship is checked
    Then active maps to a recovered dependent range, reversed maps to the exact same affine range in outstanding state, and a terminal partitioned recovery parent maps to its terminal partitioned dependent parent
    And on partial debt_reopen the selected reversed recovery child maps to the aligned outstanding DebtRange child while every active recovery remainder maps to its aligned recovered DebtRange remainder
    And every child stable ID/range pair equals its DebtRecoveryApplicationChildRangeSetV1 member, that member's dependent pair equals DebtRangeChildRangeSetV1, and each mapped value projection is byte-identical to the corresponding ApplicationRealizedValueProjectionSetV1 group-final member and event-group AffectedState fold
    And substituting the transaction-start parent for a selected child, drifting dependent state, using another child, or including a current-group dependent full-object/event hash rejects the recovery relationship

  Scenario Outline: Other control-set member value projections are mutation-exhaustive
    Given control-set member kind "<kind>" has only value-projection fields "<fields>"
    When each universal mutation is independently applied to a named field, a member is omitted/duplicated/reordered, or a forbidden audit-envelope field is inserted
    Then strict decoding or its canonical member/set value-projection hash fails before event or head acceptance
    And the projection excludes only current-group event bytes/signatures/complete hashes, current/resulting ledger positions/hashes, and resulting full-object/control-leaf hashes while retaining every money, lineage, authority, state, range, hold, deadline, and policy field

    Examples:
      | kind | fields |
      | SourceIntervalValueProjectionV1 | schema/network/protocol, SettlementReceiptV2 hash, funding-slice ID/kind/source-lot reference and FundingSourceLotV1 revision/hash/provenance, FundingSourceReservationV1/FundingSourceReservationSetV1 complete hashes, currency/unit/scale, source interval start/end, cumulative job-cost interval start/end, consumer/Station allocations, source authority sequence, grant-bound FundingAllocationPolicyV1 series/revision/complete hash and funding-allocation rule, owning aggregate ID, materializing compensation-event stable ID and expected group index |
      | EntitlementAggregateValueProjectionV1 | schema/network/protocol, aggregate ID, SettlementReceiptV2 hash and funding-allocation-array hash, operator, currency/unit/scale, compensation-policy version, AuthoritativePaymentRevisionSetV1 complete hash, cumulative G/S/F/N/A, signed-delta sum, current-transition compensation-event stable ID and expected group index, pending_reconciliation/current_zero/current_positive/conflict_quarantined state, revision with fixed creation revision 1, prior committed EntitlementAggregateValueProjectionV1 complete hash or canonical creation absence |
      | ApplicationRangeValueProjectionV1 | schema/network/protocol, reconciliation transaction and application IDs, parent entitlement_delta stable ID and plan-local range, causal positive source-event stable ID/range, application-owner compensation-event stable ID and expected group index, ApplicationDescriptorSetV1/descriptor hashes, application kind, transaction-start primary/dependent target IDs/hashes/ranges or canonical absences, ApplicationResultRangeSetV1 expected hash and immutable creation/current-at-application ApplicationRealizedValueProjectionSetV1 hash, exact atoms, currency/unit/scale, immutable committed state, fixed revision 1, and canonical no-prior-projection absence |
      | RailResultValueProjectionV1 | schema/network/protocol, platform account, payout-instruction ID and complete hash, idempotency/destination hashes, provider result ID/revision, success or confirmed-failure shape, currency/unit/scale and exact amount, authenticated authority tuple and complete source-result hash |

  Scenario: Entitlement aggregates have one acyclic projection chain
    Given an EntitlementAggregateValueProjectionV1 is created or revised by one entitlement_delta event
    Then creation is revision 1 with canonical prior-projection absence
    And every later revision is exactly the accepted revision plus one and binds the immediately prior committed EntitlementAggregateValueProjectionV1 complete hash
    And the resulting projection binds the current event's stable ID and expected group index but contains no current event signature, complete hash, ledger position, resulting control-leaf hash, or undefined full aggregate-object hash
    And exact replay is idempotent, while a skipped or overflowing revision, prior presence at creation, prior absence later, wrong aggregate or event identity, stale prior projection, same-group self-reference, or conflicting projection bytes fails before the event or control leaf commits

  Scenario: PayoutLotV1 and PayoutLotValueProjectionV1 are mutation-exhaustive
    Given PayoutLotValueProjectionV1 has these independently addressable fields and no others:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | payout-lot ID |
      | parent payout-lot ID and transaction-start complete hash or canonical root absence |
      | immutable PayoutLotChildRangeSetV1 complete hash or canonical nonpartitioned absence |
      | operator ID |
      | Tower ID |
      | SettlementReceiptV2 ID and complete hash |
      | entitlement-aggregate ID |
      | source entitlement_delta stable ID and expected transaction-group index |
      | original creating compensation-event stable ID and expected transaction-group index |
      | current-transition compensation-event stable ID and expected transaction-group index |
      | source-event half-open atom range |
      | funding-slice ID plus parent SettlementReceiptV2/funding-allocation-array hashes and opaque source-lot reference |
      | currency |
      | unit |
      | scale |
      | exact share atoms |
      | immutable MaturityPolicyV1 series/revision/complete hash, maturity-start AuthoritativePaymentRevisionV1 sequence/complete hash/capture-maturity Core tuple, and deterministically derived required maturity Core tuple |
      | not_matured or matured status |
      | maturity transition stable event ID/group index, MaturityAuthorityV1 ID/complete hash, and actual Core tuple or canonical not-yet-mature absence |
      | immutable compensation-policy version |
      | immature, mature_payable, withheld, partitioned, reserved_prepared, reserved_submitted, paid, cancelled, or forfeited state |
      | state revision |
      | prior committed lot complete hash or canonical creation absence |
      | prior value-projection complete hash or canonical creation absence |
      | state-transition-effective HoldReferenceSetV1 complete hash including the canonical empty set |
      | dust-cycle ID/generation/revision/value-projection hash/deadline or canonical non-dust absence |
      | payout ID or canonical unreserved absence |
      | payout-preparation ID or canonical unprepared absence |
      | TowerPayoutInstructionV1 complete hash or canonical unsubmitted absence |
      | send-fence ID/Core tuple or canonical unsubmitted absence |
      | authenticated rail-result complete hash or canonical unresolved absence |
      | terminal cancellation authority kind and either negative-application stable ID/plan index/descriptor hash or payout_confirmed_failed stable ID/group index/failure-descriptor ID/PendingFailureSegmentSetV1 hash, immutable creation/current-at-forfeit unpaid-forfeiture coverage value-projection hash, or canonical absence for every state other than cancelled or forfeited |
    When each universal mutation is independently applied to each value field or state-conditional presence while the creating transition signature remains
    Then strict decoding or the resulting PayoutLotValueProjectionV1 complete-hash relationship fails
    And state-conditional fields follow only the closed state table: immature requires not_matured; mature_payable, reserved_prepared, reserved_submitted, and paid require matured; withheld, partitioned, cancelled, and forfeited retain either immutable maturity status; matured requires its transition/evidence/actual tuple and not_matured requires canonical absence
    And reserved_prepared has no instruction/fence/result, reserved_submitted has instruction/fence but no final result, paid has authenticated success, and terminal states cannot carry an active reservation
    And cancelled requires exactly one closed cancellation authority shape—negative_application or submitted_failure_resolution—whose range and result descriptor owns that child, forfeited requires only its immutable at-forfeit coverage projection hash, and immature, mature_payable, withheld, partitioned, reserved_prepared, reserved_submitted, and paid require canonical absence for both terminal-authority alternatives
    And PayoutLotChildRangeSetV1 follows its one CanonicalTypedSetV1 registry row exactly; only partitioned requires it, and later child state transitions cannot stale the terminal parent
    And immature, mature_payable, reserved_prepared, reserved_submitted, partitioned parents, cancelled, forfeited, and paid require the canonical empty operational hold set, while withheld requires a nonempty transition-effective set
    And post-send eligibility/tax/reconciliation authorities remain external incident and operator-wide payout holds while reserved_submitted is rail-locked; authenticated failure then applies their complete current set to surviving returned ranges, while success leaves the paid lot's operational set empty
    And active holds transfer only to live partition children or remain operator-wide external authorities rather than mutating a terminal parent
    And a later closure or supersession cannot invalidate a terminal lot because its operational hold set is empty and its transition event retains the forensic prior-state/authority relationship
    And creation revision 1 requires both prior full-lot and prior value-projection absence; an ordinary later transition requires both committed prior hashes; a child created and transitioned later in the same atomic group requires prior full-lot absence, the exact earlier intermediate value-projection hash, revision 2, and increasing group indices
    And every other prior-hash/revision combination, a same-group reference to an uncommitted full-object/event hash, or a child transition at the same/earlier index is rejected
    And post-commit PayoutLotV1 adds only source entitlement_delta complete hash, original-creating and current-transition compensation-event complete-hash/signature linkages, and ledger sequence/hash; those audit fields and every same-group full child hash are excluded from the current-group value projection and are relationship-checked by every later CAS/head

  Scenario: DustCycleV1 and DustCycleValueProjectionV1 have one acyclic closed schema
    Given DustCycleValueProjectionV1 has these independently addressable fields and no others:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | dust-cycle ID |
      | monotonic operator/currency generation |
      | prior terminal DustCycleV1 complete hash or canonical first-generation absence |
      | operator ID |
      | currency |
      | unit |
      | scale |
      | open or terminal_cleared state |
      | current-open pointer ID/generation for open or canonical terminal absence |
      | first-below-threshold Core authority time and sequence |
      | payout-policy series ID, revision, and complete hash |
      | payout-policy applicability Core tuple |
      | minimum payout rail-minor units |
      | maximum dust carry interval |
      | exact dust-review deadline Core tuple |
      | DustLotReferenceSetV1 complete hash and member count |
      | exact total share atoms |
      | state revision with fixed creation revision 1 |
      | transaction-start committed DustCycleV1 complete hash or canonical current-group cycle-creation absence |
      | immediate prior DustCycleValueProjectionV1 complete hash or canonical first-revision absence |
      | original creating compensation-event stable ID and expected transaction-group index |
      | current-transition compensation-event stable ID and expected transaction-group index |
      | terminal_cleared reason and terminal Core tuple or canonical open absence |
    When each universal mutation is independently applied to a field or illegal conditional shape while the creating transition signature retains the original resulting projection hash
    Then strict decoding or the DustCycleValueProjectionV1 complete-hash relationship fails before any lot, hold, pointer, or control transition
    And open requires positive atoms, one matching current-open pointer, and an unexpired-or-overdue deadline; terminal_cleared requires zero atoms, an empty reference set, pointer absence, and exactly one closed reason threshold_consumed, liability_cancelled, or fraud_forfeited
    And a new generation is exactly the prior terminal generation plus one and binds its committed full hash, while every noncreation revision preserves generation, first anchor, policy, interval, and deadline
    And creation revision 1 has transaction-start full/hash and immediate-prior-projection absence with original creator equal to current transition; the first transition of a preexisting cycle binds its transaction-start committed full hash and that object's value projection
    And a later transition in the same atomic group retains the same transaction-start full hash or creation absence, binds the exact earlier intermediate value-projection hash, increments revision by one, and uses a strictly greater current group index without any intermediate full-object/event hash
    And every other revision/prior combination, skipped or reused group index, or intermediate self/full hash is rejected
    And post-commit DustCycleV1 adds only original-creating and current-transition event complete-hash/signature linkages plus ledger sequence/hash to the final projection, while a later transaction binds that committed full object as its transaction start

  Scenario: DustLotReferenceSetV1 commits stable lot ranges without a dust-to-lot hash cycle
    Given every DustLotReferenceV1 contains only payout-lot stable ID, parent-or-root stable ID, immutable half-open atom-range start/end, and exact share atoms
    When DustLotReferenceSetV1 is encoded
    Then it is the exact strict JCS object containing schema/network/protocol, operator, currency/unit/scale, member count, total share atoms, and the member array sorted by the canonical JCS-byte key of payout-lot ID, parent-or-empty, range start, and range end
    And the members name every current leaf lot range in that cycle exactly once, are nonoverlapping, sum to the cycle total, and the count equals the array length
    And payout-lot complete hashes, PayoutLotValueProjectionV1 hashes or objects, compensation-event hashes, ledger positions, unknown fields, duplicates, alternate order, gaps, or overlaps are forbidden
    And the one-way relation is PayoutLotValueProjectionV1 to DustCycleValueProjectionV1 to stable DustLotReferenceV1 only, so no dust-cycle preimage contains the lot projection that refers to it

  Scenario: ClassificationAuthorityReferenceV1 has one closed member schema
    Given each ClassificationAuthorityReferenceV1 contains only schema/network/protocol, classification kind, stable reference ID, authority object type/ID, preexisting signed authority complete hash or current-group authority stable ID/expected group index, authority series/revision/prior hash or canonical nonrevisioned absence, operator, TowerIDScopeSetV1 complete hash or canonical global absence, currency/unit/scale, AuthorityRangeScopeSetV1 stable ID/complete hash, closed result code, one closed kind-specific applicability-source union, evidence complete hash, effective Core tuple, and expiry Core tuple or canonical nonexpiring absence
    When its classification kind is decoded
    Then the kind is exactly maturity_authority, payout_eligibility_decision, payout_policy, or tax_withholding_decision
    And its exact conditional authority shape is:
      | kind | authority object type | allowed result/state | required revision/deadline shape | exact scope |
      | maturity_authority | MaturityAuthorityV1 | matured | nonrevisioned one-use authority with exact MaturityPolicyV1, MaturitySourceRevisionSetV1, derived required tuples, actual Core tuple, and canonical expiry absence | exact source lot range, immutable policy/start revision, current payment revision, and event stable ID/group index |
      | payout_eligibility_decision | PayoutEligibilityDecisionV1 | eligible or held | signed decision series/revision/prior hash and required unexpired expiry tuple | exact operator/identity/destination/currency/rail applicability intersected with lot range |
      | payout_policy | PayoutPolicyV1 | applicable | signed policy series/revision/prior hash and required applicability/expiry tuples | exact currency/unit/scale/rail and lot range |
      | tax_withholding_decision | TaxWithholdingDecisionV1 | zero, positive, or unknown | signed decision series/revision/prior hash and required preparation applicability/expiry tuples | exact operator/preparation/destination/currency/rail and returned lot range |
    And exactly one preexisting-complete-hash or current-group-stable-ID/index authority shape is present, every inapplicable conditional field is canonically absent, and current-group event complete hashes/signatures/ledger positions are forbidden
    And payout_eligibility_decision uses only an exact byte-identical copy of its decision's applicability evidence ordinal/kind/source tag, object type/ID/complete hash or canonical missing-decision-commit absence, and Core tuple
    And tax_withholding_decision uses only an exact byte-identical copy of its decision's applicability source ordinal, tax_profile_fact or payout_policy tag, object type/ID/complete hash, and Core tuple; maturity_authority and payout_policy use their exact registered applicability event/current-group tuple and have both decision-source variants canonically absent

  Scenario: HoldReferenceV1 has one closed transition-effective authority schema
    Given each HoldReferenceV1 contains only schema/network/protocol, hold kind, stable hold-reference ID, authority object type/ID, preexisting signed authority complete hash or current-group authority stable ID/expected group index, authority series/revision/prior hash or canonical nonrevisioned absence, compensation-created initial authorized range-lineage digest or canonical external-authority absence, operator, TowerIDScopeSetV1 complete hash or canonical global absence, currency/unit/scale, AuthorityRangeScopeSetV1 stable ID/complete hash, closed reason/result code, immutable compensation-created causal-evidence complete hash or canonical external-authority absence, current evidence complete hash, one closed kind-specific applicability-source union, effective Core tuple, expiry/review Core tuple or canonical indefinite absence, state at the set's decision Core tuple, and that decision tuple
    When its hold kind is decoded
    Then the kind is exactly eligibility_decision_hold, eligibility_authority_unavailable_hold, tax_decision_hold, tax_authority_unavailable_hold, eligibility_incident_hold, tax_correction_incident_hold, reconciliation_hold, policy_hold, enforcement_hold, provider_unavailability_hold, or dust_review_hold
    And its exact conditional authority shape is:
      | kind | authority object type | allowed current state/reason | required revision/expiry-review shape | exact scope/evidence |
      | eligibility_decision_hold | PayoutEligibilityDecisionV1 | held with its closed signed reason | decision series/revision/prior hash and required expiry tuple | exact operator/identity/destination/currency/rail scope and decision evidence |
      | eligibility_authority_unavailable_hold | TowerCompensationReceiptV1 withhold event | eligibility-authority-unavailable | hold series with fixed first revision 1/prior absence or exact successor, canonical expiry absence, required review tuple | exact operator/identity/currency/rail/lot scope and unavailable evidence/head/signer/time component set |
      | tax_decision_hold | TaxWithholdingDecisionV1 | positive or unknown with its closed signed reason | decision series/revision/prior hash and required preparation applicability/expiry tuples | exact operator/preparation/destination/currency/rail/lot scope and tax evidence |
      | tax_authority_unavailable_hold | TowerCompensationReceiptV1 withhold event | tax-decision-unavailable | hold series with fixed first revision 1/prior absence or exact successor, canonical expiry absence, required review tuple | exact operator/preparation/destination/currency/rail/lot scope and unavailable evidence/head/signer/time component set |
      | eligibility_incident_hold | PayoutEligibilityIncidentV1 | open_rail_unknown or open_postsend_disbursement | incident ID/revision/prior hash, canonical expiry absence, required review tuple | exact operator/payout/instruction/lot-range intersection and incident evidence |
      | tax_correction_incident_hold | TaxDecisionCorrectionIncidentV1 | open_rail_unknown or open_noncompliant_disbursement | incident ID/revision/prior hash, canonical expiry absence, required review tuple | exact operator/preparation/instruction/lot-range intersection and tax evidence |
      | reconciliation_hold | TowerCompensationReceiptV1 withhold event | pending_reconciliation or conflict_quarantined | hold series with fixed first revision 1/prior absence or exact successor, canonical expiry absence, required review tuple | exact settlement/aggregate/lot ranges and reconciliation evidence |
      | policy_hold | TowerCompensationReceiptV1 withhold event | policy_restriction | hold series with fixed first revision 1/prior absence or exact successor and required policy review tuple | exact signed restriction evidence, policy reference, and lot ranges |
      | enforcement_hold | TowerCompensationReceiptV1 withhold event | enforcement_review | hold series with fixed first revision 1/prior absence or exact successor, canonical expiry absence, required review tuple | exact operator/Tower/settlement/lot ranges and enforcement evidence |
      | provider_unavailability_hold | TowerCompensationReceiptV1 withhold event | provider_unavailability | hold series with fixed first revision 1/prior absence or exact successor, canonical expiry absence, required review tuple | exact adapter/rail/currency/lot ranges and availability evidence |
      | dust_review_hold | TowerCompensationReceiptV1 withhold event | dust_review_deadline | hold series with fixed first revision 1/prior absence or exact successor, canonical expiry absence, review tuple equal to dust deadline | exact DustCycleValueProjectionV1 hash, generation, and stable lot-range set |
    And exactly one preexisting-complete-hash or current-group-stable-ID/index authority shape is present, an authority closed/expired/superseded at the decision tuple is excluded, every inapplicable conditional field is canonically absent, and current-group event complete hashes/signatures/ledger positions are forbidden
    And eligibility_decision_hold uses only an exact byte-identical copy of its held decision's applicability evidence ordinal/kind/source tag, object type/ID/complete hash or canonical missing-decision-commit absence, and Core tuple
    And tax_decision_hold and tax_correction_incident_hold use only the exact tax-profile-or-policy applicability source shape copied from their tax decision or incident; every other hold kind uses its exact registered applicability event/current-group tuple and has both decision-source variants canonically absent

  Scenario: withhold has one closed external-materialization or compensation-created source
    Given a TowerCompensationReceiptV1 withhold event selects one exact hold kind and PayoutLotRangeSetV1
    When its source tag is external_materialization
    Then the kind is eligibility_decision_hold, tax_decision_hold, eligibility_incident_hold, or tax_correction_incident_hold; it binds that preexisting purpose-signed decision or incident ID/complete hash and exact series/revision/prior hash, and every compensation-created hold-series field is canonically absent
    And when its source tag is compensation_created the kind is eligibility_authority_unavailable_hold, tax_authority_unavailable_hold, reconciliation_hold, policy_hold, enforcement_hold, provider_unavailability_hold, or dust_review_hold; the withhold event is the current-group authority at its stable ID/index and binds the exact derived hold-series/revision/prior-event shape, while every external decision/incident authority field is canonically absent
    And either source signs the exact prior/resulting HoldReferenceSetV1, resulting ClassificationAuthoritySetV1, AuthorityRangeScopeSetV1, state, reason/evidence/effective/review tuples, and standalone absence or exact AbortVoidHoldPlanSetV1 parent/member binding
    And an external decision or incident itself never mutates a payout lot: its acceptance and the materializing withhold event may commit in one serializable transaction, but the compensation event alone owns the lot/hold/journal/control mutation and directly binds the already constructible authority complete hash
    And an unknown source, source-kind mismatch, foreign conditional field, free hold-series on an external source, missing series on a compensation-created source, or incomplete current hold set rejects the event before state changes

  Scenario: Classification and hold reference identities are deterministic and alias-free
    Given one AuthorityRangeScopeSetV1 has an immutable range-lineage digest computed as the fixed-length unpadded case-preserving base64url SHA-256 digest over the UTF-8 bytes of strict JCS [AuthorityRangeLineageV1,network-ID,operator-ID,currency,unit,scale,ordered members each containing payout-lot stable ID,parent-or-empty,range-start,range-end,atoms,Tower-ID,SettlementReceiptV2-ID,SettlementReceiptV2-complete-hash]
    Then ClassificationAuthorityReferenceV1 stable reference ID is the identically encoded SHA-256 digest over strict JCS [ClassificationAuthorityReferenceV1-id-v1,network-ID,classification-kind,authority-object-type,authority-stable-series-ID-or-nonrevisioned-object-ID,immutable-range-lineage-digest]
    And an external-authority HoldReferenceV1 stable hold-reference ID is the identically encoded SHA-256 digest over strict JCS [HoldReferenceV1-id-v1,network-ID,hold-kind,authority-object-type,authority-stable-series-ID-or-nonrevisioned-object-ID,immutable-range-lineage-digest]
    And a compensation-created stable hold-series ID is the identically encoded SHA-256 digest over strict JCS [CompensationHoldSeriesV1-id-v1,network-ID,hold-kind,operator-ID,TowerIDScopeSetV1 semantic ordered Tower-ID members,currency,unit,scale,initial-authorized-range-lineage-digest,immutable-causal-evidence-complete-hash]
    And its current HoldReferenceV1 stable hold-reference ID is the identically encoded SHA-256 digest over strict JCS [CompensationHoldReferenceV1-id-v1,network-ID,hold-kind,stable-hold-series-ID,current-immutable-range-lineage-digest]
    And all derivations exclude mutable authority revision/head, decision tuple, event-owned AuthorityRangeScopeSetV1 ID/hash, current event hash, and resulting object hash
    And ClassificationAuthoritySetV1 and HoldReferenceSetV1 reject a nonderived ID and reject duplicate [kind,external-authority-stable-series-or-object-ID or compensation-hold-series-ID,current-immutable-range-lineage-digest] identity even if distinct encoded IDs, heads, or member bytes are presented

  Scenario: Compensation-created hold series have one signed current head
    Given a TowerCompensationReceiptV1 withhold event creates or revises one deterministically derived stable hold-series ID and one or more deterministically derived current-range HoldReferenceV1 IDs
    Then revision 1 requires canonical prior-withhold-event absence and every successor is exactly current revision plus one with the immediately prior signed withhold-event ID/complete hash
    And every successor preserves hold kind, operator, currency/unit/scale, semantic ordered Tower scope, initial authorized range-lineage digest, immutable causal-evidence complete hash, derived hold-series identity, and closed reason while signing only a changed current revision-evidence hash and review tuple
    And the successor binds the prior current AuthorityRangeScopeSetV1 ID/hash and re-encodes the byte-identical immutable lineage under its own newly derived consumer-event/index scope ID and decision tuple; preserving or reusing the prior scope ID/hash is forbidden
    And a successor PayoutLotRangeSetV1 exhaustively contains every and only nonterminal live lot range whose current HoldReferenceSetV1 names that series head, including all conserving partition descendants; the event atomically re-encodes every such reference to the new head so no sibling retains a superseded revision
    And each current HoldReferenceV1 copies that exact series/revision/prior/current-event authority, derives its own ID from its current immutable live range lineage, and is valid only for an exact conserving descendant/subrange of the signed initial authorized scope; at most one event compare-and-swaps the current series revision/hash and exact replay is idempotent
    And a skipped/zero/overflow revision, wrong-series prior, prior presence at creation, absence later, omitted/extra current descendant, changed immutable scope, stale/forked successor, or HoldReferenceV1 naming a nonhead event is rejected before a hold set or lot changes

  Scenario: Partitioned children inherit authority only through exact immutable lineage
    Given partition_lot replaces one immutable active payout-lot leaf with conserving child leaves
    When a current child retains maturity classification or any external or compensation-created hold
    Then the verifier resolves the child's complete immutable parent chain to exactly one source member in the MaturityAuthorityV1 PayoutLotRangeSetV1, external hold authority scope, or compensation hold-series initial authorized scope
    And every link has the committed parent ID/complete hash, identical operator/Tower/SettlementReceiptV2/funding/currency/unit/scale lineage, a contained half-open economic source range, and atoms equal to range length; child unions cover the partitioned parent exactly once
    And the inherited authority applies only to the exact affine descendant intersection, never a sibling, remainder outside the original authority range, newly introduced lineage, or expanded range
    And each retained reference is re-encoded under the child event-owned AuthorityRangeScopeSetV1, preserves the same external authority or compensation hold-series current head, and derives its reference ID from the child current-range lineage without minting or advancing a hold series
    And a missing parent, broken hash, nonconserving split, lineage substitution, ambiguous ancestor, stale series head, or child reference outside the signed ancestor scope rejects the entire partition group

  Scenario: HoldResolutionAuthorityReferenceV1 has one closed exact authority
    Given release_hold proposes to clear one exact current HoldReferenceV1 from one exact prior HoldReferenceSetV1
    Then HoldResolutionAuthorityReferenceV1 contains only schema/network/protocol, resolution-reference ID, resolved hold kind/series ID/revision/complete member hash and current signed authority object ID/complete hash, operator, TowerIDScopeSetV1 complete hash or canonical global absence, currency/unit/scale, prior AuthorityRangeScopeSetV1 stable ID/complete hash, release/result AuthorityRangeScopeSetV1 stable ID/complete hash, resolution authority object type/ID/complete hash or current-group stable ID/expected group index, authority series/revision/prior hash or canonical nonrevisioned absence, closed resolution result, evidence complete hash, applicability Core tuple, effective Core tuple, and decision Core tuple
    And the two scope sets have byte-identical ordered lot IDs/parents/ranges/atoms/Tower/receipt lineage but their owner consumer/group-index/decision fields are exactly their different prior-event and release-event contexts; reusing either owner for the other is forbidden
    And every duplicated operator/Tower/currency and range-member field is byte-identical through the applicable scope, prior hold, resolution authority, release event, and selected PayoutLotRangeSetV1
    When every universal mutation is independently applied to a field, relationship, current-group index, or resolution authority while the release_hold signature remains
    Then strict decoding, purpose-signature verification, current-head CAS, scope equality, or resulting-set relationship fails before a hold or lot changes

  Scenario Outline: Each hold kind has one permitted release authority or no v1 release
    Given the exact current HoldReferenceV1 kind/reason is "<hold>"
    Then release_hold requires "<authority>"
    And an administrator assertion, expiry alone, review time alone, unrelated policy/decision, stale authority, wrong scope, or generic resolution hash cannot clear it

    Examples:
      | hold | authority |
      | eligibility_decision_hold | a newer applicable unexpired PayoutEligibilityDecisionV1 with eligible result over the exact range |
      | eligibility_authority_unavailable_hold | a current applicable PayoutEligibilityDecisionV1 over the exact range; held result replaces this member with eligibility_decision_hold and eligible result permits removal |
      | tax_decision_hold | a newer applicable unexpired TaxWithholdingDecisionV1 with exact zero result over the preparation/range |
      | tax_authority_unavailable_hold | a current applicable TaxWithholdingDecisionV1; positive/unknown replaces this member with tax_decision_hold and zero permits removal |
      | eligibility_incident_hold | exact next-revision PayoutEligibilityIncidentV1 closed_no_disbursement after authenticated rail failure |
      | tax_correction_incident_hold | exact next-revision TaxDecisionCorrectionIncidentV1 closed_no_disbursement after authenticated rail failure |
      | reconciliation_hold | the accepted purpose-signed TowerCompensationReceiptV1 entitlement_delta or zero-delta reconciliation event whose exact resulting aggregate projection/head binds an exhaustive current AuthoritativePaymentRevisionSetV1 in which every conflicted source is uniquely superseded by its own strictly newer authenticated monotonic revision with complete lineage and every unaffected member is byte-identical |
      | policy_hold with policy_restriction | no release_hold authority in v1 pending a separately approved purpose-signed policy-clearance decision; PayoutPolicyV1 threshold fields do not imply clearance |
      | enforcement_hold | no release_hold authority in v1; only an exact purpose-signed CompensationForfeitureDecisionV1 may forfeit, otherwise it remains held |
      | provider_unavailability_hold | no release_hold authority in v1 pending a separately approved typed provider-recovery contract |
      | dust_review_hold | no release_hold authority; exact dust_reclassify/threshold-consumption transitions own any later state change |

  Scenario: release_hold removes exactly one current member and rederives destination authority
    Given a valid HoldResolutionAuthorityReferenceV1 applies at the release decision tuple
    When release_hold commits for its exact PayoutLotRangeSetV1
    Then the resulting HoldReferenceSetV1 re-encodes under the release/result scope and decision tuple one current reference with the same stable authority identity/head for every still-current prior member except that one resolved member, plus every newly effective hold, with no omission or priority deletion
    And no prior member bytes/hash or prior event-owned scope hash is reused as a resulting member; only immutable authority identity/head and byte-identical ordered range lineage carry forward into the prescribed new scope
    And resulting ClassificationAuthoritySetV1 binds the current maturity authority or not_matured classification, current PayoutEligibilityDecisionV1, current PayoutPolicyV1, and every other classification required for the exact range
    And a nonempty resulting hold set stays withheld; the canonical empty set becomes mature_payable only with matured plus eligible current classifications and otherwise becomes immature only with not_matured plus eligible current classification
    And one transaction compare-and-swaps the prior lot/hold-series heads, resolution authority, classification heads, prior/result sets, lot state, journal posting, and control leaf or commits none
    And exact replay is idempotent, while clearing two members with one reference, retaining the resolved member, dropping another member, missing a new hold, using stale maturity/eligibility, or selecting a different destination rejects the event

  Scenario: Reconciliation release authority is signed, accepted, and current
    Given HoldResolutionAuthorityReferenceV1 proposes to clear reconciliation_hold
    Then its resolution authority object is the exact accepted compensation event, not a naked AuthoritativePaymentRevisionSetV1 hash, administrator statement, or provider response
    And the event's purpose signature, ledger position/hash, prior/resulting EntitlementAggregateValueProjectionV1 hashes, resulting aggregate revision/state, bound exhaustive AuthoritativePaymentRevisionSetV1 complete hash, and compensation control-head inclusion all verify
    And the resulting aggregate projection is the current CAS head at the release decision tuple and has current_zero or current_positive state rather than pending_reconciliation or conflict_quarantined
    And every affected hold range's SettlementReceiptV2/funding source is covered by that exact exhaustive set and no newer payment or aggregate revision exists
    And a constructible but unsigned set, stale aggregate event/head, omitted source, unrelated zero delta, or later conflicting revision cannot release the hold

  Scenario: ClassificationAuthoritySetV1 and HoldReferenceSetV1 are canonical and exhaustive
    Given either set is encoded for one operator, currency/unit/scale, Core decision tuple, and exact AuthorityRangeScopeSetV1
    When its complete hash is computed
    Then the preimage is one strict JCS object containing schema/network/protocol, operator, currency/unit/scale, decision Core tuple, AuthorityRangeScopeSetV1 stable ID/complete hash, canonical member count, and the complete member objects array
    And ClassificationAuthoritySetV1 members sort by the UTF-8 JCS-byte key [classification-kind,stable-reference-ID,AuthorityRangeScopeSetV1-hash], while HoldReferenceSetV1 members sort by [hold-kind,stable-hold-reference-ID,AuthorityRangeScopeSetV1-hash]
    And count equals array length, duplicate key/object, omitted current authority/hold, retained closed hold, alternate ordering, unknown field/kind, or an authority outside the exact scope rejects the classification; the no-hold form is the one canonical empty HoldReferenceSetV1 object/hash rather than null or absence
    And all holds effective at the decision tuple coexist without priority-based deletion: any nonempty HoldReferenceSetV1 makes an otherwise surviving returned range withheld, while an empty set plus matured status and current eligible classification makes it mature_payable
    And a pending-negative affected range becomes cancelled first and cannot inherit a hold, while every unaffected returned range binds the full current classification and hold sets; tax, eligibility, reconciliation, policy, enforcement, unavailability, and dust-review holds cannot silently override or erase one another

  Scenario Outline: Hold and classification reference mutations are exhaustive
    Given canonical object "<object>" is used by a resulting payout lot or payout-failure descriptor
    When each universal mutation is independently applied to every common or kind-conditional field, member presence, count, dedupe key, order, applicability tuple, state, scope, or canonical absence
    Then strict decoding or the object/set complete-hash relationship fails before payout-lot state, dust, debt, journal, or rail state changes

    Examples:
      | object |
      | ClassificationAuthorityReferenceV1 |
      | HoldReferenceV1 |
      | ClassificationAuthoritySetV1 |
      | HoldReferenceSetV1 |
