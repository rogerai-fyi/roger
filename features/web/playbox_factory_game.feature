Feature: Waveworks factory game
  The Playbox offers a welcoming factory-building game beside, not inside,
  the Console and Wave Mesh engineering workbench.

  Scenario: Enter a separate factory deck
    Given the Playbox deck selector is visible
    Then Console, Wave Mesh, and Factory are three sibling choices
    And choosing Factory opens the Waveworks production floor
    And Wave Mesh remains the full recorded engineering sandbox

  Scenario: Learn by building rather than being tested
    Given I have ninety game bolts and an empty production floor
    When I buy a part shaper and a crate packer
    And I run a batch
    Then a part visibly travels across the line
    And a good crate earns game bolts
    And I am never asked which model is the correct answer

  Scenario: Recover a held batch without losing the game
    Given a recorded fault card reaches a line without enough automation
    Then the batch waits in the hold bay
    When I manually rework it
    Then the crate ships for a smaller reward
    And I can keep building without passing a knowledge test

  Scenario: Model tiers are factory upgrades
    Given Wave Pico is installed on a machine
    When its recorded confident result matches the signal truth
    Then the game automatically routes the part through rework
    Given Wave Nano is installed at the gateway
    When Pico is doubtful and Nano's recorded result matches the truth
    Then the gateway automates that handoff
    But no unrecorded Micro or Giga inference is invented

  Scenario: Expand the factory
    Given six crates have shipped
    When I claim the second production line
    Then each run can fill two crates
    And the site and plant upgrade pads become part of the game economy
