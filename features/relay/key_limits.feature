# PROPOSED SPEC - 2026-09-30, awaiting founder approval. Part of the routing-expression set;
# the vocabulary is features/routing/ROUTING-EXPRESSION-CONTRACT.md §11 (plus §1b, §2). The
# management surface (mint / patch / list / delete, windows, expiry, audit) is
# features/auth/key_guardrails.feature; THIS file is what a key does to a relay.
#
# GROUND TRUTH (origin/main 518c698b, cmd/rogerai-broker/tunnel.go relay()):
#   order of gates on a priced relay today:
#     1. identity: grant bearer first (1633), else signature / web session / anon (1640-1672)
#     2. rate limit per identity (1680-1715)
#     3. grant token caps (daily/monthly tokens), 429 (1735-1740)
#     4. request id minted (1747)
#     5. moderation: sync mode blocks HERE (451/503) before any pick; async hands off (1752-1786)
#     6. routing constraints parsed (1788-1833); grant node/model allow-lists (1836-1847)
#     7. pickFor (1852); failover plan (1955-1993)
#     8. HOLD GATE (1995-2050): for a paid plan, monthlyCapCheck(payer, maxCost) rejects 402
#        when spend+maxCost > cap (monthlycap.go:63-95) and sets X-RogerAI-Monthly-* headers;
#        the plan CEILING (priciest failover candidate) is only ATTEMPTED when monthlyCapFits
#        (no headers, no email); HoldFor is the atomic wallet reserve (402 "insufficient
#        balance - add funds" when it fails); trimPlan drops what the hold cannot cover.
#     9. dispatch; settle clamps cost at the authorized maximum (features/pricing/
#        price_floor.feature "cost above the authorized maximum is still capped"); a recount
#        only ever lowers the billed count (features/money/recount_billing.feature).
#   Free / self-use ($0) skips the whole hold block (maxCost == 0), so it is never cap-blocked.
#   The monthly cap is ONE number per account (store MonthlyCapOf), notify at 80%
#   (store.CapNearThreshold, internal/store/cap.go:18), headers set by setCapHeaders
#   (monthlycap.go:107-125).
#
# WHAT CHANGES. A `Bearer rog-key_...` request resolves to the minting account's wallet as
# payer and adds, at the SAME hold gate, a per-key spend limit over a UTC reset window, and,
# at the same point as the grant allow-lists, per-key allowed_models / allowed_nodes. The key
# limit is reserved ATOMICALLY in the shared store (a key-scoped reserve placed next to the
# wallet hold, captured or released on the same exit paths) so concurrent requests on any
# number of instances can never push settled key spend over the limit. [PROPOSED: the
# directive allowed "one in-flight hold per instance" of overshoot; a read-then-hold check
# would have that race, an atomic reserve has none, and money paths do not get races.]
#
# Enforced by: cmd/rogerai-broker/key_limits_bdd_test.go (godog, strict; to be written at
# RED) driving the real relay against a real store (testcontainers Postgres) with fake
# stations, sharing steps with relay_spend_bdd_test.go and the monthly-cap tests.

