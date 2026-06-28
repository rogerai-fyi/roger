# Routing SCORING + SELECTION: once the eligible candidates are known (see
# eligibility.feature), pickFor scores each and selects one. The score blends price,
# reliability, speed-fit, and an exploration lift, then divides by a load factor; a
# two-tier health gate keeps healthy nodes strictly ahead of probationary ones; and the
# final pick is power-of-two-choices (P2C) so load spreads instead of dogpiling the top.
#
# GROUND TRUTH (cmd/rogerai-broker/tunnel.go pickFor, second pass):
#   rangeMin/rangeMax = cheapest/dearest eligible OUTPUT price (free offers don't count).
#   rmax = max(rangeMax, maxPriceOut)   # a user cap widens the ceiling ("reward me below X").
#   per candidate:
#     pm    = priceMod(out, rangeMin, rmax, kPrice, priceExp)   # range-relative, cheaper=higher
#     score = ucb(rel*fit*pm, radius) * loadFactor(inflight, capacity)
#       rel    = reliabilityFactor(probed, probeOK, probeFails, successRate, score)  # the spine
#       fit    = speedFit(tps, ttftMs, promptTokens, speedMul)                       # speed-fit
#       radius = ucbRadius(...) ONLY when probed && probeOK (canary-passed); else 0   # explore
#       inflight = local b.inflight + b.peerInflight (cross-instance merged load)
#   Tier A = probeFails<2 AND (successRate unmeasured OR >=0.55); everything else on-air = Tier B.
#     select from Tier A; fall back to Tier B ONLY when Tier A is empty.
#   chosen = selectP2C(pool, beta, rng); chosen<0 => not found.
#
# Enforced by: cmd/rogerai-broker/router_test.go, probe_adaptive_test.go, pick_bench_test.go.

Feature: Routing scoring + selection

  Background:
    Given a broker with two eligible nodes on air for "gpt-oss-20b", both seen just now
    And both nodes are healthy (Tier A) with equal reliability, speed, and load

  # --- price ------------------------------------------------------------------
  Scenario: All else equal, the cheaper eligible offer is favored
    Given node "n-cheap" offers "gpt-oss-20b" at out-price $0.20/1M
    And node "n-dear" offers "gpt-oss-20b" at out-price $0.60/1M
    When many requests route for "gpt-oss-20b"
    Then "n-cheap" wins the majority of picks

  Scenario: A user price cap widens the reward range ceiling
    Given the eligible out-prices are $0.20/1M and $0.30/1M
    When a request routes for "gpt-oss-20b" with max-out $0.90/1M
    Then the price-mod ceiling (rmax) is $0.90/1M, not the eligible max $0.30/1M

  Scenario: Free offers price-tie (out<=0 contributes no range), broken by health/speed/load
    Given nodes "n-free-a" and "n-free-b" both offer "gpt-oss-20b" free
    When requests route for "gpt-oss-20b"
    Then neither node sets the price range, and the pick is decided by the other factors

  # --- reliability spine ------------------------------------------------------
  Scenario: A more reliable node outscores a flaky one at the same price
    Given node "n-solid" has a clean probe history and a 0.95 success rate
    And node "n-flaky" has a 0.60 success rate at the same price
    When many requests route for "gpt-oss-20b"
    Then "n-solid" wins the majority of picks

  # --- speed fit --------------------------------------------------------------
  Scenario: For a large prompt, the faster (higher t/s, lower TTFT) node fits better
    Given node "n-fast" runs at 90 t/s with low TTFT
    And node "n-slowish" runs at 40 t/s at the same price and reliability
    When a large-prompt request routes for "gpt-oss-20b"
    Then "n-fast" is favored by the speed-fit term

  # --- UCB exploration (canary-gated) ----------------------------------------
  Scenario: Exploration lift is granted only to canary-passed nodes
    Given node "n-proven" has been probed and passed (probed && probeOK)
    And node "n-unproven" has never passed a probe
    When scoring runs for "gpt-oss-20b"
    Then "n-proven" receives a UCB exploration radius
    And "n-unproven" receives a zero radius (we never explore unproven-flaky capacity)

  # --- load (capacity-aware, cross-instance) ---------------------------------
  Scenario: A more-loaded node is penalized by the load factor
    Given node "n-busy" has higher inflight relative to its capacity than "n-idle"
    When many requests route for "gpt-oss-20b"
    Then "n-idle" wins the majority of picks

  Scenario: Cross-instance peer load counts toward a node's load (multi-instance)
    Given node "n-shared" has 0 local inflight but high peer-instance inflight
    When a request routes for "gpt-oss-20b"
    Then the load factor reflects local + peer inflight, not just local

  # --- two-tier health gate ---------------------------------------------------
  Scenario: Healthy beats failing as an absolute gate
    Given node "n-healthy" is Tier A and node "n-probation" is Tier B (probeFails>=2)
    When a request routes for "gpt-oss-20b"
    Then "n-healthy" (Tier A) is selected even if "n-probation" would score higher

  Scenario: Tier B is used only when Tier A is empty (availability over silence)
    Given the only on-air node "n-probation" is Tier B (probation)
    When a request routes for "gpt-oss-20b"
    Then "n-probation" is selected (a transient blip never blanks a live model)

  # --- P2C selection ----------------------------------------------------------
  Scenario: Power-of-two-choices spreads load instead of dogpiling the single top node
    Given several Tier-A nodes have near-equal scores for "gpt-oss-20b"
    When many requests route for "gpt-oss-20b"
    Then picks spread across the top nodes (load-aware), not all onto one
    And the spread tightens or loosens with the beta knob

  Scenario: An empty selection pool returns not-found
    Given no candidate survives into either tier
    When a request routes for "gpt-oss-20b"
    Then selectP2C returns < 0 and pickFor reports found=false
