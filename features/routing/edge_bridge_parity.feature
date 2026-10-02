# ROUTING - EDGE / TOWER BRIDGE PARITY: every consumer constraint binds on the bridge path
# exactly as on the direct path. The fan-out coin chooses between two ELIGIBLE servers; it
# never widens eligibility.
#
# CONTRACT: features/routing/ROUTING-EXPRESSION-CONTRACT.md §6 (bridge parity), §1a (carriers
# stripped before a station sees the body), §5 (the constraints themselves), §7 (X-RogerAI-Model
# and the receipt). PROPOSED 2026-09-30, awaiting founder approval.
#
# GROUND TRUTH at origin/main 518c698b:
#   cmd/rogerai-broker/tunnel.go:1852-1874  pickFor runs with EVERY consumer constraint; then, when
#                                           a direct node was picked AND a Tower hosts the model, a
#                                           request-seeded coin sends half the traffic to
#                                           relayViaEdge(soft=true). tunnel.go:1875-1884: with no
#                                           direct pick, relayViaEdge(soft=false) is the fallback.
#   cmd/rogerai-broker/tunnel.go:1859-1864  edgeBridgeAuth carries ONLY: wallet, pubHex, grant,
#                                           sessionAuthed, confidentialOnly, maxPriceIn, maxPriceOut,
#                                           pinNode, freqBand, freeOrSelf.
#   cmd/rogerai-broker/edgebridge.go:76-102 grant, browser-session, anonymous, wallet mismatch, rate,
#                                           slot -> the bridge declines (soft) or refuses (hard).
#   cmd/rogerai-broker/edgebridge.go:139-166 confidential, pin, freq band, free/self -> decline.
#   cmd/rogerai-broker/edgebridge.go:166-168 "The client's failover exclusions are DIRECT node ids, a
#                                           different namespace from a Tower id, so they are not
#                                           applied here" - X-Roger-Exclude-Nodes is NOT honored.
#   cmd/rogerai-broker/edgebridge.go:189-199 the consumer's in/out price caps ARE honored per row.
#   cmd/rogerai-broker/toweredge.go:546-...  edgeTargetFor: routable rows for the model, pass one
#                                           authority re-check, pass two edgeEligible (stale, banned,
#                                           owner-banned, private, trust, load), then selectP2C.
#                                           NOTHING about min-tps, pref, quant, capability, params,
#                                           ctx, region, self-hosted.
#   cmd/rogerai-broker/edgebridge.go:126-130 bridged answer headers: Provider (relay name), Relay
#                                           (tower id), Cost, Tokens-In, Tokens-Out. No Model.
#   features/tower/edge_fanout.feature      the approved fan-out contract this file extends.
#
# THE DEFECT THIS FILE PINS: for half of the requests where a direct node was picked, the
# bridge may serve a request that pickFor filtered for min-tps, exclusions and pref, and none of
# those three reach the bridge. With the new knobs (quant, require, params_b, min_ctx,
# max_ttft_ms, trust_min, self_hosted_only, region, order/only/ignore/allow_fallbacks, sort,
# models[]) the same hole would widen. Parity means: a Tower row is evaluated under the SAME
# filters as a direct node; a row that cannot be evaluated (the attribute is undeclared) is
# INELIGIBLE under that filter, as a direct node with the attribute undeclared is.
#
# Enforced by: cmd/rogerai-broker/edge_bridge_parity_bdd_test.go (to be written; RED first).

