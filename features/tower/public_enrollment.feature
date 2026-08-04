# APPROVED SPEC - founder approved 2026-08-03. Changes to an approved scenario need
# re-approval; they are not a diff to be reviewed.
#
# Scope: account-bound joined-Tower enrollment, proof of possession, short-lived identity,
# lifecycle, certificate rotation, revocation, and public-advertisement authority.

Feature: Roger Core admits and controls every joined Tower identity
  A community Tower joins the existing network only through a fresh, account-bound,
  capability-scoped admission that Roger Core can expire, quarantine, suspend, or revoke.

  Background:
    Given a Tower initialized in joined mode with distinct identity and TLS keys
    And Roger Core has a supported Tower protocol version

  # --- enrollment token ----------------------------------------------------

  Scenario: An approved operator enrolls a new Tower by proving its local key
    Given a signed-in eligible operator has approved the Tower terms
    And Roger Core issued a short-lived one-time enrollment token for that operator
    When the Tower answers a fresh challenge and submits a CSR bound to its identity key
    Then Roger Core creates exactly one account-bound Tower ID through one acyclic bundle: revision-1 pending-to-quarantine TowerLifecycleEventV1 first, then its prescribed short-lived certificate and sequence-1 TowerAdmissionLeaseV1 binding that lifecycle hash
    And the proof/token, lifecycle event, certificate, and quarantine lease scoped to that Tower ID commit atomically or none do
    And records the identity key, TLS key, owner, token ID, certificate serial, version, and requested capabilities
    And the enrollment token is irreversibly consumed

  Scenario Outline: Invalid enrollment fails without creating partial authority
    Given an enrollment attempt with "<condition>"
    When Roger Core evaluates it
    Then no certificate, lease, active directory entry, or reusable partial Tower identity is created
    And the reason is recorded without logging a token or private key

    Examples:
      | condition                                      |
      | no token                                       |
      | an unknown token                               |
      | an expired token                               |
      | a revoked token                                |
      | a token already consumed successfully          |
      | a token issued to another operator              |
      | a token issued for another Tower ID             |
      | an operator who did not accept current terms    |
      | a suspended operator                            |
      | an operator over the Tower quota                |
      | a missing challenge signature                   |
      | a challenge signature from the wrong key        |
      | a modified challenge                            |
      | a replayed challenge                            |
      | an expired challenge                            |
      | a CSR for a different TLS key                   |
      | an identity key already bound to another Tower  |
      | a TLS key already bound to another Tower        |
      | an unsupported protocol version                 |
      | software below the signed minimum version       |
      | a clock outside the admitted skew               |
      | a malformed capability request                  |
      | a standalone network identity                   |

  Scenario: Concurrent use of one enrollment token creates at most one Tower
    Given two valid enrollment requests race with the same one-time token
    When Roger Core commits them concurrently
    Then exactly one request receives one Tower identity and certificate
    And the other is rejected as a consumed token
    And no duplicate owner, key, or certificate record exists

  Scenario: Retrying a committed enrollment response is idempotent
    Given Roger Core committed enrollment but the response was lost
    When the same Tower retries with the same enrollment transaction and key proof
    Then Roger Core returns the already-issued identity outcome without creating another Tower or certificate

  Scenario: Re-enrollment after local key loss requires a fresh operator decision
    Given an operator has lost an active Tower identity key
    When a new key asks to assume the old Tower ID without a fresh recovery approval
    Then enrollment is rejected
    And the old identity remains suspended or revoked according to recovery policy

  Scenario: V1 rejects in-place Tower ownership transfer
    Given an existing Tower ID is bound to operator A and its admitted identity key
    When operator A, operator B, the Tower, or an administrator requests owner B on that Tower ID
    Then no owner, key, lease, certificate, lifecycle, directory, grant, compensation, balance, or debt binding changes
    And B can operate only a newly enrolled Tower ID after fresh account proof, keys, quarantine, and probes
    And A's Tower is independently drained, suspended, expired, or revoked through the existing lifecycle

  # --- certificate content and channel authentication ----------------------

  Scenario: A joined Tower certificate has one scoped workload identity
    Given enrollment succeeds
    When the certificate is inspected
    Then its URI identity names exactly the issued Tower ID
    And its key usage permits only the joined-Tower channel
    And its issuer, serial, not-before, not-after, protocol constraints, and revocation lookup are unambiguous
    And it contains no wallet, settlement, admin, or platform-signing authority

  Scenario Outline: Roger Core refuses a joined channel with an invalid certificate
    Given a Tower opens the outer TLS channel using "<certificate>"
    When Roger Core authenticates the peer
    Then the TLS or admission handshake fails before inventory or jobs are accepted

    Examples:
      | certificate                                  |
      | none                                         |
      | self-signed                                  |
      | issued by an unknown root                    |
      | valid for a Station rather than a Tower      |
      | valid for a different Tower ID               |
      | not yet valid                                |
      | expired                                      |
      | revoked                                      |
      | malformed                                    |
      | signed correctly but paired with wrong key   |
      | missing the required URI identity            |
      | carrying an unsupported critical constraint  |

  Scenario: A copied certificate without its private key is useless
    Given an attacker copies an active Tower certificate but not its private key
    When it opens a joined channel
    Then mutual TLS proof of possession fails

  Scenario: A copied active key remains bounded by certificate scope
    Given an attacker steals a joined Tower's active TLS key and certificate
    When it calls a wallet, settlement, admin, public-signing, or another Tower's route
    Then every out-of-scope action is rejected
    And Roger Core can revoke the serial and end new sessions

  # --- lifecycle and directory authority ----------------------------------

  Scenario: A newly enrolled Tower starts in quarantine and is not self-advertised
    Given enrollment succeeds
    When the Tower submits a valid inventory
    Then Roger Core records it as quarantine inventory
    And only Roger Core can choose whether any probe or bounded beta traffic reaches it
    And it does not appear as an active public Tower merely because it advertised itself

  Scenario Outline: Lifecycle state controls new work centrally
    Given a joined Tower is in state "<state>"
    When routing considers it for a new ordinary public job
    Then eligibility is "<eligibility>"

    Examples:
      | state      | eligibility                  |
      | pending    | ineligible                   |
      | quarantine | probes or bounded beta only  |
      | active     | eligible within its limits   |
      | draining   | ineligible                   |
      | suspended  | ineligible                   |
      | revoked    | ineligible                   |
      | expired    | ineligible                   |

  Scenario Outline: The lifecycle graph permits only explicit transitions
    Given a joined Tower is in state "<from>"
    When Roger Core commits a lifecycle event to "<to>"
    Then the transition requires "<condition>"

    Examples:
      | from | to | condition |
      | pending | quarantine | completed enrollment proof plus a deterministic sequence-1 admission-lease identity/expected later group index in the same atomic lifecycle-then-lease bundle; active-attempt action not_applicable |
      | pending | expired | enrollment timeout; active-attempt action not_applicable |
      | pending | revoked | security or abuse decision; active-attempt action cancel_at |
      | quarantine | active | completed admission probes and explicit promotion; active-attempt action not_applicable |
      | quarantine | suspended | policy, health, or security decision; cancel_at for security and otherwise the signed disposition |
      | quarantine | expired | admission-lease expiry with no active attempt, or bounded drain_until for an admitted probe |
      | quarantine | revoked | security or abuse decision; active-attempt action cancel_at |
      | active | draining | ordinary operator, upgrade, or policy drain with bounded drain_until |
      | active | suspended | non-security review with bounded drain_until, or security review with cancel_at |
      | active | expired | ordinary lease/certificate expiry with bounded drain_until for already-issued work |
      | active | revoked | security, credential compromise, or terminal policy action with cancel_at |
      | draining | active | explicit drain cancellation before cutoff while eligibility still passes; active-attempt action not_applicable |
      | draining | suspended | a cutoff no later than the prior drain cutoff; security reasons require cancel_at |
      | draining | expired | the existing cutoff or an earlier cutoff; it cannot be extended |
      | draining | revoked | cancel_at no later than the prior drain cutoff |
      | suspended | quarantine | explicit review clearance and fresh probes; active-attempt action not_applicable |
      | suspended | expired | lease/certificate expiry; active-attempt action not_applicable after all attempts are terminal |
      | suspended | revoked | terminal policy or security decision with cancel_at |
      | expired | quarantine | fresh operator authorization/key proof plus an exact next certificate/lease identity in the same atomic lifecycle-then-lease bundle and fresh probes; never direct activation |
      | expired | revoked | terminal security or policy decision; active-attempt action not_applicable when no attempt remains |

  Scenario Outline: Every unlisted lifecycle transition is rejected
    Given a joined Tower transition is "<transition>"
    When Roger Core validates it
    Then no lifecycle revision, credential scope, routing eligibility, or active-attempt disposition changes

    Examples:
      | transition |
      | pending to active |
      | pending to draining |
      | quarantine to draining |
      | suspended to active |
      | expired to active |
      | expired to draining |
      | any transition out of revoked |
      | any same-state event with different bytes |
      | any state value outside the defined enum |

  Scenario: An exact lifecycle event replay is idempotent
    Given Roger Core already committed lifecycle event E with canonical hash H and revision R
    When exact event E is replayed sequentially or concurrently
    Then it returns the existing revision and disposition without a new event, cutoff, or money action

  Scenario Outline: Active-attempt action has one canonical variant
    Given a lifecycle event uses action "<action>"
    Then "<contract>"

    Examples:
      | action | contract |
      | not_applicable | cutoff is canonically absent and the transition does not restrict a state with active attempts |
      | drain_until | one cutoff authority tuple is present, is after the effective tuple, is no later than the signed maximum ceiling, and only complete evidence strictly before it remains eligible |
      | cancel_at | one cutoff authority tuple is present at or after the effective tuple, affected attempts are swept terminal once, and security/compromise events use this variant |

  Scenario: A Tower cannot sign its own promotion
    Given a quarantined Tower sends a statement claiming state active and maximum weight
    When Roger Core processes the statement
    Then its centrally stored state and weight do not change
    And the false claim is recorded as security evidence

  Scenario: Declared location and capacity remain claims until measured
    Given a Tower declares a region, hardware shape, bandwidth, and capacity
    When Roger Core publishes or routes using its metadata
    Then declared and observed values are stored separately
    And unverified claims do not receive a verified label or override central limits

  # --- renewal, rotation, expiry, revocation -------------------------------

  Scenario: An active Tower rotates its TLS key before certificate expiry
    Given an authenticated active Tower is inside its rotation window
    When it proves its identity key and a fresh TLS CSR over the existing channel
    Then Roger Core atomically issues a new short-lived certificate for the same Tower ID and the exact next TowerAdmissionLeaseV1 head binding that serial plus the current TowerLifecycleEventV1 revision/hash
    And both serial validity intervals are retained for historical verification
    And the new serial opens no session unless that successor lease is current
    And the old serial stops opening new sessions immediately after head replacement but may finish only sessions authenticated before the signed overlap cutoff

  Scenario Outline: Certificate renewal fails safely
    Given a Tower requests renewal with "<condition>"
    When Roger Core evaluates the request
    Then no replacement certificate is issued
    And the current certificate gains no extra lifetime or scope

    Examples:
      | condition                                  |
      | no active authenticated channel            |
      | wrong identity-key proof                   |
      | replayed renewal nonce                     |
      | CSR for another Tower                      |
      | unsupported software version               |
      | suspended owner                            |
      | suspended Tower                            |
      | revoked Tower                              |
      | requested privilege expansion              |

  Scenario: Expiry stops new leases without erasing already observed valid work
    Given a Tower certificate or admission lease expires during active jobs
    When Roger Core observes expiry
    Then no new job is leased to the Tower
    And active attempts finish only within their already-signed deadlines
    And complete valid evidence observed by Roger Core before its deadline remains eligible for settlement under the grant-time snapshot
    And later offline work cannot be imported for settlement

  Scenario: Every suspension or revocation event has an authoritative cutoff disposition
    Given Roger Core changes a Tower's lifecycle state
    When it commits the signed lifecycle event
    Then the event binds Tower ID, prior/new state, lifecycle revision and prior-event hash, reason class, Core effective tuple, exactly one active-attempt action of not_applicable, drain_until(cutoff), or cancel_at(cutoff), compensation disposition, administrator/policy evidence, and signer/policy versions
    And Tower-controlled timestamps cannot move the cutoff
    And that signed TowerLifecycleEventV1 revision/complete hash, compensation disposition, and effective Core tuple are themselves the synchronous compensation fence; no second fence object or hash exists
    And payout preparation and send-fence CAS read that exact current lifecycle event directly, so asynchronous lot-hold materialization cannot leave a payout-send window

  Scenario Outline: Lifecycle compensation disposition has one closed non-money meaning
    Given TowerLifecycleEventV1 carries compensation disposition "<disposition>"
    When Roger Core applies the lifecycle event
    Then compensation handling is "<handling>"
    And the lifecycle signer cannot create an entitlement, payout, forfeiture, debt, or writeoff

    Examples:
      | disposition | handling |
      | not_applicable | no compensation-affecting transition exists and historical state is unchanged |
      | preserve_historical | timely evidence uses its immutable grant snapshot and historical untainted candidates follow normal later checks |
      | withhold_unpaid | immature and mature_payable lots enter withheld through exact withhold events; reserved_prepared lots use the atomic abort-or-void plus ordered withhold group; reserved_submitted lots remain rail-locked under an attached hold/incident and cause later payouts to be held; candidates and paid history remain immutable |
      | forfeiture_decision_required | unsubmitted unpaid lots follow the same abort-or-withhold handling, reserved_submitted lots remain rail-locked under an attached hold/incident, and a separate purpose-signed CompensationForfeitureDecisionV1 is required before any exact unpaid range becomes forfeited |

  Scenario Outline: Contradictory lifecycle actions fail closed
    Given a lifecycle event carries "<combination>"
    When Roger Core validates it
    Then no lifecycle revision is committed and no new grant uses the ambiguous state

    Examples:
      | combination |
      | both drain_until and cancel_at |
      | more than one action variant |
      | no action variant |
      | not_applicable with a cutoff |
      | not_applicable on a restrictive transition with active attempts |
      | security revocation with drain_until |
      | credential compromise with drain_until |
      | a cutoff earlier than the signed effective-time rules allow |

  Scenario: Security revocation stops new work and cancels evidence after its cutoff
    Given Roger Core revokes an active Tower at time T
    And the signed security event says cancel_at(T)
    When 60 seconds have elapsed
    Then every Roger Core router excludes it from new work
    And every new channel and certificate renewal is rejected
    And every active session is closed
    And evidence not completely observed before T cannot settle even when a prefix arrived earlier

  Scenario: Non-security suspension can drain already granted work explicitly
    Given Roger Core suspends a Tower for health or administrative review with drain_until(C)
    When an already-issued attempt is complete-observed before min(its signed deadline, C)
    Then no new grant is issued
    And complete valid evidence is eligible for settlement under its grant-time snapshot
    And compensation follows the separately signed suspension disposition

  Scenario: A stale routing snapshot cannot commit a post-transition grant
    Given a router selected a Tower or Station before a newer owner, key, lifecycle, or admission revision committed
    When it tries to commit the hold and execution grant afterward
    Then the transaction compare-and-swaps every referenced admission revision
    And stale selection fails before the hold, lease, or grant becomes authoritative

  Scenario: Historical receipts remain verifiable after key rotation or revocation
    Given complete receipt evidence was observed and ledger-anchored by Roger Core while every required key was valid
    When that key later rotates, expires, or is revoked
    Then the trust directory retains its historical key, validity interval, purpose, and status
    And verification uses Core-observed/ledger time plus any compromise-effective time
    And a signer-controlled backdated timestamp cannot authorize new evidence

  Scenario: Authority-store failure blocks admission changes
    Given the admission or revocation store is unavailable
    When Roger Core cannot prove current Tower state
    Then it does not enroll, renew, promote, or dispatch to that Tower
    And readiness for the affected control-plane function is false

  Scenario: Self-service enrollment remains quarantine-only behind a measured pilot gate
    Given a signed automation policy requires 30 consecutive external-pilot days, at least 25 independent verified operator accounts, at least 100000 completed joined attempts, every tested revocation within 60 seconds, no unresolved P0/P1 incident, no unauthorized debit/credit/payout, and passing signed overload/fraud-loss budgets
    When any threshold is missing, stale, failed, or based on unverifiable evidence
    Then self-service enrollment-token issuance remains disabled and manual admission continues

  Scenario: An eligible self-service operator receives no automatic activation
    Given every signed automation threshold passes and self-service issuance is enabled
    When an account passes current owner, key, region, version, quota, terms, payout-risk, and abuse-history checks
    Then it may receive one account/Tower/capability-bound enrollment token for quarantine only
    And ordinary public routing still requires separate probe evidence and Roger Core promotion

  Scenario: One signed rollback switch returns enrollment to manual mode
    Given self-service quarantine enrollment is enabled
    When its signed policy switch disables issuance or a safety threshold fails
    Then no new automated token is issued after the Core effective tuple
    And already issued tokens follow the explicit revoke/expire disposition without widening scope
