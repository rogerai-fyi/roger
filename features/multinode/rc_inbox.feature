# AWAITING FOUNDER APPROVAL (spec-first workflow step 3) - do NOT write step definitions or
# implementation until approved. Remote-control sessions on the dispatch plane.
#
# TODAY: every viewer's SSE stream holds its own store SUBSCRIBE for its whole life, and the
# host's poll opens one per 25 s poll: 1 + V dedicated store connections per session.
# Pub/sub is fire-and-forget, which leaves real loss windows:
#   - two inbounds arriving during one host poll: the poll returns the first and drops the
#     rest (they counted as delivered, so they were never buffered);
#   - the gap buffer drains one item per poll and expires after 2 min;
#   - frames published before a viewer's subscription is confirmed are lost;
#   - multi-instance never fills the replay ring, so Last-Event-ID is ignored;
#   - seq is INCR then PUBLISH, so two instances can publish out of seq order.
# rc_cross_instance.feature (X1-X4) has no step definitions: it is documentation only.
#
# THE DESIGN (the same pattern as job dispatch):
#   - inbound (viewer -> host): a per-session LIST; the host's poll pops it; the sender wakes
#     the instance where the host's poll waits (a route entry per session), through that
#     instance's inbox. Exactly one taker, nothing dropped, order kept.
#   - outbound (host -> viewers): seq from the shared counter; the frame goes once to each
#     INSTANCE holding viewers of the session (a route entry per session), through its inbox;
#     that instance fans out to its local viewers. A capped shared ring (last 200 frames) per
#     session serves Last-Event-ID replay on any instance.
#   - store connections: none per viewer or per host poll.
#
# DECISIONS:
#   R1  X1-X4 of rc_cross_instance.feature become executable and pass.
#   R2  Every inbound sent while the host is between polls or mid-poll reaches the host
#       exactly once, in send order.
#   R3  Every viewer receives every frame in seq order, whichever instances host and
#       viewers are on.
#   R4  Last-Event-ID replay works on any instance.
#   R5  Store connections do not grow with sessions, viewers or host polls.
#   R6  Single-instance remote control is unchanged.
#
# ENFORCED BY (planned): cmd/rogerai-broker/rc_inbox_bdd_test.go plus step definitions for
# rc_cross_instance.feature; real miniredis or ROGERAI_TEST_REDIS_URL, two and three
# instances, no mocks.

Feature: Remote-control sessions cross instances through inboxes
  Background:
    Given a multi-instance broker of two instances sharing one store
    And a remote-control session whose host polls instance A

  # R2
  Scenario: Two turns sent during one host poll both reach the host, in order
    Given a viewer streaming on instance B
    When the viewer sends turn "one" and then turn "two" within 10 ms
    Then the host receives "one" and then "two"
    And each exactly once

  Scenario: A turn sent while the host is between polls is delivered on the next poll
    Given the host is between polls
    When a viewer on instance B sends turn "late"
    And the host polls instance B
    Then the host receives "late" exactly once

  Scenario: A burst of 20 inbounds drains in order across several polls
    When a viewer on instance B sends 20 turns in a row
    Then the host receives all 20 in send order, each exactly once

  Scenario: An inbound nobody polls for expires rather than lingering forever
    Given the host stops polling
    When a viewer sends a turn
    Then the turn is gone from the store after the inbound expiry

  # R3
  Scenario: Frames reach viewers on every instance in seq order
    Given viewers streaming on instance A and instance B
    When the host posts 50 frames in two concurrent batches through different instances
    Then each viewer receives all 50 frames in increasing seq order, each exactly once

  Scenario: A viewer's own turn is echoed to every viewer
    Given viewers streaming on instance A and instance B
    When the viewer on B sends turn "hi"
    Then both viewers receive the user frame "hi"

  Scenario: An instance with no viewers of the session receives none of its frames
    Given viewers streaming only on instance A
    When the host posts 10 frames
    Then instance B's inbox carries none of them

  Scenario: A viewer that attaches after frames were posted is not sent them twice
    Given the host has posted 5 frames
    When a viewer attaches on instance B without Last-Event-ID
    Then it receives only frames posted after it attached, plus the host's backfill

  # R4
  Scenario: Last-Event-ID replay on a different instance
    Given a viewer on instance A has received frames up to seq 10
    And the host has posted frames up to seq 15
    When the viewer reconnects on instance B with Last-Event-ID 10
    Then it receives seq 11 to 15, then live frames

  Scenario: Replay older than the ring starts from the oldest kept frame
    Given the host has posted 300 frames
    When a viewer reconnects with Last-Event-ID 1
    Then it receives the last 200 frames in order

  # Lifecycle
  Scenario: Revocation ends every viewer stream on every instance
    Given viewers streaming on instance A and instance B
    When the owner disables the session on instance B
    Then both viewer streams end with an ended frame
    And the host's next poll is refused

  Scenario: A viewer that disconnects stops receiving and is dropped from the route
    Given a viewer streaming on instance B
    When the viewer disconnects
    Then instance B leaves the session's viewer route within one refresh

  # R5
  Scenario: Store connections do not grow with sessions or viewers
    Given 50 sessions, each with a polling host and 3 viewers spread over both instances
    Then the store reports at most 2 x (pool size + 1) client connections from the brokers

  # R1 / R6
  Scenario: The cross-instance invariants X1-X4 hold
    Then every scenario of rc_cross_instance.feature passes

  Scenario: Single-instance remote control is unchanged
    Given the broker runs in single-instance mode
    When a viewer sends a turn and the host posts a frame
    Then the host receives the turn and the viewer receives the frame
