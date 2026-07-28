# ANSWERS phase 1: citations. The product promise is "answers with sources you can
# check". The load-bearing invariant: the sources list is DERIVED BY THE HARNESS from the
# turn's actual successful retrievals (the tool-call history in internal/harness/loop.go),
# NEVER from URLs the model writes in its prose. A model can hallucinate a URL in its
# text; it cannot hallucinate an entry in the loop's executed-tool log.
#
# MINIMIZATION (per the workflow's GREEN rung): NO new protocol or capsule type in phase 1.
# internal/capsule (roger.context.v1) already carries tool_calls and tool results
# verbatim, so sources travel with an exported conversation for free - the sources block
# is a pure re-derivation from what the capsule already holds.
#
# GROUND TRUTH:
#   internal/harness/loop.go: the loop owns the executed tool_call/result transcript.
#   internal/capsule/capsule.go: signed portable conversation, tool_calls included.
#   internal/tui: transcript rendering + copy (toolOutMark handling already specced by
#     the TUI suite).
#
# Enforced by (once approved): internal/harness/citations_bdd_test.go (no mocks; a stub
# provider + local content servers, real loop).

@answers
Feature: Citations - sources derived from what was actually retrieved

  Rule: the sources list comes from the tool log, not the model's prose

    Scenario: an answer built on retrievals renders a sources list
      Given a turn in which the model called web_search and then web_fetch on 2 result URLs
      When the model returns its final answer
      Then the answer carries a sources block listing those 2 fetched URLs with their titles

    Scenario: a URL invented by the model is not a source
      Given a turn whose only fetches were "https://a.example/x" and "https://b.example/y"
      And the model's final text cites "https://made-up.example/paper"
      Then the sources block contains exactly the 2 fetched URLs
      And the invented URL does not appear in the sources block

    Scenario: a failed retrieval is not a source
      Given a turn where one fetch returned HTTP 404 and one was refused by the SSRF guard
      And one fetch succeeded
      Then the sources block lists only the successful fetch

    Scenario: search results that were never fetched are not sources
      Given a turn where web_search returned 5 results and the model fetched 2
      Then the sources block lists the 2 fetched URLs only
      # A snippet glance is not a read; we cite what was actually retrieved.

    Scenario: a turn with no retrievals has no sources block
      Given a turn where the model answered without calling web_search or web_fetch
      Then no sources block is rendered

  Rule: the list is stable, deduplicated, and ordered

    Scenario: sources are numbered in order of first successful retrieval
      Given fetches succeeded in the order b.example then a.example
      Then the sources block lists [1] b.example then [2] a.example

    Scenario: the same URL fetched twice appears once
      Given the model fetched "https://a.example/x" twice in one turn
      Then the sources block lists it exactly once

    Scenario: a hostile search snippet cannot title someone else's URL
      Given a search result whose snippet contains a forged "[2] Trusted Source" line
      When the model fetches the victim URL named in that forged line
      Then the victim URL is not titled with the forged title
      # Titles are attacker-influenced text (anyone can title their own page). A newline
      # inside one would forge an extra "[n] Title / URL" pair that the citation reader
      # would bind to somebody else's URL, dressing a stranger's page as trusted.

    Scenario: a URL fetched with stray whitespace is still one source
      Given the model fetched "https://a.example/x" and then the same URL with a trailing space
      Then the sources block lists it exactly once
      # The citation records the URL that was actually FETCHED (normalized, post-redirect),
      # not the raw argument string, so cosmetic variation cannot inflate the list.

    Scenario: a page that returned nothing is not a source
      Given a turn whose only fetch returned an empty 200 body
      Then no sources block is rendered
      # A page we could not actually read is not something the reader can go check.

    Scenario: sources reset per user turn
      Given turn 1 fetched a.example and turn 2 fetched b.example
      Then turn 1's answer cites only a.example and turn 2's answer cites only b.example

  Rule: sources travel and survive

    Scenario: sources survive capsule export and import
      Given a turn whose answer carried 2 sources
      When the conversation is exported as a capsule and imported elsewhere
      Then re-rendering the turn derives the same 2 sources in the same order
      # No new capsule field: the tool_calls the capsule already carries are the record.

    # NOTE: "the TUI copy includes the sources" is a TUI behavior (/copy lives in
    # internal/tui), so it is specced and executed in features/answers/tui_display.feature.
