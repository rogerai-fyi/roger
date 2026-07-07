# GUEST OPERATORS — Phase 3: the session budget on the pre-launch plate (money path).
#
# Phase 1 built the ceiling (ProxyOptionsHolder.Budget, the 402 brake, DefaultSessionBudget
# = $2.00, client.go:409-412) and Phase 2 arms it fresh per handoff (SetBudget + ResetSpend
# + ResetCalls at exec, operator.go:394-396). Phase 3 makes the ceiling VISIBLE and
# ADJUSTABLE at the one moment the user is deciding: the plate.
#
# THE MECHANISM (proposed — [FOUNDER FLAG B1: approve the keystroke, the preset ladder,
# and the values]):
#   · the plate shows "session budget $2.00 · b raises the ceiling"
#   · pressing b cycles the presets: $2.00 → $5.00 → $10.00 → uncapped → $2.00
#   · the choice arms the holder ONLY on y (accept). Cancel discards it; the next plate
#     starts back at the $2.00 default (non-sticky). [FOUNDER FLAG B2: confirm non-sticky.]
#   · "uncapped" is holder Budget 0 (the Phase 1 semantic: 0 = no ceiling) and is loudly
#     marked — the B4 spend-blowout corner is answered by making "no ceiling" impossible
#     to miss, never by hiding it.
#
# Money-path discipline: these scenarios run through the REAL hardened proxy over the stub
# billing broker (the Phase 2 opBDD harness) — spend figures come from the proxy
# accumulator, never from hand-set state.

Feature: The plate's session budget
  The spend ceiling a guest runs under is visible, adjustable, and armed only
  on an explicit accept — and "no ceiling" is impossible to miss.

  Background:
    Given an AGENT session with a tuned band "qwen3-32b-fp8" and a live proxy holder
    And a detected guest "opencode"
    And the open channel's station reports a context window of 131072 tokens

  # ── the default and the ladder ───────────────────────────────────────────────

  Scenario: Every fresh plate starts at the $2.00 default
    When the user runs "/operator opencode"
    Then the plate shows the session budget "$2.00"

  Scenario: b cycles the budget to $5.00
    When the user runs "/operator opencode"
    And the user presses "b"
    Then the plate shows the session budget "$5.00"

  Scenario: b b cycles the budget to $10.00
    When the user runs "/operator opencode"
    And the user presses "b" 2 times
    Then the plate shows the session budget "$10.00"

  Scenario: b b b reaches uncapped, loudly marked (B4)
    When the user runs "/operator opencode"
    And the user presses "b" 3 times
    Then the plate shows the session budget "uncapped"
    And the plate warns "no ceiling - the guest can spend your whole balance"

  Scenario: The ladder wraps back to $2.00
    When the user runs "/operator opencode"
    And the user presses "b" 4 times
    Then the plate shows the session budget "$2.00"
    And the plate does not warn about a missing ceiling

  # ── arming the holder ────────────────────────────────────────────────────────

  Scenario: Accepting the default arms the holder at $2.00 with fresh counters
    When the user runs "/operator opencode"
    And the user presses "y"
    Then the holder budget is the default session budget
    And the holder spend reads $0.00
    And the holder call counter reads 0

  Scenario: Accepting a cycled $5.00 arms the holder at $5.00
    When the user runs "/operator opencode"
    And the user presses "b"
    And the user presses "y"
    Then the holder budget reads $5.00

  Scenario: Accepting uncapped arms the holder at 0 — the Phase 1 uncapped semantic
    When the user runs "/operator opencode"
    And the user presses "b" 3 times
    And the user presses "y"
    Then the holder budget reads uncapped

  Scenario: Cycling without accepting never touches the holder
    When the user runs "/operator opencode"
    And the user presses "b" 2 times
    And the user presses "n"
    Then the holder budget is unchanged

  Scenario: A cancelled choice is not sticky — the next plate is $2.00 again (FOUNDER FLAG B2)
    When the user runs "/operator opencode"
    And the user presses "b" 2 times
    And the user presses "n"
    And the user runs "/operator opencode"
    Then the plate shows the session budget "$2.00"

  # ── the ceiling governs, and the return is honest ────────────────────────────

  Scenario: The armed ceiling brakes the guest at 402 (money path, real proxy)
    When the user runs "/operator opencode"
    And the user presses "y"
    And the guest spends up to the session budget during the handoff
    And the guest returns
    Then the summary notes the session budget was reached
    And the summary spend figure is at or just past the default session budget

  Scenario: A raised ceiling is the one the summary names
    When the user runs "/operator opencode"
    And the user presses "b"
    And the user presses "y"
    And the guest spends up to a "$5.00" ceiling during the handoff
    And the guest returns
    Then the summary notes the session budget was reached
    And the summary names the ceiling "$5.00"

  Scenario: The return still restores the DJ's uncapped session (Phase 2 regression)
    When the user runs "/operator opencode"
    And the user presses "b"
    And the user presses "y"
    And the guest returns with exit 0
    Then the holder budget reads uncapped

  # ── honesty at the edges ─────────────────────────────────────────────────────

  Scenario: A ceiling above the balance warns on the plate (spend-blowout honesty)
    Given the fetched balance is "$3.50"
    When the user runs "/operator opencode"
    And the user presses "b"
    Then the plate shows the session budget "$5.00"
    And the plate warns the ceiling is above the balance

  Scenario: b is a plate key only — at the ask prompt it just types
    When the user has typed "b"
    Then the ask prompt echoes "b"
    And no pre-launch plate is shown

  Scenario: NO_COLOR — the uncapped warning survives without color
    Given color is disabled
    When the user runs "/operator opencode"
    And the user presses "b" 3 times
    Then the plate warns "no ceiling - the guest can spend your whole balance"
