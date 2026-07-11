Feature: the `roger context` CLI
  export signs a draft into a portable .rcap.json with the operator's own key; import
  verifies it (and can append-only merge it into a base thread). This is the file interop
  surface for hermes/opencode and a same-owner/local handoff.

  Scenario: export then import round-trips and verifies
    Given a draft with turns "0,1"
    When I export it to a capsule file
    And I import the capsule file
    Then the import reports 2 turns
    And the import reports it verified

  Scenario: a tampered capsule file is rejected on import
    Given a draft with turns "0,1"
    And I export it to a capsule file
    When the capsule file is tampered on disk
    And I import the capsule file
    Then the import fails

  Scenario: import --into merges a guest capsule append-only
    Given a draft with turns "0,1"
    And I export it to a capsule file as the base thread
    And a guest draft with turns "0,1,2"
    And I export the guest draft to a capsule file
    When I import the guest capsule into the base thread
    Then the merged capsule has 3 turns
    And the merged capsule verifies

  Scenario: export signs a draft carrying tool_calls (gate lifted)
    Given a draft carrying tool_calls
    When I export it to a capsule file
    Then the export succeeds
    And I import the capsule file
    Then the import reports it verified
