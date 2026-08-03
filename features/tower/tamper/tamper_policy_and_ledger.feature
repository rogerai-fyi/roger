# PROPOSED SPEC — founder approval is required before step definitions or implementation.
#
# Scope: exhaustive Cartesian tamper matrices for every policy, decision, and incident authority, control projections, control totals, and the compensation ledger head.
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
  Scenario: TowerCompensationPolicyV1 has one exact signed revenue-share rule
    Given TowerCompensationPolicyV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | tower-compensation-policy signer key ID |
      | fixed tower_operator_revenue_share program kind |
      | stable policy-series ID |
      | positive policy revision |
      | previous TowerCompensationPolicyV1 complete hash or canonical first-revision absence |
      | canonical rate_ppm bounded from zero through 1000000 |
      | fixed net_platform_revenue_after_station_and_processor_fee share-base kind |
      | exact external_cash eligible funding kind and grant_credit exclusion |
      | exact required FundingAllocationPolicyV1 series/revision/complete hash |
      | positive bounded maximum compensated-capability validity interval |
      | effective Core tuple |
      | expiry Core tuple |
      | policy evidence complete hash |
      | issue tuple and Core policy-ledger commit tuple |
    When every universal mutation is independently applied to every listed field while retaining the tower-compensation-policy signature
    Then strict decoding, policy-chain verification, published-directory relationship, or purpose-signature verification fails before a grant snapshot, candidate, or compensation event is created
    And revision 1 requires prior absence, every successor is exactly current revision plus one with its immediate prior complete hash, and no signer time or unpublished policy can select a rate, funding-policy relationship, or capability lifetime

  Scenario: FundingAllocationPolicyV1 has one exact universal source-order rule
    Given FundingAllocationPolicyV1 has these independently addressable signed fields:
      | field |
      | schema version, network ID, and protocol version |
      | funding-allocation-policy signer key ID |
      | deterministic currency/unit/scale-scoped policy-series ID |
      | positive revision and previous FundingAllocationPolicyV1 complete hash or canonical first absence |
      | fixed allocation-rule version v1 |
      | exact supported funding kinds expiring_grant, nonexpiring_grant, and external_cash |
      | closed order expiring_grant by expiry/Core-credit-sequence/source-lot-ID/available-range-start, then nonexpiring_grant by Core-credit-sequence/source-lot-ID/available-range-start, then external_cash by Core-credit-sequence/source-lot-ID/available-range-start |
      | fixed lowest-available-range-first selection and actual-cost-prefix consumption/remainder-release rules |
      | effective and expiry Core tuples |
      | policy evidence complete hash |
      | issue tuple and Core policy-ledger commit tuple |
    When every universal mutation is independently applied to every listed field while retaining the funding-allocation-policy signature
    Then strict decoding, deterministic-series/current-head verification, published-directory relationship, or purpose-signature verification fails before a funding reservation or grant exists
    And every public grant chooses the unique greatest published applicable revision for its currency scope; no client, grant signer, settlement signer, compensation policy, or iteration order can select or alter a source rule

  Scenario: MaturityPolicyV1 has one exact signed source-scoped rule
    Given MaturityPolicyV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | maturity-policy signer key ID |
      | policy series ID |
      | positive policy revision |
      | previous MaturityPolicyV1 complete hash or canonical first-revision absence |
      | provider-neutral adapter ID and merchant/account binding hash |
      | external-cash payment-source kind |
      | currency |
      | unit |
      | scale |
      | fixed authoritative_capture_maturity_start kind |
      | positive bounded reversal-window seconds |
      | positive bounded minimum global-authority-sequence advance |
      | effective Core tuple |
      | expiry Core tuple |
      | policy evidence complete hash |
      | issue and Core commit authority tuples |
    When every universal mutation is independently applied to every listed field while retaining the maturity-policy signature
    Then every mutation or wrong-purpose signature is rejected before a lot, required maturity tuple, or maturity authority is created
    And revision 1 has canonical prior absence, every successor is exactly prior plus one with the immediate prior complete hash, effective is before expiry, and unknown source/start kinds, zero/overflow duration, cross-currency scope, stale/forked revision, or signer-selected time source is rejected

  Scenario: MaturityAuthorityV1 binds exact current payment evidence and deterministic deadlines
    Given a maturity sweep selects one exact nonempty PayoutLotRangeSetV1 under one applicable MaturityPolicyV1
    When the purpose-separated maturity-authority signer signs MaturityAuthorityV1
    Then its strict signing bytes contain only schema/network/protocol, maturity-authority signer key ID, authority ID, operator, TowerIDScopeSetV1 complete hash, currency/unit/scale, maturity-event stable ID/expected group index, PayoutLotRangeSetV1 complete hash/member count/total atoms, MaturityPolicyV1 series/revision/complete hash, MaturitySourceRevisionSetV1 complete hash/member count/total atoms, actual maturity Core authority tuple, evidence-fetch transaction hash, and issue/commit Core tuples
    And every duplicated count/amount/scope is byte-identical, each source member's required time is checked start Core time plus policy reversal-window seconds without overflow, its required minimum sequence is checked start sequence plus policy minimum advance, and actual tuple meets both
    And each current AuthoritativePaymentRevisionV1 is the unique latest authenticated revision at the sweep transaction, has final capture and processor fee, no open/lost dispute covering the selected surviving range, and relationship-checks the aggregate's exhaustive current AuthoritativePaymentRevisionSetV1
    And the immutable lot policy/start/source/range fields are byte-identical to each source member, grant-only funds are forbidden, the policy was applicable at the independently authoritative lot-create tuple, and one authority can be consumed only by its bound maturity event/ranges
    And policy expiry is the cutoff for selecting it for new lots, not erasure of its authority for already-bound historical lots; later compromise revocation still applies its signed effective cutoff
    And actual maturity Core authority tuple is independently allocated by the serializable Core authority sequencer and is byte-identical to the MaturityAuthorityV1 commit tuple, bound maturity event common-envelope Core tuple, and atomic ledger-group tuple, while every evidence fetch tuple is no later than actual
    And a stale source, pending fee, immature capture, unresolved dispute, removed target range, early time/sequence, mixed policy/source scope, omitted/duplicate range, alternate total, unknown field, or wrong-role signature rejects the authority before maturity or payout

  Scenario: Maturity authority fields and source relationships are mutation-exhaustive
    Given one valid MaturityAuthorityV1, policy, source set, lot-range set, and current aggregate authority are fixed
    When every universal mutation is independently applied to an authority field, source member, count, total, ordering, start/current revision relationship, required tuple, actual tuple, or event binding while retaining its signature
    Then strict decoding, a named set hash, deterministic deadline derivation, current-source comparison, scope equality, one-use CAS, or signature verification fails before any lot changes state

  Scenario: lot_create cannot choose an earlier source revision or alternate maturity policy
    Given a positive entitlement_delta and its exact AuthoritativePaymentRevisionSetV1 create one payout-lot range from an external-cash funding slice
    When the lot_create event selects maturity authority inputs at its independently assigned Core tuple
    Then it copies the byte-identical causal source member from the parent entitlement_delta's current revision set, including the revision that produced that positive descriptor, payment/source identity, revision/event-set hashes, and capture-maturity Core tuple
    And it selects the unique greatest accepted MaturityPolicyV1 revision effective at that lot-create tuple whose adapter/merchant/source-kind/currency/unit/scale scope matches that member
    And the lot's required Core time is checked start time plus reversal-window seconds and its required minimum global sequence is checked start sequence plus the policy advance, all stored byte-identically in the lot_create event and resulting PayoutLotValueProjectionV1
    And policy expiry prevents selection for a lot created at or after expiry, but an already selected accepted policy remains the immutable historical rule through its derived maturity tuple
    And an earlier/later/foreign source revision, noncausal payment-set member, less-than-greatest policy, policy outside its effective interval, source/slice mismatch, signer-supplied start, overflow, or boundary equality at policy expiry rejects lot creation

  Scenario: PayoutPolicyV1 exhaustive field and mutation Cartesian product
    Given PayoutPolicyV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | payout-policy series ID |
      | payout-policy revision |
      | previous PayoutPolicyV1 complete hash or canonical first-revision absence |
      | currency |
      | unit |
      | scale |
      | payout rail |
      | accounting quanta per rail minor unit |
      | minimum payout rail-minor units |
      | maximum preparation authorization interval |
      | maximum dust carry interval |
      | maximum tax-profile-fact validity interval |
      | maximum tax-profile age-at-use interval |
      | fixed same_currency_future_compensation_offset_only debt-recovery mode |
      | fixed external_debit_forbidden true |
      | applicability Core authority tuple |
      | issue time |
      | expiry time |
      | payout-policy signer key ID |
      | Core policy-ledger commit authority tuple |
    When each universal mutation is independently applied to each listed field while the payout-policy signature remains
    Then every field and mutation pair fails strict decoding, published-directory relationship, or payout-policy purpose-signature verification
    And an unknown field, wrong-purpose signature, noncanonical integer, stale/skipped/conflicting revision, or invalid applicability tuple is rejected before preparation

  Scenario: FeeFinalityPolicyV1 exhaustive field and mutation Cartesian product
    Given FeeFinalityPolicyV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | stable policy-series ID, positive policy revision, and previous FeeFinalityPolicyV1 complete hash or canonical first-revision absence |
      | provider-neutral adapter ID |
      | payment source kind |
      | currency and scale scope |
      | maximum fee-finality interval |
      | retry policy version |
      | incident policy version |
      | applicability Core authority tuple |
      | issue time |
      | expiry time |
      | fee-finality-policy signer key ID |
      | Core policy-ledger commit authority tuple |
    When each universal mutation is independently applied to each listed field while the fee-finality-policy signature remains
    Then every field and mutation pair fails strict decoding, published-directory relationship, or fee-finality-policy purpose-signature verification
    And an absent, zero, negative, noncanonical, overflowing, unbounded, stale, or cross-adapter interval is rejected before compensated allocation

  Scenario: FeeFinalityIncidentV1 exhaustive field and mutation Cartesian product
    Given FeeFinalityIncidentV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | fee-finality incident signer key ID |
      | incident ID |
      | incident state |
      | incident revision |
      | previous FeeFinalityIncidentV1 complete hash or canonical first-revision absence |
      | provider-neutral adapter ID hash |
      | merchant/account binding hash |
      | payment source ID hash |
      | currency |
      | unit |
      | scale |
      | last authoritative payment revision and complete hash |
      | fee-finality policy version and complete hash |
      | capture Core authority tuple |
      | fee-finality deadline Core tuple |
      | FeeFinalityAffectedAggregateSetV1 complete hash |
      | stable reconciliation key hash |
      | closed reason code |
      | recovery payment revision and complete hash or canonical absence |
      | issue Core authority time and sequence |
    When each universal mutation is independently applied to each listed field while the fee-incident signature remains
    Then every field and mutation pair fails strict decoding or fee-incident signature/relationship verification
    And an unknown state, illegal transition, wrong-purpose signature, provider timestamp as deadline, or synthesized fee is rejected before money movement

  Scenario: PayoutEligibilityPolicyV1 has one exact published rule
    Given PayoutEligibilityPolicyV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | payout-eligibility-policy signer key ID |
      | stable policy-series ID |
      | positive policy revision |
      | previous PayoutEligibilityPolicyV1 complete hash or canonical first-revision absence |
      | exact PayoutEligibilityEvidenceRegistryV1 version/complete hash/member count 7 |
      | exact eligible result plus ordered held-reason/result semantics supplied only by the bound PayoutEligibilityEvidenceRegistryV1 |
      | positive bounded maximum decision-validity interval |
      | currency/unit/scale and payout-rail scope or canonical global-scope tags |
      | effective Core tuple |
      | expiry Core tuple |
      | policy evidence complete hash |
      | issue tuple and Core policy-ledger commit tuple |
    When every universal mutation is independently applied to every listed field while the payout-eligibility-policy signature remains
    Then strict decoding, revision-chain verification, published-directory relationship, scope/evidence-registry equality, or purpose-signature verification fails before an eligibility decision or hold exists
    And revision 1 requires prior absence, every successor is exactly current revision plus one with immediate prior hash, and decision expiry cannot exceed its selected policy's bounded validity or expiry

  Scenario: PayoutEligibilityDecisionV1 exhaustive field and mutation Cartesian product
    Given PayoutEligibilityDecisionV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | payout-eligibility decision signer key ID |
      | deterministic eligibility-scope ID |
      | decision-series ID |
      | decision revision with fixed first revision 1 |
      | previous decision complete hash or canonical first-revision absence |
      | operator ID |
      | payout-identity verification version or canonical held-missing-identity absence |
      | immutable destination fingerprint or canonical held-missing-identity-or-destination absence |
      | operator-account stable series ID, revision, and complete hash or canonical held-missing-account absence |
      | accepted-terms revision and complete hash or canonical held-missing-terms absence |
      | sanctions-ruleset version and complete hash or canonical held-missing-sanctions absence |
      | jurisdiction code or canonical held-missing-determination absence |
      | currency |
      | unit |
      | scale |
      | payout rail |
      | eligible or held result |
      | closed reason code |
      | applicability-authority evidence ordinal/kind/source tag plus object type/ID/complete hash or exact missing-decision-commit absence |
      | applicability-authority Core time and global sequence |
      | PayoutEligibilityEvidenceSetV1 complete hash |
      | payout-eligibility-policy series ID, revision, and complete hash |
      | issue time |
      | expiry time |
      | independently assigned eligibility-decision-ledger Core commit time and global sequence |
    When each universal mutation is independently applied to each listed field while the eligibility-decision signature remains
    Then every field and mutation pair fails strict decoding or eligibility-decision signature/relationship verification
    And an unknown result, invalid or nonderived scope/series, stale/skipped/conflicting revision, wrong-purpose signature, noncurrent fact/policy/key head, or signer-controlled cutoff is rejected before preparation or send

  Scenario: PayoutEligibilityIncidentV1 exhaustive field and mutation Cartesian product
    Given PayoutEligibilityIncidentV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | payout-eligibility incident signer key ID |
      | incident ID |
      | incident state |
      | incident revision |
      | previous PayoutEligibilityIncidentV1 complete hash or canonical first-revision absence |
      | operator ID |
      | payout ID |
      | payout-preparation ID |
      | TowerPayoutInstructionV1 complete hash |
      | prior eligible decision revision and complete hash |
      | held decision revision and complete hash |
      | byte-identical held-decision applicability-authority evidence ordinal, kind, and source tag |
      | byte-identical held-decision applicability-authority object type, ID, and complete hash or exact missing-decision-commit absence |
      | byte-identical held-decision applicability Core time and global sequence |
      | payout send-fence authority tuple |
      | payout rail state |
      | authenticated rail-result complete hash or canonical unknown absence |
      | immutable destination fingerprint |
      | jurisdiction code |
      | currency |
      | unit |
      | scale |
      | payout rail |
      | gross and net rail-minor units |
      | evidence complete hash |
      | independently assigned eligibility-incident-ledger Core commit time and global sequence |
    When each universal mutation is independently applied to each listed field while the eligibility-incident signature remains
    Then every field and mutation pair fails strict decoding or eligibility-incident signature/relationship verification
    And incident ID is the fixed-length unpadded case-preserving base64url SHA-256 digest over strict UTF-8 JCS [PayoutEligibilityIncidentV1-series-v1,network-ID,payout-ID]
    And one transaction assigns the independent incident-ledger tuple and compare-and-swaps the incident revision/prior hash, held eligibility-decision head, byte-identical applicability tuple, payout send-fence, rail state/result, and operator payout-hold head
    And key validity/compromise uses that commit tuple, while each later transition revalidates the current incident head and incident signer active/nonrevoked in the greatest current trust publication
    And an unknown state, illegal transition, stale decision/send-fence/incident head, signer timestamp, wrong-purpose signature, post-fence redirection, deletion, resend, or callback-created eligibility is rejected

  Scenario: TaxProfileFactV1 exhaustive field and current-head contract
    Given TaxProfileFactV1 has only schema/network/protocol, tax-profile-fact signer key ID, deterministic operator-scoped series ID, positive revision, immediate prior complete hash or canonical first absence, operator ID, payout-identity verification version, tax-jurisdiction code, tax-profile version/evidence complete hash, tax-ruleset version/complete hash, verified/review_required/invalid result, effective Core tuple, finite expiry Core tuple, issue tuple, and independently assigned tax-profile-ledger Core commit tuple
    Then stable series ID is the fixed-length unpadded case-preserving base64url SHA-256 digest over strict UTF-8 JCS [TaxProfileFactV1-series-v1,network-ID,operator-ID]
    And creation is revision 1/prior absence, every successor is current revision plus one/immediate prior under CAS, and any identity/jurisdiction/profile/ruleset/result/evidence change advances that same series
    And effective is no earlier than fact-ledger commit, use-time expiry is no later than the minimum of fact commit plus the selected published PayoutPolicyV1 maximum tax-profile validity interval and signer-key not-after, while use time minus fact commit is no greater than that policy's maximum tax-profile age-at-use interval
    And activation of any named identity, jurisdiction, profile, or ruleset input advances that same current head to its new assessment or a nonverified successor before capability, decision, instruction, or send use
    When each universal mutation is independently applied to a common, payload, result, time, head, evidence, signer, or signature field
    Then strict decoding, deterministic-series/current-head, purpose signature, Core anchor, bounded freshness, or duplicated-field verification fails before a capability, zero tax decision, instruction, or send fence exists

  Scenario: TaxWithholdingDecisionV1 exhaustive field and mutation Cartesian product
    Given TaxWithholdingDecisionV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | tax-decision signer key ID |
      | decision-series ID |
      | decision revision with fixed first revision 1 |
      | previous decision complete hash or canonical first-revision absence |
      | payout-preparation ID |
      | PayoutPreparationV1 complete hash |
      | operator ID |
      | payout-identity verification version |
      | TaxProfileFactV1 stable series ID, revision, complete hash, result, effective/expiry, and fact-ledger commit Core tuples |
      | jurisdiction code |
      | immutable destination fingerprint |
      | currency |
      | unit |
      | scale |
      | payout rail |
      | PayoutPolicyV1 series/revision/complete hash and maximum tax-profile validity/age intervals |
      | accounting quanta per rail minor unit |
      | selected available share atoms |
      | gross reserved share atoms |
      | gross rail-minor units |
      | decision result |
      | required withholding rail-minor units or canonical unknown absence |
      | closed reason code |
      | applicability-authority source ordinal and tax_profile_fact or payout_policy tag |
      | applicability-authority object type, ID, and complete hash |
      | applicability-authority Core time |
      | applicability-authority global sequence |
      | issue time |
      | expiry time |
      | independently assigned tax-decision-ledger Core commit time |
      | independently assigned tax-decision-ledger global sequence |
    When each universal mutation is independently applied to each listed field while the tax-decision signature remains
    Then every field and mutation pair fails strict decoding or tax-decision signature/relationship verification
    And decision-series ID is the fixed-length unpadded case-preserving base64url SHA-256 digest over strict UTF-8 JCS [TaxWithholdingDecisionV1-series-v1,network-ID,PayoutPreparationV1-ID]
    And the applicability source is the lexicographic maximum of fact effective at ordinal zero and policy effective/first-publication at ordinal one, decision commit/use is at or after both effective tuples and strictly before both expiries, every decision commit CASes the sole derived series plus current fact/policy/publication heads, and exact replay is idempotent
    And an unknown field, wrong-purpose signature, alternate series, stale profile/policy/key head, nonfresh fact, or invalid zero/positive/unknown shape is rejected before instruction creation or send without mutating the existing preparation

  Scenario: CompensationEnforcementPolicyV1 has one exact published authority
    Given CompensationEnforcementPolicyV1 has only schema/network/protocol, compensation-enforcement-policy signer key ID, fixed tower_operator_revenue_share_enforcement kind, deterministic network/program-scoped policy-series ID, positive revision, immediate prior complete hash or canonical first absence, exact permitted unpaid_forfeiture and paid_clawback dispositions, closed fraud_confirmed or manufactured_traffic or forged_evidence reason set, fixed current_final_substantiated_finding requirement, fixed immutable_grant_snapshot_historical_terms rule, fixed no_submitted_range and one_open_decision_per_target_atom rules, positive bounded maximum finding age and decision-validity interval, effective/expiry Core tuples, policy evidence complete hash, issue tuple, and independent Core policy-ledger commit tuple
    Then policy-series ID derives from strict JCS [CompensationEnforcementPolicyV1-series-v1,network-ID,tower_operator_revenue_share_enforcement], revision 1 has prior absence, and every successor is current revision plus one/immediate prior hash under CAS
    And it gains selection authority only through its exact compensation_enforcement member in the greatest accepted CompensationPolicyDirectorySetV1 publication and is selected as the unique greatest effective unexpired revision at decision commit
    When any field, enum, bound, signature, revision link, directory member, or authority relationship is mutated
    Then strict decoding, purpose-signature, deterministic-series/current-head, policy-ledger, or publication verification fails before a finding or decision gains authority

  Scenario: DebtWriteoffPolicyV1 has one exact published authority
    Given DebtWriteoffPolicyV1 has only schema/network/protocol, debt-writeoff-policy signer key ID, deterministic currency/unit/scale-scoped policy-series ID, positive revision, immediate prior complete hash or canonical first absence, closed legal_discharge or documented_uncollectible or accounting_correction reason set, fixed current_approved_one_use_approval requirement, fixed originating_instruction_historical_terms rule, fixed same_currency_only and one_open_decision_per_target_atom rules, positive bounded maximum approval age and decision-validity interval, effective/expiry Core tuples, policy evidence complete hash, issue tuple, and independent Core policy-ledger commit tuple
    Then policy-series ID derives from strict JCS [DebtWriteoffPolicyV1-series-v1,network-ID,currency,unit,scale], revision 1 has prior absence, and every successor is current revision plus one/immediate prior hash under CAS
    And it gains selection authority only through its exact debt_writeoff member in the greatest accepted CompensationPolicyDirectorySetV1 publication and is selected as the unique greatest effective unexpired revision at approval and decision commit
    When any field, enum, bound, signature, revision link, directory member, or authority relationship is mutated
    Then strict decoding, purpose-signature, deterministic-series/current-head, policy-ledger, or publication verification fails before an approval or decision gains authority

  Scenario: CompensationEnforcementFindingV1 is a typed current finding authority
    Given CompensationEnforcementFindingV1 has only schema/network/protocol, compensation-enforcement-finding signer key ID, deterministic finding-series ID, positive revision, immediate prior complete hash or canonical first absence, deterministic finding ID, operator ID, Tower ID, immutable TargetScopeDigestV1, substantiated/unsubstantiated/review_pending result, one closed policy-permitted reason code, final_no_open_appeal or appeal_open adjudication state, bounded evidence-manifest complete hash, exact CompensationEnforcementPolicyV1 series/revision/complete hash, effective/expiry Core tuples, issue tuple, and independently assigned enforcement-finding-ledger Core commit tuple
    Then finding-series ID derives from strict JCS [CompensationEnforcementFindingV1-series-v1,network-ID,operator-ID,Tower-ID,TargetScopeDigestV1] and finding ID derives from [CompensationEnforcementFindingV1-id-v1,finding-series-ID,revision]
    And revision 1 has prior absence, every successor is current revision plus one/immediate prior hash under CAS, effective is no earlier than finding commit and policy applicability, and expiry is bounded by policy maximum finding age and every signer/policy cutoff
    And only the exact current substantiated plus final_no_open_appeal revision is positive authority; an appeal, new evidence, result change, or policy-head change advances or invalidates that same head before decision use
    When any field, result/adjudication shape, evidence relationship, revision, signer, tuple, or target digest is mutated
    Then strict decoding, purpose signature, current-head/policy CAS, bounded-time, or target relationship verification fails before a forfeiture decision

  Scenario: DebtWriteoffApprovalV1 is a typed current one-use approval authority
    Given DebtWriteoffApprovalV1 has only schema/network/protocol, debt-writeoff-approval signer key ID, deterministic approval-series ID, positive revision, immediate prior complete hash or canonical first absence, deterministic approval ID, operator ID, immutable TargetScopeDigestV1, DebtWriteoffAuthorizedRangeSetV1 complete hash, exact share atoms/currency/unit/scale, approved/rejected/revoked state, one closed policy-permitted reason code, bounded approval-evidence complete hash, exact DebtWriteoffPolicyV1 series/revision/complete hash, effective/expiry Core tuples, issue tuple, and independently assigned writeoff-approval-ledger Core commit tuple
    Then approval-series ID derives from strict JCS [DebtWriteoffApprovalV1-series-v1,network-ID,operator-ID,TargetScopeDigestV1] and approval ID derives from [DebtWriteoffApprovalV1-id-v1,approval-series-ID,revision]
    And revision 1 has prior absence, every successor is current revision plus one/immediate prior hash under CAS, effective is no earlier than approval commit and policy applicability, and expiry is bounded by policy maximum approval age and every signer/policy cutoff
    And only an exact current approved revision may be claimed once by one DebtWriteoffDecisionV1; the claim binds decision series/revision/hash and remains current through consumption, while rejection, revocation, expiry, a newer approval head, or a second claim authorizes nothing
    When any field, state, range/amount/policy relationship, revision, signer, tuple, one-use claim, or target digest is mutated
    Then strict decoding, purpose signature, current-head/policy CAS, bounded-time, or one-use relationship verification fails before a writeoff decision or debt mutation

  Scenario: CompensationForfeitureDecisionV1 exhaustive field and mutation Cartesian product
    Given CompensationForfeitureDecisionV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | compensation-forfeiture decision signer key ID |
      | decision series ID |
      | decision revision with fixed first revision 1 |
      | previous decision complete hash or canonical first-revision absence |
      | decision ID |
      | immutable TargetScopeDigestV1 |
      | operator ID |
      | Tower ID |
      | unpaid_forfeiture or paid_clawback disposition shape |
      | EnforcementAuthorizedRangeSetV1 complete hash |
      | exact unpaid-forfeiture share atoms |
      | exact paid-clawback share atoms |
      | exact total disposition share atoms |
      | currency |
      | unit |
      | scale |
      | closed reason code |
      | CompensationEnforcementFindingV1 series/revision/complete hash/effective/expiry/commit tuples |
      | CompensationEnforcementPolicyV1 series/revision/complete hash/effective/expiry/first-publication tuples |
      | HistoricalAcceptedTermsAuthoritySetV1 complete hash |
      | effective Core authority tuple |
      | issue time |
      | expiry time |
      | independently assigned decision-ledger Core commit time and global sequence |
    When each universal mutation is independently applied to each listed field while the forfeiture-decision signature remains
    Then every field and mutation pair fails strict decoding or forfeiture-decision signature/relationship verification
    And decision-series ID derives from strict JCS [CompensationForfeitureDecisionV1-series-v1,network-ID,operator-ID,Tower-ID,disposition-shape,TargetScopeDigestV1] and decision ID derives from [CompensationForfeitureDecisionV1-id-v1,decision-series-ID,decision-revision]
    And exactly one category amount equals total disposition atoms while the other is canonical zero and every ordered causal range has that homogeneous state
    And the authorized set contains at most one contiguous half-open range from any transaction-start payout-lot leaf; a second disjoint range from that leaf requires another decision and transaction
    And a mixed/unknown field shape, wrong operator/Tower/lot/currency, wrong-purpose signature, stale revision, overlap, submitted range, or coverage/result range-set sum not exactly equal to the authorized held or paid causal atoms is rejected

  Scenario: DebtWriteoffDecisionV1 exhaustive field and mutation Cartesian product
    Given DebtWriteoffDecisionV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | debt-writeoff decision signer key ID |
      | decision series ID |
      | decision revision with fixed first revision 1 |
      | previous decision complete hash or canonical first-revision absence |
      | decision ID |
      | immutable TargetScopeDigestV1 |
      | operator ID |
      | DebtWriteoffAuthorizedRangeSetV1 complete hash |
      | exact writeoff share atoms |
      | currency |
      | unit |
      | scale |
      | closed legal/accounting reason code |
      | DebtWriteoffPolicyV1 series/revision/complete hash/effective/expiry/first-publication tuples |
      | DebtWriteoffApprovalV1 series/revision/complete hash/effective/expiry/commit tuples |
      | HistoricalAcceptedTermsAuthoritySetV1 complete hash |
      | effective Core authority tuple |
      | issue time |
      | expiry time |
      | independently assigned decision-ledger Core commit time and global sequence |
    When each universal mutation is independently applied to each listed field while the debt-writeoff signature remains
    Then every field and mutation pair fails strict decoding or debt-writeoff signature/relationship verification
    And decision-series ID derives from strict JCS [DebtWriteoffDecisionV1-series-v1,network-ID,operator-ID,TargetScopeDigestV1] and decision ID derives from [DebtWriteoffDecisionV1-id-v1,decision-series-ID,decision-revision]
    And its one-use event must map every ordered authorized range one-to-one to one whole-range written_off result or conserving selected child in the same order, with each range amount and the result-set sum exactly equal to the signed writeoff atoms
    And an unknown field, wrong operator/debt/currency, wrong-purpose signature, stale revision, omitted/duplicate/reordered/extra range, nonconserving partition, or amount different from exact outstanding authorized debt is rejected

  Scenario: Destructive compensation decisions use non-signer commit authority
    Given a CompensationForfeitureDecisionV1 or DebtWriteoffDecisionV1 is proposed over exact current ranges
    When its decision-series head is committed
    Then one transaction compare-and-swaps its deterministic revision/prior head, every target source-state head, the unique no-overlap claim for every target atom, current published policy head, and current final finding or approved one-use approval head at an independently assigned decision-ledger Core commit tuple
    And forfeiture additionally verifies every historical accepted_terms fact byte-identical to its GrantCompensationSnapshotV1 lineage, while writeoff verifies every such fact byte-identical to its originating TowerPayoutInstructionV1 and DebtRangeV1 lineage; those historical facts need not be the latest terms heads
    And effective is the deterministic maximum of target-state, current policy, and current finding-or-approval authority tuples, is no later than decision commit, expiry is strictly after commit and within the selected policy bound, and key validity/compromise selection derives only from decision commit
    And later forfeit, paid-enforcement debt_create, or debt_writeoff consumption revalidates the exact current unconsumed decision, no-overlap claims, target heads, current policy and finding-or-approval heads, the historical terms lineage and original-commit compromise status, plus all decision/policy/finding-or-approval signers active/nonrevoked in the greatest current trust publication
    And signer issue time, backdated free-form evidence, opaque version/hash standing alone, stale target or input head, overlapping live decision, consumed approval, revoked current signer, or a decision committed outside its effective/expiry shape authorizes no destructive mutation

  Scenario Outline: TaxWithholdingDecisionV1 result shapes are mutation-exhaustive
    Given a valid tax decision has result "<result>"
    When required-withholding is independently encoded as "<invalid shape>" while retaining the signature
    Then strict shape or tax-decision signature verification fails before reservation

    Examples:
      | result | invalid shape |
      | zero | absent, null, negative, or nonzero |
      | positive | absent, null, zero, negative, above gross, or noninteger |
      | unknown | present as null, zero, positive, negative, or another wire type |

  Scenario: TaxDecisionCorrectionIncidentV1 exhaustive field and mutation Cartesian product
    Given TaxDecisionCorrectionIncidentV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | tax-incident signer key ID |
      | incident ID |
      | incident state |
      | incident revision |
      | previous TaxDecisionCorrectionIncidentV1 complete hash or canonical first-revision absence |
      | operator ID |
      | payout ID |
      | payout-preparation ID |
      | TowerPayoutInstructionV1 complete hash |
      | prior tax-decision revision and complete hash |
      | corrected tax-decision revision and complete hash |
      | byte-identical corrected-decision applicability source ordinal/tag/object type/ID/complete hash |
      | byte-identical corrected-decision applicability Core time and global sequence |
      | payout send-fence authority tuple |
      | payout rail state |
      | authenticated rail-result complete hash or canonical unknown absence |
      | gross rail-minor units |
      | net transfer rail-minor units |
      | required withholding rail-minor units or canonical unknown absence |
      | immutable destination fingerprint |
      | jurisdiction code |
      | currency |
      | unit |
      | scale |
      | payout rail |
      | evidence complete hash |
      | independently assigned tax-incident-ledger Core commit time |
      | independently assigned tax-incident-ledger global sequence |
    When each universal mutation is independently applied to each listed field while the tax-incident signature remains
    Then every field and mutation pair fails strict decoding or tax-incident signature/relationship verification
    And incident ID is the fixed-length unpadded case-preserving base64url SHA-256 digest over strict UTF-8 JCS [TaxDecisionCorrectionIncidentV1-series-v1,network-ID,payout-ID]
    And one transaction assigns the independent incident-ledger tuple and compare-and-swaps the incident revision/prior hash, corrected tax-decision head, byte-identical applicability tuple, payout send-fence, rail state/result, and operator payout-hold head
    And key validity/compromise uses that commit tuple, while each later transition revalidates the current incident head and incident signer active/nonrevoked in the greatest current trust publication
    And an unknown state, illegal transition, stale correction/send fence/incident head, signer timestamp, wrong-purpose signature, or invalid required-withholding/result shape is rejected

  Scenario Outline: Every revisioned policy, decision, and incident has one genesis shape
    Given revisioned authority "<object>" uses stable series key "<series>" and bounded monotonic counter "<counter>"
    When its previous-complete-hash relationship is checked
    Then counter 1 requires the one canonical first-revision absence and no other counter may use absence
    And every later counter equals the accepted series head plus one and binds that exact prior complete hash under the same network, signer purpose, and series scope
    And exact replay returns the existing object, while a skipped/zero/overflowing counter, wrong prior, cross-series prior, two different objects at one counter, or a present prior at counter 1 is rejected or conflict-quarantined before authority use

    Examples:
      | object | series | counter |
      | PayoutPolicyV1 | payout-policy series ID | payout-policy revision |
      | FeeFinalityPolicyV1 | adapter, payment-source-kind, currency, and scale policy key | policy version |
      | FeeFinalityIncidentV1 | incident ID | incident revision |
      | PayoutEligibilityDecisionV1 | decision-series ID | decision revision |
      | PayoutEligibilityIncidentV1 | incident ID | incident revision |
      | TaxWithholdingDecisionV1 | decision-series ID | decision revision |
      | CompensationEnforcementPolicyV1 | policy-series ID | policy revision |
      | DebtWriteoffPolicyV1 | policy-series ID | policy revision |
      | CompensationEnforcementFindingV1 | finding-series ID | finding revision |
      | DebtWriteoffApprovalV1 | approval-series ID | approval revision |
      | CompensationForfeitureDecisionV1 | decision-series ID | decision revision |
      | DebtWriteoffDecisionV1 | decision-series ID | decision revision |
      | TaxDecisionCorrectionIncidentV1 | incident ID | incident revision |

  Scenario: ControlValueProjectionV1 exhaustive field and mutation Cartesian product
    Given ControlValueProjectionV1 has these independently addressable fields and no others:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | currency |
      | unit |
      | scale |
      | journal-template version |
      | source-interval count and SourceIntervalValueProjectionV1-kind ControlProjectionMemberSetV1 complete hash |
      | entitlement-aggregate count and EntitlementAggregateValueProjectionV1-kind ControlProjectionMemberSetV1 complete hash |
      | application-range count and ApplicationRangeValueProjectionV1-kind ControlProjectionMemberSetV1 complete hash |
      | payout-lot count and PayoutLotValueProjectionV1-kind ControlProjectionMemberSetV1 complete hash |
      | debt-range count and DebtRangeValueProjectionV1-kind ControlProjectionMemberSetV1 complete hash |
      | debt-recovery-application count and DebtRecoveryApplicationValueProjectionV1-kind ControlProjectionMemberSetV1 complete hash |
      | pending-submitted-negative count and PendingSubmittedNegativeValueProjectionV1-kind ControlProjectionMemberSetV1 complete hash |
      | enforcement-disposition-coverage count and EnforcementDispositionCoverageValueProjectionV1-kind ControlProjectionMemberSetV1 complete hash |
      | dust-cycle count and DustCycleValueProjectionV1-kind ControlProjectionMemberSetV1 complete hash |
      | rail-result count and RailResultValueProjectionV1-kind ControlProjectionMemberSetV1 complete hash |
      | source-derived program-net share atoms T_N |
      | policy-ceiling share atoms T_C |
      | entitlement-target share atoms T_A |
      | immature-liability share atoms |
      | mature-payable-liability share atoms |
      | withheld-liability share atoms |
      | reserved-prepared-liability share atoms |
      | reserved-submitted-liability share atoms |
      | pending-submitted-recourse share atoms |
      | current uncovered paid source-range share atoms |
      | active unreversed debt-recovery-application share atoms |
      | cumulative authenticated rail-disbursement share atoms |
      | cumulative cancelled share atoms |
      | current enforcement-disposition-coverage share atoms |
      | cumulative enforcement-coverage-derecognition share atoms |
      | outstanding-debt share atoms |
      | cumulative debt-recovery share atoms |
      | cumulative debt-reopening share atoms |
      | cumulative debt-writeoff share atoms |
      | journal debit share atoms |
      | journal credit share atoms |
    When each universal mutation is independently applied to each field or a ControlProjectionMemberSetV1 member while an event retains the original projection hash
    Then strict decoding or the ControlValueProjectionV1 complete-hash relationship fails before commit
    And current event bytes/signatures/complete hashes, current or resulting ledger sequence/hash, resulting control-leaf hash, causal backreferences to them, an unknown field, or a non-value-projection state-set member are forbidden while preselected stable current event IDs/group indices are allowed
    And every ControlProjectionMemberSetV1 encoding, count, closed kind tag, and stable order follows the exact contract below

  Scenario Outline: Every ControlProjectionMemberSetV1 has one exact JCS container and total sort key
    Given projection kind "<kind>" uses canonical stable sort-key JCS array "<key>"
    When its ControlProjectionMemberSetV1 is constructed
    Then the hash preimage is exactly one strict JCS object with schema/network/protocol, currency/unit/scale, projection-kind tag, canonical nonnegative member count, and a members array containing the complete strict value-projection objects rather than their hashes
    And members are sorted ascending by bytewise lexicographic comparison of their exact UTF-8 JCS sort-key arrays, with no locale, database collation, insertion order, or implementation tie-break
    And the encoded member count equals both array length and the matching ControlValueProjectionV1 count, while an empty set has count zero and exactly one empty members-array encoding
    And duplicate sort keys, duplicate member bytes, omitted/extra/reordered members, an unknown field/kind, alternate integer text, or a forbidden current-event/full-object audit field fails the set-hash relationship

    Examples:
      | kind | key |
      | SourceIntervalValueProjectionV1 | [kind,SettlementReceiptV2-hash,funding-slice-ID,source-lot-ID,source-interval-start,source-interval-end,cumulative-cost-start,cumulative-cost-end] |
      | EntitlementAggregateValueProjectionV1 | [kind,aggregate-ID] |
      | ApplicationRangeValueProjectionV1 | [kind,reconciliation-transaction-ID,application-ID,parent-entitlement-delta-ID,plan-local-start,plan-local-end] |
      | PayoutLotValueProjectionV1 | [kind,payout-lot-ID,parent-or-empty,range-start,range-end] |
      | DebtRangeValueProjectionV1 | [kind,debt-range-ID,parent-or-empty,range-start,range-end] |
      | DebtRecoveryApplicationValueProjectionV1 | [kind,recovery-application-ID,parent-or-empty,source-range-start,source-range-end,target-range-start,target-range-end] |
      | PendingSubmittedNegativeValueProjectionV1 | [kind,pending-ID,negative-entitlement-ID,plan-local-start,plan-local-end,submitted-lot-ID,submitted-range-start,submitted-range-end] |
      | EnforcementDispositionCoverageValueProjectionV1 | [kind,coverage-ID,parent-or-empty,source-lot-ID,source-range-start,source-range-end] |
      | DustCycleValueProjectionV1 | [kind,operator-ID,generation,dust-cycle-ID] |
      | RailResultValueProjectionV1 | [kind,platform-account,provider-result-ID,provider-result-revision,payout-instruction-ID] |

  Scenario: CompensationControlTotalLeafV1 exhaustive field and mutation Cartesian product
    Given each canonical CompensationControlTotalLeafV1 committed in the signed head set has these independently addressable fields:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | currency |
      | unit |
      | scale |
      | compensation ledger sequence |
      | compensation ledger complete hash |
      | ControlValueProjectionV1 complete hash |
      | journal-template version |
      | source-interval count and SourceIntervalValueProjectionV1-kind ControlProjectionMemberSetV1 complete hash |
      | entitlement-aggregate count and EntitlementAggregateValueProjectionV1-kind ControlProjectionMemberSetV1 complete hash |
      | application-range count and ApplicationRangeValueProjectionV1-kind ControlProjectionMemberSetV1 complete hash |
      | payout-lot count and PayoutLotValueProjectionV1-kind ControlProjectionMemberSetV1 complete hash |
      | debt-range count and DebtRangeValueProjectionV1-kind ControlProjectionMemberSetV1 complete hash |
      | debt-recovery-application count and DebtRecoveryApplicationValueProjectionV1-kind ControlProjectionMemberSetV1 complete hash |
      | pending-submitted-negative count and PendingSubmittedNegativeValueProjectionV1-kind ControlProjectionMemberSetV1 complete hash |
      | enforcement-disposition-coverage count and EnforcementDispositionCoverageValueProjectionV1-kind ControlProjectionMemberSetV1 complete hash |
      | dust-cycle count and DustCycleValueProjectionV1-kind ControlProjectionMemberSetV1 complete hash |
      | rail-result count and RailResultValueProjectionV1-kind ControlProjectionMemberSetV1 complete hash |
      | source-derived program-net share atoms T_N |
      | policy-ceiling share atoms T_C |
      | entitlement-target share atoms T_A |
      | immature-liability share atoms |
      | mature-payable-liability share atoms |
      | withheld-liability share atoms |
      | reserved-prepared-liability share atoms |
      | reserved-submitted-liability share atoms |
      | pending-submitted-recourse share atoms |
      | current uncovered paid source-range share atoms |
      | active unreversed debt-recovery-application share atoms |
      | cumulative authenticated rail-disbursement share atoms |
      | cumulative cancelled share atoms |
      | current enforcement-disposition-coverage share atoms |
      | cumulative enforcement-coverage-derecognition share atoms |
      | outstanding-debt share atoms |
      | cumulative debt-recovery share atoms |
      | cumulative debt-reopening share atoms |
      | cumulative debt-writeoff share atoms |
      | journal debit share atoms |
      | journal credit share atoms |
    When each universal mutation is independently applied to each leaf field without changing its containing signed CompensationLedgerHeadV1 bytes
    Then strict leaf decoding or the CompensationControlTotalSetV1 complete-hash relationship fails
    And an unknown field, alternate integer encoding, missing currency leaf, duplicate key, reordering, wrong fold, unequal debit/credit totals, T_A not equal to T_C or above T_N, or T_A not equal to active unpaid minus pending submitted recourse plus uncovered paid plus active recovery plus current enforcement-disposition coverage rejects the head

  Scenario: CompensationLedgerHeadV1 exhaustive field and mutation Cartesian product
    Given CompensationLedgerHeadV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | compensation-ledger-head signer key ID |
      | head ID |
      | head sequence |
      | previous CompensationLedgerHeadV1 complete hash or canonical first-sequence absence |
      | compensation ledger sequence |
      | compensation ledger entry count |
      | compensation ledger head hash |
      | SQL snapshot transaction ID hash |
      | compensation journal-template version |
      | sorted per-currency control-total leaf count |
      | CompensationControlTotalSetV1 complete hash |
      | sorted covered payout-authority leaf count |
      | CoveredPayoutAuthoritySetV1 complete hash |
      | SQL commit authority time |
      | SQL commit authority sequence |
      | ledger schema version |
      | trust-document version |
      | issue time |
      | expiry time |
      | independently assigned head-ledger Core commit time and global sequence |
    When each universal mutation is independently applied to each listed field while the compensation-head signature remains
    Then every field and mutation pair fails strict decoding or compensation-head signature/relationship verification
    And an unknown field, wrong-purpose signature, nonmonotonic sequence, alternate SQL head, invalid snapshot, unbalanced control totals, or broken previous-head link is rejected

  Scenario: Compensation ledger-head copied set counts are exact
    Given one CompensationLedgerHeadV1 binds CompensationControlTotalSetV1 and CoveredPayoutAuthoritySetV1
    When its set relationships are verified
    Then sorted per-currency control-total leaf count is byte-identical to CompensationControlTotalSetV1 member count
    And sorted covered payout-authority leaf count is byte-identical to CoveredPayoutAuthoritySetV1 member count
    And either count mismatch, alternate set with the same count, or valid set paired to another head rejects the head before instruction creation

  Scenario: Compensation ledger-head chain has one first-link shape
    Given CompensationLedgerHeadV1 sequence Q is checked against accepted head history
    Then sequence 1 requires canonical previous-head absence and every later sequence requires the immediately preceding accepted head complete hash
    And each later sequence is exactly prior plus one, its covered SQL compensation prefix is monotonic, and exact replay returns the existing head
    And zero, a gap, overflow, previous presence at sequence 1, absence after sequence 1, wrong prior, shorter SQL prefix, or conflicting bytes at one sequence rejects or conflict-quarantines the head before payout use

  Scenario: Compensation ledger heads use independent commit authority
    Given a signed CompensationLedgerHeadV1 was constructed from one committed SQL snapshot
    When its head-series revision is committed
    Then one transaction compare-and-swaps the immediately preceding accepted head and assigns a head-ledger Core commit time/global sequence independent of its signer-controlled issue time and SQL snapshot tuple
    And SQL snapshot commit is no later than head-ledger commit, issue time is no later than head-ledger commit, and head-ledger commit is strictly before expiry
    And key validity and compromise selection use only head-ledger commit against the greatest accepted trust publication, while instruction creation and send also require that exact head signer to remain current, active, and nonrevoked
    And a replay at the same head sequence is idempotent only for identical bytes; a fork, future SQL snapshot, missing tuple, expired head, or signer timestamp cannot become accepted payout authority

  Scenario: A covered payout-authority leaf has one closed mutation-exhaustive schema
    Given each leaf in CompensationLedgerHeadV1's canonical sorted covered payout-authority set has these independently addressable fields:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | payout ID |
      | payout-preparation ID |
      | PayoutPreparationV1 complete hash |
      | operator ID |
      | TowerIDScopeSetV1 complete hash |
      | TowerLifecyclePayoutAuthoritySetV1 complete hash |
      | payout-identity verification version |
      | accepted_terms PayoutEligibilityFactV1 stable series/revision/complete hash |
      | immutable destination fingerprint |
      | zero tax-decision series ID |
      | zero tax-decision revision and complete hash |
      | TaxProfileFactV1 stable series/revision/complete hash |
      | tax-decision applicability and expiry Core tuple |
      | payout-eligibility decision series ID |
      | payout-eligibility decision revision and complete hash |
      | payout-eligibility applicability and expiry Core tuple |
      | payout-policy series ID |
      | payout-policy revision and complete hash |
      | payout-policy applicability and expiry Core tuple |
      | preparation-authorization deadline Core tuple |
    When each universal mutation is independently applied to each leaf field or a leaf is omitted, duplicated, reordered, or inserted without changing the signed head bytes
    Then strict leaf decoding or the CoveredPayoutAuthoritySetV1 complete-hash relationship fails
    And an unknown field, null, alternate tuple encoding, ID/hash mismatch, nonzero tax result, authority committed after the SQL snapshot, or duplicate payout/preparation key rejects the head relationship

  Scenario: TowerLifecyclePayoutAuthorityV1 leaves and scope derivation are mutation-exhaustive
    Given each leaf in TowerLifecyclePayoutAuthoritySetV1 has Tower ID, exact current TowerLifecycleEventV1 revision/complete hash/compensation disposition/effective Core tuple, and TowerSelectedPayoutLotRangeSetV1 complete hash, member count, and total share atoms
    When each universal mutation is independently applied to a leaf field, a leaf is omitted/duplicated/reordered/inserted, or a selected lot's immutable Tower lineage is changed while the preparation or instruction signature remains
    Then strict decoding or the lifecycle-authority-set and canonical Tower-ID-scope relationships fail
    And the sorted unique leaf Tower IDs must equal exactly the sorted unique Tower IDs derived from every selected lot and SettlementReceiptV2, while each leaf's selected ranges exhaust only that Tower's contribution
    And a stale lifecycle revision/hash, applicable hold/forfeiture-required disposition, extra Tower, missing Tower, cross-operator Tower, or asynchronously unmaterialized hold blocks preparation or send

  Scenario: CompensationLedgerHeadV1 covers the completed preparation without a hash cycle
    Given payout preparation already committed its prepare_payout event and exact reserved_prepared lot states without a compensation-head or payout-instruction field
    When its CompensationLedgerHeadV1 relationship is verified
    Then the signed SQL prefix includes that prepare_payout event, its exact PayoutPreparationV1 complete hash, current zero TaxWithholdingDecisionV1 and its current TaxProfileFactV1, current payout-eligibility decision and all six fact heads including accepted_terms, current payout policy, destination, every selected event and reserved_prepared lot state at preparation, funding slice, debt offset, and balanced journal posting
    And one canonical covered payout-authority leaf binds payout/preparation IDs, PayoutPreparationV1 complete hash, operator, TowerIDScopeSetV1 complete hash, TowerLifecyclePayoutAuthoritySetV1 complete hash, payout-identity version, accepted_terms fact head, destination fingerprint, zero tax-decision and TaxProfileFactV1 heads, payout-eligibility series/revision/complete hash, payout-policy series/revision/complete hash, preparation-authorization deadline, and each authority's applicability/expiry Core tuple
    And the leaf belongs to the head's CoveredPayoutAuthoritySetV1 and the tax decision committed before the head's SQL snapshot tuple
    And every selected object's create/current-state ledger sequence is at or below the signed compensation ledger sequence
    And a full fold of the SQL snapshot reproduces its exact ledger hash, entry count, journal-template version, CompensationControlTotalSetV1 complete hash, and CoveredPayoutAuthoritySetV1 complete hash
    And the head signer only attests that committed snapshot and cannot create an event, alter a control total, reserve a lot, or authorize a payout
    And only after that head commits may TowerPayoutInstructionV1 bind its complete hash
    And the head is unexpired, accepted, and an ancestor of the current SQL head when the payout send fence commits
    And current control-total readiness for the payout currency remains healthy with no later open mismatch incident
    And unrelated descendant events do not invalidate it, while every selected lot, preparation, decision, policy, destination, and instruction relationship is freshly CAS-checked unchanged
    And an omitted selected event or authority leaf, selected-state change after the head, expired/inconsistent/non-ancestor head, unbalanced control-total set, or another ledger fork blocks instruction creation or send

  Scenario: TowerPayoutInstructionV1 exhaustive field and mutation Cartesian product
    Given TowerPayoutInstructionV1 has these independently addressable signed fields:
      | field |
      | schema version |
      | network ID |
      | protocol version |
      | currency |
      | unit |
      | scale |
      | payout ID |
      | payout-instruction ID |
      | payout-preparation ID |
      | PayoutPreparationV1 complete hash |
      | stable rail idempotency key |
      | operator ID |
      | TowerIDScopeSetV1 complete hash |
      | TowerLifecyclePayoutAuthoritySetV1 complete hash |
      | payout-identity verification version |
      | payout-eligibility decision series ID |
      | payout-eligibility decision revision |
      | payout-eligibility decision complete hash |
      | accepted_terms PayoutEligibilityFactV1 stable series ID, revision, and complete hash |
      | zero-withholding tax-decision series ID |
      | zero-withholding tax-decision revision |
      | zero-withholding tax-decision complete hash |
      | TaxProfileFactV1 stable series ID, revision, and complete hash |
      | immutable destination fingerprint |
      | payout rail |
      | payout-policy series ID |
      | payout-policy revision and complete hash |
      | minimum payout rail-minor units |
      | fixed same-currency future-compensation-offset-only and external-debit-forbidden fields |
      | preparation authorization deadline Core time and sequence |
      | CompensationEventIDSetV1 complete hash |
      | FundingSliceIDSetV1 complete hash |
      | PayoutLotAtomRangeSetV1 complete hash |
      | SettlementReceiptHashSetV1 complete hash |
      | accounting quanta per rail minor unit |
      | selected available share atoms |
      | gross reserved share atoms |
      | gross rail-minor units |
      | withholding rail-minor units |
      | net transfer rail-minor units |
      | retained share-atom remainder |
      | CompensationLedgerHeadV1 complete hash |
      | covered payout-authority leaf complete hash |
      | CompensationControlTotalSetV1 complete hash |
      | issue time |
      | expiry time |
      | independently assigned payout-authorization-ledger Core commit time and global sequence |
      | payout-authorization signer key ID |
    When each mutation is applied independently to each listed field while retaining the original payout-authorization signature:
      | mutation |
      | replace with a different valid in-range value of the same semantic type |
      | remove the field |
      | encode the field as explicit null |
      | duplicate the field with the same encoded value |
      | duplicate the field with a conflicting encoded value |
      | encode the field using a different wire type |
    Then every field and mutation pair fails payout-instruction verification or strict decoding
    And inserting any unknown field or altering the payout-authorization signature is rejected

  Scenario: Payout instruction copies only its bound head authority
    Given TowerPayoutInstructionV1 binds CompensationLedgerHeadV1 and one covered payout-authority leaf
    When its head and leaf relationships are verified
    Then its CompensationControlTotalSetV1 complete hash is byte-identical to the field in that exact head
    And its covered payout-authority leaf complete hash is byte-identical to exactly one member of that head's CoveredPayoutAuthoritySetV1, whose payout/preparation IDs and every copied authority field match the instruction
    And a valid set or leaf from another head, a reconstructed alias, an omitted member, or a same-count substitution rejects the instruction before the send fence

  Scenario: Payout instructions use independent commit authority
    Given a purpose-signed TowerPayoutInstructionV1 has passed all preparation and head relationship checks
    When its immutable instruction record is authorized
    Then one transaction compare-and-swaps the still-current prepared payout, preparation, selected lot, decision, policy, destination, and compensation-head relationships and assigns an independent payout-authorization-ledger Core commit time/global sequence
    And issue time is no later than authorization commit, authorization commit is strictly before instruction expiry and preparation deadline, and key validity/compromise selection derives only from authorization commit
    And the send fence requires the byte-identical committed tuple, the instruction as the current unconsumed payout authority, and the payout-authorization signer still current, active, and nonrevoked in the greatest accepted trust publication
    And an uncommitted instruction, conflicting instruction at one payout, stale relationship, revoked signer, signer-controlled timestamp, or commit at either deadline makes no rail call

  Scenario Outline: Role signatures are never interchangeable
    Given a valid object requiring "<required role>"
    When its signature bytes come from a valid "<wrong role>" signer
    Then verification fails before the object gains authority

    Examples:
      | required role | wrong role |
      | dispatch lease | execution grant |
      | execution grant | dispatch lease |
      | provider assertion | Tower transit |
      | Tower transit | provider assertion |
      | Core transit observation | settlement |
      | Core transit observation | Tower transit |
      | settlement | Core transit observation |
      | compensation | settlement |
      | payout authorization | compensation |
      | payout-eligibility decision | payout authorization |
      | payout-eligibility incident | payout-eligibility decision |
      | fee-finality incident | compensation |
      | compensation-forfeiture decision | compensation |
      | debt-writeoff decision | compensation-forfeiture decision |
      | tax-withholding decision | payout authorization |
      | tax-withholding decision | tax-correction incident |
      | tax-correction incident | tax-withholding decision |
      | compensation ledger head | public transparency checkpoint |
      | public transparency checkpoint | compensation ledger head |
