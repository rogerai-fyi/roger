# APPROVED by the founder 2026-07-01 (spec-first workflow step 3) - step definitions +
# implementation may now proceed. Written from the 2026-07-01 launch audit.
#
# Spec-first behavior contract for VOLUNTARY REFUND clawback - the charge.refunded
# webhook (cmd/rogerai-broker/billing.go), the gap the 2026-07-01 launch audit found:
# today only checkout.session.completed and charge.dispute.created are handled, so a
# manual dashboard refund moves real money out of Stripe with NO ledger clawback -
# the consumer keeps spendable credits their card got back.
#
# A refund reuses the dispute clawback machinery (store.ChargebackLineage) with a
# refund-flavored idempotency key and NO card-network fee semantics: the operator's
# share of already-earned lots is clawed / transfer-reversed exactly like a dispute,
# any shortfall is platform_loss, and the consumer wallet is debited the refunded
# amount. Differences from a dispute are ONLY:
#   - the trigger event (charge.refunded, incl. partial refunds), and
#   - idempotency is per REFUND id (one charge can be partially refunded N times).
#
# Invariants (mirroring chargebacks.feature I1-I4):
#   R1 CONSERVATION: clawed + reversed + platform_loss == refunded amount.
#   R2 NO COLLATERAL CLAW: only lots funded by the refunded consumer's charges.
#   R3 IDEMPOTENCY: the same Stripe refund id is a total no-op on re-delivery.
#   R4 CONSUMER DEBIT: the wallet is debited the refunded amount exactly once,
#      even if it drives the balance negative (the money is ALREADY gone from
#      Stripe; a floor would mint credits out of thin air).
#   R5 SIGNATURE: an unsigned / bad-signature webhook is rejected before any of this.

Feature: A voluntary Stripe refund claws back the refunded credits like a dispute

  Background:
    Given a fresh money store
    And the platform fee rate is 30%
    And Stripe billing is configured with a webhook secret

  # ===========================================================================
  # 1. The core clawback - full refund
  # ===========================================================================

  Scenario: a full $50 refund debits the consumer and claws the operator share
    Given wallet "alice" topped up 50.00 via Stripe charge "ch_1"
    And alice has a settled request of cost 50.00 on node "n1" owned by "op1"
    When a "charge.refunded" webhook arrives for charge "ch_1" with amount_refunded 50.00 and refund id "re_1"
    Then alice's balance is reduced by exactly 50.00
    And exactly 35.00 is clawed from operator "op1"
    And the platform loss is 15.00
    And clawed plus reversed plus platform loss equals 50.00

  Scenario: a refund for unspent credits debits the wallet only (nothing to claw)
    Given wallet "bob" topped up 25.00 via Stripe charge "ch_2"
    And bob has spent nothing
    When a "charge.refunded" webhook arrives for charge "ch_2" with amount_refunded 25.00 and refund id "re_2"
    Then bob's balance is reduced by exactly 25.00
    And no operator lot is touched
    And the platform loss is 0.00

  Scenario: a refund exceeding the remaining balance still debits in full (negative balance)
    Given wallet "carol" topped up 20.00 via Stripe charge "ch_3"
    And carol has a settled request of cost 20.00 on node "n1" owned by "op1"
    When a "charge.refunded" webhook arrives for charge "ch_3" with amount_refunded 20.00 and refund id "re_3"
    Then carol's balance is negative by the unrecovered remainder
    And the refund ledger row records the full 20.00 debit

  # ===========================================================================
  # 2. Partial + repeated refunds (the shape disputes never have)
  # ===========================================================================

  Scenario: a partial refund claws only the refunded fraction
    Given wallet "dave" topped up 100.00 via Stripe charge "ch_4"
    When a "charge.refunded" webhook arrives for charge "ch_4" with amount_refunded 30.00 and refund id "re_4a"
    Then dave's balance is reduced by exactly 30.00

  Scenario: two partial refunds of the same charge process independently by refund id
    Given wallet "dave" topped up 100.00 via Stripe charge "ch_4"
    When a "charge.refunded" webhook arrives for charge "ch_4" with amount_refunded 30.00 and refund id "re_4a"
    And a "charge.refunded" webhook arrives for charge "ch_4" with amount_refunded 20.00 and refund id "re_4b"
    Then dave's balance is reduced by exactly 50.00 in total

  Scenario: Stripe re-delivers charge.refunded with a cumulative amount_refunded - only the NEW refund is applied
    Given wallet "erin" topped up 100.00 via Stripe charge "ch_5"
    And a processed refund "re_5a" of 30.00 on charge "ch_5"
    When a "charge.refunded" webhook arrives for charge "ch_5" with amount_refunded 50.00 carrying refunds ["re_5a": 30.00, "re_5b": 20.00]
    Then only 20.00 more is debited (re_5a is not double-applied)

  # ===========================================================================
  # 3. Idempotency + interplay with disputes
  # ===========================================================================

  Scenario: re-delivering the same refund id is a total no-op
    Given wallet "frank" topped up 40.00 via Stripe charge "ch_6"
    And a processed refund "re_6" of 40.00 on charge "ch_6"
    When the same "charge.refunded" webhook for refund id "re_6" is delivered again
    Then no wallet is debited again
    And no new ledger rows are minted

  Scenario: a refund after a dispute on the same charge never double-claws
    Given wallet "gina" topped up 60.00 via Stripe charge "ch_7"
    And a processed dispute "dp_7" of 60.00 on charge "ch_7"
    When a "charge.refunded" webhook arrives for charge "ch_7" with amount_refunded 60.00 and refund id "re_7"
    Then the total debited across the dispute and the refund never exceeds 60.00

  # ===========================================================================
  # 4. Adversarial / malformed input
  # ===========================================================================

  Scenario: a refund for a charge with no wallet mapping is acknowledged and logged, never guessed
    When a "charge.refunded" webhook arrives for unknown charge "ch_ghost" with refund id "re_8"
    Then the webhook is acknowledged 2xx (Stripe must not retry forever)
    And no wallet is debited
    And the orphan refund is logged for operator follow-up

  Scenario: an unsigned charge.refunded webhook is rejected
    When a "charge.refunded" webhook arrives with an invalid signature
    Then it is rejected with 400 before any parsing of the refund

  Scenario: a zero/negative amount_refunded is a no-op
    Given wallet "hank" topped up 10.00 via Stripe charge "ch_9"
    When a "charge.refunded" webhook arrives for charge "ch_9" with amount_refunded 0.00 and refund id "re_9"
    Then no wallet is debited
