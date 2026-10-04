# MONTHLY CAP UNDER CONCURRENCY: an account's monthly spend limit is never overshot, no matter how
# many requests race for it, on how many broker instances, or after a restart.
#
# PURPOSE
#   An account may set a monthly spend cap (POST /account/limit). The relay must never authorize
#   spend that could take the month past the cap. Today the check reads CAPTURED spend (settled
#   requests) before the hold and ignores holds that are still open, so N concurrent requests each
#   see the same "spend so far" and each pass; the fast-path counter that accelerates the read can
#   also be overwritten by a reconcile while a peer is incrementing it. This feature pins the
#   founder's ruling (2026-10-04): the cap is enforced where the money is reserved, inside the hold
#   transaction, counting open holds, under the wallet row lock; the shared counter is only a fast
#   pre-check, never the authority.
#
# GROUND TRUTH (origin/main 12207827)
#   - cmd/rogerai-broker/monthlycap.go:56 monthlyCapCheck: cap from MonthlyCapOf, spend from
#     monthSpend, rejects when spend + maxCost > cap. Called once per request at tunnel.go:2011
#     (and audio.go:404) BEFORE HoldFor; the ceiling attempt uses monthlyCapFits (monthlycap.go:92)
#     with the same spend read. Neither counts open holds.
#   - cmd/rogerai-broker/cacheaccel.go:148 monthSpend: reads the shared counter
#     "cap:spend:<wallet>:<yyyymm>"; on a miss it recomputes the ledger SUM and counterSet()s it,
#     which can overwrite a concurrent recordMonthSpend INCR (cacheaccel.go:172) from a peer.
#   - internal/store/postgres.go:1194 HoldFor (and store.go:1385 Mem.HoldFor): reserves the
#     amount against the wallet balance only; no cap is passed in.
#   - cmd/rogerai-broker/edgebridge.go:237: the Tower bridge places its hold with HoldFor and never
#     calls monthlyCapCheck, so a bridged request is not under the monthly cap at all.
#   - Approved and kept: features/relay/spend.feature and the X-RogerAI-Monthly-* headers
#     (setCapHeaders, monthlycap.go:113); the 402 message text "monthly spend limit reached".
#
# RULES PINNED HERE
#   C1 The hold for a request is placed only if month-to-date captured spend + the sum of the
#      wallet's open holds + this hold's amount <= the cap. The check and the reservation happen in
#      one transaction under the wallet row lock (Postgres) / the store mutex (in-memory).
#   C2 A refusal at that point is the same 402 as today ("monthly spend limit reached ...") with the
#      at-limit headers, X-RogerAI-Cost: 0, and no hold left behind.
#   C3 The plan's pricier ceiling is attempted only if IT fits under C1; otherwise the first pick's
#      hold is tried under C1 (today's trimPlan behavior, now race-free).
#   C4 The shared counter is a pre-check and a cache: it may refuse early only when captured spend
#      alone already exceeds the cap; it never authorizes. A reconcile never overwrites a peer's
#      increment (increment-only, or set-if-absent then increment).
#   C5 The cap covers every paid path that places a hold: chat (stream and non-stream), voice,
#      and the Tower bridge.
#   C6 A free or self-use ($0) request is never refused by the cap (unchanged).
#   C7 Outage: if the durable store cannot be reached, the request is refused (the hold cannot be
#      placed); if only the shared counter is unreachable, the transactional check still decides.
#
# Enforced by: cmd/rogerai-broker/monthly_cap_concurrency_bdd_test.go

