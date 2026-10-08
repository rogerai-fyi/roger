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


  # superseded 2026-10-05 by contract §14.6 (founder-approved, fairness_and_abuse.feature #16):
  # home stations rank first and curated is overflow unless the consumer opts in; with an
  # opt-in, neither kind gets a thumb on the scale. Old Then: the router picks by the same
  # price, health and signal rules it always uses / And no preference for either kind is hard-coded.
  Scenario: Routing judges curated by the same terms as any station
    Given a human and a curated station on one band
    Then by default the human station is picked while it has room
    And with an opt-in to curated no preference for either kind is hard-coded
    # founder 2026-09-01: "not sure" on preferring humans - so neither side gets a thumb
    # on the scale until a ruling says otherwise

  Scenario: Among curated stations of one model, the best connection wins
    Given two curated stations serving the same model via different providers
    When their measured speed and health differ
    Then routing favors the better-measured connection
    # the "best in class connections of the same models" half of what the routing fee buys


  # --- the filter --------------------------------------------------------------
  # The off switch is a BROKER filter, not a client-side exclude list: the request says
  # `roger.self_hosted_only: true` (contract features/routing/ROUTING-EXPRESSION-CONTRACT.md
  # section 5) and curated stations are ineligible for it - including ones that registered
  # after the consumer last looked at the dial. The TUI's U key and `roger use --self-hosted`
  # both send exactly this key (features/routing/regression_pins.feature, defect 3).

  Scenario: A consumer that sets roger.self_hosted_only true is never served by a curated station on a mixed band
    Given a mixed band with a human station and a curated station, both healthy and equally priced
    When a funded consumer relays with body roger.self_hosted_only true twelve times
    Then every one of those relays is served by the human station
    And the curated station serves none of them
    # superseded 2026-10-05 by contract §14.6 (founder-approved, fairness_and_abuse.feature #16):
    # curated is overflow without an opt-in. Old line: And without the key the same consumer
    # can still be served by the curated station.
    And without the key the same consumer is served by the curated station once the human station has no room
    # neutrality above still holds for everyone who does not ask; the key only narrows



  # --- anonymization: the other half of what the routing fee buys ----------------
  # Founder, 2026-09-01: the fee is "for the anonymization and routing of our broker and
  # tower network". Anonymization is a checkable property, not adjective: the upstream
  # provider transacts with the STATION's credentials and sees the station's connection -
  # never the consumer's identity, account, key, or address.



