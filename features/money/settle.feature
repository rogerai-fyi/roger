# Direct Settle behavior: the atomic "debit consumer, mint operator share, record
# receipt" primitive used by the metering ($0) path AND, for the fee-split math, the
# reference for how every priced capture mints earnings.
#
# GROUND TRUTH (internal/store/store.go Settle + realEarnShare; cmd grant.go):
#   Settle(user, node, cost, ownerShare, rec):
#     wallet -= cost                       (UNCONDITIONAL: no overdraft guard here -
#                                           the guard is the Hold pre-auth; a direct
#                                           Settle CAN drive a wallet negative.)
#     spend  += cost
#     earnShare = realEarnShare(user, cost, ownerShare)   (the EARNED amount)
#     earnings[node] += earnShare
#     records a spend ledger row (-cost) + a receipt Entry carrying earnShare
#   realEarnShare(user, cost, ownerShare):
#     returns 0 immediately when cost <= 0 OR ownerShare <= 0 (and does NOT drain seed)
#     else drains min(cost, seedRemain) from the wallet's seed bucket and scales the
#     owner share by the REAL (non-seed) funded fraction: ownerShare * (cost-seedUsed)/cost
#   ownerShare is computed by the caller as cost * (1 - feeRate); platform take = cost - ownerShare.
#
# 1 credit = $1. Default fee rate 30% -> owner keeps 70%, platform keeps 30%.
Feature: Settle debits the consumer, mints only real-funded earnings, and conserves money

  Background:
    Given a fresh ledger-backed store
    And the platform fee rate is 30%
    And the starter seed grant is 100.00 credits

  # --- real-funded settle mints the full owner share ---------------------

  Scenario: A real-funded request mints the full owner share
    Given wallet "alice" has 10.00 in real credits
    And node "n1" is owned by account "op1"
    When the request settles via Settle with cost 2.00, owner share 1.40
    Then alice's balance is 8.00
    And alice's lifetime spend is 2.00
    And operator "op1" has earned 1.40
    And the platform keeps 0.60

  Scenario Outline: Real-funded settle mints owner share = cost * (1 - fee)
    Given wallet "alice" has 1000.00 in real credits
    And node "n1" is owned by account "op1"
    When the request settles via Settle with cost <cost>, owner share <share>
    Then alice's balance is <after>
    And operator "op1" has earned <share>
    And the platform keeps <platform>
    And the operator share plus the platform take equals the cost <cost>

    Examples: fee rate 30%
      | cost    | share    | platform | after    |
      | 0.00045 | 0.000315 | 0.000135 | 999.99955|
      | 1.00    | 0.70     | 0.30     | 999.00   |
      | 2.50    | 1.75     | 0.75     | 997.50   |
      | 10.00   | 7.00     | 3.00     | 990.00   |
      | 123.456789 | 86.4197523 | 37.0370367 | 876.543211 |

  # --- FREE seed-funded settle mints ZERO earning (P0-1) ------------------

  Scenario: Seed-funded spend mints NO operator earning (the P0-1 invariant)
    Given wallet "seeded" has 100.00 in FREE seed credits
    And node "n1" is owned by account "op1"
    When the request settles via Settle with cost 2.00, owner share 1.40
    Then seeded's balance is 98.00
    And seeded's lifetime spend is 2.00
    And operator "op1" has earned 0.00

  Scenario: REGRESSION - draining an entire seed grant mints exactly zero earnings
    Given wallet "freeloader" has 100.00 in FREE seed credits
    And node "n1" is owned by account "op1"
    When freeloader settles 50 requests of cost 2.00 each (owner share 1.40 each)
    Then freeloader's balance is 0.00
    And freeloader's lifetime spend is 100.00
    And operator "op1" has earned 0.00
    And no payable earning lot was ever created for operator "op1"

  Scenario Outline: No fee rate lets seed credits mint an earning
    Given wallet "seeded" has 100.00 in FREE seed credits
    And the platform fee rate is <fee>
    And node "n1" is owned by account "op1"
    When the request settles via Settle with cost <cost>, owner share <share>
    Then operator "op1" has earned 0.00

    Examples:
      | fee  | cost  | share |
      | 0%   | 5.00  | 5.00  |
      | 25%  | 5.00  | 3.75  |
      | 30%  | 5.00  | 3.50  |
      | 50%  | 5.00  | 2.50  |
      | 100% | 5.00  | 0.00  |

  # --- MIXED seed + real funding mints only the real fraction -------------

  Scenario: A request straddling the seed/real boundary earns only on the real fraction
    Given wallet "mixed" has 1.00 in FREE seed credits and 9.00 in real credits
    And node "n1" is owned by account "op1"
    When the request settles via Settle with cost 4.00, owner share 2.80
    Then mixed's balance is 6.00
    And mixed's lifetime spend is 4.00
    And the seed-funded portion of the cost is 1.00
    And the real-funded portion of the cost is 3.00
    And operator "op1" has earned 2.10
    And the platform keeps 0.90

  Scenario Outline: Mixed funding scales the owner share by the real fraction of cost
    Given wallet "mixed" has <seed> in FREE seed credits and <real> in real credits
    And node "n1" is owned by account "op1"
    When the request settles via Settle with cost <cost>, owner share <share>
    Then operator "op1" has earned <earned>

    Examples: fee 30%, owner share = cost*0.70, earned = share * (cost-seedUsed)/cost
      | seed  | real   | cost  | share | earned |
      | 0.00  | 100.00 | 10.00 | 7.00  | 7.00   |
      | 10.00 | 100.00 | 10.00 | 7.00  | 0.00   |
      | 5.00  | 100.00 | 10.00 | 7.00  | 3.50   |
      | 2.50  | 100.00 | 10.00 | 7.00  | 5.25   |
      | 7.50  | 100.00 | 10.00 | 7.00  | 1.75   |
      | 9.999 | 100.00 | 10.00 | 7.00  | 0.0007 |

  Scenario: Seed is drained BEFORE real, so the first requests earn nothing and later ones earn fully
    Given wallet "mixed" has 6.00 in FREE seed credits and 100.00 in real credits
    And node "n1" is owned by account "op1"
    When the request settles via Settle with cost 4.00, owner share 2.80
    Then operator "op1" has earned 0.00
    When the request settles via Settle with cost 4.00, owner share 2.80
    Then operator "op1" has earned 1.40
    When the request settles via Settle with cost 4.00, owner share 2.80
    Then operator "op1" has earned 4.20

  # --- fee-rate permutations ---------------------------------------------

  Scenario Outline: Owner share across the full fee-rate range, real-funded
    Given wallet "alice" has 1000.00 in real credits
    And the platform fee rate is <fee>
    And node "n1" is owned by account "op1"
    When the request settles via Settle with cost 10.00, owner share <share>
    Then operator "op1" has earned <share>
    And the platform keeps <platform>
    And the operator share plus the platform take equals the cost 10.00

    Examples:
      | fee  | share | platform |
      | 0%   | 10.00 | 0.00     |
      | 25%  | 7.50  | 2.50     |
      | 30%  | 7.00  | 3.00     |
      | 50%  | 5.00  | 5.00     |
      | 100% | 0.00  | 10.00    |

  Scenario: A 0% fee rate gives the operator the entire cost and the platform nothing
    Given wallet "alice" has 100.00 in real credits
    And the platform fee rate is 0%
    And node "n1" is owned by account "op1"
    When the request settles via Settle with cost 10.00, owner share 10.00
    Then operator "op1" has earned 10.00
    And the platform keeps 0.00

  Scenario: A 100% fee rate gives the operator nothing and the platform the entire cost
    Given wallet "alice" has 100.00 in real credits
    And the platform fee rate is 100%
    And node "n1" is owned by account "op1"
    When the request settles via Settle with cost 10.00, owner share 0.00
    Then operator "op1" has earned 0.00
    And the platform keeps 10.00
    And no payable earning lot was created (owner share is zero)

  # --- zero cost ----------------------------------------------------------

  Scenario: A zero-cost settle moves no money and mints no earning but records the receipt
    Given wallet "alice" has 10.00 in real credits
    And node "n1" is owned by account "op1"
    When the request settles via Settle with cost 0.00, owner share 0.00
    Then alice's balance is 10.00
    And alice's lifetime spend is 0.00
    And operator "op1" has earned 0.00
    And a metering receipt is recorded for the request

  Scenario: A zero-cost settle does NOT drain seed credits
    Given wallet "seeded" has 100.00 in FREE seed credits
    And node "n1" is owned by account "op1"
    When the request settles via Settle with cost 0.00, owner share 0.00
    Then seeded's balance is 100.00
    And the remaining seed for seeded is 100.00

  # --- owner-share conservation ------------------------------------------

  Scenario Outline: Consumer cost always equals operator earning plus platform take (real-funded)
    Given wallet "alice" has 100000.00 in real credits
    And the platform fee rate is <fee>
    And node "n1" is owned by account "op1"
    When the request settles via Settle with cost <cost>, owner share <share>
    Then the operator share plus the platform take equals the cost <cost>
    And no credits were created or destroyed in the split

    Examples:
      | fee  | cost        | share        |
      | 0%   | 0.00        | 0.00         |
      | 30%  | 0.00        | 0.00         |
      | 30%  | 0.00045     | 0.000315     |
      | 30%  | 1.00        | 0.70         |
      | 30%  | 2.50        | 1.75         |
      | 30%  | 123.456789  | 86.4197523   |
      | 25%  | 999.99      | 749.9925     |
      | 50%  | 0.01        | 0.005        |
      | 100% | 7.77        | 0.00         |

  # --- adversarial: no overdraft guard on direct Settle ------------------
  # Settle is unconditional; the negative-balance guard is the Hold pre-auth that
  # precedes a priced capture. A DIRECT Settle for more than the balance WILL drive
  # the wallet negative. Documented so a future caller never settles unheld. See FINDINGS.

  Scenario: A direct Settle is not overdraft-guarded and can drive a wallet negative
    Given wallet "alice" has 1.00 in real credits
    And node "n1" is owned by account "op1"
    When the request settles via Settle with cost 5.00, owner share 3.50
    Then alice's balance is -4.00
    And this is only safe because the production path always Holds before it Settles
