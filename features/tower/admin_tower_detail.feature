# BUILD STATUS: BUILT. The GET /admin/tower endpoint, the per-tower earnings TowerTraffic
# read method, the country-origin tally (internal/towercore/origin), and their capture at
# the edge bridge all ship with this spec. Exercised by cmd/rogerai-broker
# admin_tower_detail_test.go; the roger-admin detail UI consumes it.
Feature: The admin Tower detail view
  So an operator of Roger Core can manage, monitor, and control the quality of a
  self-hosted Tower, the dashboard needs one place that gathers everything Core knows
  about a single Tower: who it is and where it stands in its lifecycle, the quality
  signals that decide whether it may carry traffic, the traffic it actually carried -
  on what models and from which countries - and what that owes, and the Stations serving
  behind it. This is a READ surface for the private admin dashboard; the lifecycle
  controls (approve, suspend, cut off) already exist as their own endpoints and this view
  links to them rather than duplicating them.

  Background:
    Given a Core with an admin caller
    And an enrolled Tower "tw-detail" owned by "op-alice"

  # ---------------------------------------------------------------------------
  # Access: the same admin gate as the approval queue, and nothing about a Tower
  # that does not exist.
  # ---------------------------------------------------------------------------
  Scenario: Only an admin may read a Tower's detail
    When an unauthenticated caller requests the detail of "tw-detail"
    Then the response is 403
    And the body carries no Tower fields

  Scenario: A non-GET method is refused
    When an admin sends POST to the Tower detail endpoint
    Then the response is 405

  Scenario: An unknown Tower id is a not-found, not an empty Tower
    When an admin requests the detail of "tw-does-not-exist"
    Then the response is 404
    And the body carries no Tower fields

  Scenario: A request with no id is a bad request
    When an admin requests the Tower detail endpoint with no id
    Then the response is 400

  # ---------------------------------------------------------------------------
  # Identity and lifecycle: exactly the approval-queue fields, on one Tower.
  # ---------------------------------------------------------------------------
  Scenario: The detail carries the Tower's identity and lifecycle state
    When an admin requests the detail of "tw-detail"
    Then the response is 200
    And the detail names tower id "tw-detail"
    And the detail names owner "op-alice"
    And the detail carries the enrolment time as an RFC3339 timestamp
    And the detail carries the current lifecycle state
    And the detail says whether the link is live right now
    And the detail carries the advertised endpoint when the link plane is known

  Scenario: The detail never leaks a secret
    When an admin requests the detail of "tw-detail"
    Then the response carries no epoch key, dispatch key, session key, grant, or admin token

  # ---------------------------------------------------------------------------
  # Quality signals: the reputation tally AND the thresholds that read it, so an
  # approver sees not just the numbers but how close the Tower is to suspension.
  # ---------------------------------------------------------------------------
  Scenario: The detail carries the reputation tally over the window
    Given "tw-detail" has recorded 8 canary passes and 1 canary failure
    And "tw-detail" has 20 corroborated and 5 uncorroborated attempts
    And "tw-detail" has 1 audit mismatch and 3 station faults
    When an admin requests the detail of "tw-detail"
    Then the quality block reports 8 canary passes and 1 canary failure
    And the quality block reports 20 corroborated and 5 uncorroborated attempts
    And the quality block reports 1 audit mismatch and 3 station faults
    And the quality block reports the total attempts in the window

  Scenario: The detail carries the suspension thresholds the tally is judged against
    When an admin requests the detail of "tw-detail"
    Then the quality block states the minimum canaries before a rate is judged
    And the quality block states the maximum canary failure rate allowed
    And the quality block states the Tower's current canary failure rate

  Scenario: A Tower over the failure threshold is flagged as suspendable
    Given "tw-detail" has recorded 3 canary passes and 7 canary failures
    When an admin requests the detail of "tw-detail"
    Then the quality block marks the Tower as over the failure threshold

  Scenario: A Tower with too few canaries is not yet judgeable
    Given "tw-detail" has recorded 1 canary pass and 1 canary failure
    When an admin requests the detail of "tw-detail"
    Then the quality block marks the failure rate as not yet judgeable
    And the quality block does not mark the Tower as over the failure threshold

  Scenario: Station faults are counted but never feed the suspension rate
    Given "tw-detail" has recorded 5 canary passes and 0 canary failures
    And "tw-detail" has 40 station faults
    When an admin requests the detail of "tw-detail"
    Then the quality block reports 40 station faults
    And the quality block does not mark the Tower as over the failure threshold

  # ---------------------------------------------------------------------------
  # Per-station quality: which leaf behind the Tower is the one dragging it down.
  # ---------------------------------------------------------------------------
  Scenario: The detail breaks quality down by Station
    Given station "st-good" behind "tw-detail" has 10 canary passes and 0 failures
    And station "st-bad" behind "tw-detail" has 2 canary passes and 8 failures
    When an admin requests the detail of "tw-detail"
    Then the per-station quality names "st-good" with 0 failures
    And the per-station quality names "st-bad" with 8 failures

  # ---------------------------------------------------------------------------
  # Traffic and earnings: how much work the Tower carried, on what models, and
  # what that owes - the money view, per model, over the window.
  # ---------------------------------------------------------------------------
  Scenario: The detail rolls traffic up by model
    Given "tw-detail" carried 30 attempts of "llama-3.1-8b" totalling 6000 in and 12000 out tokens
    And "tw-detail" carried 10 attempts of "qwen-2.5-7b" totalling 1000 in and 2500 out tokens
    When an admin requests the detail of "tw-detail"
    Then the traffic block reports "llama-3.1-8b" with 30 attempts, 6000 in and 12000 out tokens
    And the traffic block reports "qwen-2.5-7b" with 10 attempts, 1000 in and 2500 out tokens
    And the traffic block reports the earned amount for each model in micros

  Scenario: The traffic view distinguishes corroborated work from uncorroborated
    Given "tw-detail" carried 18 corroborated and 12 uncorroborated attempts of "llama-3.1-8b"
    When an admin requests the detail of "tw-detail"
    Then the traffic block reports 18 corroborated attempts for "llama-3.1-8b"
    And the traffic block reports 12 uncorroborated attempts for "llama-3.1-8b"

  Scenario: Self-dealing traffic is surfaced for review and earns nothing
    Given "tw-detail" carried 5 self-dealing attempts of "llama-3.1-8b"
    When an admin requests the detail of "tw-detail"
    Then the traffic block surfaces the self-dealt amount separately
    And the self-dealt amount is not counted in what the Tower is owed

  # ---------------------------------------------------------------------------
  # Traffic origin: WHERE the work came from, geographically, so an operator can
  # see the shape of a Tower's demand and spot an anomaly (all traffic from one
  # country overnight). Origin is COARSE and PRIVACY-PRESERVING by construction:
  #   - The only origin recorded is the 2-letter ISO country Cloudflare already
  #     hands Core on the inbound request (CF-IPCountry). No IP is ever stored.
  #   - It is PRIVACY-PRESERVING BY CONSTRUCTION: no stored record ever carries an
  #     attempt id beside a country. Idempotency is tracked by the attempt id alone,
  #     the country under a surrogate id with no attempt id - so nothing in the store
  #     joins a consumer (reached via the attempt id) to where their request came from.
  #     The view answers "how much from where" but the schema itself cannot answer "who".
  #   - A request with no country header (dev, a non-CF path) is counted as
  #     "unknown" rather than dropped or guessed.
  # ---------------------------------------------------------------------------
  Scenario: The detail rolls traffic origin up by country
    Given "tw-detail" routed 40 attempts from country "US"
    And "tw-detail" routed 15 attempts from country "DE"
    And "tw-detail" routed 5 attempts from country "BR"
    When an admin requests the detail of "tw-detail"
    Then the origin block reports 40 attempts routed from "US"
    And the origin block reports 15 attempts routed from "DE"
    And the origin block reports 5 attempts routed from "BR"

  Scenario: Traffic with no country header is counted as unknown, not dropped
    Given "tw-detail" routed 7 attempts with no country header
    When an admin requests the detail of "tw-detail"
    Then the origin block reports 7 attempts routed from "unknown"

  Scenario: The origin tally records only the country, never an address or an identity
    Given "tw-detail" routed 40 attempts from country "US"
    When an admin requests the detail of "tw-detail"
    Then the origin block carries no IP address, consumer account, pubkey, or wallet

  Scenario: The country is taken from Cloudflare's header, not a client-supplied one
    Given a consumer request carrying CF-IPCountry "US" and a forged X-Country "CN"
    When the attempt's origin is recorded for "tw-detail"
    Then the origin block counts it under "US"

  # ---------------------------------------------------------------------------
  # The fleet behind the Tower: the Stations it is currently serving through.
  # ---------------------------------------------------------------------------
  Scenario: The detail lists the Stations serving behind the Tower
    Given station "st-good" behind "tw-detail" advertises "llama-3.1-8b" chat
    And station "st-bad" behind "tw-detail" advertises "qwen-2.5-7b" chat
    When an admin requests the detail of "tw-detail"
    Then the fleet block names "st-good" serving "llama-3.1-8b"
    And the fleet block names "st-bad" serving "qwen-2.5-7b"
    And the fleet block carries each Station's advertised price

  # ---------------------------------------------------------------------------
  # Multi-instance: the detail reads the fleet-wide, shared view, not one
  # instance's memory - the same union the routing fabric serves from.
  # ---------------------------------------------------------------------------
  Scenario: The detail reflects work another instance recorded
    Given a second Core instance sharing the durable stores
    And the second instance recorded 12 attempts of "llama-3.1-8b" for "tw-detail"
    When an admin on the first instance requests the detail of "tw-detail"
    Then the traffic block reports 12 attempts of "llama-3.1-8b"

  Scenario: The origin tally is fleet-wide too
    Given a second Core instance sharing the durable stores
    And the first instance recorded 10 attempts from country "US" for "tw-detail"
    And the second instance recorded 6 attempts from country "US" for "tw-detail"
    When an admin on the first instance requests the detail of "tw-detail"
    Then the origin block reports 16 attempts routed from "US"