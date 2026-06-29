# AGENT: the in-TUI tool-using agent. From a channel you can hand the wheel to an agent that
# runs an autonomous tool-use loop ON the channel's model (the band you're tuned to) — reading,
# running commands, editing — while every turn still meters spend like a normal relay. The
# behaviors that matter: it runs on the RIGHT model, it loops over tool calls, ESC cancels a
# stuck turn AND stops the spend, and the wallet's spend caps still bound it.
#
# GROUND TRUTH:
#   internal/agent/agent.go: Run(cfg) drives the tool-use loop against an OpenAI-compatible
#     endpoint (the channel's local proxy -> the broker -> the chosen node).
#   internal/tui: `/agent` (and [0]) enters the agent on THIS channel's model (enterAgent);
#     esc returns / cancels the turn. (v4.8.3: esc cancels a stuck agent turn and stops spend.)
#
# Enforced by: internal/agent/*_test.go + the TUI agent tests. (Doc spec; convertible to godog.)

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
