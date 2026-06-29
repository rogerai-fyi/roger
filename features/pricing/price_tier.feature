# Price-tier classification: a NEUTRAL, buyer-facing "how expensive is this band
# RIGHT NOW, for THIS model" signal — Yelp-style $ … $$$$ — computed ONCE on the
# broker and carried on every offer so the TUI, the web models page, and the web
# companion all render the SAME interpretation. Pricing stays OPEN (operators set
# their own price); this only INTERPRETS the market, it never sets a price.
#
# BASELINE (founder decision): a band is graded against an EXTERNAL same-model
# reference price when one is known, else against the live INTERNAL market, else not
# at all. The two baselines need DIFFERENT thresholds because RogerAI bands sit well
# BELOW commercial prices (a crowd-sourced 8B is routinely a fraction of OpenRouter),
# so the external scale grades DISCOUNT DEPTH while the internal scale grades position
# among peers.
#
# GROUND TRUTH:
#   - offer feed: cmd/rogerai-broker/market.go offerView, served on /discover and —
#     with the SAME per-offer metrics — private /bands (band.go:208, via the shared
#     enrichOffersForNode). The /market view is a per-MODEL aggregate (marketView); it carries
#     a per-MODEL price_tier for the model's BEST (cheapest) active out-price — priceTier over
#     the same external-ref-else-median baseline — so the aggregate row shows the SAME $-reading
#     the cheapest provider's offer shows on /discover. A field `price_tier int
#     (json:"price_tier")` carries the result on BOTH the per-offer feed and the /market row;
#     0 = no tier.
#   - external reference: the OUT-price ($/1M) for the SAME open model on a popular
#     commercial aggregator (OpenRouter's public models API), synced best-effort on a
#     slow cadence (~12-24h) into a cached + persisted model->ref map; last-known value
#     is kept on any fetch failure (classification NEVER depends on a live fetch); a
#     small static seed (the metrics_series.go frontierTable pattern, but per OPEN
#     model) is the pre-first-sync fallback. Matching is by NORMALIZED model name
#     (operator free-text vs "vendor/model" ids) — a model with no match has no ref.
#     NOTE: the cross-model frontierTable (gpt-4o etc.) drives the savings headline and
#     is NOT a per-band tier baseline (you can't grade an 8B against gpt-4o).
#   - internal median: mirrors internal/client/failover.go MarketMedianOut — the median
#     OUT-price over the model's ONLINE offers (odd: middle; even: mean of the two
#     middle). MEDIAN not mean, so outliers can't move it.
#   - price caps: cmd/rogerai-broker/pricesafety.go (out ≤ $100/1M); prices validated
#     ≥ 0, so a negative price never reaches the classifier.
#
# CONTRACT — priceTier(priceOut, refOut, onlineOutPricesForThisModel) -> 0..4:
#   priceOut ≤ 0                              -> 0  (FREE; the renderer shows FREE)
#   choose the baseline B and its scale:
#     refOut > 0                              -> B = refOut, EXTERNAL scale
#     else ≥3 online offers AND median > 0    -> B = median, INTERNAL scale
#     else                                    -> 0  (UNKNOWN; raw price, no badge)
#   r = priceOut / B, then:
#     EXTERNAL scale (vs the same model elsewhere — grades discount depth):
#       r ≤ 0.25 -> 1 ($)    + "good price" chip   (≥75% under the reference)
#       r ≤ 0.50 -> 2 ($$)
#       r ≤ 0.90 -> 3 ($$$)
#       r  > 0.90 -> 4 ($$$$)                       (≈ paying commercial price here)
#     INTERNAL scale (vs other live bands — grades position among peers):
#       r ≤ 0.70 -> 1 ($)    + "good price" chip
#       r ≤ 1.15 -> 2 ($$)
#       r ≤ 2.00 -> 3 ($$$)
#       r  > 2.00 -> 4 ($$$$)
#   Only tier 1 is ever editorialized (the favorable "good price" chip); $$–$$$$ are
#   neutral with NO "expensive"/negative wording, ever. The external ref takes
#   PRECEDENCE over the internal median (the more meaningful "vs the world" anchor),
#   so a band can't dodge it by flooding cheap peers. Thresholds are tunable from real
#   data. Computed server-side once, rendered client-side, so every surface agrees.

