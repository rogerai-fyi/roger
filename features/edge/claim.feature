# ROGER EDGE - ADOPT A CANDIDATE, AND LET IT CLAIM MEMBERSHIP (PROPOSED).
#
# STATUS: PROPOSED 2026-09-23, founder-chosen ("b, more realistic use case and better ux").
# Implemented alongside this spec.
#
# WHY: the phone should JOIN by being adopted from the machine that owns the Edge - the UniFi
# gesture - not by the owner typing the authority's address into the phone. A phone advertises
# itself as a CANDIDATE (features/edge/agents.feature §2, EdgeAdvertiser on iOS). Today, adopting a
# candidate only records a sighting: a non-serving, un-enrolled phone then fails verification and
# goes dark, and the advert never carried the account key the authority needs to authorize an
# enrolment. So adoption did not actually make the phone a member.
#
# THE HANDSHAKE THIS ADDS - a candidate becomes a real, certificated member by CLAIM:
#   1. The phone advertises as a candidate (node id derived from its node key; no account, no cert).
#   2. The owner ADOPTS it on the authority (`roger edge adopt <node id>`, or the [3] adopt key).
#      Adopting a candidate now records a CLAIM GRANT for that node id - the owner's decision IS the
#      authorization, exactly as `authority allow` is for the typed-address path.
#   3. The phone, still advertising, polls the authority's /edge/claim. Once granted, it proves
#      possession of its node key and receives a certificate - the same certificate an enrolment
#      issues, under the same root and account (edge-local).
#   4. The phone then PRESENTS (features/edge/presence.feature) and is drawn as a verified member.
#
# THE TRUST: a claim is authorized by (the owner's adopt grant, keyed by node id) AND (a signature
# by the node key the id derives from). An attacker who sees the advert has the node id but not the
# node key, so cannot claim; and nothing is granted that the owner did not adopt. The grant is
# CONSUMED on a successful claim - one adopt, one certificate.
#
# GROUND TRUTH: internal/edgeauth/claim.go (ClaimRequest), internal/edgeauth/local.go (GrantClaim /
# ClaimGranted / ConsumeClaim / Claim), internal/edgeauth/enrollhttp (ClaimPath, ClaimHandler),
# cmd/rogerai/edgehost.go (mounts it; adopt creates the grant), features/edge/presence.feature.
#
# Tags: @edge (grant + claim), @security (the trust checks).

Feature: The owner adopts a candidate from the authority, and the candidate claims its certificate

  @edge
  Scenario: adopting a candidate grants it a claim, and the claim issues a certificate
    Given a phone advertising as a candidate with node id N
    When the owner adopts N on the authority
    And the phone claims with a signature by its node key
    Then the authority issues it a certificate under the Edge root and account
    And the grant is consumed, so a second claim with the same node key is refused

  @security
  Scenario: a claim for a node the owner did not adopt is refused
    Given a phone advertising as a candidate that the owner has NOT adopted
    When it claims
    Then the authority refuses it, issuing nothing

  @security
  Scenario: a claim whose node id does not match its node key is refused
    Given an adopted node id N
    When a claim presents node id N but a signature by a DIFFERENT key
    Then it is refused as an identity mismatch, and nothing is issued

  @security
  Scenario: a claim with a bad or missing signature is refused
    Given an adopted node id N
    When a claim for N carries no valid signature by N's node key
    Then it is refused, and nothing is issued

  @security
  Scenario: a stale claim is refused
    Given an adopted node id N
    When a claim for N is timestamped far outside the freshness window
    Then it is refused as stale

  @edge
  Scenario: a claimed candidate then presents and is drawn as a verified member
    Given a candidate the owner adopted and that has claimed its certificate
    When it presents with that certificate
    Then it is recorded as a verified member, drawn like any other node
    And the whole join needed no address typed on the phone and no manual allow
