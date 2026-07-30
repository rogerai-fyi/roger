Feature: Resume durable local Roger AGENT sessions
  Roger preserves completed AGENT conversations locally so work can continue after the
  process exits. A session is private to the local OS user and is distinct from a live
  BASE STATION remote-control session.

  Background:
    Given Roger's local session directory belongs to the current OS user

  Scenario: A completed AGENT turn becomes resumable
    Given I start a new Roger AGENT session in a working directory
    When a user prompt and its assistant answer complete
    Then Roger atomically saves the session under a stable opaque session ID
    And the saved session records its working directory, created time, updated time, title, model, and completed conversation
    And the session file is readable and writable only by the current OS user
    And no broker token, API key, environment secret, or approval credential is saved

  Scenario: An interrupted turn is never restored as completed context
    Given an AGENT turn is streaming or running a tool
    When Roger is interrupted before the assistant answer completes
    Then the unfinished user turn, partial answer, and in-flight tool state are not committed
    And the last atomically saved completed conversation remains readable

  Scenario: The picker opens for a bare resume command
    Given multiple saved sessions exist
    When I run "roger resume" in an interactive terminal
    Then a full-screen "Resume a previous session" picker opens
    And each row shows a human time, a clipped title, and enough of the opaque ID to disambiguate it
    And the initially selected row is the most recently updated matching session
    And the picker initially filters sessions to the current working directory

  Scenario: Picker filtering can include every working directory
    Given saved sessions exist in the current working directory and elsewhere
    When I toggle the picker filter from "Cwd" to "All"
    Then sessions from every working directory are shown
    And each non-current row identifies its working directory without exposing more path than fits safely on screen

  Scenario: Picker search is incremental and case-insensitive
    Given the resume picker is open
    When I type search text
    Then rows are filtered case-insensitively by title, session ID, and working directory
    And backspace updates the results immediately
    And an empty result explains how to clear the search or include all directories

  Scenario: Picker sort switches between updated and created time
    Given the resume picker is open
    When I toggle sort between "Updated" and "Created"
    Then rows are ordered newest first by the selected timestamp
    And ties are ordered deterministically by session ID

  Scenario Outline: Picker keys are predictable
    Given the resume picker is open
    When I press "<key>"
    Then "<result>"

    Examples:
      | key        | result                                      |
      | up         | the selection moves to the previous row     |
      | down       | the selection moves to the next row         |
      | k          | the selection moves to the previous row     |
      | j          | the selection moves to the next row         |
      | home       | the first row is selected                    |
      | end        | the last row is selected                     |
      | tab        | the Cwd or All filter toggles                |
      | ctrl+s     | the Updated or Created sort toggles          |
      | enter      | the selected session resumes                 |
      | escape     | the picker exits without changing a session  |
      | ctrl+c     | the picker exits without changing a session  |

  Scenario: A known full session ID resumes without the picker
    Given a saved session has ID "th_example123"
    When I run "roger resume th_example123"
    Then Roger loads that exact session without opening the picker
    And Roger opens the AGENT surface at the restored transcript

  Scenario: An unambiguous session ID prefix resumes directly
    Given exactly one saved session ID begins with "th_exam"
    When I run "roger resume th_exam"
    Then Roger resumes the matching session without opening the picker

  Scenario: An ambiguous session ID prefix is rejected
    Given more than one saved session ID begins with "th_exam"
    When I run "roger resume th_exam"
    Then Roger returns an error that lists only the matching safe IDs and titles
    And no session is modified

  Scenario: An unknown session ID is rejected
    When I run "roger resume th_missing"
    Then Roger returns a not-found error with a hint to run "roger resume"
    And a new session is not silently created

  Scenario: Direct resume works without an interactive terminal
    Given a saved session has a known ID
    When "roger resume <id>" runs with piped input or output
    Then session lookup does not require the interactive picker
    And the resumed TUI still reports the normal terminal requirement if it cannot launch

  Scenario: Bare resume degrades safely without an interactive terminal
    When "roger resume" runs without an interactive terminal
    Then Roger prints a stable newest-first session list instead of emitting terminal control sequences
    And it explains how to use "roger resume <id>"

  Scenario: Resuming restores model context and visible conversation
    Given a saved session contains completed user and assistant turns
    When that session resumes
    Then its completed turns are restored into the embedded model conversation in original order
    And its visible AGENT transcript is reconstructed from durable semantic turns
    And the next prompt includes the restored conversation as context
    And tool calls are displayed as historical events but are never executed again
    And confirmation decisions and in-flight runtime state are not restored

  Scenario: Resume is robust when the prior model is unavailable
    Given a saved session names a model that is no longer available
    When that session resumes
    Then its transcript and context still load
    And Roger clearly asks the user to tune or select an available model before the next turn

  Scenario: Resume preserves the original working-directory boundary
    Given a session was saved in a working directory that still exists
    When the session resumes from another directory
    Then Roger uses the saved working directory as the AGENT tool root
    And the masthead identifies that restored working directory

  Scenario: A missing prior working directory does not widen tool access
    Given a session's saved working directory no longer exists
    When the session resumes
    Then Roger loads the transcript read-only
    And tools remain unavailable until the user explicitly chooses an existing working directory
    And Roger never silently substitutes the caller's broader directory

  Scenario: Corrupt and incompatible session files do not break the picker
    Given the session directory contains valid sessions, a corrupt file, and an unsupported future schema
    When I run "roger resume"
    Then valid sessions remain selectable
    And invalid entries are skipped with a concise warning
    And the invalid bytes are not overwritten

  Scenario: No saved sessions is a calm empty state
    Given no valid saved sessions exist
    When I run "roger resume"
    Then Roger says there are no saved sessions
    And it explains that completing an AGENT turn creates one
    And it exits successfully

  Scenario: Clearing a resumed AGENT starts a fresh durable session
    Given I resumed an existing session
    When I run the AGENT "/clear" command
    Then the existing saved session remains available as history
    And subsequent completed turns are saved under a new session ID
    And cleared conversation is not supplied to the new model context

  Scenario: Concurrent Roger processes cannot corrupt one session
    Given two Roger processes attempt to save the same resumed session
    When their writes overlap
    Then every visible session file is a complete valid snapshot
    And the snapshot with the later updated time wins
    And no temporary file is presented in the picker

  Scenario: Resume aliases and help are discoverable
    When I run "roger help"
    Then help includes "roger resume [session-id]"
    And "roger continue [session-id]" is documented as an alias
