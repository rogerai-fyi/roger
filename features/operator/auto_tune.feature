# AGENT [0] DESK ENTRY REDESIGN — the silent auto-tune (design R1/R6).
#
# When the AGENT lands with nothing tuned in, a SILENT auto-tune finds a band for the DJ.
# pickAutoBand is PURE + deterministic and runAutoTune folds the outcome into the model:
#
#   R1 (NEVER auto-spend): only a FREE band is ever silently connected ($0, no confirm). A
#     PAID band is offered ONLY when logged in, and even then the model lands on the honest
#     "no free band on air - [1] picks a paid band" state instead of spending. A logged-out
#     user with no free band lands on an honest empty state, never a named paid band.
#   R6 (agent-ready first): a coding handoff must not dead-end, so agent-ready bands (window
#     unknown or >=16k) sort before KNOWN-small ones; within a partition FREE precedes paid,
#     free sorts by signal desc (the iOS order), paid by cheapest out-price.
#
# There is NO retry loop: a single empty scan lands on the honest empty state (the founder's
# "spams no station on air" regression). Grounding: pickAutoBand / runAutoTune (tui.go).

Feature: The AGENT [0] silent auto-tune (pickAutoBand + runAutoTune)
  The desk finds a FREE band for the DJ with no spend and no confirm, prefers windows a
  coding agent can use, and is honest - never a paid auto-spend, never a retry storm.

  # ── pickAutoBand ordering (the pure function) ────────────────────────────────

  Scenario: Free by signal — the strongest free band wins
    Given the market has a free band "free-hi" at signal 90
    And the market has a free band "free-lo" at signal 40
    Then pickAutoBand logged in picks "free-hi"

  Scenario: Logged out is free-only — a paid-only market picks nothing
    Given the market has a paid band "paid" out 0.30
    Then pickAutoBand logged out picks nothing

  Scenario: Logged in falls back to the cheapest paid band
    Given the market has a paid band "cheap" out 0.20
    And the market has a paid band "dear" out 0.90
    Then pickAutoBand logged in picks "cheap"

  Scenario: Agent-ready first — a 16k+ free band beats a stronger known-small free band
    Given the market has a free band "small-strong" at signal 99 window 8192
    And the market has a free band "big-weak" at signal 30 window 32768
    Then pickAutoBand logged in picks "big-weak"

  Scenario: An unknown window is treated as agent-ready (never dead-ended)
    Given the market has a free band "unknown" at signal 50 window 0
    And the market has a free band "small" at signal 80 window 8192
    Then pickAutoBand logged in picks "unknown"

  Scenario: Free beats paid regardless of signal
    Given the market has a free band "free-weak" at signal 10
    And the market has a paid band "paid-any" out 0.10
    Then pickAutoBand logged in picks "free-weak"

  Scenario: An empty market picks nothing
    Given the market is empty
    Then pickAutoBand logged in picks nothing

  # ── runAutoTune outcomes (the model folds the decision) ──────────────────────

  Scenario: A free band is connected silently, at $0, no confirm
    Given a fresh AGENT session with a free band "gpt-oss-20b" on air
    When the desk auto-tunes
    Then the agent runs on "gpt-oss-20b"
    And a channel is open
    And no cost confirm is shown

  Scenario: Logged in with paid-only lands on the honest paid state, never a spend
    Given a fresh AGENT session logged in with only a paid band on air
    When the desk auto-tunes
    Then the transcript shows "no free band on air"
    And no channel is open

  Scenario: Logged out with paid-only lands on an honest empty state, never a paid band
    Given a fresh AGENT session logged out with only a paid band on air
    When the desk auto-tunes
    Then the transcript shows "no free band on air"
    And the transcript points at logging in
    And no channel is open

  Scenario: An empty market lands on the honest empty state (no retry loop)
    Given a fresh AGENT session with an empty market
    When the desk auto-tunes
    Then the transcript shows "no station on air right now"
    And no channel is open

  Scenario: A parked prompt is sent once a free band lands
    Given a fresh AGENT session with a free band "gpt-oss-20b" on air
    When the user submits the prompt "hello"
    And the desk auto-tunes
    Then the agent runs on "gpt-oss-20b"

  # ── sticky + no-op ───────────────────────────────────────────────────────────

  Scenario: Sticky — a last-tuned band still on air is reused, not re-picked
    Given an AGENT session whose last band "llama-3.3-70b-instruct" is still on air
    Then the agent runs on "llama-3.3-70b-instruct"
    And the auto-tune did not arm
