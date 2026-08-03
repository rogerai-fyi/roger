# PROPOSED SPEC — founder approval is required before step definitions or implementation.
#
# Scope: provider-neutral authentication and reconciliation for external cash and payout-rail
# events used by Tower compensation. No provider webhook, Tower, Station, or client is money
# authority. Tests use the real configured payment adapter and real ledger dependencies.

Feature: Only authenticated reconciled external events can change Tower compensation or payout
  Push notifications are bounded hints. Roger Core derives authoritative payment and payout
  revisions by authenticating the configured merchant-scoped provider API and exact source.

  Background:
    Given each external adapter has a purpose-scoped merchant/account identity, TLS trust policy, endpoint allowlist, credential version, and bounded timeout
    And webhook credentials, authenticated-fetch credentials, and payout authorization keys are distinct

  Scenario: A valid payment webhook schedules reconciliation but moves no money
    Given a supported provider sends a webhook whose signature, timestamp, endpoint role, merchant binding, event ID, and raw-body digest verify
    When Roger Core atomically records its unique event ID and canonical raw-body hash
    Then it schedules an authenticated fetch of the named payment source and revision
    And the webhook alone does not mark cash captured, mature, refunded, disputed, fee-final, or compensation-eligible

  Scenario Outline: An invalid payment webhook has no authority
    Given a payment webhook has "<defect>"
    When webhook ingress validates it
    Then it neither changes payment/compensation state nor schedules an attacker-selected fetch
    And a bounded redacted rejection is recorded

    Examples:
      | defect |
      | missing provider authentication |
      | invalid provider authentication |
      | signature over normalized rather than exact raw bytes |
      | wrong endpoint or event purpose |
      | timestamp older than the admitted replay window |
      | timestamp too far in the future |
      | unknown credential version |
      | retired credential outside its overlap window |
      | wrong merchant or platform account |
      | missing, malformed, or oversized event ID |
      | a source ID from another merchant account |
      | body above the ingress limit |
      | invalid content type or canonical envelope |
      | a request above the source or merchant rate limit |

  Scenario: Webhook secret rotation has one bounded overlap
    Given webhook credential version W is active and W+1 is authorized to replace it
    When the signed rotation window opens and closes
    Then both exact versions verify only during the bounded overlap
    And W is rejected after the close while W+1 remains purpose- and merchant-scoped
    And an unknown, rolled-back, reused-across-purpose, or prematurely retired version moves no state

  Scenario Outline: Webhook replay and mutation are distinguished
    Given webhook event ID E was stored with raw-body hash H
    When Roger Core receives "<delivery>"
    Then the result is "<result>"

    Examples:
      | delivery | result |
      | exact E and H sequentially | idempotent acknowledgement; at most one reconciliation trigger remains |
      | exact E and H concurrently | one unique ingress record and one coalesced reconciliation trigger |
      | E with another raw-body hash | conflict quarantine; no payment state is guessed |
      | another event ID with identical bytes where the provider declares IDs unique | distinct authenticated hint; authoritative fetch still prevents duplicate money |

  Scenario: Authenticated provider fetch creates one authoritative payment revision
    Given a valid ingress hint or scheduled reconciliation names a configured payment source
    When Roger Core uses the purpose-scoped credential and pinned adapter endpoint to fetch it
    Then the authenticated response binds provider-neutral adapter, merchant/account, source ID, source kind, currency/unit/scale, original principal, cumulative captured principal, cumulative refunds, dispute categories and outcomes, cumulative fees and fee-finality, canonical provider revision, ProviderPaymentEventIDSetV1, and provider-observed time claims
    And Roger Core range-checks and canonicalizes every field before committing one strict AuthoritativePaymentRevisionV1 with monotonic Core source-revision sequence and complete hash
    And compensation consumes those committed revisions only through the exact exhaustive AuthoritativePaymentRevisionSetV1 bound to its SettlementReceiptV2 funding allocation

  Scenario: Fee-finality policy fixes a finite deadline without trusting provider time
    Given a supported provider/source-kind adapter has a signed fee-finality policy
    When Roger Core validates it for compensated traffic
    Then the policy binds provider-neutral adapter and source kind, currency/scale scope, positive maximum interval, retry and incident policy versions, effective Core tuple, expiry, and signer purpose
    And each captured source binds an immutable deadline derived from its Core capture-commit tuple plus that interval
    And equality at the deadline is timed out using the global Core authority sequence while provider timestamps cannot extend it
    And an absent, zero, negative, noncanonical, overflowing, expired, or unbounded interval makes that adapter ineligible for positive Tower compensation

  Scenario Outline: An authenticated fetch response still fails closed on context
    Given the provider connection authenticates but the response has "<defect>"
    When Roger Core reconciles it
    Then no new payment or compensation revision commits
    And the source remains pending-reconciliation or conflict-quarantined without a guessed value

    Examples:
      | defect |
      | merchant or platform account mismatch |
      | payment source ID mismatch |
      | unexpected source kind |
      | currency or scale mismatch |
      | negative or overflowing principal, capture, refund, fee, or dispute amount |
      | cumulative refund above cumulative captured principal |
      | capture above original authorized principal without an explicit supported adjustment |
      | fee state declared final with an internally inconsistent amount |
      | provider revision lower than the committed revision |
      | equal revision with different canonical bytes |
      | missing required provider event lineage |
      | response body above the configured maximum |
      | redirect or resolved address outside the pinned adapter endpoint policy |
      | invalid TLS service identity |

  Scenario Outline: Push and pull disagreement has one authority
    Given a valid webhook claims "<push claim>"
    But the authenticated provider fetch reports "<pull state>"
    When reconciliation runs
    Then the committed payment state is "<result>"
    And the disagreement is retained without treating the push claim as money authority

    Examples:
      | push claim | pull state | result |
      | captured | authorization only | authorization only; no mature cash |
      | no refund | cumulative partial refund | cumulative partial refund |
      | dispute won | dispute still open | open dispute; payout held |
      | fee final | fee pending | fee pending; no positive compensation delta |
      | capture failed | captured under the same source and merchant | captured subject to maturity and every other check |

  Scenario: Provider unavailability cannot turn a hint into authoritative cash
    Given a valid webhook was recorded but authenticated fetch times out, rate-limits, fails TLS, or returns an ambiguous response
    When reconciliation reaches its bounded retry policy
    Then the source stays pending with the stable reconciliation key and no positive compensation delta
    And retries use bounded backoff without changing merchant, endpoint, source, or credential purpose

  Scenario Outline: Fee finality at its ceiling opens exactly one incident
    Given captured source S remains fee-pending, is reached by at least one current compensation aggregate, and its finality deadline has "<relation>" the sweep authority tuple
    When the durable deadline sweep runs concurrently, repeatedly, or after restart
    Then the result is "<result>"

    Examples:
      | relation | result |
      | not yet reached | source remains pending with no incident and no positive compensation delta |
      | exactly reached | one open FeeFinalityIncidentV1 commits and the source remains pending with no positive compensation delta |
      | already passed | the same one incident is returned or created once and the source remains pending with no positive compensation delta |

  Scenario: FeeFinalityIncidentV1 has one exact signed authority
    Given a captured source reached by at least one current compensation aggregate reached its fee-finality deadline without an authoritative final fee
    When the purpose-separated fee-incident service commits FeeFinalityIncidentV1
    Then its signature binds schema/network/protocol, fee-incident signer key, incident ID/state/revision/prior hash, provider-neutral adapter/merchant/source identity hashes, currency/unit/scale, last AuthoritativePaymentRevisionV1 sequence/complete hash, fee-finality policy version/hash, capture and deadline Core tuples, FeeFinalityAffectedAggregateSetV1 complete hash, stable reconciliation key, closed reason, issue Core tuple, and recovery revision/hash or canonical absence
    And exact replay returns the incident while equal revision with different bytes is conflict-quarantined
    And no webhook, provider timestamp, operator, Tower, administrator, compensation signer, or incident signer may invent a final fee or positive entitlement

  Scenario Outline: Fee-finality incident recovery is exhaustive
    Given FeeFinalityIncidentV1 is "<state>"
    When "<event>"
    Then the outcome is "<outcome>"

    Examples:
      | state | event | outcome |
      | open | authenticated monotonic provider revision reports a final fee | atomically close incident, reconcile one cumulative compensation delta, and release only otherwise-clear holds |
      | open | provider still reports fee pending or omits required finality | remain open, raise adapter readiness/incident status, and create no positive delta |
      | open | provider permanently declares finality unsupported | remain terminal-in-v1 unresolved_unsupported, disable new compensated allocations through that adapter, and create no positive delta |
      | open | equal provider revision has different canonical bytes | become conflict_quarantined with no money movement |
      | resolved | exact recovery replay arrives | return the committed result without another delta or hold release |
      | resolved | stale or conflicting recovery arrives | reject without reopening or changing money |

  Scenario: An authenticated payout callback may schedule one bounded reconciliation fetch
    Given Roger Core sent a verified TowerPayoutInstructionV1 with stable rail idempotency key K
    When a callback passes the configured callback-authentication version and binds the exact platform account, payout provider ID, K, payout instruction hash, event ID, and bounded timestamp window
    Then it records that untrusted result hint idempotently and schedules one purpose-authenticated fetch of K and the provider payout ID
    And callback replay schedules no unbounded or duplicate work
    And only a fetched result bound to the configured platform account, K, payout instruction hash, destination fingerprint, currency, and exact net amount can commit succeeded or confirmed-failed
    And neither callback nor fetched rail status can create, clear, or revise operator payout eligibility

  Scenario: A missing or ambiguous payout send response schedules reconciliation from local authority
    Given reserved_submitted and its verified TowerPayoutInstructionV1 committed before the rail call
    When the send response is missing, times out, or is ambiguous
    Then Roger Core schedules a purpose-authenticated merchant-scoped fetch from its committed instruction and stable idempotency key
    And it never requires or invents a callback and never sends a second instruction

  Scenario Outline: An invalid payout callback schedules no fetch or state change
    Given a payout callback has "<defect>"
    When bounded callback ingress validation runs
    Then no reconciliation fetch, rail call, lot transition, destination change, or detailed response is produced
    And only a bounded redacted invalid-callback audit event is recorded

    Examples:
      | defect |
      | missing, malformed, or invalid callback authentication |
      | authentication under a closed credential version |
      | wrong platform or merchant account |
      | unknown or mismatched rail idempotency key |
      | payout provider ID or instruction hash mismatch |
      | missing, replay-conflicting, stale, or future event ID/timestamp |
      | amount, currency, scale, or destination supplied outside the authenticated callback schema |
      | oversized, duplicate-field, unknown-field, or invalidly encoded body |

  Scenario Outline: An invalid payout result cannot release or mark reserved lots paid
    Given payout reconciliation receives "<defect>"
    When it validates the fetched rail result
    Then reserved_submitted lots remain reserved_submitted with rail result unknown and no second instruction is sent
    And no paid, released, clawback, or destination change is committed

    Examples:
      | defect |
      | wrong platform or merchant account |
      | unknown or mismatched rail idempotency key |
      | payout provider ID already bound to another instruction |
      | payout instruction hash mismatch |
      | destination fingerprint mismatch |
      | currency, scale, or amount mismatch |
      | success and failure both claimed at one revision |
      | stale revision after a newer result |
      | equal revision with different bytes |
      | transient, timeout, pending, or unknown result presented as confirmed failure |

  Scenario: Conflicting payout results quarantine rather than double-send
    Given one authenticated rail revision says succeeded and another valid-looking response conflicts
    When Roger Core cannot establish a monotonic authoritative provider state
    Then the payout remains conflict-quarantined with its lots unavailable to another payout
    And manual/provider reconciliation references the original stable key and instruction

  # --- zero-withholding tax authority -------------------------------------

  Scenario: TaxProfileFactV1 is the independently signed current tax input
    Given an operator's tax profile is evaluated from verified identity, jurisdiction, and tax evidence
    When the purpose-separated tax-profile service commits TaxProfileFactV1
    Then it signs only schema/network/protocol, tax-profile-fact signer key ID, deterministic operator-scoped stable series ID, positive revision, immediate prior complete hash or canonical first absence, operator ID, payout-identity verification version, tax-jurisdiction code, tax-profile version/evidence complete hash, tax-ruleset version/complete hash, verified or review_required or invalid result, effective Core tuple, finite expiry Core tuple, issue tuple, and independently assigned tax-profile-ledger Core commit tuple
    And the stable series ID derives from strict JCS [TaxProfileFactV1-series-v1,network-ID,operator-ID], so an identity, jurisdiction, profile, ruleset, result, or evidence change advances that same head rather than creating a parallel series
    And revision 1 has prior absence, each successor is current revision plus one with immediate prior hash under CAS, exact replay is idempotent, and no signer-controlled issue/evidence time grants authority
    And effective is no earlier than the independently assigned fact-ledger commit tuple, and activation of any named identity, jurisdiction, profile, or ruleset input atomically advances the same head to its new assessment or a nonverified result before positive use
    And every positive use requires the fact fresh under the current published PayoutPolicyV1 maximum tax-profile validity/age intervals, unexpired, byte-identical to its current head, and signed by a key active and nonrevoked in the greatest current trust publication

  Scenario: Only the purpose-separated central tax service issues a withholding decision
    Given an exact payout preparation has verified operator, payout identity, destination, jurisdiction, currency/unit/scale, rail, conversion, and gross-amount inputs
    When the centrally authorized tax service commits TaxWithholdingDecisionV1
    Then its signature binds schema/network/protocol, tax-decision signer key ID, deterministic decision-series ID/revision/prior hash, PayoutPreparationV1 ID/complete hash, operator ID, payout-identity version, exact TaxProfileFactV1 series/revision/complete hash/result/effective/expiry/commit tuples, jurisdiction, destination fingerprint, currency/unit/scale, payout rail, exact PayoutPolicyV1 series/revision/complete hash and tax-profile validity/age bounds, accounting quanta per rail minor unit, selected and reserved share atoms, gross rail-minor units, decision result, required withholding or canonical absence, reason, deterministic applicability-authority tax_profile_fact or payout_policy source type/ID/hash/Core tuple, issue/expiry, and Core commit authority tuple
    And decision-series ID derives from strict JCS [TaxWithholdingDecisionV1-series-v1,network-ID,PayoutPreparationV1-ID], excluding mutable identity, profile, policy, destination, amount, and result fields
    And the decision commit transaction is at or after both fact effective and policy applicability and strictly before both expiries, and compare-and-swaps that sole series head, the exact current tax-profile head, the current applicable payout-policy/publication head, and the tax-decision, tax-profile, and payout-policy keys' current active nonrevoked states
    And only a decision whose complete hash is bound into TowerPayoutInstructionV1 can authorize that exact preparation
    And an operator, Tower, Station, payout callback, ordinary administrator, or payment provider cannot issue or edit it

  Scenario: Tax applicability cannot be backdated by the decision signer
    Given a tax decision names its deterministic applicability-authority source
    When its cutoff is compared with a payout send fence
    Then it selects the lexicographically greatest [Core time,global authority sequence,source ordinal] of the exact TaxProfileFactV1 effective tuple at ordinal zero and selected PayoutPolicyV1 effective/first-publication tuple at ordinal one
    And source type, ID, complete hash, Core time, and global authority sequence are byte-identical to that fact or policy object and its independently accepted ledger/publication anchor
    And the decision's claimed issue time, signer clock, opaque evidence time, or newly invented historical timestamp cannot precede or replace that selected anchor
    And only the resolved applicability tuple, not a free-form date in the correction, is compared with the send-fence tuple

  Scenario Outline: A tax decision has one closed result shape
    Given TaxWithholdingDecisionV1 has result "<result>"
    Then its withholding shape is "<shape>"
    And its v1 payout authority is "<authority>"

    Examples:
      | result | shape | authority |
      | zero | required withholding rail-minor units is canonical zero | may authorize only its exact payout preparation while current |
      | positive | required withholding rail-minor units is an integer from one through gross rail-minor units | no payout instruction; exact affected lots are withheld under tax_decision_hold bound to this decision |
      | unknown | required withholding is canonically absent and a closed reason is required | no payout instruction; exact affected lots are withheld under tax_decision_hold bound to this decision |

  Scenario Outline: A tax decision outside exact context has no payout authority
    Given a validly signed TaxWithholdingDecisionV1 has "<condition>"
    When instruction authorization verifies it against an existing PayoutPreparationV1
    Then no preparation, lot, hold, identity, decision head, TowerPayoutInstructionV1, send fence, or rail state changes because of that object
    And the existing preparation remains reserved_prepared only until a current authoritative decision commits or its bounded authorization deadline triggers a separate signed abort

    Examples:
      | condition |
      | operator, payout identity, preparation ID/complete hash, or destination mismatch |
      | jurisdiction, currency, unit, scale, rail, Q, B, X, or gross mismatch |
      | issue tuple in the future or expiry at or before the verification tuple |
      | decision revision below the committed revision |
      | previous decision hash not equal to the committed head |
      | tax-profile or policy applicability source missing, stale, hash-mismatched, not current, or committed/published after the decision tuple |
      | signer key missing the tax-decision purpose |
      | signer key not yet valid, expired, revoked, or compromised at the Core commit authority tuple |
      | result/withholding shape mismatch or amount above gross |
      | unknown field, duplicate field, explicit null outside the unknown shape, or noncanonical encoding |

  Scenario: Tax decision revision and replay use one durable CAS head
    Given decision series D is at revision R and complete hash H for one exact payout preparation
    When one or many workers submit a next decision
    Then D is the deterministic preparation-derived series and one transaction may compare-and-swap D, R, H, the exact current TaxProfileFactV1 head, and the selected current PayoutPolicyV1/publication head to exactly R+1 and the new complete hash
    And an exact committed decision replay returns that decision without another reservation, hold, incident, or event
    And a lower revision is stale while equal revision with different canonical bytes is conflict-quarantined and authorizes no payout
    And a skipped revision, wrong prior hash, alternate revision-1 series, another decision series, another preparation, or stale profile/policy head cannot advance D

  Scenario: Every zero-withholding positive use revalidates tax-profile currentness
    Given a current zero TaxWithholdingDecisionV1 binds one exact TaxProfileFactV1 and PayoutPolicyV1
    When Roger Core constructs or accepts a covered ledger head, payout instruction, or send fence
    Then the serializable use fence is inside every bound [effective,expiry) interval and revalidates the derived decision series/head, tax-profile series/head/result/freshness/expiry, selected payout-policy/publication head, all duplicated identity/jurisdiction/destination/scope fields, and the tax-decision, tax-profile, and payout-policy signers as active and nonrevoked in the greatest current trust publication
    And a tax-profile, ruleset, identity, policy, key-state, decision, or send update takes the same scope lock so exactly one side wins
    And any drift, review_required or invalid profile, unavailable head, stale fact, or expired authority rejects zero withholding and creates no rail call; positive or unknown decisions remain restrictive

  Scenario: Tax-decision signer rotation and compromise use ledger-anchored authority time
    Given tax-decision signer K is replaced by K2 under the signed trust document
    When a TaxWithholdingDecisionV1 is verified
    Then K and K2 are accepted only during their exact published purpose/validity overlap at the decision's Core commit tuple
    And after overlap K cannot authorize a new revision even with a backdated claimed issue time
    And a compromise-effective cutoff invalidates decisions committed at or after the cutoff and triggers correction processing for every affected submitted payout
    And trust-document rollback, an unknown key, another key purpose, or a signer-controlled time grants no authority

  Scenario: Unavailable tax authority cannot be replaced by a cached guess
    Given an existing reserved_prepared payout cannot obtain current tax profile evidence, decision-series head, tax-decision signer, or authoritative time
    When its signed preparation-authorization deadline is reached
    Then no zero decision or payout instruction is synthesized from a prior operator claim or default
    And one deadline sweep uses the exact atomic abort-or-void then withhold group: the parent removes the reservation and each ordered child creates tax_authority_unavailable_hold with reason tax-decision-unavailable over its exact range
    And invalid presented tax objects cannot trigger that sweep or change its Core deadline tuple

  Scenario Outline: A correction around the send fence has one exact effect
    Given payout P used zero decision Z and has rail state "<rail state>"
    When authoritative correction Z2 with result positive or unknown commits and its applicability cutoff is "<cutoff relation>" P's send-fence tuple
    Then correction handling is "<outcome>"

    Examples:
      | rail state | cutoff relation | outcome |
      | reserved_prepared with no instruction or send fence | at or before | use one atomic abort-then-withhold group whose ordered child materializes the exact tax decision hold, and create no instruction, rail call, or incident |
      | reserved_prepared with an instruction but no send fence | at or before | use one atomic signed-void-then-withhold group whose ordered child materializes the exact tax decision hold, and create no rail call or incident |
      | reserved_submitted with rail result unknown | after | prospective only for later payouts; P continues exact reconciliation under Z |
      | reserved_submitted with rail result unknown | at or before | append one open_rail_unknown TaxDecisionCorrectionIncidentV1, keep P rail-locked, and withhold every later payout for the operator |
      | paid | after | prospective only for later payouts; the completed P and its paid lots remain unchanged |
      | paid | at or before | append one open_noncompliant_disbursement incident and withhold every later payout; do not resend, debit, relabel gross as compliant, or claim remittance |
      | confirmed failed | at or before | cancel any pending-negative affected ranges, move every surviving returned range to withheld under the canonical set of all current hold references including tax, and append one closed_no_disbursement incident with no tax amount claimed paid |

  Scenario: A post-send tax incident is immutable, bounded, and idempotent
    Given a correction applicable at or before payout P's send fence requires positive withholding W or declares it unknown
    When TaxDecisionCorrectionIncidentV1 commits
    Then its tax-incident signature binds schema/network/protocol, deterministic incident ID/state/revision/prior hash, operator/payout/preparation IDs, payout-instruction hash, Z/Z2 revisions and complete hashes, applicability and send-fence tuples, rail state/result hash, gross/net amounts, W or canonical unknown, destination/jurisdiction/currency/rail bindings, independently assigned tax-incident-ledger Core commit tuple, evidence hash, and incident signer key ID
    And incident ID derives from strict JCS [TaxDecisionCorrectionIncidentV1-series-v1,network-ID,payout-ID], and one transaction assigns its commit tuple while compare-and-swapping its current revision/head, Z2, send fence, rail state/result, and operator payout-hold head
    And key validity uses the incident commit tuple; any successor also requires the exact current incident head and incident signer active/nonrevoked in the greatest current trust publication
    And duplicate or concurrent correction creates one incident and one operator payout hold
    And rail failure changes open_rail_unknown to closed_no_disbursement, excludes that closed incident from the current hold set, cancels pending-negative affected ranges, and returns each surviving range to mature_payable only if no current hold remains or otherwise withheld with the canonical set including any still-current positive/unknown tax_decision_hold, eligibility, reconciliation, and policy hold reference
    And rail success changes open_rail_unknown to terminal-in-v1 open_noncompliant_disbursement while the paid lots remain paid
    And v1 has no transition that calls a tax rail, claims withholding/remittance, debits a bank, clears an open disbursement incident, or releases later payouts without a separately approved future contract

  Scenario: External adapter credentials never enter Tower artifacts or observability
    Given payment ingress, authenticated fetch, compensation, and payout paths run through success and failure
    When Tower packages, process environments, logs, metrics, traces, support bundles, and receipts are inspected
    Then provider secrets, merchant credentials, raw webhook signatures, and recoverable tokens are absent
    And only bounded provider-neutral IDs, credential versions, hashes, and result classes appear where authorized
