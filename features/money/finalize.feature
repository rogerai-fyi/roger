# Hold -> Finalize capture: conservation of money across the authorize-then-capture
# flow. After a Hold of H and a Finalize at cost C, the wallet must be down by EXACTLY
# C (the unused H-C is refunded), the operator must earn EXACTLY the real-funded owner
# share, and no credits may be created or destroyed.
#
# GROUND TRUTH (internal/store/store.go Finalize; cmd tunnel.go + grant.go):
#   Finalize(user, node, held, cost, ownerShare, rec):
#     wallet += held - cost           (refund the unused reservation; net of Hold+Finalize
#                                       on the wallet is -cost)
#     spend  += cost
#     earnShare = realEarnShare(user, cost, ownerShare)   (seed-aware; see settle.feature)
#     earnings[node] += earnShare
#     ledger: HoldRelease(+held) then Spend(-cost); earn lot for earnShare
#   THE COST<=HELD CLAMP LIVES IN THE CALLER (tunnel.go): cost = min(cost, maxCost)
#     BEFORE Finalize is called. The Finalize primitive itself does NOT clamp; if handed
#     cost > held it will subtract the excess from the wallet (held-cost goes negative).
#   Finalize IS idempotent on rec.RequestID: the settled-map guard makes a repeat capture of the
#     SAME request a no-op (no double refund / lot drift). With an EMPTY request id the primitive
#     does not dedupe (it trusts the caller to pass a request id). [Corrected after godog wiring
#     showed the deployed settled-map guard; the spec had documented the pre-guard behavior.]
#
# 1 credit = $1. Default fee 30% -> owner share = cost * 0.70.
Feature: Finalize captures a held reservation and conserves money exactly

  Background:
    Given a fresh ledger-backed store
    And the platform fee rate is 30%
    And the starter seed grant is 100.00 credits

  # --- conservation: wallet down by exactly cost --------------------------

  Scenario: Hold then Finalize debits exactly the cost and refunds the rest
    Given wallet "alice" has 10.00 in real credits
    And node "n1" is owned by account "op1"
    When alice places a hold of 5.00
    And the request settles via Finalize with hold 5.00, cost 2.00, owner share 1.40
    Then alice's balance is 8.00
    And alice's balance fell by exactly the cost 2.00 across the hold-and-capture
    And alice's lifetime spend is 2.00
    And operator "op1" has earned 1.40
    And the platform keeps 0.60
    And no credits were created or destroyed

  Scenario Outline: The net wallet change over Hold+Finalize is always exactly minus the cost
    Given wallet "alice" has <balance> in real credits
    And node "n1" is owned by account "op1"
    When alice places a hold of <hold>
    And the request settles via Finalize with hold <hold>, cost <cost>, owner share <share>
    Then alice's balance is <after>
    And the unused hold of <refund> was refunded
    And operator "op1" has earned <share>
    And the operator share plus the platform take equals the cost <cost>

    Examples: fee 30%, hold >= cost (the production-clamped regime)
      | balance | hold   | cost    | share    | refund  | after     |
      | 10.00   | 5.00   | 0.00    | 0.00     | 5.00    | 10.00     |
      | 10.00   | 5.00   | 1.00    | 0.70     | 4.00    | 9.00      |
      | 10.00   | 5.00   | 2.50    | 1.75     | 2.50    | 7.50      |
      | 10.00   | 5.00   | 5.00    | 3.50     | 0.00    | 5.00      |
      | 100.00  | 80.00  | 0.00045 | 0.000315 | 79.99955| 99.99955  |
      | 50.00   | 50.00  | 49.999  | 34.9993  | 0.001   | 0.001     |

  # --- cost == hold (full capture, no refund) -----------------------------

  Scenario: Finalizing at exactly the held amount captures everything and refunds nothing
    Given wallet "alice" has 10.00 in real credits
    And node "n1" is owned by account "op1"
    When alice places a hold of 4.00
    And the request settles via Finalize with hold 4.00, cost 4.00, owner share 2.80
    Then alice's balance is 6.00
    And the unused hold of 0.00 was refunded
    And operator "op1" has earned 2.80

  # --- zero cost (full hold refunded, no earning) -------------------------

  Scenario: A zero-cost Finalize refunds the entire hold and mints no earning
    Given wallet "alice" has 10.00 in real credits
    And node "n1" is owned by account "op1"
    When alice places a hold of 5.00
    And the request settles via Finalize with hold 5.00, cost 0.00, owner share 0.00
    Then alice's balance is 10.00
    And alice's lifetime spend is 0.00
    And operator "op1" has earned 0.00
    And a metering receipt is recorded for the request

  Scenario: A zero-cost Finalize does not drain seed credits
    Given wallet "seeded" has 100.00 in FREE seed credits
    And node "n1" is owned by account "op1"
    When seeded places a hold of 5.00
    And the request settles via Finalize with hold 5.00, cost 0.00, owner share 0.00
    Then seeded's balance is 100.00
    And the remaining seed for seeded is 100.00

  # --- seed-aware capture (P0-1 across the hold path) ---------------------

  Scenario: A seed-funded capture refunds the hold but mints zero earning
    Given wallet "seeded" has 100.00 in FREE seed credits
    And node "n1" is owned by account "op1"
    When seeded places a hold of 5.00
    And the request settles via Finalize with hold 5.00, cost 3.00, owner share 2.10
    Then seeded's balance is 97.00
    And seeded's lifetime spend is 3.00
    And operator "op1" has earned 0.00

  Scenario Outline: Mixed-funded capture earns only on the real fraction
    Given wallet "mixed" has <seed> in FREE seed credits and <real> in real credits
    And node "n1" is owned by account "op1"
    When mixed places a hold of <hold>
    And the request settles via Finalize with hold <hold>, cost <cost>, owner share <share>
    Then operator "op1" has earned <earned>

    Examples: fee 30%, earned = share * (cost-min(cost,seed))/cost
      | seed  | real   | hold  | cost  | share | earned |
      | 0.00  | 100.00 | 10.00 | 10.00 | 7.00  | 7.00   |
      | 10.00 | 100.00 | 10.00 | 10.00 | 7.00  | 0.00   |
      | 4.00  | 100.00 | 10.00 | 10.00 | 7.00  | 4.20   |
      | 6.00  | 100.00 | 8.00  | 8.00  | 5.60  | 1.40   |

  # --- cost > hold: the clamp is the caller's job -------------------------

  Scenario: The broker clamps cost to the held amount before Finalize, so over-cost is never captured
    Given wallet "alice" has 10.00 in real credits
    And node "n1" is owned by account "op1"
    And a priced request holds 3.00 of alice's credits
    When the node reports a cost of 5.00 that exceeds the 3.00 hold
    Then the broker caps the captured cost at 3.00
    And the request settles via Finalize with hold 3.00, cost 3.00, owner share 2.10
    And alice's balance is 7.00
    And alice is never charged more than was authorized

  Scenario: The Finalize primitive itself does NOT clamp - cost over hold subtracts the excess (caller-guarded)
    Given wallet "alice" has 10.00 in real credits
    And node "n1" is owned by account "op1"
    When alice places a hold of 3.00
    And Finalize is called directly with hold 3.00, cost 5.00, owner share 3.50
    Then alice's balance is 5.00
    And the 2.00 over-capture is only prevented by the broker's min(cost, hold) clamp

  # --- double finalize (idempotent on the request id) --------------------
  # The settled-map guard makes Finalize idempotent on rec.RequestID: a repeat capture of the
  # SAME request is a no-op (no double refund / lot drift). The production caller also guarantees
  # one capture per request via its own `settled` flag. [Section corrected after godog wiring.]

  Scenario: A single Finalize is the supported path - the broker captures each request once
    Given wallet "alice" has 10.00 in real credits
    And node "n1" is owned by account "op1"
    And a priced request holds 5.00 of alice's credits
    When the request is captured via Finalize with cost 2.00 and owner share 1.40
    And the request completes
    Then Finalize was called exactly once
    And alice's balance is 8.00
    And operator "op1" has earned 1.40

  Scenario: REGRESSION - Finalize is idempotent on the request id; a second capture is a no-op
    Given wallet "alice" has 10.00 in real credits
    And node "n1" is owned by account "op1"
    When alice places a hold of 5.00
    And Finalize is called with hold 5.00, cost 2.00, owner share 1.40
    Then alice's balance is 8.00
    When Finalize is called a second time with hold 5.00, cost 2.00, owner share 1.40
    Then alice's balance is still 8.00
    And the second capture was a no-op (the settled-map guard prevents double-refund / lot drift)
    And the spend ledger row is recorded only once (idem key "spend:<request>")
    And operator "op1" has earned 1.40

  # --- no credits created or destroyed across a fleet of captures ---------

  Scenario: Across many captures, total consumer spend equals total operator earnings plus total platform take
    Given wallet "alice" has 1000.00 in real credits
    And node "n1" is owned by account "op1"
    When alice runs 100 held requests of cost 1.00 each (owner share 0.70 each)
    Then alice's balance is 900.00
    And alice's lifetime spend is 100.00
    And operator "op1" has earned 70.00
    And the platform keeps 30.00
    And total spend equals operator earnings plus platform take
