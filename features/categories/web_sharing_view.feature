# Slice 4 - a signed-in provider sees, on the website, what each shared model says it is for.
#
# Read-only in this slice: the website console ("Models & pricing", /provider/models) shows
# each station's category, tags and whether it was declared or guessed, and says where to
# change it. Editing from the website is slice 5 (an owner-scoped override in the broker's
# store, founder ruling 2026-10-05).
#
# The @web scenario runs against the built site (web/test); the rest against the broker.

Feature: The website console shows the taxonomy of what you share

  Scenario: /provider/models carries each station's taxonomy as registered
    Given an owner with a station sharing "qwen3-vl-8b" declared as "vision/ocr" with tags "ppe"
    When the owner GETs /provider/models
    Then the row for "qwen3-vl-8b" has category "vision", subcategory "ocr", tags "ppe" and source "declared"

  Scenario: An inferred taxonomy is reported as inferred
    Given an owner with a station sharing "nomic-embed" inferred as "retrieval/embeddings"
    When the owner GETs /provider/models
    Then the row for "nomic-embed" has category "retrieval", subcategory "embeddings", tags "" and source "inferred"

  Scenario: A row with no taxonomy carries no taxonomy keys
    Given an owner with a station sharing "mystery-m" with no taxonomy
    When the owner GETs /provider/models
    Then the JSON row for "mystery-m" has none of the keys "category,subcategory,tags,content_policy,taxonomy_source"

  Scenario: Another owner's taxonomy is never shown
    Given an owner with a station sharing "qwen3-vl-8b" declared as "vision/ocr" with tags "ppe"
    And another owner with a station sharing "secret-m" declared as "language/code" with tags "internal"
    When the owner GETs /provider/models
    Then no row for "secret-m" is listed

  @web
  Scenario: The console page shows the category and says where to change it
    When the console page is built
    Then each Models & pricing model card shows a "Category" line
    And it says the category is changed from "roger share --category", the TUI SHARE tab, or the local console
    And console.js marks a guessed category with "~"
