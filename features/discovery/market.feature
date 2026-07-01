# DISCOVERY (public): how a consumer finds stations. GET /discover lists the live PUBLIC
# offers; GET /market is the per-model aggregated marketplace view (price + a signal-strength
# reading per model). Both are PUBLIC + cacheable. The invariant that matters: PRIVATE (band-
# only) nodes are NEVER listed here — they exist only behind their frequency code.
#
# GROUND TRUTH (cmd/rogerai-broker/market.go):
#   discover()  -> the live public offers (on-air, not private, within nodeTTL).
#   market() -> serveCachedJSON("market:"+query, publicMarketTTL, computeMarket).
#   computeMarket() -> per-model aggregate (offers, price range, signal, neutral $-tier).
#   private nodes (broker.private[id], main.go:53) are EXCLUDED from /discover + /market.
#   The per-model price_tier mirrors assignPriceTiers: priceTier(cheapest active OUT-price,
#   external reference preferred else the live per-model median of online OUT-prices) -> 0..4,
#   so the /market row carries the SAME $-reading the cheapest provider's offer shows on
#   /discover (single source of truth; the homepage marketplace teaser renders it).
#
# Enforced by: cmd/rogerai-broker market/discover tests. (Doc spec; convertible to godog.)

Feature: Discovery — the public marketplace

  Scenario: /discover lists live public on-air offers
    Given nodes are on air for "gpt-oss-20b" and "qwen3-4b"
    When a consumer GETs /discover
    Then both models' public offers are listed with their node + price

  # Spec correction (deployed code is source of truth): computeDiscover LISTS every public
  # node with an Online flag (a consumer sees an offline station), so a stale node is shown
  # OFFLINE on /discover rather than omitted; only the /market AGGREGATE drops it.
  Scenario: A stale node reads OFFLINE on /discover and drops out of the /market aggregate
    Given a node on air for "gpt-oss-20b" that has not heartbeat within nodeTTL
    When a consumer GETs /discover
    Then that node is shown OFFLINE on /discover and omitted from the /market aggregate

  Scenario: A PRIVATE node is hidden from public discovery
    Given a node sharing "gpt-oss-20b" on a PRIVATE band
    When a consumer GETs /discover or /market
    Then the private node never appears (it is reachable only via its frequency code)

  Scenario: /market aggregates a per-model view with a signal reading
    Given several nodes on air for "gpt-oss-20b" at different prices
    When a consumer GETs /market
    Then "gpt-oss-20b" shows the aggregated offer count, price range, and a signal strength

  Scenario: /market is cached for a short TTL (public, shareable data)
    Given /market was just computed
    When another consumer GETs /market within the cache TTL
    Then the cached payload is served (computeMarket is not recomputed per hit)

  Scenario: The market signal reflects liveness/recency, not just presence
    Given two nodes for "gpt-oss-20b": one freshly probed, one going stale
    When /market is computed
    Then the fresher node contributes a stronger signal than the staling one

  # The per-model aggregate carries a NEUTRAL buyer-facing $-tier for the model's BEST
  # (cheapest) active out-price, graded vs the live per-model median (external reference
  # preferred) — the SAME priceTier the cheapest provider's offer shows on /discover, so
  # every surface agrees. A thin market (no reference, fewer than 3 online providers) WITHHOLDS
  # the tier (0). Earlier the /market aggregate carried no tier at all.
  Scenario Outline: /market carries a per-model neutral $-tier for the model's best price
    Given several nodes on air for "<model>" at out-prices "<prices>"
    When a consumer GETs /market
    Then "<model>" carries price_tier <tier>

    Examples:
      | model       | prices              | tier |
      | gpt-oss-20b  | 0.04,0.10,0.10,0.12 | 1    |
      | qwen3-4b     | 0.10,0.10,0.10,0.12 | 2    |
      | niche-model  | 0.10,0.50           | 0    |

  # Each /discover offer carries its MODALITY so the consumer's client + TUI can tell a VOICE
  # station (tts/stt) apart from a chat station and never (wrongly) offer a voice band as a chat
  # channel. A voice offer reads its real modality ("tts"/"stt"); a plain chat / pre-voice offer
  # reads the canonical "chat" (the back-compat empty modality is normalized, never a bare "").
  # GROUND TRUTH: enrichOffersForNode copies offerModality(o.Modality) into offerView.Modality.
  Scenario Outline: /discover exposes each offer's modality (voice vs chat)
    Given a node on air for "<model>" with modality "<registered>"
    When a consumer GETs /discover
    Then that offer carries modality "<seen>"

    Examples:
      | model              | registered | seen |
      | eager-puma-54-voice | tts        | tts  |
      | whisper-listener    | stt        | stt  |
      | gpt-oss-20b         | chat       | chat |
      | legacy-model        |            | chat |
