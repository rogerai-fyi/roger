# Spec-first behavior contract for the operator money-out rail:
#   store.RequestPayout / SettlePayout / FailPayout (internal/store/store.go),
#   the 120-day hold + promotion (promoteLocked, LoadPayoutPolicy),
#   the reserve coupling (addLotLocked, splitLocked),
#   the Stripe Connect transfer + reversal + retry sweep
#   (cmd/rogerai-broker/payouts.go), and refreshConnectStatus / the KYC gate.
#
# Money/lot model recap:
#   - A request's owner share becomes an earning lot in state `held`. After the
#     120-day hold (policy default) it promotes to `payable`; a payout marks it
#     `paid`; a dispute marks it `clawed`.
#   - RequestPayout debits ONLY payable lots, records a PENDING payout, and returns
#     the EXACT debited amount. The broker then creates the Stripe transfer for that
#     amount and either SettlePayout (money moved) or FailPayout (transfer failed).
#   - Policy defaults (Option A): hold=120 days, reserve=0, min payout=$25,
#     schedule=monthly.

Feature: Operator payouts move only cleared, payable earnings and never strand money

  Background:
    Given a fresh money store
    And the platform fee rate is 30%
    And the payout policy is hold 120 days, reserve 0, minimum 25.00, schedule monthly

  # ===========================================================================
  # 1. THE 120-DAY HOLD WINDOW (held -> payable).
  # ===========================================================================

  Scenario: A freshly earned lot is held and not yet payable
    Given node "n1" is owned by account "op1"
    And operator "op1" earned a lot of 70.00 just now on node "n1"
    When the earnings split for "op1" is read now
    Then the held balance is 70.00
    And the payable balance is 0.00
    And the next release is 120 days from now

  Scenario: A lot one second before its release is still held
    Given node "n1" is owned by account "op1"
    And operator "op1" has a held lot of 70.00 releasing in 1 second
    When the earnings split for "op1" is read now
    Then the held balance is 70.00
    And the payable balance is 0.00

  Scenario: A lot exactly at its release instant promotes to payable (boundary)
    Given node "n1" is owned by account "op1"
    And operator "op1" has a held lot of 70.00 with release time exactly now
    When the earnings split for "op1" is read now
    Then the payable balance is 70.00
    And the held balance is 0.00

  Scenario Outline: Promotion is a sweep-on-read keyed on the 120-day boundary
    Given node "n1" is owned by account "op1"
    And operator "op1" has a held lot of 70.00 created <age_days> days ago
    When the earnings split for "op1" is read now
    Then the payable balance is <payable>
    And the held balance is <held>

    Examples:
      | age_days | payable | held  |
      | 0        | 0.00    | 70.00 |
      | 119      | 0.00    | 70.00 |
      | 120      | 70.00   | 0.00  |
      | 200      | 70.00   | 0.00  |

  Scenario: A lot held by an OPEN node recount discrepancy does not promote even past 120 days
    Given node "n1" is owned by account "op1"
    And operator "op1" has a held lot of 70.00 created 200 days ago on node "n1"
    And node "n1" has an open recount hold
    When the earnings split for "op1" is read now
    Then the held balance is 70.00
    And the payable balance is 0.00

  Scenario: A lot held by an OWNER-account recount hold does not promote (survives node-id rotation)
    Given node "n1" is owned by account "op1"
    And operator "op1" has a held lot of 70.00 created 200 days ago on node "n1"
    And account "op1" has an open recount hold
    When the earnings split for "op1" is read now
    Then the payable balance is 0.00

  Scenario: Clearing a recount hold lets the matured lot promote on the next read
    Given node "n1" is owned by account "op1"
    And operator "op1" has a held lot of 70.00 created 200 days ago on node "n1"
    And node "n1" has an open recount hold
    When the hold on node "n1" is cleared
    And the earnings split for "op1" is read now
    Then the payable balance is 70.00

  # ===========================================================================
  # 2. RequestPayout: minimum gate, payable-only, exact debited amount.
  # ===========================================================================

  Scenario: A payout below the minimum is refused with no debit and no payout row
    Given node "n1" is owned by account "op1"
    And operator "op1" has a payable balance of 24.99
    When operator "op1" requests a payout
    Then the payout is refused for being below the minimum
    And no payout row is created
    And the payable balance is still 24.99

  Scenario: A payout exactly at the minimum is allowed (boundary)
    Given node "n1" is owned by account "op1"
    And operator "op1" has a payable balance of 25.00
    When operator "op1" requests a payout
    Then the payout succeeds
    And the payout amount is 25.00
    And the payable balance is 0.00

  Scenario: A payout of one cent over the minimum is allowed
    Given node "n1" is owned by account "op1"
    And operator "op1" has a payable balance of 25.01
    When operator "op1" requests a payout
    Then the payout succeeds
    And the payout amount is 25.01

  Scenario: RequestPayout debits ONLY payable lots — held lots are left behind
    Given node "n1" is owned by account "op1"
    And operator "op1" has a payable lot of 40.00
    And operator "op1" has a held lot of 1000.00
    When operator "op1" requests a payout
    Then the payout amount is 40.00
    And the held balance is still 1000.00
    And only the payable lot is marked paid

  Scenario: RequestPayout never includes a clawed lot
    Given node "n1" is owned by account "op1"
    And operator "op1" has a payable lot of 40.00
    And operator "op1" has a clawed lot of 70.00
    When operator "op1" requests a payout
    Then the payout amount is 40.00
    And the clawed lot stays clawed

  Scenario: RequestPayout sums multiple payable lots into one payout and one ledger debit
    Given node "n1" is owned by account "op1"
    And operator "op1" has payable lots of 10.00, 20.00, and 30.00
    When operator "op1" requests a payout
    Then the payout amount is 60.00
    And a single payout ledger row of -60.00 is written for "op1"
    And all three lots are marked paid with the same payout id

  Scenario: With nothing payable the payout is refused (below minimum / nothing to pay)
    Given node "n1" is owned by account "op1"
    And operator "op1" has only held lots totaling 500.00
    When operator "op1" requests a payout
    Then the payout is refused for being below the minimum
    And no payout row is created

  Scenario: RequestPayout promotes matured held lots first, then pays them
    Given node "n1" is owned by account "op1"
    And operator "op1" has a held lot of 100.00 created 200 days ago
    When operator "op1" requests a payout
    Then the payout amount is 100.00
    And the lot is marked paid

  # ===========================================================================
  # 3. SettlePayout (money moved) and FailPayout (rollback).
  # ===========================================================================

  Scenario: SettlePayout flips the pending payout to paid and records the transfer id
    Given node "n1" is owned by account "op1"
    And operator "op1" has a pending payout of 60.00
    When the payout is settled with transfer "tr_1"
    Then the payout state is paid
    And the payout records transfer "tr_1"
    And the payout ledger row references transfer "tr_1"

  Scenario: SettlePayout is idempotent — settling an already-paid payout is a no-op
    Given node "n1" is owned by account "op1"
    And operator "op1" has a payout already settled with transfer "tr_1"
    When the payout is settled again with transfer "tr_2"
    Then the payout state is paid
    And the payout still records transfer "tr_1"

  Scenario: FailPayout rolls the debited lots back to payable and reverses the debit
    Given node "n1" is owned by account "op1"
    And operator "op1" has a pending payout of 60.00 covering lots of 10.00, 20.00, and 30.00
    When the payout is failed
    Then the payout state is failed
    And all three lots return to payable
    And each rolled-back lot has its payout id cleared
    And the payout ledger row is reversed
    And the payable balance is 60.00 again

  Scenario: FailPayout is idempotent / safe on a non-pending payout
    Given node "n1" is owned by account "op1"
    And operator "op1" has a payout already settled with transfer "tr_1"
    When the payout is failed
    Then the payout state is still paid
    And the lots stay paid

  Scenario: A rolled-back payout's amount can be requested again and matches
    Given node "n1" is owned by account "op1"
    And operator "op1" has a pending payout of 60.00
    When the payout is failed
    And operator "op1" requests a payout again
    Then the new payout amount is 60.00

  # ===========================================================================
  # 4. THE FULL HANDLER FLOW: debit -> transfer -> settle/rollback ordering.
  #    (cmd/rogerai-broker/payoutsRequest)
  # ===========================================================================

  Scenario: Happy path — debit, transfer, settle, exactly one transfer for the debited amount
    Given operator "op1" has completed Connect KYC (transfers active)
    And operator "op1" has a payable balance of 60.00
    When operator "op1" calls POST /payouts/request
    Then a Stripe transfer is created for exactly 60.00
    And the transfer idempotency key is the payout id
    And the payout is settled and returned as paid

  Scenario: KYC gate — a payout is refused until Connect transfers are active
    Given operator "op1" has a payable balance of 60.00
    And operator "op1" has not completed Connect onboarding
    When operator "op1" calls POST /payouts/request
    Then the request is rejected with 403
    And no Stripe transfer is created
    And no payout row is created

  Scenario: Transfer fails AFTER the debit — the payout is rolled back, lots return to payable
    Given operator "op1" has completed Connect KYC (transfers active)
    And operator "op1" has a payable balance of 60.00
    And the Stripe transfer will fail
    When operator "op1" calls POST /payouts/request
    Then the payout is rolled back via FailPayout
    And the lots return to payable
    And the response is a 502
    And no completed transfer is left associated with payable lots

  Scenario: Transfer SUCCEEDS but the settle write fails — the lots are NOT rolled back and support is told the transfer id
    Given operator "op1" has completed Connect KYC (transfers active)
    And operator "op1" has a payable balance of 60.00
    And the Stripe transfer will succeed with "tr_ok"
    And the settle write will fail
    When operator "op1" calls POST /payouts/request
    Then the lots are NOT rolled back
    And the response is a 500 mentioning transfer "tr_ok"

  Scenario: Concurrent payout requests for one operator never both debit the payable lots
    Given operator "op1" has completed Connect KYC (transfers active)
    And operator "op1" has a payable balance of 60.00
    When two payout requests for "op1" run concurrently
    Then exactly one payout debits 60.00
    And the other sees nothing payable or is below minimum
    And the total transferred is 60.00

  Scenario: The pre-check below-minimum returns a clean 400 with no transfer and no payout row
    Given operator "op1" has completed Connect KYC (transfers active)
    And operator "op1" has a payable balance of 10.00
    When operator "op1" calls POST /payouts/request
    Then the response is a 400 below-minimum
    And no Stripe transfer is created
    And no payout row is created

  # ===========================================================================
  # 5. RESERVE handling: the reserve releases WITH the lot (no separate tail) and
  #    is never stranded. Default reserve is 0; these cover a configured reserve.
  # ===========================================================================

  Scenario: With the default zero reserve, the whole lot is payable at release and fully paid
    Given the payout policy reserve is 0
    And node "n1" is owned by account "op1"
    And operator "op1" has a held lot of 70.00 created 200 days ago
    When operator "op1" requests a payout
    Then the payout amount is 70.00
    And no reserve is left behind

  Scenario: With a configured reserve, the reserve releases together with the lot (coupled release_at == reserve_release_at)
    Given the payout policy reserve is 0.10
    And node "n1" is owned by account "op1"
    And operator "op1" earns a lot whose owner share is 100.00 just now
    Then the lot reserve is 10.00
    And the lot reserve_release_at equals its release_at

  Scenario: A reserved lot before release shows reserve as reserved, not payable
    Given the payout policy reserve is 0.10
    And node "n1" is owned by account "op1"
    And operator "op1" has a held lot with gross 100.00 and reserve 10.00 releasing in the future
    When the earnings split for "op1" is read now
    Then the held balance is 90.00
    And the reserved balance is 10.00
    And the payable balance is 0.00

  Scenario: A reserved lot after release is fully payable (reserve not stranded)
    Given the payout policy reserve is 0.10
    And node "n1" is owned by account "op1"
    And operator "op1" has a lot with gross 100.00 and reserve 10.00 created 200 days ago
    When the earnings split for "op1" is read now
    Then the payable balance is 100.00
    And the reserved balance is 0.00

  Scenario: Requesting a payout on a matured reserved lot pays gross including the reserve
    Given the payout policy reserve is 0.10
    And node "n1" is owned by account "op1"
    And operator "op1" has a lot with gross 100.00 and reserve 10.00 created 200 days ago
    When operator "op1" requests a payout
    Then the payout amount is 100.00
    And the lot is marked paid in full
    And no reserve remains attributable to the lot

  # ===========================================================================
  # 6. Stripe Connect transfer + reversal primitives + status refresh.
  # ===========================================================================

  Scenario: payoutTransfer converts credits to cents using the credit-USD rate
    Given the credit-to-USD rate is 1.00
    When a payout transfer of 60.00 credits is created
    Then the Stripe amount is 6000 cents

  Scenario Outline: payoutTransfer cents conversion rounds to the nearest cent
    Given the credit-to-USD rate is <rate>
    When a payout transfer of <credits> credits is created
    Then the Stripe amount is <cents> cents

    Examples:
      | rate | credits | cents |
      | 1.00 | 25.00   | 2500  |
      | 1.00 | 0.005   | 1     |
      | 1.00 | 0.004   | 0     |
      | 0.50 | 60.00   | 3000  |
      | 2.00 | 10.00   | 2000  |

  Scenario: refreshConnectStatus maps the transfers capability to our status vocabulary
    Given operator "op1" has a Connect account
    When the Connect account reports transfers capability "active"
    Then the stored status becomes "active"
    And the operator can request a payout

  Scenario Outline: refreshConnectStatus status mapping
    Given operator "op1" has a Connect account
    When the Connect account reports transfers "<transfers>" and disabled reason "<reason>"
    Then the stored status becomes "<status>"

    Examples:
      | transfers | reason            | status      |
      | active    |                   | active      |
      |           | requirements.past | restricted  |
      | pending   |                   | onboarding  |
      |           |                   | onboarding  |

  Scenario: A transport error during refresh leaves the prior status unchanged
    Given operator "op1" has a stored status of "active"
    When the Connect status refresh hits a transport error
    Then the status read falls back to the stored "active"

  # ===========================================================================
  # 7. Production fail-closed guards (ROGERAI_REQUIRE_LIVE).
  # ===========================================================================

  Scenario: Under REQUIRE_LIVE without an sk_live key, payouts are disabled (no dev stub, no test-mode money)
    Given ROGERAI_REQUIRE_LIVE is set
    And the Stripe secret key is not an sk_live key
    When the payout rail loads
    Then payouts are disabled
    And no transfer is ever issued with a dev-stub transfer id

  Scenario: Under REQUIRE_LIVE a transfer to a stub connect account is refused and the payout rolls back
    Given ROGERAI_REQUIRE_LIVE is set
    And operator "op1" has connect account "acct_dev_stub"
    And operator "op1" has a payable balance of 60.00
    When a payout transfer is attempted for "op1"
    Then the transfer is refused
    And SettlePayout is never reached with a fake transfer id
    And the payout rolls back via FailPayout

  Scenario: Without a Stripe key in dev, the transfer is stubbed and labeled as moving no real money
    Given no Stripe secret key is configured
    And REQUIRE_LIVE is not set
    And operator "op1" has a payable balance of 60.00
    When a payout transfer is attempted for "op1"
    Then a dev-stub transfer id prefixed "tr_dev_stub_" is returned
    And the payout can still be settled in dev
