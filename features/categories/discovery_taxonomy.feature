# Slice 2 - the taxonomy reaches the public market, and a consumer can filter by it.
#
# /discover lists offers; /market aggregates per model. Both now carry the offer taxonomy
# (offer_taxonomy.feature) and accept category / subcategory / tag / policy filters.
#
# GROUND TRUTH (cmd/rogerai-broker/market.go): both views are PUBLIC and served through
# serveCachedJSON, which collapses the full-market recompute (the one expensive thing these
# endpoints do) to <= 1 per publicMarketTTL across instances, and /discover deliberately has
# no per-IP rate limit because of that. A filter that became part of the cache key would let
# anyone force a recompute per request with ?tag=x1, ?tag=x2, ... So filters are applied as a
# cheap pass over the ONE cached unfiltered view, and never reach the cache key.
#
# Founder rulings 2026-10-05: one primary category + tags; content policy shown normally.

Feature: The public market carries and filters by the model taxonomy

  Background:
    Given a broker with these public stations on air:
      | node | model        | modality | category  | subcategory | tags                        | policy     | source   |
      | n1   | qwen3-vl-8b  | chat     | vision    | ocr         | nameplate-ocr,manufacturing | aligned    | declared |
      | n2   | qwen3-vl-8b  | chat     | vision    | ocr         | manufacturing               |            | inferred |
      | n3   | qwen3-vl-8b  | chat     | vision    | vlm         | ppe                         |            | declared |
      | n4   | dolphin-8b   | chat     | language  | chat        |                             | uncensored | declared |
      | n5   | gpt-oss-20b  | chat     |           |             |                             |            |          |
      | n6   | kokoro       | tts      |           |             |                             |            |          |

  # ---------------------------------------------------------------- /discover carries it
  Scenario: Each /discover offer carries its own taxonomy
    When a consumer GETs /discover
    Then the offer from "n1" has category "vision", subcategory "ocr", tags "nameplate-ocr,manufacturing", content policy "aligned" and source "declared"
    And the offer from "n2" has category "vision", subcategory "ocr", tags "manufacturing", content policy "" and source "inferred"
    And the offer from "n4" has category "language", subcategory "chat", tags "", content policy "uncensored" and source "declared"

  # Updated 2026-10-27 with founder OK (broker_inference.feature): a station that sends no
  # taxonomy is published with the broker's guess; only a curated proxy, never guessed, stays bare.
  Scenario: A station that sent no taxonomy is published with the broker's guess
    When a consumer GETs /discover
    Then the offer from "n5" has category "language", subcategory "chat", tags "", content policy "" and source "inferred"

  Scenario: An offer with no taxonomy and no guess carries no taxonomy keys
    Given curated station "c9" is on air for "proxy-x" with no taxonomy
    When a consumer GETs /discover
    Then the JSON offer from "c9" has none of the keys "category,subcategory,tags,tuning,base_model,content_policy,license,taxonomy_source"

  Scenario: A voice offer reads as audio even though it declared nothing
    When a consumer GETs /discover
    Then the offer from "n6" has category "audio" and subcategory "tts"

  # An offer can reach the node map WITHOUT passing registration (shared-registry mirror,
  # lazy learn, DB re-hydrate), so the taxonomy is re-canonicalized at emission, exactly as
  # quant/weights/variant are.
  Scenario: A raw taxonomy that skipped registration is canonicalized at emission
    Given station "n7" reached the node map without registration carrying model "raw-m", category "Vision", subcategory "OCR", tags "PPE,diagnosis,a\u001bb" and content policy "abliterated"
    When a consumer GETs /discover
    Then the offer from "n7" has category "vision", subcategory "ocr", tags "ppe", content policy "uncensored" and source "declared"

  Scenario: A private band's offers carry the taxonomy too
    Given a private band station "p1" serving "qwen3-vl-8b" with category "vision" and subcategory "ocr"
    When the band is resolved by its frequency code
    Then the band offer from "p1" has category "vision" and subcategory "ocr"
    And a consumer GETs /discover
    And "p1" is not listed

  # ---------------------------------------------------------------- /discover filters
  Scenario Outline: /discover filters by the taxonomy
    When a consumer GETs /discover?<query>
    Then the listed nodes are "<nodes>"

    Examples:
      | query                                   | nodes          |
      | category=vision                         | n1,n2,n3       |
      | category=Vision                         | n1,n2,n3       |
      | category=vision&subcategory=ocr         | n1,n2          |
      | subcategory=ocr                         | n1,n2          |
      | category=language                       | n4,n5          |
      | category=audio                          | n6             |
      | category=audio&subcategory=tts          | n6             |
      | tag=manufacturing                       | n1,n2          |
      | tag=MANUFACTURING                       | n1,n2          |
      | tag=manufacturing&tag=nameplate-ocr     | n1             |
      | tag=ppe&tag=manufacturing               |                |
      | policy=uncensored                       | n4             |
      | policy=aligned                          | n1             |
      | policy=none                             | n2,n3,n5,n6    |
      | policy=aligned&policy=uncensored        | n1,n4          |
      | category=vision&policy=none             | n2,n3          |
      | category=vision&tag=ppe                 | n3             |
      | source=declared                         | n1,n3,n4       |
      | source=inferred                         | n2,n5,n6       |

  Scenario Outline: An unknown or invalid filter value matches nothing, it is not ignored
    When a consumer GETs /discover?<query>
    Then the response status is 200
    And the listed nodes are ""

    Examples:
      | query                       |
      | category=medical            |
      | category=vision&subcategory=code |
      | subcategory=nope            |
      | tag=diagnosis               |
      | tag=bad%2Ftag               |
      | policy=nsfw                 |
      | source=verified             |

  Scenario: An empty filter value is no filter
    When a consumer GETs /discover?category=&tag=&policy=
    Then the listed nodes are "n1,n2,n3,n4,n5,n6"

  # corrected 2026-10-27 (founder ruling 2026-10-27): the routing contract's discovery filters
  # (features/discovery/filters.feature) win - an unknown query parameter is refused, with or
  # without a taxonomy filter beside it.
  Scenario: An unknown query parameter is refused
    When a consumer GETs /discover?foo=bar&category=language
    Then the response status is 400
    And the error code is "unknown_query_param"

  Scenario: A filtered /discover keeps the cheapest-first order
    When a consumer GETs /discover?category=vision
    Then the listed offers are in ascending input-price order

  # ---------------------------------------------------------------- /market aggregates it
  # A model id can be served by stations that describe it differently. The row shows the
  # pair MOST of its online stations declare (ties broken alphabetically), how many back it,
  # the union of their tags (most-shared first), and EVERY content-policy grade any station
  # declared - a consumer never sees a single "aligned" hiding a station that said otherwise.
  Scenario: /market shows the majority category and how many stations back it
    When a consumer GETs /market
    Then the market row for "qwen3-vl-8b" has category "vision", subcategory "ocr" and category providers 2
    And the market row for "qwen3-vl-8b" has providers 3

  Scenario: /market unions tags, most-shared first, then alphabetical
    When a consumer GETs /market
    Then the market row for "qwen3-vl-8b" has tags "manufacturing,nameplate-ocr,ppe"

  Scenario: /market lists every declared content-policy grade for a model
    Given station "n8" is on air for "qwen3-vl-8b" with content policy "uncensored"
    When a consumer GETs /market
    Then the market row for "qwen3-vl-8b" has content policies "aligned,uncensored"

  Scenario: A /market row with no taxonomy and no guess carries no taxonomy keys
    Given curated station "c9" is on air for "proxy-x" with no taxonomy
    When a consumer GETs /market
    Then the JSON market row for "proxy-x" has none of the keys "category,subcategory,category_providers,tags,content_policies"

  Scenario: A category tie is broken alphabetically
    Given station "n9" is on air for "tie-m" with category "vision" and subcategory "vlm"
    And station "n10" is on air for "tie-m" with category "language" and subcategory "chat"
    When a consumer GETs /market
    Then the market row for "tie-m" has category "language", subcategory "chat" and category providers 1

  Scenario: The /market tag union is capped at eight
    Given 12 stations on air for "tag-m" each declaring a different tag
    When a consumer GETs /market
    Then the market row for "tag-m" has 8 tags

  Scenario: Offline stations do not vote
    Given station "n3" has not heartbeat within nodeTTL
    When a consumer GETs /market
    Then the market row for "qwen3-vl-8b" has tags "manufacturing,nameplate-ocr"

  # ---------------------------------------------------------------- /market filters
  Scenario Outline: /market filters rows by their aggregated taxonomy
    When a consumer GETs /market?<query>
    Then the market models are "<models>"

    Examples:
      | query                            | models       |
      | category=vision                  | qwen3-vl-8b  |
      | category=vision&subcategory=vlm  |              |
      | tag=ppe                          | qwen3-vl-8b  |
      | policy=uncensored                | dolphin-8b   |
      | policy=none                      | gpt-oss-20b,kokoro |
      | category=audio                   | kokoro       |
      | category=medical                 |              |

  # Founder ruling 2026-10-05 (review): a /market row is routed by model id, so ?policy= on
  # /market must hold for EVERY station that declared a grade, or a consumer filtering for
  # "aligned" could be routed to an uncensored station of the same model. /discover filters
  # per station and is unaffected.
  Scenario: /market?policy= excludes a model when any of its stations declares another grade
    Given station "n8" is on air for "qwen3-vl-8b" with content policy "uncensored"
    When a consumer GETs /market?policy=aligned
    Then the market models are ""

  Scenario: /market?policy= with several allowed grades keeps a model whose grades all fit
    Given station "n8" is on air for "qwen3-vl-8b" with content policy "uncensored"
    When a consumer GETs /market?policy=aligned&policy=uncensored
    Then the market models are "dolphin-8b,qwen3-vl-8b"

  # Founder ruling 2026-10-05 (review): "providers" is the market's HUMAN-supply claim, so a
  # curated (commercial-proxy) station never counts toward category_providers; its vote only
  # labels a model that no human station has categorized.
  Scenario: Curated stations do not count toward category providers
    Given curated station "c1" is on air for "qwen3-vl-8b" with category "vision" and subcategory "vlm"
    And curated station "c2" is on air for "qwen3-vl-8b" with category "vision" and subcategory "vlm"
    When a consumer GETs /market
    Then the market row for "qwen3-vl-8b" has category "vision", subcategory "ocr" and category providers 2

  Scenario: A curated-only model takes its curated stations' category
    Given curated station "c1" is on air for "proxy-m" with category "language" and subcategory "code"
    When a consumer GETs /market
    Then the market row for "proxy-m" has category "language", subcategory "code" and category providers 0

  # Review hardening: a station carries at most 8 tags, so more than 8 distinct required
  # tags can match nothing, and repeating a tag is one tag.
  Scenario: Repeating a tag filter is the same as giving it once
    When a consumer GETs /discover?tag=manufacturing&tag=manufacturing&tag=MANUFACTURING
    Then the listed nodes are "n1,n2"

  Scenario: More than eight distinct required tags match nothing
    When a consumer GETs /discover?tag=a&tag=b&tag=c&tag=d&tag=e&tag=f&tag=g&tag=h&tag=i
    Then the response status is 200
    And the listed nodes are ""

  # /market rows carry no claim source, so ?source= is not a /market filter at all.
  Scenario Outline: ?source= is ignored on /market
    When a consumer GETs /market?source=<value>
    Then the market models are "dolphin-8b,gpt-oss-20b,kokoro,qwen3-vl-8b"

    Examples:
      | value    |
      | declared |
      | verified |

  # ---------------------------------------------------------------- the cache is not a lever
  Scenario: Filtered reads never recompute the market
    Given the public market cache is warm
    When a consumer GETs /discover with 50 different random tag filters
    And a consumer GETs /market with 50 different random tag filters
    Then the full-market recompute ran 0 more times

  # Regression (found while specifying this slice): the public views keyed their cache on the
  # model/confidential/freq query values although neither view filters on them, so varying
  # those values forced a fresh recompute each time, and enough distinct keys reset the whole
  # in-process cache. Nothing in the payload depends on them, so they no longer reach the key.
  # corrected 2026-10-27 (founder ruling 2026-10-27): features/discovery/filters.feature wins.
  # model and confidential are real /discover filters applied over the one cached entry, and an
  # unknown parameter (freq) is a 400; neither recomputes. /market?model= is a real filter whose
  # aggregates are recomputed over the surviving providers, so its row is removed here (pinned
  # in filters.feature).
  Scenario Outline: Unused query values never recompute the market
    Given the public market cache is warm
    When a consumer GETs /<view> with 50 different random "<param>" values
    Then the full-market recompute ran 0 more times

    Examples:
      | view     | param        |
      | discover | model        |
      | discover | confidential |
      | discover | freq         |
      | market   | freq         |

  Scenario: A filtered read on a cold cache computes the market once
    Given the public market cache is cold
    When a consumer GETs /discover?category=vision
    And a consumer GETs /discover?category=language
    And a consumer GETs /discover
    Then the discover recompute ran exactly once

  Scenario: Filtering never leaks a private band
    Given a private band station "p1" serving "qwen3-vl-8b" with category "vision" and subcategory "ocr"
    When a consumer GETs /discover?category=vision
    Then "p1" is not listed
