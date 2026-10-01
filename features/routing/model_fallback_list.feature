# ROUTING - THE ORDERED MODEL LIST ("serve the first of these that can, bill me once, tell me
# which one you served").
#
# PURPOSE. Today a request names ONE model and the broker fails over between STATIONS of that
# model (features/routing/upstream_failover.feature). A consumer who would accept any of three
# models cannot say so: the client has to catch the 503/429, pick another id and resend, paying
# the round-trip and losing the broker's hold/receipt/cooldown machinery in between. OpenRouter
# accepts `models: [...]` as a priority list and moves on to the next model on "any error"
# (context length, moderation flags, rate limiting, downtime), billing at the model that served
# (https://openrouter.ai/docs/guides/routing/model-fallbacks, read 2026-09-29). This spec adds
# the same expression on top of OUR failover core, without relaxing one money invariant:
#
#   1. LIST. effective list = [model] ++ models, de-duplicated (first occurrence wins), at most
#      5 after de-dup. `model` absent and `models` present: the first entry is primary.
#   2. PLAN. The attempt plan is built PER MODEL IN ORDER: up to ROGERAI_RELAY_ATTEMPTS (3)
#      stations for the model under EVERY constraint of the request, then the next model. The
#      request's one deadline (>= relayMinAttemptBudget 10 s left for a new attempt) bounds the
#      whole plan. The plan is built up front so the ONE hold can be sized over it.
#   3. MOVE ON to the next model when: the current model has NO eligible station (silently); an
#      attempt fails on a station-failover trigger (429, 5xx, station-502, 2xx-empty) and the
#      model has no station left in its plan; the upstream answers the context-window 400; the
#      broker's own declared-ctx gate found every station of the model too small. DO NOT move
#      on: any other 4xx (the request is bad and will be bad on the next model), a moderation
#      verdict (prompt-level, model-independent), after the first streamed content frame, a
#      client disconnect, an exhausted deadline.
#   4. MONEY. One hold, sized to the priciest (model, station) pair of the whole plan when the
#      balance and monthly cap allow, else the first pair alone with the pricier pairs trimmed
#      (never attempted). Billed at the (model, station) that served, at THAT station's locked
#      price for THAT model (the price lock is keyed (user, node, model): a model switch can
#      mint a new 24 h lock at first serve). The fee applies once, on the served attempt. A
#      free plan (anonymous / free band / $0 grant / self-use) never plans a paid pair - the
#      paid model is skipped, not refused with 402. Every failed attempt is voided ($0 receipt
#      in the station's chain); the failed-over operator earns nothing and is not struck for a
#      429 or a ctx-400.
#   5. TRANSPARENCY. X-RogerAI-Model names the bare served model id on EVERY 2xx path; the
#      receipt's model is the dispatched model; the stream usage chunk names it; /generation
#      lists every attempt with its model. The station receives a body whose `model` IS the
#      model dispatched to it; `models`, `provider`, `roger` never leave the broker.
#
# GROUND TRUTH (origin/main 518c698b): cmd/rogerai-broker/tunnel.go relay(): body decode reads
# only model+stream (1724-1728); the first pickFor (1852); the ctx-400 re-pick (1911-1922:
# "request exceeds the context window" is answered ONLY when a no-ctx re-pick finds a station,
# i.e. the model IS on air but too small); "no node offers" 503 (1924-1931); the attempt plan
# (1957-1993: relayAttempts() stations for ONE model, each money-gated: same payer,
# anonCannotPay, a free first pick admits only free followers); monthlyCapCheck on the first
# pick, planCeiling attempted under monthlyCapFits, HoldFor once, trimPlan (2011-2056); the
# attempt loop with rekeyHold + nextAttempt (2073-2178); lockedPrice keyed user|node|model
# (main.go:1055-1085, lockWin 24 h, shared quote first); settle clamp = min(plan hold, the
# serving station's own ceiling) (2205); the response headers (2252-2265). relayStream +
# streamAttempt + lazySSE commit on first frame / streamCommitGrace 20 s (2589-2625).
# cooling.go: failoverable (429 | >=500 | <400 empty), attemptID "<req>-n", holdCostFor,
# planCeiling, trimPlan, nextAttempt (skips cooling / departed candidates), bandAllow.
# Grants: gc.modelDenied (empty Models = any), gc.nodeAllow; a denied model is a 403 BEFORE
# the pick today (tunnel.go:1843-1846). Bands: freqBand.ModelDenied -> the uniform "no station
# on that frequency" 503 (1805-1810). The contract: features/routing/ROUTING-EXPRESSION-
# CONTRACT.md §1a, §1b, §2, §3, §7.
#
# KNOBS: ROGERAI_RELAY_ATTEMPTS=3 (per model)  ROGERAI_RELAY_FAILOVER=1  relayMinAttemptBudget
#        10 s  nonStreamRelayWait 90 s  streamCommitGrace 20 s  lockWin 24 h
#
# Enforced by: cmd/rogerai-broker/model_fallback_list_bdd_test.go (godog; real in-memory store
# + miniredis, real httptest upstreams behind real station loops, real Postgres money path via
# testcontainers for the ledger scenarios; no mocks).

