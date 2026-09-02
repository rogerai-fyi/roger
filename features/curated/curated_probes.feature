# CURATED PROBE ECONOMICS - the network's own verification must not drain the operator.
#
# Founder, 2026-09-01, on the first live house stations: "we do not want to really spend
# this ... or the probe will take all the api credit and not pay anything." A curated
# station fronts a METERED commercial API: every canary the broker sends is billed to
# the operator's upstream account and pays them nothing. On the standard adaptive lane
# (30s floor, 15m ceiling, 384-token canary budget) a single expensive band could burn
# half a dollar a day on verification alone. A human GPU pays for probes in idle watts;
# a curated operator pays in cash - so curated verification rides its own SLOW LANE,
# and the overhead is disclosed where the operator signs up.

Feature: Curated stations are verified on a slow lane the operator can afford
  As a curated operator fronting a metered commercial API
  I want verification traffic bounded and disclosed
  So that the network's own canaries can never eat my upstream credit.

  Scenario: A curated station is probed once, then only a minimal weekly recheck
    Given a curated station and a human station on the air
    Then the human station follows the adaptive probe schedule
    And the curated station is not due again before the curated probe interval
    # founder ruling (second pass, same day, on a dollar-a-day of live burn): "probe
    # once and that's it ... something minimal". Default recheck: every 7 DAYS
    # (ROGERAI_PROBE_CURATED_INTERVAL; 0 = the first probe is the only one, ever).
    # A dead upstream key surfaces on the first real request via ordinary failover.

  Scenario: Browsing the market cannot pull a curated probe in early
    Given a curated station probed moments ago
    When consumers browse the market repeatedly
    Then no earlier probe becomes due for the curated station
    # demand-driven refresh is the right call for a GPU (stale speed misroutes); for a
    # curated relay the reading barely moves and every early canary is the operator's cash

  Scenario: The probe overhead is disclosed where a curated operator signs up
    Then the curated share help says one sign-up canary plus a weekly recheck is billed to the upstream
    # "make it known that so much will be used to validate the connection over a period"
