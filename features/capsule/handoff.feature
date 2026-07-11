Feature: carry the conversation through an operator handoff
  The TUI records each completed turn into a minimal per-turn ring (ruling Q4). On a
  same-owner/local operator handoff it EXPORTS the conversation as a signed capsule the
  guest can import, and on recall MERGES the guest's return capsule back append-only. The
  flat transcript stays the render source; the stranger encrypted transport is a follow-on.

  Scenario: completed turns accumulate in the ring in order
    Given a fresh session
    When the user says "hello" and the station replies "hi there"
    Then the ring has 2 turns
    And the ring turn indexes are "0,1"

  Scenario: an exported capsule is signed by the operator and carries the transcript
    Given a fresh session
    And the user says "hello" and the station replies "hi there"
    When I export the context capsule
    Then the exported capsule verifies
    And the exported capsule has 2 turns
    And the exported capsule owner is the operator key

  Scenario: a guest's return capsule merges back append-only
    Given a fresh session
    And the user says "hello" and the station replies "hi there"
    And I write the handoff capsule to the guest workdir
    When the guest appends a turn and leaves a return capsule
    And the DJ reads the return capsule
    Then the ring has 3 turns
    And the ring turn indexes are "0,1,2"

  Scenario: a stranger export is summary-only (no full transcript)
    Given a fresh session
    And the user says "secret one" and the station replies "secret two"
    When I export the context capsule for a stranger
    Then the exported capsule redaction is "summary"
    And the exported capsule has 1 turns

  Scenario: no return capsule is a clean no-op
    Given a fresh session
    And the user says "hello" and the station replies "hi there"
    When the DJ reads the return capsule from an empty workdir
    Then the recall added 0 turns
