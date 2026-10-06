# ROUTING EXPRESSION - the local proxy carries the body object (CONTRACT §1, §9, §10 and the
# defect-8 fix from features/routing/regression_pins.feature).
#
# PURPOSE: the local OpenAI-compatible endpoint (`roger use`, the TUI booth, guest operators)
# passes a caller's `models` / `provider` / `roger` through untouched, folds the owner's own
# limits in as DEFAULTS the caller's body overrides (the owner's price caps remain the ceiling),
# stops overwriting `model` when the caller is being explicit, and speaks headers only to a
# broker that does not yet accept the body.
#
# GROUND TRUTH at origin/main 518c698b (internal/client/client.go):
#   - rewriteModel (668-690) replaces the top-level `model` with ProxyOptions.Model whenever
#     Model != "" (every guest session; `roger use` legacy mode passes ""). Every other field's
#     raw JSON survives. Non-object body -> ok=false -> 400 before any relay
#     (features/proxy/model_rewrite.feature).
#   - relayWithFailover (811-920) injects X-Roger-Confidential / -Min-TPS / -Max-Price /
#     -Max-Price-Out (ALWAYS, via effectiveMaxOut) / -Freq / -Pref / -Node (its own failover
#     pin) / -Exclude-Nodes (union of the failover set and ProxyOptions.ExcludeNodes). Criteria
#     (Model, Confidential, MinTPS, MaxPriceIn, MaxPriceOut, Pref) drive the /discover
#     re-pick (failover.go:104-176).
#   - retryable (failover.go:79-85): transport errors and >= 500 only; a 429 is returned to the
#     caller. After a failover the proxy PINS the alternative (X-Roger-Node), which the broker
#     treats as "no station failover" (tunnel.go:1964) - defect 8.
#   - Budget (client.go:757-784, features/proxy/budget.feature): 402 budget_exceeded before
#     dispatch; streamed cost read from the `: rogerai-cost=` comment.
#   - /v1/models lists the one tuned model (646-658, features/proxy/models.feature).
#
# APPROVED SPECS KEPT TRUE: model_rewrite.feature ("any incoming model name relays as the band's
# model") continues to hold for a body WITHOUT a routing carrier - the guest that ships "gpt-4o"
# still lands on the band. The new rule only applies when the body carries `models`, `provider`,
# `roger`, or a `@profile/` reference: then the caller is being explicit and `model` is theirs.
# FLAG for the reviewer: model_rewrite.feature's Scenario Outline has no row with a routing
# carrier, so no approved row is contradicted; this file adds the carrier rows.
#
# PROPOSED (marked): broker capability negotiation (body first, header fallback on 400
# unknown_routing_key, remembered per session).
#
# Enforced by: internal/client/routing_passthrough_bdd_test.go (future; a real httptest broker
# that records what it received, the real ProxyHandlerLive, no mocks).

