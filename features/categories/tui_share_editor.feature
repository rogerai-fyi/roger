# Slice 4 - the TUI [2] SHARE tab shows and edits what each shared model is for.
#
# The editor is a front-end on the share controller's declaration (declaration.feature):
# it validates through the same SetMeta, so the TUI refuses exactly what the console and
# `roger share` refuse, with the same reason. It is NOT login-gated: describing a model is
# not earning. The node Snapshot carries each row's declared and inferred taxonomy, so the
# TUI and the local web console render from the same data.
#
#   g          open the category editor on the selected row
#   tab / ↑↓   move between fields
#   ←/→        cycle a choice field (category, subcategory, tuning, content policy)
#   typing     edit a text field (tags as a comma list, license, base model)
#   enter      save            ctrl+x  clear the declaration            esc  cancel

Feature: The SHARE tab shows and edits a model's taxonomy

  Background:
    Given the SHARE tab lists "qwen3-vl-8b" inferred as "vision/vlm"
    And the SHARE tab lists "llama3.2" inferred as "language/chat"

  # ---------------------------------------------------------------- the column
  Scenario: The CATEGORY column marks a guess with "~"
    When the operator views the SHARE tab
    Then the row "qwen3-vl-8b" shows category "vision/vlm~"

  Scenario: A declared category shows without the mark
    Given "llama3.2" is declared as "language/code"
    When the operator views the SHARE tab
    Then the row "llama3.2" shows category "language/code"

  Scenario: The snapshot carries both halves for every row
    Given "llama3.2" is declared as "language/code"
    When the node snapshot is taken
    Then the snapshot row "llama3.2" has declared "language/code" and inferred "language/chat"
    And the snapshot row "qwen3-vl-8b" has declared "" and inferred "vision/vlm"

  # ---------------------------------------------------------------- the editor
  Scenario: g opens the editor prefilled with the guess when nothing is declared
    Given the cursor is on the row "qwen3-vl-8b"
    When the operator presses "g"
    Then the category editor is open for "qwen3-vl-8b"
    And the editor shows category "vision" and subcategory "vlm"
    And the editor says the values are a guess until saved

  Scenario: g opens the editor prefilled with the declaration when there is one
    Given "llama3.2" is declared as "language/code" with tags "ppe"
    And the cursor is on the row "llama3.2"
    When the operator presses "g"
    Then the editor shows category "language" and subcategory "code"
    And the editor shows tags "ppe"

  Scenario: The editor does not need a login
    Given the operator is not logged in
    And the cursor is on the row "qwen3-vl-8b"
    When the operator presses "g"
    Then the category editor is open for "qwen3-vl-8b"

  Scenario: Arrow keys cycle the category through the v1 list and back to none
    Given the category editor is open for "llama3.2" with nothing chosen
    When the operator presses "right" 7 times on the category field
    Then the category field went "language", "vision", "audio", "retrieval", "generation", "signals", then none

  Scenario: Changing the category resets the subcategory
    Given the category editor is open for "qwen3-vl-8b"
    When the operator presses "right" 1 times on the category field
    Then the editor shows category "audio" and subcategory ""

  Scenario: The subcategory cycles only within the chosen category
    Given the category editor is open for "qwen3-vl-8b"
    When the operator presses "right" 1 times on the subcategory field
    Then the editor shows category "vision" and subcategory "ocr"

  Scenario: Saving declares the model and closes the editor
    Given the category editor is open for "qwen3-vl-8b"
    When the operator types "manufacturing, ppe" into the tags field
    And the operator presses "enter"
    Then the controller holds for "qwen3-vl-8b" a declaration "vision/vlm" with tags "manufacturing,ppe"
    And the category editor is closed
    And the row "qwen3-vl-8b" shows category "vision/vlm"

  Scenario: Saving while the model is on air says when it applies
    Given "qwen3-vl-8b" is on air
    And the category editor is open for "qwen3-vl-8b"
    When the operator presses "enter"
    Then the status line says "applies the next time it goes on air"

  Scenario: A refused declaration keeps the editor open with the reason
    Given the category editor is open for "qwen3-vl-8b"
    When the operator types "diagnosis" into the tags field
    And the operator presses "enter"
    Then the category editor is open for "qwen3-vl-8b"
    And the editor shows the error "reserved tag"
    And the controller holds no declaration for "qwen3-vl-8b"

  Scenario: ctrl+x clears the declaration and the guess returns
    Given "llama3.2" is declared as "language/code"
    And the category editor is open for "llama3.2"
    When the operator presses "ctrl+x"
    Then the controller holds no declaration for "llama3.2"
    And the row "llama3.2" shows category "language/chat~"

  Scenario: esc cancels without saving
    Given the category editor is open for "qwen3-vl-8b"
    When the operator types "ppe" into the tags field
    And the operator presses "esc"
    Then the category editor is closed
    And the controller holds no declaration for "qwen3-vl-8b"

  Scenario: A voice model's category is fixed to audio
    Given the SHARE tab lists the tts model "kokoro"
    And the category editor is open for "kokoro"
    When the operator presses "right" 1 times on the category field
    Then the editor shows category "audio" and subcategory "tts"

  Scenario: The SHARE tab help names the g key
    When the operator views the SHARE tab
    Then the key hints include "g category"

  # Regression (slice-4 review): a terminal sends a space as its own key, not as a rune.
  Scenario: A typed space is kept in a text field
    Given the category editor is open for "qwen3-vl-8b"
    When the operator types "food safety" into the tags field
    Then the editor shows tags "food safety"

  Scenario: A declaration with no category says so
    Given "llama3.2" is declared with only the license "MIT"
    When the operator views the SHARE tab
    Then the row "llama3.2" shows category "(no category)"
