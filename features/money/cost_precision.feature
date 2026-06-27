# Spec-only behavior contract for the per-request cost arithmetic, the round6 display
# boundary, and the free-model rule.
# Ground truth:
#   internal/protocol/protocol.go    - Cost() and CostWith2(): (in*priceIn + out*priceOut)/1e6
#   internal/protocol/cost_math_test.go - the exact, table-driven arithmetic lock
#   cmd/rogerai-broker/main.go:857   - round6(f) = float64(int64(f*1e6+0.5))/1e6  (banker-less, round-half-up)
#   cmd/rogerai-broker/tunnel.go     - X-RogerAI-Cost header is round6(cost); the LEDGER captures the
#                                      UNROUNDED cost (Finalize/Settle get the full-precision value)
#   internal/webui/assets/console.js - fmtUSD = "$" + n.toFixed(2)  (UI shows 2 dp today)
#
# Two distinct numbers, pinned separately here:
#   * The CAPTURED cost (what the wallet is actually debited) is full float precision.
#   * The DISPLAYED cost (X-RogerAI-Cost header / dashboard) is round6, which rounds a
#     sub-$0.0000005 per-request cost to a bare 0.
#
# DECISION PINNED BY THIS SPEC: a cost that is nonzero but rounds to zero under round6 must
# be DISPLAYED as "<$0.000001", never as a bare "$0.00"/"0", so a real (if tiny) charge is
# never shown to the user as free. (See the flagged note: this display rule is NOT yet
# implemented - round6 currently emits a bare 0 - so these scenarios are the target contract.)
#
# NO step definitions and NO Go. Spec only.

