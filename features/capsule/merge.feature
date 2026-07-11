Feature: append-only capsule merge
  A returning capsule may only ADD turns; a handoff never erases context. Merge verifies
  the incoming signature FIRST, rejects a forked turn or an unknown version or tool_calls,
  then appends only the turns at/after the target's watermark that are not already present.

  Background:
    Given a fresh operator keypair

  Scenario: merging a full capsule into an empty thread reproduces the transcript
    Given a signed capsule with watermark 3 and turns
      | turn | role      | content |
      | 0    | user      | hi      |
      | 1    | assistant | yo      |
      | 2    | user      | more    |
    When I merge it into an empty thread
    Then the merge succeeds
    And the merged thread has turns "0,1,2"
    And the merged watermark is 3
    And the merged signature is cleared

  Scenario: a second merge of the same capsule is idempotent
    Given a signed capsule with watermark 2 and turns
      | turn | role      | content |
      | 0    | user      | hi      |
      | 1    | assistant | yo      |
    When I merge it into an empty thread
    And I merge it again into the result
    Then the merge succeeds
    And the merged thread has turns "0,1"

  Scenario: a smaller incoming capsule never truncates the target
    Given a signed capsule with watermark 3 and turns
      | turn | role      | content |
      | 0    | user      | hi      |
      | 1    | assistant | yo      |
      | 2    | user      | more    |
    When I merge it into an empty thread
    And a signed capsule with watermark 1 and turns
      | turn | role | content |
      | 0    | user | hi      |
    And I merge it into the result
    Then the merge succeeds
    And the merged thread has turns "0,1,2"

  Scenario: an unverified capsule is rejected and the target is unchanged
    Given the target thread has turns "0" at watermark 1
    And a signed capsule with watermark 2 and turns
      | turn | role      | content |
      | 0    | user      | hi      |
      | 1    | assistant | yo      |
    And the incoming capsule is tampered after signing
    When I merge it into the target
    Then the merge is rejected as unverified
    And the merged thread has turns "0"

  Scenario: a forked turn rejects the whole capsule and the target is unchanged
    Given the target thread has turns "0,1" at watermark 2
    And a signed capsule with watermark 2 and turns
      | turn | role      | content |
      | 0    | user      | hi      |
      | 1    | assistant | FORKED  |
      | 2    | user      | new     |
    When I merge it into the target
    Then the merge is rejected as forked
    And the merged thread has turns "0,1"

  Scenario: an unknown capsule version is rejected
    Given a signed capsule with watermark 1 and turns
      | turn | role | content |
      | 0    | user | hi      |
    And the incoming capsule version is changed and re-signed to "roger.context.v2"
    When I merge it into an empty thread
    Then the merge is rejected as unknown-version
