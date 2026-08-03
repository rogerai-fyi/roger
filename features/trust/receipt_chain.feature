# Receipt chain continuity — the per-node hash chain only proves something if SOMEONE
# holds the head and checks it. Before 2026-08-02 nobody did: PrevHash was written by
# the node from process-local memory and no party ever compared it, so omission,
# reorder, fork, and restart-reset were all invisible.
#
# This spec covers the DETECT-AND-RECORD stage. The broker persists a per-node chain
# head, compares each settled receipt against it, and counts breaks — but never refuses
# settlement and never STRIKES on that basis. A strike freezes the owner's earning lots
# and escalates toward a ban, which is enforcement; enforcement is a LATER stage with
# its own approved spec. Two reasons it would be wrong today: the node-side chain does
# not survive a restart, so an honest restarted node would be punished; and the broker
# has only just begun recording heads, so every existing node's first receipt after
# this ships legitimately fails to continue a chain nobody was tracking.
#
# GROUND TRUTH: internal/store (ChainHead/AdvanceChain), cmd/rogerai-broker settle path.

Feature: The broker holds each node's receipt chain head and records every break
  A node's receipt chain is evidence only if the broker remembers where that node's
  chain was and notices when the next receipt does not follow from it.

  Background:
    Given a registered node whose owner is known

  # --- opening and advancing a chain ---------------------------------------

  Scenario: A node's first settled receipt opens its chain
    Given the broker holds no chain head for the node
    When a receipt with an empty PrevHash settles
    Then the chain is recorded as continuous
    And the stored head becomes that receipt's hash

  Scenario: A receipt that continues the chain advances the head
    Given the broker holds a chain head H for the node
    When a receipt whose PrevHash is H settles
    Then the chain is recorded as continuous
    And the stored head becomes the new receipt's hash

  Scenario: Chains are held per node, not per account or process
    Given two nodes under the same owner each have their own chain head
    When each node settles a receipt continuing its OWN chain
    Then both are recorded as continuous
    And neither node's head is advanced by the other's receipt

  # --- detecting a break ----------------------------------------------------

  Scenario Outline: A chain that does not continue is recorded as a break
    Given the broker holds a chain head H for the node
    When a receipt whose PrevHash is "<supplied>" settles
    Then the chain is recorded as broken with the expected head H
    And settlement still completes and the node is still paid
    And the node's durable break counter is incremented
    And NO strike is recorded, because a strike freezes earnings and that is enforcement

    Examples:
      | supplied                            |
      | empty, as if the node restarted     |
      | the hash of an older receipt        |
      | a hash the broker has never seen    |
      | a syntactically valid but wrong hash |

  Scenario: A break still advances the head, so one break is not reported forever
    Given the broker holds a chain head H for the node
    When a receipt with a mismatched PrevHash settles
    And a following receipt continues from THAT receipt
    Then the second receipt is recorded as continuous
    And exactly one break is recorded across the two

  Scenario: A node cannot hide an omission by skipping ahead
    Given a node settles receipts R1 then R2 then R3 but never presents R2
    When R3 settles with R2's hash as its PrevHash
    Then the chain is recorded as broken, because the broker's head is still R1's hash
    And the omission is visible as evidence rather than silently accepted

  # --- replay and idempotency ----------------------------------------------

  Scenario: Re-applying the same receipt does not manufacture a break
    Given a receipt has already advanced the chain to its hash
    When the identical receipt is applied again after a retry or restart
    Then the chain is recorded as continuous
    And the stored head is unchanged
    And no additional strike is recorded

  Scenario: A replayed receipt does not inflate the break counter
    Given a receipt has already advanced the chain to its hash
    When the identical receipt is applied again
    Then the break counter is unchanged

  # --- failure and safety ---------------------------------------------------

  Scenario: Chain bookkeeping never blocks the money path
    Given the chain-head store is unavailable
    When a receipt settles
    Then settlement completes normally and the consumer is billed
    And the chain result is recorded as unknown rather than broken
    And no break is counted from an unavailable store

  Scenario: Detection does not refuse work in this stage
    Given a node has accumulated several recorded chain breaks
    When it serves another request whose receipt binds to the dispatched job
    Then the request settles and the node earns
    And the evidence remains available for the enforcement stage

  # --- what the owner can see ----------------------------------------------

  Scenario: An owner can see the chain status of each of their nodes
    Given an owner has nodes with continuous and broken chains
    When they read their station list
    Then each node reports its current chain head, last check time, and break count
    And a broken chain is labelled as an audit signal, not a ban
