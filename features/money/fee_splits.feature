# The fee split math and its rounding/precision boundaries.
#
# GROUND TRUTH:
#   cost (internal/protocol/protocol.go Cost / CostWith2):
#     cost = (promptTokens*priceIn + completionTokens*priceOut) / 1e6   (prices are $/1M)
#   split (cmd/rogerai-broker/grant.go settleRequest):
#     ownerShare = cost * (1 - feeRate)
#     platform take = cost - ownerShare = cost * feeRate
#     ownerShare + platform take ALWAYS == cost (no credits created/destroyed)
#   display rounding (cmd/rogerai-broker/main.go round6, tunnel.go):
#     round6(f) = floor(f*1e6 + 0.5) / 1e6   (round-half-up to 6 decimals)
#     round6 is applied ONLY to the X-RogerAI-Cost / X-RogerAI-Balance HEADERS.
#     IT IS NOT APPLIED TO THE LEDGER. The wallet is actually debited the FULL-precision
#     cost; a sub-micro-credit charge is debited for real but DISPLAYED as $0. See FINDINGS.
#
# Default fee rate 30%. 1 credit = $1.
Feature: Fee split math always sums back to cost, with documented rounding boundaries

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

  # --- rounding / precision boundary: round6 DISPLAY around 0.0000005 -----
  # The whole-system invariant under test: the LEDGER carries full precision; the
  # COST HEADER is round-half-up to 6 decimals. The two diverge below 0.000001.

  Scenario: REGRESSION - a 34-token completion costs a real but sub-micro amount, DISPLAYED as $0
    Given a model priced at 0.00 per 1M input and 0.01 per 1M output
    When a request uses 0 prompt tokens and 34 completion tokens
    Then the exact cost is 0.00000034 credits
    And the displayed cost header rounds to 0.000000
    But the wallet is actually debited 0.00000034 credits
    And the displayed-vs-billed gap of 0.00000034 is real dust the consumer pays

  Scenario Outline: the round6 display boundary sits at exactly 0.0000005
    Given a model priced at 0.00 per 1M input and 0.01 per 1M output
    When a request uses 0 prompt tokens and <completion> completion tokens
    Then the exact cost is <exact> credits
    And the displayed cost header is <displayed>

    Examples: round-half-up at 6 decimals; 1 token = 0.00000001 credits here
      | completion | exact      | displayed |
      | 1          | 0.00000001 | 0.000000  |
      | 34         | 0.00000034 | 0.000000  |
      | 49         | 0.00000049 | 0.000000  |
      | 50         | 0.00000050 | 0.000001  |
      | 51         | 0.00000051 | 0.000001  |
      | 99         | 0.00000099 | 0.000001  |
      | 100        | 0.00000100 | 0.000001  |
      | 149        | 0.00000149 | 0.000001  |
      | 150        | 0.00000150 | 0.000002  |

  Scenario: round6 rounds exactly half a micro-credit UP, never down (round-half-up)
    Given an exact amount of 0.0000005 credits
    Then round6 of that amount is 0.000001
    Given an exact amount of 0.00000049 credits
    Then round6 of that amount is 0.000000
    Given an exact amount of 0.0000015 credits
    Then round6 of that amount is 0.000002

  Scenario: a request at or above one micro-credit displays its real cost
    Given a model priced at 0.00 per 1M input and 0.01 per 1M output
    When a request uses 0 prompt tokens and 100 completion tokens
    Then the exact cost is 0.000001 credits
    And the displayed cost header is 0.000001
    And the displayed cost equals the billed cost (no divergence at or above 1e-6)

  # --- split + rounding interaction at the dust boundary ------------------

  Scenario: the split of a sub-micro cost is itself sub-micro on both sides (still conserves)
    Given the platform fee rate is 30%
    When a request costs 0.00000034 credits
    Then the owner share is 0.000000238
    And the platform take is 0.000000102
    And the owner share plus the platform take equals 0.00000034
    And both shares display as 0.000000 in the headers

  # --- adversarial: micro-cost requests in aggregate ---------------------

  Scenario: many sub-micro requests accumulate a real (non-zero) charge despite each displaying $0
    Given wallet "alice" has 1.00 in real credits
    And node "n1" is owned by account "op1"
    When alice runs 1000000 requests that each cost 0.00000034 credits
    Then each request displays a cost of 0.000000
    But alice's balance falls by 0.34 in total
    And the aggregate charge is real even though every line item displayed as $0
