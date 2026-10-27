# Slice 4 - the website models tuner (web/src/models.html + js/bands.js) browses by category.
#
# The rules live in ONE small module, web/src/js/taxonomy.js (window.RogerTaxonomy), so they
# are tested by running them (node:vm), not by matching source text. bands.js uses it for
# labels, filtering and search. The category list comes from GET /categories, so the site
# never hard-codes it. Content-policy grades are shown normally (founder ruling), with the
# note that broker screening still applies.

Feature: The models tuner browses, filters and searches by category

  # ---------------------------------------------------------------- the rules (taxonomy.js)
  Scenario Outline: A band's label names its category and marks a guess
    Given a band whose stations carry "<pairs>"
    When the tuner labels the band
    Then the label is "<label>"

    Examples:
      | pairs                                         | label                 |
      | vision/ocr:declared,vision/ocr:inferred       | vision/ocr            |
      | retrieval/embeddings:inferred                 | retrieval/embeddings~ |
      | vision/vlm:declared,vision/vlm:declared,vision/ocr:declared | vision/vlm |
      | vision/vlm:declared,language/chat:declared    | language/chat         |
      | vision:declared                               | vision                |
      |                                               |                       |
      # The /market rule (slice-4 review): online human stations vote; curated label only a
      # band no human station labels; an all-offline band falls back to every station.
      | vision/ocr:declared,language/chat:declared:curated,language/chat:declared:curated | vision/ocr |
      | language/code:declared:curated                | language/code         |
      | vision/ocr:declared,vision/vlm:declared:offline,vision/vlm:declared:offline | vision/ocr |
      | vision/ocr:declared:offline                   | vision/ocr            |

  Scenario Outline: The tuner filter matches a band by category, subcategory and tag
    Given a band labelled "vision/ocr" with tags "manufacturing,ppe" and grades "aligned"
    When the tuner filters by <filter>
    Then the band <result>

    Examples:
      | filter                         | result      |
      | category "vision"              | is shown    |
      | category "language"            | is hidden   |
      | category "vision" sub "ocr"    | is shown    |
      | category "vision" sub "vlm"    | is hidden   |
      | category ""                    | is shown    |

  Scenario Outline: Search matches the model name, the category and the tags
    Given a band "qwen3-vl-8b" labelled "vision/ocr" with tags "manufacturing,ppe" and grades ""
    When the tuner searches for "<query>"
    Then the band <result>

    Examples:
      | query         | result    |
      | qwen          | is shown  |
      | OCR           | is shown  |
      | manufact      | is shown  |
      | ppe           | is shown  |
      | embeddings    | is hidden |

  Scenario Outline: A declared grade other than aligned is badged on the band
    Given a band labelled "language/chat" with tags "" and grades "<grades>"
    When the tuner badges the band
    Then the grade badge is "<badge>"

    Examples:
      | grades                 | badge           |
      | uncensored             | uncensored      |
      | reduced-refusal        | reduced-refusal |
      | aligned,uncensored     | uncensored      |
      | aligned                |                 |
      |                        |                 |

  Scenario: Category options come from /categories in canonical order, empty aisles counted
    Given /categories reports vision with 3 stations and retrieval with 0
    When the tuner builds its category options
    Then the options are "All categories, language (0), vision (3), audio (0), retrieval (0), generation (0), signals (0)"

  Scenario Outline: A deep link preselects the filter
    When the tuner reads the page address "<query>"
    Then the category filter is "<category>" and the subcategory filter is "<sub>"

    Examples:
      | query                         | category | sub |
      | ?category=vision              | vision   |     |
      | ?category=Vision&sub=OCR      | vision   | ocr |
      | ?category=medical             |          |     |
      | ?category=vision&sub=code     | vision   |     |
      | ?utm=%&category=vision        | vision   |     |
      |                               |          |     |

  # Anything a station sent is rendered as text, never as markup.
  Scenario: Taxonomy text is escaped when rendered
    Given a band labelled "vision/ocr" with tags "<img src=x onerror=alert(1)>" and grades ""
    When the tuner renders the band's tags
    Then the rendered tags contain no markup

  # ---------------------------------------------------------------- the page wiring
  Scenario: The models page loads the rules and offers the category filter
    When the models page is built
    Then it loads "js/taxonomy.js" before "js/bands.js"
    And it has a category select labelled "Category"
    And bands.js reads "/categories"
    And bands.js maps the category fields from both /market and /discover

  Scenario: The band detail on the site carries the moderation note for a graded band
    When the models page is built
    Then the band detail copy says "RogerAI still screens every request"
