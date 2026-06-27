# Spec-only behavior contract for money idempotency and concurrency safety.
# Ground truth:
#   cmd/rogerai-broker/billing.go    - webhook(): CreditOnce("stripe:"+sessionID), dispute handling
#   internal/store/store.go          - MarkProcessed (processed set), CreditOnce, Hold,
#                                      ChargebackLineage (disputes map), SeedOnce
#   cmd/rogerai-broker/strikes.go    - strike(idemKey) -> OwnerStrike(...,idemKey)
#   internal/store/ledger.go         - PendingReversal key "reverse:<disputeID>:<lotID>"
#
# Invariants pinned:
#   * Stripe events credit a wallet exactly once (CreditOnce is mark+add in one txn).
#   * A chargeback is idempotent on the Stripe dispute id (the disputes set).
#   * A retried request never double-strikes an owner (the strike idempotency key).
#   * Concurrent holds never overdraw: exactly floor(balance/amount) parallel holds succeed.
#
# NO step definitions and NO Go. Spec only.

@money @idempotency @concurrency
Feature: Money events are idempotent and concurrency-safe

  Background:
    Given a fresh store
    And node "n1" is owned by account "op1"

  # ----------------------------------------------------------------------------
  # STRIPE TOP-UP IDEMPOTENCY (CreditOnce / MarkProcessed).
  # ----------------------------------------------------------------------------

  Rule: A Stripe checkout session credits the wallet exactly once

    Scenario: First delivery of a completed session credits the wallet
      Given wallet "alice" has 0.00 credits
      When the Stripe event "checkout.session.completed" for session "cs_1" credits 10.00 to "alice"
      Then the credit is applied
      And alice's balance is 10.00

    Scenario: A duplicate redelivery of the same session does NOT double-credit
      Given wallet "alice" has 0.00 credits
      When the Stripe event for session "cs_1" credits 10.00 to "alice"
      And the Stripe event for session "cs_1" is delivered again
      Then the second delivery is recognized as already-processed
      And alice's balance is still 10.00
      And exactly one topup ledger row exists for "stripe:cs_1"

    Scenario: Distinct sessions each credit once
      Given wallet "alice" has 0.00 credits
      When session "cs_1" credits 10.00 and session "cs_2" credits 5.00 to "alice"
      Then alice's balance is 15.00

    Scenario: MarkProcessed returns true the first time and false on every repeat
      Given the idempotency key "stripe:cs_9" has never been seen
      When MarkProcessed is called with "stripe:cs_9"
      Then it returns true
      When MarkProcessed is called with "stripe:cs_9" again
      Then it returns false

    Scenario: Credits derive from amount_total, not caller metadata
      Given wallet "alice" has 0.00 credits
      And a session "cs_3" whose amount_total is 10.00 but whose metadata claims 1000.00 credits
      When the session is processed
      Then alice is credited 10.00 from amount_total
      And the metadata divergence is logged but not trusted

    Scenario Outline: CreditOnce dedup across redeliveries
      Given wallet "<wallet>" has <start> credits
      When session "<key>" crediting <amount> is delivered <deliveries> times
      Then "<wallet>" balance is <final>

      Examples:
        | wallet | start | key   | amount | deliveries | final |
        | bob    | 0.00  | cs_a  | 10.00  | 1          | 10.00 |
        | bob    | 0.00  | cs_a  | 10.00  | 2          | 10.00 |
        | bob    | 0.00  | cs_a  | 10.00  | 5          | 10.00 |
        | bob    | 50.00 | cs_b  | 10.00  | 3          | 60.00 |

  # ----------------------------------------------------------------------------
  # CHARGEBACK IDEMPOTENCY (ChargebackLineage on the dispute id).
  # ----------------------------------------------------------------------------

  Rule: A chargeback is idempotent on the Stripe dispute id

    Scenario: A redelivered dispute claws back nothing the second time
      Given wallet "alice" has 100.00 real credits
      And alice has one settled request of cost 50.00 with owner share 35.00
      When a chargeback of 50.00 with dispute id "dp_1" is processed
      And the same dispute "dp_1" is delivered again
      Then the second delivery is marked already-handled
      And the second delivery claws back 0.00
      And alice's balance is unchanged by the redelivery
      And the operator is debited only once

    Scenario: Two DISTINCT disputes are each processed once
      Given wallet "alice" has 200.00 real credits
      And alice has two settled requests of cost 50.00 each with owner share 35.00 each
      When a chargeback "dp_1" of 50.00 and a chargeback "dp_2" of 50.00 are processed
      Then both are applied
      And 100.00 total is removed from alice's wallet

    Scenario Outline: Dispute redelivery count never changes the clawed total
      Given wallet "alice" has 100.00 real credits
      And alice has one settled request of cost 50.00 with owner share 35.00
      When dispute "<id>" of 50.00 is delivered <times> times
      Then the operator is clawed 35.00 in total
      And the platform loss is 15.00 in total

      Examples:
        | id   | times |
        | dp_x | 1     |
        | dp_x | 2     |
        | dp_x | 4     |

    Scenario: A paid-lot reversal intent is idempotent on reverse:<dispute>:<lot>
      Given alice has one ALREADY-PAID earning lot of gross 35.00 for owner "op1"
      When a chargeback "dp_1" claws that paid lot
      Then a pending reversal keyed "reverse:dp_1:<lot>" is recorded once
      And a webhook redelivery of "dp_1" does not create a second reversal intent

  # ----------------------------------------------------------------------------
  # STRIKE IDEMPOTENCY (same request id never double-strikes).
  # ----------------------------------------------------------------------------

  Rule: The same request id never strikes an owner twice

    Scenario: A retried empty-output settle strikes the owner only once
      Given a void request with request id "req_1" against owner "op1"
      When the empty-output strike "empty:req_1" is recorded
      And the empty-output strike "empty:req_1" is recorded again
      Then owner "op1" has exactly one empty-output strike
      And the redelivery is a no-op via the idempotency key

    Scenario: Output and input over-report strikes on the same request are distinct keys
      Given a request id "req_2" against owner "op1"
      When a recount-over-report strike "recount:output:req_2" is recorded
      And a recount-over-report strike "recount:input:req_2" is recorded
      Then owner "op1" has two distinct recount strikes
      And re-recording either key adds nothing

    Scenario: An impossible-input strike is idempotent on imposs:<requestID>
      Given a request id "req_3" against owner "op1"
      When the impossible-input strike "imposs:req_3" is recorded twice
      Then exactly one impossible-input strike exists for "req_3"

    Scenario Outline: Strike idempotency keys
      Given owner "op1" with no prior strikes
      When the strike key "<key>" is recorded <times> times
      Then owner "op1" has exactly <count> strike(s) for that key

      Examples:
        | key                 | times | count |
        | empty:r1            | 1     | 1     |
        | empty:r1            | 3     | 1     |
        | recount:output:r1   | 2     | 1     |
        | imposs:r1           | 4     | 1     |

  # ----------------------------------------------------------------------------
  # CONCURRENT HOLDS NEVER OVERDRAW.
  # ----------------------------------------------------------------------------

  Rule: Parallel holds never drive a wallet negative; exactly floor(balance/amount) succeed

    Scenario: A hold larger than the balance is refused and leaves the balance intact
      Given wallet "alice" has 10.00 credits
      When alice places a hold of 100.00
      Then the hold is refused
      And alice's balance is still 10.00

    Scenario: A hold equal to the balance succeeds and zeroes the wallet
      Given wallet "alice" has 10.00 credits
      When alice places a hold of 10.00
      Then the hold succeeds
      And alice's balance is 0.00

    Scenario: 100 concurrent holds of 1.00 on a balance of 10.00 - exactly 10 succeed
      Given wallet "alice" has 10.00 credits
      When 100 holds of 1.00 are placed concurrently
      Then exactly 10 holds succeed
      And exactly 90 holds are refused
      And alice's balance is 0.00
      And the balance never went negative at any point

    Scenario Outline: floor(balance/amount) concurrent holds succeed
      Given wallet "alice" has <balance> credits
      When <attempts> holds of <amount> are placed concurrently
      Then exactly <succeed> holds succeed
      And alice's balance is <remaining>
      And the balance never went negative at any point

      Examples:
        | balance | amount | attempts | succeed | remaining |
        | 10.00   | 1.00   | 100      | 10      | 0.00      |
        | 10.00   | 3.00   | 100      | 3       | 1.00      |
        | 5.00    | 5.00   | 50       | 1       | 0.00      |
        | 5.00    | 6.00   | 50       | 0       | 5.00      |
        | 100.00  | 0.50   | 500      | 200     | 0.00      |
        | 0.00    | 1.00   | 10       | 0       | 0.00      |

    Scenario: A refused hold is captured-free (no ledger money row for a failed hold)
      Given wallet "alice" has 1.00 credits
      When alice places a hold of 2.00
      Then the hold is refused
      And no pending hold ledger row is written for the refused attempt

    Scenario: Concurrent top-up and holds never lose or invent credits (conservation)
      Given wallet "alice" has 10.00 credits
      When 10 holds of 1.00 and one top-up of 5.00 are applied concurrently
      Then the sum of remaining balance, successful holds, and released holds equals the credits in
      And no credits were created or destroyed
