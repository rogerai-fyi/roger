# Layer 3 (broker relay + meter) — TTS money contract. A voice (tts) request is metered by the
# EXACT input character count, run through the SAME cost arithmetic, hold, finalize, and ledger as
# chat — only the UNIT changes (chars, not tokens). No node can bill more than the chars it was
# sent. Back-compat: chat is untouched (see relay_routing.feature).
#
# Ground truth (to be built, per VOICE-AUDIO-DESIGN.md sections 4.3 + 7):
#   internal/protocol      - ModelOffer.Modality "tts" / Unit "char" (SHIPPED, layer 1);
#                            Cost(chars, 0, priceIn, 0) reuses the exact chat arithmetic
#   cmd/rogerai-broker     - relay() routes POST /v1/audio/speech, meters BROKER-counted chars
#   internal/store         - HoldFor / Finalize / ledger row carries unit="char" + the char count
#
# DECISIONS PINNED BY THIS SPEC (await founder approval):
#   D1  chars are counted as UNICODE CODE POINTS (runes), not UTF-8 bytes — fair across scripts.
#   D2  empty / whitespace-only input is REJECTED (400) BEFORE any hold — no charge for nothing.
#   D3  the metered char count is the BROKER's count of the sent text, never the node's claim
#       (anti-over-bill, exactly like the chat token recount / CostWith2).
#   D4  a free window / price-0 tts offer is exactly $0 (no hold, no money rows) — same as chat.
#
# Step definitions come AFTER approval (RED-first), like the other money features.

@money @voice @tts @metering
Feature: A TTS request is metered by exact input characters, capped, on the same wallet

  Background:
    Given a consumer with a funded wallet
    And a node offering "roger-operator" with modality "tts" at price_in 15 per 1M chars

  Rule: cost = chars * price_per_1M_chars / 1e6, using the broker's char count

    Scenario Outline: Character-to-credit arithmetic is exact
      Given a TTS request whose input is <chars> characters
      When the request is metered
      Then the cost in credits is <cost>
      And the ledger row records unit "char" and <chars> characters

      Examples:
        | chars   | cost      |
        | 0       | 0.000000  |
        | 1       | 0.000015  |
        | 100     | 0.001500  |
        | 1000    | 0.015000  |
        | 1000000 | 15.000000 |

    Scenario: The char count is the broker's count of the sent text, not the node's claim (D3)
      Given a TTS request whose input is 100 characters
      And the node's response claims 9999 characters
      When the request is metered
      Then the cost in credits is 0.001500
      And the node's inflated claim is ignored

  Rule: characters are Unicode code points, not bytes (D1)

    Scenario Outline: Multibyte text is counted by runes, not UTF-8 bytes
      Given a TTS request whose input is the text <text>
      When the character count is taken
      Then the counted characters are <runes>

      Examples:
        | text    | runes |
        | "hello" | 5     |
        | "café"  | 4     |
        | "🌍🌎🌏"  | 3     |
        | "日本語"   | 3     |

  Rule: empty input is refused before any money moves (D2)

    Scenario Outline: Empty or whitespace-only input is rejected with no hold
      Given a TTS request whose input is <input>
      When the request is relayed
      Then it is rejected with status 400
      And no hold is placed and no ledger money rows are written

      Examples:
        | input |
        | ""    |
        | "   " |

  Rule: the hold caps the charge — a request never bills more than authorized

    Scenario: The hold is placed for the input's char cost before the node is called
      Given a TTS request whose input is 1000 characters
      When the request is relayed
      Then a hold of 0.015000 credits is placed before the node is called
      And on completion the hold is finalized to the actual 0.015000 credits

    Scenario: Insufficient balance refuses the request before relay
      Given a consumer whose wallet holds 0.001000 credits
      And a TTS request whose input is 1000 characters costing 0.015000
      When the request is relayed
      Then the request is refused for insufficient funds
      And the node is never called

  Rule: a free tts offer is exactly $0 (D4)

    Scenario: A price-0 tts offer bills nothing
      Given a node offering "free-voice" with modality "tts" at price_in 0 per 1M chars
      And a TTS request whose input is 5000 characters
      When the request is metered
      Then the cost in credits is 0.000000
      And no hold is placed and no ledger money rows are written

  Rule: grant-key / device auth applies to the audio path identically

    Scenario: A device-signed TTS request bills the signing consumer's wallet
      Given a TTS request signed by the consumer's device key
      When the request is relayed and metered
      Then the consumer's wallet is debited the char cost
      And the same signed-request auth path as chat is used
