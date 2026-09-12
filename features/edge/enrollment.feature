# ROGER EDGE - SELF-ENROLL ("the machine you are already logged in on joins itself").
#
# STATUS: PROPOSED. Written for the founder's approval. NO production code exists for this and
# none will be written until the spec is signed off (CLAUDE.md step 3).
#
# WHY THIS IS THE GATE
#
# Everything else about Roger Edge is built and green: the node record, LAN discovery with
# certificate pinning, the [3] EDGE screen and the `roger edge` command surface. Run `roger`
# today and the screen is honestly still, because NOTHING ADVERTISES. Advertising truthfully
# needs a node identity and a certificate, and issuing one is enrollment. Until this ships, two
# of the owner's own machines on one desk cannot see each other. This file is that unlock.
#
# SCOPE. Path 1 of the three ergonomics in docs/roger-edge-design.md section 4: a machine that
# is running `roger` where the owner is ALREADY LOGGED IN enrolls itself. Token enroll (for a
# device that cannot hold an account key) and flash enroll (baked into a board image) are
# deliberately NOT here; they are later files and they reuse the certificate path this one
# establishes. Naming them keeps this spec honest about what it does not cover.
#
# ============================================================================
# THE DECISION THIS SPEC NEEDS FROM THE FOUNDER - please read before approving
# ============================================================================
#
# There is no single "account key" to act as a trust root. `internal/client/identity.go`
# LoadOrCreateUserKey() CREATES A DIFFERENT ed25519 KEY ON EVERY MACHINE (<config>/rogerai/
# user.key); machines are bound to the account because Core records each public key against it
# at login. So machine A cannot verify machine B's Edge certificate from anything either of them
# holds locally today. Three ways to give an Edge a trust root:
#
#   (a) A ROOT THAT TRAVELS. Generate one Edge root on the first machine and copy it to the
#       others. Fully offline. Rejected in the draft below: it moves a private root key between
#       machines, and a root that travels is a root that leaks.
#   (b) CORE ISSUES, THE EDGE VERIFIES OFFLINE  <-- what this spec is written against.
#       Enrollment is an ONLINE, once-per-node call: the node generates its key, signs the
#       request with the user key it already holds, and Core - which already knows that key
#       belongs to this account - returns a certificate plus the account's Edge root. From then
#       on discovery, verification and the whole fleet work with NO internet at all. This is
#       exactly how a Tower already enrolls (internal/towercore/{enroll,cert,admit}), so it
#       reuses machinery that exists and is audited, and no private root ever moves.
#   (c) A ROSTER OF PEERS. No root: Core publishes the set of public keys authorized on the
#       account and each node trusts that set. Also offline-capable after a sync, but it invents
#       a second trust model beside the certificate one the codebase already has.
#
# The cost of (b), stated plainly so it is chosen with eyes open: A MACHINE MUST BE ONLINE ONCE,
# at enrollment, before it can be seen on a fully offline LAN. If the founder wants a plant
# network that has NEVER had internet to form an Edge, (b) is the wrong answer and this spec
# must be rewritten around (c). That is the single question to settle before approval.
#
# GROUND TRUTH: internal/client/identity.go (LoadOrCreateUserKey, SignRequestWith - the signing
# the node already does), internal/towercore/cert (Authority, LoadOrCreate, ExportRoot,
# persisted revocation), internal/towercore/enroll + admit (the Tower flow this mirrors),
# internal/edge/verify.go (VerifyPeer already takes a *cert.Authority - this is what fills it),
# internal/edge/fleet.go (Enroll, Forget), cmd/rogerai/edge.go (the CLI surface that gains it).
#
# Step definitions drive a REAL certificate authority, a REAL TLS listener and a REAL mDNS
# responder on loopback. No mocks.

