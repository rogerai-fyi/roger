# Share-node re-registration loop (2026-11-17..19): "broker restarted - re-registered node <id>"
# 45 to 86 times a minute on one station after a power outage, and a burst of 1 to 8 on every
# normal restart of any share, against a broker that had NOT restarted.
#
# ROOT CAUSE (code evidence, reproduced by cmd/rogerai-broker/rereg_loop_test.go):
#   1. The node rotates its bridge token on EVERY re-register (internal/agent/agent.go,
#      reregistrar.recover: newTok := randHex(16)), and re-registers on the FIRST 404/401/403
#      from any node-facing call (brokerForgot, no confirmation, no pacing on success).
#   2. With more than one broker process sharing the registry, a rotated token reaches the
#      instance that took the register at once (register mirrors it before answering 200), but
#      a PEER that already holds a tunnel for the node authenticates against its CACHED token:
#      tunnelFor reads the shared registry through on a local MISS and never on a MISMATCH
#      (cmd/rogerai-broker/tunnel.go tunnelFor, heartbeat, agentPoll, agentResult, agentStream).
#      The peer adopts the new token only on its next sync tick, and NOT AT ALL while its own
#      local-register grace (syncLocalRegisterGrace, 15 s) is open (applyRegistry).
#   3. So the peer answers 401, the node rotates again, the new register lands on one instance
#      and leaves the other stale. Once a register has landed on each instance within 15 s,
#      BOTH are inside their grace, each pins a different token, every refused request mints a
#      third, and every register re-arms the grace where it lands: the loop is self-sustaining.
#      Restarting the share puts the node back in the transient regime, which usually escapes.
#   The existing liveness_churn.feature convergence scenarios wait the grace out first ("the
#   register grace lapses"), which is exactly the window this loop lives in.
#
# THE FIX (two independent halves; either alone stops the loop, both are wanted):
#   NODE:   a re-register is idempotent. It re-sends the session's CURRENT bridge token. A fresh
#           token is minted only when a process starts (agent.Start), never on recover.
#   BROKER: on a token MISMATCH for a node it already knows, an instance with a shared registry
#           re-reads that node's shared registration once and adopts it when it is not older
#           than the local one and its token equals the presented token. The superseded token is
#           still refused (there is NO accept-previous-token window: it would not help, because
#           the stale peer never knew the new token, and it would widen what a leaked token buys).
#           The local-register grace never blocks a strictly newer shared registration.
#
# Enforced by: cmd/rogerai-broker/rereg_loop_bdd_test.go (broker + end to end, real HTTP + real
# store protocol) and internal/agent/rereg_loop_bdd_test.go (node), each running its scenarios
# by line; cmd/rogerai-broker/rereg_loop_test.go adds field agents and the real agent end to end.

