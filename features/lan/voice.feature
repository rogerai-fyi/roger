# STATUS: APPROVED 2026-10-07 (founder)
# BUILD STATUS: BUILT 2026-10-07.
#
# LAN MODE - VOICE: `roger --lan say` and the TUI voice speak through a LOCAL voice, or refuse.
#
# WHY THIS EXISTS
#
# routing_and_inference.feature scenario 238 ("`roger say` and voice in LAN mode use a local
# voice or refuse", RULED 2026-10-06: "voice uses the same filter as text; a local TTS station is
# a band like any other") only pins the refusal. Today cmd/rogerai/say.go sayLAN refuses even
# when a local voice is on the dial ("roger say cannot speak through a LAN voice yet").
# FOUNDER RULING 2026-10-06: "yes, spec it first" - speak through a local voice.
#
# WHAT IS LOCAL FOR VOICE
#   - this machine's own voice: a TTS band this machine has on air (in-process SHARE, or a
#     separate `roger share` process via its owner-only lan-shares record).
#   - an Edge member serving a TTS band, reached over the member's Edge face with mutual TLS.
#   - NOT a LAN Tower: internal/localplane has no audio route (it serves /v1/chat/completions
#     only), so a Tower is never asked for speech, and the refusal says so.
#
# THE RULES
#   1. Nothing is ever sent to the public broker's /v1/audio/speech in LAN mode.
#   2. The ladder order is the text ladder's: this machine, then Edge members; no Tower rung,
#      no market rung.
#   3. A voice that fails partway is reported, never silently replaced by another voice.
#   4. Speech is free in LAN mode: no balance read, no price, no hold.
#
# GROUND TRUTH: cmd/rogerai/say.go (cmdSay, sayLAN, lanDial), internal/client/say.go (Speak,
# Voices), internal/client/lan_relay.go (relayLAN, tryEdgePeers), cmd/rogerai/lanshareself.go
# (lanShareRecord: today band + chat_url + key only), internal/edge/infer.go (handleInfer:
# relays chat to serving.Upstream), internal/tui/voice.go (isTTS, startVoicePreview,
# synthVoiceSample), protocol.ModalityTTS.
#
# Tags: @cli, @client, @tui, @edge.

