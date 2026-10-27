# Slice 4 - the manual teaches categories wherever an operator or a consumer meets them.
# Checked against the BUILT manual (web/dist/manual.html), so a page that drops a line fails.

Feature: The manual documents model categories

  Scenario: The TUNE IN key table lists the category filter (and the curated filter it was missing)
    When the manual is built
    Then the TUNE IN key table lists "G" as the category filter
    And the TUNE IN key table lists "U" as the curated filter

  Scenario: The SHARE section documents declaring a category
    When the manual is built
    Then the share section documents "--category", "--tag", "--tuning", "--policy", "--license" and "--base-model"
    And the share section documents the SHARE tab "g" editor
    And the share section explains declared versus guessed ("~")

  Scenario: The manual lists the categories and points at the live list
    When the manual is built
    Then it names the categories "language, vision, audio, retrieval, generation, signals"
    And it links to the /categories API

  Scenario: The manual is honest about what a category is
    When the manual is built
    Then it says a category is the station's own description and is not verified
    And it says a content-policy grade never turns screening off

  Scenario: The API reference documents the filters
    When the manual is built
    Then it documents the "category", "subcategory", "tag" and "policy" filters on /discover and /market
