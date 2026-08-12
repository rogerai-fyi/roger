# APPROVED SPEC - founder approved 2026-08-03. Changes to an approved scenario need
# re-approval; they are not a diff to be reviewed.
#
# BUILD STATUS: PARTIAL. Approval is not implementation - this line says which.
# Enforced by internal/towercore/featurestatus_test.go against the "Contract:"
# references in the code. Changing the status without changing the code fails.
#
# Scope: the mutually exclusive joined and standalone Tower modes, their configuration,
# trust roots, network reachability, and local bootstrap behavior.
#
# Out of scope: public enrollment mechanics (public_enrollment.feature), job integrity and
# settlement (job_and_settlement.feature), and release packaging (packaging.feature).

Feature: A Tower is either a joined child of RogerAI or a standalone private network
  The same downloadable Tower product can extend the public RogerAI network or operate a
  private local network, but one data directory and process can never do both.

  # --- initialization and immutable mode -----------------------------------

  Scenario Outline: A fresh data directory can be initialized in either supported mode
    Given an empty Tower data directory
    When the operator initializes it with mode "<mode>"
    Then the stored network mode is "<mode>"
    And the network binding is "<network binding>"
    And the generated authority is "<generated authority>"
    And every private key is readable only by the Tower owner
    And the mode is printed plainly by status and doctor

    Examples:
      | mode       | network binding | generated authority |
      | joined     | the fixed RogerAI public network ID from the pinned bootstrap; no new network ID or trust root | a new Tower identity and local attachment authority only |
      | standalone | a new unique local network ID that cannot equal a RogerAI public ID | a pinned offline root, revision-1 local trust publication and policy, distinct online purpose keys, local receipt-ledger signer, and local service identity |

  Scenario Outline: An invalid or ambiguous mode is rejected before files are created
    Given an empty Tower data directory
    When the operator initializes it with mode "<mode>"
    Then initialization fails without creating an identity or partial configuration

    Examples:
      | mode               |
      | missing            |
      | empty              |
      | public             |
      | private            |
      | hybrid             |
      | joined,standalone  |
      | unknown            |

  Scenario Outline: A data directory cannot be changed to the other mode in place
    Given a Tower data directory initialized as "<original>"
    When configuration or a command requests mode "<requested>"
    Then startup fails before opening a listener or network connection
    And the error requires a new data directory and explicit initialization
    And no identity, trust root, or stored Station is copied automatically

    Examples:
      | original   | requested  |
      | joined     | standalone |
      | standalone | joined     |

  Scenario: Two processes cannot concurrently own one Tower identity directory
    Given one Tower process holds the identity-directory lock
    When another process starts with the same data directory
    Then the second process fails before connecting or listening
    And it does not reuse the first process's session or sequence

  # --- strict configuration ------------------------------------------------

  Scenario Outline: Unknown or cross-mode configuration fails closed
    Given a Tower configured in "<mode>" mode
    When configuration contains "<field>"
    Then config validation fails and serve does not start

    Examples:
      | mode       | field                                      |
      | joined     | an unknown field                           |
      | standalone | an unknown field                           |
      | joined     | a local settlement signer                  |
      | joined     | a Roger Core database credential           |
      | joined     | a Roger Core message-bus credential        |
      | joined     | a payment-provider credential              |
      | joined     | a public-network admin credential          |
      | joined     | standalone offline-root, trust-publication, or local-purpose-key configuration |
      | standalone | a public authority URL                     |
      | standalone | an enrollment token                        |
      | standalone | a joined-Tower certificate                 |
      | standalone | a RogerAI credit or payout setting         |
      | standalone | public advertisement enabled               |

  Scenario: Printed configuration is complete but secret-safe
    Given valid Tower configuration using secret files
    When the operator prints configuration with redaction
    Then every effective non-secret value and default is shown
    And secret paths are shown without reading their contents
    And enrollment tokens, private keys, database passwords, local API keys, and certificates' private material are absent

  Scenario Outline: A secret supplied unsafely is rejected
    Given otherwise valid Tower configuration
    When "<secret>" is supplied directly in a command argument or ordinary scalar field
    Then validation fails and the secret is not logged

    Examples:
      | secret                   |
      | enrollment token         |
      | private identity key      |
      | local administrator key   |
      | database URL with password|

  # --- standalone is structurally isolated --------------------------------

  Scenario: Standalone mode defaults to loopback and no public-network egress
    Given a fresh standalone Tower with default configuration
    When it starts and serves a local Station and client request
    Then its client, Station, admin, and metrics listeners bind only to loopback
    And it performs no RogerAI DNS lookup or network connection
    And any configured local dependency traffic stays inside the declared private network
    And it sends no telemetry or update check

  Scenario Outline: Standalone mode cannot contact or impersonate the public network
    Given a running standalone Tower
    When an operator or caller attempts "<action>"
    Then no such runtime route exists and configuration is rejected before a network call
    And no RogerAI public credential is emitted or accepted

    Examples:
      | action                                      |
      | public Tower enrollment                     |
      | public Station advertisement                |
      | RogerAI public model discovery             |
      | RogerAI credit authorization                |
      | RogerAI hold or settlement                   |
      | RogerAI payout                              |
      | RogerAI trust badge issuance                 |
      | use of a public network ID in a local grant  |
      | use of a joined Tower certificate            |

  Scenario: Standalone outbound destinations are an explicit private allowlist
    Given a standalone Tower uses a local PostgreSQL, optional Valkey, and attached private-network services
    When it resolves and connects to a configured dependency
    Then every resolved address must remain inside the operator-declared loopback, Unix-socket, cluster, or private CIDR allowlist on every connection
    And redirects, alternate addresses, and proxy variables cannot escape that allowlist

  Scenario Outline: Request-controlled URL fetching cannot turn standalone mode into an egress proxy
    Given a standalone client or Station supplies "<target>"
    When the Tower validates the request
    Then the Tower does not resolve or fetch the target in v1
    And no redirect or DNS rebinding can cause an outbound connection

    Examples:
      | target                                      |
      | a RogerAI hostname                          |
      | a public Internet URL                       |
      | a cloud instance-metadata address           |
      | a loopback admin endpoint                   |
      | a private address outside the declared CIDR |
      | a hostname that changes from allowed to forbidden IP |

  Scenario: Restored standalone configuration cannot inject joined behavior
    Given a valid standalone backup contains modified endpoints, network IDs, or mode fields
    When restore and strict config validation run
    Then any joined/public field or destination outside the private allowlist makes readiness false
    And no restored token, certificate, URL, or route causes a RogerAI connection

  Scenario: A standalone Tower is not an existing private band
    Given a standalone Tower and an existing RogerAI private-band code
    When a caller supplies the private-band code to the standalone Tower
    Then it grants no access and causes no RogerAI request
    And the local network continues to use its own network ID and trust root

  Scenario: Explicit LAN serving stays private
    Given a standalone operator explicitly configures a LAN client listener
    When the Tower starts
    Then the listener requires local TLS and authenticated clients
    And no public discovery or automatic port mapping is enabled
    And status labels the endpoint "standalone/private"

  Scenario: Explicit Kubernetes serving stays private
    Given a standalone Tower is exposed by a cluster Service
    When a client uses the Service
    Then the Tower authenticates it under the local trust root
    And public RogerAI discovery cannot resolve or advertise that Tower

  # --- standalone bootstrap and local authority ----------------------------

  Scenario: A standalone Tower creates a unique pinned offline root and purpose-separated online issuers
    Given an empty standalone data directory
    When it is initialized
    Then it creates an offline local root distinct from every public RogerAI key and commits LocalTrustDocumentV1 revision 1 through LocalTrustPublicationV1 revision 1 under that root
    And the document delegates every distinct online purpose key required by LocalTrustDocumentV1, including trust/publication, policy, client/Station admission and certificate, bootstrap-verifier-head, singleton operator-set, bridge, grant, receipt-ledger, administrator-audit, key-escrow authorization/result, and service-TLS keys
    And it commits LocalBootstrapVerifierHeadV1 revision 1/generation 1 with an empty invitation set and LocalOperatorAuthorityHeadSetV1 revision 1 with an empty member set before issuing the first bootstrap invitation
    And it prints the offline-root fingerprint and first-publication hash through a local trusted channel
    And no root or issuer private key is returned by an API

  Scenario: A local bootstrap code has cryptographic entropy and secret-safe storage
    Given a standalone Tower creates a bootstrap invitation
    When the code is generated and persisted
    Then it contains at least 128 bits from the operating-system cryptographic random source
    And plaintext code bytes are never stored; the durable state stores only their HMAC-SHA-256 verifier record under the invitation's exact current LocalBootstrapVerifierHeadV1 HMAC-key generation plus the signed invitation/head/set-transition records binding LocalClientInvitationV1 ID, expiry, attempt budget, exact source kind and role, local network ID, pinned offline-root fingerprint, bound current trust-publication hash, and requesting client public-key hash
    And first_bootstrap stores bootstrap generation 1 and local_operator role, root_bootstrap_reissue stores its exact next bootstrap generation/prior invitation and local_operator role, and administrator_created stores canonical bootstrap-generation/prior absence plus its consumed client_invite authorization
    And verifier comparison is constant-time after the durable attempt budget is claimed
    And plaintext code is displayed once through the local trusted channel and never logged, traced, metered, or returned again

  Scenario Outline: A local bootstrap code is one-time and bounded
    Given a fresh local bootstrap code
    When it is used in the "<condition>" condition
    Then the result is "<result>"

    Examples:
      | condition                         | result   |
      | first valid use                    | accepted |
      | exact replay after success         | rejected |
      | use after expiry                   | rejected |
      | use by a second concurrent client  | rejected |
      | use with the wrong offline-root fingerprint or trust-publication hash | rejected |
      | use with a modified request        | rejected |

  Scenario: Wrong bootstrap guesses are durably rate-limited across processes
    Given a valid or unknown bootstrap invitation ID
    When wrong, malformed, empty, oversized, or random codes are attempted sequentially, concurrently, across source addresses, or after restart
    Then the per-invitation and global attempt counters advance atomically in shared durable state
    And bounded exponential delay and the signed maximum-attempt lockout apply before expensive verification can exhaust resources
    And responses do not reveal whether the invitation ID, code prefix, client key, or target role was correct
    And every failed attempt for a known current invitation advances its signed verifier head revision and complete attempt-count member through one exact invitation_failed_attempt or invitation_attempt_lock transition without changing the HMAC-key generation, secret commitment, or generation not-after; an unknown ID advances only the separate durable global anonymous-attempt limiter and cannot invent a set member

  Scenario: Bootstrap-verifier rotation is ordinary administration after bootstrap
    Given a standalone network has its exact current singleton local_operator and verifier head V, whether or not the old HMAC secret is usable
    When the operator consumes one bootstrap_verifier_rotate LocalAdminAuthorizationV1 through the owner-scoped administration path
    Then one transaction installs a fresh operating-system-random HMAC secret, advances verifier generation and head revision, terminalizes every outstanding invitation, and publishes an empty resulting invitation set while preserving all client, Station, request, grant, and receipt history
    And loss or compromise of only the verifier secret does not require the offline root after bootstrap; if the operator is also unusable, the pinned root first performs exact singleton sole-admin credential recovery and that recovered operator then performs ordinary verifier rotation
    And bootstrap_reissue_recovery remains available only for the pristine never-consumed bootstrap with no client or job history and cannot reset an active network

  Scenario: Bootstrap consumption and scoped credential issuance are atomic
    Given a correct unexpired current first_bootstrap or root_bootstrap_reissue LocalClientInvitationV1 code bound to its generation/prior head, client public key K, local network N, pinned offline-root fingerprint F, current trust-publication hash P, and local_operator role R
    When the client proves possession of K and consumes the invitation
    Then one transaction advances the verifier head/set through invitation_consume, marks it consumed, commits exactly one revision-1 LocalClientCredentialAuthorityV1 plus its distinct-online-issuer certificate restricted to K, N, F, P, R, and a bounded validity interval, and advances the empty operator-set head to its one singleton member
    And response loss returns that same credential outcome only after fresh proof of K
    And a crash cannot leave both a reusable code and an issued credential

  Scenario: Standalone v1 has one and only one current local operator
    Given first bootstrap installed the singleton LocalOperatorAuthorityHeadSetV1 member
    When later invitation, role-change, revocation, renewal, key-rotation, or recovery operations run
    Then later invitations admit only inference or status, ordinary transitions cannot add a second operator or remove/demote/revoke the singleton, and only renewal/key rotation of that stable operator may advance the singleton normally
    And offline-root sole-admin recovery compare-and-swaps the complete singleton set and exact old credential, fences it at the recovery tuple, proves the replacement key, and atomically installs exactly one replacement member

  Scenario Outline: Bootstrap binding mismatch never consumes the legitimate invitation
    Given a correct bootstrap secret is presented with "<mismatch>"
    When standalone admission validates it
    Then no credential is issued and the invitation is not marked successfully consumed
    And the failed attempt follows the durable attempt budget without revealing the expected value

    Examples:
      | mismatch |
      | another client public key |
      | another local network ID |
      | another offline-root fingerprint or trust-publication hash |
      | another role, bootstrap generation, source kind, or invitation predecessor |
      | another invitation ID |
      | an expired invitation policy version |

  Scenario: A locally admitted client pins the offline root and monotonic publication history
    Given a client consumes a valid one-time bootstrap code and expected offline-root fingerprint and first-publication hash
    When it reconnects later
    Then it verifies the pinned root, contiguous greatest LocalTrustPublicationV1 history, online local-client-certificate issuer purpose/state, and scoped current LocalClientCredentialAuthorityV1
    And a rollback, fork, different root, wrong-purpose certificate, stale credential, or public RogerAI certificate is rejected

  Scenario: Standalone mode routes only attached local Stations in v1
    Given a standalone Tower with an authenticated local Station
    When a local client requests an offered model
    Then the Tower verifies LocalRequestAuthorizationV1 and current trust/policy/client/Station heads before it routes the request without RogerAI credits or payout
    And LocalSettlementReceiptV1 is visibly identified by the standalone network ID and pinned local trust history
    And it makes no claim of RogerAI public verification

  Scenario Outline: Standalone durable startup fails instead of silently losing state
    Given standalone mode is configured as durable
    When "<dependency>" is unavailable or invalid
    Then readiness remains false and no request is accepted
    And the operator receives a specific repair instruction

    Examples:
      | dependency                         |
      | PostgreSQL connection              |
      | database schema migration          |
      | identity volume                    |
      | pinned offline-root/trust-publication history or required online purpose key |
      | bootstrap-verifier head or singleton operator-set history |
      | local receipt-ledger signing key   |

  Scenario: Development in-memory mode is unmistakably non-durable
    Given an operator explicitly selects the standalone development profile
    When the Tower starts
    Then startup and status warn that all identity and state may be lost
    And it binds only to loopback by default
    And joined mode and public advertisement remain impossible

  # --- joined-mode negative space -----------------------------------------

  Scenario: Joined mode has no local public-network authority
    Given a joined Tower has an active parent connection
    When its operator inspects its available routes and credentials
    Then no wallet, hold, settlement, payout, public-user auth, moderation-decision, or Roger Core admin route exists
    And no Roger Core database, message-bus, payment, session, evidence, or signing secret exists

  Scenario: Joined mode cannot serve public work while disconnected
    Given a joined Tower loses its Roger Core session
    When a new public job is offered locally or restored from disk
    Then the Tower rejects it as not leased
    And it cannot queue offline work for later public settlement
