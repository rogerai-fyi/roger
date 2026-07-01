# APPROVED by the founder 2026-07-01 (spec-first workflow step 3) - step definitions +
# implementation may now proceed. Written from the 2026-07-01 launch audit.
#
# Spec-first behavior contract closing P2-5 (cross-instance confidential attestation
# trust) - open since the 2026-06-30 review, re-confirmed by the 2026-07-01 audit:
# the multi-instance mirror publishes the node's signed CLAIM (reg.Confidential),
# never the broker's verified VERDICT. verifyRegistration computes the verdict into a
# local var (tunnel.go ~:289) and b.confidential locally (:390), but reg.Confidential
# is never overwritten before json.Marshal(reg) -> putNode (:438-447). Peers then do
# b.confidential[id] = reg.Confidential on sync (:755), private-band sync (:790), and
# lazy tunnelFor learn (:847). Under ROGERAI_TEE_REQUIRE=0 (today's default) a node
# that FAILED attestation on instance A is downgraded locally yet mirrored with
# Confidential=true - instance B grants it the ◆ badge and confidential routing.
#
# THE FIX THIS SPEC PINS: the mirror carries the broker's VERDICT. Minimum: overwrite
# reg.Confidential with the verified verdict before the shared-store put, so peers
# ingest what the entry instance PROVED, not what the node claimed. (The stronger
# broker-signed verdict record is rung 2; these scenarios hold for both.)
#
# Invariants:
#   A1 the shared store NEVER carries Confidential=true for a node whose attestation
#      did not verify on the instance that admitted it.
#   A2 a peer's ◆ badge / confidential routing / re-attest clock derive from the
#      mirrored VERDICT (A1), so claim-vs-verdict can never diverge across instances.
#   A3 ROGERAI_TEE_REQUIRE=1 behavior is unchanged: failed claim -> rejected outright.
#   A4 verified nodes keep working across instances exactly as today (no regression:
#      the ◆ tier still mirrors - as the verdict).

Feature: The cross-instance mirror carries the attestation verdict, never the raw claim

  Background:
    Given a two-instance broker pair "A" and "B" sharing a store
    And ROGERAI_TEE_REQUIRE is 0

  Scenario: a node whose attestation FAILS on A is mirrored as NOT confidential
    Given node "sneaky" registers on A claiming Confidential with a quote that fails verification
    When A admits it (require=0 downgrades instead of rejecting)
    Then the shared-store record for "sneaky" has Confidential=false
    And B never grants "sneaky" the confidential tier
    And B never routes confidential-only traffic to "sneaky"

  Scenario: a node whose attestation VERIFIES on A keeps its tier on B (no regression)
    Given node "fortress" registers on A with a quote that verifies
    Then the shared-store record for "fortress" has Confidential=true
    And B grants "fortress" the confidential tier on sync
    And B seeds its re-attestation clock

  Scenario: the lazy tunnelFor learn path also ingests the verdict
    Given node "sneaky" (failed verdict) is only in the shared store
    When B first learns of "sneaky" via tunnelFor
    Then B records it as NOT confidential

  Scenario: the private-band sync path also ingests the verdict
    Given node "sneaky" (failed verdict) registered on a private band
    When B syncs private bands
    Then B records it as NOT confidential

  Scenario: a poisoned mirror record cannot confer the tier
    Given the shared-store record for "sneaky" is hand-edited to Confidential=true but A's verdict for it was false
    When A re-registers or re-attests "sneaky"
    Then the shared-store record is overwritten back to the verdict (false)
    # (Full tamper-proofing of the store itself is the rung-2 signed verdict record.)

  Scenario: require=1 still rejects a failed claim outright on the entry instance
    Given ROGERAI_TEE_REQUIRE is 1
    When node "sneaky" registers on A claiming Confidential with a failing quote
    Then the registration is rejected and nothing is mirrored

  Scenario: a node claiming NOTHING is never upgraded by the mirror
    Given node "plain" registers on A with no confidential claim
    Then the shared-store record has Confidential=false
    And B treats it as non-confidential

  Scenario: re-attestation flipping the verdict updates the mirror
    Given node "fortress" was mirrored Confidential=true
    When its scheduled re-attestation on A fails
    Then A downgrades it AND the shared-store record flips to Confidential=false
    And B drops its confidential tier on the next sync
