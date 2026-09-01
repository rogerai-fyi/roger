# Re-entering SHARE keeps the table. The scan happens BEHIND it.
#
# Founder, 2026-09-01: "if i go to the share page, and then to another page, then back to
# the share page: it's again cleared and i have to wait for it to render again. can't we
# just do it in the background and then modify the list based on the changes/diff rather
# than abruptly clearing it."
#
# Every [2] re-entry set the loading pose, which swaps the WHOLE table for "Clearing the
# static… / scanning the band…" - even though the rows from the previous scan are still
# right there in the shared controller. The operator's mental model is a page they are
# returning to; the TUI treated it as a page that never existed. The loud scan is only
# honest on the FIRST open, when there is genuinely nothing to show.

Feature: Returning to SHARE shows the last scan at once and refreshes behind it
  As an operator hopping between SHARE and the other screens
  I want the table I just saw to still be there when I come back
  So that checking my stations never costs a rescan's wait.

  Background:
    Given a share table was already scanned with models on it

  Scenario: The first open is a loud scan
    Given no scan has happened yet this session
    When I open SHARE
    Then the scanning pose is shown
    And a detection is started

  Scenario: Re-entry shows the table immediately
    When I leave SHARE and come back
    Then the table renders at once with the previous rows
    And the scanning pose is not shown

  Scenario: Re-entry still refreshes, quietly
    When I leave SHARE and come back
    Then a detection is started in the background
    And the header says it is refreshing

  Scenario: The refresh folds changes in rather than resetting the view
    Given the cursor is on a specific model
    When a background refresh lands with an extra model
    Then the new model appears in the table
    And the cursor is still on the model it was on

  Scenario: On-air stations stay on air across a refresh
    Given a model is ON-AIR
    When a background refresh lands
    Then that model still shows ON-AIR

  Scenario: A refresh that finds nothing keeps the table
    When a background refresh lands with no servers found
    Then the previous rows are still shown
    And a quiet note says the re-scan found nothing
    And the operator is not dropped into the setup wizard

  Scenario: A late refresh cannot yank the operator back to SHARE
    When I leave SHARE before the background refresh lands
    Then the refresh result is folded in silently
    And the screen I am on does not change

  Scenario: The manual re-scan keeps the table too
    Given the table is showing
    When I press r
    Then the rows stay visible while the re-scan runs
