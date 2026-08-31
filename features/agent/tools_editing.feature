# The agent's EDITING tools: a surgical edit, and a file it can actually finish reading.
#
# Today the agent can only WRITE A WHOLE FILE. To change one line it must reproduce the
# entire file from context, which is expensive on every turn and silently destructive on a
# long one: anything it fails to reproduce is simply gone, and nothing in the loop can tell
# a deliberate deletion from a dropped paragraph.
#
# read_file compounds it. It reads the whole file and clips at maxToolOutput (16 KiB), with
# no way to ask for the rest - so a file larger than that cannot be read at all, and the
# agent is asked to rewrite from a copy it has only partly seen.

Feature: The agent edits files surgically, and can read a file of any size
  As an operator watching an agent work in my repository
  I want it to change the lines it means to change
  So that a long file is never quietly rewritten from an incomplete copy.

  Background:
    Given a working directory with files the agent may edit

  # --- edit_file: exactness is the whole point -------------------------------

  Scenario: An edit replaces exactly the text it matched
    Given a file containing "alpha", "beta" and "gamma" on separate lines
    When the agent edits it, replacing "beta" with "delta"
    Then the file reads "alpha", "delta", "gamma"
    And every other byte is unchanged

  Scenario: An edit that matches nothing fails instead of doing nothing
    Given a file that does not contain "nonexistent"
    When the agent edits it, replacing "nonexistent" with "x"
    Then the edit fails
    And the error says the text was not found
    And the file is unchanged

  Scenario: An ambiguous edit fails rather than guessing
    Given a file containing "dup" three times
    When the agent edits it, replacing "dup" with "one"
    Then the edit fails
    And the error says how many matches were found
    And the file is unchanged

  Scenario: An ambiguous edit succeeds when the agent says it means all of them
    Given a file containing "dup" three times
    When the agent edits it, replacing "dup" with "one", replacing all
    Then all three are replaced
    And the error is not raised

  Scenario: Replacing text with the empty string deletes it
    Given a file containing a line the agent wants gone
    When the agent replaces that line's text with nothing
    Then the line's text is gone
    And the surrounding lines are untouched

  Scenario: An edit that would change nothing is refused
    When the agent edits a file replacing text with the identical text
    Then the edit fails
    And the error says the replacement is identical to the match

  Scenario: A multi-line match is replaced as one unit
    Given a file containing a three-line block
    When the agent replaces that whole block with a single line
    Then the block is gone and the single line is in its place

  # --- edit_file: it is a side effect, and it stays inside the root ------------

  Scenario: An edit asks the operator first
    Then edit_file is a mutating tool
    And it is gated exactly as write_file is

  Scenario: An edit cannot reach outside the working directory
    When the agent edits a path that escapes the working directory
    Then the edit fails
    And nothing outside the working directory is touched

  Scenario: An edit will not create a file
    When the agent edits a file that does not exist
    Then the edit fails
    And the error points at write_file for creating one

  Scenario: An edit refuses a file it cannot read as text
    When the agent edits a binary file
    Then the edit fails rather than corrupting it

  # --- read_file: reaching the whole file ------------------------------------

  Scenario: A small file still reads whole, exactly as before
    Given a file well under the output cap
    When the agent reads it with no range
    Then it gets the entire file

  Scenario: A file over the cap says so, and says how to get the rest
    Given a file larger than the tool output cap
    When the agent reads it with no range
    Then the result is truncated
    And it names the range to ask for next

  Scenario: A range reads exactly the lines asked for
    Given a file of 500 lines
    When the agent reads it from line 100 for 50 lines
    Then it gets lines 100 to 149
    And the result says which lines these are

  Scenario: A range that runs past the end returns what exists
    Given a file of 10 lines
    When the agent reads it from line 5 for 100 lines
    Then it gets lines 5 to 10
    And the result does not claim there is more

  Scenario: A range starting past the end is an error, not silence
    Given a file of 10 lines
    When the agent reads it from line 999
    Then the read fails
    And the error says how many lines the file has

  Scenario Outline: A nonsensical range is refused
    When the agent reads a file with offset <offset> and limit <limit>
    Then the read fails with a message naming the bad argument

    Examples:
      | offset | limit |
      | 0      | 10    |
      | -5     | 10    |
      | 1      | 0     |
      | 1      | -1    |
