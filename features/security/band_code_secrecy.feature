# PRIVATE BAND CODE SECRECY (fixed vulnerability -> permanent regression pin).
#
# A private band's secret frequency code is shown ONCE at mint. What the broker PERSISTS
# (code_display) is a MASKED cosmetic string ("147.520 MHz · ••••-••••") that CANNOT be
# canonicalized back to the secret tail - so it can NEVER reconstruct or resolve the band.
# Only the one-time full code resolves it.
#
# WAS VULNERABLE: NewBandCode embedded the secret tail in the persisted display
# (display = freq + "·" + tail), so CanonicalBandTail(code_display) recovered the tail and
# BandCodeHash(code_display) resolved the band straight out of persisted state. Now the
# one-time code and the persisted display are separated.
#
# GROUND TRUTH: internal/protocol/band.go:NewBandCode (returns code, display, tail);
#   cmd/rogerai-broker/band.go:mintBandForNode persists CodeDisplay=masked, returns the
#   full code once; band.go:bandResolve hashes the canonical tail of the presented string.
# Enforced by: this feature (executable) + internal/protocol/band_test.go:
#   TestBandDisplayNotRecoverable + TestNewBandCodeFormat.

Feature: Private band code secrecy

  Scenario: The one-time code resolves the band; the persisted display does not
    Given an owner mints a private band
    Then the one-time frequency code resolves to the hidden node
    But the persisted cosmetic display does NOT resolve the band
    And the persisted display carries no recoverable secret tail
