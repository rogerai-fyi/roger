# HANDOFF increment 1: the AGENT's conversation must reach the capsule ring.
#
# THE BUG THIS FIXES: recordTurn is called from exactly two places today, both CHANNEL
# turns (internal/tui/tui.go:1530 assistant, :1967 user). The AGENT conversation - the one
# you are actually in when you are working on something locally - never enters the ring at
# all. So `writeHandoffCapsule` hands a guest an EMPTY capsule (it returns early on an
# empty ring), and every guest handoff from the agent silently carries nothing. This is not
# claude-specific: opencode, hermes and aider have been getting the same empty capsule.
#
# GROUND TRUTH:
#   internal/tui/context_capsule.go:42 recordTurn(role, content, agent, mdl, provider) -
#     appends one turn, assigns the next sequential index, bounds the ring at 400.
#   internal/tui/agent.go:1444 onAgentEvent - the live event stream: EventToolCall,
#     EventToolResult, EventFinal. EventFinal is where an agent turn completes.
#   internal/capsule/capsule.go:85 ToolCall{Arguments,Denied,Failed,ID,Name,Result} - the
#     FLAT cross-language shape, results inline on the call. ToolCallsRaw() serializes it
#     canonically; producers must use it so the at-rest bytes are already canonical.
#
# The channel and the agent share ONE thread and ONE index sequence: a capsule is the
# session, not a per-surface log.
#
# Enforced by: internal/tui/handoff_agent_turns_bdd_test.go

@handoff
Feature: The agent's conversation travels in the context capsule

  Rule: an agent turn is recorded like a channel turn

    Scenario: the user's agent prompt is recorded
      Given the agent is on a band
      When the user sends an agent prompt
      Then the ring carries that prompt as a user turn

    Scenario: the agent's answer is recorded
      Given an agent turn that ends with an answer
      Then the ring carries that answer as an assistant turn
      And the turn is attributed to the agent and the model it ran on

    Scenario: agent and channel turns share one index sequence
      Given a channel turn, then an agent turn, then another channel turn
      Then the three turns carry consecutive indices in one thread
      # A capsule is the SESSION. A guest reading it should see the work in the order it
      # happened, not two interleaved logs with colliding indices.

    Scenario: a turn that produced no answer records nothing
      Given an agent turn that ends with no text at all
      Then the ring is unchanged
      # An empty turn is not context; it is noise a guest would have to read past.

  Rule: tool calls ride WITH the turn that made them

    Scenario: a completed tool call carries its name, arguments and result
      Given an agent turn that called web_fetch and then answered
      Then the assistant turn carries one tool call
      And it carries the tool name, the arguments, and the result

    Scenario: a denied tool call is marked denied and carries no result
      Given an agent turn where the user denied a run_shell confirm
      Then the assistant turn carries that call marked denied
      And it carries no result for it
      # What the user REFUSED is context a guest needs: it is a decision, not an absence.

    Scenario: a failed tool call is marked failed
      Given an agent turn where a tool returned an error
      Then the assistant turn carries that call marked failed

    Scenario: several calls in one turn all ride on it
      Given an agent turn that called three tools before answering
      Then the assistant turn carries all three calls in the order they ran

    Scenario: tool calls do not leak into the NEXT turn
      Given an agent turn with tool calls, then a second turn with none
      Then the second turn carries no tool calls

    Scenario: a turn that ERRORS does not leave its calls behind
      Given an agent turn whose tools ran and then the turn errored
      And a second turn that answers with no tools of its own
      Then the second turn carries no tool calls
      # A cancelled or failed turn never reaches the answer that would consume its calls.
      # Left pending, they would be recorded as work the NEXT answer did.

  Rule: the capsule stays bounded and clean

    Scenario: a large tool result is truncated in the capsule
      Given an agent turn whose tool returned a very large result
      Then the recorded result is truncated to the capsule's per-result cap
      And the truncation is marked
      # A capsule is handed to another agent and may cross the wire. One fetched page must
      # not be able to make it enormous.

    Scenario: the ring cap still bounds a long agent session
      Given more agent turns than the ring holds
      Then the ring holds only the most recent turns
      And the turn indices of the survivors are unchanged
      # Regression of the existing ring bound, now that the agent can fill it.

    Scenario Outline: a credential never enters the capsule
      Given an agent session whose runtime holds a session key and a broker URL
      When turns are recorded and a capsule is exported
      Then the capsule contains no <secret>

      Examples:
        | secret            |
        | session key       |
        | broker auth token |
      # The capsule is written to a file a GUEST PROCESS reads. It carries conversation,
      # never the credentials that would let the guest spend on the band.

  Rule: clearing the agent clears what travels

    Scenario: /clear drops the agent turns from the ring
      Given recorded agent turns
      When the user clears the agent transcript
      Then those turns no longer travel in a handoff
      # What the user cleared from their screen must not still be handed to a guest.
