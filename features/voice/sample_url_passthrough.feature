# Layer 3/4 - voice discovery, sample_url pass-through. LIVE-VERIFIED REGRESSION (2026-07-02):
# a node registered a tts offer WITH SampleURL populated (node-side proven down to the
# protocol.ModelOffer, agent.go:356), yet the deployed broker's GET /voices response for that
# voice carried NO "sample_url" - while its SIBLING fields (name, language) survived the same
# register -> /voices trip. These scenarios drive the REAL register path (b.register, signed,
# owner-bound) into the REAL /voices aggregation (computeVoices) so the drop site is pinned by
# an executable spec, not a guess - including the RE-REGISTRATION cases (a stale persisted
# offer must never shadow a fresh one) and the RESTART re-hydrate path (the store round-trip:
# real Postgres when ROGERAI_TEST_DATABASE_URL is set, the in-memory reference store else).
#
# DECISIONS PINNED (founder, BROKER-VOICE-API.md + discovery.feature 2026-06-30):
#   - voice metadata (name/language/sample_url/latency_ms) rides as OPTIONAL fields on the
#     offer and /voices passes it through VERBATIM; absent fields serialize as null (omitted).
#   - the FRESH registration is authoritative: a re-register REPLACES the node's offers -
#     adding, changing, or removing sample_url must all be visible on the next /voices read.
#   - a broker restart re-hydrates the persisted registration WITHOUT losing offer fields.
#
# Step definitions come AFTER approval (RED-first).

@voice @discovery @sample_url @regression
Feature: A tts offer's sample_url survives the register -> /voices trip verbatim

  Background:
    Given the broker with content screening configured and required
    And an owner "Luis" (GitHub login "bownux") who has logged in

  Rule: sample_url registered through the REAL register path is served exactly

    Scenario: A fresh register carrying name, language, and sample_url lists all three
      When "bownux" registers an on-air tts offer with model "roger-operator-voice" named "Roger Operator", language "en-US", sample_url "https://rogerai.fyi/samples/roger-operator.mp3"
      And the registration succeeded
      And an anonymous GET /voices arrives
      Then a voice with raw id "roger-operator-voice" is listed
      And the voice with raw id "roger-operator-voice" carries sample_url "https://rogerai.fyi/samples/roger-operator.mp3"
      And the voice with raw id "roger-operator-voice" carries name "Roger Operator" and language "en-US"

    Scenario: Two voices each keep their OWN sample_url (no cross-voice bleed)
      When "bownux" registers an on-air tts offer with model "voice-a" named "Voice A", language "en-US", sample_url "https://rogerai.fyi/samples/a.mp3"
      And "bownux" registers an on-air tts offer with model "voice-b" named "Voice B", language "en-GB", sample_url "https://rogerai.fyi/samples/b.mp3"
      And an anonymous GET /voices arrives
      Then the voice with raw id "voice-a" carries sample_url "https://rogerai.fyi/samples/a.mp3"
      And the voice with raw id "voice-b" carries sample_url "https://rogerai.fyi/samples/b.mp3"

  Rule: a re-registration REPLACES the offer - no stale persisted field survives it

    Scenario: A re-register that ADDS sample_url shows the new value (stale-merge catcher)
      When "bownux" registers an on-air tts offer with model "roger-operator-voice" named "Roger Operator", language "en-US" and no sample_url
      And the registration succeeded
      And the same node re-registers with sample_url "https://rogerai.fyi/samples/roger-operator.mp3"
      And the registration succeeded
      And an anonymous GET /voices arrives
      Then the voice with raw id "roger-operator-voice" carries sample_url "https://rogerai.fyi/samples/roger-operator.mp3"

    Scenario: A re-register that CHANGES sample_url shows the newest value
      When "bownux" registers an on-air tts offer with model "roger-operator-voice" named "Roger Operator", language "en-US", sample_url "https://rogerai.fyi/samples/old.mp3"
      And the same node re-registers with sample_url "https://rogerai.fyi/samples/new.mp3"
      And the registration succeeded
      And an anonymous GET /voices arrives
      Then the voice with raw id "roger-operator-voice" carries sample_url "https://rogerai.fyi/samples/new.mp3"

    Scenario: A re-register that REMOVES sample_url clears it (the fresh registration is authoritative)
      When "bownux" registers an on-air tts offer with model "roger-operator-voice" named "Roger Operator", language "en-US", sample_url "https://rogerai.fyi/samples/roger-operator.mp3"
      And the same node re-registers with no sample_url
      And the registration succeeded
      And an anonymous GET /voices arrives
      Then the voice with raw id "roger-operator-voice" carries no sample_url

  Rule: the persisted registration re-hydrates with sample_url intact (store round-trip)

    # These two scenarios re-hydrate the REAL store, which (on the Postgres backend) is one
    # database shared across the suite's scenarios - so their model ids are UNIQUE to each
    # scenario, keeping the /voices row they assert on unambiguous among re-hydrated rows.

    Scenario: A broker restart re-hydrates the voice with its sample_url
      When "bownux" registers an on-air tts offer with model "rehydrate-voice-a" named "Rehydrate Voice A", language "en-US", sample_url "https://rogerai.fyi/samples/roger-operator.mp3"
      And the registration succeeded
      And the broker restarts and re-hydrates from the store
      And an anonymous GET /voices arrives
      Then a voice with raw id "rehydrate-voice-a" is listed
      And the voice with raw id "rehydrate-voice-a" carries sample_url "https://rogerai.fyi/samples/roger-operator.mp3"
      And the voice with raw id "rehydrate-voice-a" carries name "Rehydrate Voice A" and language "en-US"

    Scenario: A restart after a sample_url-adding re-register re-hydrates the NEW value
      When "bownux" registers an on-air tts offer with model "rehydrate-voice-b" named "Rehydrate Voice B", language "en-US" and no sample_url
      And the same node re-registers with sample_url "https://rogerai.fyi/samples/roger-operator.mp3"
      And the registration succeeded
      And the broker restarts and re-hydrates from the store
      And an anonymous GET /voices arrives
      Then the voice with raw id "rehydrate-voice-b" carries sample_url "https://rogerai.fyi/samples/roger-operator.mp3"
