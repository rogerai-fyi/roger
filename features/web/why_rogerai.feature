# WHY ROGERAI - the argument that answers "why pay a routing fee at all?".
#
# MERGE RESPEC (founder, 2026-09-02): the why is no longer its own page - "it should
# just be wrapped into the pricing page, probably on the top to quickly show the
# value". The argument now opens pricing.html (§0, anchor #why); why.html survives as
# a pointer stub so old links land. The scenarios below read against pricing.
#
# Founder directive 2026-09-01: the site needs one place that properly explains what the
# network's routing buys - the anonymization story told HONESTLY (identity unlinking is
# not content privacy), the benefits over going direct or through an aggregator, and the
# self-hosting story (towers, private relays). The comparison must stay checkable: no
# competitor numbers we cannot source, no claim the architecture does not enforce.

Feature: The why page sells the routing honestly
  As a consumer deciding between RogerAI, an aggregator, and going direct
  I want the trade told straight, including what the fee does NOT buy
  So that whoever stays was convinced by true things.

  Scenario: The argument opens the pricing page
    When the site builds
    Then the pricing page opens with what the fee buys, before any number
    And why.html survives as a pointer to it

  Scenario: Anonymization is claimed exactly as far as the architecture enforces it
    When the pricing page's why section renders
    Then it says the upstream sees one account and cannot tell consumers apart
    And it says plainly that prompt content still reaches the model's operator
    And it never uses the words "anonymous" or "private" about prompt content on curated bands

  Scenario: The comparison names real differences, not adjectives
    When the pricing page's why section renders
    Then every comparison row is a capability the code enforces or a fact a reader can check
    And human-station supply is presented apart from curated supply
    And the page says what the routing fee pays for in concrete terms

  Scenario: Self-hosting is a first-class exit, not a footnote
    When the pricing page's why section renders
    Then it explains running your own tower on a private network for free
    And it links the tower quickstart
    # the honest sales move: the reader who can leave and stays is the one who trusts you
