# Node-liveness churn (task #52, part 1) - the "broker restarted - re-registered node
# <id> every ~10s" storm seen LIVE on 2026-07-02 against a stable broker.
#
# ROOT CAUSE (the exact read/write asymmetry, pinned by the two-process scenarios below):
# with a shared backend wired (ROGERAI_REDIS_URL set), the LIVENESS layer is
# flag-independent - markSeen write-throughs last_seen to Valkey and syncLiveness merges
# it back on every tick, gated ONLY on `shared != nil` (tunnel.go markSeen /
# syncLivenessOnce; main.go launches syncLiveness whenever shared is wired) - but the
# NODE-REGISTRY mirror was gated on ROGERAI_MULTI_INSTANCE=1 as well:
#   - WRITE: register/rehydrate published the registration (putNode) only when
#     multiInstance (tunnel.go register + rehydrateNodes),
#   - READ: tunnelFor lazy-learned a locally-unknown node from the shared registry and
#     syncRegistry mirrored peers only when multiInstance (tunnel.go tunnelFor /
#     syncLivenessOnce).
# So under ROGERAI_MULTI_INSTANCE=0 with MORE THAN ONE broker process sharing the Valkey
# (a scale-down that has not fully applied, a rolling-deploy overlap, or an instance
# count drifting from the flag - production's app.yaml pins instance_count:2), the peer
# instance answers every heartbeat/poll for the other instance's nodes with 404 "unknown
# node"; the node's self-healing re-registrar re-registers with a ROTATED bridge token,
# which lands on ONE instance and makes the OTHER stale -> alternating 401s -> a
# re-register on EVERY ~10s heartbeat, forever. Registrations ping-pong and cross-
# instance relay results are impossible - the v5.0.0 launch symptom.
#
# THE FIX (read/write symmetry under BOTH flag values): the registry mirror follows the
# SAME gate as the liveness layer - `shared != nil` - on BOTH sides (register/rehydrate
# publish + tunnelFor lazy-learn + the syncRegistry tick + the attestation-lapse
# republish). The dispatch BUS (jobs/results/streams) stays behind ROGERAI_MULTI_INSTANCE
# - the flag keeps meaning exactly "relay dispatch goes over the Valkey bus", and the
# single-instance relay hot path stays byte-for-byte free of Valkey round-trips.
#
# GROUND TRUTH: cmd/rogerai-broker/tunnel.go (register, rehydrateNodes, markSeen,
# syncLivenessOnce, syncRegistry, tunnelFor, heartbeat, agentPoll),
# internal/agent/agent.go (reregistrar.recover: 404/401/403 -> rotate token +
# re-register; heartbeat every 10s). The node is NEVER "told to re-register" =
# heartbeat/poll never answer 404/401/403 to the live token.
#
# Enforced by: cmd/rogerai-broker/liveness_churn_bdd_test.go - real broker instances
# over REAL HTTP (httptest servers running the full route table), a REAL Valkey
# (ROGERAI_TEST_REDIS_URL container when set - CI service / local podman - else an
# in-process miniredis speaking the real protocol), real signed registrations, and a
# node loop that mirrors internal/agent's reregistrar exactly. No mocks.

