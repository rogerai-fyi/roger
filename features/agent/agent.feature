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

  # A TOOL RESULT MUST FIT THE MODEL IT IS FED TO (2026-08-07, calm-lynx-53-foundation).
  # The founder ran the agent on Apple's on-device `foundation` band - an 8192-token window -
  # and one web_fetch returned ~10KB. The station answered "Exceeded model context window
  # size" and the turn died. The tools' clip was a flat 16 KiB regardless of model: a
  # rounding error on a 128K band, HALF THE WINDOW on an 8K one.
  # Enforced by: internal/harness/ctxbudget_test.go + ctxbudget_loop_test.go +
  #   internal/tui/agent_ctx_budget_test.go. (Doc spec.)

  Scenario: A tool result is capped to fit the band the agent runs on
    Given the agent is running on a small-context band
    When a tool returns more than that window can hold
    Then the result is cut to a share of the window before it enters the conversation
    And it is marked as truncated, so a partial file is never read as complete
    And a roomy band is unaffected, because this is a floor-and-ceiling, not a downgrade

  Scenario: The cap follows a /model switch
    Given the agent moves from a roomy band to a small one
    Then the tool cap shrinks with it, rather than keeping the roomy budget

  Scenario: An unknown context window keeps the historical cap
    Given the broker reports no context window for the tuned model
    Then the flat 16 KiB cap applies, because a guess could be worse than the status quo

  Scenario: The operator sees exactly what the model saw
    When a tool result is truncated
    Then the transcript shows the truncated text, not the full one

  # YOUR OWN MODELS, WITHOUT SHARING THEM (founder ask 2026-08-07).
  # The only way to reach your own model from the agent used to be to put it ON AIR -
  # register it with the broker and let every turn relay back to your own box. A PRIVATE
  # band is not an offline mode either: features/discovery/bands.feature is explicit that
  # --private is a DISCOVERY choice, so it still registers, still binds to the account, and
  # still obeys the price ceiling. Nothing offered a model that simply stays home.
  # GROUND TRUTH: internal/harness/local.go (LocalCompleter - BrokerCompleter minus the
  #   marketplace), internal/tui/agent_local.go (detect-backed rows, background scan),
  #   internal/tui/agent.go (bindAgentEndpoint routes the turn).
  # Enforced by: internal/harness/local_test.go + internal/tui/agent_local_test.go. (Doc spec.)

  Scenario: The picker offers models running on this machine
    Given an OpenAI-compatible server is running locally
    Then its chat models appear in /model under their own heading, marked local
    And a local voice model is never offered, because it cannot run a tool loop
    And a local model shows no price, because there is none to show

  Scenario: A local turn never touches the broker
    Given the agent is running on a local model
    When it takes a turn
    Then the request goes straight to that server
    And it carries no signature, no price cap, and no broker user
    And nothing is registered, metered, or billed

  Scenario: Switching back to a band restores the marketplace path
    Given the agent was running on a local model
    When the operator picks a broker band
    Then the turn relays through the broker again, not the local server

  Scenario: Opening the picker never waits on a port scan
    Then /model opens from memory, and a background scan folds its results in when it lands
