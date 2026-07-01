# `roger say` — the one-shot CLI TTS consumer command. It SIGNS + SPENDS (TTS is char-metered), so
# this spec is EXHAUSTIVE across the happy path AND the adversarial money/auth corners. It mirrors
# the broker's real /v1/audio/speech contract (cmd/rogerai-broker/audio.go): a signed POST of
# {model, input, response_format:"wav"[, speed]}, the returned WAV + the X-RogerAI-Cost meter
# header, and the uniform 503 / anon-paid 403 / insufficient-funds 402 / broker-down errors.
#
# FOUNDER-APPROVED (2026-07-01), with these decisions baked in:
#   - --voice is REQUIRED; omitting it errors with a hint at `roger voices` / rogerai.fyi/voices and
#     spends NOTHING (no broker call).
#   - the LOCAL playback format is WAV (response_format:"wav") — universally + trivially playable, so
#     no lame/ffmpeg is needed; the cross-platform player lives in the shared internal/audio package
#     (extracted from internal/tui, reused by BOTH surfaces).
#   - the voice id is passed VERBATIM as `model` (the broker treats it as opaque + routes on the raw
#     id; a namespaced @login/name from /voices resolves broker-side).
#   - cost is read from the X-RogerAI-Cost header and printed as `spoke N chars · $X` (N = rune count).
#   - a PAID voice for an anonymous (not-logged-in) caller surfaces the broker's own sign-in gate.
# The standing guarantee holds: a node's pubkey / node id / bridge URL / hostname / IP never appears.
#
# The step definitions drive the REAL cmdSay / cmdVoices handlers and the REAL client.Speak /
# client.Voices against an httptest broker, stubbing ONLY the audio player (the injectable player
# seam) — no domain mocks.

@voice @say @money
Feature: roger say — synthesize a line through a shared voice and play it locally

  Background:
    Given a signed-in consumer with a funded wallet
    And a broker with a tts voice "af_heart" on air at 15 credits per 1M chars

  # ---------------------------------------------------------------------------
  Rule: flag + text parsing (and NO spend on a parse error)

    Scenario: A voice plus positional words is parsed into model + joined text
      When the consumer runs say with args "--voice af_heart hello there operator"
      Then the broker receives a speech POST for model "af_heart" with input "hello there operator"

    Scenario: A quoted phrase is preserved as one input
      When the consumer runs say with a voice "af_heart" and text "roger that"
      Then the broker receives a speech POST for model "af_heart" with input "roger that"

    Scenario: A namespaced voice id is sent verbatim as the model
      When the consumer runs say with args "--voice @bownux/operator hi"
      Then the broker receives a speech POST for model "@bownux/operator" with input "hi"

    Scenario: Omitting --voice errors with a hint and spends nothing
      When the consumer runs say with args "hello there"
      Then the command fails with an error naming "roger voices"
      And the error names "rogerai.fyi/voices"
      And no speech POST is ever made to the broker

    Scenario: Giving --voice but no text is a usage error that spends nothing
      When the consumer runs say with args "--voice af_heart"
      Then the command fails with a usage error
      And no speech POST is ever made to the broker

  # ---------------------------------------------------------------------------
  Rule: the signed POST carries the exact OpenAI-shaped body

    Scenario: The request body is {model, input, response_format:"wav"} and is signed
      When the consumer runs say with a voice "af_heart" and text "hello"
      Then the broker receives a speech POST for model "af_heart" with input "hello"
      And the request body response_format is "wav"
      And the request carries a valid client signature

    Scenario: --voice-speed rides the body only when set
      When the consumer runs say with args "--voice-speed 1.5 --voice af_heart hello"
      Then the request body speed is 1.5

    Scenario: Without --voice-speed the body omits speed (the server default is used)
      When the consumer runs say with a voice "af_heart" and text "hello"
      Then the request body has no speed field

  # ---------------------------------------------------------------------------
  Rule: playback + cost surfacing on success

    Scenario: A played sample prints the char count and the billed cost
      Given the broker bills 0.0008 for the request
      When the consumer runs say with a voice "af_heart" and text "hello"
      Then the audio is handed to the player
      And the output reads "spoke 5 chars · $0.0008"

    Scenario: A free voice plays and reads $0.00
      Given the broker bills 0 for the request
      When the consumer runs say with a voice "af_heart" and text "hello"
      Then the audio is handed to the player
      And the output reads "spoke 5 chars · $0.00"

    Scenario: With no audio player the sample is saved and the path is printed
      Given no audio player is available on this host
      When the consumer runs say with a voice "af_heart" and text "hello"
      Then the output names the saved file path
      And the command succeeds

  # ---------------------------------------------------------------------------
  Rule: the money + reachability gates (adversarial)

    Scenario: An anonymous caller on a PAID voice hits the broker's sign-in gate
      Given the broker rejects the request with 403 "sign in to use this voice model"
      When the consumer runs say with a voice "af_heart" and text "hello"
      Then the command fails with an error containing "sign in to use this voice model"
      And no audio is handed to the player

    Scenario: Insufficient balance surfaces the funds error and the topup hint
      Given the broker rejects the request with 402 "insufficient balance - add funds"
      When the consumer runs say with a voice "af_heart" and text "hello"
      Then the command fails with an error containing "insufficient balance"
      And the error names the topup step

    Scenario: No station serving the voice gives the uniform no-station message
      Given the broker rejects the request with 503 "no station on air for af_heart"
      When the consumer runs say with a voice "af_heart" and text "hello"
      Then the command fails with an error containing "no station on air for af_heart"

    Scenario: An unreachable broker fails gracefully
      Given the broker is unreachable
      When the consumer runs say with a voice "af_heart" and text "hello"
      Then the command fails with an error containing "broker unreachable"

  # ---------------------------------------------------------------------------
  Rule: roger voices lists the roster, cheapest first

    Scenario: The roster lists each voice with name, operator, and price, cheapest first
      # The broker already returns /voices cheapest-first, so the free voice leads; the CLI preserves
      # that order verbatim (it never re-sorts a second time).
      Given the broker lists voices:
        | id      | operator | name     | language | price_per_1k_chars | free  |
        | v-free  | acme     | Kiosk    | en-GB    | 0                  | true  |
        | v-heart | bownux   | Operator | en-US    | 0.02               | false |
      When the consumer runs voices
      Then the output lists "Operator" by "@bownux"
      And the output shows "Kiosk" as FREE
      And the free voice is listed before the paid voice

    Scenario: An empty roster points at roger share
      Given the broker lists no voices
      When the consumer runs voices
      Then the output names "roger share"

    Scenario: An unreachable broker fails the roster gracefully
      Given the broker is unreachable
      When the consumer runs voices
      Then the command fails with an error containing "broker unreachable"
