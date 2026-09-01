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

  Scenario: The curated operator's earnings page shows pass-through, not profit
    Then a curated operator's earnings view labels upstream pass-through distinctly
    And never presents reimbursement as income
