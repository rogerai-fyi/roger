# TUNE IN composer parity — founder capture 2026-07-29.

Feature: TUNE IN prompt wrapping and vertical fit
  The basic chat composer must preserve authored text with the same fit guarantees as AGENT.

  Scenario: A long TUNE IN prompt wraps instead of scrolling horizontally
    Given I am typing in a TUNE IN channel
    When the terminal is 80 columns by 18 rows
    And I enter a prompt long enough to wrap twice
    Then the beginning and end of the prompt are both visible
    And no rendered line exceeds 80 columns
    And the complete TUNE IN frame fits within 18 rows

  Scenario: Growing from one visual row to two keeps the first row visible
    Given I am typing in a TUNE IN channel
    When I rapidly type enough text to fill one TUNE IN row
    And the next input chunk wraps onto a second TUNE IN row
    Then both the beginning and continuation of the TUNE IN prompt remain visible

  Scenario: Empty TUNE IN keeps the existing one-row placeholder
    Given I am typing in a TUNE IN channel
    When the terminal is 80 columns by 18 rows
    Then the TUNE IN composer occupies one row

  Scenario: Smart select-and-copy works by default
    Given I launch the RogerAI TUI
    Then terminal mouse reporting is enabled
    And releasing selected transcript text copies it to the clipboard
    And a counted copy notification is visible
    And keyboard transcript scrolling remains available

  Scenario: Native terminal selection remains one toggle away
    Given I launch the RogerAI TUI
    When I run "/mouse"
    Then terminal mouse reporting is disabled
    And ordinary click-drag selection belongs to the terminal
    And running "/mouse" again restores smart selection and wheel scrolling

  # ctrl+o moved to the tool-machinery fold (founder 2026-08-20). Mouse mode is a
  # thing you set once a session, not one you reach for mid-turn, so it keeps the
  # command and gives up the key. The verb is not lost - only its shortcut moved.
  Scenario: Mouse mode can be changed from AGENT with the command
    Given I am typing in AGENT mode
    When I run the AGENT command "/mouse"
    Then terminal mouse reporting is disabled
    When I run the AGENT command "/mouse"
    Then terminal mouse reporting is enabled

  Scenario: Native selection is not erased by idle discovery repaint
    Given I launch the RogerAI TUI
    And native selection owns the mouse
    Then idle ticks do not poll discovery and repaint the screen

  Scenario: TUNE IN does not blink-repaint over native selection
    Given I am typing in a TUNE IN channel
    And native selection owns the mouse
    Then the TUNE IN cursor is steady and emits no recurring blink repaint
