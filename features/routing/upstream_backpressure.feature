# ROUTING - UPSTREAM BUDGETS AND BACKPRESSURE ("know the provider's ceiling before you hit it").
#
# THE INCIDENT (verified, prod 2026-09-07 18:02-18:24 UTC): the house Cerebras station
# house-cb-qwen-3-8-27b forwarded 400K-770K input tokens per minute from ONE consumer to a
# provider whose pay-as-you-go tier allows 150K uncached / 450K total tokens per minute and 300
# requests per minute for that model. Cerebras answered 66 requests with 429. Nothing in RogerAI
# knew the ceiling existed: the station has no rate knob at all (`roger share` has only
# --parallel, default 4 per serving plane, and the poll plane and the Tower fabric plane share NO
# limiter, so a station can run 8 concurrent generations), the registration and heartbeat carry
# no budget or load fields, the broker's pickFor has no hard capacity filter (loadFactor is a soft
# discount that never reaches zero, so the only station for a model is picked every time), and
# JobResult carries {id, status, body, receipt} with no headers, so the upstream's Retry-After
# never reaches the broker or the consumer. The consumer got 66 bare 429s and the client's
# retryable() (status >= 500 only) treated each as terminal.
#
# THE POSTURE THIS SPEC PINS:
#
#   1. A STATION CAN DECLARE ITS UPSTREAM BUDGET. New optional, signed-omitempty registration
#      fields per offer: rpm (requests/min) and tpm (tokens/min, prompt+completion). `roger share`
#      gains --upstream-rpm / --upstream-tpm; the house conf line gains two trailing fields.
#      Undeclared = unlimited = today's behavior. This is a generic station capability
#      (features/curated/curated_routing.feature "Routing judges curated by the same terms as
#      any station" stands: curated stations SET it, routing does not special-case them).
#   2. THE BROKER ADMITS AGAINST THE BUDGET BEFORE DISPATCH. One shared token bucket per station
#      per axis (the existing rateLimiter/allowAt + Valkey Lua bucket, keyed rogerai:rl:station-
#      rpm:<node> and rogerai:rl:station-tpm:<node>, so two instances share ONE budget - the
#      features/multinode/rate_limits.feature rule). Tokens are reserved from the prompt estimate
#      already used for the hold and reconciled to actual usage at settle. A station whose bucket
#      cannot cover the request is a HARD FILTER in pickFor when another candidate exists, and a
#      clean 503 "band saturated" + Retry-After when none does (never a silent dispatch into a
#      known 429). Tier-B availability (features/routing/scoring.feature "Tier B is used only when
#      Tier A is empty") is preserved: saturation is a filter with a Retry-After, not a blank band.
#   3. THE STATION ENFORCES THE SAME BUDGET LOCALLY, SHARED ACROSS BOTH PLANES. Second line of
#      defense (the broker's estimate can be wrong; a Tower can dispatch too). Over budget, the
#      station refuses with 503 + retry_after (NOT 429: a 503 fails over client-side, a 429 kills
#      the request) and never spends the provider key.
#   4. RETRY-AFTER TRAVELS. JobResult gains retry_after_sec (omitempty). The station captures the
#      upstream's Retry-After on a 429/503 (or computes one from its bucket). The broker sets
#      Retry-After on the consumer response for any 429/503 that carries it and on its own
#      saturation 503. Nothing else about the pass-through changes (status + body verbatim).
#   5. AN UPSTREAM 429 THAT STILL HAPPENS COOLS THE STATION. The broker marks the station cooling
#      for retry_after_sec (default 15s, cap 120s) in the shared store; cooling is the same hard
#      filter as saturation. Cooldown is routing state, never trust state
#      (features/safety/upstream_throttle_not_a_strike.feature).
#   6. PROBES AND CANARIES ARE BUDGET-EXEMPT (features/curated/curated_probes.feature keeps its
#      minimal cadence; the founder pays those tokens knowingly).
#   7. VISIBLE. /market and /discover carry per-offer budget state (saturated/cooling, until
#      when); /admin/live counts saturation refusals and cooldowns; the dial shows a station that
#      is on air but throttled as on air, not dark.
#
# NOT IN SCOPE: server-side retry/failover (the broker still picks once; the client fails over on
# 5xx as today), queueing requests at the broker, output-token prediction beyond the existing
# hold estimate, per-consumer fairness across a saturated band.
#
# GROUND TRUTH (origin/main e7d492d5): cmd/rogerai/main.go:1099-1149 share flags (+ :1057
# validateCuratedShare); internal/protocol/protocol.go:33 ModelOffer, :346 NodeRegistration,
# :449 regSigningBytes (new fields MUST be omitempty - old-node signatures), :729 JobResult;
# internal/agent/agent.go:515 pollLoop workers, :816 serve, :698 serveStream, :626 heartbeat,
# internal/agent/tower.go:1100 the second serving plane; cmd/rogerai-broker/ratelimit.go
# rateLimiter/allowAt, sharedstore.go:645 valkey rateAllow (Lua bucket, rogerai:rl:<name>:<key>);
# tunnel.go:2740 pickFor (hard filters -> two-tier gate -> P2C), :1810 "no node offers", :1949
# "node busy (no poller free)"; router.go:273 loadFactor, :262 capacityOf; market.go:96 offerView,
# :712 congestion term; internal/client/failover.go:79 retryable, client.go:969 copyRelayResponse
# (Retry-After already on the allowlist); ~/rogerai-house/run-station.sh + stations.conf (the
# `cerebras:<model>|<in>|<out>|<ctx>` line). Step definitions: real in-memory store + miniredis
# for the shared bucket, a real httptest upstream behind a real station loop, godog (no mocks).
#
# KNOBS:
#   station:  --upstream-rpm N  --upstream-tpm N   (0/absent = unlimited)
#   conf:     cerebras:<model>|<in>|<out>|<ctx>|<rpm>|<tpm>   (trailing fields optional)
#   broker:   ROGERAI_STATION_COOLDOWN_DEFAULT=15s  ROGERAI_STATION_COOLDOWN_MAX=120s
#             ROGERAI_STATION_BUDGET_ADMIT=1 (0 = observe-only: count, never filter)
#   Cerebras qwen-3.8-27b pay-as-you-go reference: rpm 300, tpm 150000 uncached / 450000 total.

