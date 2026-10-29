# Slice 4 - the TUI dial ([1] TUNE IN) shows what each band is for and filters by it.
#
# The dial groups /discover offers into bands by (model, quant) (internal/tui/view_band.go
# groupBands). A band's category follows the /market rule (discovery_taxonomy.feature): the
# pair MOST of its online HUMAN stations carry, ties broken alphabetically; curated proxies
# label only a band no human station labels, and a band with no station on air falls back to
# every station so it still reads (founder ruling 2026-10-05, slice-4 review). The label says when it is
# only a guess: a trailing "~" when no station backing it DECLARED it - the same honesty
# mark the dial already uses for an inferred lineage (⌁~). Absent claims nothing: a band
# whose stations carry no taxonomy shows no label (band_badges.feature).
#
# G cycles the category filter, exactly as Q cycles the quant filter: off, then each
# category on the dial in canonical order, then off.

Feature: The TUI dial labels bands by category and filters by it

  Background:
    Given the dial shows these stations:
      | station | model        | category | subcategory | source   | tags          | policy     |
      | s1      | qwen3-vl-8b  | vision   | ocr         | declared | manufacturing | aligned    |
      | s2      | qwen3-vl-8b  | vision   | ocr         | inferred |               |            |
      | s3      | nomic-embed  | retrieval| embeddings  | inferred |               |            |
      | s4      | dolphin-8b   | language | chat        | declared |               | uncensored |
      | s5      | mystery-m    |          |             |          |               |            |

  # ---------------------------------------------------------------- labels
  Scenario: A band shows its category, unmarked when a station declared it
    When the operator looks at the dial
    Then the band "qwen3-vl-8b" is labelled "vision/ocr"

  Scenario: A band whose category is only inferred is marked as a guess
    When the operator looks at the dial
    Then the band "nomic-embed" is labelled "retrieval/embeddings~"

  Scenario: A band with no taxonomy carries no label
    When the operator looks at the dial
    Then the band "mystery-m" has no category label

  Scenario: A declared reduced-refusal or uncensored grade is shown on the band
    When the operator looks at the dial
    Then the band "dolphin-8b" shows the grade "uncensored"
    And the band "qwen3-vl-8b" shows no grade badge

  Scenario: The legend explains the guess mark
    When the operator looks at the dial
    Then the badge legend explains "~" as "guessed, not declared"

  Scenario: Stations that disagree: the most common pair labels the band
    Given station "s6" also serves "qwen3-vl-8b" as "vision/vlm" declared
    And station "s7" also serves "qwen3-vl-8b" as "vision/vlm" declared
    And station "s8" also serves "qwen3-vl-8b" as "vision/vlm" declared
    When the operator looks at the dial
    Then the band "qwen3-vl-8b" is labelled "vision/vlm"

  # ---------------------------------------------------------------- band detail
  Scenario: The band detail lists the taxonomy in full
    When the operator opens the band "qwen3-vl-8b"
    Then the detail shows category "vision/ocr" declared by 1 of 2 stations
    And the detail shows tags "manufacturing"
    And the detail shows content policies "aligned"

  Scenario: The band detail says plainly that moderation still applies to an uncensored band
    When the operator opens the band "dolphin-8b"
    Then the detail shows content policies "uncensored"
    And the detail says "RogerAI still screens every request"

  # ---------------------------------------------------------------- the G filter
  Scenario: G cycles through the categories on the dial, in canonical order, then off
    When the operator presses "G" 4 times
    Then the category filter went "language", "vision", "retrieval", then off

  Scenario: The category filter shows only bands in that category
    When the operator presses "G" 2 times
    Then the visible bands are "qwen3-vl-8b"
    And the dial header shows "[cat:vision]"

  Scenario: An inferred category filters the same as a declared one
    When the operator presses "G" 3 times
    Then the visible bands are "nomic-embed"

  Scenario: A band with no category is hidden while a category filter is on
    When the operator presses "G" 1 times
    Then the band "mystery-m" is not visible

  Scenario: The category filter composes with the other filters
    Given the free filter is on
    And station "s4" charges for its band
    When the operator presses "G" 1 times
    Then the visible bands are ""

  Scenario: With no categorized bands on the dial, G says so and stays off
    Given the dial shows only uncategorized stations
    When the operator presses "G" 1 times
    Then the category filter is off
    And the status line says "no categorized bands on the dial"

  Scenario: The cursor stays on a visible band when the filter narrows the list
    Given the cursor is on the band "mystery-m"
    When the operator presses "G" 2 times
    Then the cursor is on the band "qwen3-vl-8b"

  # ---------------------------------------------------------------- the /market rule (review)
  Scenario: Curated stations only label a band no human station labels
    Given curated station "c1" also serves "qwen3-vl-8b" as "language/chat" declared
    And curated station "c2" also serves "qwen3-vl-8b" as "language/chat" declared
    When the operator looks at the dial
    Then the band "qwen3-vl-8b" is labelled "vision/ocr"

  Scenario: A curated-only band takes its curated stations' category
    Given curated station "c3" also serves "proxy-m" as "language/code" declared
    When the operator looks at the dial
    Then the band "proxy-m" is labelled "language/code"

  Scenario: Offline stations do not vote while a station is on air
    Given offline station "o1" also serves "qwen3-vl-8b" as "vision/vlm" declared
    And offline station "o2" also serves "qwen3-vl-8b" as "vision/vlm" declared
    And offline station "o3" also serves "qwen3-vl-8b" as "vision/vlm" declared
    When the operator looks at the dial
    Then the band "qwen3-vl-8b" is labelled "vision/ocr"

  Scenario: A band with no station on air still shows its stations' category
    Given offline station "o4" also serves "dark-m" as "vision/ocr" declared
    When the operator looks at the dial
    Then the band "dark-m" is labelled "vision/ocr"
