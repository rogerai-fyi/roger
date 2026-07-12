Feature: Reasoning-model streamed output is recognized, never false-voided or false-struck
  # THE BUG: the STREAM relay's sseDelta() parsed only delta.content/delta.text and DROPPED
  # delta.reasoning / delta.reasoning_content / delta.thinking. A reasoning-model stream then
  # captured an EMPTY completion, so producedUsableOutput() returned false and the request was
  # VOIDED with an empty-output strike. Worse, a reasoning+content reply passed the empty check
  # but the reasoning-STRIPPED completion re-counted far fewer tokens than the node claimed, so
  # it ALSO tripped a recount-discrepancy strike - a SECOND distinct signal class - which
  # satisfied strike corroboration and AUTO-BANNED the honest reasoning node.
  #
  # THE FIX: one SHARED detector (producedUsableOutput) used by BOTH the stream and non-stream
  # paths, fed by capture functions (sseDelta / completionText) that fold EVERY thinking-model
  # output signal. Output is recognized as an OR-of-all, accumulated to end-of-stream:
  #   - any content, or any reasoning alias (reasoning / reasoning_content / thinking),
  #   - inline reasoning tags in content (<think>, harmony <|channel|>analysis/final),
  #   - a tool_call / function_call, a non-empty refusal, OR
  #   - the usage backstop: completion_tokens > 0 on the final chunk.
  # It VOIDS + strikes ONLY the TRUE-negative: no text AND completion_tokens == 0. The re-count
  # counts reasoning too, so a reasoning+content stream never trips recount-discrepancy.
  #
  # Driven against the REAL streaming relay (a node goroutine streams real SSE deltas to
  # /agent/stream, then posts a signed receipt) with a real broker + a real tokenizer sidecar.
  # The bus (multi-instance) capture path shares the SAME drainSSEDeltas function as the local
  # path (unit-pinned in reasoning_detector_test.go), so this fix covers both.

  Background:
    Given a reasoning-capable node registered with the broker
    And a funded consumer

  Scenario: INCIDENT - a stream of reasoning deltas then a content delta is served, not voided or struck
    Given the node streams 4 reasoning deltas then 1 content delta
    And the node's receipt claims the full reasoning+content token count
    When the consumer relays the streaming request
    Then the stream is served and settled for a non-zero cost
    And the node earns for the request
    And the node's owner is NOT struck

  Scenario: reasoning-only with finish_reason=length is billed off the usage, not voided
    Given the node streams reasoning deltas only with an empty content and finish_reason length
    And the node's receipt claims the reasoning token count
    When the consumer relays the streaming request
    Then the stream is served and settled for a non-zero cost
    And the node's owner is NOT struck

  Scenario: usage backstop - empty deltas but completion_tokens>0 is served, not voided or struck
    Given the node streams no output text but its receipt claims 5 completion tokens
    When the consumer relays the streaming request
    Then the stream is served and settled for a non-zero cost
    And the node's owner is NOT struck

  Scenario: anti-fraud - an unverifiable text-less completion claim is NOT paid
    # The usage backstop keeps an honest reasoning node from being false-struck, but a
    # re-count-enabled broker never PAYS a completion it could not capture and count: the
    # empty-capture guard bills the unverifiable completion 0, so a huge text-less claim
    # cannot mint output revenue.
    Given the node streams no output text but its receipt claims 100000 completion tokens
    When the consumer relays the streaming request
    Then the node's owner is NOT struck
    And the consumer is not billed for the unverifiable completion claim

  Scenario: the verdict is computed at END of stream - content only in the last delta still serves
    Given the node streams 3 reasoning deltas and puts the only content in the last delta
    And the node's receipt claims the full reasoning+content token count
    When the consumer relays the streaming request
    Then the stream is served and settled for a non-zero cost
    And the node's owner is NOT struck

  Scenario: ESCALATION GUARD - an honest reasoning node is never auto-banned
    Given the node serves 3 reasoning-only and 3 reasoning+content honest streams
    When the consumer relays each streaming request
    Then the node's owner is NOT banned
    And the node's owner has no strikes of any kind

  Scenario: TRUE-NEGATIVE - a genuinely empty stream with zero tokens is still voided and struck
    Given the node streams no output text and its receipt claims 0 completion tokens
    When the consumer relays the streaming request
    Then the stream is voided at zero cost with the hold refunded
    And the node's owner IS struck for empty output

  Scenario: the idle/void timer resets on a reasoning delta so a long think never trips a stall
    Given a short idle window and the node streams reasoning deltas spaced across more than that window
    When the consumer relays the streaming request
    Then the stream is served and settled for a non-zero cost
    And the node's owner is NOT struck
