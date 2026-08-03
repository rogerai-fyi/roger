# Lineage receipts — the "model-lineage guarantee": every served request produces a
# per-request UsageReceipt that is SIGNED by the serving node and COUNTER-SIGNED by the
# broker, hash-chained per node, and the settlement is BOUND to that verified receipt so a
# node cannot profit from lying about price or token counts. This is RogerAI's core trust
# differentiator; the scenarios below are the permanent regression net for it.
#
# GROUND TRUTH:
#   - internal/protocol/protocol.go UsageReceipt:
#       nodeSigningBytes() = JSON with GrantID, BrokerPromptTokens, BrokerCompletionTokens,
#         NodeSig, BrokerSig ZEROED — what the NODE signs (it signs before those exist).
#       brokerSigningBytes() = JSON with ONLY NodeSig/BrokerSig zeroed — what the BROKER
#         counter-signs. It is a SUPERSET: the broker's own re-counts decide the bill, so
#         its signature must cover them (they were unsigned before 2026-08-02).
#       Hash() = sha256(nodeSigningBytes) — the NEXT receipt's PrevHash (per-node chain),
#         deliberately on the node form so broker fields never disturb the chain.
#       SignNode/VerifyNode use the node form; SignBroker/VerifyBroker use the broker form.
#       BindsTo(requestID,nodeID) — the receipt must name the job the broker DISPATCHED;
#         settlement keys the hold on rec.RequestID, so an unbound receipt would clear the
#         wrong row and strand the real hold.
#       Cost() = (PromptTokens*PriceIn + CompletionTokens*PriceOut)/1e6.
#       CostWith2(p,c) = bill the SUPPLIED prompt+completion counts (the settle path passes
#         min(claim, broker-recount) per axis).
#   - cmd/rogerai-broker/tunnel.go relay (the settle path):
#       only proceeds if rec.VerifyNode(node.PubKey) AND rec.BindsTo(dispatched job)
#         (forged, wrong-key, foreign, empty-id, or replayed receipt → skipped → the
#         deferred ReleaseHold refunds the consumer in full, nothing settles/earns).
#       rec.PriceIn/PriceOut are OVERWRITTEN with the broker-resolved active price
#         (lock-window protected) — the node's claimed price is ignored for billing.
#       VOID on no usable output (status>=400, empty/whitespace completion, or claimed
#         tokens with no text): $0, hold refunded, owner flagged, but a $0 metering receipt
#         is still SignBroker'd + recorded for the lineage trail.
#       else bills min(claim, recount) on BOTH axes via settleRecountPrompt/settleRecount →
#         BrokerPromptTokens/BrokerCompletionTokens, SignBroker AFTER (so the broker form
#         covers them), cost = CostWith2(billed), capped at the authorized hold maxCost,
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
    And the co-signed receipt is returned on the X-RogerAI-Receipt header

  # --- the broker signature must cover what the broker decides -------------
  #
  # The node and broker sign DIFFERENT canonical forms on purpose. The node cannot
  # sign fields the broker sets after the fact, so the node form excludes them. But
  # the broker form must INCLUDE them: BrokerPromptTokens and BrokerCompletionTokens
  # are what billedTokens() uses to set the bill, and GrantID attributes the spend.
  # A field that decides money and is covered by no signature is a field anyone who
  # can touch a stored or relayed receipt can change for free.

  Scenario: The node signature covers the node-authored fields only
    Given a receipt signed by the node
    When the broker later sets BrokerPromptTokens, BrokerCompletionTokens, and GrantID
    Then the node signature still verifies
    And the receipt hash used as the next PrevHash is unchanged by those broker fields

  Scenario: The broker signature covers the broker-set billing fields
    Given the broker has set BrokerPromptTokens, BrokerCompletionTokens, and GrantID
    When the broker counter-signs the receipt
    Then the broker signature verifies over a canonical form that includes those fields
    And the node signature and the broker signature are over different canonical forms

  Scenario Outline: Tampering with a broker-set field invalidates the broker signature
    Given a fully signed receipt whose broker recount is lower than the node claim
    When "<field>" is altered after the broker counter-signs
    Then the broker signature no longer verifies
    And the tampered receipt cannot be settled

    Examples:
      | field                  |
      | BrokerPromptTokens     |
      | BrokerCompletionTokens |
      | GrantID                |

  Scenario Outline: Tampering with a node-authored field invalidates both signatures
    Given a fully signed receipt
    When "<field>" is altered after both signatures
    Then the node signature no longer verifies
    And the broker signature no longer verifies

    Examples:
      | field            |
      | PromptTokens     |
      | CompletionTokens |
      | PriceIn          |
      | PriceOut         |
      | RequestID        |
      | NodeID           |
      | PrevHash         |

  Scenario: The broker signature is verifiable by a third party
    Given a co-signed receipt and the broker's published public key
    When a consumer verifies it without contacting the broker
    Then VerifyBroker confirms the broker signed exactly the billed counts and grant attribution
    And a receipt carrying no broker signature does not verify

  # --- the receipt must be FOR the job the broker dispatched ---------------
  #
  # A valid node signature proves only that the node signed those bytes. It does not
  # prove the receipt describes the request the broker actually dispatched. Settlement
  # claims the hold keyed on the receipt's own RequestID, so a receipt naming a foreign,
  # empty, or already-used request id makes the broker clear the WRONG hold row: the
  # real hold is never captured and is later swept back to the payer, which is served
  # inference nobody paid for.

  Scenario: A receipt must name the dispatched request and the serving node
    Given the broker dispatched request "req-A" to node "node-1"
    When the node returns a validly signed receipt for request "req-A" from "node-1"
    Then the receipt binds to the dispatched job
    And settlement proceeds against that job's own hold

  Scenario Outline: An unbound receipt is refused by the REAL relay
    Given the node returns a validly signed receipt naming "<defect>"
    When the broker relays the request
    Then settlement does not run and no earning is minted
    And the consumer's pre-authorized hold is refunded in full
    And no co-signed receipt is emitted

    Examples:
      | defect              |
      | another request     |
      | an empty request id |
      | another node        |
      | an empty node id    |

  Scenario Outline: A receipt that does not bind is rejected by the binding rule
    Given the broker dispatched request "req-A" to node "node-1"
    When the node returns a validly signed receipt with "<defect>"
    Then the receipt does not bind to the dispatched job
    And no foreign or absent hold row is cleared

    Examples:
      | defect                              |
      | RequestID naming another request    |
      | an empty RequestID                  |
      | RequestID of an earlier settled job |
      | NodeID naming another node          |
      | an empty NodeID                     |

  Scenario: Replaying an old signed receipt cannot settle a new request
    Given node "node-1" holds a validly signed receipt from an earlier settled request
    When it returns that same receipt for a newly dispatched request
    Then the receipt does not bind to the dispatched job
    And the earlier request's ledger rows are untouched
    And the new request settles nothing and its hold is refunded

  # An unbound receipt means the work was served and cannot be billed. Refusing it
  # silently lets a broken or hostile node do that indefinitely, so it accrues evidence
  # against the owner exactly like the other provable violations.

  Scenario: An unbound receipt accrues a strike against the node's owner
    Given the node returns a validly signed receipt naming "another request"
    When the broker relays the request
    Then a "receipt-unbound" strike is recorded against the node's owner
    And the strike evidence names the dispatched request and the returned request

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

  # The chain is per NODE, so one process serving several nodes must keep them
  # separate. A single process-wide "last hash" interleaves unrelated nodes into one
  # chain, which makes every link wrong for both of them and destroys the audit value
  # the chain exists to provide.

  Scenario: Two nodes served by one process keep independent chains
    Given one agent process serves node "node-A" and node "node-B"
    When each node produces two receipts in an interleaved order
    Then each node's second receipt chains to that SAME node's first receipt
    And neither node's chain contains a hash produced by the other node

  Scenario: A node's first receipt opens its chain
    Given a node that has produced no receipts yet
    When it produces its first receipt
    Then that receipt's PrevHash is empty
    And it does not inherit a hash from any other node

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
