# GUEST OPERATORS — Phase 2: agent-CLI detection (THE DESK roster source).
#
# A static REGISTRY of guest operators (design doc §4 empirical table) is the ONE source of
# who can ever appear at the desk. MVP set: opencode, hermes, aider. claude and codex are
# EXCLUDED in v1 (Anthropic /v1/messages + Responses-API wire; naive launch silently falls
# back to the user's REAL Anthropic account — the exact failure §4 measured). Detection is a
# pure function over an injectable Env seam (the internal/audio/audio.go:35 Env pattern:
# LookPath + a bounded version Probe), so every PATH/version permutation is table-testable
# with no real binary and the TUI can deliver results async like onSharesDetected
# (tui.go:2341).
#
# GROUND TRUTH (empirical, dev box 2026-07-06):
#   - `opencode --version`  -> "1.17.11"                                  (bare semver)
#   - `hermes --version`    -> "Hermes Agent v0.16.0 (2026.6.5) · upstream 9688c1a9"
#   - `aider --version`     -> "aider X.Y.Z" (not installed on the dev box; format per docs)
#   - exec.LookPath already rejects a file on PATH without the execute bit (ErrNotFound),
#     so "exists but not executable" is NOT detected — pinned here as a permanent scenario.
#
# Version policy (§8 "version skew"): a failed/garbled/below-floor version probe NEVER hides
# a guest — it marks the detection UNVERIFIED so the picker can degrade gracefully (warn +
# fall back to the /connect snippet) instead of lying that the desk is empty.
#
# Placement (minimization rung): new pure package internal/operator — registry + detection +
# config materialization have zero bubbletea dependencies; internal/tui keeps only the
# command/picker/exec glue. Mirrors the internal/audio precedent.

Feature: Guest operator detection registry
  The desk roster is derived from a static registry of known guest operators,
  detected via an injectable LookPath seam and an optional bounded version probe,
  so the TUI only ever offers a handoff to a CLI that is really on this machine.

  Background:
    Given the guest operator registry

  Scenario: The MVP registry is exactly opencode, hermes, aider — claude and codex excluded
    Then the registry lists exactly "opencode", "hermes", "aider" in that order
    And no registry entry is named "claude" or "codex"
    And every entry carries a name, a PATH binary, a provider tag, an install hint, and a known-good version

  Scenario: Registry entries carry the empirically-proven wiring strategy
    Then the "opencode" entry uses the scratch-config strategy with known-good version "1.17.11"
    And the "hermes" entry uses the scratch-home strategy with known-good version "0.16.0"
    And the "aider" entry uses the env-and-flags strategy with no config file at all

  Scenario: All three guests on PATH are detected in registry order
    Given LookPath resolves "opencode" to "/home/u/.opencode/bin/opencode"
    And LookPath resolves "hermes" to "/home/u/.local/bin/hermes"
    And LookPath resolves "aider" to "/home/u/.local/bin/aider"
    When the desk is scanned
    Then the detections are "opencode", "hermes", "aider" in that order
    And each detection records the resolved path

  Scenario: A guest missing from PATH is simply absent — never an error
    Given LookPath resolves "opencode" to "/home/u/.opencode/bin/opencode"
    And LookPath fails for "hermes" with "executable file not found in $PATH"
    And LookPath fails for "aider" with "executable file not found in $PATH"
    When the desk is scanned
    Then the detections are exactly "opencode"

  Scenario: An empty PATH detects nothing and the scan still succeeds
    Given LookPath fails for every binary
    When the desk is scanned
    Then the detections are empty
    And no error is surfaced

  Scenario: A file on PATH without the execute bit is NOT at the desk
    # exec.LookPath excludes non-executable files; the seam preserves that contract.
    Given LookPath fails for "opencode" with "permission denied"
    When the desk is scanned
    Then the detections do not include "opencode"

  Scenario: The version probe pins the proven versions
    Given LookPath resolves every registry binary
    And the version probe answers "opencode" with "1.17.11"
    And the version probe answers "hermes" with "Hermes Agent v0.16.0 (2026.6.5) · upstream 9688c1a9"
    When the desk is scanned
    Then the "opencode" detection has version "1.17.11" and is verified
    And the "hermes" detection has version "0.16.0" and is verified

  Scenario Outline: Version strings parse across the real formats
    When the raw version output "<raw>" for "<guest>" is parsed
    Then the parsed version is "<version>"

    Examples:
      | guest    | raw                                                    | version |
      | opencode | 1.17.11                                                | 1.17.11 |
      | opencode | 1.17.11\n                                              | 1.17.11 |
      | hermes   | Hermes Agent v0.16.0 (2026.6.5) · upstream 9688c1a9    | 0.16.0  |
      | aider    | aider 0.86.1                                           | 0.86.1  |

  Scenario: A failed version probe degrades to UNVERIFIED, never to hidden
    Given LookPath resolves "opencode" to "/home/u/.opencode/bin/opencode"
    And the version probe fails for "opencode" with "exit status 1"
    When the desk is scanned
    Then the detections include "opencode"
    And the "opencode" detection is unverified with an empty version

  Scenario: Garbage version output degrades to UNVERIFIED, never to hidden
    Given LookPath resolves "hermes" to "/home/u/.local/bin/hermes"
    And the version probe answers "hermes" with "Traceback (most recent call last): ..."
    When the desk is scanned
    Then the detections include "hermes"
    And the "hermes" detection is unverified

  Scenario: A version below the known-good floor is detected but flagged unverified
    # §8 version skew: degrade gracefully to the /connect snippet, don't refuse outright.
    Given LookPath resolves "opencode" to "/usr/bin/opencode"
    And the version probe answers "opencode" with "0.9.0"
    When the desk is scanned
    Then the "opencode" detection is unverified with version "0.9.0"

  Scenario: A wedged version probe cannot hang the scan
    # The Probe seam is BOUNDED (the audio.PlayTimeout discipline): a hung `--version`
    # returns an error at the deadline and the guest degrades to unverified.
    Given LookPath resolves "hermes" to "/home/u/.local/bin/hermes"
    And the version probe for "hermes" blocks past its deadline
    When the desk is scanned
    Then the scan completes
    And the "hermes" detection is unverified

  Scenario: Re-scan reflects a changed PATH — detection holds no cached state
    Given LookPath fails for every binary
    When the desk is scanned
    Then the detections are empty
    When LookPath resolves "aider" to "/home/u/.local/bin/aider"
    And the desk is scanned again
    Then the detections are exactly "aider"

  Scenario: Detection never launches, writes, or bills anything
    # Scanning the desk is read-only: LookPath + `--version` only. No config is generated,
    # no scratch dir is created, no proxy call is made, no budget is touched.
    Given LookPath resolves every registry binary
    When the desk is scanned
    Then no file was written anywhere
    And no request hit the local proxy
