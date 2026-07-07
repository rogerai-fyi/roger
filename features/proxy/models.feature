# GUEST OPERATORS — Phase 1 proxy hardening (money-path, spec-first).
#
# /v1/models — external agent CLIs (opencode, Crush, Continue, the OpenAI SDKs) probe
# GET /v1/models at startup to learn what to talk to. Today ProxyHandler
# (internal/client/client.go:390) is a SINGLE-ROUTE mux (only /v1/chat/completions), so
# /v1/models 404s with Go's plain-text "404 page not found" and those agents abort before
# the first prompt. This spec adds the endpoint in OpenAI list shape.
#
# GROUND TRUTH: ProxyHandler currently derives the model PER-REQUEST from the body
# (client.go:397-400) — it has NO notion of "the band's model". So /v1/models needs a NEW
# source of truth: the tuned band's model must be carried on ProxyOptions (proposed field
# ProxyOptions.Model, set by Use()/openChannel from the resolved band). That is the seam
# this behavior introduces.
#
# OpenAI list shape (the contract agents decode):
#   {"object":"list","data":[{"id":"<model>","object":"model","created":<unix>,"owned_by":"rogerai"}]}
#
# FOUNDER RULINGS NEEDED (see report):
#   - When the proxy serves ONE band (the only case today — openChannel binds after a single
#     tune-in), /v1/models lists exactly that band's model. AGREED default.
#   - "Multiple session bands": the proxy is bound ONCE per session to one ProxyOptions; a
#     re-tune re-points it (see live_options.feature). Decision: /v1/models reflects the
#     CURRENTLY-tuned band only (one entry), never a stale union. Confirm.
#   - owned_by string ("rogerai" proposed) and whether `created` is a real bind time or 0.
#
# EXECUTABLE: step definitions deferred to post-approval per CLAUDE.md (approve the spec
# before step defs). RED evidence for this phase is the table test
# TestProxyModelsEndpoint in internal/client/proxy_hardening_test.go.

Feature: Local proxy /v1/models endpoint

  Background:
    Given a tuned band whose model is "qwen3-32b-fp8"
    And the local proxy is bound to that band

  Scenario: A probe returns the tuned band's model in OpenAI list shape
    When an agent sends GET "/v1/models" with the session key
    Then the status is 200
    And the Content-Type is "application/json"
    And the body is {"object":"list"} with a data array
    And data[0].id is "qwen3-32b-fp8"
    And data[0].object is "model"
    And data[0].owned_by is "rogerai"

  Scenario: The list reflects the currently-tuned band, not a hardcoded name
    Given a tuned band whose model is "gpt-oss-120b"
    And the local proxy is bound to that band
    When an agent sends GET "/v1/models" with the session key
    Then data[0].id is "gpt-oss-120b"

  Scenario: Exactly one entry — the proxy relays to one band per session
    When an agent sends GET "/v1/models" with the session key
    Then the data array has exactly 1 entry

  Scenario: After a re-tune the list follows the new band (no stale model)
    Given the band is re-tuned from "qwen3-32b-fp8" to "llama-3.3-70b"
    When an agent sends GET "/v1/models" with the session key
    Then data[0].id is "llama-3.3-70b"
    And "qwen3-32b-fp8" does not appear in the list

  # Auth is enforced on /v1/models too (see auth.feature) — a probe with no key is refused.
  Scenario: A probe with no bearer key is refused OpenAI-shaped 401
    When an agent sends GET "/v1/models" with no Authorization header
    Then the status is 401
    And the body is an OpenAI error with type "authentication_error"

  Scenario: A probe with the wrong bearer key is refused
    When an agent sends GET "/v1/models" with the bearer key "roger-local"
    Then the status is 401
    And the body is an OpenAI error with type "authentication_error"

  Scenario Outline: Only GET is a models probe; other methods are not the models list
    When an agent sends "<method>" "/v1/models" with the session key
    Then the status is not 200 with a models list body

    Examples:
      | method |
      | POST   |
      | PUT    |
      | DELETE |

  # Regression corner: the OLD behavior (the bug this closes) — plain-text 404 that crashes
  # SDK JSON decoders — must never come back on this route.
  Scenario: The endpoint never falls through to Go's plain-text 404
    When an agent sends GET "/v1/models" with the session key
    Then the response body is valid JSON
    And the body is not the plain text "404 page not found"
