# GUEST OPERATORS — Phase 2: agent-CLI detection (THE DESK roster source).
#
# A static REGISTRY of guest operators (design doc §4 empirical table) is the ONE source of
# who can ever appear at the desk: opencode, hermes, aider (wired to the band), plus
# claude and codex (CONTEXT-ONLY). The context-only guests deliberately use their
# existing vendor accounts; RogerAI injects no credentials, endpoint, or model. Detection is a
# pure function over an injectable Env seam (the internal/audio/audio.go:35 Env pattern:
# LookPath + a bounded version Probe), so every PATH/version permutation is table-testable
# with no real binary and the TUI can deliver results async like onSharesDetected
# (tui.go:2341).
#
# GROUND TRUTH (empirical, dev box 2026-07-06):
#   - `opencode --version`  -> "1.17.11"                                  (bare semver)
#   - `hermes --version`    -> "Hermes Agent v0.16.0 (2026.6.5) · upstream 9688c1a9"
#   - `aider --version`     -> "aider X.Y.Z" (not installed on the dev box; format per docs)
#   - `codex --version`     -> "codex-cli 0.146.0" from ~/.npm-global/bin/codex
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

  # ORDER IS A RULE, not a list: FULL guests first (they take a RogerAI band through
  # their own config), context-only last (they run on the operator's vendor account and
  # get the brief but no credentials). dsh joined the full half on 2026-08-21; pi on
  # 2026-08-23, after the founder asked why an installed pi was not at the desk - it had
  # never been in the registry, which is the ONE source of who can take the mic.
  Scenario: The registry includes both context-only coding guests
    Then the registry lists exactly "opencode", "hermes", "aider", "dsh", "pi", "claude", "codex" in that order
    And every entry carries a name, a PATH binary, a provider tag, an install hint, and a known-good version

  Scenario: Registry entries carry the empirically-proven wiring strategy
    Then the "opencode" entry uses the scratch-config strategy with known-good version "1.17.11"
    And the "hermes" entry uses the scratch-home strategy with known-good version "0.16.0"
    And the "aider" entry uses the env-and-flags strategy with no config file at all
    And the "pi" entry uses the scratch-config strategy with known-good version "0.84.2"
    And the "codex" entry uses the context-only strategy with known-good version "0.1.0"
    # dsh shares the scratch-config CONSTANT but has no recipe of its own, which is how it
    # spent weeks launching with opencode's wiring. It is gated until one is written.
    And the "dsh" entry is gated behind operator setup

  Scenario: The three band-wired guests on PATH are detected in registry order
    Given LookPath resolves "opencode" to "/home/u/.opencode/bin/opencode"
    And LookPath resolves "hermes" to "/home/u/.local/bin/hermes"
    And LookPath resolves "aider" to "/home/u/.local/bin/aider"
    When the desk is scanned
    Then the detections are "opencode", "hermes", "aider" in that order
    And each detection records the resolved path

  Scenario: Codex is detected from the user's common npm-global install
    Given "codex" is executable at "/home/u/.npm-global/bin/codex"
    And the inherited GUI PATH does not include "/home/u/.npm-global/bin"
    When the default desk environment resolves "codex"
    Then it returns "/home/u/.npm-global/bin/codex"
    # A GUI-launched Roger process can inherit a smaller PATH than an interactive login
    # shell. This is the founder's real install and must not make Codex silently disappear.

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
      | codex    | codex-cli 0.146.0                                      | 0.146.0 |

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