Feature: The bridge honors every consumer constraint the direct path honors

  Background:
    Given a broker with the tower subsystem and a signed-in consumer who accepted the terms
    And a direct station "s1" serves "m", healthy, seen just now, out 1.00 per 1M
    And an approved Tower "t1" hosts "m" through station "t1-a" at out 1.00 per 1M
    And the fan-out coin is under test control

  # ============================================================================
  # the coin only chooses between eligible servers
  # ============================================================================

  Scenario: with no constraint, both fabrics may serve (the approved fan-out, unchanged)
    When 40 consumers relay for "m"
    Then some are served by "s1" and some through "t1"

  Scenario: the coin never widens eligibility - direct eligible, Tower not
    Given "t1-a" fails a constraint the consumer stated and "s1" passes it
    When 40 consumers relay with that constraint and the coin always says "edge"
    Then every relay is served by "s1"
    And the bridge was never entered

  Scenario: the coin never widens eligibility - Tower eligible, direct not
    Given "s1" fails a constraint the consumer stated and "t1-a" passes it
    When 40 consumers relay with that constraint
    Then every relay is served through "t1"
    And "s1" received nothing

  Scenario: neither fabric eligible is no_match, not a coin flip
    Given both "s1" and "t1-a" fail a constraint the consumer stated
    When a consumer relays with that constraint
    Then the response is 503 {"error":{"code":"no_match"}}
    And no station received anything and no hold was placed

  Scenario: the hard-mode bridge (no direct pick) is under the same constraints
    Given no direct station serves "m"
    And "t1-a" fails a constraint the consumer stated
    When a consumer relays with that constraint
    Then the response is 503 {"error":{"code":"no_match"}}
    And the bridge did not submit anything to "t1"'s hub

  Scenario: tower-to-tower fallback inside the bridge is under the same constraints
    Given no direct station serves "m"
    And Towers "t1" and "t2" host "m", "t1-a" 429s, and "t2-a" fails the consumer's constraint
    When a consumer relays with that constraint
    Then the bridge tried "t1" only
    And the response is the last upstream error with Retry-After

  # ============================================================================
  # per-constraint parity matrix
  # ============================================================================

  # --- price caps (already honored; pinned) -----------------------------------
  Scenario Outline: price caps bind on the bridge
    Given "t1-a" prices <tower_price> and "s1" prices <direct_price>
    When 40 consumers relay with <constraint>
    Then <outcome>
    Examples:
      | tower_price | direct_price | constraint                                   | outcome                                  |
      | out 5.00    | out 1.00     | provider.max_price.completion 2              | every relay is served by "s1"            |
      | out 1.00    | out 5.00     | provider.max_price.completion 2              | every relay is served through "t1"       |
      | out 5.00    | out 5.00     | provider.max_price.completion 2              | 503 no_match                             |
      | in 3.00     | in 0.50      | provider.max_price.prompt 1                  | every relay is served by "s1"            |
      | in 0.50     | in 3.00      | provider.max_price.prompt 1                  | every relay is served through "t1"       |
      | out 12.00   | out 1.00     | no max_price (the $10 default)               | every relay is served by "s1"            |
      | out 12.00   | out 12.00    | no max_price (the $10 default)               | 503 no_match                             |
      | out 12.00   | out 1.00     | provider.max_price.completion 15             | some relays ride "t1" (the cap admits it) |

  Scenario: the per-request cap lowers max_tokens on the Tower exactly as on a direct station (not a plan filter)
    # §1a: the cap sizes the hold and lowers the max_tokens each server is sent to what the cap
    # buys after the prompt's input cost; the Tower row stays a candidate with a smaller ceiling.
    Given "t1-a" prices out 8.00 and "s1" prices out 1.00, both in 0.10
    When 40 consumers relay a 100-token prompt with max_tokens 1000 and provider.max_price.request 0.002
    Then relays served through "t1" carried a max_tokens no larger than what $0.002 buys at 8.00/1M after the input cost
    And relays served by "s1" carried a max_tokens no larger than what $0.002 buys at 1.00/1M after the input cost
    And every hold placed was $0.002

  Scenario: the per-request cap drops the Tower row only when the prompt's input cost alone meets the cap
    Given "t1-a" prices in 0.50 and "s1" prices in 0.10
    When 40 consumers relay a 20000-token prompt with provider.max_price.request 0.004
    Then every relay is served by "s1"
    # 20000 tokens at in 0.50/1M = 0.010 >= 0.004: the Tower can buy no output; at 0.10 it can
    And /admin/live shows edge_bridge_declined{max_price_request} 40

  Scenario: the per-request cap that neither fabric can buy output under is no_match with no hold
    When a consumer relays a 20000-token prompt with provider.max_price.request 0.00001
    Then the response is 503 {"error":{"code":"no_match"}}
    And no hold was placed

  # --- pin / order / only / ignore / allow_fallbacks ---------------------------
  Scenario: a pin to a direct node declines the bridge (pinned; unchanged)
    When 40 consumers relay with X-Roger-Node "s1"
    Then every relay is served by "s1"

  Scenario: a pin to a Tower relay id serves through that Tower
    When 40 consumers relay with X-Roger-Node "t1"
    Then every relay is served through "t1"
    And "s1" received nothing

  Scenario: order listing the Tower first serves through the Tower
    When 40 consumers relay with provider.order ["t1", "s1"]
    Then every relay is served through "t1"

  Scenario: order listing the direct node first serves direct
    When 40 consumers relay with provider.order ["s1", "t1"]
    Then every relay is served by "s1"

  Scenario: order listing the Tower first falls to the direct node when the Tower 429s
    Given "t1-a" 429s
    When a consumer relays with provider.order ["t1", "s1"]
    Then the bridge tried "t1" and the relay was served by "s1"
    And the consumer sees 200

  Scenario: only naming the direct node excludes the bridge
    When 40 consumers relay with provider.only ["s1"]
    Then every relay is served by "s1"
    And the bridge was never entered

  Scenario: only naming the Tower excludes the direct node
    When 40 consumers relay with provider.only ["t1"]
    Then every relay is served through "t1"
    And "s1" received nothing

  Scenario: only naming neither is no_match
    When a consumer relays with provider.only ["s9"]
    Then the response is 503 {"error":{"code":"no_match"}}

  Scenario: ignore naming the Tower excludes the bridge
    When 40 consumers relay with provider.ignore ["t1"]
    Then every relay is served by "s1"

  Scenario: ignore naming the direct node sends everything through the Tower
    When 40 consumers relay with provider.ignore ["s1"]
    Then every relay is served through "t1"

  Scenario: X-Roger-Exclude-Nodes naming the direct node sends everything through the Tower
    When 40 consumers relay with X-Roger-Exclude-Nodes "s1"
    Then every relay is served through "t1"

  Scenario: X-Roger-Exclude-Nodes naming the Tower excludes the bridge (regression pin, edgebridge.go:166-168)
    When 40 consumers relay with X-Roger-Exclude-Nodes "t1"
    Then every relay is served by "s1"
    And the bridge was never entered
    # today the header is documented as not applied on the bridge; parity applies it

  Scenario: a client-proxy failover exclusion of a Tower is honored on the retry
    Given the local proxy's previous attempt was served through "t1" and failed with a 5xx
    When the proxy retries with X-Roger-Exclude-Nodes "t1"
    Then the retry is served by "s1"

  Scenario: allow_fallbacks false with the Tower named is one bridged attempt and no direct fallback
    Given "t1-a" 429s with Retry-After 6
    When a consumer relays with provider.order ["t1"] and provider.allow_fallbacks false
    Then the bridge tried "t1" once
    And the response is 429 with Retry-After: 6
    And "s1" received nothing

  Scenario: allow_fallbacks false with the direct node named never enters the bridge
    Given "s1" 429s
    When a consumer relays with provider.order ["s1"] and provider.allow_fallbacks false
    Then exactly one attempt was made, to "s1"
    And the bridge was never entered

  Scenario: allow_fallbacks false with nothing named and the coin saying edge is one bridged attempt
    Given "t1-a" 429s
    When a consumer relays with provider.allow_fallbacks false and the coin says "edge"
    Then the bridge tried "t1" once and the response is 429
    And "s1" received nothing
    # the consumer accepted one server; the coin picked it; no second server is added

  Scenario: the soft-mode single-Tower try still respects allow_fallbacks true (unchanged)
    Given "t1-a" 429s
    When a consumer relays with the coin saying "edge"
    Then the bridge tried "t1" once and fell back to "s1"

  # --- sort and pref ----------------------------------------------------------
  Scenario: sort price ranks the Tower row and the direct node on one scale
    Given "t1-a" prices out 0.50 and "s1" prices out 1.00
    When 40 consumers relay with provider.sort "price"
    Then every relay is served through "t1"

  Scenario: sort price picks the direct node when it is cheaper
    Given "t1-a" prices out 2.00 and "s1" prices out 1.00
    When 40 consumers relay with provider.sort "price"
    Then every relay is served by "s1"

  Scenario: sort throughput ranks by the measured tps of the node behind the Tower row
    Given the node behind "t1-a" measures 80 tok/s and "s1" measures 30 tok/s
    When 40 consumers relay with provider.sort "throughput"
    Then every relay is served through "t1"

  Scenario: sort throughput puts an unmeasured Tower row last
    Given the node behind "t1-a" has no tps measurement and "s1" measures 30 tok/s
    When 40 consumers relay with provider.sort "throughput"
    Then every relay is served by "s1"

  Scenario: sort latency ranks by the measured TTFT of the node behind the Tower row
    Given the node behind "t1-a" measures 200ms TTFT and "s1" 900ms
    When 40 consumers relay with provider.sort "latency"
    Then every relay is served through "t1"

  Scenario: a strict sort replaces the coin with the ranking - a Tower row is one more candidate
    Given "t1-a" and "s1" are priced identically and "s1" has the better score
    When 40 consumers relay with provider.sort "price"
    Then every relay is served by "s1"
    # the coin is a load-spreading device; a strict sort is the consumer declining it. The
    # Tower row is not excluded - it is ranked by its own price/tps/ttft like any candidate
    # (the two scenarios above show it winning) and loses only the tie, on score.

  Scenario: a strict sort never prefers the Tower row either
    Given "t1-a" is priced at out $0.60 and "s1" at out $0.50 for "m"
    When 40 consumers relay with provider.sort "price"
    Then every relay is served by "s1"
    And /admin/live edge_coin_flips did not change (the coin is never consulted under a strict sort)

  Scenario: pref is carried to the bridge's ranking
    Given Towers "t1" (node tps 80, out 2.00) and "t2" (node tps 20, out 0.50) host "m" and no direct station
    When 200 consumers relay with roger.pref "fast"
    Then "t1" serves more often than under balanced
    When 200 consumers relay with roger.pref "cheap"
    Then "t2" serves more often than under balanced
    # regression pin: today edgeBridgeAuth has no pref field; edgeTargetFor ranks with fixed weights

  Scenario: pref does not disable the coin
    When 100 consumers relay with roger.pref "cheap" and both fabrics price identically
    Then some relays ride "t1" and some "s1"

  # --- quantizations ----------------------------------------------------------
  Scenario: quantizations binds on the bridge - Tower row wrong quant
    Given "t1-a" declares quant "Q4_K_M" and "s1" declares quant "Q8_0"
    When 40 consumers relay with provider.quantizations ["Q8_0"]
    Then every relay is served by "s1"

  Scenario: quantizations binds on the bridge - direct wrong quant
    Given "t1-a" declares quant "Q8_0" and "s1" declares quant "Q4_K_M"
    When 40 consumers relay with provider.quantizations ["Q8_0"]
    Then every relay is served through "t1"

  Scenario: a Tower row with no quant declared is ineligible under a quant filter
    Given "t1-a" declares no quant and "s1" declares quant "Q8_0"
    When 40 consumers relay with provider.quantizations ["Q8_0"]
    Then every relay is served by "s1"

  Scenario: both fabrics undeclared under a quant filter is no_match
    Given neither "t1-a" nor "s1" declares a quant
    When a consumer relays with provider.quantizations ["Q8_0"]
    Then the response is 503 {"error":{"code":"no_match"}}

  Scenario: quant matching on the bridge is case-insensitive like the direct path
    Given "t1-a" declares quant "q8_0" and no direct station serves "m"
    When a consumer relays with provider.quantizations ["Q8_0"]
    Then the relay is served through "t1"

  # --- require: explicit and implicit -----------------------------------------
  Scenario: require tools binds on the bridge - Tower row unverified
    Given the node behind "t1-a" never passed a tool-call canary and "s1" did
    When 40 consumers relay with roger.require ["tools"]
    Then every relay is served by "s1"

  Scenario: require tools binds on the bridge - direct unverified
    Given the node behind "t1-a" passed a tool-call canary and "s1" did not
    When 40 consumers relay with roger.require ["tools"]
    Then every relay is served through "t1"

  Scenario: a Tower row's self-declared tools does not count (stripped at register, same as direct)
    Given "t1-a" declares "tools" but its node never passed a canary, and "s1" passed
    When 40 consumers relay with roger.require ["tools"]
    Then every relay is served by "s1"

  Scenario: the implicit tools requirement (a body with tools) binds on the bridge
    Given the node behind "t1-a" never passed a tool-call canary and "s1" did
    When 40 consumers relay with a non-empty tools array and no roger.require
    Then every relay is served by "s1"
    # today: the bridge would serve half of these on a node that cannot call tools

  Scenario: the implicit vision requirement (an image_url part) binds on the bridge
    Given "t1-a" does not declare "vision" and "s1" does
    When 40 consumers relay with an image_url content part and no roger.require
    Then every relay is served by "s1"

  Scenario: require vision binds on the bridge - direct lacks it
    Given "t1-a" declares "vision" and "s1" does not
    When 40 consumers relay with roger.require ["vision"]
    Then every relay is served through "t1"

  Scenario: require on both fabrics lacking is no_match
    Given neither "t1-a" nor "s1" has "vision"
    When a consumer relays with roger.require ["vision"]
    Then the response is 503 {"error":{"code":"no_match"}}

  Scenario: require_parameters true with tool_choice binds on the bridge
    Given the node behind "t1-a" never passed a tool-call canary and "s1" did
    When 40 consumers relay with tool_choice "auto", tools, and provider.require_parameters true
    Then every relay is served by "s1"

  # --- params_b ---------------------------------------------------------------
  @slice2
  Scenario: params_b binds on the bridge - Tower row out of range
    Given "t1-a" declares params_b 7 and "s1" declares params_b 32
    When 40 consumers relay with roger.params_b [20, 70]
    Then every relay is served by "s1"

  @slice2
  Scenario: params_b binds on the bridge - direct out of range
    Given "t1-a" declares params_b 32 and "s1" declares params_b 7
    When 40 consumers relay with roger.params_b [20, 70]
    Then every relay is served through "t1"

  @slice2
  Scenario: a Tower row with no params_b is ineligible under a params filter
    Given "t1-a" declares no params_b and "s1" declares params_b 32
    When 40 consumers relay with roger.params_b [20, 70]
    Then every relay is served by "s1"

  @slice2
  Scenario: params_b inclusive bounds apply identically on both fabrics
    Given "t1-a" declares params_b 70 and "s1" declares params_b 20
    When 40 consumers relay with roger.params_b [20, 70]
    Then some relays ride "t1" and some "s1"

  # --- min_ctx ----------------------------------------------------------------
  @slice2
  Scenario: min_ctx binds on the bridge - Tower row too small
    Given "t1-a" declares ctx 8192 and "s1" declares ctx 65536
    When 40 consumers relay with roger.min_ctx 32768
    Then every relay is served by "s1"

  @slice2
  Scenario: min_ctx binds on the bridge - direct too small
    Given "t1-a" declares ctx 65536 and "s1" declares ctx 8192
    When 40 consumers relay with roger.min_ctx 32768
    Then every relay is served through "t1"

  @slice2
  Scenario: an estimated ctx on a Tower row is unknown under min_ctx, ineligible
    Given "t1-a"'s ctx 65536 is an estimate and "s1" declares ctx 65536
    When 40 consumers relay with roger.min_ctx 32768
    Then every relay is served by "s1"

  Scenario: the measured-prompt declared-window gate applies on the bridge (unchanged intent)
    # §3: when every server on air for the model is too small the answer is the 400 "exceeds
    # the context window" naming the widest window, as on the direct path, not a no_match.
    Given "t1-a" declares ctx 4096 and no direct station serves "m"
    When a consumer relays an 8000-token prompt
    Then the response is 400 "request exceeds the context window" naming 4096
    And the bridge did not submit anything to "t1"'s hub
    And no hold was placed and no operator is struck

  # --- min_tps and max_ttft_ms (measured; unmeasured passes) -------------------
  Scenario: min_tps binds on the bridge (regression pin - today it does not)
    Given the node behind "t1-a" measures 10 tok/s and "s1" measures 40 tok/s
    When 40 consumers relay with roger.min_tps 20
    Then every relay is served by "s1"
    And the bridge was never entered

  Scenario: X-Roger-Min-TPS header binds on the bridge too
    Given the node behind "t1-a" measures 10 tok/s and "s1" measures 40 tok/s
    When 40 consumers relay with X-Roger-Min-TPS 20
    Then every relay is served by "s1"

  Scenario: min_tps binds on the bridge - direct too slow
    Given the node behind "t1-a" measures 40 tok/s and "s1" measures 10 tok/s
    When 40 consumers relay with roger.min_tps 20
    Then every relay is served through "t1"

  Scenario: an unmeasured Tower row passes min_tps, as an unmeasured direct node does
    Given the node behind "t1-a" has no tps measurement and "s1" measures 10 tok/s
    When 40 consumers relay with roger.min_tps 20
    Then every relay is served through "t1"

  @slice2
  Scenario: max_ttft_ms binds on the bridge - Tower row too slow to first token
    Given the node behind "t1-a" measures 3000ms TTFT and "s1" 400ms
    When 40 consumers relay with roger.max_ttft_ms 1000
    Then every relay is served by "s1"

  @slice2
  Scenario: max_ttft_ms binds on the bridge - direct too slow
    Given the node behind "t1-a" measures 300ms TTFT and "s1" 3000ms
    When 40 consumers relay with roger.max_ttft_ms 1000
    Then every relay is served through "t1"

  @slice2
  Scenario: an unmeasured TTFT on a Tower row passes max_ttft_ms
    Given the node behind "t1-a" has no TTFT measurement and "s1" measures 3000ms
    When 40 consumers relay with roger.max_ttft_ms 1000
    Then every relay is served through "t1"

  # --- trust_min --------------------------------------------------------------
  @slice2
  Scenario: trust_min verified binds on the bridge - Tower node not canary-verified
    Given the node behind "t1-a" is not verified and "s1" is verified
    When 40 consumers relay with roger.trust_min "verified"
    Then every relay is served by "s1"

  @slice2
  Scenario: trust_min verified binds on the bridge - direct not verified
    Given the node behind "t1-a" is verified and "s1" is not
    When 40 consumers relay with roger.trust_min "verified"
    Then every relay is served through "t1"

  @slice2
  Scenario: trust_min confidential declines the bridge outright (a Tower is a third party)
    Given "s1" is TEE-attested
    When 40 consumers relay with roger.trust_min "confidential"
    Then every relay is served by "s1"
    And the bridge was never entered

  Scenario: roger.confidential true declines the bridge (unchanged; header pinned)
    Given "s1" is TEE-attested
    When 40 consumers relay with roger.confidential true
    Then every relay is served by "s1"
    When 40 consumers relay with X-Roger-Confidential 1
    Then every relay is served by "s1"

  Scenario: confidential with no attested direct station is no_match even though a Tower hosts the model
    Given "s1" is not TEE-attested
    When a consumer relays with roger.confidential true
    Then the response is 503 {"error":{"code":"no_match"}}
    And the message says no confidential station matches

  # corrected 2026-10-01 (founder-approved): kept as written; contract §6 now states the two-tier health gate holds ACROSS fabrics.
  Scenario: the Tier A before Tier B gate holds across fabrics
    Given the node behind "t1-a" is Tier B and "s1" is Tier A
    When 40 consumers relay for "m"
    Then every relay is served by "s1"

  # --- self_hosted_only -------------------------------------------------------
  Scenario: self_hosted_only excludes a curated Tower station
    Given "t1-a" is a curated (commercial-proxy) station and "s1" is a human station
    When 40 consumers relay with roger.self_hosted_only true
    Then every relay is served by "s1"

  Scenario: self_hosted_only excludes a curated direct station and admits a human Tower station
    Given "s1" is curated and "t1-a" is a human station
    When 40 consumers relay with roger.self_hosted_only true
    Then every relay is served through "t1"

  Scenario: self_hosted_only with both curated is no_match
    Given both "s1" and "t1-a" are curated
    When a consumer relays with roger.self_hosted_only true
    Then the response is 503 {"error":{"code":"no_match"}}

  Scenario: self_hosted_only false is the default and changes nothing
    Given "t1-a" is curated
    When 40 consumers relay with roger.self_hosted_only false
    Then some relays ride "t1" and some "s1"

  # --- region -----------------------------------------------------------------
  @slice2
  Scenario: region binds on the bridge - Tower row elsewhere
    Given "t1-a" declares region "us" and "s1" declares region "eu"
    When 40 consumers relay with roger.region ["eu"]
    Then every relay is served by "s1"

  @slice2
  Scenario: region binds on the bridge - direct elsewhere
    Given "t1-a" declares region "eu" and "s1" declares region "us"
    When 40 consumers relay with roger.region ["eu"]
    Then every relay is served through "t1"

  @slice2
  Scenario: a Tower row with no region is ineligible under a region filter
    Given "t1-a" declares no region and "s1" declares region "eu"
    When 40 consumers relay with roger.region ["eu"]
    Then every relay is served by "s1"

  @slice2
  Scenario: region with several values admits any listed region on either fabric
    Given "t1-a" declares region "us" and "s1" declares region "eu"
    When 40 consumers relay with roger.region ["eu", "us"]
    Then some relays ride "t1" and some "s1"

  # --- freq (private band) ----------------------------------------------------
  Scenario: a private-band request never rides a public Tower (unchanged; pinned)
    Given a private band "B" with station "p1" for "m"
    When 40 consumers relay with roger.freq for band "B"
    Then every relay is served by "p1"
    And the bridge was never entered

  Scenario: a private-band request whose only station is cooling is band cooling, not a Tower
    Given a private band "B" with station "p1" for "m" and "p1" is cooling for 7 more seconds
    When a consumer relays with roger.freq for band "B"
    Then the response is 503 {"error":{"code":"band_cooling"}} with Retry-After: 7
    And the bridge was never entered

  Scenario: the body roger.freq declines the bridge exactly as the X-Roger-Freq header does
    Given a private band "B" with station "p1" for "m"
    When 40 consumers relay with X-Roger-Freq for band "B"
    Then every relay is served by "p1" and the bridge was never entered

  # --- models[] ---------------------------------------------------------------
  Scenario: a models[] plan may cross fabrics between models
    Given "s1" serves "m" only and "t1" hosts "m2" only, and "s1" 429s
    When a consumer relays with model "m" and models ["m2"]
    Then attempt 1 hit "s1" for "m" and attempt 2 rode "t1" for "m2"
    And X-RogerAI-Model is "m2" and X-RogerAI-Relay is "t1"

  Scenario: a models[] entry with no eligible server on either fabric is skipped silently
    Given no station or Tower serves "m0", "s1" serves "m"
    When a consumer relays with model "m0" and models ["m"]
    Then the relay is served by "s1" with X-RogerAI-Model "m"

  Scenario: the bridge honors the per-model constraints of the entry it serves
    Given "t1" hosts "m2" with quant "Q4_K_M" and "s1" serves "m" with quant "Q8_0", "s1" 429s
    When a consumer relays with model "m", models ["m2"], and provider.quantizations ["Q8_0"]
    Then attempt 1 hit "s1" and there is no attempt 2
    And the response is 429 with Retry-After

  Scenario: the hold for a cross-fabric plan covers the priciest pair including the Tower's price
    Given "s1" serves "m" at out 1.00 and "t1" hosts "m2" at out 4.00
    When a consumer relays with model "m" and models ["m2"]
    Then the hold covers 4.00 per 1M
    And on success at "s1" the consumer is charged at 1.00

  # ============================================================================
  # what the Tower sees and what the consumer learns
  # ============================================================================

  Scenario: the carriers are stripped before the Tower's hub sees the sealed body
    When a consumer relays with provider.order ["t1"], roger.pref "cheap", roger.require ["tools"], models ["m2"]
    Then the plaintext sealed to "t1-a" has no "provider", "roger" or "models" key
    And its "model" is the served model id
    And it is otherwise byte-identical to what the consumer sent

  Scenario: a bridged answer carries X-RogerAI-Model
    When a consumer relays and the bridge serves through "t1"
    Then X-RogerAI-Model is "m"
    And X-RogerAI-Provider names the relay and X-RogerAI-Relay names "t1"

  Scenario: a bridged answer's receipt names the served model, the relay and the Tower
    When a consumer relays and the bridge serves through "t1"
    Then the receipt names model "m", the relay, and Tower "t1"
    And the receipt's model equals X-RogerAI-Model

  Scenario: a bridged receipt is signed by the Tower's relay key and co-signed by the broker, and VerifyBroker covers it
    # §7: the node signature on a bridged receipt is the party the broker dispatched to, the
    # Tower's relay key; the broker co-signs exactly as on the direct path. Today a bridged
    # answer carries no receipt at all (edgebridge.go:126-130) - this is the pin.
    When a consumer relays and the bridge serves through "t1"
    Then the receipt's node signature verifies against "t1"'s relay public key
    And the receipt's broker signature verifies under VerifyBroker with the same key version scheme as a direct receipt
    And the receipt does not verify against any direct station's key

  Scenario: a bridged streaming answer carries the final usage chunk with the served model
    When a consumer relays with "stream": true and the bridge serves through "t1"
    Then the final usage chunk names model "m" and relay "t1"

  Scenario: a bridged 503 no_match carries X-RogerAI-Cost 0 and no receipt
    Given no direct station serves "m" and "t1-a" fails the consumer's constraint
    When a consumer relays with that constraint
    Then X-RogerAI-Cost is 0 and there is no receipt

  # ============================================================================
  # the gates that keep the bridge off the path (unchanged, pinned as parity)
  # ============================================================================

  Scenario Outline: a bridge-ineligible caller is served direct or refused, never widened by a constraint
    Given the caller is <caller>
    When 40 such callers relay with provider.order ["t1", "s1"]
    Then every relay is <outcome>
    And the bridge was never entered
    Examples:
      | caller                                  | outcome                                       |
      | a grant holder                          | served by "s1" if the grant covers it         |
      | a browser-session (Playbox) caller      | served by "s1"                                |
      | anonymous                               | served by "s1" if free, else refused as today |
      | a free or self-use ($0) caller          | served by "s1"                                |
    # naming a Tower in order cannot make a grant, a browser session, an anonymous caller
    # or $0 traffic ride the edge; the order entry is simply ineligible for that caller

  Scenario: a grant holder naming only the Tower is the grant's own refusal, not a bridge ride
    Given a grant scoped to nodes ["s1"]
    When the grant holder relays with provider.only ["t1"]
    Then the response is 503 {"error":{"code":"no_match"}}
    And the bridge was never entered

  Scenario: an anonymous caller naming only a Tower gets the uniform 503 no_match - nothing discloses that "t1" is a Tower
    # §6: a 403 "tower inference requires a signed-in account" would tell an anonymous caller
    # which ids are Towers; the refusal is the same no_match any unknown or ineligible id gets.
    When an anonymous caller relays with provider.only ["t1"]
    Then the response is 503 {"error":{"code":"no_match"}}
    And the response body and X-RogerAI-Cost "0" are identical to those for provider.only ["no-such-id"]
    And no hold was placed
    And nothing was submitted to "t1"'s hub

  Scenario: free or self-use traffic naming the Tower first still never bills a Tower
    Given the consumer owns "s1"
    When the owner relays with provider.order ["t1", "s1"]
    Then the relay is served by "s1" at $0
    And the bridge was never entered

  # ============================================================================
  # namespaces and spoofing
  # ============================================================================

  Scenario: a Tower cannot spoof a direct node id to appear in only
    Given a Tower station whose relay name is "s1"
    When 40 consumers relay with provider.only ["s1"]
    Then every relay is served by the direct station "s1"
    And the bridge was never entered

  Scenario: a Tower cannot spoof a direct node id to dodge ignore
    Given a Tower "t1" whose station relay name is "s1" and the consumer ignores "t1"
    When 40 consumers relay with provider.ignore ["t1"]
    Then every relay is served by the direct station "s1"

  Scenario: a Tower id and a node id that collide are matched in their own namespaces
    Given a direct node with id "t1" and a Tower with id "t1" both serve "m"
    When 40 consumers relay with provider.only ["t1"]
    Then relays may be served by either, and each is matched in its own namespace
    And the operator is warned once on /admin/live about the id collision

  @slice2
  Scenario: a Tower cannot declare attributes it does not have to pass a filter it would fail
    Given "t1-a" declares quant "Q8_0" and params_b 70 but its known-model table entry says 7B
    When 40 consumers relay with roger.params_b [60, 80]
    Then the row is flagged params_estimated false with a mismatch on /admin/live
    And the row is ineligible under the filter until the declaration is corrected

  # ============================================================================
  # telemetry
  # ============================================================================

  Scenario: /admin/live counts every fan-out coin flip
    Given 10 relays where both a direct station and the Tower row were eligible under balanced pref
    And 10 relays with provider.sort "price" where both were eligible
    When the founder reads /admin/live
    Then edge_coin_flips increased by exactly 10

  Scenario: /admin/live counts bridge declines by constraint
    Given 5 relays where the Tower row failed min_tps and 3 where it failed require
    When the founder reads /admin/live
    Then it shows edge_bridge_declined{min_tps} 5 and edge_bridge_declined{require} 3

  Scenario: a bridge decline for a constraint is logged once per request, without the band code
    When a consumer relays with roger.min_tps 20 and the Tower row is too slow
    Then exactly one log line names the request, "t1", and "min_tps"
    And no log line contains a band code

  Scenario: parity does not change the approved fan-out numbers when no constraint is given
    When 1000 consumers relay for "m" with no routing object
    Then the share served through "t1" is within the approved fan-out band of edge_fanout.feature
