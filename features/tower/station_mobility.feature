# PROPOSED SPEC - written 2026-11-13, NOT YET APPROVED. Nothing here is built until the
# founder approves it; changes after approval need re-approval.
# BUILD STATUS: NOT BUILT.
#
# Founder request 2026-11-13: idea 3, "move Stations between Towers, starting with idle ones",
# "with rehoming and so on". This file builds the shape already DECIDED in
# docs/relay-selection-design.md:
#   - section 6.3b (founder, 2026-08-20): sticky placement with supervised mobility. A Station
#     keeps one Tower and Core moves it only while idle, or when its Tower is gone or bad.
#   - section 6.3c (founder, 2026-08-20): no drain state. The race is accepted and made harmless
#     by the Station-epoch fence (section 6.6b, already built). A move that catches live work
#     voids that attempt: nobody is paid, the consumer's hold is released, the consumer retries.
#   - section 6.3c "who performs the move": nothing new writes origin_tower. A move is a dormant
#     write plus a re-attach; Admit's existing revival branch writes the new Tower and raises the
#     epoch by one, and stays the epoch's only writer.
#
# It supersedes the approved scenario "Rehoming a Station fences its prior origin"
# (inventory_and_routing.feature) ONLY in how the move is started: that scenario says "its owner
# and Roger Core approve a move". Here Core starts moves on its own, for the reasons below, and
# the owner is told. That change to an approved scenario needs the founder's re-approval, and is
# listed in the approval request.
#
# NUMBERS. Section 6.3c proposed 2 fresh observations for the failure trigger (reusing
# edgeCanaryFailBar = 2), 3 for the improvement trigger, and a 25% plus 30 ms materiality bar.
# This file keeps 2 and 3. It REPLACES the 25%/30 ms bar with "a strictly better class" (tier,
# locality or group), because Core has no Station-to-Tower round-trip measurement for 30 ms to
# be measured against, and a discrete class cannot flap on noise. Founder's call.

