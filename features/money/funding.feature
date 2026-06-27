# Seed (free starter) vs real (paid) credit accounting.
#
# GROUND TRUTH (internal/store/store.go):
#   SeedOnce / BalanceOf seed a wallet via grantSeedLocked, keyed on idem "seed:<wallet>":
#     - applied AT MOST ONCE per wallet (a re-login / second BalanceOf is a no-op)
#     - no-op when seed == 0, or when the seed cap is exhausted (a new wallet -> balance 0)
#     - on grant: wallet += seed; seedRemain += seed (the FREE bucket); seedCount++
#   PeekBalance NEVER seeds (reads 0 for an unknown wallet).
#   consumeSeedLocked drains the FREE bucket BEFORE real credits on every earning spend.
#   realEarnShare scales the owner share by the real (non-seed) fraction, so SEED CREDITS
#     NEVER MINT AN OPERATOR EARNING (P0-1). consumeSeed only runs when cost>0 AND
#     ownerShare>0 (a $0 or 0-share settle does not drain the free bucket).
#   AddCredits / CreditOnce top up the wallet as REAL credits (they do NOT add to
#     seedRemain), so a top-up earns the operator normally.
#   SetSeedLimit(N) caps DISTINCT seeded wallets at N (atomic with the grant); SeedStatus
#     reports seeded / limit / remaining (remaining = -1 when unlimited).
#
# Default starter seed grant 100.00 credits. Default fee 30%.
Feature: Seed and real credit accounting - free starter credits never mint earnings

  Background:
    Given a fresh ledger-backed store
    And the platform fee rate is 30%
    And the starter seed grant is 100.00 credits

  # --- SeedOnce: at most once per wallet ----------------------------------

  Scenario: A brand-new wallet is seeded once on first balance read
    Given wallet "newbie" has never been seen
    When newbie's balance is read with a 100.00 seed
    Then newbie's balance is 100.00
    And newbie's free seed bucket is 100.00
    And the seeded-wallet count is 1

  Scenario: SeedOnce is idempotent - a re-login does not re-seed
    Given wallet "returning" has never been seen
    When SeedOnce grants returning a 100.00 seed
    Then the grant reports seeded = true
    And returning's balance is 100.00
    When SeedOnce grants returning a 100.00 seed again
    Then the grant reports seeded = false
    And returning's balance is still 100.00
    And the seeded-wallet count is 1

  Scenario: BalanceOf seeds a new wallet, and a second BalanceOf does not re-seed
    Given wallet "alice" has never been seen
    When alice's balance is read with a 100.00 seed
    Then alice's balance is 100.00
    When alice's balance is read with a 100.00 seed again
    Then alice's balance is still 100.00
    And the seeded-wallet count is 1

  Scenario: A wallet that already has credits is left untouched by SeedOnce
    Given wallet "topped" has 50.00 in real credits
    When SeedOnce grants topped a 100.00 seed
    Then the grant reports seeded = true
    And topped's balance is 150.00
    And topped's free seed bucket is 100.00
    When SeedOnce grants topped a 100.00 seed again
    Then the grant reports seeded = false
    And topped's balance is still 150.00

  Scenario: PeekBalance never seeds an unknown wallet
    Given wallet "anon" has never been seen
    When anon's balance is peeked
    Then anon's balance is 0.00
    And anon has not been seeded
    And the seeded-wallet count is 0

  Scenario: A zero seed grant is a no-op and does not count as a seeded wallet
    Given wallet "alice" has never been seen
    When alice's balance is read with a 0.00 seed
    Then alice's balance is 0.00
    And the grant reports seeded = false
    And the seeded-wallet count is 0

  # --- seed drained before real on spend ---------------------------------

  Scenario: Spend drains the free seed bucket before any real credits
    Given wallet "mixed" has 100.00 in FREE seed credits and 50.00 in real credits
    And node "n1" is owned by account "op1"
    When the request settles via Settle with cost 30.00, owner share 21.00
    Then mixed's balance is 120.00
    And mixed's free seed bucket is 70.00
    And operator "op1" has earned 0.00

  Scenario: Spend that exceeds the seed bucket splits across free then real
    Given wallet "mixed" has 100.00 in FREE seed credits and 50.00 in real credits
    And node "n1" is owned by account "op1"
    When the request settles via Settle with cost 120.00, owner share 84.00
    Then mixed's balance is 30.00
    And mixed's free seed bucket is 0.00
    And the seed-funded portion of the cost is 100.00
    And the real-funded portion of the cost is 20.00
    And operator "op1" has earned 14.00

  Scenario: Once the seed bucket is empty, all further spend is fully real and earns fully
    Given wallet "mixed" has 100.00 in FREE seed credits and 100.00 in real credits
    And node "n1" is owned by account "op1"
    When the request settles via Settle with cost 100.00, owner share 70.00
    Then mixed's free seed bucket is 0.00
    And operator "op1" has earned 0.00
    When the request settles via Settle with cost 50.00, owner share 35.00
    Then operator "op1" has earned 35.00

  # --- seed never mints earnings (P0-1 regression) -----------------------

  Scenario: REGRESSION - a fully seed-funded request mints zero operator earning
    Given wallet "freeloader" has 100.00 in FREE seed credits
    And node "n1" is owned by account "op1"
    When the request settles via Settle with cost 10.00, owner share 7.00
    Then freeloader's balance is 90.00
    And operator "op1" has earned 0.00
    And no earning lot was created for operator "op1"

  Scenario: An operator cannot mint a payout from a consumer's free seed credits
    Given wallet "abuser_consumer" has 100.00 in FREE seed credits
    And node "collusion" is owned by account "abuser_operator"
    When abuser_consumer drains the full 100.00 seed against node "collusion"
    Then operator "abuser_operator" has earned 0.00
    And no payable earning lot exists for account "abuser_operator"

  # --- seed limit (free-credit liability cap) -----------------------------

  Scenario: The seed cap stops new wallets from being seeded once the limit is reached
    Given the seed limit is 2
    When wallet "w1" is seeded with 100.00
    Then w1's balance is 100.00
    When wallet "w2" is seeded with 100.00
    Then w2's balance is 100.00
    When wallet "w3" is seeded with 100.00
    Then w3's balance is 0.00
    And the grant for w3 reports seeded = false
    And the seeded-wallet count is 2

  Scenario: SeedStatus reports seeded, limit, and remaining as the cap fills
    Given the seed limit is 3
    Then the seed status is 0 seeded of 3, with 3 remaining
    When wallet "w1" is seeded with 100.00
    Then the seed status is 1 seeded of 3, with 2 remaining
    When wallet "w2" is seeded with 100.00
    And wallet "w3" is seeded with 100.00
    Then the seed status is 3 seeded of 3, with 0 remaining
    When wallet "w4" is seeded with 100.00
    Then w4's balance is 0.00
    And the seed status is 3 seeded of 3, with 0 remaining

  Scenario: A zero or negative seed limit means unlimited seeding
    Given the seed limit is 0
    When 5 distinct wallets are each seeded with 100.00
    Then all 5 wallets have a balance of 100.00
    And the seed status reports unlimited with remaining -1

  Scenario: Concurrent first-seeds never over-grant past the cap
    Given the seed limit is 10
    When 100 distinct wallets are concurrently seeded with 100.00 each
    Then exactly 10 wallets were seeded with 100.00
    And 90 wallets have a balance of 0.00
    And the seeded-wallet count is exactly 10

  Scenario: A cap-blocked new wallet still exists at zero and can be topped up to spend
    Given the seed limit is 1
    And wallet "first" is seeded with 100.00
    When wallet "second" is seeded with 100.00
    Then second's balance is 0.00
    When 25.00 in real credits are added to second
    Then second's balance is 25.00
    And second's free seed bucket is 0.00

  # --- real top-ups earn normally -----------------------------------------

  Scenario: A real top-up adds spendable, fully-earning credits (not seed)
    Given wallet "alice" has never been seen
    When alice's balance is read with a 100.00 seed
    And 50.00 in real credits are added to alice
    Then alice's balance is 150.00
    And alice's free seed bucket is 100.00

  Scenario: After the seed bucket is spent, a real top-up earns the operator the full share
    Given wallet "alice" has 10.00 in FREE seed credits
    And node "n1" is owned by account "op1"
    When the request settles via Settle with cost 10.00, owner share 7.00
    Then operator "op1" has earned 0.00
    When 100.00 in real credits are added to alice
    And the request settles via Settle with cost 40.00, owner share 28.00
    Then operator "op1" has earned 28.00
    And alice's balance is 60.00

  Scenario Outline: Earnings track only the real-funded share regardless of how the wallet was funded
    Given wallet "w" has <seed> in FREE seed credits and <real> in real credits
    And node "n1" is owned by account "op1"
    When the request settles via Settle with cost <cost>, owner share <share>
    Then operator "op1" has earned <earned>
    And w's balance is <after>

    Examples: fee 30%
      | seed   | real   | cost  | share | earned | after  |
      | 100.00 | 0.00   | 10.00 | 7.00  | 0.00   | 90.00  |
      | 0.00   | 100.00 | 10.00 | 7.00  | 7.00   | 90.00  |
      | 5.00   | 100.00 | 10.00 | 7.00  | 3.50   | 95.00  |
      | 100.00 | 100.00 | 10.00 | 7.00  | 0.00   | 190.00 |
      | 50.00  | 50.00  | 80.00 | 56.00 | 21.00  | 20.00  |

  # --- idempotent real top-ups (CreditOnce) -------------------------------

  Scenario: A real top-up keyed by a Stripe session is applied exactly once
    Given wallet "alice" has 0.00 in real credits
    When a 50.00 top-up with key "cs_test_1" is applied to alice
    Then the top-up reports credited = true
    And alice's balance is 50.00
    When the same 50.00 top-up with key "cs_test_1" is delivered again
    Then the top-up reports credited = false
    And alice's balance is still 50.00
