# CURATED pricing: the upstream's price, plus the routing fee, with nobody underwater.
#
# Founder, 2026-09-01 (original): "raise the posted prices by 30%". Amended by founder
# ruling later the same day, after the market research (fee survey in the curated-review
# doc): ONE 10% FEE ACROSS BOTH PLANES - the curated markup drops to 10%, and the human
# platform fee drops to 10% with it (tower relay 90/5/5). The mechanism is unchanged;
# only the constant moved.
#
# THE TRAP THIS FILE EXISTS TO PIN. Under the standard settlement a node earns the
# owner share (90% at the current fee) of the posted price. A curated node pays the
# upstream's full list price for every token it relays - so at posted = 1.10 x list,
# the standard split hands it 0.99 x list against a 1.00 x list bill: UNDERWATER ON
# EVERY TOKEN, forever, invisibly (and worse at any higher fee). Curated settlement is
# therefore its own rule: the operator's share is the DECLARED upstream cost, passed
# through; the markup is the broker's routing fee.

Feature: Curated prices derive from the upstream and settle without loss
  As the network operator
  I want curated posted prices and settlement to be a formula, not a hand-typed number
  So that no curated relay can quietly lose money and no consumer is quietly overcharged.

  Background:
    Given a curated station declaring upstream list prices in and out

  # --- the posted price --------------------------------------------------------

  Scenario: The posted price is the declared upstream price plus the routing markup
    Given declared upstream prices of $1.00 in and $2.00 out per 1M
    Then the posted prices are $1.10 in and $2.20 out

  Scenario: The markup is one broker-owned constant
    Then the curated markup is defined in exactly one place
    And changing it re-derives every curated posted price

  Scenario: A free upstream stays free
    Given declared upstream prices of $0 in and $0 out
    Then the posted prices are $0
    And the row still carries the curated mark

  Scenario: A curated node cannot post a price below its declared upstream cost
    When a curated node posts prices under its declared upstream list
    Then the registration is rejected naming the underwater price
    # the broker refuses to build a station that loses money on every request


  # --- settlement --------------------------------------------------------------

  Scenario: Settlement passes the upstream cost through and keeps the markup
    Given a curated request that cost $1.10 at the posted price
    Then the curated operator is credited $1.00
    And the broker retains $0.10

  Scenario: The consumer pays exactly the posted price
    Given a curated request
    Then the consumer's charge equals the posted price
    And their receipt is identical in shape to a human-station receipt

  Scenario: Free seed credits never mint curated earnings
    Given a consumer spending seed credit on a curated band
    Then the metering receipt records the request
    And no payable earning is minted
    # P0-1 holds for curated exactly as for human stations

  Scenario: A curated settlement is marked as curated in the ledger
    Then the ledger row for a curated request carries the curated designation
    And the monthly money-safety sweep can total curated flow separately

  # --- receipts and history ----------------------------------------------------


