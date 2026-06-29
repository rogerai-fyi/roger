# Spec-first behavior contract for the lineage-attributed dispute clawback
# (store.ChargebackLineage / store.Chargeback, wired from the
# charge.dispute.created webhook in cmd/rogerai-broker/billing.go).
#
# These scenarios are the SOURCE OF TRUTH for what a chargeback must and must not
# do. They run identically against the in-memory reference store and the Postgres
# store. No step definitions or Go yet — approve this spec first.
#
# Money model recap (so the numbers below are unambiguous):
#   - 1 credit == $1 (creditUSD default). All amounts are credits == dollars.
#   - The consumer pays `cost` per request. The operator's lot Gross == owner share
#     == cost * (1 - feeRate). At a 30% fee, a 100.00 request earns the operator a
#     70.00 lot and the platform keeps 30.00.
#   - A dispute amount is in CONSUMER dollars (what the card network pulls back).
#   - The clawback claws the OPERATOR'S SHARE of the disputed money — never more,
#     never lots funded by the consumer's OTHER, non-disputed top-ups, and never an
#     unrelated honest operator's lots. Whatever the operator share does not cover
#     is booked as a platform_loss (the platform is liable), not clawed elsewhere.
#
# Invariants every scenario upholds (unless a scenario is explicitly flagged as a
# latent-bug witness):
#   I1  CONSERVATION: clawed(held/payable) + reversed(already-paid) + platform_loss
#       == disputed amount, to the penny.
#   I2  NO COLLATERAL CLAW: a lot funded by a top-up that was NOT disputed, or owned
#       by an operator the disputed consumer never paid, is never touched.
#   I3  IDEMPOTENCY: re-processing the same Stripe dispute id is a total no-op
#       (claws 0, debits the wallet 0 more, mints no new ledger rows).
#   I4  CONSUMER DEBIT: the consumer wallet is debited the FULL disputed amount
#       exactly once (a chargeback row of -amount), independent of how the operator
#       side is recovered.

