# RELAY AUTH (request signing): every SPEND request to the broker is signed with the
# consumer's Ed25519 key, so the broker verifies WHO is spending. This is the P0 fix for the
# old "trust the X-Roger-User header" model where anyone could spend from anyone's wallet by
# setting a header. The signature binds method + path + timestamp + a body digest, and the
# timestamp must be fresh — so a captured signature can't be replayed against a different
# route, a swapped body, or outside a 5-minute window.
#
# GROUND TRUTH (internal/protocol/auth.go): SignRequest / VerifyRequest over
#   CanonicalRequest = method "\n" path "\n" ts "\n" hex(sha256(body)); |now-ts| <= SigMaxSkew
#   (5m); userID = "u_" + sha256(pubHex)[:16] (stable, opaque, not reversible).
#
# EXECUTABLE: run by godog via internal/protocol/auth_bdd_test.go (the step definitions drive
# the real SignRequest/VerifyRequest). This is the proven pattern for executable behavior specs.

Feature: Relay auth — signed spend requests

  Scenario: A correctly-signed request verifies and resolves the wallet
    Given a fresh consumer keypair
    When the consumer signs "POST" "/v1/chat/completions" with body "hello"
    And the broker verifies the signed request
    Then verification succeeds
    And the user id is derived from the public key

  Scenario: The same key always maps to the same opaque user id
    Given a fresh consumer keypair
    When the consumer signs "POST" "/v1/chat/completions" with body "one"
    And the broker verifies the signed request
    And the consumer signs "POST" "/v1/chat/completions" with body "two"
    And the broker verifies the signed request
    Then both verifications resolve to the same user id

  Scenario: A bad signature is rejected
    Given a fresh consumer keypair
    When the consumer signs "POST" "/v1/chat/completions" with body "hello"
    And the signature is corrupted
    And the broker verifies the signed request
    Then verification fails

  Scenario: A swapped body invalidates a captured signature
    Given a fresh consumer keypair
    When the consumer signs "POST" "/v1/chat/completions" with body "hello"
    And the body is changed to "evil" before verification
    And the broker verifies the signed request
    Then verification fails

  Scenario: A swapped route invalidates a captured signature
    Given a fresh consumer keypair
    When the consumer signs "POST" "/v1/chat/completions" with body "hello"
    And the path is changed to "/v1/embeddings" before verification
    And the broker verifies the signed request
    Then verification fails

  Scenario: A stale timestamp (older than the window) is rejected
    Given a fresh consumer keypair
    When the consumer signs "POST" "/v1/chat/completions" with body "hello"
    And the request timestamp is set to 6 minutes ago
    And the broker verifies the signed request
    Then verification fails

  Scenario: A future timestamp beyond the window is rejected
    Given a fresh consumer keypair
    When the consumer signs "POST" "/v1/chat/completions" with body "hello"
    And the request timestamp is set to 6 minutes ahead
    And the broker verifies the signed request
    Then verification fails

  Scenario: A timestamp inside the window is accepted
    Given a fresh consumer keypair
    When the consumer signs "POST" "/v1/chat/completions" with body "hello"
    And the request timestamp is set to 2 minutes ago
    And the broker verifies the signed request
    Then verification succeeds

  Scenario Outline: Malformed key or signature material is rejected cleanly
    Given a fresh consumer keypair
    When the consumer signs "POST" "/v1/chat/completions" with body "hello"
    And the <field> is set to "<value>"
    And the broker verifies the signed request
    Then verification fails

    Examples:
      | field     | value      |
      | pubkey    | nothex     |
      | pubkey    | aabbcc     |
      | signature | nothex     |
      | signature | aabbcc     |
