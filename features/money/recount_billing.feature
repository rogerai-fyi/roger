# Spec-only behavior contract for the L1 broker token re-count + settle-time billing.
# Ground truth (read, not guessed):
#   cmd/rogerai-broker/recount.go    - settleRecount / settleRecountPrompt / observeRecount /
#                                      observeRecountInput / completionText / producedUsableOutput
#   cmd/rogerai-broker/strikes.go    - flagEmptyOutput / flagRecountOver thresholds
#   cmd/rogerai-broker/tunnel.go     - relay/relayStream wiring: VOID gate, CostWith2, hold/refund
#   internal/protocol/protocol.go    - UsageReceipt.CostWith2 / Cost
#   internal/store/store.go          - billedTokens (min(claim,broker) on each axis), Settle/Finalize
#
# Core invariants this file pins:
#   * Billing uses min(node-claim, broker-recount) on BOTH axes (input + output) whenever an
#     EXACT broker re-count exists and is strictly smaller; otherwise the node's claim stands
#     (the broker NEVER inflates a claim, and a heuristic-only count never re-bills).
#   * Reasoning/harmony output counts as completion text (completionText folds message.reasoning
#     in) so reasoning-only replies are neither mis-billed to $0 nor false-flagged empty.
#   * Empty / no-usable-output VOIDS the charge ($0, hold refunded in full, $0 metering receipt).
#   * An over-report past the BILLING tolerance holds the node's earnings from promotion.
#   * An over-report past the (wider) STRIKE tolerance also accrues an owner strike.
#
# NO step definitions and NO Go are part of this change. This is the approved-behavior contract.

