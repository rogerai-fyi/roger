# CURATED on the DIAL: the badge, the filter, and the wall.
#
# The presentation half of the approved curated spec set, split out verbatim so the TUI
# runner owns it (the wire-level halves live beside it in curated_identity /
# curated_routing_filter, run by the broker suite). Founder rulings 2026-09-01: shown by
# default, with a NEW badge made for it - not a reuse of any existing mark.

Feature: Curated stations are badged, filterable, and counted apart
  As an operator on the dial
  I want curated supply obvious, hideable, and never mixed into the human counts
  So that a fuller band never blurs what kind of station is serving.

  Scenario: The dial badges a curated band
    Given a curated station serving "deepseek-v4" via "openrouter"
    When the band browser lists it
    Then the row carries a curated mark and the provider name
    And it is visually distinct from every human-station badge

  Scenario: The stations wall separates curated from human service
    When the public stations dashboard renders
    Then curated stations are listed under their own heading
    And human-station counts never include them

  Scenario: Curated bands are shown by default, badged
    When the band browser renders with curated stations present
    Then the curated rows appear with their mark
    And the filter line says how many are curated

  Scenario: One toggle hides curated supply
    When the operator toggles curated off in the band filter
    Then every curated-only band disappears from the dial
    And a band with mixed supply stays, counting only its human stations

  Scenario: The toggle persists for the session and is visible while active
    Given the operator toggled curated off
    When they leave and return to the browser
    Then curated is still hidden
    And the filter strip says so

  Scenario: The agent's band resolution respects the filter
    Given the operator hid curated supply
    When the agent auto-tunes a band
    Then it never binds a curated-only band

  Scenario: Declared upstream prices are visible on the row's detail
    Then the band card shows the upstream list price and the routing fee separately
    # the consumer sees exactly what the 30% buys; nothing is folded into a mystery number

  Scenario: The consumer's history shows the routing, privately
    Given a consumer with curated requests in their history
    Then their usage history names the band, the provider and the split
    And no other account can see any of it

  Scenario: The curated operator's earnings page shows pass-through, not profit
    Then a curated operator's earnings view labels upstream pass-through distinctly
    And never presents reimbursement as income
