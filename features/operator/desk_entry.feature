# AGENT [0] DESK ENTRY REDESIGN — the focused landing (design R3, spam-kill, auto-tune).
#
# Founder live-test pain: [0] AGENT dropped into a DEAD ask box and spammed "no station on
# air" on every turn. The redesign, on a FRESH session (nothing ever tuned in - no proxy
# holder bound):
#
#   * a SILENT background auto-tune (R1/R6) finds a FREE band, at $0, no confirm;
#   * when the async desk scan lands GUESTS, THE DESK takes focus as a selectable operator
#     picker (R3) - arrows move the cursor, Enter on the DJ hands focus to the ask box,
#     Enter on a guest opens the pre-launch plate; ANY printable rune falls through to the
#     ask box and de-focuses the desk (the DJ-still-types-through path);
#   * the honest "no station / no free band / no model" states render AT MOST ONCE per
#     state change (noteOnce), never a per-turn pile-up.
#
# A GENUINELY FRESH session has no proxy holder; a session that has tuned in (a live
# holder) keeps the ask box focused and the static desk preview (desk_view.feature) - those
# bytes are unchanged. Grounding: enterAgent / onAgentKey / onOperatorDetected
# (internal/tui/agent.go, operator.go); deskFocused / deskCursor state (tui.go).

Feature: THE DESK takes focus on the AGENT [0] landing
  A newcomer who opens [0] with nothing tuned in lands on a live, selectable desk that
  auto-tunes a free band in the background - never a dead ask box that spams "no station".

  # ── focus lands on the DESK, not the ask box ─────────────────────────────────

  Scenario: A fresh landing with a detected guest gives THE DESK focus
    Given a fresh AGENT session with nothing tuned in
    When the desk scan lands guest "opencode"
    Then THE DESK has focus
    And the ask box is not focused
    And the desk cursor is on the DJ row

  Scenario: A fresh landing with NO guests keeps the ask box focused (zero-guest invariant)
    Given a fresh AGENT session with nothing tuned in
    When the desk scan lands no guests
    Then THE DESK does not have focus
    And the ask box has focus

  Scenario: A tuned-in session (a live holder) never steals focus to the desk
    Given an AGENT session at the ask prompt
    When the desk scan lands guest "opencode"
    Then THE DESK does not have focus
    And the ask box has focus

  # ── arrows move the desk cursor ──────────────────────────────────────────────

  Scenario: Down moves the desk cursor off the DJ onto the first guest
    Given a fresh AGENT session with nothing tuned in
    And the desk scan lands guest "opencode"
    When the user presses "down"
    Then the desk cursor is on "opencode"

  Scenario: The desk cursor never runs past the last operator
    Given a fresh AGENT session with nothing tuned in
    And the desk scan lands guest "opencode"
    When the user presses "down"
    And the user presses "down"
    And the user presses "down"
    Then the desk cursor is on "opencode"

  Scenario: Up from the DJ row stays on the DJ row
    Given a fresh AGENT session with nothing tuned in
    And the desk scan lands guest "opencode"
    When the user presses "up"
    Then the desk cursor is on the DJ row

  # ── type-through: any printable rune falls to the ask box ────────────────────

  Scenario: Typing a printable rune de-focuses the desk and lands in the ask box
    Given a fresh AGENT session with nothing tuned in
    And the desk scan lands guest "opencode"
    When the user types "h"
    Then THE DESK does not have focus
    And the ask box has focus
    And the ask box echoes "h"

  Scenario: A vim-style j does NOT navigate — it types through
    Given a fresh AGENT session with nothing tuned in
    And the desk scan lands guest "opencode"
    When the user types "j"
    Then THE DESK does not have focus
    And the ask box echoes "j"

  # ── Enter on the DJ vs a guest ───────────────────────────────────────────────

  Scenario: Enter on the DJ row hands focus to the ask box
    Given a fresh AGENT session with nothing tuned in
    And the desk scan lands guest "opencode"
    When the user presses "enter"
    Then the ask box has focus
    And THE DESK does not have focus
    And no child process is launched

  Scenario: Enter on a guest row opens the pre-launch plate (auto-tuning a free band first)
    Given a fresh AGENT session with a free band "gpt-oss-20b" on air
    And the desk scan lands guest "opencode"
    When the user presses "down"
    And the user presses "enter"
    Then the pre-launch plate is shown for "opencode"

  Scenario: Enter on a guest with no free band to auto-tune is refused once
    Given a fresh AGENT session with only a paid band on air
    And the desk scan lands guest "opencode"
    When the user presses "down"
    And the user presses "enter"
    Then no pre-launch plate is shown
    And the transcript notes "no channel to patch into"

  # ── the spam regression: the honest state renders at most once ───────────────

  Scenario: The honest empty state renders exactly once (noteOnce)
    Given a fresh AGENT session with an empty market
    When the desk auto-tunes
    And the desk auto-tunes
    Then the transcript shows "no station on air right now" exactly once

  Scenario: A doomed turn parks instead of spamming "no station on air"
    Given a fresh AGENT session with an empty market
    When the user submits the prompt "hello"
    And the desk auto-tunes
    Then the transcript shows "no station on air" at most once
    And no chat turn is submitted

  # ── auto-tune never overrides a deliberate tune ──────────────────────────────

  Scenario: A deliberately-tuned band is never overridden by auto-tune
    Given a fresh AGENT session with a free band "gpt-oss-20b" on air
    And a channel is opened on "llama-3.3-70b-instruct" before the scan lands
    When the desk auto-tunes
    Then the agent runs on "llama-3.3-70b-instruct"
    And no honest "no band" note appears

  # ── the landing DESK collapses once the transcript fills (Phase 3 preserved) ──

  Scenario: The focused desk collapses once the transcript has lines
    Given a fresh AGENT session with nothing tuned in
    And the desk scan lands guest "opencode"
    And the transcript already has lines
    Then the AGENT landing renders no desk roster
