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

  # A live proxy holder with NO resolved model (a disconnected / oddly-seeded re-entry) is
  # NOT a fresh landing and NOT a tuned band: keep the ask box focused, never steal to the
  # desk. Only a genuinely-resolved model surfaces the desk (the once-per-model path below).
  Scenario: A live holder with no resolved model keeps the ask box focused
    Given an AGENT session at the ask prompt
    When the desk scan lands guest "opencode"
    Then THE DESK does not have focus
    And the ask box has focus

  # ── first-time-per-model desk focus: a tuned band surfaces the desk ONCE ──────
  #
  # Founder ruling: when a band IS tuned (resolveAgentModel non-empty), the FIRST AGENT
  # entry for that model this session surfaces the focused desk (cursor on the DJ row) once
  # a guest is detected; a SECOND entry for the same model stays ask-focused. Switching to a
  # DIFFERENT model re-surfaces the desk once for it (operatorSeenModels, per session).

  Scenario: A first entry for a tuned model surfaces the focused desk and marks it seen
    Given an AGENT session whose last band "gpt-oss-20b" is still on air
    When the desk scan lands guest "opencode"
    Then THE DESK has focus
    And the ask box is not focused
    And the desk cursor is on the DJ row
    And the model "gpt-oss-20b" is marked surfaced this session

  Scenario: A second entry for the same model stays ask-focused (surfaced once per model)
    Given an AGENT session whose last band "gpt-oss-20b" is still on air
    And the model "gpt-oss-20b" has already surfaced the desk this session
    When the desk scan lands guest "opencode"
    Then THE DESK does not have focus
    And the ask box has focus

  Scenario: A different model re-surfaces the desk once, even after another model was seen
    Given an AGENT session whose last band "llama-3.3-70b-instruct" is still on air
    And the model "gpt-oss-20b" has already surfaced the desk this session
    When the desk scan lands guest "opencode"
    Then THE DESK has focus
    And the desk cursor is on the DJ row

  Scenario: Type-through de-focuses a tuned-model focused desk and lands the char in ask
    Given an AGENT session whose last band "gpt-oss-20b" is still on air
    And the desk scan lands guest "opencode"
    When the user types "h"
    Then THE DESK does not have focus
    And the ask box has focus
    And the ask box echoes "h"

  # Regression (audit 2026-07-08): the focused-desk hint used to linger on the status line
  # after a type-through defocus, advertising arrow-selection that no longer applied.
  Scenario: Typing through clears the focused-desk hint (no stale arrow-selection advice)
    Given an AGENT session whose last band "gpt-oss-20b" is still on air
    And the desk scan lands guest "opencode"
    When the user types "h"
    Then the AGENT view shows "the DJ has the mic"
    And the AGENT view does not show "choose an operator"

  Scenario: A tuned first-entry with NO guests keeps the ask box focused (zero-guest invariant)
    Given an AGENT session whose last band "gpt-oss-20b" is still on air
    When the desk scan lands no guests
    Then THE DESK does not have focus
    And the ask box has focus

  Scenario: The focused desk shows the choose-an-operator hint
    Given an AGENT session whose last band "gpt-oss-20b" is still on air
    When the desk scan lands guest "opencode"
    Then THE DESK has focus
    And the AGENT view shows "type to just ask"

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

  # A repeated auto-tune trigger does NOT re-spam the honest state (the single-shot guard
  # + noteOnce). The tail-compare dedup itself is pinned by TestNoteOnceDedup.
  Scenario: A repeated auto-tune trigger renders the honest empty state exactly once
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

  # ── the background auto-tune must not steal focus from an active picker ───────

  Scenario: A guest scan then a free-band auto-tune keeps the DESK focused (no focus-steal)
    Given a fresh AGENT session with a free band "gpt-oss-20b" on air
    And the desk scan lands guest "opencode"
    When the desk auto-tunes
    Then THE DESK has focus
    And the agent runs on "gpt-oss-20b"

  # ── auto-tune never overrides a deliberate tune ──────────────────────────────

  Scenario: A deliberately-tuned band is never overridden by auto-tune
    Given a fresh AGENT session with a free band "gpt-oss-20b" on air
    And a channel is opened on "llama-3.3-70b-instruct" before the scan lands
    When the desk auto-tunes
    Then the agent runs on "llama-3.3-70b-instruct"
    And no honest "no band" note appears

  # ── esc-exit clears the desk focus (no dual-focus owner on re-entry) ─────────

  # Audit finding: deskFocused was never cleared on the esc-exit path, so re-entering
  # AGENT yielded a DUAL-focus state (the ask box focused AND the desk focused). Leaving
  # AGENT must reset it so a re-entry has exactly ONE focus owner.
  Scenario: Re-entering AGENT after an esc-exit has exactly one focus owner
    Given a fresh AGENT session with nothing tuned in
    And the desk scan lands guest "opencode"
    When the user presses the escape key
    And the user re-enters AGENT
    Then THE DESK does not have focus
    And the ask box has focus

  # Audit finding: leaving AGENT (esc to BROWSE) during a COLD auto-tune left it armed with
  # a prompt parked; the async result then landed OUTSIDE AGENT - binding a channel and
  # firing the parked turn. The esc-exit must disarm the auto-tune and drop the parked
  # prompt, and a stray auto-tune that resolves after the exit must be a no-op.
  Scenario: Leaving AGENT during a cold auto-tune binds no channel and fires no turn
    Given a fresh AGENT session with a free band "gpt-oss-20b" on air
    When the user submits the prompt "mid-flight ask"
    And the user presses the escape key
    And the desk auto-tunes
    Then no channel is open
    And no chat turn is submitted
    And the auto-tune did not arm

  # ── an ALREADY-CONNECTED auto-tune must not steal focus either ────────────────

  # Audit finding: the free-pick branch already guards focus (f6c5be7), but the
  # already-connected branch yanked focus to the ask box unconditionally. With the desk
  # focused and a user mid-pick, an already-connected auto-tune must NOT steal focus.
  Scenario: An already-connected auto-tune keeps the focused desk (no focus-steal)
    Given a fresh AGENT session with a free band "gpt-oss-20b" on air
    And the desk scan lands guest "opencode"
    And a channel is opened on "llama-3.3-70b-instruct"
    When the desk auto-tunes
    Then THE DESK has focus
    And the agent runs on "llama-3.3-70b-instruct"

  # ── a cold auto-tune that can't reach the broker fails cleanly ────────────────

  # Audit finding: broker-unreachable during the COLD auto-tune (the /discover fetch that
  # feeds the landing) left autoTuning armed and the "finding a free band" beat up until a
  # later rescan. The failure must disarm, splice the beat, and note the honest unreachable
  # state ONCE (noteOnce), dropping any parked prompt silently.
  Scenario: Broker-unreachable during the cold auto-tune clears the beat and notes once
    Given a fresh AGENT session with nothing tuned in
    When the broker is unreachable during the cold auto-tune
    Then the transcript no longer shows "finding a free band"
    And the transcript shows "couldn't reach the broker to find a band" exactly once
    And the auto-tune did not arm

  # A parked prompt typed before the unreachable failure is dropped silently (no band to
  # send it to) and never leaks a chat turn.
  Scenario: A prompt parked before a broker-unreachable cold auto-tune is dropped silently
    Given a fresh AGENT session with nothing tuned in
    When the user submits the prompt "hello"
    And the broker is unreachable during the cold auto-tune
    Then the transcript shows "couldn't reach the broker to find a band" exactly once
    And no chat turn is submitted

  # Audit finding: ANY errMsg disarmed the auto-tune - a NON-unreachable errMsg (e.g.
  # fetchBalance's errMsg("") landing in the cold-fetch window) killed a tune whose
  # /discover then succeeds, and wrongly noted "couldn't reach the broker". The disarm must
  # be scoped to broker-unreachable errors only.
  Scenario: A non-unreachable error during the cold auto-tune does not disarm it
    Given a fresh AGENT session with nothing tuned in
    When the user submits the prompt "still coming"
    And a non-unreachable error arrives during the cold auto-tune
    Then the auto-tune is still armed
    And the transcript no longer shows "couldn't reach the broker to find a band"

  # ── the landing DESK collapses once the transcript fills (Phase 3 preserved) ──

  Scenario: The focused desk collapses once the transcript has lines
    Given a fresh AGENT session with nothing tuned in
    And the desk scan lands guest "opencode"
    And the transcript already has lines
    Then the AGENT landing renders no desk roster
