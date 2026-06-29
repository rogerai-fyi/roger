# BANNING (strikes -> ban): how a misbehaving operator is graded out of the marketplace. Bad
# behavior accrues STRIKES against the node's OWNER (not just the node, so a fresh node id can't
# dodge it). Strikes are idempotent, some kinds need corroboration before they count, egregious
# (zero-doubt) strikes count immediately, and strikes DECAY so a reformed operator recovers. At a
# warn threshold the owner is warned; at a ban threshold the owner is durably banned.
#
# GROUND TRUTH (cmd/rogerai-broker/strikes.go + internal/store/safety.go):
#   strike(nodeID, kind, idemKey, zeroDoubt, evidence): records a strike against the node's owner
#     (idemKey = idempotent; zeroDoubt = counts now without corroboration; else needs
#     strikeCorroborateKinds distinct kinds). Thresholds: strikeWarnAt, strikeBanAt. Strikes age
#     out after strikeDecayDays. banOwner(accountID, reason, evidence) durably bans the owner.
#   OwnerStrike(account, kind, evidence, idemKey) -> count (store).
#
# Enforced by: cmd/rogerai-broker/strikes_test.go + internal/store safety tests.

Feature: Banning — strikes graded to a ban

  Scenario: A strike accrues against the node's OWNER, not just the node
    Given node "n1" owned by operator "op-1"
    When "n1" earns a strike
    Then the strike is recorded against "op-1" (a fresh node id won't shed it)

  Scenario: Strikes are idempotent on their key
    Given a strike with idem-key "k1" was recorded for "op-1"
    When the same strike "k1" is submitted again (a retry / webhook redelivery)
    Then the strike count does not increase

  Scenario: A warn threshold warns before banning
    Given "op-1" reaches strikeWarnAt strikes
    Then the operator is WARNED (not yet banned)

  Scenario: The ban threshold durably bans the owner
    Given "op-1" reaches strikeBanAt strikes
    Then banOwner records a durable ban with the reason + evidence
    And (per routing eligibility) all of "op-1"'s nodes are excluded from routing

  Scenario: Ambiguous strike kinds need corroboration before they count
    Given a strike kind that requires corroboration
    When only ONE such signal exists
    Then it does not yet count toward a ban (needs strikeCorroborateKinds distinct kinds)

  Scenario: An egregious zero-doubt strike counts immediately
    Given a zero-doubt strike (e.g. a confirmed-abuse signal)
    Then it counts without waiting for corroboration

  Scenario: Strikes decay so a reformed operator recovers
    Given "op-1" has old strikes older than strikeDecayDays
    Then those aged strikes no longer count toward the thresholds
