# GRANT MANAGEMENT for APPLE-BOUND owners (money/auth path — founder contract in roger-ios
# docs/EXTERNAL-READINESS.md §2, 2026-07-02). Today /grants (POST/GET/DELETE/PATCH) gates on
# payoutOwner, which requires GitHubID != 0 — a payout-grade KYC gate that grant MANAGEMENT
# doesn't need. An Apple-signed-in user with a funded wallet gets 401 "log in to manage keys"
# when minting a key from the iOS app, even though grant SPEND already resolves Apple funder
# wallets (grant.go). The contract:
#   - grant management accepts ANY bound, non-anonymized owner — exactly the set for which
#     accountWalletForOwner resolves (GitHubID != 0 OR AppleSub != ""), pubkey-bound;
#   - payoutOwner itself stays GitHub/KYC-only for actual payouts (unchanged);
#   - NEVER suggest "link GitHub" as a workaround: accountWalletForOwner is GitHub-wins, so
#     linking flips a funded u_apple_ wallet to u_gh_ and strands the Apple balance. (That
#     precedence is EXISTING behavior — pinned here, not changed.)
#
# Ground truth (real code this spec is anchored in):
#   cmd/rogerai-broker/grants_http.go — both gates: payoutOwner(...) -> 401, then
#       owner.GitHubID == 0 || owner.Pubkey == "" -> 403 (collection AND item handlers)
#   cmd/rogerai-broker/payouts.go payoutOwner — signed leg requires rec.GitHubID != 0
#   cmd/rogerai-broker/main.go accountWalletForOwner — GitHub-wins wallet precedence
#   cmd/rogerai-broker/grant.go — grant SPEND already resolves Apple funder wallets
#
# DECISIONS PINNED BY THIS SPEC (await founder approval):
#   G1  a signed request from a BOUND, NON-ANONYMIZED owner may manage grants when
#       GitHubID != 0 OR AppleSub != "" (and the owner has a bound pubkey — grants are keyed
#       on it). The refusal message no longer claims GitHub is required.
#   G2  an Apple-bound owner's minted grant is FUNDED BY the Apple wallet (u_apple_…), via
#       the same accountWalletForOwner resolution spend already uses.
#   G3  payouts remain GitHub/KYC-only: the SAME Apple-bound owner is still refused on the
#       payout surface (payoutOwner unchanged).
#   G4  everything else is byte-identical: anonymous/unbound signed keypairs 401, unsigned
#       401, anonymized (deleted) owners refused, GitHub-linked owners unchanged, the web
#       session leg unchanged.
#   G5  a DUAL-bound owner (GitHub AND Apple) manages grants with the GitHub wallet —
#       the existing GitHub-wins precedence, pinned so the stranding hazard stays visible.

Feature: Grant key management for Apple-bound owners
  As an operator who signed in with Apple (no GitHub account)
  I want to mint, list, inspect, and revoke my grant keys
  So that my bots and devices can use my funded Apple wallet,
  while payouts stay behind the GitHub/KYC gate

  Background:
    Given a broker with a bound APPLE owner "apple-ana" (AppleSub set, no GitHub) whose wallet holds $20.00
    And a bound GITHUB owner "gina" (GitHubID set) whose wallet holds $20.00

  # ── G1: management opens to Apple-bound owners ─────────────────────────────────────────

  Scenario: an Apple-bound owner mints a grant key with a signed request
    When "apple-ana" sends a signed POST /grants with name "my-iphone"
    Then the response status is 200
    And the response carries a one-time grant secret
    And the grant is recorded under "apple-ana"'s pubkey

  Scenario: an Apple-bound owner lists their grants
    Given "apple-ana" has a minted grant "g-ana"
    When "apple-ana" sends a signed GET /grants
    Then the response status is 200
    And the list contains "g-ana"

  Scenario: an Apple-bound owner inspects one grant
    Given "apple-ana" has a minted grant "g-ana"
    When "apple-ana" sends a signed GET /grants/g-ana
    Then the response status is 200

  Scenario: an Apple-bound owner revokes a grant
    Given "apple-ana" has a minted grant "g-ana"
    When "apple-ana" sends a signed DELETE /grants/g-ana
    Then the response status is 200
    And the grant "g-ana" is revoked

  Scenario: an Apple-bound owner updates a grant's caps
    Given "apple-ana" has a minted grant "g-ana"
    When "apple-ana" sends a signed PATCH /grants/g-ana setting a monthly cap
    Then the response status is 200

  Scenario: an Apple-bound owner never sees the GitHub-required refusal
    When "apple-ana" sends a signed POST /grants with name "my-iphone"
    Then the response does not mention GitHub

  # ── G2: the Apple wallet funds the grant ───────────────────────────────────────────────

  Scenario: an Apple owner's grant is funded by the Apple wallet
    When "apple-ana" sends a signed POST /grants with name "my-iphone"
    Then the minted grant's funder wallet is "apple-ana"'s u_apple wallet

  # ── G3: payouts stay GitHub/KYC-only ───────────────────────────────────────────────────

  Scenario: the same Apple-bound owner is still refused on the payout surface
    When "apple-ana" sends a signed payout-status request
    Then the payout request is refused for lacking a GitHub-linked account

  # ── G4: everything else byte-identical ─────────────────────────────────────────────────

  Scenario: a GitHub-linked owner still manages grants exactly as before
    When "gina" sends a signed POST /grants with name "gina-bot"
    Then the response status is 200
    And the response carries a one-time grant secret

  Scenario: a signed-but-unbound (anonymous) keypair is still refused
    When an anonymous signed keypair sends POST /grants
    Then the response status is 401

  Scenario: an unsigned request is still refused
    When an unsigned POST /grants arrives
    Then the response status is 401

  Scenario: an anonymized (deleted) owner is still refused
    Given "apple-ana" has anonymized their account
    When "apple-ana" sends a signed POST /grants with name "ghost"
    Then the response status is one of 401 or 403

  # ── G5: dual-bound precedence pinned (the stranding hazard stays visible) ──────────────

  Scenario: a dual-bound owner manages grants on the GitHub wallet
    Given a bound owner "dana" with BOTH GitHub and Apple identities
    When "dana" sends a signed POST /grants with name "dana-bot"
    Then the response status is 200
    And the minted grant's funder wallet is "dana"'s u_gh wallet
