# VARIANT SUGAR - `model:free`, `model:floor`, `model:nitro`.
#
# PURPOSE: OpenRouter lets a caller fold the two most common routing intents into the model id
# itself (`:floor` = cheapest, `:nitro` = fastest, `:free` = free endpoints only), so a client
# that can set nothing but the model string (a dropdown, a guest operator's config line, a curl
# one-liner) still gets a say. This spec pins the SAME three suffixes here as pure sugar over the
# body carriers of features/routing/request_shape.feature: recognized only from a CLOSED set,
# applied right-to-left, stackable, and NEVER a new model id. Everything downstream (the station,
# the price lock, the receipt, X-RogerAI-Model) sees the bare id.
#
# The open network has a hazard OpenRouter does not: model ids here are what stations declare,
# and Ollama-style ids carry a colon tag (`llama3:8b`, `qwen3:latest`). A suffix is therefore
# sugar ONLY when it is exactly one of the three known words; any other `:tag` stays part of the
# id. An id that literally ends in `:free` cannot be registered (the suffix wins), which is the
# accepted cost.
#
# CONTRACT: features/routing/ROUTING-EXPRESSION-CONTRACT.md §4 (PROPOSED 2026-09-30), with §3
# for sugar on models[] entries and §5 for what sort means.
#
# GROUND TRUTH (origin/main 518c698b):
#   cmd/rogerai-broker/tunnel.go 3268  pickFor: `o.Model != model` -> skip (exact, case-sensitive
#                                       string equality; a colon is an ordinary character)
#   cmd/rogerai-broker/tunnel.go 1724-1728 the model is read verbatim from body.model
#   cmd/rogerai-broker/main.go 1055-1063 lockedPrice key = user|node|model (24h lock)
#   cmd/rogerai-broker/router.go 52-63 parsePref (cheap|fast|reliable|balanced) - the only
#                                       "sort-like" knob today; there is no strict sort
#   cmd/rogerai-broker/market.go 29-107 /discover exposes free_now per offer; ActivePrice()
#                                       resolves the time-of-use price (afree when 0/0 now)
#   internal/protocol/protocol.go 141  Normalize canonicalizes an offer but does NOT reject a
#                                       colon in Model; the register path accepts `llama3:8b`
#
# Enforced by: cmd/rogerai-broker/variant_sugar_bdd_test.go (to be written; REAL broker, REAL
# stations, REAL ledger - no mocks).

