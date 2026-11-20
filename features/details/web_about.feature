# The web surfaces show the same ABOUT content as the TUI (tui_about.feature), from the same
# /discover fields, with the same source labels and the same band voting rule.
#
# Two web surfaces:
#   1. The LOCAL console's BROWSE tab (internal/webui/assets/console.js buildBrowseRow,
#      /api/browse). Today a read-only table with no detail view. Each row gains an "about"
#      toggle that opens a details row under it (a <tr> with one cell), built with el() and
#      textContent only. One open at a time; it survives a refresh if the same offer is still
#      listed.
#   2. The PUBLIC website /models QSL card (web/app/routes/models/+page.svelte,
#      web/src/js/bands.js). Its CATEGORY block (qslTax) becomes an ABOUT block with the same
#      lines. The website groups offers into bands like the TUI, so it uses the same vote.
#
# Links: only a CURATED page is ever a link, built by the code from a site path (never from
# station text), same-origin, no target=_blank needed. Station text is never linkified.

Feature: The local console and the website show what a model is for

  Background:
    Given these on-air offers:
      | node | model                  | modality | vision | tuning  | base_model | license    | summary          | best_at       | not_for        |
      | n1   | demo-1b-fire-decision  | chat     | no     | adapter | demo-1b    | Apache-2.0 |                  |               |                |
      | n2   | demo-1b-fire-vl        | chat     | yes    | adapter | demo-1b    | Apache-2.0 |                  |               |                |
      | n3   | someone-7b             | chat     | no     | instruct| base-7b    | MIT        | A 7B chat model. | Long answers. | Short replies. |
      | n4   | mystery-m              | chat     | no     |         |            |            |                  |               |                |
    And RogerAI's curated notes describe "demo-1b-fire-decision" and "demo-1b-fire-vl" as in curated_catalog.feature

  # ---------------------------------------------------------------- local console BROWSE
  Scenario: Each BROWSE row has an about toggle
    When the operator opens the console BROWSE tab
    Then every row's model cell has a button labelled "about"
    And the button has aria-expanded "false"

  Scenario: The about toggle opens a details row under the offer
    When the operator clicks "about" on the "demo-1b-fire-decision" row
    Then a details row appears directly under it spanning all 8 columns
    And the button has aria-expanded "true"
    And the details row shows "RogerAI's description of this model"
    And the details row shows "Picks one fire-defense action from site state."
    And the details row shows "Best at: Choosing among a fixed list of actions."
    And the details row shows "Not for: Chat. It answers with one action only."
    And the details row shows "Kind: text in, text out · adapter on demo-1b"
    And the details row shows "License: Apache-2.0"
    And the details row has a link "Model page" to "https://rogerai.fm/research-demo-1b.html#fire"

  Scenario: The vision adapter reads as image in
    When the operator clicks "about" on the "demo-1b-fire-vl" row
    Then the details row shows "Kind: text and image in, text out · adapter on demo-1b"

  Scenario: Sibling models on the same base are named
    When the operator clicks "about" on the "demo-1b-fire-decision" row
    Then the details row shows "Same base: demo-1b-fire-vl"

  Scenario: Declared text is labelled as the station's own words, with no link
    When the operator clicks "about" on the "someone-7b" row
    Then the details row shows "The station's own words"
    And the details row shows "A 7B chat model."
    And the details row has no link

  Scenario: An offer with nothing to say says so
    When the operator clicks "about" on the "mystery-m" row
    Then the details row shows "No description yet. The station has not said what this model is for."

  Scenario: Only one details row is open at a time
    Given the details row for "someone-7b" is open
    When the operator clicks "about" on the "demo-1b-fire-vl" row
    Then only the "demo-1b-fire-vl" details row is open

  Scenario: Clicking about again closes the row
    Given the details row for "someone-7b" is open
    When the operator clicks "about" on the "someone-7b" row
    Then no details row is open
    And the button has aria-expanded "false"

  Scenario: The open row survives a refresh when the offer is still listed
    Given the details row for "someone-7b" on node "n3" is open
    When the operator clicks refresh
    Then the details row for "someone-7b" on node "n3" is open

  Scenario: The open row closes when the offer leaves the market
    Given the details row for "someone-7b" on node "n3" is open
    When node "n3" goes off the market and the operator clicks refresh
    Then no details row is open

  Scenario: The toggle is keyboard reachable
    When the operator tabs to the "about" button on the "someone-7b" row and presses Enter
    Then the details row for "someone-7b" is open

  Scenario: The empty-market and broker-down rows have no toggle
    Given the market is empty
    When the operator opens the console BROWSE tab
    Then no "about" button is shown

  Scenario: The details row wraps long text and never scrolls the page sideways
    Given an offer whose best_at is 200 characters with no spaces
    When the operator opens its details row at 360 pixels wide
    Then the page has no horizontal scroll

  Scenario: The console API passes the fields through unchanged
    When a client reads /api/browse
    Then each offer carries "summary", "best_at", "not_for", "details_source" and "page" when the broker sent them

  # ---------------------------------------------------------------- public website QSL card
  Scenario: The QSL card's CATEGORY block becomes ABOUT
    When a visitor opens the QSL card for "demo-1b-fire-decision" on /models
    Then the card has a block headed "ABOUT"
    And the ABOUT block shows "RogerAI's description of this model"
    And the ABOUT block shows "Picks one fire-defense action from site state."
    And the ABOUT block shows "best at: Choosing among a fixed list of actions."
    And the ABOUT block shows "not for: Chat. It answers with one action only."
    And the ABOUT block shows "kind: text in, text out · adapter on demo-1b"
    And the ABOUT block shows "license: Apache-2.0"
    And the ABOUT block shows the existing category line
    And the ABOUT block links "model page" to "/research-demo-1b.html#fire"

  Scenario: The QSL card uses the band vote
    Given three on-air stations for "someone-7b", two with summary "A 7B chat model." and one with "Other words."
    When a visitor opens the QSL card for "someone-7b"
    Then the ABOUT block shows "A 7B chat model."
    And the ABOUT block shows "the station's own words · 2 of 3 stations"

  Scenario: The QSL card for a band with nothing to say keeps a plain line
    When a visitor opens the QSL card for "mystery-m"
    Then the ABOUT block shows "no description yet"

  Scenario: Switching QSL cards never leaves the previous model's text
    Given a visitor opened the QSL card for "someone-7b"
    When the visitor opens the QSL card for "mystery-m"
    Then the ABOUT block does not show "A 7B chat model."
    And the ABOUT block has no "model page" link

  Scenario: The band list row carries a one-line summary under the name
    When a visitor views the band list on /models
    Then the "demo-1b-fire-decision" row shows its summary in a secondary line, cut with an ellipsis at one line
    And the "mystery-m" row shows no secondary line

  Scenario: The /models page keeps working against a broker without details
    Given a broker whose /discover offers carry no details fields
    When a visitor opens /models
    Then the band list and QSL cards render with no script error
    And every ABOUT block shows "no description yet" or the category line

  Scenario: The website ABOUT block passes the contrast and phone-width checks
    When the /models page is checked at 360 pixels wide in light and dark themes
    Then the ABOUT block has no horizontal overflow
    And its text meets the site's existing contrast test

  Scenario: The QSL "model page" link is only ever a same-origin site path
    Given a /discover offer carrying page "https://example.invalid/x" (a hostile or broken broker)
    When a visitor opens its QSL card
    Then the ABOUT block has no "model page" link
