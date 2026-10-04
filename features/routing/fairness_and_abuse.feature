# FAIRNESS AND ABUSE (slice 6, hardening after the 2026-10-02 product / security / fairness
# audit). PURPOSE: nobody on the network can hurt an honest party cheaply. A consumer cannot
# get an operator struck or frozen for free, cannot put a whole station into cooldown for
# everyone with one request, cannot farm free supply by minting keypairs; a station cannot
# capture a sorted pool with a fractional undercut or a lucky node id; one Tower does not get
# the same share as twenty direct stations; home GPUs are not crowded out by commercial
# pass-through by default. Contract text: features/routing/ROUTING-HARDENING-CONTRACT-DRAFT-A.md
# (to be merged as ROUTING-EXPRESSION-CONTRACT.md §14). Founder rulings 2026-10-02 (#4, #7,
# #8, #15, #16) and the audit's BUG #1.
#
# GROUND TRUTH (wt/routing-slice6 at b1754466):
#   #1  cmd/rogerai-broker/recount.go:495 voidReasonFor maps every status >= 400 except 429 to
#       VoidUpstreamError; tunnel.go settleVoid (~3934) then calls maybeFlagEmptyOutput
#       (strikes.go:454), which strikes for ANY status but 429 and a plausible overflow, so an
#       upstream 400/401/404/413/422 strikes the operator. strikes.go:197-265 holds payouts at
#       the warn threshold (defaultStrikeWarnAt=3). cooling.go:90 failoverable already
#       excludes 4xx from failover (a 4xx is answered, not retried: KEPT). Unknown top-level
#       OpenAI keys pass to the station untouched (contract §1a), so a consumer controls them.
#   #4  cooling.go:300 coolStation cools the WHOLE node for every payer and every model, on
#       every instance (shared markCooling), up to ROGERAI_STATION_COOLDOWN_MAX (120 s), from
#       ONE 429 on ONE request; the consumer pays $0 (the attempt is voided).
#   #7  tunnel.go:1755-1774: the per-IP anon limiter applies only when user=="anon"; any
#       signed caller gets its own bucket keyed on its pubkey-derived id (rlKey = user), so a
#       fresh keypair per request is a fresh bucket. ratelimit.go: ROGERAI_RATE_RPM 120 /
#       BURST 40; ROGERAI_ANON_RATE_RPM 30 / BURST 15.
#   #8  edgeplan.go:106 edgeMetricLess + tunnel.go pickFor strict sort: top-1 by metric, ties
#       by score then node id order (sorted candidates); load only breaks exact ties.
#   #15 edgeplan.go:216-229 mergeEdgePlan: when both heads are Tier-A the coin (seededRand,
#       50/50) decides which fabric goes first, independent of how many stations each side has.
#   #16 tunnel.go pickFor scores curated and human stations with the same terms
#       (features/curated/curated_routing.feature:26 "no preference for either kind"); a fast
#       curated upstream saturates speedFit (router.go tpsTarget) and at-cost pricing wins
#       priceMod; the house owner is exempt from the per-owner on-air cap (tunnel.go:44-56).
#
# SUPERSEDES (approved scenarios this set overrides; founder approval of this set approves
# the override):
#   features/safety/upstream_throttle_not_a_strike.feature:93 "only 429 is exempt - every other
#     no-output status still strikes exactly as today": the 400, 401 and 404 rows (and 413/422
#     where present) no longer strike; 5xx and the 200-empty row still do, subject to the
#     distinct-payer floor below.
#   features/routing/upstream_failover.feature:277 "a 429 with Retry-After cools the station for
#     that long", :283 "a 429 without Retry-After cools for the default", :301 "a cooling station
#     is skipped while a sibling exists", :346 "the only station cooling returns 503 band
#     cooling": a single 429 now cools the (station, payer) PAIR; the station-wide cooldown
#     those scenarios describe applies once the distinct-payer threshold is met. Their
#     Retry-After, extend-never-stack, cap and shared-store rules carry over unchanged to both
#     scopes.
#   features/routing/model_fallback_list.feature:875 "a 429 on the first model cools that
#     station for its Retry-After": same narrowing (pair first).
#   features/routing/node_preference.feature:766 "sort price takes the cheapest out-price every
#     time", :776 "sort price breaks a full price tie on score", :820 "sort throughput takes the
#     highest measured tok/s every time", :836 "sort throughput breaks a tps tie on score",
#     :859 "sort latency takes the lowest measured TTFT every time": sort now picks within a
#     band of the best (see the money hardening set for the price metric itself).
#   features/routing/edge_bridge_parity.feature:267 "pref does not disable the coin" and
#     features/tower/edge_fanout.feature:33 "A model served by both fabrics places on either,
#     neither silently preferred": the coin is kept but weighted by eligible capacity.
#   features/curated/curated_routing.feature:26 "Routing judges curated by the same terms as
#     any station": by default home stations go first and curated is overflow.
#
# DESIGN CHOICES MADE HERE (the rulings did not settle them; each is a knob):
#   - "distinct payers" means distinct payer wallets (the billing identity; an anonymous or
#     unbound caller counts by its per-IP bucket key), so one consumer with many keypairs on
#     one IP is one payer for these thresholds.
#   - The empty-output distinct-payer floor applies to the WARN/hold step; the strike row is
#     still recorded (evidence is append-only) but carries the payer and does not count toward
#     warn until the floor is met within the decay window.
#   - The per-request TPM guard needs a declared TPM, a NEW optional offer field `tpm` that
#     only curated registrations may carry (a human station's capacity is its slots).
#   - Pair cooldowns are keyed (station, payer, model); the station-wide cooldown keeps today's
#     per-node scope (all models) because a provider's rate limit is per account.
#
# Enforced by: cmd/rogerai-broker/routing_fairness_bdd_test.go (TestRoutingFairnessBDD).

