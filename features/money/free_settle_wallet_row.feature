# A $0 settle for a wallet that has never had a row: the receipt is recorded, a
# zero-balance wallet row is created, and NO seed is granted by the settle.
#
# PURPOSE: a relay that costs its payer nothing - signed self-use of your own station, or a
# FREE grant - skips the hold gate entirely (maxCost = 0), and so never runs the seed path
# that creates a wallet row. Its settle is the metering-only `Settle(payer, node, 0, 0, rec)`.
# On Postgres that settle used to UPDATE a wallet row that did not exist, fail with
# "no rows in result set", and the relay was served with no receipt, no X-RogerAI-* billing
# headers, no lineage row and no metering. The in-memory store never showed it. The same
# store call writes the $0 lineage receipt of a VOIDED attempt and the free audio path, where
# the error was swallowed and the receipt silently lost.
#
# FOUNDER RULING (2026-10-01): the store creates a ZERO-BALANCE wallet row, with NO seed,
# when a settle finds none - the same upsert BalanceOf performs, without the seed grant. No
# money is created; the wallet's one-time starter seed is still granted by its first PAID
# request (ensureSeeded), exactly once.
#
# GROUND TRUTH (origin/main 12207827):
#   cmd/rogerai-broker/grant.go:174-193  resolvePricing: free = a grant priced 0/0 (payer
#       "g_<id>") or signed self-use (payer = the caller's wallet). holdCostFor (cooling.go:118)
#       returns 0 for a free plan, so relay() skips the whole paid block (tunnel.go:2005) -
#       no monthly-cap check, no ensureSeeded, no hold.
#   cmd/rogerai-broker/grant.go:258-262  settleRequest, free branch: db.Settle(payer,node,0,0,rec).
#   cmd/rogerai-broker/tunnel.go:2200-2209  a settle error leaves the relay unbilled: the body
#       is still returned, with no billing headers ("relay settle FAILED ... releasing hold").
#   cmd/rogerai-broker/tunnel.go:2916  settleVoid: the $0 lineage receipt of a voided attempt,
#       error ignored. cmd/rogerai-broker/audio.go:521: the free audio path, error ignored.
#   internal/store/postgres.go:721-765  Settle: claimReceipt, then
#       `UPDATE rogerai.wallet SET balance=balance-$2 WHERE usr=$1 RETURNING balance`
#       (sql.ErrNoRows when the wallet has no row; the tx rolls back, receipt included).
#   internal/store/postgres.go:661-679  BalanceOf: the wallet upsert at balance 0, THEN
#       grantSeedTx (its own per-wallet claim in rogerai.seed_grants + the "seed:<wallet>"
#       ledger row). Row existence and the seed are independent.
#   internal/store/store.go:1304  Mem.Settle: `wallet[user] -= cost` creates the entry.
#   internal/store/store.go:1220  Mem.BalanceOf: seeded only when the wallet entry was
#       ABSENT, so a wallet first touched by a $0 settle was never seeded afterwards - the
#       mirror-image parity gap, closed by the same ruling.
#   NOT this path (unchanged, pinned below): a ZERO-PRICED PUBLIC station is not a free
#       plan. estimateMaxCost floors its hold at 1e-6, so it runs the hold gate and therefore
#       the seed path (features/money/seed_failure.feature, holds.feature).
#   Finalize (the paid capture) follows a hold; a hold exists only for a wallet that
#       ensureSeeded already gave a row. It is not changed.
#   A PAID direct settle (cost > 0) for a wallet with no row still errors and rolls back
#       (internal/store TestSettleAndFinalizeNoWalletRollBack): debiting a wallet that does
#       not exist is an upstream bug, so only a $0 settle creates the row. Every production
#       caller of Settle passes cost 0 (grant.go:262, tunnel.go:2916, audio.go:521).
#   In-memory parity: Mem.BalanceOf seeds a wallet only when its balance entry is ABSENT,
#       so Mem.Settle must not materialize that entry for a $0 settle (it did, and the
#       wallet then never received its seed). The Postgres row and its seed claim are
#       independent, so there the zero-balance row exists and the seed is still owed.
#
# Both stores: every scenario runs on the in-memory reference and, when
# ROGERAI_TEST_DATABASE_URL is set, on the real Postgres - with the same outcome.
#
# Enforced by: cmd/rogerai-broker/free_settle_wallet_row_bdd_test.go (the relay, real
# stations, real store) and internal/store/free_settle_parity_test.go (the store primitive:
# idempotency, concurrency, atomicity, seed independence).

