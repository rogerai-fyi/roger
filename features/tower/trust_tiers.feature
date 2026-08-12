# APPROVED SPEC - founder approved 2026-08-03. Changes to an approved scenario need
# re-approval; they are not a diff to be reviewed.
#
# BUILD STATUS: NOT BUILT. Approval is not implementation - this line says which.
# Enforced by internal/towercore/featurestatus_test.go against the "Contract:"
# references in the code. Changing the status without changing the code fails.
#
# Scope: honest meanings of enrollment, signatures, artifact verification, observed
# behavior, claimed metadata, optional attestation, and public trust labels.

Feature: Tower trust labels say exactly what RogerAI has verified
  Enrollment and cryptography identify keys and bind bytes; they do not turn an independent
  operator, claimed machine, or claimed model into a trusted central authority.

  # --- precise trust meanings ---------------------------------------------

  Scenario: Account-bound enrollment verifies control, not operational truth
    Given an operator completed joined-Tower enrollment
    When Roger Core describes that Tower's identity
    Then it may state that the enrolled account proved possession of the admitted Tower key
    But it does not thereby verify geography, hardware, capacity, software runtime, attached model, token counts, output honesty, uptime, or legal identity beyond the completed account checks

  Scenario: A signature label describes bytes and key purpose
    Given a valid Tower, Station, or Roger Core signature
    When it is shown to a client or operator
    Then the label names the signer identity, key purpose, signed object, Core-observed or ledger-anchored validity time, and verification result
    And it does not call the signed factual claim true merely because the signature verifies

  Scenario: Transit attribution describes the authenticated key rather than physical proof
    Given Roger Core observed bound envelopes on a session authenticated to a Tower key
    And the Tower supplied a consistent signed transit statement
    When that evidence is displayed or used for a compensation candidate
    Then it states that the traffic used the Core session attributed to that Tower identity
    And it does not claim proof of physical packet path, geography, unmodified runtime, or non-collusion

  Scenario: Release verification is not runtime attestation
    Given a Tower downloaded and verified an officially signed artifact
    When its operator has root and later modifies or replaces the running process
    Then the Tower retains no "verified runtime" label solely from download verification
    And RogerAI continues to treat its protocol statements as hostile inputs

  Scenario Outline: Claimed metadata remains visibly declared until an independent oracle exists
    Given a Tower or Station declares "<claim>"
    When Roger Core stores, routes with, or displays it
    Then it is labeled declared separately from observed or attested evidence

    Examples:
      | claim                    |
      | geographic region        |
      | network latency          |
      | bandwidth                |
      | CPU or GPU model         |
      | memory capacity          |
      | concurrency capacity     |
      | model family             |
      | exact model weights      |
      | confidential execution   |
      | software version         |

  # --- behavioral evidence ------------------------------------------------

  Scenario: Health and performance are based on Roger Core observations
    Given a Tower declares perfect availability and low latency
    When authenticated traffic and probes produce different observations
    Then routing health uses bounded recent central observations and policy
    And the declaration does not overwrite measured history

  Scenario: Public canaries are variable and only behavioral evidence
    Given Roger Core probes a joined Tower or Station
    When it chooses canary content, timing, and expected invariants
    Then probes are not one fixed source-visible prompt that can be permanently special-cased
    And passing a probe affects probation or health only within defined bounds
    And it does not prove exact model identity or all future outputs

  Scenario: Failed or inconsistent evidence can quarantine without rewriting history
    Given centrally observed mismatch, replay, fork, latency, availability, or policy evidence crosses an approved threshold
    When Roger Core applies enforcement
    Then new routing weight may fall to zero and the Tower or Station enters quarantine or suspension
    And prior signed evidence and appeal/audit records remain immutable

  Scenario: Anonymous reports are tips, not automatic money authority
    Given an unauthenticated reporter names a Tower, Station, or request
    When RogerAI receives the report without a matching authenticated final receipt
    Then it may retain a bounded abuse tip for review
    But it does not automatically debit, ban, eject, or withhold solely from that report

  Scenario: Receipt-bound reports can be correlated without trusting report claims blindly
    Given an authenticated client reports an attempt and proves possession of its Roger Core receipt
    When policy evaluates the report
    Then Roger Core can correlate it to exact centrally stored evidence
    And enforcement still follows documented thresholds, review, and appeal policy

  # --- optional hardware attestation --------------------------------------

  Scenario: Ordinary joined Towers do not require a TEE in v1
    Given a Tower has valid identity, policy, version, and behavioral state but no hardware attestation
    When admission and routing run
    Then it may enter the baseline joined trust tier
    And it receives no confidential-runtime or exact-model badge

  Scenario Outline: An attested tier requires a complete current policy
    Given a Tower requests an attested trust label
    When "<requirement>" is absent or invalid
    Then the attested label and any attestation-gated work are denied
    And baseline admission is evaluated separately

    Examples:
      | requirement                                  |
      | fresh Roger Core challenge nonce             |
      | genuine supported hardware certificate chain |
      | revocation status                            |
      | minimum TCB                                  |
      | approved measured boot value                 |
      | report-data binding to Tower and session key |
      | unexpired evidence                           |
      | exact policy version                         |

  Scenario: An attestation claim is no broader than its measurement
    Given a valid hardware quote binds an approved boot measurement and session key
    When RogerAI displays its result
    Then it states exactly which hardware, boot components, policy, key, and time were verified
    And it does not infer exact model weights, honest inference output, token accounting, geography, or operator identity unless those facts have separate bound evidence

  # --- sybil and compensation boundary ------------------------------------

  Scenario: Multiple keys do not bypass account-bound Tower quotas
    Given one operator creates many Tower keys or reinstalls repeatedly
    When enrollment policy applies owner limits and recovery history
    Then new keys do not automatically become independent trusted operators or escape suspension

  Scenario: Tower admission alone has no earning authority
    Given a joined Tower is active and relays valid paid Station work
    But it lacks the separately authorized compensated capability
    When trust status and settlement are displayed
    Then Tower trust affects routing and accountability only
    And no compensation, payout identity, tax status, or earning is inferred from Tower admission

  Scenario: Compensated capability verifies eligibility without making operational claims true
    Given an operator has verified payout identity, tax/region eligibility, current terms, and the compensated capability
    When its trust status is displayed
    Then it states only that Roger Core authorized compensation eligibility under a versioned policy
    And the Tower's model, counts, transit statement, host location, runtime, and outputs remain independently untrusted claims

  # --- central authority and transparency ---------------------------------

  Scenario: Roger Core is explicitly the public admission and settlement authority
    Given independent Towers participate in the public network
    When the network's trust model is described
    Then RogerAI is identified as the authority for membership, directory, policy, routing, holds, settlement, revocation, and final receipts
    And independent operation is not described as permissionless consensus or a separate public network

  Scenario: A standalone fork cannot advertise RogerAI public authority
    Given an operator modifies standalone Tower code or creates its own ledger
    When it signs local listings or receipts
    Then those objects carry its separate network and trust root
    And RogerAI clients and trust documents do not validate them as public RogerAI membership

  Scenario: Merkle checkpoints improve auditability without changing governance
    Given Roger Core publishes signed append-only ledger checkpoints
    When a verifier checks inclusion and consistency proofs
    Then it can detect omission or inconsistent history relative to published checkpoints
    And the proofs do not claim decentralized admission, consensus, or custody