Feature: The local proxy relays the routing body object and folds the owner's limits in as defaults

  Background:
    Given a tuned band whose model is "qwen3-32b-fp8"
    And the local proxy is bound to that band with session key "sk-test-0123"
    And the proxy owner tuned with --max-out 2 --min-tps 10
    And the broker accepts the routing body object

  # --- passthrough ----------------------------------------------------------------------

  # corrected 2026-10-02 (founder ruling): guest may only tighten - the models[] row names only
  # the tuned band's model; a list reaching other models is refused locally (scenarios below)
  Scenario Outline: A routing carrier in the guest body reaches the broker byte-for-byte
    When a chat request arrives with <carrier>
    Then the broker receives <carrier> unchanged

    Examples:
      | carrier                                                     |
      | "models": ["qwen3-32b-fp8"]                                 |
      | "provider": {"only": ["n1"], "sort": "price"}               |
      | "provider": {"order": ["n1","n2"], "allow_fallbacks": false} |
      | "provider": {"quantizations": ["Q8_0"]}                     |
      | "provider": {"max_price": {"prompt": 0.1, "completion": 1}} |
      | "roger": {"require": ["tools"], "params_b": [7, 70]}        |
      | "roger": {"pref": "fast", "region": ["eu"]}                 |
      | "roger": {"self_hosted_only": true, "min_ctx": 32768}       |

  Scenario: Unknown keys inside a carrier are passed through for the broker to refuse
    When a chat request arrives with "roger": {"prefer": "fast"}
    Then the broker receives roger.prefer = "fast"
    And the broker's 400 unknown_routing_key is returned to the guest OpenAI-shaped
    And no header fallback is attempted for a 400 whose message names a key other than the carrier itself

  Scenario: The proxy does not validate the carrier's semantics, the broker does
    When a chat request arrives with "roger": {"params_b": [70, 7]}
    Then the broker receives it unchanged
    And the broker's 400 invalid_routing_value is returned to the guest OpenAI-shaped

  Scenario: Every non-routing field still survives untouched next to a carrier
    When a chat request arrives with tools, tool_choice, response_format, temperature 0.2, stream true, and "roger": {"pref": "fast"}
    Then the broker receives each of those fields byte-identical

  Scenario: A carrier in a streaming request behaves identically
    When a streaming chat request arrives with "provider": {"sort": "latency"}
    Then the broker receives provider.sort = "latency" and stream = true

  # --- model rewrite: a guest may name only the tuned model ---------------------------------

  # corrected 2026-10-02 (founder ruling): guest may only tighten - was "A body with a routing
  # carrier keeps its own model"; a guest naming another model could reach models the owner never
  # tuned, billed to the owner. A bare foreign id with NO carrier is still rewritten to the band
  # model (model_rewrite.feature, unchanged).
  @slice0
  Scenario Outline: A body with a routing carrier cannot switch to another model
    When a chat request arrives with model "<incoming>" and <carrier>
    Then the guest receives an OpenAI-shaped 400 "model <incoming> is outside this session's band"
    And nothing reaches the broker

    Examples:
      | incoming        | carrier                              |
      | llama-3.3-70b   | "models": ["qwen3-32b-fp8"]          |
      | qwen3-30b-a3b   | "roger": {"pref": "cheap"}           |
      | llama-3.3-70b   | "provider": {}                       |
      | gpt-4o:floor    | "provider": {"sort": "price"}        |

  # added 2026-10-02 (founder ruling): naming exactly the tuned model (sugar stripped) keeps it
  @slice0
  Scenario Outline: A body with a routing carrier that names the tuned model keeps it
    When a chat request arrives with model "<incoming>" and <carrier>
    Then the guest's response carries no error
    And the broker receives model "<incoming>"

    Examples:
      | incoming             | carrier                       |
      | qwen3-32b-fp8        | "provider": {"sort": "price"} |
      | qwen3-32b-fp8:free   | "roger": {"pref": "cheap"}    |

  # added 2026-10-02 (bug fix): hermes/opencode send roger/<model>, aider openai/<model>; the
  # provider prefix their materialized config writes is not part of the model id
  @slice0
  Scenario Outline: A guest's provider-prefixed tuned model is accepted and reaches the broker unprefixed
    When a chat request arrives with model "<incoming>" and <carrier>
    Then the guest's response carries no error
    And the broker receives model "<sent>"

    Examples:
      | incoming                  | carrier                       | sent                |
      | roger/qwen3-32b-fp8       | "roger": {"pref": "cheap"}    | qwen3-32b-fp8       |
      | openai/qwen3-32b-fp8      | "provider": {"sort": "price"} | qwen3-32b-fp8       |
      | openai/qwen3-32b-fp8:free | "roger": {"pref": "cheap"}    | qwen3-32b-fp8:free  |

  # added 2026-10-02 (bug fix): the same prefix inside models[] is stripped before the broker
  @slice0
  Scenario: A guest's provider-prefixed models[] entries reach the broker unprefixed
    When a chat request arrives with model "roger/qwen3-32b-fp8" and "models": ["openai/qwen3-32b-fp8:floor"]
    Then the broker receives model "qwen3-32b-fp8" and models ["qwen3-32b-fp8:floor"]

  # added 2026-10-02 (bug fix): only the known guest prefixes are stripped
  @slice0
  Scenario: An unknown provider prefix is part of the model id and is refused with a carrier
    When a chat request arrives with model "vendor/qwen3-32b-fp8" and "roger": {"pref": "cheap"}
    Then the guest receives an OpenAI-shaped 400 "model vendor/qwen3-32b-fp8 is outside this session's band"
    And nothing reaches the broker

  Scenario: A body with a carrier and NO model gets the band model as the primary
    # corrected 2026-10-02 (founder ruling): guest may only tighten - the list names the band model
    When a chat request arrives with no model field and "models": ["qwen3-32b-fp8:floor"]
    Then the broker receives model "qwen3-32b-fp8" and models ["qwen3-32b-fp8:floor"]

  Scenario: A @profile/ whose profile names no model gets the band model as the primary (no local 400)
    Given profile "quiet" sets only "roger": {"pref": "reliable"}
    When a chat request arrives with model "@profile/quiet"
    Then the broker receives model "qwen3-32b-fp8" and roger.pref "reliable"
    And the guest's response carries no error

  Scenario: A body with a carrier and an empty model string gets the band model as the primary
    When a chat request arrives with model "" and "roger": {"pref": "fast"}
    Then the broker receives model "qwen3-32b-fp8"

  Scenario: A body with a @profile/ model is resolved, not rewritten
    Given profile "coding" sets models = ["qwen3-32b-fp8", "llama-3.3-70b"]
    When a chat request arrives with model "@profile/coding"
    Then the broker receives model "qwen3-32b-fp8" and models ["llama-3.3-70b"]

  # regression 2026-10-05: audit finding, contract §3 + §9 (a profile's first model survives the band rewrite)
  Scenario: A profile whose first model is not the band keeps it as the first fallback
    Given profile "coding" sets models = ["llama-3.3-70b", "qwen3-32b-fp8"]
    When a chat request arrives with model "@profile/coding"
    Then the broker receives model "qwen3-32b-fp8" and models ["llama-3.3-70b", "qwen3-32b-fp8"]

  # regression 2026-10-05: audit finding, contract §9 (a profile's :free on the band survives the rewrite)
  Scenario: A profile naming the band's free variant keeps asking for free
    Given profile "freebie" sets model = "qwen3-32b-fp8:free"
    When a chat request arrives with model "@profile/freebie"
    Then the broker receives model "qwen3-32b-fp8:free"

  Scenario: A body with no carrier is rewritten to the band model exactly as approved
    When a chat request arrives with model "gpt-4o" and no routing carrier
    Then the broker receives model "qwen3-32b-fp8"

  # corrected 2026-10-04 (founder-approved): a guest may only tighten - a carrier never lets it switch model
  Scenario: An empty carrier object still counts as explicit, so a foreign model is refused locally
    When a chat request arrives with model "gpt-4o" and "roger": {}
    Then the guest receives a local 400 with error.code "routing_outside_session"
    And nothing reaches the broker

  # corrected 2026-10-04 (founder-approved): a guest may only tighten - the tuned model with a malformed carrier goes to the broker for its 400
  Scenario: A carrier of the wrong type is forwarded for the broker's 400, not rewritten around
    When a chat request arrives with model "qwen3-32b-fp8" and "provider": "openai"
    Then the broker receives model "qwen3-32b-fp8" and provider "openai"
    And the broker's 400 is returned to the guest

  # added 2026-10-04 (founder-approved): the same malformed carrier on a foreign model is refused locally first
  Scenario: A carrier of the wrong type on a foreign model is refused locally
    When a chat request arrives with model "gpt-4o" and "provider": "openai"
    Then the guest receives a local 400 with error.code "routing_outside_session"
    And nothing reaches the broker

  # corrected 2026-10-04 (founder-approved): a guest may only tighten - the model the broker sees is the tuned model
  Scenario: Failover re-discovery uses the model the broker will see
    When a chat request arrives with model "roger/qwen3-32b-fp8" and "roger": {"pref": "fast"}
    And the first relay attempt fails with a transport error
    Then the /discover re-pick matches "qwen3-32b-fp8"

  Scenario: Failover re-discovery with models[] matches the primary
    # corrected 2026-10-02 (founder ruling): guest may only tighten - the list names the band model
    When a chat request arrives with "models": ["qwen3-32b-fp8:nitro"] and model "qwen3-32b-fp8"
    And the first relay attempt fails with a transport error
    Then the /discover re-pick matches "qwen3-32b-fp8"

  # --- the owner's limits are defaults; the owner's caps are the ceiling ----------------

  @slice0
  Scenario: The owner's limits are added when the guest body has none
    When a chat request arrives with no routing carrier
    Then the broker receives provider.max_price.completion = 2 and roger.min_tps = 10

  @slice0
  Scenario: The guest may tighten a price cap below the owner's
    When a chat request arrives with "provider": {"max_price": {"completion": 1}}
    Then the broker receives provider.max_price.completion = 1

  @slice0
  Scenario: The guest cannot raise a price cap above the owner's (stricter wins, silently)
    When a chat request arrives with "provider": {"max_price": {"completion": 50}}
    Then the broker receives provider.max_price.completion = 2
    And the guest's response carries no error
    And one proxy log line says "guest max_price.completion 50 clamped to owner cap 2"

  @slice0
  Scenario: The guest's max_price.prompt is clamped to the owner's --max-in when set
    Given the proxy owner also tuned with --max-in 0.5
    When a chat request arrives with "provider": {"max_price": {"prompt": 3}}
    Then the broker receives provider.max_price.prompt = 0.5

  @slice0
  Scenario: Without an owner --max-in the guest's prompt cap passes as given
    When a chat request arrives with "provider": {"max_price": {"prompt": 3}}
    Then the broker receives provider.max_price.prompt = 3

  Scenario: The guest's max_price.request is clamped to the owner's --max-cost when set
    Given the proxy owner also tuned with --max-cost 0.02
    When a chat request arrives with "provider": {"max_price": {"request": 1}}
    Then the broker receives provider.max_price.request = 0.02

  # corrected 2026-10-04 (founder-approved): min_tps = max(owner, guest); a guest may only tighten
  Scenario: The guest cannot loosen the owner's min_tps floor
    When a chat request arrives with "roger": {"min_tps": 0}
    Then the broker receives roger.min_tps = 10

  Scenario: The guest cannot loosen the owner's confidential requirement
    Given the proxy owner tuned with --confidential
    When a chat request arrives with "roger": {"confidential": false}
    Then the broker receives roger.confidential = true
    And one proxy log line says "guest confidential=false ignored: owner requires confidential"

  # corrected 2026-10-04 (founder-approved): a guest region outside the owner's is refused with the local 400 (the scenario below), never clamped; this one covers the knobs that clamp
  Scenario: The guest cannot loosen the owner's self-hosted-only or trust, and a guest region outside the owner's is refused
    Given the proxy owner tuned with --self-hosted --trust verified --region eu
    When a chat request arrives with "roger": {"self_hosted_only": false, "trust_min": "any"}
    Then the broker receives roger.self_hosted_only = true, roger.trust_min = "verified", roger.region = ["eu"]
    When a chat request arrives with "roger": {"region": ["us"]}
    Then the guest receives an OpenAI-shaped 400 "region us is outside this session's allowed regions"

  Scenario: The guest may narrow the owner's region
    Given the proxy owner tuned with --region eu,us
    When a chat request arrives with "roger": {"region": ["eu"]}
    Then the broker receives roger.region = ["eu"]

  Scenario: A guest region outside the owner's is refused locally
    Given the proxy owner tuned with --region eu
    When a chat request arrives with "roger": {"region": ["us"]}
    Then the guest receives an OpenAI-shaped 400 "region us is outside this session's allowed regions"
    And nothing reaches the broker

  Scenario: The owner's --only is the ceiling for the guest's only/order
    Given the proxy owner tuned with --only n1,n2
    When a chat request arrives with "provider": {"only": ["n2","n3"]}
    Then the broker receives provider.only = ["n2"]
    When a chat request arrives with "provider": {"order": ["n3"]}
    Then the guest receives an OpenAI-shaped 400 "order names n3, outside this session's allowed stations"

  # regression 2026-10-05: audit finding, contract §9
  Scenario: An owner's no-fallback pin is the ceiling for the guest's order and only
    Given the proxy owner tuned with --node n1
    When a chat request arrives with "provider": {"order": ["n9"]}
    Then the guest receives an OpenAI-shaped 400 "order names n9, outside this session's pinned stations"
    And nothing reaches the broker
    When a chat request arrives with "provider": {"only": ["n1","n9"]}
    Then the broker receives provider.only = ["n1"]
    When a chat request arrives with "provider": {"only": ["n9"]}
    Then the guest receives an OpenAI-shaped 400 "only names no station inside this session's pinned stations"

  # regression 2026-10-05: audit finding, contract §9
  Scenario: An owner's --order with --no-fallbacks lets the guest narrow the order, never leave it
    Given the proxy owner tuned with --order n1,n2 --no-fallbacks
    When a chat request arrives with "provider": {"order": ["n2"]}
    Then the broker receives provider.order = ["n2"]
    When a chat request arrives with "provider": {"order": ["n2","n3"]}
    Then the guest receives an OpenAI-shaped 400 "order names n3, outside this session's pinned stations"

  Scenario: The owner's --exclude is unioned with the guest's ignore
    Given the proxy owner tuned with --exclude n9
    When a chat request arrives with "provider": {"ignore": ["n8"]}
    Then the broker receives provider.ignore = ["n8","n9"]

  Scenario: The owner's private freq cannot be dropped or swapped by the guest
    Given the proxy owner tuned with --freq "147.520 MHz 8F3K-9M2Q"
    When a chat request arrives with "roger": {"freq": "147.520 MHz ZZZZ-ZZZZ"}
    Then the broker receives the owner's freq
    And the guest's code is never logged

  Scenario: The owner's quant rule is a default the guest may narrow but not widen
    # The owner's standing RULE travels as the labels plus "unknown" (contract §9); the guest
    # may narrow within that set. A label outside it is refused LOCALLY like a region or an
    # order outside the owner's set: the proxy never synthesizes `quantizations: []`, because
    # an empty list means NO filter and would widen the owner's rule (§1b, §9).
    Given the proxy owner's limit for the band has quants ["Q8_0","BF16"]
    When a chat request arrives with no routing carrier
    Then the broker receives provider.quantizations = ["Q8_0","BF16","unknown"]
    When a chat request arrives with "provider": {"quantizations": ["BF16"]}
    Then the broker receives provider.quantizations = ["BF16"]
    When a chat request arrives with "provider": {"quantizations": ["Q4_K_M"]}
    Then the guest receives an OpenAI-shaped 400 "quantization Q4_K_M is outside this session's quant rule"
    And nothing reaches the broker
    And the proxy never sends provider.quantizations = []

  Scenario: The guest's models[] is bounded by the owner's models when the owner set one
    # corrected 2026-10-05 (founder ruling): guest may only tighten - was "the broker receives
    # models = []", which the broker reads as no list and routes the owner's whole set
    Given the proxy owner tuned with --models qwen3-32b-fp8,llama-3.3-70b
    When a chat request arrives with "models": ["mistral-large"]
    Then the guest receives an OpenAI-shaped 400 "models names no model inside this session's allowed models"
    And the guest receives a local 400 with error.code "routing_outside_session"
    And nothing reaches the broker

  @slice0
  Scenario: A guest models[] naming a model outside the tuned band is refused locally
    # corrected 2026-10-02 (founder ruling): guest may only tighten - was "without an owner
    # models[] the guest's list passes as given"; a guest list could reach models the owner never
    # tuned, billed to the owner
    When a chat request arrives with "models": ["llama-3.3-70b", "mistral-large"]
    Then the guest receives an OpenAI-shaped 400 "model llama-3.3-70b is outside this session's band"
    And nothing reaches the broker

  @slice0
  Scenario: A guest models[] naming only the tuned model passes (sugar on it included)
    # added 2026-10-02 (founder ruling): the tightening the guest may still express
    When a chat request arrives with "models": ["qwen3-32b-fp8", "qwen3-32b-fp8:floor"]
    Then the guest's response carries no error
    And the broker receives models ["qwen3-32b-fp8", "qwen3-32b-fp8:floor"]

  @slice0
  Scenario: A guest models[] entry that is not a string is refused locally, never forwarded
    # added 2026-10-02 (founder ruling): an entry the proxy cannot read cannot be checked
    When a chat request arrives with "models": [42]
    Then the guest receives an OpenAI-shaped 400 "models"
    And nothing reaches the broker

  @slice0
  Scenario: The out cap default is always present even when the owner set nothing (headless guard)
    Given the proxy owner tuned with no caps at all
    When a chat request arrives with no routing carrier
    Then the broker receives provider.max_price.completion = 10

  Scenario: A guest request that only names a profile still gets the owner's ceiling applied
    Given profile "loose" sets provider.max_price.completion = 50
    When a chat request arrives with model "@profile/loose"
    Then the broker receives provider.max_price.completion = 2

  # --- the session budget is unchanged --------------------------------------------------

  Scenario: The per-session budget applies regardless of routing keys
    Given the session budget is $2.00 and $1.99 is spent
    When a chat request arrives with "provider": {"sort": "price"}
    Then the request is served
    And the next request is refused 402 budget_exceeded before any relay

  Scenario: A 400 from the broker for a bad routing key bills nothing to the budget
    When a chat request arrives with "roger": {"pref": "quick"}
    Then the broker's 400 is returned and the session spend is unchanged

  Scenario: The budget reads the new stream usage chunk as well as the meter comment
    When a streaming request settles with a final usage chunk carrying cost 0.0123
    Then the session spend increases by exactly 0.0123 once
    And the trailing ": rogerai-cost=" comment is not double-counted

  # --- failover (defect 8) ----------------------------------------------------------------

  Scenario: After a transport failure the proxy prefers the alternative without pinning it
    Given the first attempt fails with a transport error at station "n1"
    When the proxy re-picks "n2" from /discover
    Then the retry body carries provider.order = ["n2"] and provider.ignore = ["n1"]
    And the retry body carries no provider.allow_fallbacks = false
    And no X-Roger-Node header is set

  Scenario: The broker may still fail over past the proxy's preferred station
    Given the retry body carries provider.order = ["n2"]
    When "n2" answers an upstream 429 and "n3" is eligible
    Then the broker serves from "n3" and the guest sees 200
    And X-RogerAI-Provider names "n3"

  Scenario: A caller's own allow_fallbacks:false is respected by the proxy's failover
    When a chat request arrives with "provider": {"order": ["n1"], "allow_fallbacks": false}
    And "n1" fails with a transport error
    Then the proxy does not re-pick another station
    And the guest receives the broker's last error OpenAI-shaped

  Scenario: A caller's own order is preserved ahead of the proxy's re-pick
    When a chat request arrives with "provider": {"order": ["n5"]}
    And the first attempt fails with a transport error at "n5"
    Then the retry body carries provider.order = ["n2"] and provider.ignore = ["n5"]

  Scenario: A caller's own ignore is unioned with the failed set
    When a chat request arrives with "provider": {"ignore": ["n9"]}
    And the first attempt fails with a transport error at "n1"
    Then the retry body carries provider.ignore = ["n1","n9"]

  Scenario: A broker 503 band_cooling with a short Retry-After is retried once after the wait
    Given the broker answers 503 error.code "band_cooling" with Retry-After: 3
    When the proxy waits 3 seconds and retries once
    And the broker then serves 200
    Then the guest sees 200
    And the guest never saw the 503

  Scenario: A band_cooling Retry-After longer than 5 seconds is returned to the guest as today
    Given the broker answers 503 error.code "band_cooling" with Retry-After: 30
    Then the guest receives the 503 OpenAI-shaped with Retry-After: 30
    And the proxy does not wait

  Scenario: A 429 from the broker is still returned to the guest, not retried (approved retry_after.feature)
    Given the broker answers 429 with Retry-After: 7
    Then the guest receives the 429 OpenAI-shaped rate_limit_error with Retry-After: 7

  Scenario: A 503 no_match is not retried (nothing will change on a retry)
    Given the broker answers 503 error.code "no_match"
    Then the guest receives the 503 OpenAI-shaped immediately

  Scenario: The re-pick honors every routing key in the caller's body, not only Criteria's old fields
    When a chat request arrives with "roger": {"require": ["tools"], "self_hosted_only": true}
    And the first attempt fails with a transport error
    Then the /discover re-pick considers only stations with the tools capability that are not curated

  Scenario: The recovered alert names the served station from X-RogerAI-Provider as today
    Given the first attempt fails and the retry is served by "n2"
    Then the alert reads "recovered: re-routed to n2 after 1 attempt(s)"

  # --- stripping and shape --------------------------------------------------------------

  Scenario: The proxy never adds a carrier the caller did not send beyond the owner's defaults
    When a chat request arrives with no routing carrier and the owner set only --max-out 2
    Then the broker receives exactly provider.max_price.completion = 2 as the routing object

  Scenario: The proxy's added defaults are visible in a debug header for the guest
    When a chat request arrives with no routing carrier
    Then the response to the guest carries X-Roger-Routing-Defaults: "provider.max_price.completion,roger.min_tps"

  Scenario: Body size is checked before any merge
    When a chat request arrives over 4 MiB carrying a large routing object
    Then the guest receives the OpenAI-shaped 413 and nothing reaches the broker

  Scenario: The merged body is still a valid JSON object with top-level key order irrelevant
    When a chat request arrives with a carrier
    Then the body sent to the broker parses as one JSON object

  # --- headers for old brokers (PROPOSED negotiation) ------------------------------------

  Scenario: PROPOSED - the first relay of a session sends the body object and no routing headers
    When the first chat request of the session is relayed
    Then it carries the body object
    And it carries no X-Roger-Min-TPS, X-Roger-Confidential, X-Roger-Pref, X-Roger-Node or X-Roger-Exclude-Nodes
    And it still carries X-Roger-Max-Price-Out for one release

  @slice0
  Scenario: PROPOSED - the mode is negotiated at tune time by probing GET /v1/models (a positive signal)
    # An OLD broker never answers 400 unknown_routing_key: it forwards unknown top-level keys
    # to the station untouched (request_shape.feature, "top-level keys pass through"), so a
    # 400-based trigger would never fire and the carriers would reach the station. The probe
    # is the positive signal from contract §9: the broker's /v1/models exists only on brokers
    # that read the body carriers.
    Given the broker answers 200 to GET /v1/models
    When the operator tunes a band
    Then the session is in body mode
    And the first chat request carries the body object and no routing headers beyond X-Roger-Max-Price-Out

  @slice0
  Scenario: PROPOSED - a 404 from GET /v1/models at tune time puts the session in header mode
    Given an old broker that answers 404 to GET /v1/models
    When the operator tunes a band
    Then the session is in header mode
    And a chat request with the owner's defaults carries X-Roger-Min-TPS: 10 and X-Roger-Max-Price-Out: 2
    And it carries no "provider" or "roger" carrier
    And no request is ever retried to negotiate

  @slice0
  Scenario: PROPOSED - a probe failure that is neither 200 nor 404 defaults to body mode and is logged once
    Given the broker answers 503 to GET /v1/models at tune time
    When the operator tunes a band
    Then the session is in body mode
    And one proxy log line says "routing mode probe failed (503), assuming body mode"

  Scenario: PROPOSED - a 400 from the broker is never treated as an old-broker signal
    Given the session is in body mode
    And the broker answers 400 "request body is not valid JSON"
    When a chat request arrives
    Then the guest receives the 400 and the session mode is unchanged

  @slice0
  Scenario: PROPOSED - in header mode, keys with no header form are dropped with one warning
    Given the session is in header mode
    When a chat request arrives with "roger": {"require": ["tools"], "min_tps": 5}
    # the owner's floor is 10 (Background): a guest may only TIGHTEN it, so 5 is raised to 10
    Then the request carries X-Roger-Min-TPS: 10
    And one proxy log line says "old broker: dropped roger.require (no header form)"
    And the guest's response carries X-Roger-Routing-Dropped: "roger.require"

  @slice0
  Scenario: PROPOSED - in header mode, a caller's models[] cannot be honored and is refused honestly
    Given the session is in header mode
    When a chat request arrives with "models": ["a","b"]
    Then the guest receives an OpenAI-shaped 400 "this broker does not support model fallback lists"

  @slice0
  Scenario: PROPOSED - a re-tune resets the negotiated mode by probing again
    Given the session negotiated header mode
    And the broker now answers 200 to GET /v1/models
    When the operator re-tunes to another band
    Then the session is in body mode

  Scenario: PROPOSED - the mode is per proxy instance, never persisted to config
    When the session negotiated header mode
    Then config.json is unchanged

  # --- /v1/models ---------------------------------------------------------------------

  Scenario: /v1/models on the proxy is specified in features/discovery/models_endpoint.feature
    Given a tuned band
    When GET /v1/models is probed on the proxy
    Then the approved one-entry list is returned unchanged (features/proxy/models.feature)

  # --- guest end-to-end (one per operator; config shapes from features/operator) ---------

  Scenario: opencode - an OpenRouter-style provider block in the request reaches the broker
    Given the opencode launch materialized per features/operator/config_opencode.feature
    And opencode's roger provider passes extra body fields per model (UNVERIFIED against the binary: the exact opencode option key for extra body fields)
    When opencode sends {"model": "qwen3-32b-fp8", "provider": {"sort": "throughput"}, "messages": [...]}
    Then the broker receives provider.sort = "throughput" and model "qwen3-32b-fp8"

  Scenario: hermes - a @profile/ default model resolves through the proxy
    Given the hermes launch materialized per features/operator/config_hermes.feature with default model "@profile/coding"
    And profile "coding" sets models = ["qwen3-32b-fp8"] and roger.require = ["tools"]
    When hermes sends {"model": "@profile/coding", "messages": [...]}
    Then the broker receives model "qwen3-32b-fp8" and roger.require = ["tools"]
    And hermes's argv still pins "roger/@profile/coding" (the -m pin is the guest's, the proxy resolves it)

  Scenario: aider - the env-based wiring needs no change and gets the owner's defaults
    Given the aider launch materialized per features/operator/config_aider.feature
    When aider sends {"model": "openai/qwen3-32b-fp8", "messages": [...]}
    Then the broker receives model "qwen3-32b-fp8" (the approved rewrite, no carrier present)
    And the broker receives the owner's default caps

  Scenario: claude (context-only guest) never relays, so no routing object is built
    Given the claude guest per features/operator (context-only, no proxy relay)
    Then no chat request is relayed and no routing object is built
