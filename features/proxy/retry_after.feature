# GUEST OPERATORS — Phase 1 proxy hardening (money-path, spec-first).
#
# RETRY-AFTER PASSTHROUGH — the broker rate-limits at 120 RPM/identity, LOCAL per instance
# (design doc §8). When it 429s it sends a `Retry-After` header telling the client how long to
# back off. Today copyRelayResponse (client.go:535-561) copies a FIXED allowlist —
# X-RogerAI-Provider, X-RogerAI-Cost, X-RogerAI-Balance, X-RogerAI-Receipt, X-RogerAI-Price,
# X-RogerAI-TPS — and DROPS Retry-After. A well-behaved agent (and the OpenAI SDK's retry
# logic) reads Retry-After to pace itself; without it, parallel guest subagents hammer the
# limiter and spin. Fix: add Retry-After to the response-header allowlist. Keep the allowlist
# tight — unsafe / hop-by-hop / connection-scoped headers must NOT be forwarded.
#
# GROUND TRUTH: copyRelayResponse sets Content-Type then loops the allowlist (client.go:536-545).
# 429 is already NON-retryable in the failover sense (retryable() returns false for 429,
# failover_test.go:358) so a 429 is passed straight back — but WITHOUT its Retry-After today.
#
# SAFETY: never forward hop-by-hop headers (RFC 7230 §6.1: Connection, Keep-Alive,
# Proxy-Authenticate, Proxy-Authorization, TE, Trailer, Transfer-Encoding, Upgrade) or a
# broker-side Set-Cookie / Server-internal header. The allowlist stays explicit — deny by
# default, allow the few safe ones.
#
# FOUNDER RULING NEEDED: the final safe-to-forward allowlist. Proposed additions beyond today:
# Retry-After (required). Confirm nothing else (e.g. no ETag/Cache-Control) needs to pass for
# the agent CLIs in scope.
#
# EXECUTABLE: step defs deferred. RED evidence: TestProxyForwardsRetryAfter and
# TestProxyDropsUnsafeHeaders in proxy_hardening_test.go — the Retry-After assertion is RED
# today (dropped); the hop-by-hop-not-leaked assertion already passes (tight allowlist) and is
# pinned as a regression guard.

Feature: Local proxy preserves Retry-After and only safe response headers

  Background:
    Given a tuned band whose model is "qwen3-32b-fp8"
    And the local proxy is bound to that band

  Scenario: A broker 429 forwards Retry-After to the agent
    Given the broker returns 429 with Retry-After "30"
    When a chat request is made
    Then the status is 429
    And the client sees Retry-After "30"

  Scenario: A 429 with a date-form Retry-After forwards it verbatim
    Given the broker returns 429 with Retry-After "Wed, 21 Oct 2026 07:28:00 GMT"
    When a chat request is made
    Then the status is 429
    And the client sees Retry-After "Wed, 21 Oct 2026 07:28:00 GMT"

  Scenario: A 429 body stays OpenAI-shaped rate_limit_error
    Given the broker returns 429 with an OpenAI rate-limit error body and Retry-After "5"
    When a chat request is made
    Then the body is an OpenAI error with type "rate_limit_error"
    And the client sees Retry-After "5"

  Scenario: The existing safe meter headers still pass through
    Given the broker returns 200 with X-RogerAI-Cost "0.10" and X-RogerAI-Provider "node-7"
    When a chat request is made
    Then the client sees X-RogerAI-Cost "0.10"
    And the client sees X-RogerAI-Provider "node-7"
    And the client sees the broker's Content-Type

  Scenario Outline: Hop-by-hop and connection-scoped headers are NOT forwarded
    Given the broker returns 200 and also sets "<header>" to "<value>"
    When a chat request is made
    Then the client does NOT see the "<header>" header

    Examples:
      | header             | value        |
      | Connection         | close        |
      | Keep-Alive         | timeout=5    |
      | Transfer-Encoding  | chunked      |
      | Upgrade            | h2c          |
      | Proxy-Authenticate | Basic        |
      | Set-Cookie         | sid=abc      |
      | Server             | broker/1.2.3 |

  Scenario: Retry-After is only forwarded when the broker sent one (not synthesized)
    Given the broker returns 429 with NO Retry-After header
    When a chat request is made
    Then the status is 429
    And the client sees no Retry-After header
