# GUEST OPERATORS — Phase 2: aider wiring (pure env + flags, ZERO generated files).
#
# aider is the one MVP guest whose wiring is honestly env-based (§4): OPENAI_API_BASE +
# OPENAI_API_KEY plus `--model openai/<m>`. Minimization rung: since no file is generated,
# NO scratch dir is created for aider at all — the cleanup path is a no-op by construction
# and there is nothing to leak on crash.
#
# The flag set is pinned from §4 verbatim:
#   --no-show-model-warnings  (an unknown model name otherwise produces a scary wall)
#   --no-auto-commits         (a guest must NEVER commit to the user's repo on its own —
#                              this is a safety flag, not a convenience; it stays even if
#                              some future aider makes it the default)
#
# NOTE for the founder: aider is NOT installed on the dev box, so unlike opencode/hermes
# its wiring is doc-proven (§4) but not re-verified today. The GREEN-stage live E2E needs
# an aider install; no known-good version is pinned in the registry yet — ruling requested.

Feature: aider per-session wiring
  Handing the mic to aider composes env and flags only: proxy base URL and session
  key in the environment, model and safety flags on the argv, no file anywhere.

  Background:
    Given a live proxy session at "http://127.0.0.1:44017/v1" with session key "sk-test-0123" and band model "qwen3-32b-fp8"

  Scenario: The launch argv is exact and carries the safety flags
    When the aider launch is materialized
    Then the argv is exactly "aider --model openai/qwen3-32b-fp8 --no-show-model-warnings --no-auto-commits"

  Scenario: The launch env composes the OpenAI-compatible pair from the live options
    When the aider launch is materialized
    Then the env additions are exactly:
      | OPENAI_API_BASE | http://127.0.0.1:44017/v1 |
      | OPENAI_API_KEY  | sk-test-0123              |
    And the parent environment is otherwise inherited unmodified

  Scenario: No file and no scratch dir are created for aider
    When the aider launch is materialized
    Then no scratch dir exists for the session
    And no file was written anywhere

  Scenario: --no-auto-commits is present on every composed argv (permanent safety pin)
    When the aider launch is materialized for any band model
    Then the argv contains "--no-auto-commits"

  Scenario: Wiring reads the LIVE proxy options at materialize time
    Given the band is re-tuned to model "llama-3.3-70b" before the handoff
    When the aider launch is materialized
    Then the argv pins "--model openai/llama-3.3-70b"

  Scenario: A stale OPENAI_API_KEY in the parent environment is overridden, not merged
    # The user may already export a real OpenAI key. The child must get OUR session key,
    # or aider bills the user's real OpenAI account instead of the tuned band.
    Given the parent environment already sets OPENAI_API_KEY to "sk-users-real-openai-key"
    When the aider launch is materialized
    Then the child env sets OPENAI_API_KEY to "sk-test-0123"
    And the child env sets OPENAI_API_BASE to "http://127.0.0.1:44017/v1"
