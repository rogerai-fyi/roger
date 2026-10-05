# RUNTIME SHARED-STORE OUTAGE: a multi-instance broker that became ready keeps serving when the
# shared store (Valkey) stops answering, on the stations that long-poll THIS instance.
#
# PURPOSE
#   Founder ruling (2026-10-04): a broker that was ready must keep serving through a runtime
#   Valkey outage instead of answering every request with 503. Cross-instance dispatch needs the
#   store (a job for a station polling a PEER instance can only travel through it), but a station
#   long-polling this instance can be handed the job in memory and its result read back in
#   memory. A boot-time outage is unchanged: the broker is NOT READY until the store answers
#   (features/ops/shared_store_readiness.feature).
#
# GROUND TRUTH (wt/state-main, before this change)
#   - cmd/rogerai-broker/tunnel.go dispatchAwait: in multi-instance mode every job goes through
#     dispatchRemote (dispatchq.go dispatch), whose FIRST step writes the job's origin key to
#     Valkey; a failed write aborts the dispatch with a store error, mapped to
#     dispatchBusErr -> 503 "dispatch bus unavailable" (writeDispatchFailure), even when an idle
#     poller of the station is waiting on this very instance.
#   - tunnel.go agentResult / agentStream: in multi-instance mode a station's result and stream
#     chunks are routed by reading the job's origin from Valkey (dispatchq.go originOf); with
#     the store down the result is refused with 503 "result bus unavailable".
#   - tunnel.go pickFor knows nothing of where a station polls, so with the store down it still
#     picks a station that polls only a peer instance.
#
# RULES PINNED HERE
#   O1 While this instance sees the shared store as unhealthy (the same signal /ready reports as
#      shared "degraded"), a station is eligible on this instance only if it has long-polled
#      this instance within the last 30 seconds (one poll hold plus margin). Stations polling
#      only a peer are skipped by the pick, never dispatched into a dead bus.
#   O2 A job for an eligible station is handed to it in memory on this instance, and its result
#      and stream chunks are read back in memory: no store round trip on the request path.
#   O3 Only when no station of the request can be served this way: 503 with error code
#      "dispatch_bus_unavailable", a Retry-After, X-RogerAI-Cost: 0, and no hold left behind.
#   O4 Money stays exact: the hold, the settle, the price lock and the monthly cap live in the
#      durable store and are enforced as when the shared store is up.
#   O5 Per-instance fallbacks keep their documented outage behavior: rate limits fall back to
#      per-instance buckets (fail open, as today); station cooldowns are per-instance until the
#      store returns; moderation screening is in-process and never blocks or changes serving.
#   O6 When the store answers again, cross-instance dispatch resumes without a restart.
#   Residual, stated honestly: behind a load balancer without affinity, a station's result POST
#   can land on the peer instance while the store is down; that result cannot be routed, so the
#   relay ends at its own deadline (504, hold refunded, nothing billed). Stations keep their
#   connection open between polls, which makes this the uncommon case.
#
# Enforced by: cmd/rogerai-broker/runtime_store_outage_bdd_test.go (two real broker instances
# over one real miniredis, real long-polling stations, real store; no mocks).

Feature: A ready broker keeps serving through a runtime shared-store outage

  Background:
    Given a multi-instance broker of two instances sharing one Valkey
    And a funded consumer

  # --- O1/O2: stations polling this instance keep serving -------------------------------

  Scenario: A station polling this instance serves while the shared store is down
    Given station "near" long-polls instance 1 and posts its results to instance 1
    And the shared store stops answering and instance 1 has noticed
    When the consumer relays through instance 1
    Then the response is 200 served by "near"
    And the consumer was billed exactly the station's price for the reply
    And the station operator earned for it

  Scenario: A station polling only the peer is skipped, and a sibling polling this instance serves
    Given station "near" long-polls instance 1 and posts its results to instance 1
    And station "far" long-polls only instance 2
    And "far" is the better-scored station
    And the shared store stops answering and instance 1 has noticed
    When the consumer relays through instance 1 5 times
    Then every response is 200 served by "near"
    And "far" was never handed a job

  Scenario: A streamed reply is served while the shared store is down
    Given station "near" long-polls instance 1 and posts its results to instance 1
    And the shared store stops answering and instance 1 has noticed
    When the consumer streams through instance 1
    Then the stream carries the station's content and ends with [DONE]
    And the consumer was billed exactly the station's price for the reply

  # --- O3: nothing servable here ---------------------------------------------------------

  Scenario: Only peer-polled stations: a clear 503, no hold, nothing billed
    Given station "far" long-polls only instance 2
    And the shared store stops answering and instance 1 has noticed
    When the consumer relays through instance 1
    Then the response is 503 with error code "dispatch_bus_unavailable"
    And the response carries a Retry-After and X-RogerAI-Cost "0"
    And no hold is left for the consumer and the balance is unchanged

  Scenario: A station that stopped polling this instance more than 30 seconds ago is not used
    Given station "near" last long-polled instance 1 more than 30 seconds ago
    And the shared store stops answering and instance 1 has noticed
    When the consumer relays through instance 1
    Then the response is 503 with error code "dispatch_bus_unavailable"

  # --- O4: money ---------------------------------------------------------------------------

  Scenario: The monthly cap is still enforced while the shared store is down
    Given station "near" long-polls instance 1 and posts its results to instance 1
    And the consumer's monthly cap is already reached
    And the shared store stops answering and instance 1 has noticed
    When the consumer relays through instance 1
    Then the response is 402
    And "near" was never handed a job

  # --- O6: recovery ------------------------------------------------------------------------

  Scenario: Cross-instance dispatch resumes when the shared store answers again, without a restart
    Given station "far" long-polls only instance 2
    And the shared store stops answering and instance 1 has noticed
    And the shared store answers again and instance 1 has noticed
    When the consumer relays through instance 1
    Then the response is 200 served by "far"

  # --- unchanged ---------------------------------------------------------------------------

  Scenario: With the shared store up, a station polling only the peer is served across instances (unchanged)
    Given station "far" long-polls only instance 2
    When the consumer relays through instance 1
    Then the response is 200 served by "far"
