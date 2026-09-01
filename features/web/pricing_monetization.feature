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
    And it states the pass-through rule: the operator is reimbursed their declared list, whole
    And it states that the consumer pays list plus the 10% routing fee

  Scenario: The fee is explained, not just stated
    When the pricing page renders
    Then the 10% is tied to what it buys: anonymized routing, receipts, failover, settlement
    And it links the why page for the full argument

  Scenario: The tower split matches the ruling everywhere on the page
    When the pricing page renders
    Then every relay split reads 90/5/5
    And no 70/30-era number survives outside dated changelogs
