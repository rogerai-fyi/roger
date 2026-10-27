# GUEST OPERATORS - routing expression from a guest's seat (CONTRACT §9 profiles, §1 body).
#
# PURPOSE: a guest operator (opencode, hermes, aider; claude is context-only and never relays)
# can express routing the two ways a guest has - naming a profile as its model, or carrying an
# OpenRouter-shaped body - and the plate's ceiling budget and SSE cost meter keep reading the
# spend when the broker's answer changes shape (the new final usage chunk).
#
# GROUND TRUTH at origin/main 518c698b:
#   - Guests reach the band only through the local proxy (internal/client, session bearer key,
#     $2 default ceiling: features/operator/plate_budget.feature, client.go:453).
#   - Materialized wiring: opencode = scratch opencode.json + OPENCODE_CONFIG + `-m roger/<model>`
#     (features/operator/config_opencode.feature); hermes = scratch HERMES_HOME config.yaml +
#     `-m roger/<model>` (config_hermes.feature); aider = OPENAI_API_BASE/KEY + `--model
#     openai/<model>` (config_aider.feature). All three pin the tuned band model at launch.
#   - Every guest body today has its `model` rewritten to the band (client.go:668-690). The
#     guest has NO routing say at all.
#   - The cost meter: non-stream X-RogerAI-Cost header, stream `: rogerai-cost=` comment
#     (client.go:757-784); the plate ceiling stops the next request at 402.
#   - The handoff frame carries model/spend metadata, never guest content
#     (features/operator/rc_enrichment.feature).
#
# PROPOSED (marked): the plate offers a profile picker; the materializers accept `@profile/<n>`
# as the pinned model; a "routing" line on the plate.
#
# Enforced by: internal/operator/guest_routing_bdd_test.go (future; real materializers, a real
# proxy over a recording httptest broker, no mocks).