Feature: A consumer names the models it accepts, in order - the broker serves the first that can, bills once, and names what it served

  Background:
    Given a broker with a real store and a shared store
    And relay failover is on with ROGERAI_RELAY_ATTEMPTS "3"
    And scripted upstreams behind real stations
    And a funded consumer with balance $10.00 unless a scenario says otherwise

  # ===========================================================================
  # 1. LIST CONSTRUCTION - what the broker accepts as the ordered list
  # ===========================================================================

  Scenario: model plus models forms the effective list in order
    Given stations serve "a", "b" and "c"
    When a consumer relays with "model": "a" and "models": ["b", "c"]
    Then the effective model list is ["a", "b", "c"]

  Scenario: models without model makes the first entry primary
    Given stations serve "b" and "c"
    When a consumer relays with no "model" and "models": ["b", "c"]
    Then the effective model list is ["b", "c"]
    And a station serving "b" received the request with body model "b"

  Scenario: neither model nor models is today's 400
    When a consumer relays with no "model" and no "models"
    Then the response is 400 and the message is today's missing-model message
    And no station received anything and no hold was placed

  Scenario: duplicates are removed keeping the first occurrence
    When a consumer relays with "model": "a" and "models": ["b", "a", "c", "b"]
    Then the effective model list is ["a", "b", "c"]

  Scenario: the primary repeated inside models is not a second attempt at the same model
    Given station "s1" serves "a" and its upstream returns 429
    When a consumer relays with "model": "a" and "models": ["a"]
    Then "s1" received exactly one request
    And the response is 429 with a Retry-After

  Scenario: five distinct models after de-dup is the maximum
    When a consumer relays with "model": "a" and "models": ["b", "c", "d", "e"]
    Then the request is accepted (five entries)

  Scenario: five entries with duplicates that de-dup below the cap are accepted
    When a consumer relays with "model": "a" and "models": ["a", "b", "b", "c", "c", "d"]
    Then the effective model list is ["a", "b", "c", "d"] and the request is accepted

  Scenario: a sixth distinct model is refused before any pick
    When a consumer relays with "model": "a" and "models": ["b", "c", "d", "e", "f"]
    Then the response is 400 with error code "invalid_routing_value" naming "models"
    And no pick ran, no hold was placed, and no station received anything

  Scenario Outline: malformed models values are refused before any pick
    When a consumer relays with "model": "a" and "models": <value>
    Then the response is 400 with error code "<code>" naming "models"
    And no hold was placed

    Examples:
      | value                 | code                  |
      | "b"                   | invalid_routing_value |
      | 42                    | invalid_routing_value |
      | {"0": "b"}            | invalid_routing_value |
      | ["b", ""]             | invalid_routing_value |
      | ["b", "   "]          | invalid_routing_value |
      | ["b", null]           | invalid_routing_value |
      | ["b", 7]              | invalid_routing_value |
      | [["b"]]               | invalid_routing_value |

  Scenario: an empty models array is the same as no models
    Given station "s1" serves "a"
    When a consumer relays with "model": "a" and "models": []
    Then the request is served by "s1" exactly as a single-model request

  Scenario: a null models value is the same as no models
    Given station "s1" serves "a"
    When a consumer relays with "model": "a" and "models": null
    Then the request is served by "s1" exactly as a single-model request

  Scenario: an entry may carry variant sugar and the bare id is what is matched
    Given station "s1" serves "b" priced 0/0 right now
    When a consumer relays with "model": "a" (no station) and "models": ["b:free"]
    Then the request is served by "s1"
    And X-RogerAI-Model is "b"

  Scenario: sugar on one entry filters that entry only
    Given station "s1" serves "a" priced 1.00/1.00 and station "s2" serves "b" priced 1.00/1.00
    And "s1"'s upstream returns 429
    When a consumer relays with "model": "a" and "models": ["b:free"]
    Then "b" contributes no candidate (no free offer) and the response is 429 with a Retry-After
    And "s2" received nothing

  Scenario: entries are matched by exact case-sensitive id like a single model is
    Given station "s1" serves "Qwen3-32B"
    When a consumer relays with "model": "x" and "models": ["qwen3-32b"]
    Then the response is 503 with error code "no_match"

  Scenario: a model id longer than the existing model-id limit is refused as today
    When a consumer relays with "model": "a" and "models" containing a 600-character id
    Then the response is 400 (the existing model-id length rule applies per entry)

  # ===========================================================================
  # 2. PLAN CONSTRUCTION - stations per model, models in order, one deadline
  # ===========================================================================

  Scenario: the first model with an eligible station is served and later models are never contacted
    Given stations "a1" serves "a" and "b1" serves "b", both healthy
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the response is 200 from "a1"
    And "b1" received nothing

  Scenario: a model with no station on air is skipped silently
    Given no station serves "a" and station "b1" serves "b"
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the response is 200 from "b1" with X-RogerAI-Model "b"
    And no error, no voided receipt and no strike exists for "a"

  Scenario: up to ROGERAI_RELAY_ATTEMPTS stations are planned per model before the next model
    Given stations "a1", "a2", "a3", "a4" serve "a" and all their upstreams return 429
    And station "b1" serves "b" and returns a completion
    When a consumer relays with "model": "a" and "models": ["b"]
    Then exactly three "a" stations received the request, then "b1"
    And "a4" received nothing
    And the response is 200 from "b1"

  Scenario: the per-model attempt cap follows the knob
    Given ROGERAI_RELAY_ATTEMPTS is "1"
    And stations "a1", "a2" serve "a" and both return 429, and "b1" serves "b"
    When a consumer relays with "model": "a" and "models": ["b"]
    Then exactly one "a" station received the request, then "b1"

  Scenario: a model whose every station is cooling contributes nothing to the plan and the next model serves
    Given every station of "a" is in a 429 cooldown and "b1" serves "b"
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the response is 200 from "b1" with no Retry-After
    And no dispatch into a cooling "a" station happened

  Scenario: every model cooling is the band-cooling 503 with the soonest expiry
    Given every station of "a" cools for 30 s and every station of "b" cools for 12 s
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the response is 503 with error code "band_cooling" and Retry-After "12"
    And no hold was placed and no station received anything

  Scenario: no model on air at all is one no_match 503 naming the whole list
    Given no station serves "a", "b" or "c"
    When a consumer relays with "model": "a" and "models": ["b", "c"]
    Then the response is 503 with error code "no_match"
    And the message names "a, b, c"
    And X-RogerAI-Cost is "0" and no receipt header is present

  Scenario: the plan never repeats a station even when it serves two models in the list
    Given station "s1" serves both "a" and "b" and its upstream returns 429
    And station "b2" serves "b" and returns a completion
    When a consumer relays with "model": "a" and "models": ["b"]
    Then "s1" received exactly one request (for "a") and is not re-tried for "b"
    And the response is 200 from "b2"

  Scenario: a station serving two listed models is planned once, under the first model
    Given station "s1" serves "a" and "b" and returns a completion
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the response is 200 from "s1" with X-RogerAI-Model "a"

  Scenario: the request deadline bounds the whole multi-model plan
    Given "a1" serves "a" and holds the request for 85 s then returns 500
    And "b1" serves "b" and is healthy
    When a consumer relays (non-stream, 90 s deadline) with "model": "a" and "models": ["b"]
    Then no attempt on "b" starts with less than 10 s of deadline remaining
    And the response is the first-failure outcome as today, voided
    And the hold is released in full

  Scenario: a five-model list with three attempts each cannot outlive the deadline
    Given five models each with three stations whose upstreams each take 8 s to answer 500
    When a consumer relays with all five models
    Then attempts stop when less than 10 s remain on the 90 s deadline
    And the response is the last failure with the consumer charged 0
    And no attempt started after the deadline

  Scenario: the plan is built once before the first dispatch
    Given "a1" serves "a" and "b1" serves "b"
    And "b1" goes off air after the plan is built and while "a1" is answering 429
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the departed "b1" is skipped (as a departed sibling is today)
    And the response is 429 with a Retry-After

  @unit
  Scenario: the per-attempt seed makes the station choice within a model reproducible (planner-level, unit step)
    # Request ids are minted per request, so this cannot be driven through the relay; the
    # step invokes the planner directly with an injected seed, as router_test.go does.
    Given two equal stations serve "b" and no station serves "a"
    When the planner is invoked twice with the same seed and the same candidate set for "model": "a" and "models": ["b"]
    Then it yields the same plan both times, with the same "b" station first

  # ===========================================================================
  # 3. MOVING ON - every trigger that advances to the next model
  # ===========================================================================

  Scenario Outline: a no-output failure on the last station of a model moves to the next model
    Given "a1" serves "a" and its upstream <failure>
    And "b1" serves "b" and returns a completion
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the response is 200 from "b1" with X-RogerAI-Model "b"
    And "a1"'s receipt is voided with void_reason "<reason>"

    Examples:
      | failure                              | reason             |
      | returns 429                          | upstream-throttled |
      | returns 500                          | upstream-error     |
      | returns 502                          | upstream-error     |
      | returns 503                          | upstream-error     |
      | returns 504                          | upstream-error     |
      | is unreachable (station posts 502)   | upstream-error     |
      | returns 200 with an empty completion | empty-output       |
      | returns 200 with whitespace only     | empty-output       |
      | returns 200 claiming tokens, no text | empty-output       |

  Scenario: stations of the same model are exhausted before the next model is tried
    Given "a1" and "a2" serve "a"; "a1" returns 429 and "a2" returns a completion
    And "b1" serves "b"
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the response is 200 from "a2" with X-RogerAI-Model "a"
    And "b1" received nothing

  Scenario: the upstream context-window 400 moves to the next model
    Given "a1" serves "a" and its upstream returns 400 with a context-length error body
    And "b1" serves "b" with a wider window and returns a completion
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the response is 200 from "b1"
    And "a1"'s receipt is voided with void_reason "context-window"
    And "a1"'s owner has no strike

  Scenario Outline: the context-window 400 is recognized by the same vocabulary the client compacts on
    Given "a1" serves "a" and its upstream returns 400 with body <body>
    And "b1" serves "b"
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the response is 200 from "b1"

    Examples:
      | body                                                                    |
      | {"error":{"message":"This model's maximum context length is 8192 tokens"}} |
      | {"error":{"message":"context length exceeded"}}                          |
      | {"error":{"message":"prompt is too long: 9000 tokens > 8192 maximum"}}   |
      | {"error":{"code":"context_length_exceeded"}}                             |

  Scenario: the broker's own declared-ctx gate skips a too-small model before any dispatch
    Given "a1" serves "a" with a DECLARED window of 8192 and "b1" serves "b" with 131072
    When a consumer relays a prompt the broker measures at ~20000 tokens with "model": "a" and "models": ["b"]
    Then "a1" received nothing and no receipt exists for "a"
    And the response is 200 from "b1"

  Scenario: an ESTIMATED window never gates a model out of the plan
    Given "a1" serves "a" with an ESTIMATED window of 8192 and "b1" serves "b"
    When a consumer relays a ~20000-token prompt with "model": "a" and "models": ["b"]
    Then "a1" is attempted first

  Scenario: the context-window 400 is answered only when EVERY listed model is too small
    Given "a1" serves "a" declared 8192 and "b1" serves "b" declared 16384
    When a consumer relays a ~20000-token prompt with "model": "a" and "models": ["b"]
    Then the response is 400 "request exceeds the context window"
    And the message names the widest window across the list (16384)
    And no station received anything and no hold was placed

  Scenario: a too-small model followed by an off-air model is the ctx-400, not a no_match (the only on-air model is too small)
    Given "a1" serves "a" declared 8192 and no station serves "b"
    When a consumer relays a ~20000-token prompt with "model": "a" and "models": ["b"]
    Then the response is 400 "request exceeds the context window" naming 8192
    And the off-air model contributed nothing to the answer (§3: skipped silently)

  Scenario: a model that fails over inside itself and then out to the next model keeps one lineage
    Given "a1" returns 429, "a2" returns 500, "b1" returns a completion
    When a consumer relays with "model": "a" and "models": ["b"]
    Then receipts exist for attempts 1 ("a1", voided), 2 ("a2", voided), 3 ("b1", settled)
    And all three carry the consumer's request id lineage in order

  # ===========================================================================
  # 4. NOT MOVING ON - failures the next model would repeat, or that are final
  # ===========================================================================

  Scenario Outline: a client-caused 4xx is answered from the first attempt and no other model is tried
    Given "a1" serves "a" and its upstream returns <status> with <body>
    And "b1" serves "b"
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the response is <status> with the upstream body (one attempt, voided as today)
    And "b1" received nothing
    And the hold is released in full

    Examples:
      | status | body                                   |
      | 400    | {"error":"invalid request"}            |
      | 401    | {"error":"upstream key rejected"}      |
      | 403    | {"error":"forbidden"}                  |
      | 404    | {"error":"model not found"}            |
      | 413    | {"error":"request too large"}          |
      | 415    | {"error":"unsupported media type"}     |
      | 422    | {"error":"unprocessable"}              |

  Scenario: a 400 that is not a context-window error does not move on
    Given "a1" serves "a" and returns 400 {"error":"invalid tool schema"}
    And "b1" serves "b"
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the response is 400 from "a1" and "b1" received nothing

  Scenario: a synchronous moderation reject is final regardless of the list
    Given moderation mode is "sync" and the prompt is one the screener rejects
    And "a1" serves "a" and "b1" serves "b"
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the response is the screener's status (451 or 503) before any pick
    And no station received anything and no hold was placed

  Scenario: a synchronous moderation fail-closed 503 is not a routing failure
    Given moderation mode is "sync" and the screener is unreachable (fail-closed)
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the response is 503 with the moderation message, not "no_match", and no station was contacted

  Scenario: the async moderation verdict is recorded against the station that served, once
    Given moderation mode is "async"
    And "a1" returns 429 and "b1" returns a completion
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the screening job's served node is "b1"
    And the verdict is recorded once for the request, not per attempt

  Scenario: a failure after the first streamed content frame does not move to the next model
    Given "a1" streams two chunks then dies mid-stream, "b1" is healthy
    When a consumer relays with "stream": true, "model": "a" and "models": ["b"]
    Then the stream ends as today (partial output, settled per today's mid-stream rule)
    And "b1" received nothing

  Scenario: a client that disconnects while the first model is being tried ends the plan
    Given "a1" serves "a" slowly and "b1" serves "b"
    When the consumer disconnects during the attempt on "a1" with "models": ["b"] set
    Then no attempt on "b1" is started
    And the hold is released in full

  Scenario: a dispatch failure that is not an upstream verdict is answered as today
    Given "a1" serves "a" but no poller is listening on it, and "b1" serves "b"
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the response is the existing "node busy" 503 (dispatch outcome), not a model fallback
    And the hold is released in full

  Scenario: the non-stream 504 timeout on the first model is answered, not failed over
    Given "a1" serves "a" and never answers within nonStreamRelayWait, "b1" serves "b"
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the response is the existing 504 "node timed out" and "b1" received nothing

  Scenario: a receipt that fails verification ends the request as today
    Given "a1" returns a completion with a receipt that does not bind to the job
    And "b1" serves "b"
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the response is "a1"'s status and body as today, "a1" is struck for the unbound receipt
    And "b1" received nothing and the hold is released in full

  Scenario: the failover knob off restores a single pick even with a list
    Given ROGERAI_RELAY_FAILOVER is "0"
    And "a1" returns 429 and "b1" serves "b"
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the response is 429 (one attempt) with a Retry-After
    And "b1" received nothing

  Scenario: the knob off still skips a model with no station on air
    Given ROGERAI_RELAY_FAILOVER is "0"
    And no station serves "a" and "b1" serves "b"
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the response is 200 from "b1" (list resolution is not failover)

  # ===========================================================================
  # 5. STREAMING - across models until the first content frame, never after
  # ===========================================================================

  Scenario: a streaming 429 on the first model moves to the next model before any frame
    Given "a1" returns 429 and "b1" streams a completion
    When a consumer relays with "stream": true, "model": "a" and "models": ["b"]
    Then the SSE stream carries only "b1"'s chunks
    And X-RogerAI-Provider is "b1" and X-RogerAI-Model is "b"

  Scenario: SSE headers are not committed before the first verdict across models
    Given "a1" returns 429 and "b1" streams
    When a consumer relays with "stream": true across ["a", "b"]
    Then the consumer receives exactly one 200 response with no leaked 429

  Scenario: the stream commit grace still applies across models
    Given "a1" holds the request for 25 s then returns 429, and "b1" streams
    When a consumer relays with "stream": true across ["a", "b"]
    Then the SSE headers go out at streamCommitGrace (20 s) with no content
    And the failover to "b1" does not happen (headers are committed)
    And the stream ends with "a1"'s failure per today's committed-stream rule

  Scenario: a streaming context-window 400 before any frame moves to the next model
    Given "a1" returns 400 with a context-length body and "b1" streams
    When a consumer relays with "stream": true across ["a", "b"]
    Then the SSE stream carries only "b1"'s chunks

  Scenario: every listed model failing on a stream returns the last error with Retry-After
    Given "a1" returns 429 with Retry-After 7 and "b1" returns 503 with Retry-After 20
    When a consumer relays with "stream": true across ["a", "b"]
    Then the response is 503 with Retry-After "20" and "b1"'s body
    And the consumer was charged 0 and both receipts are voided

  Scenario: a streaming 5xx on the last model is answered without a Retry-After
    Given "a1" returns 429 and "b1" returns 500
    When a consumer relays with "stream": true across ["a", "b"]
    Then the response is 500 with no Retry-After

  Scenario: the stream usage chunk names the served model and pair
    Given "a1" returns 429 and "b1" streams
    When a consumer relays with "stream": true across ["a", "b"]
    Then the final SSE chunk before [DONE] carries usage.rogerai.model "b" and usage.rogerai.node "b1"
    And usage.cost equals the settled cost of "b1"'s attempt

  Scenario: a streamed first frame commits the model as well as the station
    Given "a1" streams one frame then 429s mid-stream, "b1" streams
    When a consumer relays with "stream": true across ["a", "b"]
    Then no attempt on "b1" starts and X-RogerAI-Model is "a"

  # ===========================================================================
  # 6. MONEY - one hold over the whole plan, one charge, the right lock
  # ===========================================================================

  Scenario: the hold is placed once and covers the priciest pair in the whole plan
    Given "a1" serves "a" at 1.00/1.00 and returns 429; "b1" serves "b" at 3.00/3.00 and serves
    When a consumer relays with "model": "a" and "models": ["b"]
    Then exactly one hold row exists, at least the max cost at 3.00/3.00
    And one hold_release and one spend for "b1"'s cost exist
    And the consumer's balance equals start minus "b1"'s cost

  Scenario: the hold ceiling spans stations of different models, not only the first model
    Given "a1" at 2.00/2.00 429s, "a2" at 1.00/1.00 500s, "b1" at 4.00/4.00 serves
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the single hold is sized for 4.00/4.00

  Scenario: a pair the hold cannot cover is trimmed and the plan continues past it
    Given "a1" at 1.00/1.00 429s, "b1" at 50.00/50.00 serves, "c1" at 1.50/1.50 serves
    And the consumer's balance covers 1.50/1.50 but not 50.00/50.00
    When a consumer relays with "model": "a" and "models": ["b", "c"]
    Then "b1" is never tried
    And the response is 200 from "c1" with X-RogerAI-Model "c"
    And the hold was sized for 1.50/1.50

  Scenario: a wallet too thin for even the cheapest pair is a 402 before any dispatch
    Given "a1" at 5.00/5.00 and "b1" at 4.00/4.00 serve
    And the consumer's balance is $0.0001
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the response is 402 "insufficient balance - add funds"
    And no station received anything and no hold row remains

  Scenario: the first pair is held for when the plan ceiling does not fit the balance
    Given "a1" at 1.00/1.00 serves; "b1" at 9.00/9.00
    And the consumer's balance covers 1.00/1.00 max cost but not 9.00/9.00
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the hold equals "a1"'s max cost and the response is 200 from "a1"

  Scenario: the served pair is billed at its own locked price, never the plan ceiling
    Given "a1" at 3.00/3.00 429s and "b1" at 1.00/1.00 serves but over-claims its tokens
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the consumer is charged at most "b1"'s own max cost at 1.00/1.00
    And the remainder of the hold is returned

  Scenario: the fee is applied once, on the served attempt only
    Given the fee rate is 30%
    And "a1" 429s and "b1" serves
    When a consumer relays with "model": "a" and "models": ["b"]
    Then exactly one earn row exists, for "b1"'s owner, at 70% of "b1"'s cost
    And no earn row exists for "a1"'s owner

  Scenario: the price lock is keyed on the served model, so a fallback can mint a new lock
    Given the consumer has a 24 h lock on ("b1", "b") at 1.00/1.00 from yesterday
    And "b1" now lists "b" at 2.00/2.00, and "a1" 429s
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the served pair is billed at the locked 1.00/1.00
    And X-RogerAI-Price carries locked_until of the existing lock

  Scenario: a first serve on a new pair mints a 24 h lock for that pair only
    Given the consumer has no lock on ("b1", "b") and "a1" 429s
    When a consumer relays with "model": "a" and "models": ["b"]
    Then a lock exists for ("b1", "b") until now + 24 h
    And no lock was created for ("a1", "a")

  Scenario: the lock of the failed pair is not consumed or created by a voided attempt
    Given the consumer has no lock on ("a1", "a") and "a1" 429s, "b1" serves
    When a consumer relays with "model": "a" and "models": ["b"]
    Then no lock exists for ("a1", "a") after the request

  Scenario: a scheduled free window on the served pair bills 0 and never pins a lock
    Given "a1" 429s and "b1" serves "b" in a published free window right now
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the consumer is charged 0 for "b1", no lock is created, and the hold is released in full

  Scenario: the price lock is honored across instances after a model switch
    Given two broker instances share a store and instance 1 locked ("b1", "b") at 1.00/1.00
    When instance 2 relays with "model": "a" (429s) and "models": ["b"]
    Then instance 2 bills the served pair at the shared 1.00/1.00 quote

  Scenario: a hold reclaimed mid-plan stops the model fallback
    Given "a1" 429s and "b1" serves
    And the backstop sweep reclaims the consumer's hold while "a1" is serving
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the response is 429 with Retry-After and "b1" received nothing
    And the consumer was charged 0 and no hold row remains

  Scenario: a store refusal to rekey the hold answers with the failure at hand
    Given "a1" 429s, "b1" serves, and the store refuses RekeyHold
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the response is 429 with Retry-After and "b1" received nothing
    And the hold is released in full

  Scenario: a settle failure on the served pair refunds in full and still returns the body
    Given "a1" 429s and "b1" serves but the ledger rejects the settle
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the response is 200 with "b1"'s body and no billing headers
    And the hold is released in full
    And "b1"'s operator gets no earn row
    And "b1"'s receipt is voided with void_reason "settle-failed"
    And "b1"'s operator is not struck

  Scenario: a voided-then-served request leaves exactly one spend and one hold_release
    Given "a1" 429s, "a2" 500s, "b1" serves
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the ledger holds one hold, one hold_release, one spend, and no other money rows for this request

  Scenario: a max_tokens-sized hold uses the served pair's window, not the largest window in the list
    Given "a1" serves "a" (ctx 131072, ESTIMATED) at 1.00/1.00 and 429s; "b1" serves "b" (ctx 8192 declared) at 1.00/1.00
    When a consumer relays with a large max_tokens across ["a", "b"]
    Then the hold was sized with the estimated window clamped to 32768 for "a1" and 8192 for "b1"
    And the settle for "b1" is clamped to "b1"'s own ceiling

  # ===========================================================================
  # 7. FREE, ANONYMOUS, SELF-USE, GRANTS, BANDS, CAPS - the intersections
  # ===========================================================================

  Scenario: an anonymous free relay skips a paid model rather than refusing with 401
    Given an anonymous (not logged in) consumer
    And "a1" serves "a" at 1.00/1.00 and "f1" serves "b" at 0/0
    When the consumer relays with "model": "a" and "models": ["b"]
    Then the response is 200 from "f1" with X-RogerAI-Model "b"
    And "a1" received nothing and no 401 was answered

  Scenario: an anonymous consumer with only paid models in the list gets today's login 401 (§1a: anonCannotPay, never a 503)
    Given an anonymous consumer and paid stations for "a" and "b"
    When the consumer relays with "model": "a" and "models": ["b"]
    Then the response is 401 "log in to spend on paid models" and no station received anything
    And no hold was placed and X-RogerAI-Cost is "0"

  Scenario: a free first pick admits only free followers across models
    Given "f1" serves "a" at 0/0 and returns 429; "p1" serves "b" at 1.00/1.00; "f2" serves "c" at 0/0
    When a logged-in consumer relays with "model": "a" and "models": ["b", "c"]
    Then the plan is ["f1", "f2"] and "p1" is never tried
    And the response is 200 from "f2" and no hold was placed

  Scenario: a paid first pick followed by a free station on a later model settles free and returns the hold
    Given "p1" serves "a" at 1.00/1.00 and returns 429; "f1" serves "b" at 0/0
    When a logged-in consumer relays with "model": "a" and "models": ["b"]
    Then the response is 200 from "f1" with X-RogerAI-Cost "0"
    And the hold placed for "p1" is returned in full

  Scenario: self-use stays $0 on any model the caller's own node serves
    Given the consumer owns "mine" which serves "b"; "a1" serves "a" and 429s
    When the consumer relays with "model": "a" and "models": ["b"]
    Then the response is 200 from "mine" with X-RogerAI-Cost "0" and the hold is returned

  Scenario: a grant that denies some listed models skips them silently
    Given a grant allowing models ["b"] on owner O's nodes, and O's "o1" serves "a" and "b"
    When the grant relays with "model": "a" and "models": ["b"]
    Then "o1" received a request with body model "b"
    And X-RogerAI-Model is "b"

  Scenario: a grant that denies every listed model is a 403 grant_model_denied
    Given a grant allowing models ["z"]
    When the grant relays with "model": "a" and "models": ["b", "c"]
    Then the response is 403 with error code "grant_model_denied"
    And the message names the denied models and no station received anything

  Scenario: a grant with an empty model list allows every listed model
    Given a grant with no model restriction on owner O's nodes serving "a" and "b"
    When the grant relays with "model": "a" and "models": ["b"]
    Then the request is served for "a" on O's node

  Scenario: a grant never leaves its owner's nodes for a later model
    Given a grant on owner O; O's "o1" serves "a" and 429s; a public "x1" serves "b"
    When the grant relays with "model": "a" and "models": ["b"]
    Then "x1" received nothing
    And the response is 503 "no node of this grant's owner is serving" with error code "no_match"

  Scenario: a grant's daily token cap is debited once, for the served attempt
    Given a grant with a daily token cap; O's "o1" serves "a" and 429s, O's "o2" serves "b"
    When the grant relays with "model": "a" and "models": ["b"]
    Then the grant's cap is debited once, for "o2"'s served tokens only

  Scenario: a grant's price caps intersect every model in the list
    Given a sponsored grant with price_out 1.00; O's "o1" serves "a" at 2.00 out and O's "o2" serves "b" at 0.50 out
    When the grant relays with "model": "a" and "models": ["b"]
    Then "o1" is never planned and the response is 200 from "o2"

  Scenario: a private band that denies some listed models serves the allowed one
    Given private band B on "s1" allows models ["b"] and "s1" serves "a" and "b"
    When a band-B relay is made with "model": "a" and "models": ["b"]
    Then the response is 200 from "s1" with X-RogerAI-Model "b"

  Scenario: a private band that denies every listed model is the uniform band message
    Given private band B on "s1" allows models ["z"]
    When a band-B relay is made with "model": "a" and "models": ["b"]
    Then the response is 503 "no station on that frequency (it may be off air) - check the code"
    And the body carries no error code that distinguishes model-denied from off-air

  Scenario: a band request never leaves the band for a later model
    Given "s1" is the only station on band B, serves "a" and 429s; a public "x1" serves "b"
    When a band-B relay is made with "model": "a" and "models": ["b"]
    Then "x1" received nothing and the response is 429 with a Retry-After

  Scenario: the consumer's output price cap is applied per model, skipping a model whose only stations exceed it
    Given "a1" serves "a" at 5.00 out and "b1" serves "b" at 0.50 out
    When a consumer relays with max_price.completion 1.00 and "model": "a" and "models": ["b"]
    Then "a1" is never planned and the response is 200 from "b1"

  Scenario: the consumer's input price cap is applied per model
    Given "a1" serves "a" at 5.00 in and "b1" serves "b" at 0.10 in
    When a consumer relays with max_price.prompt 1.00 across ["a", "b"]
    Then the response is 200 from "b1"

  Scenario: the default $10 output cap applies to every model in the list when the body omits it
    Given "a1" serves "a" at 12.00 out and "b1" serves "b" at 9.00 out
    When a consumer relays with no price cap across ["a", "b"]
    Then "a1" is never planned and the response is 200 from "b1"

  Scenario: every model over the consumer's cap is a no_match 503, not a 402
    Given "a1" serves "a" at 5.00 out and "b1" serves "b" at 6.00 out
    When a consumer relays with max_price.completion 1.00 across ["a", "b"]
    Then the response is 503 with error code "no_match" and no hold was placed

  Scenario: a confidential-only request only moves to confidential stations of the next model
    Given confidential "a1" 429s; "b1" is confidential; "b2" is not
    When a confidential-only relay is made across ["a", "b"]
    Then the response is 200 from "b1"

  Scenario: a pinned node with a model list is honored as a pin
    Given "a1" serves "a" and returns 429; "b1" serves "b"
    When a consumer relays with X-Roger-Node "a1" and "models": ["b"]
    Then the response is 429 with a Retry-After (no failover of any kind)
    And "b1" received nothing

  Scenario: a pinned node that serves a later model is used for that model
    Given "s1" serves only "b"; "a1" serves "a"
    When a consumer relays with X-Roger-Node "s1", "model": "a" and "models": ["b"]
    Then the response is 200 from "s1" with X-RogerAI-Model "b"
    And "a1" received nothing

  Scenario: an excluded node is skipped for every model in the list
    Given "s1" serves "a" and "b"; "b2" serves "b"
    When a consumer relays with X-Roger-Exclude-Nodes "s1" across ["a", "b"]
    Then the response is 200 from "b2"

  Scenario: allow_fallbacks false governs STATION failover only - the model list is still walked
    # The consumer named those models on purpose; OpenRouter keeps provider fallbacks and the
    # models list independent as well (contract §3).
    Given "a1", "a2" serve "a" and "a1" returns 429; "b1" serves "b"
    When a consumer relays with provider.allow_fallbacks false across ["a", "b"]
    Then "a2" is never tried
    And "b1" is tried next and the response is 200 from "b1"
    And X-RogerAI-Model is "b"
    And exactly one hold was placed and it covered the pricier of ("a","a1") and ("b","b1")

  Scenario: allow_fallbacks false with every model's one station failing returns the last error
    Given "a1" serves "a" and returns 429 with Retry-After 7; "b1" serves "b" and returns 503
    When a consumer relays with provider.allow_fallbacks false across ["a", "b"]
    Then exactly two attempts were made, "a1" then "b1"
    And the response is 503 with the last upstream's Retry-After when present
    And the hold is released in full

  # ===========================================================================
  # 8. MONTHLY CAP
  # ===========================================================================

  Scenario: a monthly cap that fits only the cheaper model plans only that pair
    Given "a1" serves "a" at 1.00/1.00 and "b1" serves "b" at 2.00/2.00
    And the consumer's monthly cap fits "a1"'s max cost but not "b1"'s
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the hold covers "a1" alone and the response is 200 from "a1"
    And no "monthly limit reached" notice and no cap email

  Scenario: a monthly cap that does not fit the first pair is a cap refusal before dispatch
    Given "a1" at 5.00/5.00 and "b1" at 0.10/0.10 serve
    And the consumer's monthly cap fits "b1" but not "a1"
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the response is the existing monthly-cap refusal for the first pick
    And "b1" received nothing

  Scenario: the plan ceiling is attempted under the cap without notice headers
    Given "a1" at 1.00/1.00 429s and "b1" at 2.00/2.00 serves
    And the consumer's monthly cap fits both
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the hold covers 2.00/2.00 and the response is 200 from "b1" with the ordinary monthly headers

  # ===========================================================================
  # 9. EDGE / TOWER BRIDGE
  # ===========================================================================

  Scenario: a Tower serving a later model is a candidate when no direct node serves it
    Given no direct node serves "a" or "b" and an approved Tower serves "b"
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the bridge serves "b" and X-RogerAI-Model is "b" with X-RogerAI-Relay naming the Tower

  Scenario: a free or self-use relay is never diverted to a billed Tower for a later model
    Given the consumer owns "mine" serving "b" and a billed Tower also serves "b"; "a1" 429s
    When the consumer relays with "model": "a" and "models": ["b"]
    Then the response is 200 from "mine" at $0 and the Tower received nothing

  Scenario: the bridge coin cannot pick a model the direct pick would not
    Given a Tower serves "c" (not in the list) and "a1" serves "a"
    When a consumer relays with "model": "a" and "models": ["b"]
    Then the Tower received nothing

  Scenario: the bridge honors the consumer's price caps per model
    Given a Tower serves "b" at 5.00 out and "b1" serves "b" at 0.50 out; "a1" 429s
    When a consumer relays with max_price.completion 1.00 across ["a", "b"]
    Then the Tower is never tried and the response is 200 from "b1"

  Scenario: the body forwarded to a Tower carries the served model and no routing carriers
    Given a Tower is the only server of "b" and "a1" 429s
    When a consumer relays with "model": "a", "models": ["b"] and a "roger" object
    Then the Tower received body model "b" and no "models", "provider" or "roger" key

  # ===========================================================================
  # 10. RECEIPTS, LINEAGE, STRIKES, COOLDOWN
  # ===========================================================================

  Scenario: every attempt writes a receipt in its station's chain with the model it was dispatched
    Given "a1" 429s, "b1" 500s, "c1" serves
    When a consumer relays across ["a", "b", "c"]
    Then "a1"'s voided receipt names model "a", "b1"'s names "b", "c1"'s settled receipt names "c"
    And each is chained to its station's prev_hash

  Scenario: the receipt's model is the dispatched model even if the station claims another
    Given "b1" serves "b" but returns a receipt claiming model "a"; "a1" 429s
    When a consumer relays across ["a", "b"]
    Then the settled row is keyed on model "b" and the recount uses "b"'s tokenizer
    And X-RogerAI-Model is "b"

  Scenario: the failed-over operator earns nothing and is not struck for a 429
    Given "a1" 429s and "b1" serves
    When a consumer relays across ["a", "b"]
    Then "a1"'s owner has no earn row and no strike
    And "b1"'s owner has one pending earn row

  Scenario: the failed-over operator is not struck for a context-window 400
    Given "a1" returns a context-length 400 and "b1" serves
    When a consumer relays across ["a", "b"]
    Then "a1"'s owner has no strike and "a1"'s health is not graded down for it

  Scenario: a 5xx on the first model grades health as today and does not cool
    Given "a1" returns 500 and "b1" serves
    When a consumer relays across ["a", "b"]
    Then "a1" is not cooling and its success EWMA is graded down as today

  Scenario: a 429 on the first model cools that station for its Retry-After
    Given "a1" returns 429 with Retry-After 30 and "b1" serves
    When a consumer relays across ["a", "b"]
    Then "a1" is cooling for 30 s for model "a"
    And the next single-model request for "a" skips "a1" while a sibling exists

  Scenario: cooldown is per station regardless of which listed model triggered it
    Given "s1" serves "a" and "b" and returns 429 for "a"; "b2" serves "b"
    When a consumer relays across ["a", "b"] and then another consumer relays for "b" alone
    Then the second request does not dispatch into a cooling "s1" while "b2" exists

  Scenario: the served model's canary and health evidence are recorded for the served station only
    Given "a1" 429s and "b1" serves under load
    When a consumer relays across ["a", "b"]
    Then "b1"'s successCount, TPS and capacity evidence are updated and "a1"'s are not

  # ===========================================================================
  # 11. TELEMETRY AND LOGGING
  # ===========================================================================

  Scenario: a model fallback is counted separately from a station failover
    Given "a1" 429s, "a2" 500s, "b1" serves
    When a consumer relays across ["a", "b"]
    Then /admin/live relay_failovers increments by 2 and model_fallbacks increments by 1

  Scenario: a model skipped for having no station on air is not a fallback
    Given no station serves "a" and "b1" serves "b"
    When a consumer relays across ["a", "b"]
    Then model_fallbacks does not increment and relay_failovers does not increment

  Scenario: the fallback is logged once naming the requested list and the served pair
    Given "a1" 429s and "b1" serves
    When a consumer relays across ["a", "b"]
    Then exactly one log line names requested=[a,b] served=b@b1
    And no log line contains the prompt

  Scenario: a single-instance /admin/live omits nothing and adds only the new counter
    When /admin/live is read on a single instance after a fallback
    Then the existing keys are unchanged and model_fallbacks is present

  # ===========================================================================
  # 12. ADVERSARIAL
  # ===========================================================================

  Scenario: a station that answers a context-window 400 to everything never earns and is quarantined by probes
    Given "a1" returns a context-length 400 to every request including canaries; "b1" serves
    When ten consumers relay across ["a", "b"]
    Then "a1" earns nothing on any of them
    And "a1" is quarantined by the probe streak as a station that completes nothing

  Scenario: a station cannot bounce traffic to a competitor by faking a context-window 400 on tiny prompts
    Given "a1" returns a context-length 400 to a 20-token prompt; "b1" serves
    When a consumer relays across ["a", "b"]
    Then the response is 200 from "b1" and "a1"'s void_reason is "context-window"
    And "a1"'s probe canary fails on a small prompt and it drops out of Tier-A

  Scenario: double-charge is impossible across a model switch
    Given "a1" returns 200 with an empty completion, "b1" serves
    When a consumer relays across ["a", "b"]
    Then exactly one spend row exists and the consumer's balance dropped by exactly "b1"'s cost

  Scenario: a voided first-model attempt that later posts a late result cannot settle
    Given "a1" is voided (429) and then posts a second, successful result for the same job id after "b1" served
    When a consumer relays across ["a", "b"]
    Then the late result is discarded, no second spend exists, and "a1" earns nothing

  Scenario: an error body from the first model never leaks into the second model's success
    Given "a1" returns 500 with body {"error":"secret-upstream-detail"} and "b1" serves
    When a consumer relays across ["a", "b"]
    Then the 200 body is exactly "b1"'s completion and contains no "secret-upstream-detail"

  Scenario: a station cannot see the rest of the list
    Given "a1" serves "a"
    When a consumer relays with "model": "a", "models": ["b", "c"] and a "provider" object
    Then "a1" received a body with model "a" and no "models", "provider" or "roger" key

  Scenario: the station receives the model it was dispatched, not the primary
    Given no station serves "a" and "b1" serves "b"
    When a consumer relays with "model": "a" and "models": ["b"]
    Then "b1" received a body with model "b"

  Scenario: rewriting model for the station does not change the pseudonym, the prompt or max_tokens
    Given "b1" serves "b" and no station serves "a"
    When a consumer relays with "model": "a" and "models": ["b"]
    Then "b1"'s job User is the pseudonym for (user, "b1") and the messages and max_tokens are byte-identical to the consumer's

  Scenario: a five-model list cannot be used to hold five stations' capacity at once
    Given five models each with one healthy station
    When a consumer relays across all five
    Then exactly one station is in flight at any moment for this request

  Scenario: a list cannot widen a grant, a band or a cap
    Given a grant allowing models ["a"] on O's nodes; a public "x1" serves "b"
    When the grant relays with "model": "a" (O has no "a" on air) and "models": ["b"]
    Then "x1" received nothing and the response is 503 with error code "no_match"

  Scenario: the request id lineage cannot be forged by a station naming another attempt's id
    Given "a1" 429s and "b1" returns a receipt whose request id is attempt 1's id
    When a consumer relays across ["a", "b"]
    Then the receipt does not bind and "b1" is struck for an unbound receipt, nothing settles, the hold is refunded

  # ===========================================================================
  # 13. TRANSPARENCY - the consumer learns what was served
  # ===========================================================================

  Scenario: X-RogerAI-Model names the bare served id on a non-stream response
    Given "a1" 429s and "b1" serves "b"
    When a consumer relays across ["a", "b:floor"]
    Then X-RogerAI-Model is "b" (no sugar)

  Scenario: X-RogerAI-Model is present on a single-model request too
    Given "a1" serves "a"
    When a consumer relays with "model": "a" only
    Then X-RogerAI-Model is "a"

  Scenario: X-RogerAI-Model is set before the first frame on a stream
    Given "b1" streams "b" and no station serves "a"
    When a consumer relays with "stream": true across ["a", "b"]
    Then the response headers carry X-RogerAI-Model "b" and X-RogerAI-Provider "b1"

  Scenario: the receipt header names the served model
    Given "a1" 429s and "b1" serves
    When a consumer relays across ["a", "b"]
    Then the decoded X-RogerAI-Receipt has model "b" and node "b1"

  Scenario: X-RogerAI-Price and the lock refer to the served pair
    Given "a1" at 1.00/1.00 429s and "b1" at 2.00/2.00 serves
    When a consumer relays across ["a", "b"]
    Then X-RogerAI-Price is "in=2.0000;out=2.0000;locked_until=<b1 lock>"

  Scenario: a failed request carries no served model
    Given "a1" 429s and "b1" 429s
    When a consumer relays across ["a", "b"]
    Then the 429 response has no X-RogerAI-Model and X-RogerAI-Cost "0"

  Scenario: /generation lists every attempt with its model in order
    Given "a1" 429s, "a2" 500s, "b1" serves
    When a consumer relays across ["a", "b"] and then reads /generation?id=<request id>
    Then attempts are [{1,a1,a,429},{2,a2,a,500},{3,b1,b,200}]
    And served is {node: b1, model: b} and models is ["a", "b"]

  Scenario: /generation is owner-scoped across a model switch too
    Given a request that fell over from "a" to "b"
    When another identity reads /generation?id=<that request id>
    Then the response is 404 with no hint that the id exists

  Scenario: the OpenAI body model field passes through from the serving station
    Given "b1" serves "b" and echoes "model": "b" in its completion body
    When a consumer relays across ["a", "b"]
    Then the body's model is "b" (the broker does not rewrite station bodies)

  Scenario: the header wins for clients that only read headers when a station mislabels its body
    Given "b1" serves "b" and echoes "model": "something-else" in its body
    When a consumer relays across ["a", "b"]
    Then X-RogerAI-Model is "b" regardless of the body's claim

  # ===========================================================================
  # 14. COMPATIBILITY - old clients, headers, the proxy
  # ===========================================================================

  Scenario: a body without models behaves byte-for-byte as before this feature
    Given "a1" and "a2" serve "a" and "a1" 429s
    When a consumer relays with "model": "a" and no list
    Then the response, headers, receipts and ledger rows match the pre-feature single-model failover

  @proxy
  Scenario: the local proxy passes models through untouched
    Given the local proxy is tuned to "a" and "b1" serves "b"
    When a guest sends "model": "a" and "models": ["b"] through the proxy
    Then the broker received "model": "a" and "models": ["b"] (no overwrite)

  @proxy
  Scenario: the local proxy still rewrites a bare foreign id when no list is present
    Given the local proxy is tuned to "a"
    When a guest sends "model": "gpt-4o" and no list
    Then the broker received "model": "a" as today

  Scenario: an old station that ignores the body model field is still dispatched only its own model
    Given "b1" is an old station serving only "b" and no station serves "a"
    When a consumer relays across ["a", "b"]
    Then "b1" received model "b" and served it; the receipt is for "b"

  Scenario: the request-body size limit counts the list
    When a consumer relays with a body at the limit plus a "models" array
    Then the response is the existing body-limit 413 and no station received anything
