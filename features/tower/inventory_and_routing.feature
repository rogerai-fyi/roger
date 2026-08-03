# PROPOSED SPEC — founder approval is required before step definitions or implementation.
#
# Scope: version negotiation, Tower/Station inventory, central directory ownership,
# exact leaf selection, dispatch origin, and isolation from the trusted replica bus.

Feature: Roger Core routes transparently to Stations behind a joined Tower
  A joined Tower may aggregate and transport Stations, but Roger Core verifies and selects
  each leaf offer and remains authoritative for eligibility, price, policy, and load limits.

  Background:
    Given an authenticated joined Tower with a current quarantine or active lease
    And a Station attached to that Tower with its own signing key

  # --- protocol negotiation ------------------------------------------------

  Scenario: A joined session negotiates one mutually supported protocol
    Given the Tower offers protocol versions N and N-1
    And Roger Core supports protocol versions N and N-1
    When the joined session starts
    Then both peers bind the session to version N and its mandatory capabilities
    And every later message carries that network, version, Tower, and session identity

  Scenario Outline: Negotiation fails before inventory
    Given the joined handshake has "<condition>"
    When protocol negotiation runs
    Then no inventory, lease, payload, result, or settlement message is accepted

    Examples:
      | condition                                      |
      | no common protocol version                     |
      | a version below the signed minimum             |
      | a missing mandatory integrity capability       |
      | a missing inner secure-session capability      |
      | contradictory capability values                |
      | an unknown mandatory capability                |
      | a network ID other than the public network     |
      | a Tower ID different from the certificate      |
      | a replayed session ID                          |

  # --- signed and revisioned inventory ------------------------------------

  Scenario: A valid full inventory is admitted as candidate evidence
    Given the Tower submits the exact next signed full inventory revision with its immediate prior hash, current admission-lease/lifecycle heads, and expiry
    And every Station offer has a valid Station signature
    When Roger Core validates the inventory
    Then it records each leaf with its origin Tower and exact signed offer
    And applies central ownership, ban, policy, price, liveness, version, and capacity checks
    And only eligible leaves become routing candidates

  Scenario Outline: Invalid Tower inventory is rejected atomically
    Given a submitted inventory has "<defect>"
    When Roger Core validates it
    Then none of that inventory revision becomes routable
    And the last fully accepted revision remains authoritative until its expiry

    Examples:
      | defect                                           |
      | no Tower signature                               |
      | a Tower signature by another Tower               |
      | a Tower ID different from the channel identity   |
      | an invalid canonical encoding                    |
      | an unknown required field                        |
      | a missing required field                         |
      | a duplicate Station ID                           |
      | a duplicate offer ID                             |
      | a revision equal to the accepted revision        |
      | a revision lower than the accepted revision      |
      | a revision that skips the accepted revision plus one |
      | a previous hash other than the accepted current head |
      | a revision that overflows the sequence           |
      | an issued time in the future beyond skew         |
      | an expired inventory                             |
      | an expiry beyond the allowed lease               |
      | a public network ID mismatch                     |
      | a capability count above the Tower's limit       |
      | total encoded bytes above the inventory limit    |

  Scenario Outline: An invalid leaf is excluded without granting its claim truth
    Given an otherwise valid inventory contains a leaf with "<defect>"
    When Roger Core applies leaf admission policy
    Then that leaf is not routable
    And no claimed metadata is labeled verified merely because the Tower signed it

    Examples:
      | defect                                             |
      | no Station signature                               |
      | a Station signature by another key                 |
      | Station ID inconsistent with its registered key    |
      | owner missing for public admission                 |
      | suspended owner                                    |
      | banned Station                                     |
      | key revoked                                        |
      | unsupported model                                  |
      | price below the public floor                       |
      | price above the public ceiling                     |
      | Station-earning rate above the matching consumer rate |
      | non-finite or negative price                       |
      | zero or negative capacity                          |
      | unsupported modality                               |
      | expired offer                                      |
      | capabilities omitted from the signed leaf bytes    |
      | offer bound to a different Tower                   |

  Scenario: A valid delta applies only to its declared base revision
    Given Roger Core accepted full revision 40
    When a signed delta from revision 40 to 41 adds, changes, and removes leaves
    Then the result is exactly revision 41
    And unchanged leaves retain their prior signed offer and origin

  Scenario: One Station identity has one active origin in v1
    Given the same Station key is advertised directly and through a Tower or through two Towers
    When Roger Core validates the competing origins
    Then it does not multiply the Station's capacity or dispatch concurrently through both
    And joined-to-joined movement alone may use the signed fenced StationRehomeLeaseV1 flow
    And direct-to-joined or joined-to-direct movement is forbidden in v1 because origin kind is immutable for one Station ID; it requires terminal revocation of the old Station identity and fresh attachment under a new Station ID

  Scenario: Rehoming a Station fences its prior origin
    Given a Station is active behind Tower A
    When its owner and Roger Core approve a move to Tower B with a newer Station origin epoch
    Then Tower A stops receiving new grants for that Station before Tower B becomes active
    And old-origin sessions and inventory cannot complete new-epoch attempts

  Scenario Outline: An ambiguous inventory delta causes resynchronization
    Given Roger Core accepted revision 40
    When it receives a delta with "<condition>"
    Then the delta is not partially applied
    And Roger Core requests a full snapshot

    Examples:
      | condition                         |
      | base revision 39                  |
      | base revision 41                  |
      | target revision 40                |
      | target revision 42                |
      | removal of an unknown leaf        |
      | duplicate operation on one leaf   |
      | signature failure                 |
      | truncated body                    |

  Scenario: Inventory expiry removes every stale leaf from new routing
    Given a Tower inventory expires without a newer accepted revision
    When routing takes its next eligibility snapshot
    Then no leaf from the expired inventory receives a new job
    And active attempts retain only their already-issued deadlines

  Scenario: Disconnect cannot leave immortal inventory
    Given an active Tower disconnects without a drain message
    When its connection lease or inventory freshness window expires
    Then every leaf behind it is removed from new routing
    And no heartbeat fabricated by another Tower refreshes it

  # --- exact central routing ------------------------------------------------

  Scenario: Roger Core selects an exact leaf behind a Tower
    Given two eligible Stations behind one Tower offer the requested model at different scores
    When Roger Core routes a request
    Then its normal central policy chooses one exact Station
    And the encrypted signed execution grant names that Tower, Station, model, offer, quote, and limits
    And the Tower-visible signed dispatch lease binds that Station and the opaque grant envelope
    And the Tower cannot substitute the other Station

  Scenario: Direct and Tower-backed Stations share one policy but separate dispatchers
    Given an eligible direct Station and an eligible joined-Tower Station offer one model
    When Roger Core scores and selects them
    Then both use the same central eligibility and scoring contract
    And the selected candidate is dispatched only through its recorded origin
    And no local bridge token or transport handle crosses origins

  Scenario Outline: A Tower cannot override central routing authority
    Given Roger Core signed a grant for Station A
    When the Tower attempts "<action>"
    Then Station B cannot produce a settleable result for that grant
    And central price, limits, hold, and selected Station remain unchanged

    Examples:
      | action                                      |
      | substitute Station B                       |
      | substitute a different model               |
      | substitute a different offer               |
      | increase the consumer input or output rate  |
      | increase the Station-earning input or output rate |
      | increase maximum tokens                     |
      | increase maximum cost                       |
      | route a grant issued to another Tower       |
      | reuse a grant from another network          |

  Scenario: Tower-local Station credentials never leave the Tower
    Given a Station uses a local bridge credential to poll its Tower
    When inventory and jobs flow between the Tower and Roger Core
    Then the local credential is absent from every Roger Core message, store, log, and receipt
    And Roger Core authenticates the leaf through signed Station evidence and the Tower origin

  Scenario: Joined Towers never connect to the trusted replica store
    Given Roger Core uses a private database and cross-instance message bus
    When a joined Tower runs, reconnects, routes, and reports results
    Then it receives no database or bus credential
    And it cannot read, publish, or subscribe through the trusted replica protocol
    And its scoped link exposes only joined-Tower message types

  # --- central policy and public visibility --------------------------------

  Scenario Outline: Central state overrides a valid signed offer
    Given a cryptographically valid leaf offer
    And Roger Core marks its "<subject>" as "<state>"
    When routing evaluates the offer
    Then the offer is ineligible for new public work

    Examples:
      | subject  | state                 |
      | Tower    | suspended             |
      | Tower    | revoked               |
      | Tower    | draining              |
      | Tower    | over concurrency cap  |
      | Station  | banned                |
      | Station  | stale                 |
      | owner    | suspended             |
      | offer    | expired               |
      | version  | below minimum         |

  Scenario: Public discovery reports the Station and its joined origin honestly
    Given an active Tower leaf is eligible for public discovery
    When a client obtains discovery data
    Then Roger Core is the signer and source of the listing
    And the Station identity, Tower path, model, authoritative price, and verification tier are distinguishable
    And operator-declared geography or hardware is not labeled measured

  Scenario: A Tower cannot advertise itself directly to public clients
    Given a Tower serves a valid locally signed directory document
    When it is absent or inactive in Roger Core's directory
    Then RogerAI clients do not treat it as public-network membership

  # --- health, capacity, and failover --------------------------------------

  Scenario: Tower and Station capacity are both enforced
    Given a Tower has room for one new stream but its selected Station has no room
    When Roger Core routes a request
    Then that Station receives no grant
    And another eligible candidate may be selected without charging the failed attempt

  Scenario: A failed joined attempt can fail over without grant reuse
    Given a joined Station drops an attempt before usable output and settlement
    When Roger Core retries on another eligible Station
    Then the first attempt is failed and its hold allocation is not captured
    And the retry has a new attempt ID, nonce, Station binding, deadline, and grant signature
    And a late result from the first attempt cannot settle

  Scenario: A malicious Tower can be unavailable but cannot manufacture success
    Given a Tower drops, delays, duplicates, or reorders transport messages
    When Roger Core reaches the signed attempt deadline
    Then the attempt cannot settle without complete valid Station evidence and exact context
    And failure and health evidence are recorded
    And the consumer is not charged for absent usable output

  Scenario: Public admission alone earns no network share
    Given a request successfully runs through a joined Tower and paid Station
    But the Tower lacks the separately authorized compensated capability
    When Roger Core settles it
    Then the Station/client settlement records no Tower earning
    And the compensation-candidate reason is "not enrolled in revenue share"

  Scenario: A compensated Tower receives only a later policy-governed candidate
    Given a request successfully runs through a joined Tower with the compensated capability
    When Roger Core settles exact Core-observed and corroborated transit evidence
    Then the job receipt records an immutable eligible compensation candidate with no future money state
    And no payable Tower earning exists until the separate funds, reversal, operator-state, and compensation-ledger checks pass
