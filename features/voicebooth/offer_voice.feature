# SHARE-side VOICE BOOTH — the offer's chosen voice/speed rides as a DEFAULT, and the node
# INJECTS it into a /v1/audio/speech request that OMITS `voice`, so a consumer gets the
# operator's picked voice (a named DJ), not the raw local-server default (Kokoro's af_heart).
#
# BLEND (founder decision): the blend rides ON offer.Voice. offer.Voice may be a SINGLE id
# ("af_heart") OR a BLEND STRING ("af_heart:0.5+af_aoede:0.5" — Kokoro's Hermes-style weighted
# mix). The node injects WHICHEVER string the operator picked; the operator's LOCAL Kokoro resolves
# the blend. Speed rides as offer.Speed. The broker never sees a node address.
#
# WIRE-HONESTY (founder): the node MUST NOT clobber a voice the caller explicitly set — a request
# that already names a voice is served verbatim (the operator default is only a fallback when
# `voice` is absent).
#
# Grounded in the REAL protocol.ModelOffer + agent.serve() (internal/agent/agent.go): serve() already
# routes a job whose Path is /v1/audio/speech to the local speech endpoint; this adds the default-
# voice injection ON that path. Steps use the REAL serve()/Config/ModelOffer (no mocks); an httptest
# stub stands in for the LOCAL speech server and CAPTURES the exact body the node forwarded.
#
# Step definitions come AFTER approval (RED-first, per TDD-WORKFLOW.md).

Feature: a voice offer carries a default voice/speed that the node injects on serve

  # --- the field exists + round-trips (Layer 1: protocol) ---

  Scenario: ModelOffer carries an optional Voice default that survives a JSON round-trip
    Given a tts offer for "roger-operator-voice" with voice "af_heart" and speed 1.25
    When it is marshalled to JSON and decoded back
    Then the decoded offer voice is "af_heart"
    And the decoded offer speed is 1.25

  Scenario: a chat offer omits the voice/speed fields entirely (no wire bloat, back-compat)
    Given a chat offer for "gpt-oss-20b" with no voice set
    When it is marshalled to JSON
    Then the JSON does not contain a "voice" field
    And the JSON does not contain a "speed" field

  # --- the node injects the default on the speech path (Layer 2: agent.serve) ---

  Scenario: the node injects the offer's voice into a speech request that omits `voice`
    Given a local speech server that echoes back the request body it received
    And a voice offer whose default voice is "am_onyx"
    When the broker relays a /v1/audio/speech job whose body omits `voice`
    Then the request the node forwards to the local server carries voice "am_onyx"

  Scenario: the node injects a BLEND STRING default verbatim (the blend IS the shared voice)
    Given a local speech server that echoes back the request body it received
    And a voice offer whose default voice is "af_heart:0.5+af_aoede:0.5"
    When the broker relays a /v1/audio/speech job whose body omits `voice`
    Then the request the node forwards to the local server carries voice "af_heart:0.5+af_aoede:0.5"

  Scenario: the node injects the offer's speed too when the request omits `speed`
    Given a local speech server that echoes back the request body it received
    And a voice offer whose default voice is "am_onyx" and speed is 0.75
    When the broker relays a /v1/audio/speech job whose body omits `voice`
    Then the request the node forwards to the local server carries voice "am_onyx"
    And the request the node forwards to the local server carries speed 0.75

  Scenario: a caller's explicit voice is NEVER overwritten by the default
    Given a local speech server that echoes back the request body it received
    And a voice offer whose default voice is "am_onyx"
    When the broker relays a /v1/audio/speech job whose body already sets voice "bf_emma"
    Then the request the node forwards to the local server carries voice "bf_emma"

  Scenario: a caller's explicit speed is NEVER overwritten by the default
    Given a local speech server that echoes back the request body it received
    And a voice offer whose default voice is "am_onyx" and speed is 0.75
    When the broker relays a /v1/audio/speech job whose body already sets speed 1.5
    Then the request the node forwards to the local server carries speed 1.5

  Scenario: with NO default voice configured, the request is forwarded unchanged (omitting voice)
    Given a local speech server that echoes back the request body it received
    And a voice offer whose default voice is empty
    When the broker relays a /v1/audio/speech job whose body omits `voice`
    Then the request the node forwards to the local server omits `voice`

  Scenario: the default voice injection NEVER touches a chat job (only the speech path)
    Given a local chat server that echoes back the request body it received
    And a voice offer whose default voice is "am_onyx"
    When the broker relays a /v1/chat/completions job whose body omits `voice`
    Then the request the node forwards to the local server omits `voice`

  Scenario: an unparseable request body on the speech path is forwarded unchanged (no crash)
    Given a local speech server that echoes back the request body it received
    And a voice offer whose default voice is "am_onyx"
    When the broker relays a /v1/audio/speech job whose body is not valid JSON
    Then the node forwards the body byte-for-byte unchanged
