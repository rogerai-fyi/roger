# ask_operator: the agent puts a QUESTION to the person watching.
#
# The only channel to the operator today is the mutating-tool y/N gate, which can express
# exactly one thing: may I run this. An agent that has reached a genuine fork - two
# reasonable designs, an ambiguous instruction, a destructive step worth naming out loud -
# has no way to ask. It must guess and hope, or stop and hand the turn back.
#
# It is deliberately NOT the confirm gate wearing a hat. The confirm gate is a permission,
# so a permissive session (`/perms all`, `--yolo`) auto-approves it and that is correct: the
# operator said run without asking. A QUESTION is not a permission, and auto-answering one
# would be answering on the operator's behalf - so no permission mode may resolve it.
#
# It inherits, in full, the cancellation rules the confirm gate was just given: a stopped
# turn must never raise one, and one already on screen is never withdrawn behind the
# operator's back.

Feature: The agent can ask the operator a question
  As an operator
  I want the agent to ask me when it reaches a real fork
  So that it stops guessing at decisions that are mine to make.

  Background:
    Given I have entered AGENT mode with a band tuned in

  # --- the ordinary path -----------------------------------------------------

  Scenario: A question reaches the operator and its answer reaches the agent
    When the agent asks a question mid-turn
    Then the question is shown
    And my answer comes back as the tool's result
    And the turn continues from there

  Scenario: A question with options offers them
    When the agent asks a question with three options
    Then all three are shown
    And picking one returns that option as the result

  Scenario: A free-text answer is returned verbatim
    When the agent asks an open question
    And I type an answer
    Then the answer reaches the agent exactly as I typed it

  Scenario: Answering keeps the stream alive
    When I answer a question
    Then the drain is still armed
    And the turn reaches its end normally

  # --- what makes it a question and not a permission -------------------------

  Scenario: A question is not a mutating tool
    Then ask_operator is not mutating
    And it does not pass through the tool-approval gate

  Scenario Outline: No permission mode answers a question for me
    Given the approval mode is <mode>
    When the agent asks a question
    Then it is still shown to me
    And nothing answers it automatically

    Examples:
      | mode    |
      | confirm |
      | edits   |
      | all     |

  # --- the cancellation rules, inherited whole -------------------------------

  Scenario: A stopped turn never raises a question
    Given a turn has been force-stopped
    When its goroutine reaches an ask
    Then no question is shown
    And the agent is told the turn is over

  Scenario: A question already on screen is never withdrawn
    Given a question is on screen
    When the turn behind it is cancelled
    Then the question stays
    And my answer is still delivered to whoever asked

  Scenario: A question cannot outlive its turn on screen
    Given a question was shown for a turn that has since ended
    Then the prompt does not sit there swallowing keys

  Scenario: A pending question does not strand the drain
    Given a question is outstanding
    When the turn's goroutine exits
    Then the drain returns promptly rather than parking

  # --- the surfaces it has to reach ------------------------------------------

  Scenario: A question is mirrored to an attached BASE STATION
    Given a remote surface is attached
    When the agent asks a question
    Then the question is mirrored to it
    And an answer from either surface resolves it once

  Scenario: A stale remote answer cannot resolve a later question
    Given a question was answered and a second one is now open
    When a late answer for the FIRST arrives from a remote surface
    Then it is dropped
    And the second question is still waiting

  # --- keeping it honest -----------------------------------------------------

  Scenario: An empty question is refused
    When the agent asks with no question text
    Then the tool call fails
    And nothing is shown to the operator

  Scenario: A question does not count as a tool the turn may repeat forever
    When the agent asks the same question twice in one turn
    Then the repeat guard treats it like any other repeated call
