# IDEMPOTENT RELAY (slice 6, audit item #10): a client that retries a request it is unsure
# about (a timeout, a dropped connection) must never be charged twice or make a station do the
# work twice. The retry gets the first request's outcome.
#
# GROUND TRUTH (routing-slice6 at b1754466):
#   - The relay has no Idempotency-Key handling (the only Idempotency-Key uses in the broker are
#     outbound to Stripe and the mail provider: payouts.go:439, email.go:218).
#   - Every relay mints a fresh request id at the top of relay() and returns it as
#     X-RogerAI-Request-Id (slice 3). Two identical requests are two jobs, two holds, two settles.
#   - After a non-stream 504 ("node timed out", tunnel.go:2862) the station's later result is
#     discarded unpaid; a client that retries on that 504 starts a second job.
#   - The local proxy retries transport errors and >= 500 up to 4 times with 200/400/800 ms
#     backoff and, on retry, prefers an alternative from /discover via provider.order
#     (internal/client/failover.go:72, 125-160). It does not know whether the broker already
#     failed over internally.
#
# RULES (contract draft B §14.B2):
#   - Header `Idempotency-Key` on POST /v1/chat/completions: 1..128 printable ASCII bytes, else
#     400 invalid_idempotency_key. Scope = (payer, key). Window 10 min from the first request
#     (ROGERAI_IDEMPOTENCY_TTL, default 10m).
#   - Fingerprint = sha256 of the exact request body bytes plus the routing headers the broker
#     reads (X-Roger-*). Same (payer, key) + same fingerprint within the window:
#       * superseded 2026-10-06 by founder ruling: only a success or a client error (4xx other
#         than 429) is replayed; a retryable outcome (429, any 5xx) gives the key back and the
#         retry runs fresh, still with at most one hold and one charge overall.
#       * first request finished with a replayable status and was NOT a stream → replay its stored outcome:
#         same status, same body bytes, same X-RogerAI-* headers incl. the SAME
#         X-RogerAI-Request-Id, plus `X-RogerAI-Idempotent-Replay: true`. No dispatch, no hold,
#         no settle, no moderation call, no rate-limit token consumed beyond the replay lookup.
#       * first request still in flight → 409 request_in_flight with Retry-After (seconds until
#         its deadline, at least 1).
#       * first request was a STREAM that committed its first frame → 409 stream_not_replayable
#         (a stream cannot be re-sent byte-for-byte); if the first stream failed BEFORE any frame
#         (a refusal or an error answer with no SSE commit) its outcome is stored and replayed
#         like a non-stream one.
#   - Same (payer, key) + DIFFERENT fingerprint → 422 idempotency_key_reused. No dispatch.
#   - A different payer using the same key string is an unrelated request.
#   - Stored outcomes are bounded: body up to 1 MiB (larger bodies store status + headers and
#     replay as 409 response_too_large_to_replay); at most 1000 live keys per payer (the oldest
#     falls out of the window early; the request that would exceed it is still served).
#   - Shared across instances via the shared store. corrected 2026-10-06 (founder-approved): the
#     claim itself always lives in the store, so a retry whose saved reply is gone (another
#     instance's local fallback while the shared store is down, or a lost entry) is answered
#     409 response_unavailable, never served as a second job.
#   - The broker returns `X-RogerAI-Attempts: <n>` (the number of station attempts made) on
#     every relay response. The local proxy does not re-pick on its own retry when n > 1 (the
#     broker already walked its plan); it retries only transport errors, and every retry of one
#     client request carries the same Idempotency-Key (the proxy mints one per client request if
#     the client sent none, and forwards the client's own key if it sent one).
#   - Money invariant: one Idempotency-Key never yields two settles, two holds or two earns.
#
# Enforced by: cmd/rogerai-broker/idempotency_bdd_test.go,
# internal/client/idempotency_bdd_test.go (the proxy section, tagged @proxy)

