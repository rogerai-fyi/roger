# RELAY AUTH (request signing): every SPEND request to the broker is signed with the
# consumer's Ed25519 key, so the broker verifies WHO is spending. This is the P0 fix for the
# old "trust the X-Roger-User header" model where anyone could spend from anyone's wallet by
# setting a header. The signature binds method + path + timestamp + a body digest, and the
# timestamp must be fresh — so a captured signature can't be replayed against a different
# route, a swapped body, or outside a 5-minute window.
#
# GROUND TRUTH (internal/protocol/auth.go):
#   CanonicalRequest = method "\n" path "\n" ts "\n" hex(sha256(body))
#   SignRequest(priv, method, path, body) -> (X-Roger-Pubkey, X-Roger-TS, X-Roger-Sig)
#   VerifyRequest(pubHex, sigHex, ts, method, path, body) -> (userID, ok):
#     - pubHex/sigHex must decode to the right ed25519 lengths
#     - |now - ts| must be <= SigMaxSkew (5 min)  (anti-replay window)
#     - ed25519.Verify over CanonicalRequest must pass
#     - userID = "u_" + sha256(pubHex)[:16]  (stable, opaque, NOT reversible to identity)
#   X-Roger-User is LEGACY/unauthenticated (transition only): it grants nothing; a spend
#   ALWAYS requires a valid signature.
#
# Enforced by: internal/protocol/auth_test.go (+ the broker relay path tests).

Feature: Relay auth — signed spend requests

  # --- the happy path ---------------------------------------------------------
  Scenario: A correctly-signed request verifies and resolves the wallet
    Given a consumer signs "POST /v1/chat/completions" with body B at the current time
    When the broker verifies the request
    Then verification succeeds
    And the resolved user id is "u_" + the first 16 hex of sha256(pubkey)

  Scenario: The same key always maps to the same opaque wallet id
    Given two requests signed by the SAME key
    Then both resolve to the identical user id
    And the id is not reversible to the key holder's real identity

  # --- signature integrity ----------------------------------------------------
  Scenario: A bad signature is rejected
    Given a request whose X-Roger-Sig does not match the body/key
    When the broker verifies the request
    Then verification fails (401), no spend occurs

  Scenario: A swapped body invalidates a captured signature
    Given a valid signature captured for body B
    When the same signature + headers are replayed with a DIFFERENT body B2
    Then the body-digest in CanonicalRequest differs and verification fails

  Scenario: A swapped route invalidates a captured signature
    Given a valid signature for "POST /v1/chat/completions"
    When the same signature is replayed against "POST /v1/embeddings"
    Then the path binding fails verification

  # --- replay window ----------------------------------------------------------
  Scenario: A stale timestamp (older than the skew window) is rejected
    Given a request whose X-Roger-TS is 6 minutes in the past
    When the broker verifies the request
    Then it is rejected as stale (|skew| > 5m)

  Scenario: A future timestamp beyond the skew window is rejected
    Given a request whose X-Roger-TS is 6 minutes in the future
    When the broker verifies the request
    Then it is rejected as skewed

  Scenario: A timestamp inside the window is accepted
    Given a request whose X-Roger-TS is 2 minutes ago
    And the signature is otherwise valid
    Then verification succeeds

  # --- malformed inputs -------------------------------------------------------
  Scenario Outline: Malformed key/signature material is rejected, not crashed
    Given a request with <what>
    When the broker verifies the request
    Then verification fails cleanly (no panic)

    Examples:
      | what                                  |
      | a non-hex X-Roger-Pubkey              |
      | a pubkey of the wrong byte length     |
      | a non-hex X-Roger-Sig                 |
      | a signature of the wrong byte length  |

  # --- the legacy header cannot spend -----------------------------------------
  Scenario: Setting X-Roger-User without a signature spends nothing
    Given a request that sets X-Roger-User to a victim's id but carries NO valid signature
    When the broker processes a spend
    Then no debit is made against the victim's wallet (a spend ALWAYS requires a signature)
