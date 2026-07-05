# Multi-instance DISCOVERY UNION - the public /discover + /market + /admin-live
# on-air view must be identical on EVERY instance behind the load balancer.
#
# THE PROD BUG (2026-07-04, instance_count=2 + a round-robin LB): a node's tunnel is
# AFFINE to the ONE instance it dialed. The public dial (/discover) is round-robined
# across instances, so an instance WITHOUT a given node's tunnel used to read its own
# empty in-memory registry and report that node ABSENT - the TUNE-IN dial alternated
# "8 online" (the affine instance) <-> "0 online" (the peer) every few seconds.
#
# THE FIX (already shipped as task #52's registry+liveness mirror, PINNED here so it can
# never regress): every instance WRITE-THROUGHs its live registrations to the shared
# store (putNode, keyed by node id, under a TTL refreshed on every heartbeat) and MIRRORS
# the shared registry into its own in-memory b.nodes on the same 5s sync tick that already
# carries liveness (syncRegistry, gated on `shared != nil` - NOT the bus flag). So the
# UNION of every instance's nodes is what /discover, computeMarket and liveMarket all read
# from b.nodes - the peer no longer disagrees. Reads are fail-OPEN: a slow/absent shared
# store degrades each instance to its LOCAL registry (never an empty dial), never blocks
# the hot path on Valkey.
#
# GROUND TRUTH: cmd/rogerai-broker/tunnel.go (register putNode, markSeen, syncRegistry),
# market.go (computeDiscover/computeMarket read b.nodes, skip private/banned/offline),
# admin.go (liveMarket reads b.nodes). Enforced end-to-end over REAL HTTP against real
# broker instances sharing one store + one Valkey (ROGERAI_TEST_REDIS_URL container / CI
# service / local podman, else an in-process miniredis speaking the real protocol). No
# mocks. Registrations are real signed registrations; the mirror is the real sync tick.

Feature: The public marketplace on-air view is the union across every instance

  # ---------------------------------------------------------------------------
  # (1) THE FLICKER: a node registered on A must be ON AIR on the peer B's public
  # /discover after the sync tick - and both instances must AGREE.
  # ---------------------------------------------------------------------------
  Scenario: a node registered on instance A is on air on instance B's public discover
    Given two broker processes A and B with the multi-instance flag OFF sharing one Valkey and one store
    And node "du-alpha" registers over HTTP on instance A and heartbeats
    When both instances run their liveness sync sweep
    Then instance A's public discover shows 1 node online
    And instance B's public discover shows 1 node online
    And instance A and instance B agree on the online count

  # ---------------------------------------------------------------------------
  # (1b) THE 8<->0 DIAL, generalized to a COUNT: many nodes on A -> the peer's
  # dial reports the SAME count, never 0.
  # ---------------------------------------------------------------------------
  Scenario: the peer's dial reports the full online count, not zero
    Given two broker processes A and B with the multi-instance flag OFF sharing one Valkey and one store
    And 5 nodes register over HTTP on instance A and heartbeat
    When both instances run their liveness sync sweep
    Then instance A's public discover shows 5 nodes online
    And instance B's public discover shows 5 nodes online

  # Nodes SPLIT across instances -> BOTH dials show the UNION of all of them.
  Scenario: both instances show the union when nodes are split across A and B
    Given two broker processes A and B with the multi-instance flag OFF sharing one Valkey and one store
    And 2 nodes register over HTTP on instance A and heartbeat
    And 3 nodes register over HTTP on instance B and heartbeat
    When both instances run their liveness sync sweep
    Then instance A's public discover shows 5 nodes online
    And instance B's public discover shows 5 nodes online

  # The dial must be STABLE under the round-robin, never oscillating back to 0.
  Scenario: the peer dial stays stable across repeated round-robin sweeps
    Given two broker processes A and B with the multi-instance flag OFF sharing one Valkey and one store
    And 4 nodes register over HTTP on instance A and heartbeat
    When both instances run their liveness sync sweep
    Then instance B's public discover stays at 4 nodes online across repeated sweeps

  # ---------------------------------------------------------------------------
  # (2) AGE-OUT: a node whose instance stops heart-beating ages out of the peer's
  # on-air view once its last-seen passes the on-air TTL - and stays out.
  # ---------------------------------------------------------------------------
  Scenario: a node that stops heartbeating ages out of the peer's on-air view
    Given two broker processes A and B with the multi-instance flag OFF sharing one Valkey and one store
    And node "du-alpha" registers over HTTP on instance A and heartbeats
    And both instances run their liveness sync sweep
    And instance B's public discover shows 1 node online
    When the node stops heartbeating and its last-seen ages past the on-air TTL
    And both instances run their liveness sync sweep
    Then instance B's public discover shows 0 nodes online

  # ---------------------------------------------------------------------------
  # (3) IDEMPOTENT: re-registering the SAME node id (even with a rotated token)
  # never duplicates it in the peer's dial.
  # ---------------------------------------------------------------------------
  Scenario: re-registering the same node id does not duplicate it on the peer
    Given two broker processes A and B with the multi-instance flag OFF sharing one Valkey and one store
    And node "du-alpha" registers over HTTP on instance A and heartbeats
    And both instances run their liveness sync sweep
    And the node re-registers with a rotated token on instance A
    When both instances run their liveness sync sweep
    Then instance B's public discover shows 1 node online

  # ---------------------------------------------------------------------------
  # (4) FALL-OPEN: a single instance serves its OWN registry, and an unreachable
  # shared store degrades an instance to its LOCAL registry - the dial is NEVER
  # empty and NEVER errors.
  # ---------------------------------------------------------------------------
  Scenario: a single instance serves its local registry
    Given a single broker with the shared backend wired and the multi-instance flag OFF
    And node "du-alpha" registers over HTTP on instance A and heartbeats
    Then instance A's public discover shows 1 node online

  Scenario: an unreachable shared store degrades an instance to its local registry
    Given two broker processes A and B with the multi-instance flag OFF sharing one Valkey and one store
    And node "du-local" registers over HTTP on instance B and heartbeats
    When instance B's shared store becomes unreachable
    Then instance B's public discover still shows 1 node online

  # ---------------------------------------------------------------------------
  # (5) PRIVATE stays private: a band node is mirrored for ROUTING (the peer holds
  # it with identical metrics) yet NEVER appears in the peer's public /discover or
  # /market.
  # ---------------------------------------------------------------------------
  Scenario: a private band is routable on the peer but never in its public discover or market
    Given two broker processes A and B with the multi-instance flag OFF sharing one Valkey and one store
    And node "du-priv" registers over HTTP on instance A as a private band
    And the node heartbeats instance B
    When both instances run their liveness sync sweep
    Then the node is absent from instance B's public discovery
    And the node is absent from instance B's public market
    And instance B holds the band internally with the same offer it registered

  # ---------------------------------------------------------------------------
  # (6) computeMarket + liveMarket reflect the SAME union the dial does.
  # ---------------------------------------------------------------------------
  Scenario: computeMarket on the peer aggregates the union of providers
    Given two broker processes A and B with the multi-instance flag OFF sharing one Valkey and one store
    And 3 nodes register over HTTP on instance A and heartbeat
    When both instances run their liveness sync sweep
    Then instance B's market shows 3 providers for the model

  Scenario: liveMarket on the peer counts the union on air
    Given two broker processes A and B with the multi-instance flag OFF sharing one Valkey and one store
    And 3 nodes register over HTTP on instance A and heartbeat
    When both instances run their liveness sync sweep
    Then instance B's live-market on-air count is 3
