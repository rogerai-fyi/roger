Feature: sample

  Scenario: one
    Given a step

  Scenario Outline: many
    Given a <n> step

    Examples:
      | n |
      | 1 |
      | 2 |
