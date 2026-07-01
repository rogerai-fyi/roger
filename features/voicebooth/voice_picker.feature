# SHARE-side VOICE PICKER popover — the operator picks one of the ~33 Kokoro voice ids for their
# DJ. The ids come from the operator's LOCAL server (GET /v1/audio/voices); when the server can't
# enumerate them, the picker falls back to a BUNDLED static list of the known Kokoro ids so it
# NEVER blanks. Auditioning a highlighted voice is LOCAL + FREE (the operator's own GPU; no broker
# relay, no billing) — the deliberate asymmetry with the consumer's paid preview.
#
# Grounded: the consumer voice.go (already on main) reserves GET /v1/audio/voices "for the producer
# share wizard" and OMITS `voice` on the consumer preview; this is that wizard. Voices group by the
# Kokoro prefix (af_/am_/bf_/bm_ = American/British female/male). Steps use a REAL httptest LOCAL
# server (no mocks); the no-server case asserts the bundled fallback.
#
# Step definitions come AFTER approval (RED-first, per TDD-WORKFLOW.md).

Feature: the VOICE BOOTH picker fetches local voices, falls back to a bundled list, and auditions free

  # --- fetch from the local server ---

  Scenario: the picker fetches the operator's local voice ids from GET /v1/audio/voices
    Given a local server that lists voices "af_heart", "am_onyx", "bf_emma"
    When the operator opens the voice picker against that local server
    Then the picker lists "af_heart"
    And the picker lists "am_onyx"
    And the picker lists "bf_emma"

  Scenario: the picker groups voices by the Kokoro prefix into female/male, American/British
    Given a local server that lists voices "af_heart", "am_onyx", "bf_emma", "bm_george"
    When the operator opens the voice picker against that local server
    Then "af_heart" is grouped under "American female"
    And "am_onyx" is grouped under "American male"
    And "bf_emma" is grouped under "British female"
    And "bm_george" is grouped under "British male"

  # --- the bundled fallback: never blank ---

  Scenario: with a local server that does not enumerate voices, the picker uses the bundled list
    Given a local server that returns 404 for GET /v1/audio/voices
    When the operator opens the voice picker against that local server
    Then the picker is not empty
    And the picker lists "af_heart"

  Scenario: with NO local server reachable, the picker still uses the bundled list
    Given no reachable local server
    When the operator opens the voice picker
    Then the picker is not empty
    And the picker lists at least 20 voices

  Scenario: the bundled Kokoro list contains the known American + British ids
    Given the bundled Kokoro voice list
    Then it contains "af_heart"
    And it contains "am_onyx"
    And it contains "bf_emma"
    And it contains "bm_george"

  # --- filter by typing ---

  Scenario: typing filters the picker to matching ids
    Given a local server that lists voices "af_heart", "af_bella", "am_onyx"
    And the operator opened the voice picker against that local server
    When the operator types "af_" into the picker filter
    Then the picker lists "af_heart"
    And the picker lists "af_bella"
    And the picker does not list "am_onyx"

  # --- audition is LOCAL + FREE ---

  Scenario: auditioning the highlighted voice plays it through the LOCAL server for free
    Given a local speech server that returns wav audio
    And the operator opened the voice picker with "af_heart" highlighted
    When the operator auditions the highlighted voice
    Then a POST is made to the local server's /v1/audio/speech
    And the audition request names voice "af_heart"
    And the audition is not relayed through the broker

  Scenario: an audition against an unreachable local server surfaces an error, never a crash
    Given no reachable local speech server
    And the operator opened the voice picker with "af_heart" highlighted
    When the operator auditions the highlighted voice
    Then the picker shows a local-server error
    And the picker does not crash
