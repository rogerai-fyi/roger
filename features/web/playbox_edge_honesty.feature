# Corrections + the framing principle from the models agent's 2026-08-01 answer
# (docs-internal/ANSWER-FROM-MODELS-AGENT-playbox-nano.md):
#   - Wave models are CONTRACT models: the device prompt is part of the device -
#     unframed they floor, framed they perform. Model + prompt ship as one unit.
#   - ESCALATE is the models' strongest measured skill: it renders as a GOOD
#     outcome, never as a warning state.
#   - Naming truth: Wave Nano is the trained gateway-class brain.
#   - NAMING CORRECTED 2026-09-12 (founder ruling): ROGER EDGE IS THE LAYER - the
#     owner's devices discovered and enrolled into one fleet, from a board to a
#     Jetson to a Mac. The MCU classifier line is ONE PRODUCT INSIDE that layer,
#     and it still has no trained artifact. The earlier wording here named the
#     layer when it meant the line. Nothing about the artifact honesty changes:
#     there is still no trained MCU classifier, and this spec still says so.
#     (The 2026-08-17 edge correction in web/test/playbox.test.mjs already read
#     "Roger Edge is the sensing/glue layer, not a Wave tier" - this finishes
#     that move rather than starting a new one.)
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
    And Roger Edge as the device layer, whose MCU classifier line is in design with no trained artifact
    And no copy calls Wave Nano "an in-development slot"

  Scenario: captured replays are labelled as recordings
    Then the captured sample block says it was recorded from a real checkpoint
    And no numeric benchmark scores appear on the page
