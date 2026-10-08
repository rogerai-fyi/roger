# Routing REGRESSION PINS (slice 0 of the routing-expression set): the eight defects the
# 2026-09-29 routing audit found in the EXISTING consumer routing surface, plus the doc drift
# around it. Each defect gets (a) a scenario stating the desired behavior so it goes RED
# against today's code, (b) its boundaries and neighbours, (c) the invariant next to it that
# must keep holding. Contract: features/routing/ROUTING-EXPRESSION-CONTRACT.md (§1a, §2, §5,
# §6, §7, §10, §12 rulings 5 and 6).
#
# THE EIGHT DEFECTS (as of origin/main 518c698b):
#   1. X-Roger-Pref is unreachable. The broker honors it (cmd/rogerai-broker/router.go:52-63,
#      tunnel.go:1818) and the client forwards ProxyOptions.Pref (internal/client/client.go:861),
#      but no CLI flag, config key or TUI setting ever sets it, and openapi.yaml omits it.
#   2. `--max-out 0` is documented as "no cap" (cmd/rogerai/main.go:991, main.go:2274) but
#      EffectiveMaxOut(0) returns ConsumerDefaultMaxOut = $10/1M (client.go:1436-1441) and the
#      broker does the same for an absent/0 header (pricesafety.go:39-44). A consumer cannot
#      remove the output cap.
#   3. Hide-curated (TUI key U, fNoCurated) refuses only curated-ONLY bands
#      (internal/tui/agent.go:1543-1553); routeExcludes (internal/tui/quant_route.go:119-130)
#      never names curated node ids, so a MIXED band can still route to a curated station.
#      features/curated/curated_dial.feature:51-55 promises the opposite.
#   4. The edge/Tower coin (tunnel.go:1870-1874) skips X-Roger-Min-TPS, X-Roger-Exclude-Nodes
#      (documented at edgebridge.go:166-168) and X-Roger-Pref on the bridge path.
#   5. capabilities are advertised (market.go:225, tools canary-verified; vision declared) but
#      pickFor (tunnel.go:3165-3355) never reads them: a `tools` or image request can land on a
#      node without the capability.
#   6. The standing quant rule (config limits.models.<m>.quants, Limit.acceptsQuant) binds only
#      inside the TUI via exclude lists; standalone `roger use` never applies it
#      (quant_route.go:65-70), contradicting the Limit.Quants doc (internal/tui/view_money.go:33-35).
#   7. Streams carry only X-RogerAI-Provider (tunnel.go:2528); cost arrives as the trailing
#      `: rogerai-cost=` SSE comment (tunnel.go:2884). No receipt, tokens, TPS or price lock.
#   8. The client proxy retries only transport errors and >= 500 (internal/client/failover.go:79-85),
#      and when it fails over it PINS the alternative via X-Roger-Node (client.go:864-866);
#      a pinned request skips the broker's own 3-station 429 plan (tunnel.go:1964).
#
# DOC DRIFT: openapi.yaml:276-289 documents 5 of 8 request headers and 6 of 14 response
# headers, and its description (openapi.yaml:266) still says "cheapest active price";
# web/src/manual.html mentions one routing header (line 1252).
#
# GROUND TRUTH for the fixes (per the contract): the pref knob reaches the header from CLI
# `--pref`, config `pref`, and the TUI; `--max-out 0` keeps the $10 default and SAYS so,
# `--max-out unlimited` sends the register ceiling (pricesafety.go:21, ROGERAI_MAX_PRICE_OUT
# default $100/1M) and the broker clamps anything above the ceiling to the ceiling; hidden
# curated supply is expressed as `roger.self_hosted_only:true`, a broker-side filter; the
# bridge evaluates every constraint the direct path evaluates; capability gating is implicit
# for tools/vision; the quant rule is sent as `provider.quantizations`; streams end with the
# broker's usage chunk; the client proxy fails over with `provider.order:[alt]` and never
# `allow_fallbacks:false`, and waits out a short band-cooling 503 once.
#
# Enforced by: cmd/rogerai-broker/routing_regression_pins_bdd_test.go (broker pins),
# internal/client/routing_regression_pins_bdd_test.go (proxy pins),
# cmd/rogerai/routing_flags_bdd_test.go (CLI pins), internal/tui/routing_pins_bdd_test.go.

