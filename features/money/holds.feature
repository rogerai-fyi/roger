# Hold (authorize-then-capture pre-authorization) behavior.
#
# Spec-first target: store.Hold / store.ReleaseHold and the broker's deferred
# single-release guard around every priced relay/stream request. These scenarios
# pin the invariant the whole money system rests on: a wallet can NEVER go negative
# through the hold path, and a refused hold leaves the balance untouched.
#
# GROUND TRUTH (internal/store/store.go):
#   Hold(user, amount):        refused iff wallet < amount; else wallet -= amount,
#                              pending ledger row (-amount). Note: the guard is
#                              strictly-less, so an EXACT-balance hold SUCCEEDS.
#   ReleaseHold(user, held):   wallet += held UNCONDITIONALLY (the primitive trusts
#                              the caller to release each hold at most once, with the
#                              originally-held amount). The broker enforces that via a
#                              `settled` flag + a single deferred ReleaseHold.
#   estimateMaxCost floors the hold at 1e-6, so a real hold is always >= 0.000001.
#
# 1 credit = $1 by default.
Feature: Hold pre-authorization never overdraws and always refunds cleanly

  Background:
    Given a fresh ledger-backed store
    And the platform fee rate is 30%
    And the starter seed grant is 100.00 credits

  # --- overdraft guard ----------------------------------------------------

  Scenario: A hold larger than the balance is refused and changes nothing
    Given wallet "alice" has 10.00 in real credits
    When alice places a hold of 100.00
    Then the hold is refused
    And alice's balance is still 10.00
    And no pending hold row is recorded for alice

  Scenario: A hold one micro-credit over the balance is refused
    Given wallet "alice" has 10.00 in real credits
    When alice places a hold of 10.000001
    Then the hold is refused
    And alice's balance is still 10.00

  Scenario Outline: Overdraft guard across balances and requested holds
    Given wallet "alice" has <balance> in real credits
    When alice places a hold of <hold>
    Then the hold is <outcome>
    And alice's balance is <after>

    Examples: refused holds leave the balance whole
      | balance | hold      | outcome  | after   |
      | 0.00    | 0.000001  | refused  | 0.00    |
      | 0.00    | 100.00    | refused  | 0.00    |
      | 5.00    | 5.000001  | refused  | 5.00    |
      | 10.00   | 10.01     | refused  | 10.00   |
      | 100.00  | 100.01    | refused  | 100.00  |
      | 0.50    | 1.00      | refused  | 0.50    |

    Examples: holds within balance succeed and debit exactly
      | balance | hold      | outcome   | after    |
      | 10.00   | 0.000001  | succeeds  | 9.999999 |
      | 10.00   | 1.00      | succeeds  | 9.00     |
      | 10.00   | 9.99      | succeeds  | 0.01     |
      | 100.00  | 25.00     | succeeds  | 75.00    |
      | 0.50    | 0.50      | succeeds  | 0.00     |

  # --- exact-balance boundary --------------------------------------------

  Scenario: A hold for the EXACT balance succeeds and drains the wallet to zero
    Given wallet "alice" has 10.00 in real credits
    When alice places a hold of 10.00
    Then the hold succeeds
    And alice's balance is 0.00

  Scenario: After an exact-balance hold, any further hold is refused
    Given wallet "alice" has 10.00 in real credits
    When alice places a hold of 10.00
    Then the hold succeeds
    When alice places a hold of 0.000001
    Then the hold is refused
    And alice's balance is still 0.00

  # --- zero / near-zero ---------------------------------------------------

  Scenario: A zero-amount hold always succeeds and leaves the balance unchanged
    Given wallet "alice" has 10.00 in real credits
    When alice places a hold of 0.00
    Then the hold succeeds
    And alice's balance is still 10.00

  Scenario: A zero-amount hold on an empty wallet still succeeds
    Given wallet "broke" has 0.00 in real credits
    When broke places a hold of 0.00
    Then the hold succeeds
    And broke's balance is still 0.00

  Scenario: The smallest representable hold (1e-6) on a wallet that can cover it
    Given wallet "alice" has 0.000001 in real credits
    When alice places a hold of 0.000001
    Then the hold succeeds
    And alice's balance is 0.00

  Scenario: The smallest representable hold on an empty wallet is refused
    Given wallet "broke" has 0.00 in real credits
    When broke places a hold of 0.000001
    Then the hold is refused
    And broke's balance is still 0.00

  # --- many sequential holds draining to zero -----------------------------

  Scenario: Sequential holds drain the wallet to exactly zero, then the next is refused
    Given wallet "alice" has 10.00 in real credits
    When alice places a hold of 4.00
    Then the hold succeeds
    And alice's balance is 6.00
    When alice places a hold of 4.00
    Then the hold succeeds
    And alice's balance is 2.00
    When alice places a hold of 2.00
    Then the hold succeeds
    And alice's balance is 0.00
    When alice places a hold of 0.01
    Then the hold is refused
    And alice's balance is still 0.00

  Scenario: One thousand one-credit holds against a 1000-credit wallet all succeed; the 1001st is refused
    Given wallet "whale" has 1000.00 in real credits
    When whale places 1000 sequential holds of 1.00
    Then all 1000 holds succeed
    And whale's balance is 0.00
    When whale places a hold of 1.00
    Then the hold is refused
    And whale's balance is still 0.00

  Scenario Outline: Draining a wallet in uneven steps never overdraws
    Given wallet "alice" has 10.00 in real credits
    When alice places sequential holds of <steps>
    Then the holds that fit succeed in order and the rest are refused
    And alice's balance is <after>
    And alice's balance is never negative at any point

    Examples:
      | steps                  | after |
      | 3.00, 3.00, 3.00, 3.00 | 1.00  |
      | 7.50, 2.50, 0.01       | 0.00  |
      | 9.99, 0.01, 0.01       | 0.00  |
      | 10.00, 0.01            | 0.00  |

  # --- concurrency: never overdraw ---------------------------------------

  Scenario: Two concurrent holds that together exceed the balance: at most enough succeed to not overdraw
    Given wallet "alice" has 10.00 in real credits
    When alice places two concurrent holds of 7.00 each
    Then exactly one hold succeeds
    And the other hold is refused
    And alice's balance is 3.00
    And alice's balance is never negative at any point

  Scenario: Many concurrent holds against a small balance never drive it negative
    Given wallet "alice" has 10.00 in real credits
    When 100 concurrent holds of 1.00 are placed against alice
    Then exactly 10 holds succeed
    And 90 holds are refused
    And alice's balance is 0.00
    And alice's balance is never negative at any point

  Scenario Outline: Concurrent holds settle deterministically on the total, never overdrawing
    Given wallet "alice" has <balance> in real credits
    When <count> concurrent holds of <each> each are placed against alice
    Then exactly <succeed> holds succeed
    And alice's balance is <after>
    And alice's balance is never negative at any point

    Examples:
      | balance | count | each  | succeed | after |
      | 10.00   | 20    | 1.00  | 10      | 0.00  |
      | 5.00    | 10    | 2.00  | 2       | 1.00  |
      | 100.00  | 50    | 3.00  | 33      | 1.00  |
      | 1.00    | 1000  | 0.01  | 100     | 0.00  |

  # --- ReleaseHold: full refund ------------------------------------------

  Scenario: Releasing a hold returns the full reservation to the wallet
    Given wallet "alice" has 10.00 in real credits
    When alice places a hold of 6.00
    Then alice's balance is 4.00
    When alice's hold of 6.00 is released
    Then alice's balance is 10.00

  Scenario: Hold then release leaves the wallet exactly as it began
    Given wallet "alice" has 42.50 in real credits
    When alice places a hold of 42.50
    Then alice's balance is 0.00
    When alice's hold of 42.50 is released
    Then alice's balance is 42.50
    And no credits were created or destroyed across the hold-and-release

  Scenario Outline: Release always restores the held amount exactly
    Given wallet "alice" has <balance> in real credits
    When alice places a hold of <hold>
    And alice's hold of <hold> is released
    Then alice's balance is <balance>

    Examples:
      | balance | hold     |
      | 10.00   | 0.000001 |
      | 10.00   | 1.00     |
      | 10.00   | 10.00    |
      | 100.00  | 33.33    |

  # --- double release / over release (caller-trust primitive) ------------
  # ReleaseHold is unconditional: it has no record of an outstanding hold, so it
  # refunds whatever amount it is handed. These scenarios document the REAL
  # primitive behavior the broker layer must protect against with its single-release
  # guard. They are NOT a desired money outcome; see FINDINGS.

  Scenario: Double-releasing the same hold over-refunds the wallet (primitive trusts its caller)
    Given wallet "alice" has 10.00 in real credits
    When alice places a hold of 6.00
    Then alice's balance is 4.00
    When alice's hold of 6.00 is released
    Then alice's balance is 10.00
    When alice's hold of 6.00 is released a second time
    Then alice's balance is 16.00
    And the over-refund of 6.00 is a money invariant violation that the caller must prevent

  Scenario: The broker's deferred single-release guard ensures a hold is released at most once
    Given wallet "alice" has 10.00 in real credits
    And a priced request holds 6.00 of alice's credits
    When the request fails before capture
    Then the broker releases the hold exactly once
    And alice's balance is 10.00

  Scenario: The broker never releases a hold that was already captured by Finalize
    Given wallet "alice" has 10.00 in real credits
    And node "n1" is owned by account "op1"
    And a priced request holds 6.00 of alice's credits
    When the request is captured via Finalize with cost 2.00 and owner share 1.40
    Then the broker does NOT also release the hold
    And alice's balance is 8.00

  Scenario: Releasing MORE than was held over-credits the wallet (primitive trusts its caller)
    Given wallet "alice" has 10.00 in real credits
    When alice places a hold of 6.00
    Then alice's balance is 4.00
    When alice's hold of 9.00 is released
    Then alice's balance is 13.00
    And the 3.00 excess is a money invariant violation that the caller must prevent

  # --- hold then partial finalize refunds the remainder -------------------

  Scenario: Finalizing a hold for less than was held refunds the unused remainder
    Given wallet "alice" has 10.00 in real credits
    And node "n1" is owned by account "op1"
    When alice places a hold of 5.00
    Then alice's balance is 5.00
    When the request settles via Finalize with hold 5.00, cost 2.00, owner share 1.40
    Then alice's balance is 8.00
    And the refunded remainder is 3.00
    And operator "op1" has earned 1.40

  Scenario Outline: Partial finalize always refunds hold-minus-cost
    Given wallet "alice" has <balance> in real credits
    And node "n1" is owned by account "op1"
    When alice places a hold of <hold>
    And the request settles via Finalize with hold <hold>, cost <cost>, owner share <share>
    Then alice's balance is <after>
    And the refunded remainder is <refund>
    And operator "op1" has earned <share>

    Examples: fee rate 30%, owner share = cost * 0.70
      | balance | hold   | cost  | share  | refund | after   |
      | 10.00   | 5.00   | 0.00  | 0.00   | 5.00   | 10.00   |
      | 10.00   | 5.00   | 1.00  | 0.70   | 4.00   | 9.00    |
      | 10.00   | 5.00   | 2.50  | 1.75   | 2.50   | 7.50    |
      | 10.00   | 5.00   | 5.00  | 3.50   | 0.00   | 5.00    |
      | 100.00  | 50.00  | 12.34 | 8.638  | 37.66  | 87.66   |

  Scenario: Finalizing for the full held amount refunds nothing (full capture)
    Given wallet "alice" has 10.00 in real credits
    And node "n1" is owned by account "op1"
    When alice places a hold of 5.00
    And the request settles via Finalize with hold 5.00, cost 5.00, owner share 3.50
    Then alice's balance is 5.00
    And the refunded remainder is 0.00
    And operator "op1" has earned 3.50

  Scenario: A hold consumed by a zero-cost finalize is refunded in full and mints no earning
    Given wallet "alice" has 10.00 in real credits
    And node "n1" is owned by account "op1"
    When alice places a hold of 5.00
    And the request settles via Finalize with hold 5.00, cost 0.00, owner share 0.00
    Then alice's balance is 10.00
    And operator "op1" has earned 0.00
