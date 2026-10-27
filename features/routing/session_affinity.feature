# SESSION AFFINITY (slice 6, audit item #6): a multi-turn conversation keeps landing on the
# station that served its previous turn, so the station's prompt cache is reused instead of
# re-prefilling the whole context on a new machine every turn. A PREFERENCE, never a limit.
#
# GROUND TRUTH (routing-slice6 at b1754466):
#   - The pick is seeded per REQUEST: seededRand(requestID) feeds pickFor (cmd/rogerai-broker/
#     tunnel.go:2230) and the edge plan (tunnel.go:2416). Nothing carries state between two
#     requests of one conversation, so consecutive turns spread over stations by design.
#   - No `session_id` / `roger.session` is parsed anywhere on the relay path (routingreq.go has
#     no such key; the only "session_id" in the broker is the remote-control session, rc.go:228,
#     unrelated). Today a body carrying `roger.session` is 400 unknown_routing_key and a top-level
#     `session_id` is forwarded to the station untouched.
#   - The consumer pseudonym a station sees is per (user, node): pseudonym(user, node) =
#     "u_" + hex(sha256(seed || user|node))[:16] (main.go:1212). Affinity must not change it.
#   - Cooling is per station today (cooling.go); slice 6 also adds per-(station, payer) cooling
#     (features/routing/fairness_and_abuse.feature). Affinity respects both.
#
# RULES (contract draft B §14.B1):
#   - `roger.session` (string) and OpenRouter's top-level `session_id` (string) mean the same
#     thing. Either alone works; both present and equal is fine; both present and different is
#     400 conflicting_routing_keys.
#   - Key = (payer, session id, served bare model). Value = the station id (or Tower relay id)
#     that served the last successful turn. Written on a SERVED attempt only; TTL 10 min of
#     inactivity (ROGERAI_AFFINITY_TTL, default 10m), refreshed on each served turn.
#   - On the next turn the affine server is tried FIRST if, at pick time, it is eligible under
#     every constraint of THIS request, Tier-A, not cooling (station or the (station, payer)
#     pair), and below its load limit (in_flight < capacity). Otherwise normal routing runs
#     silently and the session re-sticks to whoever serves.
#   - Explicit routing wins over affinity: provider.order, a pin (X-Roger-Node / order of one
#     with allow_fallbacks:false), provider.sort and sort sugar ignore affinity for the head.
#     roger.pref keeps affinity (pref only weights the scored pick, which affinity precedes).
#   - The affine server is a plan HEAD only; the failover plan behind it is built as today
#     (excluding the head), so affinity never removes a fallback.
#   - Session ids: opaque, 1..256 bytes of printable ASCII; longer or empty or control bytes are
#     400 invalid_routing_value. Stored hashed (HMAC with the broker secret), never logged raw,
#     never forwarded to a station (stripped like every routing carrier; top-level session_id
#     is stripped too).
#   - Never crosses payers: two payers sending the same session id get independent entries.
#   - Multi-instance: the entry lives in the shared store; an instance without the shared store
#     keeps a bounded local map (best effort, same rules).
#   - Telemetry: /admin/live counters affinity_hits, affinity_misses{ineligible,cooling,busy,
#     expired,explicit}; never per-session detail.
#
# Enforced by: cmd/rogerai-broker/session_affinity_bdd_test.go

