# ROUTE EXPLAIN / DRY RUN (slice 6, audit item #17): a consumer can ask "where would this
# request go, what would it cost, and why not elsewhere" without sending the prompt anywhere,
# placing a hold or spending a cent.
#
# GROUND TRUTH (routing-slice6 at b1754466):
#   - Eligibility reasons exist only on FAILURE: noMatchFilters names the filters that turned
#     offers away in a 503 no_match message (cmd/rogerai-broker/netfilters.go:100). A successful
#     request never says why other stations were not picked.
#   - There is no pre-request cost estimate; the hold is computed inside relay() from
#     estimateMaxCost / holdCostFor and never surfaced except as the settled cost afterwards.
#   - `roger.dry_run` is not a routing key today (400 unknown_routing_key) and there is no
#     /v1/route/explain route.
#   - Admission rules a dry run must not leak around: private bands (uniform "no station on that
#     frequency" message, tunnel.go:1964), grant scope, anonymous-cannot-pay (tunnel.go:2528),
#     Tower relay ids (contract §6 no-oracle rule for anonymous callers).
#
# RULES (contract draft B §14.B3):
#   - `roger.dry_run: true` in a chat-completions body, or POST /v1/route/explain with the same
#     body (dry_run implied), returns 200 with an explain document and NEVER: dispatches, places
#     a hold or key reserve, runs moderation (the prompt is sent nowhere and not screened),
#     mints a price lock, consumes a station's capacity, writes a receipt or /generation record.
#   - The request is validated exactly like a real one: the same 400s / 401s / 403s (grant model
#     denial) with the same codes. A dry run of a request that would be refused for money
#     (402) or no supply (503 no_match / band_cooling) still returns 200 with
#     `"would": {"status": 402|503, "code": ...}` so the caller learns the outcome without the
#     error path (explain is a question, not a request).
#   - Explain document: {"request_id", "models": [effective list], "plan": [{model, station|tower,
#     price_in, price_out, tier, reason: "order"|"sort:<metric>"|"score"|"affinity"|"head"}],
#     "excluded": [{station, model, reasons: [filter names from the existing vocabulary]}],
#     "hold": usd, "cost_estimate": {"min": usd, "max": usd}, "would": {...}}.
#   - Visibility: exactly what a real request by the same caller could reach. Private-band
#     stations appear only when the request carries a valid code for that band; other private
#     stations never appear (not even in `excluded`). Towers appear for a signed-in funded
#     caller; for an anonymous caller excluded/plan entries are limited to stations listed on
#     /discover. Grant callers see only the grant owner's stations.
#   - Costs: the hold is the hold the real request would place; cost_estimate.min = measured
#     prompt tokens × in price of the plan head (zero output), max = the hold.
#   - Headers: X-RogerAI-Cost: 0, X-RogerAI-Request-Id (a dry-run id, prefixed "dry_"),
#     X-RogerAI-Dry-Run: true. Rate-limited like /market (its own bucket), never the relay
#     bucket.
#   - dry_run combined with stream: true is fine (the explain document is returned as JSON, no
#     SSE). dry_run with any non-boolean value is 400 invalid_routing_value; false/null = absent.
#
# Enforced by: cmd/rogerai-broker/route_explain_bdd_test.go

