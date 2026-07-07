# GUEST OPERATORS — Phase 2: the remote-control interlock (iOS parity review, REQUIRED).
#
# THE HAZARD: tea.ExecProcess suspends the TUI event loop, but the RCBridge poll loop
# (internal/client/rc.go:329) keeps pumping inbound messages into the program's channel.
# Without an interlock, a viewer's turns during a handoff (a) go silently deaf, then
# (b) REPLAY AS A BURST into the DJ the moment the exec callback lands — remote turns the
# sender believes were dropped suddenly fire minutes later. Both are unacceptable on a
# money path (a replayed turn bills).
#
# THE CHOSEN BEHAVIOR (recommended: PARK + DROP with a status frame; founder confirm
# requested — the alternative "queue and replay on return" is explicitly rejected as the
# blind-replay hazard):
#   - the interlock lives at the BRIDGE level (the bridge goroutines run through the
#     suspend; the TUI's Update loop does not): parked BEFORE the exec cmd is issued,
#     unparked in the return callback
#   - while parked: inbound "turn" messages are DROPPED at the bridge and answered with a
#     status frame ("guest has the mic") so the sender knows immediately; nothing is
#     queued, nothing replays on return
#   - while parked: inbound "confirm" answers are dropped (no DJ confirm can be pending —
#     the DJ loop is idle by the handoff preconditions)
#   - while parked: backfill requests are still answered (transcript snapshot + a status
#     frame) so a viewer attaching mid-handoff sees a live, honest session, not a blank
#     stream
#   - handoff start and end each emit ONE status frame (kind "status") carrying operator
#     metadata, additively — existing viewers render or ignore it (unknown fields are
#     ignored by the wire contract)
#
# RESERVED NAMES (constants documented in internal/protocol/rc.go, NO behavior in v1):
#   frame kind    "operator_status"   — future dedicated operator-state frame
#   inbound kind  "operator_handoff"  — future remote-initiated handoff
#   inbound kind  "operator_recall"   — future remote "give the DJ the mic back"
# Reserving the strings NOW keeps old hosts and new surfaces from colliding on the names
# later (persistent-state lesson: additive, idempotent wire evolution).

Feature: While a guest has the mic, remote control parks — never deaf, never a replay burst
  A live BASE STATION session stays honest through a handoff: viewers are told the
  guest has the mic, their turns are parked-and-dropped with a status frame instead
  of vanishing or replaying, and the desk announces the handoff start and end.

  Background:
    Given a HOST agent session with an attached remote-control bridge
    And a tuned band and a detected guest "opencode"

  Scenario: Handoff start emits a status frame with operator metadata
    When the handoff to "opencode" begins
    Then a frame of kind "status" is emitted before the exec
    And the frame names the operator "opencode"

  Scenario: The bridge is parked before the exec command is issued
    When the handoff to "opencode" begins
    Then the bridge is parked
    And parking happened before the exec command was returned

  Scenario: A remote turn during the handoff is dropped with a status frame
    Given the guest has the mic
    When a viewer sends a turn "refactor the parser"
    Then the turn is not queued for the DJ
    And the viewers receive a status frame saying the guest has the mic
    And no agent turn ever fires for "refactor the parser"

  Scenario: Parked turns do NOT burst-replay when the guest returns
    Given the guest has the mic
    And viewers sent 3 turns during the handoff
    When the guest returns and the bridge unparks
    Then no queued turn fires
    And the DJ receives zero injected turns from the parked window

  Scenario: A remote confirm answer during the handoff is dropped
    # No DJ confirm can be pending (handoff preconditions), so any confirm arriving
    # parked is stale by definition — dropping it mirrors the stale-confirm guard
    # (internal/tui/rc.go:191).
    Given the guest has the mic
    When a viewer sends a confirm approval
    Then it is dropped
    And no tool runs

  Scenario: A viewer attaching mid-handoff gets a status frame, not a blank stream
    Given the guest has the mic
    When a new viewer attaches and requests backfill
    Then the viewer receives the transcript snapshot
    And the viewer receives a status frame saying the guest has the mic

  Scenario: Handoff end emits a status frame and normal service resumes
    Given the guest has the mic
    When the guest returns and the bridge unparks
    Then a frame of kind "status" is emitted announcing the DJ is back
    And a subsequent viewer turn injects into the DJ exactly like local typing

  Scenario: Handoff with NO bridge attached emits nothing and parks nothing
    Given the remote-control bridge is not enabled
    When the handoff to "opencode" begins
    Then no frame is emitted
    And the handoff proceeds normally

  Scenario: The bridge ending mid-handoff leaves the return path safe
    # A revoke-all can 401 the poll while the guest has the mic (onRemoteHostEnd,
    # internal/tui/rc.go:155). Unparking a dead bridge must be a no-op, not a panic.
    Given the guest has the mic
    And the remote session is revoked during the handoff
    When the guest returns
    Then the unpark is a no-op
    And the desk summary still renders

  Scenario: The reserved operator wire names are pinned constants
    Then the protocol reserves frame kind "operator_status"
    And the protocol reserves inbound kind "operator_handoff"
    And the protocol reserves inbound kind "operator_recall"
    And v1 attaches no behavior to any of them

  Scenario: Frames emitted around the handoff are content-blind about the guest's work
    # The guest's terminal I/O NEVER tees to viewers (the broker relay stays content-blind
    # and the guest session is not the DJ loop). Only the status transitions are visible.
    When the handoff to "opencode" begins and the guest works for a while
    Then no frame carries any guest terminal output
