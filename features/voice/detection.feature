# Layer 2 (detect) spec — `roger share` detects local voice/audio servers and turns them into
# the right kind of offer, reusing the existing OpenAI-compatible probe. CPU or GPU makes no
# difference (detection probes the endpoint, not the silicon), so your existing Kokoro / Whisper
# are picked up exactly like a local LLM. See VOICE-AUDIO-DESIGN.md §4.2, §5.
# Steps use REAL httptest stub servers (no mocks), per TDD-WORKFLOW.md.

Feature: roger detects local voice/audio servers (TTS + STT), CPU or GPU alike

  Scenario: A local TTS server is detected as a tts offer billed per character
    Given a local server that serves GET /v1/models listing "roger-operator-voice"
    And that server serves POST /v1/audio/speech
    When roger detects local models
    Then "roger-operator-voice" is detected with modality "tts"
    And its unit is "char"

  Scenario: A local Whisper server is detected as an stt offer billed per byte
    Given a local server that serves GET /v1/models listing "whisper-large-v3"
    And that server serves POST /v1/audio/transcriptions
    When roger detects local models
    Then "whisper-large-v3" is detected with modality "stt"
    And its unit is "byte"

  Scenario: A chat server is still detected as chat (no regression)
    Given a local server that serves GET /v1/models listing "llama3.2"
    And that server serves POST /v1/chat/completions
    When roger detects local models
    Then "llama3.2" is detected with modality "chat"

  Scenario: CPU vs GPU makes no difference — detection reads the endpoint, not the silicon
    Given a local server that serves GET /v1/models listing "kokoro"
    And that server serves POST /v1/audio/speech
    When roger detects local models
    Then "kokoro" is detected with modality "tts"
    And its unit is "char"

  Scenario: A server exposing both chat and speech splits into per-modality offers
    Given a local server that serves both POST /v1/chat/completions and POST /v1/audio/speech
    And it lists "llama3.2" and "kokoro" on /v1/models
    When roger detects local models
    Then "llama3.2" is detected as "chat"
    And "kokoro" is detected as "tts"

  Scenario: When only /v1/models is available, the model id classifies the modality
    Given a local server that lists "kokoro" and "whisper-1" on /v1/models
    And that server answers neither audio endpoint on a capability probe
    When roger detects local models
    Then "kokoro" is classified "tts"
    And "whisper-1" is classified "stt"

  Scenario: The existing roggentoo-tts server is detected out of the box
    Given a local server matching roggentoo-tts (GET /v1/models -> "roger-operator-voice", POST /v1/audio/speech)
    When roger detects local models
    Then "roger-operator-voice" is detected with modality "tts"

  Scenario: A key-protected voice server surfaces as needs-key, not "nothing detected"
    Given a local TTS server that answers 401 to an unauthenticated GET /v1/models
    And no usable key is present in the environment
    When roger detects local models
    Then the server is reported as needing a key
    And it is not silently dropped

  Scenario: A transcription server with an unbranded model id is stt by capability
    Given a local server that serves GET /v1/models listing "voxtral-mini"
    And that server serves POST /v1/audio/transcriptions
    When roger detects local models
    Then "voxtral-mini" is detected with modality "stt"
    And its unit is "byte"
