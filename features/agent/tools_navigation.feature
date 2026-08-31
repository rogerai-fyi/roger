# The agent's NAVIGATION tools: finding text, and finding files.
#
# Neither exists today, so the agent reaches for run_shell - which is Mutating and
# confirm-gated. Searching a repository, the most ordinary read there is, therefore raises a
# y/N prompt every time, and the operator learns to approve shell commands by reflex. That
# is worse than the friction: it trains away the attention the gate exists to collect.
#
# read_file, list_dir, web_search and delegate all run automatically because a read changes
# nothing. grep and glob are reads. They follow the same rule.

Feature: The agent finds things without asking permission to look
  As an operator
  I want searching the tree to be as free as reading a file
  So that the y/N gate keeps meaning "this will change something".

  Background:
    Given a working directory with a mix of files

  # --- grep ------------------------------------------------------------------

  Scenario: A search returns the matching lines with their locations
    Given "needle" appears in two files
    When the agent searches for "needle"
    Then it gets both matches
    And each names its file and line number

  Scenario: A search that matches nothing says so plainly
    When the agent searches for text that appears nowhere
    Then the result says there were no matches
    And it is not an error

  Scenario: A search is a read and runs without a prompt
    Then grep is not a mutating tool
    And it does not raise the confirm gate

  Scenario: A search can be narrowed to a subtree
    Given "needle" appears both inside and outside a subdirectory
    When the agent searches for "needle" under that subdirectory
    Then only the matches inside it are returned

  Scenario: A search can be narrowed to a file kind
    Given "needle" appears in a .go file and a .md file
    When the agent searches for "needle" in "*.go"
    Then only the .go match is returned

  Scenario: A search takes a regular expression
    Given a file containing "version 6.3.3"
    When the agent searches for "version [0-9]+\.[0-9]+\.[0-9]+"
    Then the line is found

  Scenario: An invalid regular expression is reported, not swallowed
    When the agent searches for an unparseable pattern
    Then the search fails
    And the error names the pattern problem

  Scenario: A search cannot read outside the working directory
    When the agent searches under a path that escapes the working directory
    Then the search fails
    And nothing outside the working directory is read

  Scenario: A search skips the noise nobody means to search
    Given a repository containing .git and node_modules
    When the agent searches for text that appears in both
    Then neither directory is searched

  Scenario: A search does not try to match inside binaries
    Given a binary file containing the byte sequence
    When the agent searches for that sequence
    Then the binary is skipped rather than dumped into the transcript

  Scenario: A very large result set is capped, and says it was
    Given a pattern matching thousands of lines
    When the agent searches for it
    Then the result is capped
    And it says the result was truncated

  # --- glob ------------------------------------------------------------------

  Scenario: A glob returns the paths that match
    Given .go files at several depths
    When the agent globs "**/*.go"
    Then every one is returned, relative to the working directory

  Scenario: A glob is a read and runs without a prompt
    Then glob is not a mutating tool
    And it does not raise the confirm gate

  Scenario: A glob that matches nothing says so plainly
    When the agent globs a pattern matching nothing
    Then the result says there were no matches
    And it is not an error

  Scenario: A glob returns the most recently modified first
    Given files modified at different times
    When the agent globs them
    Then the most recently modified comes first

  Scenario: A glob cannot escape the working directory
    When the agent globs a pattern that reaches outside the working directory
    Then the glob fails
    And nothing outside the working directory is listed

  Scenario: A glob skips the same noise a search does
    Given a repository containing .git and node_modules
    When the agent globs for files that exist in both
    Then neither directory is walked
