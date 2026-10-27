# ONE ERROR ENVELOPE (slice 6, audit item #20): every error the broker returns on the relay and
# discovery paths has the same machine-readable shape, so SDKs and agents can branch on a code
# instead of parsing English, and every refusal says it cost nothing and which request it was.
#
# GROUND TRUTH (routing-slice6 at b1754466):
#   - Two helpers: jsonErr writes {"error":{"message"}} (cmd/rogerai-broker/httputil.go:43) and
#     jsonErrCode writes {"error":{"code","message"}} (httputil.go:49); errorBody builds the same
#     for the lazy SSE fail path (httputil.go:55). There is no `type` and no `metadata`.
#   - Relay paths that answer with NO code today (tunnel.go): station off air (3113), station busy
#     (3122), no poller (3125), node timed out 504 (2862), rate limit 429 (1759, 1773), grant rate
#     limit 429 (1744), invalid request signature 401 (1697, 418), spending requires a signed
#     request 401 (1713), session expired 401 (1719), the anonymous "log in to spend" 401 (2528),
#     the uniform private-band refusal 503 (1964, 1977, 2535), the grant-owner-not-serving 503
#     (2082), moderation 451 / fail-closed 503 (moderation.go: status + msg from the screener),
#     a truncated over-limit body (the 4 MiB reader truncates and the signature check answers
#     401). Upstream error bodies on a final failure are passed through raw (the station's own
#     JSON or text).
#   - Codes that already exist (keep them): unknown_routing_key, invalid_routing_value,
#     conflicting_routing_keys, unsupported_routing_key, unknown_profile, no_match,
#     band_cooling, grant_model_denied, insufficient_balance, monthly_cap_reached,
#     routing_outside_session (proxy), unknown_query_param, invalid_query_param, model_not_found.
#   - Slice 3 already sets X-RogerAI-Request-Id on every relay response; refusals carry
#     X-RogerAI-Cost: 0 (contract §2).
#
# RULES (contract draft B §14.B6):
#   - Envelope: {"error": {"code": string, "message": string, "type": string, "metadata": object}}.
#     `code` always present. `type` is one of OpenAI's values: invalid_request_error,
#     authentication_error, permission_error, not_found_error, rate_limit_error,
#     insufficient_quota, server_error, overloaded_error, timeout_error. `metadata` always
#     present (possibly {}), holding only: request_id, retry_after_s, station, model, attempts,
#     filters, raw (see below). Never: wallet ids, owner ids, IPs, band codes, prompt text.
#   - Code assignments for paths that have none today:
#       station off air            503 station_off_air        overloaded_error
#       station busy               503 station_busy           overloaded_error
#       no poller free             503 no_poller              overloaded_error
#       node timed out             504 station_timeout        timeout_error
#       relay rate limit           429 rate_limited           rate_limit_error
#       grant rate limit           429 grant_rate_limited     rate_limit_error
#       invalid signature          401 invalid_signature      authentication_error
#       unsigned spend             401 signature_required     authentication_error
#       expired session            401 session_expired        authentication_error
#       anonymous cannot pay       401 login_required         authentication_error
#       private-band refusal       503 band_unavailable       overloaded_error (the SAME code and
#                                   message for an unknown code, an off-air band and a band that
#                                   denies the model: no oracle)
#       grant owner not serving    503 grant_unavailable      overloaded_error
#       moderation refusal         451 content_refused        invalid_request_error (no category)
#       moderation fail-closed     503 moderation_unavailable server_error
#       final upstream failure     <upstream status> upstream_error  server_error / rate_limit_error
#                                   for 429; the upstream body goes under metadata.raw (truncated
#                                   to 4 KiB, as a string), metadata.station and metadata.model
#                                   name the station and served model EXCEPT on a private-band
#                                   request and for an anonymous caller naming a Tower (no-oracle
#                                   rules), where they are omitted.
#   - Existing codes keep their meaning; they gain `type` and `metadata`.
#   - Every error response carries X-RogerAI-Cost: 0 (unless a settle actually billed, which no
#     error path does) and X-RogerAI-Request-Id; Retry-After when retry_after_s is set, and the
#     two agree.
#   - Streams: an error after SSE commit is written as one `data: {"error":{...}}` frame in the
#     same envelope, then the broker's usage chunk and [DONE] (unchanged ordering rules).
#
# SUPERSEDES (founder approval needed for each):
#   - ROUTING-EXPRESSION-CONTRACT.md §2: "private band code unresolvable or band denies every
#     model: 503 (uniform message, no code that distinguishes)" -> one generic code
#     band_unavailable for all band refusals (still non-distinguishing).
#   - features/routing/node_preference.feature:143 "only naming a public station on a
#     private-band request is the uniform band message (no code)" and :333 "ignore that empties a
#     private band is the uniform band message (no code)" -> same message, code band_unavailable.
#   - Raw pass-through of a final upstream error body (today's behavior; pinned indirectly by
#     features/routing/upstream_failover.feature final-error scenarios and features/proxy/
#     errors.feature) -> wrapped under metadata.raw. upstream_failover.feature:470 ("failover
#     cannot leak one station's error body into another's success") is unaffected.
#
# Enforced by: cmd/rogerai-broker/error_envelope_bdd_test.go

