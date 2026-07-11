Feature: roger.context.v1 canonical signing bytes
  The ONE load-bearing interop contract: the Go canonical() must reproduce the app's
  canonical bytes token-for-token, so an app-signed capsule verifies in Go and vice-versa.
  Byte-parity is pinned by the golden vector (both producers); a signature is asserted to
  VERIFY, never pinned to a fixed value (CryptoKit randomizes the ed25519 nonce).

  Scenario: the golden vector reproduces the brief's bytes for the roger-cli producer
    Given the golden fixture capsule exported by "roger-cli"
    When I compute its canonical bytes
    Then they equal the golden canonical string for "roger-cli"

  Scenario: the golden vector reproduces the brief's bytes for the roger-ios producer
    Given the golden fixture capsule exported by "roger-ios"
    When I compute its canonical bytes
    Then they equal the golden canonical string for "roger-ios"

  Scenario: the two producers differ only in exported_by
    Given the golden fixture capsule exported by "roger-cli"
    And the golden fixture capsule exported by "roger-ios"
    Then their canonical bytes differ only in the exported_by value

  Scenario: a nil model and provider emit the literal null, not an omitted key
    Given the golden fixture capsule exported by "roger-cli"
    When I compute its canonical bytes
    Then the canonical bytes carry a literal null model and provider

  Scenario: the signature field is never part of the canonical bytes
    Given the golden fixture capsule exported by "roger-cli"
    When I set the signature to "deadbeef"
    Then the canonical bytes are unchanged
