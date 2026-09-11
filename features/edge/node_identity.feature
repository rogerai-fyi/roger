# ROGER EDGE - THE NODE RECORD ("a node does not have to host a model to belong").
#
# WHY THIS EXISTS
#
# Every machine-shaped thing in RogerAI today is a STATION: something that serves a model into
# the market. That is the only device class there is, and it is the wrong shape for a fleet. A
# sensor produces readings and serves nothing. A relay board accepts commands and serves nothing.
# A Mac in the other room might serve a model on Tuesday and be a plain member on Wednesday.
# None of those are Stations, and a layer that can only see Stations cannot show an owner their
# own machines.
#
# So the node record is the account-scoped, capability-typed thing an owner owns. A Station
# becomes ONE capability a node may declare (`serve`), not the definition of a node.
#
# FOUNDER RULING 2026-09-11: "a device that hosts NO model is a first-class member, with declared
# capabilities". That is the whole reason this file exists, and it is why `sense` and `actuate`
# are peers of `serve` rather than afterthoughts.
#
# THE ONE CAPABILITY THAT IS NEVER INFERRED
#
# `actuate` means the node can change the physical world. It is DECLARED and owner-confirmed,
# never derived from a probe, because the cost of guessing wrong is not a bad answer, it is a
# moving machine. Everything else may be verified by observation; `actuate` may only be granted.
#
# GROUND TRUTH: internal/store (where the record lives beside wallets, nodes and grants),
# internal/towercore/cert (the identity a node's key and certificate come from), the existing
# `nodes` table and its `reg` payload (the Station shape this must not disturb).
#
# SCOPE: the record, its identity, its capabilities, its lifecycle. Discovery is
# features/edge/discovery.feature. Enrollment ergonomics are enrollment.feature. What a node can
# be asked to DO is protocol.feature and control.feature.

Feature: A node is the owner's machine, typed by what it can do rather than by whether it serves a model

  # =========================================================================
  # 1. IDENTITY
  # =========================================================================

  Scenario: a node's identity is its own keypair, never issued to it
    When a node joins an Edge
    Then the private key was generated on the node and never transmitted
    And the node id is derived from the public key
    And two nodes can never share an id without sharing a key

  Scenario: the record is scoped to exactly one account
    Given a node enrolled to account "acct-1"
    Then its record carries account "acct-1"
    And no surface of account "acct-2" can list it, describe it, or address it

  Scenario: a name is the owner's, and unique within the account
    Given a node enrolled to account "acct-1"
    When the owner names it "bench-pi"
    Then it is addressable as "bench-pi" within that account
    And a second node in the same account cannot take the name "bench-pi"
    And a node in a DIFFERENT account may hold the same name without conflict

  Scenario: renaming keeps identity, history and grants
    Given a node "bench-pi" with a history and a grant addressed to it
    When the owner renames it to "cabinet-pi"
    Then its id is unchanged
    And its history is unchanged
    And the grant still addresses it

  Scenario Outline: a name must be usable in a command line and a graph
    When the owner names a node "<name>"
    Then the name is <verdict>

    Examples:
      | name                  | verdict  |
      | bench-pi              | accepted |
      | pi5-cabinet-2         | accepted |
      | a                     | accepted |
      | (empty)               | rejected |
      | bench pi              | rejected |
      | ../etc/passwd         | rejected |
      | a-name-of-65-chars... | rejected |
      | UPPER                 | accepted |

  # =========================================================================
  # 2. CAPABILITIES
  # =========================================================================

  Scenario: a node with no capability at all is still a member
    Given a node that declares nothing
    When the fleet is listed
    Then it appears with no capability marks
    And it is a member, because membership is ownership, not usefulness

  Scenario Outline: each capability means one thing and is verified its own way
    Given a node declaring "<capability>"
    When the fleet verifies it
    Then verification is "<how>"

    Examples:
      | capability | how                                        |
      | serve      | the existing serving probe                 |
      | classify   | a canary sample with a known label         |
      | sense      | a read that returns a well-formed sample   |
      | actuate    | declared only, owner-confirmed, never probed |
      | relay      | reachability observed from a second node   |
      | operate    | declared, and gated by grant at use time   |

  Scenario: a declared capability that fails verification is shown as claimed, not granted
    Given a node declaring "serve"
    And its serving probe fails
    Then the capability is shown as CLAIMED and not VERIFIED
    And nothing routes to it on the strength of that claim

  Scenario: actuate is never inferred from behavior
    Given a node that has accepted commands that changed the world
    And it does not declare "actuate"
    Then the fleet does not add "actuate"
    And the next such command is refused

  Scenario: the owner must confirm actuate before it takes effect
    Given a node declaring "actuate" for the first time
    Then it is PENDING CONFIRMATION
    And it accepts no invoke until the owner confirms
    And the confirmation is recorded with who confirmed it and when

  Scenario: capabilities can change without the node changing identity
    Given a verified node with "serve"
    When it stops serving and declares "sense" instead
    Then its id and name are unchanged
    And "serve" is dropped and "sense" is added
    And the change is visible in its history

  Scenario: a Station is a node with the serve capability, and the existing Station path is untouched
    Given an existing `roger share` Station registered the way Stations register today
    Then it appears in the owner's fleet as a node with capability "serve"
    And its market registration, its offers and its receipts are byte-identical to before
    And nothing about the Edge record is required for it to serve

  # =========================================================================
  # 3. KIND, TRANSPORTS, LIFECYCLE
  # =========================================================================

  Scenario Outline: kind describes what the thing is, and bounds what may be asked of it
    Given a node of kind "<kind>"
    Then its encoding is "<encoding>"
    And it may declare <caps>

    Examples:
      | kind   | encoding | caps                          |
      | host   | json     | any capability                |
      | board  | compact  | classify, sense, actuate only |
      | mobile | json     | any except relay              |

  Scenario: transports are ordered by preference, not by discovery order
    Given a node reachable on the LAN and through a relay
    Then its transports read LAN first, relay second
    And the LAN entry carries the pinned certificate fingerprint

  Scenario: forgetting a node removes it and its pin, and nothing else
    Given a verified node with a pin and a history
    When the owner forgets it
    Then it is gone from the fleet
    And its pin is cleared, so it may be adopted again later
    And its receipts and ledger rows are untouched, because money is not fleet state

  Scenario: forgetting a node revokes what was addressed to it
    Given a grant addressed to a node
    When the owner forgets that node
    Then the grant no longer authorizes anything on it
    And a later invoke against that id is refused

  Scenario: a node may belong to exactly one Edge at a time
    Given a node enrolled to account "acct-1"
    When it attempts to enroll to account "acct-2" while still enrolled
    Then the second enrollment is refused
    And the refusal names the account it already belongs to
    And the owner of "acct-2" learns nothing about "acct-1"
