# /v1/models: OPENROUTER-COMPATIBLE FIELDS (slice 6, audit item #18): SDKs and coding agents
# (opencode, aider via LiteLLM, Continue) read a model's context length, per-token pricing,
# supported parameters and modalities from top-level fields. Today those facts exist only under
# the RogerAI-specific `rogerai` block, so those clients see no context length and no price.
#
# GROUND TRUTH (routing-slice6 at b1754466):
#   - /v1/models (cmd/rogerai-broker/models.go) returns OpenAI's list shape: {object:"list",
#     data:[{id, object:"model", created, owned_by:"rogerai", rogerai:{...}}]}. The rogerai block
#     carries ctx_max (DECLARED windows only, models.go:127-166), min_price_in, min_price_out
#     ($/1M), capabilities, params_b, quants, verified, free_now, confidential, curated, cooling.
#   - min_price_in and min_price_out are independent minimums, so they can come from two
#     different stations: no single station may offer that pair.
#   - Capabilities follow the verified/declared rule: "tools" only from the tool-call canary
#     verdict (toolcall.go), "vision" declared; id-guessed vision is display-only.
#
# RULES (contract draft B §14.B4):
#   - Each data entry gains, at the top level (the rogerai block is unchanged):
#       context_length: integer, the max DECLARED context window across the model's eligible
#         public offers; omitted when every offer's window is estimated.
#       pricing: {"prompt": "<usd per token>", "completion": "<usd per token>"} as decimal
#         strings (OpenRouter shape: $/1M ÷ 1e6, no exponent notation, trailing zeros trimmed),
#         taken from ONE coherent offer: the offer with the lowest blended cost, where blended =
#         in_price × 3 + out_price (the 3:1 prompt-to-completion weighting OpenRouter-style
#         clients assume), ties broken by offer order on /discover. A model whose cheapest
#         coherent offer is free shows "0" for both.
#       supported_parameters: the OpenAI parameter names a client may send with a reasonable
#         expectation of support: always ["max_tokens", "temperature", "top_p", "stop", "seed",
#         "stream"]; plus "tools" and "tool_choice" when at least one eligible offer is
#         tools-VERIFIED; plus "response_format" when at least one offer is tools-verified (the
#         only structured-output signal verified today). Never from id guesses.
#       architecture: {"input_modalities": ["text"] plus "image" when an offer declares vision,
#         "output_modalities": ["text"]}; voice offers are not listed (unchanged).
#   - The same fields appear on GET /v1/models/{id}, and on filtered reads (computed over the
#     surviving offers, consistent with the rogerai block).
#   - Class aliases (features/routing/class_aliases.feature) appear as their own entries with
#     id "@class/<name>", owned_by "rogerai", and rogerai.expands_to = the current expansion.
#
# Enforced by: cmd/rogerai-broker/models_openrouter_fields_bdd_test.go

