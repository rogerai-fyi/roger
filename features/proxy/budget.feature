# GUEST OPERATORS — Phase 1 proxy hardening (money-path, spec-first). CORE MONEY PATH.
#
# PER-SESSION SPEND BUDGET — a guest agent runs an autonomous loop: dozens or hundreds of
# priced calls with no human in the loop. Today NOTHING caps the session total. The proxy
# already SEES the per-response cost in the X-RogerAI-Cost header (copyRelayResponse copies
# it, client.go:541), so it can ACCUMULATE and HARD-STOP at a configurable budget with a
# clean OpenAI-shaped 402. This is the backstop for design doc §8 "Key exfil" (the agent can
# read the wallet key, but the budget bounds the blast radius) and the safety rail that makes
# handing the mic to a third-party CLI acceptable.
#
# GROUND TRUTH: X-RogerAI-Cost is credits (1 credit = $1), parsed as float (see ChatDetailed
# client.go:1079-1080). The budget accumulator lives in the proxy handler closure, shared
# across all concurrent requests. Proposed seams: ProxyOptions.Budget (dollars; 0 = the
# default), a thread-safe running total, and a way to RAISE/RESET it (the TUI /budget knob and
# the pre-launch plate, Phase 3).
#
# ACCOUNTING RULE: accumulate the ACTUAL billed X-RogerAI-Cost of each COMPLETED response
# (streaming AND non-streaming — the header is present on both once the stream closes). The
# check is: if (spent_so_far >= budget) refuse the NEXT request with 402; a request already
# in flight is not retro-killed. Concurrency: the accumulate-and-check is atomic so N parallel
# guest subagents cannot each read "under budget" and all slip through (design doc §8 rate
# limits notes parallel-subagent CLIs).
#
# FOUNDER RULINGS (approved 2026-07-06; boundary re-approved 2026-07-07):
#   - DEFAULT budget value: $2.00, raisable (design doc §5).
#   - Boundary semantics (founder ruling 2026-07-07 — the LITERAL CEILING): admit while
#     cumulative spent < budget; refuse once spent >= budget. The call that CROSSES the budget
#     completes (the spend may tip slightly over); the NEXT call is refused with the OpenAI
#     402. This supersedes the earlier pre-emptive "spent + last-cost would exceed" draft;
#     the streaming scenario below was rewritten to the ceiling accordingly (founder-approved
#     spec edit, 2026-07-07).
#   - 402 error `type`: "insufficient_quota" (OpenAI's billing type) vs a custom
#     "budget_exceeded". Proposed code "budget_exceeded", type "insufficient_quota".
#   - Does the budget-402 message tell the user how to raise it (the /budget knob)?
#
# EXECUTABLE: step defs deferred; the Budget seam lands with implementation. RED evidence:
# TestProxySpendBudget in proxy_hardening_test.go asserts a 402 arrives once the running total
# reaches the cap — RED today because no budget is enforced (every request 200s). No REAL
# money: a stub broker returns a fixed X-RogerAI-Cost header.

Feature: Local proxy per-session spend budget

  Background:
    Given a tuned band whose model is "qwen3-32b-fp8"
    And a session spend budget of $1.00
    And a stub broker that bills $0.25 per response via X-RogerAI-Cost

  Scenario: Requests under budget pass through
    When 3 chat requests are made
    Then all 3 return 200
    And the accumulated session spend is $0.75

  Scenario: The request that reaches the budget is served; the next is hard-stopped 402
    When 4 chat requests are made
    Then the first 4 return 200 and accumulate $1.00
    When a 5th chat request is made
    Then the status is 402
    And the body is an OpenAI error with type "insufficient_quota"
    And the broker was never called for the 5th request (no further spend)

  Scenario: Exactly-at-boundary — spend equal to budget refuses the next request
    Given the accumulated session spend is exactly $1.00
    When another chat request is made
    Then the status is 402
    And no additional cost is accumulated

  Scenario: Just-under-boundary still admits
    Given the accumulated session spend is $0.75
    When another $0.25 chat request is made
    Then the status is 200
    And the accumulated session spend is $1.00

  Scenario: A refused request bills nothing (the brake is before dispatch)
    Given the accumulated session spend is exactly $1.00
    When another chat request is made
    Then the broker was never called (no hold, no spend)
    And the accumulated session spend is still $1.00

  Scenario: Raising the budget unblocks further requests
    Given the accumulated session spend is exactly $1.00 and requests are being refused 402
    When the session budget is raised to $2.00
    And another $0.25 chat request is made
    Then the status is 200
    And the accumulated session spend is $1.25

  Scenario: Resetting the budget (new session) zeroes the accumulator
    Given the accumulated session spend is exactly $1.00
    When the session spend is reset
    Then the accumulated session spend is $0.00
    And the next $0.25 chat request returns 200

  Scenario: Non-streaming responses are accounted from X-RogerAI-Cost
    Given a stub broker that bills $0.40 per non-streaming response
    When 2 chat requests are made
    Then the accumulated session spend is $0.80

  Scenario: Streaming responses are accounted the same as non-streaming
    Given a stub broker that streams an SSE response then sets X-RogerAI-Cost to $0.40
    When 2 streaming chat requests are made
    Then the accumulated session spend is $0.80
    # Founder ruling 2026-07-07 (literal ceiling): $0.80 is still UNDER the $1.00 budget, so
    # the 3rd call is ADMITTED and completes - the crossing call may tip the spend over.
    When a 3rd streaming chat request is made
    Then the status is 200
    And the accumulated session spend is $1.20
    And a 4th streaming request past the $1.00 budget is refused 402

  Scenario: A response with no cost header accumulates nothing (fail-safe, not fail-open on spend)
    Given a stub broker that returns 200 with NO X-RogerAI-Cost header
    When a chat request is made
    Then the accumulated session spend is unchanged
    And the request still returns 200

  # CONCURRENCY INVARIANT — the crux for parallel guest subagents.
  Scenario: Concurrent requests cannot race past the budget
    Given a session spend budget of $1.00
    And a stub broker that bills $0.25 per response
    When 20 chat requests are fired concurrently
    Then the total accumulated spend never exceeds $1.00
    And at most 4 requests are served 200 and the rest are refused 402
    And the accumulate-and-check is atomic (no read-modify-write race)

  Scenario: The budget is independent of the broker-side monthly cap
    Given the account has a broker monthly cap that is not exceeded
    And the session budget is exceeded
    Then the proxy 402s locally without ever calling the broker
    # (the two limits compose: either can stop a spend)