Feature: Every broker error has one machine-readable envelope

  Background:
    Given a broker with an empty in-memory node registry
    And station "s1" is on air for "m" at in $0.10 out $0.30 per 1M
    And "u-1" is a funded consumer

  # --- the shape -------------------------------------------------------------------

  Scenario: An error has code, message, type and metadata
    When "u-1" relays for "m" with roger.bogus 1
    Then the response is 400
    And the body is {"error": {"code", "message", "type", "metadata"}}
    And error.type is "invalid_request_error"
    And error.metadata is an object

  Scenario: metadata carries the request id that the header carries
    When "u-1" relays for "m" with roger.bogus 1
    Then error.metadata.request_id equals X-RogerAI-Request-Id

  Scenario Outline: Existing codes keep their meaning and gain type and metadata
    Given <setup>
    When "u-1" relays for "m" with <request>
    Then the response is <status> with error code "<code>"
    And error.type is "<type>"

    Examples:
      | setup                              | request                                   | status | code                     | type                  |
      | nothing                            | roger.bogus 1                             | 400    | unknown_routing_key      | invalid_request_error |
      | nothing                            | provider.sort "fastest"                   | 400    | invalid_routing_value    | invalid_request_error |
      | nothing                            | provider.sort "price" and roger.pref "cheap" | 400 | conflicting_routing_keys | invalid_request_error |
      | nothing                            | roger.region ["ap"]                       | 503    | no_match                 | overloaded_error      |
      | "s1" is cooling for 20 more seconds| nothing                                   | 503    | band_cooling             | overloaded_error      |
      | "u-1" has a balance of $0.000001   | max_tokens 100000                         | 402    | insufficient_balance     | insufficient_quota    |

  # --- paths that gain a code ---------------------------------------------------------

  Scenario: Station off air is 503 station_off_air
    Given "s1" goes off air between the pick and the dispatch
    When "u-1" relays for "m" with provider.allow_fallbacks false
    Then the response is 503 with error code "station_off_air" and type "overloaded_error"

  Scenario: Station busy is 503 station_busy
    Given "s1" is the only station and has no free slot
    When "u-1" relays for "m" with provider.allow_fallbacks false
    Then the response is 503 with error code "station_busy"

  Scenario: No poller free is 503 no_poller
    Given "s1" has no poller listening on any instance
    When "u-1" relays for "m" with provider.allow_fallbacks false
    Then the response is 503 with error code "no_poller"

  Scenario: A non-stream station timeout is 504 station_timeout
    Given "s1" takes longer than the non-stream relay window
    When "u-1" relays for "m"
    Then the response is 504 with error code "station_timeout" and type "timeout_error"

  Scenario: The relay rate limit is 429 rate_limited with Retry-After in both places
    Given "u-1" has exhausted its relay rate bucket
    When "u-1" relays for "m"
    Then the response is 429 with error code "rate_limited" and type "rate_limit_error"
    And error.metadata.retry_after_s equals the Retry-After header

  Scenario: A grant's rate limit is 429 grant_rate_limited
    Given a grant "g1" with rpm 1 that was just used
    When the grant holder relays for "m"
    Then the response is 429 with error code "grant_rate_limited"

  Scenario: An invalid signature is 401 invalid_signature
    When a caller relays for "m" with a signature that does not verify
    Then the response is 401 with error code "invalid_signature" and type "authentication_error"

  Scenario: An unsigned spend is 401 signature_required
    When an unsigned caller with an X-Roger-User header relays for a paid station
    Then the response is 401 with error code "signature_required"

  Scenario: An expired web session is 401 session_expired
    When a browser with an expired session cookie relays for "m"
    Then the response is 401 with error code "session_expired"

  Scenario: An anonymous caller facing only paid supply is 401 login_required
    When an anonymous caller relays for "m"
    Then the response is 401 with error code "login_required"

  Scenario: An over-limit signed body is 401 invalid_signature (the reader truncates it)
    When "u-1" relays for "m" with a signed body larger than 4 MiB
    Then the response is 401 with error code "invalid_signature"

  Scenario: The grant owner not serving is 503 grant_unavailable
    Given a grant "g1" whose owner has no station on air
    When the grant holder relays for "m"
    Then the response is 503 with error code "grant_unavailable"

  Scenario: A moderation refusal is 451 content_refused and never names the category
    Given sync moderation flags the prompt as category "S1"
    When "u-1" relays for "m"
    Then the response is 451 with error code "content_refused"
    And the body does not contain "S1"

  Scenario: Moderation unavailable in fail-closed mode is 503 moderation_unavailable
    Given sync moderation is required and the classifier is unreachable
    When "u-1" relays for "m"
    Then the response is 503 with error code "moderation_unavailable" and type "server_error"

  # --- the private-band refusal stays uniform -------------------------------------------

  Scenario Outline: Every private-band refusal is the same code and the same message
    Given <band state>
    When "u-1" relays for "m" with X-Roger-Freq "<code>"
    Then the response is 503 with error code "band_unavailable"
    And the message is "no station on that frequency (it may be off air) - check the code"
    And error.metadata has no station and no model

    Examples:
      | band state                                     | code    |
      | no band has code "WRONG"                       | WRONG   |
      | band "B1" with code "FREQ-1" is off air        | FREQ-1  |
      | band "B1" with code "FREQ-1" denies model "m"  | FREQ-1  |

  Scenario: The three band refusals are byte-identical apart from the request id
    Given the three private-band refusal situations above
    When "u-1" triggers each once
    Then the three bodies are identical after removing error.metadata.request_id

  # --- upstream failures ----------------------------------------------------------------

  Scenario: A final upstream failure wraps the station's body under metadata.raw
    Given "s1" answers with status 500 and body {"detail":"CUDA out of memory"}
    When "u-1" relays for "m" with provider.allow_fallbacks false
    Then the response is 500 with error code "upstream_error" and type "server_error"
    And error.metadata.raw contains "CUDA out of memory"
    And error.metadata.station is "s1"
    And error.metadata.model is "m"

  Scenario: A final upstream 429 is upstream_error with type rate_limit_error and its Retry-After
    Given "s1" answers with status 429 and Retry-After 7
    When "u-1" relays for "m" with provider.allow_fallbacks false
    Then the response is 429 with error code "upstream_error" and type "rate_limit_error"
    And error.metadata.retry_after_s is 7

  Scenario: A plain-text upstream body is wrapped as a string
    Given "s1" answers with status 502 and the text body "bad gateway"
    When "u-1" relays for "m" with provider.allow_fallbacks false
    Then error.metadata.raw is the string "bad gateway"

  Scenario: metadata.raw is truncated to 4 KiB
    Given "s1" answers with status 500 and a 64 KiB body
    When "u-1" relays for "m" with provider.allow_fallbacks false
    Then error.metadata.raw is at most 4096 bytes

  Scenario: metadata.attempts counts the station attempts made
    Given "s1" and "s2" are on air for "m" and both answer 503
    When "u-1" relays for "m"
    Then error.metadata.attempts is 2

  Scenario: A private-band upstream failure does not name the station
    Given a private band "B1" with code "FREQ-1" whose station "p1" answers 500
    When "u-1" relays for "m" with X-Roger-Freq "FREQ-1"
    Then error.metadata has no station
    And error.metadata.raw is present

  Scenario: An anonymous caller naming a Tower never learns it is a Tower
    Given an approved Tower "t1" serves "m"
    When an anonymous caller relays for "m" with provider.only ["t1"]
    Then the response is 503 with error code "no_match"
    And error.metadata has no station

  Scenario: no_match lists the binding filters under metadata.filters
    When "u-1" relays for "m" with roger.region ["ap"]
    Then error.metadata.filters is ["region"]

  # --- headers on every error ----------------------------------------------------------

  Scenario Outline: Every error carries X-RogerAI-Cost 0 and X-RogerAI-Request-Id
    Given <setup>
    When "u-1" relays for "m" with <request>
    Then X-RogerAI-Cost is "0"
    And X-RogerAI-Request-Id is present

    Examples:
      | setup                                 | request                     |
      | nothing                               | roger.bogus 1               |
      | nothing                               | roger.region ["ap"]         |
      | "u-1" has exhausted its relay bucket  | nothing                     |
      | sync moderation flags the prompt      | nothing                     |
      | "s1" answers 500                      | provider.allow_fallbacks false |

  Scenario: Retry-After and metadata.retry_after_s always agree
    Given "s1" is cooling for 12 more seconds
    When "u-1" relays for "m"
    Then Retry-After is "12"
    And error.metadata.retry_after_s is 12

  # --- streams -------------------------------------------------------------------------

  Scenario: A stream refused before commit answers the envelope as plain JSON
    When "u-1" streams for "m" with roger.region ["ap"]
    Then the response is 503 with Content-Type application/json and the envelope

  Scenario: A stream failing after commit writes one error frame in the envelope, then the usage chunk and [DONE]
    Given "s1" sends one content frame then fails
    When "u-1" streams for "m"
    Then one data frame is {"error": {"code", "message", "type", "metadata"}}
    And the broker's usage chunk follows it
    And "[DONE]" is last

  # --- discovery paths ------------------------------------------------------------------

  Scenario Outline: Discovery errors use the same envelope
    When a consumer GETs <path>
    Then the response is <status> with error code "<code>"
    And the body has error.type and error.metadata

    Examples:
      | path                          | status | code                |
      | /discover?bogus=1             | 400    | unknown_query_param |
      | /discover?min_tps=abc         | 400    | invalid_query_param |
      | /v1/models/no-such-model      | 404    | model_not_found     |

  Scenario: /generation errors use the same envelope
    When "u-1" GETs /generation?id=not-a-valid-id
    Then the response is 400 with error code "invalid_request_id" and the envelope

  # --- privacy -------------------------------------------------------------------------

  Scenario: No error ever contains a wallet id, an owner id, an IP or a band code
    Given every refusal path above has been triggered once, including with band code "FREQ-1"
    Then no error body contains a "u_gh_" or "u_email_" id, an IP address, or "FREQ-1"

  Scenario: An error never echoes the caller's bearer secret
    When a caller relays for "m" with Authorization "Bearer rog-grant_SECRETVALUE" that is invalid
    Then the error body does not contain "SECRETVALUE"
