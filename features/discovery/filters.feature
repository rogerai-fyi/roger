# DISCOVERY FILTERS: a consumer can ask the public market the same questions the relay can
# answer - "which stations serve this model under this quant, above this speed, in this
# region, self-hosted, free right now" - and get back only those. Today /discover and
# /market ACCEPT query params and IGNORE every one of them; the params are keyed into the
# cache and nothing else. This spec makes them filter, without touching the two invariants
# that matter on these endpoints: PRIVATE nodes are never listed, and a public read is never
# throttled into an empty-looking market.
#
# Contract: features/routing/ROUTING-EXPRESSION-CONTRACT.md §8 (first two bullets), §5 for
# the meaning of each filter (the SAME semantics as the relay: declared-unknown ineligible,
# measured-unknown passes), §1a for validation shape.
#
# GROUND TRUTH as of origin/main 518c698b (cmd/rogerai-broker/market.go):
#   - normalizedMarketQuery (market.go:14-26) reads model / confidential / freq into the cache
#     key only; "/discover + /market do not filter today" (market.go:19).
#   - discover() (market.go:249-275): NO per-IP anon gate, deliberately - a 429 body renders as
#     an empty market (the release-day "dial flickers to empty" incident, market.go:256-263;
#     regression discover_ratelimit_test.go TestDiscoverNeverRateLimitsToEmpty +
#     TestPublicReadsShareCacheOnlyThrottlePosture). Cost is amortized by
#     serveCachedJSON("discover:"+key, publicMarketTTL=3s) (sharedstore.go:2174, 2268).
#   - computeDiscover (market.go:282-309): skips banned + private, enrichOffersForNode per node,
#     assignPriceTiers, sort by In asc. market() / computeMarket (market.go:352-569) aggregate
#     per model over on-air, non-banned, non-private nodes.
#   - offerView fields (market.go:29-107); marketView fields (market.go:312-346). No params.
#
# After this spec: the cache key is the UNFILTERED feed (one shared entry per view); filters
# are applied to the cached payload per request, so a filtered read costs a filter pass over
# a small slice and never a recompute. /market aggregates are recomputed over the FILTERED
# providers so min_price / best_tps / providers reflect what the consumer asked about.
#
# Enforced by: cmd/rogerai-broker/discovery_filters_bdd_test.go (godog, strict) - after
# approval; RED table tests next to discover_ratelimit_test.go.