Feature: The broker classifies each band into a neutral $…$$$$ tier vs an external reference, falling back to the live internal market

  Background:
    Given a broker with a live market

  # === EXTERNAL reference baseline (preferred) ==============================

  Scenario Outline: A band is graded against the same model's external reference price
    Given model "qwen3-8b" has an external reference out-price of 0.20 $/1M
    When a band for "qwen3-8b" is priced <price> $/1M out
    Then it has price_tier <tier>

    Examples:
      | price | tier |
      | 0.04  | 1    |
      | 0.05  | 1    |
      | 0.06  | 2    |
      | 0.10  | 2    |
      | 0.12  | 3    |
      | 0.18  | 3    |
      | 0.20  | 4    |
      | 0.40  | 4    |

  Scenario: A band ~75% under the external reference is $ with a good-price chip
    Given model "qwen3-8b" has an external reference out-price of 0.20 $/1M
    When a band for "qwen3-8b" is priced 0.04 $/1M out
    Then it has price_tier 1
    And it renders a "good price" chip

  Scenario: A band at ~the external reference is $$$$ with no negative wording
    Given model "qwen3-8b" has an external reference out-price of 0.20 $/1M
    When a band for "qwen3-8b" is priced 0.22 $/1M out
    Then it has price_tier 4
    And the rendered band carries no "expensive" or otherwise negative wording

  Scenario: The external reference takes precedence over the internal median
    Given model "qwen3-8b" has an external reference out-price of 0.20 $/1M
    And model "qwen3-8b" has online bands priced 0.04, 0.10, 0.10, 0.12 $/1M out
    # internal median is 0.10 (band 0.04 would be $$ internally: 0.04/0.10=0.4),
    # but the external ref wins: 0.04/0.20=0.2 -> $.
    Then the band priced 0.04 has price_tier 1

  # === reference freshness: best-effort, never live-blocking =================

  Scenario: A failed latest sync still classifies against the last-known reference
    Given model "qwen3-8b" has a last-known external reference out-price of 0.20 $/1M
    And the latest external price sync failed
    When a band for "qwen3-8b" is priced 0.04 $/1M out
    Then it has price_tier 1

  Scenario: Before the first sync the static seed reference is used
    Given no external sync has run yet
    And the seed reference out-price for "qwen3-8b" is 0.20 $/1M
    When a band for "qwen3-8b" is priced 0.04 $/1M out
    Then it has price_tier 1

  # === INTERNAL median fallback (no external reference) =====================

  Scenario Outline: A model with no external reference is graded vs its live median
    Given model "niche-finetune" has no external reference price
    And model "niche-finetune" has an online median of 0.10 $/1M out
    When a band for "niche-finetune" is priced <price> $/1M out
    Then it has price_tier <tier>

    Examples:
      | price | tier |
      | 0.070 | 1    |
      | 0.071 | 2    |
      | 0.115 | 2    |
      | 0.120 | 3    |
      | 0.200 | 3    |
      | 0.210 | 4    |

  Scenario: With no external reference and fewer than 3 online bands, the tier is withheld
    Given model "niche-finetune" has no external reference price
    And model "niche-finetune" has online bands priced 0.10, 0.50 $/1M out
    Then the band priced 0.10 has price_tier 0
    And the band priced 0.10 renders its raw price with no $ tier and no chip

  Scenario: A lone band for a model with no reference is UNKNOWN (cannot self-declare a deal)
    Given model "solo-model" has no external reference price
    And model "solo-model" has 1 online band priced 0.01 $/1M out
    Then the band priced 0.01 has price_tier 0

  Scenario: Internal-median fallback uses the median, not the mean (outlier-robust)
    Given model "niche-finetune" has no external reference price
    And model "niche-finetune" has online bands priced 0.10, 0.10, 0.10, 0.10, 5.00 $/1M out
    # median 0.10 (mean ~1.08 would mislabel everyone)
    Then the band priced 0.10 has price_tier 2
    And the band priced 5.00 has price_tier 4

  Scenario: Offline bands do not count toward the internal-median fallback
    Given model "niche-finetune" has no external reference price
    And model "niche-finetune" has 2 online bands priced 0.10, 0.50 $/1M out
    And model "niche-finetune" has 3 offline bands priced 0.01, 0.02, 0.03 $/1M out
    Then the online band priced 0.10 has price_tier 0

  # === FREE always wins =====================================================

  Scenario: A free band shows FREE, never a $ tier (even with a reference)
    Given model "qwen3-8b" has an external reference out-price of 0.20 $/1M
    And a band for "qwen3-8b" priced 0.00 $/1M out (free)
    Then the free band has price_tier 0
    And the free band renders the FREE badge, not a $ tier

  Scenario: The tier uses the active (time-of-use) price right now
    Given model "qwen3-8b" has an external reference out-price of 0.20 $/1M
    And a band for "qwen3-8b" whose active price right now is 0.00 (a free window)
    Then that band has price_tier 0 and renders FREE

  # === per-model isolation ==================================================

  Scenario: The same dollar price lands in different tiers across models
    Given model "small-8b" has an external reference out-price of 0.20 $/1M
    And model "big-70b" has an external reference out-price of 2.00 $/1M
    When a band priced 0.40 $/1M out is offered for "small-8b"
    And a band priced 0.40 $/1M out is offered for "big-70b"
    Then the "small-8b" band has price_tier 4
    And the "big-70b" band has price_tier 1

  # === caps =================================================================

  Scenario: A band at the out-price cap is $$$$ with no negative wording
    Given model "qwen3-8b" has an external reference out-price of 0.20 $/1M
    And a band for "qwen3-8b" priced 100.00 $/1M out (the cap)
    Then that band has price_tier 4
    And the rendered band carries no negative wording

  # === single source of truth: every surface agrees ========================

  Scenario: The same offer carries the same tier on the public feed and as a private band
    Given model "qwen3-8b" has an external reference out-price of 0.20 $/1M
    And a public band for "qwen3-8b" priced 0.04 $/1M out
    And a private band (frequency-code only) for "qwen3-8b" priced 0.04 $/1M out
    Then the public band has price_tier 1 on /discover
    And the private band has price_tier 1 when resolved by frequency code