@money @cost @precision
Feature: Per-request cost is exact, free models are $0, and tiny costs display honestly

  # ----------------------------------------------------------------------------
  # THE CORE ARITHMETIC: cost = (promptTokens*priceIn + completionTokens*priceOut)/1e6
  # ----------------------------------------------------------------------------

  Rule: Cost is exactly (prompt*priceIn + completion*priceOut)/1e6

    Scenario Outline: Token-to-credit arithmetic is exact across the matrix
      Given a receipt with <prompt> prompt tokens and <completion> completion tokens
      And price_in <priceIn> per 1M and price_out <priceOut> per 1M
      When the cost is computed
      Then the cost in credits is <cost>

      Examples: from cost_math_test.go plus edges
        | prompt    | completion | priceIn | priceOut | cost        |
        | 0         | 0          | 0.50    | 0.50     | 0.000000    |
        | 1000      | 5000       | 0       | 0        | 0.000000    |
        | 1000      | 500        | 0.20    | 0.50     | 0.000450    |
        | 0         | 800        | 0       | 0.30     | 0.000240    |
        | 2000      | 0          | 0.10    | 0        | 0.000200    |
        | 1000000   | 1000000    | 1.00    | 1.00     | 2.000000    |
        | 1         | 1          | 0.01    | 0.01     | 0.00000002  |

    Scenario: CostWith2 with the receipt's own tokens equals Cost
      Given a receipt with 1000 prompt tokens and 500 completion tokens
      And price_in 0.20 per 1M and price_out 0.50 per 1M
      When the cost is computed both via Cost and via CostWith2 with the same counts
      Then both results are equal to 0.000450

    Scenario: CostWith2 bills the broker counts, so an over-claim cannot inflate the charge
      Given a receipt claiming 9999 prompt tokens and 9999 completion tokens
      And price_in 0.50 per 1M and price_out 0.50 per 1M
      And the broker re-counts 100 prompt tokens and 200 completion tokens
      When the cost is computed via CostWith2 with the broker counts
      Then the cost in credits is 0.000150
      And the node's inflated claim is ignored

  # ----------------------------------------------------------------------------
  # FEE SPLIT CONSERVATION: owner + platform always sum back to the exact cost.
  # ----------------------------------------------------------------------------

  Rule: ownerShare + platform take == cost, with no negative shares

    Scenario Outline: The 70/30-style split conserves credits across fee rates and costs
      Given a settled cost of <cost> credits
      And a platform fee rate of <fee>
      When the cost is split
      Then the owner share is <cost> times one-minus-fee
      And the platform take is <cost> times fee
      And owner share plus platform take equals <cost>
      And neither share is negative

      Examples:
        | cost       | fee  |
        | 0.000000   | 0    |
        | 0.000450   | 0.25 |
        | 1.000000   | 0.30 |
        | 2.500000   | 0.50 |
        | 123.456789 | 1.00 |

  # ----------------------------------------------------------------------------
  # THE round6 BOUNDARY: where a real sub-microcredit cost rounds to a bare 0.
  # ----------------------------------------------------------------------------

  Rule: round6 rounds half-up to 6 decimals; sub-$0.0000005 rounds to 0

    Scenario: 34 tokens at $0.01 per 1M is a real $0.00000034 cost that round6 shows as 0
      Given a receipt with 34 completion tokens at price_out 0.01 per 1M
      When the cost is computed
      Then the captured cost in credits is 0.00000034
      And the round6 display value is 0.000000
      And the wallet is still debited the full 0.00000034 credits

    Scenario: 285 tokens at $0.01 per 1M crosses the boundary and round6 shows 0.000003
      Given a receipt with 285 completion tokens at price_out 0.01 per 1M
      When the cost is computed
      Then the captured cost in credits is 0.00000285
      And the round6 display value is 0.000003

    Scenario Outline: round6 boundary table
      Given a raw cost of <raw> credits
      When round6 is applied
      Then the displayed value is <rounded>

      Examples: half-up at the 1e-6 grid
        | raw         | rounded   |
        | 0.0000000   | 0.000000  |
        | 0.00000034  | 0.000000  |
        | 0.00000049  | 0.000000  |
        | 0.00000050  | 0.000001  |
        | 0.00000051  | 0.000001  |
        | 0.00000149  | 0.000001  |
        | 0.00000150  | 0.000002  |
        | 0.00000285  | 0.000003  |
        | 0.0000015   | 0.000002  |
        | 1.2345674   | 1.234567  |
        | 1.2345675   | 1.234568  |

  # ----------------------------------------------------------------------------
  # DISPLAY RULE (pinned target): nonzero-but-rounded must read "<$0.000001".
  # ----------------------------------------------------------------------------

  Rule: A nonzero cost that rounds to zero displays as "<$0.000001", not a bare "$0.00"

    Scenario: A real sub-microcredit charge is shown as "<$0.000001"
      Given a captured cost of 0.00000034 credits
      When the cost is rendered for the consumer
      Then the displayed cost reads "<$0.000001"
      And it never reads "$0.00" for a nonzero charge

    Scenario: A genuinely free request reads exactly "$0.000000"
      Given a captured cost of 0.000000 credits
      When the cost is rendered for the consumer
      Then the displayed cost reads "$0.000000"

    Scenario: A cost at or above one microcredit reads its rounded numeric value
      Given a captured cost of 0.00000285 credits
      When the cost is rendered for the consumer
      Then the displayed cost reads "$0.000003"

    Scenario Outline: Display-rule decision table
      Given a captured cost of <captured> credits
      When the cost is rendered for the consumer
      Then the displayed cost reads <display>

      Examples:
        | captured    | display       |
        | 0.000000    | "$0.000000"   |
        | 0.00000034  | "<$0.000001"  |
        | 0.00000049  | "<$0.000001"  |
        | 0.00000050  | "$0.000001"   |
        | 0.00000285  | "$0.000003"   |
        | 0.045000    | "$0.045000"   |

  # ----------------------------------------------------------------------------
  # FREE MODEL: price 0 on both axes is ALWAYS exactly $0.
  # ----------------------------------------------------------------------------

  Rule: A free model (price 0) is always exactly $0, whatever the token counts

    Scenario Outline: Free model is always $0
      Given a receipt with <prompt> prompt tokens and <completion> completion tokens
      And price_in 0 per 1M and price_out 0 per 1M
      When the cost is computed
      Then the cost in credits is 0.000000
      And no hold is placed and no ledger money rows are written
      And the displayed cost reads "$0.000000"

      Examples:
        | prompt  | completion |
        | 0       | 0          |
        | 1000    | 5000       |
        | 1000000 | 1000000    |

    Scenario: A free time-of-use window zeroes the cost even on a priced base offer
      Given an offer with base price_out 5.00 per 1M and a FREE schedule window active now
      And a receipt with 1000 completion tokens
      When the active price is resolved and the cost computed
      Then the active price is 0
      And the cost in credits is 0.000000
