# The TUI shows what a model is for, where people already go to inspect one.
#
# [1] TUNE IN already has ONE inspect key: `i` opens the STATION LOG (modeBandDetail,
# internal/tui/update.go, view_band.go bandDetailView), which today prints one taxonomy line
# (bandTaxonomyDetail) above the station table. This spec grows that into an ABOUT block. No
# new mode, no new key, no new screen: the place exists, it was just nearly empty.
#
# The ABOUT block, in this order, each line omitted when it has nothing to say:
#
#   ABOUT  <source label>
#     <summary>
#     best at   <best_at>
#     not for   <not_for>
#     kind      <modality>  ·  <tuning, "adapter on <base_model>" when tuning=adapter>
#     base      <base_model>                      (omitted when already said on the kind line)
#     same base <other on-air bands with the same base_model, up to 4, then "+N more">
#     license   <license>
#     category  <existing taxonomy line: category · declared by n of m · tags · policies>
#     page      <host/path of the curated page>   (curated notes only)
#
# Modality words: chat -> "text in, text out"; chat + declared vision -> "text and image in,
# text out"; tts -> "text in, speech out"; stt -> "speech in, text out".
#
# A band is many stations. The band's text follows the /market voting rule already used for
# its category (foldTaxonomy): curated text when any station carries details_source
# "rogerai"; otherwise the (summary, best_at, not_for) triple MOST of its online human
# stations declared, ties broken by the alphabetically first summary; curated proxies only
# when no human station has text; offline stations only when nothing is on air.
#
# Source labels:
#   "RogerAI's description of this model"          curated
#   "the station's own words · 1 of 3 stations"    declared (n of m that sent THIS text)
#   (no ABOUT heading at all)                      when there is no text and no taxonomy

