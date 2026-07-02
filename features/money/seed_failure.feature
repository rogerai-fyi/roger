# Starter-seed failure honesty - a store failure is never billed to the user as
# "insufficient balance".
#
# THE SMELL (2026-07-02 backfill audit): cmd/rogerai-broker/cacheaccel.go ensureSeeded
# swallowed the seed-write error (`_, _ = b.db.BalanceOf(...)`, both the direct path and
# the W4 flag path). When Postgres blips during the starter-seed transaction, the relay
# proceeded with an UNSEEDED wallet, HoldFor found no wallet row (held=false, err=nil,
# indistinguishable from a genuinely empty wallet), and the user got 402 "insufficient
# balance - add funds" for a pure server-side failure. Worse, in W4 (shared seeded-flag)
# mode the SETNX flag was set BEFORE the seed tx ran, so ONE transient DB failure poisoned
# the wallet: every retry, on EVERY instance, skipped the seed and false-402'd until the
# 7-day flag TTL expired.
#
# GROUND TRUTH (the code these scenarios pin):
#   internal/store/postgres.go BalanceOf: ONE transaction - wallet upsert (balance 0),
#     grantSeedTx (seed_grants per-wallet claim + seed_counter cap bump + wallet credit +
#     the idem-keyed "seed:<wallet>" ledger row), then the balance read. ANY statement
#     failing rolls the WHOLE tx back: no wallet row, no claim, no counter bump, no ledger
#     row. The seed IS a ledger row, so DeriveBalance == balance by design.
#   internal/store/postgres.go HoldFor: UPDATE ... WHERE usr=$1 AND balance>=$2. A missing
#     wallet row yields (held=false, err=nil) - the "insufficient" verdict, NOT an error.
#   cmd/rogerai-broker/tunnel.go + audio.go hold gates: a store error (herr != nil) is
#     500 "wallet error" - the money-path store-failure convention; !held is the 402.
#     estimateMaxCost floors at 1e-6 "so a hold is always placed": even a ZERO-priced
#     public offer runs the hold gate (and therefore the seed path). Only pricing.free
#     (a free grant, or signed self-use of your own node) truly skips it (maxCost = 0).
#     A cap-blocked seed keeps its per-wallet seed_grants CLAIM row (the atomic
#     claim+bump mechanics) with NO credit, NO ledger row, NO counter bump.
#   cmd/rogerai-broker/cacheaccel.go ensureSeeded (W4): shared flag ON -> SETNX
#     "seeded:<wallet>"; an already-set flag skips the write tx; a first-set or ANY shared
#     store error runs the authoritative BalanceOf (the Postgres ON-CONFLICT seed_grants
#     guard is the real authority - a lost flag can never double-seed).
#   internal/client/failover.go retryable(): every status >= 500 fails over and retries;
#     a 4xx (a false 402) is surfaced to the caller as their own fault and is NOT retried.
#     So the honest signal for a transient seed-store failure is the SAME 500 "wallet
#     error" the adjacent HoldFor store failure already returns: client-retryable, and
#     never a billing verdict.
#
# APPROVED BEHAVIOR (founder, 2026-07-02): a transient seed-write failure must surface as
# a RETRYABLE server error - never a false 402 "insufficient balance". Seed semantics, the
# seed limit counter, and the money math are UNCHANGED: this is error propagation only.
#
# Executable against the REAL Postgres store (ROGERAI_TEST_DATABASE_URL - the repo's
# testcontainer/cover-gate pattern; the suite skips without it, CI always provisions it)
# in a PRIVATE database, plus miniredis (real Redis protocol) for the W4 shared flag.
# The store failure is a REAL Postgres error (the seed relation taken offline), never a
# driver mock. Store-level seed accounting (at-most-once, cap, concurrency) stays pinned
# by funding.feature; grantSeedTx's in-tx error propagation by postgres_txerr_test.go;
# the sponsor-side 402 message for grant keys by the grants domain.
Feature: Starter-seed failure honesty on the paid relay paths

  Background:
    Given a broker backed by the real Postgres money store
    And the starter seed grant is 1.00 credits
    And a paid public chat station "m" is on air
    And "alice" is logged in with a signed keypair

  # --- the seed path working as designed (pin the current truth) --------------

  Scenario: A first priced request seeds the wallet exactly once and serves
    When alice sends a priced chat request
    Then the request is served with status 200
    And alice's wallet was seeded exactly once
    And the seeded-wallet count is 1
    And alice's derived ledger balance equals her wallet balance

  Scenario: A second priced request does not re-seed
    Given alice sends a priced chat request
    When alice sends a priced chat request
    Then the request is served with status 200
    And alice's wallet was seeded exactly once
    And the seeded-wallet count is 1

  Scenario: An exhausted seed cap stays an honest 402 - the policy verdict, not an error
    Given the seed limit is 1
    And "bob" is logged in with a signed keypair
    And bob has already consumed the last seed slot
    When alice sends a priced chat request
    Then the request is rejected with status 402 and message "insufficient balance - add funds"
    And alice's wallet row exists with balance 0.00
    And alice holds a seed claim but received no seed credit
    And the seeded-wallet count is 1

  # --- THE REGRESSION: a transient store failure during the seed --------------

  Scenario: A seed-write failure is a retryable server error, never a false 402
    Given the seed-grant relation is offline
    When alice sends a priced chat request
    Then the request is rejected with status 500 and message "wallet error"
    And the failure status is in the client-retryable 5xx range, not the 4xx billing range
    And the response does not claim "insufficient balance"
    And no partial seed state exists for alice

  Scenario: A seed-counter failure inside the seed transaction is the same retryable error
    Given the seed-counter relation is offline
    When alice sends a priced chat request
    Then the request is rejected with status 500 and message "wallet error"
    And the failure status is in the client-retryable 5xx range, not the 4xx billing range
    And no partial seed state exists for alice

  Scenario: The voice relay surfaces the same honest error on a seed-write failure
    Given a paid voice station "v" is on air
    And the seed-grant relation is offline
    When alice sends a priced speech request
    Then the request is rejected with status 500 and message "wallet error"
    And the failure status is in the client-retryable 5xx range, not the 4xx billing range
    And no partial seed state exists for alice

  # --- recovery: the retry the 5xx invites must be safe -----------------------

  Scenario: A retry after the store recovers seeds exactly once - full rollback, no drift
    Given the seed-grant relation is offline
    And alice sends a priced chat request
    When the seed-grant relation is restored
    And alice sends a priced chat request
    Then the request is served with status 200
    And alice's wallet was seeded exactly once
    And the seeded-wallet count is 1
    And alice's derived ledger balance equals her wallet balance

  Scenario: Failed seed attempts never consume the free-credit budget, and the cap still binds
    Given the seed limit is 1
    And the seed-grant relation is offline
    And alice sends a priced chat request
    And alice sends a priced chat request
    When the seed-grant relation is restored
    Then the seed status is 0 seeded of 1, with 1 remaining
    When alice sends a priced chat request
    Then the request is served with status 200
    And the seed status is 1 seeded of 1, with 0 remaining
    Given "bob" is logged in with a signed keypair
    When bob sends a priced chat request
    Then the request is rejected with status 402 and message "insufficient balance - add funds"

  Scenario: ADVERSARIAL - seed-failure games cannot mint a second seed
    Given the seed-grant relation is offline
    And alice sends a priced chat request
    When the seed-grant relation is restored
    And alice sends a priced chat request
    And alice sends a priced chat request
    Then alice's wallet was seeded exactly once
    And alice's lifetime seed credit is exactly 1.00
    And the seeded-wallet count is 1
    And alice's derived ledger balance equals her wallet balance

  Scenario: The hold floor sends even zero-priced public traffic through the seed path - same honest error
    Given a zero-priced public chat station "z" is on air
    And the seed-grant relation is offline
    When alice sends a chat request for the zero-priced model
    Then the request is rejected with status 500 and message "wallet error"
    And the failure status is in the client-retryable 5xx range, not the 4xx billing range
    And no partial seed state exists for alice

  Scenario: Self-use traffic is genuinely free and immune to a seed-store outage
    Given alice also operates her own station "own"
    And the seed-grant relation is offline
    When alice sends a chat request to her own station
    Then the request is served with status 200
    And alice's wallet was never seeded

  # --- W4 shared seeded-flag (Redis accelerator + multi-instance) -------------

  Scenario: A failed seed unwinds the shared seeded-flag so the retry can seed
    Given the broker runs with the shared seeded-flag accelerator
    And the seed-grant relation is offline
    And alice sends a priced chat request
    And the request is rejected with status 500 and message "wallet error"
    When the seed-grant relation is restored
    And alice sends a priced chat request
    Then the request is served with status 200
    And alice's wallet was seeded exactly once
    And the seeded-wallet count is 1

  Scenario: A shared-store outage on the seeded-flag read never breaks the money path
    Given the broker runs with the shared seeded-flag accelerator
    And the shared store is down
    When alice sends a priced chat request
    Then the request is served with status 200
    And alice's wallet was seeded exactly once

  Scenario: MULTI-INSTANCE - a peer instance can serve the retry after a seed failure
    Given two broker instances share the Postgres store and the seeded-flag accelerator
    And the seed-grant relation is offline
    And alice sends a priced chat request to instance A
    And the request is rejected with status 500 and message "wallet error"
    When the seed-grant relation is restored
    And alice sends a priced chat request to instance B
    Then the request is served with status 200
    And alice's wallet was seeded exactly once
    And the seeded-wallet count is 1

  Scenario: MULTI-INSTANCE - the seeded-flag skip on a peer never double-seeds and never false-402s
    Given two broker instances share the Postgres store and the seeded-flag accelerator
    And alice sends a priced chat request to instance A
    And the request is served with status 200
    When alice sends a priced chat request to instance B
    Then the request is served with status 200
    And alice's wallet was seeded exactly once
    And the seeded-wallet count is 1
