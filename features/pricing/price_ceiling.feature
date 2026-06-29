# GLOBAL PRICE CEILING: a hard, DELIBERATE safety max on what any station may charge, so a
# fat-fingered / deterrent / abusive price can never land on the market and burn a consumer.
# Founder rule: there is ALWAYS a global max — NO flag exempts it. --private hides a station
# from the public market, but it is NOT a price-bypass: a private (and a confidential) band
# is held to the SAME ceiling as a public one. (Corrects the stale "private bypasses the
# ceiling" claim, which this spec previously asserted and the broker proved false: 400.)
#
# GROUND TRUTH:
#   cmd/rogerai-broker/tunnel.go (register): registerPriceCeiling(reg.Offers) runs
#     UNCONDITIONALLY, before owner-binding / attestation / the private mint — so public,
#     private, and confidential registrations are all checked.
#   cmd/rogerai-broker/pricesafety.go: registerPriceCeiling checks every offer's base AND
#     scheduled-window price against maxPriceOut/InCeiling ($100/$50 per 1M by default).
#   cmd/rogerai-broker/provider_models.go: the same ceiling guards a web-Console price edit.
#
# Enforced by: features/pricing/price_ceiling.feature (this file, executable) +
#   cmd/rogerai-broker/pricesafety_test.go:TestRegisterCeilingGlobalAllBands.

Feature: Global price ceiling — a hard max for EVERY band (public, private, confidential)

  Scenario: A public station over the ceiling is rejected
    Given an owner registers a node priced ABOVE the public ceiling
    When the node is registered as a public station
    Then the registration is rejected by the global ceiling

  Scenario: A PRIVATE band over the ceiling is ALSO rejected (no --private exemption)
    Given an owner registers a node priced ABOVE the public ceiling
    When the node is registered with --private
    Then the registration is rejected by the global ceiling

  Scenario: A CONFIDENTIAL band over the ceiling is ALSO rejected (no tier exemption)
    Given an owner registers a node priced ABOVE the public ceiling
    When the node is registered with the confidential tier
    Then the registration is rejected by the global ceiling

  Scenario: A within-ceiling registration is accepted
    Given an owner registers a node priced WITHIN the public ceiling
    When the node is registered as a public station
    Then the registration is accepted