Feature: A share node is never told to re-register by a broker instance that is merely behind

  Background:
    Given two broker processes A and B sharing one registry and one store

  # ---------------------------------------------------------------------------
  # Broker: read-through on mismatch (the peer that is behind catches up in-line)
  # ---------------------------------------------------------------------------
  Scenario Outline: a peer that cached the previous token accepts the node's current token
    Given node "st-pico" registered token T0 on A and B learned T0
    And the node re-registered token T1 on B
    And <sync state on A>
    When the node calls <endpoint> on A with token T1
    Then A answers <status>
    And A now holds token T1 for the node

    Examples:
      | sync state on A                                    | endpoint         | status |
      | A has not run a sync tick since                    | heartbeat        | 200    |
      | A has not run a sync tick since                    | poll             | 204    |
      | A has not run a sync tick since                    | result           | 200    |
      | A has not run a sync tick since                    | stream           | 200    |
      | A ran a sync tick inside its own register grace    | heartbeat        | 200    |
      | A ran a sync tick inside its own register grace    | poll             | 204    |
      | A ran a sync tick after its register grace lapsed  | heartbeat        | 200    |

  Scenario: the same holds for a private band (separate namespace) without leaking it
    Given node "st-priv" registered token T0 on A as a private band and B learned T0
    And the node re-registered token T1 on B
    When the node heartbeats A with token T1
    Then A answers 200
    And the node is absent from A's public discovery

  Scenario Outline: tokens that are not the current one are still refused
    Given node "st-pico" registered token T0 on A and B learned T0
    And the node re-registered token T1 on B
    When a caller heartbeats <instance> with <token>
    Then <instance> answers 401

    Examples:
      | instance | token                         |
      | A        | a forged token                |
      | B        | the superseded token T0       |
      | A        | an empty bearer               |
      | A        | T1 with different letter case |
      | A        | T1 with a trailing space      |

  Scenario: a superseded token is refused even after the peer catches up
    Given node "st-pico" registered token T0 on A and the node re-registered token T1 on B
    And A has adopted T1
    When a caller heartbeats A with token T0
    Then A answers 401

  Scenario: read-through never adopts an older shared registration
    Given node "st-pico" holds a registration on A with a newer timestamp than the shared copy
    When a caller presents the shared copy's token to A
    Then A answers 401
    And A keeps its newer registration
    And the shared registry is healed to A's newer registration

  Scenario: read-through when the shared registry is unreachable degrades to today's answer
    Given node "st-pico" registered token T0 on A and B learned T0
    And the node re-registered token T1 on B
    And the shared registry is unreachable from A
    When the node heartbeats A with token T1
    Then A answers 401
    And A does not crash or block longer than the store timeout

  Scenario: read-through is bounded against token guessing
    Given node "st-pico" registered token T0 on A
    When a caller heartbeats A 1000 times in one second with random tokens
    Then every answer is 401
    And A reads the shared registry for "st-pico" at most once per read-through interval

  Scenario: an unknown node is still 404 on every instance
    When a caller heartbeats A for node "never-registered"
    Then A answers 404

  Scenario: the local register grace never blocks a strictly newer peer registration
    Given node "st-pico" registered token T0 on A at second S
    And the node re-registered token T1 on B at second S+1
    When A runs one sync tick inside its register grace
    Then A holds token T1 for the node

  Scenario: the local register grace still protects a fresh local token from an older shared read
    Given node "st-pico" registered token T1 on A at second S+1
    And an in-flight sync on A read the shared registry when it still held T0 at second S
    When that sync applies
    Then A still holds token T1 for the node

  Scenario: an out-of-order register never regresses the token
    Given node "st-pico" registered token T1 on A at second S+1
    When a register for the same node with token T0 at second S arrives on A
    Then A still holds token T1 for the node

  # ---------------------------------------------------------------------------
  # Node: re-register is idempotent (the token does not rotate on recover)
  # ---------------------------------------------------------------------------
  Scenario Outline: recover re-sends the current token
    Given a running share whose live token is T0
    When the share recovers <n> times
    Then every re-register carried token T0
    And the live token is still T0
    And the generation advanced <n> times

    Examples:
      | n |
      | 1 |
      | 3 |

  Scenario: a fresh share process mints a fresh token
    Given a share process stopped with token T0
    When a new share process starts for the same node id
    Then it registers a token different from T0

  Scenario: concurrent pollers and the heartbeat seeing one refusal cause one re-register
    Given a running share with 4 pollers and a heartbeat at generation N
    When all five see 401 for generation N
    Then exactly one re-register is sent

  Scenario: a refusal for a request sent before the last re-register triggers nothing
    Given a poller sent a request with generation N
    And the share re-registered to generation N+1 meanwhile
    When that request comes back 401
    Then no re-register is sent

  Scenario: back-to-back recoveries are paced
    Given a share that re-registered successfully less than the minimum re-register interval ago
    When a poller sees 401 for the new generation
    Then the next re-register waits until the interval has passed
    And the live token is unchanged

  Scenario: confidential re-attestation still happens on every re-register
    Given a confidential share whose live token is T0
    When the share recovers once
    Then the re-register carried a fresh attestation nonce and quote
    And it carried token T0

  # ---------------------------------------------------------------------------
  # End to end: the production loops, reproduced
  # ---------------------------------------------------------------------------
  Scenario: a restart against two instances settles after at most one re-register
    Given node "st-pico" has been on air with token Told on A and B long past every grace
    When the share restarts and registers a new token on A
    And its requests alternate B, A, B, A with a sync tick after every pair, for ten requests
    Then the node re-registers at most once
    And the last request is answered 200

  Scenario: both instances inside their grace with different tokens still converge
    Given node "st-pico" registered token T1 on A and token T2 on B within the same 15 seconds
    And the share's live token is T2
    When the share's requests alternate A, B for ten requests with no grace lapse
    Then the node re-registers at most once
    And every instance holds T2

  Scenario: a node unknown on every instance still recovers with one re-register
    Given the shared registry and both instances have forgotten node "st-pico"
    When the node heartbeats A
    Then A answers 404
    And the node re-registers once with its current token
    And its next heartbeat on B answers 200

  # ---------------------------------------------------------------------------
  # Out of scope here, tracked for a founder decision: TWO live processes for one node id
  # (same node key) would still alternate tokens, because each process holds its own. The
  # on-air lock (internal/onair) is read-then-write and per user config dir, so a boot-time
  # race can let two processes through. Candidate follow-ups: an exclusive create/flock lock,
  # and a broker "token flapping" detector that answers 409 to the loser.
  # ---------------------------------------------------------------------------