Feature: A station's upstream budget is declared, admitted against, enforced locally, and never silently exceeded

  Background:
    Given a broker with a real store and a shared bucket store
    And a scripted upstream that records request timestamps and token counts

  # ===========================================================================
  # 1. DECLARATION - the budget rides the registration, signed, optional
  # ===========================================================================

  Scenario: a station declares rpm and tpm per offer and the broker stores them
    When a station registers "m" with --upstream-rpm 300 --upstream-tpm 150000
    Then the stored registration offer carries rpm 300 and tpm 150000
    And /discover shows the offer with "rpm": 300 and "tpm": 150000

  Scenario: an undeclared budget means unlimited (today's behavior, byte-identical registration)
    When a station registers "m" with neither flag
    Then the registration JSON has no "rpm" and no "tpm" key
    And the registration signature verifies with the pre-change signing bytes

  Scenario: an old node without the fields still registers and still verifies
    Given a registration payload signed by a node built before the fields existed
    When it is submitted
    Then it is accepted and the station is unlimited

  Scenario Outline: invalid budget values are rejected at the station, never sent
    When `roger share` is started with <flags>
    Then it exits non-zero with an error naming the flag

    Examples:
      | flags                  |
      | --upstream-rpm -1      |
      | --upstream-tpm -5      |
      | --upstream-rpm 0.5     |
      | --upstream-tpm abc     |

  Scenario: the house conf line carries the budget
    Given a stations.conf line "cerebras:qwen-3.8-27b|0.99|1.49|128000|300|150000"
    When run-station.sh renders the exec line
    Then it passes --upstream-rpm 300 --upstream-tpm 150000
    And a line without the trailing fields passes neither flag

  Scenario: a budget change is picked up on re-register, not mid-flight
    Given a station registered with tpm 150000
    When it re-registers with tpm 450000
    Then admission uses 450000 for requests picked after the re-register
    And in-flight requests are unaffected

  Scenario: a tampered budget fails the registration signature
    Given a signed registration with tpm 150000
    When the tpm field is altered in transit to 999999
    Then the broker rejects the registration (signature mismatch)

  # ===========================================================================
  # 2. BROKER ADMISSION - pickFor never knowingly dispatches into a 429
  # ===========================================================================

  Scenario: a request within budget is admitted and the estimate is reserved
    Given station "s1" serves "m" with tpm 100000 and rpm 60
    When a funded consumer relays a prompt estimated at 20000 tokens
    Then it is dispatched to "s1"
    And the station's tpm bucket shows about 80000 tokens remaining

  Scenario: the reservation is reconciled to actual usage at settle
    Given station "s1" serves "m" with tpm 100000
    When a relay estimated at 20000 tokens settles with actual usage 12000
    Then the bucket is credited back 8000
    When a relay estimated at 20000 tokens settles with actual usage 30000
    Then the bucket is debited a further 10000

  Scenario: a voided request releases its reservation
    Given station "s1" serves "m" with tpm 100000
    When a relay estimated at 20000 tokens is voided (upstream 5xx)
    Then the bucket is credited back the full 20000

  Scenario: a saturated station is a hard filter when a sibling exists
    Given stations "s1" (tpm 50000) and "s2" (unlimited) both serve "m"
    And "s1"'s bucket holds 10000 tokens
    When a funded consumer relays a prompt estimated at 30000 tokens
    Then it is dispatched to "s2"
    And the admin counter station_saturated_skips reads 1

  Scenario: the only station being saturated returns a clean 503 with Retry-After, not a dispatch
    Given station "s1" (tpm 50000) is the only station for "m"
    And "s1"'s bucket holds 10000 tokens refilling at 833 per second
    When a funded consumer relays a prompt estimated at 30000 tokens
    Then the response is 503 {"error":{"message":"band saturated - the station serving m is at its upstream budget, retry after 24s"}}
    And the Retry-After header is 24 (the seconds until the bucket can cover the estimate)
    And no job was dispatched, no hold was placed, no receipt was written
    And the upstream received nothing

  Scenario: saturation is never a blank band - the offer stays listed as on air
    Given station "s1" (tpm 50000) is saturated
    When a client reads /market and /discover
    Then "m" still shows 1 provider, online true
    And the offer carries "saturated_until" with a future timestamp

  Scenario: the rpm axis is enforced independently of tpm
    Given station "s1" serves "m" with rpm 2 and tpm unlimited
    When a funded consumer relays 3 tiny prompts within one second
    Then two are dispatched and the third is 503 band saturated with Retry-After

  Scenario: a request larger than the whole budget is refused immediately with a clear message
    Given station "s1" serves "m" with tpm 50000
    When a funded consumer relays a prompt estimated at 90000 tokens
    Then the response is 503 with "exceeds the station's per-minute budget (50000 tokens)" and no Retry-After
    And no strike, no hold, no receipt

  Scenario: two broker instances share one budget
    Given two broker instances share the bucket store and station "s1" (tpm 50000) serves "m"
    When instance A admits a 30000-token relay and instance B admits a 30000-token relay in the same second
    Then exactly one is dispatched and the other is 503 band saturated
    And the shared bucket, not two local ones, decided it

  Scenario: the bucket store being unreachable falls back to a local bucket and keeps serving
    Given the shared bucket store is unreachable
    When a funded consumer relays within budget
    Then it is dispatched using the instance-local bucket
    And one "station-budget: shared store unavailable, local buckets" line is logged (once)

  Scenario: observe-only mode counts but never filters
    Given ROGERAI_STATION_BUDGET_ADMIT is "0"
    And station "s1" (tpm 50000) is the only station for "m" and is saturated
    When a funded consumer relays a 30000-token prompt
    Then it is dispatched anyway
    And the admin counter station_saturated_observed reads 1

  Scenario: an unlimited station is never touched by admission
    Given station "s1" serves "m" with no budget
    When 100 funded consumers relay simultaneously
    Then all 100 are dispatched and no bucket key was created for "s1"

  Scenario: the estimate uses the same token estimate as the hold
    Given a prompt whose hold estimate is 4321 prompt tokens plus the requested max_tokens 500
    When it is admitted
    Then the reservation is 4821 tokens

  # ===========================================================================
  # 3. STATION ENFORCEMENT - the provider key is never spent over budget
  # ===========================================================================

  Scenario: the station refuses over budget with 503 and retry_after, and never calls upstream
    Given a station with --upstream-rpm 2 receiving 3 jobs within one second (broker admission off)
    Then the upstream received exactly 2 requests
    And the third job result is status 503 with body {"error":{"message":"station at upstream budget","retry_after_sec":N}} and retry_after_sec > 0

  Scenario: the station's bucket is shared across the poll plane and the Tower fabric plane
    Given a station with --upstream-tpm 60000 attached to both the broker poll plane and a Tower fabric
    When each plane delivers a 40000-token job in the same second
    Then the upstream received exactly one request
    And the other job was refused 503 with retry_after_sec

  Scenario: the station counts actual usage, not estimates, against its own bucket
    Given a station with --upstream-tpm 60000
    When a job's upstream usage reports 45000 total tokens
    Then the station's bucket holds about 15000
    And a following 20000-token job is refused until the bucket refills

  Scenario: the station's refusal is a 503 so the client fails over, never a 429
    Given a station refuses a job for budget
    When the consumer's client receives the response through the broker
    Then the status is 503 and retryable() is true (the client tries a sibling station)

  Scenario: an unlimited station forwards exactly as today
    Given a station with neither flag
    When 50 jobs arrive
    Then all 50 reach the upstream with no added latency beyond a bucket check

  Scenario: the station's bucket check adds no measurable latency
    Given a station with --upstream-tpm 1000000
    When 1000 jobs are admitted
    Then the median added latency of the budget check is under 1 millisecond

  # ===========================================================================
  # 4. RETRY-AFTER TRAVELS END TO END
  # ===========================================================================

  Scenario: an upstream 429's Retry-After reaches the consumer
    Given the upstream returns 429 with "Retry-After: 7"
    When a funded consumer relays
    Then the JobResult carries retry_after_sec 7
    And the consumer response is 429 with the upstream body and Retry-After: 7

  Scenario: an upstream 429 without Retry-After gets the default cooldown as the hint
    Given the upstream returns 429 with no Retry-After header
    When a funded consumer relays
    Then the consumer response is 429 with Retry-After equal to the default cooldown (15)

  Scenario Outline: a non-numeric or HTTP-date Retry-After is normalized to seconds, capped
    Given the upstream returns 429 with "Retry-After: <value>"
    When a funded consumer relays
    Then the consumer response has Retry-After <seconds>

    Examples:
      | value                            | seconds |
      | 30                               | 30      |
      | 0                                | 15      |
      | 99999                            | 120     |
      | an HTTP-date 45 seconds from now | 45      |
      | garbage                          | 15      |

  Scenario: an upstream 503 also carries Retry-After when present
    Given the upstream returns 503 with "Retry-After: 3"
    When a funded consumer relays
    Then the consumer response is 503 with Retry-After: 3

  Scenario: a 200 never carries a Retry-After
    Given the upstream returns 200 with a completion
    When a funded consumer relays
    Then the consumer response has no Retry-After header

  Scenario: JobResult without the field decodes as before (old stations)
    Given a JobResult JSON with no retry_after_sec
    When the broker decodes it
    Then retry_after_sec is 0 and nothing else changes

  Scenario: the proxy allowlist already forwards Retry-After to the agent (regression pin)
    Given the broker returns 429 with Retry-After: 7
    When the local proxy relays it to the agent
    Then the agent sees Retry-After: 7

  # ===========================================================================
  # 5. COOLDOWN - an upstream 429 that still happens takes the station out of the pick for a beat
  # ===========================================================================

  Scenario: an upstream 429 puts the station in cooldown for its Retry-After
    Given stations "s1" and "s2" serve "m"
    And "s1"'s upstream returns 429 with "Retry-After: 10"
    When a funded consumer relays and lands on "s1"
    Then "s1" is cooling for 10 seconds
    And the next relay for "m" is dispatched to "s2"
    And after 10 seconds "s1" is eligible again

  Scenario: cooldown is shared across instances
    Given two broker instances and station "s1" cooling on instance A
    When instance B picks for "m"
    Then it skips "s1" (the cooldown lives in the shared store, TTL = the cooldown)

  Scenario: a cooling only-station returns 503 with the remaining cooldown, not a dispatch
    Given "s1" is the only station for "m" and is cooling for 8 more seconds
    When a funded consumer relays
    Then the response is 503 band saturated with Retry-After 8
    And the upstream received nothing

  Scenario: cooldown is capped and defaults
    Given "s1"'s upstream returns 429 with "Retry-After: 600"
    When a funded consumer relays
    Then "s1" cools for at most 120 seconds
    Given "s1"'s upstream returns 429 with no Retry-After
    When a funded consumer relays
    Then "s1" cools for 15 seconds

  Scenario: a 5xx does not trigger cooldown (health grading handles it as today)
    Given "s1"'s upstream returns 503 with no Retry-After
    When a funded consumer relays
    Then "s1" is not cooling
    And its success average dropped as today

  Scenario: cooldown never touches trust
    Given "s1" cooled 20 times today
    Then "s1"'s owner has zero strikes and is not held

  Scenario: the market shows a cooling station as on air with cooling_until
    Given "s1" is cooling
    When a client reads /discover
    Then the offer shows online true and "cooling_until" with a future timestamp

  # ===========================================================================
  # 6. PROBES ARE EXEMPT
  # ===========================================================================

  Scenario: a probe canary does not consume the station's budget
    Given station "s1" (tpm 1000) is saturated
    When the prober runs its canary against "s1"
    Then the canary is dispatched (probes bypass admission)
    And the bucket is unchanged

  Scenario: a probe that gets an upstream 429 is a probe failure, not a cooldown and not a strike
    Given "s1"'s upstream returns 429 to the canary
    When the prober runs
    Then the probe records FAIL (consecutive=1) as today
    And "s1" is not cooling and its owner has no strike

  # ===========================================================================
  # 7. OBSERVABILITY
  # ===========================================================================

  Scenario: /admin/live carries the budget counters
    Given 3 saturation skips, 2 saturation 503s, and 4 cooldowns happened
    When the founder reads /admin/live
    Then it shows station_saturated_skips 3, station_saturated_503 2, station_cooldowns 4
    And a per-station table with rpm/tpm remaining and cooling_until

  Scenario: the dial shows a throttled station as on air, marked
    Given "s1" is saturated or cooling
    When the TUI renders the band
    Then the station row is on air with a "throttled" marker and the seconds remaining
    And the band is never shown as dark because of throttling

  Scenario: a sustained saturation pages the founder once
    Given "s1" has been saturated for more than 10 minutes with real demand behind it
    Then the founder alert "station_saturated:s1" fired once naming the band, the budget, and the demand
    And it clears when the station admits again

  # ===========================================================================
  # 8. ADVERSARIAL
  # ===========================================================================

  Scenario: a station cannot win routing preference by declaring a huge budget
    Given "s1" declares tpm 10^9 and "s2" declares none
    When 100 relays are picked
    Then the split follows price/health/signal as today (the budget is a filter, never a score)

  Scenario: a station cannot hide behind a tiny budget to dodge probes
    Given "s1" declares rpm 1
    When the prober's weekly recheck runs
    Then the canary is dispatched regardless

  Scenario: a consumer cannot exhaust a sibling's budget
    Given "s1" (tpm 50000) and "s2" (tpm 50000) serve "m"
    When one consumer floods "m"
    Then each station's bucket is debited only for requests dispatched to it

  Scenario: a forged retry_after_sec cannot cool a station for longer than the cap
    Given a station posts a JobResult with retry_after_sec 10^6
    Then the station cools for 120 seconds at most

  Scenario: a forged retry_after_sec on a 200 is ignored
    Given a station posts a 200 JobResult with retry_after_sec 60
    Then the station is not cooling and the consumer sees no Retry-After

  # ===========================================================================
  # 9. REGRESSION - Sep 7 replayed
  # ===========================================================================

  Scenario: the 2026-09-07 burst against a declared budget never reaches the provider's ceiling
    Given the house Cerebras station declares rpm 300 tpm 150000 and is the only station for "qwen-3.8-27b"
    And the upstream returns 429 for any minute that receives more than 150000 tokens
    When one funded consumer relays 30 prompts of 83000 tokens each within 3 minutes
    Then the upstream received at most 150000 tokens in any 60-second window
    And the upstream never returned a 429
    And the refused relays were 503 band saturated with a Retry-After the consumer could act on
    And the operator has zero strikes and is not held
