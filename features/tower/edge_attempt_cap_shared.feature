# BUILD STATUS: BUILT. Specified 2026-10-04 (persistent-state audit); the cap is one shared set per account across instances.
# EDGE ATTEMPT CAP ACROSS INSTANCES: an account can hold at most 32 open Tower attempts at once,
# counted across every broker instance, surviving restarts, and freed by whichever instance sees
# the attempt end.
#
# PURPOSE
#   Every request bridged to a Tower opens an "attempt" that pins a station on the Tower fabric
#   until it settles or its deadline passes. The per-account cap (32 open attempts) exists so one
#   account cannot pin the whole fabric. Today the count lives in each instance's memory: with N
#   instances an account may open 32 per instance, a restart forgets the open attempts while they
#   are still pinning stations, and a settlement that arrives at a different instance than the one
#   that opened the attempt does not free the slot (only the opening instance's expiry timer does).
#   This feature pins the founder's ruling (2026-10-04): the cap is enforced from the shared store.
#
# GROUND TRUTH (origin/main 12207827)
#   - cmd/rogerai-broker/toweredge.go:1161 maxOpenEdgeAttemptsPerAccount = 32.
#   - toweredge.go:1171 edgeAccountReserve / :1188 edgeAccountRelease: a per-instance
#     map edgeOpenByAccount under metricsMu.
#   - toweredge.go:1231 edgeEnterInflight: records the attempt in the per-instance edgeInflight map
#     and schedules edgeExitInflight at the attempt deadline (time.AfterFunc, per process).
#   - toweredge.go:1281 edgeExitInflight decrements the opening instance's count; it is "a no-op on
#     any instance that did not open this attempt" (toweredge.go:1868, the settle path).
#   - cmd/rogerai-broker/edgebridge.go:127 the bridge refuses with 429 "too many edge attempts open
#     on this account at once ..." and Retry-After 5 when the reserve fails (hard mode); soft mode
#     declines silently (edge_fanout_test.go:516-524 pins both).
#
# RULES PINNED HERE
#   E1 The open attempts of an account are one shared set (member = attempt id, score = the
#      attempt's deadline). Reserve = trim expired members, count, and add, in one atomic step.
#   E2 The cap is 32 across all instances together.
#   E3 An attempt leaves the set when ANY instance sees it end: settle, abandon, release, or its
#      deadline passing (expired members are trimmed on every reserve, so no timer is needed).
#   E4 A restarted instance sees the attempts opened before the restart.
#   E5 Outage: the cap is an abuse limit and fails CLOSED: if the shared store cannot be reached,
#      a bridged request is refused (hard mode 503 "shared_store_unavailable" with Retry-After;
#      soft mode declines the bridge and the direct path may still serve). A single-instance broker
#      with no shared store configured keeps the in-process count (unchanged).
#   E6 The refusal text, status and Retry-After of an at-cap request are unchanged (approved).
#
# Enforced by: cmd/rogerai-broker/edge_attempt_cap_shared_bdd_test.go

Feature: The open-attempt cap of an account is shared by every broker instance

  Background:
    Given two broker instances "A" and "B" over the same durable and shared stores
    And an account "acct"

  # --- E1/E2: one count across instances -------------------------------------------------

  Scenario: 32 attempts opened on A leave no slot on B
    Given "acct" has 32 open attempts opened on "A"
    When "acct" reserves a slot on "B"
    Then the reserve is refused

  Scenario: Attempts split across instances share one cap
    Given "acct" has 20 open attempts opened on "A"
    And "acct" has 12 open attempts opened on "B"
    When "acct" reserves a slot on "A"
    Then the reserve is refused

  Scenario: Below the cap a reserve succeeds on either instance
    Given "acct" has 31 open attempts opened on "A"
    When "acct" reserves a slot on "B"
    Then the reserve succeeds
    And "acct" has 32 open attempts

  Scenario: Concurrent reserves on two instances at the last slot admit exactly one
    Given "acct" has 31 open attempts opened on "A"
    When "acct" reserves a slot on "A" and on "B" at the same moment
    Then exactly one reserve succeeds

  Scenario: Forty concurrent reserves across two instances admit exactly 32
    When "acct" reserves 20 slots on "A" and 20 on "B" at the same moment
    Then exactly 32 reserves succeed

  Scenario: Accounts do not share slots
    Given "acct" has 32 open attempts opened on "A"
    When account "other" reserves a slot on "B"
    Then the reserve succeeds

  # --- E3: any instance frees the slot -------------------------------------------------------

  Scenario: An attempt settled on B frees the slot it held on A
    Given "acct" has 32 open attempts opened on "A"
    When one of those attempts settles on "B"
    And "acct" reserves a slot on "A"
    Then the reserve succeeds

  Scenario: An attempt abandoned on the opening instance frees its slot everywhere
    Given "acct" has 32 open attempts opened on "A"
    When "A" releases one of them before dispatch
    And "acct" reserves a slot on "B"
    Then the reserve succeeds

  Scenario: An attempt past its deadline no longer counts on any instance
    Given "acct" has 32 open attempts opened on "A" with deadlines 30 seconds away
    When 31 seconds pass
    And "acct" reserves a slot on "B"
    Then the reserve succeeds

  Scenario: Settling the same attempt twice frees only one slot
    Given "acct" has 32 open attempts opened on "A"
    When one attempt settles on "B" and the same attempt settles again on "A"
    Then "acct" has 31 open attempts

  Scenario: A settle for an attempt id that was never opened frees nothing
    Given "acct" has 32 open attempts opened on "A"
    When an unknown attempt id settles on "B"
    Then "acct" has 32 open attempts

  # --- E4: restart -----------------------------------------------------------------------------

  Scenario: A restarted instance still counts the attempts it opened before the restart
    Given "acct" has 32 open attempts opened on "A"
    When "A" restarts as a fresh broker over the same stores
    And "acct" reserves a slot on the fresh "A"
    Then the reserve is refused

  Scenario: Attempts opened before a restart still free their slot when they settle afterwards
    Given "acct" has 32 open attempts opened on "A"
    When "A" restarts as a fresh broker over the same stores
    And one of those attempts settles on the fresh "A"
    And "acct" reserves a slot on "B"
    Then the reserve succeeds

  # --- E6: the request-level refusal (unchanged wording) --------------------------------------

  Scenario: An at-cap bridged request on B is refused with today's 429 even though A holds the slots
    Given an approved Tower serves "tm" and no direct station serves it
    And "acct" has 32 open attempts opened on "A"
    When a funded "acct" relays to "tm" on "B"
    Then the response is 429 "too many edge attempts open on this account at once"
    And it carries Retry-After "5"
    And no attempt was submitted to the Tower

  # --- E5: outage --------------------------------------------------------------------------

  Scenario: An unreachable shared store refuses a bridged request instead of counting locally
    Given an approved Tower serves "tm" and no direct station serves it
    And the shared store is unreachable
    When a funded "acct" relays to "tm" on "A"
    Then the response is 503 with error code "shared_store_unavailable"
    And no attempt was submitted to the Tower

  Scenario: An unreachable shared store declines the soft bridge and the direct station serves
    Given an approved Tower serves "m" and a direct station also serves "m"
    And the shared store is unreachable
    When a funded "acct" relays to "m" on "A" 20 times
    Then every relay is served by the direct station
    And no attempt was submitted to the Tower

  Scenario: A single-instance broker with no shared store keeps the in-process cap (unchanged)
    Given a single broker with no shared store configured
    And "acct" has 32 open attempts on it
    When "acct" reserves a slot on it
    Then the reserve is refused
