# Receipt signature versions — the broker's counter-signature changed which bytes it
# covers on 2026-08-02 (it now covers the broker-set billing fields, which it previously
# did not). Receipts co-signed BEFORE that change are already persisted, so verification
# must be able to tell the two forms apart instead of reading all history as forged.
#
# GROUND TRUTH: internal/protocol/protocol.go
#   SigVersion  — broker-set; absent/0 = legacy (node form), 1 = current (broker form).
#                 It lives INSIDE the broker-signed bytes, so a v1 receipt cannot be
#                 downgraded to the legacy rule and then edited freely.
#   VerifyBrokerCoverage(pubHex) -> (ok, covers) — `covers` is false for a legacy
#                 signature: it is genuine but is NOT evidence of the billed counts.

Feature: A broker signature declares which canonical form it covers
  Verification distinguishes a legacy counter-signature from a current one, and reports
  honestly which fields each actually protects.

  # --- versioning the broker signature so old receipts stay verifiable ------
  #
  # Receipts co-signed before the coverage repair were signed over the NODE form. They
  # are already persisted. Without a version tag a verifier cannot tell which bytes a
  # given receipt was signed over, so every historical receipt would read as forged.
  # SigVersion records it: absent/0 means the legacy form, 1 means the broker form.

  Scenario: A newly co-signed receipt declares the current signature version
    Given the broker counter-signs a receipt
    Then the receipt carries broker signature version 1
    And VerifyBroker verifies it over the broker canonical form

  Scenario: A legacy receipt with no version tag still verifies
    Given a receipt co-signed before the coverage repair, over the node canonical form
    And it carries no broker signature version
    When VerifyBroker checks it
    Then it verifies under the legacy rule
    And the receipt is reported as legacy-signed so its billed counts are known to be uncovered

  Scenario: A legacy receipt's broker-set fields are NOT protected by its signature
    Given a legacy co-signed receipt
    When its BrokerCompletionTokens is altered
    Then VerifyBroker still verifies, because the legacy form never covered that field
    And the verification result reports the legacy coverage so a caller cannot mistake it for proof

  Scenario: A version-1 receipt is not accepted under the legacy rule
    Given a receipt declaring broker signature version 1
    When its signature is actually over the legacy node form
    Then VerifyBroker rejects it

  Scenario Outline: An unknown signature version is rejected
    Given a receipt declaring broker signature version "<version>"
    When VerifyBroker checks it
    Then it is rejected without attempting either canonical form

    Examples:
      | version |
      | 2       |
      | 99      |
      | -1      |
