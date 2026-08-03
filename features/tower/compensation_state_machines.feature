# PROPOSED SPEC — founder approval is required before step definitions or implementation.
#
# Scope: exact immutable candidate, cumulative entitlement, program control totals,
# payout-lot, reservation, and operator-debt states for the founder-approved compensated
# Tower tier. Arithmetic and external-event authentication are specified in
# operator_revenue_share.feature and payment_authority.feature.

Feature: Tower compensation has separate monotonic state machines rather than one ambiguous status
  Job eligibility, changing cash entitlement, payout availability, rail transfer, and debt
  are different authorities with separate keys, invariants, and compare-and-swap transitions.

  Scenario Outline: SettlementReceiptV2 has one immutable candidate shape
    Given a joined job settlement has candidate shape "<shape>"
    When the exact SettlementReceiptV2 commits
    Then "<contract>"
    And the candidate never changes shape afterward

    Examples:
      | shape | contract |
      | ineligible | status and one closed reason code are present; future compensation amount, entitlement, lot, payout, and debt fields are absent |
      | eligible | status, Tower/operator, exact grant-snapshot eligibility-fact and CompensatedTowerCapabilityV1 heads, policy/rate, dispatch, Core observation, verified Tower-statement, and funding-slice bindings are present; every future money field is absent |

  Scenario Outline: Candidate eligibility is deterministic
    Given a SettlementReceiptV2 candidate has "<condition>"
    Then its immutable result is "<result>"

    Examples:
      | condition | result |
      | direct origin | ineligible: no joined Tower origin |
      | joined without compensated capability at grant | ineligible: not enrolled |
      | joined with on-time required Station/Core evidence but missing or invalid Tower corroboration | successful Station/client settlement with ineligible: corroboration reason |
      | joined without a complete matching Core observation | no successful job settlement and no candidate |
      | joined whose required Station/Core evidence completes at or after its deadline or lifecycle cutoff | no successful job settlement and no candidate |
      | joined whose Tower corroboration arrives only after a missing-corroboration settlement commits | the successful settlement keeps terminal ineligible reason missing-corroboration and the late statement is audit-only |
      | eligible joined evidence but grant-only funding | ineligible: no external-cash funding |
      | eligible joined evidence with exact external-cash lineage | eligible pending reconciliation |
      | missing or nonconserving funding lineage | ineligible: invalid funding lineage |

  Scenario Outline: Compensation reads one named authority at each decision time
    Given compensation reaches the "<decision>" decision
    When Roger Core evaluates it
    Then it reads "<authority>"
    And later state cannot rewrite an earlier immutable decision

    Examples:
      | decision | authority |
      | grant issue | the atomically committed grant-time compensation snapshot and policy in force at the grant issue tuple |
      | job settlement | the grant-time snapshot plus required evidence completely observed before the signed deadline and lifecycle cutoff; current active status alone is irrelevant |
      | entitlement reconciliation | the immutable candidate plus current authenticated payment and maturity revisions; enforcement disposition is a separate state machine that cannot rewrite the entitlement target |
      | payout preparation | current mature payable lots plus current payout identity, operator-payout-eligibility, payout policy/threshold, and transactional control totals; tax decision and signed ledger head deliberately follow the committed preparation |
      | payout send fence | fresh CAS rechecks of every preparation authority and its exact current revision/hash |
      | payout callback or fetch | the immutable submitted instruction and authenticated rail result only; it cannot reinterpret grant or candidate eligibility |

  # --- cumulative entitlement ----------------------------------------------

  Scenario: One entitlement aggregate has one durable CAS identity
    Given an eligible candidate and currency
    When compensation reconciliation creates its aggregate
    Then its unique key is network, SettlementReceiptV2 complete hash, operator ID, currency, unit, scale, and compensation-policy version
    And it stores AuthoritativePaymentRevisionSetV1 complete hash, cumulative G/S/F/N/A, sum of prior signed deltas, state revision, and prior state hash
    And another Tower, operator, settlement, currency, scale, or policy cannot share that aggregate

  Scenario Outline: Entitlement aggregate transitions are exhaustive
    Given an entitlement aggregate is "<from>"
    When "<event>"
    Then its state becomes "<to>"

    Examples:
      | from | event | to |
      | absent | eligible candidate exists but capture, fee, dispute, or maturity is incomplete | pending_reconciliation |
      | pending_reconciliation | all source revisions are authoritative but cumulative exact A is zero | current_zero |
      | pending_reconciliation | all source revisions are authoritative and cumulative exact A is positive | current_positive |
      | current_zero | a newer authoritative revision makes A positive | current_positive |
      | current_positive | a newer authoritative revision changes A but keeps it positive | current_positive |
      | current_positive | a newer authoritative revision makes A zero | current_zero |
      | current_zero | a newer authoritative revision remains zero | current_zero |
      | any non-conflict state | required payment or maturity facts become incomplete | pending_reconciliation with no guessed positive delta |
      | any non-conflict state | equal provider revision has conflicting canonical bytes | conflict_quarantined |
      | conflict_quarantined | one exhaustive current AuthoritativePaymentRevisionSetV1 has a unique strictly newer authenticated monotonic complete-lineage revision for every conflicted source and byte-identical current members for every unaffected source | current_zero or current_positive from that exact complete set |

  Scenario: Incomplete or conflicting reconciliation fences every unpaid causal lot
    Given a positive aggregate has immature, mature_payable, reserved_prepared, reserved_submitted, or paid causal lots
    When it atomically enters pending_reconciliation or conflict_quarantined
    Then every immature and mature_payable causal lot becomes withheld with that reconciliation reference
    And every reserved_prepared preparation uses the exact atomic abort-or-void then reconciliation-withhold group before its send fence
    And every reserved_submitted lot remains locked to its one instruction with no resend, release, or destination change until authenticated rail reconciliation
    And paid lots remain paid while a later negative target uses debt_create only for an uncovered paid range and uses enforcement_coverage_derecognize with no new debt for a range already covered by current paid_clawback enforcement disposition
    When one exhaustive current AuthoritativePaymentRevisionSetV1 contains a unique strictly newer authenticated monotonic complete-lineage AuthoritativePaymentRevisionV1 for every conflicted source and retains every unaffected source member byte-identically
    Then each withheld lot is released to immature or mature_payable only if its exact target still exists and no other hold applies
    And a removed unpaid target becomes cancelled while a submitted result follows its exact success-or-failure transition
    And absent that one exact provider-authenticated exhaustive superseding set the aggregate and all affected unpaid lots remain quarantined, with no partial-source clearing, administrator-selected history, or unnamed reconciliation authority

  Scenario: A newer authoritative zero target has one exact causal record
    Given a current_zero aggregate receives a newer authoritative payment revision whose cumulative target A remains zero
    When the aggregate CAS commits that revision
    Then the aggregate remains current_zero
    And exactly one signed zero-delta causal audit event binds the new authoritative revision and prior committed EntitlementAggregateValueProjectionV1 complete hash
    And no payout lot, reservation, rail instruction, DebtRange, or money balance is created

  Scenario: Entitlement CAS prevents duplicate or stale deltas
    Given aggregate revision R has prior EntitlementAggregateValueProjectionV1 complete hash H and sum of signed deltas D
    When one or many workers apply authenticated payment revision V
    Then exactly one transaction may compare-and-swap R and H to the new cumulative target A
    And appended money delta equals A minus D
    And a duplicate or stale V returns the existing state without another delta
    And equal V with different bytes enters conflict_quarantined without money movement

  Scenario: Every nonzero entitlement delta has one exhaustive range-application plan
    Given authoritative reconciliation changes aggregate A by signed delta D
    When its entitlement_delta receipt is signed
    Then it binds reconciliation transaction ID, aggregate ID/revision/prior committed EntitlementAggregateValueProjectionV1 complete hash, AuthoritativePaymentRevisionSetV1 complete hash, D, application count, and ApplicationDescriptorSetV1 complete hash
    And each descriptor binds application kind/ID, parent entitlement_delta stable ID and plan-local half-open delta-atom range, causal positive source-event stable ID/range, its exact transaction-start target shape from the closed mapping below, target half-open atom range, exact atoms, currency/unit/scale, expected prior/result state kinds, and deterministic ApplicationResultRangeSetV1 complete hash
    And positive D permits only debt_offset followed by lot_create descriptors, while negative D permits only debt_reopen, lot_cancel, submitted_negative_fence, debt_create, or enforcement_coverage_derecognize descriptors
    And descriptor plan-local ranges are ordered, nonoverlapping, start at zero, end at absolute D, and sum exactly to absolute D
    And positive plans use the parent entitlement_delta as their causal positive source with the same range, while negative plans bind each exact prior-positive event/range selected by deterministic reverse recognition and the target's immutable lineage
    And each one-use application-owner event binds the entitlement_delta complete hash plus its immutable ApplicationDescriptorSetV1 complete hash, zero-based index, exact descriptor hash, and ApplicationRealizedValueProjectionSetV1 complete hash, so it cannot change its kind, target, partition, range, or amount
    And zero D has count zero and the canonical empty ApplicationDescriptorSetV1 complete hash

  Scenario Outline: Application kind fixes its one transaction-start target shape
    Given an entitlement application descriptor has kind "<kind>"
    Then its primary target shape is "<target>"
    And its required relationship is "<relationship>"
    And every target kind/field not named by that row is canonically absent

    Examples:
      | kind | target | relationship |
      | lot_create | canonical target absence | the one application-owner lot_create event creates only the exact immature PayoutLotV1 source range |
      | debt_offset | one outstanding DebtRangeV1 ID/complete hash/range | the owner creates one active DebtRecoveryApplicationV1 and recovers only that whole or partition-selected range |
      | debt_reopen | one active DebtRecoveryApplicationV1 ID/complete hash/source range plus its dependent recovered DebtRangeV1 ID/hash and affine-mapped target range | the owner reverses only the mapped recovery range and reopens only the dependent debt range |
      | lot_cancel | one unreserved immature, mature_payable, or withheld PayoutLotV1 ID/complete hash/range | the owner cancels only that whole or partition-selected lot range |
      | submitted_negative_fence | one reserved_submitted PayoutLotV1 ID/complete hash/range plus its TowerPayoutInstructionV1/PayoutSendFenceV1 complete hashes | the owner leaves the lot submitted and creates one PendingSubmittedNegativeV1 as the realized result rather than a transaction-start target |
      | debt_create | one uncovered paid PayoutLotV1 ID/complete hash/range | the economic_reversal owner leaves the lot paid and creates one DebtRangeV1; resolved-submitted and enforcement debt_create events are outside an entitlement application plan |
      | enforcement_coverage_derecognize | one current EnforcementDispositionCoverageV1 ID/complete hash/range | the owner leaves the forensic forfeited-or-paid lot unchanged and derecognizes only that whole or partition-selected coverage range |

  Scenario: Application ranges are globally nonoverlapping and one-use
    Given an entitlement application targets immutable atoms in a lot, debt range, recovery application, submitted instruction, or enforcement-disposition coverage
    When its serializable transaction validates the plan
    Then ordinary lot, debt, recovery, and pending targets prove the half-open range is within its source amount and is not already consumed by a cancellation, debt, reversed recovery, submitted-negative fence, or prior application for the same causal entitlement
    And an enforcement_coverage_derecognize target instead must be an exact current, not-yet-derecognized EnforcementDispositionCoverage range even though its forensic source lot is forfeited or paid
    And exact application replay returns the committed event while overlapping, skipped, duplicate, cross-source, cross-operator, cross-currency, or out-of-bounds ranges reject the entire event group

  Scenario: Negative deltas select prior positive source ranges deterministically
    Given one entitlement aggregate has surviving atoms from several positive delta events and application kinds
    When a later cumulative target decreases by D atoms
    Then its plan consumes the most recently recognized surviving positive source-event ranges first by descending Core global sequence, event ID, and range end
    And when D ends inside one surviving leaf range, it consumes that leaf's maximal high-end suffix [range-end minus remaining-D,range-end)
    And within each selected source range it reverses that range's exact debt-recovery application, unpaid lot, submitted range, paid range, or enforcement-disposition coverage rather than choosing a cheaper state
    And event splitting, worker order, operator identity, Tower input, administrator preference, payout state, or restart cannot select different causal atoms
    And the resulting surviving positive source-range set plus cumulative negative applications equals the current aggregate A exactly

  # --- currency-scoped journal and program-cap backstop --------------------

  Scenario: Compensation has one balanced journal rather than a second budget authority
    Given one or more compensation events exist in a currency
    When Roger Core folds the global compensation-ledger prefix
    Then its unique control-total key is network, currency, unit, and scale
    And balanced journal postings reproduce source-derived recognized net revenue, policy entitlement ceiling, current entitlement target, active unpaid liability by lot state, pending submitted recourse, rail-clearing disbursement, current and derecognized enforcement-disposition coverage, debt receivable, recovery, reopening, and debt-writeoff totals
    And the control row stores the journal-template version, last global ledger sequence/hash, and no separately mutable spendable balance
    And cumulative debits equal cumulative credits for every currency while cross-currency netting is impossible

  Scenario: Per-currency control totals classify every atom exactly once
    Given the compensation ledger is folded at global sequence L
    Then active unpaid liability equals leaf-lot atoms in immature, mature_payable, withheld, reserved_prepared, and reserved_submitted
    And terminal partitioned parent lots contribute zero because their immutable children carry every atom
    And preparation and submission only reclassify active liability while authenticated payout success discharges reserved_submitted liability against rail clearing
    And unpaid negative adjustments reduce exact liability while paid negative adjustments create same-currency debt receivable
    And new positive entitlement offsets outstanding same-currency debt in the canonical debt-priority order before creating active liability
    And forfeiture, debt recovery, and debt writeoff post only through their purpose-authorized event templates
    And paid, cancelled, forfeited, and partitioned payout-lot states cannot also appear in active liability, while outstanding, recovered, written_off, and partitioned belong only to separate DebtRanges

  Scenario: Current entitlement target reconciles to one exhaustive application partition
    Given the compensation ledger is folded for one currency at global sequence L
    When U is active unpaid liability, W is pending submitted recourse, P is current uncovered paid source-range atoms, R is active unreversed DebtRecoveryApplication atoms, and V is current EnforcementDispositionCoverage atoms
    Then T_A equals checked_add(checked_subtract(U, W), P, R, V)
    And W is no greater than reserved_submitted liability and covers only nonoverlapping ranges still locked to authenticated instructions
    And every surviving positive source-event atom belongs exactly once to active unpaid, uncovered paid, active debt recovery, or current enforcement-disposition coverage, except that a pending submitted-negative range is counted in U and subtracted once through W
    And cancelled, debt-covered paid, reversed recovery, derecognized enforcement-coverage, and written-off ranges contribute zero to current T_A while remaining in forensic history
    And a missing, duplicate, overlapping, unclassified, or differently summed range rejects the event group and control head

  Scenario: CompensationControlTotalSetV1 has one canonical head representation
    Given a CompensationLedgerHeadV1 snapshot contains one leaf for each currency/unit/scale control-total key
    When its CompensationControlTotalSetV1 complete hash is computed
    Then leaves use strict canonical integer fields and are sorted lexicographically by canonical currency, unit, and scale bytes
    And the strict JCS set contains only schema/network/protocol, journal-template version, member count, and the complete sorted CompensationControlTotalLeafV1 array
    And member count equals array length and the canonical empty member array is the only zero-currency form
    And a missing, duplicate, reordered, unknown, overflowing, or differently encoded leaf produces another hash and is rejected

  Scenario: A currency's first exact event atomically materializes one deterministic zero control leaf
    Given a first otherwise-valid compensation event names a policy-supported currency/unit/scale with no existing control leaf or event and the global compensation ledger is at transaction-start sequence Q/hash H
    When the first-event serializable transaction prepares its prior control authority
    Then the zero leaf has global compensation sequence Q and hash H, where Q zero requires H to equal the accepted compensation-kind RogerLedgerGenesisV1 complete hash and positive Q requires the exact current global entry hash, plus the current journal-template version and one canonical zero ControlValueProjectionV1
    And that projection has the exact canonical empty ControlProjectionMemberSetV1 complete hash and count zero for all ten closed projection kinds, zero T_N/T_C/T_A, zero for every liability/recourse/rail/cancel/coverage/debt/recovery/reopening/writeoff/journal amount, and equal zero debit/credit totals
    And the leaf reproduces those exact fields, is unique by network/currency/unit/scale, and its deterministic complete hash is bound as the first event's prior-control-leaf field
    And one transaction commits that zero leaf, the first event at exact global sequence Q plus one with previous hash H, the resulting nonzero-or-zero control leaf, and global SQL ledger head or commits none
    And concurrent first events have one compare-and-swap winner, while exact replay returns the committed group and a different event rereads that resulting leaf as its ordinary prior authority
    And a currency cannot be initialized without its first event, after an event, twice, from a stale global sequence/hash, under another genesis/ledger/schema/template, or with a nonempty set/nonzero count/nonzero amount

  Scenario: Each CompensationControlTotalLeafV1 has one closed derived schema
    Given the global compensation ledger is folded at sequence L for one currency, unit, and scale
    When its CompensationControlTotalLeafV1 is materialized in the same serializable SQL transaction
    Then the leaf contains only schema/network/protocol, currency/unit/scale, L and ledger hash, exact ControlValueProjectionV1 complete hash, and every reproduced field of that projection: journal-template version; the exact count and ControlProjectionMemberSetV1 complete hash for each source-interval, entitlement-aggregate, application-range, payout-lot, debt-range, debt-recovery-application, pending-submitted-negative, enforcement-disposition-coverage, dust-cycle, and preexisting rail-result projection kind; T_N, T_C, T_A; immature liability, mature-payable liability, withheld liability, reserved-prepared liability, reserved-submitted liability, pending-submitted-recourse, current uncovered paid source-range atoms, active unreversed debt-recovery-application atoms, cumulative authenticated rail disbursement, cumulative cancelled atoms, current enforcement-disposition coverage, cumulative enforcement-coverage derecognition, outstanding debt, cumulative debt recovery, cumulative debt reopening, cumulative debt writeoff, and equal journal debit/credit atom totals
    And every count and amount is one bounded canonical nonnegative integer string with no absent, null, negative, floating, exponent, alternate-scale, or implementation-defined field
    And all counts, hashes, amounts, and the ledger position reproduce exactly from the authoritative SQL prefix rather than a mutable operator or administrator balance

  Scenario Outline: Compensation application cases have closed explanatory atom flows
    Given a signed compensation event matches descriptive case "<event case>"
    When its versioned journal template is applied
    Then its only allowed atom flow is "<flow>"
    And the containing event binds the prior/resulting control context, while JournalPostingSetV1 binds event ID/template/disposition/currency once and each strict posting member binds its zero-based template slot, closed role/account, debit-or-credit side, canonical atoms, source/destination EntityKeyV1 or canonical absence, and authority stable ID
    And total debit atoms equal total credit atoms within that currency and event
    And an unknown account, dynamic account name, wrong event template, cross-currency posting, omitted leg, duplicate leg, unbalanced amount, or template-version mismatch rejects the whole transition
    And every zero-delta entitlement_delta has an empty application plan, canonical zero postings, unchanged T_N/T_C/T_A amounts, and a newly derived aggregate/control-value projection for its newer source revision
    And the entitlement_delta never posts money already owned by an application event, so no application can be recognized twice or leave an intermediate spendable balance

    Examples:
      | event case | flow |
      | entitlement_delta | aggregate and application-plan authority only, with canonical zero postings |
      | lot_create | compensation_expense to immutable immature liability |
      | debt_offset | compensation_expense to the exact recovered debt range |
      | lot_cancel | the exact causal active-liability account to compensation_expense_reversal |
      | debt_reopen | the exact recovered debt range back to debt_receivable against compensation_expense_reversal |
      | submitted_negative_fence | pending_submitted_recourse against compensation_expense_reversal without moving submitted liability |
      | enforcement_coverage_derecognize for unpaid_forfeiture | forfeited_entitlement to compensation_expense_reversal while current enforcement coverage reclassifies to cumulative derecognition and the forensic forfeited lot does not change |
      | enforcement_coverage_derecognize for paid_clawback | paid_clawback_enforcement_coverage to compensation_expense_reversal while the existing debt_receivable/enforcement_recourse pair remains intact, the forensic paid lot and existing DebtRange do not change, and no new or reversed debt posts |
      | dust_reclassify | canonical zero postings while only the signed dust-cycle/control classification changes |
      | maturity, withhold, release_hold, partition_lot, prepare_payout, abort_preparation, or void_payout | exact source liability state to exact destination liability state with atom conservation |
      | submit_payout | reserved_prepared liability to reserved_submitted liability |
      | payout_succeeded without pending submitted recourse | reserved_submitted liability to authenticated_rail_disbursement |
      | payout_succeeded with pending submitted recourse | full reserved_submitted liability to authenticated_rail_disbursement plus exact pending recourse to debt_receivable |
      | payout_confirmed_failed without pending submitted recourse | full reserved_submitted liability to its exact current unpaid state |
      | payout_confirmed_failed with pending submitted recourse | full submitted liability returns to its exact current unpaid state, exact pending recourse becomes cancelled entitlement, and unaffected atoms remain unpaid |
      | debt_create from a later negative application against an already-paid range | the exact paid atom range to debt_receivable against compensation_expense_reversal without rewriting authenticated_rail_disbursement |
      | debt_create resolving pending submitted recourse after success | canonical zero additional postings because payout_succeeded owns the pending-recourse-to-debt flow |
      | debt_create from a paid fraud-clawback decision | uncovered paid coverage to paid_clawback enforcement coverage plus debt_receivable against enforcement_recourse, without changing T_A or authenticated rail history |
      | forfeit | exact held liability to forfeited_entitlement under its purpose-signed decision |
      | debt_writeoff | debt_writeoff_expense to debt_receivable under its purpose-signed decision |

  Scenario: Journal templates expand into one deterministic ordered member array
    Given a nonzero journal disposition selects canonical atom-flow units from its already signed application descriptor, PayoutLotRangeSetV1, PayoutLotChildRangeSetV1, PayoutLotAtomRangeSetV1, PendingFailureResolutionDescriptorSetV1, PendingSuccessResolutionDescriptorSetV1, DebtCreateResultSetV1, EnforcementAuthorizedRangeSetV1, EnforcementCoverageResultSetV1, or DebtWriteoffAuthorizedRangeSetV1 as required by the closed table below
    When JournalPostingSetV1 members are derived
    Then the closed account codes are exactly compensation_expense, compensation_expense_reversal, immature_liability, mature_payable_liability, withheld_liability, reserved_prepared_liability, reserved_submitted_liability, pending_submitted_recourse, authenticated_rail_disbursement, cancelled_entitlement, uncovered_paid_coverage, paid_clawback_enforcement_coverage, enforcement_recourse, forfeited_entitlement, debt_receivable, and debt_writeoff_expense
    And liability(state) maps immature, mature_payable, withheld, reserved_prepared, and reserved_submitted to their identically prefixed closed liability account and rejects every other state
    And failure_destination(role,state) maps affected_cancelled to cancelled_entitlement and maps unaffected_returned only to liability(mature_payable) or liability(withheld)
    And each flow clause `source to destination` expands for each authority unit to exactly two adjacent members: source_debit with the source account and debit side, then destination_credit with the destination account and credit side, both for the unit's exact positive atoms and source/destination EntityKeyV1/authority ID
    And clauses expand in their table order, units inside a clause retain the exact canonical order of the named signed authority/result set, no two authority or state boundaries merge, and the concatenated members receive contiguous zero-based template slots
    And in a two-clause row the unit-source text before the literal `then` feeds clause zero and the text after it feeds clause one; in a one-clause row the entire unit source feeds clause zero
    And zero dispositions produce no unit or member; no implementation may aggregate units, invent a zero leg, reorder clause-major expansion, choose another account alias, or derive a unit from SQL iteration order

  Scenario Outline: Every journal disposition has one exact ordered flow pattern
    Given journal disposition "<disposition>" uses canonical unit source/order "<units>"
    Then clause zero is "<clause zero>"
    And clause one is "<clause one>"
    And no further clause is permitted

    Examples:
      | disposition | units | clause zero | clause one |
      | entitlement_delta_zero | no units | canonical_zero | canonical_absence |
      | dust_reclassify_zero | no units | canonical_zero | canonical_absence |
      | resolved_submitted_success_debt_create_zero | no units | canonical_zero | canonical_absence |
      | lot_create | one application descriptor/result range | compensation_expense to immature_liability | canonical_absence |
      | debt_offset | one application descriptor selected DebtRange | compensation_expense to debt_receivable | canonical_absence |
      | lot_cancel | one application descriptor selected whole/child PayoutLot | liability(prior-state) to compensation_expense_reversal | canonical_absence |
      | debt_reopen | one application descriptor selected mapped recovery/debt range | debt_receivable to compensation_expense_reversal | canonical_absence |
      | submitted_negative_fence | one application descriptor pending range | pending_submitted_recourse to compensation_expense_reversal | canonical_absence |
      | enforcement_coverage_derecognize_unpaid | one application descriptor coverage range | forfeited_entitlement to compensation_expense_reversal | canonical_absence |
      | enforcement_coverage_derecognize_paid | one application descriptor coverage range | paid_clawback_enforcement_coverage to compensation_expense_reversal | canonical_absence |
      | maturity | PayoutLotRangeSetV1 order | liability(prior-state) to liability(result-state) | canonical_absence |
      | withhold | PayoutLotRangeSetV1 order | liability(prior-state) to withheld_liability | canonical_absence |
      | release_hold | PayoutLotRangeSetV1 order | withheld_liability to liability(result-state) | canonical_absence |
      | partition_lot | PayoutLotChildRangeSetV1 child order | liability(parent-prior-state) to liability(child-revision-1-state) | canonical_absence |
      | prepare_payout | PayoutLotAtomRangeSetV1 order | mature_payable_liability to reserved_prepared_liability | canonical_absence |
      | abort_preparation | PayoutLotAtomRangeSetV1 order | reserved_prepared_liability to liability(result-state) | canonical_absence |
      | void_payout | PayoutLotAtomRangeSetV1 order | reserved_prepared_liability to liability(result-state) | canonical_absence |
      | submit_payout | PayoutLotAtomRangeSetV1 order | reserved_prepared_liability to reserved_submitted_liability | canonical_absence |
      | payout_succeeded_no_pending | PayoutLotAtomRangeSetV1 order | reserved_submitted_liability to authenticated_rail_disbursement | canonical_absence |
      | payout_succeeded_with_pending | PayoutLotAtomRangeSetV1 order then PendingSuccessResolutionDescriptorSetV1 order | reserved_submitted_liability to authenticated_rail_disbursement | debt_receivable to pending_submitted_recourse |
      | payout_confirmed_failed_no_pending | PendingFailureResolutionDescriptorSetV1 descriptor/result-range order | reserved_submitted_liability to liability(result-state) | canonical_absence |
      | payout_confirmed_failed_with_pending | PendingFailureResolutionDescriptorSetV1 descriptor/result-range order then PendingFailureSegmentSetV1 order | reserved_submitted_liability to failure_destination(result-role,result-state) | cancelled_entitlement to pending_submitted_recourse |
      | economic_reversal_debt_create | DebtCreateResultSetV1 order | debt_receivable to compensation_expense_reversal | canonical_absence |
      | paid_enforcement_debt_create | EnforcementAuthorizedRangeSetV1 zipped with EnforcementCoverageResultSetV1 in identical order then EnforcementAuthorizedRangeSetV1 zipped with DebtCreateResultSetV1 in identical order | uncovered_paid_coverage to paid_clawback_enforcement_coverage | debt_receivable to enforcement_recourse |
      | forfeit | EnforcementAuthorizedRangeSetV1 zipped with EnforcementCoverageResultSetV1 in identical order | withheld_liability to forfeited_entitlement | canonical_absence |
      | debt_writeoff | DebtWriteoffAuthorizedRangeSetV1 zipped with DebtWriteoffResultSetV1 in identical order | debt_writeoff_expense to debt_receivable | canonical_absence |

  Scenario: Journal state keys, authority IDs, and nested flattening are exact
    Given one canonical flow unit is expanded into its debit and credit posting members
    Then a liability account leg uses the exact participating PayoutLotValueProjectionV1 EntityKeyV1, pending_submitted_recourse uses the exact PendingSubmittedNegativeValueProjectionV1 key, uncovered_paid_coverage uses the forensic PayoutLot key, and cancelled_entitlement uses the group-final whole or selected cancelled PayoutLot key
    And unpaid forfeit uses the helper-created selected PayoutLot child as its withheld-liability key and the whole source only when PayoutLotPartitionHelperSetV1 is canonically empty
    And paid_clawback_enforcement_coverage and forfeited_entitlement use the newly created current EnforcementDispositionCoverage key for forfeit or paid-enforcement debt creation, while enforcement_coverage_derecognize uses the group-final whole or selected derecognized coverage key and never its terminal partition parent or current remainder
    And every debt_receivable leg uses the group-final whole or selected non-partition-parent DebtRangeValueProjectionV1 EntityKeyV1 carried by the applicable ApplicationRealizedValueProjectionSetV1, DebtCreateResultSetV1, or DebtWriteoffResultSetV1: debt_offset selects recovered, debt_reopen and debt_create select outstanding, and debt_writeoff selects written_off
    And a partial operation never uses its terminal transaction-start parent key or an unselected remainder key
    And compensation_expense, compensation_expense_reversal, authenticated_rail_disbursement, enforcement_recourse, and debt_writeoff_expense are nominal accounts whose corresponding source or destination EntityKeyV1 has canonical absence
    And an application unit uses application ID as authority stable ID, a PayoutLotRangeSetV1 unit uses payout-lot ID, a partition child unit uses partition_lot helper event ID, a PayoutLotAtomRangeSetV1 unit uses payout ID, a pending-success unit uses PendingSubmittedNegative ID, a failure return unit uses failure-descriptor ID, a failure bridge unit uses PendingSubmittedNegative ID, a DebtCreateResultSetV1 unit uses debt_create event ID, an enforcement unit uses CompensationForfeitureDecisionV1 decision ID, and a writeoff unit uses DebtWriteoffDecisionV1 decision ID
    And payout_confirmed_failed clause zero flattens outer PendingFailureResolutionDescriptorSetV1 order then each descriptor's PendingFailureLotResultSetV1 range order
    And payout_confirmed_failed clause one uses that same descriptor-major order and within each descriptor uses PendingFailureSegmentSetV1 member order, pairing every segment to its unique covering affected result range
    And any account/key kind mismatch, alternate candidate ID, absent key for a state account, present key for a nominal account, or alternate nested flattening rejects JournalPostingSetV1

  Scenario: Every compensation event updates its state and control totals atomically
    Given aggregate S changes from prior target A_old to exact target A_new
    When its compensation transaction commits
    Then one serializable transaction appends the signed entitlement_delta and its ordered application events, changes exact aggregate, lot-range, debt-range, pending-recourse, enforcement-coverage, dust-cycle, and control states, and applies each application event's sole balanced journal template
    And source-derived program net T_N is the checked sum of every immutable eligible candidate's current nonnegative externally funded settlement N expressed as 1000000 share atoms across all operators in that currency
    And the policy ceiling T_C is the checked sum of each such current N multiplied by that candidate's grant-bound rate_ppm
    And entitlement target T_A is the checked sum of every current aggregate A
    And T_A equals T_C and zero is less than or equal to T_A and T_A is less than or equal to T_N
    And the entitlement_delta binds ApplicationDescriptorSetV1 and its complete hash without child event hashes, while each child binds that set hash/index/descriptor hash and the entitlement_delta complete hash without a hash cycle
    And plan atoms equal the signed delta magnitude exactly, target half-open ranges do not overlap within the same immutable target/root/lineage namespace, every present plan/causal/target/result range length equals its exact atoms, and no amount remains unapplied
    And every event in the group binds the same prior committed per-currency control-leaf complete hash and final resulting control-value-projection complete hash, while each event binds its own event-owned JournalPostingSetV1 complete hash without binding the post-commit leaf hash
    And all consecutive event-group sequences, state mutations, source-bound ranges, and control totals either commit once or none commit

  Scenario: Control commitments have one acyclic preimage order
    Given a compensation event group starts from committed control leaf C0 and ledger hash H0
    When its canonical signed events are constructed and committed
    Then ControlValueProjectionV1 contains the exact post-state counts, bounded amounts, state-value projection-set hashes, journal totals/template, currency/unit/scale, and preselected stable current event IDs/group indices, but excludes the current ledger sequence/hash, current event bytes/complete hashes/signatures, causal backreferences to those hashes, and resulting control-leaf hash
    And every projected state value excludes its creating current-event complete hash and ledger position while retaining stable entity IDs, revisions, immutable lineage, and all economic/security fields
    And each event signs C0's complete hash, H0 or the immediately preceding event hash, and the final ControlValueProjectionV1 complete hash
    And only after the final event and SQL state commit does CompensationControlTotalLeafV1 bind the final ledger sequence/hash plus that exact projection complete hash and its reproduced fields
    And the later CompensationLedgerHeadV1 commits the canonical set of those post-commit leaves
    And no current event signs a value whose preimage contains that event's own bytes, signature, complete hash, resulting ledger hash, or resulting leaf complete hash

  Scenario: Every control value-projection set has one canonical preimage
    Given one ControlValueProjectionV1 commits source-interval, aggregate, application-range, payout-lot, debt-range, debt-recovery-application, pending-submitted-negative, enforcement-disposition-coverage, dust-cycle, and preexisting rail-result member sets
    When any ControlProjectionMemberSetV1 complete hash is computed
    Then its preimage is the exact JCS object containing schema/network/protocol, currency/unit/scale, the closed projection-kind tag, canonical nonnegative member count, and one members array of the complete strict value-projection objects
    And members are ordered by the projection-kind-specific canonical stable sort key defined in features/tower/tamper/tamper_compensation_variants.feature, the encoded count equals the array length and matching control count, and the canonical empty array is the only zero-member representation
    And duplicate stable keys, duplicate member bytes, an omitted or extra member, a member hash in place of its object, an unknown kind or field, alternate ordering, or a current-group audit-envelope field changes the set hash and rejects the event group

  Scenario: The ledger-wide cap is an independent backstop across every operator
    Given individually valid-looking compensation events across one or many operators and policy versions
    When source intervals overlap, an aggregate/event is omitted or duplicated, a journal template does not balance, a rate-weighted target differs, a total overflows, or T_A would exceed T_N
    Then no positive, payable, reservation, or payout transition commits
    And the only permitted state change makes compensation for that currency conflict_quarantined or pending_reconciliation with one durable incident reference
    And affected immature and mature_payable lots become withheld, reserved_prepared lots use the exact atomic abort-or-void then withhold group defined below, and reserved_submitted lots remain rail-locked under the incident while later payouts are held
    And periodic full replay from the authoritative source and compensation ledgers must reproduce CompensationControlTotalSetV1 complete hash exactly
    And no operator-local check, administrator override, event signature, or head signature can bypass the program invariant

  Scenario: A late reversal cannot be hidden by the program cap
    Given previously paid compensation later has a refund, chargeback, fee correction, or other authoritative negative revision
    When the current program targets decrease
    Then T_N, T_C, and T_A move to the exact new current values through one signed negative delta and balanced postings
    And any already-disbursed amount above the new target is represented exactly by causal operator debt or an independently authorized debt write-off
    And the historical rail payment is not falsely described as unmade or silently excluded from program exposure reporting

  # --- payout lots ----------------------------------------------------------

  Scenario: Every positive entitlement delta offsets debt first and creates only exact excess lots
    Given a signed positive compensation delta commits
    When its ordered application plan commits in the same transaction
    Then each lot has a unique ID, operator, Tower, settlement, compensation-event, funding-slice, source-lot, currency/unit/scale, exact atoms, maturity tuple, policy, prior-ledger binding, and canonically absent dust-cycle fields until maturity
    And each debt_offset application binds a unique half-open source-event range to the first available same-operator same-currency outstanding DebtRange in canonical debt-priority order
    And debt-offset atoms plus created-lot atoms sum exactly to the positive delta, with zero created lots allowed when debt consumes it all
    And a lot never changes owner, Tower, source, currency, scale, or amount

  Scenario: PayoutLotV1 separates its acyclic value projection from post-commit audit linkage
    Given any source-bound payout lot is created or changes state
    When its compensation transaction signs the transition
    Then the event binds the prior committed PayoutLotV1 complete hash when one exists and the resulting PayoutLotValueProjectionV1 complete hash
    And the value projection contains every immutable identity/lineage/range/amount field plus exact conditional state, revision, hold, dust, reservation, instruction, fence, and rail-result values, but excludes the creating current event bytes/hash/signature, current ledger position/hash, and resulting full lot hash
    And only after commit does PayoutLotV1 attach the creating compensation-event ID/complete hash and ledger sequence to that exact value projection
    And later CAS uses the committed lot ID, state revision, full prior lot hash, value-projection hash, and reservation ID without mutating immutable lineage

  Scenario Outline: Payout-lot state transitions are exhaustive
    Given a payout lot is "<from>"
    When "<event>"
    Then it becomes "<to>"

    Examples:
      | from | event | to |
      | immature | maturity tuple passes, current PayoutEligibilityDecisionV1 is eligible, and canonical HoldReferenceSetV1 is empty | mature_payable |
      | immature | maturity tuple passes while current payout eligibility is held or canonical HoldReferenceSetV1 is nonempty | withheld |
      | immature | cumulative entitlement removes it before maturity | cancelled |
      | immature | any current HoldReferenceV1 kind begins | withheld |
      | withheld and not_matured | maturity tuple passes while any current eligibility, tax, reconciliation, policy, enforcement, unavailability, or dust-review hold remains | withheld with matured status and immutable maturity evidence/Core tuple |
      | mature_payable | any current HoldReferenceV1 kind begins | withheld |
      | withheld | another HoldReferenceV1 begins or any exact revisioned external or compensation-created hold authority advances to a still-held/open current successor while the resulting set remains nonempty | withheld with the exact revised current reference/scope set |
      | withheld | one hold clears but canonical HoldReferenceSetV1 remains nonempty | withheld with the exact remaining set |
      | withheld | all holds clear, current payout eligibility is eligible, and maturity has passed | mature_payable with the canonical empty hold set |
      | withheld | all holds clear, current payout eligibility is eligible, and maturity has not passed | immature with the canonical empty hold set |
      | withheld | signed scoped forfeiture decision applies to the whole exact lot or a partitioned child | forfeited |
      | immature | an exact adjustment-boundary partition transaction creates conserving immutable child lots | partitioned |
      | mature_payable | an exact payout, adjustment, or forfeiture-boundary partition transaction creates conserving immutable child lots | partitioned |
      | withheld | an exact adjustment or forfeiture-boundary partition transaction creates conserving immutable child lots | partitioned |
      | mature_payable | one preparation transaction reserves exact lots and commits PayoutPreparationV1 with the payout instruction canonically absent | reserved_prepared |
      | reserved_prepared | the durable one-way send fence commits for that instruction | reserved_submitted |
      | reserved_prepared | signed abort_preparation commits before any instruction exists with no hold or negative adjustment | mature_payable |
      | reserved_prepared | signed abort_preparation when no instruction exists or void_payout when one exists commits before the send fence while a preexisting current HoldReferenceSetV1 is nonempty | the exact maturity-derived unreserved state, which is withheld under that preexisting current hold set |
      | reserved_prepared | signed abort_preparation or void_payout commits before the send fence because a negative adjustment requires a new reconciliation hold | the abort or void first returns the range to its exact maturity-derived unreserved state and an ordered same-group withhold child creates the reconciliation hold, leaving the group-final state withheld; only a later serializable application transaction may target that committed group-final state and partition/cancel it |
      | reserved_prepared | a signed void_payout commits for an existing instruction before the send fence with no hold or negative adjustment | mature_payable |
      | reserved_prepared | bounded preparation-authorization deadline sweep signed-aborts when no instruction exists or signed-voids when one exists before any send fence | the exact maturity-derived unreserved state; ordered same-group withhold children leave it withheld only when current evidence independently requires tax-authority-unavailable or eligibility-authority-unavailable holds, while deadline expiry alone creates no persistent hold |
      | reserved_submitted | rail state is pending, timeout, unavailable, or unknown | reserved_submitted |
      | reserved_submitted | authenticated rail failure is final, current PayoutEligibilityDecisionV1 is eligible, and canonical HoldReferenceSetV1 is empty | mature_payable |
      | reserved_submitted | authenticated rail failure is final and any current eligibility, tax, reconciliation, policy, enforcement, unavailability, or dust-review hold applies | withheld with the canonical complete hold-reference set |
      | reserved_submitted | authenticated rail failure is final with a pending submitted negative | its full instruction returns before affected ranges are partitioned/cancelled and unaffected ranges take their current payable-or-held state |
      | reserved_submitted | authenticated rail success is final | paid |
      | immature | an exact whole-lot or partition-child negative application applies | cancelled |
      | mature_payable | an exact whole-lot or partition-child negative application applies | cancelled |
      | withheld | an exact whole-lot or partition-child negative application applies without a forfeiture finding | cancelled |
      | paid without current enforcement coverage | a nonoverlapping negative application creates a separate same-currency DebtRange | paid |
      | paid with current paid_clawback coverage | enforcement_coverage_derecognize consumes that coverage without creating or reversing debt | paid without lot mutation |

  Scenario Outline: Invalid payout-lot transitions fail without mutation
    Given a payout lot transition is "<transition>"
    When its CAS validation runs
    Then no state, reservation, rail instruction, balance, or receipt changes

    Examples:
      | transition |
      | immature directly to reserved_prepared or reserved_submitted |
      | immature directly to paid |
      | mature_payable directly to paid |
      | withheld directly to reserved_prepared or reserved_submitted |
      | reserved_prepared to paid without the send fence and authenticated success |
      | reserved_submitted to mature_payable without authenticated final rail failure |
      | reserved_prepared or reserved_submitted to another payout ID |
      | reserved_submitted to withheld or cancelled before authenticated final rail failure |
      | paid to mature_payable |
      | reserved_prepared with no instruction submitted without first creating and verifying the exact signed instruction |
      | abort_preparation when an instruction already exists or void_payout when it is canonically absent |
      | partitioned, cancelled, or forfeited to a payable state |
      | any transition with another operator, currency, destination, or lot amount |
      | a reused causal event ID with different canonical bytes or hash |

  Scenario: Abort or void plus every newly required compensation hold is one ordered atomic group
    Given one reserved_prepared payout has no send fence and its exact current classifications, preexisting HoldReferenceSetV1, and newly required external-materialization or compensation-created hold reasons are fixed at the transaction authority tuple
    When abort_preparation commits if no instruction exists or void_payout commits if its exact instruction exists
    Then the parent event is first, removes the reservation, and derives each selected range's exact maturity-based unreserved state using only preexisting current classification and hold authorities
    And its canonical AbortVoidHoldPlanSetV1 is empty when no new hold materialization is required, otherwise it prescribes every and only later withhold child in canonical hold-kind, derived hold-reference-ID, and scope-hash order; ordinal equals array index, child ID is the fixed-length unpadded case-preserving base64url SHA-256 digest over the UTF-8 bytes of the strict JCS array [AbortVoidWithholdEventV1-id-v1,network-ID,parent-event-ID,ordinal], and expected group index is parent index plus one plus ordinal
    And each prescribed child consumes the exact earlier intermediate PayoutLotValueProjectionV1 without a current-group full-object hash, either materializes one exact current external decision/incident authority or creates/revises one compensation hold series according to its closed source tag, and moves its exact range to withheld with the complete resulting HoldReferenceSetV1
    And preparation-authorization expiry by itself only removes the reservation; it is neither policy_restriction nor authority-unavailability evidence and cannot create policy_hold
    And the parent, plan, children, intermediate/final projections, journal postings, compensation control leaf, and ledger hashes commit in one serializable transaction or none do, so no unreserved intermediate state is externally observable
    And omitted, extra, reordered, duplicated, stale-series, wrong-range, wrong-reason, wrong-index, or nonfinal child output rejects the whole group before reservation, lot, hold, journal, control, or ledger state changes

  Scenario: Payout-lot CAS identity is exact
    Given lot L is at state revision R with state hash H
    When maturity, hold, adjustment, reservation, or rail workers race
    Then one transition may compare-and-swap L, R, H, and its current reservation ID
    And every loser rereads the committed state before taking another action
    And a rail call occurs only after the exact reserved_prepared state, signed instruction, and irreversible reserved_submitted send fence commit

  Scenario: A general unpaid boundary splits one lot without changing atoms or lineage
    Given payout, negative-adjustment, or forfeiture application needs X atoms from an unreserved immature, mature_payable, or withheld lot L containing Y atoms where zero is less than X and X is less than Y
    When its serializable application transaction commits
    Then L becomes terminal partitioned and one selected child plus one or two immutable range-ordered remainder children contain X and Y minus X atoms
    And every child preserves L's operator, Tower, settlement, event, funding/source lineage, currency/unit/scale, maturity, and policy plus L as parent
    And every child initially preserves L's prior state, after which only the selected child takes the exact planned reserved_prepared, cancelled, or forfeited transition in that transaction
    And the two or three child ranges are nonoverlapping and exactly cover L, child atoms sum exactly to Y, and no future transition may use parent L as payable
    And a reserved_prepared lot must first signed-abort or signed-void as a whole before adjustment partition, while reserved_submitted is never partitioned before authenticated rail resolution

  Scenario: Partial PayoutLot consumers use one canonical helper block
    Given lot_cancel, prepare_payout, or unpaid forfeit selects one contiguous proper subrange from each of P committed unreserved payout-lot parents
    When its atomic event group commits
    Then PayoutLotPartitionHelperSetV1 prescribes exactly one partition_lot helper per proper-subrange parent, zero helpers for whole sources, and canonical parent-key order
    And the consecutive helper block commits immediately before the one consumer, each helper binds that consumer's stable ID/group index and exact descriptor/preparation/decision range authority, and both sides bind the same helper-set hash without current event hashes
    And each helper terminalizes its parent, creates conserving live-state children at revision 1, and owns only the partition-conservation journal before the consumer advances selected children to revision 2 and owns the cancellation, reservation, or forfeiture posting
    And only the consumer consumes the application or decision; every remainder stays live at revision 1, and the entire helper-plus-consumer chain commits once or not at all

  Scenario: Preparation, attestation, instruction, and send have an acyclic order
    Given exact mature payable lots pass payout selection
    When the preparation transaction commits
    Then PayoutPreparationV1 is exactly the signed TowerCompensationReceiptV1 prepare_payout variant and its complete hash, not a second object
    And that preparation reserves the lots, binds a Core preparation-authorization deadline derived from signed PayoutPolicyV1, and encodes tax decision, CompensationLedgerHeadV1, TowerPayoutInstructionV1, and send fence as canonically absent
    When a current authoritative zero TaxWithholdingDecisionV1 commits for that preparation
    Then the decision binds the PayoutPreparationV1 ID and complete hash
    And a later unexpired CompensationLedgerHeadV1 may attest the committed preparation, its exact zero tax-decision and payout-authority-set binding, journal posting, and control totals at an independently assigned head-ledger Core commit tuple
    And only after that head commits may the payout-authorization signer create and atomically commit one immutable TowerPayoutInstructionV1 at an independently assigned payout-authorization-ledger Core tuple without mutating the preparation or lots
    When the send-fence transaction verifies and attaches that instruction
    Then submit_payout moves the lots to reserved_submitted before the one rail call
    And a crash or outage before an instruction exists uses abort_preparation while one after the instruction exists but before the send fence uses void_payout

  Scenario: Below-threshold and sub-minor dust has a bounded visible lifecycle
    Given canonical mature payable lots total B atoms under a signed currency/rail payout policy
    When floor(B divided by K) is below the positive minimum_payout_minor
    Then no partition, reservation, payout instruction, or rail call occurs
    And every atom remains in source-bound mature_payable lots and combines with later same-operator same-currency atoms
    And the first signed event that leaves positive mature liability below threshold atomically compare-and-swaps the operator/currency current-open-cycle pointer from absent, assigns the next monotonic cycle generation and stable ID, and binds the prior terminal-cycle hash or canonical first-generation absence, first-below-threshold Core tuple, payout-policy series/revision/hash, minimum, finite maximum_dust_carry_interval, exact review deadline, DustLotReferenceSetV1 complete hash/member count/total atoms, state revision, prior hash, and resulting value-projection hash
    And every affected current lot records that dust-cycle ID, revision, complete hash, and review deadline
    And every later maturity, hold, release, authorized forfeiture, causal negative adjustment, policy reclassification, or added lot below threshold binds and advances that same cycle revision without changing its first-below-threshold tuple or deadline
    When that deadline is reached without crossing the threshold
    Then one idempotent signed dust-review hold moves the exact lots to withheld with an open review reference
    And the hold event binds the dust-cycle ID/revision/hash and the unchanged deadline
    And operator notification, accounting liability totals, and incident metrics expose the exact atoms rather than dropping or rounding them

  Scenario Outline: Every first transition below threshold starts the cycle in the same transaction
    Given positive mature same-operator same-currency liability has no dust cycle
    When "<cause>" first leaves its canonical payable selection below the current signed threshold
    Then "<event>" creates the one cycle using that event's Core commit tuple as the first-below-threshold anchor and binds every required cycle field
    And no restart, delayed sweep, old threshold, operator time, or later event may substitute a newer anchor
    And a transition leaving zero liability closes an existing cycle or keeps canonical no-cycle absence rather than starting one

    Examples:
      | cause | event |
      | maturity makes the first atoms payable | maturity |
      | release_hold returns atoms but the selection remains below threshold | release_hold |
      | withhold removes part of an above-threshold payable selection | withhold |
      | a causal negative adjustment leaves a positive below-threshold remainder | lot_cancel |
      | a scoped forfeiture leaves a positive below-threshold untainted remainder | forfeit |
      | authenticated payout failure returns a positive below-threshold unaffected remainder | payout_confirmed_failed |
      | a stricter applicable payout-policy revision raises the threshold above the current selection | dust_reclassify |
  Scenario: Payout remainder preserves or starts exactly one dust cycle
    Given a payout preparation leaves retained remainder R greater than zero
    When its prepare_payout event commits
    Then an existing operator/currency dust cycle preserves its first-below-threshold tuple and deadline
    And if no cycle exists, the event creates one using its Core preparation tuple as the first anchor and derives one deadline from the bound PayoutPolicyV1
    And the prepare event binds the dust-cycle ID, prior/resulting revision/value-projection hash, first anchor, policy hash, interval, deadline, remainder DustLotReferenceSetV1 complete hash/member count/total atoms, and R
    And retry, restart, payout failure, later accrual, or account closure cannot reset that cycle silently

  Scenario Outline: An existing dust cycle has one signed continuation or terminal transition
    Given operator/currency dust cycle D exists and an exact causal event leaves current dust atoms "<remaining>"
    When that event commits
    Then its dust-cycle effect is "<effect>"
    And the event binds D's ID, transaction-start committed full hash or current-group creation absence, immediate prior revision/value-projection hash, next revision/resulting value-projection hash, prior/current group indices, unchanged first-below-threshold tuple and deadline, exact resulting DustLotReferenceSetV1 complete hash/member count/total atoms, and terminal reason or canonical absence

    Examples:
      | remaining | effect |
      | positive and below threshold | D remains open at its next revision |
      | positive and at or above threshold before preparation | D remains open until the exact prepare_payout event consumes or preserves its remainder |
      | zero because prepare_payout reserves all selected atoms with R equal to zero | D becomes terminal_cleared with reason threshold_consumed |
      | zero because a causal negative entitlement adjustment cancels every remaining lot | D becomes terminal_cleared with reason liability_cancelled |
      | zero because a scoped CompensationForfeitureDecisionV1 forfeits every remaining tainted lot | D becomes terminal_cleared with reason fraud_forfeited |

  Scenario: Terminal dust history permits only a fresh monotonic generation
    Given operator/currency cycle generation G is terminal_cleared and its current-open-cycle pointer is absent
    When later positive mature liability first falls below the applicable signed threshold
    Then the causal signed event may compare-and-swap that absent pointer to exactly generation G plus 1 with a new stable cycle ID
    And the new cycle binds G's terminal complete hash as previous-cycle hash and derives a fresh first-below-threshold anchor and deadline from the newly applicable policy
    And the terminal G record remains immutable and cannot be reopened, reused, replaced, or treated as the new cycle's prior open revision
    And a skipped/reused generation, wrong prior terminal hash, concurrent second cycle, or open pointer rejects the entire transition

  Scenario: Release, preparation, and adjustment cannot orphan an existing dust cycle
    Given a release_hold, prepare_payout, entitlement adjustment, or scoped forfeiture affects lots bound to an open dust cycle
    When the compensation transaction validates its exact resulting lot set
    Then positive remainder advances the same cycle and terminal zero closes it through the preceding closed transition table
    And a prepare_payout with no prior cycle and R equal to zero carries canonical no-cycle absence
    And a prepare_payout with a prior cycle and R equal to zero carries that cycle's terminal_cleared transition rather than canonical absence
    And no affected event can omit, replace, restart, backdate, or silently delete the cycle

  Scenario: A final dust review cannot invent a payout or silently erase a liability
    Given dust-review lots remain below the ordinary payout threshold
    When an authorized review resolves them
    Then the lots return to mature_payable only if later same-currency atoms make the selection meet the ordinary signed minimum and every other payout check
    And every sub-minor remainder stays an exact same-currency operator liability under its open signed review state
    And account closure remains closure_pending while that liability exists
    And no v1 age rule, account-deletion request, Tower revocation, administrator action, debt-writeoff decision, or rail callback can cancel, write off, redirect, donate, escheat, or forfeit untainted dust
    And only an existing exact CompensationForfeitureDecisionV1 may forfeit tainted dust under the ordinary fraud contract
    And any other future terminal disposition requires its own founder-approved legal/accounting state machine and cannot be inferred from this contract

  # --- negative adjustments and debt ---------------------------------------

  Scenario: A negative candidate adjustment consumes only its own causal lots
    Given settlement S has a negative exact entitlement delta
    When Roger Core applies it
    Then it selects S's surviving positive source ranges by the deterministic reverse-recognition contract
    And for each selected range it first derecognizes an exact current unpaid_forfeiture or paid_clawback EnforcementDispositionCoverage when one exists, regardless of the forensic lot state
    And otherwise it reverses any still-active debt-offset application and reopens that exact prior DebtRange
    And otherwise it partitions as needed and cancels the range if immature, mature_payable, withheld, or signed-aborted or signed-voided and returned
    And otherwise it creates a nonoverlapping PendingSubmittedNegative if reserved_submitted or a nonoverlapping DebtRange only if the paid range has no current enforcement coverage
    And it never consumes another settlement, operator, source event range, currency, or Tower's atoms
    And derecognized enforcement coverage plus reopened debt plus cancelled unpaid plus pending submitted plus newly debt-covered uncovered-paid atoms equals the negative delta magnitude exactly

  Scenario: Reversing a positive event that recovered old debt reopens only that same debt range
    Given positive event E previously created DebtRecoveryApplication R from E's source range to exact recovered DebtRange D
    When a later authoritative negative revision reverses that source range
    Then one debt_reopen event marks R's exact affected range reversed and changes that same range of D from recovered to outstanding
    And DebtRecoveryApplication R defines an equal-length affine map from source range [s0,s1) to target DebtRange range [t0,t1), where s1 minus s0 equals t1 minus t0
    And partial reversal of source range [r0,r1) partitions the recovery application and its actual dependent DebtRange into immutable nonoverlapping ranges, selects only target range [t0 plus r0 minus s0,t0 plus r1 minus s0), and makes every recovery child bind its aligned actual dependent DebtRange child stable identity through the one-way child-set relation
    And the reopened amount cannot exceed active recovery applications made by E or select a different debt, operator, currency, or source event
    And repeated or conflicting reversal cannot reopen the same recovery range twice

  Scenario Outline: A submitted negative remains fenced until one authenticated rail result
    Given reserved_submitted instruction I has a negative application over exact nonoverlapping atom ranges
    When submitted_negative_fence commits
    Then PendingSubmittedNegative binds TowerPayoutInstructionV1 I and exact committed PayoutSendFenceV1 complete hashes, negative event/plan/descriptor, one exact transaction-start submitted lot ID/hash/range and atoms, operator/currency, prior/result revision/hash, state pending, and no destination change or rail call
    And its atoms appear in pending-submitted-recourse controls while every submitted lot stays immutable and unavailable
    When authenticated final rail result "<result>" commits
    Then "<outcome>"
    And the pending record becomes "<state>"
    And the final result binds the rail result, PendingSubmittedNegativeSetV1 complete hash, and exactly PendingSuccessResolutionDescriptorSetV1 or PendingFailureResolutionDescriptorSetV1 complete hash selected by result, and may happen only once

    Examples:
      | result | outcome | state |
      | confirmed failure | the full instruction returns atomically, affected ranges partition and become cancelled, and unaffected ranges become mature_payable or withheld under current authority | resolved_failed |
      | success | the full instruction becomes paid and each affected pending range creates one exact outstanding DebtRange | resolved_paid |

  Scenario: V1 paid-lot recourse creates debt and never calls an external reversal rail
    Given a paid lot receives an exact causal negative entitlement adjustment
    When its compensation transaction commits
    Then the forensic payout lot remains paid and one operator/currency DebtRange binds its paid-lot ID/hash, immutable half-open atom range, payout instruction/result, adjustment event/application descriptor, exact atoms, terms/policy version, and prior/result debt state
    And exclusion constraints prove all DebtRanges over that paid lot are nonoverlapping and their sum never exceeds its authenticated paid atoms
    And successive partial revisions may cover only previously uncovered ranges
    And no bank debit, reversal instruction, external idempotency key, rail call, or claim of recovered funds exists
    And this path applies only to an adjustment; an authenticated rail return of the disbursed funds is a distinct event that restores entitlement and creates no debt
    And duplicate or concurrent adjustment delivery creates the same one DebtRange

  # A rail RETURN is not a negative entitlement adjustment. In a return the operator never
  # received the money and the platform holds it again, so the correct result is restored
  # entitlement and zero debt. Encoding a return as an adjustment would bill an operator for
  # cash the platform still has.

  Scenario: An authenticated post-success rail return restores entitlement and creates no debt
    Given a payout instruction reached paid on an authenticated successful rail result
    And the rail later returns the funds to the platform with an authenticated return advice bound to that instruction's external idempotency key
    When the return commits
    Then the affected paid atom ranges leave paid and become mature_payable in the operator ledger under their original grant-time snapshot and policy version
    And cumulative authenticated_rail_disbursement is reduced by exactly the returned amount
    And no DebtRange is created, reopened, or enlarged by the return
    And the operator's entitlement total is unchanged, because a return moves cash position rather than entitlement
    And the restored atoms are eligible for a later payout only through a new preparation, instruction, and send fence

  Scenario Outline: A rail return is distinguished from every adjacent outcome
    Given an authenticated rail event of kind "<event>"
    When the compensation transaction classifies it
    Then the result is "<classification>"

    Examples:
      | event                                                        | classification                                                  |
      | a confirmed failure before any disbursement                  | resolved_failed; ranges cancel or return to payable; no debt     |
      | an authenticated success                                     | paid; disbursement recognized                                    |
      | an authenticated return of previously disbursed funds        | restored to mature_payable; disbursement reduced; no debt        |
      | a negative entitlement adjustment against a paid range       | lot stays paid; one DebtRange is created                         |
      | an unauthenticated or unbound return advice                   | rejected; one bounded incident; no state change                  |

  Scenario: A return advice must bind the exact instruction it reverses
    Given a return advice whose external idempotency key, currency, unit, scale, or amount does not match an authenticated paid instruction
    When it is presented
    Then it is rejected before any ledger movement
    And one bounded open_postsend_disbursement incident records the mismatch

  Scenario: A duplicated or replayed return moves the ledger once
    Given one authenticated return advice is delivered several times or concurrently
    When each delivery commits
    Then exactly one return event exists for that instruction and range
    And repeated delivery produces byte-identical state with no additional restoration

  Scenario: A partial return restores only its exact returned range
    Given an authenticated return covers part of a paid instruction's atoms
    When it commits
    Then only the exact returned half-open range is restored to mature_payable
    And the unreturned remainder stays paid with its disbursement intact
    And the returned and retained ranges are nonoverlapping and sum to the original paid range

  Scenario: A return on a range already covered by debt resolves without double counting
    Given a paid range already carries an outstanding DebtRange from an earlier negative adjustment
    And an authenticated rail return then restores that same range
    When both facts serialize
    Then the returned atoms first extinguish the covering debt up to the returned amount
    And only any surplus becomes mature_payable
    And the operator is never charged twice nor credited twice for one range

  Scenario: Rail success racing pending negative adjustments appends one plus N ordered events
    Given reserved_submitted lots have an authenticated successful rail result and PendingSubmittedNegativeSetV1 contains N nonoverlapping records where N is at least one
    When the compensation transaction serializes both facts
    Then payout_succeeded at group index zero moves the exact submitted lots to paid and binds every authenticated rail-result field, PendingSubmittedNegativeSetV1, and PendingSuccessResolutionDescriptorSetV1 with one member per pending record
    And exactly N resolved_submitted_success debt_create events follow at group indices one through N in ascending PendingSubmittedNegative stable sort-key order
    And each debt_create binds exactly one descriptor and pending record, leaves its forensic lot paid, marks that record resolved_paid, and creates its one prescribed exact outstanding DebtRange
    And PendingSuccessResolutionDescriptorSetV1 and the child DebtCreateResultSetV1 objects exhaust PendingSubmittedNegativeSetV1 exactly once, while payout_succeeded alone owns the pending-recourse-to-debt journal posting and every debt_create has canonical zero additional postings
    And all one plus N events receive consecutive ledger sequences and linked prior/result state hashes in one atomic transaction, or none commit
    And N equal to zero uses the no-pending payout_succeeded shape with no descriptor or debt_create child, while omission, duplication, reordering, an extra child, or mixed variant fields rejects the group

  Scenario Outline: Debt creation has one closed origin and never consumes an application twice
    Given debt_create has origin "<origin>"
    When its exact relationships and journal disposition are checked
    Then "<contract>"
    And an unknown origin, both origin shapes, missing conditional field, second descriptor consumption, or posting under both events rejects the transaction

    Examples:
      | origin | contract |
      | economic_reversal | debt_create is the one application owner for its negative plan descriptor, binds no PendingSubmittedNegative, owns the debt-recognition posting, and covers a previously uncovered paid range |
      | resolved_submitted_success | the prior submitted_negative_fence remains the one application owner; debt_create binds that exact resolved_paid PendingSubmittedNegative and rail-success hash, has canonical zero additional postings, and creates only its prescribed DebtRange |
      | paid_enforcement_clawback | no entitlement-delta application exists; debt_create binds one purpose-signed CompensationForfeitureDecisionV1 plus its exhaustive ordered paid-lot, current paid_clawback EnforcementDispositionCoverage, and DebtRange result sets, owns only the enforcement journal disposition, and leaves T_N/T_C/T_A unchanged |

  Scenario: Operator debt has one currency-scoped state machine
    Given a paid compensation range cannot be externally reversed and has a separate outstanding DebtRange
    When later positive compensation in the same operator and currency is recognized
    Then exact new source-event ranges offset outstanding DebtRanges in canonical debt-priority order before a separate lot_create descriptor may create an immature payout lot
    And a partial offset first partitions the immutable DebtRange into conserving nonoverlapping children and changes only the exact selected child to recovered
    And DebtRecoveryApplication binds the positive event range to the actual recovered result DebtRange stable key/range and current-group value projection plus its transaction-start parent snapshot and DebtRangeChildRangeSetV1 or canonical whole-target absence, without a current-group full-object hash
    And an exact offset changes all selected debt ranges to recovered, while any excess source range belongs to the following independent lot_create descriptor/event and posting rather than debt_offset
    And no cross-currency, cross-operator, or unrelated external debit is inferred

  Scenario: Outstanding debt selection has one total priority order
    Given one operator/currency has one or more outstanding DebtRangeV1 leaf ranges
    When a positive entitlement application plan is constructed
    Then debt priority is ascending by root create Core global sequence, root creating-event group index, root stable DebtRange ID UTF-8 bytes, leaf half-open range start, leaf range end, and leaf stable ID UTF-8 bytes
    And partition children inherit the root create sequence/index/ID and differ only by their conserving leaf range and stable ID tie-breakers
    And debt_offset descriptors exhaust each earlier outstanding leaf before selecting a later one; when source atoms end inside that leaf they consume its minimal low-end prefix [range-start,range-start plus remaining-source), split at every debt/funding-source boundary, and precede any lot_create descriptor for excess source atoms
    And concurrent delivery, SQL collation, insertion order, worker choice, or restart cannot produce another valid target order

  Scenario: A later reversal derecognizes enforcement disposition without rewriting forensic history
    Given a purpose-signed unpaid_forfeiture or paid_clawback disposition covered an exact lot atom range whose underlying settlement target later decreases
    When a negative application reaches that range
    Then enforcement_coverage_derecognize marks the exact EnforcementDispositionCoverage range derecognized while the payout lot remains terminal forfeited or paid and its decision remains immutable
    And the event binds the original enforcement decision/evidence hashes and creating event ID, negative entitlement event/descriptor, nonoverlapping atom range, prior/result coverage value projections, exact kind-selected balanced journal posting, and control projection
    And it creates no payable lot, operator debt, refund claim, administrator credit, or rewrite of the original finding

  Scenario Outline: Immutable range-state objects partition without overlapping their parents
    Given a partial operation selects X atoms inside range-state object "<object>" containing Y atoms where zero is less than X and X is less than Y
    When the operation's serializable transaction commits
    Then the parent becomes terminal partitioned and contributes zero to active/current controls
    And ordered immutable selected and remainder children preserve parent ID, source and target lineage, operator, currency/unit/scale, prior state, and conserving half-open ranges before only the selected child takes "<result>"
    And child atoms sum exactly to Y, exclusion constraints use leaf children only, and no later event can target the parent as active

    Examples:
      | object | result |
      | DebtRangeV1 | recovered, written_off, or outstanding through debt_reopen as authorized |
      | DebtRecoveryApplicationV1 | reversed |
      | EnforcementDispositionCoverageV1 | derecognized |

  Scenario: Partial forfeiture partitions unpaid atoms and records nonoverlapping coverage
    Given CompensationForfeitureDecisionV1 authorizes X atoms inside a larger withheld lot
    When the forfeit transaction commits
    Then the general partition creates conserving selected and remainder child ranges before only the selected child becomes forfeited
    And one EnforcementDispositionCoverage with kind unpaid_forfeiture binds the decision/evidence hashes, creating compensation-event stable ID without its complete hash, transaction-start source lot ID/hash/selected range, expected forensic result-child stable ID/range/atoms, X, currency/unit/scale, current state, revision/prior/result value-projection hash, and Core tuple
    And the canonical ordered coverage set has exactly one coverage per authorized decision range, every mapping is one-to-one and nonoverlapping within its exact scope, and its sum equals the decision's total authorized atoms
    And the remainder stays withheld under its prior hold and cannot inherit the selected child's forfeiture finding

  Scenario: Fraud disposition changes recovery state without changing the source-derived target
    Given a purpose-signed CompensationForfeitureDecisionV1 covers exact currently recognized source ranges without any payment revision
    When its compensation transaction commits
    Then T_N, T_C, T_A, the immutable candidate, and the entitlement aggregate remain unchanged
    And an unpaid selected range moves from active liability to current unpaid_forfeiture EnforcementDispositionCoverage through forfeit
    And every already-paid selected range moves from uncovered paid coverage to its one current paid_clawback EnforcementDispositionCoverage and creates its one exact enforcement-origin DebtRange while each forensic lot remains paid
    And a submitted selected range is excluded from that decision, stays rail-locked under the existing payout-eligibility incident, and receives a fresh exact unexpired unpaid-forfeiture or paid-clawback decision only after authenticated failure or success fixes its state
    And a later authoritative payment reduction consumes and derecognizes that exact current enforcement coverage rather than creating a duplicate cancellation or debt

  Scenario: Debt write-off is terminal and separately authorized
    Given operator debt remains outstanding
    When a purpose-authorized signed legal/accounting decision under the current published DebtWriteoffPolicyV1 and one exact current approved DebtWriteoffApprovalV1 writes off an exact amount
    Then the debt_writeoff TowerCompensationReceiptV1 event binds the decision, approval, policy, HistoricalAcceptedTermsAuthoritySetV1, amount, currency, operator, causal DebtRanges whose immutable lineage binds the paid lots and originating instructions, and ledger position
    And a partial amount first partitions an outstanding DebtRange into immutable conserving children
    And only the exact selected range becomes written_off and can never be payable, recovered, reopened, or written off again

  Scenario: A one-use debt-writeoff decision exhausts its exact signed range set
    Given DebtWriteoffDecisionV1 authorizes a canonical ordered set of N outstanding DebtRangeV1 ranges and exact total atoms where N is at least one
    When its one debt_writeoff transaction commits
    Then it creates exactly one written_off whole-range result or conserving selected child per authorized range in the same canonical order
    And every result maps one-to-one to its signed source range, result atoms equal source atoms, and the result-set sum equals the decision total exactly
    And an omitted, duplicated, reordered, extra, cross-debt, already-consumed, under-applied, over-applied, or nonconserving result rejects the entire decision transaction

  Scenario: Forfeiture and debt-writeoff decisions are revisioned one-use authorities
    Given a CompensationForfeitureDecisionV1 or DebtWriteoffDecisionV1 is purpose-signed for exact causal state
    When its state transition compares deterministic decision series/revision/prior hash and ID, TargetScopeDigestV1, authorized-range and HistoricalAcceptedTermsAuthoritySetV1 hashes, operator, Tower or debt scope, currency/unit/scale, exact atoms, current published CompensationEnforcementPolicyV1 plus current final CompensationEnforcementFindingV1 or current published DebtWriteoffPolicyV1 plus current approved one-use DebtWriteoffApprovalV1, effective/expiry tuple, independently assigned decision-ledger commit tuple, every purpose signer's current state, every historical terms signer's original-commit compromise status, unique nonoverlap claims, and current target state hashes
    Then one transaction may consume a CompensationForfeitureDecisionV1 revision once and append only its exact unpaid forfeit authority-consumer plus the required PayoutLot partition_lot helpers for proper subranges, or its exact paid-enforcement debt_create consumer with no PayoutLot helper
    And one transaction may consume a DebtWriteoffDecisionV1 revision once and append only its exact debt_writeoff consumer, whose DebtRangeChildRangeSetV1 partition is internal to that event and never uses partition_lot
    And exact replay returns the committed result while stale, skipped, conflicting, expired, cross-scope, or wrong-purpose authority changes nothing
    And neither a Tower/operator request nor the compensation-ledger signer alone can create the decision
    And both decision types derive historical key/compromise validity from their decision-ledger commit tuple, while a new destructive consumer requires the purpose signer active and nonrevoked in the greatest current trust publication
    And the consumer also requires the exact policy and finding-or-approval revisions still current, active, unexpired, byte-identical, and exclusively claimed where applicable; a newer/revoked input or overlapping live decision makes the prior decision inert without changing money
    And a finding spanning held and paid ranges requires two independently revisioned decisions, one for each closed disposition shape

  Scenario: A homogeneous enforcement decision is consumed by one exhaustive result set
    Given one CompensationForfeitureDecisionV1 authorizes an ordered homogeneous set of N exact payout-lot ranges where N is at least one
    When its one forfeit or paid-enforcement debt_create transaction commits
    Then it binds the decision's EnforcementAuthorizedRangeSetV1 and creates exactly N EnforcementDispositionCoverageV1 records in that order through EnforcementCoverageResultSetV1
    And each authorized range maps one-to-one to one coverage with equal atoms and the result-set sum equals the decision total
    And paid_clawback additionally creates exactly one nonoverlapping DebtRangeV1 per coverage with equal range/atoms through DebtCreateResultSetV1 whose sum equals the decision total, while unpaid_forfeiture creates no debt
    And an omitted, duplicated, reordered, split-without-conservation, extra, cross-lot, or under-applied result rejects the entire one-use decision rather than stranding unused authority

  # --- event-specific signed shapes ----------------------------------------

  Scenario Outline: TowerCompensationReceiptV1 event type has exact required and forbidden fields
    Given a compensation receipt has event type "<type>"
    Then "<shape>"

    Examples:
      | type | shape |
      | entitlement_delta | cumulative payment G/S/F/N/A, AuthoritativePaymentRevisionSetV1, prior committed EntitlementAggregateValueProjectionV1 complete hash, reconciliation transaction ID, signed delta, ApplicationDescriptorSetV1 count/complete hash, prior committed control-leaf hash, resulting control-value-projection hash, canonical empty JournalPostingSetV1, and resulting entitlement state are required; post-commit leaf hash, child event hashes, payout instruction, rail result, and ambient application fields are absent |
      | lot_create | exact positive ApplicationDescriptorSetV1 descriptor/source range, ApplicationRealizedValueProjectionSetV1, one immutable created lot ID/range/lineage, and compensation-expense-to-immature-liability postings are required; cross-lot ranges split into ordered descriptors/events and rail, debt, and negative fields are absent |
      | lot_cancel | exact negative ApplicationDescriptorSetV1 descriptor/source range, ApplicationRealizedValueProjectionSetV1, PayoutLotPartitionHelperSetV1 including canonical empty, whole or PayoutLotChildRangeSetV1 target result, dust-cycle continuation/terminal binding, and liability-to-reversal postings are required; rail-send and debt fields are absent |
      | maturity | exact lot IDs/ranges, MaturityPolicyV1, one-use MaturityAuthorityV1 and exhaustive MaturitySourceRevisionSetV1 with deterministic required/actual tuples, current payout-eligibility decision series/revision/hash/result/expiry, and dust-cycle ID/revision/prior/result hash, first-below-threshold tuple, payout-policy hash, minimum, carry interval, review deadline, DustLotReferenceSetV1 complete hash/member count/total atoms or their exact not-below-threshold absence are required; guessed time, payment recomputation, payout, forfeiture, and debt fields are absent |
      | withhold | exact lot IDs/ranges, closed external_materialization or compensation_created source shape, kind-conditional authority/head fields, prior/result HoldReferenceSetV1, current-event-owned prior/result scope relationship, resulting ClassificationAuthoritySetV1/state, standalone or AbortVoidHoldPlanSetV1 parent/member binding, and dust-cycle transition or canonical non-dust absence are required; foreign source, rail, and money-delta fields are absent |
      | release_hold | exact held lot IDs/ranges, prior HoldReferenceSetV1 and prior event-owned AuthorityRangeScopeSetV1, current resolved hold-series member, HoldResolutionAuthorityReferenceV1, release/result event-owned AuthorityRangeScopeSetV1 with byte-identical range lineage, re-encoded resulting HoldReferenceSetV1 and ClassificationAuthoritySetV1, exact destination state, and dust-cycle transition or canonical non-dust absence are required; generic resolution, scope reuse, rail, and money-delta fields are absent |
      | forfeit | EnforcementAuthorizedRangeSetV1, purpose-authorized decision, EnforcementCoverageResultSetV1 with unpaid_forfeiture kind, PayoutLotPartitionHelperSetV1 including canonical empty, exact one-to-one coverage, and dust-cycle continuation/terminal transition or canonical non-dust absence are required; rail and entitlement-delta fields are absent |
      | enforcement_coverage_derecognize | exact negative ApplicationDescriptorSetV1 descriptor/source range, ApplicationRealizedValueProjectionSetV1, immutable forensic lot, current EnforcementDispositionCoverage range/kind, prior/result coverage hashes, and kind-selected forfeited_entitlement-or-paid_clawback_enforcement_coverage to compensation_expense_reversal postings are required; payable, new/reversed debt, and rail fields are absent |
      | partition_lot | terminal parent lot, PayoutLotChildRangeSetV1, preserved lineage and dust-cycle binding, and exact payout, adjustment, or unpaid-forfeiture helper purpose are required; rail result and entitlement-delta fields are absent |
      | prepare_payout | PayoutLotAtomRangeSetV1, PayoutLotPartitionHelperSetV1 including canonical empty, payout/preparation IDs, TowerIDScopeSetV1 and TowerLifecyclePayoutAuthoritySetV1, current destination and payout-eligibility bindings, payout-policy version/hash, minimum, and authorization deadline, currency/unit/scale, quanta-per-rail-minor Q, selected atoms B, reserved atoms X, gross rail-minor units, remainder atoms R, and dust-cycle continuation/terminal transition or canonical no-cycle absence are required; tax decision, compensation-head, payout instruction, send fence, rail result, and entitlement-delta fields are absent |
      | abort_preparation | exact reserved_prepared lot IDs, preparation hash, reason, proof that both instruction and send fence are absent, canonical AbortVoidHoldPlanSetV1 complete hash including empty, final child index or canonical absence, and plan-bound group-final lot projections are required; instruction, rail result, and entitlement-delta fields are absent |
      | submit_payout | payout instruction hash, exact reserved_prepared lot IDs, and one-way send-fence authority are required; rail result and entitlement-delta fields are absent |
      | void_payout | payout instruction hash, exact reserved_prepared lot IDs, reason, proof that no send fence exists, canonical AbortVoidHoldPlanSetV1 complete hash including empty, final child index or canonical absence, and plan-bound group-final lot projections are required; rail result and entitlement-delta fields are absent |
      | submitted_negative_fence | exact negative ApplicationDescriptorSetV1 descriptor/source range, ApplicationRealizedValueProjectionSetV1, TowerPayoutInstructionV1/PayoutSendFenceV1 complete hashes, one transaction-start submitted lot ID/hash/range, one PendingSubmittedNegative state, and pending-recourse postings are required; cross-lot ranges split into ordered descriptors/events and rail result/lot-state mutation fields are absent |
      | payout_succeeded | payout instruction hash, exact reserved_submitted lot IDs, authenticated rail revision/result ID, PendingSubmittedNegativeSetV1 and PendingSuccessResolutionDescriptorSetV1 complete hashes or their exact no-pending absences are required; failure and entitlement-delta fields are absent |
      | payout_confirmed_failed | payout instruction hash, exact reserved_submitted lot IDs, authenticated rail revision/failure ID, current payout-eligibility revision/hash, optional PendingSubmittedNegativeSetV1, and mandatory PendingFailureResolutionDescriptorSetV1 whose per-result ClassificationAuthoritySetV1/HoldReferenceSetV1/dust bindings cover every returned lot even with no pending recourse are required; success and entitlement-delta fields are absent |
      | debt_create | exact origin plus its conditional negative-plan and ApplicationRealizedValueProjectionSetV1, resolved-pending, or enforcement-decision/EnforcementCoverageResultSetV1 authority, immutable paid lot/instruction/result range, DebtCreateResultSetV1, terms/policy, and resulting debt state are required; fields for the other origins and every external reversal field are canonically absent |
      | debt_offset | exact positive ApplicationDescriptorSetV1 descriptor/source range, ApplicationRealizedValueProjectionSetV1, one target DebtRange range or DebtRangeChildRangeSetV1, DebtRecoveryApplication ID/state, exact offset, and residual target-debt amount are required; any excess source range belongs to a following lot_create descriptor and rail fields are absent |
      | debt_reopen | exact negative ApplicationDescriptorSetV1 descriptor/source range, ApplicationRealizedValueProjectionSetV1, original DebtRecoveryApplication range or DebtRecoveryApplicationChildRangeSetV1, recovered DebtRange range or DebtRangeChildRangeSetV1, and prior/result recovery/debt hashes are required; payout and external-debit fields are absent |
      | debt_writeoff | DebtWriteoffAuthorizedRangeSetV1, DebtWriteoffResultSetV1, and purpose-authorized decision evidence are required; payout and entitlement fields are absent |
      | dust_reclassify | exact affected mature lot set, prior/current payout-policy hashes and applicability tuples, prior/current canonical payable-selection atoms B, accounting quanta per rail minor unit Q, checked share-atoms-per-minor K equal to 1000000 times Q, floor(B divided by K), minimum_payout_minor, below-threshold result, and full created/advanced/closed dust-cycle transition are required; money delta, rail, debt, and enforcement-disposition fields are absent |

  Scenario: Every event-shape field rejects every universal mutation
    Given every TowerCompensationReceiptV1 event type has all and only its required common and variant fields
    When each required field is independently replaced, removed, null, duplicated equally, duplicated conflictingly, or retyped while its original signature remains
    Then every event-type, field, and mutation tuple fails strict decoding or compensation-signature verification
    And inserting any field forbidden for that event type, any unknown field, or another role's signature is rejected

  Scenario: Every state transition has one signed append-only event
    Given any entitlement, payout-lot, reservation, rail, reversal, or debt transition commits
    Then its purpose-bound receipt and ledger mutation commit atomically with unique causal ID, sequence, previous hash, prior state hash, new state hash, Core authority tuple, and signer key ID
    And no transition mutates a prior receipt or relies on a Tower-controlled time
