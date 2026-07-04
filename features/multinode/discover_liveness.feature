# Cross-instance /discover ONLINE liveness - the RESIDUAL flicker after the registry union.
#
# CONTEXT: task #52 / PR #11 pinned the registry UNION - both instances re-hydrate every
# peer's registration from the shared store, so a node that dialed instance A is VISIBLE +
# pickable + authenticatable on instance B (features/multinode/liveness_churn.feature). That
# fixed WHICH nodes appear. It did NOT fix the ONLINE bit each offer carries.
#
# THE RESIDUAL BUG (proven live 2026-07-04, 2 instances, MULTI_INSTANCE=1): /discover was
# ~80% steady at 8 online but ~20% collapsed to 0 - the instance that does NOT host a given
# node's live poll intermittently showed that node OFFLINE, so its /discover reported 0
# online even though the node kept heartbeating on the other instance.
#
# ROOT CAUSE: enrichOffersForNode computes `online := live && tq.probeFails < probeDeadStreak`
# using THIS instance's LOCAL probe-fail streak (market.go). probeFails is a per-instance,
# in-memory counter (recount.go) that is NEVER shared. On the instance that does not host a
# node's live poll, that streak is NOT an authoritative liveness signal - a cross-instance
# probe can time out or never land - so a live node accumulates probeFails locally and flips
# to OFFLINE on that peer's /discover, collapsing the online count. The registry union keeps
# the node VISIBLE; this keeps it ONLINE.
#
# THE FIX: the probe-dead veto is applied only on the instance that authoritatively probes the
# node - the one that hosts its live poll (a recent LOCAL /agent/poll, tracked in localPollAt)
# - or in single-instance mode (shared == nil), where the poll is always local (byte-for-byte
# unchanged). A peer that merely mirrors the node via the shared registry/liveness keeps it
# ONLINE on heartbeat liveness alone and never probe-kills it. A node that stops heartbeating
# still ages out as OFFLINE on BOTH instances via nodeTTL.
#
# GROUND TRUTH: cmd/rogerai-broker/market.go (enrichOffersForNode / computeDiscover /
# computeMarket), tunnel.go (agentPoll stamps localPollAt; markSeen / syncLivenessOnce mirror
# liveness), recount.go (trustState.probeFails), probe.go (probeDeadStreak). Enforced by
# cmd/rogerai-broker/discover_liveness_bdd_test.go: two real broker instances sharing one
# miniredis + one store, real signed registrations, asserting on computeDiscover/computeMarket
# DIRECTLY (not the serveCachedJSON HTTP path, whose shared response cache would mask a
# per-instance flip). No mocks.

Feature: Cross-instance /discover ONLINE liveness stays steady on every instance

  Background:
    Given two broker instances A and B share one store and one shared backend, multi-instance ON
    And node "st-alpha" registers on instance A and heartbeats and polls there
    And instance B mirrors the registry and syncs liveness from the shared backend

  # THE REPRODUCED FLICKER (RED before the fix): B holds a local probe-fail streak for a node
  # whose live poll is on A, and wrongly reads it OFFLINE, collapsing /discover to 0 online.
  Scenario: a peer keeps a live node ONLINE despite its own local probe-fail streak
    Given instance B has accumulated a sustained probe-fail streak for "st-alpha" and does not host its poll
    When a consumer GETs /discover on instance B
    Then instance B lists "st-alpha" as ONLINE
    And instance B reports at least one online offer

  # The steady-state guarantee across BOTH the 20s durable-write throttle window and a probe
  # attempted on B each round: B must never flip the live node to zero-online.
  Scenario: a peer never flips a live node to zero-online across the shared-write throttle and a probe attempt
    Given instance B has accumulated a sustained probe-fail streak for "st-alpha" and does not host its poll
    When instance B computes /discover repeatedly across the shared-write throttle window with a probe attempted each round
    Then every /discover on instance B lists "st-alpha" as ONLINE

  # The veto is NOT removed - the instance that hosts the node's live poll is its authoritative
  # prober and STILL surfaces a genuinely probe-dead node (heartbeating but upstream dead) as
  # OFFLINE, so a consumer never tunes into a dead channel.
  Scenario: the poll-hosting instance still surfaces a probe-dead node OFFLINE
    Given instance A has accumulated a sustained probe-fail streak for "st-alpha"
    When a consumer GETs /discover on instance A
    Then instance A lists "st-alpha" as OFFLINE

  # Liveness derives from the shared write: even when B's LOCAL last-seen has aged past the TTL,
  # a fresh shared liveness value written by A (heartbeat write-through) restores it on the next
  # sync, so a node heartbeating on ANY instance is ONLINE everywhere.
  Scenario: a node heartbeating on A stays ONLINE on B when B's local last-seen has aged past the TTL
    Given instance B's local last-seen for "st-alpha" has aged past the node TTL
    And the shared liveness for "st-alpha" is fresh from instance A
    When instance B syncs liveness and a consumer GETs /discover and /market on instance B
    Then instance B lists "st-alpha" as ONLINE
    And instance B's /market lists a provider for "st-alpha"'s model

  # The genuine-death case: a node that stops heartbeating ages out as OFFLINE on BOTH
  # instances (the shared liveness stops refreshing, so every instance's last-seen crosses the
  # TTL) - the fix does not keep a dead node alive anywhere.
  Scenario: a node that stops heartbeating ages OUT as OFFLINE on both instances
    Given "st-alpha"'s last-seen has aged past the node TTL on both instances
    When a consumer GETs /discover and /market on instance A and instance B
    Then instance A lists "st-alpha" as OFFLINE
    And instance B lists "st-alpha" as OFFLINE
    And neither instance's /market lists a provider for "st-alpha"'s model
