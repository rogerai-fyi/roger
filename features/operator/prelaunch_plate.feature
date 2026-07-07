# GUEST OPERATORS — Phase 3: the pre-launch plate (design doc §6; adversarial B4/B6 + P1).
#
# Between picking a guest and PATCHING YOU THROUGH there is now ONE confirm plate — the
# same accept/deny idiom as the TUNE IN cost confirm (tui.go:4565: [ enter / y ] accepts,
# [ esc / n ] denies, DENY is the default). Nothing about the handoff has happened when
# the plate is up: no scratch config, no budget change, no exec, no spend.
#
# WHAT THE PLATE SHOWS, and where each figure comes from (all REAL sources, never
# fabricated):
#   guest        — the Detection (name + probed version)
#   band         — the live proxy options model (proxyHolder.Get().Model) + the open
#                  channel's station callsign (m.connected)
#   t/s · ctx    — the open channel's station offer (TPS, Ctx/CtxEstimated — the ~ honesty)
#   price        — the station's $/1M in·out + the broker's neutral price tier
#   balance      — m.balance/m.haveBal (the fetched figure; an unknown balance renders an
#                  honest dim "-", never a fabricated $0.00)
#   budget       — the $2.00 default (client.DefaultSessionBudget) + how to raise it
#                  (plate_budget.feature)
#   workdir      — the resolved absolute agentRoot() (plate_workdir.feature)
#   expectation  — the community-model line (P1): the guest runs on the BAND's model; the
#                  guest's brand never implies its vendor's quality. Proposed copy:
#                  "heads up · <guest> runs on <model> here - community band quality,
#                  not <guest>'s house models"
#                  [FOUNDER FLAG P1: approve the exact expectation-line copy.]
#
# Adversarial corners folded in: B4 spend-blowout (the budget line is never hidden, and
# uncapped is loudly marked — plate_budget.feature); B6 the plate cannot be accepted
# remotely (the BASE STATION bridge never confirms a handoff — the y must come from the
# local keyboard, matching the RC money-confirm invariant); P1 brand-quality implication.
# Moderation friction (§8) stays OFF the plate — a denial surfaces as the clean
# OpenAI-shaped error mid-session, not as pre-launch chrome. [FOUNDER FLAG P2: confirm no
# moderation note belongs on the plate.]