Feature: A dry run explains where a request would go and what it would cost, spending nothing

  Background:
    Given a broker with an empty in-memory node registry
    And the fee rate is 30%
    And station "s1" is on air for "m" at in $0.10 out $0.30 per 1M, Tier-A, tps 40, quant "Q8_0", region "eu"
    And station "s2" is on air for "m" at in $0.20 out $0.60 per 1M, Tier-A, tps 90, quant "Q4_K_M", region "us"
    And station "s3" is on air for "m" at in $0.05 out $0.10 per 1M, Tier-A, tps 8, quant "Q8_0", region "eu"
    And "u-1" is a funded consumer

  # --- spends nothing ------------------------------------------------------------------

  Scenario: A dry run returns the plan and dispatches nothing
    When "u-1" posts a chat completion for "m" with roger.dry_run true
    Then the response is 200 with an explain document
    And no station received anything
    And no hold was placed
    And X-RogerAI-Cost is "0"
    And X-RogerAI-Dry-Run is "true"

  Scenario: POST /v1/route/explain is the same as dry_run true
    When "u-1" posts the same body to /v1/route/explain
    Then the response is 200 with an explain document equal in plan and exclusions to the dry_run one

  Scenario: A dry run never sends the prompt to moderation
    Given sync moderation is enabled
    When "u-1" posts a chat completion for "m" with roger.dry_run true and a prompt the classifier would flag
    Then the moderation classifier was not called
    And the response is 200

  Scenario: A dry run mints no price lock, writes no receipt and no /generation record
    When "u-1" posts a chat completion for "m" with roger.dry_run true
    Then no price lock exists for ("u-1", any station, "m")
    And no receipt exists for the dry-run request id
    And GET /generation for the dry-run request id is 404

  # tagged @later 2026-10-04 (RED runner): needs the slice-5 key object, which is not on this branch
  @later
  Scenario: A dry run places no key reserve and counts no spend
    Given "u-1" relays through key "k1" with roger.dry_run true
    Then key "k1" spend is unchanged

  Scenario: A dry run consumes no station capacity
    Given "s1" has capacity 1 and in_flight 0
    When "u-1" posts 20 dry runs for "m" concurrently
    Then "s1" in_flight stayed 0 throughout

  # --- the plan --------------------------------------------------------------------------

  Scenario: The plan lists every attempt the real request would make, in order, with prices and reasons
    When "u-1" posts a chat completion for "m" with roger.dry_run true and provider.sort "price"
    Then the plan is ["s3", "s1", "s2"] for model "m"
    And each plan entry names its in and out price, its tier and reason "sort:price"

  Scenario: An ordered request's plan names the order as its reason
    When "u-1" posts a chat completion for "m" with roger.dry_run true and provider.order ["s2", "s1"]
    Then the plan head is "s2" with reason "order"
    And "s1" follows with reason "order"

  Scenario: A scored request's plan head names its reason "score"
    When "u-1" posts a chat completion for "m" with roger.dry_run true
    Then the plan head has reason "score"

  Scenario: A models[] dry run lists the plan per model in list order
    Given station "s4" is on air for "m2" at in $0.10 out $0.30 per 1M
    When "u-1" posts a chat completion for "m" with models ["m2"] and roger.dry_run true
    Then the effective models are ["m", "m2"]
    And every "m" entry precedes every "m2" entry in the plan

  Scenario: allow_fallbacks false shows a plan of one attempt per model
    When "u-1" posts a chat completion for "m" with roger.dry_run true and provider.allow_fallbacks false
    Then the plan has exactly 1 entry

  Scenario: A Tower row in the plan is named by its relay id
    Given an approved Tower "t1" serves "m" at out $0.25 per 1M
    When "u-1" posts a chat completion for "m" with roger.dry_run true and provider.order ["t1"]
    Then the plan head is tower "t1"

  Scenario: The plan never contains the same station twice
    When "u-1" posts a chat completion for "m" with roger.dry_run true
    Then no station appears twice in the plan

  # --- exclusions -------------------------------------------------------------------------

  Scenario Outline: Every on-air station the request excludes is listed with its reasons
    When "u-1" posts a chat completion for "m" with roger.dry_run true and <constraint>
    Then "<station>" is excluded with reason "<reason>"
    And the excluded station does not appear in the plan

    Examples:
      | constraint                                | station | reason         |
      | provider.quantizations ["Q4_K_M"]         | s1      | quantizations  |
      | roger.region ["us"]                       | s3      | region         |
      | roger.min_tps 20                          | s3      | min_tps        |
      | provider.max_price.completion 0.5         | s2      | max_price_out  |
      | provider.max_price.prompt 0.15            | s2      | max_price_in   |
      | provider.ignore ["s1"]                    | s1      | ignore         |
      | provider.only ["s2"]                      | s1      | only           |

  # founder ruling 2026-10-05: replaces the outline row that expected the never-curated "s3" to
  # be excluded for self_hosted_only (only a curated station is), with a station of its own so no
  # other scenario's Background changes.
  Scenario: A curated station is excluded for "self_hosted_only"
    Given station "s4" is a curated station on air for "m" at in $0.05 out $0.10 per 1M, Tier-A, tps 40, quant "Q8_0", region "eu"
    When "u-1" posts a chat completion for "m" with roger.dry_run true and roger.self_hosted_only true
    Then "s4" is excluded with reason "self_hosted_only"
    And the excluded station does not appear in the plan

  Scenario: A station failing two filters lists both reasons
    When "u-1" posts a chat completion for "m" with roger.dry_run true and roger.region ["us"] and roger.min_tps 20
    Then "s3" is excluded with reasons "region" and "min_tps"

  Scenario: A tools request lists stations without verified tools as excluded for "capability tools"
    Given "s1" earned verified "tools" for "m"
    When "u-1" posts a chat completion for "m" with roger.dry_run true and a non-empty tools array
    Then "s2" and "s3" are excluded with reason "capability tools"

  Scenario: A cooling station is listed as excluded for "cooling" with its Retry-After
    Given "s1" is cooling for 30 more seconds
    When "u-1" posts a chat completion for "m" with roger.dry_run true
    Then "s1" is excluded with reason "cooling" and retry_after_s about 30

  Scenario: A station whose declared window is too small for the prompt is excluded for "context_window"
    Given "s1" declares a context window of 2048
    When "u-1" posts a chat completion for "m" with roger.dry_run true and a 4000-token prompt
    Then "s1" is excluded with reason "context_window"

  Scenario: Exclusion reasons use exactly the filter vocabulary of the no_match message
    When "u-1" posts a chat completion for "m" with roger.dry_run true and roger.region ["ap"]
    Then every exclusion reason is one of the names noMatchFilters can produce, plus cooling, ignore, only, context_window and capability <name>

  # --- money -------------------------------------------------------------------------------

  Scenario: The hold reported is the hold the real request would place
    When "u-1" posts a chat completion for "m" with roger.dry_run true and max_tokens 1000
    And "u-1" then sends the same request without dry_run
    Then the dry run's hold equals the hold the real request placed

  Scenario: The cost estimate spans prompt-only to the hold
    When "u-1" posts a chat completion for "m" with roger.dry_run true, a 1000-token prompt and max_tokens 500
    Then cost_estimate.min is the 1000 prompt tokens at the plan head's in price
    And cost_estimate.max equals the hold

  Scenario: A dry run by a caller who could not afford the request says so without an error
    Given "u-poor" has a balance of $0.000001
    When "u-poor" posts a chat completion for "m" with roger.dry_run true
    Then the response is 200
    And would.status is 402 with would.code "insufficient_balance"

  Scenario: A dry run with no eligible station says no_match without an error
    When "u-1" posts a chat completion for "m" with roger.dry_run true and roger.region ["ap"]
    Then the response is 200
    And would.status is 503 with would.code "no_match"
    And the plan is empty

  Scenario: A dry run while every candidate cools reports band_cooling with the soonest expiry
    Given "s1", "s2" and "s3" are cooling for 20, 5 and 40 more seconds
    When "u-1" posts a chat completion for "m" with roger.dry_run true
    Then would.status is 503 with would.code "band_cooling" and would.retry_after_s 5

  Scenario: A dry run under the monthly cap reports the cap without an error
    Given "u-1" has a monthly cap that this request's hold would exceed
    When "u-1" posts a chat completion for "m" with roger.dry_run true
    Then would.status is 402 with would.code "monthly_cap_reached"

  # --- validation ------------------------------------------------------------------------

  Scenario Outline: A dry run is validated exactly like a real request
    When "u-1" posts a chat completion for "m" with roger.dry_run true and <bad>
    Then the response is 400 with error code "<code>"
    And no station received anything

    Examples:
      | bad                                      | code                     |
      | roger.bogus 1                            | unknown_routing_key      |
      | provider.sort "fastest"                  | invalid_routing_value    |
      | provider.sort "price" and roger.pref "cheap" | conflicting_routing_keys |
      | models with 6 distinct entries           | invalid_routing_value    |

  Scenario Outline: dry_run itself is validated
    When "u-1" posts a chat completion for "m" with roger.dry_run <value>
    Then the response is <outcome>

    Examples:
      | value   | outcome                                          |
      | true    | 200 with an explain document                     |
      | false   | 200 served by a station (a real request)         |
      | null    | 200 served by a station (a real request)         |
      | "true"  | 400 with error code "invalid_routing_value"      |
      | 1       | 400 with error code "invalid_routing_value"      |

  Scenario: A grant whose scope denies the model gives the same 403 on a dry run
    Given a grant "g1" that allows only model "other"
    When the grant holder posts a chat completion for "m" with roger.dry_run true
    Then the response is 403 with error code "grant_model_denied"

  Scenario: An unsigned spend-style request is refused the same way on a dry run
    When an invalidly signed caller posts a chat completion for "m" with roger.dry_run true
    Then the response is 401

  Scenario: dry_run with stream true returns the explain document as JSON, not SSE
    When "u-1" posts a chat completion for "m" with roger.dry_run true and stream true
    Then the response Content-Type is application/json
    And the body is an explain document

  # --- no oracle ----------------------------------------------------------------------------

  Scenario: A private-band station never appears without the band code
    Given a private band "B1" whose only station is "p1" on air for "m"
    When "u-1" posts a chat completion for "m" with roger.dry_run true
    Then "p1" appears neither in the plan nor in excluded

  Scenario: A private-band station appears when the request carries a valid code
    Given a private band "B1" with code "FREQ-1" whose only station is "p1" on air for "m"
    When "u-1" posts a chat completion for "m" with roger.dry_run true and X-Roger-Freq "FREQ-1"
    Then the plan is ["p1"]

  Scenario: An unresolvable band code on a dry run gives the same uniform message as a real request
    When "u-1" posts a chat completion for "m" with roger.dry_run true and X-Roger-Freq "WRONG"
    Then the response is the uniform "no station on that frequency (it may be off air) - check the code" refusal

  Scenario: An anonymous dry run sees only stations listed on /discover and no Towers
    Given an approved Tower "t1" serves "m"
    When an anonymous caller posts a chat completion for "m" with roger.dry_run true
    Then every station named in the plan or excluded is listed on /discover
    And no Tower relay id appears in the response

  Scenario: A grant dry run sees only the grant owner's stations
    Given a free grant "g1" owned by the operator of "s1"
    When the grant holder posts a chat completion for "m" with roger.dry_run true
    Then the plan contains only "s1"
    And "s2" and "s3" appear neither in the plan nor in excluded

  Scenario: A dry run does not reveal another payer's session affinity or cooling-per-payer state
    Given the pair ("s1", "u-2") is cooling
    When "u-1" posts a chat completion for "m" with roger.dry_run true
    Then "s1" is not excluded for "cooling"

  # --- rate limiting and abuse ---------------------------------------------------------------

  Scenario: Dry runs use their own rate bucket, not the relay bucket
    Given "u-1" has exhausted its relay rate bucket
    When "u-1" posts a chat completion for "m" with roger.dry_run true
    Then the response is 200 with an explain document

  Scenario: Dry runs are rate-limited like /market
    When "u-1" posts dry runs faster than the /market rate limit
    Then some responses are 429 with error code "rate_limited" and a Retry-After

  Scenario: The dry-run request id is distinguishable and never collides with a real one
    When "u-1" posts a chat completion for "m" with roger.dry_run true
    Then X-RogerAI-Request-Id starts with "dry_"

  Scenario: A dry run's explain document never contains the prompt
    When "u-1" posts a chat completion for "m" with roger.dry_run true and the prompt "secret plan text"
    Then the response body does not contain "secret plan text"
    And no broker log line contains "secret plan text"

  Scenario: Explain never reports a station's raw address or owner account
    When "u-1" posts a chat completion for "m" with roger.dry_run true
    Then no entry carries an IP address, owner id or wallet id
