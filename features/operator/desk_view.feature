# GUEST OPERATORS — Phase 3: THE DESK roster on the AGENT landing (design doc §3a).
#
# The roster is a STATIC PREVIEW of who can take the mic, rendered by agentView
# (internal/tui/agent.go:1051) in the landing state ONLY: >=1 guest detected
# (m.operatorDetections, the async operatorScanCmd) AND the transcript is empty
# (m.agentLines) AND no turn is running (m.agentBusy / agent.running). It collapses once
# transcript lines exist — the same pattern as the empty-band CTA — and picking happens in
# the /operator modal, never here: carats mean "interactive" in this house, so the roster
# carries NO carat and NO reverse-video row.
#
# THE ZERO-GUEST INVARIANT (pinned as a permanent regression): with zero guests detected,
# today's AGENT screen is BYTE-IDENTICAL — zero new chrome. A user with no agent CLIs
# installed never learns Phase 3 shipped.
#
# Grounding: heading style stSelBar ▌ + stBrand (agent.go:1086); resident ◉ in the house
# red like the connected band row (tui.go:5758-5760); rows follow operator.Registry()
# order; the needs-a-key status is the Guest.NeedsSetup seam (registry.go, reserved for
# the future claude row); the single not-installed suggestion mirrors buildOperatorRows
# (operator.go:153 — at most ONE, only while the desk is sparse).
#
# Phase boundary: the desk STRIP (heading line 2) + the windowshade count are
# desk_strip.feature; the picker/handoff are Phase 2 (operator_command.feature,
# handoff_lifecycle.feature).

Feature: THE DESK roster on the AGENT landing
  A newcomer who lands on AGENT with agent CLIs installed sees, at a glance,
  who can take the mic — and a user with none installed sees exactly today's screen.

  Background:
    Given an AGENT session at the ask prompt

  # ── when the roster renders ──────────────────────────────────────────────────

  Scenario: The roster renders on the landing with one guest detected
    Given detected guests "opencode"
    Then the AGENT landing renders THE DESK roster
    And the roster heading reads "THE DESK"

  Scenario: The roster heading names the tuned band model
    Given an AGENT session with a tuned band "qwen3-32b-fp8" and a live proxy holder
    And detected guests "opencode"
    Then the roster heading subtitle reads "who can take the mic on qwen3-32b-fp8"

  Scenario: No model tuned — the roster still renders, without a model tail
    Given no model is tuned in
    And detected guests "opencode"
    Then the AGENT landing renders THE DESK roster
    And the roster heading subtitle reads "who can take the mic"
    And the roster heading subtitle does not name a model

  Scenario: Zero guests detected — today's screen, byte-identical (permanent regression)
    Given no guest operators are detected
    Then the AGENT view is byte-identical to the pre-desk AGENT view
    And the AGENT view never contains "THE DESK"
    And the AGENT view never contains "at the desk"

  Scenario: The roster waits for the async desk scan to land
    Given the desk scan has not landed yet
    Then the AGENT landing renders no desk chrome

  Scenario: A re-scan that empties the desk removes the roster
    Given detected guests "opencode"
    And the AGENT landing renders THE DESK roster
    When every guest disappears from PATH and the desk is re-scanned
    Then the AGENT landing renders no desk chrome

  # ── the rows ─────────────────────────────────────────────────────────────────

  Scenario: The resident DJ row is first, with the red on-air mark
    Given detected guests "opencode"
    Then the first roster row is the DJ row
    And the DJ row carries the red ◉ on-air mark
    And the DJ row reads "resident · dj.md persona · read/list auto, write/run confirm"

  Scenario: A detected guest row shows name, wire, and status
    Given detected guests "opencode"
    Then the roster row for "opencode" shows wire "hands off"
    And the roster row for "opencode" shows status "guest · on PATH · patches into your open channel"

  Scenario: Guest rows follow registry order
    Given detected guests "aider" and "opencode"
    Then the roster guest rows are "opencode", "aider" in that order

  Scenario: All three MVP guests detected — three guest rows, registry order
    Given detected guests "opencode", "hermes" and "aider"
    Then the roster guest rows are "opencode", "hermes", "aider" in that order

  Scenario: A needs-setup guest row reads "needs a key first" with the /operator pointer
    Given a detected guest "claude" that requires setup
    Then the roster row for "claude" shows status "needs a key first - /operator claude shows how"

  Scenario: An unverified guest row carries the version honesty tag
    Given a detected guest "opencode" with an unproven version "0.9.0"
    Then the roster row for "opencode" notes the version is unproven

  Scenario: At most ONE not-installed suggestion row, at the bottom, while the desk is sparse
    Given detected guests "opencode"
    And undetected registry guests "hermes" and "aider"
    Then the roster shows exactly one not-installed row
    And the not-installed row is the last roster row
    And the not-installed row shows status "not at the desk · get it:" with the install hint

  Scenario: A healthy desk advertises nothing (mirrors the picker rule)
    Given detected guests "opencode" and "aider"
    Then the roster shows no not-installed row

  # ── static preview, not a widget ─────────────────────────────────────────────

  Scenario: The roster is a static preview — no carat, no selection bar
    Given detected guests "opencode"
    Then no roster row carries a carat
    And no roster row is rendered reverse-video

  Scenario: Arrow keys at the prompt never move a roster cursor
    Given detected guests "opencode" and "aider"
    When the user presses "down"
    Then no roster row carries a carat
    And the ask prompt still has focus

  Scenario: Enter at the prompt still asks the DJ, never picks from the roster
    Given detected guests "opencode"
    When the user submits the prompt "hello"
    Then no child process is launched
    And the roster collapses

  # ── when the roster collapses ────────────────────────────────────────────────

  Scenario: The roster collapses once the transcript has lines
    Given detected guests "opencode"
    And the transcript already has lines
    Then the AGENT landing renders no desk roster

  Scenario: The roster collapses the moment the first prompt is submitted
    Given detected guests "opencode"
    When the user submits the prompt "hello"
    Then the AGENT landing renders no desk roster

  Scenario: A busy turn with an empty transcript hides the roster
    Given detected guests "opencode"
    And a DJ turn is in flight
    Then the AGENT landing renders no desk roster

  Scenario: /clear returns the landing, and the roster with it
    Given detected guests "opencode"
    And the transcript already has lines
    When the user runs "/clear"
    Then the AGENT landing renders THE DESK roster

  Scenario: The roster yields to the /operator picker
    Given detected guests "opencode"
    When the user runs "/operator"
    Then the operator picker is open
    And the AGENT view renders the roster at most once

  Scenario: The roster yields to a handoff in staging
    Given an AGENT session with a tuned band "qwen3-32b-fp8" and a live proxy holder
    And detected guests "opencode"
    When the handoff to "opencode" begins
    Then the staged paint does not render THE DESK roster

  Scenario: The roster yields to a pending mutating-tool confirm
    Given detected guests "opencode"
    And a mutating-tool confirm is pending
    Then the AGENT landing renders no desk roster

  # ── rendering discipline ─────────────────────────────────────────────────────

  Scenario: Narrow terminal — every roster row is width-clamped, nothing overflows
    Given detected guests "opencode", "hermes" and "aider"
    And the terminal is 48 columns wide
    Then no AGENT view line exceeds the terminal width

  Scenario: NO_COLOR — the roster degrades to plain glyphs and stays legible
    Given detected guests "opencode"
    And color is disabled
    Then the roster renders without ANSI color
    And the DJ row still carries the ◉ mark as a plain rune