Feature: The pre-launch plate
  Before a guest takes the mic the user confirms, on one plate, exactly what
  will run, on what band, at what price, under what ceiling, in which directory.

  Background:
    Given an AGENT session with a tuned band "qwen3-32b-fp8" and a live proxy holder
    And a detected guest "opencode"
    And the open channel's station reports a context window of 131072 tokens

  # ── the plate interposes ─────────────────────────────────────────────────────

  Scenario: Picking a guest opens the plate, not the patch
    When the user runs "/operator"
    And the user picks "opencode"
    Then the pre-launch plate is shown for "opencode"
    And no handoff staging has begun
    And no child process is launched
    And no scratch dir exists for the session

  Scenario: The direct jump goes through the same plate — no bypass
    When the user runs "/operator opencode"
    Then the pre-launch plate is shown for "opencode"
    And no handoff staging has begun

  Scenario: The alias jump goes through the same plate
    When the user runs "/guest opencode"
    Then the pre-launch plate is shown for "opencode"

  Scenario: The DJ never gets a plate
    When the user runs "/operator"
    And the user picks "DJ"
    Then no pre-launch plate is shown
    And the transcript notes the DJ keeps the mic

  Scenario: A needs-setup guest never reaches the plate (Phase 2 regression)
    Given a detected guest "claude" that requires setup
    When the user runs "/operator"
    And the user picks "claude"
    Then no pre-launch plate is shown
    And the transcript shows the setup note for "claude"
    And no child process is launched

  # ── every field, from its real source ────────────────────────────────────────

  Scenario: The plate names the guest and its probed version
    When the user runs "/operator opencode"
    Then the plate shows the guest "opencode"
    And the plate shows the guest version from the detection

  Scenario: The plate names the band and the station
    When the user runs "/operator opencode"
    Then the plate shows the band model "qwen3-32b-fp8"
    And the plate shows the open channel's station callsign

  Scenario: The plate shows t/s, ctx, and price from the open channel's station
    Given the open channel's station serves at 62 t/s for $0.20·$0.30 per 1M
    When the user runs "/operator opencode"
    Then the plate shows "62 t/s"
    And the plate shows the context window "131k"
    And the plate shows the price "$0.20·$0.30 /1M"
    And the plate shows the band's price tier

  Scenario: An estimated context window carries the ~ on the plate
    Given the open channel's station reports an estimated context window of 32768 tokens
    When the user runs "/operator opencode"
    Then the plate renders the context window as an estimate "~32k"

  Scenario: The plate shows the current balance
    Given the fetched balance is "$42.17"
    When the user runs "/operator opencode"
    Then the plate shows the balance "$42.17"

  Scenario: An unknown balance is an honest dash, never a fabricated zero
    Given no balance has been fetched
    When the user runs "/operator opencode"
    Then the plate shows the balance as "-"
    And the plate does not show "$0.00" as the balance

  Scenario: The plate shows the default session budget and how to raise it
    When the user runs "/operator opencode"
    Then the plate shows the session budget "$2.00"
    And the plate shows how to raise the budget

  Scenario: The plate shows the workdir
    When the user runs "/operator opencode"
    Then the plate shows the resolved absolute workdir

  Scenario: The expectation line rides every plate (FOUNDER FLAG P1)
    When the user runs "/operator opencode"
    Then the plate shows the community-model expectation line
    And the expectation line names the band model "qwen3-32b-fp8"

  Scenario: The expectation line never implies the guest vendor's quality (P1 pin)
    Given a detected guest "claude" that is launchable
    When the user runs "/operator claude"
    Then the plate shows the community-model expectation line
    And the expectation line does not attribute the model to "claude"

  Scenario: An unverified guest's version warning rides the plate
    Given a detected guest "opencode" with an unproven version "0.9.0"
    When the user runs "/operator opencode"
    Then the plate warns the guest version is unproven

  # ── the y/N gate (the house confirm idiom) ───────────────────────────────────

  Scenario: y accepts — the patch begins exactly as Phase 2 specified
    When the user runs "/operator opencode"
    And the user presses "y"
    Then the next paint shows "PATCHING YOU THROUGH"
    And only after that paint is the exec command issued

  Scenario: enter accepts, same as y
    When the user runs "/operator opencode"
    And the user presses "enter"
    Then the next paint shows "PATCHING YOU THROUGH"

  Scenario: n cancels back to the picker cleanly
    When the user runs "/operator"
    And the user picks "opencode"
    And the user presses "n"
    Then the operator picker is open
    And the picker cursor is on "opencode"
    And no child process is launched

  Scenario: esc cancels back to the picker cleanly
    When the user runs "/operator"
    And the user picks "opencode"
    And the user presses esc
    Then the operator picker is open
    And no child process is launched

  Scenario: A cancelled plate from a direct jump returns to the ask prompt
    When the user runs "/operator opencode"
    And the user presses "n"
    Then no picker opens
    And the ask prompt still has focus
    And no child process is launched

  Scenario: Cancel leaves no trace — no scratch, no budget change, no spend
    When the user runs "/operator opencode"
    And the user presses "n"
    Then no scratch dir exists for the session
    And the holder budget is unchanged
    And no request hit the local proxy

  Scenario: Deny is the default — stray keys never accept
    When the user runs "/operator opencode"
    And the user presses "x"
    Then the pre-launch plate is shown for "opencode"
    And no handoff staging has begun

  Scenario: The plate is modal — mode keys do not leak underneath
    When the user runs "/operator opencode"
    And the user presses "1"
    Then the TUI did not switch modes

  Scenario: A DJ turn arriving while the plate is up cancels the plate, never a blind exec
    When the user runs "/operator opencode"
    And a remote turn starts a DJ reply while the plate is up
    Then the plate is closed
    And the transcript notes the DJ picked up a turn
    And no child process is launched

  Scenario: The plate cannot be accepted remotely (B6 — the RC money-confirm invariant)
    Given a HOST agent session with an attached remote-control bridge
    And a tuned band and a detected guest "opencode"
    When the user runs "/operator opencode"
    And a viewer sends a confirm frame
    Then the pre-launch plate is shown for "opencode"
    And no handoff staging has begun
    And no child process is launched

  # ── rendering discipline ─────────────────────────────────────────────────────

  Scenario: Compact mode — the plate folds but keeps every money field and the gate
    Given the windowshade compact view is active
    When the user runs "/operator opencode"
    Then the pre-launch plate is shown for "opencode"
    And the plate shows the session budget "$2.00"
    And the plate shows the y/N gate
    And no AGENT view line exceeds the terminal width

  Scenario: Narrow terminal — the plate is width-clamped, the budget line never dropped (B4)
    Given the terminal is 48 columns wide
    When the user runs "/operator opencode"
    Then the plate shows the session budget "$2.00"
    And no AGENT view line exceeds the terminal width

  Scenario: NO_COLOR — the plate is legible and the gate is visible
    Given color is disabled
    When the user runs "/operator opencode"
    Then the plate renders without ANSI color
    And the plate shows the y/N gate
