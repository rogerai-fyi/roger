# ANSWERS phase 1: the mode itself - loop bounds, metering, degradation, and where it
# runs. Phase 1 is deliberately the EXISTING client-side agent harness gaining retrieval
# (web_search + hardened web_fetch) and citations; no broker orchestrator, no new server
# endpoint, no billing change. Every model turn is a normal metered relay, so spend caps,
# receipts, and moderation all apply unchanged by construction.
#
# GROUND TRUTH:
#   internal/harness/loop.go + broker.go: the loop relays each turn via
#     /v1/chat/completions on the tuned channel's model with the same receipts as chat
#     (already pinned by features/agent/agent.feature - not re-specced here except where
#     answers mode adds pressure).
#   cmd/rogerai-broker/toolcall.go: `tools` is a VERIFIED capability (canary-probed,
#     never self-declared) - answers mode leans on that verification.
#
# DECISIONS (founder-approved 2026-07-27): the existing /agent gains the retrieval tools
# (no separate /answers binding); the provider is BRAVE; the budgets stand at 3 searches /
# 8 fetches per turn; the SSRF guard vets addresses, not ports. "Answers mode" below means
# an /agent turn that uses retrieval.
#
# Enforced by (once approved): internal/harness/answers_mode_bdd_test.go (real loop, real
# harness broker plumbing against the test broker, no mocks).

@answers
Feature: Answers mode - bounded retrieval on the tuned band

  Rule: answers mode runs on the market like any relay

    # NOTE: "answers mode requires a tools-verified station" is a TUI-entry behavior (the
    # band's capabilities live in the TUI's offer list, not in the harness), so it is
    # specced and executed in features/answers/tui_display.feature. It stays out of this
    # file only so every scenario runs in the package that owns the behavior.

    Scenario: retrieval turns meter exactly like chat turns
      Given an answers turn that made 2 model calls around its retrievals
      Then each model call produced the standard receipt (cost, tokens in/out, tps)
      And the wallet's spend cap bounded them like any relay

    Scenario: the spend cap refusal ends the turn cleanly mid-retrieval
      Given the spend cap is crossed between retrieval steps
      When the loop attempts the next model call
      Then the refusal surfaces as the turn's outcome
      And no further retrievals or model calls are made

  Rule: retrieval is budgeted per user turn

    Scenario: the per-turn retrieval budget bounds fetch fan-out
      Given the per-turn budget of 3 searches and 8 fetches
      When the model attempts a 4th web_search in one turn
      Then the call returns a budget-exhausted tool result without touching the network
      And the model is told to answer with what it has

    Scenario: the budget resets on the next user turn
      Given the previous turn exhausted its retrieval budget
      When the user sends a new message
      Then the new turn starts with a full budget

    Scenario: budget-exhausted is information, not an error
      Given the budget is exhausted mid-turn
      Then the turn still produces an answer (with the sources gathered so far)
      And the answer is not presented as a failure

  Rule: retrieval failures degrade, they do not dead-end

    Scenario: a search-provider outage degrades to answering without sources
      Given the search provider is down for the whole turn
      When the user asks a question
      Then the model receives the failure as tool results
      And the turn still produces an answer
      And the answer carries no sources block (nothing was retrieved)

    Scenario: esc cancels an in-flight retrieval and the turn
      Given a web_fetch is in flight against a slow host
      When the user presses esc
      Then the fetch is abandoned promptly
      And the turn stops with no further billed model call