Feature: Discovery filters - /discover and /market answer the question that was asked

  Background:
    Given a broker with an empty in-memory node registry
    And nodes are on air for "qwen3-32b":
      | node    | quant  | params_b | ctx    | ctx_estimated | tps | ttft_ms | price_in | price_out | region | curated | verified | confidential | free_now |
      | n-eu-q8 | Q8_0   | 32.8     | 131072 | false         | 40  | 400     | 0.10     | 0.30      | eu     | false   | true     | false        | false    |
      | n-us-q4 | Q4_K_M | 32.8     | 32768  | false         | 25  | 900     | 0.05     | 0.15      | us     | false   | false    | false        | false    |
      | n-free  |        |          | 32768  | true          | 0   | 0       | 0        | 0         |        | false   | false    | false        | true     |
      | n-cur   | BF16   | 32.8     | 131072 | false         | 120 | 200     | 0.20     | 0.60      | us     | true    | true     | false        | false    |
      | n-tee   | Q8_0   | 32.8     | 65536  | false         | 30  | 500     | 0.15     | 0.45      | eu     | false   | true     | true         | false    |
    And node "n-llama" is on air for "llama-3.3-70b" with quant "Q4_K_M", params_b 70, tps 12, region "us"

  # --- no filter: unchanged ----------------------------------------------------------------
  Scenario: With no query /discover lists every public on-air offer, input price ascending
    When a consumer GETs /discover
    Then 6 offers are listed
    And they are ordered by price_in ascending

  Scenario: With no query /market lists every model with its full aggregate
    When a consumer GETs /market
    Then "qwen3-32b" shows providers 4, curated_providers 1, min_price 0, min_out 0, best_tps 120

  # --- model ----------------------------------------------------------------------------------
  Scenario: model narrows /discover to that model's offers
    When a consumer GETs /discover?model=qwen3-32b
    Then 5 offers are listed and every one is for "qwen3-32b"

  Scenario: model narrows /market to one row
    When a consumer GETs /market?model=llama-3.3-70b
    Then exactly one row is listed, for "llama-3.3-70b"

  Scenario: model is exact and case-sensitive, like the relay
    When a consumer GETs /discover?model=QWEN3-32B
    Then 0 offers are listed
    And the status is 200

  Scenario: model matches no on-air offer - 200 with an empty list, never 404 or 503
    When a consumer GETs /discover?model=nobody-serves-this
    Then the status is 200
    And the body is {"offers":[]}
    When a consumer GETs /market?model=nobody-serves-this
    Then the status is 200
    And the body is {"market":[]}

  # --- price ----------------------------------------------------------------------------------
  Scenario Outline: max_price_out and max_price_in are inclusive ceilings on the ACTIVE price
    When a consumer GETs /discover?model=qwen3-32b&<param>=<value>
    Then the listed node ids are exactly <nodes>

    Examples:
      | param         | value | nodes                             |
      | max_price_out | 0.30  | n-eu-q8,n-us-q4,n-free            |
      | max_price_out | 0.29  | n-us-q4,n-free                    |
      | max_price_out | 0     | n-free                            |
      | max_price_in  | 0.10  | n-eu-q8,n-us-q4,n-free            |
      | max_price_in  | 0.05  | n-us-q4,n-free                    |
      | max_price_out | 100   | n-eu-q8,n-us-q4,n-free,n-cur,n-tee |

  Scenario: A time-of-use window changes what a price filter returns
    Given "n-us-q4" has a schedule window that is FREE right now
    When a consumer GETs /discover?model=qwen3-32b&max_price_out=0
    Then the listed node ids are exactly "n-free,n-us-q4"

  # --- speed --------------------------------------------------------------------------------
  Scenario Outline: min_tps excludes measured nodes below the floor and keeps unmeasured ones
    When a consumer GETs /discover?model=qwen3-32b&min_tps=<min>
    Then the listed node ids are exactly <nodes>

    Examples:
      | min | nodes                             |
      | 30  | n-eu-q8,n-free,n-cur,n-tee        |
      | 40  | n-eu-q8,n-free,n-cur              |
      | 41  | n-free,n-cur                      |
      | 0   | n-eu-q8,n-us-q4,n-free,n-cur,n-tee |

  Scenario Outline: max_ttft_ms excludes measured nodes above the ceiling and keeps unmeasured ones
    When a consumer GETs /discover?model=qwen3-32b&max_ttft_ms=<max>
    Then the listed node ids are exactly <nodes>

    Examples:
      | max | nodes                       |
      | 500 | n-eu-q8,n-free,n-cur,n-tee  |
      | 400 | n-eu-q8,n-free,n-cur        |
      | 100 | n-free                      |

  # --- size and window ----------------------------------------------------------------------
  Scenario Outline: params_min and params_max are inclusive and an offer without params_b is dropped under either
    # n-free omitted params_b but the known-model table filled 32 (estimated) from its id, so
    # it counts; an id the table cannot read (see "no params_b" below) is dropped.
    When a consumer GETs /discover?<query>
    Then the listed node ids are exactly <nodes>

    Examples:
      | query                              | nodes                             |
      | params_min=30                      | n-eu-q8,n-us-q4,n-free,n-cur,n-tee,n-llama |
      | params_min=33                      | n-llama                           |
      | params_max=40                      | n-eu-q8,n-us-q4,n-free,n-cur,n-tee |
      | params_min=32.8&params_max=32.8    | n-eu-q8,n-us-q4,n-cur,n-tee       |
      | params_min=70&params_max=70        | n-llama                           |
      | params_min=71                      |                                   |

  Scenario: params_min above params_max is a 400
    When a consumer GETs /discover?params_min=70&params_max=7
    Then the status is 400
    And the error code is "invalid_query_param"

  Scenario Outline: min_ctx keeps DECLARED windows at or above the floor and drops estimated ones
    When a consumer GETs /discover?model=qwen3-32b&min_ctx=<min>
    Then the listed node ids are exactly <nodes>

    Examples:
      | min    | nodes                        |
      | 32768  | n-eu-q8,n-us-q4,n-cur,n-tee  |
      | 32769  | n-eu-q8,n-cur,n-tee          |
      | 65536  | n-eu-q8,n-cur,n-tee          |
      | 65537  | n-eu-q8,n-cur                |
      | 131073 |                              |

  # --- quant, region, capability ----------------------------------------------------------
  Scenario Outline: quant matches the canonical label case-insensitively and drops unlabeled offers
    When a consumer GETs /discover?model=qwen3-32b&quant=<quant>
    Then the listed node ids are exactly <nodes>

    Examples:
      | quant  | nodes           |
      | Q8_0   | n-eu-q8,n-tee   |
      | q8_0   | n-eu-q8,n-tee   |
      | BF16   | n-cur           |
      | Q4     |                 |
      | NVFP4  |                 |

  Scenario: quant may be repeated to ask for any of several
    When a consumer GETs /discover?model=qwen3-32b&quant=Q8_0&quant=BF16
    Then the listed node ids are exactly "n-eu-q8,n-tee,n-cur"

  Scenario Outline: region matches the self-declared token and drops nodes with none
    When a consumer GETs /discover?model=qwen3-32b&region=<region>
    Then the listed node ids are exactly <nodes>

    Examples:
      | region | nodes          |
      | eu     | n-eu-q8,n-tee  |
      | us     | n-us-q4,n-cur  |
      | mars   |                |

  Scenario: region is exact lowercase on discovery as on the relay - a cased token is a 400, never folded
    # Contract §1a / §8: enumerated values are exact lowercase on both surfaces; the relay
    # answers 400 invalid_routing_value for roger.region ["EU"], discovery answers
    # 400 invalid_query_param for region=EU. Only quant labels compare case-insensitively.
    When a consumer GETs /discover?model=qwen3-32b&region=EU
    Then the status is 400
    And the error code is "invalid_query_param"
    And the error message names "region"

  Scenario: region may be repeated
    When a consumer GETs /discover?model=qwen3-32b&region=eu&region=us
    Then the listed node ids are exactly "n-eu-q8,n-us-q4,n-cur,n-tee"

  Scenario: capability keeps offers the broker RECORDS with the capability, using the same verified/declared rule as routing
    Given ("n-eu-q8", "qwen3-32b") earned verified "tools"
    And "n-us-q4" declared "vision" for "qwen3-32b"
    When a consumer GETs /discover?model=qwen3-32b&capability=tools
    Then the listed node ids are exactly "n-eu-q8"
    When a consumer GETs /discover?model=qwen3-32b&capability=vision
    Then the listed node ids are exactly "n-us-q4"

  Scenario: capability repeated is an AND (the offer must have every one)
    Given ("n-eu-q8", "qwen3-32b") earned verified "tools" and declared "vision"
    And "n-us-q4" declared "vision" for "qwen3-32b"
    When a consumer GETs /discover?model=qwen3-32b&capability=tools&capability=vision
    Then the listed node ids are exactly "n-eu-q8"

  Scenario: An unknown capability value is a 400, matching the relay's closed set
    When a consumer GETs /discover?capability=reasoning
    Then the status is 400
    And the error code is "invalid_query_param"

  Scenario: A node that declared tools on the wire does not pass capability=tools
    Given "n-us-q4" declared "tools" for "qwen3-32b" and never passed a tool-call canary
    When a consumer GETs /discover?model=qwen3-32b&capability=tools
    Then "n-us-q4" is not listed

  # --- flags -----------------------------------------------------------------------------------
  Scenario Outline: The boolean flags accept 1 / true and reject anything else
    When a consumer GETs /discover?model=qwen3-32b&<flag>=<value>
    Then the status is <status>

    Examples:
      | flag         | value | status |
      | free         | 1     | 200    |
      | free         | true  | 200    |
      | free         | 0     | 400    |
      | free         | yes   | 400    |
      | self_hosted  | 1     | 200    |
      | confidential | 1     | 200    |
      | confidential | maybe | 400    |

  Scenario: free=1 keeps offers that are free RIGHT NOW (base price or a live free window)
    Given "n-us-q4" has a schedule window that is FREE right now
    When a consumer GETs /discover?model=qwen3-32b&free=1
    Then the listed node ids are exactly "n-free,n-us-q4"

  Scenario: self_hosted=1 drops curated stations
    When a consumer GETs /discover?model=qwen3-32b&self_hosted=1
    Then the listed node ids are exactly "n-eu-q8,n-us-q4,n-free,n-tee"

  Scenario: confidential=1 keeps only TEE-attested nodes
    When a consumer GETs /discover?model=qwen3-32b&confidential=1
    Then the listed node ids are exactly "n-tee"

  Scenario Outline: trust_min mirrors the relay's meaning
    When a consumer GETs /discover?model=qwen3-32b&trust_min=<value>
    Then the listed node ids are exactly <nodes>

    Examples:
      | value        | nodes                             |
      | any          | n-eu-q8,n-us-q4,n-free,n-cur,n-tee |
      | verified     | n-eu-q8,n-cur,n-tee               |
      | confidential | n-tee                             |

  Scenario: trust_min outside the closed set is a 400
    When a consumer GETs /discover?trust_min=high
    Then the status is 400
    And the error code is "invalid_query_param"

  # --- combination -----------------------------------------------------------------------------
  Scenario: Filters are an AND
    When a consumer GETs /discover?model=qwen3-32b&quant=Q8_0&region=eu&min_tps=35
    Then the listed node ids are exactly "n-eu-q8"

  Scenario: A combination that empties the list is 200 with an empty list
    When a consumer GETs /discover?model=qwen3-32b&quant=Q8_0&region=us
    Then the status is 200
    And the body is {"offers":[]}

  Scenario: Ordering is unchanged under filters - input price ascending
    When a consumer GETs /discover?model=qwen3-32b&region=us
    Then the listed node ids are in order "n-us-q4,n-cur"

  Scenario: Every filter param is documented and any other param is a 400
    When a consumer GETs /discover?foo=1
    Then the status is 400
    And the error code is "unknown_query_param"
    And the error message names "foo"

  Scenario Outline: Malformed numeric values are a 400 naming the param
    When a consumer GETs /discover?<param>=<value>
    Then the status is 400
    And the error code is "invalid_query_param"
    And the error message names "<param>"

    Examples:
      | param         | value |
      | min_tps       | fast  |
      | min_tps       | -1    |
      | max_ttft_ms   | 0     |
      | max_price_out | -0.1  |
      | max_price_in  | NaN   |
      | params_min    | 0     |
      | params_max    | 1e400 |
      | min_ctx       | 32k   |
      | min_ctx       | 0     |

  Scenario: A repeated single-valued param takes the last value, like Go's URL query
    When a consumer GETs /discover?model=llama-3.3-70b&model=qwen3-32b
    Then every listed offer is for "qwen3-32b"

  Scenario: An empty value for a param is a 400 (not "no filter")
    When a consumer GETs /discover?quant=
    Then the status is 400
    And the error code is "invalid_query_param"

  Scenario: The 400 body is the same error envelope the relay uses, and CORS headers are still present
    When a consumer GETs /discover?foo=1 from the website origin
    Then the body is {"error":{"code":"unknown_query_param","message":...}}
    And the response carries the same CORS headers an unfiltered /discover carries

  # --- /market aggregates over the filtered providers -------------------------------------------
  Scenario: /market aggregates are recomputed over the filtered providers
    When a consumer GETs /market?model=qwen3-32b&self_hosted=1
    Then "qwen3-32b" shows providers 4, curated_providers 0, best_tps 40, min_out 0

  Scenario: /market under a price filter reports the min price among the survivors
    When a consumer GETs /market?model=qwen3-32b&min_tps=30
    Then "qwen3-32b" shows providers 3, curated_providers 1, min_out 0, best_tps 120
    # n-free is unmeasured (tps 0) and therefore kept; it is the min_out 0.

  Scenario: /market under a region filter counts only that region's providers
    When a consumer GETs /market?region=eu
    Then "qwen3-32b" shows providers 2 and curated_providers 0
    And "llama-3.3-70b" is not listed

  Scenario: /market capabilities under a capability filter still union over the survivors only
    Given ("n-eu-q8", "qwen3-32b") earned verified "tools"
    And "n-us-q4" declared "vision" for "qwen3-32b"
    When a consumer GETs /market?model=qwen3-32b&capability=tools
    Then "qwen3-32b" lists capabilities ["tools"]

  Scenario: /market signal and price tier are recomputed over the survivors, not copied from the unfiltered row
    When a consumer GETs /market?model=qwen3-32b&self_hosted=1
    Then the "qwen3-32b" signal differs from the unfiltered row's signal
    And its price_tier is graded over the surviving out-prices

  Scenario: A model whose every provider is filtered out drops off /market
    When a consumer GETs /market?quant=NVFP4
    Then the body is {"market":[]}

  # --- caching and throttle posture unchanged ----------------------------------------------------
  Scenario: The cache key is the unfiltered feed - a filtered read is served from the same cached bytes
    Given /discover was computed and cached within publicMarketTTL
    When a consumer GETs /discover?model=qwen3-32b&region=eu
    Then computeDiscover is not re-run
    And the filter is applied to the cached payload

  Scenario: Distinct filter combinations do not fragment the cache
    When 100 consumers GET /discover with 100 different filter combinations within publicMarketTTL
    Then computeDiscover ran at most once
    And the cache holds exactly one "discover:" entry

  Scenario: A filtered /discover is never rate-limited to an empty-looking body
    When one IP GETs /discover?model=qwen3-32b 200 times in a second
    Then every response is 200 with a filtered offers array
    And none is a 429

  Scenario: A filtered /market keeps the same throttle posture as an unfiltered one
    When one IP GETs /market?region=eu 200 times in a second
    Then every response is 200

  Scenario: The filter pass runs after the cache and touches no broker lock
    When a consumer GETs /discover?quant=Q8_0 while a relay holds b.mu
    Then the read returns without waiting on b.mu

  Scenario: The demand-probe side effect still fires on a cache miss under a filter
    Given the cache is cold and probing is enabled
    When a consumer GETs /discover?model=qwen3-32b
    Then the stale nodes for every public offer are scheduled for a demand probe, filtered or not

  # --- private bands stay invisible ---------------------------------------------------------
  Scenario: A filter never reveals a private node
    Given node "n-secret" shares "qwen3-32b" on a PRIVATE band with quant "RARE_Q"
    When a consumer GETs /discover?quant=RARE_Q
    Then the body is {"offers":[]}
    When a consumer GETs /market?quant=RARE_Q
    Then the body is {"market":[]}

  Scenario: freq is not a /discover filter - private offers are reached only through /bands/resolve
    When a consumer GETs /discover?freq=some-code
    Then the status is 400
    And the error code is "unknown_query_param"

  Scenario: A banned node stays absent under every filter
    Given node "n-us-q4" is banned
    When a consumer GETs /discover?model=qwen3-32b&region=us
    Then the listed node ids are exactly "n-cur"

  Scenario: An offline node still reads OFFLINE on /discover under a filter it matches, and drops out of /market as today
    Given "n-us-q4" has not heartbeat within nodeTTL
    When a consumer GETs /discover?model=qwen3-32b&region=us
    Then "n-us-q4" is listed with online false
    When a consumer GETs /market?model=qwen3-32b&region=us
    Then "qwen3-32b" shows providers 0 and curated_providers 1

  # --- new fields ---------------------------------------------------------------------------------
  Scenario: /discover carries params_b and params_estimated per offer
    When a consumer GETs /discover?model=qwen3-32b
    Then the "n-eu-q8" offer carries params_b 32.8 and params_estimated false
    And the "n-free" offer carries params_b 32 and params_estimated true
    # n-free omitted params_b; the known-model table read "32b" out of the id.

  Scenario: An offer with no params_b and no table match omits both keys
    Given node "n-odd" is on air for "my-custom-finetune" with no params_b
    When a consumer GETs /discover?model=my-custom-finetune
    Then the "n-odd" offer carries no params_b key and no params_estimated key

  Scenario: The private-band offer view carries the same new fields
    Given node "n-secret" shares "qwen3-32b" on a PRIVATE band with params_b 32.8
    When the band owner resolves the code
    Then the band's offer view carries params_b 32.8 and params_estimated false

  # --- documentation ---------------------------------------------------------------------------
  @docs
  Scenario: OpenAPI documents every /discover and /market filter param with its type and rule
    When the OpenAPI document is read
    Then /discover documents model, min_tps, max_ttft_ms, max_price_in, max_price_out, params_min, params_max, min_ctx, region, quant, capability, self_hosted, confidential, free, trust_min
    And /market documents the same set
    And each declared-attribute filter states that offers without the attribute are dropped
    And the 400 responses document error codes "unknown_query_param" and "invalid_query_param"
