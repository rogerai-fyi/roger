# Corrections + the framing principle from the models agent's 2026-08-01 answer
# (rogerai-internal-docs/ANSWER-FROM-MODELS-AGENT-playbox-nano.md):
#   - Wave models are CONTRACT models: the device prompt is part of the device -
#     unframed they floor, framed they perform. Model + prompt ship as one unit.
#   - ESCALATE is the models' strongest measured skill: it renders as a GOOD
#     outcome, never as a warning state.
#   - Naming truth: Wave Nano (350M) is the trained gateway-class brain; Roger
#     Edge is the MCU classifier line with no trained artifact yet.
Feature: The Edge simulator tells the whole truth
  In order to demonstrate contract models the way they actually work
  As a Playbox visitor on the Roger Edge surface
  I want the device prompt visible, ESCALATE celebrated, and the names honest

  Scenario: every simulated device carries its fixed device prompt
    When an event card lands on the device
    Then the readout shows the device's fixed system framing as part of the device
    And the framing text is an excerpt of the real production device prompt
      for that task class, not paraphrase

  Scenario: ESCALATE is a first-class good outcome
    Given an event whose certified contract is ESCALATE
    Then the target verdict renders in the positive style, not the warning style
    And its label reads as the right call, not as a fault

  Scenario: the brain is named honestly
    Then the panel presents Wave Nano as the trained gateway-class brain
    And Roger Edge as the MCU-class line that is in design with no trained artifact
    And no copy calls Wave Nano "an in-development slot"

  Scenario: captured replays are labelled as recordings
    Then the captured sample block says it was recorded from a real checkpoint
    And no numeric benchmark scores appear on the page
