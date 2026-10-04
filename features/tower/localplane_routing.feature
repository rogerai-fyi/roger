# BUILD STATUS: PARTIAL (2026-10-04). Built: models[], provider.only / ignore / order /
# allow_fallbacks over local station ids, evaluated trust_min / confidential, named ignored keys,
# /v1/models, scripts/localplane-routing-smoke.sh (live-run PASS). Red: "Public identity in a
# routing body is ignored and never reflected" contradicts the approved "only s9" no_match message
# (an unattached id cannot be told apart from a public one); awaiting a founder ruling.
# STANDALONE TOWER - the Core-free local plane and the routing body object (CONTRACT §1, §5, §6).
#
# PURPOSE: a client on a private network sends the same body object it would send the public
# broker; the local plane honors the keys it can evaluate, says exactly which keys it ignored,
# strips the carriers before a station sees the prompt, and never grows a billing or identity
# surface while doing it.
#
# GROUND TRUTH at origin/main 518c698b (internal/localplane):
#   - chatRequest reads ONE field, `model` (completion.go:91-96); everything else is opaque and
#     passed to the station verbatim. The plane reads no account, wallet, freq or grant.
#   - chatCompletions (completion.go:101-176): client auth -> per-client fairness -> whole-Tower
#     inflight bound -> `model` required (400 "a model is required") -> offersModel over
#     s.st.Stations() (station.Models only, completion.go:203-214) -> 404 "model not offered by
#     any local station" -> submit to the queue by model; a station long-polls for a job it can
#     serve (localPoll, 216+). Timeout -> 504; disconnect -> abandon + receipt if delivered.
#   - writeAnswer (completion.go:185-199): free local receipt, X-Roger-Cost: 0, X-Roger-Local: 1,
#     X-Roger-Curated when the attach registry says so. Never a billing shape.
#   - The plane knows a station's id and model list ONLY. No quant, params, ctx, region, tps,
#     ttft, trust tier or price exists in the attach registry. So, by the contract's own
#     "declared attribute unknown => ineligible" rule, honoring those filters would refuse every
#     request; the honest answer is to NOT evaluate them and SAY so.
#   - Bounds: request size, concurrency, per-client rate (standalone_consumer_plane.feature).
#     The console spend setting (v6.2.0 data-loss fix) is unrelated to consumer routing.
#
# DECISIONS (state, not ask):
#   HONORED locally: `models[]` (first model any local station serves, in order), `provider.only`,
#   `provider.ignore`, `provider.order`, `provider.allow_fallbacks` over LOCAL station ids.
#   EVALUATED, never ignored (CONTRACT §5a, "a default can never weaken a stated restriction"):
#   roger.confidential and roger.trust_min. The attach registry records no attestation, so a
#   request that requires one finds no attested local station and gets 503 no_match; the plane
#   never serves an unattested station and merely names the key in a header.
#   REJECTED: `@profile/` in either carrier (model or roger.profile) is 400 unknown_profile;
#   profiles resolve on the client (§9).
#   IGNORED locally, named in `X-Roger-Routing-Ignored`: provider.sort, provider.quantizations,
#   provider.max_price.*, provider.require_parameters, roger.pref, roger.require, roger.params_b,
#   roger.min_ctx, roger.min_tps, roger.max_ttft_ms, roger.self_hosted_only, roger.region,
#   roger.freq. Price keys are meaningless on a free plane; region/curated are single-owner
#   facts the operator already controls by what they attach. Ignoring is not silent: the header
#   lists every ignored key, every time.
#   CONSTRAINT MISSES are 503 no_match, never 404 (CONTRACT §2, §5a). The approved 404 "model
#   not offered by any local station" is unchanged: it is about the MODEL, not a constraint.
#   PROPOSED: the queue records the allowed station set per job so only a listed station's poll
#   may claim it (today a job is claimed by any station serving the model).
#
# Enforced by: internal/localplane/routing_bdd_test.go (future; the real plane over a real
# local store with real attached-station polls, no mocks) and the standalone smoke script.

