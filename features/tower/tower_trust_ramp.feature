# APPROVED SPEC - written and founder approved 2026-11-13 ("Approve as written"), with the
# proposed numbers accepted as written. Changes to an approved scenario need re-approval.
# BUILD STATUS: BUILT. internal/towercore/placement/stage.go (the rules), cmd/rogerai-broker/towerstage.go
# (stage from the record, sweep, demotion, excusal/incident/ceiling routes, admin and operator views),
# towerhubcheck.go + internal/towerhub/hubcheck.go (the hub check), internal/towercore/health (evidence),
# sortband.go (the stage edge weight on the coin).
#
# Founder request 2026-11-13: idea 4, "earned trust. New Towers carry a small, capped share of
# traffic and grow as their canary and audit records stay clean", and "draft another message to
# nora explaining we are routing traffic to her incrementally".
#
# The evidence is the governance record (governance_record.feature, built) and the canary record
# Core already keeps. A Tower cannot raise its own stage; only time, clean evidence and an
# attributed admin action can.
#
# NUMBERS accepted 2026-11-13 as proposed.
#
# AMENDED 2026-11-13 (founder: "Add the hub check"): a Tower with no Stations could never pass a
# canary, because canaries reach a Tower only through a Station behind it, so it could never
# leave "new". Core now also runs a Station-free hub check, and "canary passes" became "health
# checks" (hub checks and Station canaries together). Counts and thresholds are unchanged.

