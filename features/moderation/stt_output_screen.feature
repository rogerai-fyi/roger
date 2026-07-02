# APPROVED by the founder 2026-07-01 (spec-first workflow step 3) - step definitions +
# implementation may now proceed. Written from the 2026-07-01 launch audit.
#
# Spec-first behavior contract for STT OUTPUT screening - the 2026-07-01 launch audit's
# moderation gap: /v1/audio/transcriptions accepts opaque audio (nothing to screen going
# IN, acknowledged by design), but the transcription RESULT text is returned unscreened.
# That is a laundering channel: speak disallowed text, get it back as clean text that
# never touched the policy. Chat input, concierge, and TTS input are all screened; STT
# output closes the loop so EVERY text the broker hands a consumer passed the screen.
#
# Ground truth: cmd/rogerai-broker/audio.go audioRelayCore (spec.parse returns
# moderate=="" for STT), moderation.go screen()/allow(), preserveCSAM (report.go).
#
# DESIGN DECISION baked into this spec (approve or amend):
#   D1 BILLING ON BLOCK: a blocked transcription is STILL CHARGED. The node did the
#      work in good faith on opaque audio; the abuser (not the operator) eats the
#      cost, which also prices out repeat laundering probes. The 451 response carries
#      the usual X-RogerAI-Cost header so the spend is visible.
#   D2 FAIL MODE mirrors input screening: with ROGERAI_REQUIRE_MODERATION=1 a screen
#      outage 503s the transcription (fail-closed, result withheld); with 0 it is
#      served unscreened (fail-open) - same knob, same semantics, no new flag.
#   D3 The screened text is the node's transcription JSON "text" field; the raw JSON
#      is NOT forwarded on a block (no partial leak via other fields).
#
# Invariants:
#   S1 no unscreened transcription text ever reaches the consumer while the screen
#      allows screening (up/required).
#   S2 S4 (CSAM) hits preserve evidence exactly like the chat path: encrypted, with
#      pseudonym + observed IP, state 'queued' - and the AUDIO body is preserved too
#      (it is the primary evidence).
#   S3 the meter headers + settle behavior on a block match D1 (charged, settled).

Feature: STT transcription output is screened before it reaches the consumer

  Background:
    Given a broker with moderation configured
    And an on-air stt node "listener-1" transcribing uploads

  # ===========================================================================
  # 1. The happy path stays intact
  # ===========================================================================

  Scenario: a benign transcription passes through unchanged
    Given the screen classifies "wilco, meet you at the hangar" as safe
    When a consumer transcribes audio that the node hears as "wilco, meet you at the hangar"
    Then the response is 200 with the node's transcription JSON
    And the consumer is charged the metered byte cost exactly once

  # ===========================================================================
  # 2. Blocked output (S1, D1, D3)
  # ===========================================================================

  Scenario: a flagged transcription is withheld with 451
    Given the screen classifies the node's transcription as unsafe category "S5"
    When a consumer transcribes that audio
    Then the response is 451 and contains NO fragment of the transcription
    And the response names the blocking category
    And the consumer is still charged (X-RogerAI-Cost is set; the hold settles)
    And the operator is still credited their share

  Scenario: a CSAM transcription is withheld, preserved, and queued (S2)
    Given the screen classifies the node's transcription as unsafe category "S4"
    When a consumer transcribes that audio
    Then the response is 451
    And the uploaded AUDIO and the transcription are preserved encrypted with the consumer pseudonym and observed IP, report_state "queued"

  # ===========================================================================
  # 3. Fail modes (D2)
  # ===========================================================================

  Scenario: screen outage with require=1 withholds the result (fail-closed)
    Given ROGERAI_REQUIRE_MODERATION=1 and the moderation backend is down
    When a consumer transcribes any audio
    Then the response is 503 and no transcription text is returned
    And the consumer's hold is RELEASED (they got nothing; the outage is ours)

  Scenario: screen outage with require=0 serves unscreened (fail-open, pre-launch only)
    Given ROGERAI_REQUIRE_MODERATION=0 and the moderation backend is down
    When a consumer transcribes benign audio
    Then the response is 200 (availability chosen over screening, per the knob)

  # ===========================================================================
  # 4. Edges
  # ===========================================================================

  Scenario: an empty transcription result needs no screen call
    Given the node returns an empty transcription text
    Then no moderation call is made for it

  Scenario: TTS is unaffected - its INPUT screen is unchanged
    When a consumer requests speech for text the screen flags
    Then it is still refused 451 BEFORE any node is dispatched or hold placed

  Scenario: a malformed node transcription JSON is a clean 500, never served raw
    Given the node returns a body that does not parse as transcription JSON
    Then the response is 500 and the raw body is not forwarded
    And the consumer's hold is released
