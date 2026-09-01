# CURATED routing and the dial filter: through OUR relays, and yours to hide.
#
# Founder, 2026-09-01: "is there still a way to properly route the traffic through our
# broker or any serving router ... enable disable from the filter on the band search ...
# so that all requests go through our system of brokers or towers that are decentralized."
#
# Routing needs no new path: a curated station IS a node, and every request already rides
# broker -> node bridge -> upstream, signed and metered like any relay. What this file
# pins is that the existing guarantees survive the new supply - and that the operator
# holds the off switch.

Feature: Curated traffic rides the existing relay, and the dial filter controls it
  As a consumer on the dial
  I want curated supply routed and metered exactly like any station, and hideable
  So that filling the band never changes what a request through it means.

  # --- routing -----------------------------------------------------------------

  Scenario: A curated request rides the standard relay path
    Given a curated station on a band
    When a consumer sends a request to that band
    Then it is relayed broker-to-node like any request
    And metered, receipted and held like any request

  Scenario: Failover treats curated as one more station on the band
    Given a band with a human station and a curated station
    When the human station fails mid-request
    Then the retry may land on the curated station
    And the receipt names which station served

  Scenario: Routing judges curated by the same terms as any station
    Given a human and a curated station on one band
    Then the router picks by the same price, health and signal rules it always uses
    And no preference for either kind is hard-coded
    # founder 2026-09-01: "not sure" on preferring humans - so neither side gets a thumb
    # on the scale until a ruling says otherwise

  Scenario: Among curated stations of one model, the best connection wins
    Given two curated stations serving the same model via different providers
    When their measured speed and health differ
    Then routing favors the better-measured connection
    # the "best in class connections of the same models" half of what the 30% buys

  Scenario: A consumer can pin a request away from curated entirely
    Given a consumer who has hidden curated supply
    When they send a request on a band with only curated stations
    Then the request fails with "no station on air" naming the hidden curated option
    And nothing silently routes to what they hid

  # --- the filter --------------------------------------------------------------





  # --- anonymization: the other half of what the 30% buys -----------------------
  # Founder, 2026-09-01: the fee is "for the anonymization and routing of our broker and
  # tower network". Anonymization is a checkable property, not adjective: the upstream
  # provider transacts with the STATION's credentials and sees the station's connection -
  # never the consumer's identity, account, key, or address.

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
