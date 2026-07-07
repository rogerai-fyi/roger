# GUEST OPERATORS — Phase 3: the agent-ready band gate (design doc §6 + §8).
#
# WHY THIS GATE EXISTS (the adversarial pin): a coding agent handed a 4B/8k band fails on
# its FIRST prompt — context overflow, garbled tool calls — and the user reads it as
# "RogerAI is broken", not "this band is small". The gate turns that first-prompt failure
# into an honest refusal BEFORE any spend.
#
# THE SIGNAL INVESTIGATION (grounded in the merged code, 2026-07-06):
#   · Context window: EXISTS end-to-end. protocol.ModelOffer.Ctx + CtxEstimated
#     (protocol.go:43-48) → /discover offers → tui offer.Ctx/CtxEstimated (tui.go:431) →
#     bandCtx (tui.go:6282). The OPEN CHANNEL's locked station is m.connected (tui.go:664),
#     and the live proxy options carry the model — the gate reads the CHANNEL's station,
#     because that is the station the guest will actually be patched into.
#   · Tool-call support: DOES NOT EXIST TODAY. offer Capabilities is a CLOSED, node-declared
#     set whose only member is "vision" (protocol.go:105-109, the 74b109f labeling); the
#     broker's probe system (cmd/rogerai-broker/probe.go) is a canary/latency/tps probe with
#     NO tool-call canary; the TUI offer struct carries no capabilities at all.
#
# THE RULE THIS SPEC PINS (per §6, adapted to the real signals):
#   HARD GATE  — the open channel's station context window, when KNOWN (detected or
#                estimated), must be >= 16384 tokens. Below the floor: the handoff is
#                refused with the honest reason; the picker row is disabled with the same
#                reason. Never fail on prompt one.
#   WARN, NOT BLOCK — tool-call support is UNKNOWN on every band today: the pre-launch
#                plate carries a warn line, the handoff proceeds.
#                [FOUNDER FLAG G1: warn-vs-block on the missing tool-call signal, and
#                whether to fund the probe gap — a "tools" capability label + a broker
#                tool-call canary — as a follow-up.]
#   WARN, NOT BLOCK — an UNKNOWN context window (the station reports none, ctx 0) warns on
#                the plate instead of blocking: real /discover feeds carry offers without
#                ctx, and blocking on missing metadata would gate healthy 70B bands off
#                the desk. [FOUNDER FLAG G2: confirm unknown-ctx = warn, not block.]
#   THE DJ IS NEVER GATED — the resident runs in the TUI on any band, as today.
#
# The gate is LIVE (the Phase 1 live-options discipline): a re-tune flips it immediately,
# and the staging-beat recheck (operator.go:368 pattern) re-applies it at exec time.

