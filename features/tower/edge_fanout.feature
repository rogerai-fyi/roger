# APPROVED SPEC - founder approved 2026-08-23 ("approved, lets follow your suggestion"),
# formalizing the five points proposed after the relay audit found the two fabrics never
# consider each other: every real consumer hits /v1/chat/completions, which refused
# Towers outright, so no live traffic could ever ride one and Towers could not earn.
# Changes to an approved scenario need re-approval.
# BUILD STATUS: BUILT. The bridge, fan-out coin, tower-to-tower + edge-to-direct fallback shipped with this spec.

Feature: Consumer traffic fans out across both fabrics and falls back
  A consumer asks for a model; which fabric serves it - Core's own share nodes or a
  self-hosted Tower - is Core's placement decision, not a consequence of which endpoint
  the client happened to call. The consumer changes nothing: same endpoint, same
  contract, same headers.

  Background:
    Given a broker with the tower subsystem and a signed-in consumer who accepted the terms

  # --- reachability: the 503 that hid the whole edge fabric ------------------

  Scenario: A model served only behind a Tower is reachable through the ordinary endpoint
    Given a model served by no direct node and one approved Tower's station
    When the consumer requests it through /v1/chat/completions
    Then the answer comes back through the Tower's sealed hub
    And the response carries the same contract shape as a direct relay
    And the receipt names the relay that carried it

  Scenario: The Tower cannot read what it carried
    Given a request that rode the edge fabric via the bridge
    Then the payload the Tower saw was sealed to the station and the answer sealed back
    And Core's own visibility is unchanged from the direct path it already relays

  # --- fan-out: both tiers considered ----------------------------------------

  Scenario: A model served by both fabrics places on either, neither silently preferred
    Given a model served by a direct node and by an approved Tower's station
    When many requests arrive
    Then some are served by the direct node and some ride the Tower
    And no code path excludes a tier that is eligible

  Scenario: Only eligible Towers are candidates
    Given one approved Tower and one quarantined Tower both hosting the model
    Then placement only ever selects the approved one
    And a Tower suspended mid-stream stops receiving new placements immediately

  # --- fallback: a bad Tower never dead-ends a request -----------------------

  Scenario: A failing Tower falls back to another Tower
    Given two approved Towers hosting the model and the first one's hub is dead
    When the consumer requests the model
    Then the request is served by the second Tower
    And the first Tower's failure is recorded on its reputation record

  Scenario: Edge exhaustion falls back to the direct fabric
    Given every hosting Tower is failing and a direct node also serves the model
    When the consumer requests the model
    Then the direct node serves it
    And the consumer never sees the intermediate failures

  Scenario: Total exhaustion is one honest refusal
    Given every hosting Tower is failing and no direct node serves the model
    Then the consumer receives one service-unavailable answer naming the model
    And every hold placed for the failed attempts is released

  # --- money: the same rules as everywhere else ------------------------------

  Scenario: A bridged request bills exactly like an edge request
    Given a priced model served only behind a Tower
    When the consumer's request rides the bridge and settles
    Then the hold was placed on the consumer's account wallet before dispatch
    And settlement splits the price to serving node, Tower, and platform as specced
    And a failed attempt captures nothing and releases its hold

  Scenario: Streaming requests are honest about the edge's shape
    Given a streaming request for a model served only behind a Tower
    Then the answer arrives as a well-formed stream the client already understands
    And the cost headers match what a non-streamed bridged request reports