Feature: Node-liveness stability under every (flag, instance-count) combination

  # ---------------------------------------------------------------------------
  # The founder's literal case: single broker, flag=0. Register + heartbeat over
  # more than two sweep cycles -> the node must NEVER be told to re-register.
  # ---------------------------------------------------------------------------
  Scenario: flag=0 single broker never tells a heartbeating node to re-register
    Given a single broker with the shared backend wired and the multi-instance flag OFF
    And node "st-alpha" registers over HTTP
    When the node heartbeats every beat across more than two sweep cycles
    Then every heartbeat is answered 200
    And the node is never told to re-register

  Scenario: flag=0 single broker keeps answering the node's polls
    Given a single broker with the shared backend wired and the multi-instance flag OFF
    And node "st-alpha" registers over HTTP
    When a consumer relays "free-m" while the node polls the broker for work
    Then the poll authenticates and delivers the job

  Scenario: flag=0 sweeps never evict or rotate a live node's tunnel token
    Given a single broker with the shared backend wired and the multi-instance flag OFF
    And node "st-alpha" registers over HTTP
    When the broker runs its liveness sync sweep repeatedly
    Then the broker still holds the node's tunnel under its registered bridge token

  # ---------------------------------------------------------------------------
  # The symmetric case: single broker, flag=1 (bus on), including past the
  # local-register grace where syncRegistry reconciles from the shared registry.
  # ---------------------------------------------------------------------------
  Scenario: flag=1 single broker never tells a heartbeating node to re-register
    Given a single broker with the shared backend wired and the multi-instance flag ON
    And node "st-alpha" registers over HTTP
    And the node's local-register grace has lapsed
    When the node heartbeats every beat across more than two sweep cycles
    Then every heartbeat is answered 200
    And the node is never told to re-register

  # ---------------------------------------------------------------------------
  # THE PRODUCTION CHURN (RED before the fix): flag=0 but a SECOND broker process
  # shares the same Valkey + store behind one load balancer. This is the state a
  # scale-down drift / rolling deploy overlap produces. The liveness layer is
  # shared; the registry mirror was not - the peer 404s every request it receives
  # and every recover() rotation re-poisons the other instance.
  # ---------------------------------------------------------------------------
  Scenario: flag=0 with two broker processes must not demand re-registers from a round-robined heartbeat
    Given two broker processes A and B with the multi-instance flag OFF sharing one Valkey and one store
    And node "st-alpha" registers over HTTP on instance A
    When the node heartbeats alternating between A and B across more than two sweep cycles
    Then every heartbeat is answered 200
    And the node is never told to re-register

  Scenario: flag=0 with two broker processes authenticates the node's poll on the peer
    Given two broker processes A and B with the multi-instance flag OFF sharing one Valkey and one store
    And node "st-alpha" registers over HTTP on instance A
    When a consumer relays "free-m" on instance B while the node polls instance B for work
    Then the poll authenticates and delivers the job

  Scenario: flag=0 with two broker processes converges after a token-rotating re-register instead of ping-ponging
    Given two broker processes A and B with the multi-instance flag OFF sharing one Valkey and one store
    And node "st-alpha" registers over HTTP on instance A
    And the node re-registers once with a rotated token on instance B and the register grace lapses
    When the node heartbeats alternating between A and B across more than two sweep cycles
    Then at most one heartbeat demands a re-register
    And the last four heartbeats are answered 200

  # The WRITE half of the symmetry: a flag=0 register must reach the shared registry
  # (and a flag=0 restart must re-publish it) so a peer/scale-out can learn the node.
  Scenario: flag=0 register publishes the registration to the shared registry
    Given a single broker with the shared backend wired and the multi-instance flag OFF
    When node "st-alpha" registers over HTTP
    Then the shared registry holds node "st-alpha" with its bridge token

  Scenario: flag=0 rehydrate re-publishes persisted registrations to the shared registry
    Given a persisted registration for node "st-alpha" in the store
    And a single broker with the shared backend wired and the multi-instance flag OFF
    When the broker rehydrates its node registry at startup
    Then the shared registry holds node "st-alpha" with its bridge token

  # ---------------------------------------------------------------------------
  # The scale-back gate's registration half (flag=1, two instances): heartbeats
  # landing on the peer must not demand re-register, and one rotation converges.
  # ---------------------------------------------------------------------------
  Scenario: flag=1 with two instances never demands re-registers from a round-robined heartbeat
    Given two broker processes A and B with the multi-instance flag ON sharing one Valkey and one store
    And node "st-alpha" registers over HTTP on instance A
    When the node heartbeats alternating between A and B across more than two sweep cycles
    Then every heartbeat is answered 200
    And the node is never told to re-register

  Scenario: flag=1 with two instances converges after a token-rotating re-register
    Given two broker processes A and B with the multi-instance flag ON sharing one Valkey and one store
    And node "st-alpha" registers over HTTP on instance A
    And the node re-registers once with a rotated token on instance B and the register grace lapses
    When the node heartbeats alternating between A and B across more than two sweep cycles
    Then at most one heartbeat demands a re-register
    And the last four heartbeats are answered 200

  # ---------------------------------------------------------------------------
  # 8c3f9e1 regression pin, end to end over the real backend: a STALE shared
  # mirror (strictly older registration TS) must never regress a fresh local
  # registration once the grace lapses - and the shared registry must HEAL.
  # ---------------------------------------------------------------------------
  Scenario: a stale shared mirror never regresses a fresher local registration and the mirror heals
    Given a single broker with the shared backend wired and the multi-instance flag ON
    And node "st-alpha" registers over HTTP
    And the shared registry is poisoned with a strictly older registration for "st-alpha" carrying a stale token
    And the node's local-register grace has lapsed
    When the broker runs its liveness sync sweep repeatedly
    Then the broker still holds the node's tunnel under its registered bridge token
    And the shared registry holds node "st-alpha" with its bridge token
