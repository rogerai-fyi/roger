# HANDOFF increment 2: the BRIEF - a capsule rendered as readable text.
#
# The capsule is roger.context.v1 JSON: perfect for a merge, useless to hand a coding agent
# as its opening context. Nothing in the repo renders one for a human today (the only
# rendering anywhere is the one-line import summary at cmd/rogerai/context.go:211). The
# brief is that missing renderer: `<workdir>/.roger/context.md`, written next to the
# capsule, and the FIRST thing the guest is told to read.
#
# WHY A FILE AND NOT A PROMPT: Claude Code's `-p` forces non-interactive (the user wants to
# keep working), piped stdin into an interactive session is ignored with a warning, and
# writing into the project's own CLAUDE.md would be an invasive edit to a checked-in file.
# A file in the handoff dir plus one opening prompt touches nothing of the user's.
#
# GROUND TRUTH: internal/capsule/capsule.go - Capsule{Thread,Messages,Meta},
#   Message{Role,Content,ToolCalls,XRoger}, ToolCall{Name,Arguments,Result,Denied,Failed}.
#   internal/harness/sources.go - the "[retrieved from <url> - untrusted page content...]"
#   marker that web_fetch writes around a page it read.
#
# Enforced by: internal/brief/brief_bdd_test.go

@handoff
Feature: The brief - a capsule a coding agent can actually read

  Rule: the brief reads as the session, in order

    Scenario: turns render in order with their roles
      Given a capsule with a user turn, an assistant turn, and another user turn
      When the brief is rendered
      Then the three turns appear in that order, each labelled with who said it

    Scenario: the header says where this came from
      Given a capsule from a session on model "gpt-oss-20b"
      When the brief is rendered
      Then the header names RogerAI as the source and the model the session ran on
      # The guest is a different agent on a different account. It should not have to guess
      # what handed it this, or on what.

    Scenario: an empty capsule renders no brief at all
      Given a capsule with no turns
      Then no brief is produced
      # Better to hand over nothing than a file that says nothing.

    Scenario: the same capsule always renders the same brief
      Given any capsule
      When the brief is rendered twice
      Then both renderings are byte-identical
      # No timestamps-of-now, no map iteration order: a brief is a function of the capsule.

  Rule: tool work is visible, because it is most of the context

    Scenario: a tool call renders with its name and arguments
      Given a capsule turn carrying a web_fetch call
      When the brief is rendered
      Then it shows the tool name and what it was called with

    Scenario: a denied call renders as refused, not as missing
      Given a capsule turn carrying a denied run_shell call
      Then the brief shows that call and that the user refused it
      # "The user said no to this" is one of the most useful things a guest can know.

    Scenario: a failed call renders as failed
      Given a capsule turn carrying a failed call
      Then the brief shows that call and that it failed

    Scenario: a tool result renders as a bounded excerpt
      Given a capsule turn whose tool result is long
      Then the brief shows an excerpt of the result, not the whole thing
      And the excerpt is marked as shortened

  Rule: retrieved web content stays marked as untrusted quoted material

    Scenario: a fetched page keeps its provenance in the brief
      Given a capsule turn whose tool result is a page fetched from "https://example.com/x"
      When the brief is rendered
      Then the excerpt is attributed to that URL
      And it is marked as retrieved content rather than as the user's own words
      # The brief is read BY AN AGENT WITH TOOLS. Page text that arrives looking like
      # instructions from the user is exactly the injection path answers mode was hardened
      # against; the marking must survive the handoff, not stop at RogerAI's edge.

    Scenario: a result that only LOOKS like a retrieval marker cannot crash the render
      Given a capsule turn whose result is a marker-shaped line too short to be one
      When the brief is rendered
      Then it renders without crashing
      And that result is treated as ordinary text, not as a retrieval
      # The marker's prefix ends with a space and its suffix starts with one, so a short
      # line can satisfy both ends at once by overlapping on that shared space. Tool
      # results are untrusted content: a crash here takes the TUI down mid-handoff.

  Rule: the brief asks for something back

    Scenario: the brief tells the guest how to report back
      Given a capsule with any turns
      When the brief is rendered
      Then it names the file the guest should write what it did to
      # Otherwise the return trip is a reader with no writer.

  Rule: the brief is bounded and safe to hand over

    Scenario: a huge session renders a bounded brief
      Given a capsule far larger than the brief budget
      When the brief is rendered
      Then the brief is within the budget
      And it says that earlier turns were omitted
      And the MOST RECENT turns are the ones kept
      # What you were doing last is what the guest most needs.

    Scenario: a single turn larger than the whole budget still renders
      Given a capsule whose one and only turn is larger than the brief budget
      When the brief is rendered
      Then the brief still carries that turn
      # Dropping everything would leave a file that says "earlier turns omitted" with
      # nothing below it - worse than no brief at all.

    Scenario Outline: the brief never carries a credential
      Given a capsule exported from a session holding a session key and a broker token
      Then the brief contains no <secret>

      Examples:
        | secret            |
        | session key       |
        | broker auth token |

    Scenario: control bytes never reach the brief
      Given a capsule turn whose content carries ANSI escapes and control bytes
      Then the brief carries none of them
      # The brief is displayed in a terminal by whatever reads it next.