Feature: A conversation keeps its station while it can, so the prompt cache is reused

  Background:
    Given a broker with an empty in-memory node registry
    And the fee rate is 30%
    And the affinity inactivity window is 10 minutes
    And stations "s1", "s2" and "s3" are on air for "m" at in $0.10 out $0.30 per 1M, Tier-A, idle
    And "u-1" is a funded consumer

  # --- carrying the session id -----------------------------------------------------

  Scenario: A second turn with the same roger.session lands on the station that served the first
    Given "u-1" relays for "m" with roger.session "conv-42" and the turn is served by "s2"
    When "u-1" relays for "m" with roger.session "conv-42"
    Then the response is 200 from "s2"
    And the affinity_hits counter rose by 1

  Scenario: OpenRouter's top-level session_id works the same as roger.session
    Given "u-1" relays for "m" with top-level session_id "conv-42" and the turn is served by "s3"
    When "u-1" relays for "m" with top-level session_id "conv-42"
    Then the response is 200 from "s3"

  Scenario: roger.session and session_id naming the same session are accepted together
    Given "u-1" relays for "m" with roger.session "conv-42" and the turn is served by "s1"
    When "u-1" relays for "m" with roger.session "conv-42" and top-level session_id "conv-42"
    Then the response is 200 from "s1"

  Scenario: roger.session and session_id naming different sessions are a 400
    When "u-1" relays for "m" with roger.session "conv-42" and top-level session_id "conv-43"
    Then the response is 400 with error code "conflicting_routing_keys" naming "session_id"
    And no station received anything
    And X-RogerAI-Cost is "0"

  Scenario: A request with no session id behaves exactly as before (no affinity)
    Given "u-1" relays for "m" with no session id and the turn is served by "s2"
    When "u-1" relays 40 times for "m" with no session id
    Then more than one station served
    And the affinity_hits counter did not move

  Scenario: The first turn of a session routes normally and records the server
    When "u-1" relays for "m" with roger.session "fresh-1"
    Then the response is 200
    And the affinity_misses{expired} counter did not move
    And the session "fresh-1" of "u-1" for "m" is affine to the station that served

  Scenario Outline: Malformed session ids are refused before any routing
    When "u-1" relays for "m" with roger.session <value>
    Then the response is 400 with error code "invalid_routing_value" naming "roger.session"
    And no station received anything

    Examples:
      | value                       |
      | ""                          |
      | 42                          |
      | true                        |
      | ["a"]                       |
      | {"id":"a"}                  |
      | a 257-byte string           |
      | "line\nbreak"               |
      | "tab\tinside"               |

  Scenario: A 256-byte session id is accepted
    When "u-1" relays for "m" with a 256-byte roger.session
    Then the response is 200

  Scenario: A null roger.session means absent
    When "u-1" relays for "m" with roger.session null
    Then the response is 200
    And the affinity_hits counter did not move

  # --- the preference yields to eligibility ----------------------------------------

  Scenario: An affine station that went off air is skipped silently and the session re-sticks
    Given "u-1" relays for "m" with roger.session "conv-42" and the turn is served by "s2"
    And "s2" goes off air
    When "u-1" relays for "m" with roger.session "conv-42"
    Then the response is 200 from "s1" or "s3"
    And the affinity_misses{ineligible} counter rose by 1
    And the session "conv-42" of "u-1" for "m" is affine to the station that served

  Scenario: An affine station that is cooling is skipped
    Given "u-1" relays for "m" with roger.session "conv-42" and the turn is served by "s2"
    And "s2" is cooling for 30 more seconds
    When "u-1" relays for "m" with roger.session "conv-42"
    Then the response is not served by "s2"
    And the affinity_misses{cooling} counter rose by 1

  Scenario: An affine station cooling only for this payer is skipped for this payer
    Given "u-1" relays for "m" with roger.session "conv-42" and the turn is served by "s2"
    And the pair ("s2", "u-1") is cooling for 30 more seconds
    When "u-1" relays for "m" with roger.session "conv-42"
    Then the response is not served by "s2"

  Scenario: An affine station at its load limit is skipped
    Given "u-1" relays for "m" with roger.session "conv-42" and the turn is served by "s2"
    And "s2" has in_flight equal to its capacity
    When "u-1" relays for "m" with roger.session "conv-42"
    Then the response is not served by "s2"
    And the affinity_misses{busy} counter rose by 1

  Scenario: An affine station that dropped to Tier B is skipped
    Given "u-1" relays for "m" with roger.session "conv-42" and the turn is served by "s2"
    And "s2" has a failing probe streak of 1 and success rate 0.30
    When "u-1" relays for "m" with roger.session "conv-42"
    Then the response is not served by "s2"

  Scenario Outline: An affine station that fails a constraint of THIS request is skipped
    Given "u-1" relays for "m" with roger.session "conv-42" and the turn is served by "s2"
    And "s2" <attribute>
    When "u-1" relays for "m" with roger.session "conv-42" and <constraint>
    Then the response is not served by "s2"

    Examples:
      | attribute                          | constraint                                     |
      | is priced at out $5.00             | provider.max_price.completion 1                |
      | measures 8 tok/s                   | roger.min_tps 20                               |
      | has quant "Q4_K_M"                 | provider.quantizations ["Q8_0"]                |
      | has no verified tools              | a non-empty tools array                        |
      | declares region "us"               | roger.region ["eu"]                            |
      | is curated                         | roger.self_hosted_only true                    |
      | is not TEE-attested                | roger.confidential true                        |
      | is excluded                        | provider.ignore ["s2"]                         |

  Scenario: An affine station that fails mid-request fails over and the session moves
    Given "u-1" relays for "m" with roger.session "conv-42" and the turn is served by "s2"
    And "s2" answers the next request with an upstream 503
    When "u-1" relays for "m" with roger.session "conv-42"
    Then the response is 200 from "s1" or "s3"
    And the session "conv-42" of "u-1" for "m" is affine to the station that served

  Scenario: The affine station is a plan head only; the failover plan behind it is intact
    Given "u-1" relays for "m" with roger.session "conv-42" and the turn is served by "s2"
    When "u-1" relays for "m" with roger.session "conv-42" and every station answers 429 once
    Then "s2" was tried first
    And the plan contained "s1" and "s3" after "s2"

  Scenario: A failed turn does not move the affinity
    Given "u-1" relays for "m" with roger.session "conv-42" and the turn is served by "s2"
    And every station answers the next request with an upstream 503
    When "u-1" relays for "m" with roger.session "conv-42"
    Then the response is 503
    And the session "conv-42" of "u-1" for "m" is still affine to "s2"

  Scenario: A voided turn (2xx with empty output) does not move the affinity
    Given "u-1" relays for "m" with roger.session "conv-42" and the turn is served by "s2"
    And "s1" and "s3" answer the next request with a 2xx and empty output and "s2" is cooling
    When "u-1" relays for "m" with roger.session "conv-42"
    Then the session "conv-42" of "u-1" for "m" is still affine to "s2"

  # --- inactivity --------------------------------------------------------------------

  Scenario: Affinity expires after the inactivity window
    Given "u-1" relays for "m" with roger.session "conv-42" and the turn is served by "s2"
    And 11 minutes pass with no turn of "conv-42"
    When "u-1" relays for "m" with roger.session "conv-42"
    Then the affinity_misses{expired} counter rose by 1
    And the pick was not forced to "s2"

  Scenario: Each served turn refreshes the inactivity window
    Given "u-1" relays for "m" with roger.session "conv-42" and the turn is served by "s2"
    And 9 minutes pass
    And "u-1" relays for "m" with roger.session "conv-42" and the turn is served by "s2"
    And 9 more minutes pass
    When "u-1" relays for "m" with roger.session "conv-42"
    Then the response is 200 from "s2"

  Scenario Outline: The inactivity window is a broker knob
    Given ROGERAI_AFFINITY_TTL is "<ttl>"
    And "u-1" relays for "m" with roger.session "conv-42" and the turn is served by "s2"
    And <gap> pass with no turn of "conv-42"
    When "u-1" relays for "m" with roger.session "conv-42"
    Then the response is <outcome>

    Examples:
      | ttl | gap        | outcome                     |
      | 2m  | 90 seconds | 200 from "s2"               |
      | 2m  | 3 minutes  | 200 not forced to "s2"      |
      | 30m | 25 minutes | 200 from "s2"               |

  # --- explicit routing wins ---------------------------------------------------------

  Scenario Outline: Explicit routing ignores affinity for the head
    Given "u-1" relays for "m" with roger.session "conv-42" and the turn is served by "s2"
    When "u-1" relays for "m" with roger.session "conv-42" and <explicit>
    Then the head of the plan is <head>
    And the affinity_misses{explicit} counter rose by 1

    Examples:
      | explicit                                         | head      |
      | provider.order ["s3"]                            | "s3"      |
      | X-Roger-Node "s1"                                | "s1"      |
      | provider.sort "price"                            | the sorted head |
      | model "m:floor"                                  | the sorted head |
      | model "m:nitro"                                  | the sorted head |

  Scenario: roger.pref keeps affinity
    Given "u-1" relays for "m" with roger.session "conv-42" and the turn is served by "s2"
    When "u-1" relays for "m" with roger.session "conv-42" and roger.pref "cheap"
    Then the response is 200 from "s2"

  Scenario: provider.only that contains the affine station keeps affinity
    Given "u-1" relays for "m" with roger.session "conv-42" and the turn is served by "s2"
    When "u-1" relays for "m" with roger.session "conv-42" and provider.only ["s2", "s3"]
    Then the response is 200 from "s2"

  Scenario: provider.only that excludes the affine station routes inside only
    Given "u-1" relays for "m" with roger.session "conv-42" and the turn is served by "s2"
    When "u-1" relays for "m" with roger.session "conv-42" and provider.only ["s1", "s3"]
    Then the response is 200 from "s1" or "s3"

  # --- scope of an entry ---------------------------------------------------------------

  Scenario: Affinity never crosses payers
    Given "u-1" relays for "m" with roger.session "shared-name" and the turn is served by "s2"
    And "u-2" is a funded consumer
    When "u-2" relays 40 times for "m" with roger.session "shared-name"
    Then the first relay of "u-2" was not forced to "s2"
    And the session "shared-name" of "u-2" for "m" is independent of "u-1"'s

  Scenario: Affinity is per model
    Given station "s1" also serves "m2"
    And "u-1" relays for "m" with roger.session "conv-42" and the turn is served by "s2"
    When "u-1" relays for "m2" with roger.session "conv-42"
    Then the pick for "m2" was not forced to "s2"

  Scenario: With models[] the entry is keyed on the model that actually served
    Given "u-1" relays for "a" with models ["m"] and roger.session "conv-42", "a" has no station, and the turn is served by "s2" as "m"
    When "u-1" relays for "m" with roger.session "conv-42"
    Then the response is 200 from "s2"

  Scenario: The station still sees the per-(user, node) pseudonym, unchanged by the session
    Given "u-1" relays for "m" with roger.session "conv-42" and the turn is served by "s2"
    When "u-1" relays for "m" with roger.session "conv-42"
    Then the job "s2" received carries the pseudonym of ("u-1", "s2")
    And it is the same pseudonym "s2" saw on the first turn

  # --- privacy ---------------------------------------------------------------------------

  Scenario: The session id is never forwarded to a station
    When "u-1" relays for "m" with roger.session "conv-42" and top-level session_id "conv-42"
    Then the body the serving station received has no "session_id" key and no "roger" key

  Scenario: The session id never appears in a log line or on /admin/live
    When "u-1" relays for "m" with roger.session "secret-conversation-name"
    Then no broker log line contains "secret-conversation-name"
    And /admin/live does not contain "secret-conversation-name"

  Scenario: The shared store holds a keyed hash, not the raw session id
    When "u-1" relays for "m" with roger.session "secret-conversation-name"
    Then no shared-store key or value contains "secret-conversation-name"

  Scenario: /generation does not echo the session id
    When "u-1" relays for "m" with roger.session "conv-42"
    Then the /generation record for that request does not contain "conv-42"

  # --- Towers and fabrics --------------------------------------------------------------

  Scenario: A Tower row that served the previous turn is the affine head
    Given an approved Tower "t1" serves "m" and no direct station is eligible
    And "u-1" relays for "m" with roger.session "conv-9" and the turn is served through "t1"
    And direct station "s1" comes back on air
    When "u-1" relays for "m" with roger.session "conv-9"
    Then the head of the plan is "t1"

  Scenario: Affinity does not let a free or self-use request reach a billed Tower
    Given the caller owns node "n-mine" on air for "m"
    And "u-1" relays for "m" with roger.session "conv-9" and the turn is served through "t1" priced $1.00
    When the owner relays for "m" with roger.session "conv-9"
    Then "n-mine" serves at $0

  # --- multi-instance and degraded store ----------------------------------------------

  Scenario: Affinity recorded on instance A is honored on instance B
    Given a second broker instance "B" shares the store
    And "u-1" relays on instance A for "m" with roger.session "conv-42" and the turn is served by "s2"
    When "u-1" relays on instance B for "m" with roger.session "conv-42"
    Then the response is 200 from "s2"

  Scenario: With the shared store unreachable, affinity degrades to best effort and never fails a request
    Given the shared store is unreachable
    When "u-1" relays twice for "m" with roger.session "conv-42"
    Then both responses are 200

  # tagged @later 2026-10-04 (RED runner): the bound is unnamed in contract §14.B1, so a test cannot set it;
  # filling 100000 entries through the relay is minutes per run.
  @later
  Scenario: The affinity map is bounded per instance
    Given 100000 distinct sessions have been recorded on one instance without a shared store
    When "u-1" relays for "m" with roger.session "conv-new"
    Then the response is 200
    And the local affinity map holds at most its configured bound

  # --- money and fairness --------------------------------------------------------------

  Scenario: Affinity never changes what is billed
    Given "u-1" relays for "m" with roger.session "conv-42" and the turn is served by "s2"
    When "u-1" relays for "m" with roger.session "conv-42"
    Then the hold covers the priciest pair of the plan exactly as without a session
    And the settle bills "s2" at its locked price

  Scenario: An affine station cannot keep a session it can no longer serve at the consumer's cap
    Given "u-1" relays for "m" with roger.session "conv-42" and the turn is served by "s2"
    And "s2" raises its out price to $20 per 1M
    When "u-1" relays for "m" with roger.session "conv-42"
    Then the response is not served by "s2" at a price above the consumer's out cap

  Scenario: A station cannot capture sessions by advertising itself in a session id
    When "u-1" relays for "m" with roger.session "s3"
    Then the pick is not forced to "s3"

  Scenario: The affinity counters never carry payer or session detail
    When "u-1" relays twice for "m" with roger.session "conv-42"
    Then /admin/live carries affinity_hits and affinity_misses as plain numbers
