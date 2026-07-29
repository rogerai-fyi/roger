# ANSWERS phase 1: the web_search builtin. A NEW read-only tool in the harness toolset
# (internal/harness/tools.go BuiltinTools) that queries a configured search provider and
# returns ranked results (title / url / snippet) for the agent loop to read and then
# web_fetch. This is the first brick of the Perplexity-style "answers with sources" mode:
# client-side, no broker change (tools round-trip verbatim per internal/harness/broker.go).
#
# GROUND TRUTH (the precedent this extends):
#   internal/harness/tools.go: Tool{Mutating:false} auto-runs (read-only tools need no
#     confirm); maxToolOutput (16 KiB) clips every tool result with a truncation marker;
#     errors returned by Run are surfaced TO THE MODEL as the tool result so it can
#     recover (never crash the loop).
#   internal/harness/loop.go: the loop advertises ToolSchemas at turn start and executes
#     parsed tool_calls.
# Provider adapter: pluggable (config-driven), NOT hardcoded to one vendor. The provider
# key/endpoint live in the roger config. Founder-approved 2026-07-27: BRAVE is the first
# (and MVP-only) adapter; the stub server in the suite speaks the real Brave Search API
# wire shape.
#
# Enforced by (once approved): internal/harness/answers_bdd_test.go + table-driven unit
# tests. NO MOCK MODELS for the loop; the search provider is exercised against a local
# stub HTTP server speaking the provider's real wire format (same pattern as the harness
# fetch tests) - the adapter contract, not a mocked adapter.

@answers
Feature: web_search - the retrieval tool that finds sources

  Background:
    Given a search provider is configured with a valid key

  Rule: web_search is a read-only builtin that auto-runs

    Scenario: web_search is advertised in the tool schema when configured
      When the agent loop starts a turn
      Then the tools array includes "web_search" with a required "query" string parameter
      And an optional bounded "count" integer parameter

    Scenario: web_search auto-runs without a confirm prompt
      Given the model emits a web_search tool call
      When the loop executes it
      Then no confirm prompt is shown (read-only tools auto-run)
      And the results are fed back to the model as the tool result

    Scenario: web_search is NOT advertised when no provider is configured
      Given no search provider is configured
      When the agent loop starts a turn
      Then "web_search" is absent from the tools array
      # The model must never be offered a tool that can only dead-end.

  Rule: results are ranked, bounded, and fetch-ready

    Scenario: a query returns ranked results with title, url, and snippet
      When the model calls web_search with query "valkey pubsub reconnect backoff"
      Then the tool result lists results in provider rank order
      And each result carries a title, an http(s) url, and a snippet

    Scenario Outline: the result count is bounded regardless of what the model asks for
      When the model calls web_search with count <asked>
      Then at most <returned> results are returned

      Examples:
        | asked | returned |
        | 3     | 3        |
        | 100   | 10       |
        |       | 5        |
      # Default 5, hard cap 10: bounds tokens fed back and downstream fetch fan-out.

    Scenario: non-http(s) result URLs are filtered out
      Given the provider returns a result with url "ftp://mirror.example/file"
      When the results are shaped
      Then that result is dropped
      And only http:// and https:// results reach the model
      # Everything web_search surfaces must be safe to hand to web_fetch.

    Scenario: an oversized result set is clipped with a truncation marker
      Given the shaped results exceed the tool-output cap
      Then the result is clipped to maxToolOutput and marked truncated
      # Same clip() contract as every other builtin.

  Rule: provider text is cleaned before the model sees it

    Scenario: markup in a snippet is stripped
      Given the provider returns a snippet marked up with "<strong>" around the match
      When the model calls web_search
      Then the snippet reaches the model as plain text
      And no markup tags survive
      # Brave wraps query-term matches in <strong>. Passing that through spends the model's
      # context on markup and hands an agent tag-shaped text it has no reason to trust.

    Scenario: escaped markup cannot re-form into tags after decoding
      Given the provider returns a snippet containing "&lt;strong&gt;"
      When the model calls web_search
      Then no markup tags survive
      # Stripping runs before decoding, so a snippet that ESCAPES its markup would
      # otherwise decode into tag-shaped text after the stripper had already gone past.

    Scenario: escaped entities are decoded
      Given the provider returns a title containing "&amp;" and "&#39;"
      When the model calls web_search
      Then the title reaches the model with those characters decoded

  Rule: provider failures surface to the model as recoverable results, never crash the loop

    Scenario Outline: provider errors become tool results the model can react to
      Given the search provider responds with <failure>
      When the model calls web_search
      Then the loop does not abort
      And the tool result states the search failed and why
      And the model can continue the turn and answer without sources

      Examples:
        | failure               |
        | HTTP 500              |
        | HTTP 429 rate limited |
        | a connect timeout     |
        | malformed JSON        |

    Scenario: a 429 is not retried inside the same tool call
      Given the search provider responds 429
      When the model calls web_search
      Then exactly one provider request is made for that call
      # No tight retry loop hammering a rate-limited provider
      # (same lesson as the /discover 429 incident).

    Scenario: an empty result set is an explicit answer, not an error
      Given the provider returns zero results
      Then the tool result says no results were found for the query
      And it is not presented as a failure

    Scenario: an empty query is rejected as a tool error
      When the model calls web_search with query ""
      Then the tool result is an error naming the empty query
      And the loop continues

    Scenario: an over-long query is rejected before reaching the provider
      When the model calls web_search with a query longer than the query cap
      Then the tool result is an error naming the cap
      And no provider request is made

  Rule: queries are transient

    Scenario: the query is not persisted outside the conversation transcript
      When the model calls web_search with a query
      Then the query is not written to any file on disk
      And it lives only in the in-memory transcript