Feature: A $0 settle records its receipt and creates a zero-balance wallet row, never a seed

  Background:
    Given a broker over the real store
    And the starter seed grant is 0.50 credits

  # --- self-use ----------------------------------------------------------------

  Scenario: Self-use by an owner whose wallet has no row is settled and receipted
    Given owner "op" runs station "s1" for "m" priced at in 1.00 out 1.00
    And "op"'s wallet has never been touched
    When "op" relays to their own station
    Then the response is 200 served by "s1"
    And the response carries X-RogerAI-Cost "0" and an X-RogerAI-Receipt
    And one receipt is stored for "op" with cost 0
    And "op"'s lineage lists that request
    And "op"'s balance is 0
    And "op"'s wallet has no seed ledger row
    And the seeded-wallet counter is unchanged
    And station "s1" has earned nothing

  Scenario: The wallet row created by a $0 settle has balance exactly 0 and no seed
    Given owner "op" runs station "s1" for "m" priced at in 1.00 out 1.00
    When "op" relays to their own station
    Then "op"'s wallet row exists with balance 0 and no seed remaining
    And "op"'s derived ledger balance equals the stored balance

  Scenario: A streamed self-use relay is settled the same way
    Given owner "op" runs station "s1" for "m" priced at in 1.00 out 1.00
    And "op"'s wallet has never been touched
    When "op" streams from their own station
    Then the stream completes with status 200
    And one receipt is stored for "op" with cost 0
    And "op"'s balance is 0
    And "op"'s wallet has no seed ledger row

  Scenario: Repeated self-use keeps one wallet row and writes one receipt per request
    Given owner "op" runs station "s1" for "m" priced at in 1.00 out 1.00
    When "op" relays to their own station 5 times
    Then every response is 200
    And 5 receipts are stored for "op", each with cost 0
    And "op"'s balance is 0
    And "op"'s wallet has no seed ledger row

  # --- a free grant ------------------------------------------------------------

  Scenario: A free grant's first relay is settled and receipted on the grant wallet
    Given owner "op" runs station "s1" for "m" priced at in 1.00 out 1.00
    And "op" issued a free grant "g1"
    And the grant wallet has never been touched
    When a caller relays with grant "g1"
    Then the response is 200 served by "s1"
    And the response carries X-RogerAI-Cost "0" and an X-RogerAI-Receipt
    And one receipt is stored for the grant wallet with cost 0
    And the receipt names grant "g1"
    And the grant wallet's balance is 0
    And the grant wallet has no seed ledger row
    And the seeded-wallet counter is unchanged
    And grant "g1" has counted the request's tokens
    And station "s1" has earned nothing

  Scenario: A free grant's wallet is never seeded by any number of free relays
    Given owner "op" runs station "s1" for "m" priced at in 1.00 out 1.00
    And "op" issued a free grant "g1"
    When a caller relays with grant "g1" 5 times
    Then every response is 200
    And 5 receipts are stored for the grant wallet, each with cost 0
    And the grant wallet's balance is 0
    And the grant wallet has no seed ledger row

  # --- a voided $0 attempt still leaves its lineage receipt --------------------

  Scenario: A voided self-use attempt writes its $0 lineage receipt
    Given owner "op" runs station "s1" for "m" priced at in 1.00 out 1.00
    And "op"'s wallet has never been touched
    And station "s1" answers its next request with an upstream 500
    When "op" relays to their own station
    Then the response is not 200
    And one voided receipt is stored for "op" with cost 0
    And "op"'s balance is 0
    And "op"'s wallet has no seed ledger row

  Scenario: A voided free-grant attempt writes its $0 lineage receipt
    Given owner "op" runs station "s1" for "m" priced at in 1.00 out 1.00
    And "op" issued a free grant "g1"
    And station "s1" answers its next request with an upstream 500
    When a caller relays with grant "g1"
    Then the response is not 200
    And one voided receipt is stored for the grant wallet with cost 0
    And the grant wallet's balance is 0

  # --- the seed is untouched: granted by the first PAID request, exactly once --

  Scenario: The first paid request after a $0 settle still receives the one-time seed
    Given owner "op" runs station "s1" for "m" priced at in 1.00 out 1.00
    And another owner runs station "p1" for "paid" priced at in 1.00 out 1.00
    And "op" has relayed to their own station
    And "op"'s wallet has no seed ledger row
    When "op" relays to "p1" for "paid"
    Then the response is 200 served by "p1"
    And "op"'s wallet has exactly one seed ledger row of 0.50
    And the seeded-wallet counter rose by exactly 1
    And "op"'s balance is 0.50 less the cost of that request

  Scenario: The seed is granted once - a second paid request does not seed again
    Given owner "op" runs station "s1" for "m" priced at in 1.00 out 1.00
    And another owner runs station "p1" for "paid" priced at in 1.00 out 1.00
    And "op" has relayed to their own station
    And "op" has relayed to "p1" for "paid"
    When "op" relays to "p1" for "paid"
    Then the response is 200 served by "p1"
    And "op"'s wallet has exactly one seed ledger row of 0.50
    And the seeded-wallet counter rose by exactly 1

  Scenario: Reading the balance after a $0 settle grants the seed, as a first read always has
    Given owner "op" runs station "s1" for "m" priced at in 1.00 out 1.00
    And "op" has relayed to their own station
    When "op"'s balance is read through the seeding read
    Then "op"'s balance is 0.50
    And "op"'s wallet has exactly one seed ledger row of 0.50

  Scenario: A $0 settle after the seed leaves the seed intact
    Given owner "op" runs station "s1" for "m" priced at in 1.00 out 1.00
    And "op"'s balance has been read through the seeding read
    When "op" relays to their own station
    Then the response is 200 served by "s1"
    And "op"'s balance is 0.50
    And "op"'s wallet has exactly one seed ledger row of 0.50

  # --- unchanged neighbours ----------------------------------------------------

  Scenario: A zero-priced public station still runs the hold gate and the seed path (unchanged)
    # Not a free plan: the 1e-6 floor hold needs a wallet row, so ensureSeeded runs first.
    Given another owner runs station "f1" for "free" priced at in 0 out 0
    And signed caller "kim" has never been seen
    When "kim" relays to "f1" for "free"
    Then the response is 200 served by "f1"
    And "kim"'s wallet has exactly one seed ledger row of 0.50
    And "kim"'s balance is 0.50
    And one receipt is stored for "kim" with cost 0

  Scenario: An unsigned request with no allowlisted Origin is refused before any settle (unchanged)
    Given another owner runs station "f1" for "free" priced at in 0 out 0
    When an unsigned request with no Origin relays for "free"
    Then the response is 401
    And no receipt was added for the anonymous identity
    And the anonymous identity's wallet is unchanged

  Scenario: The operator earns nothing on a $0 settle (unchanged)
    Given owner "op" runs station "s1" for "m" priced at in 1.00 out 1.00
    And "op" issued a free grant "g1"
    When a caller relays with grant "g1" 3 times
    And "op" relays to their own station 3 times
    Then station "s1" has earned nothing
    And no earning lot was minted for station "s1"
