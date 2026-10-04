# DURABLE PRICE LOCK: the 24-hour price promise a consumer gets on first contact with a station is
# kept by every broker instance, survives restarts and cache flushes, and is decided once.
#
# PURPOSE
#   The first time a consumer is served a model by a station, the broker quotes that station's
#   current price and promises it for 24 hours: within the window the consumer is billed
#   min(quoted, current), so an operator cannot raise the price on someone mid-engagement but a
#   price cut still applies. Today the promise lives in per-instance memory, with a best-effort
#   copy in the evictable cache keyspace of the shared store (multi-instance only): two instances
#   serving the consumer's first requests at the same time can each mint a quote (last write wins),
#   a cache eviction or flush silently drops every promise, a restart forgets them in single-
#   instance mode, and the shared round trip runs while the broker's main lock is held. This feature
#   pins the founder's ruling (2026-10-04): the quote is durable (Postgres), first writer wins, and
#   the store round trip happens outside the broker lock.
#
#   DESIGN CHOICE: Postgres, not the shared store. Nothing in the repository or the deployment notes
#   guarantees the shared store runs with a no-eviction policy, and the quote is a money promise
#   with a 24-hour life, so it belongs in the durable store. The in-memory store used in tests
#   models the same first-writer-wins table.
#
# GROUND TRUTH (origin/main 12207827)
#   - cmd/rogerai-broker/main.go:1055 lockedPrice: takes b.mu for the whole call; reads the shared
#     quote (multi-instance only) via sharedQuoteGet (cacheGet on "quote:<user>|<node>|<model>");
#     falls back to the in-memory b.quotes; mints a new quote when absent or expired and writes it
#     through with sharedQuoteSet (a plain cache set with TTL = the remaining window, main.go:1110).
#   - main.go:212 lockWin (24 h by default, main.go:719); called at settle (tunnel.go:2149 non-stream,
#     :2791 stream) only when the active price is not a published time-of-use window (scheduled
#     prices and free windows are billed as-is and never locked).
#   - Approved and kept: features/routing/upstream_failover.feature:154 "failover respects price
#     caps and the price lock"; the X-RogerAI-Price header "in=;out=;locked_until=".
#
# RULES PINNED HERE
#   L1 A quote is one durable row keyed (payer, station, model): in price, out price, locked-until.
#   L2 Minting is insert-if-absent; on a conflict the existing row wins and is read back, so racing
#      instances bill under ONE quote. An expired row is replaced by the next mint (atomically).
#   L3 Billing within the window is min(quoted, current) on both axes (unchanged).
#   L4 Quotes survive a broker restart and a shared-store flush or eviction.
#   L5 The durable round trip happens outside the broker's main lock.
#   L6 Scheduled and free-window prices are never locked (unchanged).
#   L7 Outage: if the quote cannot be read or written at settle, the settle fails safe toward the
#      consumer (today's settle-failure path: the hold is released, the consumer is not charged),
#      never billing an unlocked current price that could be a hike.
#
# Enforced by: cmd/rogerai-broker/price_lock_durable_bdd_test.go

Feature: A consumer's 24-hour price lock is durable, decided once, and kept by every instance

  Background:
    Given two broker instances "A" and "B" over the same durable and shared stores
    And a station "s1" is on air for "m" at in $0.10 out $0.30
    And a funded consumer "c"

  # --- L1/L2: decided once ------------------------------------------------------------------

  Scenario: The first served request mints one quote
    When "c" is served "m" by "s1" on "A"
    Then exactly one price quote exists for ("c", "s1", "m") at in $0.10 out $0.30
    And it is locked until 24 hours from now

  Scenario: Two instances serving the first requests at the same moment bill under one quote
    Given the station's price changes from out $0.30 to out $0.20 between A's and B's settle
    When "c" is served "m" by "s1" on "A" and on "B" at the same moment
    Then exactly one price quote exists for ("c", "s1", "m")
    And both requests were billed under that one quote

  Scenario: A quote minted on A is honored on B
    Given "c" was served "m" by "s1" on "A"
    And the operator raises "s1" to out $0.90
    When "c" is served "m" by "s1" on "B"
    Then "c" is billed out $0.30

  Scenario: A racing mint never replaces an existing live quote
    Given "c" was served "m" by "s1" on "A" at out $0.30
    And the operator raises "s1" to out $0.90
    When "B" tries to mint a quote for ("c", "s1", "m")
    Then the quote for ("c", "s1", "m") is still out $0.30

  Scenario: An expired quote is replaced by the next served request
    Given "c" was served "m" by "s1" 25 hours ago at out $0.30
    And "s1" is now priced out $0.50
    When "c" is served "m" by "s1" on "B"
    Then "c" is billed out $0.50
    And the quote for ("c", "s1", "m") is out $0.50 locked until 24 hours from now

  Scenario: Quotes are per payer, per station, per model
    Given "c" was served "m" by "s1" on "A" at out $0.30
    And the operator raises "s1" to out $0.90
    When consumer "d" is served "m" by "s1" on "A"
    Then "d" is billed out $0.90

  # --- L3: the billing rule (unchanged) ----------------------------------------------------------

  Scenario: A price cut within the window applies
    Given "c" was served "m" by "s1" on "A" at out $0.30
    And the operator cuts "s1" to out $0.10
    When "c" is served "m" by "s1" on "B"
    Then "c" is billed out $0.10

  Scenario: The price header reports the lock
    When "c" is served "m" by "s1" on "A"
    Then X-RogerAI-Price carries "locked_until=" 24 hours from now

  # --- L4: durable across restart and cache loss ----------------------------------------------

  Scenario: A restarted instance honors a quote minted before the restart
    Given "c" was served "m" by "s1" on "A" at out $0.30
    And the operator raises "s1" to out $0.90
    When "A" restarts as a fresh broker over the same stores
    And "c" is served "m" by "s1" on the fresh "A"
    Then "c" is billed out $0.30

  Scenario: A single-instance broker honors a quote across its own restart
    Given a single broker over a durable store with no shared store configured
    And "c" was served "m" by "s1" on it at out $0.30
    And the operator raises "s1" to out $0.90
    When the broker restarts over the same durable store
    And "c" is served "m" by "s1"
    Then "c" is billed out $0.30

  Scenario: Flushing the shared store does not drop any price promise
    Given "c" was served "m" by "s1" on "A" at out $0.30
    And the operator raises "s1" to out $0.90
    When every key in the shared store is deleted
    And "c" is served "m" by "s1" on "B"
    Then "c" is billed out $0.30

  # --- L5: outside the broker lock -------------------------------------------------------------

  Scenario: A slow price-lock read does not stall routing for other requests
    Given the durable store answers price-lock reads after 500 ms
    When "c" is being settled on "A"
    Then another request on "A" can take the broker's routing lock within 50 ms

  # --- L6: unlocked prices (unchanged) -----------------------------------------------------------

  Scenario: A request served inside a published free window mints no quote
    Given "s1" publishes a free window that is active now
    When "c" is served "m" by "s1" on "A"
    Then no price quote exists for ("c", "s1", "m")

  # --- L7: outage ------------------------------------------------------------------------------

  Scenario: An unreadable quote at settle never bills an unlocked price
    Given "c" was served "m" by "s1" on "A" at out $0.30
    And the operator raises "s1" to out $0.90
    And price-lock reads fail on "B"
    When "c" is served "m" by "s1" on "B"
    Then "c" is not charged above out $0.30
    And "c"'s hold is released
