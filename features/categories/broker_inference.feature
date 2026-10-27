# Iteration - the broker guesses a category for a station that sent none.
#
# The node guesses (inference.feature), but a station on an older roger release sends no
# taxonomy at all, so on a live market most bands read "uncategorized" until every station
# upgrades. The broker applies the SAME guess (detect.InferTaxonomy, from the model id, the
# modality and the capabilities) when it publishes an offer that carries no taxonomy claim,
# labelled "inferred" - never "declared". It is a view, computed at emission: it is never
# stored, persisted or mirrored as the station's own claim, and any real taxonomy the station
# or its owner provides always wins. Curated (commercial-proxy) stations are never guessed
# (founder ruling 2026-10-27): their ids name a vendor's product, which the id hints misread.

Feature: The broker guesses a category for a station that sent none

  Scenario Outline: A station that sent no taxonomy is published with the broker's guess
    Given a public station on air serving "<model>" with modality "<modality>" and no taxonomy
    When a consumer GETs /discover
    Then its offer has category "<category>", subcategory "<subcategory>" and source "inferred"

    Examples:
      | model                        | modality | category  | subcategory |
      | gpt-oss-20b                  | chat     | language  | chat        |
      | qwen3-vl-8b                  | chat     | vision    | vlm         |
      | nomic-embed-text             | chat     | retrieval | embeddings  |
      | qwen/qwen3-coder-next        | chat     | language  | code        |
      | kokoro                       | tts      | audio     | tts         |

  Scenario: A detected vision capability informs the guess
    Given a public station on air serving "some-model" with capabilities "vision" and no taxonomy
    When a consumer GETs /discover
    Then its offer has category "vision", subcategory "vlm" and source "inferred"

  Scenario: The guess includes a name that says the refusals were removed
    Given a public station on air serving "Huihui-Qwen3-8B-abliterated" with modality "chat" and no taxonomy
    When a consumer GETs /discover
    Then its offer has content policy "uncensored"

  # Who wins: a real claim always beats the broker's guess.
  Scenario: A station's own declaration is never replaced by the guess
    Given a public station on air serving "qwen3-vl-8b" that declared "vision/ocr"
    When a consumer GETs /discover
    Then its offer has category "vision", subcategory "ocr" and source "declared"

  Scenario: A station's own guess is never replaced by the broker's
    Given a public station on air serving "gpt-oss-20b" whose node inferred "language/code"
    When a consumer GETs /discover
    Then its offer has category "language", subcategory "code" and source "inferred"

  Scenario: A station that declared only tags keeps its declaration and gets no guessed category
    Given a public station on air serving "gpt-oss-20b" that declared only the tags "ppe"
    When a consumer GETs /discover
    Then its offer has category "", subcategory "" and source "declared"

  # A view, not a claim.
  Scenario: The guess is never written back into the station's registration
    Given a public station on air serving "gpt-oss-20b" with modality "chat" and no taxonomy
    When a consumer GETs /discover
    Then the station's stored registration still carries no taxonomy

  # Every view agrees.
  Scenario: /market, /categories and the filters see the guess
    Given a public station on air serving "nomic-embed-text" with modality "chat" and no taxonomy
    When a consumer GETs /market and /categories and /discover?category=retrieval
    Then the market row for "nomic-embed-text" has category "retrieval"
    And category "retrieval" has 1 providers
    And the filtered offers are "nomic-embed-text"

  # Founder ruling 2026-10-27: no guess for a curated proxy.
  Scenario: A curated station that sent no taxonomy is published without a guess
    Given a curated station on air serving "openai/gpt-5.6-luna" with no taxonomy
    When a consumer GETs /discover
    Then its offer has category "", subcategory "" and source ""

  Scenario: A curated station's own declaration is still published
    Given a curated station on air serving "openai/gpt-5.6-luna" that declared "language/chat"
    When a consumer GETs /discover
    Then its offer has category "language", subcategory "chat" and source "declared"
