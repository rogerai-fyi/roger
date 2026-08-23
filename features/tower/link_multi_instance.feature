# APPROVED SPEC - founder approved 2026-08-22 ("approved", after the live failure below).
# BUILD STATUS: BUILT. The mirror, its PG implementation, and every union-read shipped with this spec.
# Changes to an approved scenario need re-approval; they are not a diff to be reviewed.
#
# Scope: a joined Tower's link to Roger Core when Core itself runs more than one instance
# behind a per-request load balancer.
#
# The live failure this encodes: the first real Tower on the network opened its session on
# one broker instance and its very next inventory push landed on the other, which answered
# "open a session before pushing inventory" - and serve treats the first push as fatal.
# Session liveness, the accepted inventory head, and the relay-plane record lived in
# per-instance memory on the assumption that a Tower "holds one connection, to one
# instance"; a per-request load balancer makes that assumption false on every request.
# Same class as the /discover registry split: mirror to the shared store, read the union,
# write idempotently.

Feature: A Tower's link survives Roger Core's own scaling
  Which instance answers a request is a deployment detail. A Tower that is linked is
  linked to Roger Core, not to one process's memory.

  Background:
    Given Roger Core runs two instances sharing one store

  Scenario: Opening on one instance and pushing inventory to the other
    Given a registered Tower opens its link session on instance A
    When it pushes a signed inventory to instance B
    Then the inventory is accepted
    And the Tower is live on instance A and on instance B

  Scenario: The relay plane resolves from the instance that never met the Tower
    Given a registered Tower opens its link session on instance A with a relay plane
    When a node attach or consumer authorization asks instance B for that Tower's plane
    Then instance B resolves the same endpoint and pin instance A recorded

  Scenario: A heartbeat lands on the other instance and still counts
    Given a registered Tower opens its link session on instance A
    When its heartbeat lands on instance B
    And the freshness window passes on instance A alone
    Then the Tower is still live on both instances

  Scenario: One instance restarting does not sever the link
    Given a registered Tower opens its link session on instance A
    When instance A restarts empty and the Tower's next heartbeat lands on it
    Then the heartbeat is accepted from the shared record
    And the Tower remains live

  Scenario: A close lands on the instance that never held the session
    Given a registered Tower opens its link session on instance A
    When its deliberate close lands on instance B
    Then the Tower is live on neither instance
    And a later close quoting a superseded session cannot dim the newer link

  Scenario: The shared store failing leaves each instance honest, not generous
    Given a registered Tower opens its link session on instance A
    And the shared store stops answering
    When instance B is asked whether the Tower is live
    Then instance B answers from what it can actually see
    And no instance invents liveness it cannot verify

  Scenario: Session state in the store cannot be forged by another tower
    Given two registered Towers each open a session on different instances
    When each pushes inventory and heartbeats through either instance
    Then each Tower's liveness, head, and plane are its own
    And neither can advance or expire the other's record
