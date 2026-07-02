# AWAITING FOUNDER APPROVAL (spec-first workflow step 3) — do NOT write step definitions or
# implementation until approved. RELEASE BLOCKER, audit finding #1 (money/auth).
#
# THE HOLE: in production (ROGERAI_MULTI_INSTANCE=1, 2 instances + Valkey) every `roger share`
# node runs Parallel=4 poll loops (internal/agent/agent.go), so ~4 concurrent GET /agent/poll
# handlers are open per node at steady state. Each multi-instance agentPoll opens its OWN
# Redis SUBSCRIBE on channel busjob:<node> (tunnel.go:1190 → sharedstore.go busSubscribeJobs).
# The relay dispatches ONE job via busDispatchJob → busPublishJob, which is a Redis PUBLISH
# (sharedstore.go:1041, rdb.Publish) — a FAN-OUT to EVERY subscriber. So one relayed request
# is delivered to and served by ALL ~4 of the node's pollers:
#   - non-stream: 4x duplicate upstream inference + 4 receipts (only the first result reaches
#     resCh; the rest are wasted GPU + wasted holds).
#   - stream:true: WORSE — all 4 pollers POST /agent/stream chunks to the SAME
#     busstream:<jobID> channel, and relayStream's pump writes every frame to the client, so
#     the consumer receives 4 interleaved copies truncated at the first done marker — a
#     corrupted response.
# busDispatchJob only guards delivered==0 (no poller) → errNoPoller; delivered>1 is NOT
# deduped. The local single-instance path is correct (t.jobs is a single-consumer channel);
# only the bus path fans out.
#
# GROUND TRUTH: cmd/rogerai-broker/tunnel.go agentPoll (:1190 busSubscribeJobs per poll),
# busDispatchJob (:1780, delivered==0 the only guard), sharedstore.go busPublishJob (:1035,
# rdb.Publish), internal/agent/agent.go Parallel=4 pollers. Contrast: the LOCAL t.jobs channel
# already gives exactly-once (one receiver wins the channel receive).
#
# THE INVARIANT: a relayed job is delivered to and served by EXACTLY ONE of a node's pollers,
# across all instances — never fanned out to the node's own parallel pollers.
#
# LIKELY SHAPE (for founder review, not binding on the impl): replace the per-node
# PUBLISH/ SUBSCRIBE with a single-consumer claim — a Redis LIST (LPUSH dispatch + BLPOP poll)
# or an atomic claim key per job — so exactly one poller across the fleet dequeues each job.
# The result/stream channels (busPublishResult / busstream:<jobID>) stay pub/sub (one
# publisher, one subscriber) and are unaffected.
#
# DECISIONS PINNED BY THIS SPEC:
#   D1  SINGLE DELIVERY: with N>=2 concurrent pollers for a node on one instance, a single
#       dispatched job is dequeued by exactly ONE poller — N-1 pollers keep waiting.
#   D2  CROSS-INSTANCE: with pollers split across both instances, the job is still served
#       exactly once (the instance whose poller claims it), never once per instance.
#   D3  NO DOUBLE-CHARGE: exactly one receipt/hold settles per relayed request; no duplicate
#       upstream inference is billed.
#   D4  STREAM INTEGRITY: a stream:true response reaching the consumer is a SINGLE clean
#       stream to one done marker — never interleaved duplicate frames.
#   D5  NO POLLER: when no poller is listening on ANY instance, dispatch still fails cleanly
#       with the existing "node busy (no poller free)" 503 — unchanged behavior.
#   D6  SINGLE-INSTANCE UNCHANGED: the local t.jobs single-consumer path is untouched and
#       already satisfies D1/D3/D4.

# ENFORCED BY: cmd/rogerai-broker/single_delivery_dispatch_test.go (real miniredis bus, no
# mocks: 4 concurrent pollers on one node, one dispatched job, asserts exactly one serves).
# IMPLEMENTATION NOTE: the shipped fix keeps the fan-out PUBLISH (so the delivered==0 -> 503
# no-poller signal, D5, is untouched) and adds a single-delivery CLAIM (busClaimJob = SET NX
# on the unique job id) in agentPoll: the first poller to claim serves, the rest re-poll (204).
# This satisfies D1-D4; D5/D6 are unchanged by construction. A future move to a single-consumer
# LIST/stream-group would also satisfy this spec.

Feature: Multi-instance job dispatch delivers each relayed job to exactly one poller
  As a consumer relaying through a multi-instance broker
  I want my request served once by one of the node's pollers
  So that I am billed once and my stream is not a corrupted 4x interleave

  Background:
    Given the broker runs in multi-instance mode over a shared bus
    And an on-air node "n1" running 4 concurrent poll loops

  # ── D1/D3: single delivery, single charge ──────────────────────────────────────────────

  Scenario: one dispatched non-stream job is served by exactly one poller
    When a single job is dispatched to node "n1"
    Then exactly one of "n1"'s pollers dequeues it
    And exactly one upstream inference runs
    And exactly one receipt is settled

  # ── D2: cross-instance ─────────────────────────────────────────────────────────────────

  Scenario: pollers split across both instances still serve a job exactly once
    Given 2 of "n1"'s pollers are on instance-A and 2 are on instance-B
    When a single job is dispatched to node "n1"
    Then exactly one poller across both instances dequeues it
    And the job is not served once per instance

  # ── D4: stream integrity ───────────────────────────────────────────────────────────────

  Scenario: a streamed response reaches the consumer as a single clean stream
    When a stream:true job is dispatched to node "n1"
    Then the consumer receives one contiguous stream ending at a single done marker
    And it does not contain interleaved duplicate frames

  # ── D5: no poller (unchanged failure) ──────────────────────────────────────────────────

  Scenario: a job for a node with no live poller fails cleanly
    Given node "n1" has no poller listening on any instance
    When a single job is dispatched to node "n1"
    Then dispatch fails with the "node busy (no poller free)" 503
    And no charge is made

  # ── D6: single-instance regression guard ───────────────────────────────────────────────

  Scenario: single-instance dispatch is unchanged
    Given the broker runs in single-instance mode
    When a single job is dispatched to a node with 4 local poll loops
    Then exactly one poller serves it and exactly one receipt settles
