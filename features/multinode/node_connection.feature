# AWAITING FOUNDER APPROVAL (spec-first workflow step 3) - do NOT write step definitions or
# implementation until approved. Phase 5 of the dispatch-scale design. Needs a `roger` release.
#
# TODAY: a node runs Parallel=4 HTTP long-polls. Each re-poll after a job is a round trip, and
# "taken" means only "flushed to a socket": a job written to a poll the edge already dropped
# is never confirmed and fails after the handoff grace.
#
# THE DESIGN: one WebSocket per node, `GET /agent/connect?node=<id>` with the node's Bearer
# BridgeToken. WebSocket, not a streamed HTTP body, because edge proxies commonly buffer
# streamed request bodies but pass WebSocket upgrades. TO VERIFY before building: a WebSocket
# through the production edge to the broker stays open past 100 s with pings. NEW DEPENDENCY for
# approval: github.com/coder/websocket (the repo has no WebSocket library today).
#   node -> broker   hello{credits, version} | credit{n} | ack{job} | ping
#   broker -> node   job{Job} | pong | drain
#   - Credits: the node grants one per free worker (its Parallel); a job consumes one; the node
#     returns it when the job ends. Each unspent credit is one idle poller in the dispatch
#     queue, so nothing else in dispatch changes.
#   - ack: the node confirms receipt. Only an ack sends "taken" to the origin. A job written but
#     not acked when the connection drops is put back in the node's queue.
#   - The node remembers job ids it has accepted (10 min). A re-delivered id is acked and not
#     served again, so put-back can never double-serve.
#   - Results and stream chunks still use POST /agent/result and /agent/stream (unchanged).
#   - ping every 30 s, well under the edge's ~100 s idle cap; silence for 75 s = dead.
#   - On shutdown the broker sends drain, puts back unacked jobs, and closes; the node
#     reconnects (to any instance) at once.
#   - A new node that finds no /agent/connect (an older broker answers 404) falls back to
#     long-polling. That 404 must never trigger a re-register.
#   - Old nodes keep long-polling, unchanged, forever: updates are operator-pulled.
#
# DECISIONS:
#   W1  A connected node receives at most `credits` jobs at a time and never more.
#   W2  "taken" means the node acked; a job not acked is never lost and never served twice.
#   W3  Connection drop, broker restart and drain lose no job and serve none twice.
#   W4  Old long-poll nodes and new connected nodes are served side by side.
#   W5  A new node on an older broker works by long-polling, with no re-register storm.
#   W6  Liveness and authority follow the connection (markSeen, the poll-host prober).
#   W7  Auth is the same as polling: unknown node 404, wrong token 401, rotated token honored.
#
# ENFORCED BY (planned): cmd/rogerai-broker/node_connection_bdd_test.go (real broker HTTP
# servers, real WebSocket clients) and internal/agent/connect_test.go (the node side against
# a real broker); two instances where cross-instance behaviour is asserted; no mocks.

Feature: A node holds one connection instead of many long-polls
  Background:
    Given a multi-instance broker of two instances sharing one store
    And a node "kokoro" connected to instance A with 4 credits

  # W1
  Scenario: A burst of 4 is sent at once over one connection
    When 4 jobs are dispatched to "kokoro" from instance B
    Then the node receives all 4 over its connection without polling
    And each is served exactly once

  Scenario: Jobs beyond the credits wait for a credit
    Given each job takes 300 ms to serve
    When 6 jobs are dispatched to "kokoro"
    Then the node never holds more than 4 unfinished jobs
    And all 6 are served

  Scenario: A node that grants no credit receives nothing
    Given "kokoro" has spent all its credits
    When a job is dispatched to "kokoro"
    Then no job frame is sent until the node returns a credit

  # W2
  Scenario: Taken is reported only on the node's ack
    Given the node delays its ack by 1 s
    When a job is dispatched to "kokoro"
    Then the origin sees the job taken only after the ack

  Scenario: A job the node never acks is put back and served once
    Given the node receives a job and drops the connection before acking
    When the node reconnects to instance B
    Then the job is delivered again and served exactly once

  Scenario: A re-delivered job the node already accepted is not served twice
    Given the node acked a job and its ack was lost with the connection
    When the job is delivered again after the node reconnects
    Then the node acks it without serving it again
    And exactly one result and one receipt exist for it

  # W3
  Scenario: Broker shutdown drains without losing a job
    Given 2 jobs are in flight and 1 is written but not acked on instance A
    When instance A shuts down
    Then the node receives drain and reconnects to instance B
    And the unacked job is served exactly once
    And the in-flight jobs finish and their results reach their origins

  Scenario: A silent connection is declared dead and its jobs put back
    Given the node stops sending pings and acks
    Then instance A closes the connection after 75 s of silence
    And its unacked jobs are served by the node's next connection

  Scenario: Pings keep an idle connection open past the edge's idle cap
    When the connection sits idle for 5 minutes
    Then it is still open and the node is still on air

  # W4
  Scenario: A connected node and a long-poll node share one queue
    Given a node "old" long-polling instance B with 4 pollers, offering the same model
    When 8 jobs are dispatched for that model
    Then all 8 are served, each exactly once, across both nodes

  Scenario: The same node id cannot hold a connection and long-polls at once without double-serve
    Given "kokoro" also long-polls instance B with 2 pollers
    When 20 jobs are dispatched to "kokoro"
    Then each is served exactly once

  # W5
  Scenario: A new node on an older broker falls back to long-polling
    Given a broker that has no /agent/connect
    When a new node starts
    Then it long-polls with its configured parallel
    And it does not re-register

  # W6
  Scenario: A connected node stays on air without polling
    When 2 minutes pass with no jobs
    Then "kokoro" is on air on both instances
    And instance A is its authoritative prober

  Scenario: A node whose connection dropped ages off air like a node that stopped polling
    When the connection drops and the node does not return
    Then "kokoro" is off air on both instances after the liveness window

  # W7
  Scenario Outline: Connect is authenticated like a poll
    When a connect for <node> arrives with <token>
    Then it is answered <status>

    Examples:
      | node        | token             | status |
      | "unknown"   | any token         | 404    |
      | "kokoro"    | a wrong token     | 401    |
      | "kokoro"    | the rotated token | 101    |
      | "kokoro"    | the old token     | 401    |

  Scenario: Single-instance brokers serve connected nodes too
    Given the broker runs in single-instance mode
    When 4 jobs are dispatched to a connected node
    Then each is served exactly once
