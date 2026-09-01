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

Feature: Curated traffic rides the existing relay
  As a consumer on the dial
  I want curated supply routed and metered exactly like any station, and hideable
  So that filling the band never changes what a request through it means.

  # --- routing -----------------------------------------------------------------

  Scenario: A curated request rides the standard relay path
    Given a curated station on a band
    When a consumer sends a request to that band
    Then it is relayed broker-to-node like any request
    And metered, receipted and held like any request


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
    # the "best in class connections of the same models" half of what the routing fee buys


  # --- the filter --------------------------------------------------------------





  # --- anonymization: the other half of what the routing fee buys ----------------
  # Founder, 2026-09-01: the fee is "for the anonymization and routing of our broker and
  # tower network". Anonymization is a checkable property, not adjective: the upstream
  # provider transacts with the STATION's credentials and sees the station's connection -
  # never the consumer's identity, account, key, or address.



