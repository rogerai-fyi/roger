# Layer 3 (broker relay + meter) — STT money contract. A voice (stt) request is metered by the
# EXACT number of audio BYTES uploaded — the simplest, tamper-proof measure: the broker sees
# exactly the bytes it received, so there is no audio to parse and no node claim to trust. Same
# cost arithmetic, hold, finalize, and ledger as chat; unit = "byte". Because the count is exact
# and known up front, the hold equals the final charge (no upper-bound estimate).
#
# Ground truth (to be built, per VOICE-AUDIO-DESIGN.md sections 4.3 + 7):
#   internal/protocol   - ModelOffer.Modality "stt" / Unit "byte" (SHIPPED, layer 1);
#                         Cost(bytes, 0, priceIn, 0) reuses the exact chat arithmetic
#   cmd/rogerai-broker  - relay() routes POST /v1/audio/transcriptions, meters len(uploaded audio)
#   internal/store      - HoldFor / Finalize / ledger row carries unit="byte" + the byte count
#
# DECISION (founder-approved 2026-06-30): meter STT by uploaded bytes — simplest + safest.
#   cost = audio_bytes * price_per_1M_bytes / 1e6; the byte count is the BROKER's count of the
#   request body (exact, tamper-proof); an empty upload is rejected (400) before any hold; a
#   price-0 stt offer is $0. (A friendly "~minutes" DISPLAY can be derived later from a nominal
#   bitrate; billing itself always stays on the exact byte count.)
#
# Step definitions come AFTER approval (RED-first).

@money @voice @stt @metering
Feature: An STT request is metered by exact uploaded audio bytes, on the same wallet

  Background:
    Given a consumer with a funded wallet
    And a node offering "whisper-large-v3" with modality "stt" at price_in 10 per 1M bytes

  Rule: cost = audio_bytes * price_per_1M_bytes / 1e6, from the broker's byte count

    Scenario Outline: Byte-to-credit arithmetic is exact
      Given an STT request with <bytes> bytes of audio
      When the request is metered
      Then the cost in credits is <cost>
      And the ledger row records unit "byte" and <bytes> bytes

      Examples:
        | bytes   | cost      |
        | 0       | 0.000000  |
        | 1000    | 0.010000  |
        | 100000  | 1.000000  |
        | 1000000 | 10.000000 |

    Scenario: The metered byte count is the broker's count of the upload, not the node's claim
      Given an STT request with 100000 bytes of audio
      And the node's response claims 9999999 bytes processed
      When the request is metered
      Then the cost in credits is 1.000000
      And the node's inflated claim is ignored

  Rule: an empty upload is refused before any money moves

    Scenario: A zero-byte upload is rejected with no hold
      Given an STT request with 0 bytes of audio
      When the request is relayed
      Then it is rejected with status 400
      And no hold is placed and no ledger money rows are written

  Rule: the hold is the upload's exact byte cost; the request never bills more

    Scenario: The hold equals the final charge (exact count, no estimate)
      Given an STT request with 100000 bytes of audio
      When the request is relayed
      Then a hold of 1.000000 credits is placed before the node is called
      And on completion the hold is finalized to the same 1.000000 credits

    Scenario: Insufficient balance refuses the request before relay
      Given a consumer whose wallet holds 0.001000 credits
      And an STT request with 100000 bytes of audio costing 1.000000
      When the request is relayed
      Then the request is refused for insufficient funds
      And the node is never called

  Rule: a free stt offer is exactly $0

    Scenario: A price-0 stt offer bills nothing
      Given a node offering "free-transcribe" with modality "stt" at price_in 0 per 1M bytes
      And an STT request with 1000000 bytes of audio
      When the request is metered
      Then the cost in credits is 0.000000
      And no hold is placed and no ledger money rows are written
