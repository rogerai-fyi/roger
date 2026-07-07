# GUEST OPERATORS — Phase 2: opencode config generation (per-session, scratch-scoped).
#
# GROUND TRUTH (verified against the installed opencode 1.17.11 binary on 2026-07-06 by
# inspecting its bundled config loader — not guessed from docs):
#   - `OPENCODE_CONFIG=/path/to/file.json` IS honored: "load an additional explicit config",
#     loaded AFTER the global ~/.config/opencode config and BEFORE project opencode.json
#     files found upward from cwd. OPENCODE_CONFIG_CONTENT (inline JSON env) also exists and
#     loads AFTER project configs.
#   - PRECEDENCE HAZARD: because project config loads after OPENCODE_CONFIG, a user project
#     carrying its own opencode.json { "model": ... } would silently override a model set
#     only in our scratch config — the exact "guest silently runs on the wrong backend"
#     failure class that got claude excluded. The launch argv therefore ALWAYS pins the
#     model too: `opencode -m roger/<model>` (global -m flag, verified in 1.17.11 --help),
#     which beats every config layer. The wiring is provable from argv+env alone (the
#     hermes env-ignored lesson: never trust a config file the CLI might not read).
#   - Config supports `{env:VAR}` substitution (verified in the binary), so the session
#     bearer key is passed by ENV REFERENCE and NEVER written to disk.
#
# The generated provider entry is the §4 empirically-proven shape: a custom provider
# "roger" on @ai-sdk/openai-compatible with baseURL + apiKey + the tuned model.
#
# FOUNDER DECISION REQUESTED (this spec proposes option 1):
#   1. scratch file + OPENCODE_CONFIG + argv -m pin   (debuggable artifact, shown on the
#      Phase 3 plate; recommended)
#   2. OPENCODE_CONFIG_CONTENT inline env             (zero files, highest file precedence,
#      but no on-disk artifact to show and the whole config lands in /proc/<pid>/environ)

Feature: opencode per-session config generation
  Handing the mic to opencode materializes a throwaway opencode.json in the session
  scratch dir, points opencode at it via OPENCODE_CONFIG, and pins the model on the
  argv, so the guest provably runs on the tuned band and the user's own opencode
  setup is never touched.

  Background:
    Given a live proxy session at "http://127.0.0.1:44017/v1" with session key "sk-test-0123" and band model "qwen3-32b-fp8"
    And a session scratch dir

  Scenario: The generated opencode.json is byte-exact (golden artifact)
    When the opencode launch is materialized
    Then the scratch dir contains exactly one file "opencode.json"
    And "opencode.json" equals the golden artifact:
      """
      {
        "$schema": "https://opencode.ai/config.json",
        "provider": {
          "roger": {
            "npm": "@ai-sdk/openai-compatible",
            "name": "RogerAI",
            "options": {
              "baseURL": "http://127.0.0.1:44017/v1",
              "apiKey": "{env:ROGER_SESSION_KEY}"
            },
            "models": {
              "qwen3-32b-fp8": { "name": "qwen3-32b-fp8" }
            }
          }
        },
        "model": "roger/qwen3-32b-fp8"
      }
      """

  Scenario: The launch argv pins the model so no config layer can override it
    When the opencode launch is materialized
    Then the argv is exactly "opencode -m roger/qwen3-32b-fp8"

  Scenario: The launch env composes OPENCODE_CONFIG and the key by reference
    When the opencode launch is materialized
    Then the env additions are exactly:
      | OPENCODE_CONFIG   | <scratch>/opencode.json |
      | ROGER_SESSION_KEY | sk-test-0123            |
    And the parent environment is otherwise inherited unmodified

  Scenario: The session key never appears inside any generated file
    When the opencode launch is materialized
    Then no file under the scratch dir contains the string "sk-test-0123"

  Scenario: A user project already carrying opencode.json cannot re-route the guest
    # Regression guard for the precedence hazard: project config loads AFTER
    # OPENCODE_CONFIG in 1.17.11, so only the argv -m pin makes the wiring provable.
    Given the launch workdir contains a project "opencode.json" selecting model "anthropic/claude-opus-4"
    When the opencode launch is materialized
    Then the argv still pins "-m roger/qwen3-32b-fp8"
    And the project "opencode.json" is not modified

  Scenario: Config generation reads the LIVE proxy options at materialize time
    # ProxyOptionsHolder.Get() at exec time — never the options frozen at first bind
    # (features/proxy/live_options.feature is the Phase 1 ground for this).
    Given the band is re-tuned to model "llama-3.3-70b" before the handoff
    When the opencode launch is materialized
    Then "opencode.json" wires model "llama-3.3-70b"
    And the argv pins "-m roger/llama-3.3-70b"

  Scenario: The generated config is removed after a clean exit
    When the opencode launch is materialized
    And the guest exits cleanly
    Then the session scratch dir no longer exists

  Scenario: A crashed guest still gets its scratch config cleaned up
    When the opencode launch is materialized
    And the guest crashes with exit 1
    Then the session scratch dir no longer exists

  Scenario: The user's real opencode config is never touched
    Given the user has a real "~/.config/opencode/opencode.json"
    When the opencode launch is materialized and the guest exits
    Then "~/.config/opencode/opencode.json" is byte-identical to before
    And nothing was written under "~/.config/opencode"
