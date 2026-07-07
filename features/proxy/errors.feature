# GUEST OPERATORS — Phase 1 proxy hardening (money-path, spec-first).
#
# OPENAI-SHAPED JSON ERRORS ON EVERY PATH — the OpenAI SDKs (and every agent built on them)
# JSON-DECODE the response body on a non-2xx and read error.message / error.type. Today the
# proxy emits Go PLAIN TEXT on its failure paths:
#   - unknown route → http.NotFound = "404 page not found\n" (single-route mux, client.go:393)
#   - relay exhausted → http.Error(w, msg, 502) = a bare text line (client.go:500)
# An SDK does json.loads() on those and throws a decode error, so the agent dies with an
# opaque stack trace instead of a clean, actionable message. Every non-2xx the proxy
# ORIGINATES must be {"error":{"message":...,"type":...,"code":...}} with the right status and
# Content-Type: application/json. Errors the BROKER already returns OpenAI-shaped (402 topup,
# moderation denials) pass through with their shape intact (see also retry_after.feature).
#
# GROUND TRUTH: relayWithFailover ends in http.Error (client.go:500). ProxyHandler's mux
# has no catch-all. copyRelayResponse forwards the broker's body+Content-Type as-is
# (client.go:535-561), so a broker OpenAI error already reaches the client shaped — the gap is
# the errors the PROXY itself generates.
#
# FOUNDER RULING NEEDED — the exact type/code strings (proposed, aligned to OpenAI):
#   | path                | status | type                   | code                    |
#   | unknown route       | 404    | invalid_request_error  | unknown_url             |
#   | malformed body      | 400    | invalid_request_error  | (none)                  |
#   | missing/bad bearer  | 401    | authentication_error   | (none)                  |
#   | budget exceeded     | 402    | insufficient_quota     | budget_exceeded         |
#   | body over cap       | 413    | invalid_request_error  | request_too_large       |
#   | rate limited (429)  | 429    | rate_limit_error       | (broker's)              |
#   | relay exhausted     | 502    | api_error              | upstream_unavailable    |
# Confirm these strings — agents key retry/backoff logic off type, so they are a contract.
#
# EXECUTABLE: step defs deferred. RED evidence: TestProxyUnknownRouteJSON and
# TestProxyRelayFailureJSON in proxy_hardening_test.go (both RED today — plain text).

Feature: Local proxy returns OpenAI-shaped JSON errors on every originated path

  Background:
    Given a tuned band whose model is "qwen3-32b-fp8"
    And the local proxy is bound to that band

  Scenario: An unknown route returns an OpenAI-shaped 404 (not Go's plain text)
    When an agent sends GET "/v1/embeddings" with the session key
    Then the status is 404
    And the Content-Type is "application/json"
    And the body is an OpenAI error with type "invalid_request_error"
    And the body is not the plain text "404 page not found"

  Scenario Outline: Every unknown route is JSON, never plain text
    When an agent sends GET "<path>" with the session key
    Then the status is 404
    And the response body is valid JSON with an "error" object

    Examples:
      | path            |
      | /               |
      | /v1             |
      | /v1/responses   |
      | /v1/embeddings  |
      | /healthz        |
      | /favicon.ico    |

  Scenario: A relay exhaustion returns an OpenAI-shaped 502 (not a bare text line)
    Given every matching station returns a retryable 503
    When a chat request is made
    Then the status is 502
    And the Content-Type is "application/json"
    And the body is an OpenAI error with type "api_error"
    And error.message names the failure in a human-readable way

  Scenario: A broker-unreachable failure returns an OpenAI-shaped 502
    Given the broker is unreachable
    When a chat request is made
    Then the status is 502
    And the body is an OpenAI error with type "api_error"

  Scenario: An upstream 402 topup passes through OpenAI-shaped (broker already shapes it)
    Given the broker returns a 402 with an OpenAI error body
    When a chat request is made
    Then the status is 402
    And the body is an OpenAI error the SDK can decode
    And the topup next-step is present

  Scenario: A moderation denial from the broker reaches the agent as clean JSON
    Given moderation is required and the broker denies a prompt with an OpenAI error body
    When a chat request is made
    Then the status is the broker's status
    And the body is an OpenAI error with a human-readable message
    # design doc §8: map moderation denials to clean JSON, never a bare 4xx text

  Scenario: The malformed-body 400, budget 402, 401, and 413 are all OpenAI-shaped
    # cross-references — each lives in its own feature, asserted here as a single shape rule
    Then every error the proxy originates has Content-Type "application/json"
    And every such body has an "error" object with a "message" and a "type"

  # Boundary: an error body must be complete JSON even when the message contains quotes/newlines.
  Scenario: Error messages with special characters stay valid JSON
    Given a relay failure whose message contains a quote and a newline
    When a chat request is made
    Then the response body parses as valid JSON
    And error.message round-trips the special characters
