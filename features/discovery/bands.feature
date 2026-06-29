# PRIVATE BANDS (frequency codes): a node can share on a HIDDEN band instead of the public
# market — reachable ONLY by whoever knows its secret frequency code. The secret is shown ONCE
# at mint and never stored (only sha256(code) is), the band is invisible to /discover + /market,
# and sharing privately BYPASSES the public price ceiling (you set your own price behind a code).
#
# GROUND TRUTH:
#   internal/store/band.go: CreateBand stores code_hash = sha256(canonical secret tail); the
#     code itself is NEVER persisted. code_display is the cosmetic (non-secret) "147.520 MHz · …".
#   cmd/rogerai-broker/band.go: bandResolve()/resolveFreqAllow(freq) -> the allow set (the
#     hidden node) for a presented code; bandView() returns non-secret metadata only.
#   cmd/rogerai-broker/pricesafety.go: the public out/in price CEILING is bypassed with --private
#     (share on a hidden band instead of lowering the price).
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

  Scenario: Private sharing bypasses the public price ceiling
    Given the public output-price ceiling would reject a high price
    When the operator shares with --private instead
    Then the high price is allowed on the hidden band (you price your own private frequency)
