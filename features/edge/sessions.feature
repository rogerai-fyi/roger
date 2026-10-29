# ROGER EDGE - SESSIONS ("a node is a thing; a session is a thing happening").
#
# STATUS: APPROVED 2026-09-12. IMPLEMENTED 2026-09-13 - all 27 scenarios green
# (internal/tui/edge_sessions_bdd_test.go). Not one scenario was changed to get there.
#
# WHY THIS EXISTS
#
# Founder direction 2026-09-12: `roger use` and the TUI agent are themselves connections, and
# the Edge should show them, because the Playbox is the picture of how the factory and the
# models are meant to work in unison.
#
# The fleet so far has only nouns. Machines and boards persist, and the topology draws them.
# What an owner actually wants to see is WHO IS TALKING TO WHOM RIGHT NOW: the agent working on
# the tuned band, the guest operator someone handed the mic to, and the bench sensor that could
# not name what it was seeing and escalated. Those are the same shape, and this file says so.
#
# THE INSIGHT THAT MAKES ONE VIEW POSSIBLE
#
# `roger use`, the TUI agent, a guest operator, the web console and a board escalating are NOT
# five mechanisms. Each is a participant opening a session against a band that some station
# serves. They differ in who initiated and what authority they carried, never in shape. One
# receipt already covers them all, so one view can too.
#
# WHAT THIS FILE DOES NOT DO
#
# It creates no new authority and no new way to cause traffic. A session is authorized exactly
# as traffic already is: a consumer session spends from a wallet under the existing limits, and
# a device action carries a grant scoped to nodes and actions (features/edge/control.feature,
# not yet written). THE EDGE VIEW IS A WINDOW ONTO AUTHORIZED TRAFFIC, NEVER A NEW SOURCE OF IT.
# Anything drawn here was already receipted. A scenario below pins that.
#
# THE APPROVED FRAMING THIS OBEYS (features/web/playbox_edge_honesty.feature, and the models
# agent's 2026-08-01 answer):
#   - Wave models are CONTRACT models: the device prompt is part of the device, model and prompt
#     ship as one unit.
#   - ESCALATE is the models' strongest measured skill and renders as a GOOD outcome, never as a
#     warning state. This file inherits that: an escalation is drawn in the positive style.
#   - Response routing happens AFTER the finding and never rewrites it
#     (features/web/playbox_mesh_workbench.feature).
#
# GROUND TRUTH: cmd/rogerai-broker/tunnel.go (relay, failover and the receipts every session
# already writes), cmd/rogerai/edge.go and internal/tui/edge.go (the surfaces), internal/edge
# (the fleet these sessions run between), features/edge/topology_view.feature (the graph this
# layers onto), cmd/rogerai-broker/edgebridge.go (the Tower sealed-hub path a session may take).

