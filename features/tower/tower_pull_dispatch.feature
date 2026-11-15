# APPROVED SPEC - written and founder approved 2026-11-14 ("Approve as written"): every upgraded Tower pulls,
# "same network" is the same public IPv4 or the same IPv6 /64, numbers accepted (3 s pickup, 4 held
# pulls, 25 s hold). Changes to an approved scenario need re-approval.
# BUILD STATUS: PARTIAL. Code is in; the executable runners are being written.
#
# Founder request 2026-11-14: "i don't want towers that carry nothing, thats useless. also if
# every tower has to go through our core then that is just adding an extra hop for no reason.
# please do better research on a better design". Then: "write the spec for change 1".
#
# Change 1 of three (the order the founder accepted):
#   1. this file and private_hub_placement.feature: the Tower PULLS its jobs from Core over the
#      outbound link it already holds, so Core never dials into a Tower, and a Tower with no
#      public address carries the Stations on its own network.
#   2. later: remote Stations reach a Tower with no public address directly, Core only making
#      the introduction (hole punching, IPv6, router port mapping).
#   3. later: Towers with a public address relay for the pairs that cannot connect directly.
#
# What the code does today (read 2026-11-14 at main 18bc8da1, not the design docs, which are out
# of date on this point):
#   consumer --HTTPS--> Core --POST /submit--> Tower hub <--GET /poll, POST /complete-- Station
#   Tower --outbound link--> Core: session, heartbeat, inventory, hub node list, settle. No jobs.
# Core is already on every Tower-carried request: it seals the request and submits it to the
# hub itself (cmd/rogerai-broker/edgebridge.go driveSealedF). So reversing who opens the
# connection adds no hop. It removes the only reason the Core-to-Tower leg needs an inbound port.
#
# Reuse, not new machinery: Core already hands jobs to long-polling nodes across instances with
# single delivery (busPublishJob / busPublishResult, sharedstore.go). The Tower becomes one more
# poller on that bus, keyed by its Tower ID. The sealed loop (seal, submit, open, verify the
# receipt, acknowledge) is unchanged; only the transport of "submit" moves.
#
# Proposed numbers (founder to accept or change):
#   pickup deadline    3 s   a job no Tower poll has taken by then is withdrawn and the request
#                            fails over, exactly as a dial failure does today
#   held polls         4     the most pull requests one Tower session may hold open at once
#   poll hold          25 s  how long an empty pull is held (same as GET /agent/cancels)
#   result size cap    the hub's own response cap, unchanged

