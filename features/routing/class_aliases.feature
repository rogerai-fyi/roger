# CLASS ALIASES (slice 6, audit item #25, founder ruling 2026-10-02): `@class/<name>` names a
# deterministic, transparent group of models ("a coding model around 30B", "something small")
# that the broker expands into an ordinary models[] request. The deterministic part of "auto"
# routing without a black-box classifier.
#
# GROUND TRUTH (routing-slice6 at b1754466):
#   - models[] fallback exists (slice 1): effective list = [model] ++ models, de-dup on the bare
#     id, at most 5 distinct, 256-char ids, sugar per entry (routingreq.go effectiveModels).
#   - `@profile/` in `model` is refused with 400 unknown_profile (client-side profiles, contract
#     §9); there is no `@class/` handling, so `@class/x` today is a plain model id → 503 no_match.
#   - params_b (declared or estimated from the id, internal/detect/params.go) and the tools
#     verdict (toolcall.go) exist per offer (slice 2), so a class can filter on them.
#   - Blended cost (in × 3 + out) is defined in features/discovery/models_openrouter_fields.feature.
#
# RULES (contract draft B §14.B5):
#   - Built-in alias table, defined as data in one place (not code branches):
#       @class/coding-30b : chat models with params_b in [24, 40] and at least one tools-VERIFIED
#                           eligible offer
#       @class/small      : chat models with params_b <= 9
#       @class/large      : chat models with params_b >= 60
#     params_b = the model's declared value or its id estimate; a model with neither is never in
#     a size class.
#   - Expansion: take the on-air public chat models matching the class filter, order by each
#     model's cheapest coherent blended cost ascending (ties by bare id ascending), keep at most
#     5. The request is then EXACTLY a models[] request with that list: every models[] rule
#     applies (per-model plans, one hold over the priciest pair, move-on triggers, grant/band
#     intersections, X-RogerAI-Model names the real served model, never "@class/...").
#   - The expansion is computed per request from the same snapshot the routing pass uses, under
#     the caller's own visibility (a grant expands over its owner's stations, a band over its
#     band; private stations never widen a public expansion).
#   - Errors: unknown class name → 400 unknown_model_class; `@class/...` as `model` together with
#     a non-empty `models` → 400 conflicting_routing_keys; `@class/...` inside models[] → 400
#     invalid_routing_value (a class is a list, not an entry); sugar on a class
#     (`@class/small:floor`) applies the sort sugar request-wide (`:free` filters each expanded
#     model); a class that currently expands to nothing → 503 no_match naming the class.
#   - Transparency: /v1/models lists each non-empty class with rogerai.expands_to; the response
#     carries `X-RogerAI-Class: <name>` and the record on /generation lists the expanded models.
#   - Telemetry: /admin/live class_requests{<name>}.
#
# Enforced by: cmd/rogerai-broker/class_aliases_bdd_test.go

