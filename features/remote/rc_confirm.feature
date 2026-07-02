# Executable spec for the tool-CONFIRM fan-in on a remote-control session (BASE STATION,
# v5.0.0). The embedded agent's mutating-tool y/N gate (harness Confirmer → the TUI
# confirmReq/confirmResp channels, agent.go:49-50,279) is fanned out: a pending confirm is a
# confirm_req frame with a confirm_id; the local keypress AND remote confirm messages race
# into confirmResp; FIRST answer wins; a confirm_done frame names who answered and closes it
# everywhere. Ground truth: internal/tui/agent.go + rcbridge (Increment 3).
#
# INVARIANTS:
#   F1 a mutating tool emits a confirm_req to every surface, and nothing runs until answered.
#   F2 a remote APPROVE runs the tool; a remote DENY feeds the denied result (tool skipped).
#   F3 first answer wins across local + remote; the loser's later answer is a no-op.
#   F4 confirm_done names the answering origin, shown on every surface.

Feature: Answering a remote agent's tool confirmation from any surface

  Background:
    Given a running broker
    And a host with remote control enabled and one attached remote surface

  Scenario: a mutating tool waits for a confirm on all surfaces (F1)
    When the agent proposes a mutating tool call
    Then a confirm_req is delivered to the local TUI and the remote surface
    And the tool does not run until the confirm is answered

  Scenario: a remote approve runs the tool (F2)
    Given the agent is awaiting a tool confirm
    When the remote surface approves it
    Then the tool runs on the host
    And a confirm_done naming the remote surface is shown everywhere

  Scenario: a remote deny skips the tool (F2)
    Given the agent is awaiting a tool confirm
    When the remote surface denies it
    Then the tool is skipped with a denied result
    And a confirm_done naming the remote surface is shown everywhere

  Scenario: first answer wins across surfaces (F3)
    Given the agent is awaiting a tool confirm
    When the local TUI approves it before the remote answers
    Then the tool runs once
    And a later remote answer for the same confirm is a no-op
