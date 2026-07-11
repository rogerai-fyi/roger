Feature: capsule security invariants
  The owner ed25519 signature covers every field except sig (over the canonical bytes, so
  it also covers redaction, and the tool_calls). Verify-before-merge, append-only, and
  summary-only redaction for strangers are enforced. tool_calls now interoperate: their
  canonical form is pinned cross-language, so a VERIFIED tool-call capsule crosses (an
  unverified one is still rejected, the safe state).

  Background:
    Given a fresh operator keypair

  Scenario: a stranger cannot escalate a summary-only capsule to full
    Given a signed summary-only capsule
    Then the capsule redaction is "summary"
    When I flip the redaction to "full" without the key
    Then the capsule no longer verifies

  Scenario Outline: any tampered field breaks verification
    Given a signed summary-only capsule
    When I tamper the field "<field>"
    Then the capsule no longer verifies

    Examples:
      | field       |
      | content     |
      | role        |
      | title       |
      | summary     |
      | created_at  |
      | exported_by |
      | watermark   |

  Scenario: a verified capsule carrying tool_calls now imports (gate lifted)
    Given a signed capsule carrying tool_calls
    When I import it
    Then the import succeeds (the gate is lifted)

  Scenario: exporting a draft that carries tool_calls now succeeds (gate lifted)
    Given a draft carrying tool_calls
    When I export it
    Then the export succeeds (the gate is lifted)

  Scenario: an importable capsule round-trips through marshal and verifies
    Given a signed capsule with watermark 1 and turns
      | turn | role | content |
      | 0    | user | hi      |
    When I marshal and import it
    Then the import succeeds
    And the imported capsule verifies
