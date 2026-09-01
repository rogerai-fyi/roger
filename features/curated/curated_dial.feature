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
    # the consumer sees exactly what the routing fee buys; nothing is folded into a mystery number



  Scenario: A consumer can pin a request away from curated entirely
    Given a consumer who has hidden curated supply
    When they send a request on a band with only curated stations
    Then the request fails with "no station on air" naming the hidden curated option
    And nothing silently routes to what they hid
