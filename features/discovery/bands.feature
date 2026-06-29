# PRIVATE BANDS (frequency codes): a node can share on a HIDDEN band instead of the public
# market — reachable ONLY by whoever knows its secret frequency code. The secret is shown ONCE
# at mint and never stored (only sha256(code) is), the band is invisible to /discover + /market.
# --private is a DISCOVERY choice, NOT a price-bypass: a private band is held to the SAME global
# price ceiling as a public one (see features/pricing/price_ceiling.feature).
#
# GROUND TRUTH:
#   internal/store/band.go: CreateBand stores code_hash = sha256(canonical secret tail); the
#     code itself is NEVER persisted. code_display is the cosmetic (non-secret) "147.520 MHz · …".
#   cmd/rogerai-broker/band.go: bandResolve()/resolveFreqAllow(freq) -> the allow set (the
#     hidden node) for a presented code; bandView() returns non-secret metadata only.
#   cmd/rogerai-broker/pricesafety.go + tunnel.go(register): registerPriceCeiling runs
#     UNCONDITIONALLY, so the out/in price CEILING binds private + confidential bands too -
#     --private does NOT lift it (only hides the station from the public market).
#
# Enforced by: cmd/rogerai-broker/band_test.go + internal/store band tests. (Doc spec; convertible.)

Feature: Private bands — hidden frequency codes

  Scenario: Minting a band shows the secret code ONCE and stores only its hash
    When an operator shares "gpt-oss-20b" with --private
    Then a frequency code is generated and shown ONCE
    And only sha256(code) (code_hash) is persisted, never the code itself
    And a cosmetic non-secret display string is stored for the owner's re-display

  Scenario: Resolving the correct code reaches the hidden node
    Given a node is on a private band with a known frequency code
    When a consumer presents that code
    Then resolveFreqAllow returns the allow set containing only that node
    And routing for the model is restricted to that node

  Scenario: A wrong or unknown code reaches nothing
    Given a private band exists
    When a consumer presents a code that doesn't match any code_hash
    Then no band resolves and no hidden node is reachable

  Scenario: A private band is invisible to the public market
    Given a node shares ONLY on a private band
    When anyone GETs /discover or /market
    Then the band's node never appears (privacy: code-holders only)

  Scenario: bandView never leaks the secret
    When the band's metadata is fetched
    Then it returns the cosmetic display + non-secret fields only
    And never the frequency code or its hash in a usable form

  # CORRECTED (was "Private sharing bypasses the public price ceiling" - false; the broker
  # rejects an over-ceiling private registration with 400). The global ceiling is pinned
  # executably in features/pricing/price_ceiling.feature (public + private + confidential).
  Scenario: Private sharing is ALSO subject to the global price ceiling
    Given the global price ceiling would reject a high price
    When the operator shares with --private instead
    Then the registration is still rejected by the ceiling (--private is not a price-bypass)
