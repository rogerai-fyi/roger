# APPROVED by the founder 2026-09-27 (spec-first workflow step 3). Do not edit a scenario
# without re-approval.
#
# THE CEILING: in multi-instance mode every held node poll and every in-flight relay opened
# its own shared-store SUBSCRIBE, i.e. its own dedicated connection. Store connections grew
# with pollers and requests, so the store's client limit, not broker memory, capped the
# fleet. Dispatch was also a fan-out PUBLISH + claim race: one job knocked a node's whole
# poll pool off the channel, a burst larger than one got instant 503s, and "busy" meant
# "go away" instead of "wait a moment".
#
# THE DESIGN (internal: DISPATCH-SCALE-DESIGN-2026-09-27):
#   - a per-node shared job LIST; a poll pops it (LPOP is exactly-once, no claim key);
#   - a per-node route HASH of idle pollers per instance;
#   - a per-instance inbox STREAM read by ONE reader per instance: it carries the nudge
#     ("wake an idle poller of node X"), the bounce ("I had none"), the taken notice, and
#     every result/stream chunk addressed to the instance that holds the consumer;
#   - store connections are a fixed number per instance, never per poll or per request.
#
# WORDING NOTE for "A stale route bounces": what is sent to an instance is the job's
# wake-up (the nudge); the job itself waits in the node's shared list. The origin re-sends
# the wake-up elsewhere on a bounce, and the job is served exactly once.
#
# ENFORCED BY: cmd/rogerai-broker/dispatch_inbox_bdd_test.go (real miniredis or
# ROGERAI_TEST_REDIS_URL, two broker instances, no mocks).

Feature: Cross-instance dispatch through instance inboxes
  Background:
    Given a multi-instance broker of two instances sharing one Valkey
    And a node "kokoro" with 4 pollers split 2 and 2 across the instances

  Scenario: A burst no larger than the pollers is served in full
    When 4 jobs are dispatched to "kokoro" within 10 ms
    Then all 4 are served, each by exactly one poller
    And no job is answered 503

  Scenario: A burst larger than the pollers waits, then drains
    Given each job takes 300 ms to serve
    When 8 jobs are dispatched to "kokoro" within 10 ms
    Then all 8 are served
    And the last result arrives within 900 ms of the first dispatch

  Scenario: Valkey connections do not grow with pollers or requests
    Given 200 nodes with 4 pollers each spread over both instances
    When 400 jobs are in flight
    Then Valkey reports at most 2 × (pool size + 1) client connections from the brokers

  Scenario: A stale route bounces and is re-sent, never served twice
    Given instance 1's route says it holds a poller for "kokoro"
    But that poller has just left
    When a job is sent to instance 1
    Then instance 1 bounces it to the origin
    And the origin re-sends it to instance 2
    And the job is served exactly once

  Scenario: The queue deadline is decided by the holder alone
    Given every poller of "kokoro" is serving a job that takes 10 s
    When an audio job is dispatched to "kokoro"
    Then after 3 s it is answered 503 "station busy"
    And no poller serves it after that

  Scenario: A holder that dies fails its queued jobs fast, unbilled
    Given jobs for "kokoro" are queued on instance 2
    When instance 2 dies
    Then each of those jobs is answered a retryable 503 within 15 s
    And no receipt settles for them

  Scenario: Exactly once across instances
    When 100 jobs are dispatched to "kokoro" from both instances at random
    Then every job is served exactly once
    And exactly one receipt settles per job

  Scenario: The same store runs on a ring of two Valkeys
    Given the broker is configured with a ring of two Valkeys
    When 100 jobs are dispatched from both instances
    Then every job is served exactly once
