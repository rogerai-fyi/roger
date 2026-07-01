# SHARE-side VOICE BOOTH editor — the operator's voice-sharing wizard. Per the founder DELTA §D2,
# SHARE stays MODEL-FIRST: a voice is ONE tagged row (`♪ tts` / `▽ stt`, mono) among the operator's
# models, and the VOICE BOOTH opens via `p` at the SAME depth as the chat price editor — NO
# elevation, no "share a voice" top-level entry.
#
# The BOOTH edits: dj-name, voice (via the picker), blend (weighted, auto-normalized), speed
# (0.5–2.0), language, price ($/1k chars or FREE), and a LOCAL FREE preview (▶) that synths a fixed
# line through the operator's OWN /v1/audio/speech at the current voice/blend/speed and plays it
# (reusing the cross-platform player already in internal/tui/voice.go). A live "right now you'd
# broadcast as …" line reflects the on-air identity.
#
# LOCAL-PREVIEW ASYMMETRY (founder §6): the SHARE preview is FREE (the operator's own GPU, no broker
# relay, no confirm) — unlike the consumer's paid preview. This BOOTH configures the LOCAL server;
# the wire stays honest (only the resolved `voice`/`speed` ride the offer).
#
# Grounded in the REAL shareRow/onShareKey/shareEditorView (internal/tui/tui.go) + the reused player.
# Steps drive the REAL bubbletea model (no mocks); the local preview uses a REAL httptest speech
# server with a STUBBED player (the injectable audioPlayerFn) so no audio device is needed.
#
# Step definitions come AFTER approval (RED-first, per TDD-WORKFLOW.md).