Feature: A model class expands to a transparent models[] list

  Background:
    Given a broker with an empty in-memory node registry
    And the fee rate is 30%
    And these chat models are on air with one station each:
      | model            | params_b | declared | tools verified | in   | out  |
      | qwen3-32b        | 32       | yes      | yes            | 0.10 | 0.30 |
      | qwen3-30b-a3b    | 30       | estimated| yes            | 0.05 | 0.20 |
      | gemma-3-27b      | 27       | yes      | no             | 0.04 | 0.10 |
      | llama-3.1-8b     | 8        | estimated| no             | 0.01 | 0.02 |
      | phi-4-mini       | 3.8      | yes      | no             | 0.01 | 0.01 |
      | llama-3.3-70b    | 70       | yes      | yes            | 0.20 | 0.20 |
      | mystery-model    |          | no       | yes            | 0.02 | 0.02 |
    And "u-1" is a funded consumer

  # --- expansion ---------------------------------------------------------------------

  Scenario: @class/coding-30b expands to tools-verified models between 24B and 40B, cheapest first
    When "u-1" relays for "@class/coding-30b"
    Then the effective models are ["qwen3-30b-a3b", "qwen3-32b"]
    And X-RogerAI-Class is "coding-30b"

  Scenario: @class/small expands to models of at most 9B
    When "u-1" relays for "@class/small"
    Then the effective models are ["phi-4-mini", "llama-3.1-8b"]

  Scenario: @class/large expands to models of at least 60B
    When "u-1" relays for "@class/large"
    Then the effective models are ["llama-3.3-70b"]

  Scenario: A model with no declared or estimable size is never in a size class
    When "u-1" relays for "@class/small"
    Then "mystery-model" is not among the effective models

  Scenario: An estimated size counts like a declared one
    When "u-1" relays for "@class/coding-30b"
    Then "qwen3-30b-a3b" is among the effective models

  Scenario Outline: Class boundaries are inclusive
    Given a chat model "<model>" with declared params_b <size> and verified tools is on air
    When "u-1" relays for "<class>"
    Then "<model>" <is> among the effective models

    Examples:
      | model   | size | class             | is     |
      | edge-24 | 24   | @class/coding-30b | is     |
      | edge-40 | 40   | @class/coding-30b | is     |
      | edge-41 | 41   | @class/coding-30b | is not |
      | edge-9  | 9    | @class/small      | is     |
      | edge-10 | 10   | @class/small      | is not |
      | edge-60 | 60   | @class/large      | is     |
      | edge-59 | 59   | @class/large      | is not |

  Scenario: Expansion keeps at most 5 models
    Given 8 chat models of 4B each are on air
    When "u-1" relays for "@class/small"
    Then the effective models has 5 entries
    And they are the 5 cheapest by blended cost

  Scenario: Equal blended cost is ordered by bare id
    Given chat models "zeta-3b" and "alpha-3b" of 3B are on air at the same prices
    When "u-1" relays for "@class/small"
    Then "alpha-3b" precedes "zeta-3b" in the effective models

  Scenario: The expansion is deterministic for an unchanged market
    When "u-1" relays 20 times for "@class/coding-30b"
    Then every request had the same effective models

  Scenario: A model that goes off air drops out of the next expansion
    Given "qwen3-30b-a3b" goes off air
    When "u-1" relays for "@class/coding-30b"
    Then the effective models are ["qwen3-32b"]

  # --- it is a models[] request ----------------------------------------------------------

  Scenario: The served response names the real model, never the class
    When "u-1" relays for "@class/coding-30b"
    Then X-RogerAI-Model is "qwen3-30b-a3b"
    And the receipt's served model is "qwen3-30b-a3b"

  Scenario: The station receives the real model id, never the class
    When "u-1" relays for "@class/coding-30b"
    Then the body the serving station received has "model" "qwen3-30b-a3b"

  Scenario: Fallback walks the expanded list
    Given the station for "qwen3-30b-a3b" answers the next request with an upstream 429
    When "u-1" relays for "@class/coding-30b"
    Then the response is 200 with X-RogerAI-Model "qwen3-32b"

  Scenario: One hold covers the priciest pair of the expanded plan
    When "u-1" relays for "@class/coding-30b" with max_tokens 1000
    Then exactly one hold was placed
    And it covers the priciest (model, station) pair of the expanded plan

  Scenario: Every routing constraint applies to the expanded models
    When "u-1" relays for "@class/coding-30b" with provider.max_price.completion 0.25
    Then the effective plan contains only "qwen3-30b-a3b"

  Scenario: :free on a class filters each expanded model to free offers
    Given "llama-3.1-8b" also has a free station
    When "u-1" relays for "@class/small:free"
    Then the plan contains only free offers

  Scenario: :floor on a class sorts the whole plan by price
    When "u-1" relays for "@class/small:floor"
    Then the plan follows price order across the expanded models

  Scenario: A grant expands only over its owner's stations
    Given a free grant "g1" owned by the operator of the "qwen3-32b" station only
    When the grant holder relays for "@class/coding-30b"
    Then the effective models are ["qwen3-32b"]

  Scenario: A private band never widens a public expansion
    Given a private band whose only station serves "secret-30b" (30B, tools verified)
    When "u-1" relays for "@class/coding-30b" with no band code
    Then "secret-30b" is not among the effective models

  Scenario: A band code expands within the band
    Given a private band "B1" with code "FREQ-1" whose station serves "band-30b" (30B, tools verified)
    When "u-1" relays for "@class/coding-30b" with X-Roger-Freq "FREQ-1"
    Then the effective models are ["band-30b"]

  # --- errors ---------------------------------------------------------------------------

  Scenario: An unknown class is a 400
    When "u-1" relays for "@class/turbo"
    Then the response is 400 with error code "unknown_model_class"
    And no station received anything
    And X-RogerAI-Cost is "0"

  Scenario: A class with an explicit models list is a 400
    When "u-1" relays for "@class/small" with models ["qwen3-32b"]
    Then the response is 400 with error code "conflicting_routing_keys"

  Scenario: A class inside models[] is a 400
    When "u-1" relays for "qwen3-32b" with models ["@class/small"]
    Then the response is 400 with error code "invalid_routing_value" naming "models"

  Scenario: A class that currently expands to nothing is a 503 naming the class
    Given no chat model of at least 60B is on air
    When "u-1" relays for "@class/large"
    Then the response is 503 with error code "no_match"
    And the message names "@class/large"

  Scenario Outline: Malformed class ids are refused
    When "u-1" relays for "<id>"
    Then the response is 400 with error code "<code>"

    Examples:
      | id              | code                |
      | @class/         | unknown_model_class |
      | @class/SMALL    | unknown_model_class |
      | @class/small/x  | unknown_model_class |

  Scenario: A station cannot register a model id starting with @class/
    When a station registers model "@class/small"
    Then the registration is rejected

  # --- transparency ---------------------------------------------------------------------

  Scenario: /generation lists the expanded models for a class request
    When "u-1" relays for "@class/coding-30b"
    Then the /generation record's models are ["qwen3-30b-a3b", "qwen3-32b"]
    And its model_requested is "@class/coding-30b"

  Scenario: A dry run of a class shows the expansion
    When "u-1" posts a chat completion for "@class/small" with roger.dry_run true
    Then the explain document's models are ["phi-4-mini", "llama-3.1-8b"]

  Scenario: /v1/models lists each non-empty class with its expansion
    When a consumer GETs /v1/models
    Then the "@class/coding-30b" entry's rogerai.expands_to is ["qwen3-30b-a3b", "qwen3-32b"]

  Scenario: Class usage is counted per class on /admin/live
    When "u-1" relays twice for "@class/small"
    Then /admin/live class_requests{small} rose by 2

  Scenario: There is no hidden classifier: the same request and market always give the same list
    When "u-1" relays for "@class/coding-30b" with two very different prompts
    Then both requests had the same effective models
