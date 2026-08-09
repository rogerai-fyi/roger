# v5.7.1 release repair: the published bits must not contain a reachable known Go
# vulnerability, and a running broker must identify both its release and exact source.

Feature: Release dependency and broker identity gates

  Scenario: A release is refused when Go code reaches a known vulnerability
    Given the complete module is built with the release Go toolchain
    When the current Go vulnerability database is evaluated against every package
    Then no reachable vulnerability is reported
    And the same vulnerability gate runs on main and before any tagged release is published

  Scenario: Invalid UTF-8 cannot trap Unicode normalization in an infinite loop
    Given an invalid UTF-8 sequence followed by combining input
    When the Unicode normalization dependency processes it
    Then processing terminates within the test deadline

  Scenario: A running broker reports the release and exact source revision
    Given a broker built from a known source commit
    When a client requests GET /version
    Then the response is 200 JSON
    And it contains the broker semantic version
    And it contains the complete lowercase source commit
    And the response cannot be served from a stale cache

  Scenario: Missing or malformed build metadata is not asserted
    Given the broker has no valid hexadecimal source commit
    When a client requests GET /version
    Then the semantic version is still reported
    And no source commit is claimed

  Scenario: Version discovery is read-only
    When a client sends a non-GET request to /version
    Then the broker responds with method not allowed
