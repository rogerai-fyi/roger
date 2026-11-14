# APPROVED SPEC - founder approved 2026-11-13 ("Approve as written"), with one ruling:
# operators do NOT see the record yet ("Admin only for now"), so the operator-view scenario
# was removed before approval took effect. Next slice (also ruled 2026-11-13): the signed,
# hash-chained TowerLifecycleEventV1 from public_enrollment.feature. Changes to an approved
# scenario need re-approval.
# BUILD STATUS: BUILT. internal/towercore/admit/events.go (the record), cmd/rogerai-broker/towergov.go
# (what writes it), tested in admit/events_test.go and cmd/rogerai-broker/towergov*_test.go.
# The scenarios in the section "added 2026-11-13 from the pre-push audit" were confirmed by the
# founder on 2026-11-13 ("Confirm all"). Two of them refine approved scenarios: refusals anyone could cause get actor "unauthenticated", and a
# sweep writes one station_retired per Tower (naming up to 20) rather than one per Station.
# Founder request: "we need to make sure we have proper governance over parts of this at
# least for log/audit purposes ... find what we are missing and close the gap."
#
# Why: the first outside Tower was approved and Core could not say whether it ever
# connected. Opening, heartbeating and closing a session leave no log line, a refused Tower
# request is a silent 403, administrator actions are logged as "by an administrator" with
# no name, and the only durable trace of a session is a row that is deleted on an orderly
# close. The evidence behind an automatic suspension is reaped within a day.
#
# This file covers THE RECORD: what Core writes down about each Tower, who did it, and
# what a log reader sees. The signed, hash-chained TowerLifecycleEventV1 in
# public_enrollment.feature is the integrity layer on top of it and is a separate slice.