Feature: Variant sugar on a model id - :free, :floor, :nitro

  Background:
    Given a broker with an empty in-memory node registry
    And the fee rate is 10%
    And the consumer default out-price cap is $10/1M
    And node "n-cheap" is on air for "qwen3-32b" at in $0.10 out $0.30 per 1M with 15 tok/s, seen just now
    And node "n-mid"   is on air for "qwen3-32b" at in $0.20 out $0.60 per 1M with 40 tok/s, seen just now
    And node "n-fast"  is on air for "qwen3-32b" at in $0.50 out $2.00 per 1M with 90 tok/s, seen just now
    And node "n-free"  is on air for "qwen3-32b" at in $0 out $0 per 1M with 8 tok/s, seen just now
    And every node above has passed its canary and sits in Tier A
    And a logged-in consumer "u-1" with a $5.00 balance

  # --- each suffix alone ----------------------------------------------------------------
  Scenario: :free admits only offers priced 0/0 right now
    When "u-1" posts a chat completion for "qwen3-32b:free"
    Then the response is 200
    And the served node is "n-free"
    And the settled cost is $0

  Scenario: :free with no free offer on air is a 503 no_match, not a paid fallback
    Given node "n-free" goes off air
    When "u-1" posts a chat completion for "qwen3-32b:free"
    Then the response is 503
    And the error code is "no_match"
    And no hold was placed and no station was dispatched

  Scenario: :free on a model that only ever had paid offers is a 503 no_match
    Given node "n-paid" is on air for "paid-only-model" at in $1 out $3 per 1M, seen just now
    When "u-1" posts a chat completion for "paid-only-model:free"
    Then the response is 503
    And the error code is "no_match"

  Scenario: :free respects time-of-use pricing - a scheduled-free window admits, outside it does not
    Given node "n-tou" is on air for "tou-model" at in $1 out $3 per 1M with a scheduled free window 02:00-04:00 UTC
    When at 03:00 UTC "u-1" posts a chat completion for "tou-model:free"
    Then the response is 200
    And the served node is "n-tou"
    When at 12:00 UTC "u-1" posts a chat completion for "tou-model:free"
    Then the response is 503
    And the error code is "no_match"

  Scenario: :free is not fooled by a zero out price with a non-zero in price
    Given node "n-half" is on air for "half-model" at in $0.50 out $0 per 1M, seen just now
    When "u-1" posts a chat completion for "half-model:free"
    Then the response is 503
    And the error code is "no_match"

  # corrected 2026-10-01 (founder-approved): a 0/0 public offer places the 1e-6 floor hold
  # (approved features/money/holds.feature:16), never a priced one.
  Scenario: :free places only the floor hold
    When "u-1" posts a chat completion for "qwen3-32b:free"
    Then only the floor hold was placed

  Scenario: :floor is a strict price sort - cheapest out price first, then in price
    When "u-1" posts a chat completion for "qwen3-32b:floor"
    Then the response is 200
    And the served node is "n-free"
    And the request\'s routing log line names sort "price"
    And /admin/live routing_strict_sort increased by 1
    And 20 repeats of the request with distinct request ids all pick the same node (a strict sort, no power-of-two spread)

  Scenario: :floor among paid stations only (free excluded by a cap on the other side) picks the cheapest paid
    Given node "n-free" goes off air
    When "u-1" posts a chat completion for "qwen3-32b:floor"
    Then the served node is "n-cheap"

  Scenario: :floor tie on out price breaks on in price, then on score
    Given node "n-tie" is on air for "qwen3-32b" at in $0.05 out $0.30 per 1M with 20 tok/s, seen just now
    And node "n-free" goes off air
    When "u-1" posts a chat completion for "qwen3-32b:floor"
    Then the served node is "n-tie"

  Scenario: :nitro is a strict throughput sort - highest measured tok/s first
    When "u-1" posts a chat completion for "qwen3-32b:nitro"
    Then the response is 200
    And the served node is "n-fast"
    And the request\'s routing log line names sort "throughput"
    And /admin/live routing_strict_sort increased by 1
    And 20 repeats of the request with distinct request ids all pick the same node (a strict sort, no power-of-two spread)

  Scenario: :nitro puts unmeasured stations last, not first
    Given node "n-new" is on air for "qwen3-32b" at in $0.10 out $0.30 per 1M with no measured throughput, seen just now
    When "u-1" posts a chat completion for "qwen3-32b:nitro"
    Then the served node is "n-fast"
    And "n-new" was the last candidate in the ordered plan

  Scenario: :nitro still respects the consumer's out-price cap - the fastest station over the cap is not picked
    Given node "n-fast" goes off air
    And node "n-pricey" is on air for "qwen3-32b" at in $1 out $30 per 1M with 200 tok/s, seen just now
    When "u-1" posts a chat completion for "qwen3-32b:nitro"
    Then the served node is "n-mid"
    And "n-pricey" was never a candidate

  Scenario: :nitro with an explicit higher cap admits the fast pricey station
    Given node "n-pricey" is on air for "qwen3-32b" at in $1 out $30 per 1M with 200 tok/s, seen just now
    When "u-1" posts a chat completion for "qwen3-32b:nitro" with body `"provider": {"max_price": {"completion": 50}}`
    Then the served node is "n-pricey"

  Scenario: A sort variant only orders Tier A; Tier B is still used only when Tier A is empty
    Given node "n-fast" is in Tier B (failing canaries)
    When "u-1" posts a chat completion for "qwen3-32b:nitro"
    Then the served node is "n-mid"

  Scenario: A sort variant does not bypass a station's 429 cooldown
    Given node "n-fast" is in a 429 cooldown for 30 seconds
    When "u-1" posts a chat completion for "qwen3-32b:nitro"
    Then the served node is "n-mid"

  Scenario: A sort variant does not bypass the dead-probe exclusion
    Given node "n-fast" has failed the dead-probe streak
    When "u-1" posts a chat completion for "qwen3-32b:nitro"
    Then the served node is "n-mid"

  # --- stacking -------------------------------------------------------------------------
  Scenario: :free:nitro admits only free offers and orders them by throughput
    Given node "n-free2" is on air for "qwen3-32b" at in $0 out $0 per 1M with 30 tok/s, seen just now
    When "u-1" posts a chat completion for "qwen3-32b:free:nitro"
    Then the served node is "n-free2"

  Scenario: :nitro:free is the same request as :free:nitro (order of a filter and a sort does not matter)
    Given node "n-free2" is on air for "qwen3-32b" at in $0 out $0 per 1M with 30 tok/s, seen just now
    When "u-1" posts a chat completion for "qwen3-32b:nitro:free"
    Then the served node is "n-free2"

  Scenario: Two sort variants - the LAST one wins
    When "u-1" posts a chat completion for "qwen3-32b:floor:nitro"
    Then the request\'s routing log line names sort "throughput"
    And /admin/live routing_strict_sort increased by 1
    And the served node is "n-fast"

  Scenario: Two sort variants the other way - the last still wins
    When "u-1" posts a chat completion for "qwen3-32b:nitro:floor"
    Then the request\'s routing log line names sort "price"
    And /admin/live routing_strict_sort increased by 1
    And the served node is "n-free"

  Scenario: A repeated suffix is accepted and idempotent
    When "u-1" posts a chat completion for "qwen3-32b:free:free"
    Then the response is 200
    And the served node is "n-free"

  Scenario: All three stacked
    Given node "n-free2" is on air for "qwen3-32b" at in $0 out $0 per 1M with 30 tok/s, seen just now
    When "u-1" posts a chat completion for "qwen3-32b:free:floor:nitro"
    Then the request\'s routing log line names sort "throughput"
    And /admin/live routing_strict_sort increased by 1
    And the served node is "n-free2"

  # --- what is NOT sugar --------------------------------------------------------------
  Scenario Outline: A colon tag outside the closed set is part of the model id
    Given node "n-oll" is on air for "<id>" at in $0.10 out $0.30 per 1M, seen just now
    When "u-1" posts a chat completion for "<id>"
    Then the response is 200
    And the served node is "n-oll"
    And the response header X-RogerAI-Model is "<id>"
    And the station received top-level key "model" with value "<id>"

    Examples:
      | id                      |
      | llama3:8b               |
      | qwen3:latest            |
      | gemma3:27b-it-q8_0      |
      | mistral:7b-instruct     |
      | deepseek-r1:70b         |
      | qwen3-32b:fp16          |
      | qwen3-32b:q4_K_M        |
      | qwen3-32b:thinking      |
      | qwen3-32b:online        |
      | qwen3-32b:exacto        |
      | qwen3-32b:beta          |
      | qwen3-32b:extended      |

  Scenario: An Ollama-style id plus a real suffix keeps the tag and applies the sugar
    Given node "n-oll" is on air for "llama3:8b" at in $0 out $0 per 1M, seen just now
    And node "n-oll-paid" is on air for "llama3:8b" at in $0.10 out $0.30 per 1M, seen just now
    When "u-1" posts a chat completion for "llama3:8b:free"
    Then the response is 200
    And the served node is "n-oll"
    And the response header X-RogerAI-Model is "llama3:8b"

  Scenario: Suffix matching is case-sensitive - :FREE is part of the id, and no such model is on air
    When "u-1" posts a chat completion for "qwen3-32b:FREE"
    Then the response is 503
    And the error code is "no_match"
    And the error message starts with "no node offers qwen3-32b:FREE"

  # corrected 2026-10-01 (founder-approved): dropped the row that was literally "qwen3-32b:free" (table cells are trimmed, so it was real sugar per §4, not a near-miss).
  Scenario Outline: Other casings and near-misses are part of the id too
    When "u-1" posts a chat completion for "<id>"
    Then the response is 503
    And the error code is "no_match"
    And the error message starts with "no node offers <id>"

    Examples:
      | id                  |
      | qwen3-32b:Free      |
      | qwen3-32b:Floor     |
      | qwen3-32b:NITRO     |
      | qwen3-32b:frees     |
      | qwen3-32b:nitro2    |
      | qwen3-32b:floor-    |
      | qwen3-32b: free     |

  Scenario: A trailing colon with nothing after it is a 400 invalid_routing_value naming model
    When "u-1" posts a chat completion for "qwen3-32b:"
    Then the response is 400
    And the error code is "invalid_routing_value"
    And the error message names "model"

  Scenario: A double colon (empty middle segment) is a 400 invalid_routing_value
    When "u-1" posts a chat completion for "qwen3-32b::free"
    Then the response is 400
    And the error code is "invalid_routing_value"

  Scenario: A leading colon is a 400 invalid_routing_value
    When "u-1" posts a chat completion for ":free"
    Then the response is 400
    And the error code is "invalid_routing_value"

  Scenario: A model that is ONLY a suffix word is a plain model id, not sugar
    Given node "n-odd" is on air for "free" at in $0.10 out $0.30 per 1M, seen just now
    When "u-1" posts a chat completion for "free"
    Then the response is 200
    And the served node is "n-odd"

  Scenario: Whitespace around a suffix is not trimmed - it is part of the id
    When "u-1" posts a chat completion for "qwen3-32b :free"
    Then the response is 503
    And the error code is "no_match"

  Scenario: A station cannot register an id that literally ends in a sugar suffix
    When a station registers an offer for "qwen3-32b:free"
    Then the registration is rejected with a message naming the reserved suffix
    When a station registers an offer for "qwen3-32b:nitro"
    Then the registration is rejected with a message naming the reserved suffix
    When a station registers an offer for "qwen3-32b:floor"
    Then the registration is rejected with a message naming the reserved suffix

  Scenario: A station CAN register an Ollama-style id whose tag is not a sugar word
    When a station registers an offer for "llama3:8b"
    Then the registration is accepted

  Scenario: A persisted offer whose id ends in a sugar suffix (pre-dating the rule) is dropped on re-hydrate, never served
    Given the store holds a persisted node offering "old-model:free"
    When the broker re-hydrates its registry
    Then no offer for "old-model:free" is on air
    And a request for "old-model:free" is a 503 no_match

  # --- sugar vs explicit provider.sort ---------------------------------------------------
  Scenario: :floor with an agreeing provider.sort "price" is accepted
    When "u-1" posts a chat completion for "qwen3-32b:floor" with body `"provider": {"sort": "price"}`
    Then the response is 200
    And the request\'s routing log line names sort "price"
    And /admin/live routing_strict_sort increased by 1

  Scenario: :nitro with an agreeing provider.sort "throughput" is accepted
    When "u-1" posts a chat completion for "qwen3-32b:nitro" with body `"provider": {"sort": "throughput"}`
    Then the response is 200
    And the request\'s routing log line names sort "throughput"
    And /admin/live routing_strict_sort increased by 1

  Scenario: :floor with a conflicting provider.sort "throughput" is a 400 conflicting_routing_keys
    When "u-1" posts a chat completion for "qwen3-32b:floor" with body `"provider": {"sort": "throughput"}`
    Then the response is 400
    And the error code is "conflicting_routing_keys"
    And the error message names "model"
    And the error message names "provider.sort"

  Scenario: :nitro with a conflicting provider.sort "latency" is a 400 conflicting_routing_keys
    When "u-1" posts a chat completion for "qwen3-32b:nitro" with body `"provider": {"sort": "latency"}`
    Then the response is 400
    And the error code is "conflicting_routing_keys"

  Scenario: A sort suffix with roger.pref is a 400 conflicting_routing_keys (sugar IS a sort)
    When "u-1" posts a chat completion for "qwen3-32b:floor" with body `"roger": {"pref": "cheap"}`
    Then the response is 400
    And the error code is "conflicting_routing_keys"

  Scenario: A sort suffix with the legacy X-Roger-Pref header is NOT a conflict - the header is ignored
    When "u-1" posts a chat completion for "qwen3-32b:floor" with header X-Roger-Pref "fast"
    Then the response is 200
    And the request\'s routing log line names sort "price"
    And /admin/live routing_strict_sort increased by 1

  Scenario: :free with any provider.sort is fine (a filter and a sort do not conflict)
    When "u-1" posts a chat completion for "qwen3-32b:free" with body `"provider": {"sort": "latency"}`
    Then the response is 200
    And the served node is "n-free"

  Scenario: :free with roger.pref is fine
    When "u-1" posts a chat completion for "qwen3-32b:free" with body `"roger": {"pref": "reliable"}`
    Then the response is 200

  Scenario: :floor:nitro (last wins = throughput) with provider.sort "price" is a conflict against the RESOLVED sort
    When "u-1" posts a chat completion for "qwen3-32b:floor:nitro" with body `"provider": {"sort": "price"}`
    Then the response is 400
    And the error code is "conflicting_routing_keys"

  # --- sugar on models[] entries -------------------------------------------------------
  Scenario: A :free suffix on a models[] entry filters that entry only
    Given node "n-l" is on air for "llama-3.3-70b" at in $0.30 out $0.90 per 1M, seen just now
    And node "n-free" goes off air
    When "u-1" posts a chat completion for "qwen3-32b:free" with body `"models": ["llama-3.3-70b"]`
    Then the response is 200
    And the response header X-RogerAI-Model is "llama-3.3-70b"
    And the served node is "n-l"

  Scenario: A :free suffix on a later entry does not make the primary free-only
    Given node "n-l" is on air for "llama-3.3-70b" at in $0.30 out $0.90 per 1M, seen just now
    When "u-1" posts a chat completion for "qwen3-32b" with body `"models": ["llama-3.3-70b:free"], "provider": {"ignore": ["n-free"]}`
    Then the response is 200
    And the response header X-RogerAI-Model is "qwen3-32b"
    And the served node is one of "n-cheap", "n-mid", "n-fast"

  Scenario: A sort suffix on ANY entry applies to the whole request
    Given node "n-l" is on air for "llama-3.3-70b" at in $0.30 out $0.90 per 1M, seen just now
    When "u-1" posts a chat completion for "qwen3-32b" with body `"models": ["llama-3.3-70b:nitro"]`
    Then the request\'s routing log line names sort "throughput"
    And /admin/live routing_strict_sort increased by 1
    And the served node is "n-fast"

  Scenario: Sort suffixes across entries - the last entry's sort wins
    Given node "n-l" is on air for "llama-3.3-70b" at in $0.30 out $0.90 per 1M, seen just now
    When "u-1" posts a chat completion for "qwen3-32b:nitro" with body `"models": ["llama-3.3-70b:floor"]`
    Then the request\'s routing log line names sort "price"
    And /admin/live routing_strict_sort increased by 1
    And the served node is "n-free"

  Scenario: Suffixed entries de-duplicate against their bare form - the bare id is what counts
    When "u-1" posts a chat completion for "qwen3-32b" with body `"models": ["qwen3-32b:free", "qwen3-32b:floor", "qwen3-32b:nitro", "qwen3-32b", "qwen3-32b:free:nitro", "qwen3-32b:free:floor"]`
    Then the response is not 400

  Scenario: The 5-entry limit counts bare ids after de-dup, so six suffixed spellings of one model are one entry
    When "u-1" posts a chat completion for "qwen3-32b:free" with body `"models": ["qwen3-32b:floor", "qwen3-32b:nitro", "qwen3-32b", "qwen3-32b:free:nitro", "qwen3-32b:free:floor"]`
    Then the response is 200

  Scenario: An entry whose free-only filter finds no station is skipped silently, the next entry serves
    Given node "n-l" is on air for "llama-3.3-70b" at in $0.30 out $0.90 per 1M, seen just now
    And node "n-free" goes off air
    When "u-1" posts a chat completion for "qwen3-32b:free" with body `"models": ["llama-3.3-70b"]`
    Then the response is 200
    And the response header X-RogerAI-Model is "llama-3.3-70b"
    And no failed attempt was recorded for "qwen3-32b"

  Scenario: Every entry free-only with no free stations at all is a 503 no_match
    Given node "n-l" is on air for "llama-3.3-70b" at in $0.30 out $0.90 per 1M, seen just now
    And node "n-free" goes off air
    When "u-1" posts a chat completion for "qwen3-32b:free" with body `"models": ["llama-3.3-70b:free"]`
    Then the response is 503
    And the error code is "no_match"

  Scenario: A malformed suffix on a later entry is a 400 for the whole request
    When "u-1" posts a chat completion for "qwen3-32b" with body `"models": ["llama-3.3-70b:"]`
    Then the response is 400
    And the error code is "invalid_routing_value"
    And the error message names "models"

  # --- the bare id everywhere downstream -----------------------------------------------
  Scenario: The station receives the bare id, never the suffix
    When "u-1" posts a chat completion for "qwen3-32b:free"
    Then the station received top-level key "model" with value "qwen3-32b"

  Scenario: The station receives the bare id with a stacked suffix too
    When "u-1" posts a chat completion for "qwen3-32b:free:nitro"
    Then the station received top-level key "model" with value "qwen3-32b"

  Scenario: X-RogerAI-Model carries the bare id
    When "u-1" posts a chat completion for "qwen3-32b:floor"
    Then the response header X-RogerAI-Model is "qwen3-32b"

  Scenario: X-RogerAI-Model carries the bare id on the streaming path
    When "u-1" posts a STREAMING chat completion for "qwen3-32b:floor"
    Then the response header X-RogerAI-Model is "qwen3-32b"
    And the final usage chunk names model "qwen3-32b"

  Scenario: The receipt names the bare id
    When "u-1" posts a chat completion for "qwen3-32b:nitro"
    Then the receipt names model "qwen3-32b"
    And the receipt's broker signature verifies over the bare id

  Scenario: The price lock is keyed on the bare id - a suffixed request shares the lock with the bare request
    When "u-1" posts a chat completion for "qwen3-32b" and is served by "n-mid" at out $0.60/1M
    And the owner of "n-mid" raises the out price to $5/1M
    And "u-1" posts a chat completion for "qwen3-32b:nitro" with body `"provider": {"only": ["n-mid"]}`
    Then the settled price out is $0.60/1M
    And the response header X-RogerAI-Price reports out 0.60

  Scenario: A suffixed request opens a lock the later bare request inherits
    When "u-1" posts a chat completion for "qwen3-32b:floor" and is served by "n-cheap" at out $0.30/1M
    And the owner of "n-cheap" raises the out price to $5/1M
    And "u-1" posts a chat completion for "qwen3-32b" with body `"provider": {"only": ["n-cheap"]}`
    Then the settled price out is $0.30/1M

  Scenario: The lineage receipt chain records the bare id, so a suffixed and a bare request chain together
    When "u-1" posts a chat completion for "qwen3-32b:nitro"
    And "u-1" posts a chat completion for "qwen3-32b"
    Then the station's receipt chain has two consecutive entries for model "qwen3-32b"

  Scenario: The moderation record names the bare id
    When "u-1" posts a chat completion for "qwen3-32b:free"
    Then the screening job was submitted with model "qwen3-32b"

  # corrected 2026-10-01 (founder-approved): /console shows a GitHub-linked account the OWNER
  # view (its stations' traffic), so the consumer here is one that gets the CONSUMER view.
  Scenario: /console lineage and /usage show the bare id
    Given "u-1" is signed in with an account that gets the consumer view of /console
    When "u-1" posts a chat completion for "qwen3-32b:floor"
    Then /console for "u-1" lists the request under model "qwen3-32b"

  Scenario: The context-window 400 message names the bare id
    Given every station for "qwen3-32b" declares a context of 8192
    When "u-1" posts a chat completion for "qwen3-32b:floor" with a 20000-token prompt
    Then the response is 400
    And the error message contains "advertised on qwen3-32b right now"

  Scenario: The no_match message names the bare id, not the suffixed spelling
    Given node "n-free" goes off air
    When "u-1" posts a chat completion for "qwen3-32b:free"
    Then the error message starts with "no node offers qwen3-32b"

  # --- sugar and the money rules ----------------------------------------------------------
  Scenario: :floor never routes below the operator's price floor rules (a negative price is not "cheapest")
    Given the store holds a persisted node offering "qwen3-32b" at out -$1/1M
    When the broker re-hydrates its registry
    And "u-1" posts a chat completion for "qwen3-32b:floor"
    Then the served node is "n-free"

  Scenario: :nitro's failover plan is ordered by throughput too, and the hold covers the priciest station in it
    Given node "n-fast" answers 429
    When "u-1" posts a chat completion for "qwen3-32b:nitro" with max_tokens 1000
    Then the ordered plan is "n-fast", "n-mid", "n-cheap"
    And the hold placed covers "n-fast"'s price
    And the served node is "n-mid"
    And the settled price is "n-mid"'s

  Scenario: :free's failover plan contains only free stations
    Given node "n-free2" is on air for "qwen3-32b" at in $0 out $0 per 1M with 30 tok/s, seen just now
    And node "n-free" answers 429
    When "u-1" posts a chat completion for "qwen3-32b:free"
    Then the served node is "n-free2"
    And the failover plan does not contain "n-cheap"

  Scenario: :free for an anonymous caller is the same as an anonymous request (free-only was already the rule)
    When an anonymous caller posts a chat completion for "qwen3-32b:free"
    Then the response is 200
    And the served node is "n-free"

  Scenario: :nitro for an anonymous caller still cannot reach a paid station
    When an anonymous caller posts a chat completion for "qwen3-32b:nitro"
    Then the response is 200
    And the served node is "n-free"

  Scenario: A grant-funded :floor request stays inside the grant's fleet
    Given owner "o-1" owns node "n-mid" and minted grant "rog-grant_1" for "qwen3-32b"
    When the grant holder posts a chat completion for "qwen3-32b:floor"
    Then the served node is "n-mid"

  Scenario: A private-band :nitro request stays inside the band
    Given a private band "band-1" with code "FREQ-1" whose only station is "n-p" on air for "qwen3-32b" at in $0 out $0 with 5 tok/s
    When "u-1" posts a chat completion for "qwen3-32b:nitro" with body `"roger": {"freq": "FREQ-1"}`
    Then the served node is "n-p"

  Scenario: :free on a self-owned paid station is admitted - §4 defines :free as "costs THIS CALLER nothing right now", and self-use is $0
    Given "u-1" owns node "n-mid"
    And node "n-free" goes off air
    When "u-1" posts a chat completion for "qwen3-32b:free"
    Then the response is 200
    And the served node is "n-mid"
    And the settled cost is $0

  # --- sugar and the edge/Tower bridge --------------------------------------------------
  Scenario: :free never rides the bridge to a billed Tower
    Given an approved Tower "tw-1" serves "qwen3-32b" at out $0.50/1M through the edge bridge
    When "u-1" posts a chat completion for "qwen3-32b:free" 100 times
    Then every request was served by "n-free"
    And the Tower received no job

  @part-c

  Scenario: :nitro's ordering applies on the bridge path - the Tower row is one more candidate, ranked by its measured tok/s
    # A sort (and the sugar that means one) replaces the fan-out coin with the ranking (§5):
    # the Tower wins here because 300 tok/s beats every direct station, not because a coin
    # chose the edge.
    Given an approved Tower "tw-1" serves "qwen3-32b" at 300 tok/s through the edge bridge
    When "u-1" posts a chat completion for "qwen3-32b:nitro" 20 times with distinct request ids
    Then the Tower received every job
    And the Tower received top-level key "model" with value "qwen3-32b"
    And /admin/live routing_strict_sort increased by 20
    And /admin/live edge_coin_flips did not change

  Scenario: :nitro on the bridge path loses to a faster direct station on the same ranking
    Given an approved Tower "tw-1" serves "qwen3-32b" at 300 tok/s through the edge bridge
    And node "n-fast" measures 400 tok/s
    When "u-1" posts a chat completion for "qwen3-32b:nitro" 20 times with distinct request ids
    Then every request was served by "n-fast"
    And the Tower received no job

  # --- sugar on profile references ---------------------------------------------------
  Scenario: A suffix on an @profile reference is a 400 - profiles carry their own sort
    When "u-1" posts a chat completion for "@profile/coding:nitro"
    Then the response is 400
    And the error code is "invalid_routing_value"
    And the error message names "model"

  Scenario: A bare @profile reference reaching the broker unresolved is a 400 unknown_profile (client-side resolution only)
    When "u-1" posts a chat completion for "@profile/coding"
    Then the response is 400
    And the error code is "unknown_profile"

  Scenario: An @profile reference inside models[] is also rejected at the broker
    When "u-1" posts a chat completion for "qwen3-32b" with body `"models": ["@profile/coding"]`
    Then the response is 400
    And the error code is "unknown_profile"

  # --- discovery and clients ----------------------------------------------------------
  Scenario: /discover and /market never list a suffixed model id
    When the public feed is fetched
    Then no offer's model ends in ":free", ":floor" or ":nitro"

  @proxy
  Scenario: The local proxy passes a suffixed model through untouched when the band's model matches the bare id
    Given a local proxy tuned to "qwen3-32b"
    When a guest operator posts a chat completion for "qwen3-32b:floor" to the proxy
    Then the broker received model "qwen3-32b:floor"

  @proxy
  Scenario: The local proxy's model rewrite keeps a suffix the guest put on a FOREIGN id
    Given a local proxy tuned to "qwen3-32b"
    When a guest operator posts a chat completion for "gpt-4o:floor" to the proxy
    Then the broker received model "qwen3-32b:floor"

  @proxy
  Scenario: The local proxy's /v1/models does not advertise suffixed ids
    Given a local proxy tuned to "qwen3-32b"
    When a guest operator lists /v1/models on the proxy
    Then the list is exactly "qwen3-32b"

  # --- telemetry ------------------------------------------------------------------------
  Scenario: /admin/live counts sugar usage per suffix without echoing model ids
    When "u-1" posts a chat completion for "qwen3-32b:free"
    And "u-1" posts a chat completion for "qwen3-32b:nitro"
    And "u-1" posts a chat completion for "qwen3-32b:floor:nitro"
    Then /admin/live reports variant_free 1, variant_floor 1, variant_nitro 2
