# Executable spec for the remote-control HAPPY PATH (BASE STATION, v5.0.0): enable → attach →
# send a turn → the host runs the agent loop locally and streams the answer back → every
# attached surface sees the interleaved, origin-tagged transcript. The host's own model call
# bills the host exactly as a local turn (see rc_money.feature). Ground truth:
# cmd/rogerai-broker/rc.go + internal/harness/rcbridge.go (Increments 2-3).
#
# INVARIANTS:
#   H1 enable auto-names the session "<host> · <cwd>" and returns a one-time code + host token.
#   H2 a turn from a remote surface reaches the host and its answer streams back to that surface.
#   H3 with two surfaces attached, both see the same frames, and each user turn is tagged with
#      its origin (which surface typed it).
#   H4 the host answering a remote turn goes through the host's normal billed model relay.

Feature: Continue a live agent session from another surface

  Background:
    Given a running broker
    And a host on "hermes" running the embedded agent in "~/ai/RogerAI"

  Scenario: enable auto-names and returns the secrets once (H1)
    When the host enables remote control
    Then the session is named "hermes · RogerAI"
    And a one-time link code and a host token are returned exactly once

  Scenario: a remote turn round-trips through the host (H2)
    Given the host has remote control enabled
    And the owner attaches a second surface with the link code
    When the second surface sends "summarize train.log"
    Then the host receives the turn and runs its agent loop locally
    And the assistant's answer streams back to the second surface

  Scenario: two surfaces see one interleaved, attributed transcript (H3)
    Given the host has remote control enabled
    And the owner attaches surface "web" and surface "roger @ macbook-air"
    When "web" sends "check the run" and "roger @ macbook-air" sends "and plot it"
    Then both surfaces see both turns
    And each turn is tagged with the surface that sent it

  Scenario: the host's model call bills the host (H4)
    Given the host has remote control enabled
    When a remote surface sends a turn that triggers a model call
    Then the model call is signed by the host and billed to the host wallet