Feature: A monthly spend cap is never overshot, however many requests race for it

  Background:
    Given a funded account "acct" with a balance of $20.00
    And "acct" has a monthly cap of $1.00
    And a station "s1" is on air for "m" whose worst-case cost per request is $0.10

  # --- C1: open holds count ---------------------------------------------------------------

  Scenario: Ten concurrent requests that each fit alone admit only as many as the cap allows
    Given "acct" has spent $0.50 this month
    When 10 requests from "acct" for "m" start at the same moment and hold while in flight
    Then exactly 5 of them are dispatched
    And the other 5 are refused 402 "monthly spend limit reached"
    And the month-to-date spend after every request settles is at most $1.00

  Scenario: Concurrent requests across two instances never overshoot the cap together
    Given a second broker instance over the same durable and shared stores
    And "acct" has spent $0.50 this month
    When 6 requests from "acct" start on instance A and 6 on instance B at the same moment
    Then exactly 5 of the 12 are dispatched
    And the month-to-date spend after every request settles is at most $1.00

  Scenario: The race is closed even when both instances read the same spend first
    Given a second broker instance over the same durable and shared stores
    And "acct" has spent $0.90 this month
    And instance B's whole request runs between instance A's cap read and A's hold
    When one request from "acct" runs on each instance
    Then exactly 1 of the 2 is dispatched
    And the other is refused 402 "monthly spend limit reached"

  Scenario: An open hold counts against the cap until it is released
    Given "acct" has spent $0.85 this month
    And one request from "acct" is in flight holding $0.10
    When another request from "acct" for "m" arrives
    Then it is refused 402 "monthly spend limit reached"

  Scenario: A released hold frees its share of the cap
    Given "acct" has spent $0.85 this month
    And one request from "acct" held $0.10 and then failed before any work, releasing the hold
    When another request from "acct" for "m" arrives
    Then it is dispatched

  Scenario: A settled request counts its captured cost, not its hold
    Given "acct" has spent $0.85 this month
    And one request from "acct" held $0.10 and settled at $0.02
    When another request from "acct" for "m" arrives
    Then it is dispatched

  Scenario: A request that exactly fits the cap is allowed (unchanged boundary)
    Given "acct" has spent $0.90 this month
    When one request from "acct" for "m" arrives
    Then it is dispatched

  Scenario: A request that would exceed the cap by any amount is refused (unchanged boundary)
    Given "acct" has spent $0.9000001 this month
    When one request from "acct" for "m" arrives
    Then it is refused 402 "monthly spend limit reached"

  # --- C2: the refusal ------------------------------------------------------------------------

  Scenario: A refusal at the hold leaves no hold behind and carries the at-limit headers
    Given "acct" has spent $0.95 this month
    When one request from "acct" for "m" arrives
    Then it is refused 402 "monthly spend limit reached"
    And the response carries X-RogerAI-Monthly-Cap "1" and X-RogerAI-Monthly-Notice
    And the response carries X-RogerAI-Cost "0"
    And "acct" has no open hold
    And "acct"'s balance is still $20.00

  # --- C3: the pricier ceiling -------------------------------------------------------------

  Scenario: The plan's pricier ceiling is not held when it would exceed the cap
    Given a station "s2" is on air for "m" whose worst-case cost per request is $0.30
    And "acct" has spent $0.80 this month
    When one request from "acct" for "m" arrives and its plan's first pick is "s1"
    Then the hold placed is $0.10, not $0.30
    And the request is dispatched to "s1"

  Scenario: Two racing requests cannot both take the pricier ceiling past the cap
    Given a station "s2" is on air for "m" whose worst-case cost per request is $0.30
    And a second broker instance over the same durable and shared stores
    And "acct" has spent $0.60 this month
    When one request from "acct" runs on each instance at the same moment
    Then the sum of the holds placed is at most $0.40

  # --- C4: the shared counter is never the authority ---------------------------------------

  Scenario: A stale shared counter below the truth cannot authorize an overshoot
    Given "acct" has spent $0.95 this month in the ledger
    And the shared spend counter for "acct" this month says $0.00
    When one request from "acct" for "m" arrives
    Then it is refused 402 "monthly spend limit reached"

  Scenario: A reconcile never overwrites a peer's increment
    Given a second broker instance over the same durable and shared stores
    And the shared spend counter for "acct" this month is missing
    When instance A reconciles the counter while instance B records a $0.10 settle
    Then the shared counter equals the ledger's month-to-date spend

  Scenario: An unreachable shared counter still enforces the cap from the durable store
    Given "acct" has spent $0.95 this month
    And the shared store is unreachable
    When one request from "acct" for "m" arrives
    Then it is refused 402 "monthly spend limit reached"

  # --- C5: every paid path ----------------------------------------------------------------

  Scenario: A streaming request is under the same cap
    Given "acct" has spent $0.95 this month
    When one streaming request from "acct" for "m" arrives
    Then it is refused 402 "monthly spend limit reached"
    And no stream frame was written

  Scenario: A paid voice request is under the same cap
    Given a voice station is on air whose cost per request is $0.10
    And "acct" has spent $0.95 this month
    When one voice request from "acct" arrives
    Then it is refused 402 "monthly spend limit reached"

  Scenario: A request bridged through a Tower is under the same cap
    Given no direct station serves "tm" and an approved Tower serves "tm" at a worst-case cost of $0.10
    And "acct" has spent $0.95 this month
    When one request from "acct" for "tm" arrives
    Then it is refused 402 "monthly spend limit reached"
    And no attempt was submitted to the Tower

  Scenario: Concurrent bridged requests never overshoot the cap
    Given no direct station serves "tm" and an approved Tower serves "tm" at a worst-case cost of $0.10
    And "acct" has spent $0.70 this month
    When 6 requests from "acct" for "tm" start at the same moment
    Then at most 3 are submitted to the Tower

  # --- C6: free traffic -------------------------------------------------------------------

  Scenario: A self-use request is never refused by the cap (unchanged)
    Given "acct" owns a station "own" on air for "m"
    And "acct" has spent $1.00 this month
    When one request from "acct" for "m" is served by "own"
    Then it is dispatched at $0

  # corrected 2026-10-04 (founder ruling): free stations bypass the monthly cap
  # Not "unchanged": on origin/main a 0/0 public station's 1e-6 floor hold (approved
  # features/money/holds.feature) counts as a paid hold, so an account sitting exactly at its cap
  # is refused 402 for a free request. The fix exempts the floor hold from the cap check.
  Scenario: A free station request is never refused by the cap, even exactly at the cap
    Given a free station "f1" is on air for "fm"
    And "acct" has spent $1.00 this month
    When one request from "acct" for "fm" arrives
    Then it is dispatched at $0

  # --- restart ----------------------------------------------------------------------------

  Scenario: A restarted instance enforces the cap including holds placed before the restart
    Given "acct" has spent $0.85 this month
    And one request from "acct" is in flight holding $0.10 on instance A
    When instance A restarts as a fresh broker over the same stores
    And another request from "acct" for "m" arrives at the fresh broker
    Then it is refused 402 "monthly spend limit reached"

  # --- C7: outage ---------------------------------------------------------------------------

  Scenario: An unreachable durable store refuses the request rather than skipping the cap
    Given the durable store is unreachable
    When one request from "acct" for "m" arrives
    Then it is refused with a 5xx and no stream frame
    And nothing was dispatched