Feature: Every governance event about a Tower leaves an attributed, durable record
  The Tower governance record is an append-only list of events per Tower, kept in the
  shared store so every broker instance writes to and reads from the same history. Each
  event has a Tower id, a Core timestamp, a kind, an actor, the prior and new state where
  a state changed, a reason where one exists, the broker instance that saw it, and a small
  detail object. Each event also prints one log line in a single greppable format:

    tower-event kind=<kind> tower=<id> actor=<actor> [from=<state> to=<state>] [reason=<text>] [key=value ...]

  Actors are exactly one of: admin:gh:<login>, admin:broker-key, operator:<owner>,
  system (Core decided on its own), tower (the Tower's own signed request).

  Background:
    Given a broker with joined-Tower admission on and a shared store
    And a Tower "tw-1" owned by "acct-1" that has enrolled into quarantine

  # --- the record itself ---------------------------------------------------------

  Scenario: The record is append-only
    Given "tw-1" has events in its governance record
    Then no code path updates or deletes a governance event
    And no sweep or reaper removes governance events
    And a new event never changes the time, kind or actor of an earlier one

  Scenario: Every instance writes to the same history
    Given two broker instances "web.1" and "web.2"
    When "tw-1" opens a session on "web.1" and heartbeats on "web.2"
    Then both instances read the same governance record for "tw-1"
    And each event names the instance that wrote it

  Scenario: The record never holds a secret
    When any governance event is written or logged
    Then it contains no private key, signature, enrollment token, challenge nonce, certificate body, session cookie, admin key or request body
    And a public key appears only as its hash prefix

  Scenario Outline: Free-text reasons are bounded and cleaned
    Given an administrator gives the reason "<given>"
    When the event is written
    Then the stored reason is "<stored>"

    Examples:
      | given                              | stored                          |
      | approved after email with operator | approved after email with operator |
      | line one\nline two                 | line one line two               |
      | 300 characters of text             | the first 280 characters        |
      | (empty)                            | (none)                          |
      | tab\tand\x1bescape                 | tab and escape                  |

  Scenario: A log line cannot be forged through a field
    Given a reason or detail value contains "kind=" or a newline
    Then the log line quotes the value so a reader cannot mistake it for another field or another line

  # --- the link: was the Tower ever here? ----------------------------------------

  Scenario: Opening a session is recorded
    When "tw-1" opens a session
    Then a "session_opened" event is recorded with actor tower, the session id prefix, the Tower's protocol version and whether a full inventory was requested
    And the log shows "tower-event kind=session_opened tower=tw-1"

  Scenario: The first session ever is marked as the first
    Given "tw-1" has never opened a session
    When "tw-1" opens a session
    Then the "session_opened" event is marked first=true
    And the admin view shows the first-connected time

  Scenario: Reconnecting replaces the old session and says so
    Given "tw-1" has a live session "s-1"
    When "tw-1" opens a new session "s-2"
    Then a "session_opened" event names "s-2" and that it replaced "s-1"

  Scenario: Heartbeats are not recorded one by one
    Given "tw-1" has a live session
    When it heartbeats 100 times inside the freshness window
    Then no governance event is written for those heartbeats
    And the admin view still shows the last-seen time from the newest heartbeat

  Scenario: An orderly close is recorded and the last-seen time survives it
    Given "tw-1" has a live session
    When "tw-1" closes its session
    Then a "session_closed" event is recorded with actor tower and the session's duration
    And the admin view still shows when "tw-1" was last seen

  Scenario: A Tower that goes quiet is recorded once
    Given "tw-1" has a live session
    When no heartbeat arrives for longer than the freshness window
    Then exactly one "session_stale" event is recorded, with the last-seen time
    And it is recorded once across all broker instances, not once per instance

  Scenario: A quiet Tower coming back is recorded
    Given "tw-1" has a "session_stale" event as its latest link event
    When "tw-1" heartbeats again or opens a new session
    Then a "session_opened" or "session_resumed" event is recorded with how long it was gone

  Scenario: A stale session that is already closed is not reported stale
    Given "tw-1" closed its session in an orderly way
    When the freshness window passes
    Then no "session_stale" event is recorded

  Scenario: A refused session negotiation is recorded
    When "tw-1" opens a session with a protocol version Core does not speak
    Then a "session_refused" event is recorded with the reason
    And the Tower still receives the same 400 answer as today

  # --- refused Tower requests: tried and failed is not the same as never tried ---

  Scenario Outline: A refused Tower request is logged with the reason
    Given a request names "tw-1" and is refused because <cause>
    When it reaches any Tower route
    Then the Tower still receives the same 403 answer as today, with no reason disclosed
    And the log shows "tower-event kind=request_refused tower=tw-1 reason=<reason>"
    And a "request_refused" event is recorded for "tw-1" with that reason

    Examples:
      | cause                                          | reason            |
      | the request is not signed                      | unsigned          |
      | the signing key does not match the admitted key | key_mismatch      |
      | the Tower is suspended                          | state_suspended   |
      | the Tower is revoked                            | state_revoked     |
      | the Tower's lease has lapsed                    | lease_lapsed      |
      | the Tower's certificate serial is revoked       | cert_revoked      |
      | the TLS client certificate is not the Tower's   | mtls_mismatch     |

  Scenario: Repeated refusals do not flood the log or the record
    Given "tw-1" is suspended and keeps retrying every 5 seconds
    When an hour passes
    Then at most one "request_refused" event per reason is recorded for "tw-1" in that hour
    And the event carries the count of refusals it stands for
    And at most one log line per reason per minute is printed for "tw-1"

  Scenario: A request naming an unknown Tower is logged but not recorded
    Given a request names "tw-nope", which Core never admitted
    When it is refused
    Then the log shows "tower-event kind=request_refused tower=tw-nope reason=unknown_tower" at most once per minute in total
    And no governance record is created for "tw-nope"

  # --- administrator actions: who did it and why ---------------------------------

  Scenario Outline: An administrator's lifecycle change names the administrator
    Given an administrator signed in as <how>
    When they move "tw-1" from quarantine to active with the reason "checked with operator"
    Then a "lifecycle" event is recorded with actor "<actor>", from quarantine, to active and that reason
    And the log names "<actor>" instead of "an administrator"

    Examples:
      | how                               | actor              |
      | the super-admin GitHub session    | admin:gh:<login>   |
      | the broker admin key header       | admin:broker-key   |

  Scenario: A reason is optional for an administrator lifecycle change
    When an administrator moves "tw-1" to active without a reason
    Then the change goes through
    And the event records no reason

  Scenario: An administrator lifecycle change and its record commit together
    When an administrator moves "tw-1" to active
    And the governance record cannot be written
    Then the state does not change
    And the administrator receives a server error saying the change was not made
    And a retry after the store recovers succeeds once

  Scenario Outline: A refused administrator change is recorded too
    When an administrator asks to move "tw-1" <request>
    Then the answer is the same as today (<status>)
    And a "lifecycle_refused" event is recorded with the actor, the current state, the requested state and the reason "<reason>"

    Examples:
      | request                               | status | reason             |
      | to the state it is already in          | 409    | already_in_state   |
      | along an edge the table does not allow | 409    | illegal_transition |

  Scenario: A request for an unknown Tower or an unknown state is logged only
    When an administrator asks to move "tw-nope" or asks for the state "bogus"
    Then the log shows the refusal with the actor
    And no governance record is created

  Scenario Outline: Other administrator actions name the administrator
    When an administrator <action> on "tw-1"
    Then a "<kind>" event is recorded with the administrator as actor and the optional reason
    And it commits together with the action, or the action does not happen

    Examples:
      | action                              | kind              |
      | revokes the certificate              | cert_revoked      |
      | expires the lease                    | lease_expired     |
      | promotes a Station out of quarantine | station_promoted  |

  Scenario: Administrator reads of Tower details are logged
    When an administrator opens the Towers list or one Tower's detail
    Then the log shows the actor and which Tower was read
    And no governance event is recorded for a read

  # --- operator actions ------------------------------------------------------------

  Scenario Outline: An operator's own lifecycle change is recorded
    Given "tw-1" is <from>
    When its operator "acct-1" moves it to <to>
    Then a "lifecycle" event is recorded with actor "operator:acct-1", from <from>, to <to>
    And it commits together with the change, or the change does not happen

    Examples:
      | from     | to       |
      | active   | draining |
      | draining | active   |
      | active   | revoked  |

  Scenario: An operator's refused change is recorded
    When operator "acct-1" asks to move "tw-1" from quarantine to active
    Then the answer is the same as today
    And a "lifecycle_refused" event is recorded with actor "operator:acct-1"

  Scenario: An operator revoking a Station is recorded
    When operator "acct-1" revokes Station "st-1" attached to "tw-1"
    Then a "station_revoked" event is recorded with actor "operator:acct-1" and the Station id

  # --- what Core decides on its own --------------------------------------------------

  Scenario: An automatic suspension keeps its evidence
    Given "tw-1" is active
    When its reputation evidence warrants suspension
    Then a "lifecycle" event is recorded with actor system, from active, to suspended and reason "reputation"
    And the event's detail holds the Tower's tally and the fleet baseline it was judged against
    And that detail is still readable after the outcome rows it was built from are reaped

  Scenario: A Tower already off is not reported again and again
    Given "tw-1" is already suspended
    When more bad outcomes arrive for it
    Then no new suspension event is recorded
    And the log does not repeat "evidence warrants suspension but the move failed"

  Scenario: A failed automatic suspension is recorded
    Given "tw-1" is active and its evidence warrants suspension
    When the move fails for a reason other than the Tower already being off
    Then a "lifecycle_refused" event is recorded with actor system and the error class

  Scenario: A lapsed lease is noticed and recorded once
    Given "tw-1" has a lease that ends at a set time
    When that time passes without a renewal
    Then exactly one "lease_lapsed" event is recorded for "tw-1" across all instances
    And its admission state is not changed by this slice

  # --- enrollment, certificates and placement ------------------------------------------

  Scenario: Enrollment is always logged and recorded
    When "acct-1" enrolls a new Tower
    Then an "enrolled" event is recorded with actor "operator:acct-1", to quarantine
    And the log shows it whether or not an admin email address is configured

  Scenario Outline: A refused enrollment is logged with its reason class
    When an enrollment is refused because <cause>
    Then the log shows "tower-event kind=enroll_refused" with the reason class and the owner if known
    And the token and the nonce are never printed

    Examples:
      | cause                                 |
      | the token is unknown, spent or expired |
      | the owner may not enroll               |
      | the key is already admitted            |
      | the store is unavailable               |

  Scenario: A certificate renewal is recorded
    When "tw-1" renews its certificate
    Then a "cert_renewed" event is recorded with the old and new serial and the new lease end
    And a refused renewal is logged with its reason class

  Scenario: A node attaching to a Tower is recorded
    When Station "st-1" attaches to "tw-1"
    Then a "station_attached" event is recorded with the Station id and whether it attached in quarantine
    And a refused attach is logged with its reason class

  Scenario: Sweeps that retire or reap Stations name them
    When the dormant or terminal sweep removes Stations from "tw-1"
    Then a "station_retired" event is recorded per Station with actor system
    And the log names the Station ids, not only a count

  # --- probes ---------------------------------------------------------------------------

  Scenario: Probe results are recorded when they change, not every time
    Given "tw-1" has never been probed
    When its first canary passes
    Then a "canary" event is recorded with result pass
    When the next 50 canaries pass
    Then no further canary events are recorded
    When a canary fails
    Then a "canary" event is recorded with result fail, the Station and the reason
    When a canary passes again
    Then a "canary" event is recorded with result pass

  Scenario: Every canary failure is logged with the Station and the reason
    When a canary probe of "tw-1" fails
    Then the log names the Tower, the Station and the reason

  Scenario: A Tower skipped by design is recorded once
    Given "tw-1" advertises no reachable data plane
    When the canary sweep skips it
    Then one "canary" event is recorded with result unreachable_by_design
    And it is not recorded again until the result changes

  # --- reading the record -----------------------------------------------------------------

  Scenario: The admin Tower detail shows the link history
    When an administrator opens "tw-1"'s detail
    Then it shows first connected, last connected, last seen, and the latest close or stale time
    And it lists the newest 50 governance events, newest first, with actor and reason

  Scenario: The Towers list shows whether each Tower is connected
    When an administrator opens the Towers list
    Then each row shows connected now, last seen, or never connected

  Scenario: The record answers "did it ever connect" with no log access
    Given "tw-1" was approved and never opened a session
    When an administrator opens "tw-1"'s detail
    Then it says "never connected" and when it was approved and by whom

  # --- added 2026-11-13 from the pre-push audit; founder confirmed 2026-11-13 --------------

  Scenario: A close that ended nothing is not recorded as a close
    Given "tw-1" has replaced session "s-1" with a live session "s-2"
    When "tw-1" closes "s-1"
    Then no "session_closed" event is recorded, and the line is logged as not the current session
    And "s-2" can still be recorded as gone quiet

  Scenario: One Tower cannot grow its record without bound
    Given an event kind a Tower or operator drives (session opened, closed, resumed or refused, a refused lifecycle request, a Station attached or revoked, a certificate renewed)
    When one actor produces more than 20 of that kind on one Tower within an hour, across all instances
    Then the rest are logged and not recorded, and the log says the budget was reached once a minute
    And one account's events never use up another account's room
    And events Core or an administrator decides are never capped
    But canary result changes are capped per Tower whoever records them, because the Tower decides the result

  Scenario: An operator cannot loop their own Tower's state
    Given an operator has made 20 lifecycle changes to "tw-1" within the hour
    When they ask for another
    Then it is refused with 429 and nothing changes, so the record stays transactional

  Scenario: A refusal anyone could have caused is not attributed to the Tower
    Given a request names "tw-1" but is unsigned or signed with a key that is not "tw-1"'s
    When it is refused
    Then the event's actor is "unauthenticated", not "tower"
    And a refusal of "tw-1"'s own signed request (suspended, lease lapsed, certificate revoked) still names "tower"

  Scenario: A sweep writes one retirement event per Tower
    When a sweep retires or reaps Stations behind "tw-1"
    Then one "station_retired" event records the count and names up to 20 of them
    And the log line names them all

  Scenario: An enrollment replay is recorded once
    When a committed enrollment is replayed with the same transaction
    Then no second "enrolled" event is written

  Scenario: The detail says when the Tower was approved and by whom from the whole record
    Given churn has pushed the approval out of the newest 50 events
    When an administrator opens "tw-1"'s detail
    Then it still shows the approval time and the approving actor

  Scenario: A recorded promotion that did not land is recorded as failed
    Given a "station_promoted" event was written before the promotion
    When the promotion fails
    Then a "station_promote_failed" event names the Station

  Scenario: Suppressed refusals are written when the retries stop
    Given refusals of one reason were suppressed within the hour
    When the next sweep runs after the hour
    Then one "request_refused" event carries the suppressed count

  # --- failure handling ------------------------------------------------------------------

  Scenario: Link events never block a Tower
    Given the governance record cannot be written
    When "tw-1" opens a session, heartbeats or closes
    Then the Tower gets the same answer as it would with the store up
    And the failure is logged once per minute, not once per request

  Scenario: Shutting a broker down does not invent events
    When a broker instance restarts while "tw-1" stays connected through the other instance
    Then no "session_closed" or "session_stale" event is recorded for "tw-1"
