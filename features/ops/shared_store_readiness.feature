# SHARED-STORE READINESS AT BOOT: a broker that is configured to share state with its peers
# never serves on per-instance state because the shared store was unreachable when it started.
#
# PURPOSE
#   The broker runs as several instances behind a load balancer and keeps cross-instance state
#   (rate limits, device and email login, cooldowns, price locks, request records, the dispatch
#   bus) in the shared store. Today, if the shared store cannot be reached at the moment an
#   instance boots, that instance logs one line and runs for its whole life with NO shared store:
#   every "if the shared store is nil" branch silently becomes the source of truth, and the load
#   balancer keeps sending it traffic. This feature pins the founder's ruling (2026-10-04): such an
#   instance stays NOT READY (it keeps retrying and the readiness probe says so) until the shared
#   store is reachable, and while not ready it serves nothing that depends on shared state.
#
# GROUND TRUTH (origin/main 12207827)
#   - cmd/rogerai-broker/sharedstore.go:2090 openSharedStore: when a topology is configured
#     (ROGERAI_REDIS_URL / _RING / _CLUSTER, valkeyTopologyFromEnv :599) and the connect fails, it
#     logs "falling back to in-memory (broker continues)" and returns nil. Nothing retries.
#   - cmd/rogerai-broker/main.go:801 b.shared = openSharedStore(), called once; the device flow,
#     the email flow and every named rate limiter are wired onto the shared store only if it is
#     non-nil at that moment (main.go:807-830).
#   - cmd/rogerai-broker/health.go:61 ready: /ready is 503 only when the durable store is down; a
#     nil shared store is "not configured, not a readiness dependency", so a configured-but-
#     unreachable shared store reports ready:true with no "shared" key at all.
#   - cmd/rogerai-broker/main.go:971 /health is unconditional liveness ("ok").
#   - Approved and unchanged: health_cov_test.go TestReadyMethodAndSharedBranches pins that a
#     shared store that WAS connected and later becomes unhealthy reports "shared":"degraded"
#     while staying ready. This feature changes only the boot case; the runtime-outage behavior
#     is listed under "unchanged" below.
#
# RULES PINNED HERE
#   R1 "Configured to share" means ROGERAI_MULTI_INSTANCE=1, or any of ROGERAI_REDIS_URL,
#      ROGERAI_REDIS_RING, ROGERAI_REDIS_CLUSTER is set. With none of them, nothing changes.
#   R2 A configured broker that cannot reach the shared store keeps retrying with capped backoff
#      (first retry within 1 s, doubling, capped at 30 s) for as long as the process runs.
#   R3 Until connected, GET /ready answers 503 {"ready":false,"shared":"connecting"}; GET /health
#      stays 200 (liveness: the process is up and must not be restarted for this).
#   R4 Until connected, every other endpoint answers 503 with error code
#      "shared_store_unavailable", a Retry-After, X-RogerAI-Cost: 0, and does no work: no hold,
#      no dispatch, no ledger row, no login state, no record. Node polls are refused the same way.
#   R5 Once the shared store answers, the broker finishes the shared wiring (device flow, email
#      flow, shared rate limiters, the bus) and becomes ready WITHOUT a restart; from then on it
#      behaves exactly as an instance that connected at boot.
#   R6 The wiring happens once: a later shared-store outage does not undo it (runtime outages
#      keep today's approved behavior).
#
# Enforced by: cmd/rogerai-broker/shared_store_readiness_bdd_test.go

