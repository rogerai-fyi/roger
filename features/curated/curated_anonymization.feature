# ANONYMIZATION: the checkable half of what the routing fee buys.
#
# Founder, 2026-09-01: the 30% pays for "the anonymization and routing of our broker and
# tower network". Anonymization is a property, not an adjective: the upstream provider
# transacts with the STATION's credentials over the STATION's connection, and nothing that
# reaches it can say who the consumer is. These run against the real node-agent serving
# path - the hop where the upstream request is actually built.

Feature: The upstream cannot see the consumer
  As a consumer relaying through a curated station
  I want the commercial provider to see only the station
  So that using the network is not an account I hold with every upstream it proxies.

  Scenario: The upstream never sees the consumer's identity
    Given a consumer with an account and a signed key
    When their request relays through a curated station
    Then the upstream request carries no consumer account, key, or user header
    And the upstream connection originates from the station, not the consumer

  Scenario: Two consumers are indistinguishable to the upstream
    Given two different consumers using the same curated band
    When both requests reach the upstream
    Then nothing in either upstream request tells the consumers apart
    # beyond the prompt text itself, which is theirs to write

  Scenario: The consumer-facing receipt still attributes correctly
    Given an anonymized curated request
    Then the consumer's own receipt names the band, station and split as always
    # anonymity faces the UPSTREAM; the consumer's private history loses nothing
