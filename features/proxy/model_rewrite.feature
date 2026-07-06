# GUEST OPERATORS — Phase 1 proxy hardening (money-path, spec-first).
#
# MODEL-FIELD REWRITE — an external agent ships with its own default model name
# ("gpt-4o", "claude-sonnet", "gpt-4o-mini", …). Today the proxy forwards whatever the
# body carries (client.go:397-402 reads body.model into Criteria.Model verbatim), so the
# broker looks for a station serving "gpt-4o", finds none, and the agent dies with
# "no provider available for gpt-4o". The fix: the proxy REWRITES body.model to the tuned
# band's model before relay, so ANY incoming model name just works, while EVERY other body
# field is preserved byte-for-byte.
#
# GROUND TRUTH: the rewrite target is the band model carried on ProxyOptions.Model (the same
# new seam /v1/models uses). The rewrite happens in the /v1/chat/completions handler before
# relayWithFailover(...body...). Criteria.Model must ALSO become the band model so failover
# re-discovery matches the real band.
#
# INVARIANT: rewrite ONLY the top-level "model" field. temperature, top_p, tools,
# tool_choice, messages, stream, max_tokens, response_format, and any unknown/extra fields
# pass through unchanged and in a still-valid JSON body.
#
# FOUNDER RULING NEEDED: malformed-JSON body → the current code silently ignores the
# Unmarshal error and relays garbage. Proposed: reject with an OpenAI-shaped 400
# ("invalid_request_error") BEFORE any relay/hold, so a broken client never spends. Confirm
# the 400 (vs. best-effort relay).
#
# EXECUTABLE: step defs deferred to post-approval. RED evidence:
# TestProxyRewritesModelToBand and TestProxyMalformedBody400 in proxy_hardening_test.go.

Feature: Local proxy rewrites the request model to the tuned band

  Background:
    Given a tuned band whose model is "qwen3-32b-fp8"
    And the local proxy is bound to that band

  Scenario Outline: Any incoming model name relays as the band's model
    When a chat request arrives with model "<incoming>"
    Then the broker receives model "qwen3-32b-fp8"

    Examples:
      | incoming              |
      | gpt-4o                |
      | gpt-4o-mini           |
      | claude-sonnet         |
      | claude-3-5-sonnet     |
      | o1-preview            |
      | qwen3-32b-fp8         |
      | some/random-provider  |

  Scenario: An empty model string still relays as the band's model
    When a chat request arrives with model ""
    Then the broker receives model "qwen3-32b-fp8"

  Scenario: A missing model field still relays as the band's model
    When a chat request arrives with no model field
    Then the broker receives model "qwen3-32b-fp8"

  Scenario: The already-correct band name is preserved (idempotent rewrite)
    When a chat request arrives with model "qwen3-32b-fp8"
    Then the broker receives model "qwen3-32b-fp8"

  Scenario: All other body fields survive the rewrite untouched
    When a chat request arrives with model "gpt-4o" and extra fields:
      | field           | value                          |
      | temperature     | 0.7                            |
      | top_p           | 0.9                            |
      | max_tokens      | 512                            |
      | messages        | [{"role":"user","content":"hi"}]|
    Then the broker receives model "qwen3-32b-fp8"
    And the broker receives temperature 0.7
    And the broker receives top_p 0.9
    And the broker receives max_tokens 512
    And the broker receives the same messages array

  Scenario: A tools / tool_choice request keeps its tool schema after rewrite
    When a chat request arrives with model "gpt-4o" carrying a tools array and tool_choice "auto"
    Then the broker receives model "qwen3-32b-fp8"
    And the tools array and tool_choice are preserved unchanged

  Scenario: A streaming request stays a streaming request after rewrite
    When a chat request arrives with model "gpt-4o" and "stream": true
    Then the broker receives model "qwen3-32b-fp8"
    And the broker receives "stream": true

  Scenario: Unknown / vendor-extension fields pass through unchanged
    When a chat request arrives with model "gpt-4o" and an unknown field "x_vendor_flag": 1
    Then the broker receives model "qwen3-32b-fp8"
    And the unknown field "x_vendor_flag" is preserved with value 1

  # Criteria used for failover re-discovery must ALSO be the band model, not the client's.
  Scenario: Failover re-discovery matches the band model, not the client's model
    Given the first-picked station fails with a retryable 503
    When a chat request arrives with model "gpt-4o"
    Then failover re-discovers stations for model "qwen3-32b-fp8"

  # Adversarial / boundary bodies.
  Scenario: A malformed JSON body is rejected OpenAI-shaped 400 before any spend
    When a chat request arrives with the raw body "{ this is not json"
    Then the status is 400
    And the body is an OpenAI error with type "invalid_request_error"
    And the broker was never called (no hold, no spend)

  Scenario: An empty body is rejected OpenAI-shaped 400 before any spend
    When a chat request arrives with the raw body ""
    Then the status is 400
    And the body is an OpenAI error with type "invalid_request_error"
    And the broker was never called (no hold, no spend)

  Scenario: A body whose top-level JSON is not an object is rejected 400
    When a chat request arrives with the raw body "[1,2,3]"
    Then the status is 400
    And the body is an OpenAI error with type "invalid_request_error"
