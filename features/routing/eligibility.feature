# Routing ELIGIBILITY: the hard filters that decide which on-air nodes are even
# candidates for a request, BEFORE any scoring. These are not score-able knobs - each
# one gates a node IN or OUT. If no node survives every gate, the relay returns a clean
# "no station serving" (404-ish) rather than dispatching into a failure.
#
# GROUND TRUTH (cmd/rogerai-broker/tunnel.go pickFor, the per-relay routing pass):
#   For each registered node, in order, SKIP (ineligible) when ANY holds:
#     - time.Since(lastSeen) >= nodeTTL            (stale: missed its heartbeat window)
#     - banned[nodeID]                              (the node is ejected)
#     - len(bannedOwners)>0 AND owner(node) banned  (anti-rotation: a banned operator's
#                                                    fresh node id is still refused; the
#                                                    owner lookup is SKIPPED when no owner
#                                                    is banned, so the common case is free)
#     - private[nodeID] AND NOT privateAllow[nodeID] (band-only node, caller lacks the code)
#     - pin != "" AND nodeID != pin                 (a pinned request takes only that node)
#     - exclude[nodeID]                             (caller's retry exclusion set)
#     - allow != nil AND NOT allow[nodeID]          (caller's allow-list, when present)
#     - confidentialOnly AND NOT confidential[nodeID] (TEE-attested-only request)
#     - trust.probeFails >= probeDeadStreak         (NOT-SERVING: dead/unloaded upstream)
#     - minTPS > 0 AND tps > 0 AND tps < minTPS     (too slow for the caller's floor)
#   Then, per OFFER on the surviving node, SKIP the offer when:
#     - offer.Model != model                        (wrong model)
#     - maxPriceIn  > 0 AND in  > maxPriceIn         (over the caller's input price cap)
#     - maxPriceOut > 0 AND out > maxPriceOut        (over the caller's output price cap)
#   A node with NO surviving offer contributes no candidate. Zero candidates => not found.
#
# Enforced by: cmd/rogerai-broker/router_test.go (+ multiinstance_test.go for peer load).

