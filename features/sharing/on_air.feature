# SHARING / GOING ON AIR: the provider side. An operator toggles a model "on air" so the
# broker will route traffic to it; the node registers + heartbeats to stay live, is capped by a
# MAX-ON-AIR limit (per node and per owner), and drops off the dial when it stops heartbeating
# or toggles off. This is what makes a GPU a station on the marketplace.
#
# GROUND TRUTH:
#   internal/node/controller.go: ToggleOnAir(model) flips a model on/off air; atLimitLocked()
#     blocks a new on-air past MaxOnAir() (the deliberate cap); onAirCountLocked() counts live.
#   cmd/rogerai-broker/tunnel.go: register() admits a node + its offers; heartbeat() refreshes
#     lastSeen (a node past nodeTTL is dropped from routing/discovery); ownerOnAirCount(owner)
#     enforces the per-owner on-air cap (anti-spam: one operator can't flood the dial).
#
# Enforced by: internal/node/controller_test.go + the broker register/heartbeat tests. (Doc
# spec; convertible to an executable godog suite like relay/auth once a broker+node harness exists.)

Feature: Sharing — going on air

  Scenario: Toggling a model on air makes it routable
    Given an operator with model "gpt-oss-20b" detected locally
    When the operator toggles "gpt-oss-20b" on air
    And the node registers with the broker
    Then "gpt-oss-20b" appears as a live station the broker can route to

  Scenario: Toggling off air removes the station from routing
    Given an operator is on air for "gpt-oss-20b"
    When the operator toggles it off air
    Then the broker no longer routes "gpt-oss-20b" to that node

  Scenario: A node that stops heartbeating drops off the dial
    Given a node on air for "gpt-oss-20b" that was last seen just now
    When more than nodeTTL passes with no heartbeat
    Then the node is dropped from routing and discovery (stale)
    And a fresh heartbeat brings it back

  # --- the on-air cap (deliberate, anti-spam) ---------------------------------
  Scenario: A node cannot exceed its MaxOnAir cap
    Given a node whose MaxOnAir is 1 and is already on air for "gpt-oss-20b"
    When the operator tries to toggle a second model on air
    Then the toggle is refused (at the on-air limit)
    And the first model stays on air

  Scenario: An operator cannot flood the dial past the per-owner on-air cap
    Given an operator already at the per-owner on-air cap across their nodes
    When they bring another model on air
    Then the broker's ownerOnAirCount blocks it (one operator can't monopolize the dial)

  # --- registration ----------------------------------------------------------
  Scenario: Registration admits the node's offers (model + prices)
    When a node registers offering "gpt-oss-20b" at in $0.20/1M, out $0.30/1M
    Then the broker records the node + its priced offer
    And the offer is eligible for routing (subject to the routing gates)

  Scenario: Free sharing needs no login; earning requires a linked account
    Given an operator shares FREE (no login)
    Then they can go on air and serve traffic
    But to EARN they must link a GitHub account (the payout path)
