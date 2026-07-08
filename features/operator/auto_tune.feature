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

  # A CONNECTABLE ($0 free) band must win even when it is known-small and the only
  # agent-ready band is PAID (never silently connectable, R1) - else auto-tune would
  # report "no free band on air" and drop into a dead ask box while a free band is up.
  Scenario: A known-small FREE band still beats an agent-ready PAID band
    Given the market has a free band "free-small" at signal 50 window 8192
    And the market has a paid band "paid-big" out 0.20
    Then pickAutoBand logged in picks "free-small"

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

  # Audit finding [MAJOR - R1 money-safety]: pick.cheapest is the min-PRICE station across
  # ALL stations of a band, but pick.free is a BAND-level flag (ANY station FreeNow). A band
  # mixing a FreeNow-promo station (nonzero nominal price) with a CHEAPER paid station is
  # flagged free, yet cheapest points at the PAID station - so binding cheapest would SILENTLY
  # spend on a paid station labelled "(free)". Auto-tune must bind the genuinely-free station
  # (FreeNow, or zero-priced), NEVER cheapest-across-all. R1: only a $0 station is silent-bound.
  Scenario: A band mixing a free station and a cheaper paid station tunes to the FREE one, never spends
    Given a fresh AGENT session with a band mixing a free station and a cheaper paid station
    When the desk auto-tunes
    Then the bound station is genuinely free
    And a channel is open
    And no cost confirm is shown

  # Defensive: a free-FLAGGED band that carries no genuinely-free station (a stale/mixed
  # signal, or only paid stations) must bind NOTHING and land on the honest paid state - never
  # a silent spend on the strength of the stale flag alone.
  Scenario: A free-flagged band with no actually-free station binds nothing and shows the honest paid state
    Given a fresh AGENT session with a free-flagged band whose stations are all paid
    When the desk auto-tunes
    Then no channel is open
    And the transcript shows "no free band on air"

  Scenario: A parked prompt is sent once a free band lands
    Given a fresh AGENT session with a free band "gpt-oss-20b" on air
    When the user submits the prompt "hello"
    And the desk auto-tunes
    Then the agent runs on "gpt-oss-20b"

  # The prompt was echoed + parked WHILE the "finding a band" beat was still up; clearing
  # the beat must not eat the echo (the review's echo-eating regression).
  Scenario: A prompt typed before the auto-tune resolves survives in the transcript
    Given a fresh AGENT session with a free band "gpt-oss-20b" on air
    When the user submits the prompt "summarize the readme"
    And the desk auto-tunes
    Then the transcript shows "summarize the readme"

  # Audit finding [MAJOR]: /clear dropped agentQueued but NOT agentPending, and never
  # disarmed the in-flight auto-tune. A prompt parked before /clear then fired as a phantom
  # turn (its echo already wiped by the clear) when the auto-tune landed. /clear must disarm
  # the auto-tune and drop the parked prompt too - a fresh session is genuinely fresh.
  Scenario: A prompt parked before /clear never fires when the auto-tune lands
    Given a fresh AGENT session with a free band "gpt-oss-20b" on air
    When the user submits the prompt "leftover ask"
    And the session is cleared with "/clear"
    And the desk auto-tunes
    Then no chat turn is submitted
    And the transcript no longer shows "leftover ask"
    And the auto-tune did not arm

  # ── the handoff auto-tune binds the BOUND station's window, never the band's best ──
  #
  # Finding (2026-07-08): startOperatorHandoff's pre-bind gate read the BAND ctx (the MAX
  # window across the band's stations) but then bound bestFreeStation - a SPECIFIC station.
  # A free 8k station beside a paid 32k sibling: the band ctx (32k) cleared the pre-bind
  # gate, the free 8k station was bound, and the §6 floor then refused it POST-bind - the
  # bind-then-refuse state finding #6 was meant to kill. The gate must read the station it
  # is about to bind: a free station is bound only when ITS OWN window clears the 16k floor,
  # else an honest refusal with NO bind.
  Scenario: A free small station beside a paid large sibling is refused WITHOUT binding
    Given an AGENT session with a tuned band "qwen3-32b-fp8" and a live proxy holder
    And a detected guest "opencode"
    And the proxy holder is disconnected
    And the only free band pairs a free 8k station with a paid 32k sibling
    When the user runs "/operator opencode"
    Then no channel is open
    And the transcript does not show "auto-tuned to"
    And the transcript notes "no agent-ready free band on air"
    And no child process is launched

  # ── sticky + no-op ───────────────────────────────────────────────────────────

  Scenario: Sticky — a last-tuned band still on air is reused, not re-picked
    Given an AGENT session whose last band "llama-3.3-70b-instruct" is still on air
    Then the agent runs on "llama-3.3-70b-instruct"
    And the auto-tune did not arm
