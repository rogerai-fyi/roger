# Layer 1 (protocol) spec — the ModelOffer gains a modality + a billing unit.
# Founder-approvable behaviour: chat stays the back-compatible default; voice adds TTS (speak,
# billed per character) and STT (listen, billed per audio-byte); the unit can never lie about
# the modality; and a pre-voice node keeps working unchanged through a rolling deploy.
# See VOICE-AUDIO-DESIGN.md §4.1, §5, §7 (internal docs).

Feature: Voice/audio offers carry a modality and a truthful billing unit

  Background:
    Given a node registering an offer with the broker

  # --- back-compat: the whole point is a mixed-version fleet survives a rolling deploy ---
  Scenario: A legacy offer with no modality defaults to chat billed per token
    Given an offer for model "llama3.2" with no modality field and no unit field
    When the broker normalizes the offer
    Then the offer modality is "chat"
    And the offer unit is "token"
    And it is priced as credits per 1,000,000 tokens, exactly as before

  Scenario: A JSON payload that omits modality and unit is treated as chat/token
    Given the raw offer JSON {"model":"llama3.2","price_in":50,"price_out":150,"ctx":131072}
    When the broker normalizes the offer
    Then the offer modality is "chat"
    And the offer unit is "token"

  # --- the two new modalities and their canonical units ---
  Scenario: A TTS offer is billed per character
    Given an offer for model "roger-operator" with modality "tts"
    When the broker normalizes the offer
    Then the offer unit is "char"
    And its price is read as credits per 1,000,000 input characters

  Scenario: An STT offer is billed per audio-byte
    Given an offer for model "whisper-large-v3" with modality "stt"
    When the broker normalizes the offer
    Then the offer unit is "byte"
    And its price is read as credits per 1,000,000 audio-bytes

  # --- truth-in-labeling: the unit is canonical for the modality; it cannot be spoofed ---
  Scenario Outline: The unit is canonicalized from the modality regardless of what was sent
    Given an offer with modality "<modality>" and a claimed unit "<sent_unit>"
    When the broker normalizes the offer
    Then the offer unit is "<canonical_unit>"

    Examples:
      | modality | sent_unit | canonical_unit |
      | chat     | token     | token          |
      | chat     | char      | token          |
      | tts      | token     | char           |
      | tts      | char      | char           |
      | stt      | byte      | byte           |
      | stt      | char      | byte           |

  # --- the enum is closed: an unknown modality is rejected, not silently trusted ---
  Scenario Outline: Only known modalities are accepted
    Given an offer with modality "<modality>"
    When the broker validates the offer
    Then the offer is "<result>"

    Examples:
      | modality | result   |
      |          | accepted |
      | chat     | accepted |
      | tts      | accepted |
      | stt      | accepted |
      | video    | rejected |
      | speech   | rejected |
      | TTS      | rejected |

  # --- a single node may advertise a mixed fleet across modalities ---
  Scenario: One node registers chat, TTS, and STT offers together
    Given a node registers offers:
      | model             | modality |
      | llama3.2          | chat     |
      | roger-operator    | tts      |
      | whisper-large-v3  | stt      |
    When the broker normalizes the offers
    Then each offer keeps its own modality and canonical unit
    And the node is discoverable under all three

  # --- round-trip fidelity ---
  Scenario: modality and unit survive a JSON marshal/unmarshal round-trip
    Given an offer with modality "tts" and unit "char"
    When it is marshalled to JSON and decoded back
    Then the modality and unit are preserved
