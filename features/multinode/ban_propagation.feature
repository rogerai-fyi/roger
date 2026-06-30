# Cross-instance BAN propagation (P0-2 — money + moderation). RogerAI runs at a 2-instance
# cap behind one shared Valkey + one shared Postgres. Node + owner bans were per-instance
# in-memory maps: a ban PERSISTED to the store and flipped the LOCAL map on the banning
# instance, but it was NOT mirrored to the peer. So a ban applied on instance A was invisible
# to instance B until B restarted/rehydrated — a reported NODE kept SERVING and a banned
# OWNER kept getting PICKED + SETTLED (earning) on B. That is a config-independent gap: both
# instances already share Valkey + the registry mirror; bans simply were not part of it.
#
# THE FIX (lightest, reuses the existing shared-store counters + sync tick): every ban/unban
# bumps a shared monotonic revision counter (rogerai:ctr:ban:rev). Each instance compares it
# on the EXISTING ~5s liveness sync tick and, on a change, RE-PULLS the durable banned-node
# + banned-owner sets from the store and REPLACES its local caches (replace, not merge — so
# an UNBAN propagates too). Net: a ban on instance A takes effect on instance B within one
# sync interval, with NO restart and NO Valkey round-trip on the hot pick/discover path.
#
# GROUND TRUTH: cmd/rogerai-broker/report.go (banNode / unbanNode / nodeBanSweepOnce),
# strikes.go (banOwner), tunnel.go (syncLivenessOnce → syncBanRev; pickFor ban filters),
# market.go (computeDiscover node-ban filter), grant.go (settleRequest owner-ban backstop).
# Enforced by: cmd/rogerai-broker/ban_propagation_bdd_test.go — TWO real broker instances
# sharing ONE in-process Valkey (miniredis) + ONE store, no mocks.

Feature: Cross-instance ban propagation

  Background:
    Given two broker instances A and B sharing one Valkey and one store

  # --- node ban: a reported node stops serving on the peer ---------------------
  Scenario: A node ban on instance A drops the node from pick on instance B
    Given node "n1" offering model "m" is registered on both instances
    When instance A bans node "n1"
    And instance B runs its liveness sync tick
    Then instance B no longer picks node "n1" for model "m"

  Scenario: A node ban on instance A removes the node from /discover on instance B
    Given node "n1" offering model "m" is registered on both instances
    When instance A bans node "n1"
    And instance B runs its liveness sync tick
    Then node "n1" is absent from instance B's /discover

  # --- owner ban: a banned operator stops being picked AND settled on the peer -
  Scenario: An owner ban on instance A drops the owner's node from pick on instance B
    Given node "n2" owned by operator "op-1" offering model "m" is registered on both instances
    When instance A bans owner "op-1"
    And instance B runs its liveness sync tick
    Then instance B no longer picks node "n2" for model "m"

  Scenario: An owner ban on instance A mints NO earning when node "n2" settles on instance B
    Given node "n2" owned by operator "op-1" offering model "m" is registered on both instances
    And a funded consumer
    When instance A bans owner "op-1"
    And instance B runs its liveness sync tick
    And the consumer settles a paid request served by node "n2" on instance B
    Then node "n2" accrues no earning
    And the consumer is still billed for the request

  # --- the propagation is bounded by the tick (not instantaneous, not a restart) ----
  Scenario: The ban is invisible on instance B until its next sync tick
    Given node "n1" offering model "m" is registered on both instances
    When instance A bans node "n1"
    Then instance B still picks node "n1" for model "m" before its next sync tick

  # --- UNBAN propagates too (proves the re-pull REPLACES, never only adds) ------
  Scenario: An unban on instance A restores routing on instance B
    Given node "n1" offering model "m" is registered on both instances
    And instance A has banned node "n1" and instance B has synced the ban
    When instance A unbans node "n1"
    And instance B runs its liveness sync tick
    Then instance B picks node "n1" for model "m" again

  # --- an owner FORGIVE (admin unban) must propagate too (the owner unban path) -
  Scenario: An owner forgive on instance A restores the operator on instance B
    Given node "n2" owned by operator "op-1" offering model "m" is registered on both instances
    And instance A has banned owner "op-1" and instance B has synced the ban
    When instance A forgives owner "op-1"
    And instance B runs its liveness sync tick
    Then instance B picks node "n2" for model "m" again

  # --- the auto-lift sweep is a ban WRITE too: it must bump the revision --------
  Scenario: An auto-lifted report suspension on instance A restores routing on instance B
    Given node "n1" offering model "m" is registered on both instances
    And instance A has report-banned node "n1" and instance B has synced the ban
    When instance A's node-ban sweep auto-lifts the suspension
    And instance B runs its liveness sync tick
    Then instance B picks node "n1" for model "m" again

  # --- single-instance safety: the rev bump is a guarded no-op (no panic) -------
  Scenario: A single-instance broker bans locally with no shared backend
    Given a single-instance broker with no shared backend
    When the single-instance broker bans node "n1"
    Then node "n1" is banned on that broker with no error
