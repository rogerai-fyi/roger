# APPROVED by the founder 2026-07-01 (spec-first workflow step 3) - step definitions +
# implementation may now proceed. Written from the 2026-07-01 launch audit.
#
# Spec-first behavior contract for the voice relay RESOURCE guardrails the 2026-07-01
# launch audit called out: (1) TTS input has no explicit length cap - a 32 MiB body of
# input chars places a huge hold (e.g. 32M chars at $12/1M = $384) for work that can
# only fail the node's 8 MiB result cap (money refunds via defer, but the node burns
# real GPU time and the hold locks real credits for the round trip); (2) there is no
# in-flight bound on 32 MiB audio uploads - N concurrent STT requests hold
# N x (32 MiB body + 8 MiB result) resident across 2 x 1GB instances (memory blowup).
#
# Ground truth: cmd/rogerai-broker/audio.go audioRelayCore (32<<20 LimitReader, parse,
# HoldFor), tunnel.go /agent/result 8 MiB cap.
#
# Knobs (env, with launch defaults):
#   ROGERAI_TTS_MAX_CHARS   default 10000 (a ~10-minute read; roger say + the TUI
#                           preview send sentences, far below it)
#   ROGERAI_AUDIO_INFLIGHT  default 8 per instance (8 x ~40 MiB worst case ≈ 320 MiB,
#                           fits the 1 GB instance with the brain's own headroom)
#
# Invariants:
#   G1 a refused request is refused BEFORE any hold or dispatch (no charge, no work).
#   G2 refusals are explicit: 413 (too long) / 503 + Retry-After (saturated), with a
#      human message - never a hang or a silent trim.
#   G3 the guards bound ONLY the voice relay - chat is untouched.
#   G4 both knobs are env-tunable without redeploy semantics changes (<=0 disables).

Feature: Voice relay guardrails - TTS input cap and audio in-flight bound

  Background:
    Given a broker with an on-air tts node "dj-1" and stt node "listener-1"

  # ===========================================================================
  # 1. TTS input cap (G1, G2)
  # ===========================================================================

  Scenario: input at the cap is served
    Given ROGERAI_TTS_MAX_CHARS is 10000
    When a consumer requests speech for exactly 10000 characters
    Then the request is dispatched and billed for 10000 characters

  Scenario: input over the cap is refused 413 before any hold
    Given ROGERAI_TTS_MAX_CHARS is 10000
    When a consumer requests speech for 10001 characters
    Then the response is 413 naming the cap
    And no hold was placed and no node was dispatched

  Scenario: the cap counts Unicode runes exactly like billing does
    Given ROGERAI_TTS_MAX_CHARS is 10
    When a consumer requests speech for 10 multibyte characters
    Then the request is dispatched (bytes never inflate the count)

  Scenario: cap 0 disables the guard
    Given ROGERAI_TTS_MAX_CHARS is 0
    When a consumer requests speech for 50000 characters
    Then the request is not refused for length

  Scenario: the moderation screen still runs on capped-size input first
    Given ROGERAI_TTS_MAX_CHARS is 10000
    When a consumer requests speech for 9000 flagged characters
    Then the response is 451 (screen), still before any hold

  # ===========================================================================
  # 2. Audio in-flight bound (G1, G2, G3)
  # ===========================================================================

  Scenario: requests under the bound proceed concurrently
    Given ROGERAI_AUDIO_INFLIGHT is 8
    When 8 transcriptions are in flight
    Then all 8 are dispatched normally

  Scenario: the request over the bound is refused 503 with Retry-After
    Given ROGERAI_AUDIO_INFLIGHT is 2
    And 2 transcriptions are in flight
    When a third arrives
    Then it is refused 503 with a Retry-After header
    And no hold was placed for it

  Scenario: a slot frees when a request finishes (success or failure)
    Given ROGERAI_AUDIO_INFLIGHT is 1
    And 1 transcription is in flight
    When it completes
    Then the next transcription is admitted

  Scenario: TTS and STT share the audio bound; chat is never gated by it
    Given ROGERAI_AUDIO_INFLIGHT is 1
    And 1 speech synthesis is in flight
    Then a concurrent transcription is refused 503
    And a concurrent CHAT relay proceeds untouched

  Scenario: bound 0 disables the guard
    Given ROGERAI_AUDIO_INFLIGHT is 0
    When 20 transcriptions are in flight
    Then none is refused for saturation