Feature: The eight routing defects of the 2026-09-29 audit stay fixed

  Background:
    Given a broker with an empty in-memory node registry
    And the fee rate is 30%
    And the consumer default out-cap is $10/1M
    And the register out-price ceiling is $100/1M

  # --- defect 1: the pref knob is reachable ----------------------------------------
  # First-party clients send the BODY form (roger.pref, contract §1/§9); the header
  # X-Roger-Pref stays supported for hand-rolled callers and old brokers. "The routing pass
  # ran with the <pref> profile" is observed at the broker: the pick outcome under a fixed
  # candidate set, and the /admin/live routing_pref_<value> counter.

  @cli
  Scenario: `roger use --pref cheap` reaches the broker's routing pass
    Given a tuned band whose model is "qwen3-32b"
    When the operator runs `roger use qwen3-32b --pref cheap`
    And a chat request goes through the local proxy
    Then the request body carries roger.pref "cheap"
    And the broker's routing pass ran with the cheap profile

  @cli
  Scenario Outline: Every pref value the broker knows is settable from the CLI
    When the operator runs `roger use qwen3-32b --pref <pref>`
    And a chat request goes through the local proxy
    Then the request body carries roger.pref "<pref>"
    And the broker's routing pass ran with the <pref> profile

    Examples:
      | pref     |
      | cheap    |
      | balanced |
      | fast     |
      | reliable |

  @cli
  Scenario: An unknown --pref value is refused by the CLI before any request
    When the operator runs `roger use qwen3-32b --pref turbo`
    Then the command exits non-zero
    And the message names the four accepted values
    And no request reaches the broker

  @cli
  Scenario: The config key `pref` under limits.default is sent when no flag is given
    Given the config has limits.default.pref "reliable"
    When the operator runs `roger use qwen3-32b`
    And a chat request goes through the local proxy
    Then the request body carries roger.pref "reliable"
    And the broker's routing pass ran with the reliable profile

  @cli
  Scenario: A per-model pref overrides the default pref
    Given the config has limits.default.pref "cheap"
    And the config has limits.models."qwen3-32b".pref "fast"
    When the operator runs `roger use qwen3-32b`
    And a chat request goes through the local proxy
    Then the request body carries roger.pref "fast"
    And the broker's routing pass ran with the fast profile

  @cli
  Scenario: The --pref flag overrides the config for this session only
    Given the config has limits.default.pref "cheap"
    When the operator runs `roger use qwen3-32b --pref fast`
    And a chat request goes through the local proxy
    Then the request body carries roger.pref "fast"
    And the persisted config still says limits.default.pref "cheap"

  @cli
  Scenario: `roger config set-limit default --pref reliable` persists the knob
    When the operator runs `roger config set-limit default --pref reliable`
    Then the config has limits.default.pref "reliable"
    And `roger limits` shows the pref beside the price and tps limits

  @cli
  Scenario: `roger config set-limit` refuses an unknown pref
    When the operator runs `roger config set-limit default --pref turbo`
    Then the command exits non-zero
    And the config is unchanged

  @tui
  Scenario: The TUI limits editor exposes the pref and every in-booth path sends it
    Given the TUI limits editor sets pref "fast" for "qwen3-32b"
    When an agent turn runs on "qwen3-32b"
    Then the request body carries roger.pref "fast" and the broker's routing pass ran with the fast profile
    When an in-channel chat message is sent on "qwen3-32b"
    Then the request body carries roger.pref "fast" and the broker's routing pass ran with the fast profile
    When the live proxy relays a guest request on "qwen3-32b"
    Then the request body carries roger.pref "fast" and the broker's routing pass ran with the fast profile

  @cli
  Scenario: No pref anywhere means no pref key, no header, and the balanced default (unchanged)
    When the operator runs `roger use qwen3-32b`
    And a chat request goes through the local proxy
    Then the request body carries no roger.pref
    And the request carries no X-Roger-Pref header
    And the broker's routing pass ran with the balanced profile

  @cli
  Scenario: Only X-Roger-Max-Price-Out is still sent as a header by first-party clients (one release)
    # Every other routing knob travels in the body; the out-cap header is kept for one release
    # so an old broker still applies the consumer cap (contract §9, old-broker negotiation).
    When the operator runs `roger use qwen3-32b --pref cheap --min-tps 20 --confidential`
    And a chat request goes through the local proxy
    Then the request carries the header X-Roger-Max-Price-Out
    And the request carries no X-Roger-Pref, X-Roger-Min-TPS or X-Roger-Confidential header
    And the request body carries roger.pref "cheap", roger.min_tps 20 and roger.confidential true

  @broker
  Scenario: The pref is a scoring knob, never a filter (invariant)
    Given node "n-cheap" is on air for "qwen3-32b" at out-price $0.10/1M with tps 8
    And node "n-fast" is on air for "qwen3-32b" at out-price $2.00/1M with tps 80
    When a request routes for "qwen3-32b" with X-Roger-Pref "fast"
    Then both "n-cheap" and "n-fast" are candidates
    And "n-fast" outscores "n-cheap"

  @broker
  Scenario: The body form roger.pref wins over the header when both are present
    When a request for "qwen3-32b" carries X-Roger-Pref "cheap" and body roger.pref "fast"
    Then the broker routes with the fast profile

  @docs
  Scenario: OpenAPI documents X-Roger-Pref with its four values
    Then openapi.yaml lists the request header "X-Roger-Pref" on /v1/chat/completions
    And its description names cheap, balanced, fast and reliable

  # --- defect 2: --max-out 0 and the ceiling --------------------------------------

  @cli
  Scenario: `--max-out 0` sends the $10 default cap, and the help text says so
    When the operator runs `roger use qwen3-32b --max-out 0`
    And a chat request goes through the local proxy
    Then the broker receives the header X-Roger-Max-Price-Out "10"
    And `roger use -h` describes --max-out 0 as "the default $10/1M cap", not "no cap"
    And `roger config set-limit -h` describes --max-out 0 the same way

  @cli
  Scenario: `--max-out unlimited` sends the register ceiling
    When the operator runs `roger use qwen3-32b --max-out unlimited`
    And a chat request goes through the local proxy
    Then the broker receives the header X-Roger-Max-Price-Out "100"
    And the connect line says "out cap: none (register ceiling $100/1M)"

  @cli
  Scenario: `--max-out unlimited` is case-insensitive and trims whitespace
    When the operator runs `roger use qwen3-32b --max-out " UNLIMITED "`
    Then the broker receives the header X-Roger-Max-Price-Out "100"

  @cli
  Scenario: `--max-out unlimited` does not persist as a limit
    When the operator runs `roger config set-limit default --max-out unlimited`
    Then the config has limits.default.max_out 100
    And `roger limits` shows "$100/1M (network ceiling)" for it

  @cli
  Scenario Outline: A max-out between the default and the ceiling is honored as sent
    When the operator runs `roger use qwen3-32b --max-out <flag>`
    And a chat request goes through the local proxy
    Then the broker receives the header X-Roger-Max-Price-Out "<sent>"
    And the broker's effective out-cap is $<effective>/1M

    Examples:
      | flag  | sent  | effective |
      | 0.5   | 0.5   | 0.5       |
      | 10    | 10    | 10        |
      | 10.01 | 10.01 | 10.01     |
      | 42    | 42    | 42        |
      | 100   | 100   | 100       |

  @broker
  Scenario Outline: A max-out above the ceiling is clamped by the broker, not an error
    When a request for "qwen3-32b" carries X-Roger-Max-Price-Out "<sent>"
    Then the broker's effective out-cap is $100/1M
    And the response is not a 4xx on account of the cap

    Examples:
      | sent  |
      | 100.5 |
      | 1000  |
      | 1e9   |

  @cli
  Scenario: The CLI warns once when a max-out above the ceiling is passed
    When the operator runs `roger use qwen3-32b --max-out 250`
    Then the CLI prints "capped at the network ceiling $100/1M"
    And the broker receives the header X-Roger-Max-Price-Out "250"

  @cli
  Scenario: A negative --max-out is refused by the CLI
    When the operator runs `roger use qwen3-32b --max-out -1`
    Then the command exits non-zero
    And no request reaches the broker

  @cli
  Scenario Outline: An unparsable --max-out is refused by the CLI
    When the operator runs `roger use qwen3-32b --max-out <value>`
    Then the command exits non-zero
    And the message names "a $/1M price or the word unlimited"

    Examples:
      | value     |
      | free      |
      | none      |
      | NaN       |
      | Inf       |
      | 10USD     |
      | ""        |

  @broker
  Scenario: An absent header still gets the $10 default server-side (invariant)
    Given node "n-dear" is on air for "qwen3-32b" at out-price $12/1M
    When a hand-rolled request for "qwen3-32b" carries no X-Roger-Max-Price-Out header
    Then "n-dear" is NOT a candidate
    And the request finds no station serving

  @broker
  Scenario: The body form max_price.completion follows the same rules
    Given node "n-dear" is on air for "qwen3-32b" at out-price $12/1M
    When a request for "qwen3-32b" carries body provider.max_price.completion 0
    Then the broker's effective out-cap is $10/1M
    When a request for "qwen3-32b" carries body provider.max_price.completion 1000
    Then the broker's effective out-cap is $100/1M
    And "n-dear" is a candidate

  @broker
  Scenario: A body cap can never raise a header cap
    # Limits compose to the stricter (contract §1a, founder ruling 2026-10-01): the proxy
    # owner's cap travels in the header on every installed client, a guest app controls the
    # body. Found by the pre-push audit as a way to raise the owner's cap.
    Given node "n-dear" is on air for "qwen3-32b" at out-price $12/1M
    When a request for "qwen3-32b" carries X-Roger-Max-Price-Out "2" and body provider.max_price.completion 50
    Then the broker's effective out-cap is $2/1M
    When a request for "qwen3-32b" carries X-Roger-Max-Price-Out "50" and body provider.max_price.completion 2
    Then the broker's effective out-cap is $2/1M
    When a request for "qwen3-32b" carries X-Roger-Max-Price-Out "50" and body provider.max_price.completion 20
    Then the broker's effective out-cap is $20/1M

  @broker
  Scenario: A body floor can never lower a header min-tps floor
    Given node "n-slow" is on air for "qwen3-32b" with measured tps 12
    When a request for "qwen3-32b" carries X-Roger-Min-TPS "30" and body roger.min_tps 5
    Then the response is 503 with error code "no_match"

  @harness
  Scenario: The agent harness injects the same effective cap as `use` (invariant)
    Given the agent harness runs a turn with MaxPriceOut 0
    Then the broker receives the header X-Roger-Max-Price-Out "10"

  @broker
  Scenario: No offer above the ceiling can exist, so "unlimited" never buys above it (invariant)
    When a node registers "qwen3-32b" at out-price $101/1M
    Then the registration is rejected
    And a request with X-Roger-Max-Price-Out "100" has no offer above $100/1M to bind to

  # --- defect 3: hidden curated supply never serves -------------------------------

  @tui
  Scenario: Hiding curated on a MIXED band never routes to a curated station
    Given node "n-human" is on air for "qwen3-32b" at out-price $0.50/1M
    And curated station "cur-1" is on air for "qwen3-32b" at out-price $0.20/1M
    And the TUI operator has pressed U (curated hidden)
    And the operator is tuned to the "qwen3-32b" band
    When an agent turn runs
    Then the request carries body roger.self_hosted_only true
    And "cur-1" is NOT a candidate
    And "n-human" serves

  @tui
  Scenario: The in-channel chat on a mixed band honors the hide too
    Given a mixed band for "qwen3-32b" with human "n-human" and curated "cur-1"
    And the TUI operator has pressed U (curated hidden)
    When an in-channel chat message is sent on "qwen3-32b"
    Then the request carries body roger.self_hosted_only true
    And "n-human" serves

  @tui
  Scenario: The live proxy inside the booth honors the hide for guest requests
    Given a mixed band for "qwen3-32b" with human "n-human" and curated "cur-1"
    And the TUI operator has pressed U (curated hidden)
    When a guest operator sends a request through the live proxy
    Then the request carries body roger.self_hosted_only true

  @tui
  Scenario: U is session-scoped - a later `roger use` outside the TUI does not inherit the hide (unchanged)
    # Approved features/curated/curated_dial.feature:34: the hide "persists for the session".
    # It is never written to config.json; the standing way to say it outside the booth is
    # `--self-hosted` or a profile.
    Given the TUI operator has pressed U (curated hidden)
    And the TUI has been closed
    When the operator runs `roger use qwen3-32b`
    And a chat request goes through the local proxy
    Then the request carries no roger.self_hosted_only
    And config.json carries no self_hosted_only key

  @cli
  Scenario: `roger use --self-hosted` expresses the same rule without the TUI
    When the operator runs `roger use qwen3-32b --self-hosted`
    And a chat request goes through the local proxy
    Then the request carries body roger.self_hosted_only true

  @tui
  Scenario: Hidden curated is a broker filter, not an exclude list
    Given a mixed band for "qwen3-32b" with human "n-human" and curated "cur-1"
    And the TUI operator has pressed U (curated hidden)
    When an agent turn runs
    Then the request's X-Roger-Exclude-Nodes header does not name "cur-1"
    And curated stations that register AFTER the last dial scan are still excluded

  @tui
  Scenario: A curated-only band under the hide still refuses with the named choice (unchanged)
    Given curated station "cur-1" is the only station on air for "gpt-4.1"
    And the TUI operator has pressed U (curated hidden)
    When an agent turn runs on "gpt-4.1"
    Then the turn is refused before any request
    And the refusal says curated supply is hidden and names U to show it

  @tui
  Scenario: Pressing U again restores curated supply for the next turn
    Given a mixed band for "qwen3-32b" with human "n-human" and curated "cur-1"
    And the TUI operator has pressed U twice (curated shown)
    When an agent turn runs
    Then the request carries no roger.self_hosted_only key
    And both "n-human" and "cur-1" are candidates

  @tui
  Scenario: Failover under the hide stays among human stations
    Given human nodes "n-h1" and "n-h2" and curated "cur-1" are on air for "qwen3-32b"
    And the TUI operator has pressed U (curated hidden)
    And "n-h1" answers the next request with an upstream 429
    When an agent turn runs and "n-h1" is picked first
    Then the failover plan contains "n-h2" and not "cur-1"

  @tui
  Scenario: The hide never removes a human station (invariant)
    Given a mixed band for "qwen3-32b" with human "n-human" and curated "cur-1"
    And the TUI operator has pressed U (curated hidden)
    When a request routes with roger.self_hosted_only true
    Then "n-human" is a candidate

  @docs
  Scenario: The curated_routing "filter" section is no longer empty
    Then features/curated/curated_routing.feature has a scenario under its filter section
    And it cites roger.self_hosted_only

  # --- defect 4: the edge coin honors the direct path's constraints ---------------

  @broker
  Scenario: The Tower path refuses a request whose min-tps the Tower does not meet
    Given node "n-direct" is on air for "qwen3-32b" with measured tps 40
    And an approved Tower "tw-1" serves "qwen3-32b" with measured tps 12
    And the coin would send this request to the edge
    When a request routes for "qwen3-32b" with X-Roger-Min-TPS "30"
    Then the bridge declines the Tower
    And "n-direct" serves

  @broker
  Scenario: The Tower path refuses a request that excludes that Tower
    Given node "n-direct" is on air for "qwen3-32b"
    And an approved Tower "tw-1" serves "qwen3-32b"
    And the coin would send this request to the edge
    When a request routes for "qwen3-32b" with X-Roger-Exclude-Nodes "tw-1"
    Then the bridge declines the Tower
    And "n-direct" serves

  @broker
  Scenario: The Tower path applies the pref profile when choosing among Towers
    Given no direct node is on air for "qwen3-32b"
    And approved Towers "tw-cheap" at $0.10/1M and "tw-fast" at $2.00/1M with tps 80 serve it
    When a request routes for "qwen3-32b" with X-Roger-Pref "cheap"
    Then "tw-cheap" is tried first
    When a request routes for "qwen3-32b" with X-Roger-Pref "fast"
    Then "tw-fast" is tried first

  @broker
  Scenario: The direct path's exclusion of a node id still does not touch an unrelated Tower
    Given node "n-direct" is on air for "qwen3-32b"
    And an approved Tower "tw-1" serves "qwen3-32b"
    When a request routes for "qwen3-32b" with X-Roger-Exclude-Nodes "n-direct"
    Then "n-direct" is NOT a candidate
    And the bridge may serve via "tw-1"

  @broker
  Scenario: An unmeasured Tower passes a min-tps floor, as an unmeasured node does (invariant)
    Given an approved Tower "tw-new" serves "qwen3-32b" with no tps measured yet
    When a request routes for "qwen3-32b" with X-Roger-Min-TPS "30"
    Then the bridge may serve via "tw-new"

  @broker
  Scenario: The coin still never diverts free or self-use traffic to a billed Tower (invariant)
    Given the caller owns node "n-mine" on air for "qwen3-32b"
    And an approved Tower "tw-1" serves "qwen3-32b" at $1.00/1M
    When the caller's request routes for "qwen3-32b"
    Then the bridge is declined
    And "n-mine" serves at $0

  @broker
  Scenario: The coin still never widens eligibility (invariant)
    Given no direct node satisfies X-Roger-Min-TPS "30" for "qwen3-32b"
    And no Tower satisfies it either
    When a request routes for "qwen3-32b" with X-Roger-Min-TPS "30"
    Then the response is 503 with error code "no_match"

  # --- defect 5: capabilities gate routing ----------------------------------------
  # The full matrix lives in features/routing/capability_gating.feature; one pin here.

  @broker
  Scenario: A request carrying tools never lands on a node without verified tools
    Given node "n-plain" is on air for "qwen3-32b" with no verified capabilities
    And node "n-tools" is on air for "qwen3-32b" with canary-verified "tools"
    When a request for "qwen3-32b" carries a non-empty tools array
    Then "n-plain" is NOT a candidate
    And "n-tools" serves

  @broker
  Scenario: A request carrying an image never lands on a node that did not declare vision
    Given node "n-text" is on air for "qwen3-32b" with no declared capabilities
    And node "n-eyes" is on air for "qwen3-32b" with declared "vision"
    When a request for "qwen3-32b" carries an image_url content part
    Then "n-text" is NOT a candidate
    And "n-eyes" serves

  @broker
  Scenario: A plain text request is unaffected by capability gating (invariant)
    Given node "n-plain" is on air for "qwen3-32b" with no capabilities
    When a request for "qwen3-32b" carries only text messages
    Then "n-plain" is a candidate

  # --- defect 6: the quant rule binds in standalone `roger use` -------------------

  @cli
  Scenario: The standing quant rule is sent as provider.quantizations plus "unknown" by `roger use`
    # A standing RULE reads an unlabeled row as "not contradicted" (Limit.acceptsQuant, pinned by
    # TestAbsenceIsReadDifferentlyByRowAndRule); the body form keeps that meaning by adding
    # "unknown" (contract §5, §9). A tuned ROW or a --quant flag names its labels only.
    Given the config has limits.models."qwen3-32b".quants ["Q8_0", "BF16"]
    When the operator runs `roger use qwen3-32b`
    And a chat request goes through the local proxy
    Then the request carries body provider.quantizations ["Q8_0", "BF16", "unknown"]
    And the request carries no X-Roger-Exclude-Nodes derived from quant

  @cli
  Scenario: The broker filters on the quant rule, so a late-registering station is bound too
    Given the config has limits.models."qwen3-32b".quants ["Q8_0"]
    And node "n-q8" is on air for "qwen3-32b" with quant "Q8_0"
    When `roger use qwen3-32b` is running
    And node "n-q4" registers "qwen3-32b" with quant "Q4_K_M" after the proxy started
    And a chat request goes through the local proxy
    Then "n-q4" is NOT a candidate
    And "n-q8" serves

  @cli
  Scenario: Quant matching is case-insensitive on the verbatim label
    Given the config has limits.models."qwen3-32b".quants ["q8_0"]
    And node "n-q8" is on air for "qwen3-32b" with quant "Q8_0"
    When a chat request goes through the local proxy
    Then "n-q8" is a candidate

  @cli
  Scenario: Quant labels are never bucketed (invariant)
    Given the config has limits.models."qwen3-32b".quants ["Q4_K_M"]
    And node "n-iq4" is on air for "qwen3-32b" with quant "IQ4_XS"
    When a chat request goes through the local proxy
    Then "n-iq4" is NOT a candidate

  @cli
  Scenario: `--quant` on the command line overrides the stored rule for the session
    Given the config has limits.models."qwen3-32b".quants ["Q8_0"]
    When the operator runs `roger use qwen3-32b --quant BF16`
    And a chat request goes through the local proxy
    Then the request carries body provider.quantizations ["BF16"]
    And the persisted config still says ["Q8_0"]

  @tui
  Scenario: The TUI sends the same body key instead of exclude lists
    Given the config has limits.models."qwen3-32b".quants ["Q8_0"]
    And no row is tuned by quant
    When an agent turn runs on "qwen3-32b" inside the TUI
    Then the request carries body provider.quantizations ["Q8_0", "unknown"]
    And the request carries no X-Roger-Exclude-Nodes derived from quant

  @tui
  Scenario: The tuned row's quant becomes a one-entry quantizations list in the TUI
    Given the dial groups "qwen3-32b" into a "Q4_K_M" row and a "BF16" row
    And the operator tunes the "BF16" row
    When an in-channel chat message is sent
    Then the request carries body provider.quantizations ["BF16"]

  @tui
  Scenario: The tuned row and the standing rule intersect
    Given the config has limits.models."qwen3-32b".quants ["Q8_0", "BF16"]
    And the operator tunes the "BF16" row
    When an in-channel chat message is sent
    Then the request carries body provider.quantizations ["BF16"]

  @tui
  Scenario: A tuned row outside the standing rule is refused before any request
    Given the config has limits.models."qwen3-32b".quants ["Q8_0"]
    And the operator tunes the "Q4_K_M" row
    When an in-channel chat message is sent
    Then the turn is refused with a message naming the quant rule
    And no request reaches the broker

  @cli
  Scenario: The standing quant rule keeps letting an unlabeled station pass, via "unknown"
    # Limit.acceptsQuant reads absence as "not contradicted" by design (pinned by
    # TestAbsenceIsReadDifferentlyByRowAndRule); the body form preserves that meaning by
    # sending the rule's labels plus "unknown" (contract §5). The tuned ROW, by contrast,
    # names one label and does not add "unknown".
    Given the config has limits.models."qwen3-32b".quants ["Q8_0"]
    And node "n-unlabeled" is on air for "qwen3-32b" with no quant label
    And node "n-q4" is on air for "qwen3-32b" with quant "Q4_K_M"
    When a chat request goes through the local proxy
    Then the request body carries provider.quantizations ["Q8_0", "unknown"]
    And "n-unlabeled" is a candidate
    And "n-q4" is NOT a candidate

  @tui
  Scenario: A tuned quant row does not add "unknown"
    Given the operator tunes the "Q8_0" row of "qwen3-32b"
    And node "n-unlabeled" is on air for "qwen3-32b" with no quant label
    When a chat request goes through the local proxy
    Then the request body carries provider.quantizations ["Q8_0"]
    And "n-unlabeled" is NOT a candidate

  @cli
  Scenario: No quant rule and no tuned quant sends no quantizations key (unchanged)
    When the operator runs `roger use qwen3-32b`
    And a chat request goes through the local proxy
    Then the request carries no provider.quantizations key

  @docs
  Scenario: The Limit.Quants doc comment and the behavior now agree
    Then the Limit.Quants documentation says the rule binds in the TUI AND in `roger use`
    And quant_route.go no longer carries the "does NOT yet apply" note

  # --- defect 7: streams carry the receipt -----------------------------------------
  # The full matrix lives in features/trust/stream_receipt_parity.feature; one pin here.

  @broker
  Scenario: A streamed relay ends with the broker's usage chunk carrying the receipt
    Given node "n-1" is on air for "qwen3-32b"
    When a streaming request for "qwen3-32b" is served by "n-1" and settles at cost $0.000420
    Then the last data frame before "[DONE]" is a usage chunk with empty choices
    And its usage.rogerai.receipt decodes to a receipt naming "n-1" and "qwen3-32b"
    And its usage.cost equals 0.000420
    And the response header X-RogerAI-Provider is "n-1"

  @broker
  Scenario: A non-stream relay's headers are unchanged (invariant)
    Given node "n-1" is on air for "qwen3-32b"
    When a non-streaming request for "qwen3-32b" is served by "n-1"
    Then the response carries X-RogerAI-Receipt, X-RogerAI-Cost, X-RogerAI-Tokens-In, X-RogerAI-Tokens-Out, X-RogerAI-Balance, X-RogerAI-Price, X-RogerAI-TPS and X-RogerAI-Quality

  # --- defect 8: client failover no longer disables broker failover ----------------

  @proxy
  Scenario: The client proxy fails over with provider.order, not a pin
    Given nodes "n-1" and "n-2" are on air for "qwen3-32b"
    And the broker answers the first proxy attempt with a 502 served by "n-1"
    When a chat request goes through the local proxy
    Then the proxy's second attempt carries body provider.order ["n-2"]
    And the proxy's second attempt carries no X-Roger-Node header
    And the proxy's second attempt carries no provider.allow_fallbacks false

  @proxy
  Scenario: The broker may still fail over past the proxy's preferred alternative
    Given nodes "n-1", "n-2" and "n-3" are on air for "qwen3-32b"
    And the broker answers the first proxy attempt with a 502 served by "n-1"
    And "n-2" answers its next request with an upstream 429
    When a chat request goes through the local proxy
    Then the proxy's second attempt is served by "n-3" with status 200
    And the consumer never sees the 429

  @proxy
  Scenario: The proxy's failed set travels as provider.ignore and X-Roger-Exclude-Nodes agree
    Given nodes "n-1", "n-2" and "n-3" are on air for "qwen3-32b"
    And the broker answers the first two proxy attempts with 502s served by "n-1" then "n-2"
    When a chat request goes through the local proxy
    Then the proxy's third attempt carries body provider.ignore ["n-1", "n-2"]
    And its provider.order names neither "n-1" nor "n-2"

  @proxy
  Scenario: A short band-cooling 503 is waited out once, without re-pinning
    Given node "n-only" is the only station on air for "qwen3-32b"
    And "n-only" is cooling for 3 more seconds
    When a chat request goes through the local proxy
    Then the proxy receives 503 with error code "band_cooling" and Retry-After "3"
    And the proxy waits 3 seconds and retries exactly once with no provider.order
    And the retry is served with status 200

  @proxy
  Scenario Outline: A band-cooling 503 whose Retry-After exceeds the wait budget is passed through
    Given node "n-only" is the only station on air for "qwen3-32b"
    And "n-only" is cooling for <secs> more seconds
    When a chat request goes through the local proxy
    Then the proxy does not sleep
    And the client sees 503 with Retry-After "<secs>"

    Examples:
      | secs |
      | 6    |
      | 30   |
      | 120  |

  @proxy
  Scenario: A band-cooling 503 with Retry-After of exactly 5 seconds is waited out
    Given node "n-only" is the only station on air for "qwen3-32b"
    And "n-only" is cooling for 5 more seconds
    When a chat request goes through the local proxy
    Then the proxy waits 5 seconds and retries exactly once

  @proxy
  Scenario: The band-cooling wait happens at most once per request
    Given node "n-only" is the only station on air for "qwen3-32b"
    And "n-only" is cooling for 2 more seconds, then cools again for 2 more on the retry
    When a chat request goes through the local proxy
    Then the proxy waits once
    And the client sees the second 503 with its Retry-After

  @proxy
  Scenario: The band-cooling wait respects the caller's context
    Given node "n-only" is the only station on air for "qwen3-32b"
    And "n-only" is cooling for 4 more seconds
    And the caller cancels its request after 1 second
    When a chat request goes through the local proxy
    Then the proxy stops waiting when the caller cancels
    And no retry is sent

  @proxy
  Scenario: A broker rate-limit 429 is still passed straight through (approved, unchanged)
    Given the broker returns 429 with Retry-After "30" from its per-identity limiter
    When a chat request goes through the local proxy
    Then the status is 429
    And the client sees Retry-After "30"
    And the proxy does not retry

  @proxy
  Scenario: A final upstream 429 from the broker is still passed through (approved, unchanged)
    Given node "n-only" is the only station on air for "qwen3-32b"
    And "n-only" answers with an upstream 429 and Retry-After "20"
    When a chat request goes through the local proxy
    Then the status is 429
    And the client sees Retry-After "20"
    And the proxy does not retry

  @proxy
  Scenario: The proxy's 4-attempt budget and backoff are unchanged (invariant)
    Given every broker attempt answers 502
    When a chat request goes through the local proxy
    Then exactly 4 attempts are made
    And the backoffs before attempts 2, 3 and 4 are 200ms, 400ms and 800ms
    And the client sees an OpenAI-shaped 502 "upstream_unavailable"

  @proxy
  Scenario: A confidential session's failover still stays confidential (invariant)
    Given the proxy session is confidential
    And the broker answers the first proxy attempt with a 502
    When a chat request goes through the local proxy
    Then every attempt carries body roger.confidential true
    And on every attempt only TEE-attested nodes were candidates at the broker's routing pass
    And the alternative named in provider.order is a confidential node

  @proxy
  Scenario: A private-band session has no public alternative to order (invariant)
    Given the proxy session is tuned to a private band by its code
    And the broker answers the first proxy attempt with a 502
    When a chat request goes through the local proxy
    Then the retry carries the band code and no provider.order
    And the band code never appears in a log line

  # --- doc drift ---------------------------------------------------------------------

  @docs
  Scenario Outline: OpenAPI documents every request header the relay reads
    Then openapi.yaml lists the request header "<header>" on /v1/chat/completions

    Examples:
      | header                 |
      | X-Roger-Confidential   |
      | X-Roger-Min-TPS        |
      | X-Roger-Max-Price      |
      | X-Roger-Max-Price-Out  |
      | X-Roger-Pref           |
      | X-Roger-Node           |
      | X-Roger-Exclude-Nodes  |
      | X-Roger-Freq           |

  @docs
  Scenario: OpenAPI documents the $10 default behind an absent X-Roger-Max-Price-Out
    Then the X-Roger-Max-Price-Out description names the default consumer cap
    And it says a value above the ceiling is clamped, not rejected

  @docs
  Scenario Outline: OpenAPI documents every response header the relay sets
    Then openapi.yaml lists the response header "<header>" on the 200 of /v1/chat/completions

    Examples:
      | header                     |
      | X-RogerAI-Receipt          |
      | X-RogerAI-Provider         |
      | X-RogerAI-Model            |
      | X-RogerAI-Relay            |
      | X-RogerAI-Cost             |
      | X-RogerAI-Tokens-In        |
      | X-RogerAI-Tokens-Out       |
      | X-RogerAI-Balance          |
      | X-RogerAI-Price            |
      | X-RogerAI-TPS              |
      | X-RogerAI-Quality          |
      | X-RogerAI-Monthly-Cap      |
      | X-RogerAI-Monthly-Spend    |
      | X-RogerAI-Monthly-Pct      |
      | X-RogerAI-Monthly-Notice   |

  @docs
  Scenario: OpenAPI documents the stream usage chunk and the trailing cost comment
    Then the 200 description for stream:true names the final usage chunk
    And it names the `: rogerai-cost=` comment as deprecated-but-present

  @docs
  Scenario: OpenAPI no longer describes selection as "cheapest active price"
    Then openapi.yaml contains no "cheapest active price" under /v1/chat/completions
    And the description names the eligibility filters and the scored selection

  @docs
  Scenario: OpenAPI documents the routing body object
    Then the ChatRequest schema has properties "models", "provider" and "roger"
    And each routing key in the contract appears with its type and range

  @docs
  Scenario: OpenAPI documents the routing error codes
    Then the 400 response documents unknown_routing_key, invalid_routing_value and conflicting_routing_keys
    And the 503 response documents no_match and band_cooling

  @docs
  Scenario: The manual's routing section lists the body object and every header
    Then web/src/manual.html has a routing section
    And it lists the "models", "provider" and "roger" keys
    And it lists every X-Roger-* request header the relay reads

  @docs
  Scenario: The /discover and /market docs no longer imply query filters that are no-ops
    Then the openapi.yaml description of /discover names the filter params it honors
    And the openapi.yaml description of /market names the filter params it honors