Feature: In LAN mode, voice speaks through a voice on this machine or an Edge member, or refuses

  # =========================================================================
  # 1. THIS MACHINE'S OWN VOICE
  # =========================================================================

  @cli
  Scenario: `roger --lan say` speaks through a voice this machine has on air
    Given LAN mode is on
    And this machine has the TTS band "roggentoo-voice" on air through `roger share`
    When they run "roger --lan say hello --voice roggentoo-voice"
    Then audio is produced by this machine's voice upstream
    And the output says it was spoken locally and was free
    And no request was made to the public broker

  @cli
  Scenario: with no --voice, the only local voice is used
    Given LAN mode is on and exactly one local TTS band is on air
    When they run "roger --lan say hello"
    Then that voice speaks

  @cli
  Scenario: with no --voice and several local voices, the choice is deterministic and named
    Given LAN mode is on and this machine and the Edge member "jetson" each have a TTS band on air
    When they run "roger --lan say hello"
    Then this machine's voice speaks
    And the output names the voice used and how to pick another with --voice
    # RULED 2026-10-07 (founder approved the recommendation): the same order as text (this machine, then Edge members), rather than refusing as
    # ambiguous. Recommend yes.

  @cli
  Scenario: a separate `roger share` process's voice is found through its local record
    Given LAN mode is on
    And a separate `roger --lan share` process has a TTS band on air
    When they run "roger --lan say hello"
    Then that process's voice speaks
    And the record that pointed to it is owner-only (0600) and never sent to a peer
    # The lan-shares record gains the band's modality and its speech URL beside chat_url.

  @cli
  Scenario: the voice's upstream key is never echoed back
    Given this machine's voice upstream requires a key
    And the upstream echoes the key in an error body
    When they run "roger --lan say hello"
    Then the key does not appear in any output

  # =========================================================================
  # 2. AN EDGE MEMBER'S VOICE
  # =========================================================================

  @client @edge
  Scenario: `roger --lan say` speaks through an Edge member's voice over mutual TLS
    Given LAN mode is on
    And the enrolled Edge member "jetson" has the TTS band "studio-voice" on air
    When they run "roger --lan say hello --voice studio-voice"
    Then the request goes to "jetson"'s Edge face over mutual TLS
    And audio is returned
    And "jetson" signs a local receipt for the speech
    And no request was made to the public broker

  @edge
  Scenario: an Edge face serves speech only to verified members
    Given the Edge member "jetson" serves a TTS band
    When a request for speech reaches "jetson"'s face without a member certificate
    Then it is refused, as a chat turn would be
    # RULED 2026-10-07 (founder approved the recommendation): speech gets its own path on the Edge face, "/edge/speech", gated by the same
    # caller() check as "/edge/infer", rather than overloading /edge/infer with an audio body.
    # Recommend the separate path: the request and response shapes differ (text in, audio out),
    # and one handler per shape keeps each body limit honest.

  @edge
  Scenario: an Edge face refuses speech for a band it does not serve, naming what it serves
    Given the Edge member "jetson" serves only the chat band "qwen3-8b"
    When a member asks "jetson" for speech with "studio-voice"
    Then it is refused with 404, naming the bands "jetson" serves

  @edge
  Scenario: an Edge face refuses a speech body over the limit
    When a member sends "jetson" a speech request larger than 64 KiB
    Then it is refused with 413
    # RULED 2026-10-07 (founder approved the recommendation): 64 KiB of text per request. Recommend yes: `roger say` speaks a line, not a book.

  @edge
  Scenario: an Edge face refuses a chat band asked for speech
    Given "jetson" serves the chat band "qwen3-8b" and no TTS band
    When a member asks "jetson" for speech with "qwen3-8b"
    Then it is refused, saying "qwen3-8b" is not a voice

  # =========================================================================
  # 3. TOWERS AND THE MARKET ARE NEVER ASKED FOR VOICE
  # =========================================================================

  @cli
  Scenario: a LAN Tower is never asked for speech, and the refusal says why
    Given LAN mode is on
    And no local voice is on air
    And the LAN Tower at 192.168.1.20:8787 is configured
    When they run "roger --lan say hello"
    Then it is refused, saying no local voice is on air and LAN Towers do not serve voice
    And no request was made to 192.168.1.20:8787
    And no request was made to the public broker's /v1/audio/speech

  @cli
  Scenario: nothing local on air is a clear refusal
    Given LAN mode is on and no voice is on air on this machine or any Edge member
    When they run "roger --lan say hello"
    Then it is refused, saying no local voice is on air
    And it says how to put one on air or how to use the network's voices without --lan
    And no request was made to the public broker
    # This keeps routing_and_inference.feature scenario 238's refusal exactly as approved.

  @cli
  Scenario: a voice named with --voice that is not local is refused, never looked up on the network
    Given LAN mode is on and "studio-voice" is on the public market but not on this LAN
    When they run "roger --lan say hello --voice studio-voice"
    Then it is refused, saying "studio-voice" is not on air on this LAN
    And no request was made to the public broker, including its voices list

  @cli
  Scenario: `roger --lan say --list` lists only local voices
    Given LAN mode is on, this machine has one voice on air and the market has many
    When they run "roger --lan say --list"
    Then only this machine's voice is listed, with its source
    And no request was made to the public broker

  # =========================================================================
  # 4. FAILURE
  # =========================================================================

  @cli
  Scenario: a voice that goes away mid-request is reported, not replaced
    Given LAN mode is on and the Edge member "jetson" is speaking a line for this machine
    When "jetson" goes away before the audio is complete
    Then it fails, saying the local voice stopped before finishing
    And no partial audio is played as if it were complete
    And the line is not re-spoken by a different voice

  @cli
  Scenario: a local voice that fails before producing audio moves to the next local voice
    Given LAN mode is on and both this machine and "jetson" have the same TTS band on air
    And this machine's voice upstream refuses the request before any audio
    When they run "roger --lan say hello"
    Then "jetson" speaks it
    And the output names that it moved past this machine's voice and why
    # The text ladder's rule: a failure before the first byte is moved past; after it, never.

  @cli
  Scenario: every local voice failing is a refusal naming each failure
    Given LAN mode is on and this machine and "jetson" have voices on air
    And both fail
    When they run "roger --lan say hello"
    Then it is refused, naming each local voice and how it failed
    And no request was made to the public broker

  # =========================================================================
  # 5. MONEY AND THE TUI
  # =========================================================================

  @cli
  Scenario: LAN speech is free and reads no balance
    Given LAN mode is on and a local voice is on air
    When they run "roger --lan say hello"
    Then no balance was read and no price or hold was shown
    And the output says the speech was free

  @tui
  Scenario: the TUI voice preview in LAN mode speaks through the local voice
    Given LAN mode is on and the TUI dial shows a local TTS band
    When the voice preview is started on that band
    Then the sample is synthesized by that local voice
    And no request was made to the public broker
    And no price or sample cost is shown

  @tui
  Scenario: the TUI voice preview refuses honestly when the local voice is gone
    Given LAN mode is on and the TUI dial showed a local TTS band that has since gone off air
    When the voice preview is started on that band
    Then the preview says the local voice is no longer on air
    And no request was made to the public broker

  # =========================================================================
  # 6. EDGE-ONLY
  # =========================================================================

  @cli
  Scenario: voice under edge-only uses this machine and Edge members, exactly as in LAN mode
    Given edge-only is on and the Edge member "jetson" has a voice on air
    When they run "roger say hello"
    Then "jetson" speaks it
    # Voice never uses a Tower, so edge-only changes nothing for voice beyond the badge. # unblocked 2026-10-07 by features/lan/edge_only.feature

  # review 2026-10-07 (founder ruling 2026-10-06: LAN mode is a band filter, and voice uses the
  # same filter as text): the voice roster follows it too, so the hint `roger say` gives points
  # at voices this machine can actually use.
  @cli
  Scenario: roger voices in LAN mode lists only local voices and never asks the network
    Given LAN mode is on and exactly one local TTS band is on air
    When they run "roger --lan voices"
    Then the output lists that local voice
    And no request was made to the public broker, including its voices list
