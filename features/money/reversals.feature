# Spec-first behavior contract for the DURABLE pending-reversal lifecycle — the
# silent-money-leak guard on a post-payout dispute:
#   store.RecordPendingReversal / OpenPendingReversals / MarkReversalAttempt
#   (internal/store/store.go), and the broker side
#   reversePaidLots / reversalRetrySweep / payoutTransferReversal
#   (cmd/rogerai-broker/payouts.go).
#
# Why this exists: when a dispute claws an ALREADY-PAID lot, the ledger clawback is
# recorded synchronously, but the actual money pull-back is a Stripe Transfer
# Reversal that can transiently fail. Without a durable intent that failure silently
# leaks money (the clawback stands but the cash is never recovered). One row per
# (dispute, lot), keyed "reverse:<disputeID>:<lotID>", which is ALSO the Stripe
# Idempotency-Key, so a webhook redelivery or a retry never double-reverses.

Feature: Durable transfer-reversal intents are retried until they succeed or dead-letter

  Background:
    Given a fresh money store

  # ===========================================================================
  # 1. RECORD: the intent is durable and idempotent on its key.
  # ===========================================================================

  Scenario: Recording a reversal intent persists it as open and owed
    When a pending reversal "reverse:dp1:7" for transfer "tr_1" of 70.00 is recorded for account "op1"
    Then the open pending reversals include "reverse:dp1:7"
    And "reverse:dp1:7" has 0 attempts
    And "reverse:dp1:7" is not done
    And "reverse:dp1:7" is not dead-lettered
    And "reverse:dp1:7" has a created-at timestamp

  Scenario: Re-recording the same key is a no-op that never resets attempts or done
    Given a pending reversal "reverse:dp1:7" for transfer "tr_1" of 70.00 recorded for account "op1"
    And "reverse:dp1:7" has recorded 3 failed attempts
    When the same pending reversal "reverse:dp1:7" is recorded again
    Then "reverse:dp1:7" still has 3 attempts
    And "reverse:dp1:7" is still not done

  Scenario: Re-recording a DONE key never resurrects it
    Given a pending reversal "reverse:dp1:7" that has succeeded and is done
    When the same pending reversal "reverse:dp1:7" is recorded again
    Then "reverse:dp1:7" is still done
    And "reverse:dp1:7" is not in the open list

  Scenario: An empty key is rejected and records nothing
    When a pending reversal with an empty key is recorded
    Then no pending reversal is stored

  Scenario: The reversal intent is recorded BEFORE the Stripe call so a crash mid-call still leaves it owed
    Given a dispute clawed an already-paid lot 7 paid on transfer "tr_1" for 70.00
    When the broker begins issuing the reversal for lot 7
    Then the intent "reverse:<dispute>:7" exists before the Stripe API call
    And if the process dies mid-call the intent survives in the open list

  # ===========================================================================
  # 2. OPEN: the sweep only ever reads rows still owed, oldest first.
  # ===========================================================================

  Scenario: Open list excludes done and dead-lettered rows
    Given a pending reversal "reverse:a:1" that is open
    And a pending reversal "reverse:b:2" that is done
    And a pending reversal "reverse:c:3" that is dead-lettered
    When the open pending reversals are listed
    Then the open list contains "reverse:a:1"
    And the open list does not contain "reverse:b:2"
    And the open list does not contain "reverse:c:3"

  Scenario: Open list is ordered oldest-first
    Given a pending reversal "reverse:old:1" created earliest
    And a pending reversal "reverse:mid:2" created later
    And a pending reversal "reverse:new:3" created latest
    When the open pending reversals are listed
    Then they appear in the order "reverse:old:1", "reverse:mid:2", "reverse:new:3"

  Scenario: Open list honors the limit
    Given 5 open pending reversals exist
    When the open pending reversals are listed with limit 2
    Then exactly 2 rows are returned

  Scenario: A zero limit returns all open rows
    Given 5 open pending reversals exist
    When the open pending reversals are listed with limit 0
    Then 5 rows are returned

  # ===========================================================================
  # 3. MARK ATTEMPT: success is terminal; failure backs off and eventually
  #    dead-letters at MaxAttempts.
  # ===========================================================================

  Scenario: A successful attempt marks the row done and clears the last error
    Given a pending reversal "reverse:dp1:7" that is open with 2 failed attempts
    When a successful attempt is marked for "reverse:dp1:7"
    Then "reverse:dp1:7" is done
    And "reverse:dp1:7" has 3 attempts
    And "reverse:dp1:7" has an empty last error
    And "reverse:dp1:7" leaves the open list

  Scenario: A failed attempt bumps the count, records the error, and stays open below max
    Given a pending reversal "reverse:dp1:7" that is open with 0 attempts
    And the reversal max attempts is 10
    When a failed attempt is marked for "reverse:dp1:7" with error "stripe 500"
    Then "reverse:dp1:7" has 1 attempt
    And "reverse:dp1:7" records last error "stripe 500"
    And "reverse:dp1:7" is not dead-lettered
    And "reverse:dp1:7" stays in the open list

  Scenario: Reaching max attempts parks the row as a dead-letter and removes it from the open list
    Given a pending reversal "reverse:dp1:7" that is open with 9 attempts
    And the reversal max attempts is 10
    When a failed attempt is marked for "reverse:dp1:7" with error "still failing"
    Then "reverse:dp1:7" has 10 attempts
    And "reverse:dp1:7" is dead-lettered
    And "reverse:dp1:7" is not in the open list

  Scenario: Max attempts of zero or less means never dead-letter (retry forever)
    Given a pending reversal "reverse:dp1:7" that is open with 50 attempts
    And the reversal max attempts is 0
    When a failed attempt is marked for "reverse:dp1:7" with error "x"
    Then "reverse:dp1:7" is not dead-lettered
    And "reverse:dp1:7" stays in the open list

  Scenario: Marking an attempt on an unknown key is a no-op
    When a successful attempt is marked for unknown key "reverse:none:0"
    Then no pending reversal is created or changed

  Scenario: Marking an attempt on an already-done key is a no-op (terminal)
    Given a pending reversal "reverse:dp1:7" that is done
    When a failed attempt is marked for "reverse:dp1:7" with error "late"
    Then "reverse:dp1:7" is still done
    And "reverse:dp1:7" keeps its attempt count

  Scenario: Every attempt stamps the last-attempt time
    Given a pending reversal "reverse:dp1:7" that is open
    When a failed attempt is marked for "reverse:dp1:7" at a known time
    Then "reverse:dp1:7" last-attempt time is that time

  Scenario Outline: Attempt accounting up to the dead-letter boundary
    Given a pending reversal "reverse:dp1:7" that is open with <start> attempts
    And the reversal max attempts is <max>
    When a failed attempt is marked for "reverse:dp1:7" with error "e"
    Then "reverse:dp1:7" has <after> attempts
    And dead-lettered is <dead>

    Examples:
      | start | max | after | dead  |
      | 0     | 10  | 1     | false |
      | 8     | 10  | 9     | false |
      | 9     | 10  | 10    | true  |
      | 0     | 1   | 1     | true  |
      | 99    | 0   | 100   | false |

  # ===========================================================================
  # 4. THE RETRY SWEEP (reversalRetrySweep) ties it together.
  # ===========================================================================

  Scenario: The sweep recovers a transient failure on a later pass
    Given an open pending reversal "reverse:dp1:7" for transfer "tr_1" of 70.00 that failed once
    And the Stripe reversal will now succeed
    When the retry sweep runs
    Then "reverse:dp1:7" becomes done
    And the operator is notified their paid-out earning was clawed back

  Scenario: The sweep retries again when Stripe still fails and the row is below max
    Given an open pending reversal "reverse:dp1:7" with 1 attempt
    And the reversal max attempts is 10
    And the Stripe reversal will fail
    When the retry sweep runs
    Then "reverse:dp1:7" has 2 attempts
    And "reverse:dp1:7" stays open for the next sweep

  Scenario: The sweep dead-letters loudly once a row exhausts its attempts
    Given an open pending reversal "reverse:dp1:7" with 9 attempts
    And the reversal max attempts is 10
    And the Stripe reversal will fail
    When the retry sweep runs
    Then "reverse:dp1:7" is dead-lettered
    And the failure is logged for manual handling
    And the ledger clawback already stands regardless

  Scenario: The sweep uses the row key as the Stripe Idempotency-Key so a reversal already done at Stripe is a safe no-op
    Given an open pending reversal "reverse:dp1:7" for transfer "tr_1"
    And Stripe already reversed transfer "tr_1" under key "reverse:dp1:7"
    When the retry sweep runs
    Then the re-attempt is idempotent at Stripe
    And "reverse:dp1:7" is marked done

  # ===========================================================================
  # 5. reversePaidLots: per-reversal behavior on the dispute path.
  # ===========================================================================

  Scenario: A paid lot with no recorded transfer id is skipped (no intent, no Stripe call)
    Given a dispute returns a reversal for lot 7 with an empty transfer id
    When reversePaidLots processes the dispute
    Then no Stripe reversal is attempted for lot 7
    And it is logged for manual reconciliation
    And the ledger clawback for lot 7 still stands

  Scenario: A reversal that fails on the immediate attempt is recorded for retry, not dropped
    Given a dispute returns a reversal for lot 7 on transfer "tr_1" of 70.00
    And the immediate Stripe reversal will fail
    When reversePaidLots processes the dispute
    Then the intent "reverse:<dispute>:7" is open with a recorded failed attempt
    And the ledger clawback stands

  Scenario: A reversal that succeeds immediately is marked done and the operator is emailed
    Given a dispute returns a reversal for lot 7 on transfer "tr_1" of 70.00
    And the immediate Stripe reversal will succeed
    When reversePaidLots processes the dispute
    Then the intent "reverse:<dispute>:7" is done
    And the operator gets a payout-reversed notice (best-effort)

  Scenario: A redelivered dispute webhook re-records the same key with no double reversal
    Given a dispute already produced a done reversal "reverse:dp1:7"
    When the same dispute is redelivered and reversePaidLots runs again
    Then no second Stripe reversal is issued
    And "reverse:dp1:7" stays done
