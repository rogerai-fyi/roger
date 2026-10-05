# GET /v1/models ON THE BROKER: the OpenAI-shaped catalog every SDK, agent CLI and guest
# operator probes at startup. Today the broker has no such route (a plain 404); only the LOCAL
# PROXY answers it, listing the ONE tuned band (internal/client/client.go:646-658, ruling 4 in
# features/proxy/models.feature). A consumer pointing an OpenAI SDK straight at the broker -
# which the routing-expression body object now makes worthwhile - gets nothing to pick from.
#
# Contract: features/routing/ROUTING-EXPRESSION-CONTRACT.md §8 (third bullet), §9 (guest
# operators and profiles), §1a for the error envelope.
#
# GROUND TRUTH as of origin/main 518c698b:
#   - The broker's route table (cmd/rogerai-broker/main.go) has /discover, /market, /voices,
#     /v1/chat/completions, /v1/audio/*; no /v1/models.
#   - The local proxy's writeModelsList (internal/client/client.go:646-658) emits
#     {"object":"list","data":[{id, object:"model", created, owned_by:"rogerai"}]} for the
#     currently-tuned band only; APPROVED scenarios in features/proxy/models.feature pin
#     "exactly one entry" and "follows the re-tune".
#   - /discover + /market: public, CORS via cors() (httputil.go:49), preflight 204, NO per-IP
#     gate (market.go:256-263), cached via serveCachedJSON with publicMarketTTL.
#   - Private nodes are hidden from every public view (market.go:292-296, 442-446).
#
# After this spec: the broker serves GET /v1/models and GET /v1/models/{id} as a PUBLIC READ
# with the same CORS, cache and throttle posture as /market, one entry per distinct model id
# with at least one public offer on air, each carrying a `rogerai` extension block, honoring
# the same filter params as /discover (features/discovery/filters.feature). The local proxy's
# /v1/models is UNCHANGED (see the flagged scenario at the end).
#
# Enforced by: cmd/rogerai-broker/models_endpoint_bdd_test.go (godog, strict) - after
# approval.