Feature: The agent-ready band gate
  A guest handoff is offered only when the open channel can actually carry an
  agent workload, and a refusal always names the real limitation.

  Background:
    Given an AGENT session with a tuned band "qwen3-32b-fp8" and a live proxy holder
    And a detected guest "opencode"

  # ── the floor ────────────────────────────────────────────────────────────────

  Scenario: A band that clears the floor hands off as before
    Given the open channel's station reports a context window of 131072 tokens
    When the user runs "/operator opencode"
    Then the pre-launch plate is shown for "opencode"
    And the plate carries no context warning

  Scenario: Boundary — exactly 16384 tokens clears the floor
    Given the open channel's station reports a context window of 16384 tokens
    When the user runs "/operator opencode"
    Then the pre-launch plate is shown for "opencode"

  Scenario: Boundary — 16383 tokens is under the floor
    Given the open channel's station reports a context window of 16383 tokens
    When the user runs "/operator opencode"
    Then the handoff is refused before any plate or staging
    And no child process is launched

  Scenario: Boundary honesty — a 16383-token window is named exactly, never as "16k" (review regression 2026-07-07)
    # Truncating 16383 to "16k" would make the refusal read "the window is 16k, a coding
    # agent needs 16k+" - naming the floor as both too small and the requirement. The
    # 16000-16383 corner is named in exact tokens instead.
    Given the open channel's station reports a context window of 16383 tokens
    When the user runs "/operator opencode"
    Then the transcript notes "this band is too small for a guest"
    And the refusal names the window "16383 tokens" and the floor "16k"
    And the transcript does not show "the window is 16k,"
    And no child process is launched

  Scenario: The direct-jump refusal names the band, its window, and the floor
    Given the open channel's station reports a context window of 8192 tokens
    When the user runs "/operator opencode"
    Then the transcript notes "this band is too small for a guest"
    And the refusal names the window "8k" and the floor "16k"
    And no child process is launched

  Scenario: The refusal is a local note, never a chat turn
    Given the open channel's station reports a context window of 8192 tokens
    When the user runs "/operator opencode"
    Then no chat turn is submitted
    And no request hit the local proxy

  Scenario: The adversarial pin — the refusal blames the band, never the radio
    Given the open channel's station reports a context window of 8192 tokens
    When the user runs "/operator opencode"
    Then the refusal points at re-tuning to a larger band
    And the transcript does not show "error"

  Scenario: Every alias is gated the same
    Given the open channel's station reports a context window of 8192 tokens
    When the user runs "/mic opencode"
    Then the handoff is refused before any plate or staging
    And no child process is launched

  # ── the picker under the floor ───────────────────────────────────────────────

  Scenario: Under the floor, guest picker rows are disabled with the honest reason
    Given the open channel's station reports a context window of 8192 tokens
    When the user runs "/operator"
    Then the operator picker is open
    And the picker row for "opencode" is disabled
    And the disabled row carries the reason "needs a 16k+ band"

  Scenario: Enter on a disabled row prints the reason, never a plate
    Given the open channel's station reports a context window of 8192 tokens
    When the user runs "/operator"
    And the user picks "opencode"
    Then the transcript notes "this band is too small for a guest"
    And no pre-launch plate is shown
    And no child process is launched

  Scenario: The DJ row is never gated
    Given the open channel's station reports a context window of 8192 tokens
    When the user runs "/operator"
    And the user picks "DJ"
    Then the picker closes
    And the transcript notes the DJ keeps the mic

  # ── estimated and unknown windows ────────────────────────────────────────────

  Scenario: An estimated window above the floor clears, with the ~ honesty on the plate
    Given the open channel's station reports an estimated context window of 32768 tokens
    When the user runs "/operator opencode"
    Then the pre-launch plate is shown for "opencode"
    And the plate renders the context window as an estimate "~32k"

  Scenario: An estimated window under the floor is refused with the ~ honesty
    Given the open channel's station reports an estimated context window of 8192 tokens
    When the user runs "/operator opencode"
    Then the handoff is refused before any plate or staging
    And the refusal renders the window as an estimate "~8k"

  Scenario: An unknown window warns on the plate instead of blocking (FOUNDER FLAG G2)
    Given the open channel's station reports no context window
    When the user runs "/operator opencode"
    Then the pre-launch plate is shown for "opencode"
    And the plate warns "context window unknown on this band"

  Scenario: Tool-call support is unknown on every band today — warn, never block (FOUNDER FLAG G1)
    Given the open channel's station reports a context window of 131072 tokens
    When the user runs "/operator opencode"
    Then the pre-launch plate is shown for "opencode"
    And the plate warns "tool-call support unproven on this band"

  # ── the gate reads the CHANNEL, and it reads it LIVE ─────────────────────────

  Scenario: The gate reads the open channel's station, not the band's best station
    Given the band has another station with a context window of 131072 tokens
    And the open channel's station reports a context window of 8192 tokens
    When the user runs "/operator opencode"
    Then the handoff is refused before any plate or staging

  Scenario: A re-tune to a larger band flips the gate open, live
    Given the open channel's station reports a context window of 8192 tokens
    And the handoff was refused for the small window
    When the user re-tunes the channel to a station with a context window of 131072 tokens
    And the user runs "/operator opencode"
    Then the pre-launch plate is shown for "opencode"

  Scenario: A re-tune to a smaller band flips the gate shut, live
    Given the open channel's station reports a context window of 131072 tokens
    When the user re-tunes the channel to a station with a context window of 8192 tokens
    And the user runs "/operator opencode"
    Then the handoff is refused before any plate or staging

  Scenario: The staging-beat recheck re-applies the gate at exec time
    Given the open channel's station reports a context window of 131072 tokens
    And the plate for "opencode" was accepted
    When the channel is re-tuned to a station with a context window of 8192 tokens during the staging beat
    Then the exec is aborted
    And the transcript notes the band changed under the patch
    And no child process is launched

  # ── unchanged Phase 2 refusals (regressions) ─────────────────────────────────

  Scenario: No band tuned still points at tuning in first
    Given the proxy holder is disconnected
    When the user runs "/operator opencode"
    Then the transcript points at tuning in first
    And no child process is launched
