# APPROVED SPEC - founder approved 2026-08-03. Changes to an approved scenario need
# re-approval; they are not a diff to be reviewed.
#
# BUILD STATUS: PARTIAL. Approval is not implementation - this line says which.
# Enforced by internal/towercore/featurestatus_test.go against the "Contract:"
# references in the code. Changing the status without changing the code fails.
#
# Scope: the single authoritative public attempt state machine, legal writers, CAS
# transitions, hold effects, retry creation, deadline/cutoff sweeps, and late evidence.

  Feature: Every public attempt has one Core-owned state and one terminal money outcome
  Tower, Station, client, and transport messages are evidence for Roger Core; none can write
  attempt state directly or revive a terminal attempt.

  Scenario: Attempt creation commits the complete issued authority atomically
    Given a fresh authenticated idempotent client request passed policy and has sufficient funds
    When Roger Core creates an attempt
    Then one transaction commits disclosure-safe AttemptIssueCommitmentV1 plus signed AttemptEventV1 state issued, fixed attempt revision 1, exact hold, immutable ExecutionGrantV1 hash, exact FundingSourceReservationV1 and FundingSourceReservationSetV1 complete hashes, origin-specific DispatchLeaseV1 hash or canonical absence, Tower/Station admission revisions, exact GrantCompensationSnapshotV1 complete hash or canonical absence, execution deadline, settlement-finalization/hold ceiling, canonical prior-event absence, and an independently assigned Core attempt-ledger issue time/global sequence copied byte-identically across the commitment, event, grant, and lease
    And the lease or grant cannot be transmitted before that commit
    And failure before the transaction commits creates no attempt or hold

  Scenario: Attempt event history has one canonical creation link
    Given Roger Core commits an event for attempt A at revision R
    Then issued is revision 1 with canonical prior-event absence
    And every later event is exactly the accepted revision plus one and binds that exact immediately prior attempt-event complete hash
    And exact replay is idempotent, while zero, a skipped/overflowing revision, prior presence at creation, absence later, wrong-attempt prior, wrong prior, or conflicting event bytes at one revision fails before state or hold mutation

  Scenario: AttemptEventV1 has one strict signed and independently anchored state
    Given a public attempt transition is proposed
    When the purpose-separated attempt-state service commits AttemptEventV1
    Then its strict bytes contain only schema/network/protocol, attempt-state signer key ID, deterministic event ID, job/request/attempt IDs, positive revision, previous AttemptEventV1 complete hash or canonical issued absence, closed event kind and resulting state, AttemptIssueCommitmentV1 complete hash or canonical post-issue inherited hash, ExecutionGrantV1 complete hash, DispatchLeaseV1 complete hash or canonical direct absence, FundingSourceReservationV1 and FundingSourceReservationSetV1 complete hashes, grant-time compensation-snapshot hash or canonical absence, exact hold ID/currency/unit/scale/amount/state, Tower/Station authority revisions, execution deadline, settlement-finalization/hold ceiling, event-kind evidence complete hash or canonical issued absence, terminal reason or canonical nonterminal absence, FundingSourceReleaseTransitionV1 stable ID/expected funding-ledger index or canonical nonrelease absence, and independently assigned Core attempt-ledger commit time/global sequence
    And event ID derives from strict JCS [AttemptEventV1-id-v1,network-ID,attempt-ID,revision], revision 1 is issued with prior absence, and each later revision is current plus one with immediate prior under one state/hold/funding CAS
    And issued commit tuple and object hashes are byte-identical to its AttemptIssueCommitmentV1 relationship and the Core issue tuple signed into ExecutionGrantV1 and DispatchLeaseV1, while failed/expired/cancelled terminal events alone may name a release transition and settled never may
    And key validity, compromise cutoff, deadlines, and event ordering derive from the independently assigned commit tuple, never signer issue time

  Scenario: AttemptIssueCommitmentV1 proves issuance without disclosing money to a Tower
    Given grant and joined lease bytes are fixed before attempt revision 1 commits
    When the attempt-state signer constructs AttemptIssueCommitmentV1
    Then its strict bytes contain only schema/network/protocol, attempt-state signer key ID, deterministic commitment ID, job/attempt IDs, direct or joined origin, ExecutionGrantV1 complete hash, DispatchLeaseV1 complete hash or canonical direct absence, execution deadline, settlement-finalization ceiling, expected attempt-ledger index, and independently assigned Core issue time/global sequence
    And commitment ID derives from strict JCS [AttemptIssueCommitmentV1-id-v1,network-ID,attempt-ID], while grant and lease preselect only that ID/index/tuple and contain no commitment hash
    And the full issued AttemptEventV1 binds the commitment complete hash plus private hold/client/money state and commits atomically; the commitment contains no hold ID, amount, currency, client/account identity, price, or funding source

  Scenario Outline: Nonterminal attempt transitions are exhaustive
    Given attempt A is in state "<from>"
    When the Core-authorized event "<event>" commits
    Then A becomes "<to>"
    And the hold effect is "<hold effect>"

    Examples:
      | from | event | to | hold effect |
      | issued | exact lease/grant is accepted for dispatch on its bound session | leased | unchanged and reserved |
      | issued | dispatch fails before acceptance and retry policy stops this attempt | failed | released exactly once |
      | issued | signed deadline sweep wins | expired | released exactly once |
      | issued | lifecycle cancel_at sweep wins | cancelled | released exactly once |
      | leased | selected Station's authenticated grant claim is observed | executing | unchanged and reserved |
      | leased | complete result and ProviderAssertionV2 become durably observed without a prior executing observation | evidence_complete | unchanged and reserved |
      | leased | transport or Station failure becomes terminal | failed | released exactly once |
      | leased | signed deadline sweep wins before complete evidence | expired | released exactly once |
      | leased | lifecycle cancel_at sweep wins before eligible complete evidence | cancelled | released exactly once |
      | executing | complete result and ProviderAssertionV2 become durably observed | evidence_complete | unchanged and reserved |
      | executing | execution or validation failure becomes terminal | failed | released exactly once |
      | executing | signed deadline sweep wins before complete evidence | expired | released exactly once |
      | executing | lifecycle cancel_at sweep wins before eligible complete evidence | cancelled | released exactly once |
      | evidence_complete | exact validation and the atomic settlement transaction commit | settled | exact cost captured and exact remainder released |
      | evidence_complete | complete evidence is cryptographically or contextually invalid | failed | released exactly once |
      | evidence_complete | settlement-finalization/hold ceiling sweep wins before a successful receipt commits | failed | released exactly once |

  Scenario Outline: Every unlisted attempt transition fails without authority
    Given attempt A is in state "<state>"
    When a caller requests "<transition>"
    Then A, its hold, receipt, earnings, retry links, and event hash are unchanged

    Examples:
      | state | transition |
      | issued | directly to executing without exact bound dispatch |
      | issued | directly to settled |
      | leased | back to issued |
      | executing | back to leased or issued |
      | evidence_complete | back to executing, leased, or issued |
      | evidence_complete | expired solely because settlement storage was temporarily unavailable after timely evidence |
      | settled | any different state |
      | failed | any different state |
      | expired | any different state |
      | cancelled | any different state |
      | any | an unknown state value |

  Scenario: Only Roger Core commits attempt transitions
    Given a Tower, Station, client, administrator, deadline worker, and lifecycle worker can submit messages or evidence
    When an attempt transition is evaluated
    Then only a Roger Core database transaction with the expected attempt ID, revision, prior event hash, grant/lease hashes, and current state may commit it
    And Tower/Station timestamps, local states, or signed claims cannot choose the transition or hold row

  Scenario: Attempt CAS gives concurrent events one winner
    Given attempt A has state S, revision R, and prior event hash H
    When result, timeout, cancellation, transport failure, retry, and duplicate workers race
    Then at most one transaction compare-and-swaps A, S, R, and H
    And every losing worker rereads the committed state and returns its exact idempotent outcome
    And debit, hold release, Station earning, candidate, and final receipt each occur at most once

  Scenario: Terminal states have exact durable effects
    Given attempt A reaches a terminal state
    Then settled requires one successful SettlementReceiptV2 and matching debit/Station credit
    And failed requires a closed failure reason and complete hold release with no successful receipt
    And expired requires the signed deadline and sweep authority tuple with complete hold release
    And cancelled requires the lifecycle event/cutoff reference with complete hold release
    And every terminal state binds one Core event sequence and previous event hash

  Scenario: A retry is a new attempt after the prior attempt is terminal
    Given an attempt is failed, expired, or cancelled and request-level retry policy permits another try
    When Roger Core retries the request
    Then the old attempt remains immutable and terminal
    And the new attempt has a new attempt ID, grant nonce, dispatch nonce, grant/lease signatures, Station/origin binding, deadline, state revision, and hold allocation
    And the request record links the ordered attempts without reusing evidence or charging both

  Scenario: No superseded state or in-place retry exists
    Given an attempt is nonterminal or terminal
    When retry logic runs
    Then it never changes that attempt to an undefined superseded state or rewrites its grant
    And a nonterminal attempt must first reach one defined terminal state before another attempt is issued

  Scenario: Deadline and cancellation sweeps run without traffic
    Given issued, leased, or executing attempts receive no more messages
    When a signed deadline or lifecycle cancel_at cutoff becomes due
    Then a durable Core sweep claims each attempt idempotently and transitions it to expired or cancelled as applicable
    And its hold is released without waiting for a Tower, Station, client, or reconnect

  Scenario: Timely evidence survives its execution deadline only until the signed finalization ceiling
    Given complete exact evidence entered evidence_complete with an authority tuple strictly before its execution deadline and lifecycle cutoff
    But settlement storage or signing is temporarily unavailable until after the execution deadline and before the settlement-finalization/hold ceiling
    When recovery regains every required authority
    Then it retries the same evidence_complete-to-settled transaction
    And it does not expire or fail the attempt merely because processing resumed after the execution deadline

  Scenario: The finalization ceiling releases a hold with one explicit zero-money outcome
    Given timely exact evidence is evidence_complete but no SettlementReceiptV2 committed strictly before its signed settlement-finalization/hold ceiling H
    When the H ceiling sweep and any late settlement worker compare-and-swap the attempt
    Then the one winner commits failed with reason core-finalization-timeout and releases the complete consumer hold once
    And no consumer debit, Station earning, Tower candidate, compensation, or protocol-level platform liability is created
    And the terminal attempt event binds H, the evidence hash, dependency class, Core authority tuple, and a durable incident reference for separately authorized remediation
    And a late recovered signer, receipt, result, or retry cannot charge or revive the attempt
    And if the authoritative attempt/hold store was unavailable at H, its first recovery transaction applies this same ceiling outcome

  Scenario Outline: Late or duplicate evidence cannot revive a terminal attempt
    Given attempt A is "<terminal>"
    When exact or altered result, assertion, Tower statement, frame, grant claim, or retry evidence arrives
    Then A and all money state remain unchanged
    And the delivery receives the terminal idempotent outcome or a bounded late/conflict rejection without content disclosure

    Examples:
      | terminal |
      | settled |
      | failed |
      | expired |
      | cancelled |

  Scenario: Attempt-state store uncertainty fails closed
    Given Roger Core cannot durably read the current state, revision, prior hash, hold, or lifecycle/deadline authority
    When any transition is requested
    Then it commits no state or money mutation and sends no new lease or grant
    And readiness for new public work is false while recovery uses only durable authoritative state
