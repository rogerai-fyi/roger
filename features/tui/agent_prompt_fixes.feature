# TUI Agent prompt & separation fixes
# Red-to-green for the issues raised 2026-07-28:
# - cannot copy/paste reliably in the agent window
# - transcript and prompt not properly separated (opencode-style hairline + focus)
# - y/N confirm hard to notice
# - static "ask the agent to do something" placeholder instead of context-reactive hint
# - prompt does not wrap; long input disappears off the right edge

Feature: Agent prompt input and visual separation
  As an operator in AGENT mode
  I want the ask prompt to be obviously waiting for me, to wrap cleanly, to show context-aware hints,
  and to make a pending y/N confirm impossible to miss, while the transcript stays clearly separated.

  Background:
    Given I have entered AGENT mode with a tools-capable band tuned in

  Scenario: Prompt input wraps at terminal width
    When the terminal is 80 columns wide
    And I type a 120-character prompt
    Then the input line wraps within the 80-col width
    And no characters are lost off the right edge
    And the same holds under NO_COLOR and when piped

  Scenario: Wrapped input never disappears behind landing desk chrome
    Given detected guests "opencode", "hermes" and "aider"
    When the terminal is 80 columns wide
    And I type a 120-character prompt
    Then the input line wraps within the 80-col width
    And no characters are lost off the right edge
    And the decorative landing desk collapses while I am typing

  Scenario: Growing from one visual row to two keeps the first row visible
    When I rapidly type enough text to fill one AGENT row
    And the next input chunk wraps onto a second AGENT row
    Then both the beginning and continuation of the AGENT prompt remain visible

  Scenario: Placeholder is context-reactive when idle
    Given no turn is in flight and no confirm is pending
    Then the placeholder reads "ask the agent to do something"

  Scenario: Placeholder changes when last answer contained a tool result
    Given the previous turn ended with a tool result
    Then the placeholder reads something like "run the next step" or "continue"

  Scenario: Placeholder suggests validation after a file change
    Given the previous turn successfully wrote a file
    Then the placeholder suggests running tests or reviewing the change

  Scenario: Placeholder suggests recovery after failure
    Given the previous turn ended with an error
    Then the placeholder suggests retrying or fixing the error

  Scenario: Placeholder follows up when the agent asked a question
    Given the previous answer ended with a question
    Then the placeholder suggests answering the agent's question

  Scenario: Placeholder shows y/N instruction while a confirm is pending
    Given a mutating tool is waiting for approval
    Then the placeholder (or the prompt line itself) shows "[y/N]  deny=default"
    And a red accent bar or lamp makes the confirm impossible to miss

  Scenario: Native paste works in the agent input
    When bracketed paste is armed
    And I paste a multi-line command into the ask prompt
    Then the full text appears in the input without corruption
    And the prompt still wraps correctly

  Scenario: Arrow keys edit a multiline draft before recalling history
    Given the ask prompt contains three logical lines
    And the cursor is on the last line
    When I press up
    Then the cursor moves to the previous prompt line
    And the multiline draft is not replaced by history
    When the cursor reaches the first visual line and I press up
    Then the previous sent prompt is recalled

  Scenario: Multiline prompt history round-trips as one private entry
    When I send a three-line AGENT prompt
    Then reloading AGENT history returns the exact three-line prompt as one entry

  Scenario: Transcript copy still works while input has focus
    When the transcript has content
    And I press ctrl+y
    Then the agent transcript is copied to the clipboard
    And the "✓ Copied to clipboard" toast appears
    And focus remains on the input

  Scenario: Opencode-style separation hairline appears when transcript has content
    Given the transcript has at least one line
    Then a dim hairline seam "──" is shown above the prompt
    And when the user has scrolled up or transcript focus is active the seam is lit and labeled

  Scenario: Confirm gate renders the y/N directly in the prompt row
    Given a run_shell confirm is pending
    Then the prompt row shows the full command (soft-wrapped) plus "[y/N]  deny=default"
    And the row uses the red accent so it stands out from normal transcript lines

  Scenario: All states remain safe under narrow width and NO_COLOR
    When the terminal is 40 columns wide
    And NO_COLOR is active
    Then every prompt, seam, confirm, and placeholder still renders without overflow or color leakage
    And the input still wraps

  Scenario: Wide AGENT view has a truthful right-aligned session rail
    Given the session has billed 1200 input tokens, 340 output tokens, and spent "$0.05"
    And the current turn is on step 3 of 8
    When the terminal is 120 columns wide
    Then the deck shows "SESSION", "STEPS 3/8", and "SPENT $0.05"
    And each session fact appears only once in the deck

  Scenario: Medium and narrow layouts degrade without overflow
    Given the session has billed 1200 input tokens, 340 output tokens, and spent "$0.05"
    And the current turn is on step 3 of 8
    When the terminal is 90 columns wide
    Then the session facts remain in the deck without a detached right rail
    When the terminal is 40 columns wide
    Then the compact session reading fits without overflow

  Scenario: The global footer is the only idle key-help row
    Given no turn is in flight and no confirm is pending
    Then the AGENT body has no duplicated idle command tutorial
    And the global AGENT footer teaches ask, copy, permissions, transcript focus, and exit

  Scenario: Agent calls are unlimited by default
    Given no AGENT timeout is configured
    When a model call has been working for 301 seconds
    Then the working rail reads "unlimited"
    And no cap warning or automatic stop is armed

  Scenario: A configured timeout remains an explicit safety option
    Given the AGENT timeout is configured as "10m"
    When a model call has been working for 601 seconds
    Then the working rail shows the configured 600s cap
    And the slow-call extension and stop controls are offered

  Scenario: Tool activity is one compact stateful card
    When the agent calls read_file for "internal/tui/agent.go"
    Then one running activity card is shown
    When that tool succeeds with 16 bytes
    Then the same card becomes a green success card with its target and size
    And full output remains behind the "d" toggle
