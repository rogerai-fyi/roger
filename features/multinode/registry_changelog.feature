# APPROVED by the founder 2026-09-27 (spec-first workflow step 3). Do not edit a scenario
# without re-approval. Phase 4 of the dispatch-scale design.
#
# THE COST: every instance re-reads whole sets every 5 s tick, with no revision check except
# bans: SMEMBERS regset + one GET per public node, SMEMBERS pregset + one GET per private node,
# HGETALL toolsok, SMEMBERS coolset + one GET per cooled station. Fleet cost per tick is
# (nodes x instances) even when nothing changed, and those global sets cannot be sharded.
#
# THE DESIGN: an append-only change log, one stream `rogerai:ctl:changes`. Every writer that
# already writes a registration, a drop, a cooldown or a tools verdict also appends one entry.
# Each instance keeps the last entry id it applied and, on its tick, reads only entries after
# it (non-blocking, bounded). A full snapshot happens at boot, when the log was trimmed past
# the instance's position, and as a slow safety-net reconcile (every 60 s) that also covers
# instances still running code that does not log.
#
# OUT OF SCOPE (rate data, not change data): liveness last_seen, inflight and edge-load
# counts, instance presence. They change on every heartbeat or request, so a log would cost
# the same as reading them. Bans keep their existing revision counter.
#
# DECISIONS:
#   C1  A tick with no changes costs a constant number of store commands, whatever the fleet.
#   C2  A change on one instance is applied on every other instance by its next tick.
#   C3  Every invariant of liveness_churn, discover_liveness, discovery_union, private_bands
#       and the attestation mirror still holds (those features stay green, unchanged).
#   C4  A gap (trimmed log, store restart, missed entries) is never silent: it forces a full
#       snapshot, never a partial view.
#   C5  The store unreachable degrades to the local registry, as today.
#
# ENFORCED BY (planned): cmd/rogerai-broker/registry_changelog_bdd_test.go, real miniredis or
# ROGERAI_TEST_REDIS_URL, two and three instances, no mocks.

Feature: Instances share registry changes through a change log, not a full re-read
  Background:
    Given a multi-instance broker of two instances sharing one store

  # C1
  Scenario: A quiet fleet costs a constant amount per tick
    Given 500 public and 50 private nodes registered across both instances
    And both instances have synced once
    When 10 ticks pass with no registration, cooldown or tools change
    Then each tick costs at most 6 store commands per instance

  # C2: each change kind
  Scenario: A registration on A is routable on B after one tick
    When node "n1" registers on instance A
    And instance B ticks once
    Then instance B can pick "n1" and authenticate its poll

  Scenario: A token rotation converges on the peer
    Given node "n1" is registered on both instances
    When "n1" re-registers on instance A with a rotated token
    And instance B ticks once
    Then instance B authenticates "n1" only with the new token

  Scenario: A public band turned private leaves the public view everywhere
    Given node "n1" is registered publicly on both instances
    When "n1" re-registers on instance A as a private band
    And instance B ticks once
    Then "n1" is absent from instance B's public discover
    And "n1" is still routable on instance B for its owner

  Scenario: A private band turned public appears everywhere
    Given node "n1" is registered as a private band on both instances
    When "n1" re-registers on instance A as a public band
    And instance B ticks once
    Then "n1" appears in instance B's public discover

  Scenario: A station cooled on A is cooled on B
    Given node "n1" is registered on both instances
    When instance A cools "n1" for 60 s after an upstream 429
    And instance B ticks once
    Then instance B does not route to "n1" until the cooldown ends

  Scenario: A tools verdict and its clearing propagate
    Given node "n1" is registered on both instances
    When instance A marks "n1" tool-capable for "m1"
    And instance B ticks once
    Then instance B lists "n1" as tool-capable for "m1"
    When instance A clears that verdict on an authoritative regression
    And instance B ticks once
    Then instance B no longer lists it

  # C3: the churn and staleness invariants
  Scenario: A stale log entry never regresses a fresher local registration
    Given node "n1" registered on instance B more recently than the entry instance A logged
    When instance B applies A's older entry
    Then instance B keeps its fresher registration
    And the shared registry is healed to the fresher one

  Scenario: A registration inside the local grace window is not overwritten by the log
    Given node "n1" registered on instance B 2 s ago
    When instance B applies an entry for "n1" from instance A
    Then instance B keeps its own registration

  Scenario: Applying the same entry twice changes nothing
    When instance B applies the same registration entry twice
    Then instance B's registry holds one copy of the node

  # C4: gaps
  Scenario: A log trimmed past an instance's position forces a full snapshot
    Given instance B has applied the log up to some entry
    And the log is trimmed past that entry
    When instance B ticks
    Then instance B takes a full snapshot
    And its registry matches the shared registry exactly

  Scenario: A new instance boots from a snapshot, then tails the log
    Given 20 nodes registered across instances A and B
    When a third instance starts
    Then it serves all 20 after its first tick
    And a registration on instance A afterwards reaches it by the log

  Scenario: A change written by an instance that does not log it is caught by the reconcile
    Given instance A runs code that writes registrations without logging them
    When node "n1" registers on instance A
    Then instance B can route to "n1" within the 60 s reconcile interval

  Scenario: A store restart that empties the log is treated as a gap
    Given instance B has applied the log up to some entry
    When the store restarts empty and nodes re-register
    Then instance B takes a full snapshot on its next tick

  # C5
  Scenario: An unreachable store degrades to the local registry
    Given node "n1" is registered on instance B
    When the store becomes unreachable
    Then instance B still routes to "n1"
    And its tick returns without blocking the request path
