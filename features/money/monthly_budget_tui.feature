# The MONTHLY BUDGET is editable where it is displayed.
#
# Founder, 2026-09-01: "is there a way to modify the monthly budget from the tui? i don't
# see how." There was not. The spend-limits screen - the EDITOR, where every per-band
# limit is changed - showed the one account-wide money control as a read-only row, and
# the only hint of the CLI escape hatch (`set: roger limit --monthly $X`) is dropped on
# terminals under 92 columns to keep the row from wrapping. On an ordinary 80-column
# terminal the row said "no cap" with no affordance at all: a limit you cannot discover
# how to set is a limit that does not exist.
#
# The backend needs nothing: client.SetMonthlyLimit / GetMonthlyLimit exist and the CLI
# already uses them. This wires the row into the editor around it.

Feature: The monthly budget is set, changed and cleared from the spend-limits screen
  As an operator on the spend-limits screen
  I want to edit the monthly budget where I can see it
  So that capping my spend does not require leaving the TUI to find a CLI flag.

  Background:
    Given I am logged in on the spend-limits screen

  # --- editing -----------------------------------------------------------------

  Scenario: The budget row says how to edit it, at every width
    Then the monthly budget row names the key that edits it
    And it does so on an 80-column terminal too

  Scenario: Setting a cap
    Given no monthly cap is set
    When I edit the monthly budget and enter "25"
    Then the broker is asked to set a $25.00 monthly cap
    And the row shows the new cap without waiting for the next balance poll

  Scenario: Changing a cap starts from the current value
    Given a monthly cap of $25.00
    When I edit the monthly budget
    Then the editor is prefilled with "25"

  Scenario: Clearing a cap
    Given a monthly cap of $25.00
    When I edit the monthly budget and enter "0"
    Then the broker is asked to clear the cap
    And the row shows "no cap" again

  Scenario: "off" clears it too, matching the CLI's spellings
    Given a monthly cap of $25.00
    When I edit the monthly budget and enter "off"
    Then the broker is asked to clear the cap

  Scenario: esc abandons the edit
    Given a monthly cap of $25.00
    When I edit the monthly budget and press esc
    Then the cap is unchanged
    And the broker is not called

  # --- refusals ----------------------------------------------------------------

  Scenario Outline: A value that is not a dollar amount is refused in place
    When I edit the monthly budget and enter "<value>"
    Then the edit is refused with a message
    And the broker is not called

    Examples:
      | value  |
      | abc    |
      | -5     |
      | $      |

  Scenario: A broker failure keeps the old cap and says so
    Given a monthly cap of $25.00
    And the broker will refuse the change
    When I edit the monthly budget and enter "50"
    Then the row still shows $25.00
    And the failure is shown to the operator

  Scenario: Logged out, the row still points at logging in
    Given I am not logged in
    Then the monthly budget row says to log in
    And it cannot be edited

  # --- the row's own honesty (the discoverability bug, pinned) ------------------

  Scenario: The narrow row never hides the only way to edit
    Given the terminal is 80 columns wide
    Then the monthly budget row still shows an edit affordance
    And nothing on the row wraps
