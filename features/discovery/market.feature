# DISCOVERY (public): how a consumer finds stations. GET /discover lists the live PUBLIC
# offers; GET /market is the per-model aggregated marketplace view (price + a signal-strength
# reading per model). Both are PUBLIC + cacheable. The invariant that matters: PRIVATE (band-
# only) nodes are NEVER listed here — they exist only behind their frequency code.
#
# GROUND TRUTH (cmd/rogerai-broker/market.go):
#   discover()  -> the live public offers (on-air, not private, within nodeTTL).
#   market() -> serveCachedJSON("market:"+query, publicMarketTTL, computeMarket).
#   computeMarket() -> per-model aggregate (offers, price range, signal).
#   private nodes (broker.private[id], main.go:53) are EXCLUDED from /discover + /market.
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