Feature: A broker configured to share state is not ready until the shared store answers

  Background:
    Given a durable store that is reachable
    And a shared-store address that is not answering yet

  # --- R1: who is configured to share ---------------------------------------------------

  Scenario Outline: Each sharing configuration makes the shared store a readiness dependency
    Given the broker is configured with <setting>
    When the broker boots
    Then GET /ready answers 503
    And the readiness body says ready false and shared "connecting"

    Examples:
      | setting                                   |
      | ROGERAI_REDIS_URL pointing at the address |
      | ROGERAI_REDIS_RING pointing at the address |
      | ROGERAI_REDIS_CLUSTER pointing at the address |
      | ROGERAI_MULTI_INSTANCE=1 and ROGERAI_REDIS_URL pointing at the address |

  Scenario: Multi-instance mode with no shared-store address configured is not ready
    Given the broker is configured with ROGERAI_MULTI_INSTANCE=1 and no shared-store address
    When the broker boots
    Then GET /ready answers 503
    And the readiness body says shared "not_configured"

  Scenario: A single-instance broker with no shared store configured is unchanged
    Given the broker is configured with no shared-store setting at all
    When the broker boots
    Then GET /ready answers 200 with ready true
    And the readiness body has no "shared" key
    And a signed chat request is served as it is today

  # --- R2/R3: retry and probes ------------------------------------------------------------

  Scenario: Liveness stays healthy while the broker is waiting for the shared store
    Given the broker is configured with ROGERAI_REDIS_URL pointing at the address
    When the broker boots
    Then GET /health answers 200 "ok"

  Scenario: The broker keeps retrying the shared store with capped backoff
    Given the broker is configured with ROGERAI_REDIS_URL pointing at the address
    When the broker boots and the address stays down for 2 minutes
    Then the broker has attempted the connection more than 4 times
    And no two consecutive attempts were more than 30 seconds apart
    And it logged the outage once, not once per attempt

  Scenario: The broker never gives up retrying
    Given the broker is configured with ROGERAI_REDIS_URL pointing at the address
    When the broker boots and the address stays down for 1 hour
    Then the broker is still retrying
    And GET /ready still answers 503

  # --- R4: nothing is served on per-instance state ----------------------------------------

  Scenario Outline: While not ready, every non-probe endpoint refuses without doing any work
    Given the broker is configured with ROGERAI_REDIS_URL pointing at the address
    When the broker boots
    And a client calls <endpoint>
    Then the response is 503 with error code "shared_store_unavailable"
    And it carries a Retry-After of at most 30 seconds
    And it carries X-RogerAI-Cost "0"
    And no hold, ledger row, dispatch, login record or request record was written

    Examples:
      | endpoint                                         |
      | POST /v1/chat/completions with a funded signature |
      | POST /v1/audio/speech with a funded signature     |
      | GET /discover                                     |
      | GET /market                                       |
      | GET /me                                           |
      | POST /auth/email/start                            |
      | POST /device/code                                 |
      | GET /agent/poll as a registered node              |
      | POST /nodes/register                              |
      | GET /admin/live with the admin key                |

  Scenario: An anonymous request is refused the same way while not ready
    Given the broker is configured with ROGERAI_REDIS_URL pointing at the address
    When the broker boots
    And an anonymous client posts a chat completion for a free station
    Then the response is 503 with error code "shared_store_unavailable"

  Scenario: The refusal body names the condition and never leaks the shared-store address
    Given the broker is configured with ROGERAI_REDIS_URL "redis://user:secret@10.0.0.5:6379" that is not answering
    When the broker boots
    And a client posts a signed chat completion
    Then the response body does not contain "secret"
    And the response body does not contain "10.0.0.5"

  # --- R5: becoming ready without a restart -------------------------------------------------

  Scenario: The broker becomes ready on its own once the shared store answers
    Given the broker is configured with ROGERAI_REDIS_URL pointing at the address
    When the broker boots
    And the shared store starts answering at the address
    Then within 35 seconds GET /ready answers 200 with ready true and shared "ok"
    And the broker was not restarted

  Scenario: After becoming ready, a signed chat request is served and billed normally
    Given the broker is configured with ROGERAI_REDIS_URL pointing at the address
    And a station is on air for "m"
    When the broker boots
    And the shared store starts answering at the address
    And the broker becomes ready
    And a funded consumer relays to "m"
    Then the response is 200
    And the consumer was billed through a hold and a settle

  Scenario: After becoming ready, rate limits are shared with a peer instance
    Given the broker is configured with ROGERAI_REDIS_URL pointing at the address
    When the broker boots
    And the shared store starts answering at the address
    And the broker becomes ready
    And a second instance runs over the same shared store
    Then a signed identity's requests on both instances draw from one rate-limit bucket

  Scenario: After becoming ready, a device login started on this instance completes on a peer
    Given the broker is configured with ROGERAI_REDIS_URL pointing at the address
    When the broker boots
    And the shared store starts answering at the address
    And the broker becomes ready
    And a second instance runs over the same shared store
    And a device login is started on this instance
    Then the login can be approved and polled on the second instance

  Scenario: After becoming ready, an email login code sent from this instance verifies on a peer
    Given the broker is configured with ROGERAI_REDIS_URL pointing at the address
    When the broker boots
    And the shared store starts answering at the address
    And the broker becomes ready
    And a second instance runs over the same shared store
    And an email login code is requested on this instance
    Then the code verifies on the second instance

  Scenario: After becoming ready, a station cooldown set on a peer is honored here
    Given the broker is configured with ROGERAI_REDIS_URL pointing at the address
    When the broker boots
    And the shared store starts answering at the address
    And the broker becomes ready
    And a second instance runs over the same shared store
    And the second instance cools station "s1"
    Then this instance does not pick "s1" while it cools

  Scenario: A request that arrived while not ready can simply be retried after readiness
    Given the broker is configured with ROGERAI_REDIS_URL pointing at the address
    When the broker boots
    And a funded consumer posts a chat completion and gets 503 "shared_store_unavailable"
    And the shared store starts answering at the address
    And the broker becomes ready
    And the consumer retries the same request
    Then the retry is served and billed exactly once

  # --- restart --------------------------------------------------------------------------------

  Scenario: A restarted broker with the shared store down is not ready again until it answers
    Given the broker is configured with ROGERAI_REDIS_URL pointing at the address
    And the broker was ready and then restarted while the shared store was down
    Then GET /ready answers 503 with shared "connecting"
    And a signed chat request answers 503 "shared_store_unavailable"

  # --- R6 / unchanged: runtime outages keep today's approved behavior ------------------------

  Scenario: A shared-store outage after the broker became ready keeps today's degraded readiness
    Given the broker is configured with ROGERAI_REDIS_URL pointing at the address
    And the shared store is answering
    And the broker becomes ready
    When the shared store stops answering
    Then GET /ready answers 200 with ready true and shared "degraded"

  Scenario: The shared wiring done at readiness is not undone by a later outage
    Given the broker is configured with ROGERAI_REDIS_URL pointing at the address
    And the shared store is answering
    And the broker becomes ready
    When the shared store stops answering and then answers again
    Then the device flow, the email flow and the rate limiters still use the shared store

  Scenario: A durable-store outage still makes the broker not ready (unchanged)
    Given the broker is configured with ROGERAI_REDIS_URL pointing at the address
    And the shared store is answering
    And the broker becomes ready
    When the durable store stops answering
    Then GET /ready answers 503 with db "down"
