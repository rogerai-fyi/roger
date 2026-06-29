# AGENT: the in-TUI tool-using agent. From a channel you can hand the wheel to an agent that
# runs an autonomous tool-use loop ON the channel's model (the band you're tuned to) — reading,
# running commands, editing — while every turn still meters spend like a normal relay. The
# behaviors that matter: it runs on the RIGHT model, it loops over tool calls, ESC cancels a
# stuck turn AND stops the spend, and the wallet's spend caps still bound it.
#
# GROUND TRUTH (corrected): the tool-using agent is internal/harness, NOT internal/agent
# (internal/agent is the PROVIDER/sharing node - register + serve relayed jobs).
#   internal/harness/loop.go: Loop.Send(ctx, userText, emit) runs ONE user turn - it asks the
#     model (a Completer), executes any tool_calls (confirm-gating mutating tools), feeds the
#     results back, and loops until a final answer or MaxSteps. ctx cancellation (esc) stops it
#     promptly with NO further billed model call.
#   internal/harness/broker.go: BrokerCompleter(broker, user, MODEL, …, onCost) relays each turn
#     through the broker's /v1/chat/completions on the CHANNEL'S model, carrying the consumer
#     out-price cap, and reads back the per-turn receipt headers (cost / tokens-in / tokens-out /
#     tps) - the SAME relay + receipts as plain chat. A broker refusal (e.g. over the spend cap)
#     surfaces as an error that stops the turn.
#   internal/tui/agent.go: enterAgent wires the Loop to BrokerCompleter on rt.model (the tuned
#     channel's model) + an esc-cancellable ctx; leaving keeps the session Loop, so the transcript
#     is intact. The TUI NAVIGATION half (return-to-channel, still-tuned) is exercised by the TUI
#     tests; the scenario below pins the harness-level invariant it depends on (transcript retained).
#
# Enforced by: internal/harness/agent_bdd_test.go (this executable suite) + internal/harness/
#   *_test.go + the TUI agent tests.

Feature: Agent — the in-channel tool-using agent

  Scenario: /agent runs the agent on the current channel's model
    Given a channel tuned to model "gpt-oss-20b"
    When the user runs /agent
    Then the agent starts on "gpt-oss-20b" (the band you're on), not some default

  Scenario: The agent loops over tool calls until done
    Given the agent is given a task that needs several tools
    When it runs
    Then it iterates request -> tool call -> result -> next request until it finishes or is stopped

  Scenario: ESC cancels a stuck turn AND stops the spend
    Given an agent turn is in flight (a slow or stuck model)
    When the user presses esc
    Then the turn is cancelled
    And no further tokens are billed for that turn (the spend stops)

  Scenario: The agent respects the wallet spend cap
    Given a monthly spend cap is configured
    When the agent's usage would exceed the cap
    Then the next turn is refused (the cap bounds the agent like any relay)

  Scenario: Leaving the agent returns to the open channel
    Given the user is in the agent
    When they press esc to leave
    Then they return to the channel, still tuned, transcript intact

  Scenario: The agent meters each turn like a normal relay
    Given the agent completes a turn
    Then that turn's tokens in/out, throughput, latency, and cost are recorded (same receipts as chat)
