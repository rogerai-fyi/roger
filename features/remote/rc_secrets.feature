# Executable spec for the SECRET discipline of remote-control session link codes + tokens
# (BASE STATION, v5.0.0). Mirrors band_code_secrecy.feature: the link code carries a 40-bit
# Crockford tail, only sha256(tail) is ever stored (RCSession.CodeHash), the full code is
# shown ONCE at enable, and the persisted display is a NON-RECOVERABLE mask. Host + attach
# bearers are likewise stored hash-only. Ground truth: internal/protocol/rc.go (NewRCLinkCode,
# RCLinkShort) wrapping internal/protocol/band.go, internal/store/rc.go (roster, hash-only).
#
# INVARIANTS pinned here:
#   S1 the persisted CodeDisplay can NEVER recover a resolvable tail (BandCodeHash(display)
#      does not match the session's CodeHash).
#   S2 the link code resolves to exactly its session by hash, and typing tolerance
#      (spaces/dashes/case, I/L→1, O→0) still resolves.
#   S3 an RC display is unmistakable from a station band (the "RC " prefix) yet hashes to the
#      SAME key (one constant-work lookup serves both).
#   S4 a rotated code: the OLD hash stops resolving, the NEW hash resolves.
#   S5 host_token / attach_token are stored as hashes only; the roster row carries no bearer.

Feature: Remote-control link codes and tokens keep their secrets

  Background:
    Given a fresh store

  Scenario: the persisted display cannot reconstruct the code (S1)
    When a remote-control session is enabled for wallet "u_gh_7"
    Then the one-time link code is returned exactly once
    And the stored code display is masked and carries no resolvable tail
    And resolving the stored display finds no session

  Scenario: the one-time code resolves to its session, typo-tolerantly (S2)
    Given a remote-control session enabled for wallet "u_gh_7"
    When the owner attaches with the exact link code
    Then it resolves to that session
    When the owner attaches with the link code retyped with lowercase, spaces, and O-for-0
    Then it resolves to that same session

  Scenario: an RC code is visually distinct from a band but hashes the same way (S3)
    When a remote-control session is enabled for wallet "u_gh_7"
    Then the link display begins with the "RC " marker
    And its hash is computed by the same BandCodeHash the private bands use

  Scenario: rotating the code retires the old one (S4)
    Given a remote-control session enabled for wallet "u_gh_7"
    When the owner rotates the session link code
    Then the old link code no longer resolves
    And the new link code resolves to the same session

  Scenario: bearers are stored hash-only (S5)
    Given a remote-control session enabled for wallet "u_gh_7"
    Then the stored session carries a host-token HASH, never the host token
    When a device attaches and receives an attach token
    Then the stored attach token is a HASH, never the token