@money @recount @billing
Feature: The broker re-counts tokens and bills the lesser of claim and re-count on both axes

  Background:
    Given the L1 re-count sidecar is enabled
    And the billing re-count tolerance is 2%
    And the owner strike tolerance is 25%
    And the platform fee rate is 30%
    And a consumer wallet "alice" funded with 100.00 real credits
    And node "n1" is owned by account "op1"

  # ----------------------------------------------------------------------------
  # OUTPUT AXIS: an over-reporting node is capped at the broker's exact re-count.
  # ----------------------------------------------------------------------------

  Rule: Output billing is min(claimed completion, exact broker re-count)

    Scenario: An over-reporting node is billed on the smaller broker re-count
      Given a served request on model "m" at price_out 1.00 per 1M
      And the node claims 1000 completion tokens
      And the broker exactly re-counts 600 completion tokens
      When the request settles
      Then the billed completion tokens are 600
      And the consumer is charged 0.000600 credits
      And the operator earns the owner share of 0.000600 credits
      And a claim-vs-billed audit row records claimed 1000 billed 600

    Scenario: An honest node billed exactly on a matching re-count
      Given a served request on model "m" at price_out 1.00 per 1M
      And the node claims 600 completion tokens
      And the broker exactly re-counts 600 completion tokens
      When the request settles
      Then the billed completion tokens are 600
      And no claim-vs-billed audit row is written

    Scenario: A node that UNDER-reports is billed on its own (smaller) claim, never inflated
      Given a served request on model "m" at price_out 1.00 per 1M
      And the node claims 400 completion tokens
      And the broker exactly re-counts 600 completion tokens
      When the request settles
      Then the billed completion tokens are 400
      And the broker never bills more than the node claimed
      And no over-report discrepancy is recorded

    Scenario: A HEURISTIC (non-exact) re-count never re-bills, even when smaller
      Given a served request on model "m" at price_out 1.00 per 1M
      And the node claims 1000 completion tokens
      And the broker re-counts 600 completion tokens with a NON-exact heuristic
      When the request settles
      Then the billed completion tokens are 1000
      And no over-report discrepancy is recorded

    Scenario: The sidecar being unreachable fails OPEN to the node's claim
      Given a served request on model "m" at price_out 1.00 per 1M
      And the node claims 1000 completion tokens
      And the tokenizer sidecar is unreachable
      When the request settles
      Then the billed completion tokens are 1000
      And the node is not penalized for the sidecar outage

    Scenario: Re-count disabled bills the node's claim unchanged
      Given the L1 re-count sidecar is disabled
      And a served request on model "m" at price_out 1.00 per 1M
      And the node claims 1000 completion tokens
      When the request settles
      Then the billed completion tokens are 1000

    Scenario Outline: Output cap permutations across claim / re-count / exactness
      Given a served request on model "m" at price_out <price> per 1M
      And the node claims <claim> completion tokens
      And the broker re-counts <recount> completion tokens with exact=<exact>
      When the request settles
      Then the billed completion tokens are <billed>

      Examples: exact re-counts cap an over-report; under-reports and non-exact pass through
        | price | claim | recount | exact | billed |
        | 1.00  | 1000  | 600     | true  | 600    |
        | 1.00  | 1000  | 999     | true  | 999    |
        | 1.00  | 1000  | 1000    | true  | 1000   |
        | 1.00  | 1000  | 1001    | true  | 1000   |
        | 1.00  | 1000  | 400     | true  | 400    |
        | 1.00  | 1000  | 0       | true  | 1000   |
        | 1.00  | 1000  | 600     | false | 1000   |
        | 0.50  | 250   | 100     | true  | 100    |
        | 0.50  | 1     | 0       | true  | 1      |
        | 2.00  | 5000  | 4999    | true  | 4999   |

  # ----------------------------------------------------------------------------
  # INPUT AXIS: settleRecountPrompt mirrors the output cap and adds the byte floor.
  # ----------------------------------------------------------------------------

  Rule: Input billing is min(claimed prompt, exact broker re-count), clamped to body bytes

    Scenario: An over-reporting node is capped on the INPUT axis too
      Given a served request on model "m" at price_in 1.00 per 1M
      And the request body is 4000 bytes
      And the node claims 1000 prompt tokens
      And the broker exactly re-counts 700 prompt tokens
      When the request settles
      Then the billed prompt tokens are 700
      And the consumer is charged 0.000700 credits

    Scenario: Both axes are capped independently in the same request
      Given a served request on model "m" at price_in 1.00 per 1M and price_out 1.00 per 1M
      And the request body is 8000 bytes
      And the node claims 1000 prompt tokens and 2000 completion tokens
      And the broker exactly re-counts 700 prompt tokens and 1500 completion tokens
      When the request settles
      Then the billed prompt tokens are 700
      And the billed completion tokens are 1500
      And the consumer is charged 0.002200 credits

    Scenario Outline: Input cap permutations
      Given a served request on model "m" at price_in <price> per 1M
      And the request body is <bytes> bytes
      And the node claims <claim> prompt tokens
      And the broker re-counts <recount> prompt tokens with exact=<exact>
      When the request settles
      Then the billed prompt tokens are <billed>

      Examples:
        | price | bytes | claim | recount | exact | billed |
        | 1.00  | 4000  | 1000  | 700     | true  | 700    |
        | 1.00  | 4000  | 700   | 700     | true  | 700    |
        | 1.00  | 4000  | 500   | 700     | true  | 500    |
        | 1.00  | 4000  | 1000  | 700     | false | 1000   |
        | 1.00  | 4000  | 1000  | 0       | true  | 1000   |

  # ----------------------------------------------------------------------------
  # REASONING / HARMONY OUTPUT counts as completion (the false-ban regression class).
  # ----------------------------------------------------------------------------

  Rule: Reasoning-channel text is real billable completion output

    Scenario: A reasoning-only reply (empty content, populated reasoning) is usable output
      Given the node returns a reply with empty content and reasoning "the answer is 42"
      And the node claims 7 completion tokens
      When the broker evaluates the response
      Then the request produced usable output
      And the completion text re-counted is "the answer is 42"
      And the empty-output strike does NOT fire

    Scenario: A reasoning-only reply is billed on the re-counted reasoning text, not $0
      Given a served request on model "m" at price_out 1.00 per 1M
      And the node returns a reply with empty content and a 300-token reasoning channel
      And the node claims 300 completion tokens
      And the broker exactly re-counts 300 completion tokens from the reasoning text
      When the request settles
      Then the billed completion tokens are 300
      And the request is not voided to $0

    Scenario: A plain content reply is unchanged by reasoning handling
      Given the node returns a reply with content "hello" and no reasoning
      When the broker evaluates the response
      Then the completion text re-counted is "hello"
      And the request produced usable output

    Scenario: content AND reasoning are both folded into the billed completion text
      Given the node returns a reply with content "A" and reasoning "BCD"
      When the broker evaluates the response
      Then the completion text re-counted is "ABCD"

    Scenario Outline: Output-usability classification
      Given a reply with status <status> content <content> reasoning <reasoning> claiming <claim> completion tokens
      When the broker evaluates the response
      Then producing usable output is <usable>

      Examples: reasoning rescues an otherwise-"empty" reply; a true void stays void
        | status | content | reasoning | claim | usable |
        | 200    | "hi"    | ""        | 2     | true   |
        | 200    | ""      | "answer"  | 7     | true   |
        | 200    | "hi"    | "answer"  | 9     | true   |
        | 200    | ""      | ""        | 5     | false  |
        | 200    | "   "   | ""        | 5     | false  |
        | 200    | ""      | ""        | 0     | false  |
        | 500    | "hi"    | "answer"  | 5     | false  |
        | 400    | ""      | ""        | 0     | false  |

  # ----------------------------------------------------------------------------
  # EMPTY / NO-USABLE-OUTPUT -> VOID: $0, full hold refund, $0 metering receipt.
  # ----------------------------------------------------------------------------

  Rule: A request with no usable output is charged $0 and refunds the hold in full

    Scenario: An empty 200 reply is voided and the hold is refunded
      Given alice has a pre-auth hold of 0.005000 credits for the request
      And a served request on model "m" at price_in 1.00 per 1M and price_out 1.00 per 1M
      And the node returns status 200 with an empty completion
      And the node claims 1000 prompt tokens and 0 completion tokens
      When the request settles
      Then the consumer is charged 0.000000 credits
      And alice's full hold of 0.005000 credits is refunded
      And a $0 metering receipt is recorded for lineage
      And the operator earns 0.000000 credits
      And the empty-output strike is flagged against owner "op1"

    Scenario: A node erroring with status >= 400 is voided regardless of claimed input
      Given a served request on model "m" at price_in 1.00 per 1M
      And the node returns status 503
      And the node claims 5000 prompt tokens
      When the request settles
      Then the consumer is charged 0.000000 credits
      And the hold is refunded in full
      And the empty-output strike is flagged against owner "op1"

    Scenario: Claim-without-text (tokens claimed, whitespace body) is voided
      Given a served request on model "m" at price_out 1.00 per 1M
      And the node returns status 200 with a whitespace-only completion
      And the node claims 800 completion tokens
      When the request settles
      Then the consumer is charged 0.000000 credits
      And the empty-output strike is flagged against owner "op1"

    Scenario Outline: VOID gate truth table
      Given a served request with node status <status> and completion <completion> claiming <claim> completion tokens
      When the request settles
      Then the request void state is <voided>

      Examples:
        | status | completion | claim | voided |
        | 200    | "ok"       | 3     | false  |
        | 200    | ""         | 3     | true   |
        | 200    | "  "       | 3     | true   |
        | 200    | ""         | 0     | true   |
        | 404    | "ok"       | 3     | true   |
        | 500    | ""         | 0     | true   |

  # ----------------------------------------------------------------------------
  # OVER-REPORT CONSEQUENCES: earnings hold (past billing tol) + owner strike (past strike tol).
  # ----------------------------------------------------------------------------

  Rule: Over-report past billing tolerance holds earnings; past strike tolerance strikes the owner

    Scenario: A small over-report within strike tolerance holds earnings but does NOT strike
      Given a served request on model "m" at price_out 1.00 per 1M
      And the node claims 110 completion tokens
      And the broker exactly re-counts 100 completion tokens
      When the request settles
      Then the over-report ratio is 10%
      And the over-report exceeds the 2% billing tolerance
      And the node's earnings are HELD from promotion
      And a discrepancy is recorded against the node
      And the owner is NOT struck because 10% is within the 25% strike tolerance

    Scenario: A gross over-report past strike tolerance strikes the owner
      Given a served request on model "m" at price_out 1.00 per 1M
      And the node claims 200 completion tokens
      And the broker exactly re-counts 100 completion tokens
      When the request settles
      Then the over-report ratio is 100%
      And the node's earnings are HELD from promotion
      And the recount-over-report strike is flagged against owner "op1"

    Scenario: An over-report exactly AT the billing tolerance does not flag (strict greater-than)
      Given a served request on model "m" at price_out 1.00 per 1M
      And the node claims 102 completion tokens
      And the broker exactly re-counts 100 completion tokens
      When the request settles
      Then the over-report ratio is 2%
      And no discrepancy is recorded against the node
      And the node's earnings are NOT held

    Scenario: An INPUT-axis gross over-report holds earnings and strikes the owner
      Given a served request on model "m" at price_in 1.00 per 1M
      And the request body is 100000 bytes
      And the node claims 200 prompt tokens
      And the broker exactly re-counts 100 prompt tokens
      When the request settles
      Then the over-report ratio is 100%
      And the node's earnings are HELD from promotion
      And the recount-over-report strike is flagged against owner "op1" on the input axis

    Scenario: The async probe re-count path NEVER strikes (no request id)
      Given a probe re-count observes claimed 200 vs exact re-count 100
      When the discrepancy is folded into trust
      Then the node's earnings are HELD from promotion
      And no owner strike is recorded for the probe-path discrepancy

    Scenario Outline: Over-report consequence boundary (billing tol 2%, strike tol 25%)
      Given a served request on model "m" at price_out 1.00 per 1M
      And the node claims <claim> completion tokens
      And the broker exactly re-counts <recount> completion tokens
      When the request settles
      Then the earnings-held state is <held>
      And the owner-struck state is <struck>

      Examples:
        | claim | recount | held  | struck |
        | 100   | 100     | false | false  |
        | 101   | 100     | false | false  |
        | 102   | 100     | false | false  |
        | 103   | 100     | true  | false  |
        | 110   | 100     | true  | false  |
        | 124   | 100     | true  | false  |
        | 125   | 100     | true  | false  |
        | 126   | 100     | true  | true   |
        | 150   | 100     | true  | true   |
        | 200   | 100     | true  | true   |

    Scenario Outline: Configurable tolerances change the held/struck boundary
      Given the billing re-count tolerance is <bill>%
      And the owner strike tolerance is <strike>%
      And a served request on model "m" at price_out 1.00 per 1M
      And the node claims <claim> completion tokens
      And the broker exactly re-counts 100 completion tokens
      When the request settles
      Then the earnings-held state is <held>
      And the owner-struck state is <struck>

      Examples:
        | bill | strike | claim | held  | struck |
        | 2    | 25     | 130   | true  | true   |
        | 5    | 50     | 130   | true  | false  |
        | 5    | 50     | 160   | true  | true   |
        | 0    | 25     | 101   | true  | false  |
        | 10   | 10     | 109   | false | false  |
        | 10   | 10     | 112   | true  | true   |

    # Flagged for review: strikeTolerance is clamped UP to billing tolerance at load
    # (recount.go loadRecount), so a configured strike tol below the billing tol is raised
    # to equal it - the strike then fires at the same point billing is capped.
    Scenario: A strike tolerance configured below billing tolerance is clamped up to it
      Given the billing re-count tolerance is 10%
      And the owner strike tolerance is configured to 1%
      When the re-count config loads
      Then the effective strike tolerance is 10%
