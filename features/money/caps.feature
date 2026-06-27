# Spec-first behavior contract for spend / usage caps:
#   - the per-account MONTHLY SPEND CAP (a $ budget ceiling):
#     store.MonthlyCapOf / SetMonthlyCap / MonthSpendOf (internal/store/cap.go),
#     enforced at the credit-hold gate by broker.monthlyCapCheck
#     (cmd/rogerai-broker/monthlycap.go);
#   - the per-GRANT token caps + rate limits (daily/monthly tokens, RPM/burst):
#     store.Grant / GrantUsageOf / AddGrantUsage (internal/store/grant.go),
#     broker.grantCapCheck (cmd/rogerai-broker/grant.go) and
#     rateLimiter.allowAt (cmd/rogerai-broker/ratelimit.go).
#
# No step definitions or Go yet — approve this spec first.
#
# Two distinct semantics, intentionally different — call them out:
#   - The MONTHLY SPEND CAP is fail-closed on WORST CASE: a request is rejected if
#     spend + this request's worst-case (hold) cost would EXCEED the cap. A request
#     that exactly fits is allowed. So month-to-date spend never crosses the cap.
#   - The GRANT TOKEN CAPS are checked on ACCUMULATED usage with >= BEFORE dispatch:
#     a request is rejected once usage has REACHED the cap, but a single in-flight
#     request can overshoot (no worst-case pre-reservation). They are stop-when-
#     reached guardrails, not hard never-exceed budgets.

