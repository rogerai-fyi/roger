# GUEST OPERATORS — Phase 3: the workdir on the pre-launch plate (design doc §6).
#
# A guest is execed with its cwd set to the confirmed workdir (operator.Command already
# takes it — Phase 2). Phase 3 puts that directory ON the plate so the user confirms WHERE
# the guest will read and write before it runs — and refuses the scariest default, a bare
# $HOME, without an explicit second confirm:
#
#   · the plate shows the RESOLVED ABSOLUTE workdir (agentRoot(), agent.go:1286) — never
#     "." and never an unexpanded ~
#   · workdir == $HOME exactly: the first y does NOT patch. An ember second gate asks
#     again ("this is your whole home directory"); only a second explicit y proceeds.
#     [FOUNDER FLAG W1: approve the double-y mechanism (vs a typed confirm) and that the
#     boundary is EXACTLY $HOME — a child dir like ~/ai/proj single-confirms.]
#   · the aider plate carries the no-auto-commits note: aider mutates git by default, and
#     the Phase 2 env-and-flags strategy already pins --no-auto-commits on the argv
#     (config_aider.feature) — the plate SAYS so, so the founder-approved safety flag is
#     visible, not silent.
#
# The $HOME comparison honors the live HOME env (the BDD sandbox HOME, never the
# developer's real one — the Phase 2 harness discipline).

Feature: The plate's workdir confirm
  The user always confirms where a guest will work, a bare home directory
  takes two explicit yeses, and aider's git safety is stated on the plate.

  Background:
    Given an AGENT session with a tuned band "qwen3-32b-fp8" and a live proxy holder
    And a detected guest "opencode"
    And the open channel's station reports a context window of 131072 tokens

  # ── the field ────────────────────────────────────────────────────────────────

  Scenario: The plate shows the resolved absolute workdir
    Given the session workdir is a project directory
    When the user runs "/operator opencode"
    Then the plate shows the resolved absolute workdir
    And the plate workdir is never "."
    And the plate workdir contains no "~"

  Scenario: Accepting execs the guest in the confirmed workdir
    Given the session workdir is a project directory
    When the user runs "/operator opencode"
    And the user presses "y"
    Then the child working directory is the launch workdir

  # ── the $HOME double confirm ─────────────────────────────────────────────────

  Scenario: A $HOME workdir flags the plate before any y
    Given the session workdir is the user's home directory
    When the user runs "/operator opencode"
    Then the plate warns the workdir is the home directory

  Scenario: The first y on a $HOME workdir does not patch — the second gate opens
    Given the session workdir is the user's home directory
    When the user runs "/operator opencode"
    And the user presses "y"
    Then no handoff staging has begun
    And no child process is launched
    And the home-directory second gate is shown

  Scenario: n at the second gate backs out cleanly
    Given the session workdir is the user's home directory
    When the user runs "/operator opencode"
    And the user presses "y"
    And the user presses "n"
    Then no child process is launched
    And no scratch dir exists for the session
    And the ask prompt still has focus

  Scenario: esc at the second gate backs out too
    Given the session workdir is the user's home directory
    When the user runs "/operator opencode"
    And the user presses "y"
    And the user presses esc
    Then no child process is launched

  Scenario: The second explicit y proceeds — in $HOME, as asked twice
    Given the session workdir is the user's home directory
    When the user runs "/operator opencode"
    And the user presses "y"
    And the user presses "y"
    Then the next paint shows "PATCHING YOU THROUGH"
    And the child working directory is the user's home directory

  Scenario: Deny stays the default at the second gate — stray keys never accept
    Given the session workdir is the user's home directory
    When the user runs "/operator opencode"
    And the user presses "y"
    And the user presses "x"
    Then the home-directory second gate is shown
    And no child process is launched

  Scenario: Boundary — a child directory of $HOME single-confirms (FOUNDER FLAG W1)
    Given the session workdir is a directory inside the user's home
    When the user runs "/operator opencode"
    Then the plate does not warn about the home directory
    When the user presses "y"
    Then the next paint shows "PATCHING YOU THROUGH"

  Scenario: The $HOME check honors the live HOME env
    Given HOME points at the sandbox home for this scenario
    And the session workdir is the user's home directory
    When the user runs "/operator opencode"
    Then the plate warns the workdir is the home directory

  # ── the aider git-safety note ────────────────────────────────────────────────

  Scenario: The aider plate states no-auto-commits
    Given a detected guest "aider" as well
    When the user runs "/operator aider"
    Then the plate shows the aider no-auto-commits note

  Scenario: Accepting the aider plate still pins --no-auto-commits on the argv (Phase 2 tie)
    Given a detected guest "aider" as well
    When the user runs "/operator aider"
    And the user presses "y"
    Then the handoff to "aider" begins
    And the argv still pins "--no-auto-commits"

  Scenario: Non-aider plates carry no aider note
    When the user runs "/operator opencode"
    Then the plate does not show the aider no-auto-commits note

  # ── rendering discipline ─────────────────────────────────────────────────────

  Scenario: Narrow terminal — the home warning is clamped, never dropped
    Given the session workdir is the user's home directory
    And the terminal is 48 columns wide
    When the user runs "/operator opencode"
    Then the plate warns the workdir is the home directory
    And no AGENT view line exceeds the terminal width

  Scenario: NO_COLOR — the home warning survives without color
    Given the session workdir is the user's home directory
    And color is disabled
    When the user runs "/operator opencode"
    Then the plate warns the workdir is the home directory