Feature: The Edge shows live sessions, whoever opened them, and never invents traffic it did not carry

  Background:
    Given an Edge with this machine and at least one serving station

  # =========================================================================
  # 1. A SESSION IS TRAFFIC, NOT A CAPABILITY
  # =========================================================================

  Scenario: an idle Edge shows no sessions at all
    Given no traffic is flowing
    When the Edge is viewed
    Then no session is drawn
    And the graph is still, because a moving graph must mean traffic

  Scenario: a session appears only once traffic really flowed
    Given a station that could serve this band but has not been asked
    Then no session is drawn to it
    And a link that merely could exist is never drawn as one

  Scenario: a session ends and fades rather than accumulating
    Given a completed session
    When time passes beyond the session's visible life
    Then it is no longer drawn
    And the fleet's nodes are unchanged by its passing

  Scenario: sessions never become nodes
    Given many sessions between the same two participants
    Then the graph still holds exactly those two participants
    And the session count is shown on the edge between them, not as new boxes

  # =========================================================================
  # 2. EVERY INITIATOR IS THE SAME SHAPE
  # =========================================================================

  Scenario Outline: each kind of consumer opens a session of the same shape
    Given <initiator>
    When it opens a turn against a band
    Then a session is drawn from this node to the serving station
    And it is attributed to "<attribution>"
    And its shape is identical to every other row in this table

    Examples:
      | initiator                          | attribution     |
      | a `roger use` endpoint             | roger use       |
      | the TUI's own agent                | agent           |
      | a guest operator holding the mic   | the guest's name |
      | the web console                    | console         |
      | a board that escalated a reading   | the board's name |

  Scenario: the band is named on the session, because that is what the owner is watching
    When any session is opened against a band
    Then the session names the band it is asking

  Scenario: a session names the station that actually served it
    When a session completes
    Then it names the station that served
    And that is the station its receipt names

  Scenario: a failed-over session shows both the station it left and the one that served
    Given a session whose first station returned a no-output failure
    When it is served by a sibling
    Then the session shows the station it left and the station that served
    And both receipts exist, exactly as the failover already writes them

  # =========================================================================
  # 3. THE ESCALATION CHAIN - the Playbox ladder, made real
  # =========================================================================

  Scenario: a board that cannot name what it sees escalates, and the escalation is drawn
    Given a node declaring "classify" with its contract
    When it meets a reading it cannot name
    Then it escalates to a band
    And the escalation is drawn as a session from the board, travelling to the station

  Scenario: an escalation renders as a GOOD outcome, never as a fault
    When an escalation is drawn
    Then it renders in the positive style
    And it never renders in the warning style
    And its label reads as the right call

  Scenario: the device's contract travels with the escalation
    Given a classifying node whose contract carries a fixed framing
    When it escalates
    Then the framing is part of what was sent, because model and prompt ship as one unit
    And the framing shown is the device's real production framing, not a paraphrase

  Scenario: routing a finding never rewrites the finding
    Given a completed escalation with a model answer
    When the owner changes where findings are routed
    Then the model answer is unchanged
    And only the destination of the finding changes

  Scenario: a classifier that answers locally opens no session
    Given a classifying node that names the reading itself
    Then no escalation is drawn
    And nothing was asked of any band

  Scenario: an escalation that no station can serve is drawn as it truly ended
    Given every station for the band is cooling or absent
    When a board escalates
    Then the session is drawn as refused, with the reason
    And it is not drawn as though a model answered

  # =========================================================================
  # 4. AUTHORITY IS UNCHANGED
  # =========================================================================

  Scenario: everything drawn was already receipted
    Given any session drawn on the Edge
    Then a receipt exists for it
    And the Edge view created no traffic of its own

  Scenario: the view cannot start a session
    When the owner looks at the Edge
    Then nothing is dispatched
    And no wallet is touched

  Scenario: a session that spends is bound by the existing limits
    Given a consumer session against a paid band
    Then it is subject to the same spend limits as any relay
    And exceeding them refuses the session, exactly as it does today

  Scenario: a device action carries its grant, and an unauthorized one is never drawn as success
    Given a device action with no valid grant
    Then it is refused
    And the Edge draws the refusal, not an action

  Scenario: a session of another account is never visible
    Given traffic belonging to a different account
    Then it does not appear on this Edge
    And nothing about it is inferable from what is drawn

  # =========================================================================
  # 5. THE VIEW HOLDS UP
  # =========================================================================

  Scenario: many concurrent sessions stay legible
    Given 50 sessions in flight across 6 participants
    When the Edge is viewed at 100 columns
    Then no line exceeds the width
    And the busiest edge shows a count rather than 50 overlapping marks

  Scenario: a session on a relayed path travels through the relay, in order
    Given a session to a station reachable only through a relay
    Then it is drawn travelling participant to relay to station
    And the relay is drawn as its own node, so the hop is visible

  Scenario: sessions are legible with no colour at all
    Given NO_COLOR is set
    Then a session, an escalation and a refusal remain distinguishable by glyph alone

  Scenario: the session layer costs the relay nothing
    Given sessions are being drawn
    When a consumer relays a request
    Then the relay completes at its normal speed
    And nothing about the relay waited on the view
