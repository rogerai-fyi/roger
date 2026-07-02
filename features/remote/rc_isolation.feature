# Executable spec for cross-account ISOLATION + the uniform-error discipline of the /rc/*
# surface (BASE STATION, v5.0.0). The hard security promise: same-account is REQUIRED, and a
# link code alone is never enough. Every attach failure — wrong account (even with the RIGHT
# code), wrong code, revoked, expired, nonexistent — returns a byte-identical constant-work
# uniform 404 "no such session", exactly like bandResolve (cmd/rogerai-broker/band.go:179).
# A session NEVER enters /discover, pickFor, or /market (it is not a node). Ground truth:
# cmd/rogerai-broker/rc.go (Increment 2) + internal/store/rc.go.
#
# INVARIANTS:
#   I1 attaching to my OWN session with the right code succeeds.
#   I2 a DIFFERENT account holding the correct code gets the uniform 404 (same-account is hard).
#   I3 wrong code / revoked / expired / nonexistent ALL return the identical 404 body+status
#      (no enumeration oracle; the band_code_secrecy discipline).
#   I4 an attach token minted for session A cannot drive session B.
#   I5 no remote-control session appears in /discover, /market, or pickFor — it is never routable
#      as a model node and never earns.

Feature: Remote-control sessions are isolated to their owner

  Background:
    Given a running broker
    And an owner "alice" (u_gh_7) with a remote-control session
    And a different owner "mallory" (u_gh_9)

  Scenario: the owner attaches with their own code (I1)
    When alice attaches to her session with the correct code
    Then the attach succeeds and returns a one-time attach token

  Scenario: a different account with the correct code is refused (I2)
    When mallory attaches with alice's correct code
    Then the response is the uniform 404 "no such session"

  Scenario Outline: every negative returns the identical uniform 404 (I3)
    When alice attaches with "<case>"
    Then the response is the uniform 404 "no such session"
    Examples:
      | case              |
      | a wrong code      |
      | a revoked session |
      | an expired code   |
      | a nonexistent id  |

  Scenario: an attach token cannot cross sessions (I4)
    Given alice has a second remote-control session
    When alice sends to the second session using the first session's attach token
    Then the response is unauthorized

  Scenario: a session is never a routable node (I5)
    Then no remote-control session appears in /discover
    And no remote-control session is eligible in pickFor
    And no remote-control session appears in the market
