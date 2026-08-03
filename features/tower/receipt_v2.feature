# PROPOSED SPEC — founder approval is required before step definitions or implementation.
#
# Scope: immutable provider, Tower-transit, Core-transit, settlement, and compensation
# evidence; canonical signatures;
# key discovery/rotation; chain anchoring; client verification and downgrade behavior.

Feature: Receipt v2 makes provider claims and Roger Core settlement independently verifiable
  A Station assertion is never rewritten. Roger Core signs a different final object whose
  canonical bytes cover every adjudication and money field and hash the complete evidence.

  Background:
    Given versioned canonical encoding rules for receipt v2
    And distinct Station, Tower, and Roger Core signing keys with key IDs

  Scenario: Standalone authority objects have no public-network decoding path
    Given a validly signed LocalTrustDocumentV1, LocalTrustPublicationV1, LocalPolicyV1, LocalClientInvitationV1, LocalClientCredentialAuthorityV1, LocalBootstrapVerifierHeadV1, LocalOperatorAuthorityHeadSetV1, LocalAdminAuthorizationV1, LocalBreakGlassRecoveryAuthorizationV1, LocalOfflineRootEscrowApprovalV1, LocalKeyEscrowExportAuthorizationV1, LocalKeyEscrowReservationV1, LocalKeyEscrowExportResultV1, LocalStationAttachAuthorizationV1, LocalStationOriginAuthorityV1, LocalRequestAuthorizationV1, LocalCancellationAuthorizationV1, LocalExecutionGrantV1, LocalProviderAssertionV1, LocalSettlementReceiptV1, TowerLocalStationBridgeCredentialV1, LocalAttemptTerminalStateV1, or any strict standalone child/set/audit object is presented to a RogerAI public verifier
    When public trust, routing, settlement, compensation, payout, or historical verification runs
    Then object type, standalone network ID, and pinned local root fail the public schema/domain/root relationship before any local signature can map to a public purpose
    And no field normalization, key-purpose alias, origin conversion, or administrator enrollment can turn that object into public authority

  # --- provider assertion --------------------------------------------------

  Scenario: A Station signs one immutable provider assertion
    Given a completed execution grant and response
    When the Station creates ProviderAssertionV2
    Then its signature verifies over the exact versioned canonical bytes
    And the assertion contains no Roger Core recount, cost, grant credit, or settlement field

  Scenario Outline: Every provider-owned field is covered by the Station signature
    Given a valid ProviderAssertionV2
    When "<field>" is changed, removed, duplicated, or ambiguously encoded
    Then Station verification fails

    Examples:
      | field                    |
      | schema version           |
      | network ID               |
      | protocol version          |
      | origin kind               |
      | job ID                   |
      | request ID               |
      | attempt ID               |
      | dispatch-lease hash or direct canonical absence |
      | execution-grant hash     |
      | client key ID            |
      | client nonce/idempotency key |
      | grant nonce              |
      | Tower ID or direct canonical absence |
      | Tower certificate serial or direct canonical absence |
      | Tower outer-session epoch/channel binding or direct canonical absence |
      | Station secure-session certificate serial |
      | Station inner-session epoch/channel binding |
      | Tower admission-lease stable series ID, lease ID, sequence, and complete hash or direct canonical absence |
      | Tower lifecycle revision and complete hash or direct canonical absence |
      | direct Station grant sequence or joined canonical absence |
      | Station ID               |
      | Station assertion key ID |
      | Station origin epoch     |
      | model                    |
      | offer ID                 |
      | quote ID                 |
      | deadline                 |
      | signed execution bounds  |
      | request digest           |
      | response digest          |
      | claimed input count      |
      | claimed output count     |
      | result status            |
      | start timestamp          |
      | end timestamp            |
      | Station assertion epoch  |
      | Station sequence         |
      | previous assertion complete hash or canonical first-sequence-in-epoch absence |

  Scenario: Roger Core never rewrites a verified provider assertion
    Given a Station claims a different model, usage, or status from authoritative state
    When Roger Core verifies and adjudicates it
    Then the original assertion bytes and Station signature remain unchanged
    And discrepancies appear only as separate final receipt fields or rejection evidence

  Scenario: Canonical bytes are stable across conforming implementations
    Given two independent implementations receive semantically identical receipt-v2 values
    When each canonicalizes and hashes the object
    Then the byte sequence and digest are identical
    And number, timestamp, Unicode, absent-value, and field-order rules admit one encoding

  Scenario: Signed-object v1 has one canonical JSON and signature suite
    Given any Tower-network application object is encoded for signing or hashing
    When its canonical bytes are produced
    Then strict UTF-8 parsing rejects duplicate or unknown members, trailing bytes, explicit null for absence, invalid Unicode, and schema strings outside required NFC
    And RFC 8785 JCS determines member ordering, string escaping, and whitespace
    And every security, sequence, time, count, rate, and money integer is a bounded canonical base-10 string rather than a JSON number
    And every digest, public key, and signature is fixed-length unpadded base64url
    And signing bytes prepend the network, object type, and object version domain and omit only that object's signature member
    And complete-object hashing includes the canonical signature member
    And v1 application signatures use Ed25519 under a purpose-bound trust-directory key

  Scenario Outline: Numeric strings have one accepted shape
    Given a signed-object field has numeric value "<encoding>"
    When strict schema and bounds validation runs before signature verification
    Then the result is "<result>"

    Examples:
      | encoding | result |
      | 0 | accepted when zero is in the field's semantic range |
      | 1 | accepted when one is in the field's semantic range |
      | the field's exact signed maximum | accepted |
      | an empty string | rejected |
      | +1 | rejected |
      | -0 | rejected |
      | 01 | rejected |
      | 1.0 | rejected |
      | 1e3 | rejected |
      | surrounding whitespace | rejected |
      | a JSON number token | rejected |
      | one above the field's maximum | rejected before arithmetic |
      | a negative value for an unsigned field | rejected |

  Scenario: Each role excludes only its own signature slot while signing
    Given a provider assertion, Tower statement, or settlement receipt is ready to sign
    When its role-specific canonical signing bytes are produced
    Then only that object's own signature slot is absent from those bytes
    And no recount, grant, digest, price, disposition, prior signature, or other semantic field is zeroed

  Scenario Outline: Non-canonical encodings have one published result
    Given receipt values represented with "<ambiguity>"
    When verification runs
    Then the result is "<result>"

    Examples:
      | ambiguity                                   | result |
      | duplicate field names                       | rejected before signature verification |
      | unknown required fields                     | rejected before signature verification |
      | reordered map keys                          | decoded strictly and canonicalized to the one field order |
      | alternate Unicode normalization             | rejected unless already in the required normalization form |
      | integer encoded as floating point           | rejected before signature verification |
      | integer encoded as a JSON number             | rejected before signature verification |
      | negative zero                               | rejected before signature verification |
      | NaN or infinity                             | rejected before signature verification |
      | timestamp with alternate offset             | rejected unless already in the required UTC form |
      | omitted value versus explicit null          | explicit null is rejected where omission is canonical |
      | base64 with non-canonical padding           | rejected before signature verification |
      | trailing bytes                              | rejected before signature verification |

  # --- Tower transit statement --------------------------------------------

  Scenario: A Tower transit statement signs only attributable transit evidence
    Given a valid provider assertion passed through a joined Tower
    When the Tower creates TowerTransitStatementV1
    Then its signature binds Tower and session IDs, dispatch-lease hash, opaque encrypted request/result envelope digests, local route ID, byte counts, receipt times, Tower sequence, status, and prior transit hash
    And it carries no authority to set price, billed counts, cost, hold, grant, or payout

  Scenario Outline: Transit tampering invalidates Tower attribution but never changes money
    Given a valid TowerTransitStatementV1
    When its "<field>" is altered
    Then Tower verification fails
    And the altered value cannot become settlement authority

    Examples:
      | field                    |
      | Tower ID                 |
      | Tower key ID             |
      | session ID               |
      | dispatch-lease hash      |
      | encrypted request-envelope digest |
      | encrypted result-envelope digest  |
      | local route ID           |
      | input bytes              |
      | output bytes             |
      | received timestamp       |
      | forwarded timestamp      |
      | Tower sequence           |
      | previous TowerTransitStatementV1 complete hash or canonical first-sequence-in-epoch absence |
      | transit status           |

  # --- Core-observed transit -----------------------------------------------

  Scenario: Roger Core signs what it observed on the authenticated Tower session
    Given a joined attempt's opaque envelopes traverse one authenticated Tower session
    When Roger Core creates CoreTransitObservationV1
    Then its signature binds schema, network, protocol, Core signer, Tower ID, Tower certificate serial, session epoch/channel binding, dispatch-lease hash, job ID, attempt ID, encrypted request/result envelope digests, byte counts, Core first/complete receive times, and the durable evidence-complete authority tuple
    And none of those observed fields comes from a Tower-controlled clock as authority

  Scenario Outline: Core transit observation tampering invalidates joined attribution
    Given a valid CoreTransitObservationV1
    When its "<field>" is altered
    Then Core transit verification fails
    And the attempt is ineligible for Tower compensation

    Examples:
      | field                            |
      | Tower ID                         |
      | Tower certificate serial         |
      | session epoch or channel binding |
      | dispatch-lease hash              |
      | job ID                           |
      | attempt ID                       |
      | request-envelope digest          |
      | result-envelope digest           |
      | input or output byte count       |
      | first or complete receive time   |
      | Core signer key ID               |

  Scenario: Core transit observation attributes a key, not physical geography
    Given a valid Core observation and matching Tower statement
    When trust is displayed or compensation eligibility is evaluated
    Then RogerAI states only that Core exchanged the bound envelopes on a session authenticated to that Tower key
    And it does not claim proof of the host's physical location, unmodified runtime, or honest operator

  # --- final settlement receipt -------------------------------------------

  Scenario: Roger Core signs a complete separate settlement receipt
    Given exact valid job evidence and a prepared atomic settlement
    When Roger Core signs and commits SettlementReceiptV2 with the money and ledger transition
    Then it hashes the complete immutable provider assertion, required Core transit observation for joined origin, and optional Tower statement
    And its own signature covers every identity, digest, claim, recount, price, money, disposition, ledger, time, and signer field
    And it does not replace or masquerade as the Station signature
    And the money transition and exact signed receipt become visible through one committed outcome

  Scenario Outline: Every final field is covered by the Roger Core signature
    Given a valid SettlementReceiptV2
    When "<field>" is changed, removed, duplicated, or ambiguously encoded
    Then Roger Core signature verification fails

    Examples:
      | field                         |
      | schema version                |
      | network ID                    |
      | protocol version             |
      | job ID                        |
      | request ID                    |
      | attempt ID                    |
      | origin kind                   |
      | dispatch-lease hash or canonical absence |
      | execution-grant hash          |
      | provider-assertion hash       |
      | Core-transit-observation hash or canonical absence |
      | Tower-transit hash or canonical absence |
      | client key ID                 |
      | client nonce/idempotency key  |
      | grant nonce                   |
      | Tower ID or canonical absence |
      | Tower certificate serial or canonical absence |
      | Tower outer-session epoch/channel binding or canonical absence |
      | Station secure-session certificate serial |
      | Station inner-session epoch/channel binding |
      | Station ID                    |
      | Station origin epoch         |
      | model                         |
      | Station assertion key ID      |
      | Tower key ID or canonical absence |
      | Roger Core signer key ID      |
      | request digest                |
      | response digest               |
      | policy version                |
      | offer ID                      |
      | quote ID                      |
      | Tower admission-lease stable series ID, lease ID, sequence, and complete hash or canonical absence |
      | Tower lifecycle revision and complete hash or canonical absence |
      | direct Station grant sequence or canonical absence |
      | signed execution deadline     |
      | signed settlement-finalization/hold ceiling |
      | signed byte, stream, token, result, and cost bounds |
      | Core evidence first-observed time or canonical absence |
      | Core evidence complete-observed time or canonical absence |
      | result complete-observed time |
      | provider assertion complete-observed time |
      | evidence-complete Core authority time |
      | evidence-complete Core authority sequence used for equal-time ordering |
      | provider input claim          |
      | provider output claim         |
      | Core input recount            |
      | Core output recount           |
      | billed input count            |
      | billed output count           |
      | effective consumer input rate |
      | effective consumer output rate|
      | effective Station-earning input rate |
      | effective Station-earning output rate |
      | currency, unit, or scale      |
      | authorized hold               |
      | actual cost                   |
      | Station earning               |
      | funding-allocation array and hash |
      | consumer disposition          |
      | provider disposition          |
      | grant-time compensation-snapshot complete hash plus Tower compensation policy/operator/capability versions or canonical absence |
      | immutable Tower compensation eligibility and closed ineligibility reason or canonical absence |
      | result status                 |
      | ledger sequence               |
      | previous Roger settlement-ledger entry hash or RogerLedgerGenesisV1 complete hash at first sequence |
      | settlement timestamp          |

  Scenario: Broker-owned recount and grant fields cannot be changed under valid signatures
    Given a fully signed SettlementReceiptV2
    When a caller changes the input recount, output recount, billed counts, actual cost, or grant ID
    Then final signature verification fails
    And the Station signature over its untouched assertion still has its original meaning

  Scenario: Authoritative price is bound without rewriting provider evidence
    Given the Station assertion binds the hash of a Core grant containing the authoritative quote
    When Roger Core settles at that quote
    Then the Station assertion remains byte-for-byte intact
    And the final receipt records the quote and effective prices
    And the grant, provider, and settlement signatures verify over their own immutable objects

  Scenario: Provider evidence is content-addressed in the final receipt
    Given a valid final receipt and provider assertion
    When any byte of the provider assertion or its signature changes
    Then its complete-object hash no longer matches the final receipt
    And final evidence verification fails even if separately reconstructed fields look equal

  Scenario Outline: Origin kind determines exactly which Tower fields exist
    Given a valid "<origin>" attempt
    When SettlementReceiptV2 is created
    Then Tower evidence is "<tower evidence>"

    Examples:
      | origin | tower evidence |
      | direct | Tower IDs, certificate serial, Tower outer session, Tower admission-lease identity/head, Tower lifecycle head, Core Tower-session observation, TowerTransitStatementV1 hash/rejection reason, grant-time compensation snapshot, and compensation policy are absent; Tower-statement status is present as not_applicable, and Station inner session plus direct Station grant sequence are present |
      | joined with valid corroboration | Station inner session plus every joined Tower field and Core observation are present; Tower statement status is verified and its hash is relationship-checked |
      | joined without a statement | every required joined field and Core observation is present; Tower statement status is missing, hash is absent, and compensation reason is missing-corroboration |
      | joined with an invalid statement | every required joined field and Core observation is present; Tower statement status is invalid, valid-statement hash is absent, and a closed audit reason is present |

  Scenario Outline: Tower-statement status has one wire representation
    Given a SettlementReceiptV2 has Tower-statement status "<status>"
    Then "<shape>"

    Examples:
      | status | shape |
      | not_applicable | origin is direct, statement hash and rejection reason are absent |
      | missing | origin is joined, statement hash is absent, reason is missing-corroboration |
      | verified | origin is joined, complete-object hash is required, rejection reason is absent |
      | invalid | origin is joined, valid-statement hash is absent, deterministic rejection reason is required and raw evidence is retained only in the audit store |

  Scenario: A job receipt creates a compensation candidate rather than future money
    Given a compensated joined Tower has exact Core-observed and corroborating evidence
    When the job settlement commits before external funds mature
    Then SettlementReceiptV2 relationship-checks and records the exact GrantCompensationSnapshotV1 complete hash, signed compensation policy, and immutable eligible candidate
    And no payable Tower balance is created by the job receipt alone

  Scenario: Post-grant compensation changes cannot rewrite the grant snapshot
    Given an attempt has a committed GrantCompensationSnapshotV1
    When capability, payout verification, terms, Tower lifecycle, operator state, or compensation policy changes before settlement
    Then SettlementReceiptV2 either binds the original snapshot and applies the signed lifecycle cutoff rules or the attempt does not settle
    And no newer revision is substituted to create retroactive candidacy or select another rate

  Scenario: FundingSourceLotV1 is the only authority for a spendable consumer source
    Given a consumer receives external-cash or platform-grant credit
    When the purpose-separated funding-source ledger commits or advances FundingSourceLotV1
    Then its strict signed bytes contain only schema/network/protocol, funding-source-ledger signer key ID, deterministic stable source-lot ID, positive revision, immediate prior complete hash or canonical creation absence, consumer account stable ID, external_cash or expiring_grant or nonexpiring_grant kind, currency/unit/scale, immutable original half-open source interval and amount, current FundingSourceAvailabilityRangeSetV1 complete hash and available/reserved/consumed totals, expiry Core tuple or canonical nonexpiring absence, Core credit authority sequence, closed external-cash or platform-grant provenance union, exact creation credit-authority or reservation/settlement/release causal stable-ID/expected-index union, issue tuple, and independently assigned funding-ledger Core commit tuple
    And external_cash provenance contains one exact purpose-signed ConsumerCashCreditAuthorityV1 ID/complete hash and canonical platform-grant absence, while platform-grant provenance contains one exact purpose-signed PlatformGrantCreditV1 ID/complete hash and canonical cash-credit absence
    And creation never credits more than its authenticated unallocated cash interval or purpose-authorized grant amount, every successor conserves the immutable original interval across disjoint available/reserved/consumed ranges, and one source interval can never appear in two lots
    And the stable ID is derived from network, provenance kind, immutable provenance identity, and source interval, never from a settlement signer or mutable lot state

  Scenario: Cash ownership and platform grants have independent one-use credit authorities
    Given authenticated value is to be assigned to a consumer funding source
    When the applicable purpose-separated credit authority commits
    Then ConsumerCashCreditAuthorityV1 signs only schema/network/protocol, consumer-cash-credit signer key ID, deterministic authority ID, consumer-account stable ID, provider-neutral adapter/merchant/payment-source identity, exact AuthoritativePaymentRevisionV1 sequence/complete hash and ProviderPaymentEventIDSetV1 hash, one captured unallocated half-open source interval and atoms, currency/unit/scale, effective/expiry Core tuples, evidence hash, issue tuple, and independently assigned consumer-credit-ledger Core commit tuple
    And PlatformGrantCreditV1 signs only schema/network/protocol, platform-grant-credit signer key ID, deterministic grant-credit ID, consumer-account stable ID, grant program ID, expiring_grant or nonexpiring_grant kind, exact amount and currency/unit/scale, spend expiry or canonical nonexpiring absence, evidence hash, issue/use-expiry tuples, and independently assigned grant-credit-ledger Core commit tuple
    And cash authority ID derives from network, consumer, payment-source identity, and captured interval; grant-credit ID derives from network, consumer, program, and an independently unique issuance nonce
    And each authority is consumed exactly once by the one FundingSourceLotV1 whose stable ID derives from it, no payment interval or grant authority can be split across owners or reused, and cash creation also verifies the authenticated captured amount remains unallocated at the funding-ledger transaction
    And the funding-source signer cannot assign an authority to another consumer, change its kind/currency/range/expiry, mint a grant, or synthesize an authenticated payment revision

  Scenario: Grant issue reserves one canonical authenticated funding set
    Given the exact consumer, authorized hold, maximum cost C, currency/unit/scale, current FundingSourceLotV1 heads, and grant-bound FundingAllocationPolicyV1 are fixed
    When the funding-source ledger reserves funds for one job/attempt before ExecutionGrantV1 commits
    Then FundingSourceReservationSetV1 is a strict object owned by that attempt containing consumer, C, currency/unit/scale, FundingAllocationPolicyV1 series/revision/complete hash and allocation rule, ordered member count/total, and complete members in policy order
    And each member contains its source-lot stable ID, transaction-start revision/complete hash, owner/kind/currency, expiry-or-absence, credit sequence, provenance type/ID/complete hash, selected available half-open interval, and exact amount
    And canonical selection consumes the lowest available ranges of each current lot in the published expiring-grant, nonexpiring-grant, then external-cash order until members sum exactly to C
    And FundingSourceReservationV1 purpose-signs the reservation ID, job/attempt/consumer, set complete hash, exact prior/result source-head sets, hold/deadline, policy reference, and independently assigned funding-ledger commit tuple; its serializable transaction compare-and-swaps every source head and moves each selected range from available to reserved exactly once
    And ExecutionGrantV1, its attempt row, and a compensated GrantCompensationSnapshotV1 bind that same reservation and set complete hash; no grant commits unless the reservation commits, and failure/timeout releases it once through a signed funding-ledger successor

  Scenario: Settlement consumes only the reserved actual-cost prefix
    Given FundingSourceReservationV1 reserved maximum cost C and verified settlement cost A is between zero and C inclusive
    When SettlementReceiptV2 commits
    Then FundingSourceSettlementTransitionV1 binds the exact consumed-prefix and released-suffix FundingSourceDispositionRangeSetV1 hashes plus prior/result FundingSourceHeadSetV1 hashes, consumes exactly the first A units of the reservation's ordered member ranges, releases the exact remaining C minus A suffix ranges, and compare-and-swaps every still-current reserved FundingSourceLotV1 head
    And each funding slice is the maximal nonempty intersection of that consumed prefix with one reservation member and copies its exact source-lot revision/hash, owner-bound opaque public reference, kind, provenance, credit sequence, selected source interval, policy, and cumulative cost interval
    And SettlementReceiptV2 binds that purpose-signed transition complete hash, while the transition preselects only the receipt stable ID/expected ledger index so neither complete preimage contains the other
    And a stale head, wrong consumer, relabeled kind, unsupported provenance, expired-at-reservation grant, unreserved interval, gap, overlap, alternate order, post-grant source substitution, or settlement-signer-created source rejects settlement and releases or retains the hold only through a signed FundingSourceReleaseTransitionV1

  Scenario: Job settlement records exact conserving funding slices
    Given a job cost and Station earning use grant, external cash, or both
    When SettlementReceiptV2 commits
    Then each immutable funding slice binds slice ID, funding kind, owner-bound opaque source-lot reference, exact FundingSourceLotV1 stable ID/reservation-time revision/complete hash and provenance type/ID/complete hash, source credit authority sequence, consumer-cost amount, Station-earning allocation, currency/unit/scale, half-open reserved source and cumulative job-cost intervals, FundingSourceReservationV1 and FundingSourceReservationSetV1 complete hashes, and the exact grant-bound FundingAllocationPolicyV1 series/revision/complete hash and closed funding-allocation rule
    And slice consumer amounts sum exactly to actual cost
    And slice Station allocations sum exactly to the Station earning
    And missing or ambiguous funding lineage makes compensation ineligible rather than guessed

  Scenario: SettlementReceiptV2 contains no future compensation amount
    Given processor fees, reversals, maturity, enforcement, and payout are not final at job settlement
    When SettlementReceiptV2 is verified
    Then it contains no Tower share base, share amount, payable balance, processor fee, maturity result, payout, or clawback field
    And its compensation candidate inputs cannot themselves move Tower money

  Scenario: Later compensation events never mutate the job receipt
    Given cash capture, fees, refunds, disputes, enforcement, or payout state changes after job settlement
    When Roger Core applies the event
    Then it appends a separately signed TowerCompensationReceiptV1 whose entitlement_delta directly references the immutable SettlementReceiptV2 and whose later variants bind it transitively through exact lot/range/debt source objects and canonical source-set projections
    And every earlier receipt remains byte-for-byte unchanged

  Scenario Outline: Every entitlement-delta field is covered by its purpose-bound Core signature
    Given a valid entitlement_delta TowerCompensationReceiptV1
    When "<field>" is altered, removed, duplicated, or ambiguously encoded
    Then compensation signature verification fails

    Examples:
      | field |
      | schema, network, currency, unit, and scale |
      | compensation event ID, type, reason, and causal event ID |
      | SettlementReceiptV2 ID and complete hash |
      | SettlementReceiptV2 funding-allocation-array complete hash |
      | TowerIDScopeSetV1, operator, exact grant-snapshot fact heads, CompensatedTowerCapabilityV1 head, and TowerCompensationPolicyV1 series/revision/complete hash |
      | dispatch lease and Core transit observation hashes |
      | AuthoritativePaymentRevisionSetV1 complete hash |
      | prior and new mature cash G |
      | prior and new allocated Station cost S |
      | prior and new allocated processor fee F |
      | prior and new net platform revenue N |
      | prior and new exact share atoms A |
      | signed compensation delta and ApplicationDescriptorSetV1 complete hash |
      | AffectedStateEntityKeySetV1 and prior/resulting AffectedStateProjectionSetV1 complete hashes |
      | prior committed per-currency control-leaf hash and resulting ControlValueProjectionV1 complete hash |
      | event-owned canonical empty JournalPostingSetV1 plus journal-template version/disposition |
      | ledger sequence, previous Roger compensation-ledger entry hash or RogerLedgerGenesisV1 complete hash at first sequence, Core-observed time, and signer key ID |

  Scenario: Non-entitlement compensation variants use their closed source-specific envelopes
    Given maturity, dust, hold, enforcement, partition, payout, rail-result, or debt state changes
    When its TowerCompensationReceiptV1 is verified
    Then it has exactly the common envelope and event-specific fields defined by the tamper matrices under features/tower/tamper/, including prior leaf/result projection rather than a resulting leaf hash
    And it binds source SettlementReceiptV2 values transitively through signed immutable lot/range/debt objects or canonical selected source-set hashes
    And a universal singular SettlementReceiptV2, funding slice, payment-revision, Tower, or candidate field is not inserted where a multi-source variant's closed schema does not declare it

  Scenario: Compensation events use cumulative targets and append only their delta
    Given authenticated payment revision V changes one compensation candidate
    When Roger Core recomputes cumulative mature cash G, Station cost S, fee F, net N, and exact share atoms A
    Then TowerCompensationReceiptV1 appends delta A minus the sum of prior recognition deltas for that settlement
    And duplicate or stale revision V appends no money event
    And SettlementReceiptV2 remains unchanged

  # --- sequences, heads, and centralized log ------------------------------

  Scenario: Provider assertions occupy a unique persistent per-Station sequence transactionally
    Given Roger Core expects Station S epoch E sequence 7 and previous hash H
    When a valid assertion for sequence 7 with previous hash H settles
    Then the transaction records the unique sequence and advances the contiguous verified head to the assertion hash
    And restart or another Core instance observes the same head

  Scenario Outline: Sequence cases have deterministic audit and money behavior
    Given Roger Core has a durable Station assertion epoch, occupied sequences, and contiguous head
    When it receives "<case>"
    Then the result is "<result>"

    Examples:
      | case                                      | result |
      | an exact repeated assertion               | idempotent evidence replay; no second money movement |
      | an occupied sequence with another hash    | fork rejected; Station quarantined and affected payouts frozen |
      | a unique sequence above a gap             | exact job remains settlement-eligible; provider payout is held under review-deadline pending-gap state |
      | a link closing the next expected gap       | contiguous head advances and eligible held payouts enter their normal clearing checks |
      | an unknown epoch                          | rejected until an authorized epoch transition |
      | an old closed epoch                       | rejected as historical replay |
      | a sequence integer overflow               | rejected and Station quarantined before mutation |

  Scenario: Pending Station gaps are bounded without extending consumer holds
    Given one Station has unresolved sequence gaps
    When its pending-gap count, bytes, or age reaches the configured signed limit
    Then Roger Core quarantines it from new routing and rejects additional gap growth
    And valid individual attempts still reach settled or released state by their own deadlines
    And only provider payout, not consumer money, waits for chain reconciliation

  Scenario: An authentic context-invalid assertion occupies audit order without earning
    Given sequence 11 is missing and an assertion with a valid Station signature and correct previous hash arrives with a wrong job, request, attempt, grant, identity, or digest
    When Roger Core validates it
    Then its immutable hash occupies sequence 11 with adjudication status rejected-context
    And the contiguous assertion head advances through it
    And no consumer debit or provider earning is created for its job
    And later authentic assertions are not permanently blocked

  Scenario: An unresolved gap reaches a signed epoch closure but no implicit money disposition
    Given a Station gap remains unresolved through the signed chain-gap review window
    When the review deadline is reached
    Then Roger Core closes the epoch with exact next-revision StationLifecycleEventV1 whose StationEpochClosureEvidenceSetV1 contains every observed sequence, missing sequence, fork claim, claimed previous hash, adjudication status, and Core-observed tuple
    And each earning held solely by that gap remains explicit payout-held with an appeal/support reference because v1 defines no Station-lifecycle or Station-epoch authority to release or forfeit provider money
    And no consumer balance or completed exact settlement is reversed merely because of the audit gap
    And serving again requires an authorized new epoch

  Scenario: Gap deadlines are swept without an incoming message
    Given a Station has pending gaps and sends no more traffic
    When the durable gap deadline becomes due
    Then a Core sweep claims the deadline idempotently and applies the one terminal StationLifecycleEventV1 epoch closure while preserving every money hold

  Scenario: Fork scope includes the fork and every hash descendant
    Given two different signed assertions claim one epoch and sequence
    When one unique compare-and-swap occupant is established
    Then the other is recorded as the losing fork claim and cannot settle its job
    And every stored descendant of the losing prior-hash path is payout-frozen
    And already-paid descendants receive append-only recourse rather than receipt mutation

  Scenario: Resolving the gap before review deadline releases only otherwise eligible payouts
    Given the exact valid missing assertion arrives before the chain-gap review deadline
    When its hash links the buffered sequence chain without a fork
    Then the contiguous head advances through every verified link
    And each held earning enters its ordinary payout, policy, and recourse checks exactly once

  Scenario: Final receipts advance one central append-only ledger order
    Given two valid settlements race on separate Roger Core instances
    When both transactions commit
    Then each receives a unique ledger sequence and one previous ledger hash
    And the resulting total order has no duplicate sequence or fork

  Scenario: RogerLedgerGenesisV1 has one exact signed preimage
    Given Roger Core initializes the settlement or compensation ledger before any entry exists
    When RogerLedgerGenesisV1 is signed and committed
    Then its strict JCS signing bytes contain only schema/network/protocol, settlement or compensation ledger-kind tag, stable ledger ID, ledger schema version, fixed first-entry sequence 1, hash algorithm and canonical-encoding-policy version, journal-template version or canonical settlement-ledger absence, issue Core authority tuple, trust-document version, and the settlement-signer or compensation-ledger-signer key ID selected exactly by kind
    And the purpose-separated signature is the only excluded signing slot, its complete hash is immutable, and settlement and compensation use different ledger IDs and genesis complete hashes
    And a second genesis, unknown field/kind, wrong-purpose signature, zero or different first-entry sequence, changed encoding/hash policy, or mismatched ledger ID is rejected before any entry or control leaf

  Scenario Outline: Each Roger append-only ledger has one canonical first link
    Given the "<ledger>" appends signed entry sequence Q
    When its previous-hash relationship is checked in the serializable commit
    Then sequence 1 requires the exact accepted same-kind RogerLedgerGenesisV1 complete hash and every later sequence is exactly prior plus one with the immediately preceding committed entry hash
    And exact causal replay returns the existing entry, while zero, a gap, overflow, absence or non-genesis previous hash at sequence 1, a genesis hash after sequence 1, wrong-ledger prior, wrong prior, or conflicting bytes at one sequence fails without a partial money or receipt commit

    Examples:
      | ledger |
      | Roger settlement ledger containing SettlementReceiptV2 |
      | Roger compensation ledger containing every TowerCompensationReceiptV1 event |

  Scenario: A failed settlement does not create a receipt gap disguised as success
    Given ledger sequence allocation or receipt signing fails
    When the settlement transaction cannot commit completely
    Then no debit, credit, new head, or successful final receipt becomes visible
    And recovery follows the committed-state table without choosing between success and failure heuristically

  Scenario Outline: Settlement recovery has one outcome from durable state
    Given Roger Core recovers an attempt with "<durable state>"
    When the settlement recovery sweep runs
    Then it performs "<outcome>"

    Examples:
      | durable state | outcome |
      | timely exact evidence_complete, intact hold, every authority readable, and current Core tuple before finalization ceiling | retry the same atomic settlement until one SettlementReceiptV2 commits or the ceiling CAS wins |
      | timely exact evidence_complete but a required dependency is temporarily unavailable before finalization ceiling | remain evidence_complete with hold reserved and retry boundedly until the ceiling; do not choose failure from execution-deadline delay |
      | timely exact evidence_complete with no successful receipt strictly before finalization ceiling | commit core-finalization-timeout, release the hold once, create no debit/earning/candidate/protocol liability, and preserve the incident reference |
      | evidence_complete with a permanent cryptographic, context, arithmetic, or conservation failure | commit the one failed transition and release the hold once; never sign success |
      | issued, leased, or executing before its deadline/cutoff | resume only its allowed attempt state; do not synthesize evidence or receipt |
      | issued, leased, or executing at or after its deadline/cancel cutoff | commit expired or cancelled according to the authoritative event and release once |
      | a complete committed settlement row and receipt whose response was lost | return the exact receipt without any new ledger or signature event |
      | money rows committed without the required receipt/head in a transaction claimed atomic | declare invariant violation, make money readiness false, and run repair from the transaction log; never expose success or create compensating guesses |

  Scenario Outline: Crash boundaries expose one settlement outcome
    Given Roger Core is settling one exact attempt
    When it crashes "<boundary>"
    Then recovery produces "<outcome>"

    Examples:
      | boundary | outcome |
      | before receipt signing | no committed money or receipt; deterministic retry may sign before the finalization ceiling, otherwise its ceiling sweep commits the zero-money failure outcome |
      | after signing but before database commit | unpublished signature is discarded; no committed money or receipt |
      | after database commit but before client response | exact committed receipt is returned on retry without second money movement |
      | during response transmission | exact committed receipt remains retrievable by idempotency key |

  Scenario: A signed periodic checkpoint commits to a precise ledger prefix
    Given the ledger has an immutable committed prefix
    When Roger Core publishes a Merkle checkpoint
    Then the signed checkpoint binds tree size, root hash, time, log identity, algorithm, and signer key ID
    And later inclusion and consistency proofs cannot reinterpret another prefix as that checkpoint

  # --- trust directory and independent verification -----------------------

  Scenario: The public trust document publishes purpose-bound verification keys
    When a verifier fetches the signed RogerAI trust document
    Then it can resolve current and historical Station, Tower, settlement, grant, and checkpoint key IDs
    And each key has purpose, issuer, identity, validity interval, algorithm, status, and rotation relationship
    And no private or symmetric key material is present

  Scenario: Verification starts from a pinned public-network bootstrap anchor
    Given a fresh RogerAI client or Tower package
    When it obtains the public trust document for the first time
    Then it verifies the document through a shipped or explicitly pinned bootstrap anchor
    And HTTPS or an unpinned document signer alone cannot introduce a replacement trust root

  Scenario Outline: Trust-document rollback and freeze fail closed
    Given a verifier has accepted trust-document version 12 and its expiry
    When it receives "<document>"
    Then it does not use that document to validate new evidence

    Examples:
      | document                                      |
      | version 11                                    |
      | another version 12 with different bytes       |
      | version 13 whose signature is invalid         |
      | an expired version 12                         |
      | a version with an expiry beyond policy bounds |

  Scenario: Public-network root rollover is explicitly cross-authorized
    Given the pinned public-network root must rotate
    When a replacement anchor is published
    Then the exact accepted prior RootDelegationV1 quorum and exact proposed successor RootDelegationV1 quorum both authenticate one bounded overlap transition
    And the successor revision, prior complete hash, activation/expiry window, root key set, delegated signer sets, thresholds, and signatures satisfy the closed RootDelegationV1 contract
    And v1 has no documented-recovery-policy, emergency-key, single-administrator, or other bypass path
    And an arbitrary valid TLS endpoint cannot perform root rollover

  Scenario Outline: Key lookup fails closed
    Given a receipt references "<key condition>"
    When an independent client verifies it
    Then the relevant signature is not reported valid
    And the client distinguishes unverifiable evidence from a cryptographically invalid signature

    Examples:
      | key condition                              |
      | an unknown key ID                          |
      | a key of the wrong purpose                 |
      | a key bound to another identity            |
      | a key not valid at the object's required authoritative time |
      | a key revoked before the object's required authoritative time |
      | an unsupported algorithm                   |
      | a trust document with an invalid signature |
      | a stale trust document beyond policy       |

  Scenario: A valid historical receipt survives normal rotation
    Given every signer was valid for its purpose at the Core-observed and ledger-anchored evidence time
    And the trust directory records the later rotations
    When the receipt is verified after rotation
    Then its historical signatures remain valid
    And new signatures under the expired keys are rejected

  Scenario Outline: Each object uses a non-signer-controlled key-validity anchor
    Given a verifier checks "<object>"
    Then key validity and compromise status use "<authoritative anchor>"
    And if that required anchor is absent or unverifiable, the object is reported unverifiable and gains no routing, money, trust, or historical-validity authority

    Examples:
      | object | authoritative anchor |
      | ClientRequestAuthorizationV1 | Roger Core authenticated receive tuple, checked against bounded client issue/expiry claims |
      | TowerEnrollmentProofV1 | Roger Core challenge-consumption transaction tuple |
      | TowerAdmissionLeaseV1 | independently assigned Core lease-ledger commit tuple plus the bound Tower lifecycle revision/hash |
      | TowerInventoryV1 or StationOfferV1 | Roger Core durable receive/admission tuple plus signed object expiry |
      | PublicDirectorySnapshotV1 | independently assigned Core directory-publication tuple plus its derived greatest accepted RogerTrustPublicationV1 and current-head relationship |
      | DispatchLeaseV1 | independently assigned Core attempt-issue tuple copied through AttemptIssueCommitmentV1/issued AttemptEventV1 plus signed execution deadline |
      | ExecutionGrantV1 | independently assigned Core attempt-issue tuple copied through AttemptIssueCommitmentV1/issued AttemptEventV1, signed execution deadline, and signed settlement-finalization/hold ceiling |
      | AttemptEventV1 | independently assigned Core attempt-ledger commit tuple plus exact revision/current-head relationship |
      | AttemptIssueCommitmentV1 | byte-identical issued AttemptEventV1 commit tuple/index plus attempt-state signature and exact lease/grant hash membership |
      | ProviderAssertionV2 | durable evidence-complete Core authority tuple, never Station start/end time |
      | TowerTransitStatementV1 | matching Core receive tuple on the bound Tower session, never Tower receipt/forward time |
      | CoreTransitObservationV1 | its Core durable observation tuple |
      | SettlementReceiptV2 | atomic settlement-ledger commit tuple |
      | TowerCompensationReceiptV1 | compensation-ledger commit tuple plus the authenticated payment revision or exact signed non-entitlement authority named by its closed variant |
      | TowerCompensationPolicyV1, FundingAllocationPolicyV1, PayoutPolicyV1, FeeFinalityPolicyV1, MaturityPolicyV1, PayoutEligibilityPolicyV1, CompensationEnforcementPolicyV1, or DebtWriteoffPolicyV1 | first independently accepted RogerTrustPublicationV1 containing its exact CompensationPolicyDirectorySetV1 member, plus its Core policy-ledger commit/effective tuples; expiry is the new-authority selection cutoff |
      | MaturityAuthorityV1 | independently allocated serializable maturity transaction tuple byte-identical to its actual tuple and bound maturity-event/ledger-group Core tuple |
      | FeeFinalityIncidentV1 | fee-incident ledger commit tuple and bound capture/deadline authority tuples |
      | PayoutEligibilityDecisionV1 | independently assigned eligibility-decision-ledger Core commit tuple and applicability-authority/current-head relationships |
      | PayoutEligibilityFactV1 | independently assigned fact-ledger Core commit tuple plus finite expiry; opaque source evidence time grants no authority |
      | TaxProfileFactV1 | independently assigned tax-profile-ledger Core commit tuple plus policy-bounded freshness and finite expiry |
      | TaxWithholdingDecisionV1 | independently assigned tax-decision-ledger Core commit tuple plus its applicability source and exact current decision/fact/policy heads |
      | TaxDecisionCorrectionIncidentV1 | independently assigned tax-incident-ledger Core commit tuple plus exact correction, send-fence, rail-state, and current incident-head relationships |
      | CompensatedTowerCapabilityV1 | independently assigned capability-ledger Core commit tuple plus current Tower lifecycle, fact/policy heads, and finite expiry |
      | FundingSourceLotV1, FundingSourceReservationV1, FundingSourceSettlementTransitionV1, or FundingSourceReleaseTransitionV1 | independently assigned funding-ledger Core commit tuple plus exact current source heads, authenticated provenance, and receipt-or-terminal-attempt authority |
      | ConsumerCashCreditAuthorityV1 | independently assigned consumer-credit-ledger Core commit tuple plus its authenticated payment revision and one-use cash interval |
      | PlatformGrantCreditV1 | independently assigned grant-credit-ledger Core commit tuple plus its one-use program/consumer/currency authority |
      | PayoutEligibilityIncidentV1 | independently assigned eligibility-incident-ledger Core commit tuple plus restriction, payout send-fence, rail-state, and current incident-head relationships |
      | CompensationEnforcementFindingV1 | independently assigned enforcement-finding-ledger Core commit tuple plus exact current finding-series head and target-scope digest |
      | DebtWriteoffApprovalV1 | independently assigned writeoff-approval-ledger Core commit tuple plus exact current approval-series/policy/target-scope heads and one-use state |
      | CompensationForfeitureDecisionV1 or DebtWriteoffDecisionV1 | independently assigned decision-ledger Core commit tuple plus exact current series/target-state/policy/finding-or-approval heads and historical accepted-terms lineage |
      | TowerPayoutInstructionV1 | independently assigned payout-authorization-ledger Core commit tuple plus current instruction/preparation/head relationships |
      | CompensationLedgerHeadV1 | independently assigned head-ledger Core commit tuple plus its accepted chain head and committed SQL snapshot relationship |
      | TowerLifecycleEventV1, StationLifecycleEventV1, or StationEpochResetV1 | independently assigned Core lifecycle-ledger commit tuple plus exact revision/current-head or atomic-group relationship |
      | StationAttachAuthorizationV1 | independently assigned Core authorization-ledger commit tuple plus bounded expiry and one-use consumption |
      | DirectStationOriginAuthorityV1, StationOriginLeaseRevisionAuthorityV1, StationOriginLeaseV1, or StationRehomeLeaseV1 | independently assigned Core origin-ledger transaction tuple plus signed atomic-group index/current-head relationship |
      | RogerTrustDocumentV1 | independently signed RogerTrustPublicationV1 sequence and Core authority tuple under pinned root-delegation history |
      | RogerLedgerCheckpointV1 | verified checkpoint sequence and Core-observed checkpoint time under the accepted published key history |
      | release/update metadata | locally accepted monotonic TUF role versions, expiry check time, and transparency evidence |

  Scenario: Compromise-effective time limits historical trust
    Given a key is revoked for compromise with an authoritative compromise-effective time
    When evidence signed before and after that time is verified
    Then verification reports the time-qualified status according to the signed revocation record and Core ledger observation time
    And a signer-controlled backdated timestamp cannot restore validity

  Scenario: Client verification checks object relationships as well as signatures
    Given individually valid provider, Core transit, optional Tower transit, and settlement signatures
    When their object hashes, identities, job IDs, attempt IDs, leases, nonces, digests, origins, or network IDs do not form one exact chain
    Then the combined receipt is invalid

  Scenario Outline: Receipt v1 cannot be silently treated as receipt v2
    Given a caller requires receipt v2 guarantees
    When it receives "<legacy case>"
    Then verification fails with an explicit unsupported or downgrade result

    Examples:
      | legacy case                                      |
      | a receipt with no version                        |
      | a v1 receipt relabeled v2                        |
      | a v1 Station signature plus a v2 Core signature  |
      | a v2 provider assertion plus a v1 broker receipt |
      | a response whose negotiated version was v2 but carries only v1 |

  Scenario: Verification never copies one signature into another role
    Given a receipt has a Station signature, optional Tower signature, and Roger Core signature
    When a client verifies it
    Then each signature is checked with its role-specific canonical bytes and purpose-bound key
    And moving a signature to another field or verification function fails
