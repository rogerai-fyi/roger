# The production Postgres ledger. These scenarios run against a REAL Postgres — no mocks —
# via ROGERAI_TEST_DATABASE_URL (the cover-gate provisions one; the suite SKIPS when it is
# unset). Executable under godog in internal/store/ledger_bdd_test.go (package store, so it
# runs serially with the parity tests that TRUNCATE the shared DB). The guarantees here hold
# IDENTICALLY to the in-memory reference store.
Feature: Postgres ledger holds, settles, and never loses or invents money

  Background:
    Given a fresh Postgres-backed store
    And the platform fee rate is 30%

  Scenario: A hold never overdraws the wallet
    Given wallet "alice" has 10.00 in real credits
    When alice places a hold of 100.00
    Then the hold is refused
    And alice's balance is still 10.00
    When alice places a hold of 10.00
    Then the hold succeeds
    And alice's balance is 0.00

  Scenario: Hold then Finalize debits exactly the cost and refunds the rest (conservation)
    Given wallet "alice" has 10.00 in real credits
    And node "n1" is owned by account "op1"
    When alice holds 5.00
    And the request settles via Finalize with hold 5.00, cost 2.00, owner share 1.40
    Then alice's balance is 8.00
    And operator "op1" has earned 1.40
    And the platform keeps 0.60
    And no credits were created or destroyed

  Scenario: Settle debits the wallet and mints the operator's real earning
    Given wallet "alice" has 10.00 in real credits
    And node "n1" is owned by account "op1"
    When the request settles via Settle with cost 2.00, owner share 1.40
    Then alice's balance is 8.00
    And operator "op1" has earned 1.40

  Scenario: Seed-funded spend mints NO operator earning (you can't pay out money never paid in)
    Given wallet "seeded" has 100.00 in FREE seed credits
    And node "n1" is owned by account "op1"
    When the request settles via Settle with cost 2.00, owner share 1.40
    Then operator "op1" has earned 0.00

  Scenario: A chargeback claws the operator's SHARE of the disputed amount, not more
    Given wallet "alice" has 1000.00 in real credits
    And node "n1" is owned by account "op1"
    And alice has two settled requests of cost 100.00 each (owner share 70.00 each)
    When a chargeback of 100.00 is processed for alice
    Then exactly 70.00 is clawed back from operator "op1"
    And the platform loss is 30.00
    And the operator's other 70.00 lot is untouched
    And clawed plus platform-loss equals the disputed 100.00

  Scenario: A chargeback is idempotent on the Stripe dispute id
    Given wallet "alice" has 100.00 in real credits
    And node "n1" is owned by account "op1"
    And alice has one settled request of cost 50.00 (owner share 35.00)
    When a chargeback of 50.00 with dispute id "dp_1" is processed
    And the same chargeback "dp_1" is delivered again
    Then the second delivery claws back 0.00
    And alice's balance is unchanged by the redelivery
