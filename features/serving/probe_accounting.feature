# BUILD STATUS: APPROVED 2026-11-15 ("Approve as written"), being built on wt/probe-accounting.
# Wording awaiting re-approval: R1 reads Core's record through GET /nodes/served, authenticated
# with the node's own token like its poll, instead of /stations, which needs a logged-in owner
# with a verified identity and so would leave anonymous sharers (and most phones) with no number.
# It returns aggregates only; owners can already see per-request rows in /stations.
# Also for re-approval: the difference between what a node handled and what Core counted is
# labelled "unbilled jobs" (Core's checks, or work that did not settle), not "network checks":
# a cancelled or failed customer job falls in it too.
#
# Why: a phone serving an on-device model showed 14 requests "served" within a couple of minutes.
# They were Core's own checks. Since the 2026-10-06 ruling (a canary carries a fresh customer-style
# pseudonym, so a node cannot recognise and game it), the node-side filter `user == "probe"` in the
# iOS app (ShareService.swift) and in the CLI (internal/agent/agent.go) never matches: every canary
# and tool-call check counts as served, and the CLI also adds each canary's priced value to the
# earnings it shows. Core's own ledger, receipts and /stations are correct: checks are never settled.
#
# Measured on the code as of 2026-11-15, a node that just came on air receives in its first ~4 min:
# a canary at 0 s, 30 s, 90 s, 210 s from EACH broker instance (the schedule is per process), plus a
# tool-call canary each round for any model that has not earned "tools" (re-checked at full cadence
# forever). With two instances that is about 12 jobs, each a full model run on the node.
#
# Rulings this spec asks for:
#   R1  What a node shows as served and earned comes from Core's record, never its own count.
#   R2  One probe per node per round across all instances; the result is shared with every instance.
#   R3  A model that has not earned "tools" is re-checked on a growing interval, not every round.
#   R4  A hard probe budget per node: at most 12 Core checks an hour and 60 a day, all kinds, all
#       instances together, first sight included.
#   R5  A node that says it runs on battery (the phone app) is checked at the ceiling after first sight.
#
# Ruled 2026-11-15 ("Fit the schedule to 60/day"): the idle schedule fits the budget. The probe
# ceiling is 30 minutes (48 canaries a day at rest), a verified model's tool support is re-asserted by
# each passed canary and fully re-checked once a day, and "verified" stays fresh for the ceiling.
#
# Unchanged on purpose: a canary still looks like a customer request to the node; nothing on a job
# tells the node it is a check before or after it serves it.

Feature: Core's checks are counted as checks and cost as little as possible

  Background:
    Given Roger Core runs two instances sharing one store
    And a node serving one chat model, logged in as its owner

  # --- R1: honest counts ------------------------------------------------------------------

  Scenario: The share screen's served and earned come from Core's record
    When the node serves 3 paid requests and Core sends it 5 checks
    Then the CLI share screen and the phone app show 3 served
    And they show the owner share of the 3 settled requests as earned
    And both numbers are read from Core's record of this node's settled requests (GET /nodes/served)

  Scenario: A check never adds to earnings shown anywhere
    When Core sends the node a canary priced at the node's own rate
    Then no earnings figure on the CLI, the phone app or the web console rises

  Scenario: The node still cannot tell a check from a customer
    Then a check job carries a fresh customer-style pseudonym and no field marking it as a check
    And no answer to the node, before or after it serves the job, says it was a check

  Scenario: What the node handled is shown apart from what it served
    When the node handled 8 jobs and Core's record shows 3 paid
    Then the share screen shows "3 served" as the headline
    And it may show "5 unbilled jobs" as the difference, labelled as Core's checks or work that did not settle

  Scenario: Core's record cannot be read for a moment
    When GET /nodes/served does not answer
    Then the share screen shows the last numbers Core gave, marked as not current
    And it never falls back to the node's own count as "served"

  Scenario: The record is read sparingly
    Then the share screen reads GET /nodes/served at most once every 30 seconds while it is open
    And Core answers a node that asks more often with 429 and Retry-After
    And not at all while it is closed or the app is in the background

  # --- R2: one probe per node per round -----------------------------------------------------

  Scenario: Two instances do not both probe the same node
    When a probe round comes due for the node on both instances
    Then exactly one instance sends the check, holding a shared lease on that node for the round
    And the other instance sends nothing

  Scenario: A probe result reaches every instance
    When one instance's check of the node passes
    Then both instances read the node as freshly measured, with the same latency and pass reading
    And routing on either instance uses that reading

  Scenario: The store is down
    When the shared store does not answer
    Then each instance probes on its own schedule, as today, and no instance probes faster than today

  # --- R3: tool-call checks back off ----------------------------------------------------------

  Scenario Outline: A model that has not earned "tools" is re-checked on a growing interval
    Given the model has failed or not answered <n> tool-call checks in a row
    Then its next tool-call check is due <after> after the last one

    Examples:
      | n | after      |
      | 1 | 20 minutes |
      | 2 | 40 minutes |
      | 3 | 80 minutes |
      | 6 | 24 hours   |
      | 9 | 24 hours   |

  Scenario: A passing tool-call check resets the back-off
    Given the model has failed 4 tool-call checks in a row
    When the next one passes
    Then the model earns "tools" and is re-checked on the verified throttle, as today

  Scenario: A node that re-registers does not restart the tool-call back-off
    Given the model is backed off to 80 minutes
    When the node reconnects with the same node id and model
    Then the back-off continues where it was

  # --- R4: a hard budget ---------------------------------------------------------------------

  Scenario: A node never receives more than the budget
    When a node comes on air and stays idle for a day, with consumers browsing its model all day
    Then it receives at most 12 Core checks in any hour and at most 60 in the day
    And that counts canaries, tool-call checks and Tower-carried canaries together, on all instances

  Scenario: A check the budget refuses is skipped, not queued
    When a probe comes due and the node's hourly budget is spent
    Then no check is sent and the node keeps its current reading

  Scenario: Real traffic is the measurement
    When the node serves paid requests
    Then each one counts as a fresh measurement and pushes the next check out, as today

  # --- R5: battery ---------------------------------------------------------------------------

  Scenario: A battery-powered node is checked at the ceiling after first sight
    Given the phone app registers its node as running on battery
    When the node comes on air
    Then it receives one canary at first sight
    And after that at most one check per probe ceiling (30 minutes), whatever consumers browse

  Scenario: An idle node's schedule fits the budget, and its marks never lapse
    Given an idle node with one verified chat model
    Then its canaries come once per probe ceiling (30 minutes) and its tool support is fully re-checked once a day
    And that is within its daily budget
    And each passed canary re-asserts its verified tools mark, so neither "verified" nor "tools" ages out between checks

  Scenario: A node does not pay for being browsed
    When consumers browse /discover or /market for the node's model every few seconds
    Then the node's checks stay within its cadence and its budget