Feature: Key limits on the relay - the per-key spend ceiling, window, and allow-lists

  Background:
    Given a broker with the money store and the shared store wired
    And the fee rate is 10%
    And account "acct-a" is logged in (wallet "u_gh_a") with balance $20.00 and no monthly cap
    And "acct-a" minted key "k1" with limit_usd 5.00 and reset "monthly"
    And node "n1" is on air for "qwen3-32b" at $1.00/1M in, $2.00/1M out, proven live
    And node "n2" is on air for "qwen3-32b" at $2.00/1M in, $4.00/1M out, proven live
    And a relay for "qwen3-32b" with max_tokens 1000 holds $0.003 on "n1" and $0.006 on "n2"

  # --- where the gate sits -------------------------------------------------------
  Scenario: The key limit is checked at the hold gate, after moderation and the pick
    Given moderation mode is "sync"
    When a priced relay bearing "k1" arrives
    Then moderation runs first, then the pick, then the key-limit check next to the monthly-cap check, then the hold, then dispatch
    And a prompt moderation rejects is 451 with no key-limit check, no hold, and no usage change

  # corrected 2026-10-02 (founder-approved): the $5.00 spent through the key was debited from the $20.00 balance
  Scenario: The key limit is checked before the wallet hold, so a refused request never touches the wallet
    Given "k1" has $5.00 spent this window
    When a priced relay bearing "k1" arrives
    Then it is 402 before HoldFor is called and the balance stays $15.00

  Scenario: A free ($0) relay bearing a key skips the limit entirely
    Given node "nf" is on air for "gpt-oss-20b" at $0/$0
    And "k1" has $5.00 spent this window
    When a relay bearing "k1" for "gpt-oss-20b" arrives
    Then it is served with X-RogerAI-Cost 0 and no 402 (there is nothing to protect)
    And "k1" usage stays $5.00 and its request count increments

  Scenario: A request that never served does not consume key limit
    Given every station fails the relay bearing "k1"
    When the relay returns its error
    Then the key reserve is released with the wallet hold and "k1" usage is unchanged

  # --- the ceiling and the lower-of rule ----------------------------------------
  Scenario: A request whose worst case fits exactly is allowed
    Given "k1" has $4.997 spent this window
    When a relay bearing "k1" that holds $0.003 arrives
    Then it is served (spend + hold == limit is allowed, like the monthly cap)

  Scenario: A request whose worst case exceeds the remaining by any amount is 402
    Given "k1" has $4.998 spent this window
    When a relay bearing "k1" that holds $0.003 arrives
    Then it is 402 with limit_source "key_limit"

  Scenario: The remaining is measured against the worst case, not the expected cost
    Given "k1" has $4.999 spent this window
    And the request would in practice cost $0.0005
    When it arrives holding $0.003
    Then it is 402 (we never authorize what we could not capture)

  Scenario: The plan ceiling is attempted under the key limit only when it fits
    Given "k1" has $4.995 spent this window (remaining $0.005)
    When a relay bearing "k1" plans n1 ($0.003) then n2 ($0.006)
    Then the hold covers n1 alone, n2 is trimmed from the plan, no 402 (a refused ceiling is not a refused request)
    And no key notice email or at-limit header is triggered by the ceiling probe

  Scenario: The hold is never shrunk below the first pick's true upper bound
    Given "k1" has remaining $0.002
    When a relay bearing "k1" whose cheapest pick holds $0.003 arrives
    Then it is 402 (shrinking the hold would break the settle-time ceiling; features/money/holds.feature C1)

  Scenario Outline: The lower of key remaining, monthly-cap remaining, and balance binds
    Given "k1" has remaining $<key_rem>
    And "acct-a" has monthly cap remaining $<cap_rem> and balance $<balance>
    When a relay bearing "k1" that holds $0.003 arrives
    Then the outcome is <outcome> with limit_source "<source>"

    Examples:
      | key_rem | cap_rem   | balance | outcome | source      |
      | 0.010   | 0.010     | 0.010   | served  |             |
      | 0.002   | 0.010     | 0.010   | 402     | key_limit   |
      | 0.010   | 0.002     | 0.010   | 402     | monthly_cap |
      | 0.010   | 0.010     | 0.002   | 402     | credits     |
      | 0.002   | 0.002     | 0.010   | 402     | key_limit   |
      | 0.002   | 0.010     | 0.002   | 402     | key_limit   |
      | 0.010   | 0.002     | 0.002   | 402     | monthly_cap |
      | 0.002   | 0.002     | 0.002   | 402     | key_limit   |
      | 0.003   | 0.003     | 0.003   | served  |             |

  Scenario: An unlimited key (limit 0) never produces key_limit
    Given "acct-a" minted key "k0" with limit_usd 0
    When 10,000 priced relays bearing "k0" are served over the month
    Then none is 402 for key_limit (the monthly cap and balance still bind as today)

  Scenario: A monthly cap set after the key still binds the key's requests
    Given "acct-a" sets monthly cap $1.00 and has $1.00 month spend
    When a relay bearing "k1" (remaining $5.00) arrives
    Then it is 402 with limit_source "monthly_cap" and the existing monthly message

  # --- the 402 body ---------------------------------------------------------------
  Scenario Outline: Every 402 names its source and a remedy
    When a priced relay is refused because of <source>
    Then the body is {"error":{"code":"<code>","message":"<message>","metadata":{"limit_source":"<source>","remedy_hint":"<hint>"}}}

    Examples:
      | source      | code                 | message                                                                 | hint                                                                                          |
      | key_limit   | key_limit_reached    | key spend limit reached: $5.00 of $5.00 this month (resets 2026-11-01T00:00:00Z) | raise it with `roger keys set key_x --limit`, wait for the reset, or use another key |
      | monthly_cap | monthly_cap_reached  | monthly spend limit reached: $50.00 of $50.00 this month - raise it with `roger limit --monthly` (or [3] CONFIG), or wait until next month | raise it with `roger limit --monthly` (or [3] CONFIG), or wait until next month |
      | credits     | insufficient_balance | insufficient balance - add funds                                        | top up at /billing                                                                            |

  Scenario: The monthly-cap message text is unchanged for every caller (monthlycap.go:73)
    # The message keeps today's full text, remedy clause included, for key and non-key callers
    # alike; remedy_hint repeats the remedy clause so machine readers need not parse the message.
    When a signed (no key) relay is refused by the monthly cap
    Then the message is exactly today's "monthly spend limit reached: $X of $Y this month - raise it with `roger limit --monthly` (or [3] CONFIG), or wait until next month"
    And the body additionally carries metadata.limit_source "monthly_cap" and code "monthly_cap_reached"

  Scenario: A key with reset "none" says so in the message
    Given "acct-a" minted key "kn" with limit_usd 1 and reset "none", $1.00 spent
    When a priced relay bearing "kn" arrives
    Then the message reads "key spend limit reached: $1.00 of $1.00 (no reset)"

  Scenario: The 402 never carries a receipt and carries X-RogerAI-Cost 0
    When a relay bearing "k1" is 402 for key_limit
    Then the response has X-RogerAI-Cost 0, no X-RogerAI-Receipt, no X-RogerAI-Provider

  Scenario: The 402 carries the key headers at the limit
    Given "k1" has $5.00 spent this window
    When a relay bearing "k1" is refused
    Then X-RogerAI-Key-Limit is 5, X-RogerAI-Key-Spend is 5, X-RogerAI-Key-Pct is 100, X-RogerAI-Key-Notice is "key limit reached - $5.00 of $5.00 this month"

  # --- key notice headers ---------------------------------------------------------
  Scenario: A served request under 80% carries limit and spend, no notice
    Given "k1" has $1.00 spent this window
    When a relay bearing "k1" is served
    Then X-RogerAI-Key-Limit is 5, X-RogerAI-Key-Spend is 1, X-RogerAI-Key-Pct is 20, and X-RogerAI-Key-Notice is absent

  Scenario: Crossing 80% adds the near notice
    Given "k1" has $4.00 spent this window
    When a relay bearing "k1" is served
    Then X-RogerAI-Key-Notice is "you've used $4.00 of your $5.00 key limit (80%)"

  Scenario: Exactly at 80% is near, one cent under is not
    Given "k1" has $3.99 spent this window
    When a relay bearing "k1" is served
    Then X-RogerAI-Key-Notice is absent
    Given "k1" has $4.00 spent this window
    When a relay bearing "k1" is served
    Then X-RogerAI-Key-Notice is present

  Scenario: The key headers are absent for an unlimited key
    Given key "k0" with limit 0
    When a relay bearing "k0" is served
    Then no X-RogerAI-Key-* header is set (nothing to report), as the monthly headers do when unlimited

  Scenario: The key headers and the monthly headers coexist and are independent
    Given "acct-a" has monthly cap $50 with $45 spent, and "k1" has $1 spent
    When a relay bearing "k1" is served
    Then X-RogerAI-Monthly-Pct is 90 with its notice, X-RogerAI-Key-Pct is 20 with no key notice

  Scenario: The key headers are set before the first frame on a stream
    When a stream bearing "k1" is served
    Then X-RogerAI-Key-Limit / -Spend / -Pct (/ -Notice) are in the response headers before the first data: frame
    And the spend value is the window spend BEFORE this request (the request's own cost lands in the usage chunk)

  Scenario: The stream's final usage chunk carries the key state after settle
    When a stream bearing "k1" settles for $0.002 with $1.00 spent before it
    Then the usage chunk's "rogerai" object carries key_id "k1", key_limit 5, key_spend 1.002, key_pct 20, key_reset_at "2026-11-01T00:00:00Z"

  Scenario: A non-stream response's key headers reflect the state BEFORE this request, like the monthly headers
    Given "k1" has $1.00 spent
    When a non-stream relay bearing "k1" settles for $0.002
    Then X-RogerAI-Key-Spend is 1 (the header is set at the gate; /account/keys shows 1.002 afterwards)

  Scenario: Key headers never appear on a non-key request
    When a signed relay with no key bearer is served
    Then no X-RogerAI-Key-* header is set

  Scenario: The notice headers carry no secret and no account id
    Then every X-RogerAI-Key-* value is a number or the notice sentence; none contains "rog-key_", a hash, or a wallet id

  # --- settle and recount -----------------------------------------------------------
  Scenario: Settle captures the actual cost against the key window
    Given "k1" has $1.00 spent
    When a relay bearing "k1" holds $0.003 and settles for $0.002
    Then "k1" window spend is $1.002 and the $0.001 remainder of the reserve is released

  Scenario: Settle can never exceed the authorized maximum
    Given a station claims 10x the tokens the hold was sized for
    When the relay bearing "k1" settles
    Then the cost is capped at the hold (features/pricing/price_floor.feature) and the key window rises by at most the hold

  Scenario: A recount only lowers the billed amount, so key spend never rises above the reserve
    Given the broker re-counts the station's claim DOWN
    When the relay settles
    Then key spend rises by the recounted (lower) amount; there is no path by which it rises above the reserve

  Scenario: A void (no usable output) consumes no key limit
    Given the station returns 2xx with empty output and every fallback fails
    When the relay ends
    Then the wallet hold and the key reserve are both released and "k1" usage is unchanged

  Scenario: A failover to a pricier station within the covered ceiling settles at the served station's price
    Given "k1" has remaining $0.010 and the plan is n1 ($0.003) then n2 ($0.006), ceiling covered
    When n1 returns 429 and n2 serves for $0.005
    Then key spend rises by $0.005 and the rest of the $0.006 reserve is released

  Scenario: A models[] fallback settles the key window at the served pair
    Given a relay bearing "k1" lists models ["qwen3-32b","llama-3.3-70b"]
    When the first model has no eligible station and the second serves
    Then key spend rises by the served pair's cost, once

  # --- atomic reserve and concurrency ------------------------------------------------
  Scenario: Two requests racing the last cent - exactly one is served
    Given "k1" has remaining $0.003
    When two relays bearing "k1", each holding $0.003, arrive at the same instant
    Then one is served and one is 402 with limit_source "key_limit"
    And the window spend after settle is at most $5.00

  Scenario: N concurrent requests on one instance never overshoot
    Given "k1" has remaining $0.030
    When 100 relays bearing "k1", each holding $0.003, arrive concurrently
    Then exactly 10 pass the gate and 90 are 402 (floor(remaining/hold), the same law as wallet holds in features/money/idempotency_concurrency.feature)

  Scenario: N concurrent requests across two instances never overshoot
    Given two instances share the store and "k1" has remaining $0.030
    When 50 relays arrive on A and 50 on B at the same instant, each holding $0.003
    Then exactly 10 pass in total; the reserve is one atomic operation in the shared store, not a per-instance read-then-hold

  Scenario: The reserve and the wallet hold succeed or fail together
    Given the key reserve succeeds and the wallet hold then fails (insufficient balance)
    When the relay returns 402 credits
    Then the key reserve is released in the same exit path; "k1" has no dangling reservation

  Scenario: A broker killed mid-flight leaves a reserve the orphan sweep reclaims
    Given a relay bearing "k1" reserved $0.003 and the instance is SIGKILLed before settle
    When the deploy-orphan sweep runs (features/money/orphan_holds.feature)
    Then the key reserve is released with the orphaned wallet hold, by request id

  Scenario: The shared store being unreachable at the reserve step fails closed with a 503
    # CONTRACT §11: store unreachable at key auth OR at the reserve step is 503 fail-closed, the
    # same answer as the key-auth scenario in key_guardrails.feature, never a silent bypass.
    Given the shared store is down when a relay bearing "k1" reaches the hold gate
    Then it is 503 "key reserve failed - try again shortly"
    And no wallet hold is left behind (the reserve and the hold succeed or fail together)
    And the key limit is not bypassed

  Scenario: Window spend is a sum over settled rows, so two instances agree
    Given instance A settled $2.00 and instance B settled $1.50 for "k1" this window
    When either computes the gate
    Then remaining is $1.50 on both

  # --- window edges on the relay -------------------------------------------------------
  Scenario: A request that arrives at 23:59:59Z counts in the old window; one at 00:00:00Z in the new
    Given "k1" reset "daily" with $5.00 spent today
    When a relay arrives at 23:59:59Z it is 402
    And a relay arrives at 00:00:00Z it is served

  Scenario: A stream that starts before a reset and settles after it counts in the window it STARTED in
    Given "k1" reset "daily" with $4.998 spent, a stream starts 23:59:58Z and settles 00:00:03Z for $0.002
    Then the settle is attributed to the request's start time: yesterday's window closes at $5.00 and today's opens at $0.00

  Scenario: A mid-window reset PATCH does not re-open a refused window retroactively
    Given "k1" hit its monthly limit on the 10th
    When the account changes reset to "daily" on the 10th
    Then the next request is served (a new anchor), and the month's earlier spend stays in lifetime usage

  Scenario: reset "none" is lifetime on the relay
    Given key "kn" with limit 1 and reset "none", $1.00 settled in a prior month
    When a relay bearing "kn" arrives
    Then it is 402 with limit_source "key_limit"

  # --- allowed_models and allowed_nodes ---------------------------------------------------
  Scenario: A model outside allowed_models is 403 before any pick
    Given "k1" allows models ["qwen3-32b"]
    When a relay bearing "k1" asks for "llama-3.3-70b"
    Then it is 403 with code "key_model_denied" and message "this key does not allow model llama-3.3-70b"
    And no hold, no reserve, no dispatch, no usage change

  Scenario: The 403 for a denied model comes after moderation, like the grant allow-lists
    Given moderation mode is "sync" and the prompt is illegal
    When a relay bearing "k1" for a denied model arrives
    Then it is 451 (moderation first), not 403

  Scenario: A models[] list is intersected with allowed_models
    Given "k1" allows ["qwen3-32b"]
    When a relay bearing "k1" lists models ["llama-3.3-70b","qwen3-32b"]
    Then "llama-3.3-70b" is skipped silently and "qwen3-32b" is planned

  Scenario: A models[] list with every entry denied is 403
    Given "k1" allows ["qwen3-32b"]
    When a relay bearing "k1" lists models ["llama-3.3-70b","gpt-oss-20b"]
    Then it is 403 "key_model_denied" naming both models

  Scenario: Variant sugar is stripped before the allow-list check
    Given "k1" allows ["qwen3-32b"]
    When a relay bearing "k1" asks for "qwen3-32b:floor"
    Then the bare id matches and the request proceeds

  Scenario: A pinned node outside allowed_nodes is 403, not 503
    Given "k1" allows nodes ["n1"]
    When a relay bearing "k1" pins X-Roger-Node "n2" (or provider.order ["n2"] with allow_fallbacks false)
    Then it is 403 with code "key_node_denied" and message "this key does not allow node n2"

  Scenario: provider.only is intersected with allowed_nodes
    Given "k1" allows nodes ["n1","n2"]
    When a relay bearing "k1" sends provider.only ["n2","n3"]
    Then the candidate set is {n2}

  Scenario: provider.order entries outside allowed_nodes are skipped, not fatal, when fallbacks are allowed
    Given "k1" allows nodes ["n1"]
    When a relay bearing "k1" sends provider.order ["n2","n1"]
    Then "n2" is skipped and "n1" is attempt 1

  Scenario: An empty intersection of allowed_nodes and what is on air is 503 no_match
    Given "k1" allows nodes ["n-offline"]
    When a relay bearing "k1" arrives
    Then it is 503 with code "no_match" and no dispatch

  Scenario: allowed_nodes never admits a private-band node without the code
    Given "k1" allows ["n-private"] which is band-only
    When a relay bearing "k1" arrives with no freq
    Then it is 503 no_match; with the band code it is served on "n-private"

  Scenario: allowed_nodes applies on the edge/Tower bridge path
    Given "k1" allows nodes ["n1"] and a Tower "t1" also hosts the model
    When the request-seeded coin would pick the bridge
    Then the bridge is skipped (the Tower's relay id is not allowed) and "n1" serves

  Scenario: A curated station is subject to allowed_nodes like any node
    Given "k1" allows ["n1"] and a curated station "c1" serves the model cheaper
    When a relay bearing "k1" arrives
    Then "c1" is not a candidate

  Scenario: A denied model or node writes no lineage row and no key usage, but does update last_used
    When a relay bearing "k1" is 403 key_model_denied
    Then last_used is updated and nothing else is

  # --- grants versus keys -------------------------------------------------------------------
  Scenario: A request is either a key request or a grant request, never both
    When a relay carries Authorization "Bearer rog-grant_..."
    Then it is a grant request: grant caps and grant allow-lists apply; no key limit, no key headers
    When a relay carries Authorization "Bearer rog-key_..."
    Then it is a key request: key limit and key allow-lists apply; grant logic never runs

  Scenario: A grant issued BY the key's account does not use the key's limit
    Given "acct-a" (owner) minted grant "g1" for a friend
    When the friend's relay bearing "g1" is served at the grant price
    Then it settles against the grant's wallet rules and "k1" usage is unchanged

  Scenario: A key request served by the key account's own node is billed (not self-use)
    Given "acct-a" owns "n1"
    When a relay bearing "k1" is served by "n1"
    Then it is billed at the offer price and counts toward "k1"'s window

  # --- lineage, generation, telemetry ------------------------------------------------------
  Scenario: The console lineage shows the key id per relay
    When a relay bearing "k1" is served
    Then the /console lineage row carries key_id "k1"

  # added 2026-10-04 (founder ruling): an account that runs stations AND buys inference sees both
  # views on /console - the operator view (what its stations served, unchanged) and, separately,
  # its own consumer requests (with key_id when key-funded) and its own spend today.
  Scenario: An account that runs stations and buys inference sees both views on /console
    Given "acct-a" also runs station "n-own" for "own-model" that served one relay for another account
    When a relay bearing "k1" is served
    Then /console for "acct-a" has role "owner" and an operator event served by "n-own"
    And its consumer events list the relay made with "k1", carrying key_id "k1" and its cost
    And its consumer counters count that relay in spend_today
    And no consumer event is the relay "n-own" served for the other account

  # corrected 2026-10-02 (founder-approved): key_spend_after 1.002 needs $1.00 spent through the key first
  Scenario: /generation shows key_id, and the key state at settle in the consumer view only
    Given "k1" has $1.00 spent this window
    When a relay bearing "k1" is served
    Then the consumer view of GET /generation?id= carries key_id "k1", key_limit 5, key_spend_after 1.002 (fields absent for non-key requests)
    And the owner view (the station's payout owner) carries key_id only, never key_limit or key_spend_after

  Scenario: /usage can group by key
    When "acct-a" GETs /usage?by=key
    Then rows are keyed by key id with spend, requests, tokens; non-key spend is under key_id null

  Scenario: /admin/live counts key-limit refusals and denials
    When relays are refused for key_limit, key_model_denied, key_node_denied
    Then /admin/live carries key_limit_refusals, key_model_denials, key_node_denials counters (read-only, multi-instance summed)

  Scenario: A key hitting its limit is logged once per window, not per request
    When 500 relays bearing "k1" are 402 key_limit in one window
    Then one log line names key "k1" at limit; the others are counted, not logged

  Scenario: The near-limit key notice email is de-duplicated per key per window
    Given "acct-a" has an email on file and RESEND_API_KEY is set
    When "k1" crosses 80% and then 100% in one window
    Then at most one 80% email and one 100% email are sent for "k1" that window, naming the key by name and id, never the secret

  # state audit 2026-10-05: the de-duplication is shared, so a second instance does not mail again.
  Scenario: The key notice email is de-duplicated across instances
    Given "acct-a" has an email on file and RESEND_API_KEY is set
    And a second instance shares the store, with its own mailer
    When "k1" crosses 80% and then 100% in one window on A
    And a relay bearing "k1" on B is 402 key_limit in the same window
    Then at most one 80% email and one 100% email are sent for "k1" that window, naming the key by name and id, never the secret

  Scenario: Logs never print the secret
    Then no relay log line contains "rog-key_"

  # --- adversarial -----------------------------------------------------------------------------
  Scenario: A consumer cannot raise a key's limit through a header
    When a relay bearing "k1" sends X-RogerAI-Key-Limit "1000"
    Then the request header is ignored (response headers are never read) and the limit is still 5

  Scenario: A consumer cannot raise a key's limit through a routing key
    When a relay bearing "k1" sends roger.key_limit 1000
    Then it is 400 with code "unknown_routing_key" naming "roger.key_limit"

  Scenario: A key cannot be used to spend from a different wallet
    When a relay bearing "k1" sends X-Roger-User "u_gh_b" or a web-session cookie of "acct-b"
    Then the payer is "u_gh_a" (the bearer decides; features/relay/spend.feature)

  Scenario: A stream that starts under the limit and would exceed it at settle is clamped
    Given "k1" has remaining $0.003 and a stream holding $0.003 runs long
    When the station claims $0.010 of tokens
    Then the settle is clamped to $0.003 (the authorized maximum) and the window closes exactly at the limit

  Scenario: A key that expires mid-stream completes and settles
    Given "k1" expires in 2 seconds and a stream bearing "k1" is in flight
    When the expiry passes
    Then the stream completes, settles, and counts toward the window; the next request is 401 key_expired

  Scenario: A key deleted mid-stream completes and settles
    Given a stream bearing "k1" is in flight and "acct-a" deletes "k1"
    Then the stream completes, settles against "u_gh_a", and the next request is 401 key_revoked

  Scenario: Burst-abusing a stolen key is bounded by the key limit, the account monthly cap, and the rate limit together
    Given "k1" leaked with limit $5 and the account has monthly cap $50
    When the thief runs 10,000 requests
    Then settled spend from "k1" is at most $5.00 that window, the account's month stays under $50, and the per-identity limiter (120 rpm / burst 40) paces them

  Scenario: A station cannot inflate key spend by over-claiming
    Given a station over-reports tokens 10x
    When the relay settles
    Then the recount lowers the claim and the settle is capped at the hold; key spend rises by the recounted amount only

  Scenario: A denied model cannot be reached through models[] ordering tricks
    Given "k1" allows ["qwen3-32b"]
    When a relay lists models ["qwen3-32b","llama-3.3-70b"] and "qwen3-32b" has no eligible station
    Then "llama-3.3-70b" is still skipped (the allow-list is applied before the plan) and the result is 503 no_match

  Scenario: Key enforcement does not add a store round trip to non-key requests
    When a signed relay with no key bearer is served
    Then no key lookup, reserve, or window sum is performed (the hot paid path stays one cap read + one spend read)
