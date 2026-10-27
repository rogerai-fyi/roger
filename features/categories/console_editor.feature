# Slice 4 - the local web console (internal/webui) shows and edits a model's taxonomy.
#
# The console renders the node Snapshot (tui_share_editor.feature: every row carries its
# declared and inferred taxonomy) and edits through /api/share/meta (declaration.feature),
# so it validates exactly as the TUI does. The editor is a modal beside the existing Price
# modal; the category list comes from the same closed list the broker serves.

Feature: The local web console shows and edits a model's taxonomy

  Background:
    Given the console serves a station sharing "qwen3-vl-8b" inferred as "vision/vlm"

  Scenario: The live snapshot carries the taxonomy the share table renders
    When the console reads its state
    Then the state row "qwen3-vl-8b" has inferred category "vision/vlm" and no declaration

  Scenario: The share table has a Category column and a Category action
    When the console page is served
    Then the share table has a "Category" column
    And each row offers a "Category" action beside "Price"

  Scenario: The category modal reads, saves and clears through the meta route
    When the console script is served
    Then it reads a model's taxonomy with GET /api/share/meta
    And it saves with POST /api/share/meta
    And it clears with DELETE /api/share/meta

  Scenario: The modal offers exactly the closed category list
    When the console reads the category list
    Then it lists "language, vision, audio, retrieval, generation, signals" with their subcategories

  Scenario: A guess is shown as a guess
    When the console script is served
    Then a category it did not get from a declaration is rendered with the guess mark "~"

  Scenario: The refusal reason is shown, not swallowed
    When the console script is served
    Then a refused save shows the server's reason in the modal

  Scenario: The console's content-policy field carries the moderation note
    When the console page is served
    Then the category modal says "RogerAI still screens every request"
