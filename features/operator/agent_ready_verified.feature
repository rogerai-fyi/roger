# AGENT-READY, VERIFIED — the consumer half of the tool-call capability probe. Once the
# broker emits a VERIFIED "tools" capability (features/trust/toolcall_probe.feature), the
# AGENT view stops INFERRING agent-readiness from the context window alone and starts
# reporting the real, probed signal.
#
# WHERE THIS SITS (grounded in the merged code, origin/main 76d4a24):
#   · band_gate.feature already pins the CTX floor: the open channel's station window, when
#     KNOWN, must be >= operatorCtxFloor (16384; operator.go:60). Below it, the handoff is
#     REFUSED ("✕ this band is too small for a guest"). That gate is UNCHANGED here.
#   · Today the pre-launch plate ALWAYS warns "tool-call support unproven on this band - the
#     guest may fall back to plain text" (operator.go:952), because no tool signal exists.
#     band_gate.feature calls tool-call support "UNKNOWN on every band today".
#   · The AGENT redesign (being built concurrently) renders an agent-ready marker. Grounded on
#     the ctx floor alone it is INFERRED, drawn with a "~" (⌁~) and the plate's "unproven" warn.
#     This spec makes it VERIFIED (⌁, no tilde) once "tools" is emitted for the band's model,
#     and DROPS/UPGRADES the plate warn for that band.
#
# THE THREE-STATE HONESTY this pins (the truth-in-labeling house rule, like CtxEstimated's ~
# and TokenizerExact): the AGENT view reports exactly one of:
#   VERIFIED agent-ready (⌁)   — the open channel's station model carries the probed "tools"
#                                capability AND ctx >= floor. No tilde, no "unproven" warn.
#   INFERRED agent-ready (⌁~)  — ctx >= floor but "tools" is absent (unprobed/undetermined).
#                                The tilde stays; the plate keeps the "unproven" warn.
#   TOO SMALL (✕)              — ctx KNOWN and < floor: the existing refusal, regardless of tools.
#   ABSENT                     — ctx unknown (0): the existing unknown-window warn; tools absence
#                                still claims nothing (never a positive "no tools").
#
# The marker reads the OPEN CHANNEL's station (m.connected), the station the guest is actually
# patched into — the same source band_gate reads — NOT the band's best station.
#
# FOUNDER FLAG A1: confirm the exact upgraded plate copy for a VERIFIED band (e.g. drop the warn
# entirely, or restate it as "tool-call support verified on this band ✓"). Proposed: DROP the
# unproven warn and show nothing (silence = verified), matching the desk's "no data" honesty.

Feature: The AGENT view reports agent-readiness as VERIFIED only when the band's model has the probed tool-call capability
  Agent-ready is inferred from the context window until the broker verifies tool-calling;
  once "tools" is emitted for the open channel's model, the reading upgrades to verified
  and the pre-launch plate drops its "unproven" warning. The ctx floor still gates handoff.

  Background:
    Given an AGENT session with a tuned band "qwen3-32b-fp8" and a live proxy holder
    And a detected guest "opencode"

  # --- state 1: VERIFIED (⌁) — ctx >= floor AND probed tools ----------------------------

  Scenario: A band whose model carries verified "tools" reads VERIFIED agent-ready
    Given the open channel's station reports a context window of 131072 tokens
    And the open channel's station model carries the verified "tools" capability
    Then the AGENT view reads the band as VERIFIED agent-ready
    And the verified marker carries no "~" estimate tilde

  Scenario: The pre-launch plate drops the "unproven" tool-call warning on a verified band (FOUNDER FLAG A1)
    Given the open channel's station reports a context window of 131072 tokens
    And the open channel's station model carries the verified "tools" capability
    When the user runs "/operator opencode"
    Then the pre-launch plate does NOT warn that tool-call support is unproven
    And the handoff proceeds through the plate as normal

  # --- state 2: INFERRED (⌁~) — ctx >= floor, tools absent -------------------------------

  Scenario: A large-window band with NO tools signal reads INFERRED agent-ready
    Given the open channel's station reports a context window of 131072 tokens
    And the open channel's station model has no "tools" capability
    Then the AGENT view reads the band as INFERRED agent-ready
    And the inferred marker carries the "~" unproven tilde

  Scenario: The plate KEEPS the "unproven" warning on an unprobed band
    Given the open channel's station reports a context window of 131072 tokens
    And the open channel's station model has no "tools" capability
    When the user runs "/operator opencode"
    Then the pre-launch plate warns that tool-call support is unproven

  # --- state 3: TOO SMALL (✕) — ctx known and under floor -------------------------------

  Scenario: A sub-floor band reads TOO SMALL regardless of tools, and the handoff is still refused
    Given the open channel's station reports a context window of 8192 tokens
    Then the AGENT view reads the band as too small for a guest
    When the user runs "/operator opencode"
    Then the handoff is refused because the band is too small
    And no pre-launch plate is shown

  Scenario: A verified-tools signal never overrides the ctx floor
    Given the open channel's station reports a context window of 8192 tokens
    And the open channel's station model carries the verified "tools" capability
    Then the AGENT view reads the band as too small for a guest
    And a verified tool-call capability does not lift the too-small refusal

  # --- state 4: ABSENT — unknown window, tools claims nothing ----------------------------

  Scenario: An unknown-window band warns, and absent tools still claims nothing
    Given the open channel's station reports no context window
    When the user runs "/operator opencode"
    Then the pre-launch plate warns the context window is unknown
    And the absent tool-call capability is read as undetermined, never as "no tools"

  # --- liveness: the reading tracks the open channel, and re-tunes immediately -----------

  Scenario: Re-tuning to a verified band upgrades the reading from inferred to verified
    Given the open channel's station reports a context window of 131072 tokens
    And the open channel's station model has no "tools" capability
    Then the AGENT view reads the band as INFERRED agent-ready
    When the consumer re-tunes to a station whose model carries verified "tools"
    Then the AGENT view reads the band as VERIFIED agent-ready

  Scenario: Losing the verified signal downgrades verified back to inferred, never to a false claim
    Given the open channel's station model carries the verified "tools" capability
    And the AGENT view reads the band as VERIFIED agent-ready
    When the broker drops "tools" for that model after a regression
    Then the AGENT view reads the band as INFERRED agent-ready
    And it never keeps claiming verified once the probe evidence is gone