Feature: Spend and usage caps stop spend at the limit and reset on the right boundary

  # ===========================================================================
  # 1. MONTHLY SPEND CAP — resolution & defaults.
  # ===========================================================================

  Rule: The cap a wallet sees resolves from its explicit choice, else the env default

    Background:
      Given a fresh money store

    Scenario: An un-set wallet resolves to the env default cap
      Given the default monthly cap env is 50.00
      When the monthly cap for "alice" is read
      Then the cap is 50.00

    Scenario: An un-set wallet with no env default is unlimited
      Given the default monthly cap env is unset
      When the monthly cap for "alice" is read
      Then the cap is unlimited

    Scenario: An explicitly set cap overrides the env default
      Given the default monthly cap env is 50.00
      When "alice" sets a monthly cap of 200.00
      And the monthly cap for "alice" is read
      Then the cap is 200.00

    Scenario: An explicit unlimited (0) is stored and not re-defaulted from env
      Given the default monthly cap env is 50.00
      When "alice" sets a monthly cap of 0
      And the monthly cap for "alice" is read
      Then the cap is unlimited

    Scenario: A negative cap is stored as unlimited
      When "alice" sets a monthly cap of -10.00
      And the monthly cap for "alice" is read
      Then the cap is unlimited

    Scenario: A negative env default is treated as unlimited
      Given the default monthly cap env is -5.00
      When the monthly cap for "alice" is read
      Then the cap is unlimited

  # ===========================================================================
  # 2. MONTHLY SPEND CAP — month-to-date spend from the ledger (drift-proof).
  # ===========================================================================

  Rule: Month-to-date spend is summed from posted spend rows in the calendar UTC month

    Background:
      Given a fresh money store

    Scenario: Month spend sums only posted spend rows
      Given wallet "alice" has posted spend of 10.00, 20.00, and 5.00 this month
      When the month-to-date spend for "alice" is read now
      Then the month spend is 35.00

    Scenario: A reversed spend row does not count toward month spend
      Given wallet "alice" has posted spend of 10.00 this month
      And a spend row of 5.00 this month was reversed
      When the month-to-date spend for "alice" is read now
      Then the month spend is 10.00

    Scenario: Spend in a previous month does not count toward this month
      Given wallet "alice" spent 100.00 last month
      And wallet "alice" spent 10.00 this month
      When the month-to-date spend for "alice" is read now
      Then the month spend is 10.00

    Scenario: A spend row exactly at the previous month's end boundary is excluded
      Given wallet "alice" has a spend row of 10.00 stamped at 23:59:59 UTC on the last day of last month
      When the month-to-date spend for "alice" is read now
      Then the month spend is 0.00

    Scenario: A spend row exactly at this month's start boundary is included
      Given wallet "alice" has a spend row of 10.00 stamped at 00:00:00 UTC on the first day of this month
      When the month-to-date spend for "alice" is read now
      Then the month spend is 10.00

    Scenario: Top-ups, holds, and refunds do not count as spend
      Given wallet "alice" topped up 100.00 this month
      And wallet "alice" has an open hold of 20.00 this month
      And wallet "alice" was refunded 5.00 this month
      When the month-to-date spend for "alice" is read now
      Then the month spend is 0.00

  # ===========================================================================
  # 3. MONTHLY SPEND CAP — enforcement at the hold gate (worst-case, fail-closed).
  # ===========================================================================

  Rule: A paid request is rejected when its worst-case cost would exceed the cap

    Background:
      Given a fresh money store

    Scenario: An unlimited cap never blocks
      Given "alice" has an unlimited monthly cap
      And "alice" has spent 1000000.00 this month
      When "alice" makes a paid request with worst-case cost 5.00
      Then the request is allowed
      And no monthly-cap headers are set

    Scenario: A request well under the cap is allowed and emits the budget headers
      Given "alice" has a monthly cap of 100.00
      And "alice" has spent 10.00 this month
      When "alice" makes a paid request with worst-case cost 5.00
      Then the request is allowed
      And the monthly-cap header reports 10.00 of 100.00

    Scenario: A request that exactly fits the remaining budget is allowed (boundary)
      Given "alice" has a monthly cap of 100.00
      And "alice" has spent 95.00 this month
      When "alice" makes a paid request with worst-case cost 5.00
      Then the request is allowed

    Scenario: A request one cent over the remaining budget is rejected (off-by-one)
      Given "alice" has a monthly cap of 100.00
      And "alice" has spent 95.00 this month
      When "alice" makes a paid request with worst-case cost 5.01
      Then the request is rejected with 402
      And the rejection message says the monthly limit is reached
      And the at-limit headers are set

    Scenario: A request when already exactly at the cap is rejected
      Given "alice" has a monthly cap of 100.00
      And "alice" has spent 100.00 this month
      When "alice" makes a paid request with worst-case cost 0.01
      Then the request is rejected with 402

    Scenario: Spend exactly at 80% of the cap fires the near-cap notice but is allowed
      Given "alice" has a monthly cap of 100.00
      And "alice" has spent 80.00 this month
      When "alice" makes a paid request with worst-case cost 1.00
      Then the request is allowed
      And the near-cap notice header is set
      And the at-limit notice header is not set

    Scenario: Spend just under 80% does not fire the near-cap notice
      Given "alice" has a monthly cap of 100.00
      And "alice" has spent 79.99 this month
      When "alice" makes a paid request with worst-case cost 0.01
      Then the request is allowed
      And the near-cap notice header is not set

    Scenario Outline: Cap boundary matrix (worst-case enforcement, > cap rejects)
      Given "alice" has a monthly cap of <cap>
      And "alice" has spent <spent> this month
      When "alice" makes a paid request with worst-case cost <cost>
      Then the request is <verdict>

      Examples:
        | cap    | spent  | cost  | verdict  |
        | 100.00 | 0.00   | 100.00| allowed  |
        | 100.00 | 0.00   | 100.01| rejected |
        | 100.00 | 50.00  | 50.00 | allowed  |
        | 100.00 | 50.00  | 50.01 | rejected |
        | 100.00 | 99.99  | 0.01  | allowed  |
        | 100.00 | 99.99  | 0.02  | rejected |
        | 100.00 | 100.00 | 0.00  | allowed  |
        | 100.00 | 100.01 | 0.00  | rejected |

    Scenario: Free / self ($0) spend never reaches the cap gate
      Given "alice" has a monthly cap of 100.00
      And "alice" has spent 100.00 this month
      When "alice" makes a free request with worst-case cost 0.00
      Then the request is allowed
      And the cap gate is not consulted

  # ===========================================================================
  # 4. MONTHLY SPEND CAP — reset at the calendar month boundary.
  # ===========================================================================

  Rule: The cap window is the calendar UTC month; spend resets when the month rolls

    Background:
      Given a fresh money store

    Scenario: A wallet hard-stopped at the cap is allowed again next month
      Given "alice" has a monthly cap of 100.00
      And "alice" has spent 100.00 this month
      When the clock advances into next month
      And "alice" makes a paid request with worst-case cost 5.00
      Then the request is allowed
      And the month-to-date spend reads 0.00 at the start of next month

    Scenario: The month window matches the grant-usage month definition (shared UTC calendar)
      Given the month spend window for a given instant
      Then it starts at 00:00:00 UTC on the first of that month
      And it ends at 00:00:00 UTC on the first of the next month

  # ===========================================================================
  # 5. ACCOUNT-WIDE SPEND — the cap is keyed per GitHub-linked wallet.
  # ===========================================================================

  Rule: The cap is global across every paid consume path for that wallet

    Background:
      Given a fresh money store

    Scenario: Spend from different paid relay paths accumulates into one monthly total
      Given "alice" has a monthly cap of 100.00
      And "alice" spent 40.00 via the public relay this month
      And "alice" spent 40.00 via a grant-billed path this month
      When "alice" makes a paid request with worst-case cost 25.00
      Then the request is rejected with 402
      And the month-to-date spend reads 80.00

    Scenario: One wallet hitting its cap does not affect another wallet
      Given "alice" has a monthly cap of 100.00 and has spent 100.00 this month
      And "bob" has a monthly cap of 100.00 and has spent 0.00 this month
      When "bob" makes a paid request with worst-case cost 50.00
      Then the request is allowed

  # ===========================================================================
  # 6. GRANT TOKEN CAPS — daily + monthly token ceilings (stop-when-reached).
  # ===========================================================================

  Rule: A grant is denied at 429 once its accumulated usage reaches a configured cap

    Background:
      Given a fresh money store
      And a grant "g1" issued by owner "op1"

    Scenario: A grant with no caps is never blocked
      Given grant "g1" has no daily or monthly token cap
      And grant "g1" has used 1000000 tokens today
      When a request on grant "g1" is checked
      Then the request is allowed

    Scenario: A grant under its daily cap is allowed
      Given grant "g1" has a daily token cap of 1000
      And grant "g1" has used 999 tokens today
      When a request on grant "g1" is checked
      Then the request is allowed

    Scenario: A grant exactly at its daily cap is denied (>= boundary)
      Given grant "g1" has a daily token cap of 1000
      And grant "g1" has used 1000 tokens today
      When a request on grant "g1" is checked
      Then the request is denied with 429 "grant daily token cap reached"

    Scenario: A grant over its daily cap is denied
      Given grant "g1" has a daily token cap of 1000
      And grant "g1" has used 1500 tokens today
      When a request on grant "g1" is checked
      Then the request is denied with 429 "grant daily token cap reached"

    Scenario: The daily cap resets at the UTC day boundary
      Given grant "g1" has a daily token cap of 1000
      And grant "g1" used 1000 tokens yesterday
      And grant "g1" has used 0 tokens today
      When a request on grant "g1" is checked
      Then the request is allowed

    Scenario: A grant at its monthly cap is denied even if today is fresh
      Given grant "g1" has a monthly token cap of 30000
      And grant "g1" has used 30000 tokens this month
      And grant "g1" has used 0 tokens today
      When a request on grant "g1" is checked
      Then the request is denied with 429 "grant monthly token cap reached"

    Scenario: The monthly cap resets at the UTC month boundary
      Given grant "g1" has a monthly token cap of 30000
      And grant "g1" used 30000 tokens last month
      When the clock advances into the next UTC month
      And a request on grant "g1" is checked
      Then the request is allowed

    Scenario: Daily cap is checked before monthly when both would trip
      Given grant "g1" has a daily token cap of 1000 and a monthly token cap of 30000
      And grant "g1" has used 1000 tokens today and 1000 tokens this month
      When a request on grant "g1" is checked
      Then the request is denied with 429 "grant daily token cap reached"

    Scenario: Caps are a soft pre-dispatch gate — a single request may overshoot once admitted
      Given grant "g1" has a daily token cap of 1000
      And grant "g1" has used 999 tokens today
      When a request on grant "g1" is admitted and then serves 5000 tokens
      Then usage becomes 5999 tokens today
      And the NEXT request on grant "g1" is denied with 429

    Scenario: A usage-read error fails OPEN (caps are a guardrail, not auth)
      Given grant "g1" has a daily token cap of 1000
      And the grant usage read errors
      When a request on grant "g1" is checked
      Then the request is allowed

    Scenario: AddGrantUsage increments both the day and month rollups
      Given grant "g1" has used 0 tokens
      When grant "g1" serves 250 tokens
      Then grant "g1" day usage is 250
      And grant "g1" month usage is 250

    Scenario Outline: Daily cap decision boundary
      Given grant "g1" has a daily token cap of <cap>
      And grant "g1" has used <used> tokens today
      When a request on grant "g1" is checked
      Then the request is <verdict>

      Examples:
        | cap  | used | verdict |
        | 1000 | 0    | allowed |
        | 1000 | 999  | allowed |
        | 1000 | 1000 | denied  |
        | 1000 | 1001 | denied  |
        | 0    | 9999 | allowed |

  # ===========================================================================
  # 7. GRANT RATE LIMIT — RPM / burst token bucket.
  # ===========================================================================

  Rule: A grant carries its own RPM/burst overriding the limiter default

    Background:
      Given a fresh money store
      And a grant "g1" issued by owner "op1"

    Scenario: A fresh bucket admits up to the burst depth immediately
      Given grant "g1" has rpm 60 and burst 5
      When 5 requests on grant "g1" arrive in the same instant
      Then all 5 are allowed
      And the 6th in that instant is denied with 429 and a Retry-After hint

    Scenario: Tokens refill at the sustained rpm rate
      Given grant "g1" has rpm 60 and burst 1
      And the bucket for grant "g1" is empty
      When 1 second passes
      Then 1 more request on grant "g1" is allowed

    Scenario: A zero RPM override falls back to the limiter default
      Given the grant limiter default rpm is 120 and burst 40
      And grant "g1" has rpm 0 and burst 0
      When a request on grant "g1" is checked
      Then the configured default rate applies

    Scenario: A limiter with rpm <= 0 disables rate limiting (always allow)
      Given the grant limiter default rpm is 0
      And grant "g1" has rpm 0
      When many requests on grant "g1" arrive at once
      Then all are allowed

    Scenario: A burst override of 0 with a positive rpm defaults the bucket depth to the rpm
      Given grant "g1" has rpm 30 and burst 0
      When a fresh bucket for grant "g1" is sized
      Then the bucket depth is 30

    Scenario: Each grant has its own bucket keyed by grant id
      Given grant "g1" has rpm 60 and burst 1 and its bucket is empty
      And grant "g2" has rpm 60 and burst 1 and a full bucket
      When a request on grant "g2" arrives
      Then the request on grant "g2" is allowed
      And grant "g1" remains rate-limited independently

  # ===========================================================================
  # 8. ANON / PER-IDENTITY RATE LIMITS (the non-grant relay buckets).
  # ===========================================================================

  Rule: Unauthenticated callers get a tighter per-IP bucket than signed wallets

    Background:
      Given a fresh money store

    Scenario: The anon limiter is tighter than the per-identity limiter by default
      Then the anon default rpm is 30 and burst 15
      And the per-identity default rpm is 120 and burst 40

    Scenario: An anonymous caller is limited per validated client IP
      Given the anon limiter has rpm 30 and burst 1 and an empty bucket for IP "203.0.113.7"
      When a second anon request from IP "203.0.113.7" arrives in the same instant
      Then it is denied with 429 and a Retry-After hint

    Scenario: A Retry-After hint is at least 1 second
      Given a limiter with rpm 60 and an empty bucket for key "k"
      When a request for key "k" is denied
      Then the Retry-After hint is at least 1
