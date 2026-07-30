Feature: The Roger CLI presents one clean release surface
  Roger is the canonical executable and every entry path should preserve the same
  integrations, help contract, and concise error behavior.

  Scenario: Resume preserves normal interactive integrations
    Given a saved AGENT session exists
    When the user runs "roger --webui resume <id>"
    Then the cached update notice reaches the resumed TUI
    And the browser console starts over the same shared controller
    And its URL reaches the resumed TUI hooks

  Scenario: Resume help never resolves a session
    When the user runs "roger resume --help"
    Then resume usage is printed
    And no session store, picker, browser console, or TUI is opened

  Scenario: Canonical executable branding is Roger
    When the user runs "roger version"
    Then the output begins with "roger "
    When the user runs "roger help"
    Then the heading begins with "roger -"
    And "rogerai" appears only as a legacy compatibility alias

  Scenario: Unknown commands are concise and printed once
    When the user runs an unknown command
    Then no full usage wall is printed
    And the returned error names the command
    And it suggests "roger help"
    And the top-level error renderer has only one error to print

  Scenario: Manual release metadata stays synchronized
    Given the CLI version is "5.4.8"
    Then both manual version attributes equal "v5.4.8"
    And the newest changelog entry is "v5.4.8"
    And the command table includes "roger resume"
    And the manual documents smart drag-copy and the native-selection toggle
