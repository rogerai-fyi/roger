# Slice 3 - the operator declares what a shared model is for, from the TUI, the local web
# console or `roger share`, and it rides the offer as "declared".
#
# The declaration is per model, saved beside the model's price and voice in config.json
# (share_meta), and owned by the share controller (internal/node), which every front-end
# shares - so a change made in one surface is what the others see. Operator input is
# VALIDATED: the broker silently drops what it cannot accept (offer_taxonomy.feature), but an
# operator typing a category deserves to be told it was wrong, not to have it vanish.
#
# Declared and inferred never mix (founder ruling 2026-10-05): when the
# operator has declared anything for a model, the declaration is the WHOLE taxonomy on air,
# labelled "declared"; otherwise the inference (inference.feature) goes on air, labelled
# "inferred". One label, one author.

Feature: The operator declares a model's taxonomy and it goes on air as declared

  Background:
    Given a share controller with a detected model "qwen3-vl-8b" inferred as "vision/vlm"

  # ---------------------------------------------------------------- declared vs inferred
  Scenario: With nothing declared the inferred taxonomy goes on air
    When the operator puts "qwen3-vl-8b" on air
    Then the on-air taxonomy for "qwen3-vl-8b" is category "vision", subcategory "vlm" and source "inferred"

  Scenario: A declaration replaces the inference entirely
    Given the operator declares for "qwen3-vl-8b" category "vision/ocr" and tags "nameplate-ocr,manufacturing"
    When the operator puts "qwen3-vl-8b" on air
    Then the on-air taxonomy for "qwen3-vl-8b" is category "vision", subcategory "ocr" and source "declared"
    And the on-air tags for "qwen3-vl-8b" are "nameplate-ocr,manufacturing"

  Scenario: Declaring only tags still replaces the inferred category
    Given the operator declares for "qwen3-vl-8b" tags "ppe"
    When the operator puts "qwen3-vl-8b" on air
    Then the on-air taxonomy for "qwen3-vl-8b" is category "", subcategory "" and source "declared"
    And the on-air tags for "qwen3-vl-8b" are "ppe"

  Scenario: Clearing the declaration brings the inference back
    Given the operator declares for "qwen3-vl-8b" category "vision/ocr" and tags ""
    And the operator clears the declaration for "qwen3-vl-8b"
    When the operator puts "qwen3-vl-8b" on air
    Then the on-air taxonomy for "qwen3-vl-8b" is category "vision", subcategory "vlm" and source "inferred"

  Scenario: Every declared field rides the offer
    Given the operator declares for "qwen3-vl-8b" category "vision/ocr", tags "ppe", tuning "finetune", base model "Qwen/Qwen3-VL-8B-Instruct", content policy "aligned" and license "Apache-2.0"
    When the operator puts "qwen3-vl-8b" on air
    Then the on-air offer for "qwen3-vl-8b" carries tuning "finetune", base model "Qwen/Qwen3-VL-8B-Instruct", content policy "aligned" and license "Apache-2.0"

  Scenario: Declarations are per model
    Given a detected model "llama3.2" inferred as "language/chat"
    And the operator declares for "qwen3-vl-8b" category "vision/ocr" and tags ""
    When the operator puts "llama3.2" on air
    Then the on-air taxonomy for "llama3.2" is category "language", subcategory "chat" and source "inferred"

  # Like a price change, a declaration applies the next time the model goes on air; it never
  # silently restarts a live session.
  Scenario: Declaring while on air applies at the next on-air
    Given the operator puts "qwen3-vl-8b" on air
    When the operator declares for "qwen3-vl-8b" category "vision/ocr" and tags ""
    Then the live session for "qwen3-vl-8b" was not restarted
    And the saved declaration for "qwen3-vl-8b" is category "vision/ocr"

  # ---------------------------------------------------------------- validation
  Scenario Outline: An invalid declaration is refused with a reason, and nothing is saved
    When the operator declares for "qwen3-vl-8b" <declaration>
    Then the declaration is refused with a reason mentioning "<reason>"
    And nothing was saved for "qwen3-vl-8b"

    Examples:
      | declaration                               | reason                |
      | category "medical/radiology" and tags ""  | unknown category      |
      | category "vision/code" and tags ""        | not a vision subcategory |
      | category "vision/ocr" and tags "a/b"      | invalid tag           |
      | category "vision/ocr" and tags "diagnosis" | reserved tag         |
      | category "vision/ocr" and tags "t1,t2,t3,t4,t5,t6,t7,t8,t9" | at most 8 tags |
      | tuning "lora"                             | unknown tuning        |
      | content policy "nsfw"                     | unknown content policy |

  Scenario: The refusal for an unknown category lists the valid ones
    When the operator declares for "qwen3-vl-8b" category "medical" and tags ""
    Then the declaration is refused with a reason mentioning "language, vision, audio, retrieval, generation, signals"

  Scenario: A voice model cannot be declared outside audio
    Given a detected tts model "kokoro"
    When the operator declares for "kokoro" category "language/chat" and tags ""
    Then the declaration is refused with a reason mentioning "a tts model is always audio/tts"

  Scenario: Accepted values are saved in canonical form
    When the operator declares for "qwen3-vl-8b" category "Vision/OCR" and tags " PPE ,smoke fire"
    Then the saved declaration for "qwen3-vl-8b" is category "vision/ocr"
    And the saved tags for "qwen3-vl-8b" are "ppe,smoke-fire"

  # The declared source is the controller's to stamp; a saved file cannot claim otherwise.
  Scenario: A saved declaration is always on air as declared
    Given config.json carries a saved declaration for "qwen3-vl-8b" with category "vision/ocr" and claim source "inferred"
    When the controller starts and the operator puts "qwen3-vl-8b" on air
    Then the on-air taxonomy for "qwen3-vl-8b" is category "vision", subcategory "ocr" and source "declared"

  # Regressions (slice-3 review): a hand-edited config.json entry that fails validation, or
  # that only carries a reserved tag, must not go on air as an empty "declared" taxonomy that
  # hides the inference.
  Scenario Outline: An invalid saved declaration does not hide the inference
    Given config.json carries a saved declaration for "qwen3-vl-8b" with <saved>
    When the controller starts and the operator puts "qwen3-vl-8b" on air
    Then the on-air taxonomy for "qwen3-vl-8b" is category "vision", subcategory "vlm" and source "inferred"

    Examples:
      | saved                                         |
      | category "medical" and claim source ""        |
      | tags "diagnosis"                              |
      | content policy "nsfw"                         |

  # A model that is not detected right now (its server is down) can still have its stale
  # declaration cleared; only declaring needs the model's modality.
  Scenario: A declaration for a model that is not detected right now can be cleared
    Given config.json carries a saved declaration for "offline-m" with category "vision/ocr" and claim source ""
    When the controller starts and the operator clears the declaration for "offline-m"
    Then the host was asked to remove the declaration for "offline-m"

  Scenario Outline: An over-long base model or license is refused, not cut
    When the operator declares for "qwen3-vl-8b" a <field> of <n> characters
    Then the declaration is refused with a reason mentioning "<reason>"

    Examples:
      | field      | n   | reason                  |
      | base model | 129 | at most 128 characters  |
      | license    | 65  | at most 64 characters   |

  Scenario Outline: A base model or license at its limit is accepted
    When the operator declares for "qwen3-vl-8b" a <field> of <n> characters
    Then the declaration was accepted

    Examples:
      | field      | n   |
      | base model | 128 |
      | license    | 64  |

  # ---------------------------------------------------------------- persistence
  Scenario: An accepted declaration is persisted through the host
    When the operator declares for "qwen3-vl-8b" category "vision/ocr" and tags "ppe"
    Then the host was asked to save for "qwen3-vl-8b" category "vision/ocr" and tags "ppe"

  Scenario: Clearing a declaration is persisted as a removal
    Given the operator declares for "qwen3-vl-8b" category "vision/ocr" and tags ""
    When the operator clears the declaration for "qwen3-vl-8b"
    Then the host was asked to remove the declaration for "qwen3-vl-8b"

  # ---------------------------------------------------------------- the local web console API
  # GET shows both halves so a UI can show "declared" next to "inferred"; POST validates
  # exactly as the controller does (same function), and DELETE clears.
  @console
  Scenario: The console reads a model's declared and inferred taxonomy
    Given the operator declares for "qwen3-vl-8b" category "vision/ocr" and tags "ppe"
    When the console GETs /api/share/meta for "qwen3-vl-8b"
    Then the console response has declared category "vision/ocr" and inferred category "vision/vlm"

  @console
  Scenario: The console saves a declaration
    When the console POSTs /api/share/meta for "qwen3-vl-8b" with category "vision/ocr" and tags "ppe"
    Then the console response status is 200
    And the saved declaration for "qwen3-vl-8b" is category "vision/ocr"

  @console
  Scenario: The console's invalid declaration is a 400 with the reason
    When the console POSTs /api/share/meta for "qwen3-vl-8b" with category "medical" and tags ""
    Then the console response status is 400
    And the console error mentions "unknown category"
    And nothing was saved for "qwen3-vl-8b"

  @console
  Scenario: The console clears a declaration
    Given the operator declares for "qwen3-vl-8b" category "vision/ocr" and tags ""
    When the console DELETEs /api/share/meta for "qwen3-vl-8b"
    Then the console response status is 200
    And nothing was saved for "qwen3-vl-8b"
    And the host was asked to remove the declaration for "qwen3-vl-8b"

  @console
  Scenario: The console refuses a model the station does not serve
    When the console POSTs /api/share/meta for "not-served" with category "vision/ocr" and tags ""
    Then the console response status is 404

  # The console is a local, state-changing surface: it keeps the same guard as the price and
  # rename routes (internal/webui auth: the per-launch console token, on a localhost-only
  # listener), so a web page the operator happens to have open cannot write to it.
  @console
  Scenario Outline: A console request without the console token is refused
    When a request without the console token <method>s /api/share/meta for "qwen3-vl-8b"
    Then the console response status is 403
    And nothing was saved for "qwen3-vl-8b"

    Examples:
      | method |
      | GET    |
      | POST   |
      | DELETE |

  @console
  Scenario: The console meta route only answers GET, POST and DELETE
    When the console PUTs /api/share/meta for "qwen3-vl-8b"
    Then the console response status is 405