Feature: A guest operator can express routing through a profile or an OpenRouter-shaped body

  Background:
    Given a live proxy session at "http://127.0.0.1:44017/v1" with session key "sk-test-0123" and band model "qwen3-32b-fp8"
    And the broker accepts the routing body object
    And the plate ceiling is $2.00

  # --- via @profile/ -------------------------------------------------------------------

  Scenario: PROPOSED - the plate lets the DJ pick a profile before handing the mic
    Given profiles "coding" and "cheap" exist
    When the DJ opens the pre-launch plate for opencode
    Then the plate shows "routing: default" with the hint "r cycles profile"
    When the DJ presses r
    Then the plate shows "routing: cheap"
    When the DJ presses r
    Then the plate shows "routing: coding"
    When the DJ presses r
    Then the plate shows "routing: default"

  Scenario: PROPOSED - r is a plate key only, at the ask prompt it just types
    When the DJ is at the plate's ask prompt and presses r
    Then the character "r" is typed and the profile does not cycle

  Scenario: PROPOSED - the chosen profile is pinned into the guest's model as @profile/<name>
    Given the DJ chose profile "coding" on the plate
    When the opencode launch is materialized
    Then the argv is exactly "opencode -m roger/@profile/coding"
    And opencode.json's "model" is "roger/@profile/coding"
    And opencode.json's models block lists "@profile/coding" alongside "qwen3-32b-fp8"

  Scenario: PROPOSED - hermes pins the profile the same way
    Given the DJ chose profile "coding" on the plate
    When the hermes launch is materialized
    Then the argv is exactly "hermes -m roger/@profile/coding"
    And config.yaml's model.default is "@profile/coding"

  Scenario: PROPOSED - aider pins the profile through its --model flag
    Given the DJ chose profile "coding" on the plate
    When the aider launch is materialized
    Then the argv pins "--model openai/@profile/coding"
    And no file is created for aider

  Scenario: The proxy resolves the guest's @profile/ per request
    Given profile "coding" sets models = ["qwen3-32b-fp8", "llama-3.3-70b"] and roger.require = ["tools"]
    When the guest sends {"model": "@profile/coding", "messages": [...]}
    Then the broker receives model "qwen3-32b-fp8", models ["llama-3.3-70b"], roger.require ["tools"]
    And the guest's response is OpenAI-shaped exactly as before

  Scenario: A profile that names no model still lands on the band
    Given profile "cheap" sets roger.pref = "cheap" and no model
    When the guest sends {"model": "@profile/cheap", "messages": [...]}
    Then the broker receives model "qwen3-32b-fp8" and roger.pref = "cheap"

  Scenario: A profile the guest names that does not exist is a local 400, nothing relayed
    When the guest sends {"model": "@profile/nope", "messages": [...]}
    Then the guest receives an OpenAI-shaped 400 "unknown profile nope"
    And the plate's call counter does not increase

  Scenario: The default plate (no profile chosen) behaves exactly as today
    When the opencode launch is materialized with no profile chosen
    Then the argv is exactly "opencode -m roger/qwen3-32b-fp8"
    And opencode.json equals the approved golden artifact

  Scenario: A profile's private freq is applied but never appears in any generated file or argv
    Given profile "home" sets roger.freq = "147.520 MHz 8F3K-9M2Q"
    And the DJ chose profile "home" on the plate
    When each guest launch is materialized
    Then "8F3K-9M2Q" appears in no generated file, no argv, and no env value
    And the broker receives roger.freq on each relayed request

  # --- via an OpenRouter-shaped body ------------------------------------------------------

  Scenario: opencode - a body carrying provider{} reaches the broker unchanged
    Given opencode is configured to add {"provider": {"sort": "throughput", "ignore": ["n9"]}} to its request bodies (the exact opencode option key is UNVERIFIED against the binary; the proxy contract does not depend on it)
    When opencode sends a chat request with that body
    Then the broker receives provider.sort = "throughput" and provider.ignore = ["n9"]
    And the guest's `model` is kept as sent because the body carries a routing carrier

  Scenario: hermes - a body carrying roger{} reaches the broker unchanged
    When hermes sends {"model": "roger/qwen3-32b-fp8", "roger": {"pref": "fast"}, "messages": [...]}
    Then the broker receives roger.pref = "fast"

  Scenario: aider - extra body fields via its --extra-body style setting reach the broker (UNVERIFIED flag name)
    When aider sends {"model": "openai/qwen3-32b-fp8", "provider": {"max_price": {"completion": 1}}, "messages": [...]}
    Then the broker receives provider.max_price.completion = 1

  Scenario: A guest body with a carrier keeps its own model, a guest body without one is rewritten
    When the guest sends {"model": "gpt-4o", "messages": [...]}
    Then the broker receives model "qwen3-32b-fp8"
    When the guest sends {"model": "gpt-4o", "roger": {"pref": "fast"}, "messages": [...]}
    Then the broker receives model "gpt-4o"
    And the broker answers 503 no_match if no station serves "gpt-4o", which the guest sees OpenAI-shaped

  Scenario: OpenRouter provider slugs in order match no node id and fall back to normal scoring
    When the guest sends {"model": "qwen3-32b-fp8", "provider": {"order": ["Anthropic", "OpenAI"]}, "messages": [...]}
    Then the broker's plan has no listed station (no node id equals "Anthropic" or "OpenAI")
    And allow_fallbacks defaults to true, so the request is served by normal scoring
    And one broker log line says "order named 2 unknown stations; falling back to scoring"
    And the guest sees 200

  Scenario: OpenRouter provider slugs with allow_fallbacks:false is an honest 503 no_match
    When the guest sends {"model": "qwen3-32b-fp8", "provider": {"order": ["Anthropic"], "allow_fallbacks": false}, "messages": [...]}
    Then the guest receives a 503 with error.code "no_match" OpenAI-shaped
    And the message names "Anthropic" as an unknown station
    And no hold is placed

  Scenario: OpenRouter's `route: "fallback"` is passed through and refused as unknown by the broker
    When the guest sends {"model": "qwen3-32b-fp8", "route": "fallback", "messages": [...]}
    Then the broker receives route = "fallback" as an ordinary top-level key
    And the broker passes it to the station untouched (it is not a routing carrier)

  Scenario: OpenRouter's `transforms` and `plugins` are ordinary passthrough keys
    When the guest sends {"model": "qwen3-32b-fp8", "transforms": ["middle-out"], "plugins": [{"id": "web"}], "messages": [...]}
    Then the broker receives both keys and forwards them to the station untouched

  Scenario: An OpenRouter `models` list with slugs no station serves is skipped model by model
    When the guest sends {"model": "qwen3-32b-fp8", "models": ["anthropic/claude-sonnet-4", "openai/gpt-4o"], "messages": [...]}
    Then the broker plans "qwen3-32b-fp8" first and serves it
    And the unknown models are never planned (no eligible station)

  Scenario: A guest cannot escape the owner's ceilings with an OpenRouter body (see routing_passthrough.feature)
    Given the proxy owner tuned with --max-out 2
    When the guest sends {"provider": {"max_price": {"completion": 100}}}
    Then the broker receives provider.max_price.completion = 2

  # --- the ceiling and the cost meter read the new usage chunk -----------------------------

  Scenario: A streamed guest turn is metered from the final usage chunk
    When the guest streams a turn that settles at cost 0.0150
    Then the broker's final chunk carries usage.cost = 0.015 and usage.rogerai.receipt
    And the plate's spend increases by exactly 0.015
    And the trailing ": rogerai-cost=0.015" comment is not counted a second time

  Scenario: The chunk and the comment agree; if a broker sent only the comment the meter still works
    Given an old broker that sends only the ": rogerai-cost=" comment
    When the guest streams a turn
    Then the plate's spend increases from the comment exactly as approved

  Scenario: The armed ceiling still brakes the guest at 402 after a model-fallback relay
    Given the ceiling is $2.00 and $1.995 is spent
    When the guest's next turn is served via a fallback model at cost 0.01
    Then that turn is served (the crossing turn completes)
    And the next turn is refused 402 budget_exceeded before any relay

  Scenario: The handoff frame names the served model and node from the chunk, never guest content
    When the guest streams a turn served by "n2" with model "llama-3.3-70b" via fallback
    Then the desk's guest-has-the-mic frame shows "llama-3.3-70b · n2"
    And no guest prompt or completion text appears in the frame

  Scenario: The summary on return names the profile the guest ran under
    Given the DJ chose profile "coding" and handed the mic
    When the guest exits cleanly
    Then the return summary reads "… under profile coding"
    And the DJ's own uncapped session is restored (approved regression)

  Scenario: The usage chunk passes through to the guest byte-identical
    When the guest streams a turn
    Then the guest receives the broker's final usage chunk unchanged before [DONE]
    And a guest that ignores unknown chunk fields keeps working

  # --- isolation invariants stay ---------------------------------------------------------

  Scenario: Profiles never touch the user's own guest config
    Given the DJ chose profile "coding"
    When any guest launch is materialized
    Then the user's real ~/.config/opencode, ~/.hermes/config.yaml and aider files are never opened for writing

  Scenario: The scratch artifacts still hold no session key
    Given the DJ chose profile "coding"
    When the opencode and hermes launches are materialized
    Then the session key appears in no generated file

  Scenario: Detection and the desk strip are unaffected by profiles
    Given profiles exist
    Then the desk strip renders exactly as approved in features/operator/desk_strip.feature