Feature: GET /v1/models on the broker - an OpenAI-shaped catalog of what is on air

  Background:
    Given a broker with an empty in-memory node registry
    And nodes are on air:
      | node     | model          | quant  | params_b | ctx    | tps | price_in | price_out | curated | confidential | free_now | verified |
      | n-a      | qwen3-32b      | Q8_0   | 32.8     | 131072 | 40  | 0.10     | 0.30      | false   | false        | false    | true     |
      | n-b      | qwen3-32b      | Q4_K_M | 32.8     | 32768  | 25  | 0.05     | 0.15      | false   | false        | false    | false    |
      | n-free   | qwen3-32b      |        |          | 32768  | 0   | 0        | 0         | false   | false        | true     | false    |
      | n-cur    | gpt-oss-120b   | BF16   | 120      | 131072 | 120 | 0.20     | 0.60      | true    | false        | false    | true     |
      | n-tee    | llama-3.3-70b  | Q8_0   | 70       | 65536  | 30  | 0.15     | 0.45      | false   | true         | false    | true     |

  # --- shape -----------------------------------------------------------------------------------
  @slice0
  Scenario: The list is OpenAI-shaped
    When a consumer GETs /v1/models
    Then the status is 200
    And the Content-Type is "application/json"
    And the body has object "list" and a data array
    And every data entry has object "model", a string id, an integer created and owned_by "rogerai"

  # superseded 2026-10-05 by contract §14 (founder-approved): /v1/models also lists each non-empty
  # class alias (@class/<name>, §14.B4/§14.B5); the model-id statements here are over model entries.
  @slice0
  Scenario: One entry per distinct model id with a public offer on air
    When a consumer GETs /v1/models
    Then the data ids are exactly "qwen3-32b", "gpt-oss-120b", "llama-3.3-70b"
    And no id appears twice

  # superseded 2026-10-05 by contract §14 (founder-approved): /v1/models also lists each non-empty
  # class alias (@class/<name>, §14.B4/§14.B5); the model-id statements here are over model entries.
  @slice0
  Scenario: Entries are ordered by id ascending for a stable catalog
    When a consumer GETs /v1/models
    Then the data ids are in order "gpt-oss-120b", "llama-3.3-70b", "qwen3-32b"

  Scenario: created is the unix time the model FIRST came on air, read from the SHARED store, not now
    Given "qwen3-32b" first came on air at unix 1_800_000_000
    When a consumer GETs /v1/models twice, 10 s apart
    Then both reads show created 1_800_000_000 for "qwen3-32b"

  Scenario: created is identical on every broker instance (it is not instance-local)
    # A model that first came on air while instance B was down must still show the same
    # created on B: the first-on-air time is a shared-store record, never an in-memory
    # first-seen (contract §8; the same rule that makes /generation work across instances).
    Given two broker instances "A" and "B" share the store
    And "qwen3-32b" first came on air at unix 1_800_000_000 while "B" was down
    When "B" comes up and a consumer GETs /v1/models on "A" and on "B"
    Then both instances show created 1_800_000_000 for "qwen3-32b"

  Scenario: The rogerai extension block carries the routing facts a consumer needs to choose
    When a consumer GETs /v1/models
    Then the "qwen3-32b" entry's rogerai block reads:
      | field          | value                |
      | providers      | 3                    |
      | curated        | 0                    |
      | min_price_in   | 0                    |
      | min_price_out  | 0                    |
      | best_tps       | 40                   |
      | ctx_max        | 131072               |
      | params_b       | 32.8                 |
      | capabilities   | (omitted: none declared or verified) |
      | free_now       | true                 |
      | confidential   | false                |
      | verified       | true                 |
      | quants         | ["Q4_K_M","Q8_0"]    |

  Scenario: ctx_max counts DECLARED windows only and is omitted when every offer's ctx is estimated
    Given every "qwen3-32b" offer's ctx is estimated
    When a consumer GETs /v1/models
    Then the "qwen3-32b" entry's rogerai block has no ctx_max key

  Scenario: params_b in the block is the declared value when any offer declares one, else the table estimate with params_estimated true
    When a consumer GETs /v1/models
    Then the "qwen3-32b" entry's rogerai block has params_b 32.8 and params_estimated false
    Given node "n-guess" is the only station for "mistral-small-3.1-24b" and declares no params_b
    When a consumer GETs /v1/models
    Then the "mistral-small-3.1-24b" entry's rogerai block has params_b 24 and params_estimated true

  Scenario: Two offers of one model that declare different params_b - the block carries the range
    Given node "n-pruned" is also on air for "qwen3-32b" with params_b 18
    When a consumer GETs /v1/models
    Then the "qwen3-32b" entry's rogerai block has params_b_min 18 and params_b_max 32.8

  Scenario: capabilities in the block is the union across on-air providers under the same verified/declared rule as /market
    Given ("n-a", "qwen3-32b") earned verified "tools"
    And "n-b" declared "vision" for "qwen3-32b"
    When a consumer GETs /v1/models
    Then the "qwen3-32b" entry's rogerai block has capabilities ["tools", "vision"]

  Scenario: The curated count is kept apart from providers, as on /market
    When a consumer GETs /v1/models
    Then the "gpt-oss-120b" entry's rogerai block has providers 0 and curated 1

  Scenario: confidential is true when at least one provider is TEE-attested
    When a consumer GETs /v1/models
    Then the "llama-3.3-70b" entry's rogerai block has confidential true
    And the "qwen3-32b" entry's rogerai block has confidential false

  Scenario: free_now is true when at least one provider is free right now, including a live free window
    Given "n-tee" has a schedule window that is FREE right now
    When a consumer GETs /v1/models
    Then the "llama-3.3-70b" entry's rogerai block has free_now true

  # superseded 2026-10-05 by contract §14 (founder-approved): /v1/models also lists each non-empty
  # class alias (@class/<name>, §14.B4/§14.B5); the model-id statements here are over model entries.
  @slice0
  Scenario: An OpenAI client that ignores the rogerai block still parses the list
    When an OpenAI SDK lists models against the broker
    Then it decodes 3 models without error

  # --- presence rules ---------------------------------------------------------------------------
  @slice0
  Scenario: A model whose only station is COOLING is still listed (on air, not routed to for now)
    Given "n-tee" is in a 429 cooldown for 30 s
    When a consumer GETs /v1/models
    Then "llama-3.3-70b" is listed
    And its rogerai block has cooling true and cooling_until about 30 s from now

  @slice0
  Scenario: A model whose only station is past its heartbeat window is absent
    Given "n-tee" has not heartbeat within nodeTTL
    When a consumer GETs /v1/models
    Then "llama-3.3-70b" is not listed

  Scenario: A model whose only station is probe-dead on its authoritative host is absent
    Given "n-tee" has failed a sustained probe streak on its poll host with no recent serving evidence
    When a consumer GETs /v1/models
    Then "llama-3.3-70b" is not listed

  @slice0
  Scenario: A model whose only station is banned is absent
    Given "n-tee" is banned
    When a consumer GETs /v1/models
    Then "llama-3.3-70b" is not listed

  @slice0
  Scenario: A model served only on a private band is never listed without its code
    Given node "n-secret" shares "secret-model" on a PRIVATE band
    When a consumer GETs /v1/models
    Then "secret-model" is not listed
    When a consumer GETs /v1/models/secret-model
    Then the status is 404

  @slice0
  Scenario: A private band's models are not listed even with the band code on /v1/models - the code is a /bands/resolve concern
    Given node "n-secret" shares "secret-model" on a PRIVATE band with frequency code "fq-1"
    When a consumer GETs /v1/models with header "X-Roger-Freq: fq-1"
    Then "secret-model" is not listed
    And the response is identical to one without the header

  Scenario: A curated model is listed with the curated marker
    When a consumer GETs /v1/models
    Then "gpt-oss-120b" is listed
    And its rogerai block has curated 1 and providers 0

  Scenario: A voice (tts/stt) offer is not a chat model and is not listed
    Given node "n-voice" is on air for "kokoro-82m" with modality "tts"
    When a consumer GETs /v1/models
    Then "kokoro-82m" is not listed

  Scenario: An empty market is 200 with an empty data array
    Given every node has left the air
    When a consumer GETs /v1/models
    Then the status is 200
    And the body is {"object":"list","data":[]}

  # --- filters ------------------------------------------------------------------------------------
  # superseded 2026-10-05 by contract §14 (founder-approved): /v1/models also lists each non-empty
  # class alias (@class/<name>, §14.B4/§14.B5); the model-id statements here are over model entries.
  Scenario Outline: /v1/models honors the same filter params as /discover and lists models with at least one surviving offer
    When a consumer GETs /v1/models?<query>
    Then the data ids are exactly <ids>

    Examples:
      | query               | ids                                  |
      | quant=Q8_0          | llama-3.3-70b,qwen3-32b              |
      | self_hosted=1       | llama-3.3-70b,qwen3-32b              |
      | confidential=1      | llama-3.3-70b                        |
      | free=1              | qwen3-32b                            |
      | min_tps=100         | gpt-oss-120b,qwen3-32b               |
      | params_min=60       | gpt-oss-120b,llama-3.3-70b           |
      | min_ctx=65536       | gpt-oss-120b,llama-3.3-70b,qwen3-32b |
      | min_ctx=131072      | gpt-oss-120b,qwen3-32b               |
      | trust_min=verified  | gpt-oss-120b,llama-3.3-70b,qwen3-32b |
      | region=eu           |                                      |
    # min_tps=100 keeps n-free (unmeasured passes) so qwen3-32b survives.

  Scenario: The rogerai block is computed over the SURVIVING offers under a filter
    When a consumer GETs /v1/models?quant=Q8_0
    Then the "qwen3-32b" entry's rogerai block has providers 1, min_price_out 0.30 and quants ["Q8_0"]

  Scenario: An unknown or malformed filter param is a 400 with the same codes as /discover
    When a consumer GETs /v1/models?foo=1
    Then the status is 400
    And the error code is "unknown_query_param"
    When a consumer GETs /v1/models?min_tps=fast
    Then the status is 400
    And the error code is "invalid_query_param"

  # --- GET /v1/models/{id} ----------------------------------------------------------------------
  Scenario: A single model is fetched by id
    When a consumer GETs /v1/models/qwen3-32b
    Then the status is 200
    And the body is one model object with id "qwen3-32b", object "model", owned_by "rogerai" and a rogerai block

  # superseded 2026-10-05 by contract §14 (founder-approved): the one envelope adds error.type and error.metadata (§14.B6).
  Scenario: An id with no public offer on air is a 404 in the OpenAI error shape
    When a consumer GETs /v1/models/nobody-serves-this
    Then the status is 404
    And the body is {"error":{"code":"model_not_found","message":...,"type":"not_found_error","metadata":...}}

  Scenario: The id lookup is exact and case-sensitive
    When a consumer GETs /v1/models/QWEN3-32B
    Then the status is 404

  Scenario: A model id containing a colon or a slash is addressable
    Given node "n-tag" is on air for "llama3:8b"
    And node "n-org" is on air for "org/model-v1"
    When a consumer GETs /v1/models/llama3:8b
    Then the status is 200 and the id is "llama3:8b"
    When a consumer GETs /v1/models/org%2Fmodel-v1
    Then the status is 200 and the id is "org/model-v1"

  Scenario: Variant sugar on the id is not a catalog entry
    When a consumer GETs /v1/models/qwen3-32b:free
    Then the status is 404

  Scenario: A profile reference is not a catalog entry on the broker
    When a consumer GETs /v1/models/@profile/coding
    Then the status is 404

  Scenario: The single-model path honors the same filters
    When a consumer GETs /v1/models/qwen3-32b?quant=NVFP4
    Then the status is 404

  # --- posture: CORS, cache, throttle, methods -------------------------------------------------
  Scenario: CORS headers are identical to /market
    When a consumer GETs /v1/models from the website origin
    Then the response carries Access-Control-Allow-Origin "*", Allow-Methods "GET, OPTIONS" and Allow-Headers "Content-Type"

  Scenario: A preflight OPTIONS is answered 204 with the CORS headers
    When a browser sends OPTIONS /v1/models
    Then the status is 204
    And the CORS headers are present

  Scenario Outline: Only GET is allowed
    When a consumer sends <method> /v1/models
    Then the status is 405

    Examples:
      | method |
      | POST   |
      | PUT    |
      | DELETE |

  Scenario: /v1/models is served from the same short public cache as /market
    Given /v1/models was computed within publicMarketTTL
    When another consumer GETs /v1/models
    Then the catalog is not recomputed

  Scenario: /v1/models is never rate-limited to an empty-looking body
    When one IP GETs /v1/models 200 times in a second
    Then every response is 200 with a data array

  Scenario: /v1/models needs no authentication
    When an anonymous consumer with no Authorization header GETs /v1/models
    Then the status is 200

  Scenario: A bearer token on /v1/models is ignored, never validated, never logged
    When a consumer GETs /v1/models with header "Authorization: Bearer garbage"
    Then the status is 200
    And no log line contains "garbage"

  Scenario: A filtered /v1/models does not fragment the cache
    When 100 consumers GET /v1/models with 100 different filter combinations within publicMarketTTL
    Then the catalog was computed at most once

  # --- consistency with the other views ----------------------------------------------------------
  # superseded 2026-10-05 by contract §14 (founder-approved): /v1/models also lists each non-empty
  # class alias (@class/<name>, §14.B4/§14.B5); the model-id statements here are over model entries.
  Scenario: /v1/models, /market and /discover agree on the set of models on air
    When a consumer GETs /v1/models, /market and /discover in the same cache window
    Then the /v1/models ids equal the /market model set
    And equal the set of distinct model ids of ONLINE /discover offers

  Scenario: For a funded consumer, every listed id whose stations are not all cooling routes by that exact id
    Given "u-1" is a funded consumer
    When "u-1" GETs /v1/models and then POSTs /v1/chat/completions with each listed id whose stations are not all cooling
    Then no request fails with "no node offers"

  Scenario: A cooling-only model is listed (it is on air) but routes to a 503 band_cooling, not "no node offers"
    Given the only station serving "gpt-oss-120b" is in an upstream-429 cooldown
    When a funded consumer GETs /v1/models
    Then "gpt-oss-120b" is listed with rogerai.cooling true
    When the consumer POSTs /v1/chat/completions for "gpt-oss-120b"
    Then the status is 503
    And the error code is "band_cooling"
    And the response carries a Retry-After

  Scenario: An anonymous caller on a listed paid-only model gets today's 401, not "no node offers"
    # Contract §1a: the stations exist, the caller cannot pay them (anonCannotPay).
    Given every station serving "llama-3.3-70b" is priced above 0/0
    When an anonymous consumer GETs /v1/models
    Then "llama-3.3-70b" is listed with rogerai.free_now false
    When the anonymous consumer POSTs /v1/chat/completions for "llama-3.3-70b"
    Then the status is 401
    And the error message says to log in to spend

  # --- the local proxy (UNCHANGED - flagged) -------------------------------------------------------
  # FLAG FOR THE FOUNDER: contract §9 says a guest operator picks routing by naming
  # "@profile/<name>" as the model. For an agent CLI to pick it from a menu, the LOCAL PROXY's
  # /v1/models would have to list profile entries. That contradicts the APPROVED scenario
  # "Exactly one entry - the proxy relays to one band per session" (features/proxy/models.feature).
  # This spec does NOT change the proxy. It pins today's behavior and records that a guest can
  # still SEND "@profile/<name>" as the model without it being listed. Listing profiles needs a
  # re-ruling on that approved scenario.
  @proxy
  Scenario: The local proxy's /v1/models is unchanged - one entry, the tuned band
    Given a tuned band whose model is "qwen3-32b" and the local proxy bound to it
    And the consumer's config defines profiles "coding" and "cheap"
    When an agent sends GET "/v1/models" with the session key
    Then the data array has exactly 1 entry
    And data[0].id is "qwen3-32b"
    And no "@profile/" entry is listed

  @proxy
  Scenario: A guest can name a profile as the model through the local proxy even though it is not listed
    Given a tuned band whose model is "qwen3-32b" and the local proxy bound to it
    And the consumer's config defines profile "coding"
    When an agent POSTs /v1/chat/completions through the proxy with model "@profile/coding"
    Then the proxy resolves the profile into the body before relaying
    And the broker receives no "@profile/" string

  @proxy
  Scenario: The local proxy does not forward GET /v1/models to the broker
    Given a tuned band whose model is "qwen3-32b" and the local proxy bound to it
    When an agent sends GET "/v1/models" with the session key
    Then the broker's /v1/models was not called

  # --- documentation -----------------------------------------------------------------------------
  @docs
  Scenario: OpenAPI documents GET /v1/models and GET /v1/models/{id} with the rogerai block schema
    When the OpenAPI document is read
    Then it documents /v1/models and /v1/models/{id} as public GET reads
    And the response schema documents the OpenAI list shape and every rogerai block field
    And it references the same filter params as /discover
    And the 404 documents error code "model_not_found"
