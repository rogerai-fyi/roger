# Slice 2 - GET /categories: the browse landing for every surface.
#
# One public, cacheable read that lists the WHOLE v1 category list (offer_taxonomy.feature)
# with live supply per category and subcategory, so the website, TUI and API clients never
# hard-code the list, and an empty aisle is visible (the "be first in this category" hook).
# Built from the same cached /discover view (discovery_taxonomy.feature), so it costs no
# extra full-market recompute and never lists a private band.

Feature: GET /categories lists every category with its live supply

  Background:
    Given a broker with these public stations on air:
      | node | model        | modality | category  | subcategory | tags          | policy     | source   | price_out | tps |
      | n1   | qwen3-vl-8b  | chat     | vision    | ocr         | manufacturing | aligned    | declared | 40        | 30  |
      | n2   | qwen3-vl-8b  | chat     | vision    | ocr         |               |            | inferred | 20        | 55  |
      | n3   | florence-2   | chat     | vision    | detection   | ppe           |            | declared | 10        | 0   |
      | n4   | dolphin-8b   | chat     | language  | chat        |               | uncensored | declared | 15        | 80  |
      | n5   | gpt-oss-20b  | chat     |           |             |               |            |          | 5         | 90  |
      | n6   | kokoro       | tts      |           |             |               |            |          | 3         | 0   |

  Scenario: Every v1 category and subcategory is listed, in the canonical order, even when empty
    When a consumer GETs /categories
    Then the response status is 200
    And the taxonomy version is 1
    And the categories are "language,vision,audio,retrieval,generation,signals"
    And category "retrieval" lists subcategories "embeddings,rerank"
    And category "retrieval" has 0 providers and 0 models

  Scenario: A category counts its online stations and distinct models
    When a consumer GETs /categories
    Then category "vision" has 3 providers and 2 models
    And subcategory "vision/ocr" has 2 providers and 1 models
    And subcategory "vision/detection" has 1 providers and 1 models
    And subcategory "vision/vlm" has 0 providers and 0 models

  Scenario: A category shows its cheapest output price and best measured speed
    When a consumer GETs /categories
    Then category "vision" has min output price 10 and best tps 55
    And subcategory "vision/ocr" has min output price 20 and best tps 55

  Scenario: An empty category has no price or speed keys
    When a consumer GETs /categories
    Then the JSON for category "retrieval" has none of the keys "min_out,best_tps"

  Scenario: A voice station counts under audio
    When a consumer GETs /categories
    Then subcategory "audio/tts" has 1 providers and 1 models

  Scenario: Stations with no category are counted as uncategorized, not dropped
    When a consumer GETs /categories
    Then the uncategorized count is 1 providers and 1 models

  Scenario: Offline stations are not counted
    Given station "n2" has not heartbeat within nodeTTL
    When a consumer GETs /categories
    Then subcategory "vision/ocr" has 1 providers and 1 models

  Scenario: A private band never counts
    Given a private band station "p1" serving "qwen3-vl-8b" with category "vision" and subcategory "ocr"
    When a consumer GETs /categories
    Then subcategory "vision/ocr" has 2 providers and 1 models

  Scenario: A banned station never counts
    Given station "n1" is banned
    When a consumer GETs /categories
    Then subcategory "vision/ocr" has 1 providers and 1 models

  # Founder ruling 2026-10-05 (review): providers is HUMAN supply; curated proxies are counted
  # apart, as on /market.
  Scenario: Curated stations are counted apart from providers
    Given curated station "c1" is on air for "proxy-ocr" with category "vision" and subcategory "ocr"
    When a consumer GETs /categories
    Then subcategory "vision/ocr" has 2 providers and 2 models
    And subcategory "vision/ocr" has 1 curated providers
    And category "vision" has 1 curated providers

  Scenario: /categories costs no extra market recompute
    Given the public market cache is warm
    When a consumer GETs /categories 20 times
    Then the full-market recompute ran 0 more times

  Scenario: /categories answers CORS for the website
    When a consumer GETs /categories
    Then the response allows cross-origin reads

  Scenario: /categories only answers GET
    When a consumer POSTs /categories
    Then the response status is 405
