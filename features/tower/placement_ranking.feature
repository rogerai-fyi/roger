# APPROVED SPEC - written and founder approved 2026-11-13 ("Approve as written"), with the
# proposed numbers accepted as written. Changes to an approved scenario need re-approval.
# BUILD STATUS: NOT BUILT.
#
# Founder request 2026-11-13: implement ideas 1 (pick the Tower by distance) and 2 (house
# traffic stays on Towers we run; community Towers carry community Stations), "trully and
# completely". This file replaces first-fit placement (toweredgeattach.go, "matchmaking beyond
# first-fit is a later refinement") with a ranked choice.
#
# HOW IDEA 1 IS DELIVERED, AND WHY NOT PER REQUEST. The founder ruled on 2026-08-20 (design doc
# section 6.3b) that a Station keeps ONE sticky Tower and moves only while idle. Choosing a Tower
# per request (M3 with per-attempt M4 sessions) was rejected because every hard money problem
# (self-dealing, two holds, mid-flight attribution) comes from the relay being a per-request
# variable. So distance enters at the two moments the ruling allows: when a Station is placed
# (this file) and when an idle Station is moved (station_mobility.feature). Re-opening per-request
# relay choice would need that ruling reversed first; this spec does not assume it.
#
# Ruling this file depends on (design doc section 4.2/4.3, already written down): locality must
# not defeat load balancing (no "nearest wins" magnet), and our own Towers may not be ranked ahead
# of an operator's for an operator's Station without a measured reason.

