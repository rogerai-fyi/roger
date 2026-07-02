# Cross-instance relay correctness (task #52, part 2) - THE SCALE-BACK GATE for
# returning the broker to 2 instances + ROGERAI_MULTI_INSTANCE=1 after the v5.0.0
# launch blocker ("relay-error (result timeout)": jobs relayed on the instance the
# node was NOT polling never reached it, and registrations ping-ponged).
#
# THE CONTRACT (the design the multi-instance layer intends, held end to end here):
# with the flag ON, the job/result/stream rendezvous is the Valkey bus - the SINGLE
# delivery path (no double-serve):
#   - relay on ANY instance subscribes the per-job result channel FIRST, then
#     publishes the job on the node's bus channel (tunnel.go busDispatchJob);
#   - whichever instance holds the node's long-poll delivers the job (agentPoll's
#     bus subscription); delivered==0 -> an HONEST 503 "node busy (no poller free)"
#     with the pre-auth hold refunded - never a hang;
#   - the node's /agent/result + /agent/stream may land on ANY instance (the LB
#     decides): the receiving instance authenticates the node and publishes the
#     result/chunks back on the per-job channel the ORIGINATING instance awaits;
#   - money: the pre-dispatch Hold + the Postgres Settle are the durable truth; a
#     lost rendezvous fails the request CLEANLY (hold released, never a double
#     charge, never a double earning).
#
# GROUND TRUTH: cmd/rogerai-broker/tunnel.go (relay, relayStream, busDispatchJob,
# agentPoll, agentResult, agentStream, tunnelFor, syncRegistry), sharedstore.go (the
# bus), report.go/syncBanRev (revoke coherence), grant.go (grant revoke), store
# Hold/Finalize/ReleaseStaleHolds (money).
#
# Enforced by: cmd/rogerai-broker/cross_instance_bdd_test.go - TWO broker
# instances running the FULL route table over REAL HTTP (httptest servers), ONE REAL
# Valkey (ROGERAI_TEST_REDIS_URL container when set - CI service / local podman -
# else in-process miniredis speaking the real protocol), ONE shared store (real
# Postgres via ROGERAI_TEST_DATABASE_URL when set, else the in-memory reference), a
# REAL long-polling node tunnel, real signed registrations/receipts. No mocks.

