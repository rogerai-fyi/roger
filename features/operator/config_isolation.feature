# GUEST OPERATORS — Phase 2: config isolation invariants (cross-guest, security-adjacent).
#
# THE INVARIANT (design doc §4, "avoided by construction"): a handoff NEVER touches the
# user's real agent configs, the user's cwd, or anything outside the one session scratch
# dir. This file is the explicit, adversarial pin of that construction for EVERY guest —
# the scariest failure ("RogerAI clobbered my ~/.config/opencode") must be impossible by
# spec, not by care.
#
# Scratch discipline:
#   - one dir per handoff under os.TempDir(): rogerai-operator-<random>, mode 0700
#     (it holds a file whose CONTENT references the session key env var; 0700 keeps other
#      local users from even enumerating it)
#   - removed on return, clean or crashed exit alike (the cleanup runs in the ExecProcess
#     return callback, which bubbletea delivers for every child outcome)
#   - a crash of ROGER ITSELF mid-handoff leaks the dir; a best-effort sweep of stale
#     rogerai-operator-* dirs (older than 24h) runs at the next desk scan
#   - the SESSION KEY NEVER TOUCHES DISK: config files reference it by env-var name only
#     ({env:ROGER_SESSION_KEY} / ${ROGER_SESSION_KEY}); aider gets it purely via env

Feature: Never touch the user's config — the scratch-dir invariant
  Every byte a handoff writes lands inside one throwaway session scratch dir,
  the session secret never lands on disk at all, and the dir is gone when the
  guest leaves the desk — however the guest left.

  Background:
    Given a live proxy session at "http://127.0.0.1:44017/v1" with session key "sk-test-0123" and band model "qwen3-32b-fp8"

  Scenario Outline: Materializing <guest> writes nothing outside the session scratch dir
    Given a sentinel snapshot of the user's home and the launch workdir
    When the <guest> launch is materialized
    Then every written path is under the session scratch dir
    And the user's home matches the sentinel snapshot
    And the launch workdir matches the sentinel snapshot

    Examples:
      | guest    |
      | opencode |
      | hermes   |
      | aider    |

  Scenario Outline: The session key is never written to disk for <guest>
    When the <guest> launch is materialized
    Then no file on the entire scratch path contains "sk-test-0123"

    Examples:
      | guest    |
      | opencode |
      | hermes   |
      | aider    |

  Scenario: The scratch dir is private (0700)
    When the opencode launch is materialized
    Then the session scratch dir has mode 0700

  Scenario: The launch workdir is the user's confirmed workdir, never the scratch dir
    # The guest edits the user's project; only its CONFIG lives in scratch. Mixing the
    # two would make the guest edit throwaway files and then delete its own work.
    When the opencode launch is materialized
    Then the child working directory is the launch workdir
    And the child working directory is not the session scratch dir

  Scenario Outline: Cleanup after a clean exit removes the whole scratch dir for <guest>
    When the <guest> launch is materialized
    And the guest exits cleanly
    Then no rogerai-operator scratch dir remains for this session

    Examples:
      | guest    |
      | opencode |
      | hermes   |

  Scenario Outline: Cleanup after a crash removes the whole scratch dir for <guest>
    When the <guest> launch is materialized
    And the guest crashes with exit 1
    Then no rogerai-operator scratch dir remains for this session

    Examples:
      | guest    |
      | opencode |
      | hermes   |

  Scenario: Cleanup tolerates a guest that deleted its own config
    When the opencode launch is materialized
    And the guest removed files inside the scratch dir before exiting
    Then cleanup still succeeds
    And no rogerai-operator scratch dir remains for this session

  Scenario: A stale scratch dir from a crashed roger is swept at the next desk scan
    Given a leftover "rogerai-operator-dead1" scratch dir older than 24 hours
    And a fresh "rogerai-operator-live2" scratch dir from a running session
    When the desk is scanned
    Then "rogerai-operator-dead1" is removed
    And "rogerai-operator-live2" is untouched

  Scenario: Two rapid handoffs never share a scratch dir
    When the opencode launch is materialized
    And the guest exits cleanly
    And the opencode launch is materialized again
    Then the second session scratch dir differs from the first
    And only the second scratch dir exists

  Scenario: Key exfil by the guest itself is out of scope and bounded by the budget
    # §8 residual: the guest CAN read ROGER_SESSION_KEY from its own env — that is the
    # wire. What bounds the blast radius is the $2 session budget (Phase 1) and the key
    # dying with the session. Pinned here so nobody "fixes" it by writing the key to a
    # file with tighter modes (which would be strictly worse).
    When the opencode launch is materialized
    Then the child env contains ROGER_SESSION_KEY
    And the session budget is capped at the default session budget
