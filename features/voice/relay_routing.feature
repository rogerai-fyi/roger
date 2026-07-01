# Layer 3 (broker relay + meter) — routing + modality isolation. The broker routes each audio
# path to a node that ACTUALLY offers that modality, never crosses modalities, and leaves the
# chat path byte-for-byte unchanged.
#
# Ground truth (to be built, per VOICE-AUDIO-DESIGN.md sections 4.3):
#   cmd/rogerai-broker/main.go    - POST /v1/audio/speech + /v1/audio/transcriptions handlers
#   cmd/rogerai-broker/tunnel.go  - relay() + pickFor() filter candidates by request modality
#
# DECISIONS PINNED BY THIS SPEC (await founder approval):
#   D9   POST /v1/audio/speech routes ONLY to tts offers; /v1/audio/transcriptions ONLY to stt.
#   D10  pickFor filters candidates by the request's modality — a chat-only node is never picked
#        for audio, and a voice node is never picked for /v1/chat/completions.
#   D11  no eligible node => a clear 503 (no capacity), NEVER a mis-route to the wrong modality.
#   D12  chat relay + metering is byte-for-byte unchanged (permanent regression guard).
#
# Step definitions come AFTER approval (RED-first).

@money @voice @routing
Feature: Audio requests route to the right modality; chat is untouched

  Rule: each audio path routes only to a node offering that modality (D9, D10)

    Scenario: /v1/audio/speech routes to a tts node
      Given a node offering "roger-operator-voice" with modality "tts"
      When a POST /v1/audio/speech for "roger-operator-voice" arrives
      Then the request is relayed to that tts node

    Scenario: /v1/audio/transcriptions routes to an stt node
      Given a node offering "whisper-large-v3" with modality "stt"
      When a POST /v1/audio/transcriptions for "whisper-large-v3" arrives
      Then the request is relayed to that stt node

    Scenario: A chat-only node is never picked for a speech request
      Given a node offering "llama3.2" with modality "chat"
      And no tts node is registered
      When a POST /v1/audio/speech arrives
      Then no node is selected
      And the request fails with status 503 for no capacity
      And the chat node is never called

    Scenario: A tts node is never picked for a chat request
      Given a node offering "roger-operator-voice" with modality "tts"
      And no chat node is registered
      When a POST /v1/chat/completions arrives
      Then no node is selected
      And the tts node is never called

  Rule: no eligible node => 503, never a cross-modality mis-route (D11)

    Scenario: An stt request with only a tts node available fails cleanly
      Given a node offering "roger-operator-voice" with modality "tts"
      And no stt node is registered
      When a POST /v1/audio/transcriptions arrives
      Then no node is selected
      And the request fails with status 503 for no capacity

  Rule: chat is byte-for-byte unchanged (D12 — regression guard)

    Scenario: A chat request meters by tokens exactly as before
      Given a node offering "llama3.2" with modality "chat" at price_in 0.20 and price_out 0.50 per 1M
      And a chat request billed 1000 prompt and 500 completion tokens
      When the request is metered
      Then the cost in credits is 0.000450
      And the ledger row records unit "token"

    Scenario: A pre-voice node with no modality field still serves chat unchanged
      Given a node whose offer has no modality field
      When a chat request is relayed to it
      Then the offer normalizes to modality "chat" and unit "token"
      And it is metered by tokens with nothing about the chat path changed

  # --- the exact status codes the BUILT app maps (roger-ios docs/BROKER-VOICE-API.md, APIError);
  #     each one makes the app fall back to the on-device Apple voice ---
  Rule: the audio path returns the status codes the app maps

    Scenario: A bad or missing signature/key is 401
      Given a POST /v1/audio/speech with an invalid signature and no key
      When it is relayed
      Then the response is 401

    Scenario: Insufficient funds or monthly cap hit is 402
      Given a consumer whose wallet cannot cover the request, or who is over the monthly cap
      And a POST /v1/audio/speech for a paid voice
      When it is relayed
      Then the response is 402

    Scenario: A paid voice requested without a funded wallet is 403
      Given a device-signed request with no funded wallet
      And a POST /v1/audio/speech for the PAID voice "roger-operator-voice"
      When it is relayed
      Then the response is 403

    Scenario: No station on air for that voice id is 503
      Given no tts node offers "roger-operator-voice"
      When a POST /v1/audio/speech for "roger-operator-voice" arrives
      Then the response is 503

  Rule: the audio response carries the same balance-meter headers as chat

    Scenario: A served TTS request returns the metering headers
      Given a paid TTS request that succeeds
      When the response is returned
      Then it carries X-RogerAI-Provider, X-RogerAI-Cost, and X-RogerAI-Balance
