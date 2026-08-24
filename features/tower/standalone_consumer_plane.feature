# APPROVED SPEC - founder-dictated 2026-08-23, then rewritten after a pre-implementation
# security review found the first draft's "structural impossibility of payment bypass"
# claim was FALSE: the gate that proves no-egress scans one package only, and the binary
# that would host the plane links the Core-dialing code. This version makes the claim
# TRUE by construction. Changes need re-approval.
# BUILD STATUS: PARTIAL. Built and tested so far:
#  - multi-client admission + revocation (internal/tower).
#  - the Core-free consumer binary (cmd/roger-tower-local) and handler package
#    (internal/localplane), with the STRUCTURAL GUARANTEE enforced: a dependency-graph test
#    proves the binary links none of towerjoin/towercore/towerhub, and a source-scan gate
#    forbids any outbound call in the handler package.
#  - signature auth mapping to an admitted client by the one canonical rule
#    (protocol.UserIDFromPubkey), with all failures a byte-identical 401.
#  - /discover over the Tower's own stations, admitted clients only.
#  - the bind posture: loopback default, public/all-interfaces refused without an override.
# NOT yet built: the completion loop (/v1/chat/completions + the local work queue a station
# polls + persisted receipts), replay defense, the airgap clock seam, and resource limits.

Feature: A standalone Tower serves its own network and no one else's
  The airgap plant: model boxes `roger share` to a local Tower, operators point roger at
  it, and nothing - traffic, names, receipts, or money - ever touches the public network.
  Sealed by CONSTRUCTION, not by a runtime flag: the process that serves consumers does
  not link a single line that can reach Roger Core.

  Background:
    Given a standalone Tower serving its consumer endpoint on the local network

  # === THE STRUCTURAL GUARANTEE (this is what makes the rest safe) ===========

  Scenario: The consumer process cannot reach Roger Core, by linkage not by policy
    Given the binary that serves the standalone consumer plane
    When its full dependency graph is inspected
    Then it imports none of towerjoin, towercore, or towerhub
    And no reachable code can dial Roger Core or any public address
    # The first draft trusted a runtime `if Mode != joined`. A policy check in a binary
    # that LINKS the forwarding code is one bug or one patch from a payment bridge. This
    # binary does not contain the forwarding code at all.

  Scenario: The no-egress gate covers every file that serves a consumer
    Given the consumer handler lives in the egress-gated package
    When the package's source-scan gate runs
    Then it forbids net.Dial, net.Listen, and every http client in the handler's files
    And the handler makes no outbound call - it only reads a request and writes a reply
    # net.Listen is inbound but still forbidden here: a listener is created outside this
    # package, in the Core-free binary, and the handler is handed the connection.

  # === THE LOOP THE FIRST SLICE PROVES =======================================

  Scenario: An admitted local client gets a completion through a local station
    Given a roger share node registered its model to this Tower
    And a client admitted by this Tower's own bootstrap invitation
    When the client runs roger use pointed at the Tower and sends a prompt
    Then the answer comes from the local station
    And the station was reached without the Tower dialing out: the station polls the Tower
    And a local receipt records the route and is persisted
    And no connection of any kind was made outside the declared private network

  Scenario: The consumer surface speaks the contract roger already knows
    Then the Tower answers /discover with its OWN attached stations and nothing else
    And it answers /v1/chat/completions in the shape the public broker does
    And it emits a free cost header and a local receipt, never a billing shape
    And roger config set broker to the Tower's address is the only client change needed

  # === AUTHENTICATION: THE REVIEW'S FINDING 2 ================================

  Scenario: A verified signature maps to an admitted client by one pinned derivation
    Given roger signs every request with its Ed25519 key (X-Roger-Pubkey/TS/Sig)
    When the Tower verifies a request
    Then the client key hash is derived from the pubkey by exactly one canonical rule
    And the same rule is what admission recorded, so a signature can actually be checked
    And a validly-signed request from a non-admitted key is refused

  Scenario: All authentication failures are one uniform refusal
    Given three callers: a bad signature, a valid signature from an unadmitted key, and a
      request for a client that was revoked
    Then all three receive a byte-identical 401
    And none reveals whether a key verified, whether a client exists, or any model name
    # The first draft leaked: Route() said "only the admitted client may route" for a
    # valid-but-unadmitted key, distinct from a generic bad-signature 401 - an oracle.

  Scenario: A captured request cannot be replayed
    Given an admitted client's signed request is captured on the wire
    When it is replayed within the signature's freshness window
    Then the replay is refused by a per-client nonce or monotonic-timestamp gate
    # protocol.VerifyRequest only checks a 5-minute skew; on a free local plane that is a
    # 5-minute forgery window. The plane adds its own replay defense.

  Scenario: An airgapped clock does not lock out or widen the window
    Given a plant host with no NTP and a drifted clock
    Then the freshness check uses the Tower's own clock seam, consistently
    And the window is stated for airgap operation, not assumed to be internet-tight

  # === MULTIPLE CLIENTS AND REVOCATION: THE REVIEW'S FINDING 2 ===============

  Scenario: More than one client can be admitted
    Given the network needs several operators and agents
    When each consumes its own one-time invitation
    Then each is an independent admitted client with its own key
    # ConsumeInvitation admits into a client set (the first admitted client is the operator);
    # each subsequent invitation admits an additional independent, revocable client.

  Scenario: A client can be cut off locally, and only that client
    Given an admitted client the operator no longer trusts
    When the operator revokes it
    Then that client's next request is refused with the uniform 401
    And every other client and station is unaffected
    And a consumed invitation stays dead across the revoke and across restarts

  # === NEVER A BRIDGE TO THE OPEN MARKET =====================================

  Scenario: An Open Market model is refused after auth, and nothing is dialed
    Given the public network sells a model this Tower does not host
    When an admitted client requests that model
    Then it is refused as not offered by any local station
    And the refusal names the model only to the ALREADY-authenticated client
    And no outbound connection is attempted - there is no code linked that could

  Scenario: Open Market identity is ignored and never reflected
    When a request carries a RogerAI account, wallet, X-Roger-Freq band, or grant key
    Then none of it authenticates or routes anything
    And none of it is logged, echoed, or reflected in any response
    # Reflecting X-Roger-Freq would confirm the private-band namespace to a prober.

  Scenario: The Tower's own advertisements never leave the machine
    When the Tower has stations attached and clients admitted
    Then nothing it knows appears on the public /discover
    And no heartbeat, inventory, telemetry, or DNS lookup leaves the private network

  # === EXPOSURE POSTURE: THE REVIEW'S FINDING 1b =============================

  Scenario: The consumer plane refuses to masquerade on a public address
    Given serve is asked to bind the consumer plane to a public or unspecified address
    Then it refuses, requiring an explicit acknowledged override
    And the default bind is loopback, widened to a private-LAN address only on request
    # An unbound standalone Tower on a public IP is a broker lookalike for phishing
    # ROGER_BROKER; admission does not stop that, a bind refusal does.

  # === HONEST ACCOUNTING, NO MONEY ===========================================

  Scenario: Local receipts are persisted bookkeeping, never billing
    Given requests have flowed through the Tower
    Then each produced a persisted local receipt naming client, station, and model
    And receipts state plainly they are free and locally accounted
    And nothing accrues, settles, or converts to RogerAI credit - there is no rail here

  # === RESOURCE SAFETY ========================================================

  Scenario: The consumer plane bounds request size, concurrency, and per-client rate
    Given a client floods the Tower with large or rapid requests
    Then requests over a body cap are refused, in-flight work is bounded, and a per-client
      rate limit applies
    And one abusive client cannot starve the stations for the others
