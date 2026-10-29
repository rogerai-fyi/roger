# Slice 3 - `roger share` declares a model's taxonomy from the command line.
#
# The flags mirror the controller's declaration (declaration.feature) for the headless path:
# validated the same way, saved to config.json share_meta for that model, and carried on the
# offer as "declared". Explicit flags win over the saved declaration, exactly as --voice wins
# over the saved share_voices profile (applyShareVoice).
#
#   --category <cat>[/<sub>]   e.g. vision/ocr
#   --tag <tag>                repeatable
#   --tuning <t>               base|instruct|finetune|merge|quantized|adapter
#   --policy <grade>           aligned|reduced-refusal|uncensored
#   --license <spdx>
#   --base-model <id>

Feature: roger share declares the model taxonomy from flags

  Scenario: Taxonomy flags ride the offer as declared
    Given a share of model "qwen3-vl-8b" detected as "vision/vlm"
    When the operator runs share with "--category vision/ocr --tag nameplate-ocr --tag manufacturing --tuning finetune --policy aligned --license Apache-2.0 --base-model Qwen/Qwen3-VL-8B-Instruct"
    Then the offer taxonomy is category "vision", subcategory "ocr" and source "declared"
    And the offer tags are "nameplate-ocr,manufacturing"
    And the offer carries tuning "finetune", base model "Qwen/Qwen3-VL-8B-Instruct", content policy "aligned" and license "Apache-2.0"

  Scenario: With no flags and nothing saved, the inference goes on air
    Given a share of model "qwen3-vl-8b" detected as "vision/vlm"
    When the operator runs share with ""
    Then the offer taxonomy is category "vision", subcategory "vlm" and source "inferred"

  Scenario: Flags are saved for the model
    Given a share of model "qwen3-vl-8b" detected as "vision/vlm"
    When the operator runs share with "--category vision/ocr --tag ppe"
    Then config.json share_meta for "qwen3-vl-8b" has category "vision/ocr" and tags "ppe"

  Scenario: A saved declaration is used when no taxonomy flag is given
    Given a share of model "qwen3-vl-8b" detected as "vision/vlm"
    And config.json share_meta for "qwen3-vl-8b" has category "vision/ocr" and tags "ppe"
    When the operator runs share with ""
    Then the offer taxonomy is category "vision", subcategory "ocr" and source "declared"
    And the offer tags are "ppe"

  # Any taxonomy flag replaces the saved declaration for that model as a whole, so what is on
  # air is exactly what the command line says - never a blend of today's flags and last
  # month's file.
  Scenario: Any taxonomy flag replaces the saved declaration entirely
    Given a share of model "qwen3-vl-8b" detected as "vision/vlm"
    And config.json share_meta for "qwen3-vl-8b" has category "vision/ocr" and tags "ppe"
    When the operator runs share with "--tag manufacturing"
    Then the offer taxonomy is category "", subcategory "" and source "declared"
    And the offer tags are "manufacturing"
    And config.json share_meta for "qwen3-vl-8b" has category "" and tags "manufacturing"

  Scenario: A category without a subcategory is accepted
    Given a share of model "qwen3-vl-8b" detected as "vision/vlm"
    When the operator runs share with "--category vision"
    Then the offer taxonomy is category "vision", subcategory "" and source "declared"

  Scenario: Saved declarations for other models are left alone
    Given a share of model "qwen3-vl-8b" detected as "vision/vlm"
    And config.json share_meta for "llama3.2" has category "language/chat" and tags "x"
    When the operator runs share with "--category vision/ocr"
    Then config.json share_meta for "llama3.2" has category "language/chat" and tags "x"

  Scenario Outline: An invalid flag stops the share before anything goes on air
    Given a share of model "qwen3-vl-8b" detected as "vision/vlm"
    When the operator runs share with "<flags>"
    Then share fails with an error mentioning "<reason>"
    And nothing went on air
    And config.json share_meta for "qwen3-vl-8b" is unchanged

    Examples:
      | flags                         | reason                   |
      | --category medical            | unknown category         |
      | --category vision/code        | not a vision subcategory |
      | --tag a/b                     | invalid tag              |
      | --tag diagnosis               | reserved tag             |
      | --tuning lora                 | unknown tuning           |
      | --policy nsfw                 | unknown content policy   |

  Scenario: The taxonomy flags are listed in share --advanced help
    When the operator runs share with "--advanced"
    Then the help lists "--category", "--tag", "--tuning", "--policy", "--license" and "--base-model"

  # Regression (slice-3 review): the invalid-flag outline starts from an empty share_meta, so
  # it proves nothing was ADDED; this proves an existing declaration SURVIVES a refused flag.
  Scenario: An invalid flag leaves an existing saved declaration intact
    Given a share of model "qwen3-vl-8b" detected as "vision/vlm"
    And config.json share_meta for "qwen3-vl-8b" has category "vision/ocr" and tags "ppe"
    When the operator runs share with "--tag diagnosis"
    Then share fails with an error mentioning "reserved tag"
    And config.json share_meta for "qwen3-vl-8b" has category "vision/ocr" and tags "ppe"
