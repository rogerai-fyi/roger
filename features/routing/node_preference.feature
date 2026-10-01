# ROUTING - NODE PREFERENCE: `provider.only`, `provider.ignore`, `provider.order`,
# `provider.allow_fallbacks`, `provider.sort`, `roger.pref` (and the header equivalents
# X-Roger-Node, X-Roger-Exclude-Nodes, X-Roger-Pref).
#
# CONTRACT: features/routing/ROUTING-EXPRESSION-CONTRACT.md §1a (precedence and validation),
# §2 (error codes), §5 (node selection semantics), §6 (bridge parity, spec'd in
# edge_bridge_parity.feature). PROPOSED 2026-09-30, awaiting founder approval.
#
# GROUND TRUTH at origin/main 518c698b (what exists, and what this file changes):
#   cmd/rogerai-broker/tunnel.go:1832-1833  pinNode := X-Roger-Node; exclude := parseNodeSet(X-Roger-Exclude-Nodes)
#   cmd/rogerai-broker/tunnel.go:1818       routePref := parsePref(X-Roger-Pref) (router.go:52-63:
#                                           cheap|fast|reliable, ANY other value -> balanced, silently)
#   cmd/rogerai-broker/tunnel.go:1836-1847  allow is filled ONLY by a grant (gc.nodeAllow); a private band
#                                           fills privateAllow (tunnel.go:1796-1811). No consumer allow-list.
#   cmd/rogerai-broker/tunnel.go:3165-3355  pickFor: hard filters (stale, banned, owner-banned, private
#                                           without code, pin, exclude, allow, confidential, cooling,
#                                           dead-probe streak, min-tps, model, modality, price caps,
#                                           declared-ctx gate), then score, Tier A before Tier B, then
#                                           selectP2C(pool, beta, rng). There is NO strict ordering path
#                                           and NO ordered-preference input.
#   cmd/rogerai-broker/tunnel.go:1955-1993  the failover plan (up to ROGERAI_RELAY_ATTEMPTS stations,
#                                           each re-picked excluding the earlier ones); tunnel.go:1964
#                                           a pin DISABLES the plan (one attempt).
#   cmd/rogerai-broker/cooling.go:28-50     ROGERAI_RELAY_ATTEMPTS=3, ROGERAI_RELAY_FAILOVER knob,
#                                           cooldown default 15s / cap 120s, >= 10s deadline left per attempt.
#   cmd/rogerai-broker/pricesafety.go:31-45 the consumer out-cap default ($10/1M when the cap is absent or 0).
#   cmd/rogerai-broker/openapi.yaml:276-289 documents Node and Exclude-Nodes; Pref is undocumented.
#   internal/client/client.go:861-870       the local proxy forwards Pref / Node / Exclude when set on
#                                           ProxyOptions; nothing first-party sets Pref (dead knob).
#
# WHAT CHANGES: a consumer may state an allow-list (only), a deny-list (ignore), a strict
# priority (order), a no-fallback bound (allow_fallbacks:false), a single-metric strict sort
# (sort), or a weighted preference (pref), in the body; body wins over the header for the same
# knob; malformed input is a 400 naming the key; nothing here can WIDEN what a grant, a band,
# a ban, a price cap or the moderation gate admits. Strict `order` and `sort` disable the
# power-of-two-choices spread (they are the consumer saying "I know what I want"); `pref`
# keeps it.
#
# Enforced by: cmd/rogerai-broker/node_preference_bdd_test.go (to be written; RED first).

