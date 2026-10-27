# Regression spec - a detected capability must survive the share controller onto the offer.
#
# The CLI share path carries detected capabilities (cmd/rogerai main.go foundCapabilities).
# The TUI and the local web console share through node.Controller instead, and its
# ShareRow had no Capabilities field: detection found "vision", loadRows dropped it, and
# the model went on air as text-only. /market masked it with a name fallback; /discover
# did not. Category work rides the same rows, so this is fixed first.

Feature: The share controller carries detected capabilities onto the on-air offer

  Background:
    Given a share controller for station "kitchen"

  Scenario: A vision model detected by a scan goes on air labelled vision
    Given detection found model "qwen3-vl-8b" on "http://127.0.0.1:8081" with capabilities "vision"
    When the controller loads the detected rows
    And the operator puts "qwen3-vl-8b" on air
    Then the on-air offer for "qwen3-vl-8b" has capabilities "vision"

  Scenario: A text-only model goes on air with no capabilities
    Given detection found model "gpt-oss-20b" on "http://127.0.0.1:8060" with no capabilities
    When the controller loads the detected rows
    And the operator puts "gpt-oss-20b" on air
    Then the on-air offer for "gpt-oss-20b" has no capabilities

  Scenario: Capabilities stay with their own model across several servers
    Given detection found model "qwen3-vl-8b" on "http://127.0.0.1:8081" with capabilities "vision"
    And detection found model "gpt-oss-120b" on "http://127.0.0.1:8080" with no capabilities
    When the controller loads the detected rows
    Then the row for "qwen3-vl-8b" has capabilities "vision"
    And the row for "gpt-oss-120b" has no capabilities

  Scenario: A duplicate model id keeps the first server's capabilities
    Given detection found model "llava" on "http://127.0.0.1:8081" with capabilities "vision"
    And detection found model "llava" on "http://127.0.0.1:9000" with no capabilities
    When the controller loads the detected rows
    Then there is exactly one row for "llava"
    And the row for "llava" has capabilities "vision"

  Scenario: The controller hands the row a copy, not the detector's slice
    Given detection found model "qwen3-vl-8b" on "http://127.0.0.1:8081" with capabilities "vision"
    When the controller loads the detected rows
    And the detector's capability list for "qwen3-vl-8b" is later overwritten with "tools"
    Then the row for "qwen3-vl-8b" has capabilities "vision"

  # A node-declared "tools" is still stripped by the broker (trust/toolcall_probe.feature);
  # carrying capabilities through the controller does not change who may grant it.
