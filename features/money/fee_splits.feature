# The fee split math and its cost-DISPLAY / rounding boundaries.
#
# GROUND TRUTH:
#   cost (internal/protocol/protocol.go UsageReceipt.Cost / CostWith2):
#     cost = (promptTokens*priceIn + completionTokens*priceOut) / 1e6   (prices are $/1M)
#   split (cmd/rogerai-broker/grant.go settleRequest):
#     ownerShare = cost * (1 - feeRate); platform take = cost - ownerShare
#     ownerShare + platform take ALWAYS == cost (no credits created/destroyed)
#   COST DISPLAY (cmd/rogerai-broker/httputil.go fmtCostHeader + tunnel.go:1459):
#     X-RogerAI-Cost = fmtCostHeader(cost): the EXACT cost at 6 SIGNIFICANT figures, re-emitted
#     as a plain decimal (never scientific, never a bare "0" for a paid turn). This REPLACED an
#     earlier round6(cost) that floored a real sub-microcredit charge to "0" and made a PAID turn
#     read "$0.00" as if it were free. The REGRESSION scenario below pins that the dust is shown,
#     not hidden, and never silently re-collapses.
#   round6 (cmd/rogerai-broker/main.go) round-half-up to 6 decimals STILL governs the
#     X-RogerAI-Balance header + the money JSON fields (balance/spend/earnings/...), NOT the cost
#     header. The wallet + ledger always settle at FULL precision regardless of display.
#
# Default fee rate 30%. 1 credit = $1.
# EXECUTABLE: cmd/rogerai-broker/fee_splits_bdd_test.go drives the real
# Cost / round6 / fmtCostHeader / Settle. This spec was corrected (cost-display section) after
# wiring it to godog exposed it still documented the removed round6-collapses-to-$0 behavior.
Feature: Fee split math always sums back to cost, with the exact cost shown to the client

  Background:
    Given a fresh ledger-backed store
    And the starter seed grant is 100.00 credits

  # --- split conservation across fee rates x costs ------------------------

  Scenario Outline: owner share + platform take always equals cost
    Given the platform fee rate is <fee>
    When a request costs <cost> credits
    Then the owner share is <share>
    And the platform take is <platform>
    And the owner share plus the platform take equals <cost>
    And neither share is negative

    Examples: fee 0%
      | fee | cost       | share      | platform |
      | 0%  | 0.00       | 0.00       | 0.00     |
      | 0%  | 0.00045    | 0.00045    | 0.00     |
      | 0%  | 1.00       | 1.00       | 0.00     |
      | 0%  | 123.456789 | 123.456789 | 0.00     |

    Examples: fee 25%
      | fee | cost       | share       | platform    |
      | 25% | 0.00       | 0.00        | 0.00        |
      | 25% | 0.00045    | 0.0003375   | 0.0001125   |
      | 25% | 1.00       | 0.75        | 0.25        |
      | 25% | 2.50       | 1.875       | 0.625       |
      | 25% | 123.456789 | 92.59259175 | 30.86419725 |

    Examples: fee 30% (the default)
      | fee | cost       | share       | platform    |
      | 30% | 0.00       | 0.00        | 0.00        |
      | 30% | 0.00045    | 0.000315    | 0.000135    |
      | 30% | 1.00       | 0.70        | 0.30        |
      | 30% | 2.50       | 1.75        | 0.75        |
      | 30% | 123.456789 | 86.4197523  | 37.0370367  |

    Examples: fee 50%
      | fee | cost       | share      | platform   |
      | 50% | 0.00       | 0.00       | 0.00       |
      | 50% | 0.00045    | 0.000225   | 0.000225   |
      | 50% | 1.00       | 0.50       | 0.50       |
      | 50% | 2.50       | 1.25       | 1.25       |
      | 50% | 123.456789 | 61.7283945 | 61.7283945 |

    Examples: fee 100%
      | fee  | cost       | share | platform   |
      | 100% | 0.00       | 0.00  | 0.00       |
      | 100% | 0.00045    | 0.00  | 0.00045    |
      | 100% | 1.00       | 0.00  | 1.00       |
      | 100% | 123.456789 | 0.00  | 123.456789 |

  # --- cost from tokens ---------------------------------------------------

  Scenario Outline: cost = (prompt*priceIn + completion*priceOut) / 1e6
    Given a model priced at <priceIn> per 1M input and <priceOut> per 1M output
    When a request uses <prompt> prompt tokens and <completion> completion tokens
    Then the cost is <cost>

    Examples:
      | priceIn | priceOut | prompt    | completion | cost      |
      | 0.00    | 0.00     | 1000      | 5000       | 0.00      |
      | 0.20    | 0.50     | 1000      | 500        | 0.00045   |
      | 0.00    | 0.30     | 0         | 800        | 0.00024   |
      | 0.10    | 0.00     | 2000      | 0          | 0.0002    |
      | 1.00    | 1.00     | 1000000   | 1000000    | 2.00      |
      | 0.15    | 0.60     | 3500000   | 1250000    | 1.275     |
      | 0.01    | 0.01     | 1         | 1          | 0.00000002|

  # --- COST DISPLAY: the exact value reaches the client (never a bare $0) --
  # REGRESSION (the divergence wiring this to godog caught): the cost header once round6'd to 6
  # decimals, collapsing a real sub-microcredit charge to "0" so a PAID turn read $0.00. The
  # header now sends the EXACT value via fmtCostHeader. This pins that the dust is shown.

  Scenario: REGRESSION - a 34-token completion costs a real sub-micro amount, DISPLAYED in full (not $0)
    Given a model priced at 0.00 per 1M input and 0.01 per 1M output
    When a request uses 0 prompt tokens and 34 completion tokens
    Then the exact cost is 0.00000034 credits
    And the displayed cost header is 0.00000034
    And the wallet is actually debited 0.00000034 credits
    And the displayed cost equals the billed cost (no $0 collapse, no dust hidden)

  Scenario Outline: the cost header shows the exact cost at 6 significant figures (no $0 collapse)
    Given a model priced at 0.00 per 1M input and 0.01 per 1M output
    When a request uses 0 prompt tokens and <completion> completion tokens
    Then the exact cost is <exact> credits
    And the displayed cost header is <displayed>

    Examples: 1 token = 0.00000001 credits here; fmtCostHeader emits the exact decimal, never "0"
      | completion | exact      | displayed  |
      | 1          | 0.00000001 | 0.00000001 |
      | 34         | 0.00000034 | 0.00000034 |
      | 49         | 0.00000049 | 0.00000049 |
      | 50         | 0.00000050 | 0.0000005  |
      | 51         | 0.00000051 | 0.00000051 |
      | 99         | 0.00000099 | 0.00000099 |
      | 100        | 0.00000100 | 0.000001   |
      | 149        | 0.00000149 | 0.00000149 |
      | 150        | 0.00000150 | 0.0000015  |

  Scenario: the displayed cost equals the billed cost even below a micro-credit (the fix)
    Given a model priced at 0.00 per 1M input and 0.01 per 1M output
    When a request uses 0 prompt tokens and 36 completion tokens
    Then the exact cost is 0.00000036 credits
    And the displayed cost header is 0.00000036
    And the displayed cost equals the billed cost (no $0 collapse, no dust hidden)

  # --- round6 STILL governs the BALANCE header + money JSON (round-half-up, 6 decimals) ----

  Scenario: round6 rounds exactly half a micro-credit UP, never down (Balance header + JSON)
    Given an exact amount of 0.0000005 credits
    Then round6 of that amount is 0.000001
    Given an exact amount of 0.00000049 credits
    Then round6 of that amount is 0.000000
    Given an exact amount of 0.0000015 credits
    Then round6 of that amount is 0.000002

  # --- split + display interaction at the dust boundary -------------------

  Scenario: the split of a sub-micro cost conserves at full precision (shares round6 to 0 in JSON)
    Given the platform fee rate is 30%
    When a request costs 0.00000034 credits
    Then the owner share is 0.000000238
    And the platform take is 0.000000102
    And the owner share plus the platform take equals 0.00000034
    And the owner share displays as 0.000000 in the earnings JSON
    And the platform take displays as 0.000000 in the earnings JSON

  # --- adversarial: micro-cost requests in aggregate ----------------------

  Scenario: many sub-micro requests accumulate a real charge, and each line item now shows its true cost
    Given wallet "alice" has 1.00 in real credits
    And node "n1" is owned by account "op1"
    When alice runs 1000000 requests that each cost 0.00000034 credits
    Then each request displays a cost of 0.00000034
    And alice's balance falls by 0.34 in total
    And the aggregate charge is real and each line item now shows its true cost