Feature: The TUI's station log opens with what the model is for

  Background:
    Given the dial shows these stations:
      | station | model                  | modality | vision | tuning  | base_model | license    | online | curated | summary                     | best_at          | not_for       |
      | s1      | demo-1b-chat           | chat     | no     | adapter | demo-1b    | Apache-2.0 | yes    | no      |                             |                  |               |
      | s2      | demo-1b-fire-decision  | chat     | no     | adapter | demo-1b    | Apache-2.0 | yes    | no      |                             |                  |               |
      | s3      | demo-1b-fire-vl        | chat     | yes    | adapter | demo-1b    | Apache-2.0 | yes    | no      |                             |                  |               |
      | s4      | someone-7b             | chat     | no     | instruct| base-7b    | MIT        | yes    | no      | A 7B chat model.            | Long answers.    | Short replies.|
      | s5      | someone-7b             | chat     | no     | instruct| base-7b    | MIT        | yes    | no      | A 7B chat model.            | Long answers.    | Short replies.|
      | s6      | someone-7b             | chat     | no     | instruct| base-7b    | MIT        | yes    | no      | The best model, try it.     |                  |               |
      | s7      | mystery-m              | chat     | no     |         |            |            | yes    | no      |                             |                  |               |
    And RogerAI's curated notes describe "demo-1b-chat", "demo-1b-fire-decision" and "demo-1b-fire-vl" as in curated_catalog.feature

  # ---------------------------------------------------------------- entry, unchanged keys
  @tui
  Scenario: i on a band opens the station log with the ABOUT block first
    Given the cursor is on the band "demo-1b-fire-decision"
    When the operator presses "i"
    Then the screen is the station log for "demo-1b-fire-decision"
    And the first block under the heading is ABOUT
    And the station table follows the ABOUT block

  @tui
  Scenario Outline: The station log keys are unchanged
    Given the station log for "demo-1b-chat" is open
    When the operator presses "<key>"
    Then <result>

    Examples:
      | key   | result                                  |
      | esc   | the screen is the TUNE IN list          |
      | left  | the screen is the TUNE IN list          |
      | h     | the screen is the TUNE IN list          |
      | i     | the screen is the TUNE IN list          |
      | q     | the screen is the TUNE IN list          |
      | enter | the TUI tunes in to "demo-1b-chat"      |

  @tui
  Scenario: The TUNE IN status line names the ABOUT block
    Given the cursor is on the band "demo-1b-chat"
    When the operator presses "i"
    Then the status line reads "about + station log - every station on demo-1b-chat · esc/← back · enter tunes in"

  @tui
  Scenario: The TUNE IN key help lists i as about and station log
    When the operator opens help with "?"
    Then the TUNE IN keys list "i" as "about + station log"

  # ---------------------------------------------------------------- curated content
  @tui
  Scenario: A curated band shows RogerAI's text and the page
    When the operator opens the station log for "demo-1b-fire-decision"
    Then the ABOUT heading reads "ABOUT  RogerAI's description of this model"
    And the ABOUT block shows "Picks one fire-defense action from site state."
    And the ABOUT block shows "best at   Choosing among a fixed list of actions."
    And the ABOUT block shows "not for   Chat. It answers with one action only."
    And the ABOUT block shows "kind      text in, text out  ·  adapter on demo-1b"
    And the ABOUT block has no "base" line
    And the ABOUT block shows "license   Apache-2.0"
    And the ABOUT block shows "page      rogerai.fm/research-demo-1b.html#fire"

  @tui
  Scenario: Sibling bands on the same base are named
    When the operator opens the station log for "demo-1b-fire-decision"
    Then the ABOUT block shows "same base demo-1b-chat, demo-1b-fire-vl"

  @tui
  Scenario: The vision adapter reads as image in
    When the operator opens the station log for "demo-1b-fire-vl"
    Then the ABOUT block shows "kind      text and image in, text out  ·  adapter on demo-1b"

  @tui
  Scenario: Same-base siblings are capped at four
    Given six more on-air bands with base model "demo-1b"
    When the operator opens the station log for "demo-1b-chat"
    Then the "same base" line names 4 bands then "+4 more"

  @tui
  Scenario: Off-air bands are not named as siblings
    Given an off-air band "demo-1b-old" with base model "demo-1b"
    When the operator opens the station log for "demo-1b-chat"
    Then the "same base" line does not name "demo-1b-old"

  @tui
  Scenario: A band is never its own sibling, including another quant of the same model
    Given a second band "demo-1b-chat" with quant "Q8_0" and base model "demo-1b"
    When the operator opens the station log for "demo-1b-chat"
    Then the "same base" line does not name "demo-1b-chat"

  @tui
  Scenario: Sibling matching is on the canonical base model
    Given an on-air band "demo-1b-x" with base model " DEMO-1B "
    When the operator opens the station log for "demo-1b-chat"
    Then the "same base" line names "demo-1b-x"

  # ---------------------------------------------------------------- declared content and voting
  @tui
  Scenario: Declared text follows the majority of online human stations
    When the operator opens the station log for "someone-7b"
    Then the ABOUT heading reads "ABOUT  the station's own words · 2 of 3 stations"
    And the ABOUT block shows "A 7B chat model."
    And the ABOUT block shows "best at   Long answers."
    And the ABOUT block shows "not for   Short replies."

  @tui
  Scenario: A tie is broken by the alphabetically first summary
    Given station "s5" goes off air
    When the operator opens the station log for "someone-7b"
    Then the ABOUT block shows "A 7B chat model."
    And the ABOUT heading reads "ABOUT  the station's own words · 1 of 2 stations"

  @tui
  Scenario: Fields are never mixed across stations
    Given station "s4" has summary "A 7B chat model." and no best_at
    And station "s5" has summary "Other words." and best_at "Long answers."
    And station "s6" is off air
    When the operator opens the station log for "someone-7b"
    Then the ABOUT block shows "A 7B chat model."
    And the ABOUT block has no "best at" line

  @tui
  Scenario: Curated proxies speak only when no human station has text
    Given a curated proxy station on "someone-7b" with summary "Proxy words."
    When the operator opens the station log for "someone-7b"
    Then the ABOUT block shows "A 7B chat model."
    And the ABOUT block does not show "Proxy words."

  @tui
  Scenario: A band with no text and no taxonomy has no ABOUT block
    When the operator opens the station log for "mystery-m"
    Then the station log has no ABOUT block
    And the station log shows "no description yet - the station has not said what this model is for"

  @tui
  Scenario: A band with taxonomy but no text shows the kind and category only
    Given station "s7" declares category "language/chat" and license "MIT"
    When the operator opens the station log for "mystery-m"
    Then the ABOUT heading reads "ABOUT"
    And the ABOUT block shows "license   MIT"
    And the ABOUT block has no "best at" or "not for" line

  @tui
  Scenario: A declared band shows no page line
    When the operator opens the station log for "someone-7b"
    Then the ABOUT block has no "page" line

  # ---------------------------------------------------------------- the category line moved, not lost
  @tui
  Scenario: The existing category line is kept inside the ABOUT block
    Given station "s4" declares category "language/chat", tags "ops" and policy "aligned"
    When the operator opens the station log for "someone-7b"
    Then the ABOUT block contains the line produced by the existing category detail
    And the station log shows the category detail exactly once

  @tui
  Scenario: The reduced-refusal grade note still shows
    Given station "s4" declares policy "uncensored"
    When the operator opens the station log for "someone-7b"
    Then the station log shows "RogerAI still screens every request"

  # ---------------------------------------------------------------- layout
  @tui
  Scenario Outline: Long text wraps at word boundaries within the width
    Given station "s4" has best_at of 200 characters made of 8-letter words
    And the terminal is <w> columns wide
    When the operator opens the station log for "someone-7b"
    Then no rendered line is wider than <w> columns
    And the best_at text is split only at spaces
    And continuation lines are indented under the value column

    Examples:
      | w   |
      | 40  |
      | 60  |
      | 80  |
      | 120 |
      | 200 |

  @tui
  Scenario: A single word longer than the width is hard-broken, not overflowed
    Given station "s4" has summary of one 140-character word
    And the terminal is 40 columns wide
    When the operator opens the station log for "someone-7b"
    Then no rendered line is wider than 40 columns

  @tui
  Scenario: Wide characters are measured by display width
    Given station "s4" has summary of 140 "火" characters
    And the terminal is 60 columns wide
    When the operator opens the station log for "someone-7b"
    Then no rendered line is wider than 60 display columns

  @tui
  Scenario: On a very narrow terminal only the summary is shown
    Given the terminal is 30 columns wide
    When the operator opens the station log for "demo-1b-fire-decision"
    Then the ABOUT block shows the summary
    And the ABOUT block shows "more at wider width"
    And no rendered line is wider than 30 columns

  @tui
  Scenario: The ABOUT block is capped so the station table stays on screen
    Given the terminal is 80 columns by 24 rows
    And every text field is at its maximum length
    When the operator opens the station log for "someone-7b"
    Then the ABOUT block is at most 10 rows
    And the first station row is visible

  @tui
  Scenario: The compact layout keeps the summary
    Given the TUI is in its compact layout
    When the operator opens the station log for "demo-1b-chat"
    Then the summary line is shown

  # ---------------------------------------------------------------- safety
  @tui
  Scenario: The about block re-canonicalizes on read
    Given station "s4" arrives with summary "a\u001b[31mb" from a broker that did not normalize it
    When the operator opens the station log for "someone-7b"
    Then the rendered screen contains "a[31mb"
    And the rendered screen contains no byte 0x1b from station text

  @tui
  Scenario: Rendering is pure
    Given the station log for "someone-7b" is open
    When the view is rendered twice with the same model
    Then both renders are identical
