# ROUTING - UPSTREAM FAILOVER AND COOLDOWN ("route around the provider that said no").
#
# THE INCIDENT (verified, prod 2026-09-07 18:02-18:24 UTC): the house Cerebras station
# house-cb-qwen-3-8-27b forwarded 400K-770K input tokens per minute from ONE consumer to a
# provider whose pay-as-you-go tier allows 150K uncached / 450K total tokens per minute. Cerebras
# answered 66 requests with 429. The broker picked once, dispatched, received the 429, voided the
# request, and handed the bare 429 to the consumer - no Retry-After (JobResult carries only
# {id,status,body,receipt}; the station drops upstream headers), no second pick, and the client's
# retryable() (status >= 500 only) treated each as terminal. The next request was routed to the
# same station a second later and got the same answer. 66 times.
#
# HOW THE ROUTERS THIS PRODUCT COMPETES WITH HANDLE IT (their own docs, read 2026-09-08):
#   OpenRouter: load-balances a model across providers by price "while taking uptime into
#     account"; when "the provider serving your request is rate limiting or at capacity ...
#     fallback routing retries other providers for the same model automatically before the error
#     reaches you"; only when every provider is exhausted does the 429 reach the caller, with a
#     Retry-After header the SDKs honor. Providers declare nothing; OpenRouter learns from errors.
#   LiteLLM router: "cooldowns, fallbacks, timeouts and retries (fixed + exponential backoff)
#     across multiple deployments/providers", cooldown state tracked in Redis; a deployment in
#     cooldown "falls through to the routing strategy across the remaining healthy deployments".
#     Declared tpm/rpm per deployment exist as an OPTIONAL routing input, not the mechanism.
#   Cerebras: estimates tokens before processing, rejects with 429 naming the bucket, and asks
#     clients to back off.
#
# THE POSTURE THIS SPEC PINS (the OpenRouter/LiteLLM core; no declared budgets - a station's
# ceiling is learned from its 429s, not asked for):
#
#   1. FAILOVER BEFORE THE ERROR REACHES THE CONSUMER. When a dispatched station answers with a
#      no-output failure (upstream 429, 5xx, or the station's own 502 "upstream unreachable")
#      BEFORE any content byte has reached the consumer, the broker re-picks excluding the
#      failed station(s) and dispatches again, up to ROGERAI_RELAY_ATTEMPTS (3) stations total
#      and within the request's existing deadline. The consumer sees only the final outcome.
#      Each failed attempt is voided exactly as today ($0 receipt, void_reason, upstream_status;
#      features/safety/upstream_throttle_not_a_strike.feature) so lineage is complete. The
#      consumer's hold is placed ONCE and must cover the priciest candidate actually tried; the
#      consumer is charged once, for the station that served. Streaming fails over until the
#      first content chunk; after that the stream ends as today (no mid-stream provider swap).
#   2. COOLDOWN, LEARNED. A station that answers 429 is cooling for the upstream's Retry-After
#      (default ROGERAI_STATION_COOLDOWN_DEFAULT 15s, cap ROGERAI_STATION_COOLDOWN_MAX 120s),
#      recorded in the shared store (TTL = cooldown) so every instance skips it. Cooling is a
#      pickFor hard filter when another candidate exists. It is routing state, never trust state.
#      A 5xx does not cool (health grading already handles it: success EWMA + probe streak).
#   3. THE ONLY STATION COOLING IS A FAST, HONEST ANSWER. When no eligible station remains
#      because every candidate is cooling, the broker returns 503 "band cooling" with
#      Retry-After = the soonest cooldown expiry, WITHOUT dispatching into a known 429 (no hold,
#      no receipt, no upstream call). Tier-B availability (features/routing/scoring.feature
#      "Tier B is used only when Tier A is empty") is preserved: cooling is a filter with a
#      Retry-After, never a blank band; /market and /discover keep the model on air.
#   4. RETRY-AFTER TRAVELS. JobResult gains retry_after_sec (omitempty). The station captures the
#      upstream's Retry-After on a 429/503. The broker sets Retry-After on the consumer response
#      whenever the final answer is a 429/503 (upstream-derived, or its own cooling 503), and the
#      proxy allowlist already forwards it (features/proxy/retry_after.feature).
#   5. PROBES ARE EXEMPT from cooldown filtering (a canary may probe a cooling station; a canary
#      that gets a 429 is a probe FAIL as today, not a cooldown and not a strike).
#   6. VISIBLE. /market and /discover carry cooling_until per offer; /admin/live counts failovers,
#      cooldowns, and cooling-503s; the dial shows a cooling station as on air, marked.
#
# NOT IN SCOPE (deliberately, YAGNI): station-declared rpm/tpm budgets (LiteLLM-style; revisit if
# a single-station band keeps 429ing after cooldown), broker-side queueing/waiting for a cooling
# station, mid-stream failover, per-consumer fairness, changing the client's retryable().
#
# GROUND TRUTH (origin/main e7d492d5): cmd/rogerai-broker/tunnel.go relay() ~1531 (one pickFor at
# ~1745, hold at ~1830-1870, dispatch, settle; the void branch ~2036-2053 passes status+body to
# the consumer with no Retry-After), relayStream() and its first-chunk boundary, pickFor ~2740
# (hard filters -> two-tier gate -> P2C; exclude set already exists for the client's
# X-Roger-Exclude-Nodes), exitInflight; internal/protocol/protocol.go:729 JobResult;
# internal/agent/agent.go serve ~816 / serveStream ~698 (forward resp.StatusCode + body; add
# Retry-After capture); sharedstore.go (rogerai:* keys, TTL primitives); market.go:96 offerView;
# internal/client/failover.go:79 retryable (unchanged), client.go copyRelayResponse (Retry-After
# already allowlisted). Step definitions: real in-memory store + miniredis, real httptest
# upstreams behind real station loops, godog, no mocks.
#
# KNOBS: ROGERAI_RELAY_ATTEMPTS=3  ROGERAI_STATION_COOLDOWN_DEFAULT=15s
#        ROGERAI_STATION_COOLDOWN_MAX=120s  ROGERAI_RELAY_FAILOVER=1 (0 = today's single pick)

