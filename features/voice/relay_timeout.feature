# VOICE RELAY TIMEOUT HONESTY (2026-07-02 live incident): a dead voice station made
# /v1/audio/speech die at the EDGE - the broker's non-stream response deadline
# (http.TimeoutHandler, 30s) killed the handler long before audio.go's intended 90s
# provider-wait could fire, so the consumer saw an edge-mangled generic timeout
# (and at the CF edge, 504 HTML) instead of the broker's own retryable
# {"error":{"message":"station timed out"}}.
#
# THE CONTRACT (mirrors the chat relay exactly): the audio money relays do their OWN
# Cloudflare-aware bounding internally - the same nonStreamRelayWait the non-stream
# chat relay uses, held BELOW CF's ~100s proxy cap - so they are EXEMPT from the
# blanket non-stream response deadline (streamRoutes), exactly as /v1/chat/completions
# is and for the same documented reason: a blanket TimeoutHandler double-bounds a
# handler that already bounds itself, and its generic reply replaces the honest one.
# A dead voice station must therefore yield the broker's own 504 JSON, with the
# consumer's pre-dispatch hold refunded in full.
#
# GROUND TRUTH:
#   cmd/rogerai-broker/main.go: streamRoutes + nonStreamTimeout + streamSafeHandler
#     (the per-route deadline discipline); the streamRoutes comment for
#     /v1/chat/completions is the precedent this feature extends to voice.
#   cmd/rogerai-broker/audio.go: audioRelayCore's terminal select - the intended
#     504 "station timed out" + the deferred ReleaseHoldFor refund.
#   cmd/rogerai-broker/tunnel.go: nonStreamRelayWait (90s, < CF ~100s).
#
# Enforced by: cmd/rogerai-broker/audio_timeout_bdd_test.go - the FULL production
# handler stack (streamSafeHandler(b.routes()) over a real HTTP server), a real
# signed+funded consumer, a registered voice station whose bridge never returns.

Feature: Voice relays time out honestly, inside their own budget

  # The reachability invariant - this is what rotted: the intended reply existed but
  # could never fire behind the blanket deadline.
  Scenario: The audio provider-wait is reachable and inside the edge budget
    Then each audio money path escapes the non-stream response deadline exactly like the chat relay
    And the non-stream provider-wait stays below Cloudflare's proxy cap

  Scenario: A dead speech station yields the broker's own 504 JSON with the hold refunded
    Given a funded signed voice consumer
    And a priced tts station is on air whose bridge never returns a result
    When the consumer posts speech for that station through the full production handler stack
    Then the response is the broker's own 504 JSON saying the station timed out
    And the consumer's hold is refunded in full

  Scenario: A dead transcription station yields the same honest 504 JSON with the hold refunded
    Given a funded signed voice consumer
    And a priced stt station is on air whose bridge never returns a result
    When the consumer posts a transcription for that station through the full production handler stack
    Then the response is the broker's own 504 JSON saying the station timed out
    And the consumer's hold is refunded in full
