# A content-policy grade describes the WEIGHTS a provider serves. It never describes the
# NETWORK. The broker's pre-dispatch screen (features/moderation/screen.feature,
# recalibration.feature) runs on every relay request whatever the target offer declares,
# so "uncensored" can never be read, by a consumer or by the code, as "unscreened".
# Founder ruling 2026-10-05: graded and shown normally; moderation still applies.
#
# Ground truth: the screen is content-based and takes no offer metadata
# (cmd/rogerai-broker/moderation.go). These scenarios lock that in as the taxonomy lands,
# so a later "skip screening for uncensored bands" shortcut fails red.

Feature: A declared content policy never weakens the broker's screen

  Background:
    Given a broker with moderation required in "sync" mode
    And a station serving model "open-model" on air

  Scenario Outline: A flagged prompt is blocked whatever the offer's content policy
    Given the offer for "open-model" declares content policy "<policy>"
    And a moderation backend that flags the prompt as "unsafe S1"
    When a consumer sends a chat request to "open-model"
    Then the response status is 451
    And the station never receives the request

    Examples:
      | policy          |
      |                 |
      | aligned         |
      | reduced-refusal |
      | uncensored      |

  Scenario Outline: CSAM is preserved and queued for report whatever the content policy
    Given the offer for "open-model" declares content policy "<policy>"
    And a moderation backend that flags the prompt as "unsafe S4"
    When a consumer sends a chat request to "open-model"
    Then the response status is 451
    And the incident is preserved and a report is queued
    And the station never receives the request

    Examples:
      | policy          |
      |                 |
      | uncensored      |

  Scenario Outline: A clean prompt is screened then served whatever the content policy
    Given the offer for "open-model" declares content policy "<policy>"
    And a moderation backend that returns "safe"
    When a consumer sends a chat request to "open-model"
    Then the response status is 200
    And the prompt was screened exactly once
    And the station receives the request

    Examples:
      | policy          |
      |                 |
      | aligned         |
      | uncensored      |

  Scenario: A node cannot opt its offer out of screening through the taxonomy
    Given the offer for "open-model" declares content policy "uncensored" and tags "unmoderated,no-filter"
    And a moderation backend that flags the prompt as "unsafe S1"
    When a consumer sends a chat request to "open-model"
    Then the response status is 451

  # ---------------------------------------------------------------- async (the production default)
  # Off-path screening serves first and screens after (features/moderation/off_path_screening
  # .feature). The declared policy must not let a relay skip that queue: every prompt is still
  # screened, a flag is still recorded, CSAM is still preserved and queued for report.
  # Added at founder request 2026-10-05 (review finding: sync-only coverage).

  Scenario Outline: In async mode a flagged prompt is still screened and flagged whatever the policy
    Given a broker with moderation required in "async" mode
    And the offer for "open-model" declares content policy "<policy>"
    And a moderation backend that flags the prompt as "unsafe S1"
    When a consumer sends a chat request to "open-model"
    And off-path screening drains
    Then the prompt was screened exactly once
    And a moderation flag is recorded for the request

    Examples:
      | policy          |
      |                 |
      | aligned         |
      | reduced-refusal |
      | uncensored      |

  Scenario Outline: In async mode CSAM is preserved and queued whatever the policy
    Given a broker with moderation required in "async" mode
    And the offer for "open-model" declares content policy "<policy>"
    And a moderation backend that flags the prompt as "unsafe S4"
    When a consumer sends a chat request to "open-model"
    And off-path screening drains
    Then the incident is preserved and a report is queued

    Examples:
      | policy     |
      |            |
      | uncensored |

  Scenario: In async mode taxonomy tags cannot opt an offer out of the screening queue
    Given a broker with moderation required in "async" mode
    And the offer for "open-model" declares content policy "uncensored" and tags "unmoderated,no-filter"
    And a moderation backend that flags the prompt as "unsafe S1"
    When a consumer sends a chat request to "open-model"
    And off-path screening drains
    Then the prompt was screened exactly once
    And a moderation flag is recorded for the request