Feature: Node preference - allow, deny, order, no-fallback, sort and pref

  Background:
    Given a broker with an empty in-memory node registry
    And the fee rate is 30%
    And stations "s1", "s2", "s3" serve "m", all on air, healthy (Tier A), seen just now
    And "s1" prices out 1.00, "s2" prices out 2.00, "s3" prices out 3.00 per 1M
    And a funded consumer

  # ============================================================================
  # only (allow-list)
  # ============================================================================

  # --- only: alone ------------------------------------------------------------
  Scenario: only with one station takes only that station
    When a funded consumer relays with provider.only ["s3"]
    Then the pick is "s3"
    And X-RogerAI-Provider names "s3"

  Scenario: only with two stations picks among exactly those two
    When 20 funded consumers relay with provider.only ["s2", "s3"]
    Then every pick is "s2" or "s3"
    And "s1" received nothing

  Scenario: only naming every station is the same as no only
    When 20 funded consumers relay with provider.only ["s1", "s2", "s3"]
    Then the pick distribution matches 20 relays with no routing object

  Scenario: only with an empty array is a 400
    When a funded consumer relays with provider.only []
    Then the response is 400 {"error":{"code":"invalid_routing_value","message":"provider.only must not be empty"}}
    And no station received anything
    And no hold was placed

  Scenario: only naming a station that is not on air contributes nothing
    When a funded consumer relays with provider.only ["s9"]
    Then the response is 503 {"error":{"code":"no_match"}}
    And no Retry-After header is present
    And X-RogerAI-Cost is 0 and there is no receipt

  Scenario: only naming one live and one unknown station takes the live one
    When a funded consumer relays with provider.only ["s9", "s2"]
    Then the pick is "s2"

  Scenario: only is exact-match and case-sensitive
    When a funded consumer relays with provider.only ["S1"]
    Then the response is 503 {"error":{"code":"no_match"}}

  Scenario Outline: only rejects malformed entries
    When a funded consumer relays with provider.only <value>
    Then the response is 400 with error.code "invalid_routing_value" naming "provider.only"
    Examples:
      | value                     |
      | [""]                      |
      | ["  "]                    |
      | [" s1"]                   |
      | ["s1", ""]                |
      | [1]                       |
      | [null]                    |
      | "s1"                      |
      | {"id":"s1"}               |
      | 33 distinct station ids   |

  Scenario: only with exactly 32 entries is accepted
    When a funded consumer relays with provider.only of 32 distinct ids including "s1"
    Then the pick is "s1"

  Scenario: only with duplicate entries is accepted and de-duplicated
    When a funded consumer relays with provider.only ["s1", "s1", "s1"]
    Then the pick is "s1"

  # --- only: the plan ---------------------------------------------------------
  Scenario: the failover plan never leaves the only set
    Given "s2" 429s and "s3" serves
    When a funded consumer relays with provider.only ["s2", "s3"] and the pick lands on "s2"
    Then the failover goes to "s3"
    And "s1" received nothing
    And the consumer sees 200 with X-RogerAI-Provider "s3"

  Scenario: the only set exhausted by failover returns the last error, not a station outside it
    Given "s2" 429s with Retry-After 7 and "s1" serves
    When a funded consumer relays with provider.only ["s2"]
    Then the response is 429 with Retry-After: 7
    And "s1" received nothing
    And the hold is released in full

  # --- only: intersections with admission sets --------------------------------
  Scenario: only cannot reach a private-band station without the code
    Given "p1" is a private (band-only) station for "m"
    When a funded consumer relays with provider.only ["p1"] and no X-Roger-Freq
    Then the response is 503 {"error":{"code":"no_match"}}
    And "p1" received nothing
    And the error message does not reveal that "p1" exists or is private

  # corrected 2026-10-01 (founder-approved): assumes a private band with several stations; a band is ONE node id today (resolveFreqAllow). Multi-station bands are not part of this set.
  @later
  Scenario: only narrows a private band to one of its stations when the code is presented
    Given a private band "B" with stations "p1" and "p2" for "m"
    When a funded consumer relays with roger.freq for band "B" and provider.only ["p2"]
    Then the pick is "p2"
    And "p1" received nothing

  Scenario: only naming a public station on a private-band request is the uniform band message (no code)
    Given a private band "B" with station "p1" for "m"
    When a funded consumer relays with roger.freq for band "B" and provider.only ["s1"]
    Then the response is 503 "no station on that frequency (it may be off air) - check the code"
    And the body carries no error code (§2: nothing on the band path distinguishes its refusals)
    And "s1" received nothing
    And "p1" received nothing

  Scenario: only intersects a grant's node allow-list
    Given a grant from the owner of "s1" and "s2" scoped to nodes ["s1", "s2"]
    When the grant holder relays with provider.only ["s2", "s3"]
    Then the pick is "s2"
    And "s3" received nothing

  Scenario: only that misses the grant's nodes entirely is the grant's own refusal
    Given a grant from the owner of "s1" scoped to nodes ["s1"]
    When the grant holder relays with provider.only ["s3"]
    Then the response is 503 {"error":{"code":"no_match"}}
    And the message says no node of this grant's owner matches
    And "s3" received nothing

  Scenario: only cannot admit a banned station
    Given "s1" is banned
    When a funded consumer relays with provider.only ["s1"]
    Then the response is 503 {"error":{"code":"no_match"}}
    And "s1" received nothing

  Scenario: only cannot admit a banned operator's fresh station id
    Given operator "op-1" is banned
    And "s7" is owned by "op-1", on air for "m", seen just now
    When a funded consumer relays with provider.only ["s7"]
    Then the response is 503 {"error":{"code":"no_match"}}
    And "s7" received nothing

  Scenario: only cannot admit a stale station
    Given "s1" was last seen 2*nodeTTL ago
    When a funded consumer relays with provider.only ["s1"]
    Then the response is 503 {"error":{"code":"no_match"}}

  Scenario: only cannot admit a dead-probe station
    Given "s1" has failed probeDeadStreak liveness probes in a row
    When a funded consumer relays with provider.only ["s1"]
    Then the response is 503 {"error":{"code":"no_match"}}
    And "s1" received nothing

  Scenario: only cannot admit a non-confidential station on a confidential request
    Given only "s3" is TEE-attested
    When a funded consumer relays with roger.confidential true and provider.only ["s1"]
    Then the response is 503 {"error":{"code":"no_match"}}
    And the message says no confidential station matches

  Scenario: only cannot bypass the consumer out-price cap
    Given "s3" prices out 12.00 per 1M
    When a funded consumer relays with provider.only ["s3"] and no max_price
    Then the response is 503 {"error":{"code":"no_match"}}
    And "s3" received nothing
    # the $10/1M default (pricesafety.go) is applied server-side whatever the allow-list says

  Scenario: only with an explicit cap admits the station the default would refuse
    Given "s3" prices out 12.00 per 1M
    When a funded consumer relays with provider.only ["s3"] and provider.max_price.completion 15
    Then the pick is "s3"
    And the hold covers "s3" at 12.00

  Scenario: only cannot admit an offer over the register ceiling because none exists
    When an operator registers "s9" for "m" at out 101.00 per 1M
    Then the registration is rejected
    And a relay with provider.only ["s9"] is 503 {"error":{"code":"no_match"}}

  Scenario: only with a cooling station and no other listed station is band cooling
    Given "s1" is cooling for 8 more seconds
    When a funded consumer relays with provider.only ["s1"]
    Then the response is 503 {"error":{"code":"band_cooling"}} with Retry-After: 8
    And no station received anything
    And no hold was placed

  Scenario: only with one cooling and one live station takes the live one
    Given "s1" is cooling
    When a funded consumer relays with provider.only ["s1", "s2"]
    Then the pick is "s2"

  Scenario: only with a station not offering the model finds nothing
    Given "s4" is on air for "other-model" only
    When a funded consumer relays for "m" with provider.only ["s4"]
    Then the response is 503 {"error":{"code":"no_match"}}

  Scenario: only with a station whose declared window is too small for the prompt is the ctx-400, not a no_match
    # §3: the only station on air for the model is too small, so the honest answer is the
    # existing 400 "request exceeds the context window" naming the widest window in scope.
    Given "s1" declares ctx 4096 for "m"
    When a funded consumer relays an 8000-token prompt with provider.only ["s1"]
    Then the response is 400 "request exceeds the context window" naming 4096
    And "s1" received nothing and no hold was placed
    And "s1"'s operator is not struck

  Scenario: an anonymous free relay with only is confined to free stations
    Given "f1" serves "m" free (0/0) and "s1" is paid
    When an anonymous consumer relays with provider.only ["s1", "f1"]
    Then the pick is "f1"
    And "s1" received nothing

  Scenario: an anonymous free relay whose only set is all paid gets today's login 401, not a no_match
    # §1a: the station exists, the caller cannot pay it (anonCannotPay).
    When an anonymous consumer relays with provider.only ["s1"]
    Then the response is 401 "log in to spend"
    And no hold was placed and X-RogerAI-Cost is "0"

  # ============================================================================
  # ignore (deny-list)
  # ============================================================================

  @slice0
  Scenario: ignore removes one station from the pool
    When 20 funded consumers relay with provider.ignore ["s1"]
    Then every pick is "s2" or "s3"
    And "s1" received nothing

  @slice0
  Scenario: ignore removing every station finds nothing
    When a funded consumer relays with provider.ignore ["s1", "s2", "s3"]
    Then the response is 503 {"error":{"code":"no_match"}}

  @slice0
  Scenario: ignore naming an unknown station is a silent no-op
    When 20 funded consumers relay with provider.ignore ["s9"]
    Then the pick distribution matches 20 relays with no routing object

  @slice0
  Scenario: ignore is exact-match and case-sensitive
    When 20 funded consumers relay with provider.ignore ["S1"]
    Then "s1" is picked at least once

  @slice0
  Scenario: ignore with an empty array is the same as no ignore
    When 20 funded consumers relay with provider.ignore []
    Then the pick distribution matches 20 relays with no routing object

  @slice0
  Scenario Outline: ignore rejects malformed entries
    When a funded consumer relays with provider.ignore <value>
    Then the response is 400 with error.code "invalid_routing_value" naming "provider.ignore"
    Examples:
      | value                   |
      | [""]                    |
      | ["s1", " "]             |
      | [7]                     |
      | "s1,s2"                 |
      | 33 distinct station ids |

  @slice0
  Scenario: ignore and the X-Roger-Exclude-Nodes header are unioned
    When a funded consumer relays with X-Roger-Exclude-Nodes "s1" and provider.ignore ["s2"]
    Then the pick is "s3"

  @slice0
  Scenario: ignore and the header naming the same station is not an error
    When a funded consumer relays with X-Roger-Exclude-Nodes "s1" and provider.ignore ["s1"]
    Then every pick is "s2" or "s3"

  @slice0
  Scenario: the failover plan honors ignore on every attempt
    Given "s2" 429s and "s3" serves
    When a funded consumer relays with provider.ignore ["s1"] and the pick lands on "s2"
    Then the failover goes to "s3", never "s1"

  @slice0
  Scenario: ignore cannot un-ban, un-cool or admit anything
    Given "s1" is banned and "s2" is cooling
    When a funded consumer relays with provider.ignore ["s9"]
    Then the pick is "s3"

  Scenario: ignore on a grant relay narrows the grant's nodes
    Given a grant scoped to nodes ["s1", "s2"]
    When the grant holder relays with provider.ignore ["s1"]
    Then the pick is "s2"

  Scenario: ignore that empties the grant's nodes is the grant's own refusal
    Given a grant scoped to nodes ["s1"]
    When the grant holder relays with provider.ignore ["s1"]
    Then the response is 503 {"error":{"code":"no_match"}}
    And the message says no node of this grant's owner matches

  # corrected 2026-10-01 (founder-approved): assumes a private band with several stations; a band is ONE node id today (resolveFreqAllow). Multi-station bands are not part of this set.
  @later
  Scenario: ignore on a private-band relay narrows the band
    Given a private band "B" with stations "p1" and "p2" for "m"
    When a funded consumer relays with roger.freq for band "B" and provider.ignore ["p1"]
    Then the pick is "p2"

  @slice0
  Scenario: ignore that empties a private band is the uniform band message (no code)
    Given a private band "B" with station "p1" for "m"
    When a funded consumer relays with roger.freq for band "B" and provider.ignore ["p1"]
    Then the response is 503 "no station on that frequency (it may be off air) - check the code"
    And the body carries no error code (§2)

  # --- only ∩ ignore -----------------------------------------------------------
  Scenario: a station in both only and ignore is ignored
    When a funded consumer relays with provider.only ["s1", "s2"] and provider.ignore ["s1"]
    Then the pick is "s2"

  Scenario: ignore that empties only finds nothing
    When a funded consumer relays with provider.only ["s1"] and provider.ignore ["s1"]
    Then the response is 503 {"error":{"code":"no_match"}}

  # ============================================================================
  # order (strict priority)
  # ============================================================================

  # --- order: alone -----------------------------------------------------------
  @slice0
  Scenario: order takes the first listed eligible station, every time
    When 20 funded consumers relay with provider.order ["s3", "s1"]
    Then every pick is "s3"
    # strict priority: no power-of-two-choices over the listed portion

  @slice0
  Scenario: order skips an ineligible listed station silently and takes the next
    Given "s3" was last seen 2*nodeTTL ago
    When a funded consumer relays with provider.order ["s3", "s1"]
    Then the pick is "s1"
    And the response carries no warning about "s3"

  @slice0
  Scenario Outline: order skips every kind of ineligible listed station
    Given "s3" is <state>
    When a funded consumer relays with provider.order ["s3", "s1"]
    Then the pick is "s1"
    Examples:
      | state                                        |
      | banned                                       |
      | owned by a banned operator                   |
      | stale (last seen 2*nodeTTL ago)              |
      | past the dead-probe streak                   |
      | cooling                                      |
      | private (band-only) and no code is presented |
      | not offering "m"                             |
      | priced out 12.00 (over the $10 default cap)  |
      | declaring ctx 4096 for an 8000-token prompt  |
      | below the request's min_tps                  |
      | lacking a required capability                |
      | not TEE-attested on a confidential request   |

  @slice0
  Scenario: order with every listed station ineligible falls through to the rest by score
    Given "s3" is banned and "s2" is cooling
    When a funded consumer relays with provider.order ["s3", "s2"]
    Then the pick is "s1"
    # allow_fallbacks defaults to true: the remainder is used

  Scenario: after the listed stations, the remainder is chosen by score with P2C
    Given "s1" is cooling
    And "s2" and "s3" are equal in every score term
    When 40 funded consumers relay with provider.order ["s1"]
    Then both "s2" and "s3" are picked
    # P2C is disabled for the LISTED portion only; the remainder spreads as today

  @slice0
  Scenario: order naming an unknown station contributes nothing
    When a funded consumer relays with provider.order ["s9"]
    Then the pick is "s1", "s2" or "s3" by score

  @slice0
  Scenario: order with one live station is that station
    When 20 funded consumers relay with provider.order ["s2"]
    Then every pick is "s2"

  @slice0
  Scenario: order is exact-match and case-sensitive
    When 20 funded consumers relay with provider.order ["S3"]
    Then the pick distribution matches 20 relays with no routing object

  @slice0
  Scenario Outline: order rejects malformed entries
    When a funded consumer relays with provider.order <value>
    Then the response is 400 with error.code "invalid_routing_value" naming "provider.order"
    Examples:
      | value                   |
      | []                      |
      | [""]                    |
      | ["s1", "\t"]            |
      | [true]                  |
      | "s1"                    |
      | 33 distinct station ids |

  @slice0
  Scenario: order with duplicates keeps the first occurrence
    When 20 funded consumers relay with provider.order ["s2", "s2", "s3"]
    Then every pick is "s2"

  # --- order: the plan --------------------------------------------------------
  @slice0
  Scenario: the failover plan follows the order
    Given "s3" 429s, "s1" 429s, and "s2" serves
    When a funded consumer relays with provider.order ["s3", "s1", "s2"]
    Then attempt 1 hit "s3", attempt 2 hit "s1", attempt 3 hit "s2"
    And the consumer sees 200 with X-RogerAI-Provider "s2"

  Scenario: the plan is still bounded by ROGERAI_RELAY_ATTEMPTS
    Given stations "s1".."s5" serve "m" and "s1", "s2", "s3" 429
    When a funded consumer relays with provider.order ["s1", "s2", "s3", "s4", "s5"] and ROGERAI_RELAY_ATTEMPTS 3
    Then exactly 3 attempts were made, to "s1", "s2", "s3"
    And the response is 429 with Retry-After
    And "s4" and "s5" received nothing

  @slice0
  Scenario: the plan after the listed stations continues by score when fallbacks are allowed
    Given "s3" 429s and "s1" and "s2" serve
    When a funded consumer relays with provider.order ["s3"]
    Then attempt 1 hit "s3" and attempt 2 hit "s1" or "s2"
    And the consumer sees 200

  @slice0
  Scenario: the plan never re-picks a failed ordered station within the same request
    Given "s3" 429s twice in a row and "s1" serves
    When a funded consumer relays with provider.order ["s3", "s3", "s1"]
    Then "s3" received exactly one attempt

  @slice0
  Scenario: the hold covers the priciest station in the ordered plan
    When a funded consumer relays with provider.order ["s1", "s3"]
    Then the hold covers "s3" at 3.00
    And on success at "s1" the consumer is charged at 1.00 and the remainder released

  @slice0
  Scenario: an ordered station the hold cannot cover is dropped, the plan continues
    Given the consumer's balance covers "s1" but not "s3"
    When a funded consumer relays with provider.order ["s3", "s1"]
    Then the pick is "s1"
    And "s3" received nothing

  Scenario: the request deadline bounds an ordered plan
    Given "s3" takes 85 seconds then 429s
    When a funded consumer relays with provider.order ["s3", "s1"] with the 90 second relay deadline
    Then no attempt is made to "s1" (less than 10 seconds left)
    And the response is 429 with Retry-After

  # --- order: with other knobs ------------------------------------------------
  Scenario: order and only agree when every ordered station is in only
    When a funded consumer relays with provider.only ["s1", "s2"] and provider.order ["s2", "s1"]
    Then the pick is "s2"

  Scenario: order naming a station outside only is a 400
    When a funded consumer relays with provider.only ["s1"] and provider.order ["s2"]
    Then the response is 400 {"error":{"code":"conflicting_routing_keys","message":"provider.order names s2, which is not in provider.only"}}
    And no station received anything

  @slice0
  Scenario: order with ignore of an ordered station skips it
    When a funded consumer relays with provider.order ["s3", "s1"] and provider.ignore ["s3"]
    Then the pick is "s1"
    # ignore is a hard filter; order is a preference among what survives

  @slice0
  Scenario: order with ignore of every ordered station falls through to the rest
    When a funded consumer relays with provider.order ["s3"] and provider.ignore ["s3"]
    Then the pick is "s1" or "s2" by score

  @slice0
  Scenario: order and the X-Roger-Node header - the body wins
    When a funded consumer relays with X-Roger-Node "s1" and provider.order ["s3"]
    Then the pick is "s3"
    And the header is ignored without error

  @slice0
  Scenario: order and the X-Roger-Node header naming the same station
    When a funded consumer relays with X-Roger-Node "s3" and provider.order ["s3"]
    Then the pick is "s3"
    And fallbacks are allowed (the body's default), not disabled by the header

  # corrected 2026-10-01 (founder-approved): assumes a private band with several stations; a band is ONE node id today (resolveFreqAllow). Multi-station bands are not part of this set.
  @later
  Scenario: order on a private band ranks within the band
    Given a private band "B" with stations "p1" and "p2" for "m"
    When a funded consumer relays with roger.freq for band "B" and provider.order ["p2", "p1"]
    Then the pick is "p2"

  @slice0
  Scenario: order naming a public station on a private-band request cannot escape the band
    Given a private band "B" with station "p1" for "m"
    When a funded consumer relays with roger.freq for band "B" and provider.order ["s1"]
    Then the pick is "p1"
    And "s1" received nothing

  @slice0
  Scenario: order naming a private station without the code cannot enter the band
    Given "p1" is a private (band-only) station for "m"
    When a funded consumer relays with provider.order ["p1", "s1"]
    Then the pick is "s1"
    And "p1" received nothing

  Scenario: order on a grant ranks within the grant's nodes
    Given a grant scoped to nodes ["s1", "s2"]
    When the grant holder relays with provider.order ["s2", "s3"]
    Then the pick is "s2"
    And "s3" received nothing

  @slice0
  Scenario: order cannot bypass the consumer out-price cap
    Given "s3" prices out 12.00 per 1M
    When a funded consumer relays with provider.order ["s3", "s1"] and no max_price
    Then the pick is "s1"
    And "s3" received nothing

  @slice0
  Scenario: order cannot admit a banned station
    Given "s3" is banned
    When a funded consumer relays with provider.order ["s3", "s1"]
    Then the pick is "s1"
    And "s3" received nothing

  @slice0
  Scenario: order with a confidential request ranks within attested stations
    Given only "s2" and "s3" are TEE-attested
    When a funded consumer relays with roger.confidential true and provider.order ["s1", "s3"]
    Then the pick is "s3"

  @slice0
  Scenario: an anonymous free relay with order ranks within free stations
    Given "f1" and "f2" serve "m" free (0/0)
    When an anonymous consumer relays with provider.order ["s1", "f2", "f1"]
    Then the pick is "f2"
    And "s1" received nothing

  # ============================================================================
  # allow_fallbacks
  # ============================================================================

  @slice0
  Scenario: allow_fallbacks defaults to true
    Given "s1" 429s and "s2" serves
    When a funded consumer relays with provider.order ["s1"]
    Then the failover goes to "s2"
    And the consumer sees 200

  @slice0
  Scenario: allow_fallbacks true stated explicitly behaves as the default
    Given "s1" 429s and "s2" serves
    When a funded consumer relays with provider.order ["s1"] and provider.allow_fallbacks true
    Then the failover goes to "s2"

  @slice0
  Scenario Outline: allow_fallbacks rejects non-boolean values
    When a funded consumer relays with provider.allow_fallbacks <value>
    Then the response is 400 with error.code "invalid_routing_value" naming "provider.allow_fallbacks"
    Examples:
      | value   |
      | "false" |
      | 0       |
      | "yes"   |

  # --- allow_fallbacks:false with order -----------------------------------------
  @slice0
  Scenario: no-fallback with an order of one station is a single attempt
    Given "s1" 429s with Retry-After 7 and "s2" serves
    When a funded consumer relays with provider.order ["s1"] and provider.allow_fallbacks false
    Then exactly one attempt was made, to "s1"
    And the response is 429 with Retry-After: 7
    And "s2" received nothing

  @slice0
  Scenario: no-fallback with an order of two stations tries both and no other
    Given "s1" 429s, "s2" 429s with Retry-After 4, and "s3" serves
    When a funded consumer relays with provider.order ["s1", "s2"] and provider.allow_fallbacks false
    Then attempt 1 hit "s1" and attempt 2 hit "s2"
    And the response is 429 with Retry-After: 4
    And "s3" received nothing

  @slice0
  Scenario: no-fallback with an order of two stations succeeds on the second
    Given "s1" 429s and "s2" serves
    When a funded consumer relays with provider.order ["s1", "s2"] and provider.allow_fallbacks false
    Then the consumer sees 200 with X-RogerAI-Provider "s2"

  @slice0
  Scenario: no-fallback with an order of five is bounded by ROGERAI_RELAY_ATTEMPTS
    Given stations "s1".."s5" serve "m" and all 429
    When a funded consumer relays with provider.order ["s1","s2","s3","s4","s5"], provider.allow_fallbacks false, and ROGERAI_RELAY_ATTEMPTS 3
    Then exactly 3 attempts were made, to "s1", "s2", "s3"
    And the response is 429 with Retry-After

  @slice0
  Scenario: no-fallback with an order whose listed stations are all ineligible finds nothing
    Given "s1" is banned
    When a funded consumer relays with provider.order ["s1"] and provider.allow_fallbacks false
    Then the response is 503 {"error":{"code":"no_match"}}
    And no station received anything
    And no hold was placed

  @slice0
  Scenario: no-fallback with one ineligible and one eligible listed station takes the eligible one
    Given "s1" is stale
    When a funded consumer relays with provider.order ["s1", "s2"] and provider.allow_fallbacks false
    Then the pick is "s2"
    And "s3" received nothing

  # --- allow_fallbacks:false with only -----------------------------------------
  Scenario: no-fallback with only is the only set, by score, and nothing else
    Given "s2" 429s with Retry-After 6 and "s3" 429s with Retry-After 9
    When a funded consumer relays with provider.only ["s2", "s3"] and provider.allow_fallbacks false
    Then attempts were made to "s2" and "s3" in score order
    And "s1" received nothing
    And the response is 429 with the last upstream Retry-After

  Scenario: no-fallback with only of one station is a single attempt
    Given "s2" 429s and "s1" serves
    When a funded consumer relays with provider.only ["s2"] and provider.allow_fallbacks false
    Then exactly one attempt was made, to "s2"
    And the response is 429

  # --- allow_fallbacks:false with neither ---------------------------------------
  @slice0
  Scenario: no-fallback without order or only is a single scored attempt
    Given "s1" 429s with Retry-After 7 and "s2" serves
    When a funded consumer relays with provider.allow_fallbacks false and the pick lands on "s1"
    Then exactly one attempt was made
    And the response is 429 with Retry-After: 7
    # the consumer asked for no failover; the broker never adds a station they did not accept

  @slice0
  Scenario: no-fallback without order or only succeeds like a plain relay when the pick serves
    When a funded consumer relays with provider.allow_fallbacks false
    Then the consumer sees 200
    And the hold covered exactly the picked station, not the priciest of three

  # --- allow_fallbacks:false with the pin header --------------------------------
  @slice0
  Scenario: the X-Roger-Node header is order of one with no fallbacks
    Given "s1" 429s and "s2" serves
    When a funded consumer relays with X-Roger-Node "s1"
    Then exactly one attempt was made, to "s1"
    And the response is 429
    # regression pin: tunnel.go:1964 today, unchanged

  @slice0
  Scenario: a pin header with body allow_fallbacks true - the body wins
    Given "s1" 429s and "s2" serves
    When a funded consumer relays with X-Roger-Node "s1" and provider.allow_fallbacks true
    Then the failover goes to "s2"
    # body precedence: the header's implied no-fallback is overridden by the body's explicit true

  @slice0
  Scenario: a pin header with body order naming another station - the body wins
    Given "s1" 429s and "s3" serves
    When a funded consumer relays with X-Roger-Node "s2", provider.order ["s1"], and provider.allow_fallbacks false
    Then exactly one attempt was made, to "s1"
    And "s2" received nothing

  # --- allow_fallbacks:false: cooling, dead, banned, stale ----------------------
  @slice0
  Scenario: no-fallback with a cooling listed station is band cooling with that station's expiry
    Given "s1" is cooling for 11 more seconds and "s2" serves
    When a funded consumer relays with provider.order ["s1"] and provider.allow_fallbacks false
    Then the response is 503 {"error":{"code":"band_cooling"}} with Retry-After: 11
    And no station received anything
    And no hold was placed

  @slice0
  Scenario: no-fallback with two cooling listed stations reports the soonest expiry
    Given "s1" is cooling for 30 more seconds and "s2" is cooling for 5 more seconds
    When a funded consumer relays with provider.order ["s1", "s2"] and provider.allow_fallbacks false
    Then the response is 503 {"error":{"code":"band_cooling"}} with Retry-After: 5

  @slice0
  Scenario: no-fallback with one cooling and one ineligible listed station is band cooling, not no_match
    Given "s1" is cooling for 9 more seconds and "s2" is banned
    When a funded consumer relays with provider.order ["s1", "s2"] and provider.allow_fallbacks false
    Then the response is 503 {"error":{"code":"band_cooling"}} with Retry-After: 9
    # a cooling station will come back; the consumer is told when

  @slice0
  Scenario Outline: no-fallback with an ineligible listed station is no_match without Retry-After
    Given "s1" is <state>
    When a funded consumer relays with provider.order ["s1"] and provider.allow_fallbacks false
    Then the response is 503 {"error":{"code":"no_match"}}
    And no Retry-After header is present
    And "s2" and "s3" received nothing
    Examples:
      | state                            |
      | banned                           |
      | owned by a banned operator       |
      | stale (last seen 2*nodeTTL ago)  |
      | past the dead-probe streak       |
      | not offering "m"                 |
      | priced over the default out-cap  |

  @slice0
  Scenario: no-fallback never widens to a station the consumer did not name, even when the market is idle
    Given "s2" and "s3" are idle and "s1" is at capacity
    When a funded consumer relays with provider.order ["s1"] and provider.allow_fallbacks false
    Then the pick is "s1"

  # --- allow_fallbacks:false: money ---------------------------------------------
  @slice0
  Scenario: no-fallback places exactly one hold, sized to the named station
    When a funded consumer relays with provider.order ["s1"] and provider.allow_fallbacks false
    Then exactly one hold was placed
    And the hold covers "s1" at 1.00, not "s3" at 3.00

  Scenario: no-fallback refunds the hold in full on failure
    Given "s1" 429s
    When a funded consumer relays with provider.order ["s1"] and provider.allow_fallbacks false
    Then the hold is released in full
    And a $0 voided receipt is written in "s1"'s chain
    And the consumer's balance is unchanged

  Scenario: no-fallback with a grant is a single attempt under the grant's caps
    Given a grant scoped to nodes ["s1", "s2"] and "s1" 429s
    When the grant holder relays with provider.order ["s1"] and provider.allow_fallbacks false
    Then exactly one attempt was made, to "s1"
    And the grant's caps were charged for one attempt, not two

  Scenario: no-fallback on a streaming request before the first frame
    Given "s1" 429s before any chunk
    When a funded consumer relays with "stream": true, provider.order ["s1"], and provider.allow_fallbacks false
    Then the response is 429 with Retry-After and no SSE headers were committed
    And "s2" received nothing

  # ============================================================================
  # sort (strict single-metric ordering)
  # ============================================================================

  # --- sort: price ------------------------------------------------------------
  Scenario: sort price takes the cheapest out-price every time
    When 20 funded consumers relay with provider.sort "price"
    Then every pick is "s1"
    # strict: P2C is disabled; the consumer asked for the floor

  Scenario: sort price breaks an out-price tie on in-price
    Given "s1" and "s2" both price out 1.00, "s1" prices in 0.50, "s2" prices in 0.20
    When 20 funded consumers relay with provider.sort "price"
    Then every pick is "s2"

  Scenario: sort price breaks a full price tie on score
    Given "s1" and "s2" price identically and "s2" has the better reliability
    When 20 funded consumers relay with provider.sort "price"
    Then every pick is "s2"

  Scenario: sort price puts free (0/0) offers first
    Given "f1" serves "m" free (0/0)
    When 20 funded consumers relay with provider.sort "price"
    Then every pick is "f1"

  Scenario: sort price uses the active (time-of-use) price right now
    Given "s3" has a scheduled window pricing out 0.50 that is active now
    When a funded consumer relays with provider.sort "price"
    Then the pick is "s3"

  Scenario: the failover plan follows the price sort
    Given "s1" 429s, "s2" 429s, and "s3" serves
    When a funded consumer relays with provider.sort "price"
    Then attempt 1 hit "s1", attempt 2 hit "s2", attempt 3 hit "s3"

  Scenario: sort price still cannot pick over the consumer cap
    Given "s1" and "s2" price out 12.00 and "s3" prices out 9.00
    When a funded consumer relays with provider.sort "price" and no max_price
    Then the pick is "s3"

  Scenario: sort price cannot pick over the register ceiling because none exists
    Then no offer over the register ceiling is on air
    And a relay with provider.sort "price" and provider.max_price.completion 100 picks "s1"

  Scenario: sort price with an explicit cap below every station finds nothing
    When a funded consumer relays with provider.sort "price" and provider.max_price.completion 0.50
    Then the response is 503 {"error":{"code":"no_match"}}

  Scenario: sort price is Tier A first, Tier B only when Tier A is empty
    Given "s1" is Tier B (success rate 0.30) and "s2", "s3" are Tier A
    When 20 funded consumers relay with provider.sort "price"
    Then every pick is "s2"

  Scenario: sort price uses Tier B when Tier A is empty
    Given "s1", "s2", "s3" are all Tier B
    When 20 funded consumers relay with provider.sort "price"
    Then every pick is "s1"

  # --- sort: throughput -------------------------------------------------------
  Scenario: sort throughput takes the highest measured tok/s every time
    Given measured tps "s1" 20, "s2" 60, "s3" 40
    When 20 funded consumers relay with provider.sort "throughput"
    Then every pick is "s2"

  Scenario: sort throughput puts unmeasured stations last
    Given measured tps "s1" 20, "s2" unmeasured, "s3" 40
    When 20 funded consumers relay with provider.sort "throughput"
    Then every pick is "s3"
    And "s2" is planned after "s1"

  Scenario: sort throughput with every station unmeasured falls back to score
    Given no station has a tps measurement
    When 20 funded consumers relay with provider.sort "throughput"
    Then the picks follow the score order

  Scenario: sort throughput breaks a tps tie on score
    Given measured tps "s1" 40 and "s2" 40, and "s1" has the better reliability
    When 20 funded consumers relay with provider.sort "throughput"
    Then every pick is "s1"

  Scenario: sort throughput with min_tps filters first, then sorts
    Given measured tps "s1" 20, "s2" 60, "s3" 40
    When 20 funded consumers relay with provider.sort "throughput" and roger.min_tps 30
    Then every pick is "s2"
    And "s1" is not in the plan

  Scenario: sort throughput ignores price entirely within the cap
    Given measured tps "s3" 90 and "s3" prices out 9.00
    When 20 funded consumers relay with provider.sort "throughput"
    Then every pick is "s3"
    And the hold covers "s3" at 9.00

  Scenario: the failover plan follows the throughput sort
    Given measured tps "s1" 20, "s2" 60, "s3" 40 and "s2" 429s
    When a funded consumer relays with provider.sort "throughput"
    Then attempt 1 hit "s2" and attempt 2 hit "s3"

  # --- sort: latency ----------------------------------------------------------
  Scenario: sort latency takes the lowest measured TTFT every time
    Given measured ttft "s1" 900ms, "s2" 300ms, "s3" 600ms
    When 20 funded consumers relay with provider.sort "latency"
    Then every pick is "s2"

  Scenario: sort latency puts unmeasured stations last
    Given measured ttft "s1" 900ms, "s2" unmeasured, "s3" 600ms
    When 20 funded consumers relay with provider.sort "latency"
    Then every pick is "s3"

  Scenario: sort latency breaks a TTFT tie on score
    Given measured ttft "s1" 400ms and "s3" 400ms, and "s3" has the better reliability
    When 20 funded consumers relay with provider.sort "latency"
    Then every pick is "s3"

  @slice2

  Scenario: sort latency with max_ttft_ms filters first, then sorts
    Given measured ttft "s1" 900ms, "s2" 300ms, "s3" 600ms
    When 20 funded consumers relay with provider.sort "latency" and roger.max_ttft_ms 700
    Then every pick is "s2"
    And "s1" is not in the plan

  Scenario: the failover plan follows the latency sort
    Given measured ttft "s1" 900ms, "s2" 300ms, "s3" 600ms and "s2" 429s
    When a funded consumer relays with provider.sort "latency"
    Then attempt 1 hit "s2" and attempt 2 hit "s3"

  # --- sort: shared rules -----------------------------------------------------
  Scenario Outline: sort disables the power-of-two-choices spread
    Given "s1" is strictly best under <metric> and every station is idle
    When 100 funded consumers relay with provider.sort "<metric>"
    Then every pick is "s1"
    Examples:
      | metric     |
      | price      |
      | throughput |
      | latency    |

  Scenario Outline: sort still respects the load-shedding gates that are hard filters
    Given "s1" is strictly best under <metric> but is <state>
    When a funded consumer relays with provider.sort "<metric>"
    Then the pick is not "s1"
    Examples:
      | metric     | state                      |
      | price      | cooling                    |
      | throughput | past the dead-probe streak  |
      | latency    | banned                     |
      | price      | stale                      |

  Scenario: sort keeps the Tier A before Tier B gate
    Given "s1" is strictly fastest but Tier B, and "s2", "s3" are Tier A
    When 20 funded consumers relay with provider.sort "throughput"
    Then every pick is the faster of "s2" and "s3"

  Scenario Outline: sort rejects unknown values
    When a funded consumer relays with provider.sort <value>
    Then the response is 400 with error.code "invalid_routing_value" naming "provider.sort"
    Examples:
      | value                             |
      | "cheapest"                        |
      | "Price"                           |
      | "tps"                             |
      | ""                                |
      | 1                                 |
      | ["price"]                         |
      | {"by":"price","partition":"none"} |
    # the OpenRouter object form is not accepted in v1; partition has no meaning here

  Scenario: sort with only sorts within the only set
    Given measured tps "s1" 90, "s2" 20, "s3" 40
    When 20 funded consumers relay with provider.sort "throughput" and provider.only ["s2", "s3"]
    Then every pick is "s3"

  Scenario: sort with ignore sorts what survives
    When 20 funded consumers relay with provider.sort "price" and provider.ignore ["s1"]
    Then every pick is "s2"

  Scenario: sort with order - the order wins for the listed portion, sort ranks the remainder
    Given measured tps "s1" 90, "s2" 20, "s3" 40 and "s2" 429s
    When a funded consumer relays with provider.order ["s2"] and provider.sort "throughput"
    Then attempt 1 hit "s2" and attempt 2 hit "s1"

  Scenario: sort with allow_fallbacks false is a single attempt at the sorted head
    Given "s1" 429s
    When a funded consumer relays with provider.sort "price" and provider.allow_fallbacks false
    Then exactly one attempt was made, to "s1"
    And the response is 429

  # corrected 2026-10-01 (founder-approved): assumes a private band with several stations; a band is ONE node id today (resolveFreqAllow). Multi-station bands are not part of this set.
  @later
  Scenario: sort on a private band sorts within the band
    Given a private band "B" with stations "p1" (out 2.00) and "p2" (out 1.00) for "m"
    When 20 funded consumers relay with roger.freq for band "B" and provider.sort "price"
    Then every pick is "p2"

  Scenario: sort on a grant sorts within the grant's nodes
    Given a grant scoped to nodes ["s2", "s3"]
    When 20 grant relays with provider.sort "price"
    Then every pick is "s2"

  Scenario: sort on an anonymous free relay sorts within free stations
    Given "f1" (tps 10) and "f2" (tps 50) serve "m" free
    When 20 anonymous consumers relay with provider.sort "throughput"
    Then every pick is "f2"

  Scenario: sort on a streaming request behaves identically
    When 20 funded consumers relay with "stream": true and provider.sort "price"
    Then every pick is "s1"

  Scenario: sort widens nothing - a station excluded by any hard filter stays excluded
    Given "s1" lacks the "tools" capability
    When a funded consumer relays with tools and provider.sort "price"
    Then the pick is "s2"

  # ============================================================================
  # pref (weighted knob)
  # ============================================================================

  Scenario: pref defaults to balanced
    When 200 funded consumers relay with no routing object
    And 200 funded consumers relay with roger.pref "balanced"
    Then the two pick distributions are statistically indistinguishable

  Scenario: pref cheap favors the cheaper station more than balanced does
    When 200 funded consumers relay with roger.pref "cheap"
    Then "s1" is picked more often than under balanced

  # corrected 2026-10-01 (founder-approved): at tps 90 vs 20 "s3" already took every pick under every pref, so its share could not rise; at 90 vs 60 balanced splits the picks (about 70/30) and fast moves it.
  Scenario: pref fast favors the faster station more than balanced does
    Given measured tps "s3" 90 and "s1" 60
    When 200 funded consumers relay with roger.pref "fast"
    Then "s3" is picked more often than under balanced

  Scenario: pref reliable concentrates on the most reliable station more than balanced does
    Given "s2" has the best reliability spine
    When 200 funded consumers relay with roger.pref "reliable"
    Then "s2" is picked more often than under balanced

  Scenario: pref keeps the power-of-two-choices spread
    Given "s1" is strictly cheapest and every station is idle
    When 100 funded consumers relay with roger.pref "cheap"
    Then "s1" is not picked 100 times
    # a weighted preference, not a strict sort

  Scenario: pref never changes eligibility
    Given "s1" is strictly cheapest but cooling
    When a funded consumer relays with roger.pref "cheap"
    Then the pick is not "s1"

  Scenario Outline: pref rejects unknown values with a 400, not a silent balanced
    When a funded consumer relays with roger.pref <value>
    Then the response is 400 with error.code "invalid_routing_value" naming "roger.pref"
    Examples:
      | value      |
      | "cheapest" |
      | "CHEAP"    |
      | "fastest"  |
      | ""         |
      | 2          |
      | ["cheap"]  |
    # regression: router.go:52-63 maps any unknown header value to balanced silently

  Scenario: an unknown X-Roger-Pref header value still degrades to balanced (header compat)
    When a funded consumer relays with X-Roger-Pref "cheapest" and no body pref
    Then the request is served under balanced
    And a warning counter pref_header_unknown increments
    # the header path stays lenient for old clients; the body path is strict

  Scenario: pref and sort together is a 400
    When a funded consumer relays with roger.pref "cheap" and provider.sort "price"
    Then the response is 400 {"error":{"code":"conflicting_routing_keys","message":"provider.sort and roger.pref are exclusive"}}

  Scenario: pref and a :floor or :nitro sugar together is a 400
    When a funded consumer relays for "m:floor" with roger.pref "reliable"
    Then the response is 400 with error.code "conflicting_routing_keys"

  # corrected 2026-10-01 (founder-approved): same fixture defect as the pref-fast scenario (90 vs 20 left no share to move); 90 vs 60 lets cheap and fast disagree.
  Scenario: body pref wins over the X-Roger-Pref header
    Given measured tps "s3" 90 and "s1" 60
    When 200 funded consumers relay with X-Roger-Pref "cheap" and roger.pref "fast"
    Then "s3" is picked more often than under cheap

  Scenario: header pref alone is honored
    When 200 funded consumers relay with X-Roger-Pref "cheap" and no body
    Then "s1" is picked more often than under balanced

  Scenario: pref with order applies to the remainder only
    Given "s3" 429s
    When a funded consumer relays with provider.order ["s3"] and roger.pref "cheap"
    Then attempt 1 hit "s3" and attempt 2 is scored under cheap

  Scenario: pref with only applies within the only set
    When 200 funded consumers relay with provider.only ["s2", "s3"] and roger.pref "cheap"
    Then "s2" is picked more often than "s3"

  Scenario: pref with allow_fallbacks false is a single scored attempt under that pref
    Given "s1" 429s
    When 50 funded consumers relay with roger.pref "cheap" and provider.allow_fallbacks false
    Then every relay made exactly one attempt

  # --- pref: reachable from the first-party clients (closes the dead knob) ------
  @cli
  Scenario: roger use --pref sends the body pref
    When a consumer runs `roger use m --pref fast`
    Then every relay through the local proxy carries roger.pref "fast"

  @cli
  Scenario: roger use --pref rejects an unknown value locally
    When a consumer runs `roger use m --pref cheapest`
    Then the command exits non-zero naming the allowed values
    And no request reached the broker

  @cli
  Scenario: the config file's routing pref is sent when no flag is given
    Given the config sets limits.default.pref "reliable"
    When a consumer runs `roger use m`
    Then every relay carries roger.pref "reliable"

  @cli
  Scenario: the flag overrides the config file's pref
    Given the config sets limits.default.pref "reliable"
    When a consumer runs `roger use m --pref cheap`
    Then every relay carries roger.pref "cheap"

  @tui
  Scenario: the TUI limits editor exposes pref
    When the consumer opens the limits editor and chooses "fast"
    Then relays from the tuned band carry roger.pref "fast"
    And the choice persists to the config file

  @harness
  Scenario: the agent harness forwards the session's pref
    Given a session with pref "reliable"
    When an agent turn relays
    Then the relay carries roger.pref "reliable"

  @docs
  Scenario: OpenAPI documents pref, sort, order, only, ignore and allow_fallbacks
    When a client reads the OpenAPI document
    Then the chat-completions request schema lists provider.order, provider.only, provider.ignore, provider.allow_fallbacks, provider.sort and roger.pref with their allowed values
    And the 503 description no longer says "cheapest active price"

  # ============================================================================
  # Tower relay ids in node lists
  # ============================================================================

  @part-c

  Scenario: a Tower relay id in only admits the bridge for that Tower only
    Given a Tower "t1" hosts "m" and no direct station is in the only set
    When a signed-in consumer relays with provider.only ["t1"]
    Then the bridge serves through "t1"
    And X-RogerAI-Relay names "t1"

  @part-c

  Scenario: a Tower relay id in order ranks the bridge among direct stations
    Given a Tower "t1" hosts "m"
    When 20 signed-in consumers relay with provider.order ["t1", "s1"]
    Then every relay is served through "t1"

  Scenario: a direct node id in only never admits a Tower
    Given a Tower "t1" hosts "m"
    When 20 signed-in consumers relay with provider.only ["s1"]
    Then every pick is "s1" and the bridge is never entered

  Scenario: a Tower id in ignore excludes the bridge for that Tower
    Given a Tower "t1" hosts "m" and no direct station serves "m"
    When a signed-in consumer relays with provider.ignore ["t1"]
    Then the response is 503 {"error":{"code":"no_match"}}

  Scenario: a Tower cannot pose as a direct node id to appear in only
    Given a Tower registers a station whose relay name equals "s1"
    When a funded consumer relays with provider.only ["s1"]
    Then the pick is the direct station "s1"
    And the bridge is never entered
    # node ids and Tower relay ids are distinct namespaces; a list entry is matched in BOTH
    # independently (§5), so the direct "s1" is admitted and the Tower row named "s1" is
    # admitted too - and loses on the direct-first rule below, never by posing as "s1"

  @part-c

  Scenario: an id that exists in both namespaces admits both rows and the collision is warned once
    Given a Tower registers a station whose relay name equals "s2"
    When 20 funded consumers relay with provider.only ["s2"]
    Then every pick is either the direct "s2" or the Tower row "s2" and no other server
    And /admin/live warns exactly once about the id collision

  # ============================================================================
  # header equivalents: precedence table
  # ============================================================================

  Scenario Outline: body wins over header for the same node knob
    When a funded consumer relays with header <header> and body <body>
    Then the effective selection is <effective>
    And no error is returned
    Examples:
      | header                       | body                              | effective                 |
      | X-Roger-Node: s1             | provider.order ["s3"]             | order s3, fallbacks on    |
      | X-Roger-Node: s1             | provider.allow_fallbacks true     | s1 first, fallbacks on    |
      | X-Roger-Node: s1             | provider.only ["s1", "s2"]        | pin s1 kept (it is in only) |
      | X-Roger-Exclude-Nodes: s1    | provider.ignore ["s2"]            | ignore s1 and s2 (union)  |
      | X-Roger-Pref: cheap          | roger.pref "fast"                 | fast                      |
      | X-Roger-Pref: cheap          | provider.sort "latency"           | sort latency              |

  Scenario: a kept header pin outside a body only is a 400 conflicting_routing_keys
    # the header pin survives a body provider object with no order (§1a); a kept pin that
    # is not in only is the same conflict as order outside only
    When a funded consumer relays with header X-Roger-Node: s1 and body provider.only ["s2"]
    Then the response is 400 {"error":{"code":"conflicting_routing_keys"}}
    And no hold was placed
    # the pin header combined with a body only that excludes the pinned node: the body's
    # only is the allow set; the header's pin is not added to it

  Scenario: header only, no body - unchanged behavior
    Given "s1" 429s and "s2" serves
    When a funded consumer relays with X-Roger-Node "s1" and X-Roger-Exclude-Nodes "s3" and no body routing object
    Then exactly one attempt was made, to "s1"
    And the response is 429

  Scenario: a pin header naming an unknown station with no body finds nothing
    When a funded consumer relays with X-Roger-Node "s9"
    Then the response is 503 {"error":{"code":"no_match"}}

  # ============================================================================
  # carriers never reach the station
  # ============================================================================

  Scenario: the station receives no provider or roger object
    When a funded consumer relays with provider.order ["s1"], provider.ignore ["s3"], roger.pref "cheap"
    Then the body "s1" received has no "provider", "roger" or "models" key
    And the body "s1" received is otherwise byte-identical to what the consumer sent

  # ============================================================================
  # telemetry and logs
  # ============================================================================

  Scenario: /admin/live carries strict-order and no-fallback counters
    Given 4 relays used provider.order, 3 used provider.sort, and 2 no-fallback relays were refused no_match
    When the founder reads /admin/live
    Then it shows routing_strict_order 4, routing_strict_sort 3, routing_nofallback_refused 2

  Scenario: a strict pick is logged once with the reason
    When a funded consumer relays with provider.order ["s3"]
    Then exactly one log line names the request, "order", and "s3"

  Scenario: logs never print a band code from roger.freq alongside an order or only list
    When a funded consumer relays with roger.freq for band "B" and provider.only ["p1"]
    Then no log line contains the band code

  Scenario: a 400 for a malformed routing object is not a strike, not a hold, not a receipt
    When a funded consumer relays with provider.order [""]
    Then no hold was placed, no receipt was written, and no operator was struck

  # ============================================================================
  # anti-abuse
  # ============================================================================

  Scenario: a consumer cannot steer another consumer's traffic
    When consumer A relays with provider.order ["s3"]
    And consumer B relays with no routing object
    Then B's pick is by score and unaffected by A's order

  Scenario: a station cannot make itself first by echoing an order in its response
    Given "s1" answers with a body containing {"provider":{"order":["s1"]}}
    When a funded consumer relays and later relays again with no routing object
    Then the second pick is by score

  Scenario: a strict order at one station does not dogpile it past capacity without consent
    Given "s3" is at capacity
    When 50 funded consumers relay with provider.order ["s3"]
    Then every relay is dispatched to "s3" (the consumer chose it)
    And the capacity-aware load factor is reported on /admin/live for "s3"
    # strict means strict; the network does not second-guess a named station, it reports

  Scenario: a no-fallback relay cannot be used to probe whether a private station exists
    Given "p1" is a private (band-only) station for "m"
    When a funded consumer relays with provider.order ["p1"] and provider.allow_fallbacks false
    And a funded consumer relays with provider.order ["nonexistent"] and provider.allow_fallbacks false
    Then both responses are byte-identical 503 {"error":{"code":"no_match"}} bodies
    And both refusals ran the same constant-work path (the private lookup is performed whether or not the id exists)