Feature: the SHARE VOICE BOOTH edits an on-air DJ (model-first, opened via p, local-free preview)

  # --- SHARE stays model-first: the voice row is one tagged row (DELTA §D2) ---

  Scenario: a tts model shows a mono ♪ tts tag in the SHARE table (not elevated)
    Given the operator is on the SHARE table with a detected "kokoro" tts model
    Then the SHARE row for "kokoro" is tagged as a tts voice
    And the tag glyph folds to ASCII on a legacy console

  Scenario: an stt model shows a mono ▽ stt tag in the SHARE table
    Given the operator is on the SHARE table with a detected "whisper-large-v3" stt model
    Then the SHARE row for "whisper-large-v3" is tagged as an stt voice

  Scenario: an unconfigured tts row prompts to set a voice instead of a price
    Given the operator is on the SHARE table with a detected "kokoro" tts model
    Then the SHARE row for "kokoro" prompts the operator to set a voice

  Scenario: a plain chat model is NOT tagged as a voice (no regression)
    Given the operator is on the SHARE table with a detected "gpt-oss-20b" chat model
    Then the SHARE row for "gpt-oss-20b" is not tagged as a voice

  # --- p on a tts row opens the VOICE BOOTH at the same depth as the chat editor ---

  Scenario: p on a tts row opens the VOICE BOOTH (not the price+schedule editor)
    Given a logged-in operator on the SHARE table with a detected "kokoro" tts model selected
    When the operator presses "p"
    Then the VOICE BOOTH editor opens for "kokoro"

  Scenario: p on a chat row still opens the ordinary price+schedule editor
    Given a logged-in operator on the SHARE table with a detected "gpt-oss-20b" chat model selected
    When the operator presses "p"
    Then the price and schedule editor opens for "gpt-oss-20b"

  Scenario: the VOICE BOOTH is login-gated, like the chat price editor (earning needs an account)
    Given an anonymous operator on the SHARE table with a detected "kokoro" tts model selected
    When the operator presses "p"
    Then the login-to-earn gate is shown
    And the VOICE BOOTH does not open

  # --- the BOOTH fields ---

  Scenario: the BOOTH shows the dj-name, voice, blend, speed, language, and price fields
    Given the operator has the VOICE BOOTH open for "kokoro"
    Then the BOOTH shows a dj name field
    And the BOOTH shows a voice field
    And the BOOTH shows a blend field
    And the BOOTH shows a speed field
    And the BOOTH shows a language field
    And the BOOTH shows a price field

  Scenario: typing sets the dj name (the offer Name the picker shows)
    Given the operator has the VOICE BOOTH open for "kokoro"
    And the dj name field is focused
    When the operator types "1950s Operator"
    Then the dj name reads "1950s Operator"
    And the live broadcast-as line names "1950s Operator"

  Scenario: speed nudges within the 0.5 to 2.0 range and never escapes it
    Given the operator has the VOICE BOOTH open for "kokoro"
    And the speed field is focused
    When the operator nudges the speed all the way down
    Then the speed is not below 0.5
    When the operator nudges the speed all the way up
    Then the speed is not above 2.0

  Scenario: price accepts a $/1k chars value and FREE when zero
    Given the operator has the VOICE BOOTH open for "kokoro"
    And the price field is focused
    When the operator sets the price to "0"
    Then the live broadcast-as line reads FREE
    When the operator sets the price to "0.020"
    Then the live broadcast-as line shows the per-1k-chars price

  # --- blend: weighted, auto-normalized ---

  Scenario: adding a blend voice yields a weighted blend
    Given the operator has the VOICE BOOTH open for "kokoro" with voice "af_heart"
    When the operator adds "af_bella" to the blend
    Then the blend reads a weighted mix of "af_heart" and "af_bella"

  Scenario: blend weights auto-normalize to sum to 1 on save
    Given the operator has the VOICE BOOTH open for "kokoro" with a blend "af_heart 0.7 + af_bella 0.3"
    When the operator saves the BOOTH
    Then the saved blend weights sum to 1

  Scenario: clearing the blend returns to a single voice
    Given the operator has the VOICE BOOTH open for "kokoro" with a blend "af_heart 0.7 + af_bella 0.3"
    When the operator clears the blend
    Then the blend is a single voice "af_heart"

  # --- the LOCAL FREE preview (reuses the cross-platform player) ---

  Scenario: the local preview synths a fixed line through the operator's OWN speech server, free
    Given the operator has the VOICE BOOTH open for "kokoro" with voice "af_heart"
    And a local speech server that returns wav audio
    When the operator plays the local preview
    Then a POST is made to the local server's /v1/audio/speech
    And the preview request names voice "af_heart"
    And the preview is not relayed through the broker
    And nothing is billed for the preview

  Scenario: the local preview passes the current speed to the local server
    Given the operator has the VOICE BOOTH open for "kokoro" with voice "af_heart" and speed 1.25
    And a local speech server that returns wav audio
    When the operator plays the local preview
    Then the preview request carries speed 1.25

  Scenario: the local preview plays the returned audio through the injected player
    Given the operator has the VOICE BOOTH open for "kokoro" with voice "af_heart"
    And a local speech server that returns wav audio
    And a stub audio player that records what it was handed
    When the operator plays the local preview
    Then the stub player received the wav bytes

  Scenario: a local preview against an unreachable server surfaces an error, never a crash
    Given the operator has the VOICE BOOTH open for "kokoro" with voice "af_heart"
    And no reachable local speech server
    When the operator plays the local preview
    Then the BOOTH shows a local-server error
    And the BOOTH does not crash

  # --- saving arms the row to go on air with the chosen voice as the offer default ---

  Scenario: saving the BOOTH carries the chosen name + voice onto the model's offer
    Given a logged-in operator has the VOICE BOOTH open for "kokoro"
    And the operator set the dj name to "1950s Operator" and the voice to "am_onyx"
    When the operator saves the BOOTH
    Then the pending offer for "kokoro" has name "1950s Operator"
    And the pending offer for "kokoro" has default voice "am_onyx"
    And the SHARE table shows "kokoro" as ready to go on air

  Scenario: esc cancels the BOOTH without arming the row
    Given a logged-in operator has the VOICE BOOTH open for "kokoro"
    When the operator presses "esc"
    Then the SHARE table is shown
    And "kokoro" is not armed with a voice
