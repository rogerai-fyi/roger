# ROGER EDGE - THE TROUBLE FEED: alerts reach the owner's phone, through the Roger app, privately.
#
# STATUS: APPROVED 2026-09-26 (founder: "approve, pull is fine, member-wide read") and LIVE. Not
# godog-bound: the scenarios are pinned one-for-one by Go tests against a real mTLS face -
# internal/edge/trouble_test.go (the feed, roger, stand by, refusals, cursor, test calls) and
# cmd/rogerai/edgetrouble_test.go (@cli `roger edge trouble`, --watch, the pinned dial).
#
# WHY. health.feature §5 delivers alerts to a configured sink. The founder's ruling (2026-09-26):
# for now the ONE sink is the Roger iOS app - its notification system, and an alert list on the
# Edge tab. The Edge is airgap-capable, so this cannot depend on a push service in a cloud: the
# phone is a MEMBER of the Edge, holding a certificate this authority issued, and it PULLS the
# standing trouble calls from the machine over the LAN, raises local notifications for what is
# new, and says ROGER back under its own certificate. Nothing leaves the machine unless a member
# asks for it; a non-member, or a member whose certificate was revoked, gets nothing.
#
# WHAT. A member-only path on the machine's face (the mTLS listener that already serves
# /edge/infer and /edge/invoke): GET /edge/trouble returns the standing calls (priority, age,
# unit, wording, value, why it matters, what to do, acknowledgement, stand by), the transitions
# since a cursor (so a phone can tell what is new since it last looked), the pile-up rate and the
# sign-off; POST /edge/trouble/roger acknowledges a call and POST /edge/trouble/standby quiets one
# for a bounded time, each attributed to the member that asked, by name.
#
# GROUND TRUTH: internal/edge/alertlog.go (Standing, Roger, StandBy, Report, the history),
# internal/edge/server.go + infer.go (the face, its caller authentication, the revocation check),
# internal/fleetview (the same words the TUI shows), cmd/rogerai/edgefleettabs.go (why/do).
#
# Tags: @edge runs in internal/edge against a real face over real mTLS; @cli in cmd/rogerai.

Feature: The owner's phone receives the Edge's trouble calls, privately, and can answer them

  Background:
    Given a local authority "shed" with a machine "desk" serving its face
    And a member phone "gentle-ibex-14" holding a certificate the authority issued
    And a rule that CPU temperature over 85 is a warning

  # =========================================================================
  # 1. WHO MAY READ THE FEED
  # =========================================================================

  @edge
  Scenario: a member reads the standing calls under its own certificate
    Given "desk" reports 95 degrees and the authority has judged it
    When the phone GETs /edge/trouble with its certificate
    Then it receives one standing call for "desk", "cpu_temp > 85", HI, with the value 95
    And the call carries why it matters and what to do, in the same words the TUI shows
    And the feed says when it was read and the pile-up rate

  @edge
  Scenario: a caller with no certificate is refused
    When a client GETs /edge/trouble with no client certificate
    Then it is refused with 401
    And nothing about the fleet is in the reply

  @edge
  Scenario: a revoked member is refused, even with the certificate it still holds
    Given the owner forgot the phone, revoking its certificate
    When the phone GETs /edge/trouble with that certificate
    Then it is refused
    And the refusal names no call

  @edge
  Scenario: a device that is not a member is refused
    Given a device with a certificate from a different authority
    When it GETs /edge/trouble
    Then it is refused

  # =========================================================================
  # 2. WHAT IS NEW SINCE THE PHONE LAST LOOKED
  # =========================================================================

  @edge
  Scenario: the feed tells the phone what changed since a cursor, so it can notify once
    Given the phone read the feed at 12:00 and kept the cursor it was given
    And "desk" cooled to 70 at 12:05 and ran hot again at 12:10
    When the phone GETs /edge/trouble since its cursor
    Then the transitions are the clear at 12:05 and the firing at 12:10, in that order
    And a new cursor is given
    And the standing calls are still complete, not only the new ones

  @edge
  Scenario: without a cursor the phone gets the standing calls and the last hour of transitions
    When the phone GETs /edge/trouble with no cursor
    Then it receives the standing calls
    And the transitions of the last hour, no more

  @edge
  Scenario: a call already acknowledged is marked so the phone does not alarm for it again
    Given the owner said roger to the call from the TUI
    When the phone reads the feed
    Then the call is standing, acknowledged by the owner, with when
    And the phone is told it is not new

  # =========================================================================
  # 3. ANSWERING FROM THE PHONE
  # =========================================================================

  @edge
  Scenario: the phone says roger and the acknowledgement names the phone
    When the phone POSTs /edge/trouble/roger for "cpu_temp" on "desk" with its certificate
    Then the call is acknowledged by "gentle-ibex-14"
    And the TUI's TROUBLE tab shows ROGER gentle-ibex-14
    And the console shows the same

  @edge
  Scenario: the phone puts a call on stand by for a bounded time
    When the phone POSTs /edge/trouble/standby for "cpu_temp" on "desk" for 2 hours
    Then the call is on stand by until two hours from now
    When it asks for 48 hours
    Then it is bounded to 8

  @edge
  Scenario: an answer for a call that is not standing is refused, never invented
    When the phone POSTs /edge/trouble/roger for "disk" on "desk"
    Then it is refused with 404
    And nothing is acknowledged

  @edge
  Scenario: an answer is never accepted from a non-member or a revoked certificate
    Given the owner forgot the phone, revoking its certificate
    When the phone POSTs /edge/trouble/roger with that certificate
    Then it is refused
    And the call is not acknowledged

  # =========================================================================
  # 4. PRIVACY AND HONESTY
  # =========================================================================

  @edge
  Scenario: the feed carries no secret and no address that is not the Edge's own
    When the phone reads the feed
    Then the reply holds no key, token, grant signature or e-mail address
    And the acknowledger is a handle or a node name, never a whole address

  @edge
  Scenario: nothing is pushed off the machine
    Given no member has asked for the feed
    When a rule fires
    Then no connection is opened by the machine to anything
    And the call waits in the log until a member asks

  @edge
  Scenario: the feed is honest about the machine's own clock
    When the phone reads the feed
    Then every time in it is unix seconds from this machine
    And the phone is expected to show ages relative to its own read time, not to trust the machine's wall clock

  # =========================================================================
  # 5. THE CLI
  # =========================================================================

  @cli
  Scenario: roger edge alert names the phone as the sink once it has read the feed
    Given the phone read the feed a minute ago
    When they run "roger edge alert"
    Then it names where alerts are being sent: the Roger app on gentle-ibex-14, last read a minute ago
    And it prints no secret

  @cli
  Scenario: roger edge alert test marks a test call for the phone
    Given the phone is a member
    When they run "roger edge alert test gentle-ibex-14"
    Then a clearly-marked TEST call appears in the feed once
    And it raises no real alarm and changes no rule state

  @cli
  Scenario: roger edge trouble reads a member's feed from a terminal, the way the phone does
    Given a member "nas" serving its face
    When they run "roger edge trouble nas"
    Then it reads the feed over nas's pinned face with this machine's certificate
    And it prints the standing calls with priority, age, unit, wording and value, each with why it matters and what to do
    And "--json" prints the feed as the phone receives it
    And a face that does not match its pin is refused before a byte is sent