Feature: A machine the owner is logged in on enrolls itself, and from then on the Edge works with no internet

  Background:
    Given an owner logged in on this machine
    And the machine holds the user key that login left on disk

  # =========================================================================
  # 1. THE HAPPY PATH
  # =========================================================================

  Scenario: a logged-in machine enrolls itself and becomes advertisable
    When the owner enrolls this machine as "workshop"
    Then the machine generates a NEW keypair for its node identity
    And the request is signed with the user key it already held
    And it receives a certificate naming this account and this node
    And it receives the account's Edge root
    And it can now advertise, because it has something true to advertise

  Scenario: the node's private key never leaves the machine
    When the owner enrolls this machine
    Then the node's private key was generated locally
    And no request carried it
    And it is stored readable only by its owner

  Scenario: the certificate binds the account and the node, and nothing else
    When the owner enrolls this machine as "workshop"
    Then the certificate names this account
    And it names this node's id
    And it carries no capability, because a capability is declared and verified, never granted by a certificate

  Scenario: enrollment is what makes two of the owner's machines see each other
    Given a second machine of the same account, also enrolled
    When both browse the LAN
    Then each sees the other as a VERIFIED member
    And neither is a candidate, because both certificates check out

  Scenario: the node appears in the fleet on both surfaces
    When the owner enrolls this machine as "workshop"
    Then "roger edge list" shows it
    And the [3] EDGE screen draws it as self

  # =========================================================================
  # 2. IT WORKS OFFLINE AFTERWARDS - the whole point of (b)
  # =========================================================================

  Scenario: once enrolled, discovery and verification need no internet
    Given two enrolled machines of one account
    And there is no route to the internet
    When they browse the LAN
    Then each verifies the other's certificate against the Edge root it already holds
    And both appear as VERIFIED members
    And nothing in the path contacted Core

  Scenario: a certificate keeps working while Core is unreachable
    Given an enrolled machine
    And Core is unreachable
    When the fleet is listed
    Then the machine is still a member
    And its certificate is still honoured until it expires

  Scenario: enrolling REQUIRES connectivity, and says so plainly when it is missing
    Given there is no route to Core
    When the owner tries to enroll this machine
    Then it fails with a message naming connectivity as the reason
    And it says the machine can be enrolled later without losing anything
    And nothing half-enrolled is left behind

  # =========================================================================
  # 3. AUTHORITY - who may enroll what
  # =========================================================================

  Scenario: enrolling requires a login
    Given no owner is logged in
    When enrollment is attempted
    Then it is refused
    And the refusal says to log in first

  Scenario: a user key Core does not know cannot enroll
    Given a user key that was never registered to any account
    When enrollment is attempted
    Then Core refuses it
    And no certificate is issued

  Scenario: a machine cannot enroll into an account that is not its owner's
    When enrollment names another account
    Then it is refused
    And the refusal reveals nothing about that account

  Scenario: a replayed enrollment request cannot mint a second identity
    Given a completed enrollment request
    When the exact same signed request is submitted again
    Then it is refused as already used
    And the node keeps the one identity it has

  Scenario: a request signed by the wrong key is refused
    Given an enrollment request signed by a key that is not the machine's user key
    Then it is refused
    And no certificate is issued

  Scenario: a tampered request is refused
    Given an enrollment request whose node id was altered after signing
    Then the signature check fails
    And it is refused

  # =========================================================================
  # 4. RE-ENROLLMENT, IDENTITY AND THE PIN
  # =========================================================================

  Scenario: enrolling an already-enrolled machine is a clean no-op
    Given this machine is enrolled as "workshop"
    When the owner enrolls it again
    Then it keeps the same node id
    And it keeps the same certificate until that certificate is near expiry
    And nothing in the fleet changed

  Scenario: renewing near expiry keeps the identity
    Given an enrolled machine whose certificate is close to expiry
    When it renews
    Then the node id is unchanged
    And peers that pinned it still verify it
    And its place, name and history in the fleet are unchanged

  Scenario: a machine that was forgotten and enrolls again is a NEW node
    Given a machine that was enrolled and then forgotten by the owner
    When it enrolls again
    Then it receives a new node id
    And it appears as a new member, not as the old one returning
    And the old pin does not verify it, so a peer refuses it as an identity mismatch

  Scenario: a node belongs to exactly one Edge
    Given this machine is enrolled to one account
    When it attempts to enroll to a second account while still enrolled
    Then the second enrollment is refused
    And the refusal names the account it already belongs to only to this machine's own owner
    And the other account learns nothing

  # =========================================================================
  # 5. REVOCATION - the owner can take a machine off the Edge
  # =========================================================================

  Scenario: forgetting a node revokes its certificate
    Given an enrolled machine
    When the owner forgets it
    Then its certificate is revoked
    And a peer that already pinned it refuses it from then on

  Scenario: a revoked certificate is refused even on a LAN with no internet
    Given a revoked node
    And there is no route to Core
    When a peer that has the current revocation list browses
    Then the revoked node is refused
    And the refusal reason is revocation, not expiry

  Scenario: revocation survives a restart
    Given a revoked node
    When the verifying machine restarts
    Then the node is still refused

  Scenario: a stale revocation list is stated, never silently trusted
    Given a machine whose revocation list has not been refreshed for longer than its freshness window
    Then the fleet view marks the trust information as stale, with its age
    And it does not silently claim a revoked node is fine

  # =========================================================================
  # 6. THE CERTIFICATE ITSELF
  # =========================================================================

  Scenario: a certificate has a bounded life
    When a node is enrolled
    Then its certificate carries an expiry
    And a certificate presented after that expiry is refused as expired

  Scenario: an Edge certificate is not a market credential
    When a node is enrolled
    Then the certificate authorizes membership of this Edge only
    And it cannot be used to serve a model, spend from a wallet, or authorize a payout

  Scenario: the Edge root is not the node's key
    Then the account's Edge root and the node's private key are different keys
    And a node never holds the root's private half

  Scenario Outline: every malformed enrollment response is refused rather than half-applied
    Given Core returns <response>
    When the machine enrolls
    Then enrollment fails with "<reason>"
    And the machine holds no partial identity

    Examples:
      | response                                    | reason              |
      | a certificate for a different node id       | identity mismatch   |
      | a certificate for a different account       | wrong account       |
      | a certificate already expired               | certificate expired |
      | a certificate signed by an unexpected root  | unknown authority   |
      | a truncated body                            | malformed response  |

  # =========================================================================
  # 7. IT NEVER GETS IN THE WAY
  # =========================================================================

  Scenario: enrollment is never automatic
    When the owner runs roger without asking to enroll
    Then nothing enrolls itself
    And no key is generated
    And the Edge screen is honestly empty rather than quietly joined

  Scenario: a failed enrollment leaves the machine exactly as it was
    Given enrollment fails at any step
    Then no certificate, no node key and no fleet row remain
    And retrying later behaves as a first attempt

  Scenario: enrolling does not disturb a Station already serving the market
    Given this machine is already serving a model as a Station
    When the owner enrolls it
    Then its market registration, its offers and its receipts are unchanged
    And it appears in the fleet as a node with the serve capability