Feature: The standalone Tower honors the routing keys it can evaluate and names the ones it cannot

  Background:
    Given a standalone Tower with admitted client "c1"
    And local stations "s1" (models qwen3-32b, llama-3.3-70b), "s2" (models qwen3-32b), "s3" (models mistral-7b, curated "acme")
    And every station is polling

  # --- unchanged surface ------------------------------------------------------------------

  Scenario: A plain request is served exactly as approved
    When "c1" posts {"model": "qwen3-32b", "messages": [...]}
    Then a station serving "qwen3-32b" claims and answers it
    And the response carries X-Roger-Cost: 0 and X-Roger-Local: 1
    And no X-Roger-Routing-Ignored header is present

  Scenario: The curated mark is unchanged
    When "c1" posts {"model": "mistral-7b", "messages": [...]}
    Then the response carries X-Roger-Curated: acme

  Scenario: Auth, fairness, inflight and rate bounds run before any routing key is read
    When an unadmitted caller posts a body carrying a routing object
    Then the refusal is the uniform auth refusal and no routing key is parsed
    When "c1" exceeds its per-client concurrency with routing bodies
    Then the 429 is the approved one

  Scenario: The console spend setting is unrelated and unchanged
    Given the operator set a console spend value
    When routing requests are served
    Then the console spend setting is byte-identical afterwards

  # --- honored: models[] ------------------------------------------------------------------

  Scenario: models[] picks the first model any local station serves
    When "c1" posts {"model": "gpt-4o", "models": ["claude-3", "llama-3.3-70b", "qwen3-32b"], "messages": [...]}
    Then the job is submitted for "llama-3.3-70b"
    And the response carries X-RogerAI-Model: llama-3.3-70b

  Scenario: The primary model is tried before the list
    When "c1" posts {"model": "qwen3-32b", "models": ["llama-3.3-70b"], "messages": [...]}
    Then the job is submitted for "qwen3-32b"

  Scenario: No model in the list is offered locally is the approved 404 naming every model
    When "c1" posts {"model": "gpt-4o", "models": ["claude-3"], "messages": [...]}
    Then the response is 404 "model not offered by any local station: gpt-4o, claude-3"
    And nothing is dialed

  Scenario: A local timeout on the first model does not fall through to the second
    Given no station claims "qwen3-32b" within the completion timeout
    When "c1" posts {"model": "qwen3-32b", "models": ["llama-3.3-70b"], "messages": [...]}
    Then the response is the approved 504
    And no job was submitted for "llama-3.3-70b" (a local plane has no upstream error to fail over on)

  Scenario: models[] over five entries is refused before anything is queued
    When "c1" posts a body with six distinct models across model and models
    Then the response is 400 with error "too many models (max 5)"

  Scenario: The station receives the served model in `model` and no models[] key
    When "c1" posts {"model": "gpt-4o", "models": ["qwen3-32b"], "messages": [...]}
    Then the station's job body has model "qwen3-32b" and no "models" key

  # --- honored: only / ignore / order / allow_fallbacks over local station ids ---------------

  Scenario: PROPOSED - provider.only restricts which station may claim the job
    When "c1" posts {"model": "qwen3-32b", "provider": {"only": ["s2"]}, "messages": [...]}
    Then only "s2"'s poll is handed the job
    And "s1" polling for "qwen3-32b" gets 204

  Scenario: PROPOSED - provider.ignore removes a station from claiming
    When "c1" posts {"model": "qwen3-32b", "provider": {"ignore": ["s1"]}, "messages": [...]}
    Then "s1"'s poll never receives the job and "s2" serves it

  Scenario: PROPOSED - only with no local match is a 503 no_match that names the constraint
    When "c1" posts {"model": "qwen3-32b", "provider": {"only": ["s9"]}, "messages": [...]}
    Then the response is 503 with error code "no_match" and message "no local station matches: only s9 for qwen3-32b"
    And the response carries no Retry-After (nothing is cooling on a local plane)
    And nothing is queued

  Scenario: PROPOSED - ignore that removes every station is the same 503 no_match
    When "c1" posts {"model": "qwen3-32b", "provider": {"ignore": ["s1", "s2"]}, "messages": [...]}
    Then the response is 503 with error code "no_match" and message "no local station matches: ignore removed every station for qwen3-32b"

  Scenario: The approved 404 stays for a MODEL nobody offers, and is never used for a constraint miss
    When "c1" posts {"model": "gpt-4o", "provider": {"only": ["s1"]}, "messages": [...]}
    Then the response is the approved 404 "model not offered by any local station: gpt-4o"
    # the model gate runs first; a constraint on a model nobody serves never reaches no_match

  Scenario: PROPOSED - order prefers listed stations for the first claim window, then any
    When "c1" posts {"model": "qwen3-32b", "provider": {"order": ["s2"]}, "messages": [...]}
    Then for the first poll interval only "s2" may claim
    And after it, if unclaimed, any station serving the model may claim (allow_fallbacks default true)

  Scenario: PROPOSED - order with allow_fallbacks:false never widens past the list
    When "c1" posts {"model": "qwen3-32b", "provider": {"order": ["s2"], "allow_fallbacks": false}, "messages": [...]}
    And "s2" never polls
    Then "s1" never receives the job and the response is the approved 504

  Scenario: PROPOSED - allow_fallbacks:false alone means the first claimant only, which is already true
    When "c1" posts {"model": "qwen3-32b", "provider": {"allow_fallbacks": false}, "messages": [...]}
    Then the behavior equals a plain request (one claim, no re-dispatch exists on the plane)
    And X-Roger-Routing-Ignored is absent (the key was honored trivially)

  Scenario: A station id that is not attached is not an error for ignore
    When "c1" posts {"model": "qwen3-32b", "provider": {"ignore": ["nope"]}, "messages": [...]}
    Then the request is served normally

  Scenario: Station ids in only/order are local attach ids, never public node ids or Tower relay ids
    When "c1" posts {"model": "qwen3-32b", "provider": {"only": ["<a public node id>"]}, "messages": [...]}
    Then the response is the 503 no_match naming the constraint
    And nothing about the public network is consulted or revealed

  Scenario: The carriers are stripped before the station's job body
    When "c1" posts {"model": "qwen3-32b", "provider": {"only": ["s2"]}, "roger": {"pref": "fast"}, "messages": [...]}
    Then the station's job body has neither "provider" nor "roger"
    And every other field is byte-identical

  # --- ignored keys are named, every time ------------------------------------------------

  Scenario Outline: A key the plane cannot evaluate is ignored and named in the header
    When "c1" posts {"model": "qwen3-32b", <carrier>, "messages": [...]}
    Then the request is served by a station serving "qwen3-32b"
    And the response carries X-Roger-Routing-Ignored: <ignored>

    Examples:
      | carrier                                                | ignored                                            |
      | "provider": {"sort": "price"}                          | provider.sort                                      |
      | "provider": {"quantizations": ["Q8_0"]}                | provider.quantizations                             |
      | "provider": {"max_price": {"completion": 1}}           | provider.max_price.completion                      |
      | "provider": {"max_price": {"prompt": 1, "request": 1}} | provider.max_price.prompt,provider.max_price.request |
      | "provider": {"require_parameters": true}               | provider.require_parameters                        |
      | "roger": {"pref": "fast"}                              | roger.pref                                         |
      | "roger": {"require": ["tools"]}                        | roger.require                                      |
      | "roger": {"params_b": [7, 70]}                         | roger.params_b                                     |
      | "roger": {"min_ctx": 32768}                            | roger.min_ctx                                      |
      | "roger": {"min_tps": 20}                               | roger.min_tps                                      |
      | "roger": {"max_ttft_ms": 1500}                         | roger.max_ttft_ms                                  |
      | "roger": {"self_hosted_only": true}                    | roger.self_hosted_only                             |
      | "roger": {"region": ["eu"]}                            | roger.region                                       |
      | "roger": {"freq": "147.520 MHz AAAA-AAAA"}             | roger.freq                                         |

  Scenario: Several ignored keys are listed once each, sorted, comma-separated
    When "c1" posts {"model": "qwen3-32b", "provider": {"sort": "price"}, "roger": {"pref": "fast", "region": ["eu"]}, "messages": [...]}
    Then the response carries X-Roger-Routing-Ignored: provider.sort,roger.pref,roger.region

  Scenario: Honored keys are never listed as ignored
    When "c1" posts {"model": "qwen3-32b", "provider": {"only": ["s2"], "sort": "price"}, "messages": [...]}
    Then X-Roger-Routing-Ignored equals "provider.sort" exactly

  Scenario: Variant sugar is ignored, named, and the bare id is used
    When "c1" posts {"model": "qwen3-32b:free", "messages": [...]}
    Then the job is submitted for "qwen3-32b"
    And X-Roger-Routing-Ignored includes "model:free"
    And X-RogerAI-Model is "qwen3-32b"

  Scenario: A @profile/ model cannot be resolved by the plane and is a 400 naming it
    When "c1" posts {"model": "@profile/coding", "messages": [...]}
    Then the response is 400 with error code "unknown_profile" and message "profiles resolve on the client; send the model"
    And nothing is queued

  Scenario: roger.profile is the same 400 unknown_profile, never an ignored key
    When "c1" posts {"model": "qwen3-32b", "roger": {"profile": "@profile/coding"}, "messages": [...]}
    Then the response is 400 with error code "unknown_profile"
    And X-Roger-Routing-Ignored is absent
    And nothing is queued

  # --- confidential / trust_min are evaluated, never ignored ----------------------------------

  Scenario: roger.confidential true finds no attested local station and is a 503 no_match
    # The attach registry records no attestation, so nothing local can satisfy a TEE requirement.
    # Serving an unattested station and naming the key in a header would weaken a stated
    # restriction (CONTRACT §1a, §5a); the plane refuses instead.
    When "c1" posts {"model": "qwen3-32b", "roger": {"confidential": true}, "messages": [...]}
    Then the response is 503 with error code "no_match" and message "no attested local station serves qwen3-32b"
    And X-Roger-Routing-Ignored is absent
    And nothing is queued

  Scenario: roger.trust_min "confidential" is the same refusal
    When "c1" posts {"model": "qwen3-32b", "roger": {"trust_min": "confidential"}, "messages": [...]}
    Then the response is 503 with error code "no_match" and message "no attested local station serves qwen3-32b"

  Scenario: roger.trust_min "verified" finds no canary-verified local station and is a 503 no_match
    # No canary runs on the plane, so no local station is ever "verified" in the contract's sense.
    When "c1" posts {"model": "qwen3-32b", "roger": {"trust_min": "verified"}, "messages": [...]}
    Then the response is 503 with error code "no_match" and message "no verified local station serves qwen3-32b"

  Scenario: roger.trust_min "any" and roger.confidential false change nothing and are not named as ignored
    When "c1" posts {"model": "qwen3-32b", "roger": {"trust_min": "any", "confidential": false}, "messages": [...]}
    Then the request is served by a station serving "qwen3-32b"
    And X-Roger-Routing-Ignored is absent

  Scenario: An X-Roger-Confidential header is the same refusal on the plane
    When "c1" posts {"model": "qwen3-32b", "messages": [...]} with header X-Roger-Confidential "1"
    Then the response is 503 with error code "no_match" and message "no attested local station serves qwen3-32b"

  Scenario: The ignored header is present on the 404, 503 and 504 paths too when keys were ignored
    When "c1" posts {"model": "gpt-4o", "roger": {"pref": "fast"}, "messages": [...]}
    Then the response is the approved 404 and carries X-Roger-Routing-Ignored: roger.pref

  Scenario: The ignored header never echoes a value, only key names
    When "c1" posts {"model": "qwen3-32b", "roger": {"freq": "147.520 MHz 8F3K-9M2Q"}, "messages": [...]}
    Then X-Roger-Routing-Ignored is "roger.freq"
    And "8F3K-9M2Q" appears in no header, log line, or receipt

  # --- validation on the plane ----------------------------------------------------------------

  Scenario Outline: Malformed carriers are a 400 before anything is queued, with the key named
    When "c1" posts {"model": "qwen3-32b", <carrier>, "messages": [...]}
    Then the response is 400 naming "<key>"
    And nothing is queued

    Examples:
      | carrier                            | key             |
      | "provider": "s1"                   | provider        |
      | "provider": {"only": "s1"}         | provider.only   |
      | "provider": {"only": []}           | provider.only   |
      | "provider": {"only": [""]}         | provider.only   |
      | "provider": {"order": [1]}         | provider.order  |
      | "provider": {"allow_fallbacks": 1} | provider.allow_fallbacks |
      | "models": "qwen3-32b"              | models          |
      | "models": [""]                     | models          |
      | "roger": []                        | roger           |

  Scenario: Unknown keys under a carrier are a 400 here too (no silent drop)
    When "c1" posts {"model": "qwen3-32b", "provider": {"foo": 1}, "messages": [...]}
    Then the response is 400 "unknown routing key provider.foo"

  Scenario: A 400 routing error is not a rate-limit spend beyond the request itself
    When "c1" posts a malformed carrier
    Then the per-client token spent is exactly one, as for any request

  Scenario: The body size bound applies to the whole body including carriers
    When "c1" posts a body over the plane's size bound
    Then the approved size refusal is returned

  # --- receipts and bookkeeping ------------------------------------------------------------

  Scenario: The local receipt records the served model, not the requested primary
    When "c1" posts {"model": "gpt-4o", "models": ["qwen3-32b"], "messages": [...]}
    Then the recorded receipt names model "qwen3-32b" and the serving station

  Scenario: Receipts stay free bookkeeping with no price field added
    When any routed request is served
    Then the receipt shape is unchanged from standalone_consumer_plane.feature

  Scenario: A disconnect after a station delivered still records the receipt with the served model
    When "c1" disconnects after "s1" delivered a models[] fallback answer
    Then the receipt names the served model

  # --- no-Core guarantee ------------------------------------------------------------------------

  Scenario: Routing keys never cause an outbound connection
    When "c1" posts a body carrying every routing key
    Then the no-egress gate still holds by linkage
    And no name is resolved and no socket is dialed

  Scenario: Public identity in a routing body is ignored and never reflected
    When "c1" posts {"model": "qwen3-32b", "roger": {"freq": "…"}, "provider": {"only": ["<public node id>"]}, "messages": [...]}
    Then no header or body echoes the freq or the public id
    And the response is the constraint 503 no_match (only) with the ignored header listing roger.freq

  # --- the standalone smoke check ---------------------------------------------------------------

  Scenario: The standalone smoke script exercises models[] and only
    When the standalone smoke check runs against a fresh Tower with two stations
    Then it posts one models[] request and one only request
    And it asserts X-RogerAI-Model, X-Roger-Cost: 0 and X-Roger-Routing-Ignored on an ignored-key request
    And the check fails if the station's job body still carries "provider" or "roger"
