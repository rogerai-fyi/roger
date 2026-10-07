# STATUS: PROPOSED 2026-10-07 (awaiting founder approval)
#
# STANDALONE TOWER STREAMING: a streamed request through a LAN Tower arrives as a stream.
#
# WHY THIS EXISTS
#
# FOUNDER RULING 2026-10-06: "build it" - streaming for roger-tower-local. Today the Tower path
# is one-shot: localplane queues the job (internal/localplane/completion.go chatCompletions),
# the station long-polls /local/poll, agent/localtower.go runLocalJob FORCES "stream":false
# (unstreamLocal) and posts the whole answer to /local/complete, and writeAnswer writes it
# once. A streamed request waits for the whole reply. features/lan/routing_and_inference.feature
# scenario 102 ("a mid-stream failure is not retried somewhere the caller cannot see") is
# tagged @blocked-on-tower-streaming until this file is built.
#
# THE RULES OF THIS FILE
#   1. The Tower still dials NOBODY. The station connects IN with each chunk; the structural
#      gates (no towerjoin/towercore/towerhub linked, no outbound call in internal/localplane)
#      hold unchanged.
#   2. Every station chunk is authenticated exactly like /local/complete: a signed request from
#      an ATTACHED station, with the nonce replay guard and the station rate limit, and every
#      refusal is the same uniform 401. A chunk is accepted only from the station that took the
#      job.
#   3. Chunks are sequenced. The Tower forwards chunk n only after chunk n-1; a duplicate, an
#      out-of-order, or a replayed chunk is refused and never reaches the consumer.
#   4. Once the first byte has reached the consumer, the reply is never silently restarted:
#      a broken stream ends with an error frame and a zero-cost usage chunk (the market relay's
#      rule).
#   5. A non-stream request is byte-for-byte what it is today.
#
# GROUND TRUTH: internal/localplane (completion.go, queue.go, plane.go, limits.go, replay.go,
# structural_test.go), internal/agent/localtower.go (pollLocalJob, runLocalJob, unstreamLocal,
# completeLocalJob, signLocal), internal/client/lan_relay.go (the Tower rung).
#
# Tags: @tower = internal/localplane; @station = internal/agent; @client = internal/client;
# @structural = the dependency and source-scan gates.

