Feature: Encrypted stranger transport (Stage 3, client crypto)
  A conversation handed to a MARKETPLACE / STRANGER operator rides as a summary-only,
  owner-signed capsule, encrypted client-side under a one-time code and stored on the broker
  as an opaque blob. The code (never the bytes, never the key) travels the reference channel.
  The encryption key is HKDF-derived from the code and DOMAIN-SEPARATED from the broker lookup,
  so the broker is content-blind by construction.

  Background:
    Given a fresh operator keypair

  Scenario: Round-trip a summary-only capsule under a code
    Given a signed summary-only capsule for a stranger
    When I seal it for a fresh code and open it with the same code
    Then the opened capsule verifies
    And the opened capsule redaction is "summary"

  Scenario: The wrong code recovers no plaintext
    Given a signed summary-only capsule for a stranger
    When I seal it for a fresh code
    And I open it with a different code
    Then opening fails and yields no plaintext

  Scenario: A flipped ciphertext byte fails the GCM tag
    Given a signed summary-only capsule for a stranger
    When I seal it for a fresh code
    And I flip a byte of the sealed blob
    Then opening fails and yields no plaintext

  Scenario: A truncated blob is rejected, never a panic
    Given a signed summary-only capsule for a stranger
    When I seal it for a fresh code
    And I truncate the sealed blob to the nonce length
    Then opening fails and yields no plaintext

  Scenario: A decrypt-valid but signature-tampered capsule still fails ed25519
    Given a signed summary-only capsule for a stranger
    When I tamper the capsule body then seal it for a fresh code
    And I open it with the same code
    Then the opened bytes decrypt but fail verification on merge

  Scenario: The encryption key is domain-separated from the broker lookup
    When I seal any capsule for a fresh code
    Then the derived key does not equal the broker lookup
    And the broker lookup is the sha256 of the canonical code tail
    And from the lookup and the blob alone the plaintext is unrecoverable without the code

  Scenario: The redaction floor holds - a stranger capsule is summary-only
    Given a full-transcript conversation with memory and many turns
    When I build the stranger capsule
    Then the stranger capsule redaction is "summary"
    And it carries only the current turn and no memory

  Scenario: The handing side refuses to seal a full capsule for a stranger
    Given a signed FULL capsule
    When I try to seal it for a stranger under a fresh code
    Then sealing is refused as full-not-allowed-for-stranger

  Scenario: Verify-before-merge survives the decrypt path
    Given a signed summary-only capsule for a stranger
    When I seal it for a fresh code and open it with the same code
    And I merge the opened capsule into a fresh thread
    Then the merge succeeds and appends the turn append-only

  Scenario: Recall rides back under a FRESH code (no key reuse)
    Given a stranger capsule sealed under an outbound code
    When the guest returns a capsule sealed under a fresh recall code
    Then the recall code differs from the outbound code
    And opening the recall with the outbound code fails