Feature: A chargeback claws the operator's share of the disputed money and nothing more

  Background:
    Given a fresh money store
    And the platform fee rate is 30%

  # ===========================================================================
  # 1. THE OVER-CLAW REGRESSION (headline). The single most important set.
  #    Real bug we fixed: capping the claw on operator GROSS instead of CONSUMER
  #    cost over-clawed by 1/(1-feeRate), reaching into lots funded by the
  #    consumer's OTHER (non-disputed) top-ups and making an honest operator
  #    absorb the platform's fee. The cap MUST be on consumer cost.
  # ===========================================================================

  Scenario: $100 dispute at 30% fee claws exactly the $70 operator share, books $30 platform loss, leaves the other $70 lot intact
    Given wallet "alice" has 1000.00 in real credits
    And node "n1" is owned by account "op1"
    And alice has a settled request "r_disputed" of cost 100.00 on node "n1"
    And alice has a settled request "r_other" of cost 100.00 on node "n1"
    And the lot for "r_disputed" has gross 70.00
    And the lot for "r_other" has gross 70.00
    When a dispute "dp_regress" of 100.00 is opened on alice's most recent charge for "r_disputed"
    Then exactly 70.00 is clawed from operator "op1"
    And the platform loss is 30.00
    And the lot for "r_other" is still intact and not clawed
    And operator "op1" retains 70.00 in lots
    And clawed plus reversed plus platform loss equals 100.00
    And alice's balance is reduced by exactly 100.00

  Scenario: The other top-up's lot survives even when it is the NEWEST lot (recency must still respect the consumer-cost cap)
    # Recency claws newest-first; the cap on consumer cost stops it before it bleeds
    # into the second top-up, no matter which lot is newer.
    Given wallet "alice" has 1000.00 in real credits
    And node "n1" is owned by account "op1"
    And alice has a settled request "r_old" of cost 100.00 on node "n1"
    And alice has a later settled request "r_new" of cost 100.00 on node "n1"
    And the lot for "r_old" has gross 70.00
    And the lot for "r_new" has gross 70.00
    When a wallet-recency dispute "dp_recency" of 100.00 is opened on alice
    Then exactly one lot is clawed
    And the newest lot "r_new" is clawed
    And the lot for "r_old" is still intact and not clawed
    And exactly 70.00 is clawed from operator "op1"
    And the platform loss is 30.00

  Scenario: A consumer with MANY non-disputed top-ups loses only one top-up's worth of operator share
    Given wallet "alice" has 10000.00 in real credits
    And node "n1" is owned by account "op1"
    And alice has 5 settled requests of cost 100.00 each on node "n1" with owner share 70.00 each
    When a wallet-recency dispute "dp_one_of_five" of 100.00 is opened on alice
    Then exactly one lot is clawed
    And exactly 70.00 is clawed from operator "op1"
    And the platform loss is 30.00
    And operator "op1" retains 280.00 across 4 untouched lots
    And clawed plus reversed plus platform loss equals 100.00

  Scenario: Two separate consumers, one disputes — the other consumer's operator lots are never touched (cross-consumer isolation)
    Given wallet "alice" has 1000.00 in real credits
    And wallet "bob" has 1000.00 in real credits
    And node "n1" is owned by account "op1"
    And alice has a settled request "r_alice" of cost 100.00 on node "n1" with owner share 70.00
    And bob has a settled request "r_bob" of cost 100.00 on node "n1" with owner share 70.00
    When a wallet-recency dispute "dp_alice" of 100.00 is opened on alice
    Then exactly 70.00 is clawed from operator "op1"
    And the lot for "r_bob" is still intact and not clawed
    And bob's balance is unchanged

  Scenario: Disputing consumer paid TWO different operators — only the lots that consumer funded are eligible, capped by consumer cost
    Given wallet "alice" has 1000.00 in real credits
    And node "n1" is owned by account "op1"
    And node "n2" is owned by account "op2"
    And alice has a later settled request "r_n2" of cost 100.00 on node "n2" with owner share 70.00
    And alice has an earlier settled request "r_n1" of cost 100.00 on node "n1" with owner share 70.00
    When a wallet-recency dispute "dp_two_ops" of 100.00 is opened on alice
    Then exactly 70.00 is clawed from operator "op2"
    And operator "op1" is not clawed at all
    And the lot for "r_n1" is still intact and not clawed
    And the platform loss is 30.00

  # ===========================================================================
  # 2. FEE-RATE PERMUTATIONS. The operator share clawed and the platform loss
  #    booked both scale with the fee rate; conservation holds at every rate.
  # ===========================================================================

  Scenario Outline: A single full-charge dispute claws the operator share and books the fee as platform loss, at any fee rate
    Given wallet "alice" has 100000.00 in real credits
    And node "n1" is owned by account "op1"
    And the platform fee rate is <fee_pct>%
    And alice has a settled request "r1" of cost <cost> on node "n1" with owner share <gross>
    When a wallet-recency dispute "dp_fee" of <cost> is opened on alice
    Then exactly <gross> is clawed from operator "op1"
    And the platform loss is <loss>
    And clawed plus reversed plus platform loss equals <cost>

    Examples:
      | fee_pct | cost   | gross  | loss   |
      | 0       | 100.00 | 100.00 | 0.00   |
      | 10      | 100.00 | 90.00  | 10.00  |
      | 20      | 100.00 | 80.00  | 20.00  |
      | 30      | 100.00 | 70.00  | 30.00  |
      | 30      | 50.00  | 35.00  | 15.00  |
      | 30      | 33.33  | 23.331 | 9.999  |
      | 50      | 100.00 | 50.00  | 50.00  |
      | 70      | 100.00 | 30.00  | 70.00  |
      | 100     | 100.00 | 0.00   | 100.00 |

  Scenario Outline: Number-of-lots permutations — disputing one charge among many claws exactly one charge's share
    Given wallet "alice" has 1000000.00 in real credits
    And node "n1" is owned by account "op1"
    And alice has <n_lots> settled requests of cost 100.00 each on node "n1" with owner share 70.00 each
    When a wallet-recency dispute "dp_n" of 100.00 is opened on alice
    Then exactly 70.00 is clawed from operator "op1"
    And the platform loss is 30.00
    And <untouched> lots are still intact and not clawed

    Examples:
      | n_lots | untouched |
      | 1      | 0         |
      | 2      | 1         |
      | 3      | 2         |
      | 10     | 9         |
      | 50     | 49        |

  # ===========================================================================
  # 3. LOT-STATE COVERAGE: held / payable lots are clawed in place; an
  #    ALREADY-PAID lot is reversed via Stripe.
  # ===========================================================================

  Scenario: A held lot (inside the 120-day window) is clawed in place with no Stripe action
    Given wallet "alice" has 1000.00 in real credits
    And node "n1" is owned by account "op1"
    And alice has a settled request "r1" of cost 100.00 on node "n1" with owner share 70.00
    And the lot for "r1" is held
    When a wallet-recency dispute "dp_held" of 100.00 is opened on alice
    Then the lot for "r1" is marked clawed
    And 70.00 is counted as clawed-in-place
    And no Stripe transfer reversal is requested
    And an operator adjustment ledger row of -70.00 is written for the dispute
    And the platform loss is 30.00

  Scenario: A payable lot (hold cleared, not yet paid out) is clawed in place with no Stripe action
    Given wallet "alice" has 1000.00 in real credits
    And node "n1" is owned by account "op1"
    And alice has a settled request "r1" of cost 100.00 on node "n1" with owner share 70.00
    And the lot for "r1" is payable
    When a wallet-recency dispute "dp_payable" of 100.00 is opened on alice
    Then the lot for "r1" is marked clawed
    And 70.00 is counted as clawed-in-place
    And no Stripe transfer reversal is requested

  Scenario: An ALREADY-PAID lot is marked clawed, books a payout_reversed row, and is returned as a Stripe reversal
    Given wallet "alice" has 1000.00 in real credits
    And node "n1" is owned by account "op1"
    And alice has a settled request "r1" of cost 100.00 on node "n1" with owner share 70.00
    And the lot for "r1" was paid out on Stripe transfer "tr_paid_1"
    When a wallet-recency dispute "dp_paid" of 100.00 is opened on alice
    Then the lot for "r1" is marked clawed
    And 0.00 is counted as clawed-in-place
    And one Stripe transfer reversal is returned for transfer "tr_paid_1" of amount 70.00
    And a payout_reversed ledger row of -70.00 is written for operator "op1"
    And the reversal carries dispute "dp_paid", the lot id, and operator account "op1"
    And the platform loss is 30.00

  Scenario: A dispute spanning a PAID lot and a HELD lot reverses one and claws the other
    Given wallet "alice" has 1000.00 in real credits
    And node "n1" is owned by account "op1"
    And alice has an earlier settled request "r_paid" of cost 100.00 on node "n1" with owner share 70.00
    And alice has a later settled request "r_held" of cost 100.00 on node "n1" with owner share 70.00
    And the lot for "r_paid" was paid out on Stripe transfer "tr_x"
    And the lot for "r_held" is held
    When a wallet-recency dispute "dp_span" of 200.00 is opened on alice
    Then the lot for "r_held" is clawed in place for 70.00
    And one Stripe transfer reversal is returned for transfer "tr_x" of amount 70.00
    And clawed-in-place plus reversed equals 140.00
    And the platform loss is 60.00
    And clawed plus reversed plus platform loss equals 200.00

  Scenario: A legacy paid lot with no recorded transfer id still claws the ledger but issues no reversal
    Given wallet "alice" has 1000.00 in real credits
    And node "n1" is owned by account "op1"
    And alice has a settled request "r1" of cost 100.00 on node "n1" with owner share 70.00
    And the lot for "r1" was paid out with no recorded Stripe transfer id
    When a wallet-recency dispute "dp_legacy" of 100.00 is opened on alice
    Then the lot for "r1" is marked clawed
    And a payout_reversed ledger row of -70.00 is written for operator "op1"
    And the returned reversal has an empty transfer id
    And the broker skips the Stripe reversal and logs it for manual reconciliation
    And the ledger clawback still stands

  # ===========================================================================
  # 4. NEWEST-FIRST ORDERING (wallet-recency path only).
  # ===========================================================================

  Scenario: Recency claws the newest eligible lots first
    Given wallet "alice" has 1000.00 in real credits
    And node "n1" is owned by account "op1"
    And alice has settled requests in this order oldest-first: "r1", "r2", "r3" each cost 100.00 owner share 70.00 on node "n1"
    When a wallet-recency dispute "dp_order" of 100.00 is opened on alice
    Then the lot for "r3" is clawed
    And the lot for "r2" is still intact and not clawed
    And the lot for "r1" is still intact and not clawed

  Scenario: Recency walks newest to oldest until the consumer-cost cap is met
    Given wallet "alice" has 1000.00 in real credits
    And node "n1" is owned by account "op1"
    And alice has settled requests in this order oldest-first: "r1", "r2", "r3" each cost 100.00 owner share 70.00 on node "n1"
    When a wallet-recency dispute "dp_two" of 200.00 is opened on alice
    Then the lot for "r3" is clawed
    And the lot for "r2" is clawed
    And the lot for "r1" is still intact and not clawed
    And 140.00 is clawed from operator "op1"
    And the platform loss is 60.00

  # ===========================================================================
  # 5. PARTIAL COVERAGE & PLATFORM LOSS. A dispute larger than what this
  #    consumer's lots cover books the remainder as platform loss — it NEVER
  #    claws unrelated operators to make up the difference.
  # ===========================================================================

  Scenario: Dispute exceeds the consumer's total operator share — remainder is all platform loss, no unrelated claw
    Given wallet "alice" has 1000.00 in real credits
    And node "n1" is owned by account "op1"
    And node "n2" is owned by account "op2"
    And alice has a settled request "r1" of cost 100.00 on node "n1" with owner share 70.00
    And an unrelated consumer "carol" has a settled request "r_carol" of cost 500.00 on node "n2" with owner share 350.00
    When a wallet-recency dispute "dp_big" of 500.00 is opened on alice
    Then exactly 70.00 is clawed from operator "op1"
    And the platform loss is 430.00
    And operator "op2" is not clawed at all
    And the lot for "r_carol" is still intact and not clawed
    And clawed plus reversed plus platform loss equals 500.00

  Scenario: Disputing consumer has NO lots at all — the whole dispute is platform loss
    Given wallet "alice" has 1000.00 in real credits
    And alice has no settled requests
    When a wallet-recency dispute "dp_nolots" of 100.00 is opened on alice
    Then nothing is clawed from any operator
    And the platform loss is 100.00
    And alice's balance is reduced by exactly 100.00
    And a platform_loss ledger row of -100.00 is written for the dispute

  Scenario: Consumer's lots are all seed-funded (zero operator gross) — dispute is platform loss
    # Seed-funded spend mints no operator earning, so there is nothing to claw.
    Given wallet "alice" has 100.00 in FREE seed credits
    And node "n1" is owned by account "op1"
    And alice has a settled request "r1" of cost 100.00 on node "n1" funded entirely by seed credits
    When a wallet-recency dispute "dp_seed" of 100.00 is opened on alice
    Then nothing is clawed from any operator
    And the platform loss is 100.00

  # ===========================================================================
  # 6. IDEMPOTENCY / REDELIVERY / DOUBLE CHARGEBACK.
  # ===========================================================================

  Scenario: A redelivered dispute id is a total no-op
    Given wallet "alice" has 1000.00 in real credits
    And node "n1" is owned by account "op1"
    And alice has a settled request "r1" of cost 100.00 on node "n1" with owner share 70.00
    When a wallet-recency dispute "dp_1" of 100.00 is opened on alice
    And the same dispute "dp_1" is delivered again
    Then the redelivery reports already-handled
    And the redelivery claws back 0.00
    And the redelivery books no additional platform loss
    And alice's balance is unchanged by the redelivery
    And the lot for "r1" is clawed exactly once
    And no second chargeback ledger row is written for "dp_1"

  Scenario: Redelivery is a no-op even when the first delivery fully clawed multiple lots
    Given wallet "alice" has 1000.00 in real credits
    And node "n1" is owned by account "op1"
    And alice has 3 settled requests of cost 100.00 each on node "n1" with owner share 70.00 each
    When a wallet-recency dispute "dp_multi" of 300.00 is opened on alice
    And the same dispute "dp_multi" is delivered again
    Then the redelivery reports already-handled
    And the redelivery claws back 0.00
    And the total clawed across both deliveries is 210.00

  Scenario: Two DIFFERENT disputes on the same consumer each claw fresh lots; already-clawed lots are skipped
    Given wallet "alice" has 1000.00 in real credits
    And node "n1" is owned by account "op1"
    And alice has settled requests in this order oldest-first: "r1", "r2" each cost 100.00 owner share 70.00 on node "n1"
    When a wallet-recency dispute "dp_a" of 100.00 is opened on alice
    And a wallet-recency dispute "dp_b" of 100.00 is opened on alice
    Then the lot for "r2" is clawed by "dp_a"
    And the lot for "r1" is clawed by "dp_b"
    And no lot is clawed twice
    And the total clawed across both disputes is 140.00

  Scenario: A second dispute after the consumer's lots are exhausted is entirely platform loss
    Given wallet "alice" has 1000.00 in real credits
    And node "n1" is owned by account "op1"
    And alice has a settled request "r1" of cost 100.00 on node "n1" with owner share 70.00
    When a wallet-recency dispute "dp_first" of 100.00 is opened on alice
    And a wallet-recency dispute "dp_second" of 100.00 is opened on alice
    Then "dp_first" claws 70.00 from operator "op1"
    And "dp_second" claws 0.00 from any operator
    And "dp_second" books a platform loss of 100.00

  # ===========================================================================
  # 7. EXPLICIT requestID PATH vs WALLET-RECENCY PATH.
  #    With a request id the clawback is PRECISE (that request's lots, no amount
  #    cap). With no request id it is wallet-recency, capped at the disputed
  #    amount in consumer cost.
  # ===========================================================================

  Scenario: Explicit-requestID path claws exactly that request's lots and ignores all others
    Given wallet "alice" has 1000.00 in real credits
    And node "n1" is owned by account "op1"
    And alice has a later settled request "r_target" of cost 100.00 on node "n1" with owner share 70.00
    And alice has an earlier settled request "r_skip" of cost 100.00 on node "n1" with owner share 70.00
    When a dispute "dp_exact" of 100.00 is opened on alice's request "r_target"
    Then the lot for "r_target" is clawed
    And the lot for "r_skip" is still intact and not clawed
    And exactly 70.00 is clawed from operator "op1"
    And the platform loss is 30.00

  Scenario: Explicit-requestID path is NOT capped on the disputed amount — it claws the whole request's lot even if the request cost less than the dispute
    Given wallet "alice" has 1000.00 in real credits
    And node "n1" is owned by account "op1"
    And alice has a settled request "r_small" of cost 40.00 on node "n1" with owner share 28.00
    When a dispute "dp_partial_req" of 100.00 is opened on alice's request "r_small"
    Then the lot for "r_small" is clawed
    And 28.00 is clawed from operator "op1"
    And the platform loss is 72.00
    And clawed plus reversed plus platform loss equals 100.00

  Scenario: Wallet-recency path stops at the consumer-cost cap; explicit path does not (contrast)
    Given wallet "alice" has 1000.00 in real credits
    And node "n1" is owned by account "op1"
    And alice has settled requests in this order oldest-first: "r1", "r2", "r3" each cost 100.00 owner share 70.00 on node "n1"
    When a wallet-recency dispute "dp_capped" of 100.00 is opened on alice
    Then exactly one lot is clawed

  # ===========================================================================
  # 8. CONSERVATION INVARIANTS (explicit, generalized).
  # ===========================================================================

  Scenario Outline: Conservation holds for every coverage shape
    Given wallet "alice" has 1000000.00 in real credits
    And node "n1" is owned by account "op1"
    And alice has lots totaling <op_share> in operator gross across <n_lots> lots of equal cost on node "n1"
    When a wallet-recency dispute "dp_cons" of <dispute> is opened on alice
    Then clawed plus reversed plus platform loss equals <dispute>
    And no operator other than "op1" is clawed
    And alice's balance is reduced by exactly <dispute>

    Examples:
      | op_share | n_lots | dispute |
      | 70.00    | 1      | 100.00  |
      | 140.00   | 2      | 100.00  |
      | 210.00   | 3      | 200.00  |
      | 70.00    | 1      | 500.00  |
      | 700.00   | 10     | 1000.00 |
      | 0.00     | 0      | 100.00  |

  # ===========================================================================
  # 9. ADVERSARIAL & BOUNDARY EDGES.
  # ===========================================================================

  Scenario: Dispute amount exactly equals one lot's consumer cost — claws exactly that one lot
    Given wallet "alice" has 1000.00 in real credits
    And node "n1" is owned by account "op1"
    And alice has settled requests in this order oldest-first: "r1", "r2" each cost 100.00 owner share 70.00 on node "n1"
    When a wallet-recency dispute "dp_exact_lot" of 100.00 is opened on alice
    Then exactly one lot is clawed
    And the lot for "r2" is clawed
    And the lot for "r1" is still intact and not clawed

  Scenario: Dispute of 0.00 claws nothing and books no platform loss
    Given wallet "alice" has 1000.00 in real credits
    And node "n1" is owned by account "op1"
    And alice has a settled request "r1" of cost 100.00 on node "n1" with owner share 70.00
    When a wallet-recency dispute "dp_zero" of 0.00 is opened on alice
    Then nothing is clawed from any operator
    And the platform loss is 0.00
    And the lot for "r1" is still intact and not clawed

  Scenario: A chargeback after the operator was already paid out triggers a transfer reversal, not a held-lot claw
    Given wallet "alice" has 1000.00 in real credits
    And node "n1" is owned by account "op1"
    And alice has a settled request "r1" of cost 100.00 on node "n1" with owner share 70.00
    And operator "op1" has requested and settled a payout that paid the lot for "r1" on transfer "tr_after"
    When a wallet-recency dispute "dp_after_payout" of 100.00 is opened on alice
    Then one Stripe transfer reversal is returned for transfer "tr_after" of amount 70.00
    And 0.00 is counted as clawed-in-place
    And the platform loss is 30.00

  Scenario: A dispute that cannot resolve a consumer wallet performs no clawback
    Given a dispute "dp_unknown" of 100.00 arrives with no stored charge mapping and no metadata wallet
    When the dispute webhook processes "dp_unknown"
    Then no clawback is performed
    And no platform loss is booked
    And the webhook still acknowledges receipt

  Scenario: Negative-amount disputes are treated as a no-op clawback (defensive)
    Given wallet "alice" has 1000.00 in real credits
    And node "n1" is owned by account "op1"
    And alice has a settled request "r1" of cost 100.00 on node "n1" with owner share 70.00
    When the dispute webhook receives "dp_neg" with amount -100.00 for alice
    Then no clawback is performed for "dp_neg"

  # --- REGRESSION GUARD (was a LATENT-BUG WITNESS): a partial dispute SMALLER
  #     than a single lot's consumer cost must recover only the operator's PRO-RATA
  #     share (gross * disputed/cost), never the whole lot. The old code clawed the
  #     entire lot and booked NO platform loss, violating conservation (clawed >
  #     disputed). That is now FIXED in store.ChargebackLineage (pro-rata frac on the
  #     overshoot lot; the lot is kept with its gross reduced) and locked here + in
  #     internal/store/chargeback_parity_test.go (TestChargebackPartialProRataParity):
  #     a 100 dispute on a 200-cost / 140-gross lot claws 70, books 30 platform loss.
  Scenario: Dispute smaller than a single lot's cost must not over-claw the operator
    Given wallet "alice" has 1000.00 in real credits
    And node "n1" is owned by account "op1"
    And alice has a single settled request "r_big" of cost 200.00 on node "n1" with owner share 140.00
    When a wallet-recency dispute "dp_under_one_lot" of 100.00 is opened on alice
    Then the amount recovered from operator "op1" must not exceed 100.00
    And clawed plus reversed plus platform loss equals 100.00
    And alice's balance is reduced by exactly 100.00
