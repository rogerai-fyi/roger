# ROGER EDGE - THE EVENTS TIMELINE: what happened on this Edge, in one place, newest first.
#
# STATUS: PROPOSED 2026-09-27. Not godog-bound: pinned by Go tests - internal/edge/events_test.go
# (the merge, the ordering, the cap), cmd/rogerai/edgeevents_test.go (`roger edge events`),
# internal/fleetview/fleet_test.go (the EVENTS tab). Founder review pending.
#
# WHY. Every fleet manager has an activity view: an operator who finds a unit in a strange state asks
# "what happened to it, and when". Roger Edge recorded plenty - enrolled, seen, went dark, renamed,
# re-pinned, probed, tagged, capabilities changed, actuate confirmed, maintenance done - and an
# alert log recorded every crossing, but the two lived apart and neither could be READ across the
# fleet. `roger edge describe <node>` showed the last five lines of one node and that was all.
#
# WHAT. One timeline, merged from the two places the Edge already writes: each node's own history,
# and the alert log's crossings. Newest first, each line saying when, which unit, what, and the
# detail. `roger edge events [--node <node>] [--since <duration>] [--limit <n>] [--json]`, and an
# EVENTS tab on the fleet screen. Nothing new is recorded for this: it is a READ.
#
# AND A CAP. Fourteen places append to a node's history and nothing ever trimmed it, so a node on a
# flaky network grew its record without bound - and that record is re-read on every CLI call. The
# history is capped at the newest 200 events per node, trimmed where it is written.
#
# GROUND TRUTH: internal/edge/events.go, internal/edge/fleet.go (mutate trims), internal/edge/
# alertlog.go (Recent), cmd/rogerai/edgeevents.go, internal/fleetview/fleet.go.

Feature: the events timeline

  Background:
    Given an Edge with the members "desk" and "shed"

  @edge
  Scenario: the timeline merges the node histories and the alert crossings
    Given desk was enrolled, then renamed, then went dark
    And an alert raised cpu_temp on desk and later cleared
    When the timeline is read
    Then it holds all five, newest first, each naming its unit
    And an alert line reads as the wording the other surfaces use, with the reading and the threshold
    And a node event reads as its own words and detail

  @edge
  Scenario: events that happened at the same second keep a stable order
    Given two events on one unit at the same second
    When the timeline is read twice
    Then the order is the same both times

  @edge
  Scenario: the timeline can be narrowed
    When the timeline is read for one node
    Then only that unit's events are in it, alert crossings included
    When it is read since a duration
    Then nothing older than that is in it
    When a limit is given
    Then at most that many are returned, and they are the newest
    And a limit of zero means the default, not nothing

  @edge
  Scenario: an Edge with no history says so rather than drawing an empty list
    Given a fleet whose nodes carry no events and an alert log with nothing in it
    When the timeline is read
    Then it is empty, and the surfaces say nothing has happened yet

  @edge
  Scenario: an unreadable alert log does not lose the node events
    Given the alert log cannot be read
    When the timeline is read
    Then the node events are still there, and the surfaces say the alert history could not be read
    And the other way round: an unreadable fleet is an error, never a quiet empty timeline

  # ---- the cap --------------------------------------------------------------

  @edge
  Scenario: a node's history is capped at the newest 200 events
    Given a node with 200 events
    When five more are recorded
    Then it holds 200, the newest five among them, and the oldest five are gone
    And the cap is applied wherever history is written, not only in one path

  @cli
  Scenario: `roger edge events`
    When the owner runs `roger edge events`
    Then the newest events are listed with when, unit, what and detail
    And `--node shed` narrows it to that unit
    And `--since 2h` narrows it by age, and an unparseable duration is refused
    And `--json` prints the same records
    And an Edge with nothing recorded says so

  @edge
  Scenario: the fleet screen's EVENTS tab
    When the owner opens the EVENTS tab
    Then the newest events are listed, each with its age, unit and words
    And an alert crossing is drawn in the alarm colour, a node event quietly
    And an empty timeline says nothing has happened on this Edge yet
