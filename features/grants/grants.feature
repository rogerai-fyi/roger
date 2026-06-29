# GRANTS: owner-issued private access keys. An operator mints a grant so their bots / family
# can use the operator's nodes (or any model) under caps the owner controls — without sharing the
# owner's own signing key. The grant SECRET is shown once and never stored (only its hash), and
# every spend through a grant is bounded by its caps, allow-lists, revocation, and expiry.
#
# GROUND TRUTH (internal/store/grant.go + cmd/rogerai-broker/grant.go, grants_http.go):
#   Grant: secret_hash (auth lookup; the secret is NEVER stored), Nodes/Models allow-lists,
#     Free, prices, RPM/Burst, DailyCap/MonthlyCap (tokens, 0 = unlimited), ExpiresAt (0 = never),
#     Revoked. GrantBySecretHash(hash) resolves a presented secret. SetGrantRevoked toggles it.
#     AddGrantUsage/GrantUsageOf track per-UTC-day/month token usage for the caps. GrantPatch:
#     a nil field means "leave unchanged".
#
# Enforced by: cmd/rogerai-broker/grant_test.go, grants_http_test.go + internal/store grant tests.

Feature: Grants — owner-issued private access keys

  Scenario: Minting a grant shows the secret once and stores only its hash
    Given an operator mints a grant labelled "my-bots"
    Then a grant secret is shown ONCE
    And only sha256(secret) (secret_hash) is persisted, never the secret

  Scenario: A request bearing a valid grant secret resolves the grant
    Given a grant exists for "my-bots"
    When a bot presents the grant secret
    Then GrantBySecretHash resolves it and the request is served under that grant

  Scenario: A revoked grant is rejected
    Given a grant that the owner has revoked
    When a bot presents its secret
    Then the request is rejected (revoked grants serve nothing)

  Scenario: An expired grant is rejected
    Given a grant whose ExpiresAt is in the past
    When a bot presents its secret
    Then the request is rejected (expired)

  Scenario: Token caps bound usage per UTC day and month
    Given a grant with a DailyCap of 100k tokens
    When usage reaches the daily cap
    Then further requests on that grant are refused until the next UTC day
    And a MonthlyCap is enforced the same way across the month

  Scenario: An unlimited cap (0) never blocks on volume
    Given a grant with DailyCap 0 and MonthlyCap 0
    Then token volume alone never refuses it

  Scenario: Node and model allow-lists scope a grant
    Given a grant restricted to node "n1" and model "gpt-oss-20b"
    When a bot uses it for a different node or model
    Then the request is refused (outside the grant's allow-list)

  Scenario: A free grant serves at no spend
    Given a grant marked free
    Then requests through it cost the owner/bot nothing

  Scenario: Patching a grant leaves nil fields unchanged
    Given an existing grant
    When the owner PATCHes only Revoked=true (other fields nil)
    Then only revocation changes; caps, prices, and allow-lists are untouched
