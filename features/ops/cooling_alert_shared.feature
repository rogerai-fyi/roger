# COOLING ALERT ACROSS INSTANCES: "this station keeps hitting its upstream rate limit" counts every
# broker instance's cooldowns and survives a deploy.
#
# PURPOSE
#   When a station's provider keeps answering 429, the broker cools it and routes around it, and once
#   the station has been cooling for more than 10 minutes cumulative within the last hour the
#   founder is paged once ("station_cooling:<node>"). Today the hour of cooldown events is kept per
#   instance: with two instances each sees roughly half the 429s and may never cross the threshold,
#   and every deploy resets the window. This feature pins the founder's ruling (2026-10-04): the
#   window is shared.
#
# GROUND TRUTH (origin/main 12207827)
#   - cmd/rogerai-broker/cooling.go:74 coolingAlertWindow = 1 h, :75 coolingAlertThreshold = 10 min.
#   - cooling.go:240 coolStation appends {at, added} to the per-instance b.coolEvents[node]
#     (cooling.go:257); "added" is only the time this cooldown extended the running one (a 429
#     inside a running cooldown adds the extension, not a fresh full window).
#   - cooling.go:422 checkCoolingAlerts sums the per-instance events and pages via adminAlert when
#     the sum exceeds the threshold; a window with no events clears the alert.
#   - The alert's dedupe is already shared (alerts.go adminAlert keeps its "fired" key in the
#     shared store, features/ops/alert_delivery.feature), so paging once across instances holds
#     once the COUNT is shared.
#   - Approved and kept: features/routing/upstream_failover.feature:446 "a station that keeps
#     cooling pages the founder once" (fired once naming the band and the count; clears after an
#     hour without a cooldown).
#
# RULES PINNED HERE
#   W1 Each cooldown appends one event (the seconds it added) to a shared per-station record of the
#      last hour; every instance's alert check reads the same record.
#   W2 The cumulative cooling time is counted once: two instances cooling the same station for the
#      same 429 window do not double count (the "added" seconds are computed against the shared
#      cooldown expiry, not each instance's local view).
#   W3 The record survives a restart; events older than the window fall out.
#   W4 Outage: the alert is ops telemetry and fails OPEN: if the shared store is unreachable the
#      instance keeps its local events and may page from them; routing is unaffected.
#
# Enforced by: cmd/rogerai-broker/cooling_alert_shared_bdd_test.go

Feature: The station-cooling alert counts every instance's cooldowns

  Background:
    Given two broker instances "A" and "B" over the same durable and shared stores
    And a station "s1" is on air for "m"
    And the founder alert address is configured

  # --- W1: one count across instances ------------------------------------------------------

  Scenario: Cooldowns split across two instances add up to one page
    Given "s1" cooled for 6 minutes total on "A" in the last hour
    And "s1" cooled for 6 minutes total on "B" in the last hour, in separate windows
    When the alert checker runs on "A"
    Then the founder alert "station_cooling:s1" fired once

  Scenario: Either instance's checker sees the shared total
    Given "s1" cooled for 6 minutes total on "A" in the last hour
    And "s1" cooled for 6 minutes total on "B" in the last hour, in separate windows
    When the alert checker runs on "B"
    Then the founder alert "station_cooling:s1" fired once

  Scenario: Both checkers running still page once
    Given "s1" cooled for 12 minutes total across "A" and "B" in the last hour
    When the alert checker runs on "A" and on "B"
    Then the founder alert "station_cooling:s1" fired once

  Scenario: Below the threshold across both instances nothing pages
    Given "s1" cooled for 4 minutes total on "A" in the last hour
    And "s1" cooled for 5 minutes total on "B" in the last hour, in separate windows
    When the alert checker runs on "A"
    Then no cooling alert fired

  Scenario: The page names the band and the count of cooldowns across both instances
    Given "s1" cooled 3 times on "A" and 3 times on "B" in the last hour, 2 minutes each, in separate windows
    When the alert checker runs on "A"
    Then the founder alert "station_cooling:s1" names the band "m" and 6 cooldowns

  # --- W2: no double counting --------------------------------------------------------------

  Scenario: The same cooldown window seen on both instances is counted once
    Given "s1" answered 429 with Retry-After 120 on "A"
    And "s1" answered 429 with Retry-After 120 on "B" 1 second later
    When the alert checker runs on "A"
    Then the shared cooling total for "s1" is about 121 seconds, not 240

  # --- W3: restart and window ------------------------------------------------------------------

  Scenario: A deploy does not reset the hour of cooldowns
    Given "s1" cooled for 8 minutes total on "A" in the last hour
    When "A" restarts as a fresh broker over the same stores
    And "s1" cools for 3 more minutes on the fresh "A"
    And the alert checker runs on the fresh "A"
    Then the founder alert "station_cooling:s1" fired once

  Scenario: Cooldowns older than an hour fall out of the shared window
    Given "s1" cooled for 8 minutes total on "A" 70 minutes ago
    And "s1" cooled for 3 minutes total on "B" in the last hour
    When the alert checker runs on "A"
    Then no cooling alert fired

  Scenario: An hour without any cooldown on any instance clears the alert
    Given the founder alert "station_cooling:s1" fired from cooldowns on "A" and "B"
    When an hour passes with no cooldown on either instance
    And the alert checker runs on "B"
    Then the alert "station_cooling:s1" is cleared

  Scenario: A cooldown on one instance keeps the alert open for both
    Given the founder alert "station_cooling:s1" fired from cooldowns on "A" and "B"
    When 50 minutes pass and "s1" cools again on "B"
    And the alert checker runs on "A"
    Then the alert "station_cooling:s1" is not cleared

  # --- W4: outage (fails open) ------------------------------------------------------------------

  Scenario: With the shared store unreachable an instance still pages from its own cooldowns
    Given the shared store is unreachable
    And "s1" cooled for 12 minutes total on "A" in the last hour
    When the alert checker runs on "A"
    Then the founder alert "station_cooling:s1" fired once

  Scenario: A shared-store outage never blocks a cooldown or a relay
    Given the shared store is unreachable
    When "s1" answers 429 on "A"
    Then "s1" is cooling on "A"
    And the consumer got a 429 with Retry-After or was served by a sibling
