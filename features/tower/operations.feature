# PROPOSED SPEC — founder approval is required before step definitions or implementation.
#
# Scope: readiness, draining, crash/reconnect, bounded resources, observability, upgrades,
# backup/restore, benchmarks, and release gates for joined and standalone Towers.

Feature: A Tower remains operable and fails safely under load, upgrade, and dependency loss
  Operators and Roger Core can distinguish alive, ready, healthy, draining, and secure state,
  while every failure preserves identity, privacy, bounded resources, and money invariants.

  # --- health and readiness ------------------------------------------------

  Scenario Outline: Joined readiness requires every load-bearing dependency
    Given a joined Tower process is alive
    When "<dependency>" is not healthy
    Then process liveness remains true and joined readiness is false
    And Roger Core assigns no new job

    Examples:
      | dependency                              |
      | readable persistent identity            |
      | valid configuration                     |
      | acceptable system clock                 |
      | unexpired Tower certificate             |
      | authenticated Roger Core session        |
      | negotiated supported protocol           |
      | active or allowed quarantine lease      |
      | Station inventory within freshness      |
      | sequence state without a fork           |
      | buffers below the overload threshold    |

  Scenario Outline: Standalone durable readiness requires local authority state
    Given a standalone Tower process is alive
    When "<dependency>" is not healthy
    Then process liveness remains true and client readiness is false
    And no request is accepted into non-durable or ambiguous state

    Examples:
      | dependency                         |
      | pinned offline-root/publication history and required online purpose keys |
      | valid configuration                |
      | acceptable system clock            |
      | PostgreSQL                         |
      | applied schema migration           |
      | local registry                     |
      | current LocalPolicyV1              |
      | current client and Station authority heads |
      | local receipt-ledger signer        |

  Scenario: A required shared service cannot be reported ready while disconnected
    Given an explicitly configured HA profile requires its shared database or message bus
    When that service disconnects
    Then the affected replica leaves readiness
    And it does not serve work through stale local assumptions

  # --- drain, shutdown, crash, reconnect -----------------------------------

  Scenario: Graceful drain stops assignment before process shutdown
    Given a Tower has active bounded attempts
    When the operator or upgrade begins drain
    Then Roger Core marks the Tower draining before shutdown
    And no new grant is assigned
    And active attempts may finish only until their individual signed deadlines and the drain ceiling
    And status reports remaining attempts without content

  Scenario: Shutdown does not cut off a valid long stream at an unrelated fixed timeout
    Given a stream's signed deadline is longer than the old process shutdown timeout
    When a graceful drain starts
    Then the stream gets the documented bounded completion window
    And any forced cutoff produces one failed attempt and no unverified settlement

  Scenario: Process crash cannot orphan a public hold indefinitely
    Given a joined Tower or Roger Core process crashes after hold creation
    When durable recovery and the stale-attempt sweep run
    Then each attempt reaches exactly one settled or released terminal state
    And no Station earning exists without a matching client debit and final receipt

  Scenario: Reconnect creates a new fenced session
    Given a Tower connection is lost and its process reconnects with valid identity
    When Roger Core authenticates it
    Then a new unique session and monotonic lease context are created
    And messages from the old session cannot refresh inventory, complete work, or settle
    And no v1 attempt migrates to the new session
    And only evidence completely observed before the old session ended remains settlement-eligible

  Scenario: Concurrent channels for one Tower identity are centrally fenced
    Given two hosts possess the same still-valid Tower credential
    When both open joined channels concurrently
    Then the first transaction that compare-and-swaps the Tower's no-active-session record owns one new monotonic session epoch
    And while that winner remains Core-observed active, every later channel is rejected without preemption and records one bounded credential-collision event
    And one collision creates evidence only and does not quarantine, suspend, revoke, or change routing weight automatically
    And messages from every losing or prior epoch cannot refresh inventory, receive grants, or complete attempts
    And attempts already issued on a losing or prior epoch fail and release holds unless their complete evidence was durably Core-observed before that epoch was fenced

  Scenario: A disconnected session is fenced before one reconnect can win
    Given Roger Core durably marked Tower session epoch E disconnected and no active epoch exists
    When one or many valid reconnects race
    Then one compare-and-swap creates exactly E+1 and every loser is closed as a credential collision
    And no attempt migrates from E to E+1
    And unfinished E attempts fail and release holds while only timely complete E evidence remains settlement-eligible

  Scenario: Compensation-ledger-head signer outage has one authority effect
    Given compensated payouts are enabled and the mandatory CompensationLedgerHeadV1 signer is unavailable
    When Roger Core appends ordinary authoritative SQL-ledger entries
    Then admission, routing, holds, job settlement, and compensation reconciliation continue only while balanced journal postings and transactional per-currency control totals validate against the SQL head
    And compensation-head status is degraded and no payout instruction is created or sent
    And every instructionless reserved_prepared payout reaches its signed preparation-authorization deadline and uses the atomic abort-plus-withhold group when current eligibility/tax authority is unavailable; deadline expiry alone removes the reservation into its exact current classification without inventing a persistent hold
    When the signer recovers
    Then the next monotonic CompensationLedgerHeadV1 covers the complete intervening ledger prefix, journal-template version, and CompensationControlTotalSetV1 complete hash and links to the last accepted head before payouts resume
    And no backdated, partial-prefix, or alternate SQL head is accepted
    And every prepared instruction rechecks its bound head and current selected-lot states; an expired, inconsistent, or state-stale instruction is signed-voided rather than sent

  Scenario: Fee-finality and dust deadlines survive outage and restart
    Given fee-finality or dust-review deadlines become due while a worker, signer, or database is unavailable
    When durable recovery and the deadline sweep run
    Then each overdue item creates or returns its one stable incident/hold identity using the Core authority sequence
    And no deadline is silently reset from process restart or a provider/operator timestamp
    And no positive entitlement, dropped liability, duplicate notice, or duplicate incident results

  Scenario: Compensation control-total replay is a readiness gate
    Given periodic full replay folds the authoritative source and compensation ledgers by currency
    When any replayed posting, active-liability state, rail clearing, debt, or sorted control-total leaf differs from the transactional totals
    Then compensated reconciliation and every payout preparation/send for the affected currency stop
    And readiness is false with one durable bounded incident until an authorized reconciliation restores exact equality

  Scenario: Optional public-transparency checkpoint outage does not become payout authority
    Given the Phase-4 RogerLedgerCheckpointV1 Merkle publisher is enabled and its distinct signer is unavailable
    When ordinary service and compensated payouts use a fresh valid CompensationLedgerHeadV1
    Then admission, routing, settlement, compensation, and payout continue from their existing authorities
    And public transparency status reports stale with no claim of a fresh inclusion or consistency proof
    And no component substitutes an old or unsigned public checkpoint for the compensation head

  Scenario: Restart preserves identity and anti-replay state
    Given a Tower or Station restarts with its durable state intact
    When it resumes service
    Then its identity, epoch, next sequence, and prior hash remain consistent
    And old nonces, grants, results, and enrollment tokens remain consumed

  Scenario: Lost anti-replay state requires fencing rather than silent reset
    Given a joined Tower restarts without durable identity or sequence state
    When it attempts to resume the old Tower ID
    Then Roger Core rejects the old identity session, records the incident, and signed-lifecycle revokes/fences the old Tower ID
    And no recovery or administrator flow resets its statement sequence; new work requires fresh keys, enrollment, quarantine, and a new Tower ID
    And only restoration of the exact durable identity, statement head, and consumed-nonce state from an authenticated backup before revocation may preserve the existing ID, subject to fresh admission proof

  Scenario: Standalone HA requires database-backed leader and sequence fencing
    Given multiple trusted standalone replicas share a local network identity
    When they become ready concurrently or experience a partition
    Then one database-backed authority owns each mutable registry, sequence, and settlement transition
    And a replica unable to prove its current fence leaves readiness rather than serving split-brain state

  # --- bounded resources and overload -------------------------------------

  Scenario Outline: Each configured limit rejects excess work before unbounded allocation
    Given a Tower is at its configured "<limit>"
    When one more unit arrives
    Then the already-full bounded queue is not enlarged and the excess is rejected with an overloaded protocol error
    And existing healthy work is not corrupted

    Examples:
      | limit                          |
      | attached Stations              |
      | active text streams            |
      | active audio requests          |
      | total buffered input bytes     |
      | total buffered output bytes    |
      | per-stream bytes               |
      | inventory leaves               |
      | inventory bytes                |
      | pending control messages       |
      | local log disk budget          |
      | reconnect attempts per window  |

  Scenario: Oversized audio is rejected before full buffering
    Given an audio request declares or exceeds the supported maximum body size
    When it reaches a Tower path
    Then it is rejected before allocating the declared body in memory
    And no Station executes it and no public hold is captured

  Scenario: Control traffic cannot be starved by data streams
    Given data streams saturate their allowed bandwidth and buffers
    When certificate rotation, revocation, drain, ping, or deadline control arrives
    Then bounded control capacity remains available
    And revocation and deadline enforcement still meet their thresholds

  # --- observability and privacy ------------------------------------------

  Scenario: Local administration defaults to an owner-only Unix socket
    Given a Tower starts with default administration configuration
    When its admin surface opens
    Then it listens only on an owner-readable Unix socket and creates no TCP admin listener
    And peer ownership plus a scoped local administrator credential are required
    And joined-Tower administration exposes no Roger Core wallet, settlement, enrollment-policy, or platform authority

  Scenario Outline: Remote administration is explicit and strongly authenticated
    Given an operator explicitly enables a remote admin listener
    When an admin request has "<defect>"
    Then it is rejected before reading or changing protected state and the attempt is rate-limited and audited without secrets

    Examples:
      | defect |
      | plaintext transport |
      | no client certificate |
      | an untrusted or wrong-purpose client certificate |
      | a certificate outside its role scope |
      | an expired or revoked certificate |
      | a missing, replayed, expired, or divergent signed request nonce |
      | an Origin-bearing browser request without the exact configured origin and CSRF proof |
      | a body above the admin request limit |
      | requests above the per-identity or per-source rate limit |

  Scenario: Remote admin authorization is least privilege
    Given valid remote admin identities have status-only, operator, security, or backup roles
    When each identity calls every admin operation
    Then only operations explicitly assigned to that role succeed
    And identity, key, mode, trust-root, enrollment, revocation, backup, and restore changes require the documented strongest role and fresh authorization

  Scenario: Structured metrics expose operations without content
    Given a Tower processes successful, failed, replayed, mismatched, and timed-out attempts
    When metrics are scraped
    Then they report aggregate connection, certificate-expiry, inventory-age, Station-health, queue, stream, latency, byte, rejection, replay, mismatch, fork, reconnect, version, and drain values
    And labels exclude user IDs, prompts, completions, credentials, raw receipts, unbounded request IDs, and other high-cardinality secrets

  Scenario Outline: Logs redact sensitive material in normal and error paths
    Given a Tower observes "<material>"
    When success, validation failure, panic recovery, debug logging, and shutdown are exercised
    Then the material is absent from logs and traces

    Examples:
      | material                       |
      | prompt text                    |
      | completion text                |
      | tool arguments                 |
      | image bytes                    |
      | audio bytes                    |
      | transcript                     |
      | enrollment token               |
      | bridge credential              |
      | TLS private key                |
      | pinned offline-root or any online local issuer private key |
      | database password              |
      | full raw receipt               |
      | web session or admin secret    |

  Scenario: Diagnostic bundles are explicit and redacted
    Given an operator runs doctor or creates a support bundle
    When the bundle is generated
    Then it contains versions, effective redacted config, health, bounded recent logs, and public certificates
    And it excludes private keys, tokens, content, database rows, raw credentials, and unrelated host files
    And the operator is shown the exact output path and sensitivity warning

  # --- upgrades and compatibility -----------------------------------------

  Scenario Outline: N and N-1 rolling compatibility is explicit
    Given Roger Core currently supports versions N and N-1
    When a Tower running "<version>" connects
    Then the result is "<result>"

    Examples:
      | version | result                                      |
      | N       | highest common protocol is negotiated       |
      | N-1     | documented compatible protocol is negotiated|
      | N-2     | connection rejected below version floor     |
      | N+1     | version N is used only when it is also offered; otherwise connection is rejected |

  Scenario: A joined rolling upgrade preserves identity and active streams
    Given an eligible Tower version upgrade and active attempts
    When the operator upgrades
    Then the Tower drains, verifies the new artifact, stops, starts with the same protected identity, negotiates, and re-enters policy state
    And completed attempts settle once
    And attempts complete-observed before shutdown settle once
    And every unfinished attempt fails and is retried only as a new session-bound attempt

  Scenario Outline: Migration safety blocks an unsafe standalone upgrade
    Given a standalone durable Tower is upgrading
    When "<condition>" occurs
    Then the new process does not serve against ambiguous schema state
    And the prior data and verified backup remain recoverable

    Examples:
      | condition                                |
      | backup verification failed               |
      | migration lock is held                   |
      | database version is unsupported          |
      | expand migration failed                  |
      | data migration failed                    |
      | contract migration is premature          |
      | binary rollback cannot read the schema   |

  # --- standalone backup and restore --------------------------------------

  Scenario: A standalone backup includes all and only required authority state
    Given a quiesced or transactionally consistent standalone Tower
    When an encrypted backup is created
    Then the state archive includes the database, local network ID, pinned offline-root public key/fingerprint, complete LocalTrustPublicationV1, LocalBootstrapVerifierHeadV1/invitation-transition, singleton LocalOperatorAuthorityHeadSetV1, and key-escrow authorization/result/audit histories, public portions of every online purpose key, signer metadata, configuration, and migration version
    And it retains public credential-authority and certificate records required for verification and rotation, but excludes every private key, live bearer credential or credential secret, log, cache, undeclared secret, and public RogerAI bearer credential
    And a separately requested archive is produced only through exact one-use LocalKeyEscrowExportAuthorizationV1/LocalKeyEscrowExportResultV1 and contains only its complete current server-key manifest encrypted to the explicitly verified recovery key; the pinned offline root is excluded by default and included only by the separate true flag, offline-root approval, physical-media input, and isolated-command ceremony
    And neither archive contains plaintext secret material or any local client/local_operator private key; administrator recovery requires a replacement-key proof plus its exact signed authority rather than restored client secret material

  Scenario: A key-escrow backup requires fresh local authorization and is atomic
    Given an operator requests a standalone key-escrow archive
    When the owner-local Unix-socket/controlling-TTY OS peer, fresh singleton-operator proof and key_escrow_export administration authorization, recovery public-key bytes/fingerprint, exact current heads/server-key manifest, optional offline-root media approval, no-overwrite destination identity/permissions, or durable ciphertext write cannot be verified
    Then no reservation starts before those preconditions, or the one started reservation reaches exactly one signed completed/aborted LocalKeyEscrowExportResultV1 without plaintext or accepted partial archive
    And remote administration and a stolen remote operator certificate have no export route, all client/local_operator/Station private keys and bootstrap-verifier HMAC secrets remain excluded, and logs reveal only operation IDs, terminal state, and the public recovery-key fingerprint

  Scenario: Restore requires explicit possession of the private network secrets
    Given a valid state archive and either matching external protected keys or its matching key-escrow archive and recovery private key
    When it is restored into a fresh isolated environment
    Then prior clients, Stations, receipts, network ID, and sequence heads verify
    And public advertisement remains impossible
    And restored leaf and remote-admin credentials are rotated before readiness, no prior bootstrap HMAC secret is restored or trusted and ordinary operator-authorized verifier rotation commits a fresh generation/head before admission readiness, while the network root, singleton operator set, and historical ledger verification remain stable

  Scenario Outline: Invalid restore never creates a partially trusted service
    Given a standalone restore has "<defect>"
    When startup validates it
    Then readiness stays false and no client request is accepted

    Examples:
      | defect                          |
      | wrong decryption key            |
      | database checksum mismatch      |
      | pinned offline-root or trust-publication mismatch |
      | ledger signer mismatch          |
      | missing sequence head           |
      | unsupported migration version   |
      | data directory already in use   |

  # --- decisive benchmarks and release gates ------------------------------

  Scenario: Joined baseline resource target is measured reproducibly
    Given a release-candidate Linux amd64 and arm64 Tower on 1 vCPU and 1 GiB RAM
    When the controlled 100-concurrent-text-stream workload runs with fixed request corpus, Station behavior, network conditions, repetitions, and competing load
    Then there is no OOM, corruption, leaked goroutine, or unbounded queue
    And peak memory, CPU, throughput, p50/p95/p99 latency, errors, and raw samples are preserved

  Scenario: Joined latency and throughput gates compare against a direct control
    Given identical Core, Station, client, payload, region, and load
    When direct-Station and joined-Tower paths run as paired repeated trials
    Then joined throughput loss is below 5 percent
    And joined added p95 time-to-first-token is at most 50 milliseconds
    And the report includes dispersion and failures rather than only averages

  Scenario: Many Towers expose Roger Core saturation instead of claiming horizontal Core scale
    Given the release candidate has the same aggregate Station workload behind 1, 10, and 100 authenticated Towers
    When paired load steps increase streams, bytes, inventory churn, moderation work, recounts, and settlements to and beyond Core capacity
    Then the report preserves Tower and Core CPU, memory, network, queue, database, throughput, p50/p95/p99 latency, and error saturation curves
    And the public capacity claim is limited by the smallest measured Core, database, network, or Tower bottleneck
    And overload sheds new work without corrupting active streams, holds, receipts, or other Towers

  Scenario: Roger Core failover is part of the joined scale gate
    Given many joined Towers carry bounded active attempts across Roger Core instances
    When one Core instance, region link, private bus, or database dependency fails under load
    Then each Tower is fenced or reconnected under one authoritative session policy
    And every attempt reaches one settled or released terminal state without duplicate work or money
    And recovery time, lost capacity, and affected attempts are published with the benchmark

  Scenario: Maximum-body and mixed-modality load fits the published envelope
    Given the release candidate and its supported maximum text, image, and audio bodies
    When configured maximum concurrent workloads and slow-reader cases run
    Then measured peak RAM and disk stay within operational headroom
    And overload is rejected within bounded resources
    And the published minimum is no lower than the smallest passing envelope

  Scenario: Revocation propagation meets the public threshold under partition and reconnect
    Given an active Tower is revoked during normal, delayed, partitioned, and reconnecting control-plane conditions
    When 60 seconds have elapsed from authoritative revocation
    Then no router assigns it new work and no new channel is admitted
    And raw timing evidence is retained for every condition

  Scenario: Standalone no-egress gate uses packet-level evidence
    Given a default standalone Tower with local client, Station, database, and update metadata available
    When initialization, serving, failure, restart, doctor, backup, and 24 hours idle are exercised
    Then packet capture shows no RogerAI DNS lookup or network connection
    And any required local dependency traffic is enumerated separately

  Scenario Outline: A security adversarial gate must pass before joined public beta
    Given a release candidate and real persistent dependencies
    When the "<campaign>" campaign runs
    Then each attempt reaches the one rejection or settlement outcome required for its exact initial state and event sequence by the approved specs
    And no unauthorized debit, credit, authority, plaintext disclosure, or unbounded resource use occurs

    Examples:
      | campaign                             |
      | enrollment token replay              |
      | certificate theft and revocation     |
      | inventory mutation and rollback      |
      | execution-grant field mutation       |
      | result substitution and truncation   |
      | receipt context mismatch             |
      | duplicate and concurrent settlement  |
      | Station and Tower chain fork         |
      | database and bus failure             |
      | process kill and network partition   |
      | slow stream and frame flood          |
      | artifact tamper and rollback         |
      | standalone public-egress attempts    |

  Scenario: No public release proceeds with an unresolved correctness gate
    Given any required correctness, fit, performance, reproducibility, privacy, or release gate is unresolved or failed
    When release promotion is requested
    Then public joined-Tower admission and unqualified download claims remain disabled
    And the negative result and next decisive experiment are recorded rather than overwritten