Feature: Routing eligibility — the hard filters before scoring

  Background:
    Given a broker with an empty in-memory node registry
    And the fee rate is 30%

  # --- liveness ---------------------------------------------------------------
  Scenario: A fresh node serving the model is eligible
    Given node "n-live" is on air for "gpt-oss-20b" and was seen just now
    When a request routes for "gpt-oss-20b"
    Then "n-live" is a candidate

  Scenario: A node past its heartbeat window is excluded as stale
    Given node "n-stale" is on air for "gpt-oss-20b" but was last seen 2*nodeTTL ago
    When a request routes for "gpt-oss-20b"
    Then "n-stale" is NOT a candidate
    And the request finds no station serving

  # --- bans -------------------------------------------------------------------
  Scenario: A banned node is excluded
    Given node "n-bad" is on air for "gpt-oss-20b" and was seen just now
    And node "n-bad" is banned
    When a request routes for "gpt-oss-20b"
    Then "n-bad" is NOT a candidate

  Scenario: Anti-rotation — a banned operator's fresh node id is still refused
    Given operator "op-1" is banned
    And node "n-fresh" is owned by "op-1", on air for "gpt-oss-20b", seen just now
    When a request routes for "gpt-oss-20b"
    Then "n-fresh" is NOT a candidate

  Scenario: The owner-ban lookup is skipped entirely when no operator is banned
    Given no operator is banned
    And node "n-ok" is owned by "op-2", on air for "gpt-oss-20b", seen just now
    When a request routes for "gpt-oss-20b"
    Then "n-ok" is a candidate
    And no AccountOfNode store lookup was performed

  # --- private bands ----------------------------------------------------------
  Scenario: A private (band-only) node is hidden from a public request
    Given node "n-priv" is private and on air for "gpt-oss-20b", seen just now
    When a public request routes for "gpt-oss-20b" with no band code
    Then "n-priv" is NOT a candidate

  Scenario: A private node IS eligible to a caller who presents its band code
    Given node "n-priv" is private and on air for "gpt-oss-20b", seen just now
    When a request routes for "gpt-oss-20b" with "n-priv" in the privateAllow set
    Then "n-priv" is a candidate

  # --- caller routing constraints --------------------------------------------
  Scenario: A pinned request takes only the pinned node
    Given nodes "n-a" and "n-b" are on air for "gpt-oss-20b", seen just now
    When a request routes for "gpt-oss-20b" pinned to "n-a"
    Then "n-a" is a candidate
    And "n-b" is NOT a candidate

  Scenario: An excluded node (retry set) is skipped
    Given nodes "n-a" and "n-b" are on air for "gpt-oss-20b", seen just now
    When a request routes for "gpt-oss-20b" excluding "n-a"
    Then "n-a" is NOT a candidate
    And "n-b" is a candidate

  Scenario: An allow-list, when present, drops every node not in it
    Given nodes "n-a" and "n-b" are on air for "gpt-oss-20b", seen just now
    When a request routes for "gpt-oss-20b" allowing only "n-b"
    Then "n-b" is a candidate
    And "n-a" is NOT a candidate

  # --- confidential (TEE) -----------------------------------------------------
  Scenario: A confidential-only request excludes non-attested nodes
    Given node "n-plain" is on air for "gpt-oss-20b", seen just now, NOT TEE-attested
    And node "n-tee" is on air for "gpt-oss-20b", seen just now, TEE-attested
    When a confidential-only request routes for "gpt-oss-20b"
    Then "n-tee" is a candidate
    And "n-plain" is NOT a candidate

  # --- health / not-serving ---------------------------------------------------
  Scenario: A node failing the dead-probe streak is excluded entirely (clean no-serve)
    Given node "n-dead" is on air for "gpt-oss-20b", seen just now
    And "n-dead" has probeFails >= probeDeadStreak (its upstream is dead)
    When a request routes for "gpt-oss-20b"
    Then "n-dead" is NOT a candidate
    And a single recovered probe makes "n-dead" eligible again on the next pick

  # --- speed floor ------------------------------------------------------------
  Scenario: A node slower than the caller's min-tps floor is excluded
    Given node "n-slow" is on air for "gpt-oss-20b", seen just now, measured at 20 t/s
    When a request routes for "gpt-oss-20b" with min-tps 40
    Then "n-slow" is NOT a candidate

  Scenario: An UNMEASURED node is not excluded by min-tps (tps==0 is not "too slow")
    Given node "n-new" is on air for "gpt-oss-20b", seen just now, with no tps reading yet
    When a request routes for "gpt-oss-20b" with min-tps 40
    Then "n-new" is a candidate

  # --- model + price caps -----------------------------------------------------
  Scenario: A node not offering the requested model contributes no candidate
    Given node "n-other" is on air ONLY for "qwen3-4b", seen just now
    When a request routes for "gpt-oss-20b"
    Then "n-other" is NOT a candidate

  Scenario: An offer over the caller's output price cap is skipped
    Given node "n-pricey" offers "gpt-oss-20b" at out-price $0.80/1M, seen just now
    When a request routes for "gpt-oss-20b" with max-out $0.50/1M
    Then "n-pricey" is NOT a candidate

  Scenario: A free offer (out<=0) never moves the eligible price range
    Given node "n-free" offers "gpt-oss-20b" free (out-price 0), seen just now
    And node "n-paid" offers "gpt-oss-20b" at out-price $0.30/1M, seen just now
    When a request routes for "gpt-oss-20b"
    Then both nodes are candidates
    And the price range min/max is derived from the paid offer only

  # --- the empty result -------------------------------------------------------
  Scenario: No surviving candidate returns a clean not-found (no dispatch)
    Given every on-air node for "gpt-oss-20b" is excluded by some gate
    When a request routes for "gpt-oss-20b"
    Then pickFor returns found=false
    And the relay answers "no station serving" rather than dispatching into a failure
