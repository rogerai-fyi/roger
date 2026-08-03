# PROPOSED SPEC — founder approval is required before step definitions or implementation.
#
# Scope: outer/inner transport security, execution-grant integrity, exact job/result
# context binding, replay/failure behavior, and the authoritative settlement transition.
# Receipt object canonicalization and public verification are in receipt_v2.feature.

Feature: A hostile joined Tower cannot alter, replay, substitute, or settle public work
  Roger Core signs one bounded attempt for one Tower and Station. Settlement occurs only
  after the returned evidence matches every authoritative field and durable job state.

  Background:
    Given an active joined Tower authenticated by short-lived mutual TLS
    And an attached admitted Station with an authenticated inner secure session to Roger Core
    And a funded client whose request passed central authentication and policy

  # --- two security layers -------------------------------------------------

  Scenario: The outer channel authenticates the Tower and encrypts transport
    When the Tower opens its outbound Roger Core connection
    Then TLS 1.3 mutual authentication binds the connection to its active Tower ID and session
    And plaintext application messages are not sent before authentication and negotiation
    And TLS early data is disabled for every control and job message

  Scenario: The inner TLS session authenticates Roger Core and the selected Station end to end
    When the Station establishes its secure session through the Tower byte relay
    Then TLS 1.3 mutual authentication makes Roger Core verify the selected Station secure-session certificate and makes the Station verify Roger Core
    And Roger Core binds that inner session to the Station ID, assertion key, certificate serial, session epoch, channel-binding hash, and observed Tower origin
    And the Station verifies the Roger Core trust root and expected public network
    And the Tower does not possess either inner-session private key

  Scenario Outline: Roger Core rejects an invalid Station inner-session certificate
    Given the selected Station presents "<certificate>" through the Tower relay
    When the inner TLS 1.3 handshake runs
    Then Roger Core accepts no request plaintext or execution grant on that inner session
    And the Tower cannot repair the failed Station identity with its own credential

    Examples:
      | certificate |
      | none |
      | self-signed or issued by an unknown Station-session authority |
      | valid for another Station ID |
      | valid for another secure-session key |
      | valid for another network or key purpose |
      | not yet valid |
      | expired |
      | revoked before the Core authority tuple |
      | missing the required Station URI identity or key usage |
      | signed correctly but presented without proof of its private key |
      | inconsistent with the admitted StationOfferV1 secure-session key hash |

  Scenario Outline: The Station rejects an invalid Roger Core inner-session peer
    Given a Station's inner TLS peer presents "<Core identity>"
    When the Station validates the handshake through its pinned public-network bootstrap and current trust document
    Then it accepts no grant or request plaintext on that session

    Examples:
      | Core identity |
      | no certificate |
      | an ordinary public certificate outside the Roger Core purpose |
      | a Tower or Station certificate |
      | a certificate under an unknown, stale, or rolled-back trust document |
      | a certificate for another network or hostname/URI identity |
      | a not-yet-valid, expired, or revoked certificate |
      | a valid certificate without proof of its private key |

  Scenario Outline: A Tower cannot terminate or substitute the inner session
    Given the Tower attempts "<attack>"
    When Roger Core and the Station authenticate the inner session
    Then authentication fails before a request body or completion plaintext is accepted
    And no job is settled

    Examples:
      | attack                                             |
      | present its Tower certificate as a Station          |
      | present a self-signed Station certificate           |
      | substitute another admitted Station key             |
      | substitute another Roger Core endpoint               |
      | downgrade the secure-session version                 |
      | strip mandatory authenticated encryption             |
      | replay an old secure-session handshake                |
      | alter handshake bytes                                 |

  Scenario: Every TLS session binding is exporter-derived and domain-separated
    Given an authenticated Tower outer or Station inner TLS 1.3 connection
    When Roger Core derives its signed channel-binding hash
    Then it hashes the RFC 9266 tls-exporter binding with the network ID, negotiated protocol version, endpoint roles, and Core-assigned session epoch
    And it never treats the channel-binding value as secret key material
    And another connection, resumption, network, protocol, or role context cannot reuse it

  Scenario: TLS resumption cannot resume a v1 attempt
    Given a Tower or Station reconnects using an otherwise valid TLS resumption mechanism
    When the resumed transport authenticates
    Then Roger Core assigns a fresh session epoch and channel-binding hash
    And old leases, grants, frames, and incomplete attempts remain fenced from that session

  Scenario: A non-serving Tower packet capture contains no content plaintext
    Given the Tower operator does not also control the selected Station process
    When text, tool, image, speech-to-text, and text-to-speech requests and results traverse it
    Then packet capture and Tower logs reveal no prompt, tool argument, image, audio, transcript, or completion plaintext
    And only documented routing metadata, opaque ciphertext digests, timing, sizes, peer addresses, and error classes are observable

  Scenario: Roger Core still sees content under the v1 policy contract
    When a public request is screened, routed, returned, and recounted
    Then Roger Core can inspect the request before dispatch and the result before settlement
    And no UI or documentation calls this client-to-Station end-to-end privacy

  # --- immutable dispatch lease and execution grant ------------------------

  Scenario: Roger Core gives the Tower a minimal signed dispatch lease
    Given Roger Core selected one exact Tower and Station
    When it dispatches an opaque encrypted request envelope
    Then the Tower-visible lease binds schema, dispatch-signer key, network, protocol, issue time, independently assigned Core attempt-issue time/global sequence, AttemptIssueCommitmentV1 stable ID/expected attempt-ledger index, job, attempt, dispatch nonce, Tower, Tower certificate serial, Tower session epoch/channel binding, exact current TowerAdmissionLeaseV1 stable series/lease ID/sequence/complete hash, exact current TowerLifecycleEventV1 revision/complete hash, Station, Station origin epoch, encrypted-envelope digest, deadline, and byte/stream limits
    And it contains no prompt-derived digest, model parameter, price, user identity, or plaintext
    And the Tower can verify only the routing authority it needs

  Scenario Outline: Mutating any Tower-visible dispatch field invalidates the lease
    Given a valid signed dispatch lease
    When its "<field>" is altered
    Then the Tower, Station, and Roger Core reject the altered lease or result context
    And no attempt settles under it

    Examples:
      | field                     |
      | network ID                |
      | schema version            |
      | dispatch-signer key ID    |
      | protocol version          |
      | issue time                |
      | job ID                    |
      | attempt ID                |
      | dispatch nonce            |
      | Tower ID                  |
      | Tower certificate serial  |
      | Tower session epoch/channel binding |
      | Station ID                |
      | Station origin epoch      |
      | encrypted-envelope digest |
      | deadline                  |
      | byte limit                |
      | stream limit              |
      | Tower admission-lease series, lease ID, sequence, and complete hash |
      | Tower lifecycle revision and complete hash |

  Scenario: Roger Core issues a one-use grant after policy and hold authorization
    Given Roger Core selected one exact Tower and Station
    When it creates an execution attempt
    Then the inner encrypted signed grant binds exactly the ExecutionGrantV1 signed field set enumerated in features/tower/tamper/tamper_job_authority.feature, including its origin-specific fields and canonical foreign-origin absences
    And no field is added, omitted, or renamed relative to that enumeration, which remains the single source of truth
    And the settlement-finalization/hold ceiling is strictly after the execution deadline and no later than the signed policy's maximum finalization interval
    And the durable signed issued AttemptEventV1 and disclosure-safe AttemptIssueCommitmentV1 commit atomically before dispatch; grant/lease preselect the commitment ID/index/tuple, the commitment signs their exact hashes, and the event signs the commitment hash
    And every direct, joined uncompensated, and joined compensated grant issue transaction compare-and-swaps the FundingSourceReservationV1 open head, every exact resulting reserved FundingSourceLotV1 head, the current FundingAllocationPolicyV1/publication head, and the funding-source/policy signers' active nonrevoked states
    And it resolves each bound ConsumerCashCreditAuthorityV1 or PlatformGrantCreditV1 provenance signer against the greatest accepted trust publication at that authority's original independent commit tuple; normal later rotation preserves historical validity, but a newly accepted compromise-effective cutoff covering that tuple blocks live grant use
    And a released, settled, stale, policy-mismatched, source-drifted, or unverifiable reservation creates no grant or attempt regardless of compensation tier
    And Core sends only the disclosure-safe commitment with the lease/grant to the Tower and Station; they verify its attempt-state signature, index/tuple, their exact object-hash membership, and current key status before relaying or executing, while the hold/client/money-bearing AttemptEventV1 remains Core-private

  Scenario Outline: Mutating any grant field invalidates authorization
    Given a valid signed execution grant
    When "<field>" is changed, removed, duplicated, retyped, or re-encoded ambiguously after signing
    Then the Station and Roger Core reject the grant before execution or settlement

    Examples:
      | field                    |
      | network ID               |
      | schema version           |
      | grant-signer key ID      |
      | protocol version         |
      | origin kind              |
      | job ID                   |
      | request ID               |
      | attempt ID               |
      | client key ID            |
      | client nonce/idempotency key |
      | grant nonce              |
      | Tower ID                 |
      | Tower certificate serial |
      | Tower outer-session epoch/channel binding |
      | Station ID               |
      | Station assertion key ID |
      | Station secure-session certificate serial |
      | Station inner-session epoch/channel binding |
      | Station origin epoch     |
      | Station assertion epoch  |
      | Station lifecycle revision and complete hash |
      | joined StationOriginLeaseV1 or direct DirectStationOriginAuthorityV1 ID/revision/complete hash |
      | model                    |
      | offer ID                 |
      | quote ID                 |
      | consumer input rate      |
      | consumer output rate     |
      | Station-earning input rate |
      | Station-earning output rate |
      | currency, unit, or scale |
      | maximum input tokens     |
      | maximum output tokens    |
      | maximum cost             |
      | request digest           |
      | policy version           |
      | grant-time compensation-snapshot complete hash or canonical absence |
      | issued time              |
      | execution deadline       |
      | settlement-finalization/hold ceiling |
      | Tower admission-lease series/lease ID/sequence/complete hash or direct canonical absence |
      | Tower lifecycle revision/complete hash or direct canonical absence |
      | direct Station grant sequence or joined canonical absence |
      | modality                 |
      | result-size limit        |

  Scenario Outline: A correctly signed grant is still rejected outside its context
    Given a valid signed execution grant
    When it is presented "<context>"
    Then it is not executed or settled

    Examples:
      | context                                      |
      | on another RogerAI network                   |
      | through another Tower                        |
      | to another Station                           |
      | under another Tower session                  |
      | under another Station inner session          |
      | before its issue time beyond clock skew      |
      | after its deadline                           |
      | after its attempt was failed                 |
      | after its prior attempt failed and a retry was issued |
      | after its attempt was settled                |
      | after its Tower was revoked                  |
      | with a request body whose digest differs     |
      | with a model body whose model differs        |
      | with limits above its signed bounds          |

  Scenario Outline: Origin kind has exactly one grant authorization shape
    Given an "<origin>" execution attempt
    When its grant and provider assertion are canonicalized
    Then the execution grant has "<grant shape>"
    And the provider assertion has "<assertion shape>"
    And forbidden-present, required-absent, null, zero-filled, or mismatched origin fields are rejected

    Examples:
      | origin | grant shape | assertion shape |
      | direct | Station inner-session fields and direct Station grant sequence are present; Tower identity, Tower certificate, Tower outer session, Tower admission-lease identity/head, and Tower lifecycle head are absent | Station inner-session fields are present; dispatch-lease hash and Tower fields are absent |
      | joined | Station inner-session, Tower identity, Tower certificate, Tower outer-session, current Tower admission-lease identity/head, and Tower lifecycle head are present; direct Station grant sequence is absent | Station inner-session fields, matching dispatch-lease hash, and every joined Tower field are present |

  Scenario: A Station accepts a valid grant once
    Given a valid fresh grant and matching encrypted request body
    When the selected Station atomically claims its grant nonce and the origin-specific Tower lease or direct Station grant sequence
    Then it executes the attempt once
    And an exact sequential replay is rejected before executing again

  Scenario: Concurrent replay executes at most once
    Given two sessions concurrently deliver the same valid grant and body
    When the Station and Roger Core process both
    Then at most one attempt reaches executing state
    And at most one result can later settle

  Scenario: A client signs a one-use nonce before Roger Core creates a hold or grant
    Given a client request signature binds its client key ID, account, network, method, canonical path and query, body digest, model, modality, issue/expiry times, and a high-entropy client nonce/idempotency key
    When Roger Core atomically consumes that signed key
    Then exactly one authoritative request creates the hold and attempt
    And the Core-created grant contains the same client key plus a separate Core-created nonce

  Scenario: Invalid client authentication cannot squat an idempotency key
    Given an attacker submits an invalid, wrong-account, stale, or malformed client signature with another client's nonce/idempotency key
    When Roger Core authenticates the request
    Then authentication fails before nonce lookup or consumption
    And the legitimate signed request can still atomically claim its key

  Scenario Outline: An authenticated exact retry resolves to the original request only
    Given the same client presents fresh current account/session authorization plus a fresh response-retrieval challenge signature
    And it resubmits the exact consumed ClientRequestAuthorizationV1 inside its freshness window
    And Roger Core already consumed its client nonce/idempotency key
    And the original request is "<state>"
    When the exact signed request is replayed sequentially or concurrently
    Then no new hold, attempt, dispatch lease, or grant is created
    And the replay receives "<outcome>"
    And no charge or earning is duplicated

    Examples:
      | state | outcome |
      | running unary request | stable existing-request status and retrieval ID |
      | completed unary request | stored terminal response and receipt |
      | failed unary request | the same stored terminal failure |
      | active streaming request | authenticated existing-request status and retrieval ID, never a silently duplicated or rejoined stream |
      | completed streaming request | stored terminal metadata and receipt under the documented bounded replay policy |

  Scenario Outline: A captured creation signature cannot disclose the existing request
    Given an attacker captured an exact valid ClientRequestAuthorizationV1 but lacks fresh current account/session and response-retrieval authorization
    When it replays the creation signature while the original request is "<state>"
    Then Roger Core creates no hold, attempt, lease, grant, stream, or retrieval credential
    And returns only the same generic unauthorized response for every state
    And reveals no existence, status, timing, retrieval ID, content, receipt, or terminal error from the original request

    Examples:
      | state |
      | nonexistent |
      | running |
      | completed |
      | failed |
      | expired from response retention |

  Scenario Outline: A consumed client key cannot authorize altered context
    Given Roger Core consumed a client nonce/idempotency key for one signed request
    When the same key is signed or presented with a different "<field>"
    Then Roger Core returns an idempotency conflict before a hold or attempt

    Examples:
      | field        |
      | account      |
      | network      |
      | HTTP method  |
      | path         |
      | body digest  |
      | model        |

  Scenario: Concurrent divergent requests choose one canonical idempotency context
    Given two valid requests for one account race with the same client nonce/idempotency key but different signed body digests
    When Roger Core atomically claims the key
    Then exactly one canonical request hash becomes authoritative
    And the other receives an idempotency conflict before a hold or attempt

  Scenario: Idempotency tombstones outlive every replay and money window
    Given an authoritative request has reached its terminal state and response retention expires
    When compacting idempotency state
    Then uniqueness scope is network plus authenticated account plus idempotency key
    And the tombstone binds client key ID and the complete canonical request hash
    And Roger Core retains full idempotency state through the longest signature, retry, ledger, refund, dispute, chargeback, and recourse window
    And thereafter retains a compact permanent uniqueness tombstone
    And the same scoped key cannot later create another request

  Scenario: Replay-store failure blocks new money state
    Given Roger Core cannot durably read or consume the client nonce/idempotency key
    When it receives an otherwise valid new client request
    Then it fails closed before creating a hold, attempt, lease, or grant

  # --- bounded framing and flow control -----------------------------------

  Scenario Outline: Invalid joined frames have one deterministic failure scope
    Given an authenticated joined session
    When the Tower sends "<frame>"
    Then Roger Core performs "<action>"
    And never allocates unbounded memory, writes money, or interprets it as another message type

    Examples:
      | frame                                          | action                                      |
      | an unknown mandatory message type              | close the joined session                    |
      | a negative or overflowing logical length       | close the joined session                    |
      | a stream ID from an old session                | close the joined session                    |
      | a length above the per-stream maximum          | fail that stream and its attempt            |
      | truncated content                              | fail that stream and its attempt            |
      | trailing unsigned content                      | fail that stream and its attempt            |
      | duplicate unique field                         | fail that stream and its attempt            |
      | invalid UTF-8 in a text-only field              | fail that stream and its attempt            |
      | compressed content expanding above its bound   | fail that stream and its attempt            |
      | a stream ID already in use                     | fail the duplicate stream                   |
      | too many simultaneous streams                  | reject the excess stream as overloaded      |
      | too much aggregate buffered data               | reject new data and fail its affected stream|

  Scenario: A slow Tower cannot pin unlimited Core resources
    Given the Tower stops reading or writing on many partial streams
    When byte, stream, and deadline limits are reached
    Then affected attempts time out and buffers are released
    And no unsettled hold becomes an earning
    And other Towers remain schedulable

  # --- exact result and provider-assertion binding -------------------------

  Scenario Outline: Every relay modality enforces the same exact context
    Given a valid "<modality>" attempt for job J, request R, Station S, Tower T, and model M
    When a signed provider assertion and result arrive
    Then Roger Core compares every authoritative context field before recount or settlement
    And only exact matching evidence proceeds

    Examples:
      | modality       |
      | chat           |
      | chat_streaming |
      | speech_to_text |
      | text_to_speech |

  Scenario Outline: A valid Station signature cannot excuse a context mismatch
    Given a Station signs a cryptographically valid provider assertion
    And the result is posted under the correct transport job ID
    When the assertion's "<field>" differs from the authoritative attempt
    Then Roger Core rejects it before recount and settlement
    And it neither debits the client nor credits the Station or Tower
    And it does not mutate any other job's hold

    Examples:
      | field                 |
      | network ID            |
      | protocol version      |
      | job ID                |
      | request ID            |
      | attempt ID            |
      | execution-grant hash  |
      | dispatch-lease hash   |
      | client key ID         |
      | client nonce/idempotency key |
      | grant nonce           |
      | Tower ID              |
      | Tower certificate serial |
      | origin kind           |
      | Tower outer-session epoch/channel binding or direct absence |
      | Station ID            |
      | Station assertion key ID |
      | Station secure-session certificate serial |
      | Station inner-session epoch/channel binding |
      | model                 |
      | offer ID              |
      | quote ID              |
      | Tower admission-lease stable series ID, lease ID, sequence, or complete hash |
      | Tower lifecycle revision or complete hash |
      | execution deadline    |
      | settlement-finalization/hold ceiling |
      | signed byte, token, result, or cost bound |
      | request digest        |
      | response digest       |
      | result status         |
      | start time            |
      | end time              |
      | Station assertion epoch |
      | Station sequence      |
      | previous assertion hash |

  Scenario: Joined Tower authority duplicates one exact issued head across every object
    Given a joined attempt was issued under TowerAdmissionLeaseV1 stable series D, lease ID L, sequence Q, complete hash H and TowerLifecycleEventV1 revision R, complete hash T
    When DispatchLeaseV1, ExecutionGrantV1, ProviderAssertionV2, and SettlementReceiptV2 are relationship-checked against the durable authoritative attempt
    Then every duplicated D, L, Q, H, R, and T field is byte-identical across all five authorities and the Tower certificate/session fields agree with that exact lease head
    And D/L/Q/H/T were the unique current unexpired lease/lifecycle heads at attempt issue, while a later lifecycle restriction is evaluated separately through its signed cutoff and cannot rewrite the issued context
    And direct origin requires canonical absence of every D/L/Q/H/R/T Tower field in the grant, assertion, receipt, and durable attempt
    And one mismatched, omitted, null, stale, foreign, re-encoded, or inserted-direct field rejects the evidence before recount or settlement even when every individual signature verifies

  Scenario: A receipt for request B posted as the result for job A cannot settle either hold incorrectly
    Given job A has an active hold and job B has a different active or completed state
    And the selected Station returns result transport ID A with a valid signature over request ID B
    When Roger Core validates the result
    Then the evidence is rejected as a context mismatch
    And settlement never addresses a hold using request ID B from the assertion
    And job A follows its authoritative failure and hold-release transition exactly once
    And job B's balance, hold, settlement, and earning state are unchanged

  Scenario: A result body must match the signed response digest
    Given a valid provider assertion commits to response digest H
    When the Tower changes, truncates, prefixes, appends, reorders, or substitutes result bytes
    Then Roger Core computes a different digest and rejects the result
    And no altered bytes are presented as a verified successful response
    And no settlement occurs

  Scenario: A request body must match the grant and provider assertion
    Given a grant commits to request digest H
    When the Station receives different plaintext or signs a different request digest
    Then execution evidence cannot settle
    And the mismatch is attributable without exposing content in Tower logs

  Scenario: A Tower cannot forge a Station assertion
    Given a Tower controls its own valid identity but not the selected Station key
    When it fabricates a provider assertion and result
    Then Station-signature verification fails and no settlement occurs

  Scenario: A Station and Tower may collude but cannot alter central bounds
    Given the selected Station and Tower jointly sign false usage or metadata
    When Roger Core validates and recounts the attempt
    Then their signatures provide attribution but not truth authority
    And billed usage and price cannot exceed the independently authorized and verified bounds
    And centrally observed abuse can quarantine both identities

  Scenario: Roger Core records channel-bound transit from its own observation
    Given opaque request and result envelopes traverse the authenticated Tower session
    When Roger Core records CoreTransitObservationV1
    Then it signs the Tower ID, certificate serial, session epoch and channel binding, dispatch-lease hash, job and attempt IDs, opaque envelope digests, byte counts, Core first/complete receive times, and the durable evidence-complete authority tuple
    And a Tower-controlled timestamp or statement cannot replace those observed values
    And this evidence attributes the authenticated key but does not prove the key's physical location

  Scenario Outline: A joined settlement requires a complete matching Core transit observation
    Given a joined result and ProviderAssertionV2 have "<Core observation state>"
    When Roger Core validates settlement
    Then no successful Station/client settlement or compensation candidate exists
    And the consumer hold follows the authoritative failure path once

    Examples:
      | Core observation state |
      | no CoreTransitObservationV1 |
      | an observation from another Tower session |
      | an observation with another lease, envelope digest, job, or attempt |
      | an observation whose complete authority tuple is at or after deadline or lifecycle cutoff |

  Scenario Outline: Tower corroboration has deterministic settlement and compensation behavior
    Given a valid joined provider assertion, result, and Core transit observation
    When the Tower statement is "<state>"
    Then Station/client settlement is "<settlement>"
    And Tower compensation candidacy is "<compensation>"
    And no Tower claim can change authoritative price, billed counts, cost, hold, grant, or settlement disposition

    Examples:
      | state                                      | settlement                         | compensation                         |
      | valid and exactly consistent               | allowed after every other check     | eligible for later funds checks      |
      | missing                                    | allowed after every other check     | ineligible: missing corroboration     |
      | signed by the wrong Tower                  | allowed after every other check     | ineligible: wrong signer              |
      | mismatched to the lease or envelope digest | allowed after every other check     | ineligible: transit mismatch          |
      | replayed from another attempt              | allowed after every other check     | ineligible: replayed corroboration     |

  Scenario: Tower corroboration received after settlement is audit-only
    Given a joined settlement committed successfully with Tower-statement status missing and immutable ineligible reason missing-corroboration
    When a later valid matching TowerTransitStatementV1 arrives
    Then it cannot reopen settlement or upgrade the candidate
    And it moves no consumer, Station, Tower, hold, entitlement, or payout state
    And it is retained only as bounded late-corroboration audit evidence

  Scenario: A direct Station has a direct origin and no Tower transit fields
    Given Roger Core dispatched an attempt directly to a Station
    When it creates and settles the attempt evidence
    Then origin kind and the Station secure-session certificate serial and epoch/channel binding are present
    And every Tower identity, Tower certificate/session, Core Tower-session observation, TowerTransitStatementV1 hash, and statement-rejection reason is canonically absent, while Tower-statement status is present as not_applicable
    And Roger Core records the common durable evidence-complete authority tuple after the complete result and ProviderAssertionV2 are stored
    And absence is valid for Station/client settlement but never eligible for Tower compensation

  # --- replay, ordering, retry, and late arrival ---------------------------

  Scenario Outline: Duplicate delivery settles at most once
    Given one attempt has a valid result and provider assertion
    When "<duplicate>" arrives concurrently or sequentially
    Then the client is debited at most once
    And the Station is credited at most once
    And one immutable settlement outcome is returned for the attempt

    Examples:
      | duplicate                                      |
      | the exact result                               |
      | the exact provider assertion                   |
      | the exact Tower transit statement              |
      | the same evidence on another Core instance     |
      | the same evidence after process restart        |
      | the same evidence after response loss          |

  Scenario: Reusing evidence under a new attempt is rejected
    Given an earlier attempt failed and Roger Core issued a retry with a new attempt and nonce
    When the old result or provider assertion is relabeled as the retry
    Then the grant hash, attempt, nonce, and digest binding fail
    And neither attempt double-settles

  Scenario: A late success after an authoritative timeout cannot mint an earning
    Given Roger Core durably marked an attempt failed after its deadline and released its hold allocation
    When a cryptographically valid result arrives late
    Then the attempt remains failed
    And the result is retained only as late operational evidence within retention policy
    And no client debit or Station credit is created

  Scenario: A Station allocates assertion sequence at durable signing time
    Given several jobs for one Station complete concurrently
    When the Station serializes and durably spools their signed assertions
    Then each assertion receives one increasing sequence in signing order and the prior assertion hash
    And no dispatch-order assumption or process-global counter is used

  Scenario: Network reordering creates a bounded audit gap rather than a consumer-money lock
    Given Roger Core expects Station assertion epoch E sequence 11 after hash H
    When valid unique sequence 12 arrives before sequence 11
    Then sequence 12 is recorded with chain status pending-gap under a bounded per-Station count, byte, and time limit
    And its exact job remains eligible for settlement after every non-chain check without waiting beyond its own deadline
    And the Station earning is payout-held until sequence 11 closes the link
    And no consumer hold is extended because of the provider's chain gap

  Scenario: A missing chain link cannot consume unbounded Core state
    Given a Station's pending gaps reach their count, byte, or time limit
    When another out-of-order assertion or new routing decision arrives
    Then no new job is routed to that Station and it enters quarantine
    And already valid jobs reach their normal settled or released terminal states
    And gap evidence is retained within the bounded audit policy

  Scenario: An authentic wrong-context expected assertion advances audit order but never money
    Given a Station gap expects sequence 11
    When a valid Station signature and correct previous hash over sequence 11 carries any context mismatch for its authoritative job
    Then its immutable bytes occupy sequence 11 as rejected evidence and the contiguous audit head advances
    And its job settlement is rejected with no debit or provider earning
    And later authentic assertions are not blocked by the rejected job

  Scenario: Chain-gap review closes serving without inventing a money disposition
    Given provider earnings are payout-held behind an unresolved Station gap
    When the signed chain-gap review deadline arrives without exact resolution
    Then Roger Core appends one exact next-revision StationLifecycleEventV1 with state epoch_closed, cancel_at cutoff, prior terminal head, and complete StationEpochClosureEvidenceSetV1
    And the affected earnings remain payout-held with appeal/support reference; v1 neither releases nor forfeits them because Station lifecycle and epoch signers have no provider-money disposition power
    And a new epoch requires explicit StationEpochResetV1 authorization, which does not itself alter those held earnings

  Scenario: The chain-gap deadline sweep runs without new Station traffic
    Given a quarantined Station sends no further message
    When the durable signed review deadline arrives
    Then an independent Core sweep compare-and-swaps the one exact StationLifecycleEventV1 epoch closure exactly once
    And every gap-dependent payout hold remains explicit until a separately approved purpose-signed provider-payout adjudication contract exists

  Scenario: A fork affects every descendant in its epoch
    Given two different valid Station-signed hashes occupy or claim the same epoch and sequence
    When Roger Core detects the fork
    Then the losing claim and every descendant whose prior-hash path depends on it are fork-affected
    And unpaid affected earnings move to explicit escrow until signed adjudication
    And already-paid affected earnings receive append-only recourse records rather than mutated receipts

  Scenario Outline: Duplicate and fork sequences are distinguished
    Given Roger Core already stored Station assertion epoch E sequence 11 with hash H
    When it receives "<case>"
    Then the result is "<result>"

    Examples:
      | case                                      | result                                                    |
      | exact sequence 11 assertion with hash H   | idempotent replay of the existing evidence                 |
      | sequence 11 with a different hash         | fork; reject it, quarantine Station, freeze affected payouts|
      | a lower previously unseen sequence        | reject as invalid historical insertion                     |
      | an unexpected epoch                       | reject until an authorized epoch transition                |
      | a sequence integer overflow               | reject and quarantine before state mutation                 |

  Scenario: An authorized Station epoch reset is explicit
    Given Station state was lost and its prior epoch is closed
    When the owner completes the approved reset process
    Then Roger Core signs StationEpochResetV1 binding Station/owner IDs, byte-identically unchanged origin kind/Tower ID-or-direct-absence/Station origin epoch/capability-ceiling hash, old/new assertion and secure-session keys, prior/exact-next Station assertion epochs, prior terminal head, terminal StationLifecycleEventV1 ID/revision/complete hash, StationEpochClosureEvidenceSetV1 hash or canonical non-gap/fork absence, owner-authorization hash, reason, one-use nonce, effective/cutoff authority tuples, direct-or-joined replacement branch, prescribed replacement origin-authority stable identities/revisions/group indices, replacement certificate serial, replacement lifecycle stable ID/group index, initial sequence, and signer key ID
    And Roger Core creates the new epoch only in one serializable ordered transaction that consumes the nonce and records reset, then creates the epoch_reset StationOriginLeaseRevisionAuthorityV1 plus resulting StationOriginLeaseV1 for joined or the epoch_reset DirectStationOriginAuthorityV1 for direct, commits its replacement credential, and finally appends the exact next-revision active StationLifecycleEventV1
    And no new offer, session, grant, assertion, or attempt is accepted unless that complete branch is current; partial failure leaves the prior lifecycle epoch_closed and every replacement object nonauthoritative
    And sequence restarts only at the defined initial value
    And historical heads remain immutable and verifiable
    And reset grants no release, forfeiture, credit, debit, or provider-payout authority

  Scenario Outline: Station identity material alone cannot reset an epoch
    Given a closed, lost, forked, or quarantined Station assertion epoch
    When "<proof>" requests a new epoch without exact StationEpochResetV1 authority
    Then no epoch, initial sequence, routing eligibility, held payout, or historical head changes

    Examples:
      | proof |
      | a new assertion key |
      | a new secure-session key or certificate |
      | the old Station key |
      | the Station owner's ordinary request signature |
      | a Tower identity or local bridge credential |
      | a replayed or expired reset nonce |
      | a reset object signed by another Core key purpose |

  # --- settlement state and failures --------------------------------------

  Scenario: Settlement uses only the authoritative attempt identity
    Given all evidence exactly matches a held attempt
    When Roger Core recounts and settles
    Then the database transaction addresses the Core-created job, request, and attempt IDs
    And it atomically applies idempotence, hold capture or release, Station earning, chain head, and settlement receipt
    And no identifier copied solely from an untrusted assertion chooses a ledger row

  Scenario Outline: Required state failure fails closed before money movement
    Given an otherwise valid result
    When "<state>" cannot be read or atomically committed
    Then no debit, earning, chain-head advance, or final receipt is partially committed
    And the attempt remains recoverable by its authoritative state machine

    Examples:
      | state                                |
      | issued attempt                       |
      | active hold                          |
      | replay/idempotency claim             |
      | grant-time Tower admission snapshot  |
      | Core-observed lifecycle cutoff       |
      | Station key and ban state            |
      | offer and quote                      |
      | Station sequence uniqueness state    |
      | settlement transaction               |
      | final receipt sequence               |

  Scenario Outline: Lifecycle changes use grant time and Core complete-observed evidence time
    Given a grant was validly issued while the Tower and Station were eligible
    When "<event>" occurs before final database settlement
    Then the attempt result is "<result>"

    Examples:
      | event                                                        | result |
      | draining begins and evidence is complete-observed before min(grant deadline, signed drain ceiling) | settles if every non-lifecycle settlement check passes |
      | ordinary certificate expiry after evidence was complete-observed before deadline | settles if every non-lifecycle settlement check passes |
      | non-security suspension with drain_until(C) before evidence | finishes only when complete-observed before min(grant deadline, C) and settles if every non-lifecycle check passes |
      | security revocation with cancel_at(C) before evidence is complete | attempt is terminal-cancelled and the consumer hold is released once |
      | Tower or Station credential compromise effective before evidence is complete-observed | cannot settle; the consumer hold is released |
      | complete valid evidence observed before a later security cutoff | settles if every non-lifecycle check passes; later enforcement uses holds/recourse |
      | one byte observed before cutoff but evidence completed at or after cutoff | cannot settle; the consumer hold is released |
      | complete evidence observed after the effective deadline | cannot settle; the consumer hold is released |

  Scenario Outline: Timeliness uses complete durable observation in a half-open interval
    Given an attempt has signed wall-clock deadline D and lifecycle completion-cutoff authority tuple C
    When "<delivery>"
    Then the settlement result is "<result>"

    Examples:
      | delivery | result |
      | the evidence-complete authority tuple is before C and its Core time is before D | eligible after every other check |
      | first byte arrives before C but the evidence-complete authority tuple equals C | rejected as cutoff-late and hold released |
      | first byte arrives before D but either completion commits exactly at D | rejected as deadline-late and hold released |
      | the evidence-complete tuple is after C or its Core time is after D | rejected as late and hold released |
      | Station start or end timestamp is backdated before D and C but Core completion is late | rejected as late and hold released |

  Scenario: Equal-time authority events use Core sequence rather than signer time
    Given a completion and lifecycle cutoff have the same stored wall-clock precision
    When their durable commits race
    Then each receives a unique Core authority-event sequence and the lower complete tuple precedes the higher tuple
    And equality means the complete tuple is not before the cutoff
    And Station or Tower timestamps cannot break the tie

  Scenario: Cancel-at terminalizes every affected attempt and hold idempotently
    Given a signed lifecycle event has cancel_at(C)
    When Roger Core applies it across active attempts and instances
    Then each affected attempt becomes terminal-cancelled exactly once
    And each unsettled hold is released exactly once
    And late streams, results, assertions, or retries cannot reopen the attempt

  Scenario: Settlement-finalization ceiling and successful settlement have one CAS winner
    Given complete exact evidence was observed before execution deadline D and lifecycle cutoff C
    And signed settlement-finalization/hold ceiling H is strictly after D and within central policy
    When final settlement and the H ceiling sweep race
    Then a SettlementReceiptV2 may commit only when its Core commit tuple is strictly before H
    And otherwise the attempt becomes failed with reason core-finalization-timeout and its complete hold is released once
    And that failure creates no debit, Station earning, Tower candidate, compensation, or protocol-level platform liability
    And a late signer or recovered dependency cannot change the terminal winner

  Scenario: No executed or usable output costs nothing
    Given an attempt has no complete valid execution evidence and no modality-valid output
    When Roger Core finalizes the attempt
    Then no Station or Tower earning is minted
    And the client's hold is released according to one authoritative failure transition
    And a signed zero-cost settlement disposition records the failed attempt if the ledger is available

  Scenario: A valid STT result blocked by output moderation is still metered
    Given a Station produced complete valid speech-to-text evidence and Roger Core recount
    And central output moderation withholds the transcription as unsafe
    When Roger Core finalizes the attempt under the approved STT contract
    Then the consumer is charged, the Station is credited, and eligible Tower compensation remains a funds-maturity candidate
    And the response disposition records moderation-blocked without exposing the text

  Scenario: A required-moderation outage that withholds STT output is not metered
    Given complete STT evidence exists but Roger Core cannot perform required output moderation
    When the approved fail-closed moderation path returns service unavailable
    Then the consumer hold is released and no Station or Tower earning is minted

  Scenario: A partially delivered stream without complete valid final evidence cannot settle
    Given some streamed bytes reached Roger Core or the client
    But the final response digest and provider assertion are missing or invalid
    When the attempt ends
    Then no unverified usage is charged or credited
    And the attempt is marked failed with bounded evidence

  Scenario: Settlement arithmetic is exact checked integer accounting
    Given Core recount produces nonnegative input count I and output count O within every signed token and modality bound
    And the grant has nonnegative consumer rates CI and CO and Station-earning rates SI and SO in one accounting currency/unit/scale
    When Roger Core computes the settlement
    Then actual consumer cost is checked integer I times CI plus checked integer O times CO
    And Station earning is checked integer I times SI plus checked integer O times SO
    And Station earning is no greater than actual consumer cost
    And actual consumer cost is no greater than both signed maximum cost and the exact authorized hold
    And exact funding slices are the actual-cost prefix of the grant-bound FundingSourceReservationSetV1, sum to actual consumer cost, and their Station allocations sum to Station earning
    And the atomic ledger debit, Station credit, hold release, signed funding-source consume/release transitions, funding intervals, and final receipt conserve those exact values
    And no floating point, implicit currency conversion, saturation arithmetic, or independent rounding is used

  Scenario Outline: An over-bound or invalid settlement fails rather than silently capping
    Given otherwise complete valid evidence has "<condition>"
    When Roger Core validates arithmetic before presenting output as verified or committing money
    Then the attempt becomes failed with reason "<reason>"
    And no consumer debit, Station earning, Tower candidate, funding slice, or successful receipt is created
    And the complete hold is released exactly once

    Examples:
      | condition | reason |
      | Core input recount above the signed input-token maximum | input-bound-exceeded |
      | Core output recount above the signed output-token maximum | output-bound-exceeded |
      | request, result, or stream bytes above a signed byte bound | byte-bound-exceeded |
      | streams above the signed stream bound | stream-bound-exceeded |
      | consumer-cost multiplication or addition overflow | consumer-arithmetic-overflow |
      | Station-earning multiplication or addition overflow | station-arithmetic-overflow |
      | actual consumer cost above signed maximum cost | maximum-cost-exceeded |
      | actual consumer cost above the exact hold | hold-exceeded |
      | Station earning above actual consumer cost | invalid-negative-margin-quote |
      | currency, unit, or scale differs across quote, hold, ledger, or funding slices | accounting-unit-mismatch |

  Scenario: Streaming enforcement stops before accepting a frame that crosses a bound
    Given a streaming attempt is within every signed count, byte, cost, and deadline bound
    When the next frame would make any authoritative bound fail
    Then Roger Core does not deliver that frame as verified output and terminates the attempt as over-bound
    And no partial provider assertion can convert the failed attempt into a capped settlement

  Scenario: Parent disconnection drains rather than inventing offline authority
    Given the joined Tower loses Roger Core while attempts are active
    When their connections recover before or after their deadlines
    Then no new local job is accepted without a fresh Core grant and session-bound lease
    And no v1 attempt migrates or resumes on the replacement Tower session
    And only evidence already complete-observed by Core before disconnect remains settlement-eligible
    And unfinished or expired work cannot later claim settlement
