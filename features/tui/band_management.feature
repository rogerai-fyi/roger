# BAND MANAGEMENT IN THE TUI (BASE STATION [p]) - the surface half of private-band
# management. The broker/store half, and the MOVE semantics it rides on, live in
# features/sharing/band_management.feature (executable); this file is the operator's view
# of the same feature.
#
# THE INCIDENT (2026-08-07, eager-puma-54): the founder was refused a private band with
# "private band limit reached (free plan allows 1) - revoke an existing band first" while
# their one band sat on a model on ANOTHER MACHINE. The error named an action no surface
# could perform: BASE STATION rendered bands but m.rcCursor only ever indexed m.rcSessions,
# so a band row could not even be selected, and there was no revoke hook at all.
#
# THE CONTRACT:
#   - Every band an owner holds is visible, and says WHICH model and machine it is on.
#   - MOVE is offered before revoke, because moving keeps the frequency code alive and
#     revoking burns it. The irreversible action is never the easy one.
#   - A code is never re-shown. It was never stored, so offering to reveal it would be a
#     promise the system cannot keep; the remedy for a lost code is revoke + re-mint.
#
# GROUND TRUTH:
#   internal/tui/band_manage.go: bandCursorIndex maps the shared BASE STATION cursor onto
#     the bands list (sessions first, then bands); openBandManage / onBandManageKey /
#     onBandMoveKey / onBandRevokeConfirmKey drive modeBandManage, modeBandMove and
#     modeBandRevokeConfirm; moveBandTo builds the destination node id with
#     agent.ShareNodeID(ctrl.Station(), model, 0) - the SAME helper the share path
#     registers with, so a move can never bind a band to an id no node will claim.
#   internal/tui/rc.go: the PRIVATE BANDS block renders a cursor and bandWhere(bd), which
#     prints the node id VERBATIM - a station callsign is not always three words (the
#     founder's is the single word "roggentoo"), so any split would silently rename
#     someone's model in the one place they look to identify it.
#   internal/tui/tui.go: Hooks.BandRevoke / Hooks.BandMove, wired in cmd/rogerai/main.go
#     to client.RevokeBand / client.MoveBand.
#
# Out of scope (deliberately, and why):
#   - Buying band slots. No purchase path exists, so no surface here may imply one.
#   - Re-minting from BASE STATION. A re-mint needs the model to be on THIS machine (it is
#     revoke + go private again), and a band may well be parked on another box - so the
#     honest flow is revoke here, then `h` on the model where it should live.
#
# Enforced by: internal/tui/band_manage_test.go (cursor reach, selection, the manage card,
#   the revoke confirm, the move picker + node id, the revoked-band guard, the quota hint)
#   + internal/client/band_manage_test.go (RevokeBand/MoveBand + human error text)
#   + cmd/rogerai-broker/band_web_auth_test.go (the website's list). (Doc spec.)

Feature: Private band management in the TUI
  # ── seeing what you own ────────────────────────────────────────────────────

  Scenario: An owner can see every band they hold, and where it lives
    Given an owner holds a band
    When they open BASE STATION
    Then the band is listed with its masked display, its status, and the model it is on
    And a band whose node is on another machine says so, rather than appearing local

  Scenario: The list never carries the secret
    Then no listing returns the frequency code or its hash in any form
    And the masked display cannot be turned back into a working code

  Scenario: A band row can be selected, unlike today
    Given a band is listed
    Then the cursor can land on it
    And the keys it offers are the ones the surface actually honours

  # ── REVOKING ───────────────────────────────────────────────────────────────

  Scenario: Revoking a band states what it breaks before it happens
    When an owner asks to revoke a band
    Then they are told the code stops working and everyone tuned in is cut off
    And the revoke happens only on an explicit confirmation, never a single keypress

  Scenario: A revoked band frees the quota and burns its code forever
    Given an owner revokes their only band
    Then they may mint a new one
    And the revoked code never resolves again, even after the new band exists

  Scenario: Revoking never reaches another owner's band
    When a revoke is attempted against a band belonging to someone else
    Then nothing is revoked
    And the answer is the same as for a band that does not exist

  # ── RE-MINTING: the only cure for a lost code ──────────────────────────────

  Scenario: Re-minting is offered as replacement, never as recovery
    Given an owner has lost their frequency code
    Then the surface offers to re-mint, and states plainly that the old code dies
    And it never offers to show the old code again, because it was never stored

  Scenario: A re-minted band shows its new code once, like any mint
    When an owner re-mints
    Then the new code is shown exactly once
    And leaving that card clears it from the screen and from memory

  # ── honesty about the free limit ───────────────────────────────────────────

  Scenario: The free limit is stated with a remedy that exists today
    Then every surface stating the one-band limit offers moving or revoking
    And no surface promises purchasable band packs while none can be bought

  # ── the website ────────────────────────────────────────────────────────────

  Scenario: An owner's bands actually appear on the website
    Given an owner holds a band
    When they open their base station page signed in
    Then their band is listed
    And an authentication failure is shown as a failure, never as an empty list