Feature: Core places each Station on the best eligible Tower, not the first one
  When a node self-attaches (or re-attaches after a move), Core picks its Tower from every live
  Tower in the fleet. It filters to the eligible ones, ranks them, and picks among the best
  with a two-choice draw so no single Tower becomes a magnet. House Stations prefer house
  Towers. Community Stations are ranked over every Tower, and a house Tower wins one only on
  a measured signal, never on a tie.

  Terms:
    | term            | meaning                                                                    |
    | house account   | an owner on the house list Core already keeps for the per-owner cap       |
    | house Tower     | a Tower whose owner is a house account                                     |
    | house Station   | a Station whose owner is a house account                                   |
    | community       | anything that is not house                                                  |
    | health tier     | A, unless the Tower's last two health checks both failed or its reputation verdict is not clean, then B |
    | load            | the Tower's live attached Stations divided by its Station capacity         |
    | trust stage     | new, proving or established (tower_trust_ramp.feature); house Towers have none |

  Background:
    Given the link mirror is readable
    And a network-to-continent table is configured

  # --- the candidate set --------------------------------------------------------

  Scenario: Placement considers every live Tower in the fleet, not only this instance's links
    Given Tower T1's link is held by broker instance 1 and Tower T2's by instance 2
    When a node's attach lands on instance 1
    Then both T1 and T2 are candidates, read from the shared link mirror
    And the chosen Tower's hub endpoint and certificate pin come from that same mirror record

  Scenario: With the mirror unreadable, placement falls back to this instance's own links
    Given the link mirror cannot be read
    When a node attaches
    Then only Towers whose link this instance holds are candidates
    And Core logs one "placement degraded: mirror unavailable" line per minute at most
    And placement never invents a Tower it cannot see

  Scenario Outline: A Tower that cannot take this Station is not a candidate
    Given Tower T is live except that "<condition>"
    When a Station is placed
    Then T is not a candidate

    Examples:
      | condition                                                          |
      | its lifecycle state is not active or quarantine                    |
      | the registry says it may not take work                             |
      | it advertises no hub endpoint                                      |
      | its link record is older than the freshness window                 |
      | it is at its Station capacity                                      |
      | its trust stage caps community Stations and the cap is reached     |
      | the Station is a house Station and its trust stage allows none     |
      | the Station is a house Station and its house cap is reached        |
      | it is the Tower this Station is being moved off                    |
      | it is the Tower the Station's last failed move tried, within the move's retry window |

  Scenario: A Tower's Station capacity is derived from its hub connection cap
    Given a hub accepts at most 512 concurrent connections
    And each Station holds as many connections as its worker count
    When Core computes the Tower's Station capacity
    Then it is the connection cap divided by the per-Station worker count, rounded down
    And a Tower at capacity takes no new Station until one leaves

  # --- house and community -------------------------------------------------------

  Scenario: A house Station goes to a house Tower when one is eligible
    Given an eligible house Tower and an eligible established community Tower
    When a house Station is placed
    Then it goes to the house Tower, whatever the locality or load of either

  Scenario: A house Station goes to a community Tower only when no house Tower can take it
    Given no house Tower is eligible
    And a community Tower whose trust stage allows house Stations and has room under its house cap
    When a house Station is placed
    Then it goes to that community Tower
    And the placement is recorded with reason "house failover"

  Scenario: A house Station with nowhere allowed stays direct
    Given no house Tower is eligible
    And no community Tower allows house Stations
    When a house Station attaches
    Then Core answers "no tower can host this node right now" exactly as today
    And the node keeps serving on the classic direct path, unaffected

  Scenario: For a community Station, house and community Towers are ranked together, and ties go to the community
    Given an eligible community Tower and an eligible house Tower in the same health tier and locality class
    When a community Station is placed
    Then it goes to the community Tower
    And the house Tower is considered only if no community Tower is in that best tier and class

  Scenario: A house Tower wins a community Station only on a measured signal
    Given a community Station whose locality class is "same_continent" with a house Tower and "far" with every community Tower
    When the Station is placed
    Then it goes to the house Tower
    And the placement is recorded with reason "nearer house tower"

  Scenario: House Towers never win a tie
    Given the ranking is applied to a community Station
    Then a house Tower is chosen only when it is in a strictly better health tier or locality class than every eligible community Tower
    And no draw ever includes a house Tower alongside a community Tower

  # --- the ranking ---------------------------------------------------------------

  Scenario: Candidates are ranked by health first, then locality, then load
    Given the eligible candidates for a Station (for a house Station, the house group first)
    When Core ranks them
    Then a health tier A Tower ranks above any tier B Tower
    And within a tier, the locality classes rank same_network, then same_continent, then unknown, then far
    And within one tier and one class, the choice is a two-choice draw on load
    And for a community Station the draw is over community Towers only, unless none is in that tier and class

  Scenario: A trusted far Tower beats an unproven near one
    Given Tower Far is tier A and "far" from the Station
    And Tower Near is tier B and "same_network" with the Station
    When the Station is placed
    Then it goes to Tower Far

  Scenario: The two-choice draw keeps one Tower from absorbing a region
    Given five eligible Towers share the best tier and locality class
    When 1000 Stations are placed in a row
    Then each placement draws two of the five at random and takes the less loaded
    And no Tower's load exceeds the least loaded one's by more than the two-choice bound
    And the draw is seeded per placement, never from anything the node sends

  Scenario: One candidate in the best class is chosen without a draw
    Given exactly one Tower is in the best tier and locality class
    When the Station is placed
    Then it goes to that Tower

  Scenario: Equal load in the draw is broken at random, not by Tower id
    Given the two drawn Towers have equal load
    When the Station is placed
    Then either may be chosen with equal chance
    And a Tower cannot win ties by choosing an id that sorts first

  Scenario Outline: Worked placements
    Given eligible Towers "<towers>"
    When a "<kind>" Station in "<where>" is placed
    Then it goes to "<expected>"

    Examples:
      | towers                                                           | kind      | where           | expected     |
      | HouseOC(A,NA,same net as house rig), Nora(A,AF)                  | house     | the house rig   | HouseOC      |
      | HouseOC(A,NA), HouseCore(A,EU), Nora(A,AF)                       | house     | an EU network   | HouseCore    |
      | Nora(A,AF, established)                                          | house     | the house rig   | Nora         |
      | Nora(A,AF, new)                                                  | house     | the house rig   | nowhere      |
      | HouseOC(A,NA), Nora(A,AF)                                        | community | an AF network   | Nora         |
      | HouseOC(A,NA), Nora(A,AF)                                        | community | an NA network   | HouseOC      |
      | HouseOC(A,NA), OpNA(A,NA)                                        | community | an NA network   | OpNA         |
      | HouseOC(A,NA), Nora(B,AF)                                        | community | an AF network   | HouseOC      |
      | HouseOC(A,NA), Nora(A,AF), OpNA(A,NA)                            | community | an NA network   | OpNA         |
      | HouseOC(A,NA), Nora(A,AF), OpNA(A,NA)                            | community | an AF network   | Nora         |
      | HouseOC(A,NA), Nora(A,AF, at community cap)                      | community | an AF network   | HouseOC      |

  # --- what placement writes ------------------------------------------------------

  Scenario: Every placement is recorded with why
    When Core places a Station on a Tower
    Then the governance record gets a "station_placed" event for that Tower
    And its detail names the Station, the locality class, the health tier, the group (house or community), the reason and how many candidates there were
    And the existing "station_attached" event is unchanged

  Scenario: The attach reply is unchanged in shape
    When a Station is placed by the ranked choice
    Then the reply carries the same fields as today: station id, Tower id, endpoint, pin, hub token, Tower key fingerprint and state
    And an older node that knows nothing of ranking attaches exactly as before

  Scenario: A live Station's re-attach never re-runs placement
    Given a Station is live on Tower T
    When its node re-attaches
    Then Core answers from the existing attachment on Tower T, exactly as today
    And only a move (station_mobility.feature) can change a live Station's Tower

  # --- money and self-dealing ------------------------------------------------------

  Scenario: Placement does not change who is paid what
    Given a Station is placed by the ranked choice
    When an edge attempt through its Tower settles
    Then the Station, Tower and platform shares are computed exactly as today
    And the existing self-dealing checks run unchanged

  Scenario: An operator cannot buy placement
    Given a Tower operator sets a lower relay price, a region, a display name or any other field they control
    When Stations are placed
    Then none of those fields changes the ranking
