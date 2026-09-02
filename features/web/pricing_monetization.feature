# THE PRICING PAGE tells every way of monetizing, at the ruled 10% fee.
#
# Founder directive 2026-09-01 (after the fee ruling): curated stations are run by human
# operators too - anyone reselling their own provider contracts or committed API capacity.
# The pricing page must present BOTH ways of earning (serve your GPU at 90%, or resell
# your contracts as labeled pass-through), say plainly why the fee exists, and name what
# it buys: privacy-first anonymized routing, receipts, failover, one wallet.

Feature: The pricing page accommodates every way of sharing and monetizing
  As someone deciding whether to put hardware or contracts on the network
  I want every path priced and explained on one page, fee included
  So that nobody discovers a cost or a split after they are on the air.

  Scenario: Reselling API contracts is a first-class path
    When the pricing page renders
    Then it names the curated path: resell your own provider contracts as a labeled proxy
    And it states the settlement rule: the operator gets their declared list back whole, plus half the routing fee
    # amended with the 50/50 fee-pool ruling (founder, 2026-09-01) - was pure pass-through
    And it states that the consumer pays list plus the 10% routing fee

  Scenario: The fee is explained, not just stated
    When the pricing page renders
    # respec 2026-09-02: the why argument merged INTO this page (§0, anchor #why),
    # drawn as the relay wire + claim rows; the fee line reads "a station, never a
    # person" rather than the older "anonymized routing" phrasing.
    Then the 10% is tied to what it buys: the upstream sees a station, never a person
    And the page opens with the merged why argument before any number

  Scenario: The ways in are counted honestly
    # founder 2026-09-02: no wallet riddle - say it straight, and the personal
    # paths (your own station, a friend's station) are first-class free ways in.
    When the pricing page renders
    # founder 2026-09-02: the headline stopped counting; the chips still must
    Then the grid holds six paths and exactly four wear the costs-nothing chip
    And your own station and a friend's station are among the free four

  Scenario: The tower split matches the ruling everywhere on the page
    When the pricing page renders
    Then every relay split reads 90/5/5
    And no 70/30-era number survives outside dated changelogs
