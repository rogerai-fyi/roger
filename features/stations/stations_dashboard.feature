# A STATION IS NOT A TOWER. A Station is a machine SERVING inference (`roger share`):
# it earns the provider share on work it performs. A Tower is a broker-like RELAY that
# routes work to Stations and, in the compensated tier, earns a share of net platform
# revenue on traffic settled through it — its operator view is
# features/tower/operator_dashboard.feature, which is PROPOSED and not yet implementable.
# The two report different objects, different money, and different lifecycles.
#
# The operator's station list — an owner running one or more stations (nodes) needs a
# single account-bound view of what they are running, whether it is on air, what it has
# earned, and what evidence has accrued against it. Today that information is scattered
# across /earnings, /strikes and the on-air registry with no per-station roll-up.
#
# Lives in features/stations/ (not features/operator/) because the TUI guest-operator
# suite globs the whole features/operator directory, and this is a BROKER-side spec.
#
# GROUND TRUTH: cmd/rogerai-broker/stations.go; store NodesOfAccount,
# NodeRecord, ChainStatus, EarningsOf, StrikesByOwner.

Feature: An owner can see every station they run, its status, usage and evidence
  The list is scoped to the AUTHENTICATED owner. It never exposes another operator's
  stations, consumer identities, or prompt content.

  Background:
    Given an authenticated owner account

  # --- authorization --------------------------------------------------------

  Scenario: The station list requires an authenticated owner
    Given the caller presents no session and no valid request signature
    When they read the station list
    Then the request is rejected as unauthorized
    And no station data is returned

  Scenario: An owner sees only their own stations
    Given the owner runs stations "alpha" and "beta"
    And another operator runs station "gamma"
    When the owner reads their station list
    Then it contains "alpha" and "beta"
    And it does not contain "gamma"

  Scenario: An owner with no stations gets an empty list, not an error
    Given the owner runs no stations
    When they read their station list
    Then the response is an empty list

  # --- what each station reports -------------------------------------------

  Scenario: Each station reports its registration and liveness
    Given the owner runs a station that registered and last heartbeated recently
    When they read their station list
    Then the station reports its id, registered-at time, and last-seen time
    And it reports its offered model, modality, and price
    And it reports whether it is currently on air

  Scenario: A station that has stopped heartbeating reports off air
    Given a station has not heartbeated within the liveness window
    When the owner reads their station list
    Then that station reports on-air false
    And its registration and history remain visible

  Scenario: Each station reports its earnings and served volume
    Given a station has served requests and minted earnings
    When the owner reads their station list
    Then the station reports its current earnings balance
    And it reports how many requests it has served

  # --- evidence, shown honestly ---------------------------------------------

  Scenario: Each station reports its receipt-chain status
    Given the broker has recorded a chain head for the station
    When the owner reads their station list
    Then the station reports its current chain head, last check time, and break count
    And the break count is labelled an audit signal, not a penalty

  Scenario: A station with no receipts yet reports an unknown chain, not a break
    Given the broker has never recorded a receipt from the station
    When the owner reads their station list
    Then the station reports no chain head and zero breaks
    And it is not presented as broken

  Scenario: Strike evidence is surfaced to the owner it belongs to
    Given the owner's account has accrued strikes
    When they read their station list
    Then the response reports the owner's strike count and kinds
    And each strike carries the evidence that produced it

  # --- privacy --------------------------------------------------------------

  Scenario: The station list never exposes consumer or content data
    Given a station has served many requests
    When the owner reads their station list
    Then no consumer identity, prompt, completion, or request body appears in the response
    And no other operator's earnings or account identifiers appear

  Scenario: A bridge token or private band code is never returned
    Given a station registered with a bridge token and runs on a private band
    When the owner reads their station list
    Then the bridge token is absent from the response
    And the band's secret frequency code is absent