Feature: A retried request with the same Idempotency-Key is answered once and charged once

  Background:
    Given a broker with an empty in-memory node registry
    And the fee rate is 30%
    And the idempotency window is 10 minutes
    And station "s1" is on air for "m" at in $0.10 out $0.30 per 1M
    And "u-1" is a funded consumer

  # --- replay ------------------------------------------------------------------------

  Scenario: A retry with the same key and body returns the first outcome without a second job
    Given "u-1" relays for "m" with Idempotency-Key "k-1" and the response is 200 from "s1"
    When "u-1" sends the identical request with Idempotency-Key "k-1"
    Then the response is 200 with the same body bytes as the first
    And X-RogerAI-Request-Id equals the first request's id
    And X-RogerAI-Idempotent-Replay is "true"
    And "s1" received exactly 1 job in total
    And exactly 1 settle and 1 receipt exist for that request id
    And the wallet of "u-1" was debited once

  Scenario: The replay carries every X-RogerAI header of the first response
    Given "u-1" relays for "m" with Idempotency-Key "k-1" and the response is 200 from "s1"
    When "u-1" sends the identical request with Idempotency-Key "k-1"
    Then X-RogerAI-Receipt, X-RogerAI-Cost, X-RogerAI-Provider, X-RogerAI-Model and X-RogerAI-Price equal the first response's

  # superseded 2026-10-06 by founder ruling: only client errors replay; the 503 no_match row
  # moved to "A retry after a 503 no_match runs fresh" below.
  Scenario Outline: A refused first request is replayed as the same refusal
    Given "u-1" relays for "m" with Idempotency-Key "k-2" and the response is <status> <code>
    When "u-1" sends the identical request with Idempotency-Key "k-2"
    Then the response is <status> with error code "<code>"
    And X-RogerAI-Request-Id equals the first request's id
    And no station received anything for the retry

    Examples:
      | status | code                  |
      | 400    | invalid_routing_value |
      | 402    | insufficient_balance  |

  # founder ruling 2026-10-06: a retryable outcome gives the key back, so the retry runs fresh
  Scenario: A retry after an upstream 429 runs fresh
    Given "u-1" relays for "m" with Idempotency-Key "k-26" and the response is 429
    When "u-1" sends the identical request with Idempotency-Key "k-26"
    Then the retry ran fresh, not as a replay

  # founder ruling 2026-10-06
  Scenario: A retry after a 503 no_match runs fresh
    Given "u-1" relays for "m" with Idempotency-Key "k-27" and the response is 503 no_match
    When "u-1" sends the identical request with Idempotency-Key "k-27"
    Then the retry ran fresh, not as a replay

  # founder ruling 2026-10-06: a client error still replays
  Scenario: A retry after a 400 for a malformed request replays it
    Given "u-1" relays for "m" with Idempotency-Key "k-28" and the response is 400 invalid_routing_value
    When "u-1" sends the identical request with Idempotency-Key "k-28"
    Then the response is a replay with the first request's id
    And no station received anything for the retry

  Scenario: A replay does not call moderation again
    Given "u-1" relays for "m" with Idempotency-Key "k-1" and the response is 200 from "s1"
    When "u-1" sends the identical request with Idempotency-Key "k-1"
    Then the moderation classifier was called exactly once for both

  Scenario: A replay of a moderation refusal is the same refusal, without a second preserve
    Given sync moderation flags the prompt
    And "u-1" relays for "m" with Idempotency-Key "k-3" and the response is 451
    When "u-1" sends the identical request with Idempotency-Key "k-3"
    Then the response is 451
    And no second moderation record was written

  # superseded 2026-10-06 by founder ruling: a 504 is retryable, so the key is given back and the
  # retry runs fresh. Old title: "A replay after a 504 node timeout returns the 504, never a second
  # dispatch"; old Then: the response is 504, and "s1" received exactly 1 job in total.
  Scenario: A retry after a 504 node timeout runs fresh
    Given "s1" takes longer than the non-stream relay window
    And "u-1" relays for "m" with Idempotency-Key "k-4" and the response is 504
    When "u-1" sends the identical request with Idempotency-Key "k-4"
    Then the retry ran fresh, not as a replay
    And the wallet of "u-1" was debited at most once

  # --- in flight -------------------------------------------------------------------------

  # slice-6 review 2026-10-06: a request that wrote nothing gives its key back, so the retry is
  # served (a replay of "nothing" would be an unanswerable response)
  Scenario: A retry after the consumer disconnected is served fresh with exactly one hold
    Given "s1" takes 2 seconds to answer
    And "u-1" sends a request for "m" with Idempotency-Key "k-gone" and disconnects before the answer
    When "u-1" sends the identical request with Idempotency-Key "k-gone"
    Then the response is a fresh relay
    And the retry placed exactly 1 hold

  # slice-6 review 2026-10-06
  Scenario: A retry after a stream that ended before its first byte is served fresh with exactly one hold
    Given "s1" takes 2 seconds to answer
    And "u-1" streams for "m" with Idempotency-Key "k-sgone" and disconnects before the first byte
    When "u-1" sends the identical stream request with Idempotency-Key "k-sgone"
    Then the response is a fresh relay
    And the retry placed exactly 1 hold

  Scenario: A retry while the first is still running is a 409 with Retry-After
    Given "s1" takes 5 seconds to answer
    And "u-1" has relayed for "m" with Idempotency-Key "k-5" and the request is still in flight
    When "u-1" sends the identical request with Idempotency-Key "k-5"
    Then the response is 409 with error code "request_in_flight"
    And Retry-After is at least 1
    And "s1" received exactly 1 job in total

  Scenario: After the first finishes, the retry replays it
    Given "s1" takes 2 seconds to answer
    And "u-1" has relayed for "m" with Idempotency-Key "k-5" and the request is still in flight
    When the first request finishes with 200
    And "u-1" sends the identical request with Idempotency-Key "k-5"
    Then the response is 200 with the same body bytes as the first

  Scenario: Two concurrent first requests with the same key dispatch exactly once
    When "u-1" sends the same request with Idempotency-Key "k-race" twice concurrently
    Then exactly one response is 200 from "s1"
    And the other is 409 "request_in_flight" or a replay of the first
    And "s1" received exactly 1 job in total

  # --- reuse with a different body ---------------------------------------------------------

  Scenario: The same key with a different body is a 422
    Given "u-1" relays for "m" with Idempotency-Key "k-6" and the response is 200 from "s1"
    When "u-1" relays for "m" with a different prompt and Idempotency-Key "k-6"
    Then the response is 422 with error code "idempotency_key_reused"
    And no station received anything for the second request

  Scenario: The same key with the same body but a different routing header is a 422
    Given "u-1" relays for "m" with Idempotency-Key "k-7" and the response is 200 from "s1"
    When "u-1" sends the same body with X-Roger-Min-TPS "20" and Idempotency-Key "k-7"
    Then the response is 422 with error code "idempotency_key_reused"

  Scenario: Whitespace differences in the body are a different body
    Given "u-1" relays for "m" with Idempotency-Key "k-8" and the response is 200 from "s1"
    When "u-1" sends the same JSON with different whitespace and Idempotency-Key "k-8"
    Then the response is 422 with error code "idempotency_key_reused"

  # --- scope ---------------------------------------------------------------------------------

  Scenario: Keys are per payer
    Given "u-1" relays for "m" with Idempotency-Key "k-9" and the response is 200 from "s1"
    And "u-2" is a funded consumer
    When "u-2" sends the identical request with Idempotency-Key "k-9"
    Then the response is 200 from "s1"
    And X-RogerAI-Request-Id differs from "u-1"'s request
    And "s1" received exactly 2 jobs in total

  Scenario: A grant bearer's key is scoped to the grant
    Given a grant "g1" issued to "u-1"'s owner
    And a request with grant "g1" and Idempotency-Key "k-10" was served
    When "u-1" sends the identical request signed as "u-1" with Idempotency-Key "k-10"
    Then the response is a fresh relay, not a replay

  Scenario: An anonymous caller's key is scoped to its anonymous identity
    Given an anonymous caller relayed for a free station with Idempotency-Key "k-11"
    When a different anonymous IP sends the identical request with Idempotency-Key "k-11"
    Then the response is a fresh relay, not a replay

  # --- the window --------------------------------------------------------------------------

  Scenario: After the window, the same key is a fresh request
    Given "u-1" relays for "m" with Idempotency-Key "k-12" and the response is 200 from "s1"
    And 11 minutes pass
    When "u-1" sends the identical request with Idempotency-Key "k-12"
    Then the response is 200 from "s1"
    And X-RogerAI-Request-Id differs from the first request's id
    And "s1" received exactly 2 jobs in total

  Scenario Outline: The window is a broker knob
    Given ROGERAI_IDEMPOTENCY_TTL is "<ttl>"
    And "u-1" relays for "m" with Idempotency-Key "k-13" and the response is 200 from "s1"
    And <gap> pass
    When "u-1" sends the identical request with Idempotency-Key "k-13"
    Then the response is <outcome>

    Examples:
      | ttl | gap         | outcome      |
      | 1m  | 30 seconds  | a replay     |
      | 1m  | 90 seconds  | a fresh relay |
      | 60m | 45 minutes  | a replay     |

  # --- streams -----------------------------------------------------------------------------

  Scenario: A retry of a stream that committed its first frame is a 409
    Given "u-1" streams for "m" with Idempotency-Key "k-14" and the stream completed
    When "u-1" sends the identical stream request with Idempotency-Key "k-14"
    Then the response is 409 with error code "stream_not_replayable"
    And "s1" received exactly 1 job in total
    And the wallet of "u-1" was debited once

  # superseded 2026-10-06 by founder ruling: a 503 is retryable, so the stream's key is given back
  # too. Old title: "A retry of a stream that failed before any frame is replayed as that failure";
  # old Then: the response is 503 with error code "no_match" (a replay).
  Scenario: A retry of a stream that failed before any frame with a retryable status runs fresh
    Given no station is on air for "m2"
    And "u-1" streams for "m2" with Idempotency-Key "k-15" and the response is 503 no_match
    When "u-1" sends the identical stream request with Idempotency-Key "k-15"
    Then the retry ran fresh, not as a replay
    And the response is 503 with error code "no_match"

  Scenario: A retry of a stream still in flight is a 409 request_in_flight
    Given "s1" streams slowly for 5 seconds
    And "u-1" has started a stream for "m" with Idempotency-Key "k-16"
    When "u-1" sends the identical stream request with Idempotency-Key "k-16"
    Then the response is 409 with error code "request_in_flight"

  Scenario: A stream retried as a non-stream with the same key is a different body (422)
    Given "u-1" streams for "m" with Idempotency-Key "k-17" and the stream completed
    When "u-1" sends the same request with stream false and Idempotency-Key "k-17"
    Then the response is 422 with error code "idempotency_key_reused"

  # --- validation and bounds -----------------------------------------------------------------

  Scenario Outline: Malformed keys are refused before any routing
    When "u-1" relays for "m" with Idempotency-Key <key>
    Then the response is 400 with error code "invalid_idempotency_key"
    And no station received anything

    Examples:
      | key                      |
      | ""                       |
      | a 129-byte key           |
      | "has\nnewline"           |
      | "has\u0000nul"           |

  Scenario: A 128-byte key is accepted
    When "u-1" relays for "m" with a 128-byte Idempotency-Key
    Then the response is 200

  Scenario: A request with no Idempotency-Key is unaffected
    When "u-1" sends the same request twice with no Idempotency-Key
    Then both responses are 200
    And "s1" received exactly 2 jobs in total
    And the two X-RogerAI-Request-Id values differ

  Scenario: An outcome body over 1 MiB is not stored and its retry is told so
    Given "s1" answers with a 2 MiB completion
    And "u-1" relays for "m" with Idempotency-Key "k-18" and the response is 200
    When "u-1" sends the identical request with Idempotency-Key "k-18"
    Then the response is 409 with error code "response_too_large_to_replay"
    And "s1" received exactly 1 job in total

  Scenario: More than 1000 live keys for one payer evicts the oldest, never refuses a request
    Given "u-1" has 1000 live idempotency keys
    When "u-1" relays for "m" with Idempotency-Key "k-1001"
    Then the response is 200
    And the oldest key of "u-1" is no longer replayable

  # slice-6 review 2026-10-06 (M1): the bound evicts finished keys only; evicting a request still
  # in flight would let its retry run a second job and place a second hold
  Scenario: The per-payer key bound never evicts a request still in flight
    Given station "s2" is on air for "m2" at in $0.10 out $0.30 per 1M
    And "s2" takes 60 seconds to answer
    And "u-1" has relayed for "m2" with Idempotency-Key "k-live" and the request is still in flight
    And "u-1" has 1000 live idempotency keys
    When "u-1" sends the identical request with Idempotency-Key "k-live"
    Then the response is 409 with error code "request_in_flight"
    And "s2" received exactly 1 job in total

  # --- money ---------------------------------------------------------------------------------

  Scenario: One key never yields two holds, two settles or two earns
    Given "u-1" relays for "m" with Idempotency-Key "k-19" and the response is 200 from "s1"
    When "u-1" sends the identical request with Idempotency-Key "k-19" 5 more times
    Then exactly 1 hold was placed for key "k-19"
    And exactly 1 settle row and 1 earnings credit exist for it
    And the operator of "s1" earned once

  Scenario: A replay costs the consumer nothing extra and says so
    Given "u-1" relays for "m" with Idempotency-Key "k-20" and the response is 200 from "s1"
    When "u-1" sends the identical request with Idempotency-Key "k-20"
    Then the wallet balance of "u-1" is unchanged by the replay
    And X-RogerAI-Cost on the replay equals the first response's cost

  Scenario: A replay consumes no relay rate-limit token beyond the lookup bucket
    Given "u-1" is at its last relay rate-limit token
    And "u-1" relayed for "m" with Idempotency-Key "k-21" and the response is 200
    When "u-1" sends the identical request with Idempotency-Key "k-21"
    Then the response is a replay, not a 429

  # --- multi-instance -----------------------------------------------------------------------

  Scenario: A retry landing on another instance is replayed from the shared store
    Given a second broker instance "B" shares the store
    And "u-1" relays on instance A for "m" with Idempotency-Key "k-22" and the response is 200
    When "u-1" sends the identical request to instance B with Idempotency-Key "k-22"
    Then the response is a replay with the first request's id
    And "s1" received exactly 1 job in total

  Scenario: An in-flight request on instance A makes instance B answer 409
    Given a second broker instance "B" shares the store
    And "s1" takes 5 seconds to answer
    And "u-1" has relayed on instance A for "m" with Idempotency-Key "k-23" and it is in flight
    When "u-1" sends the identical request to instance B with Idempotency-Key "k-23"
    Then the response is 409 with error code "request_in_flight"

  Scenario: With the shared store down, the key is honored per instance and never blocks a request
    Given the shared store is unreachable
    When "u-1" relays for "m" with Idempotency-Key "k-24"
    Then the response is 200

  # corrected 2026-10-06 (founder-approved): the claim exists but its saved reply is gone
  Scenario: A retry whose saved reply is gone is a 409 response_unavailable
    Given "u-1" relays for "m" with Idempotency-Key "k-25" and the response is 200 from "s1"
    And the saved reply for Idempotency-Key "k-25" is lost
    When "u-1" sends the identical request with Idempotency-Key "k-25"
    Then the response is 409 with error code "response_unavailable"
    And "s1" received exactly 1 job in total

  # --- the attempts header --------------------------------------------------------------------

  Scenario Outline: Every relay response carries X-RogerAI-Attempts
    Given <setup>
    When "u-1" relays for "m"
    Then X-RogerAI-Attempts is "<n>"

    Examples:
      | setup                                              | n |
      | "s1" serves                                        | 1 |
      | "s1" 429s and "s2" serves                          | 2 |
      | no station is on air for "m"                       | 0 |

  # --- the local proxy -------------------------------------------------------------------------

  @proxy
  Scenario: The proxy mints one Idempotency-Key per client request and reuses it on its own retries
    Given the broker answers the first proxy attempt with a transport error
    When a guest sends one chat request through the local proxy with no Idempotency-Key
    Then every proxy attempt for it carries the same Idempotency-Key
    And a different client request carries a different key

  @proxy
  Scenario: The proxy forwards a client's own Idempotency-Key unchanged
    When a guest sends a chat request through the local proxy with Idempotency-Key "client-key"
    Then every proxy attempt carries Idempotency-Key "client-key"

  @proxy
  Scenario: The proxy does not re-pick when the broker already failed over
    Given the broker answers the first proxy attempt 502 with X-RogerAI-Attempts "3"
    When a guest sends one chat request through the local proxy
    Then the proxy's retry carries no provider.order
    And the proxy does not consult /discover for an alternative

  @proxy
  Scenario: The proxy keeps its alternative pick when the broker made a single attempt
    Given the broker answers the first proxy attempt 502 with X-RogerAI-Attempts "1"
    When a guest sends one chat request through the local proxy
    Then the proxy's retry carries provider.order naming an alternative station

  @proxy
  Scenario: The proxy relays a 409 request_in_flight to the guest with its Retry-After
    Given the broker answers 409 request_in_flight with Retry-After "3"
    When a guest sends one chat request through the local proxy
    Then the guest sees 409 with Retry-After "3"