Feature: A station that says no is routed around, cooled, and reported with a Retry-After - the consumer sees the band, not the station

  Background:
    Given a broker with a real store and a shared store
    And scripted upstreams behind real stations serving "m"

  # ===========================================================================
  # 1. FAILOVER - the error does not reach the consumer while a sibling can serve
  # ===========================================================================

  Scenario: an upstream 429 on the first station fails over to a sibling and the consumer sees 200
    Given stations "s1" and "s2" serve "m" at the same price
    And "s1"'s upstream returns 429 and "s2"'s returns a real completion
    When a funded consumer relays a prompt (non-stream) and the pick lands on "s1"
    Then the response is 200 with "s2"'s completion
    And X-RogerAI-Provider names "s2"
    And two receipts exist: "s1" voided with void_reason "upstream-throttled", "s2" settled with cost > 0
    And the consumer was charged exactly "s2"'s metered cost, once

  Scenario Outline: every no-output failure class fails over
    Given stations "s1" and "s2" serve "m"
    And "s1"'s upstream <failure> and "s2"'s returns a real completion
    When a funded consumer relays and the pick lands on "s1"
    Then the response is 200 from "s2"
    And "s1"'s receipt is voided with void_reason "<reason>"

    Examples:
      | failure                                  | reason             |
      | returns 429                              | upstream-throttled |
      | returns 500                              | upstream-error     |
      | returns 502                              | upstream-error     |
      | returns 503                              | upstream-error     |
      | returns 504                              | upstream-error     |
      | is unreachable (station posts 502)       | upstream-error     |
      | returns 200 with an empty completion     | empty-output       |

  Scenario Outline: client-caused failures do NOT fail over (the next station would fail the same way)
    Given stations "s1" and "s2" serve "m"
    And "s1"'s upstream returns <status> with <body>
    When a funded consumer relays and the pick lands on "s1"
    Then the response is <status> with the upstream body (one attempt, voided as today)
    And "s2"'s upstream received nothing

    Examples:
      | status | body                                          |
      | 400    | {"error":"invalid request"}                   |
      | 401    | {"error":"upstream key rejected"}             |
      | 404    | {"error":"model not found"}                   |
      | 413    | {"error":"request too large"}                 |
      | 422    | {"error":"unprocessable"}                     |

  Scenario: at most ROGERAI_RELAY_ATTEMPTS stations are tried
    Given stations "s1", "s2", "s3", "s4" serve "m" and all upstreams return 429
    When a funded consumer relays
    Then exactly 3 stations received the request
    And the response is 429 with the last upstream body and a Retry-After
    And three voided receipts exist and the consumer was charged 0

  Scenario: a failed station is never re-picked within the same request
    Given stations "s1" and "s2" serve "m" and both upstreams return 500
    When a funded consumer relays
    Then "s1" and "s2" each received exactly one request
    And the response is 500

  Scenario: failover respects the request's own deadline
    Given stations "s1" and "s2" serve "m"
    And "s1"'s upstream holds the request for 85 seconds then returns 500
    When a funded consumer relays with the 90 second relay deadline
    Then no second attempt is started with less than the minimum attempt budget remaining
    And the response is the timeout/first-failure outcome as today, voided

  Scenario: failover honors the consumer's pin and exclude headers
    Given stations "s1", "s2", "s3" serve "m" and "s1" 429s
    When a funded consumer relays with X-Roger-Exclude-Nodes "s2"
    Then the failover goes to "s3", never "s2"
    Given a funded consumer relays with X-Roger-Node "s1"
    Then a pinned request does NOT fail over: the response is 429 with Retry-After

  Scenario: failover respects price caps and the price lock
    Given "s1" (cheap) 429s and "s2" is priced above the consumer's X-Roger-Max-Price
    When a funded consumer relays
    Then "s2" is not tried and the response is 429 with Retry-After

  Scenario: a confidential-only request only fails over to confidential stations
    Given "s1" (confidential) 429s, "s2" is confidential, "s3" is not
    When a confidential-only relay is made
    Then the failover goes to "s2"

  Scenario: a private band only fails over within the band
    Given "s1" and "s2" are on private band B and "s3" is public; "s1" 429s
    When a band-B relay is made
    Then the failover goes to "s2", never "s3"

  Scenario: the failover is disabled by the knob and behaves exactly as today
    Given ROGERAI_RELAY_FAILOVER is "0"
    And "s1" 429s and "s2" is healthy
    When a funded consumer relays and lands on "s1"
    Then the response is 429 (one attempt) with Retry-After

  # ===========================================================================
  # 2. MONEY - one hold, one charge, complete lineage
  # ===========================================================================

  Scenario: the hold is placed once and covers the priciest station actually tried
    Given "s1" at 1.00/1.00 429s and "s2" at 2.00/2.00 serves
    When a funded consumer relays
    Then exactly one hold row exists, at least the max cost at 2.00/2.00
    And one hold_release and one spend for "s2"'s cost exist
    And the consumer's balance equals start minus "s2"'s cost

  Scenario: a failover to a pricier station that the hold cannot cover is not attempted
    Given "s1" at 1.00/1.00 429s and "s2" at 50.00/50.00 serves
    And the consumer's balance covers "s1" but not "s2"
    When a funded consumer relays
    Then "s2" is not tried (insufficient funds for its max cost)
    And the response is 429 with Retry-After and the consumer was charged 0

  Scenario: every attempt writes a receipt in the station's chain
    Given "s1" 429s, "s2" 500s, "s3" serves
    When a funded consumer relays
    Then "s1" and "s2" each have a voided receipt chained to their prev_hash
    And "s3" has a settled receipt
    And all three share the consumer's request id lineage (attempt 1, 2, 3)

  Scenario: the operator of a failed-over station earns nothing and is not struck for a 429
    Given "s1" 429s and "s2" serves
    When a funded consumer relays
    Then "s1"'s owner has no earn row and no strike
    And "s2"'s owner has one pending earn row

  Scenario: a grant-funded relay fails over under the grant's caps once, not per attempt
    Given a grant with a daily token cap and "s1" 429s, "s2" serves
    When a grant relay fails over
    Then the grant's cap is debited once, for the served attempt only

  Scenario: an anonymous free relay fails over between free stations only
    Given free stations "f1" (429s) and "f2" and a paid station "p1"
    When an anonymous relay is made
    Then the failover goes to "f2", never "p1"

  # ===========================================================================
  # 3. STREAMING - fail over until the first content chunk, then never
  # ===========================================================================

  Scenario: a streaming 429 before any chunk fails over transparently
    Given "s1" 429s and "s2" streams a completion
    When a funded consumer relays with "stream": true and lands on "s1"
    Then the SSE stream carries only "s2"'s chunks
    And the first chunk arrives within "s1"'s failure time plus "s2"'s TTFT
    And X-RogerAI-Provider names "s2"

  Scenario: a failure after the first content chunk does not fail over
    Given "s1" streams two chunks then dies mid-stream, "s2" is healthy
    When a funded consumer relays with "stream": true
    Then the stream ends as today (partial output, settled per today's mid-stream rule)
    And "s2"'s upstream received nothing

  Scenario: SSE headers are not committed before the first upstream verdict
    Given "s1" 429s and "s2" serves
    When a funded consumer relays with "stream": true
    Then the consumer receives a single 200 response (no 429 leaked before the failover)

  Scenario: a streaming request with every station failing returns the last error with Retry-After
    Given "s1" and "s2" both 429 with Retry-After 9
    When a funded consumer relays with "stream": true
    Then the response is 429 (not a 200 with an error event) with Retry-After 9

  # ===========================================================================
  # 4. COOLDOWN - learned from the 429, shared, capped, routing-only
  # ===========================================================================

  Scenario: a 429 with Retry-After cools the station for that long
    Given "s1"'s upstream returns 429 with "Retry-After: 10"
    When a relay hits "s1"
    Then "s1" is cooling for 10 seconds
    And the shared store holds rogerai:cool:s1 with a TTL of about 10 seconds

  Scenario: a 429 without Retry-After cools for the default
    Given "s1"'s upstream returns 429 with no Retry-After
    When a relay hits "s1"
    Then "s1" is cooling for 15 seconds

  Scenario Outline: Retry-After values are normalized and capped
    Given "s1"'s upstream returns 429 with "Retry-After: <value>"
    When a relay hits "s1"
    Then "s1" cools for <seconds> seconds

    Examples:
      | value                            | seconds |
      | 30                               | 30      |
      | 0                                | 15      |
      | 99999                            | 120     |
      | an HTTP-date 45 seconds from now | 45      |
      | garbage                          | 15      |

  Scenario: a cooling station is skipped while a sibling exists
    Given "s1" is cooling and "s2" is healthy
    When 10 relays are made for "m"
    Then all 10 land on "s2"

  Scenario: cooldown expires and the station is eligible again
    Given "s1" cooled for 10 seconds
    When 10 seconds pass
    Then "s1" is eligible in pickFor again

  Scenario: cooldown is shared across instances
    Given two broker instances share the store and "s1" cooled on instance A
    When instance B picks for "m"
    Then it skips "s1"

  Scenario: the shared store being unreachable falls back to per-instance cooldown
    Given the shared store is unreachable
    When "s1" 429s on instance A
    Then instance A skips "s1" and logs the fallback once; instance B may still pick it

  Scenario: a 5xx does not cool (health grading handles it)
    Given "s1"'s upstream returns 503
    When a relay hits "s1"
    Then "s1" is not cooling and its success average dropped as today

  Scenario: repeated 429s extend, never stack
    Given "s1" 429s with Retry-After 10 three times in a row
    Then "s1"'s cooling_until is 10 seconds after the LAST 429, not 30

  Scenario: cooldown never touches trust
    Given "s1" cooled 20 times today
    Then "s1"'s owner has zero strikes and is not held

  Scenario: a forged retry_after_sec cannot cool a station beyond the cap
    Given a station posts a JobResult with status 429 and retry_after_sec 1000000
    Then it cools for 120 seconds at most

  Scenario: a retry_after_sec on a 200 is ignored
    Given a station posts a 200 JobResult with retry_after_sec 60
    Then the station is not cooling and the consumer sees no Retry-After

  # ===========================================================================
  # 5. THE ONLY STATION IS COOLING - answer fast and honestly
  # ===========================================================================

  Scenario: the only station cooling returns 503 band cooling with Retry-After and no dispatch
    Given "s1" is the only station for "m" and is cooling for 8 more seconds
    When a funded consumer relays
    Then the response is 503 {"error":{"message":"band cooling - the station serving m was rate limited upstream, retry after 8s"}}
    And Retry-After is 8
    And no hold, no receipt, no upstream call

  Scenario: every candidate cooling picks the soonest expiry for Retry-After
    Given "s1" cools for 20s and "s2" for 5s and nothing else serves "m"
    When a funded consumer relays
    Then the response is 503 with Retry-After 5

  Scenario: a cooling band is still on air in the market
    Given "s1" is the only station for "m" and is cooling
    When a client reads /market and /discover
    Then "m" shows 1 provider, online true, and the offer carries "cooling_until"

  Scenario: a cooling-only band does not page "0 providers"
    Given "s1" is the only station for "m" and is cooling
    When the alert checker runs
    Then no "noproviders:m" alert fires

  Scenario: the first request after the cooldown goes through
    Given "s1" is the only station and its cooldown just expired
    When a funded consumer relays
    Then it is dispatched to "s1"

  # ===========================================================================
  # 6. RETRY-AFTER TRAVELS END TO END
  # ===========================================================================

  Scenario: a final upstream 429's Retry-After reaches the consumer
    Given every station for "m" 429s, the last with "Retry-After: 7"
    When a funded consumer relays
    Then the response is 429 with the upstream body and Retry-After: 7

  Scenario: a final upstream 429 without Retry-After gets the default as the hint
    Given every station for "m" 429s with no Retry-After
    When a funded consumer relays
    Then the response has Retry-After: 15

  Scenario: a final upstream 503 carries its Retry-After when present
    Given every station for "m" 503s, the last with "Retry-After: 3"
    When a funded consumer relays
    Then the response is 503 with Retry-After: 3

  Scenario: a 200 never carries Retry-After
    When a relay succeeds
    Then the response has no Retry-After header

  Scenario: a JobResult without retry_after_sec decodes as before (old stations)
    Given a JobResult JSON with no retry_after_sec
    When the broker decodes it
    Then retry_after_sec is 0 and nothing else changes

  Scenario: the station captures Retry-After only on 429 and 503
    Given the upstream returns 429 with Retry-After 7, then 503 with Retry-After 3, then 200 with Retry-After 99
    Then the JobResults carry retry_after_sec 7, 3, and 0 respectively

  Scenario: the proxy forwards Retry-After to the agent (regression pin)
    Given the broker returns 429 with Retry-After: 7
    When the local proxy relays it to the agent
    Then the agent sees Retry-After: 7

  # ===========================================================================
  # 7. PROBES
  # ===========================================================================

  Scenario: a probe may canary a cooling station
    Given "s1" is cooling
    When the prober runs
    Then the canary is dispatched to "s1" (probes bypass the cooldown filter)

  Scenario: a probe 429 is a probe failure, not a cooldown and not a strike
    Given "s1"'s upstream returns 429 to the canary
    When the prober runs
    Then the probe records FAIL (consecutive=1) as today
    And "s1" is not cooling and its owner has no strike

  # ===========================================================================
  # 8. OBSERVABILITY
  # ===========================================================================

  Scenario: /admin/live carries failover and cooldown counters
    Given 4 failovers, 6 cooldowns, and 2 cooling-503s happened
    When the founder reads /admin/live
    Then it shows relay_failovers 4, station_cooldowns 6, band_cooling_503 2
    And a per-station table with cooling_until

  Scenario: the dial shows a cooling station as on air, marked
    Given "s1" is cooling
    When the TUI renders the band
    Then the station row is on air with a "cooling" marker and the seconds remaining
    And the band is never shown as dark because of cooling

  Scenario: a failover is logged once with both stations
    Given "s1" 429s and "s2" serves
    When a funded consumer relays
    Then one "FAILOVER request=... from=s1 (upstream-throttled) to=s2" line is logged

  Scenario: a station that keeps cooling pages the founder once
    Given "s1" has been cooling for more than 10 minutes cumulative in the last hour with real demand behind it
    Then the founder alert "station_cooling:s1" fired once naming the band and the count
    And it clears after an hour without a cooldown

  # ===========================================================================
  # 9. ADVERSARIAL
  # ===========================================================================

  Scenario: a station cannot steer traffic to itself by 429ing a competitor's requests
    Given "s1" and "s2" serve "m"
    Then "s1" cannot cause "s2" to cool: cooldown is keyed on the station that answered 429

  Scenario: a malicious station returning 429 to everything is quarantined by probes, earns nothing, is not struck
    Given "s1"'s upstream returns 429 to all requests including canaries
    When 6 probe rounds run
    Then "s1" is excluded by the probe dead streak and its owner has zero earnings and zero strikes

  Scenario: failover cannot be used to double-charge
    Given "s1" 429s and "s2" serves
    When the same request is replayed on the bus (multi-instance duplicate result)
    Then exactly one spend row exists for the request

  Scenario: failover cannot leak one station's error body into another's success
    Given "s1" 429s with a body naming "s1" and "s2" serves
    When a funded consumer relays
    Then the response body is exactly "s2"'s completion

  # ===========================================================================
  # 10. REGRESSION - Sep 7 replayed twice
  # ===========================================================================

  Scenario: Sep 7 with a sibling station - the consumer never sees a 429
    Given the Cerebras station "cb" 429s for 66 of 228 requests and a sibling "pw" serves "qwen-3.8-27b"
    When one funded consumer relays 228 prompts
    Then all 228 responses are 200
    And 66 voided receipts name "cb" with void_reason "upstream-throttled" and 228 settled receipts exist
    And "cb"'s owner has zero strikes and is not held

  Scenario: Sep 7 with no sibling - every 429 carries a Retry-After and the band cools instead of churning
    Given the Cerebras station "cb" is the only station for "qwen-3.8-27b" and its upstream 429s with Retry-After 15 for any minute over 150000 tokens
    When one funded consumer relays 30 prompts of 83000 tokens within 3 minutes
    Then every non-200 response carries a Retry-After
    And after the first 429 in a minute the following requests in that cooldown get an immediate 503 band cooling without an upstream call
    And the upstream received fewer than 10 rejected requests (vs 66 on Sep 7)
    And "cb"'s owner has zero strikes and is not held