Feature: /v1/models carries the top-level fields SDKs read, from coherent offers

  Background:
    Given a broker with an empty in-memory node registry
    And station "a1" is on air for "qwen3-32b" at in $0.10 out $0.30 per 1M with a declared window of 32768
    And station "a2" is on air for "qwen3-32b" at in $0.05 out $0.90 per 1M with a declared window of 131072
    And station "b1" is on air for "llama-3.3-70b" at in $0.20 out $0.20 per 1M with an estimated window

  # --- context_length ------------------------------------------------------------------

  Scenario: context_length is the largest declared window across the model's offers
    When a consumer GETs /v1/models
    Then the "qwen3-32b" entry has context_length 131072

  Scenario: context_length is omitted when every window is estimated
    When a consumer GETs /v1/models
    Then the "llama-3.3-70b" entry has no context_length

  Scenario: A declared window wins over a larger estimated one
    Given station "a3" is on air for "qwen3-32b" with an estimated window of 262144
    When a consumer GETs /v1/models
    Then the "qwen3-32b" entry has context_length 131072

  Scenario: context_length agrees with rogerai.ctx_max
    When a consumer GETs /v1/models
    Then every entry's context_length equals its rogerai.ctx_max when both are present

  # --- pricing -------------------------------------------------------------------------

  Scenario: pricing comes from one coherent offer, the cheapest by blended cost
    When a consumer GETs /v1/models
    Then the "qwen3-32b" entry has pricing prompt "0.0000001" and completion "0.0000003"
    # a1 blended = 0.10*3+0.30 = 0.60; a2 blended = 0.05*3+0.90 = 1.05 -> a1

  Scenario: pricing never pairs the cheapest input of one station with the cheapest output of another
    When a consumer GETs /v1/models
    Then the "qwen3-32b" pricing is not prompt "0.00000005" with completion "0.0000003"

  Scenario: pricing is per token, as decimal strings with no exponent notation
    When a consumer GETs /v1/models
    Then every pricing value matches the pattern of a plain decimal string
    And no pricing value contains "e" or "E"

  Scenario: A free model shows zero pricing
    Given station "f1" is on air for "free-model" at in $0 out $0 per 1M
    When a consumer GETs /v1/models
    Then the "free-model" entry has pricing prompt "0" and completion "0"

  Scenario: A station in a free time window counts as free for pricing right now
    Given station "w1" is the only station for "windowed" at in $0.10 out $0.30 per 1M with a free window active now
    When a consumer GETs /v1/models
    Then the "windowed" entry has pricing prompt "0" and completion "0"

  Scenario: An equal blended cost is broken by /discover order
    Given stations "e1" and "e2" are on air for "tie" at the same blended cost with different in and out prices
    When a consumer GETs /v1/models
    Then the "tie" pricing comes from whichever of "e1", "e2" is first on /discover

  Scenario: A curated station prices pricing like any other offer
    Given curated station "c1" is on air for "qwen3-32b" at in $0.01 out $0.01 per 1M
    When a consumer GETs /v1/models
    Then the "qwen3-32b" entry has pricing prompt "0.00000001" and completion "0.00000001"

  Scenario: A cooling station still prices (it is on air)
    Given "a1" is cooling for 30 more seconds
    When a consumer GETs /v1/models
    Then the "qwen3-32b" entry's pricing still reflects "a1"

  Scenario: A private-band station never contributes pricing
    Given a private band whose only station "p1" serves "qwen3-32b" at in $0.001 out $0.001 per 1M
    When a consumer GETs /v1/models
    Then the "qwen3-32b" pricing does not reflect "p1"

  # --- supported_parameters ------------------------------------------------------------

  Scenario: The base parameters are always listed
    When a consumer GETs /v1/models
    Then every entry's supported_parameters include "max_tokens", "temperature", "top_p", "stop", "seed" and "stream"

  Scenario: tools, tool_choice and response_format appear only when some offer is tools-verified
    Given "a1" earned verified "tools" for "qwen3-32b"
    When a consumer GETs /v1/models
    Then the "qwen3-32b" supported_parameters include "tools", "tool_choice" and "response_format"
    And the "llama-3.3-70b" supported_parameters include none of them

  Scenario: A self-declared tools capability never adds tools
    Given "b1" registered declaring "tools" but never passed a tool-call canary
    When a consumer GETs /v1/models
    Then the "llama-3.3-70b" supported_parameters do not include "tools"

  Scenario: A tools verdict that expired removes tools
    Given "a1" earned verified "tools" for "qwen3-32b" and the verdict has expired
    When a consumer GETs /v1/models
    Then the "qwen3-32b" supported_parameters do not include "tools"

  Scenario: supported_parameters agrees with the rogerai capabilities
    Given "a1" earned verified "tools" for "qwen3-32b"
    When a consumer GETs /v1/models
    Then "tools" is in supported_parameters exactly when "tools" is in rogerai.capabilities

  # --- architecture -----------------------------------------------------------------------

  Scenario: A text model lists text in and text out
    When a consumer GETs /v1/models
    Then the "llama-3.3-70b" architecture is input_modalities ["text"] and output_modalities ["text"]

  Scenario: A declared vision offer adds image input
    Given "a2" declares "vision"
    When a consumer GETs /v1/models
    Then the "qwen3-32b" architecture input_modalities is ["text", "image"]

  Scenario: An id that merely looks like a vision model does not add image input
    Given station "v1" is on air for "llava-guess" declaring no capabilities
    When a consumer GETs /v1/models
    Then the "llava-guess" architecture input_modalities is ["text"]

  Scenario: Voice offers are still not listed
    Given a voice station is on air for "kokoro"
    When a consumer GETs /v1/models
    Then no entry has id "kokoro"

  # --- single model and filtered reads ----------------------------------------------------

  Scenario: GET /v1/models/{id} carries the same top-level fields
    When a consumer GETs /v1/models/qwen3-32b
    Then the entry carries context_length, pricing, supported_parameters and architecture equal to the list entry's

  Scenario: A filtered read computes the fields over the surviving offers
    When a consumer GETs /v1/models?max_price_out=0.5
    Then the "qwen3-32b" entry has context_length 32768
    And its pricing comes from "a1"

  Scenario: A filter leaving no offer for a model removes the entry
    When a consumer GETs /v1/models?min_ctx=200000
    Then no entry has id "qwen3-32b"

  # --- compatibility ------------------------------------------------------------------------

  Scenario: The rogerai block is unchanged by the new fields
    When a consumer GETs /v1/models
    Then each entry's rogerai block has exactly the fields it had before this change

  Scenario: An OpenAI client that reads only id, object, created and owned_by still parses the list
    When an OpenAI-shaped client lists models
    Then it reads every entry's id without error

  Scenario: An OpenRouter-shaped client reads context_length and pricing as it would from OpenRouter
    When an OpenRouter-shaped client lists models
    Then it reads "qwen3-32b" context_length 131072 and a prompt price as a number parsed from a string

  # --- class aliases ---------------------------------------------------------------------------

  Scenario: A class alias is listed as its own entry with its current expansion
    Given the class "@class/large" currently expands to ["llama-3.3-70b"]
    When a consumer GETs /v1/models
    Then an entry with id "@class/large" exists with owned_by "rogerai"
    And its rogerai.expands_to is ["llama-3.3-70b"]

  Scenario: A class alias that currently expands to nothing is not listed
    Given the class "@class/small" currently expands to nothing
    When a consumer GETs /v1/models
    Then no entry has id "@class/small"

  Scenario: A class alias entry carries the coherent pricing of its first expanded model
    Given the class "@class/large" currently expands to ["llama-3.3-70b"]
    When a consumer GETs /v1/models
    Then the "@class/large" entry has the pricing of the "llama-3.3-70b" entry
