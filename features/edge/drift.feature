# ROGER EDGE - WHAT THE FLEET SHOULD LOOK LIKE, and where it does not.
#
# STATUS: PROPOSED 2026-09-28. Not godog-bound: pinned by Go tests - internal/edge/expect_test.go
# (the expectations and the judging) and cmd/rogerai/edgedrift_test.go (`roger edge expect`,
# `roger edge drift`, the readout row). Founder review pending.
#
# WHY. It is the half of a fleet manager Edge still lacked: SAYING what the fleet should look like,
# and being told where it does not. Every tool an operator has used works this way - a desired state
# and a drift report - and it is what turns a dozen machines into something you can reason about.
# The inventory already says which roger each unit runs and flags what is behind; this generalises
# that to the handful of things an owner actually cares to hold the fleet to.
#
# WHAT AN OWNER MAY EXPECT, and nothing else. Each is a fact the fleet ALREADY collects, from a
# describe over the node's own certificate, so an expectation needs no new data and no new path:
#   roger <version>   every unit runs at least that version of roger
#   serves <band>     every unit has that band on air, VERIFIED by the serving probe
#   agent             every unit runs a roger declared an agent
#   maintenance       every unit has maintenance on, so the owner can act on it from elsewhere
# Each is scoped like an alert rule: the whole fleet, one tag (the group), or one node. The most
# specific scope that applies to a unit is the one it is judged by, exactly as a rule is.
#
# WHAT THIS IS NOT, and this is the honest boundary. It does NOT make a machine converge. Nothing
# here writes another machine's configuration: the fleet reports drift, and the owner closes it -
# with `roger edge maintenance on` on that machine, `roger edge agent on`, an upgrade, or one of the
# maintenance actions. Making a member PULL the intended state and apply it to itself is the natural
# follow-on and it needs a distribution channel (a member-only read on the authority's face, polled
# on each pass); that is its own slice, and it is not pretended here.
#
# A UNIT THAT CANNOT BE JUDGED IS NOT DRIFT. A member that has never been dialled reports no
# software and no instances, and "unknown" is not "wrong": it is said as unknown, and an owner can
# tell the two apart. A DARK unit is judged on what it last reported, and the report says it is dark.
#
# GROUND TRUTH: internal/edge/expect.go, cmd/rogerai/edgedrift.go, internal/fleetview/fleet.go.

Feature: desired state and drift

  Background:
    Given an Edge with "desk" (roger 6.13.0, serving qwen, an agent, maintenance on)
    And "shed" (roger 6.9.0, serving nothing, not an agent, maintenance off)
    And both are tagged "production"

  # ---- the expectations themselves -------------------------------------------

  @edge
  Scenario: an owner says what the fleet should look like
    When the owner expects "roger 6.13.0" across the fleet
    Then it is kept, and every unit is judged by it
    When the owner expects "serves qwen" on "tag:production"
    Then only units tagged production are judged by it
    When the owner expects something of one node
    Then only that node is judged by it
    And one expectation per thing per scope: setting it again replaces it rather than adding a second

  @edge
  Scenario: the most specific scope wins, exactly as an alert rule's does
    Given the fleet expects "roger 6.13.0" and "tag:production" expects "roger 6.9.0"
    When shed, tagged production, is judged
    Then it is judged against 6.9.0, the group's, and is not in drift
    And a node-scoped expectation beats the group's

  @edge
  Scenario: an expectation that cannot mean what it says is refused where it is made
    When the owner expects an unknown thing
    Then it is refused, naming the four that exist
    When the owner expects "roger" with no version, or a version that is not one
    Then it is refused
    When the owner expects "serves" with no band
    Then it is refused
    And a scope that is neither fleet, a tag, nor a node is refused

  @edge
  Scenario: an expectation can be dropped
    When the owner removes it
    Then nothing is judged by it, and the surfaces stop mentioning it

  # ---- the judging -----------------------------------------------------------

  @edge
  Scenario: drift is what does not match, said in the owner's own terms
    When the fleet is judged
    Then shed is in drift: it runs 6.9.0 where 6.13.0 was expected, serves no qwen, is no agent,
      and has maintenance off
    And desk is in drift over nothing
    And each line says what was expected and what is there, not merely that something is wrong

  @edge
  Scenario: unknown is not drift
    Given a member that has never been dialled, so it reports no software and no instances
    When the fleet is judged
    Then it is UNKNOWN against every expectation that needs a fact it has not reported
    And it is not counted as drift, because nothing is known to be wrong
    And a DARK unit is judged on what it last reported, and the report says it is dark

  @edge
  Scenario: an Edge with no expectations is not an Edge in perfect order
    Given no expectations are set
    When the fleet is judged
    Then the surfaces say nothing is expected yet, rather than "no drift"

  # ---- the surfaces ----------------------------------------------------------

  @cli
  Scenario: `roger edge expect` and `roger edge drift`
    When the owner runs `roger edge expect`
    Then the expectations are listed with their scope, and how many units are in drift against each
    When the owner runs `roger edge expect roger 6.13.0 --scope tag:production`
    Then it says what is now expected of whom
    When the owner runs `roger edge drift`
    Then every unit in drift is listed, with what was expected and what is there
    And `--json` prints the same records
    And an Edge in order says so, naming how many units were checked

  @edge
  Scenario: the fleet screen says a unit is in drift
    When a unit is selected and it is in drift
    Then its readout carries a DRIFT row, one line per thing that does not match
    And a unit in order carries no such row at all