Feature: Nobody can hurt an honest party on the network cheaply

  Background:
    Given a broker with an empty in-memory node registry
    And the fee rate is 30%
    And the strike warn threshold is 3 and the ban threshold is 5
    And the empty-output distinct-payer floor is 3
    And the station cooldown payer threshold is 3 within 60 seconds
    And funded consumers "alice", "bob", "carol" and "dave" each hold $10.00 in distinct wallets

  # --- #1: an upstream 4xx is consumer-caused ------------------------------------------------

  Scenario Outline: An upstream 4xx is voided at $0, never a strike, never a failover trigger
    Given node "n-1" owned by "op1" is on air for "qwen3-32b"
    And node "n-2" is on air for "qwen3-32b"
    And "n-1" answers the next request with <status> <body>
    When "alice" relays to "qwen3-32b" with provider.order ["n-1"]
    Then the response status is <status>
    And "n-2" received nothing
    And the attempt's receipt is voided with void_reason "consumer-rejected" and upstream_status <status>
    And "alice" was charged 0.000000
    And no owner_strikes row exists for "op1"
    And "op1"'s payouts are not held

    Examples:
      | status | body                                         |
      | 400    | {"error":"unknown parameter: n"}             |
      | 401    | {"error":"upstream key rejected"}            |
      | 404    | {"error":"model not found"}                  |
      | 413    | {"error":"request too large"}                |
      | 422    | {"error":"response_format not supported"}    |

  Scenario: The recognized context-window 400 keeps its own rule (model fallback, overflow guard)
    Given node "n-1" is on air for "a" and node "n-2" for "b"
    And "n-1" answers with 400 "maximum context length is 8192 tokens"
    When "alice" relays with "model": "a" and "models": ["b"]
    Then the response is 200 served by "n-2"
    And the "n-1" attempt's void_reason is "context-window", not "consumer-rejected"

  Scenario: A 429 keeps its own rule (throttle: voided, not a strike, cools the pair)
    Given node "n-1" owned by "op1" is on air for "qwen3-32b"
    And "n-1" answers with 429 and Retry-After 7
    When "alice" relays to "qwen3-32b"
    Then the attempt's void_reason is "upstream-throttled"
    And no owner_strikes row exists for "op1"

  Scenario: Harassment pattern - pinning one station and sending a parameter it rejects never strikes it
    Given node "n-1" owned by "op1" is on air for "qwen3-32b"
    And "n-1" rejects the top-level key "n" with 400
    When "alice" relays 50 times to "qwen3-32b" with provider.order ["n-1"], provider.allow_fallbacks false and top-level "n": 50
    Then every response is 400
    And "alice" was charged 0.000000
    And no owner_strikes row exists for "op1"
    And "op1"'s payouts are not held
    And "op1"'s success rate used by routing is unchanged

  Scenario Outline: Ordinary SDK drift never strikes an honest operator
    Given node "n-1" owned by "op1" runs a server that rejects <key> with 400
    When 10 different consumers relay to "qwen3-32b" with <key> set
    Then no owner_strikes row exists for "op1"

    Examples:
      | key                          |
      | "parallel_tool_calls": false |
      | "response_format": {"type":"json_schema","json_schema":{"name":"x","schema":{}}} |
      | "logprobs": true             |
      | "service_tier": "flex"       |

  Scenario: A consumer-rejected 4xx is not a failover trigger even with a sibling available
    Given nodes "n-1" and "n-2" are on air for "qwen3-32b"
    And "n-1" answers with 422 "response_format not supported"
    When "alice" relays to "qwen3-32b"
    Then the response status is 422
    And exactly one attempt was dispatched

  Scenario: A consumer-rejected 4xx with a model list still does not move to the next model
    Given node "n-1" is on air for "a" and node "n-2" for "b"
    And "n-1" answers with 400 "unknown parameter: n"
    When "alice" relays with "model": "a" and "models": ["b"]
    Then the response status is 400
    And "n-2" received nothing

  Scenario: The station's 4xx body reaches the consumer wrapped, with the station named
    Given node "n-1" is on air for "qwen3-32b"
    And "n-1" answers with 400 {"error":"unknown parameter: n"}
    When "alice" relays to "qwen3-32b"
    Then the response body has error.code "consumer_rejected"
    And error.metadata.station is "n-1"
    And error.metadata.raw carries the station's body

  Scenario Outline: Empty output and 5xx still strike, but only once enough distinct payers saw it
    Given node "n-1" owned by "op1" is on air for "qwen3-32b"
    And "n-1" answers every request with <status> <body>
    When <payers> distinct consumers each relay once to "qwen3-32b" within the decay window
    Then <rows> owner_strikes rows of kind "empty-output" exist for "op1"
    And "op1"'s payouts are <held>

    Examples:
      | status | body                     | payers | rows | held     |
      | 500    | {"error":"internal"}     | 1      | 1    | not held |
      | 500    | {"error":"internal"}     | 2      | 2    | not held |
      | 500    | {"error":"internal"}     | 3      | 3    | held     |
      | 502    | {"error":"unreachable"}  | 3      | 3    | held     |
      | 200    | {"choices":[]}           | 3      | 3    | held     |

  Scenario: One consumer repeating a failing request counts as one payer toward the warn step
    Given node "n-1" owned by "op1" is on air for "qwen3-32b"
    And "n-1" answers every request with 500
    When "alice" relays 20 times to "qwen3-32b" with provider.order ["n-1"] and allow_fallbacks false
    Then "op1"'s payouts are not held
    And the strike rows for "op1" all name the payer "alice"

  Scenario: Many keypairs on one IP count as one payer toward the warn step
    Given node "n-1" owned by "op1" is on air for "qwen3-32b" and free
    And "n-1" answers every request with 500
    When 10 unbound keypairs from one client IP each relay once to "qwen3-32b"
    Then "op1"'s payouts are not held

  Scenario: The distinct-payer floor is a knob, and 1 restores today's behavior
    Given the empty-output distinct-payer floor is 1
    And node "n-1" owned by "op1" is on air for "qwen3-32b"
    And "n-1" answers every request with 500
    When "alice" relays 3 times to "qwen3-32b" with provider.order ["n-1"] and allow_fallbacks false
    Then "op1"'s payouts are held

  Scenario: The zero-doubt impossible-input proof is unaffected by the payer floor
    Given node "n-1" owned by "op1" claims more prompt tokens than the request has bytes
    When "alice" relays once to "qwen3-32b"
    Then "op1" is banned as today

  Scenario: A consumer-rejected attempt is in the station's chain as a $0 lineage receipt
    Given node "n-1" is on air for "qwen3-32b"
    And "n-1" answers with 404 "model not found"
    When "alice" relays to "qwen3-32b"
    Then "n-1"'s chain has a $0 receipt for the attempt with void_reason "consumer-rejected"
    And GET /generation for the request lists the attempt with error_code "consumer-rejected"

  # --- #4: a 429 cools the (station, payer) pair first ------------------------------------

  Scenario: One payer's 429 cools the station for that payer only
    Given nodes "n-1" and "n-2" are on air for "qwen3-32b"
    And "n-1" answers "alice"'s next request with 429 and Retry-After 30
    When "alice" relays to "qwen3-32b"
    Then the response is 200 served by "n-2"
    When "bob" relays to "qwen3-32b" 20 times
    Then "n-1" is a candidate for "bob"
    And "n-1" is NOT a candidate for "alice" for the next 30 seconds

  Scenario: The station cools for everyone once enough distinct payers hit a 429 in the window
    Given nodes "n-1" and "n-2" are on air for "qwen3-32b"
    And "n-1" answers every request with 429 and Retry-After 20
    When "alice", "bob" and "carol" each relay once to "qwen3-32b" within 60 seconds
    Then "n-1" is cooling for every payer for 20 seconds
    And "dave" is served by "n-2"

  Scenario: Two distinct payers are below the threshold and the station stays on for others
    Given nodes "n-1" and "n-2" are on air for "qwen3-32b"
    And "n-1" answers every request with 429
    When "alice" and "bob" each relay once to "qwen3-32b"
    Then "n-1" is a candidate for "carol"

  Scenario: Distinct payers outside the window do not add up
    Given node "n-1" answers every request with 429
    When "alice" relays at t=0, "bob" at t=61s and "carol" at t=122s
    Then "n-1" never cooled for every payer

  Scenario: One payer repeating 429s extends only its own pair, never the station
    Given nodes "n-1" and "n-2" are on air for "qwen3-32b"
    And "n-1" answers every request with 429 and Retry-After 10
    When "alice" relays 30 times to "qwen3-32b" with provider.order ["n-1"]
    Then "n-1" is a candidate for "bob"
    And "alice"'s pair cooldown on "n-1" never exceeds the cooldown cap

  Scenario: The pair cooldown answers the pinned payer with 503 band_cooling and its own Retry-After
    Given node "n-1" is the only station for "qwen3-32b"
    And "alice"'s pair on "n-1" is cooling for 12 more seconds
    When "alice" relays to "qwen3-32b"
    Then the response is 503 with error code "band_cooling" and Retry-After 12
    And no attempt was dispatched
    When "bob" relays to "qwen3-32b"
    Then the response is 200 served by "n-1"

  Scenario: Pair cooldowns are shared across instances
    Given two broker instances share one store
    And "n-1" answered "alice" with 429 and Retry-After 30 on instance A
    When "alice" relays to "qwen3-32b" on instance B
    Then "n-1" is NOT a candidate for "alice" on instance B

  Scenario: The pair cooldown keeps the cap and the extend-never-stack rule
    Given "n-1" answers "alice" with 429 and Retry-After 9999
    Then "alice"'s pair cooldown on "n-1" is the cooldown cap
    When "n-1" answers "alice" again with Retry-After 5 before it expires
    Then the pair cooldown keeps the later expiry

  Scenario: An anonymous caller's pair is keyed by its per-IP bucket, not its keypair
    Given node "n-1" is free and on air for "qwen3-32b"
    And "n-1" answers every request with 429
    When 5 unbound keypairs from one client IP each relay once
    Then they count as one payer toward the station threshold

  Scenario Outline: The station threshold and window are knobs
    Given the station cooldown payer threshold is <k> within <window> seconds
    And "n-1" answers every request with 429
    When <payers> distinct consumers relay once each within <within> seconds
    Then "n-1" <cooled> for every payer

    Examples:
      | k | window | payers | within | cooled          |
      | 1 | 60     | 1      | 1      | is cooling      |
      | 2 | 60     | 2      | 30     | is cooling      |
      | 3 | 60     | 2      | 30     | is NOT cooling  |
      | 3 | 10     | 3      | 30     | is NOT cooling  |

  # corrected 2026-10-04 (founder-approved): five 120 s cooldowns reach the approved 10-minute cumulative alert threshold
  Scenario: The station-wide cooldown still never touches trust and still pages only as today
    Given "n-1" cooled for every payer five times in the alert window
    Then "n-1"'s trust state is unchanged
    And the founder is paged once, as the approved cooling alert says

  Scenario: A pair cooldown never pages and never counts as "0 providers"
    Given only "alice"'s pair on the only station is cooling
    Then no cooling alert is raised
    And the market still counts the station on air

  # --- #4: the per-request TPM guard on a curated station -----------------------------

  Scenario: A curated station may declare a tokens-per-minute budget
    When a curated station registers "gpt-oss-120b" with tpm 60000
    Then the registration is accepted and /discover shows tpm 60000 on the offer

  Scenario: A human station cannot declare a tpm budget
    When a human station registers "qwen3-32b" with tpm 60000
    Then the registration is rejected naming "tpm (curated stations only)"

  Scenario: A single request larger than the declared share of a curated TPM is refused without dispatch
    Given a curated station "cur-1" with tpm 60000 is the only station for "gpt-oss-120b"
    And the per-request TPM share is 50%
    When "alice" relays a request whose measured prompt is 40000 tokens
    Then the response is 429 with error code "request_exceeds_station_tpm" and a Retry-After
    And "cur-1" received nothing
    And "cur-1" is not cooling for anyone
    And "alice" was charged 0.000000

  Scenario: A request within the share is dispatched normally
    Given a curated station "cur-1" with tpm 60000 is the only station for "gpt-oss-120b"
    When "alice" relays a request whose measured prompt is 20000 tokens
    Then the response is 200 served by "cur-1"

  Scenario: A request too large for one curated station fails over to a sibling that can take it
    Given curated station "cur-1" with tpm 60000 and human station "n-2" are on air for "gpt-oss-120b"
    When "alice" relays a request whose measured prompt is 40000 tokens
    Then the response is 200 served by "n-2"

  Scenario: A curated station with no declared tpm is not guarded
    Given a curated station "cur-1" with no tpm is the only station for "gpt-oss-120b"
    When "alice" relays a request whose measured prompt is 40000 tokens
    Then "cur-1" receives the request

  # --- #7: keypairs cannot mint rate-limit buckets ------------------------------------

  Scenario: Unbound keypairs share the per-IP bucket with anonymous callers
    Given the anonymous per-IP limit is 30 rpm with burst 15
    When 40 requests from one client IP arrive, each signed by a fresh unbound keypair
    Then at most 15 are admitted in the first instant
    And the rest are 429 "rate limit exceeded"

  Scenario: A keypair bound to an account keeps the account's own bucket
    Given "alice"'s keypair is bound to her account
    When "alice" sends 40 requests from one client IP
    Then they draw on "alice"'s account bucket (120 rpm, burst 40), not the per-IP bucket

  Scenario: Two accounts behind one NAT are not throttled together
    Given "alice" and "bob" are bound accounts behind one client IP
    When each sends 30 requests
    Then neither is limited by the other's traffic

  Scenario: Free traffic is limited per client IP
    Given the free-traffic per-IP limit is 20 rpm
    And node "n-free" is free for "qwen3-32b"
    When one client IP sends 30 requests to "qwen3-32b:free" within a minute
    Then at most 20 are dispatched and the rest are 429 with error code "free_rate_limited"

  Scenario: Free traffic is limited per (client IP, station)
    Given the free-traffic per (IP, station) limit is 10 rpm
    And nodes "n-free-1" and "n-free-2" are free for "qwen3-32b"
    When one client IP sends 15 requests pinned to "n-free-1" within a minute
    Then at most 10 reach "n-free-1"

  Scenario: Pinning one free station is capped per caller per minute
    Given the free pin cap is 10 per caller per minute
    And node "n-free" is free for "qwen3-32b"
    When "alice" sends 15 requests with provider.order ["n-free"] within a minute
    Then at most 10 reach "n-free"
    And the rest are 429 with error code "free_pin_limited"

  Scenario: Paid traffic is not counted against the free-traffic limits
    Given the free-traffic per-IP limit is 20 rpm
    When "alice" sends 30 paid requests from one client IP within a minute
    Then none is refused for "free_rate_limited"

  Scenario: Self-use of one's own station is not counted as free traffic for the limits
    Given "alice" owns node "n-mine"
    When "alice" sends 30 requests to her own station within a minute
    Then none is refused for "free_rate_limited" or "free_pin_limited"

  Scenario: A free grant counts against its own grant rpm, not the free per-IP limit
    Given a free grant with rpm 60
    When the grant holder sends 30 requests from one client IP within a minute
    Then none is refused for "free_rate_limited"

  Scenario: The free limits are shared across instances
    Given two broker instances share one store
    When one client IP sends 15 free requests to each instance within a minute
    Then the per-IP free limit is applied to the 30 together

  # --- #8: sort picks within a band of the best ------------------------------------------

  Scenario: Sort price spreads across stations within 5% of the cheapest estimated cost
    Given stations "s1", "s2", "s3" serve "qwen3-32b" at estimated request costs $0.0100, $0.0104 and $0.0200
    And each has equal spare capacity
    When 300 consumers relay with provider.sort "price"
    Then "s1" and "s2" each serve between 35% and 65% of requests
    And "s3" serves none

  Scenario: A fractional undercut no longer captures the whole pool
    Given stations "s1" and "s2" serve "qwen3-32b" at estimated costs $0.0100 and $0.00999
    When 200 consumers relay with provider.sort "price"
    Then both "s1" and "s2" serve requests

  Scenario: Within the band, spare capacity weights the pick
    Given stations "s1" and "s2" are within the price band
    And "s1" has 3 of 4 slots busy and "s2" has 0 of 4 busy
    When 200 consumers relay with provider.sort "price"
    Then "s2" serves more requests than "s1"

  Scenario: A full station inside the band is not picked while a band sibling has room
    Given stations "s1" and "s2" are within the price band
    And "s1" has every slot busy
    When a consumer relays with provider.sort "price"
    Then the response is served by "s2"

  Scenario: Ties are broken by the request seed, never by node id
    Given stations "0000" and "zzzz" serve "qwen3-32b" at identical price, speed and capacity
    When 200 consumers relay with provider.sort "price"
    Then each serves between 35% and 65% of requests
    And the same request id always picks the same station

  Scenario: Sort throughput uses a 10% band on tok/s
    Given stations "s1", "s2", "s3" measure 100, 92 and 60 tok/s for "qwen3-32b"
    When 300 consumers relay with provider.sort "throughput"
    Then "s1" and "s2" share the requests and "s3" serves none

  Scenario: Sort latency uses a 10% band on TTFT
    Given stations "s1", "s2", "s3" measure 200 ms, 215 ms and 400 ms TTFT
    When 300 consumers relay with provider.sort "latency"
    Then "s1" and "s2" share the requests and "s3" serves none

  Scenario: Unmeasured stations stay last under throughput and latency sorts
    Given station "s1" measures 50 tok/s and "s2" is unmeasured
    When a consumer relays with provider.sort "throughput"
    Then the response is served by "s1"

  Scenario: The failover plan follows the sort order beyond the band
    Given stations "s1", "s2", "s3", "s4" at estimated costs $0.0100, $0.0102, $0.0150, $0.0200
    And every station answers the next request with 429
    When a consumer relays with provider.sort "price"
    Then the attempts are the two band stations first, in seeded order, then "s3", then "s4" up to the attempt limit

  Scenario: allow_fallbacks false with a sort is one attempt at a band pick
    Given stations "s1" and "s2" are within the price band
    When a consumer relays with provider.sort "price" and provider.allow_fallbacks false
    Then exactly one attempt was dispatched, to "s1" or "s2"

  Scenario Outline: The band widths are knobs
    Given the sort band is <price>% on price and <speed>% on speed
    And stations "s1" and "s2" differ by <diff>% on <metric>
    When 200 consumers relay with provider.sort "<sort>"
    Then "s2" <serves>

    Examples:
      | price | speed | diff | metric | sort       | serves         |
      | 5     | 10    | 4    | price  | price      | serves some    |
      | 5     | 10    | 6    | price  | price      | serves none    |
      | 0     | 10    | 1    | price  | price      | serves none    |
      | 5     | 10    | 9    | tps    | throughput | serves some    |
      | 5     | 0     | 1    | tps    | throughput | serves none    |

  Scenario: The variant suffixes inherit the band
    Given stations "s1" and "s2" are within the price band
    When 200 consumers relay for "qwen3-32b:floor"
    Then both serve requests

  Scenario: A station cannot capture the band by declaring huge capacity it does not have
    Given station "s1" declares capacity 1000 but its measured in-flight ceiling is 1
    And station "s2" declares capacity 4
    When 200 consumers relay with provider.sort "price" and both are in the band
    Then "s1"'s weight is computed from its measured concurrency, not its declaration

  # --- #15: the Tower coin is weighted by eligible capacity ------------------------------

  Scenario: Twenty direct stations against one Tower no longer split 50/50
    Given 20 direct stations with capacity 1 each and one Tower row with capacity 1 serve "m"
    And every candidate is Tier A
    When 2100 consumers relay for "m"
    Then the Tower serves between 2% and 8% of requests

  Scenario: Equal capacity on each side splits evenly
    Given direct capacity 4 and Tower capacity 4 are eligible for "m"
    When 1000 consumers relay for "m"
    Then each fabric serves between 40% and 60% of requests

  Scenario: Only ELIGIBLE capacity counts on each side
    Given direct stations with capacity 8, of which 6 are excluded by the request's filters
    And a Tower row with capacity 2 is eligible
    When 1000 consumers relay for "m" with those filters
    Then each fabric serves between 40% and 60% of requests

  Scenario: The weighted coin is still decided by the request seed
    Given direct capacity 3 and Tower capacity 1 for "m"
    When the same request id is relayed twice
    Then both go to the same fabric first

  Scenario: Sort and order still replace the coin entirely
    Given direct capacity 1 and Tower capacity 9 for "m"
    And the direct station is cheaper
    When a consumer relays with provider.sort "price"
    Then the direct station is tried first

  Scenario: Free and self-use are still never diverted to a billed Tower
    Given "alice" owns a direct station for "m" and a billed Tower also serves "m"
    When "alice" relays for "m"
    Then the Tower is never tried

  Scenario: A Tower's capacity counts its stations individually
    Given a Tower whose station serves "m" with capacity 3
    And one direct station with capacity 3
    When 1000 consumers relay for "m"
    Then each fabric serves between 40% and 60% of requests

  # --- #16: home stations first, curated as overflow -----------------------------------

  Scenario: By default a home station serves before a curated station of the same model
    Given human station "n-home" and curated station "cur-1" serve "qwen3-32b"
    And "cur-1" is faster and cheaper
    When 100 consumers relay to "qwen3-32b" with no routing object
    Then every request is served by "n-home"

  Scenario: Curated takes the overflow when the home station is busy
    Given human station "n-home" with every slot busy and curated station "cur-1" serve "qwen3-32b"
    When a consumer relays to "qwen3-32b"
    Then the response is served by "cur-1"

  Scenario: Curated is the failover after the home attempts in the plan
    Given human stations "n-h1" and "n-h2" and curated "cur-1" serve "qwen3-32b"
    And "n-h1" and "n-h2" answer the next request with 429
    When a consumer relays to "qwen3-32b"
    Then the attempts are "n-h1", "n-h2", then "cur-1"
    And the response is 200 served by "cur-1"

  Scenario: Curated serves when there is no eligible home station at all
    Given curated station "cur-1" is the only station for "gpt-4.1"
    When a consumer relays to "gpt-4.1"
    Then the response is 200 served by "cur-1"

  Scenario Outline: A consumer can opt in to curated on equal terms
    Given human station "n-home" and a faster curated station "cur-1" serve "qwen3-32b"
    When a consumer relays to "qwen3-32b" with <optin>
    Then "cur-1" competes on equal terms with "n-home"

    Examples:
      | optin                              |
      | roger.pref "fast"                  |
      | provider.sort "throughput"         |
      | provider.sort "price"              |
      | provider.order ["cur-1"]           |
      | provider.only ["cur-1", "n-home"]  |
      | the X-Roger-Node "cur-1" pin       |

  Scenario Outline: The other prefs keep home-first
    Given human station "n-home" and curated station "cur-1" serve "qwen3-32b"
    When a consumer relays to "qwen3-32b" with roger.pref "<pref>"
    Then every request is served by "n-home"

    Examples:
      | pref     |
      | cheap    |
      | balanced |
      | reliable |

  Scenario: Naming the curated station in order puts it first
    Given human station "n-home" and curated station "cur-1" serve "qwen3-32b"
    When a consumer relays with provider.order ["cur-1"]
    Then the response is served by "cur-1"

  Scenario: self_hosted_only still excludes curated entirely (unchanged)
    Given human station "n-home" with every slot busy and curated station "cur-1" serve "qwen3-32b"
    When a consumer relays with roger.self_hosted_only true
    Then the response is not served by "cur-1"

  Scenario: Home-first never refuses a request a curated station could serve
    Given every human station for "qwen3-32b" is cooling and curated station "cur-1" is on air
    When a consumer relays to "qwen3-32b"
    Then the response is 200 served by "cur-1"

  Scenario: Home-first applies to Tower rows by the curated flag of the node behind them
    Given a Tower row whose node is curated and a human direct station serve "m"
    When 100 consumers relay to "m"
    Then the human direct station serves every request

  Scenario: Curated stations ranked among themselves keep their own scoring
    Given curated stations "cur-1" and "cur-2" are the only stations for "gpt-4.1"
    When 200 consumers relay to "gpt-4.1"
    Then the pick between them follows the ordinary score
