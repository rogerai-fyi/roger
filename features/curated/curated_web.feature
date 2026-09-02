# CURATED on the WEB surfaces: the wall, the history, the earnings page.
#
# The web-rendered third of the approved curated spec set, split out verbatim for the web
# suite (slice pending; the TUI and broker thirds live beside it).

Feature: Curated is honest on every web surface
  As an operator or consumer on the web surfaces
  I want curated flow labeled apart wherever money or reputation renders
  So that no dashboard blurs a proxy into the human network.

  Scenario: The stations wall separates curated from human service
    When the public stations dashboard renders
    Then curated stations are listed under their own heading
    And human-station counts never include them

  Scenario: The consumer's history shows the routing, privately
    Given a consumer with curated requests in their history
    Then their usage history names the band, the provider and the split
    And no other account can see any of it

  # Amended by the 50/50 fee-pool ruling (founder, 2026-09-01): a curated operator now
  # earns half the routing fee on top of their reimbursement, so the page's job shifts
  # from "never call it income" to "never blur the two": the provider-bill reimbursement
  # and the fee share are shown apart, and the reimbursement alone is never dressed as profit.
  Scenario: The curated operator's earnings page keeps reimbursement and income apart
    Then a curated operator's earnings view names the list reimbursement and the fee share as two things
    And the reimbursement alone is never presented as income

  # REGRESSION 2026-09-02: /market rows carry curated-only bands as providers:0 +
  # curated_providers:N. The homepage ticker and the models page's /market path
  # counted only providers, so the site said "8 bands on air" while the TUI's
  # dial showed 15 - every curated band silently invisible on the web.
  Scenario: A curated-only band is on air on the website, counted apart
    Given a /market row with zero human providers and one curated provider
    When the homepage market ticker and the models dial render it
    Then the band counts as on air on both
    And the row shows the curated count apart, marked, never added to the human count

  # Founder, 2026-09-02: with 8 human + 7 curated bands live the homepage painted
  # six near-identical human rows. The panel is a shop window, not a leaderboard.
  Scenario: The homepage band panel shows the dial's diversity
    Given more live bands than the six painted rows
    When the panel picks its rows
    Then the three strongest signals anchor the list
    And a live curated band and a live free band are each guaranteed a seat
    And the picked rows still render in signal order
