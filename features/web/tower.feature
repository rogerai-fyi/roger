# THE TOWER - the broker, given a public page.
#
# The broker is named on twenty pages and has a page nowhere. FIG.3 on the homepage draws
# it as one hop of three, which is enough to explain the shape and nothing about why it
# can be trusted with money.
#
# NAMING. "Tower" is the public name, chosen by the founder; "broker" stays the term in
# the code, the API and the manual. The page says so once, plainly, so an engineer reading
# the API docs and a buyer reading the site know they are the same thing.
#
# GROUND TRUTH. Every claim on this page traces to a committed spec, and the two that
# matter are already exhaustively specified:
#
#   features/trust/lineage_receipts.feature - every served request produces a UsageReceipt
#     SIGNED by the serving node and COUNTER-SIGNED by the broker, hash-chained per node,
#     with settlement BOUND to the verified receipt. The node's claimed price is discarded
#     and the broker-resolved price used; billing is min(claim, broker recount) on BOTH
#     token axes; a forged or wrong-key receipt settles nothing and refunds in full.
#
#   features/routing/scoring.feature - pickFor blends price, reliability and speed-fit,
#     divides by a load factor merged across instances, gates on a two-tier health rule,
#     and makes the final choice by power-of-two-choices so load spreads.
#
# WHAT THIS PAGE MAY NOT CLAIM. The tower relays requests, so it necessarily handles
# prompt and completion content. No copy may imply otherwise - privacy.html is where that
# is described, and a "we cannot see your prompts" claim here would be false. No uptime
# figure, latency number, throughput number, or audit we have not had.
#
# Interfaces: web/src/tower.html, web/src/_partials/nav.html, web/build.mjs (CSS bundle
#   + sitemap), web/src/styles/tower.css.
#
# Out of scope: a pricing table (pricing lives on the models pages), an SLA, and any
# operator-facing setup instructions (those are the manual's job).

Feature: The tower explains how a request is routed, metered and guaranteed
  Someone deciding whether to send work - or a machine - through RogerAI should be able
  to see how a station is chosen, what stops a station overcharging, and what happens
  when one fails, without reading the source.

  Background:
    Given a visitor opens the tower page

  # ---- naming and reachability ----------------------------------------------

  Scenario: The tower is reachable and named consistently
    Given a visitor is anywhere on the marketing site
    Then the Models panel offers the tower
    And the page states that the tower is what the API and the manual call the broker
    And the page resolves at "/tower.html"

  # ---- the routing half ------------------------------------------------------

  Scenario: The page shows how a station is chosen, not just that one is
    Then it names price, reliability, and speed-fit as inputs to the score
    And it states that the score is divided by how loaded a station already is
    And it states that load is counted across broker instances, not per instance
    And it states that the final choice is power-of-two-choices so work spreads

  Scenario: The health gate is described as a gate, not a preference
    Then the page states that healthy stations are selected ahead of probationary ones
    And that a probationary station is used only when no healthy one is available

  Scenario: Failover is shown happening, not asserted
    Then the figure shows a request moving to another station when one drops
    And the copy states that the consumer is not charged for a failed attempt

  # ---- the trust half --------------------------------------------------------

  Scenario: The receipt is described as co-signed, not merely issued
    Then the page states that the serving station signs the receipt
    And that the broker counter-signs it
    And that receipts are hash-chained per station
    And that the receipt returns to the caller with the response

  Scenario Outline: The page states each thing that stops a station profiting from a lie
    Then the page states "<guarantee>"

    Examples:
      | guarantee                                                        |
      | the station's claimed price is discarded and the broker's used   |
      | billing takes the lower of the claim and the broker's own recount |
      | cost is capped at the hold that was authorised up front          |
      | a receipt signed with the wrong key settles nothing              |

  Scenario: The failure paths are stated, because they are the guarantee
    Then the page states that a request producing no usable output costs nothing
    And that the hold is refunded when settlement fails
    And that a zero-value receipt is still recorded, so the trail has no gaps

  # ---- honesty ---------------------------------------------------------------

  Scenario: The page does not claim privacy it does not provide
    Then no copy claims the tower cannot see prompts or completions
    And no copy claims requests are end-to-end encrypted from the caller to the station

  Scenario: The page invents no operational numbers
    Then no uptime percentage is stated
    And no latency, throughput, or requests-per-second figure is stated
    And no audit, certification, or compliance attestation is claimed

  Scenario: Every mechanism on the page is one the specs already pin
    Then each stated guarantee corresponds to a scenario in features/trust or features/routing

  # ---- the figures -----------------------------------------------------------

  Scenario: The routing figure is legible without motion
    Given the visitor prefers reduced motion
    Then the patch, the stations and their load are all drawn
    And no fact is available only while the animation runs

  Scenario: The figures work without JavaScript
    Given scripting is disabled
    Then both figures are fully drawn
    And every number and label is present in the served markup

  Scenario: The figures are described for screen readers
    Then each figure has an accessible name
    And decorative layers are hidden from the accessibility tree
    And a text alternative conveys what the figure shows

  Scenario: The page survives a narrow viewport
    Given the visitor is on a narrow screen
    Then the figures scale rather than overflowing
    And the page does not scroll horizontally
