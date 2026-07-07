# GUEST OPERATORS — Phase 2: hermes config generation (per-session, scratch HERMES_HOME).
#
# THE CONFIG-ISOLATION INVESTIGATION (task-mandated; result: hermes STAYS in the MVP):
#   ~/.hermes/config.yaml is the USER'S REAL FILE and is never written. hermes 0.16.0 reads
#   the `HERMES_HOME` env var as a full home override (hermes_constants.py:56 "Reads
#   HERMES_HOME env var, falls back to the platform-native default" — verified in the
#   installed source on 2026-07-06). Setting HERMES_HOME to a scratch dir isolates
#   config.yaml, sessions, checkpoints — everything.
#
# THE KEY-DELIVERY CORRECTION (supersedes the §4 model_aliases-only route): the doc's
# empirical run predated Phase 1 auth. A bare `model_aliases:` DirectAlias carries NO
# api_key field (model_switch.py:165 NamedTuple: model/provider/base_url) and its runtime
# resolution ends at "no-key-required" for a loopback base_url (runtime_provider.py:919
# — OPENAI_API_KEY is host-gated to api.openai.com and the host-derived <VENDOR>_API_KEY
# path rejects IPs/loopback). Against the Phase 1 bearer-enforcing proxy that is a
# guaranteed 401. The 0.16 path that DOES deliver a key to a custom base_url is the keyed
# `providers.<name>` schema: model_switch.py:900-931 resolves providers.roger.base_url and
# expands api_key "${VAR}" from the environment (:914-916; key_env also supported :918-920).
# So the golden config uses `providers:` + `model:` and the argv pins `-m roger/<model>`.
# GREEN-stage live E2E must confirm `hermes -m roger/<model> -z "ping"` end-to-end (memory:
# live E2E catches what tests miss); if 0.16 refuses, hermes drops from MVP per §6.
#
# FOUNDER DECISIONS REQUESTED:
#   - Confirm hermes stays IN the MVP on the HERMES_HOME isolation evidence above.
#   - A fresh HERMES_HOME may trigger hermes' first-run setup; the launch env is specced
#     with HERMES_INTERACTIVE-safe defaults only if the live E2E shows a wizard block
#     (flagged, not specced — no invented flags).

Feature: hermes per-session config generation
  Handing the mic to hermes materializes a throwaway config.yaml inside a scratch
  HERMES_HOME, so hermes runs on the tuned band through the authenticated proxy and
  the user's real ~/.hermes is never opened for writing.

  Background:
    Given a live proxy session at "http://127.0.0.1:44017/v1" with session key "sk-test-0123" and band model "qwen3-32b-fp8"
    And a session scratch dir

  Scenario: The generated config.yaml is byte-exact (golden artifact)
    When the hermes launch is materialized
    Then the scratch dir contains "hermes-home/config.yaml"
    And "hermes-home/config.yaml" equals the golden artifact:
      """
      providers:
        roger:
          base_url: http://127.0.0.1:44017/v1
          api_key: ${ROGER_SESSION_KEY}
      model:
        provider: roger
        default: qwen3-32b-fp8
      """

  Scenario: The launch env points hermes at the scratch home
    When the hermes launch is materialized
    Then the env additions are exactly:
      | HERMES_HOME       | <scratch>/hermes-home |
      | ROGER_SESSION_KEY | sk-test-0123          |
    And the parent environment is otherwise inherited unmodified

  Scenario: The launch argv pins the roger provider and model
    When the hermes launch is materialized
    Then the argv is exactly "hermes -m roger/qwen3-32b-fp8"

  Scenario: The session key never appears inside any generated file
    # api_key is the literal string "${ROGER_SESSION_KEY}" on disk; hermes expands it
    # from the env at runtime (model_switch.py:914-916).
    When the hermes launch is materialized
    Then no file under the scratch dir contains the string "sk-test-0123"

  Scenario: The user's real ~/.hermes/config.yaml is never opened for writing
    Given the user has a real "~/.hermes/config.yaml"
    When the hermes launch is materialized and the guest exits
    Then "~/.hermes/config.yaml" is byte-identical to before
    And nothing was written under "~/.hermes"

  Scenario: Config generation reads the LIVE proxy options at materialize time
    Given the band is re-tuned to model "llama-3.3-70b" before the handoff
    When the hermes launch is materialized
    Then "hermes-home/config.yaml" wires default model "llama-3.3-70b"
    And the argv pins "-m roger/llama-3.3-70b"

  Scenario: The scratch HERMES_HOME is removed after a clean exit
    When the hermes launch is materialized
    And the guest exits cleanly
    Then the session scratch dir no longer exists

  Scenario: A crashed hermes still gets its scratch home cleaned up
    # hermes writes sessions/checkpoints into HERMES_HOME while running; the sweep
    # removes the whole scratch dir regardless of what the guest left inside it.
    When the hermes launch is materialized
    And the guest crashes with exit 1
    Then the session scratch dir no longer exists

  Scenario: A model_aliases-only config is a specified regression, not a wiring option
    # Permanent regression pin for the key-delivery correction above: materialization
    # must NEVER emit a bare model_aliases entry as the sole wiring (it resolves to
    # api_key "no-key-required" on loopback and 401s against the Phase 1 proxy).
    When the hermes launch is materialized
    Then "hermes-home/config.yaml" contains a "providers:" section with an api_key reference