Feature: Cross-instance relay correctness at the 2-instance cap

  Background:
    Given two broker instances A and B with the multi-instance flag ON sharing one Valkey and one store

  # --- the rendezvous: owner-instance and foreign-instance relays --------------
  Scenario: a relay on the instance the node polls completes and settles exactly once
    Given node "st-alpha" registers over HTTP on instance A offering paid model "paid-m"
    And a funded logged-in consumer
    And the node long-polls instance A
    When the consumer relays "paid-m" on instance A
    Then the relay responds 200 with the node's completion
    And the consumer is charged exactly once and the operator earns exactly once

  Scenario: a relay accepted by the instance the node is NOT polling still produces the result
    Given node "st-alpha" registers over HTTP on instance A offering paid model "paid-m"
    And a funded logged-in consumer
    And the node long-polls instance A
    When the consumer relays "paid-m" on instance B
    Then the relay responds 200 with the node's completion
    And the consumer is charged exactly once and the operator earns exactly once

  Scenario: a free-model relay crosses instances without any charge
    Given node "st-alpha" registers over HTTP on instance A offering free model "free-m"
    And the node long-polls instance A
    When an anonymous signed consumer relays "free-m" on instance B
    Then the relay responds 200 with the node's completion

  # --- the LB splits the node's traffic: result/stream land on either instance -
  Scenario: a result posted to the OTHER instance still reaches the originating relay
    Given node "st-alpha" registers over HTTP on instance A offering free model "free-m"
    And the node long-polls instance A but posts results to instance B
    When an anonymous signed consumer relays "free-m" on instance A
    Then the relay responds 200 with the node's completion

  Scenario: a streaming relay on the foreign instance receives the SSE chunks in order
    Given node "st-alpha" registers over HTTP on instance A offering free model "free-m"
    And the node long-polls instance A and streams its answer chunk by chunk
    When an anonymous signed consumer relays "free-m" on instance B with streaming on
    Then the stream delivers the chunks in order followed by the done marker

  # The node registers with the PLAIN register step here (no heartbeat, no sweep), so
  # instance B genuinely has not learned it yet - the exact ~one-sync-tick window a
  # fresh registration (or a just-restarted peer) lives in. /agent/poll,/agent/result
  # heal this window by lazily learning the node from the shared registry; the stream
  # leg must too, or the LB sends the chunks into a 401 and the client gets an empty
  # stream.
  Scenario: stream chunks posted to the instance that never saw the node still authenticate and flow
    Given node "st-alpha" registers over HTTP on instance A
    And instance B has not yet run a registry sync
    And the node long-polls instance A and streams its answer to instance B
    When an anonymous signed consumer relays "free-m" on instance A with streaming on
    Then the stream delivers the chunks in order followed by the done marker

  # --- honest failure: no poller anywhere / originator dies mid-job ------------
  Scenario: a relay for a node with no poller on any instance is an honest 503 and the hold is refunded
    Given node "st-alpha" registers over HTTP on instance A offering paid model "paid-m"
    And a funded logged-in consumer
    And no poller is attached to the node on any instance
    When the consumer relays "paid-m" on instance B
    Then the relay responds 503 saying the node is busy
    And the consumer's balance is unchanged

  # "The originator dies mid-job": in-process we model the death as instance B's relay
  # giving up before the result returns (its in-flight waiter is gone - the same state a
  # SIGKILL leaves, whose hold-reclaim half is pinned separately by the deploy-orphan
  # sweep specs). The money contract: the hold is returned in FULL, no charge and no
  # earning ever lands, and the node's late result is absorbed at-most-once w/o error.
  Scenario: the originating instance giving up mid-job never strands the consumer's money
    Given node "st-alpha" registers over HTTP on instance A offering paid model "paid-m"
    And a funded logged-in consumer
    And the node long-polls instance A but delays its answer past the relay window
    When the consumer relays "paid-m" on instance B
    Then the relay gives up with a clean timeout
    And the node's late result is absorbed without error
    And the consumer's balance is unchanged
    And the operator has earned nothing
    And the stale-hold sweep finds no orphaned hold

  # --- the tunnel moves: node reconnects to the other instance -----------------
  Scenario: a node that reconnects its tunnel to the other instance keeps serving relays
    Given node "st-alpha" registers over HTTP on instance A offering free model "free-m"
    And the node moves its long-poll to instance B
    When an anonymous signed consumer relays "free-m" on instance A
    Then the relay responds 200 with the node's completion

  # --- registrations stay pinned while relays flow (no ping-pong) --------------
  Scenario: heartbeats on both instances stay accepted while relays flow cross-instance
    Given node "st-alpha" registers over HTTP on instance A offering free model "free-m"
    And the node long-polls instance A
    When an anonymous signed consumer relays "free-m" on instance B
    And the node heartbeats instance A and instance B
    Then the relay responds 200 with the node's completion
    And every heartbeat is answered 200

  # --- 8c3f9e1 stays pinned under relay load -----------------------------------
  Scenario: a stale shared mirror never regresses the serving registration mid-operation
    Given node "st-alpha" registers over HTTP on instance A offering free model "free-m"
    And the shared registry is poisoned with a strictly older registration for "st-alpha" carrying a stale token
    And the node's local-register grace has lapsed on instance A
    And both instances run their liveness sync sweep
    And the node long-polls instance A
    When an anonymous signed consumer relays "free-m" on instance B
    Then the relay responds 200 with the node's completion

  # --- revoke / dispatch coherence ---------------------------------------------
  Scenario: a node banned via instance A stops being picked on instance B within one sync tick
    Given node "st-alpha" registers over HTTP on instance A offering free model "free-m"
    And the node long-polls instance A
    And instance A bans the node
    And both instances run their liveness sync sweep
    When an anonymous signed consumer relays "free-m" on instance B
    Then the relay responds 503 because no node offers the model
    And the node's heartbeat is still answered 200

  Scenario: a grant revoked via instance A is refused on instance B immediately
    Given node "st-alpha" registers over HTTP on instance A offering free model "free-m" bound to an owner
    And the owner mints a grant key for the node via instance A
    And the node long-polls instance A
    When the owner revokes the grant via instance A
    And a consumer relays "free-m" on instance B with the revoked grant
    Then the relay is rejected as unauthorized

  # --- auth parity: the mirror must never weaken node auth ---------------------
  Scenario: a forged bearer is rejected on the foreign instance exactly as on the owner instance
    Given node "st-alpha" registers over HTTP on instance A offering free model "free-m"
    When the node polls instance B with a forged token
    Then the poll is rejected as unauthorized