Feature: A Tower pulls its work from Core over the link it already holds
  Every request a Tower carries already passes through Core, which seals it to the Station.
  Today Core then opens a connection into the Tower's hub to hand it over, which is why a
  Tower needs an address Core can dial. Instead the Tower asks Core for work over its own
  outbound link, runs it through its hub exactly as before, and returns the sealed answer the
  same way. The bytes take the same path as today; only who opens the connection changes.

  Background:
    Given Roger Core runs two instances sharing one store
    And a registered Tower with a live link session and a hub serving one attached Station

  # --- which Towers pull ----------------------------------------------------------------

  Scenario: A Tower that can pull says so when it opens its session
    When a Tower running this release opens a link session
    Then its session record carries the "pull" capability, mirrored to the shared store
    And every Core instance reads the same capability for that session

  Scenario: Every Tower that can pull is given its work by pull, public address or not
    Given the Tower's session carries the "pull" capability
    And the Tower advertises a public hub address
    When a consumer request is routed to a Station on that Tower
    Then Core queues the sealed job for the Tower and does not dial the hub

  Scenario: An older Tower without the capability is dialed exactly as today
    Given a Tower whose session does not carry the "pull" capability
    And it advertises a public hub address
    When a consumer request is routed to a Station on that Tower
    Then Core dials the hub and submits, as it does today
    And nothing about that path changes

  Scenario: An older Tower with no public address still carries nothing, and is told why
    Given a Tower whose session does not carry the "pull" capability
    And it advertises no hub address, or a private one
    Then no Station is placed on it
    And `roger tower serve` on that machine tells the operator to upgrade so the Tower can pull

  # --- the pull ------------------------------------------------------------------------

  Scenario: The Tower holds a pull open and receives a job the moment one is queued
    Given the Tower holds one pull request open on instance A
    When a consumer request routed to its Station arrives on instance B
    Then the sealed job reaches the Tower on instance A's held pull without waiting for the hold to end
    And the job carries the signed grant and the sealed request, and nothing else

  Scenario: An empty pull is held, then answered empty
    Given no job is queued for the Tower
    When the Tower sends a pull request
    Then Core holds it for up to 25 seconds
    And answers "no work" if nothing arrives, and the Tower pulls again at once

  Scenario: A job queued between two pulls waits for the next one
    Given the Tower has no pull open
    When a job is queued for it
    And the Tower pulls within the 3 second pickup deadline
    Then the job is delivered on that pull

  Scenario: A job is delivered once, however many pulls are held
    Given the Tower holds four pulls open across both instances
    When one job is queued for it
    Then exactly one pull receives the job
    And the other three keep holding

  Scenario: A Tower may hold at most four pulls at once
    Given the Tower holds four pulls open
    When it sends a fifth
    Then Core answers 429 with a Retry-After, and the four held pulls are unaffected

  Scenario: A Tower receives only jobs addressed to it
    Given two Towers each with a held pull
    When a job is queued for the first Tower's Station
    Then only the first Tower receives it
    And no pull request can name another Tower's ID to receive its jobs

  Scenario: A job nobody picks up fails over, uncharged
    Given the Tower has no pull open and does not pull within the 3 second pickup deadline
    When the deadline passes
    Then the job is withdrawn so no later pull can take it
    And the consumer request moves to the next candidate, as a failed dial does today
    And the consumer is not charged for the withdrawn attempt
    And the miss counts against the Tower like a failed dial, once

  Scenario: A withdrawn job is never delivered late
    Given a job was withdrawn at its pickup deadline
    When the Tower pulls a moment later
    Then that pull does not receive the withdrawn job

  # --- the answer ----------------------------------------------------------------------

  Scenario: The Tower returns the sealed answer over its link, to whichever instance it reaches
    Given the Tower took a job on instance A for a request waiting on instance B
    When the Station completes and the Tower posts the sealed answer and receipt to instance A
    Then instance B receives them and runs the same checks as today: open, receipt binds the opened bytes
    And the consumer gets the answer from instance B

  Scenario: An answer for a job the Tower was never given is refused
    When a Tower posts an answer naming an attempt that was not delivered to that Tower
    Then Core refuses it and nothing reaches any waiting request

  Scenario: An answer is accepted once
    Given a Tower posted an answer for an attempt
    When it posts an answer for the same attempt again
    Then the second post changes nothing and is answered as already received

  Scenario: An answer that arrives after the request gave up is dropped, not billed
    Given the waiting request timed out and moved to another candidate
    When the Tower posts the answer late
    Then the answer is discarded
    And the consumer is billed only for the candidate that served them

  Scenario: An answer larger than the hub's response cap is refused
    When a Tower posts an answer over the cap
    Then Core refuses it before reading the rest, and the attempt fails as a Tower fault

  Scenario: A failure the Station reports travels back the same way
    Given the Station answers the job with a model failure, such as a 429 and a Retry-After
    When the Tower posts that failure
    Then the waiting request sees the same failure class it sees today from a dialed hub

  # --- what the Tower can and cannot do with a pulled job -------------------------------

  Scenario: The Tower still cannot read the request or the answer
    Then a pulled job carries the request sealed to the Station's session key, as today
    And the answer is sealed to the consumer side, as today
    And the receipt is signed by the Station, so the Tower cannot alter the answer unnoticed

  Scenario Outline: What a Tower cannot get away with on the pull path
    When the Tower <tries>
    Then Core <answers>

    Examples:
      | tries                                                     | answers                                              |
      | posts an answer whose receipt does not bind its bytes     | fails the attempt as a Tower fault, charges nothing  |
      | posts an answer signed by a different Station             | fails the attempt as a Tower fault, charges nothing  |
      | replays an earlier attempt's answer for a new attempt     | fails the attempt as a Tower fault, charges nothing  |
      | pulls with a session that is closed or expired            | refuses the pull, delivers nothing                   |
      | pulls after its certificate serial was revoked            | refuses the pull, delivers nothing                   |
      | pulls while suspended, revoked or lease-lapsed            | refuses the pull, delivers nothing                   |
      | pulls without a valid request signature                   | refuses the pull, delivers nothing                   |

  Scenario: A Tower revoked while a pull is held gets nothing more
    Given the Tower holds a pull open
    When the Tower is revoked
    Then the held pull is answered empty and the next pull is refused
    And jobs already queued for it are withdrawn and fail over, uncharged

  Scenario: A draining Tower is given no new jobs and finishes the ones it holds
    Given the Tower holds two jobs in progress
    When it starts draining
    Then its pulls receive no new jobs
    And its answers for the two jobs it holds are still accepted

  # --- health checks ride the same path -------------------------------------------------

  Scenario: The canary is delivered by pull, like any other job
    Given the Tower's session carries the "pull" capability
    When Core runs a canary against a Station on that Tower
    Then the canary job is queued for the Tower's pull, indistinguishable from customer traffic
    And it passes or fails exactly as a dialed canary does

  Scenario: The hub check is delivered by pull for a Tower that pulls
    Given the Tower's session carries the "pull" capability
    When its hub check is due
    Then Core queues a hub check carrying a fresh nonce for the Tower's pull
    And the Tower's hub answers with the same signed identity proof it gives today
    And a correct proof is recorded as a passed hub check, with source "pull"
    And no proof by the pickup deadline is recorded as a failed hub check, once per sweep slot

  Scenario: A Tower with a public address is also checked from outside
    Given the Tower pulls and advertises a public hub address
    When its hub check is due
    Then Core also dials the public address, as today, because remote Stations depend on it
    And a failure there counts as a failed hub check even when the pull check passed

  Scenario: The pull path changes no trust ramp number
    Then promotion, demotion, caps and the house failover allowance read the same health record
    And a pull check pass or fail counts exactly as a dialed hub check pass or fail does

  # --- multi-instance and restarts ------------------------------------------------------

  Scenario: The instance holding the pull restarts while a job is in progress
    Given the Tower took a job on instance A
    When instance A restarts
    Then the Tower's answer is posted to whichever instance it reaches and still reaches the waiting request

  Scenario: The instance holding the waiting request restarts
    Given a request on instance B is waiting for the Tower's answer
    When instance B restarts
    Then the request ends for the consumer as a broken connection does today
    And a late answer for it is discarded and nothing is billed for it

  Scenario: No shared store means no pull path
    Given Core runs one instance with no shared store configured
    Then pull delivery uses the same in-process fallback the node poll uses today

  # --- the operator ---------------------------------------------------------------------

  Scenario: The serve command no longer says a Tower with no public address carries nothing
    When an operator runs `roger tower serve --hub :8444` with no --relay-public
    Then it does not print that Roger Core will not route anyone to this Tower
    And it prints that Stations on this network will be placed here and remote Stations need a public address for now

  Scenario: Tower status says how work reaches the Tower
    When an operator runs `roger tower status`
    Then it shows "work: pulled over the link"
    And it shows who the hub can serve: "this network only" or "public, at <address>"

  # --- documentation that is out of date today ------------------------------------------

  Scenario: The design documents describe what the code does
    Then docs/tower-architecture.md and docs/tower-relay-link-design.md say jobs reach a Tower by pull over its link
    And neither says a Tower must have a public address to carry traffic
