# APPROVED SPEC - founder approved 2026-08-03. Changes to an approved scenario need
# re-approval; they are not a diff to be reviewed.
#
# Scope: purpose-separated Roger Core, Tower, Station, session, and local-network keys;
# cross-purpose rejection, rotation, failure behavior, and compromise blast radius.

Feature: No Tower credential or single Roger Core secret grants unrelated authority
  Every signature and secret has one named purpose so compromising a relay, cookie,
  pseudonym, admin channel, or signer cannot silently become settlement authority.

  # --- Roger Core purpose separation --------------------------------------

  Scenario: Roger Core configures distinct keys for every authority role
    Given a production public network starts
    When its cryptographic key configuration is validated
    Then every role below resolves to a distinct key identity:
      | role                                         |
      | offline root                                 |
      | Roger Core TLS service identity              |
      | Tower-certificate issuer                     |
      | Station secure-session certificate issuer    |
      | admission-lease signer                       |
      | Tower lifecycle signer                       |
      | Station lifecycle signer                     |
      | Station-admission/origin signer              |
      | Station-epoch signer                         |
      | public-directory signer                      |
      | trust-document signer                        |
      | trust-document publication signer            |
      | tower-compensation-policy signer             |
      | funding-allocation-policy signer             |
      | payout-policy signer                         |
      | fee-finality-policy signer                   |
      | maturity-policy signer                       |
      | payout-eligibility-policy signer             |
      | compensation-enforcement-policy signer       |
      | debt-writeoff-policy signer                  |
      | compensation-enforcement-finding signer      |
      | debt-writeoff-approval signer                |
      | compensated-capability signer                |
      | consumer-cash-credit signer                  |
      | platform-grant-credit signer                 |
      | funding-source-ledger signer                 |
      | payout-identity-verification signer          |
      | operator-account-status signer               |
      | payout-terms-acceptance signer               |
      | sanctions-screening signer                   |
      | payout-jurisdiction signer                   |
      | payout-destination-verification signer       |
      | tax-profile-fact signer                      |
      | attempt-state signer                         |
      | dispatch-lease signer                        |
      | execution-grant signer                       |
      | Core-transit-observation signer              |
      | settlement signer                            |
      | compensation-ledger signer                   |
      | compensation-ledger-head signer              |
      | maturity-authority signer                    |
      | public-transparency checkpoint signer        |
      | compensation-forfeiture decision signer      |
      | debt-writeoff decision signer                |
      | payout authorization                         |
      | payout-eligibility decision signer           |
      | payout-eligibility incident signer           |
      | tax-withholding decision signer              |
      | tax-correction incident signer               |
      | fee-finality incident signer                 |
      | payment-webhook authentication               |
      | payment-reconciliation API                   |
      | payout-rail API                              |
      | session HMAC                                 |
      | pseudonym HMAC                               |
      | admin authentication                         |
      | evidence-encryption                          |
    And no two roles use the same raw key, KMS/HSM key identifier or alias, derived-key root, or fallback key

  Scenario Outline: A valid signature from the wrong Roger Core role is rejected
    Given an object requires the "<required>" key purpose
    And it is cryptographically signed by a valid "<presented>" key
    When verification runs
    Then verification fails for key-purpose mismatch

    Examples:
      | required           | presented          |
      | Tower certificate  | settlement         |
      | Tower certificate  | execution grant    |
      | execution grant    | Roger Core TLS     |
      | settlement         | Roger Core TLS     |
      | Station session certificate | Tower certificate |
      | admission lease    | lifecycle          |
      | lifecycle          | admission lease    |
      | Station epoch reset| Tower lifecycle    |
      | Station origin lease | Station epoch reset |
      | public directory   | trust document     |
      | trust document     | checkpoint         |
      | dispatch lease     | execution grant    |
      | dispatch lease     | settlement         |
      | execution grant    | dispatch lease     |
      | execution grant    | settlement         |
      | execution grant    | checkpoint         |
      | compensated capability | execution grant |
      | compensated capability | payout eligibility decision |
      | funding source reservation | execution grant |
      | funding source reservation | settlement |
      | consumer cash credit | funding source reservation |
      | platform grant credit | consumer cash credit |
      | Core transit observation | execution grant |
      | Core transit observation | Tower transit    |
      | settlement         | execution grant    |
      | settlement         | compensation ledger|
      | compensation ledger| settlement          |
      | payout instruction | compensation ledger|
      | payout-eligibility decision | payout authorization |
      | payout-eligibility incident | payout-eligibility decision |
      | compensation enforcement policy | compensation-forfeiture decision |
      | compensation enforcement finding | compensation enforcement policy |
      | debt writeoff policy | debt-writeoff decision |
      | debt writeoff approval | debt writeoff policy |
      | fee-finality incident | compensation ledger |
      | settlement         | Tower identity     |
      | checkpoint         | settlement         |
      | checkpoint         | Station identity   |

  Scenario Outline: Symmetric secrets cannot authenticate another role
    Given an attacker possesses "<secret>"
    When it attempts "<authority>"
    Then authentication fails and no cross-role key is derived from the possessed bytes

    Examples:
      | secret                 | authority                         |
      | web-session HMAC       | administrator                     |
      | web-session HMAC       | pseudonym generation              |
      | pseudonym HMAC         | web session                       |
      | pseudonym HMAC         | receipt signing                   |
      | administrator token    | settlement signing                |
      | evidence-encryption key| administrator                     |
      | evidence-encryption key| execution-grant signing           |
      | payment-webhook secret | payment-reconciliation API        |
      | payment-reconciliation credential | payout-rail API         |
      | payout-rail credential | payout-instruction signing         |

  Scenario: Raw signing-key material is never an administrator bearer token
    Given an attacker submits any public or private signing-key encoding as an admin header
    When Roger Core authenticates the request
    Then admin authentication fails unless a separately issued admin credential independently authorizes it
    And the attempt does not reveal whether a guessed signing key was correct

  Scenario: Derived separation cannot disguise one root compromise as many roles
    Given configuration derives several authority keys from one root secret or aliases several roles to one managed key
    When production key validation runs
    Then startup fails before signing, enrollment, settlement, compensation, or payout
    And the error names conflicting purposes and public key IDs without exposing secret material

  Scenario: The offline root is absent from ordinary runtime
    Given Roger Core and joined Towers are serving traffic
    When their process memory, files, environment, secret mounts, and API capabilities are inventoried
    Then the offline root private key is absent
    And routine Tower certificates and trust documents are issued through a bounded replaceable intermediate

  # --- Tower and Station separation ---------------------------------------

  Scenario: A Tower has separate identity-statement and TLS keys
    Given a joined Tower is initialized and enrolled
    When its protected key inventory is inspected
    Then its persistent Tower statement key and rotating TLS private key are distinct
    And its identity proof binds each TLS CSR explicitly

  Scenario: Tower-local bridge authorities have no public-network authority
    Given a Tower accepts local Station bridge connections
    When its local bridge-authority and bridge-certificate key inventory is inspected or a locally issued object is presented to Roger Core
    Then those two keys are distinct from each other, Tower identity, Tower TLS, Station, standalone-root, and every Roger Core key
    And Roger Core rejects their certificates and signatures for Station identity, inventory, grant, receipt, settlement, or enrollment authority

  Scenario Outline: A joined Tower key cannot exercise central or leaf authority
    Given an attacker possesses a valid Tower identity or TLS private key
    When it attempts to "<action>"
    Then the attempt is rejected for identity or purpose mismatch

    Examples:
      | action                                      |
      | sign an execution grant                     |
      | sign a dispatch lease                       |
      | sign a Roger Core settlement receipt        |
      | sign a Tower compensation ledger event      |
      | authorize a payout                          |
      | sign a Station provider assertion           |
      | issue another Tower certificate             |
      | authorize a client hold                      |
      | mint an earning                              |
      | administer Roger Core                        |
      | join under another Tower ID                  |

  Scenario Outline: A Station key cannot exercise Tower or central authority
    Given an attacker possesses a valid Station private key
    When it attempts to "<action>"
    Then the attempt is rejected for identity or purpose mismatch

    Examples:
      | action                                  |
      | authenticate a joined Tower channel     |
      | sign Tower inventory                    |
      | promote a Tower                         |
      | sign an execution grant                 |
      | sign a dispatch lease                   |
      | sign a final settlement receipt         |
      | issue a public certificate              |

  Scenario: A Station separates assertion signing from its inner TLS identity
    Given a Station is admitted for direct or joined service
    When its protected key inventory and active offer are inspected
    Then its provider-assertion signing key and secure-session TLS private key are distinct
    And the offer binds both public key identities while the execution grant binds the selected TLS certificate and inner channel
    And possession of either private key cannot exercise the other purpose

  Scenario: Local bridge credentials are distinct per Station and Tower
    Given two Stations attach to one Tower and one Station also registers elsewhere
    When their local control credentials are issued
    Then each TowerLocalStationBridgeCredentialV1 is scoped to one namespace/network, Station, Tower, distinct Station-owned bridge TLS key, distinct local_station_bridge_authority and local_station_bridge_certificate purposes, current origin authority/epoch, serial, and rotation state
    And compromise of one does not authenticate another Station or public Tower channel

  # --- standalone root separation ----------------------------------------

  Scenario: A standalone trust root has no public-network validity
    Given a standalone Tower creates a pinned offline root, purpose-separated online local trust/issuer/service keys, and receipt-ledger signer
    When their certificates or signatures are presented to Roger Core
    Then they are rejected as belonging to another network and trust root

  Scenario: Standalone local authority roles are purpose-separated
    Given a durable standalone Tower initializes
    When its local key configuration is validated
    Then pinned offline root, local trust-document, local trust-publication, local policy, local client-admission, local client-certificate, local_bootstrap_verifier_authority signer, bootstrap-verifier HMAC, local_operator_set signer, local Station-admission, local Station-certificate, local_station_bridge_authority, local_station_bridge_certificate, local grant, local receipt-ledger, local administrator-audit, local_key_escrow_authorization signer, local_key_escrow_result signer, backup encryption, and local TLS service roles resolve to distinct key identities
    And no two local roles use the same raw key, alias, derived-key root, or fallback key

  Scenario: A public RogerAI key has no implicit local admin power
    Given a standalone Tower receives a certificate or receipt valid on the public network
    When it is presented as pinned offline root, local trust/policy, local administrator, Station, client, grant, or receipt-ledger authority
    Then it is rejected unless the standalone administrator separately and explicitly enrolled that key for a local role

  # --- rotation and signer failure ----------------------------------------

  Scenario: Key rotation preserves purpose and historical verification
    Given a purpose-bound key is inside its rotation window
    When its authorized rotation completes
    Then the replacement has a new key ID and bounded validity interval for the same purpose
    And the old private key stops signing after overlap
    And historical public verification metadata remains available

  Scenario Outline: Key loading failure blocks only safe behavior
    Given "<key>" is missing, malformed, unreadable, duplicated across roles, or unavailable
    When the relevant service starts or signs
    Then "<result>"
    And it never silently generates a new production authority or falls back to another role's key

    Examples:
      | key                       | result                                                |
      | execution-grant signer    | no new joined job can be issued                       |
      | attempt-state signer      | no attempt can issue or transition and no funding reservation can release without an already committed exact terminal authority |
      | Roger Core TLS identity   | no new Tower outer or Station inner TLS session can authenticate Roger Core |
      | dispatch-lease signer     | no new joined dispatch can be issued                  |
      | Core-transit-observation signer | no joined transit observation can finalize and affected joined settlement waits or fails by its signed deadline |
      | admission-lease signer    | no new or renewed Tower admission lease can be issued |
      | Tower lifecycle signer    | no Tower lifecycle transition can commit and routing fails closed for state whose safe current authority cannot be established |
      | Station lifecycle signer  | no Station lifecycle restriction, clearance, or epoch closure can commit and affected serving fails closed at its existing cutoff/deadline |
      | Station-epoch signer      | no Station epoch reset can commit                         |
      | public-directory signer   | no new public directory snapshot can be published     |
      | trust-document signer     | no new key or policy trust document can be signed     |
      | trust-document publication signer | no signed trust document can become accepted or activate new key authority |
      | tower-compensation-policy signer | no new revenue-share rate can become applicable; existing grant-bound historical policy remains verifiable |
      | funding-allocation-policy signer | no new source-allocation rule can become applicable; no public grant reserves funds without an already published unexpired rule |
      | payout-policy signer      | no new payout conversion, threshold, preparation-deadline, or dust policy can become applicable |
      | fee-finality-policy signer | no new provider fee-finality rule can become applicable; unsupported new allocation stays pending |
      | settlement signer         | no settlement can commit as successful                |
      | compensation-ledger signer| no compensation state transition can commit           |
      | maturity-policy signer    | no new reversal-window policy can become applicable   |
      | payout-eligibility-policy signer | no new eligibility rule can become applicable; decisions require an already published unexpired rule |
      | compensation-enforcement-policy signer | no new forfeiture rule can become applicable; existing held compensation remains held |
      | debt-writeoff-policy signer | no new debt-writeoff rule can become applicable; existing debt remains outstanding |
      | compensation-enforcement-finding signer | no new substantiated final finding can authorize a forfeiture decision; affected lots remain held |
      | debt-writeoff-approval signer | no new approved writeoff authority can commit; affected debt remains outstanding |
      | compensated-capability signer | no new or renewed enabled capability can commit; grants require an existing current unexpired capability and every current prerequisite |
      | funding-source-ledger signer | no funding lot, reservation, release, or consumption transition can commit and no new job grant can reserve consumer funds |
      | consumer-cash-credit signer | no authenticated captured-cash interval can be assigned to a consumer or materialized as a funding lot |
      | platform-grant-credit signer | no new platform grant credit or grant-funded source lot can be issued |
      | payout-identity-verification signer | no decision can establish current verified payout identity; affected eligibility is held |
      | operator-account-status signer | no decision can establish current active operator status; affected eligibility is held |
      | payout-terms-acceptance signer | no decision can establish current accepted payout terms; affected eligibility is held |
      | sanctions-screening signer | no decision can establish a current clear sanctions result; affected eligibility is held |
      | payout-jurisdiction signer | no decision can establish current supported jurisdiction; affected eligibility is held |
      | payout-destination-verification signer | no decision can establish a current verified payout destination; affected eligibility is held |
      | tax-profile-fact signer | no capability or tax decision can establish a current verified tax profile; compensated grants and payout sends fail closed |
      | maturity-authority signer | no immature compensation lot can become matured       |
      | compensation-forfeiture decision signer | held lots cannot become forfeited and remain withheld pending decision authority |
      | debt-writeoff decision signer | operator debt cannot become written_off and remains outstanding |
      | payout authorization      | no new external payout instruction can be created; instructionless preparations signed-abort at their bounded authorization deadline and existing unsent instructions signed-void at that deadline |
      | payout-eligibility decision signer | no payout preparation or send fence can establish current operator eligibility; affected lots remain withheld |
      | payout-eligibility incident signer | an affected submitted payout remains rail-locked and every later operator payout remains withheld until incident authority recovers |
      | tax-withholding decision signer | no post-preparation tax decision or payout instruction can establish zero withholding; an existing preparation reaches its bounded authorization deadline and uses the atomic abort-or-void plus tax-authority-unavailable withhold group |
      | tax-correction incident signer | any affected in-flight payout remains rail-locked and all later payouts for that operator remain withheld until incident authority recovers |
      | fee-finality incident signer | timed-out fee sources remain pending with readiness false and no positive compensation delta until the incident is durably recorded |
      | payment-webhook authentication | webhook ingress rejects push hints; no hint becomes money authority |
      | payment-reconciliation API | no positive entitlement transition can establish external cash facts |
      | payout-rail API           | no reserved payout instruction is sent or retried until authenticated reconciliation is available |
      | Tower certificate issuer  | no enrollment or renewal can succeed                  |
      | Station secure-session certificate issuer | no new or renewed Station inner-session certificate can be issued |
      | Station admission/origin signer | no Station attachment or origin lease can commit          |
      | compensation-ledger-head signer | ordinary SQL-ledger serving continues with degraded head status; no new instruction is created, and instructionless preparations signed-abort at their bounded authorization deadline |
      | public-transparency checkpoint signer | ordinary serving and head-authorized payouts continue, while transparency status is stale and no fresh Merkle proof is claimed |
      | Tower identity key        | that Tower cannot authenticate or resume               |
      | Station identity key      | that Station cannot advertise or produce assertions    |

      | standalone pinned offline root | ordinary serving may continue under current unexpired history, but no root genesis/recovery, LocalBreakGlassRecoveryAuthorizationV1, or LocalOfflineRootEscrowApprovalV1 can commit |
      | standalone trust-document or trust-publication signer | no ordinary trust revision becomes current; serving stops when current trust expires unless the pinned offline root first commits the exact monotonic lost/compromised/expired/missed-renewal key-recovery publication |
      | standalone policy signer | no policy revision commits under the failed key; serving stops at current policy expiry unless the pinned root first replaces that purpose key and the replacement signer then consumes a separate exact offline break-glass policy-head recovery |
      | standalone client-admission signer | no client invitation, credential issuance, renewal, rotation, role change, revocation, or recovery target commits |
      | standalone client-certificate issuer | no new or renewed local client credential becomes usable |
      | standalone local_bootstrap_verifier_authority signer | no invitation creation, failed-attempt count, consume, expiry, lock, or verifier-head rotation can commit; ordinary non-admission serving is unchanged and pinned-root trust recovery may replace that purpose key |
      | standalone local_operator_set signer | no operator renewal/key rotation or sole-admin recovery target can advance the singleton set; ordinary serving is unchanged and pinned-root trust recovery must replace that purpose key first |
      | standalone Station-admission signer | no Station attachment, origin renewal, rotation, capability replacement, or revocation commits |
      | standalone Station-certificate issuer | no new or renewed local Station secure-session credential becomes usable |
      | standalone local_station_bridge_authority signer | no bridge authority revision, including revocation, commits; current credential, origin, compromise, and cutoff gates still apply |
      | standalone local_station_bridge_certificate issuer | no certificate-producing bridge revision becomes usable; bridge-authority-signed restriction-only revocation remains available |
      | standalone grant signer | no new local execution attempt is dispatched |
      | standalone administrator-audit signer | no ordinary local administration authorization or valid late/cancelled assertion observation-plus-rejection-audit group commits; the unchanged assertion retries without observed-head advance, while other existing serving is unchanged until another bound authority expires |
      | standalone receipt-ledger signer | standalone receipt finalization is unavailable |
      | standalone local_key_escrow_authorization signer | no new typed key-escrow authorization can commit; serving and existing terminal export audit remain unchanged |
      | standalone local_key_escrow_result signer | no reserved key export may be accepted as completed/aborted until bounded exact-attempt recovery can sign its terminal result; no second export starts |
      | standalone bootstrap-verifier HMAC | bound invitations fail closed; after any operator exists the exact singleton operator uses ordinary bootstrap_verifier_rotate even if the old secret is lost/compromised, while only a pristine no-client/no-job network uses offline-root bootstrap reissue and loss of both authorities first recovers the operator then rotates |
      | standalone backup-encryption key | ordinary serving and typed recovery-key escrow are unchanged, but no ordinary state backup is created or restored through that key until explicit recovery/rotation succeeds |
      | standalone local-service-TLS key | readiness for new local TLS connections is false; no new session authenticates, and already authenticated sessions obey their bounded credential, expiry, and cutoff state |

  Scenario: Every purpose role rejects every other configured role by Cartesian invariant
    Given the configured role registry contains every distinct role named by this feature
    When an object, decision, incident, head, instruction, certificate, observation, receipt, or callback requires role R
    Then verification accepts only a key whose immutable trust-document purpose is exactly R at the authoritative verification tuple
    And every ordered pair where presented role S is not R is rejected before state, money, network, or rail authority
    And this includes compensation-ledger-head versus compensation-ledger, payout authorization versus compensation-ledger-head, every decision signer versus every incident signer, and both directions of each substitution

  Scenario: Secret values are absent from ordinary observability
    Given every key role is configured and exercised through success and failure
    When logs, metrics, traces, status, doctor, panic reports, and config printing are inspected
    Then no private key, symmetric secret, raw token, or recoverable derived value appears
    And public key IDs and certificate expiry may appear without secret material
