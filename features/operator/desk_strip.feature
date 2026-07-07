# GUEST OPERATORS — Phase 3: the desk STRIP (§3a heading line 2) + the windowshade
# count (§3f).
#
# The strip is ONE line in the AGENT heading area, directly under the mode heading:
#
#   ◉ the DJ has the mic  ·  at the desk: opencode · aider  ·  /operator hands off
#
# It renders ONLY when >=1 guest is detected — the zero-guest screen stays byte-identical
# (the same permanent regression as desk_view.feature). Unlike the roster it SURVIVES the
# transcript filling up: once the roster collapses, the strip is the one-line reminder
# that /operator exists. [FOUNDER FLAG S1: confirm the strip persists mid-conversation,
# vs collapsing with the roster.]
#
# WINDOWSHADE (§3f): the compact AGENT heading (agent.go:1078-1081) is one dense line;
# the strip folds into it as a bare count — "· N at the desk" — in the 2000s-MP3-player
# voice. Zero guests: the compact heading is unchanged.
#
# Grounding: heading composition agent.go:1078-1090; ◉ = glyphOnAir in the house red
# (stRed); names joined with " · " in operator.Registry() order; truncVisible clamping.

Feature: The desk strip and the windowshade count
  One glanceable line says who is at the desk and how to hand the mic off,
  and the windowshade folds it to a bare count.

  Background:
    Given an AGENT session at the ask prompt

  # ── the strip ────────────────────────────────────────────────────────────────

  Scenario: The strip renders under the heading with one guest detected
    Given detected guests "opencode"
    Then the AGENT heading area shows the desk strip
    And the desk strip reads "the DJ has the mic"
    And the desk strip reads "at the desk: opencode"
    And the desk strip reads "/operator hands off"

  Scenario: The strip leads with the red on-air mark
    Given detected guests "opencode"
    Then the desk strip leads with the red ◉ on-air mark

  Scenario: Zero guests — no strip, byte-identical heading (permanent regression)
    Given no guest operators are detected
    Then the AGENT view never contains "at the desk"
    And the AGENT view is byte-identical to the pre-desk AGENT view

  Scenario: Strip names ride in registry order, dot-separated
    Given detected guests "aider" and "opencode"
    Then the desk strip reads "at the desk: opencode · aider"

  Scenario: All three guests on the strip
    Given detected guests "opencode", "hermes" and "aider"
    Then the desk strip reads "at the desk: opencode · hermes · aider"

  Scenario: A needs-setup guest is still AT the desk (detected = present)
    Given detected guests "opencode"
    And a detected guest "claude" that requires setup
    Then the desk strip reads "at the desk: opencode · claude"

  Scenario: A not-installed registry guest is never on the strip
    Given detected guests "opencode"
    And undetected registry guests "hermes" and "aider"
    Then the desk strip does not name "hermes"
    And the desk strip does not name "aider"

  Scenario: The strip survives the roster collapsing (FOUNDER FLAG S1)
    Given detected guests "opencode"
    And the transcript already has lines
    Then the AGENT landing renders no desk roster
    But the AGENT heading area shows the desk strip

  Scenario: The strip stays while a turn streams
    Given detected guests "opencode"
    And a DJ turn is in flight
    Then the AGENT heading area shows the desk strip

  Scenario: A re-scan that empties the desk removes the strip
    Given detected guests "opencode"
    When every guest disappears from PATH and the desk is re-scanned
    Then the AGENT view never contains "at the desk"

  Scenario: The strip renders only on the AGENT view
    Given detected guests "opencode"
    When the user switches to the band browser
    Then the band browser view never contains "at the desk"

  Scenario: Narrow terminal — the strip is width-clamped, never wraps
    Given detected guests "opencode", "hermes" and "aider"
    And the terminal is 48 columns wide
    Then no AGENT view line exceeds the terminal width

  Scenario: NO_COLOR — the strip degrades to plain runes
    Given detected guests "opencode"
    And color is disabled
    Then the desk strip renders without ANSI color

  # ── the windowshade count (§3f) ──────────────────────────────────────────────

  Scenario: Compact mode folds the strip to a bare count
    Given detected guests "opencode" and "aider"
    And the windowshade compact view is active
    Then the compact AGENT heading reads "2 at the desk"
    And the compact view does not render the full desk strip
    And the compact view does not render THE DESK roster

  Scenario: Compact count of one
    Given detected guests "opencode"
    And the windowshade compact view is active
    Then the compact AGENT heading reads "1 at the desk"

  Scenario: Compact with zero guests — heading unchanged (permanent regression)
    Given no guest operators are detected
    And the windowshade compact view is active
    Then the compact AGENT heading never contains "at the desk"
    And the compact AGENT heading is byte-identical to the pre-desk compact heading

  Scenario: The compact count updates on a re-scan
    Given detected guests "opencode" and "aider"
    And the windowshade compact view is active
    When "aider" disappears from PATH and the desk is re-scanned
    Then the compact AGENT heading reads "1 at the desk"

  Scenario: Expanding the windowshade restores the full strip
    Given detected guests "opencode" and "aider"
    And the windowshade compact view is active
    When the windowshade is expanded
    Then the AGENT heading area shows the desk strip
