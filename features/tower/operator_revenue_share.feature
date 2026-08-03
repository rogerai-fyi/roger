# PROPOSED SPEC — founder approval is required before step definitions or implementation.
#
# Scope: the compensated-Tower program — an opt-in tier in which a joined Tower operator
# earns a founder-set revenue share (initially 10%) of the platform's net revenue on token
# sales actually settled through that Tower. Covers eligibility, attribution, funds
# verification, accrual, payout, clawback, self-dealing, and forfeiture on enforcement.
#
# Money path: per the working agreement this domain is spec-first, exhaustive, and runs
# against real ledger dependencies (ephemeral Postgres), no mocks.

Feature: A compensated Tower earns a revenue share only on verified, attributed, funded work
  Tower admission alone earns nothing. A separately authorized compensated Tower accrues a
  share of net platform revenue for jobs whose settlement receipt binds that Tower, whose
  consumer funds are actually received and past the policy maturity window, and whose bound
  envelopes Roger Core observed on that Tower's authenticated session with consistent Tower
  corroboration. Roger Core is the sole accounting authority;
  no Tower statement can create, move, or inflate money.

  Background:
    Given the public network revenue-share policy names a share rate of 10 percent of net platform revenue
    And that rate is represented as the integer value 100000 parts per million
    And the exact TowerCompensationPolicyV1 is signed by its purpose-specific key and listed in the independently accepted trust document's CompensationPolicyDirectorySetV1
    And an operator owns an active joined Tower

  # --- program eligibility and authorization --------------------------------

  Scenario: Admission does not enroll a Tower in the revenue-share program
    Given a joined Tower completed enrollment and reached the active state
    When settlement runs for jobs relayed through it
    Then no operator share accrues
    And the settlement receipt records an ineligible compensation candidate with reason "not enrolled in revenue share"

  Scenario: A compensated Tower requires a verified payout identity before any accrual
    Given an operator applies for the compensated tier
    When their payout identity, account, terms acceptance, sanctions screening, program currency/rail jurisdiction, and current verified tax profile are complete and passing
    And Roger Core approves the application
    Then the Tower gains the compensated capability from an explicit effective time
    And accrual applies only to settlements whose grant was issued at or after that time
    And the grant-time compensation snapshot binds the operator, all six exact current fact heads defined in glossary.feature, compensated capability, eligibility registry, and compensation policy authorities

  Scenario: CompensatedTowerCapabilityV1 is an independent current Tower authority
    Given an active joined Tower and its operator have current passing payout_identity, operator_account, accepted_terms, sanctions_screening, and program-scoped jurisdiction_determination PayoutEligibilityFactV1 heads plus one current verified TaxProfileFactV1 head
    When the purpose-separated compensated-capability service enrolls, suspends, or revokes that Tower
    Then CompensatedTowerCapabilityV1 signs schema/network/protocol, capability-signer key ID, deterministic Tower-scoped series ID, revision/prior hash, Tower/operator IDs, exact Tower lifecycle revision/hash, program currency/unit/scale/rail scope, enabled/suspended/revoked state, the five exact eligibility-fact kind/series/revision/complete-hash/effective/expiry tuples, exact TaxProfileFactV1 series/revision/complete-hash/result/effective/expiry/commit tuples, current PayoutEligibilityPolicyV1 series/revision/hash and registry hash, current PayoutPolicyV1 series/revision/hash and tax-profile validity/age limits, current TowerCompensationPolicyV1 series/revision/hash and maximum capability-validity interval, effective/expiry Core tuples, evidence hash, issue tuple, and independently assigned capability-ledger Core commit tuple
    And the stable series ID is the fixed-length unpadded case-preserving base64url SHA-256 digest over UTF-8 strict JCS [CompensatedTowerCapabilityV1-series-v1,network-ID,Tower-ID]
    And revision 1 has prior absence, every successor is current revision plus one with its immediate prior hash under one CAS, and no parallel series or operator/Tower remapping is valid
    And enabled requires all five eligibility facts passing/current/fresh, the tax fact verified/current/fresh, TaxProfileFactV1 payout-identity version byte-identical to the payout_identity fact payload, TaxProfileFactV1 tax jurisdiction byte-identical to the jurisdiction_determination fact payload and program jurisdiction, jurisdiction and compensation currency/rail scopes equal, all three policies current/published, the lifecycle active, and expiry no later than every fact/policy/key expiry or capability commit plus the policy maximum interval; any drift or unavailable authority fails closed
    And capability commit compare-and-swaps those exact identity/jurisdiction payload relationships under the same lock as all six fact heads, so a concurrent identity or jurisdiction revision either wholly wins first and blocks issuance or wholly follows a committed capability

  Scenario: The grant-time compensation snapshot has one exact immutable authority
    Given Roger Core is committing a joined ExecutionGrantV1
    When the grant-issue transaction evaluates compensated eligibility
    Then GrantCompensationSnapshotV1 binds schema/network/protocol, job/attempt/Tower/operator IDs, Tower lifecycle revision/hash, program currency/unit/scale/rail scope, exact CompensatedTowerCapabilityV1 type/series/revision/complete-hash/effective/expiry, exact payout_identity/operator_account/accepted_terms/sanctions_screening/jurisdiction_determination PayoutEligibilityFactV1 kind/series/revision/complete-hash/effective/expiry tuples, exact TaxProfileFactV1 series/revision/complete-hash/result/effective/expiry/commit tuples, PayoutEligibilityPolicyV1 series/revision/hash and registry hash, PayoutPolicyV1 series/revision/hash and tax-profile limits, TowerCompensationPolicyV1 series/revision/complete hash/rate_ppm/effective/expiry tuples, FundingAllocationPolicyV1 series/revision/complete hash/allocation rule/effective/expiry tuples, FundingSourceReservationV1 and FundingSourceReservationSetV1 complete hashes, and the grant Core authority tuple
    And the serializable grant-issue transaction revalidates and compare-and-swaps the current Tower lifecycle, capability head, all six fact heads, the byte-identical TaxProfileFactV1-to-payout_identity version and TaxProfileFactV1-to-jurisdiction payload relationships, all four policy/publication heads and registry, every reserved funding-source head, and every source signer's active nonrevoked state in the greatest accepted current trust publication
    And every public ExecutionGrantV1 binds the FundingAllocationPolicyV1 and reservation/set hashes, while a compensated grant also binds the snapshot complete hash plus its byte-identical TowerCompensationPolicyV1 series/revision/complete hash
    And its authoritative attempt row stores the same complete hash
    And SettlementReceiptV2 must relationship-check and bind that exact hash before creating an eligible candidate
    And a direct or uncompensated grant encodes the snapshot as canonical absence rather than an empty or zero-valued object

  Scenario Outline: An incomplete compensated-tier application accrues nothing
    Given an operator applies for the compensated tier with "<condition>"
    When Roger Core evaluates the application
    Then the compensated capability is not granted
    And settlements through the Tower record an ineligible candidate with reason "not enrolled in revenue share"
    And no positive Tower compensation event can be appended

    Examples:
      | condition                                  |
      | no payout identity                         |
      | a payout identity that failed verification |
      | unaccepted revenue-share terms             |
      | a suspended operator account               |
      | a Tower not in the active state            |
      | a sanctioned or unsupported payout region  |
      | a payout identity bound to another account |
      | outstanding same-currency operator debt    |
      | an open fraud or forfeiture finding        |

  # --- debt follows the operator, not the Tower -----------------------------

  Scenario: Outstanding debt follows the operator across every Tower they own
    Given an operator carries an outstanding DebtRangeV1 balance in one currency
    When that operator applies for the compensated tier on any Tower, including a newly enrolled Tower ID under fresh keys
    Then the compensated capability is not granted for that currency while the debt is outstanding
    And enrolling a new Tower, rotating keys, or re-verifying the payout identity does not clear, reduce, or orphan the debt
    And the debt remains attached to the operator identity rather than to the Tower ID that incurred it

  Scenario: Debt is an operator-scoped fact head input, not a per-Tower condition
    Given an operator owns several compensated Towers in one currency
    When debt is created against any one of them
    Then the operator_account fact head reflects the outstanding debt for that operator and currency
    And every compensated capability that operator holds in that currency is suspended at its next currentness fence
    And accrual already frozen in a grant-time snapshot issued before the debt is honored, since the snapshot is immutable

  Scenario: Debt is cleared only by recovery, offset, or a purpose-signed writeoff
    Given an operator has outstanding debt and later accrues new positive same-currency compensation
    When the canonical debt-priority order applies
    Then the new entitlement offsets the outstanding debt before creating any active liability
    And the compensated capability may be reinstated only once the outstanding balance reaches zero
    And no elapsed time, account closure, Tower revocation, or re-enrollment alone reduces the balance

  # --- rolling reserve and exposure cap -------------------------------------

  Scenario: A rolling reserve holds back a policy share of every accrual until maturity closes
    Given signed TowerCompensationPolicyV1 declares a reserve_ppm between 0 and 1000000 inclusive
    When a compensation event accrues entitlement E atoms for an operator
    Then floor(E times reserve_ppm divided by 1000000) atoms are recorded in reserve_held state rather than becoming mature_payable
    And the reserve remainder is retained exactly, so reserved plus payable equals E with no atom created or lost
    And reserve_held atoms are released to mature_payable only when the signed reversal-maturity window for their source lot has fully closed
    And reserve_held atoms are consumed ahead of mature_payable atoms when a later reversal creates a negative adjustment

  Scenario: Reserve release is idempotent and cannot be accelerated by the operator
    Given reserve_held atoms whose maturity window has closed
    When release runs, is replayed, or is requested by the operator
    Then exactly one release event moves each atom range once
    And no Tower, operator, or client statement can advance a maturity tuple or trigger an early release

  Scenario Outline: Unmatured exposure above the per-operator cap is withheld rather than accrued
    Given signed policy declares a positive per-operator unmatured exposure cap of "<cap>" atoms
    And that operator's current unmatured plus reserve_held exposure is "<current>" atoms
    When a new compensation event would accrue "<accrual>" atoms
    Then the accrued result is "<result>"

    Examples:
      | cap  | current | accrual | result                                                              |
      | 1000 | 0       | 100     | accrues in full                                                     |
      | 1000 | 900     | 100     | accrues in full and reaches the cap exactly                         |
      | 1000 | 900     | 200     | accrues 100 and records 100 as exposure_withheld with a closed reason |
      | 1000 | 1000    | 50      | accrues zero and records 50 as exposure_withheld                    |
      | 1000 | 1200    | 50      | accrues zero; an over-cap balance from a policy change never inverts |

  Scenario: Exposure-withheld amounts are deferred, not forfeited
    Given atoms were recorded as exposure_withheld because the operator was at the cap
    When earlier exposure matures and the operator falls below the cap
    Then the withheld atoms become eligible for accrual under their original grant-time snapshot and policy version
    And they are never revalued at a later rate or silently dropped

  Scenario: The share rate is policy, set centrally, and versioned
    Given the founder changes the revenue-share rate
    When Roger Core signs and publishes the new policy version
    Then settlements bind the policy version in force at grant issue time
    And no Tower, Station, or client statement can select a different rate

  Scenario Outline: Compensation rate wire validation covers the complete canonical boundary
    Given proposed signed policy bytes contain <member>
    When trust-policy validation runs
    Then the result is "<result>"
    And a rejected fixture creates no trusted policy version, grant snapshot, candidate, or compensation event

    Examples:
      | member | result |
      | {"rate_ppm":"0"} | valid zero-rate policy after founder authorization |
      | {"rate_ppm":"1"} | valid lower positive boundary |
      | {"rate_ppm":"100000"} | valid initial ten-percent policy |
      | {"rate_ppm":"1000000"} | valid upper boundary of one hundred percent |
      | {"rate_ppm":"-1"} | rejected |
      | {"rate_ppm":"1000001"} | rejected |
      | {"rate_ppm":"1.0"} | rejected |
      | {"rate_ppm":"01"} | rejected |
      | {"rate_ppm":"+1"} | rejected |
      | {"rate_ppm":"1e6"} | rejected |
      | {"rate_ppm":100000} | rejected |
      | {"rate_ppm":null} | rejected |
      | {} | rejected |
      | {"rate_ppm":"9223372036854775808"} | rejected before bounded conversion |
      | {"rate_ppm":"18446744073709551616"} | rejected before bounded conversion |

  # --- attribution: Core observed the Tower-authenticated session ------------

  Scenario: A Tower share accrues only when the settlement receipt binds that Tower
    Given a compensated Tower relayed a paid job to completion
    When Roger Core settles the job
    Then SettlementReceiptV2 binds the Tower ID and certificate serial from DispatchLeaseV1
    And it hashes CoreTransitObservationV1 from that authenticated session
    And it hashes the consistent TowerTransitStatementV1 as corroboration
    And the receipt creates only an immutable eligible compensation candidate
    And final share is computed from that receipt plus authoritative post-settlement payment and enforcement events

  Scenario: Transit evidence attributes a key but not a physical host
    Given the lease, Core observation, and Tower statement are exact and consistent
    When compensation eligibility is described
    Then RogerAI states that the bound envelopes crossed a Core session authenticated to the Tower key
    And does not claim proof of physical path, geography, unmodified runtime, non-collusion, or truthful Tower byte counts

  Scenario Outline: Missing, invalid, or late Tower corroboration cannot block settlement or earn share
    Given a joined job has timely valid required Station evidence and CoreTransitObservationV1 but "<evidence gap>"
    When share accrual runs
    Then the consumer charge and Station earning settle normally under existing rules
    And the receipt records an ineligible candidate with the deterministic evidence-gap reason
    And no positive Tower compensation event can be appended for that candidate
    And the gap is recorded as attribution evidence against the Tower

    Examples:
      | evidence gap                                                      |
      | no TowerTransitStatementV1 for the attempt                        |
      | a transit statement whose lease hash mismatches the issued lease  |
      | a transit statement signed by a different Tower key               |
      | transit byte counts of zero for a non-empty settled result        |
      | a transit statement for a different attempt ID                    |
      | a transit statement replayed from a previous attempt              |
      | the statement arrives only after missing-corroboration settlement committed |

  Scenario Outline: Required joined evidence failure prevents settlement and candidacy
    Given a joined attempt has "<required evidence failure>"
    When settlement validation runs
    Then there is no successful Station/client settlement and no compensation candidate
    And the consumer hold follows the authoritative attempt failure path exactly once

    Examples:
      | required evidence failure |
      | no CoreTransitObservationV1 for the attempt |
      | a Core observation from another Tower session |
      | a Core observation whose lease or envelope digest mismatches |
      | a complete result or ProviderAssertionV2 observed at or after the deadline |
      | required joined evidence completed at or after a lifecycle cutoff |

  Scenario: Late Tower corroboration cannot upgrade an immutable candidate
    Given a valid joined settlement committed with Tower-statement status missing and terminal ineligible reason missing-corroboration
    When a valid matching TowerTransitStatementV1 arrives later
    Then the statement is retained as audit evidence only
    And SettlementReceiptV2, its candidate, consumer charge, Station earning, and every compensation balance remain unchanged

  Scenario: A Tower cannot claim jobs that Roger Core routed directly to a Station
    Given a job was dispatched to a Station attached directly to Roger Core
    When a compensated Tower submits a transit statement naming that job
    Then the statement is rejected because no lease named that Tower
    And no share accrues
    And the false claim is recorded as security evidence

  Scenario: Two Towers cannot both earn the share for one attempt
    Given transit statements from two different Towers reference one attempt
    When settlement runs
    Then only the Tower named in the issued lease and matching Core session can become a candidate
    And the foreign statement is recorded as security evidence against its signer

  Scenario: A retried job accrues to the Tower of the settled attempt only
    Given attempt 1 through Tower A failed without charge and attempt 2 through Tower B settled
    When share accrual runs
    Then only Tower B has a candidate eligible for later compensation on attempt 2
    And Tower A has no candidate or compensation event for attempt 1

  # --- funds verification: we actually received the money -------------------

  Scenario: An uncaptured consumer charge cannot create positive compensation
    Given a settled job whose consumer hold has not yet been captured as a real charge
    When share accrual runs
    Then the immutable candidate remains eligible while its separate entitlement aggregate is pending_reconciliation
    And no positive TowerCompensationReceiptV1 delta or payable balance exists

  Scenario: Settlement and compensation are separate immutable records
    Given a joined job has an immutable SettlementReceiptV2
    When capture, fee, refund, dispute, enforcement, maturity, or payout state changes
    Then Roger Core appends a signed TowerCompensationReceiptV1 referencing that settlement
    And never mutates or replaces SettlementReceiptV2
    And the latest compensation state is the fold of the append-only causal events

  Scenario Outline: Wholly unfunded or fully reversed revenue has zero cumulative entitlement
    Given a settled job whose entire allocated external-cash source ends as "<payment outcome>"
    When the payment event is recorded
    Then cumulative mature cash and cumulative Tower entitlement for that source are zero
    And any prior recognized amount is exactly negated by one causal compensation delta
    And its unpaid portion is cancelled while its already-paid portion becomes an equal clawback debit in the same currency

    Examples:
      | payment outcome        |
      | authorization expired  |
      | capture failed         |
      | refunded               |
      | charged back           |
      | disputed and lost      |

  Scenario: A dispute won restores only the actually recovered cash
    Given a captured charge became disputed and its candidate was withheld or clawed back
    When the authoritative payment record says the dispute was won and cash was restored
    Then compensation is recomputed from the restored cash and actual fee outcome
    And its maturity window starts from the final recovery event required by policy
    And no prior cancelled or clawed-back amount is duplicated

  Scenario: Duplicate and out-of-order payment events converge on one authoritative state
    Given capture, fee, refund, dispute, and resolution events arrive duplicated and out of order
    When Roger Core deduplicates provider event IDs and reconciles the versioned payment object
    Then compensation is derived once from the authoritative cumulative payment state
    And stale events cannot roll back a newer state or create a second accrual

  Scenario: A payment event arriving before its job receipt waits without guessing
    Given authoritative capture or fee state arrives before the referenced SettlementReceiptV2
    When compensation reconciliation runs
    Then no compensation money event is appended
    And the payment revision remains durably pending until the exact immutable candidate exists

  Scenario: Reusing one payment event ID with different bytes fails closed
    Given Roger Core processed a payment event ID with canonical hash H
    When the same event ID arrives with a different canonical hash
    Then compensation reconciliation stops for that payment source
    And no newer or older candidate balance is guessed
    And the conflict is recorded for payment reconciliation

  Scenario: Grant-funded and promotional usage carries no operator share
    Given a job paid entirely from platform-issued grant or promotional credit
    When settlement runs
    Then platform net revenue for the job is zero
    And the candidate is ineligible for positive compensation with reason "no external-cash funding"

  Scenario: Mixed cash and grant funding shares only cash-attributable net revenue
    Given one settled job is funded partly by captured cash and partly by platform grant credit
    When compensation is computed
    Then SettlementReceiptV2 contains immutable cash and grant funding slices whose consumer amounts sum exactly to cost
    And Station earning is assigned across those slices by deterministic source intervals and sums exactly to total Station earning
    And only mature non-reversed external-cash slices enter compensation
    And grant slices create no Tower share

  Scenario: Funding sources have one canonical consumption order
    Given a consumer has expiring grant, non-expiring grant, and external-cash lots
    When Roger Core allocates one settled job under the v1 funding policy
    Then it consumes grant lots first by earliest expiry, then Core credit sequence, then source-lot ID
    And it consumes non-expiring grant lots next by Core credit sequence then source-lot ID
    And it consumes external-cash lots last by Core credit sequence then source-lot ID
    And the exact FundingAllocationPolicyV1 series/revision/complete hash bound into every public ExecutionGrantV1 and its FundingSourceReservationV1 fixes that closed allocation order before the grant, and a compensated snapshot copies the same reference
    And no client, Tower, Station, worker scheduling, map iteration, or event arrival order can choose another source

  Scenario: Funding slices form complete non-overlapping source and job partitions
    Given a job of consumer cost C draws from ordered source lots
    When SettlementReceiptV2 records its funding allocation
    Then each slice binds a half-open source-lot interval and a half-open cumulative job-cost interval of equal length
    And source intervals never overlap prior committed consumption from that lot
    And job intervals are adjacent from zero through C with no gap, overlap, reorder, or residual
    And a unique database constraint and transaction fence enforce every source interval

  Scenario: Station earning is allocated to funding slices by prefix difference
    Given consumer cost C and Station earning S are positive checked integer accounting quanta
    And one funding slice covers cumulative job-cost interval [a,b)
    When Station-cost allocation runs
    Then that slice receives floor(S times b divided by C) minus floor(S times a divided by C)
    And all slice allocations sum exactly to S regardless of split count or worker order
    And C equal to zero permits only S equal to zero and an empty allocation

  Scenario: Several cash sources remain independently traceable
    Given one job is funded by grant plus several external cash source lots
    When one source captures, reverses, or changes fee state
    Then only slices referencing that source are recomputed
    And no cash, Station-cost, fee, or share amount moves between unrelated source lots

  # A processor fee is either ad valorem (proportional to principal) or flat (a fixed
  # per-event charge such as a dispute fee). Prorating a flat fee across principal is wrong:
  # it spreads a charge caused by one event across unrelated settlements of the same lot.

  Scenario Outline: A processor fee is allocated by its declared fee kind
    Given an authoritative processor fee of kind "<kind>" arises on a source lot
    When fee allocation runs
    Then the fee is allocated by "<rule>"

    Examples:
      | kind       | rule                                                                     |
      | ad_valorem | proportional prefix allocation across the lot's source intervals         |
      | flat       | whole assignment to the exact source interval of its causing event only  |

  Scenario: A flat dispute fee is charged to its causing settlement, not spread across the lot
    Given a captured source lot funded several settlements and one of them is disputed
    And the authoritative dispute fee is a flat amount unrelated to principal
    When fee allocation runs
    Then the whole flat fee is allocated to the source interval of the disputed settlement
    And no unrelated settlement's entitlement on that lot is reduced by the flat fee
    And a flat fee exceeding its own settlement's net revenue drives that settlement's base to zero and the excess is recorded as platform expense rather than silently discarded or spread
    And the excess never creates operator debt unless a purpose-signed negative entitlement application does so explicitly

  Scenario: A fee arriving without a declared kind fails closed
    Given an authoritative fee revision omits its fee kind or declares a kind outside the closed set
    When fee allocation runs
    Then no allocation is recomputed and one bounded fee incident is opened
    And the affected entitlements remain at their prior authoritative values

  Scenario: Ad valorem processor fees are allocated without creating or losing money
    Given one captured source lot has principal C, consumed-prefix length U, and one authoritative ad valorem processor fee F in the same accounting quanta
    And its settlement slices form adjacent nonoverlapping source intervals from zero through U
    When fee allocation runs
    Then each immutable source interval [a,b) receives floor(F times b divided by C) minus floor(F times a divided by C)
    And the consumed-fee target F_U is floor(F times U divided by C)
    And settlement-slice fee allocations sum exactly to F_U
    And F minus F_U remains attributed to the unspent or fenced remainder of that source lot and cannot reduce any settlement candidate
    And a newer authoritative F or U appends only the difference from the prior F_U target to affected slice entitlements
    And C equal to zero requires F, U, and every slice allocation to be zero and performs no division

  Scenario: Cumulative refunds and reversals consume unspent principal before attributed work
    Given an external-cash lot of principal C is fenced from new spending at consumed-prefix length U when its first refund, dispute, or reversal opens
    And the authoritative cumulative reversed principal is R
    And one settlement slice uses source interval [a,b) within [0,U)
    When compensation reconciliation allocates the reversal
    Then reversed consumed principal Q is max(0, min(U, R minus (C minus U)))
    And the slice reversal is floor(Q times b divided by U) minus floor(Q times a divided by U)
    And slice reversals sum exactly to Q while the remaining reversal applies only to the fenced unspent balance
    And a later larger or smaller authoritative cumulative R appends only the target delta
    And child-event order, event splitting, and settlement order cannot choose which Tower loses entitlement
    And U equal to zero produces no settlement reversal and performs no division

  Scenario Outline: Unknown or ambiguous source economics cannot accrue
    Given an otherwise eligible candidate has "<condition>"
    When compensation reconciliation runs
    Then it remains pending-reconciliation and no positive delta is appended

    Examples:
      | condition |
      | capture not authoritative |
      | processor fee not final |
      | an open dispute or reversal |
      | a missing funding-slice lineage |
      | a currency or scale mismatch |
      | source allocations that do not conserve principal or Station cost |

  Scenario: Fee finality has one finite Core-anchored ceiling
    Given an authoritative captured source still reports processor fee not final
    And the signed adapter/source-kind policy fixes a positive maximum_fee_finality_interval
    When Roger Core commits the capture revision
    Then it binds fee_finality_deadline to the capture Core authority tuple plus that interval with the global authority sequence as an equality tie-breaker
    And the affected entitlement aggregate stays pending_reconciliation with no guessed positive delta
    When the deadline is reached without a final fee revision
    Then exactly one signed FeeFinalityIncidentV1 opens and remains durably linked to the source and every affected aggregate
    And no zero, estimate, prior fee, administrator assertion, or Tower/operator claim substitutes for final provider authority

  Scenario: Authoritative fee recovery closes one incident and reconciles once
    Given an open fee-finality incident and held affected compensation state
    When a later authenticated monotonic provider revision makes the fee final
    Then one transaction closes the exact incident, reconciles cumulative G/S/F/N/A once, and releases only holds whose other predicates pass
    And duplicate recovery is idempotent while an equal revision with different bytes stays conflict-quarantined

  Scenario Outline: Partial cash changes recompute cumulative entitlement
    Given a compensation candidate later receives "<event>"
    When authoritative cumulative cash and fee state is reconciled
    Then net platform revenue and cumulative Tower entitlement are recomputed
    And only the delta from previously appended compensation is accrued or clawed back

    Examples:
      | event                         |
      | a partial refund              |
      | a full refund                 |
      | a partial chargeback          |
      | a full chargeback             |
      | a refunded processor fee      |
      | an added dispute fee          |
      | a won dispute cash recovery   |

  Scenario: The share is computed on net platform revenue, not gross consumer price
    Given a settled funded job with a consumer charge, a Station earning, and payment-processor fees
    When share accrual runs
    Then the share base is the consumer charge minus the Station earning and recorded processor fees
    And a non-positive base accrues zero
    And the exact base, rate, policy version, share amount, and causal payment version are bound into TowerCompensationReceiptV1

  Scenario: Compensation math uses fixed-point cumulative rounding
    Given all money is represented as checked integer accounting quanta and the share rate as integer parts per million
    When cumulative entitlement is computed
    Then one share atom is exactly one millionth of one accounting quantum
    And exact share atoms equal checked_multiply(max(0, mature cash G minus allocated Station cost S minus allocated fee F), rate parts per million)
    And 1000000 parts per million therefore produces exactly one accounting quantum of monetary value per net accounting quantum
    And exact share atoms are retained through aggregation without per-event rounding
    And any multiplication overflow fails reconciliation before a delta or lot is created
    And event splitting, duplication, or order cannot increase cumulative entitlement

  Scenario: A ledger-wide control invariant caps compensation across all operators
    Given current entitlement aggregates in one currency span one or many operators, Towers, settlements, and grant-bound policy rates
    When any compensation event commits
    Then source-derived program net T_N equals the checked sum of each immutable eligible candidate's current nonnegative externally funded settlement N expressed in share atoms
    And policy ceiling T_C equals the checked sum of each such current N times its grant-bound rate_ppm
    And current entitlement target T_A equals the checked sum of all current aggregate A values and exactly equals T_C
    And T_A never exceeds T_N
    And balanced per-currency JournalPostingSetV1 plus CompensationControlTotalSetV1 complete hash are updated in the same global-ledger transaction
    And a duplicate or overlapping source interval, omitted event, unbalanced posting, aggregate mismatch, or overflow blocks all affected positive money movement

  Scenario: The program cap preserves the per-settlement net-revenue ruling
    Given one settlement has negative raw platform margin and another has positive raw platform margin
    When their nonnegative settlement N values and compensation are computed
    Then the negative-margin settlement contributes zero N and zero entitlement
    And it does not claw compensation from an unrelated positive settlement or operator
    And the independent program cap is the sum of nonnegative settlement N values, not max of the cross-operator raw-margin sum
    And changing to aggregate-loss sharing would require a new founder-approved effective-dated compensation policy and allocation state machine

  Scenario: PayoutPolicyV1 fixes conversion, threshold, and dust review centrally
    Given Roger Core purpose-signs PayoutPolicyV1 and lists its exact hash under payout_policy_signer in an independently accepted CompensationPolicyDirectorySetV1
    When PayoutPolicyV1 is validated
    Then it binds schema/network/protocol, series/revision/prior hash, currency/unit/scale, payout rail, positive accounting_quanta_per_rail_minor Q, positive minimum_payout_minor M, positive finite maximum_preparation_authorization_interval, positive finite maximum_dust_carry_interval, positive finite maximum_tax_profile_fact_validity_interval and maximum_tax_profile_age_at_use, fixed same_currency_future_compensation_offset_only debt-recovery mode, fixed external_debit_forbidden true, applicability Core tuple, issue/expiry, and signer key ID
    And Q, M, all four intervals, and both debt-recovery fields are bounded canonical values selected before observing a payout candidate
    And an absent, zero, negative, noncanonical, noninteger, overflowing, unbounded, expired, cross-currency, or cross-rail field authorizes no preparation or send
    And no operator, Tower, Station, client, payout callback, or payout-authorization signer can select or alter those values

  Scenario: Payout conversion has one exact scale, quotient, and remainder
    Given signed currency/rail policy declares positive integer Q accounting quanta per rail minor unit
    And canonical payout selection has B available share atoms in one currency/unit/scale
    When payout preparation computes K as checked_multiply(1000000, Q)
    Then gross rail-minor units P equal floor(B divided by K)
    And gross reserved share atoms X equal checked_multiply(P, K)
    And retained share-atom remainder R equals B modulo K with zero less than or equal to R and R less than K
    And B equals X plus R exactly
    And only X is moved into reserved_prepared atom ranges while R remains mature_payable
    And a boundary payout lot is atomically split into immutable conserving children before either child changes state
    And zero, noninteger, currency-mismatched, or overflowing Q or K creates no reservation or instruction

  Scenario Outline: Payout threshold boundaries have exact no-send behavior
    Given signed PayoutPolicyV1 declares minimum_payout_minor M as "10" and canonical selection computes P as "<P>"
    When payout preparation runs
    Then the outcome is "<outcome>"

    Examples:
      | P | outcome |
      | 0 | no partition, reservation, instruction, or rail call; all atoms remain mature_payable |
      | 9 | no partition, reservation, instruction, or rail call; all atoms remain mature_payable |
      | 10 | exact whole-minor atoms may enter preparation after every other check |
      | 11 | exact whole-minor atoms may enter preparation after every other check |

  Scenario Outline: Remainder boundaries conserve the exact liability
    Given payout conversion uses the minimum valid Q equal to "1" and therefore K equal to "1000000" share atoms and produces retained remainder R equal to "<R>"
    When preparation validation runs
    Then the result is "<result>"

    Examples:
      | R | result |
      | 0 | no remainder lot is required and all selected atoms are exactly reserved |
      | 1 | one share atom remains in same-currency mature_payable lineage |
      | 999999 | the maximal valid remainder remains in same-currency mature_payable lineage |
      | 1000000 | rejected because a canonical remainder must be less than K |

  Scenario: Payout policy changes race preparation and send by authoritative cutoff
    Given reserved_prepared payout P binds PayoutPolicyV1 series D at revision R and complete hash H
    When a newer threshold or dust-policy revision races its send fence
    Then the send fence may commit only by compare-and-swapping D, R, H and the revision's applicability tuple
    And a stricter revision applicable at or before the send fence signed-aborts or signed-voids P before any rail call
    And a policy applicable after an already committed send fence is prospective only while P reconciles under H

  Scenario Outline: Invalid money input creates no compensation entry
    Given compensation math receives "<input>"
    When validation runs
    Then no accrual or payout is committed and the separate entitlement aggregate enters pending_reconciliation
    And the immutable settlement candidate is unchanged

    Examples:
      | input                                |
      | floating-point money                 |
      | NaN or infinity                      |
      | integer overflow                     |
      | currency mismatch                    |
      | negative captured cash without causal reversal |
      | processor fees exceeding signed numeric bounds |

  Scenario: A payout requires cleared funds past the reversal window and a bound threshold
    Given an operator's accrued share includes amounts inside the payment reversal window
    When a payout run executes
    Then only amounts past the reversal window whose computed whole-minor P is at least the signed positive minimum_payout_minor are prepared
    And the payout ledger entry binds every contributing compensation event, funding slice, settlement hash, currency, and exact amount

  Scenario: Dust remains visible and claimable instead of expiring silently
    Given same-operator same-currency mature share atoms remain below the payout threshold or below one rail minor unit
    When later compensation, tier exit, Tower revocation, sanctions, or account closure occurs
    Then later atoms aggregate with the existing source-bound liability under normal policy
    And exit, revocation, sanctions, or closure moves it at most to a signed withheld review state and never cancels, rounds away, redirects, or converts it to platform revenue
    And a finite signed dust-review interval raises one incident and operator notice when exceeded
    And v1 has no age-based, closure-based, administrator-selected, de-minimis, donation, escheat, or writeoff disposition for untainted dust
    And only an exact existing CompensationForfeitureDecisionV1 may forfeit tainted dust under the ordinary fraud contract; every other terminal disposition requires a separately founder-approved legal/accounting contract

  Scenario Outline: Post-preparation tax authority fails closed instead of inventing remittance
    Given an existing reserved_prepared payout has current authoritative tax decision result "<requirement>"
    When instruction authorization runs
    Then the payout result is "<result>"

    Examples:
      | requirement | result |
      | exactly zero under the bound current tax-decision version | eligible for a subsequent signed head and every other instruction check; any instruction encodes withholding rail-minor units as canonical zero |
      | positive | use one atomic abort-then-external-materialization-withhold group bound to the exact positive tax decision, and create no instruction or rail call |
      | unknown | use one atomic abort-then-external-materialization-withhold group bound to the exact unknown tax decision, and create no instruction or rail call |

  Scenario: A nonzero withholding requirement arising before send aborts or voids preparation
    Given a zero-withholding payout is reserved_prepared but has no durable send fence
    When a newer authoritative tax decision requires positive or unknown withholding
    Then one atomic group first signed-aborts the preparation if no instruction exists or signed-voids its instruction if one exists, then an ordered external-materialization withhold child binds the exact current tax decision for every selected range
    And no rail call, tax liability, remittance claim, paid transition, or new destination is created

  Scenario: The payout send fence and tax-decision correction have one CAS winner
    Given reserved_prepared payout P binds current zero decision series D at revision R and complete hash H
    When its send worker and a next tax-decision revision race
    Then the send fence may commit only by compare-and-swapping D, R, H, unexpired zero result, the exact current TaxProfileFactV1 head/freshness, the selected current PayoutPolicyV1/publication head, byte-identical operator/identity/jurisdiction/destination/currency/rail fields, the tax-decision/tax-profile/payout-policy keys' current active nonrevoked states, and the prepared instruction state
    And if the correction wins first, P uses the exact atomic abort-or-void then external-materialization-withhold group and makes no rail call
    And if the send fence wins first, P becomes reserved_submitted under H and any applicable correction follows the post-send incident state machine
    And no worker can both void and submit P or send under a stale decision head

  Scenario: A payout rail receives one purpose-signed immutable instruction
    Given a payout transaction has atomically moved exact mature compensation lots to reserved_prepared
    When Roger Core authorizes the external transfer
    Then TowerPayoutInstructionV1 binds the payout and operator IDs, PayoutPreparationV1 ID/complete hash, TowerIDScopeSetV1 and TowerLifecyclePayoutAuthoritySetV1 complete hashes, verified payout-identity version, destination fingerprint, payout-eligibility decision series/revision/complete hash, accepted_terms PayoutEligibilityFactV1 series/revision/complete hash copied from that decision, zero-withholding tax-decision series/revision/complete hash and its TaxProfileFactV1 series/revision/complete hash, payout-policy series/revision/complete hash and minimum_payout_minor/debt-recovery fields, preparation-authorization deadline, rail, currency/unit/scale, accounting quanta per rail minor unit, selected available and gross reserved share atoms, gross/net rail-minor units, canonical zero withholding, retained share-atom remainder, CompensationEventIDSetV1, FundingSliceIDSetV1, PayoutLotAtomRangeSetV1, and SettlementReceiptHashSetV1 complete hashes, CompensationLedgerHeadV1 complete hash, covered payout-authority leaf complete hash, CompensationControlTotalSetV1 complete hash, issue/expiry times, independently assigned payout-authorization-ledger Core commit time/global sequence, and stable rail idempotency key
    And the payout-authorization key is distinct from compensation, settlement, admin, and Tower keys
    And one transaction compare-and-swaps every bound current relationship and commits the immutable instruction at that independent tuple before a send fence can reference it
    And the rail request is derived only from that verified instruction

  Scenario: Payout instruction arithmetic and lot relationships conserve exactly
    Given TowerPayoutInstructionV1 is ready for verification before a rail call
    When its relationships are checked
    Then rate_ppm for every contributing compensation event is between 0 and 1000000 inclusive
    And every contributing payout lot is reserved_prepared, uniquely bound to this payout ID, in one currency/unit/scale, and absent from every other active or paid instruction
    And reserved lot atom ranges sum exactly to signed gross reserved share atoms X
    And selected available share atoms B equal X plus retained share-atom remainder R
    And X equals gross rail-minor units times 1000000 times signed accounting quanta per rail minor unit
    And withholding rail-minor units equal canonical zero under the bound authoritative tax decision
    And the instruction binds a signed nonnegative rail_fee_minor and a closed rail fee-bearer of platform or operator
    And gross rail-minor units equal net transfer rail-minor units plus operator-borne rail_fee_minor exactly
    And a platform-borne rail fee leaves net transfer equal to gross and is posted to platform expense, never to the operator ledger
    And R is nonnegative, below one rail minor unit in share atoms, and remains mature_payable in the operator ledger
    And an unexpired accepted CompensationLedgerHeadV1 covers the preparation, exact zero tax-decision authority leaf, every selected event and lot state at preparation, funding slice, debt offset, JournalPostingSetV1, and CompensationControlTotalSetV1 complete hash, and the instruction's copied set hash is byte-identical to that head field
    And the bound head is an ancestor of the current SQL head while unrelated later ledger events are allowed
    And the current affected-currency control-total state remains healthy and no later replay/conflict incident is open
    And operator, TowerIDScopeSetV1, every exact current TowerLifecycleEventV1 revision/hash/disposition in TowerLifecyclePayoutAuthoritySetV1, payout-identity version, payout-eligibility series/revision/hash and all six current fact heads, accepted-terms authority, tax-decision series/revision/hash and current TaxProfileFactV1 head, both decisions' expiry/applicability, payout-policy series/revision/hash and applicability, every source purpose key's current state including the compensation-head and payout-authorization signers, byte-identical payout-authorization-ledger commit tuple, preparation-authorization deadline, immutable destination fingerprint, CompensationEventIDSetV1, PayoutLotAtomRangeSetV1, and SettlementReceiptHashSetV1 all relationship-check at the send fence before the rail call
    And the send-fence authority tuple is strictly before both the instruction expiry and preparation-authorization deadline; equality is expired
    And every selected lot and preparation state is unchanged from the head except for storage of this exact instruction, so an unrelated ledger append cannot starve payout while a selected-state change blocks it
    And any mismatch uses one atomic signed-void-then-compensation-created-withhold group before the send fence, leaves its exact lots withheld for reconciliation, and creates no rail call

  Scenario: Currencies never share a balance or rounding operation
    Given one operator has mature compensation in multiple currencies
    When balances and payout thresholds are evaluated
    Then each currency has a separate append-only balance, threshold, payout, and clawback
    And no implicit exchange rate or cross-currency netting occurs

  # --- ledger integrity -----------------------------------------------------

  Scenario: Share accrual is idempotent per settled attempt
    Given settlement and accrual completed for an attempt
    When the same settlement event is replayed after a restart or duplicate delivery
    Then no second accrual, ledger row, or payout eligibility is created

  Scenario: Operator share entries live in the same append-only ledger discipline
    Given any accrual, cancellation, clawback, or payout entry is written
    Then it carries the append-only sequence, previous Roger compensation-ledger entry hash or RogerLedgerGenesisV1 complete hash at first sequence, timestamps, and signer key ID
    And it never mutates a prior entry

  Scenario: Every compensation transition is uniquely causal and signed
    Given an entitlement aggregate, payout lot, reservation, reversal, or debt changes through an allowed compensation_state_machines.feature transition
    When Roger Core appends the transition
    Then the entry binds operator, TowerIDScopeSetV1 complete hash, settlement, payment-event or enforcement-event IDs, exact state-machine kind, prior state hash, fixed-point inputs, result state, Core-observed time, and purpose-bound signer key ID
    And the same causal event cannot append twice

  Scenario: Ledger or payment-store failure fails closed for share money
    Given the ledger or payment state is unavailable during accrual or payout
    When the operation cannot verify authoritative state
    Then no share is accrued, cancelled, or paid
    And the operation is retried only from durable authoritative state

  Scenario: Concurrent accrual workers append one transition
    Given two workers reconcile the same settlement and payment version concurrently
    When both attempt compensation accrual
    Then one unique causal transition commits
    And the other observes and returns the exact committed state without another balance change

  Scenario: Concurrent payout runs reserve one mature balance once
    Given two payout runs select the same operator and currency concurrently
    When they prepare payable entries
    Then one database transaction moves those entries to reserved_prepared for one payout ID
    And the other run cannot include them

  # --- current operator payout eligibility and closure races ---------------

  Scenario: Only the purpose-separated eligibility authority can clear an operator for payout
    Given current account, payout identity, accepted terms, region, sanctions, currency, rail, and destination evidence has been ledger-anchored
    When the centrally authorized eligibility service commits PayoutEligibilityDecisionV1
    Then its signature binds schema/network/protocol, signer key ID, deterministic eligibility-scope ID and derived decision series/revision/prior hash, operator and payout-identity version, immutable destination fingerprint, operator-account stable series/revision/hash, accepted-terms revision/hash, sanctions-ruleset version/hash, jurisdiction/currency/unit/scale/rail scope, eligible or held result, closed reason, deterministic applicability evidence ordinal/kind/source tag/object identity-or-missing-self absence/Core tuple, exact PayoutEligibilityEvidenceSetV1 and policy/registry hashes, issue/expiry, and Core commit authority tuple
    And only current result eligible may authorize its exact payout preparation
    And an operator, Tower, ordinary administrator, payout callback, compensation signer, payout signer, or Tower lifecycle event cannot issue or edit it

  Scenario Outline: Payout eligibility has one closed result shape
    Given PayoutEligibilityDecisionV1 has result "<result>"
    Then its exact authority is "<authority>"

    Examples:
      | result | authority |
      | eligible | may authorize only the bound operator, identity, destination, scope, and preparation while current and unexpired |
      | held | no new preparation or send fence; unsubmitted lots use a signed external-materialization withhold event, reserved_prepared lots use the atomic abort-or-void then withhold group, and reserved_submitted instructions remain rail-locked under the required incident while later payouts are held |

  Scenario: Payout eligibility revision and replay use one durable CAS head
    Given deterministic eligibility-scope ID E names one operator/currency/unit/scale/rail scope and its derived decision series D is at revision R and complete hash H
    When one or many workers submit a next eligibility decision
    Then one transaction may compare-and-swap D, R, and H to exactly R plus 1 and the new complete hash
    And exact replay returns the committed decision without another hold, void, incident, or event
    And a lower revision is stale while equal revision with different bytes is conflict-quarantined and authorizes no payout
    And a skipped revision, wrong prior hash, another operator, another scope, or an alternate revision-1 series cannot advance or coexist with D

  Scenario: Payout eligibility applicability cannot be backdated
    Given an eligibility decision names its deterministic PayoutEligibilityEvidenceSetV1 applicability source
    When its cutoff is compared with payout preparation or a send fence
    Then eligible uses all seven members as prerequisites, while held uses ordinals through its first nonpassing member plus the always-present policy member and treats later facts as snapshot-only
    And the cutoff source is the prerequisite member with greatest effective Core tuple using evidence ordinal as the final tie-break; a missing member contributes the decision commit tuple and canonical object-reference absence
    And the source ordinal/kind/tag/object type/ID/hash or missing-self absence and Core tuple are byte-identical to that deterministic member shape
    And signer issue time, operator input, provider callback, or invented historical timestamp cannot move that cutoff

  Scenario: Every positive use revalidates the exact eligibility authorities
    Given a current eligible decision binds six exact PayoutEligibilityFactV1 stable-slot heads, one published policy head, its registry, and their purpose keys
    When eligibility would release or classify a lot as payable, mature compensation, return a failed payout to payable, reserve a preparation, authorize a covered head or instruction, or commit a send fence
    Then that use transaction revalidates the decision head, all six fact heads, their freshness and expiry, the current applicable policy/publication, the registry hash, every duplicated payload field, and every purpose key as active and nonrevoked in the greatest accepted current trust publication
    And the eligibility-scope lock makes a fact, policy, key-state, decision, or send update serialize so exactly one side wins
    And any drift or unavailable head fails closed immediately rather than using the still-current-looking prior eligible decision

  Scenario: Unavailable payout-eligibility authority fails closed
    Given current evidence, decision-series head, decision signer, incident signer, or authoritative time is unavailable
    When payout preparation or send runs
    Then no eligible decision, instruction, or send fence is synthesized from a prior default or operator claim
    And exact unsubmitted lots remain or become withheld under one eligibility_authority_unavailable_hold with reason eligibility-authority-unavailable, reserved_prepared lots use the atomic abort-or-void then withhold group to create that hold, while a submitted instruction remains rail-locked and every later payout is held externally until authoritative resolution

  Scenario: The payout send fence and an eligibility restriction have one CAS winner
    Given reserved_prepared payout P binds current eligible decision series D at revision R and complete hash H
    When its send worker and a next held decision race
    Then the send fence may commit only by compare-and-swapping D, R, H, its expiry/applicability tuple, all six bound current fact-slot heads and current policy/publication head, the bound identity/destination, every source purpose key's current active state, and the prepared state
    And if the held decision wins first, P uses the exact atomic abort-or-void then external-materialization-withhold group and makes no rail call
    And if the send fence wins first, P becomes reserved_submitted and the cutoff relation determines prospective handling or a post-send incident
    And equal stored times use the global Core authority sequence rather than a signer or operator timestamp

  Scenario Outline: Sanctions, identity loss, terms loss, or closure around payout has one effect
    Given payout P has state "<state>"
    When a purpose-signed held eligibility decision becomes applicable "<cutoff relation>" P's send-fence tuple
    Then handling is "<outcome>"

    Examples:
      | state | cutoff relation | outcome |
      | no preparation | before any send fence | affected mature lots become withheld and no reservation, instruction, or rail call exists |
      | reserved_prepared with no instruction | at or before | use the atomic abort-then-withhold group to materialize the exact eligibility decision hold, and create no instruction or rail call |
      | reserved_prepared with an instruction but no send fence | at or before | use the atomic signed-void-then-withhold group to materialize the exact eligibility decision hold, and create no rail call |
      | reserved_submitted with rail result unknown | after | prospective only for later payouts; P remains rail-locked to exact reconciliation |
      | reserved_submitted with rail result unknown | at or before | append one open_rail_unknown PayoutEligibilityIncidentV1, keep P rail-locked, and hold every later payout |
      | paid | after | prospective only for later payouts; P and paid lots remain unchanged |
      | paid | at or before | append one open_postsend_disbursement incident and hold later payouts without relabeling or externally debiting P |
      | confirmed failed | at or before | cancel any pending-negative affected ranges, return each surviving range to one withheld state carrying the canonical set of every current hold including eligibility, and append one closed_no_disbursement incident |

  Scenario: A post-send payout-eligibility incident is immutable and idempotent
    Given a held eligibility decision applies at or before payout P's committed send fence
    When PayoutEligibilityIncidentV1 commits
    Then its incident signature binds schema/network/protocol, signer key, incident ID/state/revision/prior hash, operator/payout/preparation IDs, instruction hash, prior/held decision revisions and hashes, applicability/send-fence tuples, rail state/result hash or canonical absence, destination/jurisdiction/currency/rail/amount bindings, evidence hash, and independently assigned eligibility-incident-ledger Core commit tuple
    And incident ID derives from strict JCS [PayoutEligibilityIncidentV1-series-v1,network-ID,payout-ID]
    And one transaction assigns that tuple and compare-and-swaps the incident revision/head, held-decision head, send fence, rail result, and operator payout-hold head; later transitions also require the current incident head and signer active/nonrevoked
    And duplicate or concurrent discovery creates one incident and one operator-wide payout hold
    And authenticated rail failure changes open_rail_unknown to closed_no_disbursement, excludes that closed incident from the current hold set, cancels pending-negative affected ranges, and returns each surviving range to mature_payable only if no hold remains or otherwise withheld with the canonical set of every still-current eligibility, tax-decision, reconciliation, and policy hold reference
    And authenticated rail success changes open_rail_unknown to open_postsend_disbursement while the exact lots become paid
    And no callback, administrator, operator deletion, or incident signer can redirect, resend, erase, or reverse the submitted transfer or clear later holds without a separately authorized decision

  Scenario: Account deletion is a retained closure state rather than monetary erasure
    Given an operator with a candidate, unpaid liability, preparation, submitted payout, incident, or debt requests account deletion
    When Roger Core accepts the closure request
    Then the operator becomes closure_pending, receives no new compensated grants, and its current payout eligibility becomes held
    And any preparation is aborted or voided according to instruction presence while every submitted payout reaches authenticated success or failure under its immutable instruction
    And hard deletion cannot remove operator/Tower monetary IDs, destination fingerprints, authority tuples, provider mappings, liability, debt, incidents, receipts, or ledger evidence required by the approved retention policy
    And privacy deletion or pseudonymization of unrelated profile data is a separate policy action that cannot change money state
    And closure cannot become terminal while a submitted payout is unresolved or erase positive dust, unpaid liability, or debt

  Scenario: Ambiguous payout-provider response is reconciled before retry
    Given Roger Core moved a payout to reserved_submitted and called the rail with a stable provider idempotency key but lost the response
    When payout state is unknown
    Then it does not send a new payout or new idempotency key
    And it queries or reconciles the provider until success or failure is authoritative
    And provider success appends one paid transition even after process restart

  Scenario Outline: Payout identity changes respect the send fence
    Given payout P is "<state>" and binds a verified payout identity version
    When the operator changes or loses that identity
    Then the result is "<result>"

    Examples:
      | state | result |
      | reserved_prepared before an instruction exists | use the atomic abort-then-external-materialization-withhold group without a rail call |
      | reserved_prepared after an instruction exists but before the send fence | use the atomic signed-void-then-external-materialization-withhold group without a rail call |
      | reserved_submitted | do not redirect, void, resend, or release; reconcile the exact original instruction and hold later payouts |
      | paid | keep confirmed success bound to the original instruction and hold later payouts under current eligibility |
      | confirmed failed | release exact lots only to current eligibility checks under the new identity state |

  Scenario: Payout and clawback racing on the same compensation lots serialize
    Given a payout reservation and a refund, dispute, or fraud clawback target the same lots concurrently
    When ledger transactions run
    Then one ordered transition owns each lot at a time
    And reserved_prepared uses one atomic group: it is first signed-aborted as a whole when no instruction exists or signed-voided as a whole when one exists before its send fence, then an ordered withhold child commits the exact unreserved lots to withheld under the reconciliation/negative-adjustment hold
    And only a later serializable application transaction may target that committed withheld state, partition an exact proper range when needed, and cancel the selected whole lot or child
    And reserved_submitted remains locked until authenticated failure applies the complete pending set or success atomically appends payout_succeeded followed by exactly one same-currency resolved_submitted_success debt_create per PendingSubmittedNegative record in canonical order
    And each already-confirmed paid lot remains paid while the exhaustive nonoverlapping causal DebtRange set covers only the exact reversed atom ranges, with no second payout-lot mutation or external debit

  Scenario: Failed payout returns reserved entries to payable exactly once
    Given a payout provider authoritatively rejects or fails a reserved_submitted payout whose lots remain eligible and unheld
    When reconciliation commits the failure
    Then its reserved_submitted entries return to mature_payable state once
    And no paid transition or duplicate payout exists

  Scenario: Clawback creates a bounded negative operator balance
    Given already-paid compensation later becomes ineligible through refund, chargeback, or fraud
    When the clawback event commits
    Then a signed negative balance offsets future compensation in the same currency
    And debt_create and every resulting DebtRangeV1 copy the exact accepted_terms PayoutEligibilityFactV1 series/revision/complete hash/effective/expiry and PayoutPolicyV1 series/revision/complete hash/applicability/expiry from the originating paid TowerPayoutInstructionV1, whose send fence proved both current and valid
    And those immutable creation-time authorities permit only the policy's signed same_currency_future_compensation_offset_only mode; later terms withdrawal or policy change neither erases that incurred debt nor retroactively substitutes another authority
    And every later debt_offset CASes the current DebtRange and positive same-currency compensation range against those historical authorities, while fixed external_debit_forbidden means v1 never initiates a bank debit or cross-currency collection
    And account closure cannot erase the debt, positive unpaid liability, dust, or audit trail

  Scenario: A Tower cannot see or alter other parties' money
    Given a compensated Tower queries its earnings
    When Roger Core serves the operator earnings view
    Then it exposes only that operator's own accruals, clawbacks, and payouts
    And no consumer identity, Station earning detail, or other operator balance is exposed

  # --- self-dealing and abuse -----------------------------------------------

  Scenario: Self-dealing traffic is withheld pending review
    Given the consumer account, Station owner, and Tower operator for a job are the same verified party
    When share accrual runs under the self-dealing policy
    Then the Tower share is withheld pending review
    And repeated patterns are recorded as abuse evidence

  # Identity equality is the naive case. The real attack uses several distinct verified
  # identities, so linkage must be evaluated on evidence rather than on account equality.

  Scenario Outline: Linked but unmerged parties are treated as self-dealing on evidence
    Given the consumer account, Station owner, and Tower operator are distinct verified identities
    And they share "<linkage>"
    When share accrual runs under the self-dealing policy
    Then the Tower share is withheld pending review under the same policy as the same-party case
    And the linkage evidence kind and its exact signals are bound to the withholding record

    Examples:
      | linkage                                              |
      | one payout destination fingerprint                   |
      | one funding instrument fingerprint                   |
      | one verified beneficial owner                        |
      | one registered business identity or tax identifier   |
      | one payout or billing postal identity                |
      | one authenticating device or credential fingerprint  |
      | a declared affiliation between the accounts          |

  Scenario: A linked-entity withholding has a bounded review with a terminal outcome
    Given a Tower share is withheld for same-party or linked-entity self-dealing
    When the signed self-dealing review deadline arrives
    Then the withholding resolves to exactly one terminal disposition of released, forfeited, or escalated to a purpose-signed enforcement decision
    And an escalation sets a new signed deadline, so no withholding remains open without one
    And a review that never runs by its deadline resolves under the signed default disposition rather than remaining open indefinitely

  Scenario: One shared anomaly budget spans every linked operator account
    Given several operator accounts are linked by any evidence kind above
    When per-operator anomaly thresholds, quotas, and unmatured exposure caps are applied
    Then the linked set shares one budget rather than receiving one budget each
    And splitting activity across the linked accounts does not raise the combined ceiling

  # --- key possession is not relay work -------------------------------------

  Scenario: Concurrent Tower sessions from disjoint origins withhold accrual pending review
    Given one Tower identity authenticates concurrent sessions from disjoint network origins
    When compensation accrual runs for attempts observed on those sessions
    Then accrual for the affected attempts is withheld pending review rather than accrued
    And the credential-collision evidence is bound to the withholding record
    And settlement, the consumer charge, and the Station earning are unaffected

  Scenario: Sustained origin instability is compensation-relevant evidence, not routing-only
    Given a Tower's authenticated sessions change network origin more often than the signed policy allows over a bounded window
    When compensation health is evaluated
    Then the pattern is recorded as attribution evidence and counts toward the operator's anomaly budget
    And crossing the signed threshold withholds new accrual until reviewed
    And a single origin change alone does not withhold, matching the routing-side evidence-only rule

  Scenario: Wash traffic bursts trigger review before payout, not silent payment
    Given a compensated Tower's share accrual rate exceeds its approved anomaly threshold
    When the payout run executes
    Then affected amounts are held for review instead of paid
    And the operator is notified with the held settlement IDs

  Scenario: Many Towers under one operator share one payout identity and one set of limits
    Given one verified operator runs several compensated Towers
    When accrual and payout run
    Then all shares accrue to that operator's single payout identity
    And per-operator anomaly thresholds and quotas apply across all their Towers
    And one same-operator same-currency payout may select lots from several Towers only by binding the same payout-owned TowerIDScopeSetV1 complete hash in its preparation, instruction, and every payout-family event

  Scenario: Multi-Tower payout scope and lifecycle authority derive only from selected source lots
    Given one same-operator same-currency payout selects exact lot ranges from one or many Towers
    When preparation, head attestation, instruction verification, or send-fence CAS runs
    Then the canonical Tower-ID scope is exactly the sorted unique set derived from every selected lot's immutable Tower and SettlementReceiptV2 lineage
    And one TowerLifecyclePayoutAuthorityV1 leaf per derived Tower binds its Tower ID, exact current TowerLifecycleEventV1 revision/complete hash/compensation disposition/effective tuple, and TowerSelectedPayoutLotRangeSetV1 complete hash, member count, and total share atoms
    And the TowerLifecyclePayoutAuthoritySetV1 complete hash is bound by preparation, covered payout-authority leaf, and instruction
    And the send fence rereads every leaf and fails if one revision/hash changed or any applicable disposition requires a hold or separate forfeiture decision
    And an omitted/extra/substituted Tower, stale lifecycle leaf, asynchronous hold lag, or scope hash supplied by the operator cannot authorize send

  Scenario: V1 never transfers a Tower identity or its compensation history in place
    Given a current or prospective operator requests that an existing Tower ID change owner
    When Roger Core evaluates the request
    Then the ownership mutation is rejected without changing admission, payout lots, balances, debt, grants, candidates, or history
    And the prospective operator must enroll a new Tower ID under fresh keys, account proof, payout verification, quarantine, and probes
    And the prior owner must separately drain or revoke the old Tower under its existing lifecycle
    And no work or compensation is moved between the old and new Tower IDs

  # --- enforcement and forfeiture -------------------------------------------

  Scenario Outline: Grant-time and enforcement-time state determine compensation precisely
    Given a job has "<condition>"
    When compensation reconciliation runs
    Then the compensation result is "<result>"

    Examples:
      | condition | result |
      | no compensated capability at grant issue | ineligible: not enrolled |
      | compensated and active at grant, exact evidence before deadline, current PayoutEligibilityDecisionV1 eligible at maturity | accrued after funds mature |
      | drain began after grant and exact evidence arrived before deadline | eligible under the grant-time snapshot |
      | ordinary certificate expiry after exact evidence arrived before deadline | eligible under the grant-time snapshot |
      | non-security suspension at maturity | withheld pending resolution |
      | security cutoff before Core observed complete evidence | no job settlement and no candidate |
      | security cutoff after Core observed complete evidence but before maturity | withheld for enforcement review |
      | fraud finding before any recognized amount is paid | eligible amount is forfeited by one homogeneous signed unpaid_forfeiture decision and exhaustive coverage set |
      | fraud finding after the entire recognized amount is paid | the recognized amount becomes an exhaustive signed paid_clawback coverage and debt set under one homogeneous decision |
      | fraud finding after only part of the recognized amount is paid | one independently revisioned unpaid_forfeiture decision exhausts the unpaid ranges and a separate independently revisioned paid_clawback decision exhausts the paid ranges |
      | ordinary later revocation unrelated to the historical settlement | historical untainted candidate follows normal funds policy |
      | payout eligibility becomes held after candidate creation but before maturity | immutable candidate remains eligible; affected unsubmitted lots are withheld and any submitted instruction remains rail-locked under the incident while later payouts are held |
      | payout eligibility becomes held after maturity but before send fence | no preparation exists or the exact atomic abort-or-void then external-materialization-withhold group commits |
      | payout eligibility becomes held after send fence | submitted payout reconciles immutably and all later payouts are withheld |

  Scenario: Suspension holds accrued but unpaid share; it does not erase it
    Given a compensated Tower is suspended for a reversible policy reason
    When the suspension is later lifted without a fraud finding
    Then withheld amounts from before suspension become payable under normal clearing rules

  Scenario: A fraud finding uses separate homogeneous authorities for unpaid and paid ranges
    Given enforcement concludes an operator forged evidence or manufactured traffic
    And one current final substantiated CompensationEnforcementFindingV1 per homogeneous unpaid or paid TargetScopeDigestV1 binds that exact digest under the current published CompensationEnforcementPolicyV1
    When one unpaid_forfeiture decision for exact withheld ranges and one independently revisioned paid_clawback decision for exact paid ranges are recorded as applicable
    Then the unpaid decision's one forfeit event maps every signed range one-to-one to exhaustive unpaid-forfeiture coverage with the decision reference bound in the ledger
    And the paid decision's one enforcement debt_create event maps every signed range one-to-one to exhaustive paid-clawback coverage and same-currency DebtRanges
    And each decision binds its exact policy/finding heads and HistoricalAcceptedTermsAuthoritySetV1 whose facts resolve byte-identically through every source lot's immutable GrantCompensationSnapshotV1 lineage
    And either decision may be canonically absent when its homogeneous range category is empty, while no single decision or event mixes unpaid and paid dispositions
    And untainted historical receipts remain immutable and verifiable

  Scenario: Standalone Towers have no revenue-share surface at all
    Given a Tower runs in standalone mode
    When any revenue-share configuration, route, or claim is attempted
    Then it is structurally absent, not merely disabled
