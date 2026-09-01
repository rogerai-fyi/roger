# CURATED stations: upstream API providers on the dial, labeled as exactly what they are.
#
# Founder direction, 2026-09-01 (having read the "Finding Stations" supply research and
# its Conifer comparison): add curated upstream providers as an OPTION on the dial so the
# band looks full while human supply grows - routed through our brokers and towers,
# filterable, at a posted markup that pays for the routing, receipts and history only the
# consumer can see. The research's caution (do not BECOME an API aggregator) is answered
# with honesty rather than abstinence: a curated row never masquerades as a person's GPU.
#
# The plumbing exists: `roger share --upstream` already lets a node serve any
# OpenAI-compatible API, and the broker relays to nodes it does not distinguish. What is
# missing is the IDENTITY - a signed, first-class "this is a proxied commercial API"
# declaration - and everything honest that hangs off it.

Feature: A curated station says what it is, everywhere it appears
  As an operator browsing the dial
  I want to tell a person's GPU from a proxied commercial API at a glance
  So that the network's human-provider story stays true while the dial fills out.

  # --- the declaration ---------------------------------------------------------

  Scenario: A node declares itself curated at registration
    When a node registers with curated set and an upstream provider name
    Then the broker records the station as curated
    And the provider name rides its offers

  Scenario: The curated flag is covered by the registration signature
    When a relay tampers with the curated flag in flight
    Then the broker rejects the registration
    # the same rule Private already follows: an identity claim nobody but the
    # node's key can make or unmake

  Scenario: A curated declaration requires the provider's name
    When a node registers curated with no provider name
    Then the registration is rejected with a message naming the missing field

  Scenario: An ordinary share is unchanged
    When a node registers without the curated flag
    Then nothing about its registration or display changes

  # --- honesty on the dial -----------------------------------------------------


  Scenario: A curated station is never counted as a human provider
    Given a band served by one human station and one curated station
    When the market reports providers for that band
    Then the human and curated counts are reported separately

  Scenario: A curated station cannot claim confidential
    When a curated node registers with a TEE attestation
    Then the confidential badge is refused for it
    # the request leaves for a commercial API; no enclave claim survives that hop

  Scenario: A curated station's region is the provider, not a place
    Given a curated station via "conifer"
    Then its region reads as the provider
    And it is never counted in any geographic story

  # --- what curated may never do ----------------------------------------------

  Scenario: A curated flag cannot be added to an already-registered human station
    Given a station registered without the curated flag
    When it re-registers with curated set
    Then the re-registration is treated as a NEW station identity
    # flipping a human callsign to a proxy behind its reputation is identity reuse

