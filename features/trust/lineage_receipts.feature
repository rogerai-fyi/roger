# Lineage receipts — the "model-lineage guarantee": every served request produces a
# per-request UsageReceipt that is SIGNED by the serving node and COUNTER-SIGNED by the
# broker, hash-chained per node, and the settlement is BOUND to that verified receipt so a
# node cannot profit from lying about price or token counts. This is RogerAI's core trust
# differentiator; the scenarios below are the permanent regression net for it.
#
# GROUND TRUTH:
#   - internal/protocol/protocol.go UsageReceipt:
#       signingBytes() = JSON of the receipt with GrantID, BrokerPromptTokens,
#         BrokerCompletionTokens, NodeSig, BrokerSig ZEROED — the canonical shape BOTH
#         parties sign (so broker-set fields never break the node signature).
#       Hash() = sha256(signingBytes) — used as the NEXT receipt's PrevHash (per-node chain).
#       SignNode(priv) / SignBroker(priv) sign signingBytes; VerifyNode(pubHex) verifies NodeSig.
#       Cost() = (PromptTokens*PriceIn + CompletionTokens*PriceOut)/1e6.
#       CostWith2(p,c) = bill the SUPPLIED prompt+completion counts (the settle path passes
#         min(claim, broker-recount) per axis).
#   - cmd/rogerai-broker/tunnel.go relay (the settle path):
#       only proceeds if rec.VerifyNode(node.PubKey) (forged/wrong-key receipt → skipped →
#         the deferred ReleaseHold refunds the consumer in full, nothing settles/earns).
#       rec.PriceIn/PriceOut are OVERWRITTEN with the broker-resolved active price
#         (lock-window protected) — the node's claimed price is ignored for billing.
#       VOID on no usable output (status>=400, empty/whitespace completion, or claimed
#         tokens with no text): $0, hold refunded, owner flagged, but a $0 metering receipt
#         is still SignBroker'd + recorded for the lineage trail.
#       else bills min(claim, recount) on BOTH axes via settleRecountPrompt/settleRecount →
#         BrokerPromptTokens/BrokerCompletionTokens, SignBroker AFTER (covers the same
#         canonical bytes), cost = CostWith2(billed), capped at the authorized hold maxCost,
#         settleRequest (idempotent on RequestID); the co-signed receipt rides X-RogerAI-Receipt.
#       settle failure → settled stays false → hold refunded (fail safe toward the consumer).

Feature: Every served request yields a node-signed, broker-co-signed, hash-chained lineage receipt that binds settlement

  Background:
    Given a registered node with an ed25519 keypair
    And a funded consumer with a pre-authorized hold

  # --- the receipt is node-signed over the canonical bytes ------------------

  Scenario: A served request yields a receipt whose node signature verifies
    When the node serves the request and signs the receipt with its key
    Then VerifyNode against the node's registered pubkey succeeds

  Scenario: Broker-set fields are excluded from the signed bytes
    Given a node-signed receipt
    When the broker sets GrantID, BrokerPromptTokens, and BrokerCompletionTokens
    Then the node signature still verifies (those fields are zeroed in signingBytes)

  Scenario Outline: Tampering any node-signed field breaks the node signature
    Given a node-signed receipt
    When the field "<field>" is altered after signing
    Then VerifyNode fails

    Examples:
      | field             |
      | Model             |
      | User              |
      | PromptTokens      |
      | CompletionTokens  |
      | PriceIn           |
      | PriceOut          |
      | PrevHash          |
      | TS                |
      | RequestID         |
      | NodeID            |

  # --- the broker counter-signs ONLY a node-verified receipt ---------------

  Scenario: The broker co-signs a verified receipt and returns it
    When the broker relays a request whose returned receipt verifies
    Then the broker counter-signs it (BrokerSig)
    And both the node and broker signatures verify over the same canonical bytes
    And the co-signed receipt is returned on the X-RogerAI-Receipt header

  Scenario: A forged receipt is not honored
    Given the node returns a receipt signed with the WRONG key
    When the broker relays the request
    Then settlement does not run and no earning is minted
    And the consumer's pre-authorized hold is refunded in full
    And no co-signed receipt is emitted

  # --- the per-node hash chain is tamper-evident ---------------------------

  Scenario: A receipt's hash is the next receipt's prev-hash
    Given a node-signed receipt R1 with hash H1
    When the node produces the next receipt R2 for the same node
    Then R2.PrevHash equals H1

  Scenario: Altering a signed field changes the receipt hash
    Given a receipt with hash H
    When any node-signed field is altered
    Then Hash no longer equals H (the chain link is broken)

  # --- settlement binds to the VERIFIED receipt (no profiting from lies) ---

  Scenario: The broker bills its resolved price, not the node's claimed price
    Given the node returns a receipt claiming an inflated price_out
    When the broker relays and settles
    Then the consumer is billed at the broker-resolved active price
    And the receipt's PriceIn/PriceOut are the broker's price, not the node's claim

  Scenario: An over-reporting node is billed on the verified lesser count, both axes
    Given the node claims more prompt and completion tokens than the broker re-count
    When the broker relays and settles
    Then the cost uses min(claim, broker-recount) on BOTH the input and output axes

  Scenario: Capture never exceeds the authorized hold
    Given the settled cost would exceed the pre-authorized hold
    When the broker settles
    Then the captured cost is clamped to the authorized hold amount

  Scenario: A settle failure fails safe toward the consumer
    Given the ledger settle returns an error
    When the broker relays the request
    Then the consumer's hold is refunded and no billing headers are emitted

  # --- void on no usable output --------------------------------------------

  Scenario Outline: A request with no usable output is voided but still recorded
    Given the node returns "<shape>"
    When the broker relays the request
    Then the consumer is charged 0 and the hold is refunded in full
    And the owner is flagged for the empty output
    And a $0 metering receipt is still broker-co-signed and recorded for the lineage trail

    Examples:
      | shape                              |
      | an error status (>=400)            |
      | an empty / whitespace completion   |
      | no output text and zero tokens     |

  # Usage backstop (thinking-model fix): empty output TEXT but the node's usage reports
  # completion tokens is NOT a no-output void - a reasoning model produced real tokens per its
  # own accounting even when the visible text was not captured. It is billed off the reported
  # tokens and the honest owner is NOT struck. Voiding this false-struck + auto-banned honest
  # reasoning nodes. (The TRUE-negative above - no text AND zero tokens - still voids + strikes.)
  Scenario: Empty text with reported completion tokens is billed, not voided or struck
    Given the node returns "empty text but usage reports completion tokens"
    When the broker relays the request
    Then the consumer is billed a non-zero cost and the node earns
    And the owner is NOT flagged for empty output

  # --- idempotent settlement on request id ---------------------------------

  Scenario: Replaying a receipt settles once
    Given a receipt that has already settled for its request id
    When the same receipt is submitted again
    Then the wallet is debited only once and the earning is minted only once

  # --- cost math -----------------------------------------------------------

  Scenario: Cost is (in*price_in + out*price_out) / 1e6
    Given a receipt with 1000 prompt tokens at 2.00 /1M in and 500 completion tokens at 6.00 /1M out
    Then Cost is 0.005

  Scenario: CostWith2 bills the supplied verified counts, not the claimed ones
    Given a receipt claiming 1000 completion tokens at 6.00 /1M out
    When settlement bills a broker-verified 400 completion tokens
    Then the billed cost reflects 400 completion tokens, not 1000
