# ANSWERS phase 1 / TUI: showing retrieval work as it happens. The agent's transcript
# renders one line per tool call (the tool name plus a per-tool argument summary) and, for
# read-only tools, a short preview of the result underneath. web_search is a NEW read-only
# tool, so without this it renders as a bare "web_search" with no query and no results -
# the one auto-running tool the user cannot see the point of.
#
# GROUND TRUTH:
#   internal/tui/agent.go toolArgSummary  - the per-tool argument summary on the call line
#     (run_shell -> cmd, write_file/read_file -> path, list_dir -> path or ".",
#     web_fetch -> url). An unknown tool summarises to "" (a bare line).
#   internal/tui/agent.go previewableTool - which tools show an inlined result preview.
#     The read-only tools do (the user asked to see that output); write_file does not
#     (its result is just "wrote N bytes").
#   internal/tui/agent.go, the /help handler - the line listing which tools auto-run and
#     which ask first.
#
# MINIMIZATION: the /help line is DERIVED from the live toolset (harness.BuiltinTools),
# not a second hand-maintained list. That is what keeps it honest when web_search is
# absent, and it cannot drift when a tool is added or removed.
#
# Enforced by: internal/tui/answers_display_bdd_test.go (this executable suite) driving the
# real helpers - they are pure functions, so no bubbletea program is needed.

@answers @tui
Feature: The TUI shows retrieval work

  Rule: a retrieval call line says what was asked for

    Scenario: a web_search call line shows the query
      When the agent calls web_search with query "valkey pubsub reconnect backoff"
      Then the call line summary is "valkey pubsub reconnect backoff"

    Scenario: a long query is clipped to one line
      When the agent calls web_search with a query longer than the line budget
      Then the call line summary is clipped to a single line

    Scenario: a web_fetch call line still shows the URL
      When the agent calls web_fetch with url "https://valkey.io/topics/pubsub/"
      Then the call line summary is "https://valkey.io/topics/pubsub/"
      # Regression: the existing behavior must survive the answers-mode wiring.

  Rule: retrieval results are previewed like any other read-only output

    Scenario Outline: the read-only tools preview their result
      Then "<tool>" is previewable

      Examples:
        | tool       |
        | web_search |
        | web_fetch  |
        | read_file  |
        | list_dir   |
        | run_shell  |

    Scenario Outline: nothing else previews
      Then "<tool>" is not previewable

      Examples:
        | tool        |
        | write_file  |
        | unknown     |
        |             |

  Rule: entering the agent says what the band can actually do

    Scenario: a band whose capabilities say tools are absent is called out on entry
      Given the tuned band declares capabilities that do not include "tools"
      When the user enters the agent
      Then the transcript says this band cannot drive tools
      And it hints to tune to a tools-capable band
      # "tools" is earned via the broker's canary probe, never self-declared, so the TUI
      # trusts that verification rather than probing again. Entry is NOT blocked: the loop
      # degrades to plain chat, and the user deserves to know that up front rather than
      # discovering it when the agent silently stops calling tools.

    Scenario: a tools-verified band enters clean
      Given the tuned band carries the verified "tools" capability
      When the user enters the agent
      Then no tools-capability warning is shown

    Scenario: a band with NO capability set is not warned about
      Given the tuned band has no capability set at all
      When the user enters the agent
      Then no tools-capability warning is shown
      # An ABSENT set is undetermined, not a negative finding - the same rule the offer
      # badges follow. A live run against production found deepseek-v4-flash driving tools
      # happily with no capability set published, so treating absence as evidence would
      # have warned users off a band that works.

    Scenario: an unknown band is not warned about
      Given the tuned model is not in the offer list
      When the user enters the agent
      Then no tools-capability warning is shown

  Rule: an answer's sources survive into what the user copies

    Scenario: the copied transcript includes the numbered source URLs
      Given an agent answer carrying a sources block is on screen
      When the user copies the transcript
      Then the copied text includes the numbered source URLs

  Rule: the help line is derived from the live toolset, never hand-maintained

    Scenario: with a search provider configured, help lists web_search as auto-running
      Given a search provider is configured
      When the agent shows its toolset line
      Then it lists "web_search" among the tools that run on their own
      And it lists "write_file" and "run_shell" among the tools that ask first

    Scenario: with no provider configured, help does not advertise web_search
      Given no search provider is configured
      When the agent shows its toolset line
      Then "web_search" is absent from the line
      And the remaining tools are still listed
      # Advertising a tool the user has not configured would send them looking for a
      # feature that cannot run.
