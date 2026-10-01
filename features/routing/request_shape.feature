# ROUTING REQUEST SHAPE - how a consumer STATES what it wants, in the request BODY.
#
# PURPOSE: today every routing preference is an X-Roger-* header and the relay decodes exactly
# two body fields (model, stream). OpenAI-compatible clients, SDKs and guest operators
# (opencode / aider / hermes) carry routing configuration in the BODY, in OpenRouter's shape
# (`provider{...}`, `models[...]`). This spec pins the three body carriers the relay accepts
# (`models`, `provider`, `roger`), how they are validated, how they rank against the legacy
# headers, and the guarantee that they are STRIPPED before any station sees the body.
# Every knob here can only NARROW what a grant, a band, a wallet or the operator ceiling already
# admits - the money and admission invariants are restated, never relaxed.
#
# CONTRACT: features/routing/ROUTING-EXPRESSION-CONTRACT.md §1, §1a, §1b, §2 (PROPOSED
# 2026-09-30). The scenarios describe behavior AFTER the change; GROUND TRUTH is the code today.
#
# GROUND TRUTH (origin/main 518c698b):
#   cmd/rogerai-broker/tunnel.go
#     1624      body, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))   (4 MiB body limit)
#     1724-1728 var req struct{ Model, Stream }; json.Unmarshal(body,&req)  (the ONLY body decode)
#     1762-1786 moderation (sync in-line or async off-path) runs BEFORE any routing header is read
#     1788-1833 X-Roger-Confidential / -Freq / -Min-TPS / -Max-Price / -Pref / -Max-Price-Out /
#               -Node / -Exclude-Nodes parsed into locals; 1836-1847 a grant's nodeAllow/model deny
#     1852      pickFor(model, confidentialOnly, minTPS, maxPrice, maxPriceOut, pin, exclude,
#               allow, privateAllow, pickReq{pref, promptTokens, rng})
#     1924-1930 "no node offers <model>" -> 503 (grant / confidential variants of the message)
#     1995-2011 ONE hold per request at plan[0].maxCost, after monthlyCapCheck
#     2923-2941 estimateMaxCost(body, in, out, ctx): max_tokens x out + body/4 x in, floor 1e-6
#   cmd/rogerai-broker/pricesafety.go
#     21-22     operator ceilings $100/1M out, $50/1M in (ROGERAI_MAX_PRICE_OUT / _IN)
#     31-45     consumerDefaultMaxOut $10/1M; effectiveRelayMaxOut(0) == the default
#     52-60     clampSettleCost floors at 0 and caps at the authorized hold
#   cmd/rogerai-broker/cooling.go 118-135 holdCostFor, 150-158 planCeiling, 161-170 trimPlan
#   cmd/rogerai-broker/edgebridge.go 140-168 bridge honors confidential / pin / freq / free-self,
#               NOT exclude (documented), NOT min-tps, NOT pref
#   cmd/rogerai-broker/httputil.go 43-45 jsonErr writes {"error":{"message":...}} (no code yet)
#   cmd/rogerai-broker/router.go 52-63 parsePref: cheap|fast|reliable, anything else = balanced
#   internal/client/client.go 668-690 rewriteModel overwrites body.model with the tuned band
#
# Enforced by: cmd/rogerai-broker/request_shape_bdd_test.go (to be written; REAL broker, REAL
# station tunnel, REAL ledger - no mocks).