Feature: A standalone Tower relays a station's streamed reply to the consumer as it is produced

  Background:
    Given a standalone Tower serving its LAN with stations attached by key
    And an admitted client
    And an attached station "shed" that serves "qwen3-8b" from an upstream that can stream

  # =========================================================================
  # 1. THE HAPPY PATH
  # =========================================================================

  @tower @station
  Scenario: a streamed request is answered as an SSE stream, chunk by chunk
    When the client posts a chat completion for "qwen3-8b" with "stream": true
    Then the response has Content-Type "text/event-stream"
    And it carries X-Roger-Cost "0" and X-Roger-Local "1" before the first event
    And the consumer receives the first content event before the station has finished
    And the events arrive in the order the upstream produced them
    And the stream ends with a usage chunk and "data: [DONE]"

  @tower @station
  Scenario: the station keeps the request's stream flag when the job asks for a stream
    When the client posts a chat completion for "qwen3-8b" with "stream": true
    Then the station's upstream request has "stream": true
    # Today unstreamLocal forces false for every job; it keeps doing so only for a non-stream job.

  @tower @station
  Scenario: a non-stream request is unchanged
    When the client posts a chat completion for "qwen3-8b" without "stream"
    Then the station's upstream request has "stream": false
    And the station returns the whole answer to /local/complete
    And the consumer receives one JSON answer with Content-Type "application/json"
    And the bytes are identical to what the Tower returns today

  @tower @station
  Scenario Outline: only a true stream flag streams
    When the client posts a chat completion for "qwen3-8b" with stream set to <value>
    Then the consumer receives <shape>

    Examples:
      | value     | shape                      |
      | true      | an SSE stream              |
      | false     | one JSON answer            |
      | "true"    | one JSON answer            |
      | 1         | one JSON answer            |
      | null      | one JSON answer            |
    # Only a JSON boolean true asks for a stream, the same reading the broker's relay uses.

  @tower
  Scenario: the local receipt is written once, when the stream completes
    When a streamed request completes
    Then exactly one local receipt is recorded for the client, the station and "qwen3-8b"
    And it is bookkeeping only, never a billing shape

  @tower
  Scenario: a curated station's stream carries the curated mark
    Given the station "shed" is attached as curated
    When a streamed request completes
    Then the response carried X-Roger-Curated before the first event

  # =========================================================================
  # 2. THE STATION CHUNK ROUTE: AUTHENTICATION
  # =========================================================================

  @tower
  Scenario: the station posts each chunk to the new chunk route, signed
    When a streamed job is served
    Then each chunk arrives on "/local/chunk" as a signed request
    And each carries the job id, a sequence number, and the chunk bytes
    And the last carries "final": true

  @tower
  Scenario: an unsigned chunk is the uniform 401
    When a chunk for a live job arrives without a signature
    Then the Tower answers the byte-identical uniform 401
    And nothing reaches the consumer

  @tower
  Scenario: a chunk signed by an unattached key is the uniform 401
    When a chunk for a live job arrives signed by a key that is not an attached station
    Then the Tower answers the byte-identical uniform 401
    And nothing reaches the consumer

  @tower
  Scenario: a chunk from a different attached station is refused
    Given a second attached station "garage"
    When "garage" posts a chunk for the job "shed" took
    Then the Tower answers the byte-identical uniform 401
    And nothing reaches the consumer
    And "shed" can still complete the job

  @tower
  Scenario: a chunk for an unknown or finished job is refused
    When a station posts a chunk for a job id that does not exist
    Then the Tower answers the byte-identical uniform 401
    # Uniform: the response reveals nothing about which job ids exist.

  @tower
  Scenario: the uniform 401 from the chunk route matches every other route
    When an unauthenticated request is sent to "/local/chunk", "/local/complete", "/local/poll" and "/v1/chat/completions"
    Then all four answers are byte-identical
    # So a probe cannot fingerprint the new route.

  # =========================================================================
  # 3. SEQUENCE, REPLAY, DUPLICATES
  # =========================================================================

  @tower
  Scenario: chunks are forwarded strictly in sequence
    When the station posts chunks 0, 1 and 2 in order
    Then the consumer receives chunk 0, then 1, then 2

  @tower
  Scenario: an out-of-order chunk is refused and not forwarded
    When the station posts chunk 0 and then chunk 2
    Then chunk 2 is refused with a 409 naming the expected sequence 1
    And the consumer has received only chunk 0

  @tower
  Scenario: a duplicate chunk is refused and not forwarded twice
    When the station posts chunk 0 twice with fresh nonces
    Then the second is refused with a 409
    And the consumer receives chunk 0 exactly once

  @tower
  Scenario: a captured chunk request replayed is the uniform 401
    When a chunk request is captured and replayed with the same nonce
    Then the replay is the byte-identical uniform 401
    And the consumer receives that chunk exactly once

  @tower
  Scenario: a chunk after the final chunk is refused
    When the station posts the final chunk and then another chunk
    Then the extra chunk is refused
    And the consumer's stream already ended with "data: [DONE]"

  @tower
  Scenario: /local/complete for a streamed job is refused from a station that announced streaming
    Given the station announced "streams": true when it polled
    When the station posts a whole answer to "/local/complete" for a streamed job
    Then it is refused with a 409 saying the job is streamed
    # One job, one delivery shape; mixing them could send the consumer two answers.

  @tower
  Scenario: a chunk for a non-stream job is refused
    When the station posts a chunk for a job the consumer asked for without a stream
    Then it is refused with a 409 saying the job is not streamed

  # =========================================================================
  # 4. FAILURE MID-STREAM: NEVER RESTARTED
  # =========================================================================

  @tower
  Scenario: a station that stops mid-stream ends the consumer's stream honestly
    Given the station has posted chunks 0 and 1
    When no further chunk arrives within the stream idle timeout
    Then the consumer receives an error event saying the local station stopped mid-reply
    And a usage chunk with cost 0
    And "data: [DONE]"
    And the job is abandoned so a late chunk is refused

  @tower
  Scenario: a station that reports an upstream failure mid-stream ends the stream
    Given the station has posted chunk 0
    When the station posts a final chunk carrying an error
    Then the consumer receives an error event naming a local upstream failure
    And a usage chunk with cost 0 and "data: [DONE]"
    And the upstream's own error text is not echoed if it carries the station's upstream key

  @tower
  Scenario: a broken stream is not re-queued for another station
    Given a second attached station "garage" also serves "qwen3-8b"
    And "shed" took a streamed job and posted chunk 0
    When "shed" stops sending
    Then the job is not offered to "garage"
    And the consumer receives the error event, not a second reply
    # The relay rule: after the first byte, no silent restart.

  @tower
  Scenario: before the first chunk, a timed-out stream is a plain 504 like today
    When no station takes the streamed job within the completion timeout
    Then the consumer receives HTTP 504 with "no local station served this request in time"
    And no SSE headers were sent

  @client
  Scenario: the LAN ladder does not retry a Tower stream once bytes have reached the caller
    Given LAN mode is on and the LAN Tower at 192.168.1.20:8787 serves "qwen3-8b" as a stream
    And the stream breaks after the first tokens
    When the turn is in progress
    Then the caller receives the tokens already sent and a clear stream error
    And the turn is not restarted on an Edge member or another Tower
    # This is features/lan/routing_and_inference.feature scenario 102, unblocked by this file.

  @client
  Scenario: a Tower that fails before the first byte is moved past, as today
    Given LAN mode is on and two LAN Towers serve "qwen3-8b"
    And the first answers 503 before any event
    When a streamed turn is dispatched
    Then the second Tower serves it

  # =========================================================================
  # 5. THE CONSUMER GOES AWAY
  # =========================================================================

  @tower @station
  Scenario: a consumer disconnect cancels the job
    Given a streamed job is in progress
    When the consumer disconnects
    Then the job is abandoned
    And the station's next chunk is refused with a 410 saying the consumer left
    And the station stops reading its upstream
    And a local receipt is recorded only if at least one chunk reached the consumer

  @station
  Scenario: the station stops its upstream when the Tower says the consumer left
    Given a streamed job is in progress on "shed"
    When the Tower answers a chunk with 410
    Then "shed" cancels its upstream request
    And "shed" polls for new work

  # =========================================================================
  # 6. LIMITS
  # =========================================================================

  @tower
  Scenario: a chunk larger than the chunk cap is refused
    When the station posts a chunk larger than 256 KiB
    Then it is refused with 413
    And the consumer receives an error event, a zero-cost usage chunk and "data: [DONE]"
    # DECISION: chunk cap 256 KiB. Recommend yes: a token batch is a few KiB; the cap only has
    # to stop a station using one chunk as an unbounded buffer.

  @tower
  Scenario: a stream larger than the total cap is ended
    When the station's chunks for one job exceed 8 MiB in total
    Then the stream is ended with an error event saying the reply exceeded the size limit
    And further chunks are refused
    # DECISION: total cap 8 MiB, the same as maxPromptBody. Recommend yes.

  @tower
  Scenario: a stream longer than the stream duration cap is ended
    When a streamed job is still sending after 10 minutes
    Then the stream is ended with an error event saying the reply ran too long
    # DECISION: duration cap 10 minutes, with a 60 second idle timeout between chunks.
    # Recommend yes: long enough for a slow local model on a long answer, finite so a stuck
    # station cannot hold a concurrency slot forever.

  @tower
  Scenario: a streamed job holds its concurrency slots until it ends
    Given the Tower's whole-Tower and per-client concurrency caps
    When a client has the per-client cap of streams open
    Then its next request is refused with 429
    And when one stream ends, the next request is admitted

  @tower
  Scenario: chunk posts count against the station rate limit only above a real stream's pace
    When a station streams a normal reply of 500 chunks
    Then no chunk is refused by the station rate limit
    # DECISION: chunk posts get their own station bucket (e.g. 200/s, burst 400) rather than
    # sharing the poll bucket (20/s). Recommend yes: a real stream must never be throttled by
    # the rate limit meant to stop a flooding key.

  @station
  Scenario: the station batches tokens so it does not post one request per token
    When the upstream produces 1000 tokens in one second
    Then the station posts at most 20 chunks for that second
    # DECISION: the station coalesces upstream events into a chunk every 50 ms or 16 KiB,
    # whichever first. Recommend yes: one signed POST per token would cost a signature and a
    # nonce per token.

  # =========================================================================
  # 7. STRUCTURE AND COMPATIBILITY
  # =========================================================================

  @structural
  Scenario: the Core-free guarantee still holds with streaming
    When the dependency graph of cmd/roger-tower-local is checked
    Then it links none of towerjoin, towercore or towerhub
    And the source-scan gate finds no outbound call in internal/localplane

  @tower
  Scenario: an older station that only knows /local/complete still serves streamed requests
    Given an attached station running a roger version without chunk support
    When the client posts a streamed request and that station takes it
    Then the station returns the whole answer to "/local/complete"
    And the consumer receives it as a single-chunk SSE stream ending with "data: [DONE]"
    # DECISION: the Tower accepts a whole answer for a streamed job from an old station and
    # wraps it, instead of refusing. Recommend yes for one release. The refusal scenario above
    # applies only to stations that announced chunk support on /local/poll. The poll request gains
    # "streams": true for that. The old station's upstream request stays "stream": false.

  @tower
  Scenario: a station announces chunk support when it polls
    When a current station polls "/local/poll"
    Then its poll carries "streams": true
    And the Tower hands it streamed jobs as streamed jobs

  @tower
  Scenario: the stream never carries the station's identity or upstream details
    When a streamed request completes
    Then no event names the station's key, its upstream URL or its upstream key
