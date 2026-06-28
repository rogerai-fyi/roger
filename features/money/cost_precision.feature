# Spec-only behavior contract for the per-request cost arithmetic, the round6 display
# boundary, and the free-model rule.
# Ground truth:
#   internal/protocol/protocol.go    - Cost() and CostWith2(): (in*priceIn + out*priceOut)/1e6
#   internal/protocol/cost_math_test.go - the exact, table-driven arithmetic lock
#   cmd/rogerai-broker/main.go       - round6(f) = float64(int64(f*1e6+0.5))/1e6 (still used for the BALANCE/quality headers)
#   cmd/rogerai-broker/httputil.go   - fmtCostHeader(cost): the EXACT cost for the X-RogerAI-Cost header
#                                      (6 significant figures, plain decimal, no scientific, no round6 truncation)
#   cmd/rogerai-broker/tunnel.go     - X-RogerAI-Cost header is fmtCostHeader(cost); the LEDGER captures the
#                                      UNROUNDED cost (Finalize/Settle get the full-precision value)
#   internal/tui/tui.go dollars()    - the consumer renderer: 0 -> "$0.00"; a sub-cent value shows ~3
#                                      significant figures (e.g. $0.00000034); >= $0.01 shows 2 dp
#   internal/webui/assets/console.js - fmtUSD = "$" + n.toFixed(2)  (the WEB console still shows 2 dp; out of scope here)
#
# Two distinct numbers, pinned separately here:
#   * The CAPTURED cost (what the wallet is actually debited) is full float precision.
#   * The DISPLAYED cost (X-RogerAI-Cost header -> dollars()) shows the EXACT value, so a real
#     sub-microcredit charge reads as e.g. $0.00000034, never as a bare $0.00.
#
# DECISION PINNED BY THIS SPEC (REVISED 2026-06-28, founder-approved): show the EXACT cost, not a
# "<$0.000001" floor. A nonzero charge displays its true value (down to the renderer's significant
# figures) so it is never shown as free; only a genuinely zero cost reads "$0.00". This SUPERSEDES
# the earlier "<$0.000001" floor decision - round6 no longer truncates the cost header.
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

  Rule: The cost is captured + SENT at full precision; round6 stays only on the balance/quality grid

    Scenario: 34 tokens at $0.01 per 1M is a real $0.00000034 cost - captured AND sent exactly
      Given a receipt with 34 completion tokens at price_out 0.01 per 1M
      When the cost is computed
      Then the captured cost in credits is 0.00000034
      And the wallet is debited the full 0.00000034 credits
      And the X-RogerAI-Cost header sends 0.00000034 exactly (NOT round6'd to a bare 0)

    Scenario: 285 tokens at $0.01 per 1M is captured AND sent exactly (no round6 grid snap)
      Given a receipt with 285 completion tokens at price_out 0.01 per 1M
      When the cost is computed
      Then the captured cost in credits is 0.00000285
      And the X-RogerAI-Cost header sends 0.00000285 exactly

    # round6 the FUNCTION is unchanged (it still rounds the balance/quality headers half-up to
    # the 1e-6 grid); it is just no longer applied to the per-request COST header.
    Scenario Outline: round6 the function rounds half-up to the 1e-6 grid
      Given a raw value of <raw> credits
      When round6 is applied
      Then round6 returns <rounded>

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
  # DISPLAY RULE (pinned target, REVISED): show the EXACT cost, never a bare $0.00 for a charge.
  # ----------------------------------------------------------------------------

  Rule: A nonzero cost displays its EXACT value; only a genuinely zero cost reads "$0.00"

    Scenario: A real sub-microcredit charge shows its exact value, not $0.00
      Given a captured cost of 0.00000034 credits
      When the cost is rendered for the consumer (dollars())
      Then the displayed cost reads "$0.00000034"
      And it never reads "$0.00" for a nonzero charge

    Scenario: A genuinely free request reads exactly "$0.00"
      Given a captured cost of 0.000000 credits
      When the cost is rendered for the consumer (dollars())
      Then the displayed cost reads "$0.00"

    Scenario: A sub-cent cost above a microcredit shows its exact value
      Given a captured cost of 0.00000285 credits
      When the cost is rendered for the consumer (dollars())
      Then the displayed cost reads "$0.00000285"

    # dollars() renders 0 as "$0.00", a sub-cent value at ~3 significant figures (exact, plain
    # decimal, no scientific), and a value >= $0.01 at 2 dp. A free request ($0.00) and a tiny
    # paid request ($0.00000034) are now visibly DISTINCT, so a charge never looks free.
    Scenario Outline: Display-rule decision table (exact value)
      Given a captured cost of <captured> credits
      When the cost is rendered for the consumer (dollars())
      Then the displayed cost reads <display>

      Examples:
        | captured    | display         |
        | 0.000000    | "$0.00"         |
        | 0.00000034  | "$0.00000034"   |
        | 0.00000050  | "$0.0000005"    |
        | 0.00000285  | "$0.00000285"   |
        | 0.00345000  | "$0.00345"      |
        | 0.12000000  | "$0.12"         |

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
      And the displayed cost reads "$0.00"

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
