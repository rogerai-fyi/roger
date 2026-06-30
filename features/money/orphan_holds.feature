# Orphan-hold backstop on a rolling deploy (MONEY PATH).
#
# THE BUG: an in-flight relay pre-authorizes the consumer's worst-case cost with a Hold,
# then captures (Finalize) or returns (ReleaseHold) it on a DEFERRED call. When DO kills an
# instance mid-redeploy with SIGKILL, that deferred call never runs: the consumer's credits
# stay debited (the hold is stranded) and accumulate every deploy. There is no hold-expiry
# backstop today.
#
# THE FIX (two parts, founder-approved):
#   1. GRACEFUL DRAIN  - on SIGTERM the broker calls srv.Shutdown(ctx) and WAITS for the
#      in-flight relays to finish (finalize/release their holds) before the process exits,
#      so an orderly redeploy strands nothing.
#   2. BACKSTOP SWEEP  - ReleaseStaleHolds (modeled on the recountHold / nodeBan sweeps)
#      reclaims any tracked hold older than (relay timeout + margin), returning the EXACT
#      held amount to the wallet, for the hard-SIGKILL case the drain can't cover.
#
# GROUND TRUTH:
#   - relay holds live in cmd/rogerai-broker/tunnel.go (HoldFor at dispatch; deferred
#     ReleaseHoldFor; Finalize on capture). The longest legitimate hold is a 300s stream.
#   - the pending-hold registry + sweep live in internal/store (Mem + Postgres).
#   - the drain lives in cmd/rogerai-broker/main.go (runServe).
#
# 1 credit = $1.
Feature: In-flight relay holds are never orphaned by a rolling deploy

  Background:
    Given a fresh ledger-backed store
    And the platform fee rate is 30%

  # --- (1) graceful drain on shutdown ------------------------------------
  # srv.Shutdown lets the in-flight relay return (running its deferred release/finalize)
  # BEFORE the process exits, so an orderly SIGTERM redeploy orphans no hold.

  Scenario: On shutdown, an in-flight relay drains and releases its hold before the server exits
    Given consumer "alice" has 10.00 in real credits
    And an in-flight priced request has placed a 5.00 hold for alice
    When the server is told to shut down while that request is still in flight
    Then the shutdown waits for the in-flight request to finish before returning
    And the in-flight request releases its hold as it drains
    And alice's balance is 10.00

  # --- (2) the backstop sweep reclaims a stranded hold -------------------

  Scenario: A hold older than its TTL is reclaimed by the sweep, restoring the exact held amount
    Given consumer "alice" has 10.00 in real credits
    And request "req-1" places a tracked hold of 4.00 for alice
    And alice's balance is 6.00
    When the stale-hold sweep runs for holds older than the TTL and req-1 is past its TTL
    Then the sweep releases 1 hold
    And alice's balance is 10.00

  # --- (3) a live (within-window) hold is NEVER released -----------------

  Scenario: A hold still inside its valid window is never released by the sweep
    Given consumer "alice" has 10.00 in real credits
    And request "req-live" places a tracked hold of 4.00 for alice
    And alice's balance is 6.00
    When the stale-hold sweep runs for holds older than the TTL and req-live is within its TTL
    Then the sweep releases 0 holds
    And alice's balance is 6.00

  Scenario Outline: The TTL boundary decides release (placed before the cutoff = stale; after = live)
    Given consumer "alice" has 100.00 in real credits
    And request "req-b" places a tracked hold of <hold> for alice
    When the stale-hold sweep runs with the hold placed <age> relative to the cutoff
    Then the sweep releases <released> holds
    And alice's balance is <after>

    Examples:
      | hold  | age           | released | after  |
      | 10.00 | before-cutoff | 1        | 100.00 |
      | 10.00 | after-cutoff  | 0        | 90.00  |
      | 0.01  | before-cutoff | 1        | 100.00 |

  # --- (4) idempotent + single-actor across two instances ---------------
  # Two broker instances run the sweep against the SAME shared store. The atomic
  # delete-and-credit claim means each stranded hold is released EXACTLY once: no
  # double-release, no wallet drift.

  Scenario: Two instances sweeping the same store release a stranded hold exactly once
    Given consumer "alice" has 10.00 in real credits
    And request "req-1" places a tracked hold of 7.00 for alice
    And alice's balance is 3.00
    When two instances run the stale-hold sweep concurrently with req-1 past its TTL
    Then exactly 1 hold is released across both instances in total
    And alice's balance is 10.00

  Scenario: Re-running the sweep after a release is a no-op (idempotent)
    Given consumer "alice" has 10.00 in real credits
    And request "req-1" places a tracked hold of 7.00 for alice
    When the stale-hold sweep runs for holds older than the TTL and req-1 is past its TTL
    And the stale-hold sweep runs again for holds older than the TTL
    Then the second sweep releases 0 holds
    And alice's balance is 10.00

  # --- (5) a normal Finalize still settles correctly (no regression) ----
  # Capture clears the tracked hold, so a settled request is NEVER reclaimed by a later
  # sweep (no double-refund), and the money math is unchanged.

  Scenario: A normal capture settles correctly and is never reclaimed by a later sweep
    Given consumer "alice" has 10.00 in real credits
    And node "n1" is owned by account "op1"
    And request "req-1" places a tracked hold of 5.00 for alice
    When request req-1 is captured via Finalize with cost 2.00 and owner share 1.40
    Then alice's balance is 8.00
    And operator "op1" has earned 1.40
    When the stale-hold sweep runs for holds older than the TTL and req-1 is past its TTL
    Then the sweep releases 0 holds
    And alice's balance is 8.00

  Scenario: A released hold is cleared so a later sweep never double-refunds it
    Given consumer "alice" has 10.00 in real credits
    And request "req-1" places a tracked hold of 5.00 for alice
    When request req-1 is released via the deferred relay release
    Then alice's balance is 10.00
    When the stale-hold sweep runs for holds older than the TTL and req-1 is past its TTL
    Then the sweep releases 0 holds
    And alice's balance is 10.00
