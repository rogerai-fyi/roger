# BUILD STATUS: PROPOSED. added 2026-10-05 (founder ruling): build the non-stream cancel with
# capability negotiation. Contract §14 "bill what was delivered" (§14.10) already bills a
# non-stream disconnect $0; this spec makes the STATION stop generating too, so a home GPU slot
# is freed the moment the consumer leaves instead of finishing a job nobody will read.
#
# TODAY (wt/routing-slice6 6e3e698c): on a non-stream disconnect relay() returns dispatchGone
# (cmd/rogerai-broker/tunnel.go ~3186) and the station's late result finds no waiter; the station
# keeps generating to the end. On a stream disconnect the broker stops reading the station's
# /agent/stream body, which ends the stream for a station on the SAME instance only.
#
# THE DESIGN (mirrors the ack negotiation in node_ack.feature):
#   - A node that supports cancels sends `X-Roger-Cancel: 1` on every /agent/poll (a header, not a
#     registration field: an older broker ignores it and the registration signature is untouched).
#   - The broker records the node as cancel-capable in the SHARED store with a short TTL (2 min),
#     refreshed by each such poll (any instance); without a shared store, in the instance's own
#     map (single-instance dev only).
#   - A cancel-capable node also long-polls GET /agent/cancels?node=<id> (Bearer BridgeToken),
#     held like /agent/poll; it answers 200 {"ids":[...]} with the job ids to stop, or 204.
#   - When a consumer disconnects before the result (non-stream) or mid-stream, the broker sends
#     a cancel for the dispatched job ONLY if the serving node is cancel-capable; the cancel is
#     delivered across instances through the shared bus (a short-TTL buffer holds it when the
#     node is between cancel polls), so it reaches the node wherever its cancel poll is held.
#   - On a cancel the node aborts its upstream request for that job and reports what it produced
#     (a non-stream job: status 499, nothing delivered; a stream: the receipt for the tokens it
#     forwarded). Billing is unchanged: "bill what was delivered" ($0 for a non-stream disconnect).
#   - A node that never sent the header is never sent a cancel (it keeps today's behaviour). A
#     node on a broker without /agent/cancels gets 404 there and backs off; it never re-registers
#     because of it.
#
# DECISIONS:
#   C1  Only a node that advertised cancel support is ever sent a cancel.
#   C2  A cancel stops the station's upstream work; billing stays "bill what was delivered".
#   C3  Cancels cross instances (shared bus + short buffer); a cancel is delivered at most once.
#   C4  A cancel cannot name another node's job, and an unknown or finished job id is a no-op.
#   C5  Old nodes and old brokers behave exactly as today.
#
# ENFORCED BY: cmd/rogerai-broker/job_cancel_bdd_test.go (real broker handlers, two instances over
# one shared store) and internal/agent/cancel_test.go (the real agent against real broker
# handlers, including an older broker without /agent/cancels).

Feature: A consumer who leaves stops the station's work

  Background:
    Given a multi-instance broker of two instances sharing one store
    And node "n-1" is on air for "qwen3-32b"

  # --- negotiation ------------------------------------------------------------------

  Scenario: A node that sends X-Roger-Cancel on its polls is recorded as cancel-capable
    When "n-1" polls with X-Roger-Cancel "1"
    Then "n-1" is cancel-capable on every instance

  Scenario: The capability ages out when the node stops advertising it
    Given "n-1" polled with X-Roger-Cancel "1"
    When 3 minutes pass with "n-1" polling without the header
    Then "n-1" is not cancel-capable

  Scenario: A node that never sent the header is never sent a cancel
    Given "n-1" polls without X-Roger-Cancel
    And "alice"'s non-stream request is dispatched to "n-1"
    When "alice" disconnects before the result arrives
    Then no cancel is queued for "n-1"
    And "alice" is billed $0

  # --- delivery ---------------------------------------------------------------------

  Scenario: A non-stream disconnect sends one cancel for the dispatched job
    Given "n-1" is cancel-capable
    And "alice"'s non-stream request is dispatched to "n-1" as job J
    When "alice" disconnects before the result arrives
    Then "n-1"'s next GET /agent/cancels answers 200 with ids [J]
    And a second GET /agent/cancels answers 204

  Scenario: A stream disconnect sends one cancel for the dispatched job
    Given "n-1" is cancel-capable
    And "alice"'s streaming request is dispatched to "n-1" as job J
    When "alice" disconnects after the first content frame
    Then "n-1"'s next GET /agent/cancels answers 200 with ids [J]

  Scenario: A cancel reaches the node through the other instance
    Given "n-1" is cancel-capable and its cancel poll is held on instance B
    And "alice"'s non-stream request is relayed by instance A to "n-1" as job J
    When "alice" disconnects before the result arrives
    Then the cancel poll held on instance B answers 200 with ids [J]

  Scenario: A cancel sent while the node is between cancel polls waits for its next poll
    Given "n-1" is cancel-capable and has no cancel poll open
    And "alice"'s non-stream request is dispatched to "n-1" as job J
    When "alice" disconnects before the result arrives
    And "n-1" opens a cancel poll 2 seconds later
    Then that poll answers 200 with ids [J]

  Scenario: An undelivered cancel expires instead of piling up
    Given "n-1" is cancel-capable and has no cancel poll open
    And a cancel for job J was queued
    When 2 minutes pass
    Then "n-1"'s next GET /agent/cancels answers 204

  Scenario: The cancel poll returns 204 when nothing is cancelled within the hold
    Given "n-1" is cancel-capable
    When "n-1" opens a cancel poll and nothing is cancelled
    Then the poll answers 204 after the hold

  # --- authorization ------------------------------------------------------------------

  Scenario: A cancel poll without the node's BridgeToken is refused
    When a caller opens GET /agent/cancels?node=n-1 with a wrong token
    Then the status is 401
    And no cancel is consumed

  Scenario: A node's cancel poll never returns another node's job ids
    Given nodes "n-1" and "n-2" are cancel-capable
    And a cancel is queued for "n-2"'s job K
    When "n-1" opens a cancel poll
    Then the poll answers 204

  # --- the node side (internal/agent against real broker handlers) ----------------------

  Scenario: The agent stops its upstream request when its job is cancelled
    Given a cancel-capable agent serving "qwen3-32b" from an upstream that takes 5 seconds
    And "alice"'s non-stream request is dispatched to it as job J
    When "alice" disconnects after 300 ms
    Then the agent's upstream request for J is aborted within 2 seconds
    And the agent posts a result for J with status 499 and no completion
    And the agent keeps serving the next job normally

  Scenario: The agent advertises cancel support on every poll
    Given a cancel-capable agent
    When it polls the broker
    Then the poll carries X-Roger-Cancel "1"

  Scenario: An agent on a broker without /agent/cancels keeps serving and never re-registers for it
    Given an agent connected to a broker that answers 404 on /agent/cancels
    When the agent opens its cancel poll
    Then it backs off before trying again
    And it does not re-register
    And it serves jobs normally

  Scenario: A cancel for a job the agent already finished is a no-op
    Given a cancel-capable agent that finished job J
    When a cancel for J arrives
    Then nothing is aborted and no extra result is posted

  # --- money: unchanged by cancels ----------------------------------------------------------

  Scenario: A cancelled non-stream job bills nothing and earns nothing
    Given "n-1" is cancel-capable
    And "alice"'s non-stream request is dispatched to "n-1" as job J
    When "alice" disconnects before the result arrives
    Then "alice" is billed $0
    And "n-1"'s operator earns nothing for J
    And no owner strike is recorded for J
