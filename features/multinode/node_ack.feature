# APPROVED by the founder 2026-09-27 (spec-first workflow step 3). Do not edit a scenario
# without re-approval. Phase 5, lean form (founder ruling 2026-09-27: no WebSocket
# for now; revisit only if broker memory or per-job latency becomes a measured problem).
# Needs a `roger` release; no new dependency.
#
# TODAY: "taken" means only "the job was flushed to a poll's socket". A job written to a poll
# the edge already dropped is never received, never confirmed, and the consumer gets a
# retryable 503 after the 5 s handoff grace instead of being served.
#
# THE DESIGN: nodes keep long-polling. A node that supports acks:
#   - sends `X-Roger-Ack: 1` on every poll (a header, not a registration field, so an older
#     broker ignores it and the registration signature is untouched);
#   - POSTs /agent/ack?node=<id>&job=<id> (Bearer BridgeToken) the moment it decodes a job,
#     before serving it;
#   - remembers the job ids it accepted for 10 min; a re-delivered id is acked again and NOT
#     served again;
#   - treats a 404 from /agent/ack as "this broker has no acks": ignore it, never re-register.
# The broker:
#   - records that the node is ack-capable (a short-TTL flag refreshed by its polls);
#   - for an ack-capable node, reports "taken" to the origin only on the ack, from whichever
#     instance the ack lands on;
#   - when an ack-capable node's job goes unacked past the grace, the origin puts it back at
#     the front of the node's queue and wakes a poller, up to 3 deliveries; after that the
#     consumer gets the retryable 503, unbilled;
#   - for a node without acks, keeps today's behaviour exactly.
#
# DECISIONS:
#   A1  For an ack-capable node, "taken" means the node received the job.
#   A2  A job written but never received is re-delivered and served exactly once.
#   A3  A job received whose ack was lost is never served twice.
#   A4  Nodes without acks, and brokers without acks, behave exactly as today.
#   A5  An ack cannot mark another node's job, or an unknown job, as taken.
#
# ENFORCED BY (planned): cmd/rogerai-broker/node_ack_bdd_test.go (two broker instances, real
# miniredis or ROGERAI_TEST_REDIS_URL) and internal/agent/ack_test.go (the node side against
# a real broker handler, including an older broker without /agent/ack); no mocks.

Feature: A node confirms each job it receives
  Background:
    Given a multi-instance broker of two instances sharing one store
    And an ack-capable node "kokoro" with 4 pollers split 2 and 2 across the instances

  # A1
  Scenario: The origin sees a job taken only when the node acks it
    Given the node delays its ack by 1 s
    When a job is dispatched to "kokoro" from instance A
    Then the origin does not see the job taken before the ack
    And it sees it taken right after the ack

  Scenario: An ack landing on a different instance than the poll still reaches the origin
    Given the node's polls are on instance A and its acks go to instance B
    When a job is dispatched to "kokoro" from instance B
    Then the origin sees the job taken

  # A2
  Scenario: A job written to a poll the node never received is re-delivered and served once
    Given the next job written to one of the node's polls is lost on the way
    When a job is dispatched to "kokoro"
    Then the job is delivered again to another poll
    And it is served exactly once
    And the consumer gets its result, not a 503

  Scenario: A job never acked after 3 deliveries fails honestly
    Given every job written to the node's polls is lost on the way
    When a job is dispatched to "kokoro"
    Then it is delivered at most 3 times
    And the consumer gets a retryable 503
    And no receipt settles for it

  # A3
  Scenario: A received job whose ack was lost is not served twice
    Given the node receives a job and its ack is lost
    When the job is delivered again
    Then the node acks it again without serving it
    And exactly one result and one receipt exist for it

  # A4
  Scenario: A node without acks behaves as today
    Given a node "old" that polls without the ack header
    When a job is dispatched to "old"
    Then the origin sees the job taken as soon as it is written to the poll
    And no ack is expected from "old"

  Scenario: A new node on a broker without acks keeps working and never re-registers
    Given a broker that has no /agent/ack
    When a new node polls, receives a job and posts its ack
    Then the ack is answered 404 and ignored
    And the node serves the job and posts its result
    And the node does not re-register

  Scenario: Single-instance brokers accept acks and change nothing
    Given the broker runs in single-instance mode
    When an ack-capable node receives a job and acks it
    Then the ack is answered 200
    And the job is served exactly once

  # A5
  Scenario Outline: An ack is authenticated and bound to its own job
    When an ack for <job> arrives from <node> with <token>
    Then it is answered <status>
    And <effect>

    Examples:
      | job                       | node       | token          | status | effect                            |
      | a job sent to "kokoro"    | "unknown"  | any token      | 404    | the job is not marked taken       |
      | a job sent to "kokoro"    | "kokoro"   | a wrong token  | 401    | the job is not marked taken       |
      | a job sent to "other"     | "kokoro"   | kokoro's token | 200    | the other job is not marked taken |
      | an id never dispatched    | "kokoro"   | kokoro's token | 200    | nothing changes                   |
      | a job sent to "kokoro"    | "kokoro"   | kokoro's token | 200    | the job is marked taken           |