Feature: Core moves an idle Station to a better Tower, and fences anything the move catches
  A Station keeps its Tower until one of four things is true: its Tower is gone, its Tower is
  failing, a strictly better Tower exists, or its Tower's trust stage no longer allows it.
  Core then waits until the Station is idle, marks it dormant, and tells its node to attach
  again. The node re-attaches with its own Station id and keys, and placement puts it on the
  chosen Tower with the next epoch. Every step is in the governance record.

  Background:
    Given a Station "S" live on Tower "A" at epoch 4
    And S's node declared at attach that it understands the re-attach nudge

  # --- triggers ------------------------------------------------------------------

  Scenario: Trigger 1, the Tower is gone
    Given Tower A has been absent from the fleet-wide live set for longer than the link freshness window
    When the mobility pass runs
    Then S is due to move with trigger "tower_gone"
    And the move does not wait for any observation count

  Scenario: A Tower that drops out briefly is not "gone"
    Given Tower A's link closed and a new session opened within the freshness window
    When the mobility pass runs
    Then S is not due to move

  Scenario: Trigger 2, the Tower is failing, on two fresh observations
    Given two consecutive fresh canary observations of Tower A both failed
    When the mobility pass runs
    Then S is due to move with trigger "tower_failing"

  Scenario: One failed observation is not enough
    Given the latest canary observation of Tower A failed and the one before it passed
    When the mobility pass runs
    Then S is not due to move

  Scenario: Trigger 3, a strictly better Tower, on three fresh observations
    Given on three consecutive fresh observations some eligible Tower B was in a strictly better class for S than Tower A
    When the mobility pass runs
    Then S is due to move with trigger "better_tower" and target B

  Scenario Outline: What counts as strictly better
    Given S on Tower A and a candidate Tower B
    When "<comparison>"
    Then B "<verdict>" strictly better for S

    Examples:
      | comparison                                                          | verdict |
      | B is health tier A and A is tier B                                  | is      |
      | same tier, B is same_network and A is same_continent                | is      |
      | same tier, B is same_continent and A is far                         | is      |
      | same tier, B is same_continent and A is unknown                     | is      |
      | same tier, same class, B is less loaded                             | is not  |
      | B is nearer but tier B, and A is tier A                             | is not  |
      | S is a house Station, B is a house Tower, A is a community Tower    | is      |
      | S is a community Station, B is a community Tower, A is a house Tower, same tier and class | is |
      | S is a community Station, B is a house Tower, A is a community Tower, same tier and class | is not |

  Scenario: Trigger 4, the Tower's trust stage no longer allows the Station
    Given Tower A was demoted and S is above A's new cap for its group
    When the mobility pass runs
    Then the newest Stations above the cap are due to move with trigger "stage_cap", oldest kept
    And the move does not wait for any observation count

  # --- observations and hysteresis --------------------------------------------------

  Scenario: The streak counts new observations, not passes
    Given the mobility pass runs more often than the canary
    When three passes see the same canary sample
    Then the streak has advanced once, not three times
    And it advances only when the sample's own timestamp is newer than the last one counted

  Scenario: A streak resets the moment the condition does not hold
    Given S has two consecutive observations favoring Tower B
    When the next fresh observation does not favor B
    Then the streak for S is zero

  Scenario: A stale streak is discarded
    Given S's last counted observation is older than twice the canary interval
    When a new observation arrives
    Then the streak starts again from one

  Scenario: A streak that changes target starts over
    Given S has two observations favoring Tower B
    When the third favors Tower C instead
    Then the streak is one, for C

  Scenario: Streaks live with the instance that owns the Tower's link and survive nothing else
    Given the mobility pass for Tower A runs on the instance holding A's link
    When that instance restarts or A's link moves to another instance
    Then S's streak starts over on the new owner
    And no streak is shared between instances or written to the store

  # --- the idle gate -----------------------------------------------------------------

  Scenario: A Station is moved only when Core can see it is idle
    Given S is due to move
    When stationQuiescent reports no work on either fabric, fleet-wide, from a fresh peer view
    Then the move starts

  Scenario Outline: A Station Core cannot prove idle is left alone this pass
    Given S is due to move
    And "<condition>"
    When the mobility pass runs
    Then the move does not start
    And S stays due and is checked again next pass

    Examples:
      | condition                                          |
      | S has a classic request in flight on this instance |
      | S has an edge attempt open on this instance        |
      | a peer instance reports load for S                 |
      | the peer load view is older than its freshness bound |
      | the shared store cannot be read                    |

  Scenario: A Tower that is gone moves its Stations even when Core cannot prove idle
    Given Tower A is gone and S shows load
    When the mobility pass runs
    Then S's move starts anyway
    Because work through a Tower that is gone cannot complete

  # --- the move itself ---------------------------------------------------------------

  Scenario: The move is a dormant write, then a nudge
    Given S is due to move to Tower B and is idle
    When the move starts
    Then S's attachment is set dormant only if it is still live, still on Tower A and still at epoch 4
    And the dormant row records the intended Tower B and the trigger
    And a "station_move_started" event is recorded for Tower A naming S, B and the trigger
    And a re-attach nudge is queued on S's node's broker poll

  Scenario: A concurrent change wins and the move gives up
    Given S's attachment changed state, Tower or epoch after the pass read it
    When the dormant write runs
    Then nothing is written and no nudge is sent
    And no move event is recorded

  Scenario: Two instances cannot move the same Station twice
    Given two instances decide to move S at the same moment
    When both attempt the dormant write
    Then exactly one write lands
    And exactly one nudge and one "station_move_started" event exist

  Scenario: The node re-attaches with its own identity and lands on the intended Tower
    Given S is dormant with intended Tower B
    When S's node re-attaches with its own station id and both keys
    Then Admit's revival branch places S on Tower B, if B is still eligible
    And S's epoch becomes 5
    And the reply carries Tower B's endpoint, pin, a new hub token and B's key fingerprint
    And a "station_moved" event is recorded for Tower B naming S, Tower A, epoch 5 and the trigger
    And a matching "station_moved_away" event is recorded for Tower A

  Scenario: An intended Tower that is no longer eligible is re-ranked
    Given S is dormant with intended Tower B
    And B became ineligible before the node re-attached
    When the node re-attaches
    Then placement ranks the eligible Towers afresh, excluding A unless A is the only one
    And the recorded reason says the intended Tower was unavailable

  Scenario: A re-attach that lands back on the same Tower is recorded as no move
    Given S is dormant and the only eligible Tower is A
    When the node re-attaches
    Then S is live on A with epoch 5
    And the record says "station_move_returned" rather than "station_moved"

  Scenario: The nudge names no Tower and is not a redirect
    When Core sends a re-attach nudge
    Then the message names no Tower, endpoint or pin
    And the node's only action is to re-attach to Core over its already-pinned Core address
    And a forged nudge can at worst cause one extra re-attach

  Scenario: An old node never receives a nudge it would serve as work
    Given a node did not declare at attach that it understands the nudge
    When its Station is due to move with trigger "better_tower" or "stage_cap"
    Then Core does not move it
    And when the trigger is "tower_gone" or "tower_failing" Core moves it with no nudge, relying on the node's own re-attach on hub failure

  Scenario: The node re-checks that it is idle before acting
    Given a node receives a nudge while serving work Core could not see
    When it handles the nudge
    Then it finishes the work first and re-attaches when idle
    And if that takes longer than the nudge's expiry it ignores the nudge

  Scenario: A node acts on at most one nudge per minute
    Given a node receives several nudges within one minute
    When it handles them
    Then it re-attaches once

  # --- self-healing ------------------------------------------------------------------

  Scenario: A lost nudge is sent again
    Given S has been dormant for a move for 30 seconds without a re-attach
    When the mobility pass runs
    Then the nudge is queued again, up to 3 times in all

  Scenario: A move that never completes is undone, not left dark
    Given S stayed dormant through 3 nudges
    When the mobility pass runs
    Then S's attachment is restored to live on Tower A at epoch 4, only if it is still that dormant row
    And a "station_move_abandoned" event is recorded naming the reason "no re-attach"
    And Tower B is skipped for S for one hour

  Scenario: A move off a Tower that is gone is never undone back onto it
    Given S is dormant because Tower A is gone and its node never re-attached
    When the abandon time comes
    Then S stays dormant, as the idle sweep would leave it
    And its node is placed afresh whenever it next attaches

  # --- the fence ----------------------------------------------------------------------

  Scenario: An attempt caught by a move is void
    Given an edge attempt was granted to S at epoch 4 through Tower A
    And S moved to Tower B at epoch 5 before the attempt settled
    When Tower A's courier presents the receipt
    Then settlement answers 410, exactly as the built fence does
    And nobody is paid, no reputation is written, and the consumer's hold is released on age
    And the "edge fence MOVED" log line names Tower A, both epochs and whether the deadline was still open

  Scenario: Every move records the load Core saw when it decided
    When a move starts
    Then the "station_move_started" event's detail carries the four load terms stationQuiescent read
    So that moves which raced live work can be counted against fence firings

  # --- pacing ---------------------------------------------------------------------------

  Scenario: Moves are paced so a Tower is not emptied or flooded in one pass
    Given 40 Stations on Tower A are due to move
    When the mobility pass runs
    Then at most 5 moves start from Tower A in that pass
    And at most 5 moves in that pass target any one Tower
    And at most 20 moves start fleet-wide in that pass
    And "tower_gone" moves are paced the same way

  Scenario: Move events count toward the governance record's per-Tower budget
    When moves are recorded
    Then they are paced by the same per actor, per kind, per Tower budget the record already applies
    And the actor is "system"

  # --- who sees what ----------------------------------------------------------------------

  Scenario: The Station's owner is told where and why
    When S is moved
    Then S's owner sees, in the console and in `roger share` output, that S moved from Tower A to Tower B and the trigger in plain words
    And the Tower ids are shown, never the Tower operators' account names

  Scenario: Both Tower operators see the change in their Station counts
    When S moves from Tower A to Tower B
    Then Tower A's operator view counts one Station fewer and Tower B's one more
    And neither operator sees the other Tower's id or S's owner

  Scenario: Admins see moves in the Tower detail
    When an admin opens Tower A's or Tower B's detail
    Then the move events appear in its events list with trigger, Station, other Tower and epoch

  # --- what does not change -----------------------------------------------------------------

  Scenario: Direct-to-joined and joined-to-direct moves remain forbidden
    When Core considers moves
    Then it only ever moves a joined Station from one Tower to another
    And origin kind stays immutable, as the approved station_attachment.feature requires

  Scenario: The epoch still has exactly one writer
    When any move completes
    Then the epoch was raised only by Admit's revival branch
    And an abandoned move restores the old epoch rather than writing a new one

  Scenario: The classic direct path is untouched by any move
    Given S's node also serves on the classic direct path
    When S is dormant mid-move
    Then direct requests to the node keep being served
