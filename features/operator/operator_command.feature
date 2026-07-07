# GUEST OPERATORS — Phase 2: the /operator command + picker (THE DESK entry points).
#
# /operator joins the ONE canonical agentCommands registry (internal/tui/agent.go:585) so
# the slash-strip, /commands, and TestAgentCommandRegistrySeam (agent_slash_complete_test.go
# :264 — sorted, lowercase, every entry dispatches) pick it up for free. Aliases /mic /guest
# /op follow the /dj /y pattern (agent.go:583): typable, dispatched by runAgentCommand,
# NEVER suggested by the strip and NEVER in the registry.
#
# The picker mirrors the /model modal exactly (open state owns every key — agent.go:307-334;
# render — agent.go:1101-1116; footer case — tui.go:7797-7801): cursor rows, enter picks,
# esc keeps the DJ. §3 rules: bare /operator only opens with >=1 DETECTED guest (zero
# guests = a transcript note, never a one-row picker); rows are detected guests plus AT
# MOST ONE dim not-installed suggestion at the bottom that the cursor SKIPS;
# installed-but-not-configured rows are selectable but print a setup note instead of
# execing (reserved in v1 for the future claude row — every MVP guest is config-generated,
# so none of the three can be "not configured"); the DJ row keeps the current session.
#
# Phase boundary: THIS file specs command dispatch + picker state. What happens after a
# valid pick (preconditions, staging, exec) is handoff_lifecycle.feature. THE DESK roster
# VIEW and the desk strip are Phase 3 — not specced here.

Feature: The /operator command and the hand-the-mic picker
  /operator is a first-class AGENT command with the house registry guarantees,
  and its picker only ever offers a handoff that can actually happen.

  Background:
    Given an AGENT session at the ask prompt

  Scenario: /operator is in the canonical command registry
    Then the agent command registry contains "/operator"
    And the registry stays sorted, lowercase, and duplicate-free

  Scenario: /operator dispatches — never "unknown:"
    When the user runs "/operator"
    Then the transcript does not show "unknown: /operator"

  Scenario Outline: The aliases dispatch but are never suggested
    When the user runs "<alias>"
    Then the transcript does not show "unknown: <alias>"
    And the agent command registry does not contain "<alias>"

    Examples:
      | alias  |
      | /mic   |
      | /guest |
      | /op    |

  Scenario: The slash strip suggests /operator, not the aliases
    When the user has typed "/o"
    Then the suggestion strip includes "/operator"
    And the suggestion strip never includes "/mic" or "/guest"

  Scenario: Bare /operator with zero guests detected prints the desk note, no picker
    Given no guest operators are detected
    When the user runs "/operator"
    Then no picker opens
    And the transcript notes "no guests at the desk"

  Scenario: Bare /operator with guests detected opens the picker
    Given detected guests "opencode" and "aider"
    When the user runs "/operator"
    Then the operator picker is open
    And the picker rows are "DJ", "opencode", "aider"

  Scenario: At most ONE not-installed suggestion row, dim, at the bottom
    Given detected guests "opencode"
    And undetected registry guests "hermes" and "aider"
    When the user runs "/operator"
    Then the picker rows are "DJ", "opencode" and one suggestion row
    And the suggestion row shows an install hint

  Scenario: The cursor skips the not-installed suggestion row
    Given detected guests "opencode"
    And an undetected registry guest "hermes"
    When the user runs "/operator"
    And the user presses down past the last selectable row
    Then the cursor stays on "opencode"
    And the cursor is never on the suggestion row

  Scenario: Enter on a detected, configured guest starts the handoff path
    Given detected guests "opencode"
    When the user runs "/operator"
    And the user picks "opencode"
    Then the picker closes
    And the handoff to "opencode" begins

  Scenario: Enter on an installed-but-not-configured guest prints the setup note, no exec
    Given a detected guest "claudeish" that requires setup
    When the user runs "/operator"
    And the user picks "claudeish"
    Then the picker closes
    And the transcript shows the setup note for "claudeish"
    And no child process is launched

  Scenario: Picking the DJ keeps the current session
    Given detected guests "opencode"
    When the user runs "/operator"
    And the user picks "DJ"
    Then the picker closes
    And the transcript notes the DJ keeps the mic
    And no child process is launched

  Scenario: Esc keeps the DJ
    Given detected guests "opencode"
    When the user runs "/operator"
    And the user presses esc
    Then the picker closes
    And no child process is launched

  Scenario: The open picker owns every key
    # Mirrors the /model modal contract (agent.go:307-334): digits, presets, left/right
    # are swallowed, never stolen by presetForKey or the prompt.
    Given detected guests "opencode"
    When the user runs "/operator"
    And the user presses "1"
    Then the picker is still open
    And the TUI did not switch modes

  Scenario: r re-scans the desk from inside the picker
    Given detected guests "opencode"
    When the user runs "/operator"
    And "aider" appears on PATH
    And the user presses "r"
    Then the picker rows include "aider"

  Scenario: /operator <name> direct-jumps to a detected guest
    Given detected guests "opencode" and "aider"
    When the user runs "/operator aider"
    Then no picker opens
    And the handoff to "aider" begins

  Scenario: /operator <name> for a registry guest not on PATH prints its install hint
    Given no guest operators are detected
    When the user runs "/operator hermes"
    Then no picker opens
    And the transcript shows the install hint for "hermes"

  Scenario: /operator <name> for an unknown name is a note, never a turn
    When the user runs "/operator warez9000"
    Then no picker opens
    And no chat turn is submitted
    And the transcript notes the name is not a known operator

  Scenario: /operator name matching is case-insensitive
    Given detected guests "opencode"
    When the user runs "/operator OpenCode"
    Then the handoff to "opencode" begins