Feature: Routing request shape - the body carriers, their validation, precedence and stripping

  Background:
    Given a broker with an empty in-memory node registry
    And the fee rate is 10%
    And the consumer default out-price cap is $10/1M
    And the operator ceilings are $100/1M out and $50/1M in
    And node "n-a" is on air for "qwen3-32b" at in $0.10 out $0.30 per 1M, seen just now
    And node "n-b" is on air for "qwen3-32b" at in $0.20 out $0.60 per 1M, seen just now
    And node "n-c" is on air for "qwen3-32b" at in $0.50 out $2.00 per 1M, seen just now
    And a logged-in consumer "u-1" with a $5.00 balance

  # --- the three carriers parse ----------------------------------------------------------
  Scenario: A request with no routing carrier behaves exactly as today
    When "u-1" posts a chat completion for "qwen3-32b" with no routing body and no routing headers
    Then the response is 200
    And the served node is one of "n-a", "n-b"
    And the station received a body whose only top-level keys are "model", "messages"

  Scenario: An empty provider object is accepted and changes nothing
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {}`
    Then the response is 200

  Scenario: An empty roger object is accepted and changes nothing
    When "u-1" posts a chat completion for "qwen3-32b" with body `"roger": {}`
    Then the response is 200

  Scenario: An empty models array is accepted when model is present
    When "u-1" posts a chat completion for "qwen3-32b" with body `"models": []`
    Then the response is 200
    And the response header X-RogerAI-Model is "qwen3-32b"

  @slice0
  Scenario: A null provider object is treated as absent
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": null`
    Then the response is 200

  @slice0
  Scenario: A null roger object is treated as absent
    When "u-1" posts a chat completion for "qwen3-32b" with body `"roger": null`
    Then the response is 200

  @slice0
  Scenario Outline: A null VALUE for any routing key means absent, at every level (§1a), never a 400
    # null is "not stated": the header counterpart or the default applies. A null ELEMENT
    # inside a list is still a malformed entry (see the type/range table).
    When "u-1" posts a chat completion for "qwen3-32b" with body `<fragment>`
    Then the response is 200
    And the routing pass saw no value for "<path>"

    Examples:
      | fragment                                              | path                          |
      | "provider": {"max_price": {"prompt": null}}           | provider.max_price.prompt     |
      | "provider": {"max_price": null}                       | provider.max_price            |
      | "provider": {"allow_fallbacks": null}                 | provider.allow_fallbacks      |
      | "provider": {"sort": null}                            | provider.sort                 |
      | "provider": {"order": null}                           | provider.order                |
      | "provider": {"only": null}                            | provider.only                 |
      | "provider": {"quantizations": null}                   | provider.quantizations        |
      | "roger": {"require": null}                            | roger.require                 |
      | "roger": {"params_b": null}                           | roger.params_b                |
      | "roger": {"min_ctx": null}                            | roger.min_ctx                 |
      | "roger": {"trust_min": null}                          | roger.trust_min               |
      | "roger": {"self_hosted_only": null}                   | roger.self_hosted_only        |
      | "roger": {"region": null}                             | roger.region                  |
      | "roger": {"freq": null}                               | roger.freq                    |
      | "roger": {"profile": null}                            | roger.profile                 |

  @slice0
  Scenario: A null models array is treated as absent
    When "u-1" posts a chat completion for "qwen3-32b" with body `"models": null`
    Then the response is 200

  Scenario: A fully populated routing body that is satisfiable is honored
    When "u-1" posts a chat completion for "qwen3-32b" with body:
      """
      "provider": {"only": ["n-a", "n-b"], "ignore": ["n-c"], "allow_fallbacks": true,
                   "sort": "price", "max_price": {"prompt": 0.25, "completion": 0.70}},
      "roger": {"min_tps": 0, "trust_min": "any", "self_hosted_only": true}
      """
    Then the response is 200
    And the served node is "n-a"

  # --- wrong container types are a 400 ---------------------------------------------------
  Scenario Outline: A routing carrier of the wrong JSON type is a 400 naming the key
    When "u-1" posts a chat completion for "qwen3-32b" with body `<fragment>`
    Then the response is 400
    And the error code is "invalid_routing_value"
    And the error message names "<key>"
    And no hold was placed and no station was dispatched

    Examples:
      | fragment                          | key      |
      | "provider": "n-a"                 | provider |
      | "provider": ["n-a"]               | provider |
      | "provider": 1                     | provider |
      | "provider": true                  | provider |
      | "roger": "cheap"                  | roger    |
      | "roger": ["cheap"]                | roger    |
      | "roger": 0                        | roger    |
      | "models": "qwen3-32b"             | models   |
      | "models": {"0": "qwen3-32b"}      | models   |
      | "models": 3                       | models   |
      | "models": [1, 2]                  | models   |
      | "models": [null]                  | models   |
      | "models": [""]                    | models   |
      | "models": [{"id": "qwen3-32b"}]   | models   |

  # --- unknown keys are a 400 at every nesting level ------------------------------------
  Scenario Outline: An unknown key under provider or roger is a 400 that names the full path
    When "u-1" posts a chat completion for "qwen3-32b" with body `<fragment>`
    Then the response is 400
    And the error code is "unknown_routing_key"
    And the error message is "unknown routing key <path>"
    And no hold was placed and no station was dispatched

    Examples:
      | fragment                                         | path                        |
      | "provider": {"foo": 1}                           | provider.foo                |
      | "provider": {"Order": ["n-a"]}                   | provider.Order              |
      | "provider": {"data_collection": "deny"}          | provider.data_collection    |
      | "provider": {"zdr": true}                        | provider.zdr                |
      | "provider": {"enforce_distillable_text": true}   | provider.enforce_distillable_text |
      | "provider": {"preferred_min_throughput": 20}     | provider.preferred_min_throughput |
      | "provider": {"preferred_max_latency": 2}         | provider.preferred_max_latency |
      | "provider": {"max_price": {"images": 1}}         | provider.max_price.images   |
      | "provider": {"max_price": {"audio": 1}}          | provider.max_price.audio    |
      | "provider": {"sort": {"by": "price", "partition": "none"}} | provider.sort.by  |
      | "roger": {"foo": 1}                              | roger.foo                   |
      | "roger": {"Pref": "cheap"}                       | roger.Pref                  |
      | "roger": {"max_cost_usd": 0.02}                  | roger.max_cost_usd          |
      | "roger": {"node": "n-a"}                         | roger.node                  |
      | "roger": {"params": [7, 70]}                     | roger.params                |
      | "roger": {"require": ["tools"], "requires": []}  | roger.requires              |

  @slice0
  Scenario: Several unknown keys are all reported, first in document order named in the message
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"foo": 1, "bar": 2}`
    Then the response is 400
    And the error code is "unknown_routing_key"
    And the error message names "provider.foo"

  @slice0
  Scenario: An unknown key with a known-looking OpenRouter name is still rejected, not silently dropped
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"require_parameters": true, "experimental": {}}`
    Then the response is 400
    And the error code is "unknown_routing_key"
    And the error message names "provider.experimental"

  @slice0
  Scenario: Unknown TOP-LEVEL OpenAI keys still pass through to the station untouched
    When "u-1" posts a chat completion for "qwen3-32b" with body `"frequency_penalty": 0.3, "some_vendor_key": {"x": 1}`
    Then the response is 200
    And the station received top-level key "frequency_penalty" with value 0.3
    And the station received top-level key "some_vendor_key" with value {"x": 1}

  @slice0
  Scenario: `route` (OpenRouter's deprecated key) is an unknown top-level key and passes through, not a 400
    When "u-1" posts a chat completion for "qwen3-32b" with body `"route": "fallback"`
    Then the response is 200
    And the station received top-level key "route" with value "fallback"

  # --- max_price.image is reserved -----------------------------------------------------
  @slice0
  Scenario: max_price.image is a 400 unsupported_routing_key until image pricing exists
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"max_price": {"image": 0.01}}`
    Then the response is 400
    And the error code is "unsupported_routing_key"
    And the error message names "provider.max_price.image"

  # --- type and range validation, one row per rule in §1a ------------------------------
  Scenario Outline: A routing value of the wrong type or out of range is a 400 naming the key
    When "u-1" posts a chat completion for "qwen3-32b" with body `<fragment>`
    Then the response is 400
    And the error code is "invalid_routing_value"
    And the error message names "<path>"
    And no hold was placed and no station was dispatched

    Examples: provider.max_price
      | fragment                                              | path                          |
      | "provider": {"max_price": {"prompt": -0.1}}           | provider.max_price.prompt     |
      | "provider": {"max_price": {"completion": -1}}         | provider.max_price.completion |
      | "provider": {"max_price": {"request": -0.01}}         | provider.max_price.request    |
      | "provider": {"max_price": {"prompt": "NaN"}}          | provider.max_price.prompt     |
      | "provider": {"max_price": {"prompt": "Inf"}}          | provider.max_price.prompt     |
      | "provider": {"max_price": {"prompt": "-Infinity"}}    | provider.max_price.prompt     |
      | "provider": {"max_price": {"prompt": "0.5"}}          | provider.max_price.prompt     |
      | "provider": {"max_price": {"prompt": true}}           | provider.max_price.prompt     |
      | "provider": {"max_price": {"prompt": [0.5]}}          | provider.max_price.prompt     |
      | "provider": {"max_price": 0.5}                        | provider.max_price            |
      | "provider": {"max_price": "cheap"}                    | provider.max_price            |
      | "provider": {"max_price": [0.5, 1.0]}                 | provider.max_price            |
      | "provider": {"max_price": {"prompt": 1e400}}          | provider.max_price.prompt     |

    Examples: provider.order / only / ignore / quantizations
      | fragment                                              | path                          |
      | "provider": {"order": "n-a"}                          | provider.order                |
      | "provider": {"order": [""]}                           | provider.order                |
      | "provider": {"order": [" "]}                          | provider.order                |
      | "provider": {"order": [1]}                            | provider.order                |
      | "provider": {"order": [null]}                         | provider.order                |
      | "provider": {"order": [{"id": "n-a"}]}                | provider.order                |
      | "provider": {"only": "n-a"}                           | provider.only                 |
      | "provider": {"only": [""]}                            | provider.only                 |
      | "provider": {"only": [2]}                             | provider.only                 |
      | "provider": {"ignore": "n-z"}                         | provider.ignore               |
      | "provider": {"ignore": [""]}                          | provider.ignore               |
      | "provider": {"ignore": [false]}                       | provider.ignore               |
      | "provider": {"quantizations": "Q8_0"}                 | provider.quantizations        |
      | "provider": {"quantizations": [""]}                   | provider.quantizations        |
      | "provider": {"quantizations": [8]}                    | provider.quantizations        |

    Examples: provider.allow_fallbacks / sort / require_parameters
      | fragment                                              | path                          |
      | "provider": {"allow_fallbacks": "false"}              | provider.allow_fallbacks      |
      | "provider": {"allow_fallbacks": 0}                    | provider.allow_fallbacks      |
      | "provider": {"sort": "cheapest"}                      | provider.sort                 |
      | "provider": {"sort": "PRICE"}                         | provider.sort                 |
      | "provider": {"sort": ""}                              | provider.sort                 |
      | "provider": {"sort": 1}                               | provider.sort                 |
      | "provider": {"sort": ["price"]}                       | provider.sort                 |
      | "provider": {"require_parameters": "yes"}             | provider.require_parameters   |
      | "provider": {"require_parameters": 1}                 | provider.require_parameters   |

    Examples: roger.pref / require / trust_min (enumerated values are exact lowercase, §1a: "Tools", "Verified", "CHEAP" are invalid)
      | fragment                                              | path                          |
      | "roger": {"pref": "cheapest"}                         | roger.pref                    |
      | "roger": {"pref": "CHEAP"}                            | roger.pref                    |
      | "roger": {"pref": ""}                                 | roger.pref                    |
      | "roger": {"pref": 1}                                  | roger.pref                    |
      | "roger": {"pref": ["cheap"]}                          | roger.pref                    |
      | "roger": {"require": "tools"}                         | roger.require                 |
      | "roger": {"require": [""]}                            | roger.require                 |
      | "roger": {"require": ["Tools"]}                       | roger.require                 |
      | "roger": {"require": ["reasoning"]}                   | roger.require                 |
      | "roger": {"require": ["json"]}                        | roger.require                 |
      | "roger": {"require": ["audio"]}                       | roger.require                 |
      | "roger": {"require": [1]}                             | roger.require                 |
      | "roger": {"trust_min": "high"}                        | roger.trust_min               |
      | "roger": {"trust_min": "Verified"}                    | roger.trust_min               |
      | "roger": {"trust_min": ""}                            | roger.trust_min               |
      | "roger": {"trust_min": 2}                             | roger.trust_min               |
      | "roger": {"trust_min": true}                          | roger.trust_min               |

    Examples: roger.params_b / min_ctx / min_tps / max_ttft_ms
      | fragment                                              | path                          |
      | "roger": {"params_b": 7}                              | roger.params_b                |
      | "roger": {"params_b": [7]}                            | roger.params_b                |
      | "roger": {"params_b": [7, 70, 100]}                   | roger.params_b                |
      | "roger": {"params_b": [70, 7]}                        | roger.params_b                |
      | "roger": {"params_b": [0, 70]}                        | roger.params_b                |
      | "roger": {"params_b": [-1, 70]}                       | roger.params_b                |
      | "roger": {"params_b": ["7", "70"]}                    | roger.params_b                |
      | "roger": {"params_b": ["NaN", 70]}                    | roger.params_b                |
      | "roger": {"params_b": [7, "Inf"]}                     | roger.params_b                |
      | "roger": {"params_b": [null, 70]}                     | roger.params_b                |
      | "roger": {"params_b": {"min": 7, "max": 70}}          | roger.params_b                |
      | "roger": {"params_b": "7-70"}                         | roger.params_b                |
      | "roger": {"min_ctx": 0}                               | roger.min_ctx                 |
      | "roger": {"min_ctx": -1}                              | roger.min_ctx                 |
      | "roger": {"min_ctx": 4096.5}                          | roger.min_ctx                 |
      | "roger": {"min_ctx": "32k"}                           | roger.min_ctx                 |
      | "roger": {"min_ctx": "NaN"}                           | roger.min_ctx                 |
      | "roger": {"min_ctx": true}                            | roger.min_ctx                 |
      | "roger": {"min_tps": -1}                              | roger.min_tps                 |
      | "roger": {"min_tps": "20"}                            | roger.min_tps                 |
      | "roger": {"min_tps": "Inf"}                           | roger.min_tps                 |
      | "roger": {"min_tps": [20]}                            | roger.min_tps                 |
      | "roger": {"max_ttft_ms": 0}                           | roger.max_ttft_ms             |
      | "roger": {"max_ttft_ms": -500}                        | roger.max_ttft_ms             |
      | "roger": {"max_ttft_ms": "1500ms"}                    | roger.max_ttft_ms             |
      | "roger": {"max_ttft_ms": "NaN"}                       | roger.max_ttft_ms             |
      | "roger": {"max_ttft_ms": 1e400}                       | roger.max_ttft_ms             |

    Examples: roger.self_hosted_only / confidential / region / freq / profile (region tokens are exact lowercase, §1a: "EU" is invalid)
      | fragment                                              | path                          |
      | "roger": {"self_hosted_only": "true"}                 | roger.self_hosted_only        |
      | "roger": {"self_hosted_only": 1}                      | roger.self_hosted_only        |
      | "roger": {"confidential": "1"}                        | roger.confidential            |
      | "roger": {"confidential": 1}                          | roger.confidential            |
      | "roger": {"region": "eu"}                             | roger.region                  |
      | "roger": {"region": [""]}                             | roger.region                  |
      | "roger": {"region": ["EU"]}                           | roger.region                  |
      | "roger": {"region": ["e"]}                            | roger.region                  |
      | "roger": {"region": ["europe-west"]}                  | roger.region                  |
      | "roger": {"region": ["eu west"]}                      | roger.region                  |
      | "roger": {"region": ["eu-1"]}                         | roger.region                  |
      | "roger": {"region": [1]}                              | roger.region                  |
      | "roger": {"freq": 1234}                               | roger.freq                    |
      | "roger": {"freq": ""}                                 | roger.freq                    |
      | "roger": {"freq": ["code"]}                           | roger.freq                    |
      | "roger": {"profile": 1}                               | roger.profile                 |
      | "roger": {"profile": ""}                              | roger.profile                 |
      | "roger": {"profile": "coding"}                        | roger.profile                 |

  Scenario Outline: Zero is a valid "no floor / no cap" for the measurement and price knobs
    When "u-1" posts a chat completion for "qwen3-32b" with body `<fragment>`
    Then the response is 200

    Examples:
      | fragment                                          |
      | "roger": {"min_tps": 0}                           |
      | "provider": {"max_price": {"prompt": 0}}          |
      | "provider": {"max_price": {"completion": 0}}      |
      | "provider": {"max_price": {"request": 0}}         |

  Scenario Outline: A list longer than 32 entries is a 400 naming the list
    When "u-1" posts a chat completion for "qwen3-32b" with body where "<path>" is a list of 33 distinct valid entries
    Then the response is 400
    And the error code is "invalid_routing_value"
    And the error message names "<path>"

    Examples:
      | path                    |
      | provider.order          |
      | provider.only           |
      | provider.ignore         |
      | provider.quantizations  |
      | roger.require           |
      | roger.region            |

  Scenario Outline: A list of exactly 32 entries is accepted
    When "u-1" posts a chat completion for "qwen3-32b" with body where "<path>" is a list of 32 distinct valid entries
    Then the response is not 400

    Examples:
      | path                    |
      | provider.order          |
      | provider.only           |
      | provider.ignore         |
      | provider.quantizations  |
      | roger.region            |

  Scenario: Duplicate entries inside a node list are de-duplicated, not rejected
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"only": ["n-a", "n-a", "n-a"]}`
    Then the response is 200
    And the served node is "n-a"

  Scenario: A models list with more than 5 distinct entries after de-dup is a 400
    When "u-1" posts a chat completion for "m-1" with body `"models": ["m-2", "m-3", "m-4", "m-5", "m-6"]`
    Then the response is 400
    And the error code is "invalid_routing_value"
    And the error message names "models"

  Scenario: A models list that de-dups to 5 or fewer is accepted
    When "u-1" posts a chat completion for "qwen3-32b" with body `"models": ["qwen3-32b", "qwen3-32b", "qwen3-32b", "qwen3-32b", "qwen3-32b", "qwen3-32b"]`
    Then the response is 200

  Scenario: Neither model nor models present is today's 400, unchanged
    When "u-1" posts a chat completion with no "model" field and body `"provider": {"sort": "price"}`
    Then the response is 400
    And no hold was placed and no station was dispatched

  Scenario: model absent but models present makes the first entry primary
    When "u-1" posts a chat completion with no "model" field and body `"models": ["qwen3-32b"]`
    Then the response is 200
    And the response header X-RogerAI-Model is "qwen3-32b"

  # --- duplicate keys and oversized routing objects -----------------------------------
  Scenario: Duplicate JSON keys for a routing carrier resolve to the LAST occurrence, like the model field does today
    When "u-1" posts a chat completion for "qwen3-32b" with raw body `{"model":"qwen3-32b","messages":[{"role":"user","content":"hi"}],"provider":{"only":["n-c"]},"provider":{"only":["n-a"]}}`
    Then the response is 200
    And the served node is "n-a"

  Scenario: A duplicate "model" key with a routing body still resolves to the last model
    When "u-1" posts a chat completion with raw body `{"model":"nope","model":"qwen3-32b","messages":[{"role":"user","content":"hi"}],"roger":{"pref":"cheap"}}`
    Then the response is 200
    And the response header X-RogerAI-Model is "qwen3-32b"

  Scenario: A routing object that pushes the body past the 4 MiB limit is truncated by the reader and rejected as malformed, never dispatched
    When "u-1" posts a chat completion for "qwen3-32b" whose "roger" object is padded to 5 MiB
    Then the response is 400
    And no hold was placed and no station was dispatched

  Scenario: A large but valid routing object under the limit is accepted and does not reach the station
    When "u-1" posts a chat completion for "qwen3-32b" whose "provider.ignore" holds 32 node ids of 200 characters each
    Then the response is 200
    And the station received no top-level key "provider"

  Scenario: Deeply nested garbage under a routing carrier is a 400, not a panic
    When "u-1" posts a chat completion for "qwen3-32b" with body `"roger": {"pref": {"a": {"b": {"c": {"d": [[[[1]]]]}}}}}`
    Then the response is 400
    And the error code is "invalid_routing_value"

  # --- exclusive pairs --------------------------------------------------------------------
  Scenario: provider.sort together with roger.pref is a 400 conflicting_routing_keys
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"sort": "price"}, "roger": {"pref": "fast"}`
    Then the response is 400
    And the error code is "conflicting_routing_keys"
    And the error message names "provider.sort"
    And the error message names "roger.pref"

  Scenario: provider.sort with roger.pref "balanced" is STILL a conflict (explicit is explicit)
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"sort": "price"}, "roger": {"pref": "balanced"}`
    Then the response is 400
    And the error code is "conflicting_routing_keys"

  Scenario: provider.sort with the LEGACY X-Roger-Pref header is NOT a conflict - the body wins and the header is ignored
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Pref "fast" and body `"provider": {"sort": "price"}`
    Then the response is 200
    And the served node is "n-a"

  Scenario: provider.order naming a node outside provider.only is a 400 conflicting_routing_keys
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"only": ["n-a"], "order": ["n-b", "n-a"]}`
    Then the response is 400
    And the error code is "conflicting_routing_keys"
    And the error message names "n-b"

  Scenario: provider.order that is a subset of provider.only is accepted
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"only": ["n-a", "n-b"], "order": ["n-b"]}`
    Then the response is 200
    And the served node is "n-b"

  Scenario: A node in both provider.only and provider.ignore is ignored (deny wins), not a 400
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"only": ["n-a", "n-b"], "ignore": ["n-a"]}`
    Then the response is 200
    And the served node is "n-b"

  Scenario: A node in both provider.order and provider.ignore is skipped from the order (deny wins), not a 400
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"order": ["n-a", "n-b"], "ignore": ["n-a"]}`
    Then the response is 200
    And the served node is "n-b"

  Scenario: allow_fallbacks false with an order of several nodes is NOT a conflict
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"order": ["n-a", "n-b"], "allow_fallbacks": false}`
    Then the response is 200
    And the served node is "n-a"

  Scenario: roger.confidential true together with roger.trust_min "verified" is accepted and the stricter (confidential) applies
    Given node "n-tee" is on air for "qwen3-32b" at in $0.10 out $0.30 per 1M, confidential-attested, seen just now
    When "u-1" posts a chat completion for "qwen3-32b" with body `"roger": {"confidential": true, "trust_min": "verified"}`
    Then the response is 200
    And the served node is "n-tee"

  Scenario: roger.confidential false together with roger.trust_min "confidential" is not an error - the stricter wins
    # A default (false) can never weaken a stated restriction; the same rule makes a body
    # `confidential:false` unable to cancel an X-Roger-Confidential header (contract §1a).
    Given node "n-plain" is on air for "qwen3-32b" and is not attested
    And node "n-tee" is on air for "qwen3-32b" and is TEE-attested
    When "u-1" posts a chat completion for "qwen3-32b" with body `"roger": {"confidential": false, "trust_min": "confidential"}`
    Then the response is 200
    And the served node is "n-tee"

  Scenario: a body confidential false cannot cancel an X-Roger-Confidential header
    Given node "n-plain" is on air for "qwen3-32b" and is not attested
    And node "n-tee" is on air for "qwen3-32b" and is TEE-attested
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Confidential "1" and body `"roger": {"confidential": false}`
    Then the response is 200
    And the served node is "n-tee"

  Scenario: roger.freq together with provider.only is accepted; the intersection applies
    Given a private band "band-1" with code "FREQ-1" whose only station is "n-p" on air for "qwen3-32b" at in $0 out $0
    When "u-1" posts a chat completion for "qwen3-32b" with body `"roger": {"freq": "FREQ-1"}, "provider": {"only": ["n-a"]}`
    Then the response is 503
    And the error message is the uniform "no station on that frequency (it may be off air) - check the code"

  # --- body wins over header: one block per header pair in §1a -------------------------
  # Each pair: both agree / conflict (body wins) / header only / body only.

  Scenario: X-Roger-Node and provider.order agree
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Node "n-b" and body `"provider": {"order": ["n-b"], "allow_fallbacks": false}`
    Then the response is 200
    And the served node is "n-b"

  Scenario: X-Roger-Node conflicts with provider.order - the body wins
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Node "n-c" and body `"provider": {"order": ["n-a"], "allow_fallbacks": false}`
    Then the response is 200
    And the served node is "n-a"

  Scenario: X-Roger-Node alone still pins exactly as today
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Node "n-b" and no routing body
    Then the response is 200
    And the served node is "n-b"
    And no broker-side failover was planned

  Scenario: provider.order with allow_fallbacks false alone pins exactly like the header
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"order": ["n-b"], "allow_fallbacks": false}`
    Then the response is 200
    And the served node is "n-b"
    And no broker-side failover was planned

  Scenario: X-Roger-Node with a body provider object that has NO order keeps the header pin (no body value to win)
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Node "n-b" and body `"provider": {"sort": "price"}`
    Then the response is 200
    And the served node is "n-b"

  Scenario: A kept header pin outside a body provider.only is a 400 conflicting_routing_keys (same rule as order outside only)
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Node "n-b" and body `"provider": {"only": ["n-a"]}`
    Then the response is 400
    And the error code is "conflicting_routing_keys"
    And no hold was placed and no station was dispatched

  Scenario: A kept header pin inside a body provider.only is honored
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Node "n-b" and body `"provider": {"only": ["n-a", "n-b"]}`
    Then the response is 200
    And the served node is "n-b"

  @slice0
  Scenario: X-Roger-Exclude-Nodes and provider.ignore agree
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Exclude-Nodes "n-a" and body `"provider": {"ignore": ["n-a"]}`
    Then the response is 200
    And the served node is "n-b"

  Scenario: X-Roger-Exclude-Nodes and provider.ignore differ - a deny list narrows, so the two are UNIONED
    # The one carrier where "body wins" does not apply: a deny list can only narrow, and a
    # default can never weaken a stated restriction (contract §1a, the narrowing exception,
    # same principle as X-Roger-Confidential vs confidential:false).
    Given node "n-c" is on air for "qwen3-32b" at in $0.10 out $0.30
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Exclude-Nodes "n-a" and body `"provider": {"ignore": ["n-b"], "sort": "price"}`
    Then the response is 200
    And the served node is "n-c"

  @slice0
  Scenario: The union of header and body deny lists leaving nothing is a 503 no_match
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Exclude-Nodes "n-a" and body `"provider": {"ignore": ["n-b", "n-c"]}`
    Then the response is 503
    And the error code is "no_match"

  Scenario: X-Roger-Exclude-Nodes alone excludes exactly as today
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Exclude-Nodes "n-a,n-b" and no routing body
    Then the response is 200
    And the served node is "n-c"

  Scenario: provider.ignore alone excludes
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"ignore": ["n-a", "n-b"]}`
    Then the response is 200
    And the served node is "n-c"

  Scenario: X-Roger-Min-TPS and roger.min_tps agree
    Given node "n-a" has a measured throughput of 10 tok/s
    And node "n-b" has a measured throughput of 50 tok/s
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Min-TPS "30" and body `"roger": {"min_tps": 30}`
    Then the response is 200
    And the served node is "n-b"

  Scenario: X-Roger-Min-TPS conflicts with roger.min_tps - the body wins
    Given node "n-a" has a measured throughput of 10 tok/s
    And node "n-b" has a measured throughput of 50 tok/s
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Min-TPS "30" and body `"roger": {"min_tps": 5}, "provider": {"sort": "price"}`
    Then the response is 200
    And the served node is "n-a"

  Scenario: X-Roger-Min-TPS alone filters exactly as today
    Given node "n-a" has a measured throughput of 10 tok/s
    And node "n-b" has a measured throughput of 50 tok/s
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Min-TPS "30" and no routing body
    Then the response is 200
    And the served node is "n-b"

  Scenario: roger.min_tps alone filters
    Given node "n-a" has a measured throughput of 10 tok/s
    And node "n-b" has a measured throughput of 50 tok/s
    When "u-1" posts a chat completion for "qwen3-32b" with body `"roger": {"min_tps": 30}`
    Then the response is 200
    And the served node is "n-b"

  Scenario: X-Roger-Max-Price and provider.max_price.prompt agree
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Max-Price "0.15" and body `"provider": {"max_price": {"prompt": 0.15}}`
    Then the response is 200
    And the served node is "n-a"

  Scenario: X-Roger-Max-Price conflicts with provider.max_price.prompt - the body wins
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Max-Price "0.15" and body `"provider": {"max_price": {"prompt": 0.25}, "ignore": ["n-a"]}`
    Then the response is 200
    And the served node is "n-b"

  Scenario: X-Roger-Max-Price alone caps exactly as today
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Max-Price "0.15" and no routing body
    Then the response is 200
    And the served node is "n-a"

  Scenario: provider.max_price.prompt alone caps
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"max_price": {"prompt": 0.15}}`
    Then the response is 200
    And the served node is "n-a"

  Scenario: X-Roger-Max-Price-Out and provider.max_price.completion agree
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Max-Price-Out "0.50" and body `"provider": {"max_price": {"completion": 0.50}}`
    Then the response is 200
    And the served node is "n-a"

  Scenario: X-Roger-Max-Price-Out conflicts with provider.max_price.completion - the body wins
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Max-Price-Out "0.50" and body `"provider": {"max_price": {"completion": 0.70}, "ignore": ["n-a"]}`
    Then the response is 200
    And the served node is "n-b"

  Scenario: X-Roger-Max-Price-Out alone caps exactly as today
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Max-Price-Out "0.50" and no routing body
    Then the response is 200
    And the served node is "n-a"

  Scenario: provider.max_price.completion alone caps
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"max_price": {"completion": 0.50}}`
    Then the response is 200
    And the served node is "n-a"

  Scenario: X-Roger-Pref and roger.pref agree
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Pref "cheap" and body `"roger": {"pref": "cheap"}`
    Then the response is 200
    And the routing pass ran with pref "cheap"

  Scenario: X-Roger-Pref conflicts with roger.pref - the body wins
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Pref "cheap" and body `"roger": {"pref": "reliable"}`
    Then the response is 200
    And the routing pass ran with pref "reliable"

  Scenario: X-Roger-Pref alone is honored exactly as today
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Pref "fast" and no routing body
    Then the response is 200
    And the routing pass ran with pref "fast"

  Scenario: An unknown X-Roger-Pref header value stays "balanced" (legacy leniency), while an unknown roger.pref is a 400
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Pref "turbo" and no routing body
    Then the response is 200
    And the routing pass ran with pref "balanced"

  Scenario: roger.pref alone is honored
    When "u-1" posts a chat completion for "qwen3-32b" with body `"roger": {"pref": "reliable"}`
    Then the response is 200
    And the routing pass ran with pref "reliable"

  Scenario: X-Roger-Confidential and roger.confidential agree
    Given node "n-tee" is on air for "qwen3-32b" at in $0.10 out $0.30 per 1M, confidential-attested, seen just now
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Confidential "1" and body `"roger": {"confidential": true}`
    Then the response is 200
    And the served node is "n-tee"

  Scenario: X-Roger-Confidential with roger.confidential false - the stricter wins, the header restriction stands (§1a narrowing exception)
    Given node "n-tee" is on air for "qwen3-32b" at in $0.10 out $0.30 per 1M, confidential-attested, seen just now
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Confidential "1" and body `"roger": {"confidential": false, "pref": "cheap"}`
    Then the response is 200
    And the served node is "n-tee"

  Scenario: X-Roger-Confidential with roger.confidential false and the only attested node ignored is a 503 no_match, not a downgrade
    Given node "n-tee" is on air for "qwen3-32b" at in $0.10 out $0.30 per 1M, confidential-attested, seen just now
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Confidential "1" and body `"roger": {"confidential": false}, "provider": {"ignore": ["n-tee"]}`
    Then the response is 503
    And the error code is "no_match"
    And "n-a" was never a candidate

  Scenario: X-Roger-Confidential alone restricts exactly as today
    Given node "n-tee" is on air for "qwen3-32b" at in $0.10 out $0.30 per 1M, confidential-attested, seen just now
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Confidential "1" and no routing body
    Then the response is 200
    And the served node is "n-tee"

  Scenario: roger.confidential alone restricts
    Given node "n-tee" is on air for "qwen3-32b" at in $0.10 out $0.30 per 1M, confidential-attested, seen just now
    When "u-1" posts a chat completion for "qwen3-32b" with body `"roger": {"confidential": true}`
    Then the response is 200
    And the served node is "n-tee"

  Scenario: X-Roger-Freq and roger.freq agree
    Given a private band "band-1" with code "FREQ-1" whose only station is "n-p" on air for "qwen3-32b" at in $0 out $0
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Freq "FREQ-1" and body `"roger": {"freq": "FREQ-1"}`
    Then the response is 200
    And the served node is "n-p"

  Scenario: X-Roger-Freq conflicts with roger.freq - the body wins
    Given a private band "band-1" with code "FREQ-1" whose only station is "n-p" on air for "qwen3-32b" at in $0 out $0
    And a private band "band-2" with code "FREQ-2" whose only station is "n-q" on air for "qwen3-32b" at in $0 out $0
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Freq "FREQ-1" and body `"roger": {"freq": "FREQ-2"}`
    Then the response is 200
    And the served node is "n-q"

  Scenario: X-Roger-Freq alone tunes in exactly as today
    Given a private band "band-1" with code "FREQ-1" whose only station is "n-p" on air for "qwen3-32b" at in $0 out $0
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Freq "FREQ-1" and no routing body
    Then the response is 200
    And the served node is "n-p"

  Scenario: roger.freq alone tunes in
    Given a private band "band-1" with code "FREQ-1" whose only station is "n-p" on air for "qwen3-32b" at in $0 out $0
    When "u-1" posts a chat completion for "qwen3-32b" with body `"roger": {"freq": "FREQ-1"}`
    Then the response is 200
    And the served node is "n-p"

  Scenario: An unresolvable roger.freq gets the same uniform message as the header form
    When "u-1" posts a chat completion for "qwen3-32b" with body `"roger": {"freq": "NOPE-0000"}`
    Then the response is 503
    And the error message is the uniform "no station on that frequency (it may be off air) - check the code"
    And the raw code "NOPE-0000" appears in no log line

  Scenario: The body freq is never logged raw, same as the header
    Given a private band "band-1" with code "FREQ-1" whose only station is "n-p" on air for "qwen3-32b" at in $0 out $0
    When "u-1" posts a chat completion for "qwen3-32b" with body `"roger": {"freq": "FREQ-1"}`
    Then the raw code "FREQ-1" appears in no log line

  Scenario: A header whose body counterpart is present but null falls back to the header (null means absent)
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Exclude-Nodes "n-a" and body `"provider": {"ignore": null, "sort": "price"}`
    Then the response is 200
    And the served node is "n-b"

  @slice0
  Scenario: Body-over-header precedence is identical on the streaming path
    When "u-1" posts a STREAMING chat completion for "qwen3-32b" with header X-Roger-Node "n-c" and body `"provider": {"order": ["n-a"], "allow_fallbacks": false}`
    Then the response is 200
    And the served node is "n-a"

  Scenario: A malformed routing body is a 400 even when the headers alone would have routed fine
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Node "n-a" and body `"provider": {"sort": "cheapest"}`
    Then the response is 400
    And the error code is "invalid_routing_value"

  # --- stripping: the station never sees the carriers -----------------------------------
  Scenario: models, provider and roger are stripped from the body the station receives (direct path)
    When "u-1" posts a chat completion for "qwen3-32b" with body `"models": ["qwen3-32b"], "provider": {"sort": "price"}, "roger": {"pref": null}`
    Then the response is 200
    And the station received no top-level key "models"
    And the station received no top-level key "provider"
    And the station received no top-level key "roger"
    And the station received top-level key "model" with value "qwen3-32b"

  Scenario: Every other body field survives the strip with its value byte-identical
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"sort": "price"}, "temperature": 0.7, "tools": [{"type": "function", "function": {"name": "f", "parameters": {"type": "object"}}}], "response_format": {"type": "json_object"}, "seed": 42, "stream": false`
    Then the response is 200
    And the station received top-level key "temperature" with value 0.7
    And the station received top-level key "tools" byte-identical to what was sent
    And the station received top-level key "response_format" byte-identical to what was sent
    And the station received top-level key "seed" with value 42

  Scenario: The strip happens on the streaming path too
    When "u-1" posts a STREAMING chat completion for "qwen3-32b" with body `"provider": {"sort": "price"}`
    Then the response is 200
    And the station received no top-level key "provider"
    And the station received top-level key "stream" with value true
    And the station received a "stream_options" object with "include_usage" true

  Scenario: The strip happens on the edge/Tower bridge path
    Given no direct node is on air for "tower-only-model"
    And an approved Tower "tw-1" serves "tower-only-model" through the edge bridge
    When "u-1" posts a chat completion for "tower-only-model" with body `"provider": {"sort": "price"}, "roger": {"pref": null}`
    Then the response is 200
    And the Tower received no top-level key "provider"
    And the Tower received no top-level key "roger"
    And the Tower received no top-level key "models"

  Scenario: The strip happens on the agent-harness relay path
    When the agent harness relays a turn for "qwen3-32b" on behalf of "u-1" with body `"roger": {"pref": "reliable"}`
    Then the station received no top-level key "roger"

  Scenario: The strip happens on a grant-funded relay
    Given owner "o-1" owns node "n-a" and minted grant "rog-grant_1" for "qwen3-32b"
    When the grant holder posts a chat completion for "qwen3-32b" with body `"provider": {"ignore": []}`
    Then the response is 200
    And the station received no top-level key "provider"

  Scenario: The prompt re-count and moderation screen see the SAME messages before and after the strip
    When "u-1" posts a chat completion for "qwen3-32b" with a 3000-token prompt and body `"provider": {"sort": "price"}`
    Then the broker's prompt estimate is within 5% of the estimate for the same request without the routing body
    And the moderation screen received the identical prompt text

  Scenario: Stripping does not alter the body used to size the hold
    # sort price ranks n-a first, but n-b and n-c stay in the plan as fallbacks, and the ONE
    # hold covers the priciest pair in the plan (§1b), which is n-c.
    When "u-1" posts a chat completion for "qwen3-32b" with max_tokens 1000 and body `"provider": {"sort": "price"}`
    Then the hold placed equals estimateMaxCost of the STRIPPED body at the priciest station in the plan, "n-c"

  # --- defaults and clamps on price ------------------------------------------------------
  Scenario: A body with no completion cap still gets the $10/1M consumer default
    Given node "n-x" is on air for "qwen3-32b" at in $1 out $40 per 1M, seen just now
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"ignore": ["n-a", "n-b", "n-c"]}`
    Then the response is 503
    And the error code is "no_match"
    And "n-x" was never a candidate

  Scenario: max_price.completion 0 means "default", not "no cap"
    Given node "n-x" is on air for "qwen3-32b" at in $1 out $40 per 1M, seen just now
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"max_price": {"completion": 0}, "ignore": ["n-a", "n-b", "n-c"]}`
    Then the response is 503
    And the error code is "no_match"

  Scenario: An explicit completion cap above the default is honored as sent
    Given node "n-x" is on air for "qwen3-32b" at in $1 out $40 per 1M, seen just now
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"max_price": {"completion": 50}, "ignore": ["n-a", "n-b", "n-c"]}`
    Then the response is 200
    And the served node is "n-x"

  Scenario: A completion cap above the operator ceiling is clamped to the ceiling, not rejected
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"max_price": {"completion": 1000}}`
    Then the response is 200
    And the routing pass ran with an out-price cap of $100/1M

  Scenario: A prompt cap above the operator in-ceiling is clamped to the ceiling, not rejected
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"max_price": {"prompt": 999}}`
    Then the response is 200
    And the routing pass ran with an in-price cap of $50/1M

  Scenario: Header and body caps are not combined - the lower one does not win, the body does
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Max-Price-Out "0.20" and body `"provider": {"max_price": {"completion": 0.70}, "ignore": ["n-a"]}`
    Then the response is 200
    And the served node is "n-b"

  Scenario: The out-cap widens the priceMod reward range exactly as the header did
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"max_price": {"completion": 5}}`
    Then the routing pass ran with a reward range ceiling of $5/1M out

  # --- max_price.request: the per-request USD cap ------------------------------------
  Scenario: A per-request cap larger than the hold changes nothing
    When "u-1" posts a chat completion for "qwen3-32b" with max_tokens 1000 and body `"provider": {"max_price": {"request": 1.00}}`
    Then the response is 200
    And the hold placed equals estimateMaxCost at the priciest station in the plan, "n-c"
    And the station received the request's own max_tokens 1000

  Scenario: A per-request cap below the natural hold sizes the hold to the cap and settle clamps to it
    Given node "n-a" alone is on air for "qwen3-32b" at in $1 out $10 per 1M
    When "u-1" posts a chat completion for "qwen3-32b" with max_tokens 100000 and body `"provider": {"max_price": {"request": 0.05}}`
    Then the response is 200
    And the hold placed is $0.05
    And the settled cost is at most $0.05

  Scenario: The station is told the output ceiling the cap implies so it can stop, not just be clamped
    Given node "n-a" alone is on air for "qwen3-32b" at in $1 out $10 per 1M
    When "u-1" posts a chat completion for "qwen3-32b" with max_tokens 100000 and body `"provider": {"max_price": {"request": 0.05}}`
    Then the station received a "max_tokens" no larger than the token count $0.05 buys at $10/1M out after the prompt estimate

  Scenario: A per-request cap so low that the prompt's input cost alone meets it on every station is a 503 no_match with no hold
    # The one case where the cap acts as a plan filter (§1a): a station whose input cost
    # (measured prompt tokens x in price) meets or exceeds the cap can buy no output at all.
    When "u-1" posts a chat completion for "qwen3-32b" with a 20000-token prompt and body `"provider": {"max_price": {"request": 0.000001}}`
    Then the response is 503
    And the error code is "no_match"
    And no hold was placed and no station was dispatched

  Scenario: A per-request cap is NOT a plan filter - a pricier station stays in the plan with a smaller max_tokens
    # 20000 prompt tokens cost $0.002 at n-a (in $0.10) and $0.010 at n-c (in $0.50). A $0.004
    # cap still buys output at n-a and n-b; at n-c the input alone exceeds the cap.
    When "u-1" posts a chat completion for "qwen3-32b" with a 20000-token prompt, max_tokens 10000 and body `"provider": {"max_price": {"request": 0.004}}`
    Then the failover plan contains "n-a" and "n-b"
    And the failover plan does not contain "n-c"
    And the max_tokens the plan carries for "n-b" is smaller than for "n-a"

  Scenario: A per-request cap that every station can buy output under keeps the whole plan
    When "u-1" posts a chat completion for "qwen3-32b" with a 100-token prompt, max_tokens 10000 and body `"provider": {"max_price": {"request": 0.004}}`
    Then the failover plan contains "n-a", "n-b" and "n-c"
    And the hold placed is $0.004

  Scenario: A wallet that cannot cover even the cheapest pair is a 402 before any dispatch - the hold is never shrunk
    Given a logged-in consumer "u-poor" with a $0.01 balance
    When "u-poor" posts a chat completion for "qwen3-32b" with max_tokens 100000 and body `"provider": {"max_price": {"request": 5.00}}`
    Then the response is 402
    And the error code is "insufficient_balance"
    And no hold was placed and no station was dispatched
    And the response carries X-RogerAI-Cost "0"

  Scenario: A per-request cap on a free relay is accepted and irrelevant (no hold)
    Given node "n-free" is on air for "free-model" at in $0 out $0 per 1M, seen just now
    When "u-1" posts a chat completion for "free-model" with body `"provider": {"max_price": {"request": 0.000001}}`
    Then the response is 200
    And no hold was placed

  Scenario: A per-request cap on a grant-funded relay narrows the grant's own hold, never widens it
    Given owner "o-1" owns node "n-a" and minted grant "rog-grant_1" for "qwen3-32b" at price_out $0.30
    When the grant holder posts a chat completion for "qwen3-32b" with max_tokens 100000 and body `"provider": {"max_price": {"request": 0.01}}`
    Then the hold placed is at most $0.01

  Scenario: A per-request cap is enforced against the FIXED grant price, not the market price
    Given owner "o-1" owns node "n-c" and minted grant "rog-grant_1" for "qwen3-32b" at price_out $0.10
    When the grant holder posts a chat completion for "qwen3-32b" with max_tokens 1000 and body `"provider": {"max_price": {"request": 0.0002}}`
    Then the response is 200

  Scenario: A settle that would exceed the per-request cap because the station over-reported tokens is clamped to the cap
    Given node "n-a" alone is on air for "qwen3-32b" at in $1 out $10 per 1M
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"max_price": {"request": 0.01}}`
    And the station's receipt claims 1000000 completion tokens
    Then the settled cost is at most $0.01

  # --- grant and band intersection: a body constraint only narrows -------------------
  Scenario: provider.only naming a node outside the grant owner's fleet cannot reach it
    Given owner "o-1" owns node "n-a" and minted grant "rog-grant_1" for "qwen3-32b"
    When the grant holder posts a chat completion for "qwen3-32b" with body `"provider": {"only": ["n-b"]}`
    Then the response is 503
    And the error code is "no_match"
    And "n-b" was never a candidate

  Scenario: provider.only inside the grant owner's fleet narrows it
    Given owner "o-1" owns nodes "n-a" and "n-b" and minted grant "rog-grant_1" for "qwen3-32b"
    When the grant holder posts a chat completion for "qwen3-32b" with body `"provider": {"only": ["n-b"]}`
    Then the response is 200
    And the served node is "n-b"

  Scenario: provider.order cannot pull a grant onto a node outside the grant
    Given owner "o-1" owns node "n-a" and minted grant "rog-grant_1" for "qwen3-32b"
    When the grant holder posts a chat completion for "qwen3-32b" with body `"provider": {"order": ["n-c", "n-a"]}`
    Then the response is 200
    And the served node is "n-a"

  Scenario: A grant's model deny-list is not bypassed by models[]
    Given owner "o-1" owns node "n-a" serving "qwen3-32b" and "llama-3.3-70b", and minted grant "rog-grant_1" allowing only "qwen3-32b"
    When the grant holder posts a chat completion for "llama-3.3-70b" with body `"models": ["qwen3-32b"]`
    Then the response is 200
    And the response header X-RogerAI-Model is "qwen3-32b"

  Scenario: A grant that denies every model in the list is a 403 grant_model_denied
    Given owner "o-1" owns node "n-a" serving "qwen3-32b" and "llama-3.3-70b", and minted grant "rog-grant_1" allowing only "qwen3-32b"
    When the grant holder posts a chat completion for "llama-3.3-70b" with body `"models": ["mistral-7b"]`
    Then the response is 403
    And the error code is "grant_model_denied"

  Scenario: A grant's price cap is not raised by a larger body cap
    Given owner "o-1" owns node "n-c" and minted grant "rog-grant_1" for "qwen3-32b" at price_out $0.10
    When the grant holder posts a chat completion for "qwen3-32b" with body `"provider": {"max_price": {"completion": 50}}`
    Then the response is 200
    And the settled price out is $0.10/1M

  Scenario: A private band's node set is not widened by provider.only
    Given a private band "band-1" with code "FREQ-1" whose only station is "n-p" on air for "qwen3-32b" at in $0 out $0
    When "u-1" posts a chat completion for "qwen3-32b" with body `"roger": {"freq": "FREQ-1"}, "provider": {"only": ["n-p", "n-a"]}`
    Then the response is 200
    And the served node is "n-p"

  Scenario: A private band's node set is not widened by provider.order
    Given a private band "band-1" with code "FREQ-1" whose only station is "n-p" on air for "qwen3-32b" at in $0 out $0
    When "u-1" posts a chat completion for "qwen3-32b" with body `"roger": {"freq": "FREQ-1"}, "provider": {"order": ["n-a", "n-p"]}`
    Then the response is 200
    And the served node is "n-p"

  Scenario: A band's model deny-list is honored across models[] with the uniform message when every entry is denied
    Given a private band "band-1" with code "FREQ-1" whose station "n-p" serves "qwen3-32b", and the band denies "llama-3.3-70b" and "mistral-7b"
    When "u-1" posts a chat completion for "llama-3.3-70b" with body `"models": ["mistral-7b"], "roger": {"freq": "FREQ-1"}`
    Then the response is 503
    And the error message is the uniform "no station on that frequency (it may be off air) - check the code"

  Scenario: An anonymous caller's body caps cannot admit a paid station
    Given node "n-free" is on air for "qwen3-32b" at in $0 out $0 per 1M, seen just now
    When an anonymous caller posts a chat completion for "qwen3-32b" with body `"provider": {"max_price": {"completion": 100}, "order": ["n-c"]}`
    Then the response is 200
    And the served node is "n-free"

  Scenario: An anonymous caller whose provider.only names only paid stations gets today's 401 "log in to spend", never a 503
    # The stations exist; the caller cannot pay them (anonCannotPay, §1a).
    When an anonymous caller posts a chat completion for "qwen3-32b" with body `"provider": {"only": ["n-c"]}`
    Then the response is 401
    And the error message says to log in to spend
    And the response carries X-RogerAI-Cost "0"
    And no hold was placed and no station was dispatched

  Scenario: The monthly cap gate still runs before the hold with any routing body
    Given "u-1" has a monthly cap of $0.001 and has spent $0.001 this month
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"sort": "price"}`
    Then the response is the monthly-cap refusal
    And no station was dispatched

  Scenario: The operator ceiling at register is unaffected by consumer routing bodies
    When a station registers "qwen3-32b" at out $200/1M
    Then the registration is rejected
    And no consumer request body can make that offer appear

  # --- moderation precedes every routing knob -------------------------------------------
  Scenario: A sync-mode moderation block happens before the pick, whatever the routing body says
    Given the broker runs moderation in sync mode
    When "u-1" posts an illegal prompt for "qwen3-32b" with body `"provider": {"order": ["n-a"], "allow_fallbacks": false}, "roger": {"self_hosted_only": true}`
    Then the response is the moderation refusal
    And no routing pass ran
    And no hold was placed and no station was dispatched

  Scenario: An async-mode screening job is submitted before the pick, whatever the routing body says
    Given the broker runs moderation in async mode
    When "u-1" posts a chat completion for "qwen3-32b" with body `"roger": {"pref": "cheap"}`
    Then the off-path screener received the prompt before the routing pass ran

  Scenario: A routing body cannot select a station that skips moderation
    When "u-1" posts a chat completion for "qwen3-32b" with body `"roger": {"trust_min": "any", "self_hosted_only": true}, "provider": {"require_parameters": false}`
    Then the moderation screen ran exactly once

  Scenario: A routing 400 is returned BEFORE moderation so a malformed body costs no screener call
    Given the broker runs moderation in sync mode
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"sort": "cheapest"}`
    Then the response is 400
    And the moderation screen did not run

  Scenario: Rate limiting still precedes routing parsing
    Given "u-1" has exhausted the per-identity rate limit
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"foo": 1}`
    Then the response is 429
    And the error code is not "unknown_routing_key"

  # --- error envelope -------------------------------------------------------------------
  Scenario Outline: Every routing error is the envelope {"error":{"code","message"}} with the code stated
    Given <setup>
    When "u-1" posts a chat completion for "<model>" with body `<fragment>`
    Then the response is <status>
    And the response body is a JSON object with an "error" object holding string "code" and string "message"
    And the error code is "<code>"
    And the response header X-RogerAI-Cost is "0"
    And the response carries no X-RogerAI-Receipt header
    And the response carries no X-RogerAI-Provider header

    Examples:
      | setup                                                       | model      | fragment                                              | status | code                     |
      | nothing else                                                | qwen3-32b  | "provider": {"foo": 1}                                | 400    | unknown_routing_key      |
      | nothing else                                                | qwen3-32b  | "provider": {"sort": "x"}                             | 400    | invalid_routing_value    |
      | nothing else                                                | qwen3-32b  | "provider": {"sort": "price"}, "roger": {"pref": "fast"} | 400 | conflicting_routing_keys |
      | nothing else                                                | qwen3-32b  | "provider": {"max_price": {"image": 1}}               | 400    | unsupported_routing_key  |
      | nothing else                                                | qwen3-32b  | "roger": {"profile": "@profile/coding"}               | 400    | unknown_profile          |
      | nothing else                                                | qwen3-32b  | "provider": {"only": ["n-none"]}                      | 503    | no_match                 |
      | every station for "qwen3-32b" is in a 429 cooldown          | qwen3-32b  | "provider": {"sort": "price"}                         | 503    | band_cooling             |

  Scenario: The legacy "no node offers" message text is preserved alongside the new code
    When "u-1" posts a chat completion for "no-such-model" with body `"provider": {}`
    Then the response is 503
    And the error code is "no_match"
    And the error message starts with "no node offers no-such-model"

  Scenario: The grant flavour of the no-station message is preserved
    Given owner "o-1" owns node "n-a" and minted grant "rog-grant_1" for "qwen3-32b"
    And node "n-a" goes off air
    When the grant holder posts a chat completion for "qwen3-32b" with body `"roger": {"pref": "cheap"}`
    Then the response is 503
    And the error message is "no node of this grant's owner is serving right now"

  Scenario: The confidential flavour of the no-station message is preserved for the body form
    When "u-1" posts a chat completion for "qwen3-32b" with body `"roger": {"confidential": true}`
    Then the response is 503
    And the error code is "no_match"
    And the error message ends with "on a confidential node"

  Scenario: band_cooling carries Retry-After equal to the soonest cooldown expiry; no_match carries none
    Given every station for "qwen3-32b" is in a 429 cooldown, the soonest expiring in 9 seconds
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"sort": "price"}`
    Then the response is 503
    And the error code is "band_cooling"
    And the Retry-After header is "9"
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"only": ["n-none"]}`
    Then the response is 503
    And the error code is "no_match"
    And the response carries no Retry-After header

  Scenario: A constraint set that matches only cooling stations is band_cooling, not no_match
    Given node "n-a" is in a 429 cooldown for 20 seconds
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"only": ["n-a"]}`
    Then the response is 503
    And the error code is "band_cooling"
    And the Retry-After header is "20"

  Scenario: A constraint set with no on-air match at all is no_match even when unrelated stations are cooling
    Given node "n-a" is in a 429 cooldown for 20 seconds
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"only": ["n-none"]}`
    Then the response is 503
    And the error code is "no_match"
    And the response carries no Retry-After header

  Scenario: The context-window 400 still fires when the constraint set has a fitting station but the request is too large
    Given node "n-a" alone is on air for "qwen3-32b" with a declared context of 8192
    When "u-1" posts a chat completion for "qwen3-32b" with a 20000-token prompt and body `"provider": {"only": ["n-a"]}`
    Then the response is 400
    And the error message contains "exceeds the context window"

  Scenario: A 503 for a streaming request is a plain JSON error, not an SSE stream
    When "u-1" posts a STREAMING chat completion for "qwen3-32b" with body `"provider": {"only": ["n-none"]}`
    Then the response is 503
    And the response Content-Type is "application/json"
    And the error code is "no_match"

  Scenario: A 400 for a streaming request is a plain JSON error, not an SSE stream
    When "u-1" posts a STREAMING chat completion for "qwen3-32b" with body `"provider": {"sort": "x"}`
    Then the response is 400
    And the response Content-Type is "application/json"

  @slice0
  Scenario: Existing (non-routing) errors gain no code field they did not have and keep their status
    When "u-1" posts a chat completion with a body that is not a JSON object
    Then the response is 400
    And the response body is a JSON object with an "error" object holding string "message"

  # --- CORS: browser callers can read the new headers ---------------------------------
  Scenario: X-RogerAI-Model is exposed to allowlisted browser origins
    When the Playbox origin posts a chat completion for "qwen3-32b" with body `"provider": {"sort": "price"}`
    Then the response header Access-Control-Expose-Headers includes "X-RogerAI-Model"

  # --- adversarial -----------------------------------------------------------------------
  Scenario: A station cannot inject a provider object into its response to influence the broker
    Given node "n-a" answers every job with a body that also carries `"provider": {"order": ["n-a"]}, "roger": {"pref": "reliable"}`
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"order": ["n-a"]}`
    Then the response is 200
    And the next request from "u-1" for "qwen3-32b" with no routing body is routed by score, not pinned to "n-a"
    And the consumer response body carries the station's keys verbatim (pass-through) but the broker read none of them

  Scenario: A station cannot lower the consumer's cap by echoing a max_price in its receipt
    Given node "n-a" answers with a receipt carrying an extra field `"max_price": {"completion": 100}`
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"max_price": {"completion": 0.50}}`
    Then the settled price out is at most $0.50/1M

  Scenario: A routing body cannot name the broker's own admin identity or a reserved id as a node
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"only": ["admin", "anon", "g_reserved"]}`
    Then the response is 503
    And the error code is "no_match"

  Scenario: Node ids in provider lists are matched exactly, never as prefixes or globs
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"only": ["n-*"]}`
    Then the response is 503
    And the error code is "no_match"
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"ignore": ["n-"]}`
    Then the response is 200

  Scenario: A node id in provider.only is not an enumeration oracle for private stations
    Given a private band "band-1" with code "FREQ-1" whose only station is "n-p" on air for "qwen3-32b" at in $0 out $0
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"only": ["n-p"]}`
    Then the response is 503
    And the error code is "no_match"
    And the error message is byte-identical to the message for `"provider": {"only": ["n-does-not-exist"]}`

  Scenario: Routing keys cannot smuggle a model the station was not told about
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"order": ["n-a"]}, "roger": {"pref": "cheap"}`
    Then the station received top-level key "model" with value "qwen3-32b"
    And the receipt names model "qwen3-32b"

  Scenario: A 400 on the routing body leaks nothing about which nodes exist
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"only": ["n-a"], "order": ["n-secret"]}`
    Then the response is 400
    And the error message names "n-secret" only as the offending ORDER entry, and says nothing about whether it exists

  Scenario: Header parsing leniency is not widened by the body parser
    When "u-1" posts a chat completion for "qwen3-32b" with header X-Roger-Min-TPS "abc" and no routing body
    Then the response is 200
    And the routing pass ran with min-tps 0

  # --- multi-instance -------------------------------------------------------------------
  Scenario: The routing body is honored identically when the job crosses the bus to a peer instance
    Given the broker runs as two instances with the poller for "n-a" on the peer
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"order": ["n-a"], "allow_fallbacks": false}`
    Then the response is 200
    And the served node is "n-a"
    And the job that crossed the bus carried a body with no top-level key "provider"

  # --- telemetry -------------------------------------------------------------------------
  Scenario: The relay counts routing-body usage and routing 400s on /admin/live, without echoing values
    When "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"sort": "price"}`
    And "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"sort": "x"}`
    Then /admin/live reports routing_body_requests 1 and routing_body_rejects 1
    And /admin/live echoes no node id, price, or band code from either request
