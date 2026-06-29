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
# GROUND TRUTH: internal/protocol/band.go:NewBandCode (returns code, display, tail) +
#   MaskBandDisplay (the per-row re-mask); cmd/rogerai-broker/band.go:mintBandForNode
#   persists CodeDisplay=masked, returns the full code once; band.go:bandResolve hashes the
#   canonical tail of the presented string; band.go:remaskExistingBands + store
#   RemaskBandDisplays re-mask legacy rows at startup.
# Enforced by: this feature (executable) + internal/protocol/band_test.go:
#   TestBandDisplayNotRecoverable + TestNewBandCodeFormat + TestMaskBandDisplay +
#   internal/store/band_test.go:TestRemaskBandDisplays.
#
# LEGACY MIGRATION: the fix stopped NEW mints from persisting the secret, but bands minted
# BEFORE it still had a recoverable "freq · TAIL" display on disk. A one-time, idempotent
# startup migration re-masks every existing row in place: the stored display can no longer
# resolve the band, yet the code_hash is untouched so the owner's saved full code still
# tunes in. (Shown-once model: the code is never re-viewable from the display - lost =>
# revoke + re-mint.)

Feature: Private band code secrecy

  Scenario: The one-time code resolves the band; the persisted display does not
    Given an owner mints a private band
    Then the one-time frequency code resolves to the hidden node
    But the persisted cosmetic display does NOT resolve the band
    And the persisted display carries no recoverable secret tail

  Scenario: A band minted before the fix is re-masked by the one-time migration
    Given a band persisted with the OLD recoverable display (legacy state)
    And the legacy display resolves the hidden node (the vulnerability)
    When the broker runs the band-display re-mask migration
    Then the persisted display no longer resolves the band
    And the owner's saved frequency code still resolves the band (hash unchanged)
    And running the migration again changes nothing
