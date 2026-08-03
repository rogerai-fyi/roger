# PROPOSED SPEC — founder approval is required before step definitions or implementation.
#
# Scope: exhaustive Cartesian tamper matrices for provider assertion, Tower transit statement, Core transit observation, settlement receipt, Tower compensation receipt.
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
  Scenario: ProviderAssertionV2 exhaustive field and mutation Cartesian product
    Given a joined ProviderAssertionV2 has these independently addressable signed fields:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | origin kind |
      | job ID |
      | request ID |
      | attempt ID |
      | AttemptIssueCommitmentV1 ID and complete hash |
      | dispatch-lease hash |
      | execution-grant hash |
      | client key ID |
      | client nonce/idempotency key |
      | grant nonce |
      | Tower ID |
      | Tower certificate serial |
      | Tower session epoch |
      | Tower channel-binding hash |
      | Tower admission-lease stable series ID, lease ID, sequence, and complete hash |
      | current TowerLifecycleEventV1 revision and complete hash |
      | Station ID |
      | Station assertion key ID |
      | Station origin epoch |
      | Station secure-session certificate serial |
      | Station inner-session epoch |
      | Station inner-channel-binding hash |
      | model |
      | offer ID |
      | quote ID |
      | execution deadline |
      | settlement-finalization/hold ceiling |
      | maximum input tokens |
      | maximum output tokens |
      | maximum cost |
      | maximum request bytes |
      | maximum result bytes |
      | maximum streams |
      | modality |
      | request digest |
      | response digest |
      | claimed input count |
      | claimed output count |
      | result status |
      | Station-claimed start timestamp |
      | Station-claimed end timestamp |
      | Station assertion epoch |
      | Station assertion sequence |
      | previous assertion complete hash or canonical first-sequence-in-epoch absence |
    When each mutation is applied independently to each listed field while retaining the original Station signature:
      | mutation |
      | replace with a different valid in-range value of the same semantic type |
      | remove the field |
      | encode the field as explicit null |
      | duplicate the field with the same encoded value |
      | duplicate the field with a conflicting encoded value |
      | encode the field using a different wire type |
    Then every field and mutation pair fails Station verification or strict decoding
    And inserting any unknown field or altering the Station signature is rejected

  Scenario: Direct ProviderAssertionV2 exhaustive origin-shape matrix
    Given a valid direct ProviderAssertionV2
    When each joined-only field is independently inserted as null, zero, empty, or a valid-looking value:
      | joined-only field |
      | dispatch-lease hash |
      | Tower ID |
      | Tower certificate serial |
      | Tower session epoch |
      | Tower channel-binding hash |
      | Tower admission-lease stable series ID, lease ID, sequence, complete hash, and current Tower lifecycle revision/complete hash |
    Then every insertion is rejected instead of normalized to absence
    When each required direct field is independently removed, null, zero, empty, or replaced:
      | direct field |
      | origin kind direct |
      | direct Station grant sequence |
    Then every direct field and mutation pair is rejected

  Scenario: Joined ProviderAssertionV2 exhaustive origin-shape matrix
    Given a valid joined ProviderAssertionV2
    When each required joined field is independently removed, null, zero, empty, or replaced:
      | joined field |
      | origin kind joined |
      | dispatch-lease hash |
      | Tower ID |
      | Tower certificate serial |
      | Tower session epoch |
      | Tower channel-binding hash |
      | Tower admission-lease stable series ID, lease ID, sequence, complete hash, and current Tower lifecycle revision/complete hash |
    Then every joined field and mutation pair is rejected
    And inserting a direct Station grant sequence is rejected

  Scenario: TowerTransitStatementV1 exhaustive field and mutation Cartesian product
    Given TowerTransitStatementV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | Tower ID |
      | Tower key ID |
      | Tower certificate serial |
      | Tower session epoch |
      | Tower channel-binding hash |
      | dispatch-lease hash |
      | job ID |
      | attempt ID |
      | encrypted request-envelope digest |
      | encrypted result-envelope digest |
      | local route ID |
      | input bytes |
      | output bytes |
      | Tower-claimed received timestamp |
      | Tower-claimed forwarded timestamp |
      | fixed Tower statement epoch 1 |
      | Tower statement sequence |
      | previous TowerTransitStatementV1 complete hash or canonical first-sequence-in-epoch absence |
      | transit status |
    When each mutation is applied independently to each listed field while retaining the original Tower signature:
      | mutation |
      | replace with a different valid in-range value of the same semantic type |
      | remove the field |
      | encode the field as explicit null |
      | duplicate the field with the same encoded value |
      | duplicate the field with a conflicting encoded value |
      | encode the field using a different wire type |
    Then every field and mutation pair fails Tower verification or strict decoding
    And inserting any unknown field or altering the Tower signature is rejected

  Scenario: Station assertion chains have one authorized assertion-epoch genesis
    Given ProviderAssertionV2 has one stable Station ID/assertion-key ID, origin epoch O, assertion epoch A, sequence Q, and previous-complete-hash field
    When its assertion-chain relationship is checked
    Then sequence 1 is the only first-in-epoch value and requires canonical previous-hash absence
    And initial direct or joined admission creates assertion epoch A equal to 1; every sequence greater than 1 requires the exact accepted sequence Q minus 1 complete hash under the same Station/assertion-key/A head
    And only exact StationEpochResetV1 advances A by one and restarts sequence 1; rehome changes O but preserves A/key and continues the existing sequence/prior hash, while reconnect changes neither
    And every assertion's O equals its bound grant and current joined-lease or direct-origin authority, so rehome cannot replay an old-origin job or reset audit order
    And zero, skipped, duplicate-conflicting, overflowed, cross-signer, cross-assertion-epoch, wrong-origin context, wrong-prior, present-at-first, or absent-after-first sequence shape is rejected before settlement or attribution

  Scenario: Tower transit statement chain cannot reset for one Tower ID in v1
    Given TowerTransitStatementV1 has fixed statement epoch 1, bounded monotonic sequence Q, and one previous-complete-hash field under its stable Tower statement key
    When its chain relationship is checked
    Then sequence 1 is permitted exactly once for the Tower ID and requires canonical previous-hash absence
    And every later sequence is exactly the accepted head plus one and binds that exact prior TowerTransitStatementV1 complete hash despite certificate, TLS session, process, host, lease, or lifecycle changes
    And no reconnect, certificate rotation, restart, suspension, reactivation, recovery claim, or administrator action can reset or skip the sequence; loss of the statement key/head requires revoking the old Tower and enrolling a new Tower ID in v1
    And epoch other than 1, zero, skipped, duplicate-conflicting, overflowed, cross-Tower/key, wrong-prior, present-at-first, or absent-after-first shape is rejected before attribution or settlement

  Scenario: CoreTransitObservationV1 exhaustive field and mutation Cartesian product
    Given CoreTransitObservationV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | Core transit signer key ID |
      | Tower ID |
      | Tower certificate serial |
      | Tower session epoch |
      | Tower channel-binding hash |
      | dispatch-lease hash |
      | job ID |
      | attempt ID |
      | encrypted request-envelope digest |
      | encrypted result-envelope digest |
      | input bytes |
      | output bytes |
      | request first-observed time |
      | request complete-observed time |
      | result first-observed time |
      | result complete-observed time |
      | provider assertion complete-observed time |
      | evidence-complete Core authority time |
      | evidence-complete Core authority sequence |
    When each mutation is applied independently to each listed field while retaining the original Core signature:
      | mutation |
      | replace with a different valid in-range value of the same semantic type |
      | remove the field |
      | encode the field as explicit null |
      | duplicate the field with the same encoded value |
      | duplicate the field with a conflicting encoded value |
      | encode the field using a different wire type |
    Then every field and mutation pair fails Core verification or strict decoding
    And inserting any unknown field or altering the Core signature is rejected

  Scenario: SettlementReceiptV2 exhaustive field and mutation Cartesian product
    Given a compensated joined SettlementReceiptV2 has these independently addressable signed fields:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | origin kind |
      | job ID |
      | request ID |
      | attempt ID |
      | issued AttemptEventV1 ID, revision, complete hash, and Core issue tuple |
      | AttemptIssueCommitmentV1 ID and complete hash |
      | dispatch-lease hash |
      | execution-grant hash |
      | provider-assertion complete-object hash |
      | Core-transit-observation complete-object hash |
      | Tower-statement status |
      | Tower-transit complete-object hash |
      | client key ID |
      | client nonce/idempotency key |
      | grant nonce |
      | Tower ID |
      | Tower certificate serial |
      | Tower key ID |
      | Tower session epoch |
      | Tower channel-binding hash |
      | Tower admission-lease stable series ID, lease ID, sequence, and complete hash |
      | Tower lifecycle revision and complete hash |
      | Station ID |
      | Station assertion key ID |
      | Station origin epoch |
      | Station secure-session certificate serial |
      | Station inner-session epoch |
      | Station inner-channel-binding hash |
      | model |
      | offer ID |
      | quote ID |
      | policy version |
      | request digest |
      | response digest |
      | signed execution deadline |
      | signed settlement-finalization/hold ceiling |
      | maximum input tokens |
      | maximum output tokens |
      | maximum cost |
      | maximum request bytes |
      | maximum result bytes |
      | maximum streams |
      | modality |
      | result complete-observed time |
      | provider assertion complete-observed time |
      | evidence-complete Core authority time |
      | evidence-complete Core authority sequence |
      | Core evidence first-observed time |
      | Core evidence complete-observed time |
      | provider input claim |
      | provider output claim |
      | Core input recount |
      | Core output recount |
      | billed input count |
      | billed output count |
      | effective consumer input rate |
      | effective consumer output rate |
      | effective Station-earning input rate |
      | effective Station-earning output rate |
      | currency |
      | price unit |
      | accounting scale |
      | authorized hold |
      | actual cost |
      | Station earning |
      | FundingSourceReservationV1 complete hash |
      | FundingSourceReservationSetV1 complete hash |
      | FundingSourceSettlementTransitionV1 complete hash |
      | funding-allocation complete hash |
      | consumer disposition |
      | provider disposition |
      | TowerCompensationPolicyV1 series/revision/complete hash |
      | CompensatedTowerCapabilityV1 series/revision/complete hash |
      | payout_identity, operator_account, accepted_terms, sanctions_screening, and jurisdiction_determination PayoutEligibilityFactV1 series/revision/complete hashes plus TaxProfileFactV1 series/revision/complete hash |
      | compensation operator ID |
      | grant-time compensation-snapshot complete hash or canonical absence |
      | compensation candidate status |
      | result status |
      | ledger sequence |
      | previous Roger settlement-ledger entry hash or RogerLedgerGenesisV1 complete hash at first sequence |
      | settlement timestamp |
      | settlement signer key ID |
    When each mutation is applied independently to each listed field while retaining the original settlement signature:
      | mutation |
      | replace with a different valid in-range value of the same semantic type |
      | remove the field |
      | encode the field as explicit null |
      | duplicate the field with the same encoded value |
      | duplicate the field with a conflicting encoded value |
      | encode the field using a different wire type |
    Then every field and mutation pair fails settlement verification or strict decoding
    And inserting any unknown field or altering the settlement signature is rejected

  Scenario Outline: Settlement compensation-candidate status and reason shapes are exclusive
    Given a valid SettlementReceiptV2 has compensation-candidate status "<status>"
    Then its exact candidate shape is "<shape>"
    When every universal mutation is independently applied to status and every required reason while the settlement signature remains
    And each forbidden reason or future compensation-money field is independently inserted
    Then every applicable mutation fails strict decoding, shape validation, or settlement-signature verification

    Examples:
      | status | shape |
      | eligible | the closed eligible status plus grant-time compensation-snapshot, operator, exact eligibility-fact and CompensatedTowerCapabilityV1 heads, policy/rate, dispatch, Core observation, Tower-statement, and funding bindings are required; candidate reason and every future compensation-money field are absent |
      | ineligible | the closed ineligible status and exactly one closed deterministic reason are required; every future compensation-money field is absent |

  Scenario Outline: SettlementReceiptV2 origin and Tower-statement shapes are exclusive
    Given a valid SettlementReceiptV2 for "<shape>"
    When each required field is independently removed, null, zero, empty, or replaced
    And each forbidden field is independently inserted as null, zero, empty, or a valid-looking value
    Then every mutation is rejected instead of normalized to another shape

    Examples:
      | shape |
      | direct with direct Station session, direct grant sequence, and Tower-statement status not_applicable required, while every Tower, dispatch, Core Tower-observation, TowerTransitStatementV1 hash/rejection-reason, and compensation field is forbidden |
      | joined missing statement with joined Tower, dispatch, and Core-observation fields required, statement hash forbidden, and missing-corroboration reason required |
      | joined invalid statement with joined Tower, dispatch, and Core-observation fields required, valid-statement hash forbidden, and deterministic rejection reason required |
      | joined verified uncompensated with joined Tower, dispatch, Core-observation, and statement-hash fields required and compensation bindings forbidden |
      | joined verified compensated with joined Tower, dispatch, Core-observation, statement-hash, grant-time compensation-snapshot, operator, exact eligibility-fact and capability heads, and policy fields required |

  Scenario: Each SettlementReceiptV2 funding slice is exhaustively signed
    Given one settlement has every supported funding-slice kind
    And each slice has these independently addressable signed fields:
      | funding-slice field |
      | slice ID |
      | funding kind |
      | opaque source-lot reference |
      | FundingSourceLotV1 stable ID, reservation-time revision, and complete hash |
      | ConsumerCashCreditAuthorityV1 or PlatformGrantCreditV1 provenance type, ID, and complete hash |
      | FundingSourceReservationV1 complete hash |
      | FundingSourceReservationSetV1 complete hash |
      | FundingSourceSettlementTransitionV1 complete hash |
      | consumer-cost amount |
      | Station-earning allocation |
      | currency |
      | unit |
      | scale |
      | source interval start |
      | source interval end |
      | cumulative job-cost interval start |
      | cumulative job-cost interval end |
      | source credit authority sequence |
      | FundingAllocationPolicyV1 series/revision/complete hash and closed funding-allocation rule |
    When each mutation is applied independently to each listed slice field while retaining the settlement signature:
      | mutation |
      | replace with a different valid in-range value of the same semantic type |
      | remove the field |
      | encode the field as explicit null |
      | duplicate the field with the same encoded value |
      | duplicate the field with a conflicting encoded value |
      | encode the field using a different wire type |
    Then every slice-field and mutation pair fails settlement verification or strict decoding
    And adding, removing, duplicating, or reordering slices to change their canonical array fails verification

  Scenario: TowerCompensationReceiptV1 exhaustive field and mutation Cartesian product
    Given an entitlement_delta TowerCompensationReceiptV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | currency |
      | unit |
      | scale |
      | compensation event ID |
      | compensation event type |
      | reason code |
      | causal event ID |
      | compensation state-machine kind |
      | AffectedStateEntityKeySetV1 complete hash |
      | prior AffectedStateProjectionSetV1 complete hash |
      | resulting AffectedStateProjectionSetV1 complete hash excluding current-group event hashes |
      | SettlementReceiptV2 ID |
      | SettlementReceiptV2 complete hash |
      | SettlementReceiptV2 funding-allocation-array complete hash |
      | TowerIDScopeSetV1 complete hash |
      | operator ID |
      | grant-snapshot payout_identity, operator_account, accepted_terms, sanctions_screening, and jurisdiction_determination fact series/revision/complete hashes plus TaxProfileFactV1 series/revision/complete hash |
      | grant-snapshot CompensatedTowerCapabilityV1 series/revision/complete hash |
      | grant-snapshot TowerCompensationPolicyV1 series/revision/complete hash |
      | rate parts per million |
      | DispatchLeaseV1 complete hash |
      | CoreTransitObservationV1 complete hash |
      | AuthoritativePaymentRevisionSetV1 complete hash |
      | prior mature cash G |
      | new mature cash G |
      | prior Station cost S |
      | new Station cost S |
      | prior processor fee F |
      | new processor fee F |
      | prior net platform revenue N |
      | new net platform revenue N |
      | prior exact share atoms A |
      | new exact share atoms A |
      | signed compensation delta |
      | reconciliation transaction ID |
      | application descriptor count |
      | ApplicationDescriptorSetV1 complete hash |
      | canonical empty JournalPostingSetV1 complete hash |
      | journal-template version and entitlement_delta_zero disposition ID |
      | prior committed EntitlementAggregateValueProjectionV1 complete hash or canonical creation absence |
      | resulting EntitlementAggregateValueProjectionV1 complete hash and state |
      | prior committed CompensationControlTotalLeafV1 complete hash |
      | resulting ControlValueProjectionV1 complete hash |
      | ledger sequence |
      | previous Roger compensation-ledger entry hash or RogerLedgerGenesisV1 complete hash at first sequence |
      | Core-observed event time |
      | Core authority sequence |
      | compensation signer key ID |
    When each mutation is applied independently to each listed field while retaining the original compensation signature:
      | mutation |
      | replace with a different valid in-range value of the same semantic type |
      | remove the field |
      | encode the field as explicit null |
      | duplicate the field with the same encoded value |
      | duplicate the field with a conflicting encoded value |
      | encode the field using a different wire type |
    Then every field and mutation pair fails compensation verification or strict decoding
    And inserting any unknown field or altering the compensation signature is rejected
    And an entitlement_delta Tower-ID scope resolves to exactly the singleton Tower bound by its SettlementReceiptV2, while payout-family events may use a larger same-operator ordered unique set
