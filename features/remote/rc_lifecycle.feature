# Executable spec for the remote-control session LIFECYCLE (BASE STATION, v5.0.0): enable,
# online/offline, reconnect, disable, revoke-all, account-delete, idle GC, and the per-owner
# quota. Ground truth: internal/store/rc.go (RCSessionQuota=5, RCHostOfflineAfter=30s,
# RCIdleGC=7d, RevokeRCSessions), cmd/rogerai-broker/rc.go (Increment 2), account.go.
#
# INVARIANTS:
#   L1 enabling returns a session; the roster lists it as online right after a host poll.
#   L2 host silent > 30s → shown offline; a reconnect (same host token) resumes the SAME id.
#   L3 disable revokes the session + its attach tokens and pushes a terminal frame.
#   L4 revoke-all ends every session for the wallet; account deletion does the same.
#   L5 a session idle > 7 days is garbage-collected.
#   L6 a 6th active session is refused (quota is 5).

Feature: Remote-control session lifecycle

  Background:
    Given a fresh store

  Scenario: enable then online (L1)
    When wallet "u_gh_7" enables a remote-control session named "hermes · RogerAI"
    Then the session is listed for that wallet
    And after a host poll the session reports online

  Scenario: offline then reconnect keeps the id (L2)
    Given an enabled session for "u_gh_7" that has polled once
    When 31 seconds pass with no host poll
    Then the session reports offline
    When the host reconnects with its host token
    Then the session reports online again with the same id

  Scenario: disable revokes the session and its tokens (L3)
    Given an enabled session for "u_gh_7" with one attached device
    When the owner disables the session
    Then the session is revoked
    And the device's attach token no longer authorizes sends

  Scenario: revoke-all and account delete end everything (L4)
    Given "u_gh_7" has 3 enabled sessions
    When the owner revokes all remote-control sessions
    Then all 3 sessions are revoked
    Given "u_gh_7" enables another session
    When the account "u_gh_7" is deleted
    Then that session is revoked too

  Scenario: idle sessions are garbage-collected (L5)
    Given an enabled session for "u_gh_7" whose host last polled 8 days ago
    When the idle sweep runs
    Then the session is garbage-collected

  Scenario: the quota caps active sessions at 5 (L6)
    Given "u_gh_7" has 5 enabled sessions
    When the owner enables a 6th remote-control session
    Then it is refused for exceeding the session quota
