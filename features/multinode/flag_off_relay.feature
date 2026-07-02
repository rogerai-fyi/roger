# HONEST DEGRADATION UNDER ROGERAI_MULTI_INSTANCE=0 (the deploy default until the
# scale-back gate re-enables the bus). Since task #52, registrations + liveness
# mirror across processes whenever a shared backend is wired - under BOTH flag
# values - but the job/result/stream DISPATCH bus stays behind the flag. So with
# the flag OFF and two processes behind one LB, a relay that lands on the process
# the node is NOT polling can see the node (mirrored registration) yet cannot
# reach it (no bus): the job sits in that process's local queue that nobody
# drains.
#
# THE CONTRACT for that degraded window: an HONEST, bounded failure - the broker's
# OWN retryable 504 after its Cloudflare-aware provider-wait, with the consumer's
# pre-dispatch hold refunded IN FULL. Never a hang, never a stranded hold, never a
# charge or an operator earning for undelivered work - and the node's own process
# keeps serving normally (the money ledger shows exactly ONE charge and ONE
# earning, from the same-process relay).
#
# GROUND TRUTH: cmd/rogerai-broker/tunnel.go (relay's single-instance local
# dispatch branch + the nonStreamRelayWait 504 + the deferred hold release),
# sharedstore.go (registry mirror vs bus gates), store Hold/ReleaseStaleHolds.
#
# Enforced by: cmd/rogerai-broker/cross_instance_bdd_test.go (TestFlagOffRelayBDD)
# - the SAME two-process real-HTTP harness as cross_instance_relay.feature (real
# Valkey/miniredis, one shared store, a real long-polling node, real signed money).

Feature: Honest cross-process degradation with the multi-instance flag OFF

  Scenario: a foreign-process relay fails honestly with the hold refunded while the owner process keeps serving
    Given two broker processes A and B with the multi-instance flag OFF sharing one Valkey and one store
    And node "st-alpha" registers over HTTP on instance A offering paid model "paid-m"
    And a funded logged-in consumer
    And the node long-polls instance A
    When the consumer relays "paid-m" on instance B
    Then the relay gives up with a clean timeout
    When the consumer relays "paid-m" on instance A
    Then the relay responds 200 with the node's completion
    And the consumer is charged exactly once and the operator earns exactly once
    And the stale-hold sweep finds no orphaned hold
