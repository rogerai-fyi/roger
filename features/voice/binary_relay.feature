# Layer 3 (broker relay + node return) — the BINARY / NON-STREAM RETURN money contract. A voice
# (tts) response is OPAQUE BINARY (a WAV / MP3), not JSON and not SSE. It takes the node's
# NON-STREAM return path — serve() + postResult() over POST /agent/result — NOT the SSE
# /agent/stream path chat streaming uses. This spec pins that the binary bytes survive that
# round-trip UNCHANGED, byte-for-byte, with the right Content-Type, and that the receipt still
# settles. It is the regression guard for the LIVE-E2E hang: `roger say` hung because a binary
# JobResult.Body could not be JSON-marshalled by the node (json.RawMessage validates its content
# as JSON; a WAV is not JSON), so postResult posted an EMPTY body the broker rejected (400 "bad
# result") and the waiting consumer got NOTHING until it timed out.
#
# Ground truth (real code this spec is anchored in):
#   internal/protocol      - JobResult.Body carries the node's returned bytes over /agent/result;
#                            it MUST round-trip ARBITRARY bytes (binary WAV, not just JSON) intact
#   internal/agent  serve()      - reads the upstream response body and puts it on JobResult.Body
#   internal/agent  postResult() - json.Marshal(JobResult) -> POST /agent/result (the failing step)
#   cmd/rogerai-broker  audioRelayCore - awaits the JobResult on the tunnel waiter, verifies the
#                            receipt, settles, then writes res.Body + spec.contentType to the
#                            consumer
#
# DECISIONS PINNED BY THIS SPEC (await founder approval):
#   B1  a NON-STREAM node result MUST carry an arbitrary binary body node -> broker -> consumer
#       byte-for-byte identical (no JSON-mangling, no truncation, no base64 leaking to the client).
#   B2  the consumer receives the broker-declared Content-Type for the modality (audio/mpeg for TTS)
#       — the binary is delivered as audio, not as an error JSON.
#   B3  the receipt STILL settles on the binary path: the consumer's wallet is debited the exact
#       broker-counted char cost, identical to a JSON result — the return-encoding change is
#       money-neutral.
#   B4  regression: an empty node body (a genuinely dead upstream) is STILL a clean failure to the
#       consumer. 2026-07-02 (founder-approved error_passthrough.feature E2): the status is 500
#       with the standard error body — a 5xx the edge passes through — superseding the old bare 502.
#       consumer with the hold refunded — the fix must not turn "no bytes" into a false success.
#
# Step definitions come AFTER approval (RED-first), like the other money features.

@money @voice @tts @binary @relay
Feature: A binary (non-stream) node result flows node -> broker -> consumer intact and settles

  Background:
    Given a live broker with a real tts node on air serving a binary WAV upstream
    And a consumer with a funded wallet

  Rule: the binary body survives the node -> broker -> consumer round-trip byte-for-byte (B1, B2)

    Scenario: A WAV synth returns the exact upstream bytes with an audio content-type
      When the consumer requests speech for the text "roger that"
      Then the consumer receives HTTP 200
      And the response body is byte-for-byte the upstream's WAV
      And the response Content-Type is "audio/mpeg"

    Scenario: An arbitrary non-JSON binary payload is relayed unchanged
      Given the upstream returns a non-JSON binary payload
      When the consumer requests speech for the text "hello"
      Then the consumer receives HTTP 200
      And the response body is byte-for-byte the upstream's payload

  Rule: the receipt settles on the binary path exactly as on a JSON path (B3)

    Scenario: The consumer's wallet is debited the exact char cost for a binary result
      When the consumer requests speech for a 100-character line
      Then the consumer receives HTTP 200
      And the consumer's wallet is debited the char cost 0.001500
      And a settled receipt is recorded for the request

  Rule: a genuinely empty upstream is still a clean failure, not a false success (B4)

    Scenario: An upstream that returns no bytes is a clean 500 with the hold refunded
      Given the upstream returns an empty body
      When the consumer requests speech for the text "roger that"
      Then the consumer receives HTTP 500
      And the consumer's wallet is not debited
