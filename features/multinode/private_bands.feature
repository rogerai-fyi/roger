# Cross-instance PRIVATE BAND mirroring (P1-3 — routing + secrecy). A --private/--freq node
# dials ONE instance; at the 2-instance cap the load balancer can land a tune-in / relay /
# the node's own poll on the OTHER instance. PUBLIC nodes mirror across instances via the
# shared registry, but private regs were EXCLUDED from it (register skipped putNode for
# reg.Private; syncRegistry skipped reg.Private; tunnelFor refused reg.Private) — so a
# private node that registered on A was UNRESOLVABLE + UNROUTABLE on B until B restarted.
#
# THE FIX: mirror private regs too, but under a SEPARATE Valkey namespace (preg:/pregset),
# pulled by syncRegistry into b.nodes with b.private=true. So B can RESOLVE + ROUTE the band,
# while the separate namespace + the private flag keep it OUT of B's PUBLIC /discover + market
# BY CONSTRUCTION (the public allNodes() never sees it). tunnelFor lazily learns a private
# node from that namespace so the node's own poll/result authenticates on B (no re-register
# storm). With no shared backend the private-namespace ops are no-ops (single-instance intact).
#
# SECURITY: the band CODE is never mirrored (it lives only in Postgres, hashed); only the node
# registration (offers + bridge token — the SAME trust level as the public mirror) is shared.
#
# GROUND TRUTH: tunnel.go (register putPrivateNode / syncRegistry private pass / tunnelFor
# private fallback / rehydrate republish), sharedstore.go (putPrivateNode / getPrivateNode /
# allPrivateNodes + markSeen TTL extension), band.go (bandOffers). Enforced by
# private_bands_bdd_test.go on the two-instance harness (real register/sync/pick), no mocks.

Feature: Cross-instance private band mirroring

  Background:
    Given two broker instances A and B sharing one Valkey and one store

  Scenario: A private node registered on A is mirrored into instance B on the sync tick
    Given a private node "p1" is registered on instance A
    When instance B runs its registry sync
    Then instance B knows node "p1" as private with its bridge token

  Scenario: The mirrored private node is RESOLVABLE on instance B
    Given a private node "p1" with a band is registered on instance A
    And instance B has synced the registry
    Then instance B resolves the band to node "p1"'s offers

  Scenario: The mirrored private node is ROUTABLE on instance B
    Given a private node "p1" with a band is registered on instance A
    And instance B has synced the registry
    Then instance B picks node "p1" for model "m" on the band frequency

  # --- secrecy: the band must NEVER leak into a peer's public surfaces ---------
  Scenario: A private node NEVER leaks into instance B's public /discover
    Given a private node "p1" with a band is registered on instance A
    And instance B has synced the registry
    Then node "p1" is absent from instance B's /discover
    And node "p1" is absent from the public shared registry

  # --- the node's own poll on B authenticates without a re-register storm ------
  Scenario: A private node's poll authenticates on instance B before a full sync (tunnelFor)
    Given a private node "p1" is registered on instance A
    Then instance B builds node "p1"'s tunnel on demand with its bridge token

  # --- single-instance safety: the private-namespace ops are guarded no-ops ----
  Scenario: A single-instance broker registers + serves a private node locally
    Given a single-instance broker with no shared backend
    When a private node "p1" is registered on it
    Then that broker knows node "p1" as private with its bridge token