Feature: A community Tower earns traffic in stages, from Core's own record of it
  Every community Tower has a trust stage, computed by Core from the governance record and
  the canary record. The stage caps how many Stations the Tower may hold, whether it may hold
  house Stations at all, and how often a request is sent through it rather than straight to
  the Station. A clean record raises the stage over time. A failure drops it at once.

  The proposed stages:
    | stage       | to enter                                                                     | community Stations | house Stations | edge weight |
    | new         | approval (quarantine to active), or any demotion                             | up to 10           | none           | 0.25        |
    | proving     | 7 days in new, clean, at least 50 passed health checks, pass rate 95% or better     | up to 50           | failover, up to 5  | 0.5     |
    | established | 30 days since entering proving, clean, pass rate 98% or better               | up to capacity     | failover, up to 25 | 1.0     |

  "Clean" over a window means: no suspension, no revocation, no lease lapse longer than one
  hour, no admin-recorded incident, and no health-check fail streak of 2 or more, except where an
  admin has recorded an excusal for that specific event.

  A "health check" is either a Station canary (as today) or a hub check: Core dials the Tower's
  hub, verifies its certificate pin where one is advertised and its signed identity proof
  against the admitted Tower key, and times the round trip. Both count toward the pass rate.

  "Edge weight" multiplies the Tower's side of the capacity-weighted coin that already decides,
  per request, whether a request for a Station behind a Tower goes through the Tower or
  straight to the Station on the classic path.

  # --- the hub check ---------------------------------------------------------------

  Scenario: Every live Tower with a hub is checked, with or without Stations
    Given a live Tower advertising a hub and holding no Stations
    When the hub check runs
    Then Core dials the hub, verifies the pin if one is advertised, and verifies the hub's signed identity proof over a fresh nonce against the admitted Tower key
    And records a "hub_check" outcome of pass or fail with the round-trip time
    And runs at the canary interval, never more often per Tower

  Scenario Outline: A hub check fails on any broken proof
    Given a live Tower whose hub "<condition>"
    When the hub check runs
    Then the outcome is a fail with reason "<reason>"

    Examples:
      | condition                                         | reason          |
      | does not accept a connection within 5 seconds     | unreachable     |
      | presents a certificate that does not match the pin | pin_mismatch   |
      | answers without an identity proof                 | no_proof        |
      | signs the nonce with a key other than the admitted one | wrong_key  |
      | signs a different nonce                           | stale_proof     |
      | answers with a malformed proof                    | malformed_proof |

  Scenario: A Tower with no hub gets no hub check and cannot pass one
    Given a live Tower that advertises no hub
    When the hub check runs
    Then no outcome is recorded for it

  Scenario: Hub-check outcomes are kept as evidence, paced like other records
    When hub checks run
    Then each outcome is stored where the ramp can count it over 30 days
    And the governance record gets a "hub_check_failed" event only for a fail, paced by the existing per-Tower budget, never one event per pass

  Scenario: A hub check carries no work and earns nothing
    When a hub check passes
    Then no Station is contacted, no grant is minted, no hold is placed and nobody is paid

  # --- computing the stage -----------------------------------------------------------

  Scenario: A newly approved Tower starts at "new"
    Given a Tower moves from quarantine to active for the first time
    When Core computes its stage
    Then the stage is "new"
    And a "trust_stage" event is recorded with from "" to "new" and actor "system"

  Scenario: Promotion is computed from the record, never from the Tower
    Given a Tower has been in "new" for 7 days with a clean record and 50 passed health checks at 96%
    When the governance sweep computes stages
    Then the stage becomes "proving"
    And a "trust_stage" event is recorded naming the evidence: days, health checks passed, pass rate

  Scenario Outline: A Tower that misses any promotion condition stays where it is
    Given a Tower in "new" with "<shortfall>"
    When the governance sweep computes stages
    Then the stage stays "new"

    Examples:
      | shortfall                                        |
      | 6 days and 23 hours in new                       |
      | 49 passed health checks                          |
      | a 94% health-check pass rate                     |
      | a suspension 3 days ago                          |
      | a lease lapse of 2 hours 5 days ago              |
      | a health-check fail streak of 2 yesterday        |
      | an admin-recorded incident 6 days ago            |

  Scenario: Proving to established
    Given a Tower has been in "proving" for 30 days, clean, at a 98.5% health-check pass rate
    When the governance sweep computes stages
    Then the stage becomes "established"

  Scenario: A stage rises at most one step per sweep
    Given a Tower in "new" whose record would satisfy "established"
    When the governance sweep computes stages
    Then the stage becomes "proving", and "established" needs its own 30 days from there

  Scenario Outline: Demotion is immediate and goes back to "new"
    Given a Tower in "<stage>"
    When "<event>" happens
    Then its stage becomes "new" in the same write that records the event
    And a "trust_stage" event is recorded naming the cause
    And Stations above the "new" caps become due to move with trigger "stage_cap"

    Examples:
      | stage       | event                                      |
      | proving     | Core suspends it                           |
      | established | Core suspends it                           |
      | established | its lease lapses for more than one hour    |
      | established | its health checks fail twice in a row      |
      | proving     | an admin records an incident against it    |

  Scenario: Revocation ends the stage with the Tower
    Given a Tower in any stage is revoked
    Then it has no stage and no Stations
    And a revoked Tower readmitted later starts at "new"

  # --- excusals and admin actions -----------------------------------------------------

  Scenario: An admin can excuse one event that was Core's fault
    Given a Tower's lease lapsed because of a Core-side defect
    When an admin records an excusal for that lapse with a reason
    Then the excusal is an attributed governance event naming the admin and the excused event
    And that lapse no longer counts against "clean" or as a demotion cause

  Scenario: An excusal cannot be blank or general
    When an admin records an excusal without naming one specific recorded event, or without a reason
    Then it is refused
    And nothing is excused

  Scenario: An admin can hold a Tower at a stage, but never raise it past the evidence
    When an admin sets a ceiling on a Tower's stage with a reason
    Then the Tower's stage is the lower of its computed stage and the ceiling
    And no admin action can set a stage higher than the computed one

  Scenario: An admin can record an incident
    When an admin records an incident against a Tower with a reason
    Then the Tower is demoted to "new" as any demotion is
    And the incident is an attributed governance event

  # --- what the stage controls ----------------------------------------------------------

  Scenario: A Tower at its community cap takes no new community Station
    Given a Tower in "new" holds 10 live community Stations
    When an 11th community Station is placed
    Then that Tower is not a candidate

  Scenario: A "new" Tower never holds a house Station
    Given only a "new" community Tower is eligible
    When a house Station is placed
    Then it is not placed there

  Scenario: House Stations reach a community Tower only as failover
    Given a "proving" community Tower and no eligible house Tower
    When a house Station is placed
    Then it may go to the community Tower, up to its house cap of 5
    And when a house Tower becomes eligible again, those Stations become due to move back with trigger "better_tower"

  Scenario: The edge weight scales the existing per-request coin
    Given a Station behind a "new" Tower is a candidate for a request alongside the direct path
    When the existing capacity-weighted coin decides which goes first
    Then the Tower side's weight is multiplied by 0.25
    And for "proving" by 0.5, and for "established" by 1.0
    And everything else in the merge (provider order, sort, tier gate, curated home first) runs unchanged and before the coin

  Scenario: The coin weight changes no money rule
    When a request goes through a Tower at any stage
    Then the Station, Tower and platform shares are exactly as today

  # --- house Towers --------------------------------------------------------------------

  Scenario: House Towers have no stage and no caps beyond capacity
    Given a house Tower
    When placement or the coin consults its stage
    Then it is treated as having no stage caps and an edge weight of 1.0
    And it still must be live, healthy and under capacity like any Tower
    And it gets no ranking advantage for community Stations (placement_ranking.feature)

  # --- who sees the stage -----------------------------------------------------------------

  Scenario: Admins see each Tower's stage and the evidence behind it
    When an admin opens a Tower's detail
    Then it shows the stage, when it was entered, the health checks passed and pass rate, the days counted, any excusal, ceiling or incident, and what the next stage still needs

  Scenario: The operator sees their own stage and what is left
    When a Tower's operator views their Tower
    Then they see its stage, the caps that stage carries, and what the next stage still needs, in plain words
    And they do not see other Towers' stages or the governance record itself (admin only, ruled 2026-11-13)

  # --- adversarial ---------------------------------------------------------------------------

  Scenario: Re-enrolling does not reset a bad record into a fresh start
    Given an owner's Tower was demoted or revoked
    When the same owner enrolls a new Tower
    Then the new Tower starts at "new" like any other
    And the owner's per-account Tower quota still applies

  Scenario: Many Towers under one owner do not multiply house failover
    Given one owner runs three "proving" Towers
    When house Stations fail over
    Then that owner's Towers hold at most 5 house Stations in total

  Scenario: Canaries a Tower can recognise do not inflate its pass rate
    Given a Tower that answers canaries well and paid work badly
    When a Tower's pass rate is computed
    Then only Core's own health-check outcomes count toward the pass rate
    And Core's own record of paid-attempt outcomes keeps feeding the health tier, as it does today
    And no number the Tower reports about itself is read
